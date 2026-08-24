package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestBuildSystemPrompt(t *testing.T) {
	prompt := buildSystemPrompt("/work/project")
	for _, want := range []string{
		"expert general-purpose assistant operating inside OSH",
		"Available tools:",
		"npx -y mcporter@0.13.7 list",
		"npx -y mcporter@0.13.7 call <server>.<tool>",
		"Do not run destructive or difficult-to-reverse commands",
		"Current working directory: /work/project",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt does not contain %q", want)
		}
	}
}

func TestPrefixUserMessageIncludesCurrentDateAndTime(t *testing.T) {
	now := time.Date(2026, time.August, 23, 19, 42, 17, 0, time.FixedZone("PDT", -7*60*60))
	got := prefixUserMessage("hello", now)
	want := "[2026-08-23T19:42:17-07:00]\n\nhello"
	if got != want {
		t.Fatalf("prefixed message = %q, want %q", got, want)
	}
}

func TestRunShellStreamingEmitsOutputBeforeCompletion(t *testing.T) {
	var chunks []string
	out, err := runShellStreaming(t.Context(), "printf first; sleep 0.05; printf second >&2", func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "firstsecond" || strings.Join(chunks, "") != out {
		t.Fatalf("output = %q, chunks = %#v", out, chunks)
	}
	if len(chunks) < 2 {
		t.Fatalf("output was not emitted while command was running: %#v", chunks)
	}
}

func TestRunShellReturnsCombinedOutputAndError(t *testing.T) {
	out, err := runShell(t.Context(), "printf stdout; printf stderr >&2; exit 7")
	if err == nil {
		t.Fatal("expected shell error")
	}
	if out != "stdoutstderr" {
		t.Fatalf("combined output = %q", out)
	}
}

func TestConsumeSteeringDeliversOneMessageAtATime(t *testing.T) {
	a := &Agent{}
	steer := make(chan string, 2)
	steer <- "first"
	steer <- "second"
	var events []ToolEvent
	emit := func(event ToolEvent) { events = append(events, event) }

	if !a.consumeSteering(steer, emit) {
		t.Fatal("first steering message was not consumed")
	}
	if len(a.history) != 1 || len(events) != 1 || events[0].Detail != "first" {
		t.Fatalf("first delivery: history=%d events=%#v", len(a.history), events)
	}
	if !a.consumeSteering(steer, emit) {
		t.Fatal("second steering message was not consumed")
	}
	if len(a.history) != 2 || len(events) != 2 || events[1].Detail != "second" {
		t.Fatalf("second delivery: history=%d events=%#v", len(a.history), events)
	}
	if a.consumeSteering(steer, emit) {
		t.Fatal("empty steering queue reported a message")
	}
}

func TestLimitToolOutputKeepsTailAndSavesFullOutput(t *testing.T) {
	var full strings.Builder
	for i := 1; i <= maxToolOutputLines+10; i++ {
		fmt.Fprintf(&full, "line-%04d\n", i)
	}
	limited := limitToolOutput(full.String())
	if strings.Contains(limited, "line-0001") || !strings.Contains(limited, "line-2010") {
		t.Fatalf("limited output did not retain the tail: prefix=%q suffix=%q", limited[:min(100, len(limited))], limited[max(0, len(limited)-100):])
	}
	if !strings.Contains(limited, "Tool output truncated:") || !strings.Contains(limited, "Full output:") {
		t.Fatalf("limited output lacks truncation details: %q", limited[max(0, len(limited)-300):])
	}
	path := strings.TrimSuffix(strings.Split(limited, "Full output: ")[1], "]")
	t.Cleanup(func() { _ = os.Remove(path) })
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != full.String() {
		t.Fatalf("saved output length = %d, want %d", len(saved), full.Len())
	}
}

func TestLimitToolOutputRespectsByteLimitAndUTF8(t *testing.T) {
	full := strings.Repeat("é", maxToolOutputBytes)
	limited := limitToolOutput(full)
	if !utf8.ValidString(limited) {
		t.Fatal("byte-limited output is not valid UTF-8")
	}
	if !strings.Contains(limited, "50KB limit") {
		t.Fatalf("byte limit was not reported: %q", limited[max(0, len(limited)-200):])
	}
	if parts := strings.Split(limited, "Full output: "); len(parts) == 2 {
		path := strings.TrimSuffix(parts[1], "]")
		t.Cleanup(func() { _ = os.Remove(path) })
	}
}

func TestDefaultLLMRetryBudget(t *testing.T) {
	if got := New().maxRetries; got != 10 {
		t.Fatalf("max retries = %d, want 10", got)
	}
}

func TestIsRetryableLLMError(t *testing.T) {
	for _, status := range []int{408, 409, 429, 500, 503} {
		if !isRetryableLLMError(&openai.Error{StatusCode: status}) {
			t.Errorf("status %d was not retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404} {
		if isRetryableLLMError(&openai.Error{StatusCode: status}) {
			t.Errorf("status %d was retryable", status)
		}
	}
	missingToolOutput := &openai.Error{
		StatusCode: http.StatusBadRequest,
		Message:    "No tool output found for function call call_test.",
	}
	if !isRetryableLLMError(missingToolOutput) {
		t.Fatal("missing tool output error was not retryable")
	}
	if isRetryableLLMError(&openai.Error{StatusCode: 429, Code: "insufficient_quota"}) {
		t.Fatal("quota exhaustion was retryable")
	}
	if !isRetryableLLMError(fmt.Errorf("dial tcp: network is unreachable")) {
		t.Fatal("transport error was not retryable")
	}
	if isRetryableLLMError(context.Canceled) {
		t.Fatal("cancellation was retryable")
	}
}

func TestRespondRetriesTransientFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests <= 2 {
			http.Error(w, `{"error":{"message":"temporarily unavailable","type":"server_error","code":"server_error"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"item_id\":\"msg_test\",\"output_index\":0,\"content_index\":0,\"sequence_number\":1}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}]}]}}\n\n")
	}))
	defer server.Close()

	a := &Agent{
		client:         openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:      "test",
		instructions:   "test",
		maxRetries:     3,
		retryBaseDelay: time.Millisecond,
	}
	var events []ToolEvent
	resp := a.Respond("hello", make(chan string), func(ev ToolEvent) { events = append(events, ev) }, t.Context())
	if resp.Err != nil || resp.Text != "hello" {
		t.Fatalf("response = %#v", resp)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	var retries []ToolEvent
	for _, ev := range events {
		if ev.Phase == "retry" {
			retries = append(retries, ev)
		}
	}
	if len(retries) != 2 || retries[0].Attempt != 1 || retries[0].Delay != time.Millisecond || retries[1].Attempt != 2 || retries[1].Delay != 2*time.Millisecond {
		t.Fatalf("retry events = %#v", retries)
	}
}

func TestWaitForRetryCanBeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if waitForRetry(ctx, time.Hour) {
		t.Fatal("cancelled wait completed")
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("cancelled retry wait did not return promptly")
	}
}
