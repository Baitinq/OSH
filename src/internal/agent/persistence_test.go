package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestRespondSavesEachModelTurn(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	id := "test"
	sessionPath := filepath.Join(root, id, "session.json")
	secondRequest := make(chan struct{})
	releaseSecond := make(chan struct{})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var output []any
		if requests == 1 {
			data, err := os.ReadFile(sessionPath)
			if err != nil {
				t.Errorf("read session before first model call: %v", err)
			}
			var saved sessionFile
			if err := json.Unmarshal(data, &saved); err != nil || len(saved.History) != 1 || saved.History[0].Role != "user" {
				t.Errorf("session before first model call = %#v, %v", saved.History, err)
			}
			output = []any{map[string]any{
				"id": "fc_test", "type": "function_call", "call_id": "call_test",
				"name": "repl", "arguments": `{"code":"value = 42"}`, "status": "completed",
			}}
		} else {
			close(secondRequest)
			<-releaseSecond
			output = []any{map[string]any{
				"id": "msg_test", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "done", "annotations": []any{}}},
			}}
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "response.completed", "sequence_number": 1,
			"response": map[string]any{
				"id": "resp_test", "object": "response", "model": "test", "status": "completed", "output": output,
			},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
	}))
	defer server.Close()

	a := &Agent{
		client:       openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:    "test",
		provider:     "openai",
		instructions: "test",
		cwd:          cwd,
		maxRetries:   0,
	}
	defer a.Close()
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	response := make(chan Response)
	go func() { response <- a.Respond("run it", nil, func(ToolEvent) {}, t.Context()) }()
	<-secondRequest

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved sessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.History) != 3 || saved.History[1].Type != "tool_call" || saved.History[2].Type != "tool_result" {
		t.Fatalf("history saved between model turns = %#v", saved.History)
	}

	close(releaseSecond)
	if result := <-response; result.Err != nil || result.Text != "done" {
		t.Fatalf("Respond() = %#v", result)
	}
	data, err = os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.History) != 4 || saved.History[3].Text != "done" {
		t.Fatalf("history saved after final model turn = %#v", saved.History)
	}
}

func TestRespondCancellationPreservesCompletedWork(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	secondRequest := make(chan struct{})
	releaseSecond := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			close(secondRequest)
			<-releaseSecond
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"id\":\"fc_test\",\"type\":\"function_call\",\"call_id\":\"call_test\",\"name\":\"repl\",\"arguments\":\"{\\\"code\\\":\\\"value = 42\\\"}\",\"status\":\"completed\"}]}}\n\n")
	}))
	defer server.Close()

	a := &Agent{
		client:       openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:    "test",
		provider:     "openai",
		instructions: "test",
		cwd:          cwd,
		maxRetries:   0,
	}
	defer a.Close()
	if err := a.StartSession("test", root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	response := make(chan Response)
	go func() { response <- a.Respond("run it", nil, func(ToolEvent) {}, ctx) }()
	<-secondRequest
	cancel()
	result := <-response
	close(releaseSecond)
	if result.Err != nil {
		t.Fatalf("Respond() = %#v", result)
	}

	data, err := os.ReadFile(filepath.Join(root, "test", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved sessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.History) != 3 || saved.History[1].Type != "tool_call" || saved.History[2].Type != "tool_result" {
		t.Fatalf("history after cancellation = %#v", saved.History)
	}
}

func TestResumeRepairsInterruptedToolCalls(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	a := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := a.StartSession("test", root); err != nil {
		t.Fatal(err)
	}
	a.history = []historyItem{
		{Type: "message", Role: "user", Text: "run both"},
		{Type: "tool_call", CallID: "call_done", Name: "repl"},
		{Type: "tool_call", CallID: "call_interrupted", Name: "repl"},
		{Type: "tool_result", CallID: "call_done", Name: "repl", Text: "done"},
	}
	if err := a.SaveSession(); err != nil {
		t.Fatal(err)
	}

	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := loaded.ResumeSession("test", root); err != nil {
		t.Fatal(err)
	}
	if len(loaded.history) != 5 {
		t.Fatalf("repaired history = %#v", loaded.history)
	}
	result := loaded.history[4]
	if result.Type != "tool_result" || result.CallID != "call_interrupted" || !result.ToolError {
		t.Fatalf("interrupted result = %#v", result)
	}
}
