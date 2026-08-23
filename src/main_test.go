package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const testAgentResponse = "test response"
const testModelName = "test-model"

func newTestModel() model {
	return newModel(testModelName, func(string, func(toolEvent)) response {
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

	if len(m.messages) != 1 {
		t.Fatalf("got %d messages before response, want 1", len(m.messages))
	}
	if m.messages[0].role != "you" || m.messages[0].text != "hello agent" || m.messages[0].sentAt == "" {
		t.Fatalf("unexpected user message: %#v", m.messages[0])
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
	if !ok || len(batch) != 3 {
		t.Fatalf("command returned %#v, want response, tool-events and spinner commands", batch)
	}
	m = updateModel(t, m, batch[0]())
	if m.responding {
		t.Fatal("model still waiting after response")
	}
	if len(m.messages) != 2 || m.messages[1] != (message{role: "agent", text: testAgentResponse}) {
		t.Fatalf("unexpected messages after response: %#v", m.messages)
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
	if cmd != nil || len(m.messages) != 0 {
		t.Fatalf("enter started another response while waiting: %#v", m.messages)
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
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.messages) != 0 {
		t.Fatalf("got %d messages, want none", len(m.messages))
	}
}

func TestLogKeepsNewestMessagesAtBottom(t *testing.T) {
	m := newTestModel()
	m.messages = []message{
		{role: "you", text: "old message", sentAt: "18:37"},
		{role: "agent", text: testAgentResponse},
	}

	log := m.renderLog(80, 2)
	if !strings.Contains(log, testAgentResponse) {
		t.Fatalf("newest message missing from clipped log: %q", log)
	}
	if strings.Contains(log, "old message") {
		t.Fatalf("old message should have scrolled out: %q", log)
	}
	if strings.Contains(log, "YOU") || strings.Contains(log, "AGENT") {
		t.Fatalf("log should not contain role labels: %q", log)
	}
}

func TestUserMessageShowsSentTime(t *testing.T) {
	m := newTestModel()
	m.messages = []message{{role: "you", text: "hello", sentAt: "18:37"}}

	log := m.renderLog(80, 3)
	if !strings.Contains(log, "[18:37] hello") {
		t.Fatalf("user message is missing its timestamp: %q", log)
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

func TestViewFillsTerminal(t *testing.T) {
	m := newTestModel()
	m.width = 60
	m.height = 18
	view := m.View()

	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("view height = %d, want %d", got, m.height)
	}
	if got := lipgloss.Width(view); got != m.width {
		t.Fatalf("view width = %d, want %d", got, m.width)
	}
}

func TestToolEventsAppearInLog(t *testing.T) {
	m := newTestModel()
	m = updateModel(t, m, toolEventMsg{phase: "call", name: "shell", detail: "echo hi"})
	m = updateModel(t, m, toolEventMsg{phase: "result", name: "shell", detail: "hi\n"})

	log := m.renderLog(80, 10)
	if !strings.Contains(log, "$ echo hi") || !strings.Contains(log, "hi") {
		t.Fatalf("tool call/output missing from log: %q", log)
	}
}
