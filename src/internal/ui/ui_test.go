package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tui "github.com/grindlemire/go-tui"
)

const (
	testModelName       = "test-model"
	testReasoningEffort = "medium"
)

type testRuntime struct{ calls chan func() }

func newTestRuntime() *testRuntime        { return &testRuntime{calls: make(chan func(), 32)} }
func (r *testRuntime) Dispatch(fn func()) { r.calls <- fn }

func (r *testRuntime) runNext(t *testing.T) {
	t.Helper()
	select {
	case fn := <-r.calls:
		fn()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UI dispatch")
	}
}

func newState(respond func(string, func(toolEvent), context.Context) response) (*oshUI, *testRuntime) {
	runtime := newTestRuntime()
	s := newUI(testModelName, testReasoningEffort, respond)
	s.ensureTextarea()
	s.dispatch = runtime.Dispatch
	s.emit = func(message) {}
	return s, runtime
}

func TestTextareaEditingAndWordAliases(t *testing.T) {
	s, _ := newState(nil)
	s.textarea.SetText("hello world")
	s.moveWord(-1)
	s.textarea.InsertText("X")
	if got := s.textarea.Text(); got != "hello Xworld" {
		t.Fatalf("word movement produced %q", got)
	}
	s.deleteWord(-1)
	if got := s.textarea.Text(); got != "hello world" {
		t.Fatalf("word deletion produced %q", got)
	}

	s.textarea.SetText("alpha beta")
	s.textarea.SetCursorPos(5)
	s.deleteToLineStart()
	if got := s.textarea.Text(); got != " beta" {
		t.Fatalf("line deletion produced %q", got)
	}
}

func TestPendingInputsAreRecordedInOrder(t *testing.T) {
	s, _ := newState(nil)
	s.responding = true
	s.submitInput("queued follow-up", true)
	s.submitInput("priority correction", false)

	if len(s.pendingInputs) != 2 || s.pendingInputs[0].kind != "queued" || s.pendingInputs[1].kind != "steer" {
		t.Fatalf("pending inputs = %#v", s.pendingInputs)
	}
}

func TestSubmitRunsAsynchronously(t *testing.T) {
	release := make(chan struct{})
	s, runtime := newState(func(input string, _ func(toolEvent), _ context.Context) response {
		if input != "hello" {
			t.Errorf("input = %q", input)
		}
		<-release
		return response{Text: "answer", ContextTokens: 1234}
	})
	s.textarea.SetText("hello")
	s.submitInput(s.textarea.Text(), false)

	if !s.responding || s.textarea.Text() != "" {
		t.Fatalf("request did not start: responding=%v input=%q", s.responding, s.textarea.Text())
	}
	if len(s.messages) != 1 || s.messages[0].role != "you" {
		t.Fatalf("user message missing: %#v", s.messages)
	}

	close(release)
	runtime.runNext(t)
	if s.responding || s.contextTokens != 1234 {
		t.Fatalf("response did not finish: %#v", s)
	}
	if got := s.messages[len(s.messages)-1]; got.role != "agent" || got.text != "answer" {
		t.Fatalf("agent response missing: %#v", s.messages)
	}
}

func TestToolEventsAreAddedToTranscript(t *testing.T) {
	s, runtime := newState(func(_ string, emit func(toolEvent), _ context.Context) response {
		emit(toolEvent{Phase: "call", Name: "shell", Detail: "pwd"})
		emit(toolEvent{Phase: "result", Name: "shell", Detail: "/tmp"})
		return response{Text: "done"}
	})
	s.textarea.SetText("inspect")
	s.submitInput(s.textarea.Text(), false)

	for s.responding {
		runtime.runNext(t)
	}
	if len(s.messages) != 4 {
		t.Fatalf("messages = %#v", s.messages)
	}
	if s.messages[1].text != "$ pwd" || s.messages[2].text != "/tmp" {
		t.Fatalf("tool events out of order: %#v", s.messages)
	}
}

func TestResponseErrorIsRetained(t *testing.T) {
	s, runtime := newState(func(string, func(toolEvent), context.Context) response {
		return response{Err: errors.New("request failed")}
	})
	s.textarea.SetText("hello")
	s.submitInput(s.textarea.Text(), false)
	for s.responding {
		runtime.runNext(t)
	}
	if got := s.messages[len(s.messages)-1]; got.role != "error" || got.text != "request failed" {
		t.Fatalf("error missing: %#v", s.messages)
	}
}

func TestEnterSteersAndShiftEnterQueues(t *testing.T) {
	s, _ := newState(func(string, func(toolEvent), context.Context) response { return response{} })
	s.responding = true

	s.textarea.SetText("queued follow-up")
	s.handleKey(tui.KeyEvent{Key: tui.KeyEnter, Mod: tui.ModShift})
	s.textarea.SetText("priority correction")
	s.handleKey(tui.KeyEvent{Key: tui.KeyEnter})

	if len(s.queued) != 1 || s.queued[0] != "queued follow-up" {
		t.Fatalf("queue = %#v", s.queued)
	}
	if s.pendingSteer != "priority correction" {
		t.Fatalf("steer = %q", s.pendingSteer)
	}
	if len(s.pendingInputs) != 2 || s.pendingInputs[0].kind != "queued" || s.pendingInputs[1].kind != "steer" {
		t.Fatalf("pending inputs = %#v", s.pendingInputs)
	}
}

func TestSteerRunsBeforeQueuedMessage(t *testing.T) {
	started := make(chan string, 2)
	s, _ := newState(func(input string, _ func(toolEvent), ctx context.Context) response {
		started <- input
		<-ctx.Done()
		return response{}
	})
	s.responding = true
	s.nextRequestID = 1
	s.queued = []string{"queued"}
	s.pendingSteer = "steer"
	s.pendingInputs = []pendingInput{{kind: "queued", text: "queued"}, {kind: "steer", text: "steer"}}

	s.finishResponse(response{id: 1, Text: "first"})
	select {
	case got := <-started:
		if got != "steer" {
			t.Fatalf("started %q, want steer", got)
		}
	case <-time.After(time.Second):
		t.Fatal("steer did not start")
	}
	if len(s.queued) != 1 {
		t.Fatal("queued message ran before steer")
	}
	s.cancelRequest()
}

func TestEscapeCancelsAndIgnoresStaleResponse(t *testing.T) {
	s, _ := newState(func(string, func(toolEvent), context.Context) response { return response{} })
	ctx, cancel := context.WithCancel(context.Background())
	s.responding = true
	s.cancel = cancel
	s.nextRequestID = 3

	s.cancelRequest()
	if ctx.Err() == nil || s.responding || s.cancel != nil {
		t.Fatalf("request was not cancelled: %#v", s)
	}
	if len(s.messages) != 1 || s.messages[0].text != "Cancelled." {
		t.Fatalf("cancellation message missing: %#v", s.messages)
	}
	s.finishResponse(response{id: 3, Text: "stale"})
	if len(s.messages) != 1 {
		t.Fatalf("stale response changed transcript: %#v", s.messages)
	}
}

func TestFormatTokenCount(t *testing.T) {
	for input, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := formatTokenCount(input); got != want {
			t.Fatalf("formatTokenCount(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestRenderedMultilineMessageRestoresStylePerLine(t *testing.T) {
	got := renderedMessage(message{role: "error", text: "first\nsecond"}, 80)
	if strings.Count(got, ";48;5;160m") != 2 {
		t.Fatalf("multiline error did not style each line independently: %q", got)
	}
	if !strings.Contains(got, " first ") || !strings.Contains(got, " second ") {
		t.Fatalf("multiline error lost indentation: %q", got)
	}
}

func TestRenderedMessagesUseOriginalANSIPalette(t *testing.T) {
	tests := []struct {
		message message
		code    string
	}{
		{message{role: "you", text: "hello", sentAt: "12:34"}, "38;5;255;48;5;236"},
		{message{role: "agent", text: "hello"}, "38;5;252"},
		{message{role: "tool", text: "$ pwd"}, "38;5;71"},
		{message{role: "tool", text: "/tmp"}, "38;5;247"},
		{message{role: "error", text: "failed"}, "38;5;255;48;5;160"},
		{message{role: "system", text: "cancelled"}, "38;5;242"},
	}
	for _, test := range tests {
		if got := renderedMessage(test.message, 80); !strings.Contains(got, test.code) {
			t.Errorf("rendered %s message missing ANSI palette %q: %q", test.message.role, test.code, got)
		}
	}
}

func TestMainScreenRendererAppendsWithoutAlternateScreen(t *testing.T) {
	var out strings.Builder
	r := newMainScreenRenderer(&out, 20, 4)
	if err := r.render([]string{"one", "input"}, 1, 2); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := r.render([]string{"one", "two", "input"}, 2, 2); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "\x1b[?1049") {
		t.Fatalf("entered alternate screen: %q", got)
	}
	if strings.Contains(got, "\x1b[3J") {
		t.Fatalf("append cleared scrollback: %q", got)
	}
	if !strings.Contains(got, "two") {
		t.Fatalf("append missing new line: %q", got)
	}
}

func TestMainScreenRendererReplaysWhenChangedLineIsAboveViewport(t *testing.T) {
	var out strings.Builder
	r := newMainScreenRenderer(&out, 20, 3)
	lines := []string{"zero", "one", "two", "input"}
	if err := r.render(lines, 3, 0); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	changed := append([]string(nil), lines...)
	changed[0] = "ZERO"
	if err := r.render(changed, 3, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[3J") {
		t.Fatalf("unaddressable edit did not replay: %q", out.String())
	}
}

func TestMainScreenRendererResizeUsesPiStyleReplay(t *testing.T) {
	var out strings.Builder
	r := newMainScreenRenderer(&out, 20, 4)
	if err := r.render([]string{"history", "input"}, 1, 0); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	r.resize(10, 4)
	if err := r.render([]string{"history", "input"}, 1, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[2J\x1b[H\x1b[3J") {
		t.Fatalf("resize did not fully replay: %q", out.String())
	}
}

func TestStreamingDeltaLivesInRenderedModel(t *testing.T) {
	s, _ := newState(nil)
	s.responding = true
	s.nextRequestID = 1
	s.handleToolEvent(1, toolEvent{Phase: "text_delta", Detail: "hello "})
	s.handleToolEvent(1, toolEvent{Phase: "text_delta", Detail: "world"})
	lines, _, _ := s.render(40)
	if !strings.Contains(strings.Join(lines, "\n"), "hello world") {
		t.Fatalf("stream missing from render: %q", lines)
	}
	if len(s.messages) != 0 {
		t.Fatalf("stream was finalized early: %#v", s.messages)
	}
}

func TestLongEditorKeepsCursorInLogicalOutput(t *testing.T) {
	s, _ := newState(nil)
	s.textarea.SetText(strings.Repeat("abcdefghij", 20))
	lines, row, col := s.render(20)
	if row < 1 || row >= len(lines)-1 {
		t.Fatalf("cursor row %d outside editor in %d lines", row, len(lines))
	}
	if col < 2 || col >= 20 {
		t.Fatalf("cursor column = %d", col)
	}
}

func TestEditorCursorUsesGraphemeClusters(t *testing.T) {
	text := "a👩‍💻b"
	_, row, col := renderEditor(text, 2, 20) // after a + one ZWJ cluster
	if row != 1 || col != 5 {                // border+padding (2), a (1), emoji (2)
		t.Fatalf("cursor = (%d,%d), want (1,5)", row, col)
	}
	if got := clusterToRuneIndex(text, 2); got != 4 {
		t.Fatalf("clusterToRuneIndex = %d, want 4", got)
	}
}

func TestLineWidthIgnoresANSI(t *testing.T) {
	if got := lineWidth("\x1b[38;5;39mhello\x1b[0m"); got != 5 {
		t.Fatalf("lineWidth = %d, want 5", got)
	}
}

func TestMainScreenRendererStopDoesNotOverwriteCursorCell(t *testing.T) {
	var out strings.Builder
	r := newMainScreenRenderer(&out, 20, 4)
	if err := r.render([]string{"input", "footer"}, 0, 0); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := r.stop(); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(out.String(), " ") {
		t.Fatalf("stop overwrote the cell under the hardware cursor: %q", out.String())
	}
}

func TestShortDocumentFillsAndBottomAlignsViewport(t *testing.T) {
	s, _ := newState(nil)
	lines, row, _ := s.render(40, 12)
	if len(lines) != 12 {
		t.Fatalf("rendered height = %d, want 12", len(lines))
	}
	if row != 9 { // blank padding, top border, then editor content row
		t.Fatalf("cursor row = %d, want 9", row)
	}
	if lines[0] != "" {
		t.Fatalf("first row = %q, want viewport filler", lines[0])
	}
	footer := stripANSI(lines[len(lines)-1])
	if !strings.Contains(footer, "test-model (medium)") {
		t.Fatalf("footer is not bottom-aligned: %q", lines[len(lines)-1])
	}
	if strings.HasPrefix(footer, "model ") {
		t.Fatalf("footer retains redundant model label: %q", footer)
	}
}

func TestLongDocumentIsNotViewportPadded(t *testing.T) {
	s, _ := newState(nil)
	for i := 0; i < 20; i++ {
		s.messages = append(s.messages, message{role: "agent", text: "line"})
	}
	lines, _, _ := s.render(40, 12)
	if len(lines) <= 12 {
		t.Fatalf("long document height = %d, want > 12", len(lines))
	}
	if lines[0] == "" {
		t.Fatal("long document was incorrectly prefixed with viewport filler")
	}
}
