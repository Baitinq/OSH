package agent

import (
	"context"
	"encoding/json"
	"errors"
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

func TestNewUsesEnvironmentOverrides(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/v1/responses" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"override-model\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("FN_BASE_URL", server.URL+"/custom/v1/")
	t.Setenv("FN_MODEL", "override-model")
	t.Setenv("FN_REASONING_EFFORT", "high")

	a := New()
	if a.ModelName() != "override-model" || a.ReasoningEffort() != "high" {
		t.Fatalf("configuration = model %q, reasoning %q", a.ModelName(), a.ReasoningEffort())
	}
	if resp := a.Respond("hello", nil, func(ToolEvent) {}, t.Context()); resp.Err != nil {
		t.Fatal(resp.Err)
	}
	if request["model"] != "override-model" {
		t.Fatalf("request model = %v", request["model"])
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("request tools = %#v", request["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "repl" {
		t.Fatalf("request tool = %#v", tools[0])
	}
	reasoning, ok := request["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("request reasoning = %#v", request["reasoning"])
	}
}

func TestCallLLMUsesFreshToolFreeRequest(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_llm\",\"object\":\"response\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_llm\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"classified\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":40,\"input_tokens_details\":{\"cached_tokens\":10},\"output_tokens\":2,\"output_tokens_details\":{\"reasoning_tokens\":1},\"total_tokens\":42}}}\n\n")
	}))
	defer server.Close()

	a := &Agent{
		client:          openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:       "test-model",
		reasoningEffort: "low",
	}
	text, err := a.callLLM(t.Context(), "classify this")
	if err != nil || text != "classified" {
		t.Fatalf("callLLM() = %q, %v", text, err)
	}
	if a.TokensUsed() != 42 {
		t.Fatalf("TokensUsed() = %d, want 42", a.TokensUsed())
	}
	usage := a.Usage()
	if len(usage) != 1 || usage[0].InputTokens != 40 || usage[0].CachedInputTokens != 10 || usage[0].OutputTokens != 2 || usage[0].ReasoningOutputTokens != 1 || usage[0].TotalTokens != 42 {
		t.Fatalf("Usage() = %#v", usage)
	}
	if _, ok := request["tools"]; ok {
		t.Fatalf("llm request included tools: %#v", request["tools"])
	}
	input := request["input"].([]any)
	message := input[0].(map[string]any)
	if message["content"] != "classify this" {
		t.Fatalf("llm input = %#v", request["input"])
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	prompt := buildSystemPrompt("/work/project")
	for _, want := range []string{
		"expert general-purpose assistant operating inside fn agent",
		"Available tool:",
		"repl: Execute Python in a persistent REPL",
		"shell(command, timeout=None) -> ShellResult",
		"web_search(query, max_results=8) -> list[SearchResult]",
		"llm(prompt) -> str",
		"Only printed output and the final expression enter model context",
		"Old REPL outputs are replaced with [output omitted] after each turn; Python state persists",
		"npx -y mcporter@latest list",
		"npx -y mcporter@latest call <server>.<tool>",
		"https://github.com/Baitinq/fn-agent",
		"~/.fn/sessions/<UUID>/session.json",
		"fn --session <UUID>",
		"Prioritize fast, verifiable iteration",
		"exercise changes end to end in the local environment",
		"Use the fastest relevant feedback loop while developing",
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

func TestPruneTransientHistoryKeepsConversationAndREPLCalls(t *testing.T) {
	a := &Agent{history: []historyItem{
		{Type: "message", Role: "user", Text: "first"},
		{Type: "message", Role: "assistant", Text: "prior"},
		{Type: "message", Role: "user", Text: "second"},
		{Type: "reasoning", Text: "thinking"},
		{Type: "message", Role: "assistant", Text: "intermediate", transient: true},
		{Type: "tool_call", CallID: "call_1", Name: "repl"},
		{Type: "tool_result", CallID: "call_1", Text: "result"},
		{Type: "message", Role: "assistant", Text: "final"},
	}}
	a.pruneTransientHistory()
	if len(a.history) != 6 {
		t.Fatalf("retained history = %#v", a.history)
	}
	if a.history[0].Text != "first" || a.history[1].Text != "prior" || a.history[3].Type != "tool_call" || a.history[4].Text != omittedREPLResult || a.history[5].Text != "final" {
		t.Fatalf("retained history = %#v", a.history)
	}
}

func TestLimitToolOutputKeepsTail(t *testing.T) {
	var full strings.Builder
	for i := 1; i <= maxToolOutputLines+10; i++ {
		fmt.Fprintf(&full, "line-%04d\n", i)
	}
	limited := limitToolOutput(full.String())
	if strings.Contains(limited, "line-0001") || !strings.Contains(limited, "line-2010") {
		t.Fatalf("limited output did not retain the tail: prefix=%q suffix=%q", limited[:min(100, len(limited))], limited[max(0, len(limited)-100):])
	}
	if !strings.Contains(limited, "Tool output truncated:") || !strings.Contains(limited, "Assign large results to a Python variable") {
		t.Fatalf("limited output lacks truncation guidance: %q", limited[max(0, len(limited)-300):])
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
	if isRetryableLLMError(missingToolOutput) {
		t.Fatal("missing tool output error was retryable")
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

func TestIsContextOverflowError(t *testing.T) {
	for _, err := range []error{
		&openai.Error{StatusCode: http.StatusBadRequest, Code: "context_length_exceeded"},
		&responseFailure{message: "Maximum context length exceeded"},
		fmt.Errorf("input too long for model"),
	} {
		if !isContextOverflowError(err) {
			t.Errorf("did not recognize context overflow: %v", err)
		}
	}
	if isContextOverflowError(&openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid argument"}) {
		t.Fatal("ordinary bad request was recognized as context overflow")
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
		retryJitter:    func() float64 { return 1 },
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
		if ev.Kind == ToolEventRetry {
			retries = append(retries, ev)
		}
	}
	if len(retries) != 2 || retries[0].Attempt != 1 || retries[0].Delay != time.Millisecond || retries[1].Attempt != 2 || retries[1].Delay != 2*time.Millisecond {
		t.Fatalf("retry events = %#v", retries)
	}
}

func TestRespondCancellationDoesNotLeaveFunctionCallWithoutOutput(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)

		var output []any
		if len(requests) == 1 {
			output = []any{map[string]any{
				"id": "fc_test", "type": "function_call", "call_id": "call_test",
				"name": "repl", "arguments": `{"code":"print('test')"}`, "status": "completed",
			}}
		} else {
			output = []any{map[string]any{
				"id": "msg_test", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "recovered", "annotations": []any{}}},
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
		instructions: "test",
		maxRetries:   0,
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.Respond("cancelled question", nil, func(event ToolEvent) {
		if event.Kind == ToolEventContextTokens {
			cancel()
		}
	}, ctx)

	response := a.Respond("next question", nil, func(ToolEvent) {}, t.Context())
	if response.Err != nil || response.Text != "recovered" {
		t.Fatalf("response = %#v", response)
	}
	input, _ := json.Marshal(requests[1]["input"])
	if strings.Contains(string(input), "function_call") {
		t.Fatalf("cancelled function call remained in history: %s", input)
	}
}

func TestRespondDropsCompletedToolHistoryFromLaterTurns(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)

		var output []any
		switch len(requests) {
		case 1:
			output = []any{map[string]any{
				"id": "fc_test", "type": "function_call", "call_id": "call_test",
				"name": "repl", "arguments": `{"code":"saved = ''.join(map(chr, [83, 69, 67, 82, 69, 84])); print(saved)"}`, "status": "completed",
			}}
		case 2:
			output = []any{map[string]any{
				"id": "msg_first", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "first answer", "annotations": []any{}}},
			}}
		case 3:
			output = []any{map[string]any{
				"id": "msg_second", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "second answer", "annotations": []any{}}},
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
		instructions: "test",
		maxRetries:   0,
	}
	defer a.Close()
	if response := a.Respond("first question", nil, func(ToolEvent) {}, t.Context()); response.Err != nil {
		t.Fatal(response.Err)
	}
	if response := a.Respond("second question", nil, func(ToolEvent) {}, t.Context()); response.Err != nil {
		t.Fatal(response.Err)
	}

	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	activeTurn, _ := json.Marshal(requests[1]["input"])
	if !strings.Contains(string(activeTurn), "function_call") || !strings.Contains(string(activeTurn), "SECRET") {
		t.Fatalf("active turn did not include complete tool history: %s", activeTurn)
	}
	laterTurn, _ := json.Marshal(requests[2]["input"])
	if !strings.Contains(string(laterTurn), "function_call") || !strings.Contains(string(laterTurn), "saved =") {
		t.Fatalf("later turn omitted the REPL code cell: %s", laterTurn)
	}
	if strings.Contains(string(laterTurn), "SECRET") || !strings.Contains(string(laterTurn), omittedREPLResult) {
		t.Fatalf("later turn did not replace the REPL result: %s", laterTurn)
	}
	for _, text := range []string{"first question", "first answer", "second question"} {
		if !strings.Contains(string(laterTurn), text) {
			t.Fatalf("later turn omitted %q: %s", text, laterTurn)
		}
	}
}

func TestRetryDelayUsesEqualJitterAndCapsBackoff(t *testing.T) {
	a := &Agent{
		retryBaseDelay: 2 * time.Second,
		retryJitter:    func() float64 { return 0 },
	}
	if got := a.retryDelay(0); got != time.Second {
		t.Fatalf("first retry delay = %s, want 1s", got)
	}
	if got := a.retryDelay(10); got != 15*time.Second {
		t.Fatalf("capped retry delay = %s, want 15s", got)
	}

	a.retryJitter = func() float64 { return 1 }
	if got := a.retryDelay(0); got != 2*time.Second {
		t.Fatalf("maximum first retry delay = %s, want 2s", got)
	}
	if got := a.retryDelay(10); got != maxRetryDelay {
		t.Fatalf("maximum capped retry delay = %s, want %s", got, maxRetryDelay)
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

func TestLoadContextFilesWalksAncestorsInOrder(t *testing.T) {
	root := t.TempDir()
	project := root + "/project"
	cwd := project + "/service"
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/AGENTS.md", []byte("root guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project+"/AGENTS.md", []byte("project guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cwd+"/CLAUDE.md", []byte("service guidance"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := loadContextFiles(cwd)
	if len(files) != 3 {
		t.Fatalf("loaded files = %#v", files)
	}
	for i, want := range []string{"root guidance", "project guidance", "service guidance"} {
		if files[i].content != want {
			t.Fatalf("file %d content = %q, want %q", i, files[i].content, want)
		}
	}

	prompt := buildSystemPrompt(cwd)
	rootIndex := strings.Index(prompt, "root guidance")
	projectIndex := strings.Index(prompt, "project guidance")
	serviceIndex := strings.Index(prompt, "service guidance")
	if rootIndex < 0 || rootIndex >= projectIndex || projectIndex >= serviceIndex {
		t.Fatalf("instructions are not ordered broadest to most specific: %q", prompt)
	}
}

func TestLoadContextFilesPrefersAgents(t *testing.T) {
	cwd := t.TempDir()
	for name, content := range map[string]string{
		"AGENTS.md": "agents guidance",
		"CLAUDE.md": "claude guidance",
	} {
		if err := os.WriteFile(cwd+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files := loadContextFiles(cwd)
	if len(files) == 0 || files[len(files)-1].content != "agents guidance" {
		t.Fatalf("loaded files = %#v", files)
	}
	prompt := buildSystemPrompt(cwd)
	if !strings.Contains(prompt, "agents guidance") || strings.Contains(prompt, "claude guidance") {
		t.Fatalf("AGENTS.md selection was not reflected in prompt: %q", prompt)
	}
}

func TestPythonREPLPersistsStateAndExposesShell(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)

	output, failed, err := repl.execute(t.Context(), "value = 40")
	if err != nil || failed || output != "" {
		t.Fatalf("assignment result = %q, failed=%v, error=%v", output, failed, err)
	}
	output, failed, err = repl.execute(t.Context(), "value + 2")
	if err != nil || failed || output != "42" {
		t.Fatalf("persistent result = %q, failed=%v, error=%v", output, failed, err)
	}
	output, failed, err = repl.execute(t.Context(), `result = shell("printf hello; exit 7"); (result.stdout, result.exit_code, result.error)`)
	if err != nil || failed || !strings.Contains(output, "('hello', 7, 'exit status 7')") {
		t.Fatalf("shell result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLExposesLLM(t *testing.T) {
	var prompts []string
	repl := newPythonREPL(func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "result for " + prompt, nil
	})
	t.Cleanup(repl.close)

	output, failed, err := repl.execute(t.Context(), `[llm(item) for item in ["first", "second"]]`)
	if err != nil || failed || output != "['result for first', 'result for second']" {
		t.Fatalf("llm result = %q, failed=%v, error=%v", output, failed, err)
	}
	if strings.Join(prompts, ",") != "first,second" {
		t.Fatalf("llm prompts = %#v", prompts)
	}
}

func TestPythonREPLRequiresStringLLMPrompt(t *testing.T) {
	repl := newPythonREPL(func(_ context.Context, prompt string) (string, error) {
		t.Fatalf("llm callback called with %q", prompt)
		return "", nil
	})
	t.Cleanup(repl.close)

	output, failed, err := repl.execute(t.Context(), `llm(42)`)
	if err != nil || !failed || !strings.Contains(output, "TypeError: llm() prompt must be a string") {
		t.Fatalf("llm result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLIsolatesRuntimeAndRestoresHostFunctions(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)

	_, failed, err := repl.execute(t.Context(), `json = "user value"; _protocol_out = None; shell = "shadowed"`)
	if err != nil || failed {
		t.Fatalf("shadowing result: failed=%v, error=%v", failed, err)
	}
	output, failed, err := repl.execute(t.Context(), `(json, _protocol_out, callable(shell))`)
	if err != nil || failed || output != "('user value', None, True)" {
		t.Fatalf("isolated result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLWebSearchReturnsValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<a class="result__a" href="https://example.com">Example</a><div class="result__snippet">A result.</div>`)
	}))
	defer server.Close()

	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)
	code := fmt.Sprintf(`web_search.__globals__["_web_search_url"] = %q; hits = web_search("test", 1); (hits[0].title, hits[0].url, hits[0].snippet)`, server.URL)
	output, failed, err := repl.execute(t.Context(), code)
	if err != nil || failed || output != "('Example', 'https://example.com', 'A result.')" {
		t.Fatalf("search result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLReportsPythonErrorsWithoutLosingState(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)
	_, _, _ = repl.execute(t.Context(), "saved = 'still here'")
	output, failed, err := repl.execute(t.Context(), "1 / 0")
	if err != nil || !failed || !strings.Contains(output, "ZeroDivisionError") {
		t.Fatalf("error result = %q, failed=%v, error=%v", output, failed, err)
	}
	output, failed, err = repl.execute(t.Context(), "saved")
	if err != nil || failed || output != "'still here'" {
		t.Fatalf("state after error = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLCancellationPreservesState(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)
	_, _, _ = repl.execute(t.Context(), "saved = 42")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err := repl.execute(ctx, `shell("sleep 10 | cat")`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	output, failed, err := repl.execute(t.Context(), "saved")
	if err != nil || failed || output != "42" {
		t.Fatalf("preserved result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLShellDoesNotInheritProtocolInput(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)
	output, failed, err := repl.execute(t.Context(), `result = shell("if read line; then printf data; else printf eof; fi", 0.1); (result.stdout, result.exit_code, result.error)`)
	if err != nil || failed || output != "('eof', 0, None)" {
		t.Fatalf("shell result = %q, failed=%v, error=%v", output, failed, err)
	}
}

func TestPythonREPLShellTimeout(t *testing.T) {
	repl := newPythonREPL(nil)
	t.Cleanup(repl.close)
	started := time.Now()
	output, failed, err := repl.execute(t.Context(), `result = shell("sleep 10 | cat", 0.05); (result.exit_code, result.error)`)
	if err != nil || failed || output != "(-1, 'timeout:0.05')" {
		t.Fatalf("timeout result = %q, failed=%v, error=%v", output, failed, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed command took %s", elapsed)
	}
}
