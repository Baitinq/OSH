package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

const testAgentResponse = "test response"
const testModelName = "test-model"

func keyPress(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func keyText(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(text)[0], Text: text}
}

func newTestModel() model {
	return newModel(testModelName, func(string, func(toolEvent), context.Context) response {
		return response{text: testAgentResponse, contextTokens: 1234}
	})
}

func updateModel(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(model)
}

func updateModelWithCmd(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	return updated.(model), cmd
}

func TestRequireOpenAIAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if err := requireOpenAIAPIKey(); err == nil {
		t.Fatal("expected an error when OPENAI_API_KEY is empty")
	}

	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := requireOpenAIAPIKey(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResponseRunsAsynchronously(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, keyText("hello agent"))
	var cmd tea.Cmd
	m, cmd = updateModelWithCmd(t, m, keyPress(tea.KeyEnter))

	if !m.responding {
		t.Fatal("model should be waiting for a response")
	}
	if !m.responding {
		t.Fatal("model should be waiting for a response")
	}
	if cmd == nil {
		t.Fatal("enter did not return an asynchronous command")
	}
	if len(m.input) != 0 || m.cursor != 0 {
		t.Fatalf("input was not reset: %q at %d", string(m.input), m.cursor)
	}
	if !strings.Contains(m.View().Content, "Thinking…") {
		t.Fatal("view does not show the loading spinner")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("command returned %#v, want a batch", cmd())
	}
	if len(batch) != 3 {
		t.Fatalf("command returned %#v, want response, tool-events and spinner commands", batch)
	}
	result := batch[0]()
	m = updateModel(t, m, result)
	if m.responding {
		t.Fatal("model still waiting after response")
	}
	respPrint, ok := result.(responseMsg)
	if !ok || respPrint.text != testAgentResponse {
		t.Fatalf("unexpected response message: %#v", result)
	}
	if m.contextTokens != 1234 {
		t.Fatalf("context tokens = %d, want 1234", m.contextTokens)
	}
}

func TestEnterSteersInProgressResponse(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.input = []rune("change direction")
	m.cursor = len(m.input)

	m, cmd := updateModelWithCmd(t, m, keyPress(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("steer started before the cancelled response finished")
	}
	if !cancelled || m.pendingSteer != "change direction" {
		t.Fatalf("response was not steered: cancelled=%v pending=%q", cancelled, m.pendingSteer)
	}
	if len(m.input) != 0 || len(m.messages) != 0 {
		t.Fatalf("steer should remain above the input until injected: %#v", m)
	}
	view := m.View().Content
	if !strings.Contains(view, steerMessageStyle.Render("    steer  change direction")) ||
		strings.Index(view, "steer") > strings.Index(view, m.renderComposer(m.width)) {
		t.Fatalf("steer is not shown above the composer: %q", view)
	}

	m, cmd = updateModelWithCmd(t, m, responseMsg{id: 1})
	if cmd == nil || !m.responding || m.nextRequestID != 2 || m.pendingSteer != "" {
		t.Fatalf("steer did not start after cancellation completed: %#v", m)
	}
	if got := m.messages[len(m.messages)-1]; got.role != "you" || got.text != "change direction" {
		t.Fatalf("steer was not added to the conversation when injected: %#v", m.messages)
	}
}

func TestShiftEnterQueuesUntilCurrentResponseFinishes(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	m.input = []rune("do this next")
	m.cursor = len(m.input)

	shiftEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	m, cmd := updateModelWithCmd(t, m, shiftEnter)
	if cmd != nil || len(m.queued) != 1 || m.queued[0] != "do this next" {
		t.Fatalf("message was not queued: %#v", m)
	}
	if len(m.messages) != 0 {
		t.Fatalf("queued message should not appear in conversation history yet: %#v", m.messages)
	}
	view := m.View().Content
	if !strings.Contains(view, queuedMessageStyle.Render("    queued  do this next")) ||
		strings.Index(view, "queued") > strings.Index(view, m.renderComposer(m.width)) {
		t.Fatalf("queued message is not shown above the composer: %q", view)
	}

	m, cmd = updateModelWithCmd(t, m, responseMsg{id: 1, text: "first response"})
	if cmd == nil || !m.responding || m.nextRequestID != 2 || len(m.queued) != 0 {
		t.Fatalf("queued message did not start after response: %#v", m)
	}
	if got := m.messages[len(m.messages)-1]; got.role != "you" || got.text != "do this next" {
		t.Fatalf("queued message was not injected after the response: %#v", m.messages)
	}
}

func TestPendingMessagesGrowDownwardInSubmissionOrder(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	m.input = []rune("first queued")
	m.cursor = len(m.input)
	m = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m.input = []rune("then steer")
	m.cursor = len(m.input)
	m = updateModel(t, m, keyPress(tea.KeyEnter))

	view := m.View().Content
	queued := queuedMessageStyle.Render("    queued  first queued")
	steer := steerMessageStyle.Render("    steer  then steer")
	queuedAt := strings.Index(view, queued)
	steerAt := strings.Index(view, steer)
	composerAt := strings.Index(view, m.renderComposer(m.width))
	if queuedAt < 0 || steerAt <= queuedAt || composerAt <= steerAt {
		t.Fatalf("pending messages are not growing downward above the composer: %q", view)
	}
	if queued == steer {
		t.Fatal("queued and steer messages should use different colors")
	}
}

func TestSpaceKeyAddsSpaceToInput(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, keyText("hello"))
	m = updateModel(t, m, keyPress(tea.KeySpace))
	m = updateModel(t, m, keyText("world"))

	if got := string(m.input); got != "hello world" {
		t.Fatalf("input = %q, want %q", got, "hello world")
	}
}

func TestEmptyInputDoesNotAddMessages(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, keyText("   "))
	m, cmd := updateModelWithCmd(t, m, keyPress(tea.KeyEnter))

	if cmd != nil {
		t.Fatalf("empty input started a command: %#v", cmd)
	}
}

func TestRenderedMessagesIncludeTimestampAndToolOutput(t *testing.T) {
	user := renderMessageLines(message{role: "you", text: "hello", sentAt: "18:37"}, 80)
	if len(user) == 0 || !strings.Contains(user[0], "[18:37] hello") {
		t.Fatalf("user message is missing its timestamp: %q", user)
	}

	call := strings.Join(renderMessageLines(message{role: "tool", text: "$ echo hi"}, 80), "\n")
	if !strings.Contains(call, "$ echo hi") {
		t.Fatalf("tool call line missing: %q", call)
	}

	output := strings.Join(renderMessageLines(message{role: "tool", text: "hi\n"}, 80), "\n")
	if !strings.Contains(output, "hi") {
		t.Fatalf("tool output line missing: %q", output)
	}
}

func TestViewShowsModelAndContextTokensBelowComposer(t *testing.T) {
	m := newTestModel()
	m.contextTokens = 1234567
	view := m.View().Content

	composer := m.renderComposer(m.width)
	want := composer + "\n" + dimStyle.Render("model test-model  ·  context 1,234,567 tokens")
	if !strings.Contains(view, want) {
		t.Fatalf("model and context info is not below composer: %q", view)
	}
}

func TestViewShowsSpinnerWhileResponding(t *testing.T) {
	m := newTestModel()
	if strings.Contains(m.View().Content, "Thinking…") {
		t.Fatal("spinner shown while idle")
	}
	m.responding = true
	if !strings.Contains(m.View().Content, "Thinking… · enter steer · shift+enter queue · esc cancel") {
		t.Fatal("spinner not shown while responding")
	}
}
func TestEscCancelsInProgressResponse(t *testing.T) {
	m := newTestModel()
	m.responding = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.nextRequestID = 1

	m, _ = updateModelWithCmd(t, m, keyPress(tea.KeyEsc))

	if !cancelled {
		t.Fatal("esc did not cancel the in-flight request")
	}
	if m.responding || m.cancel != nil {
		t.Fatal("model should not be responding after esc")
	}
	if len(m.messages) != 1 || m.messages[0].text != "Cancelled." {
		t.Fatalf("unexpected messages after cancel: %#v", m.messages)
	}
	m = updateModel(t, m, responseMsg{id: 1, text: testAgentResponse, contextTokens: 1234})
	if len(m.messages) != 1 {
		t.Fatalf("stale response was appended after cancellation: %#v", m.messages)
	}
}

func TestResizeRedrawsFrame(t *testing.T) {
	m := newTestModel()
	m.width = 60
	m.height = 18
	m = updateModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	if m.width != 100 || m.height != 30 {
		t.Fatalf("size not updated: %dx%d", m.width, m.height)
	}
	view := m.View().Content
	if height := strings.Count(strings.TrimSuffix(view, "\n"), "\n") + 1; height != 30 {
		t.Fatalf("view height = %d, want 30 after resize", height)
	}
	composer := m.renderComposer(100)
	if first := strings.Split(composer, "\n")[0]; len(first) < 96 {
		t.Fatalf("composer not resized to new width: %q", first)
	}
}

func TestGrowingViewportBackfillsMessagesFromHistory(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 8

	for _, text := range []string{"first message", "second message", "third message"} {
		m.appendMessage(message{role: "agent", text: text})
	}
	if strings.Contains(strings.Join(m.bodyLines, "\n"), "first message") {
		t.Fatal("first message should have scrolled out of the small viewport")
	}

	m = updateModel(t, m, tea.WindowSizeMsg{Width: 40, Height: 14})
	body := strings.Join(m.bodyLines, "\n")
	if !strings.Contains(body, "first message") ||
		!strings.Contains(body, "second message") ||
		!strings.Contains(body, "third message") {
		t.Fatalf("growing viewport did not backfill message history: %q", body)
	}
}
