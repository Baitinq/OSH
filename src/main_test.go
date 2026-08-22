package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func updateModel(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(model)
}

func TestEnterAddsUserMessageAndDummyResponse(t *testing.T) {
	m := newModel()
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello agent")})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(m.messages))
	}
	if m.messages[0].role != "you" || m.messages[0].text != "hello agent" || m.messages[0].sentAt == "" {
		t.Fatalf("unexpected user message: %#v", m.messages[0])
	}
	if m.messages[1] != (message{role: "agent", text: testResponse}) {
		t.Fatalf("unexpected agent message: %#v", m.messages[1])
	}
	if len(m.input) != 0 || m.cursor != 0 {
		t.Fatalf("input was not reset: %q at %d", string(m.input), m.cursor)
	}
}

func TestSpaceKeyAddsSpaceToInput(t *testing.T) {
	m := newModel()
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("world")})

	if got := string(m.input); got != "hello world" {
		t.Fatalf("input = %q, want %q", got, "hello world")
	}
}

func TestEmptyInputDoesNotAddMessages(t *testing.T) {
	m := newModel()
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("   ")})
	m = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.messages) != 0 {
		t.Fatalf("got %d messages, want none", len(m.messages))
	}
}

func TestLogKeepsNewestMessagesAtBottom(t *testing.T) {
	m := newModel()
	m.messages = []message{
		{role: "you", text: "old message", sentAt: "18:37"},
		{role: "agent", text: testResponse},
	}

	log := m.renderLog(80, 2)
	if !strings.Contains(log, testResponse) {
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
	m := newModel()
	m.messages = []message{{role: "you", text: "hello", sentAt: "18:37"}}

	log := m.renderLog(80, 3)
	if !strings.Contains(log, "[18:37] hello") {
		t.Fatalf("user message is missing its timestamp: %q", log)
	}
}

func TestViewFillsTerminal(t *testing.T) {
	m := newModel()
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
