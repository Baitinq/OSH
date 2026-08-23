package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

func setTestInput(m *model, text string, cursor int) {
	m.composer.SetValue(text)
	m.composer.SetCursorColumn(cursor)
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
	if cmd == nil {
		t.Fatal("enter did not return an asynchronous command")
	}
	if m.composer.Value() != "" || m.composer.Column() != 0 {
		t.Fatalf("input was not reset: %q at %d", m.composer.Value(), m.composer.Column())
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

func TestEnterDefersSteerUntilInProgressResponseFinishes(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	cancelled := false
	m.cancel = func() { cancelled = true }
	setTestInput(&m, "change direction", len([]rune("change direction")))

	m, cmd := updateModelWithCmd(t, m, keyPress(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("steer started before the active response finished")
	}
	if cancelled {
		t.Fatal("steer should not cancel the active response")
	}
	if !m.responding || m.pendingSteer != "change direction" {
		t.Fatalf("steer was not deferred: responding=%v pending=%q", m.responding, m.pendingSteer)
	}
	if m.composer.Value() != "" || len(m.messages) != 0 {
		t.Fatalf("steer should remain above the input until injected: %#v", m)
	}
	view := m.View().Content
	if !strings.Contains(view, pendingInputStyle.Render("    steer  change direction")) ||
		strings.Index(view, "steer") > strings.Index(view, m.renderComposer(m.width)) {
		t.Fatalf("steer is not shown above the composer: %q", view)
	}
	if !strings.Contains(view, "Thinking…") || strings.Contains(view, "Steering…") {
		t.Fatalf("pending steer changed the thinking status: %q", view)
	}

	m, cmd = updateModelWithCmd(t, m, responseMsg{id: 1, text: "completed response", contextTokens: 42})
	if cmd == nil || !m.responding || m.nextRequestID != 2 || m.pendingSteer != "" {
		t.Fatalf("steer did not start after the response completed: %#v", m)
	}
	if len(m.messages) != 2 {
		t.Fatalf("completed response and steer were not both retained: %#v", m.messages)
	}
	if got := m.messages[0]; got.role != "agent" || got.text != "completed response" {
		t.Fatalf("completed response was not retained before steer: %#v", m.messages)
	}
	if got := m.messages[1]; got.role != "you" || got.text != "change direction" {
		t.Fatalf("steer was not injected after the response: %#v", m.messages)
	}
}

func TestSteerRunsBeforeQueuedMessages(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	m.queued = []string{"ordinary follow-up"}
	m.pendingInputs = []pendingInput{{kind: "queued", text: "ordinary follow-up"}}
	setTestInput(&m, "priority correction", len([]rune("priority correction")))

	m = updateModel(t, m, keyPress(tea.KeyEnter))
	m, cmd := updateModelWithCmd(t, m, responseMsg{id: 1, text: "active answer"})

	if cmd == nil || !m.responding {
		t.Fatal("steer did not start after the active response")
	}
	if len(m.queued) != 1 || m.queued[0] != "ordinary follow-up" {
		t.Fatalf("ordinary queue ran before steer: %#v", m.queued)
	}
	if got := m.messages[len(m.messages)-1]; got.role != "you" || got.text != "priority correction" {
		t.Fatalf("steer was not given priority: %#v", m.messages)
	}
}

func TestShiftEnterQueuesUntilCurrentResponseFinishes(t *testing.T) {
	m := newTestModel()
	m.responding = true
	m.nextRequestID = 1
	setTestInput(&m, "do this next", len([]rune("do this next")))

	shiftEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	m, cmd := updateModelWithCmd(t, m, shiftEnter)
	if cmd != nil || len(m.queued) != 1 || m.queued[0] != "do this next" {
		t.Fatalf("message was not queued: %#v", m)
	}
	if len(m.messages) != 0 {
		t.Fatalf("queued message should not appear in conversation history yet: %#v", m.messages)
	}
	view := m.View().Content
	if !strings.Contains(view, pendingInputStyle.Render("    queued  do this next")) ||
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
	setTestInput(&m, "first queued", len([]rune("first queued")))
	m = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	setTestInput(&m, "then steer", len([]rune("then steer")))
	m = updateModel(t, m, keyPress(tea.KeyEnter))

	view := m.View().Content
	queued := pendingInputStyle.Render("    queued  first queued")
	steer := pendingInputStyle.Render("    steer  then steer")
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
	m = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = updateModel(t, m, keyText("world"))

	if got := m.composer.Value(); got != "hello world" {
		t.Fatalf("input = %q, want %q", got, "hello world")
	}
}

func TestModifiedArrowsMoveByWord(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{name: "control", msg: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}},
		{name: "alt", msg: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			setTestInput(&m, "one two_three four", len([]rune("one two_three four")))

			m = updateModel(t, m, tc.msg)
			if m.composer.Column() != len([]rune("one two_three ")) {
				t.Fatalf("word-left cursor = %d", m.composer.Column())
			}
			m = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tc.msg.Mod})
			if m.composer.Column() != len([]rune("one two_three four")) {
				t.Fatalf("word-right cursor = %d", m.composer.Column())
			}
		})
	}
}

func TestReadlineWordAliasesMoveCursor(t *testing.T) {
	m := newTestModel()
	setTestInput(&m, "one two", len([]rune("one two")))

	m = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	if m.composer.Column() != 4 {
		t.Fatalf("alt+b cursor = %d, want 4", m.composer.Column())
	}
	m = updateModel(t, m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if m.composer.Column() != len([]rune("one two")) {
		t.Fatalf("alt+f cursor = %d", m.composer.Column())
	}
}

func TestTextareaEditingShortcuts(t *testing.T) {
	t.Run("ctrl+w deletes previous word", func(t *testing.T) {
		m := newTestModel()
		setTestInput(&m, "one two three", len([]rune("one two three")))
		m = updateModel(t, m, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
		if got := m.composer.Value(); got != "one two " {
			t.Fatalf("input = %q at %d", got, m.composer.Column())
		}
	})

	t.Run("alt+d deletes next word", func(t *testing.T) {
		m := newTestModel()
		setTestInput(&m, "one two three", len([]rune("one ")))
		m = updateModel(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModAlt})
		if got := m.composer.Value(); got != "one  three" {
			t.Fatalf("input = %q at %d", got, m.composer.Column())
		}
	})

	t.Run("ctrl+u and ctrl+k delete around cursor", func(t *testing.T) {
		m := newTestModel()
		setTestInput(&m, "before after", len([]rune("before")))
		m = updateModel(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		if got := m.composer.Value(); got != " after" || m.composer.Column() != 0 {
			t.Fatalf("ctrl+u input = %q at %d", got, m.composer.Column())
		}

		setTestInput(&m, "before after", len([]rune("before")))
		m = updateModel(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
		if got := m.composer.Value(); got != "before" {
			t.Fatalf("ctrl+k input = %q at %d", got, m.composer.Column())
		}
	})
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
	m.queued = []string{"next"}
	view := m.View().Content
	if !strings.Contains(view, "Thinking…") {
		t.Fatal("spinner not shown while responding")
	}
	statusLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Thinking…") {
			statusLine = line
			break
		}
	}
	if strings.Contains(statusLine, "queued") || strings.Contains(statusLine, "steer") {
		t.Fatalf("thinking status includes pending input details: %q", statusLine)
	}
	for _, helper := range []string{"enter steer", "shift+enter queue", "esc cancel"} {
		if strings.Contains(view, helper) {
			t.Fatalf("view contains helper message %q", helper)
		}
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

func TestComposerFitsNarrowTerminal(t *testing.T) {
	m := newTestModel()
	m.width = 20

	composer := m.renderComposer(m.width)
	if got, want := lipgloss.Width(composer), m.width-2; got != want {
		t.Fatalf("composer width = %d, want %d", got, want)
	}
	if got := lipgloss.Height(composer); got != 3 {
		t.Fatalf("composer height = %d, want 3", got)
	}
}

func TestComposerGrowsAndWrapsLongInput(t *testing.T) {
	m := newTestModel()
	m.width = 30
	text := strings.Repeat("wrapped message ", 5)
	setTestInput(&m, text, len([]rune(text)))

	composer := m.renderComposer(m.width)
	if got := lipgloss.Height(composer); got <= 3 {
		t.Fatalf("composer height = %d, want a multiline composer", got)
	}
	if got, want := lipgloss.Width(composer), m.width-2; got != want {
		t.Fatalf("composer width = %d, want %d", got, want)
	}

	if got := m.composer.Value(); got != text {
		t.Fatal("textarea wrapping changed the input value")
	}
}

func TestComposerDoesNotReachRightEdgeWhileTypingDuringResponse(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.responding = true
	text := "a long input typed while the model is responding"
	setTestInput(&m, text, len([]rune(text)))

	composer := m.renderComposer(m.width)
	if got, want := lipgloss.Width(composer), m.width-2; got != want {
		t.Fatalf("composer expanded to width %d while typing, want %d", got, want)
	}
	if !strings.Contains(m.View().Content, composer) {
		t.Fatal("composer disappeared from the view while responding")
	}
}

func TestPendingMessagesCannotPushComposerOffscreen(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 8
	m.started = true
	m.responding = true
	for i := 0; i < 10; i++ {
		m.pendingInputs = append(m.pendingInputs, pendingInput{
			kind: "queued",
			text: "a long queued message that wraps onto several lines",
		})
	}

	view := m.View().Content
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("view height = %d, want %d", got, m.height)
	}
	if got := lipgloss.Width(view); got > m.width {
		t.Fatalf("view width = %d, terminal width = %d", got, m.width)
	}
	if !strings.Contains(view, m.renderComposer(m.width)) {
		t.Fatal("pending messages pushed the composer out of the viewport")
	}
}

func TestMouseWheelScrollsConversationHistory(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 8
	for _, text := range []string{"first message", "second message", "third message"} {
		m.appendMessage(message{role: "agent", text: text})
	}

	if strings.Contains(m.View().Content, "first message") {
		t.Fatal("oldest message should initially be above the viewport")
	}
	m = updateModel(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if !strings.Contains(m.View().Content, "first message") {
		t.Fatal("mouse wheel did not reveal retained conversation history")
	}
	m = updateModel(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.scrollOffset != 0 || strings.Contains(m.View().Content, "first message") {
		t.Fatal("mouse wheel did not return to the latest messages")
	}
}

func TestPageKeysScrollConversationHistory(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 8
	for _, text := range []string{"first message", "second message", "third message"} {
		m.appendMessage(message{role: "agent", text: text})
	}

	m = updateModel(t, m, keyPress(tea.KeyPgUp))
	if m.scrollOffset == 0 || !strings.Contains(m.View().Content, "first message") {
		t.Fatal("page up did not scroll conversation history")
	}
	m = updateModel(t, m, keyPress(tea.KeyPgDown))
	if m.scrollOffset != 0 {
		t.Fatalf("page down left scroll offset at %d", m.scrollOffset)
	}
}

func TestViewCapturesMouseForScrolling(t *testing.T) {
	view := newTestModel().View()
	if !view.AltScreen {
		t.Fatal("view should use the alternate screen")
	}
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion", view.MouseMode)
	}
}

func TestNewOutputDoesNotMoveScrolledViewport(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 8
	for _, text := range []string{"first message", "second message", "third message"} {
		m.appendMessage(message{role: "agent", text: text})
	}
	m.scrollBody(2)
	before := m.View().Content

	m.appendMessage(message{role: "agent", text: "new output"})
	if got := m.View().Content; got != before {
		t.Fatalf("new output moved the scrolled viewport\nbefore: %q\nafter:  %q", before, got)
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
	if strings.Contains(m.View().Content, "first message") {
		t.Fatal("first message should be outside the small viewport")
	}
	if !strings.Contains(strings.Join(m.bodyLines, "\n"), "first message") {
		t.Fatal("first message was discarded instead of retained for scrollback")
	}

	m = updateModel(t, m, tea.WindowSizeMsg{Width: 40, Height: 14})
	body := strings.Join(m.bodyLines, "\n")
	if !strings.Contains(body, "first message") ||
		!strings.Contains(body, "second message") ||
		!strings.Contains(body, "third message") {
		t.Fatalf("growing viewport did not backfill message history: %q", body)
	}
}

func TestToolOutputStaysInsideManagedView(t *testing.T) {
	m := newTestModel()
	m.width = 30
	m.height = 8
	m.responding = true
	m.nextRequestID = 1

	ch := make(chan toolEvent, 1)
	ch <- toolEvent{phase: "result", detail: "next event"}
	event := toolEventMsg{
		toolEvent: toolEvent{phase: "result", detail: strings.Repeat("long output ", 20)},
		requestID: 1,
		ch:        ch,
	}

	m, cmd := updateModelWithCmd(t, m, event)
	if cmd == nil {
		t.Fatal("tool listener was not continued")
	}
	if _, ok := cmd().(toolEventMsg); !ok {
		t.Fatal("overflowing tool output created unmanaged terminal output")
	}
	if !strings.Contains(m.View().Content, "long output") {
		t.Fatal("tool output was not retained in the managed viewport")
	}
}

func TestViewLeavesRightEdgeClearWhileResponding(t *testing.T) {
	for _, width := range []int{20, 30, 40} {
		m := newTestModel()
		m.width = width
		m.height = 12
		m.responding = true
		m.contextTokens = 1234567
		m.queued = []string{"next"}
		m.pendingInputs = []pendingInput{{
			kind: "queued",
			text: "a pending message long enough to wrap near the right edge",
		}}

		for lineNumber, line := range strings.Split(m.View().Content, "\n") {
			if got := lipgloss.Width(line); got > width-2 {
				t.Fatalf("width %d, line %d reaches terminal edge: visual width %d: %q", width, lineNumber, got, line)
			}
		}
	}
}
