package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const testAgentResponse = "test response"
const testModelName = "test-model"

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
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello agent")})
	var cmd tea.Cmd
	m, cmd = updateModelWithCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})

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
	if !strings.Contains(m.View(), "Thinking…") {
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

func TestEnterDoesNotStartAnotherResponseWhileWaiting(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.input = []rune("another message")
	m.cursor = len(m.input)

	m, cmd := updateModelWithCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter started another response while waiting")
	}
}

func TestSpaceKeyAddsSpaceToInput(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("world")})

	if got := string(m.input); got != "hello world" {
		t.Fatalf("input = %q, want %q", got, "hello world")
	}
}

func TestEmptyInputDoesNotAddMessages(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("   ")})
	m, cmd := updateModelWithCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})

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
	view := m.View()

	composer := m.renderComposer(m.width)
	want := composer + "\n" + dimStyle.Render("model test-model  ·  context 1,234,567 tokens")
	if !strings.Contains(view, want) {
		t.Fatalf("model and context info is not below composer: %q", view)
	}
}

func TestViewShowsSpinnerWhileResponding(t *testing.T) {
	m := newTestModel()
	if strings.Contains(m.View(), "Thinking…") {
		t.Fatal("spinner shown while idle")
	}
	m.responding = true
	if !strings.Contains(m.View(), "Thinking… (esc to cancel)") {
		t.Fatal("spinner not shown while responding")
	}
}
func TestEscCancelsInProgressResponse(t *testing.T) {
	m := newTestModel()
	m.responding = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.nextRequestID = 1

	m, _ = updateModelWithCmd(t, m, tea.KeyMsg{Type: tea.KeyEsc})

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
	view := m.View()
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
