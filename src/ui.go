package main

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

type message struct {
	role   string
	text   string
	sentAt string
}

type response struct {
	id            int
	text          string
	contextTokens int64
}

type toolEvent struct {
	phase  string // "call" or "result"
	name   string
	detail string
}

type toolEventMsg struct {
	toolEvent
	requestID int
	ch        <-chan toolEvent
}
type responseMsg response

type pendingInput struct {
	kind string
	text string
}

type model struct {
	width         int
	height        int
	input         []rune
	cursor        int
	messages      []message
	bodyLines     []string
	started       bool
	modelName     string
	contextTokens int64
	respond       func(string, func(toolEvent), context.Context) response
	toolEvents    chan toolEvent
	nextRequestID int
	cancel        context.CancelFunc
	responding    bool
	spinnerFrame  int
	queued        []string
	pendingSteer  string
	pendingInputs []pendingInput
}

type spinnerTickMsg struct{}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 80 * time.Millisecond

var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	userMessageStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Background(lipgloss.Color("236"))
	bodyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(0, 1)
	toolStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	toolOutputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	queuedMessageStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	steerMessageStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	cursorStyle        = lipgloss.NewStyle().Reverse(true)
)

func newModel(modelName string, respond func(string, func(toolEvent), context.Context) response) model {
	return model{
		width:     80,
		height:    24,
		modelName: modelName,
		respond:   respond,
	}
}

func (model) Init() tea.Cmd {
	return nil
}

func waitForResponse(respond func(string, func(toolEvent), context.Context) response, input string, ch chan toolEvent, ctx context.Context, id int) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		emit := func(ev toolEvent) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		}
		resp := respond(input, emit, ctx)
		resp.id = id
		return responseMsg(resp)
	}
}

func listenToolEvents(ch <-chan toolEvent, requestID int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return toolEventMsg{toolEvent: ev, requestID: requestID, ch: ch}
	}
}

// evictOverflow prints body lines that scrolled out of the frame into the
// terminal's native scrollback, so history survives leaving the viewport.
func (m *model) appendMessage(msg message) (evicted string) {
	width := max(m.width, 20)
	m.messages = append(m.messages, msg)
	m.bodyLines = append(m.bodyLines, renderMessageLines(msg, width)...)
	m.bodyLines = append(m.bodyLines, "") // spacing between messages

	bodyHeight := m.availableBodyHeight(width)

	if len(m.bodyLines) > bodyHeight {
		evictedCount := len(m.bodyLines) - bodyHeight
		evicted = strings.Join(m.bodyLines[:evictedCount], "\n")
		m.bodyLines = m.bodyLines[evictedCount:]
	}
	return evicted
}

// reflowBody rebuilds the viewport from message history after a resize. Lines
// previously moved into terminal scrollback can then fill newly available rows
// instead of leaving a gap above the retained viewport contents.
func (m *model) reflowBody() {
	width := max(m.width, 20)
	lines := make([]string, 0, len(m.bodyLines))
	for _, msg := range m.messages {
		lines = append(lines, renderMessageLines(msg, width)...)
		lines = append(lines, "")
	}

	bodyHeight := m.availableBodyHeight(width)
	if len(lines) > bodyHeight {
		lines = lines[len(lines)-bodyHeight:]
	}
	m.bodyLines = lines
}

func (m model) availableBodyHeight(width int) int {
	reservedHeight := 1 + lipgloss.Height(m.renderComposer(width))
	if m.responding {
		reservedHeight++
	}
	if pending := m.renderPendingMessages(width); pending != "" {
		reservedHeight += lipgloss.Height(pending)
	}
	return max(max(m.height, 8)-reservedHeight, 1)
}

func tickSpinner() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func (m *model) startRequest(text string, showUser bool) tea.Cmd {
	m.started = true
	m.responding = true
	m.spinnerFrame = 0
	m.toolEvents = make(chan toolEvent, 16)
	m.nextRequestID++
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.reflowBody()

	var cmds []tea.Cmd
	if showUser {
		if evicted := m.appendMessage(message{role: "you", text: text, sentAt: time.Now().Format("15:04")}); evicted != "" {
			cmds = append(cmds, tea.Println(evicted))
		}
	}
	cmds = append(cmds,
		waitForResponse(m.respond, text, m.toolEvents, ctx, m.nextRequestID),
		listenToolEvents(m.toolEvents, m.nextRequestID),
		tickSpinner(),
	)
	return tea.Batch(cmds...)
}

func (m *model) submitInput(queue bool) tea.Cmd {
	text := strings.TrimSpace(string(m.input))
	if text == "" {
		return nil
	}
	m.input = nil
	m.cursor = 0

	if !m.responding {
		return m.startRequest(text, true)
	}
	if queue {
		m.queued = append(m.queued, text)
		m.pendingInputs = append(m.pendingInputs, pendingInput{kind: "queued", text: text})
		m.reflowBody()
		return nil
	}

	// A steer interrupts the current response. Wait for its goroutine to finish
	// before starting the next request so the agent's local history is only ever
	// mutated by one response loop at a time.
	if m.pendingSteer == "" {
		m.pendingSteer = text
	} else {
		m.pendingSteer += "\n\n" + text
	}
	m.pendingInputs = append(m.pendingInputs, pendingInput{kind: "steer", text: text})
	if m.cancel != nil {
		m.cancel()
	}
	m.reflowBody()
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.reflowBody()
		return m, nil
	case responseMsg:
		if msg.id != m.nextRequestID {
			return m, nil
		}
		m.cancel = nil
		m.contextTokens = msg.contextTokens

		if m.pendingSteer != "" {
			text := m.pendingSteer
			m.pendingSteer = ""
			m.removePendingInputs("steer", -1)
			m.responding = false
			return m, m.startRequest(text, true)
		}

		m.responding = false
		var cmds []tea.Cmd
		if msg.text != "" {
			if evicted := m.appendMessage(message{role: "agent", text: msg.text}); evicted != "" {
				cmds = append(cmds, tea.Println(evicted))
			}
		}
		if len(m.queued) > 0 {
			text := m.queued[0]
			m.queued = m.queued[1:]
			m.removePendingInputs("queued", 1)
			cmds = append(cmds, m.startRequest(text, true))
		}
		return m, tea.Batch(cmds...)
	case toolEventMsg:
		if msg.requestID != m.nextRequestID {
			return m, nil
		}
		text := msg.detail
		if msg.phase == "call" {
			text = "$ " + text
		}
		if evicted := m.appendMessage(message{role: "tool", text: text}); evicted != "" {
			return m, tea.Batch(tea.Println(evicted), listenToolEvents(msg.ch, msg.requestID))
		}
		return m, listenToolEvents(msg.ch, msg.requestID)
	case spinnerTickMsg:
		if !m.responding {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, tickSpinner()
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "ctrl+c":
			return m, tea.Quit
		case "shift+enter":
			return m, m.submitInput(true)
		case "enter":
			return m, m.submitInput(false)
		case "esc", "escape":
			if m.responding && m.cancel != nil {
				m.cancel()
				m.pendingSteer = ""
				m.removePendingInputs("steer", -1)
				m.nextRequestID++
				m.cancel = nil
				m.responding = false
				m.reflowBody()
				if evicted := m.appendMessage(message{role: "system", text: "Cancelled."}); evicted != "" {
					return m, tea.Println(evicted)
				}
			}
			return m, nil
		case "ctrl+u":
			m.input = nil
			m.cursor = 0
		case "left":
			if m.cursor > 0 {
				m.cursor--
			}
		case "right":
			if m.cursor < len(m.input) {
				m.cursor++
			}
		case "home", "ctrl+a":
			m.cursor = 0
		case "end", "ctrl+e":
			m.cursor = len(m.input)
		case "backspace", "ctrl+h":
			if m.cursor > 0 {
				m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
				m.cursor--
			}
		case "space":
			m.input = insertRunes(m.input, m.cursor, []rune{' '})
			m.cursor++
		case "delete":
			if m.cursor < len(m.input) {
				m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
			}
		default:
			if msg.Text != "" {
				added := []rune(msg.Text)
				m.input = insertRunes(m.input, m.cursor, added)
				m.cursor += len(added)
			}
		}
	}

	return m, nil
}

func insertRunes(input []rune, at int, added []rune) []rune {
	result := make([]rune, 0, len(input)+len(added))
	result = append(result, input[:at]...)
	result = append(result, added...)
	result = append(result, input[at:]...)
	return result
}

func (m model) View() tea.View {
	width := max(m.width, 20)
	composer := m.renderComposer(width)
	pending := m.renderPendingMessages(width)
	info := dimStyle.Render("model " + m.modelName + "  \u00b7  context " + formatTokenCount(m.contextTokens) + " tokens")

	status := ""
	if m.responding {
		activity := " Thinking…"
		if m.pendingSteer != "" {
			activity = " Steering…"
		}
		queueInfo := ""
		if len(m.queued) > 0 {
			queueInfo = " · " + strconv.Itoa(len(m.queued)) + " queued"
		}
		status = "    " + titleStyle.Render(spinnerFrames[m.spinnerFrame]) + dimStyle.Render(activity+queueInfo+" · enter steer · shift+enter queue · esc cancel")
	}

	bottom := []string{composer, info}
	if pending != "" {
		bottom = append([]string{pending}, bottom...)
	}
	if status != "" {
		bottom = append([]string{status}, bottom...)
	}
	body := m.bodyLines
	if !m.started {
		body = []string{"", "    " + dimStyle.Render("No messages yet. Type below to start the conversation.")}
	}
	availBody := m.availableBodyHeight(width)
	if len(body) > availBody {
		body = body[len(body)-availBody:]
	}

	filler := availBody - len(body)
	lines := make([]string, filler)
	lines = append(lines, body...)
	lines = append(lines, bottom...)
	return tea.NewView(strings.Join(lines, "\n"))
}

func formatTokenCount(tokens int64) string {
	digits := strconv.FormatInt(tokens, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func renderMessageLines(msg message, width int) []string {
	contentWidth := max(width-6, 8)
	switch msg.role {
	case "you":
		prefix := "[" + msg.sentAt + "] "
		messageWidth := max(contentWidth-runesWidth([]rune(prefix)), 1)
		var lines []string
		for lineIndex, line := range wrapText(msg.text, messageWidth) {
			linePrefix := strings.Repeat(" ", runesWidth([]rune(prefix)))
			if lineIndex == 0 {
				linePrefix = prefix
			}
			text := linePrefix + line
			bubble := " " + text + strings.Repeat(" ", max(contentWidth-runesWidth([]rune(text)), 0)) + " "
			lines = append(lines, userMessageStyle.Render(bubble))
		}
		return lines
	case "system":
		return []string{"    " + dimStyle.Render(msg.text)}
	case "tool":
		style := toolStyle
		if !strings.HasPrefix(msg.text, "$") {
			style = toolOutputStyle
		}
		var lines []string
		for _, line := range wrapText(msg.text, contentWidth-2) {
			lines = append(lines, "    "+style.Render("│ "+line))
		}
		return lines
	default:
		var lines []string
		for _, line := range wrapText(msg.text, contentWidth) {
			lines = append(lines, "    "+bodyStyle.Render(line))
		}
		return lines
	}
}

func (m *model) removePendingInputs(kind string, limit int) {
	kept := m.pendingInputs[:0]
	removed := 0
	for _, item := range m.pendingInputs {
		if item.kind == kind && (limit < 0 || removed < limit) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	m.pendingInputs = kept
}

func (m model) renderPendingMessages(width int) string {
	var lines []string
	for _, item := range m.pendingInputs {
		style := queuedMessageStyle
		if item.kind == "steer" {
			style = steerMessageStyle
		}
		lines = append(lines, renderPendingMessage(item.kind, item.text, style, width)...)
	}
	return strings.Join(lines, "\n")
}

func renderPendingMessage(label, text string, style lipgloss.Style, width int) []string {
	prefix := "    " + label + "  "
	continuation := strings.Repeat(" ", runesWidth([]rune(prefix)))
	contentWidth := max(width-runesWidth([]rune(prefix))-2, 1)
	wrapped := wrapText(text, contentWidth)
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		linePrefix := continuation
		if i == 0 {
			linePrefix = prefix
		}
		lines = append(lines, style.Render(linePrefix+line))
	}
	return lines
}

func (m model) renderComposer(width int) string {
	innerWidth := max(width-4, 1)
	content := renderInput(m.input, m.cursor, innerWidth)
	return inputStyle.Width(max(width-2, 1)).Render(content)
}

func renderInput(input []rune, cursor, width int) string {
	if len(input) == 0 {
		placeholder := truncatePlainRunes([]rune(" Type a message…"), width-1)
		return cursorStyle.Render(" ") + dimStyle.Render(placeholder)
	}

	start := 0
	for runesWidth(input[start:cursor])+1 > width && start < cursor {
		start++
	}

	visible := input[start:]
	cursorInView := cursor - start
	before := string(visible[:cursorInView])
	cursorRune := " "
	rest := visible[cursorInView:]
	if len(rest) > 0 {
		cursorRune = string(rest[0])
		rest = rest[1:]
	}

	remaining := width - runewidth.StringWidth(before) - runewidth.StringWidth(cursorRune)
	return before + cursorStyle.Render(cursorRune) + truncatePlainRunes(rest, remaining)
}

func wrapText(text string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		remaining := []rune(paragraph)
		for len(remaining) > 0 {
			end := fittingRunes(remaining, width)
			if end < len(remaining) {
				if breakAt := strings.LastIndex(string(remaining[:end]), " "); breakAt > 0 {
					end = utf8.RuneCountInString(string(remaining[:end])[:breakAt])
				}
			}
			lines = append(lines, strings.TrimSpace(string(remaining[:end])))
			remaining = remaining[end:]
			for len(remaining) > 0 && remaining[0] == ' ' {
				remaining = remaining[1:]
			}
		}
	}
	return lines
}

func fittingRunes(input []rune, width int) int {
	used := 0
	for i, r := range input {
		next := max(runewidth.RuneWidth(r), 1)
		if used+next > width {
			return max(i, 1)
		}
		used += next
	}
	return len(input)
}

func runesWidth(input []rune) int {
	return runewidth.StringWidth(string(input))
}

func truncatePlainRunes(input []rune, width int) string {
	if width <= 0 {
		return ""
	}
	return string(input[:fittingRunes(input, width)])
}
