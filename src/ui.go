package main

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
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
	composer      textarea.Model
	messages      []message
	bodyLines     []string
	scrollOffset  int // rendered body lines above the newest viewport
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
)

func newModel(modelName string, respond func(string, func(toolEvent), context.Context) response) model {
	composer := textarea.New()
	composer.Prompt = ""
	composer.Placeholder = "Type a message…"
	composer.ShowLineNumbers = false
	composer.DynamicHeight = true
	composer.MinHeight = 1
	composer.MaxHeight = 20
	composer.SetVirtualCursor(true)
	styles := composer.Styles()
	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Text = bodyStyle
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Focused.Placeholder = dimStyle
	styles.Blurred = styles.Focused
	styles.Cursor.Blink = false
	styles.Cursor.Color = lipgloss.Color("255")
	composer.SetStyles(styles)
	composer.Focus()

	m := model{
		width:     80,
		height:    24,
		composer:  composer,
		modelName: modelName,
		respond:   respond,
	}
	m.resizeComposer()
	return m
}

func (m *model) resizeComposer() {
	width := max(m.width, 20)
	outerWidth := max(width-2, 1)
	innerWidth := max(outerWidth-4, 1) // border and horizontal padding
	m.composer.MaxHeight = max(max(m.height, 8)-5, 1)
	m.composer.SetWidth(innerWidth)
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

func (m *model) appendMessage(msg message) {
	width := max(m.width, 20)
	lines := renderMessageLines(msg, width)
	lines = append(lines, "") // spacing between messages
	m.messages = append(m.messages, msg)
	m.bodyLines = append(m.bodyLines, lines...)

	// Keep the same content under the viewport when new output arrives while
	// the user is reading history. At the bottom, continue following output.
	if m.scrollOffset > 0 {
		m.scrollOffset += len(lines)
	}
	m.clampScrollOffset(width)
}

// reflowBody rebuilds the viewport from message history after a resize, so
// messages outside the current viewport can backfill newly available rows.
func (m *model) reflowBody() {
	width := max(m.width, 20)
	lines := make([]string, 0, len(m.bodyLines))
	for _, msg := range m.messages {
		lines = append(lines, renderMessageLines(msg, width)...)
		lines = append(lines, "")
	}

	m.bodyLines = lines
	m.clampScrollOffset(width)
}

func (m *model) clampScrollOffset(width int) {
	maxOffset := max(len(m.bodyLines)-m.availableBodyHeight(width), 0)
	m.scrollOffset = min(max(m.scrollOffset, 0), maxOffset)
}

func (m *model) scrollBody(delta int) {
	width := max(m.width, 20)
	m.scrollOffset += delta
	m.clampScrollOffset(width)
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

// availablePendingHeight limits queued and steering previews so they can never
// push the composer below the bottom of the terminal. The most recently
// submitted preview lines are retained when there is not enough room.
func (m model) availablePendingHeight(width int) int {
	reservedHeight := 1 + lipgloss.Height(m.renderComposer(width)) // info + composer
	if m.responding {
		reservedHeight++
	}
	return max(max(m.height, 8)-reservedHeight-1, 0) // retain one body row
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

	if showUser {
		m.appendMessage(message{role: "you", text: text, sentAt: time.Now().Format("15:04")})
	}
	return tea.Batch(
		waitForResponse(m.respond, text, m.toolEvents, ctx, m.nextRequestID),
		listenToolEvents(m.toolEvents, m.nextRequestID),
		tickSpinner(),
	)
}

func (m *model) submitInput(queue bool) tea.Cmd {
	text := strings.TrimSpace(m.composer.Value())
	if text == "" {
		return nil
	}
	m.composer.Reset()

	if !m.responding {
		return m.startRequest(text, true)
	}
	if queue {
		m.queued = append(m.queued, text)
		m.pendingInputs = append(m.pendingInputs, pendingInput{kind: "queued", text: text})
		m.reflowBody()
		return nil
	}

	// A steer is a high-priority follow-up, not a hard cancellation. Let the
	// active response finish so its history (including any tool calls) remains
	// complete, then submit the steer before ordinary queued messages. Escape is
	// the explicit hard-cancel control.
	if m.pendingSteer == "" {
		m.pendingSteer = text
	} else {
		m.pendingSteer += "\n\n" + text
	}
	m.pendingInputs = append(m.pendingInputs, pendingInput{kind: "steer", text: text})
	m.reflowBody()
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComposer()
		m.reflowBody()
		return m, nil
	case responseMsg:
		if msg.id != m.nextRequestID {
			return m, nil
		}
		m.cancel = nil
		m.contextTokens = msg.contextTokens
		m.responding = false

		// Preserve the completed response before injecting a steer. This keeps the
		// visible transcript aligned with the agent's internal history.
		if msg.text != "" {
			m.appendMessage(message{role: "agent", text: msg.text})
		}
		if m.pendingSteer != "" {
			text := m.pendingSteer
			m.pendingSteer = ""
			m.removePendingInputs("steer", -1)
			return m, m.startRequest(text, true)
		}

		var cmds []tea.Cmd
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
		m.appendMessage(message{role: "tool", text: text})
		return m, listenToolEvents(msg.ch, msg.requestID)
	case spinnerTickMsg:
		if !m.responding {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, tickSpinner()
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollBody(3)
		case tea.MouseWheelDown:
			m.scrollBody(-3)
		}
		return m, nil
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "ctrl+c":
			return m, tea.Quit
		case "pgup":
			m.scrollBody(max(m.availableBodyHeight(max(m.width, 20))-1, 1))
			return m, nil
		case "pgdown":
			m.scrollBody(-max(m.availableBodyHeight(max(m.width, 20))-1, 1))
			return m, nil
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
				m.appendMessage(message{role: "system", text: "Cancelled."})
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

func (m model) View() tea.View {
	width := max(m.width, 20)
	composer := m.renderComposer(width)
	pending := m.renderPendingMessages(width)
	infoText := "model " + m.modelName + "  ·  context " + formatTokenCount(m.contextTokens) + " tokens"
	// Never occupy the terminal's final column. Terminals may autowrap a
	// full-width line without reporting the extra row to Bubble Tea, which
	// shifts subsequent rows over the composer while the spinner redraws.
	info := dimStyle.Render(truncatePlainRunes([]rune(infoText), max(width-2, 0)))

	status := ""
	if m.responding {
		activity := " Thinking…"
		queueInfo := ""
		if len(m.queued) > 0 {
			queueInfo = " · " + strconv.Itoa(len(m.queued)) + " queued"
		}
		prefix := "    " + titleStyle.Render(spinnerFrames[m.spinnerFrame])
		statusText := activity + queueInfo
		status = prefix + dimStyle.Render(truncatePlainRunes(
			[]rune(statusText), max(width-runesWidth([]rune("    "+spinnerFrames[m.spinnerFrame]))-2, 0),
		))
	}

	bottom := []string{composer, info}
	if pending != "" {
		bottom = append([]string{pending}, bottom...)
	}
	if status != "" {
		bottom = append([]string{status}, bottom...)
	}
	availBody := m.availableBodyHeight(width)
	maxOffset := max(len(m.bodyLines)-availBody, 0)
	offset := min(max(m.scrollOffset, 0), maxOffset)
	end := len(m.bodyLines) - offset
	start := max(end-availBody, 0)
	body := m.bodyLines[start:end]

	filler := availBody - len(body)
	lines := make([]string, filler)
	lines = append(lines, body...)
	lines = append(lines, bottom...)
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
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
	if limit := m.availablePendingHeight(width); len(lines) > limit {
		lines = lines[len(lines)-limit:]
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
	// Keep the composer clear of the terminal's right edge. Some terminals
	// autowrap a line that occupies the final column; frequent spinner redraws
	// can then scroll or overwrite the composer while a response is active.
	outerWidth := max(width-2, 1)
	innerWidth := max(outerWidth-4, 1) // border and horizontal padding
	composer := m.composer
	composer.SetWidth(innerWidth)
	content := composer.View()
	return inputStyle.Width(outerWidth).Render(content)
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
