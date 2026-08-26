package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

func historyMessage(role responses.EasyInputMessageRole, text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, role)
}

func TestFindHistoryCutKeepsRecentCompleteTurns(t *testing.T) {
	history := []responses.ResponseInputItemUnionParam{
		historyMessage(responses.EasyInputMessageRoleUser, "first request"),
		historyMessage(responses.EasyInputMessageRoleAssistant, strings.Repeat("a", 400)),
		historyMessage(responses.EasyInputMessageRoleUser, "second request"),
		historyMessage(responses.EasyInputMessageRoleAssistant, strings.Repeat("b", 400)),
		historyMessage(responses.EasyInputMessageRoleUser, "third request"),
		historyMessage(responses.EasyInputMessageRoleAssistant, strings.Repeat("c", 400)),
	}
	latestTurnTokens := estimateHistoryItemTokens(history[4]) + estimateHistoryItemTokens(history[5])
	if cut := findHistoryCut(history, latestTurnTokens+1); cut != 2 {
		t.Fatalf("cut = %d, want second user message at 2", cut)
	}
}

func TestFindHistoryCutDoesNotStartAtToolOutput(t *testing.T) {
	history := []responses.ResponseInputItemUnionParam{
		historyMessage(responses.EasyInputMessageRoleUser, "request"),
		responses.ResponseInputItemParamOfFunctionCall(`{"code":"work()"}`, "call_1", "repl"),
		responses.ResponseInputItemParamOfFunctionCallOutput("call_1", strings.Repeat("result", 200)),
		historyMessage(responses.EasyInputMessageRoleAssistant, "finished"),
	}
	cut := findHistoryCut(history, estimateHistoryItemTokens(history[3])+1)
	if cut == 2 || cut == 0 {
		t.Fatalf("unsafe split-turn cut = %d", cut)
	}
}

func TestSerializeHistoryTruncatesToolResults(t *testing.T) {
	history := []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfFunctionCallOutput("call_1", strings.Repeat("x", summaryToolOutputLimit+100)),
	}
	serialized := serializeHistory(history)
	if !strings.Contains(serialized, "100 more characters truncated") || strings.Count(serialized, "x") >= summaryToolOutputLimit+100 {
		t.Fatalf("serialized tool result was not truncated: length=%d", len(serialized))
	}
}

func TestCompactHistoryReplacesOldContextWithSummary(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_summary\",\"object\":\"response\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_summary\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"## Goal\\nContinue the test\",\"annotations\":[]}]}]}}\n\n")
	}))
	defer server.Close()

	a := &Agent{
		client:    openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName: "test",
		history: []responses.ResponseInputItemUnionParam{
			historyMessage(responses.EasyInputMessageRoleUser, "old request"),
			historyMessage(responses.EasyInputMessageRoleAssistant, strings.Repeat("old result", 100)),
			historyMessage(responses.EasyInputMessageRoleUser, "middle request"),
			historyMessage(responses.EasyInputMessageRoleAssistant, strings.Repeat("middle result", 100)),
			historyMessage(responses.EasyInputMessageRoleUser, "recent request"),
			historyMessage(responses.EasyInputMessageRoleAssistant, "recent result"),
		},
	}
	latestTokens := estimateHistoryItemTokens(a.history[4]) + estimateHistoryItemTokens(a.history[5])
	if err := a.compactHistory(t.Context(), latestTokens+1); err != nil {
		t.Fatal(err)
	}
	if a.summary != "## Goal\nContinue the test" {
		t.Fatalf("summary = %q", a.summary)
	}
	if len(a.history) != 4 || !isUserHistoryItem(a.history[0]) {
		t.Fatalf("retained history = %#v", a.history)
	}
	body, _ := json.Marshal(request)
	if !strings.Contains(string(body), "old request") || strings.Contains(string(body), "recent request") {
		t.Fatalf("summarization request used the wrong history: %s", body)
	}
	if len(a.input()) != 5 {
		t.Fatalf("compacted input has %d items, want summary plus four recent items", len(a.input()))
	}
}

func TestRespondCompactsAndRetriesContextOverflow(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Maximum context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		text, id := "## Goal\nRecovered context", "msg_summary"
		if requests == 3 {
			text, id = "recovered", "msg_answer"
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "response.completed", "sequence_number": 1,
			"response": map[string]any{
				"id": "resp_test", "object": "response", "model": "test", "status": "completed",
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12, "input_tokens_details": map[string]any{}, "output_tokens_details": map[string]any{}},
				"output": []any{map[string]any{
					"id": id, "type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
				}},
			},
		})
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
	}))
	defer server.Close()

	a := &Agent{
		client:       openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:    "test",
		instructions: "test",
		maxRetries:   0,
		history: []responses.ResponseInputItemUnionParam{
			historyMessage(responses.EasyInputMessageRoleUser, "old request"),
			historyMessage(responses.EasyInputMessageRoleAssistant, "old result"),
		},
	}
	var events []ToolEvent
	response := a.Respond(strings.Repeat("new request ", 10000), nil, func(event ToolEvent) { events = append(events, event) }, t.Context())
	if response.Err != nil || response.Text != "recovered" {
		t.Fatalf("response = %#v", response)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want failed request, summary, and retry", requests)
	}
	var started, done bool
	for _, event := range events {
		started = started || event.Kind == ToolEventCompactionStart
		done = done || event.Kind == ToolEventCompactionDone
	}
	if !started || !done || a.summary == "" {
		t.Fatalf("compaction state: started=%v done=%v summary=%q events=%#v", started, done, a.summary, events)
	}
}
