package main

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

type toolEventMsg toolEvent
type responseMsg response

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
	bodyStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	inputStyle       = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("39")).
				Padding(0, 1)
	toolStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	toolOutputStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	cursorStyle     = lipgloss.NewStyle().Reverse(true)
)

func newModel(modelName string, respond func(string, func(toolEvent), context.Context) response) model {
	return model{
		width:     80,
		modelName: modelName,
		respond:   respond,
	}
}

func (model) Init() tea.Cmd {
	return nil
}

func waitForResponse(respond func(string, func(toolEvent), context.Context) response, input string, emit func(toolEvent), ctx context.Context, id int) tea.Cmd {
	return func() tea.Msg {
		resp := respond(input, emit, ctx)
		resp.id = id
		return responseMsg(resp)
	}
}

func listenToolEvents(ch <-chan toolEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return toolEventMsg(ev)
	}
}

// evictOverflow prints body lines that scrolled out of the frame into the
// terminal's native scrollback, so history survives leaving the viewport.
func (m *model) appendMessage(msg message) (evicted string) {
	width := max(m.width, 20)
	m.messages = append(m.messages, msg)
	m.bodyLines = append(m.bodyLines, renderMessageLines(msg, width)...)

	composerH := lipgloss.Height(m.renderComposer(width))
	statusH := 0
	if m.responding {
		statusH = 1
	}
	bodyHeight := max(max(m.height, 8)-2-statusH-1-composerH, 1)

	if len(m.bodyLines) > bodyHeight {
		evictedCount := len(m.bodyLines) - bodyHeight
		evicted = strings.Join(m.bodyLines[:evictedCount], "\n")
		m.bodyLines = m.bodyLines[evictedCount:]
	}
	return evicted
}

func tickSpinner() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case responseMsg:
		if msg.id != m.nextRequestID || msg.text == "" {
			return m, nil
		}
		m.responding = false
		if evicted := m.appendMessage(message{role: "agent", text: msg.text}); evicted != "" {
			m.contextTokens = msg.contextTokens
			return m, tea.Println(evicted)
		}
		m.contextTokens = msg.contextTokens
	case toolEventMsg:
		text := msg.detail
		if msg.phase == "call" {
			text = "$ " + text
		}
		if evicted := m.appendMessage(message{role: "tool", text: text}); evicted != "" {
			return m, tea.Batch(tea.Println(evicted), listenToolEvents(m.toolEvents))
		}
		return m, listenToolEvents(m.toolEvents)
	case spinnerTickMsg:
		if !m.responding {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, tickSpinner()
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(string(m.input))
			if text != "" && !m.responding {
				m.started = true
				m.input = nil
				m.cursor = 0
				m.responding = true
				m.spinnerFrame = 0
				m.toolEvents = make(chan toolEvent)
				m.nextRequestID++
				ctx, cancel := context.WithCancel(context.Background())
				m.cancel = cancel
				emit := func(ev toolEvent) { m.toolEvents <- ev }
				var cmds []tea.Cmd
				if evicted := m.appendMessage(message{role: "you", text: text, sentAt: time.Now().Format("15:04")}); evicted != "" {
					cmds = append(cmds, tea.Println(evicted))
				}
				cmds = append(cmds, waitForResponse(m.respond, text, emit, ctx, m.nextRequestID), listenToolEvents(m.toolEvents), tickSpinner())
				return m, tea.Batch(cmds...)
			}
		case tea.KeyEsc:
			if m.responding && m.cancel != nil {
				m.cancel()
				m.nextRequestID++
				m.cancel = nil
				m.responding = false
				if evicted := m.appendMessage(message{role: "system", text: "Cancelled."}); evicted != "" {
					return m, tea.Println(evicted)
				}
				return m, nil
			}
			return m, nil
		case tea.KeyCtrlU:
			m.input = nil
			m.cursor = 0
		case tea.KeyLeft:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyRight:
			if m.cursor < len(m.input) {
				m.cursor++
			}
		case tea.KeyHome, tea.KeyCtrlA:
			m.cursor = 0
		case tea.KeyEnd, tea.KeyCtrlE:
			m.cursor = len(m.input)
		case tea.KeyBackspace, tea.KeyCtrlH:
			if m.cursor > 0 {
				m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
				m.cursor--
			}
		case tea.KeyDelete:
			if m.cursor < len(m.input) {
				m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
			}
		case tea.KeySpace:
			m.input = insertRunes(m.input, m.cursor, []rune{' '})
			m.cursor++
		case tea.KeyRunes:
			m.input = insertRunes(m.input, m.cursor, msg.Runes)
			m.cursor += len(msg.Runes)
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

func (m model) View() string {
	width := max(m.width, 20)
	height := max(m.height, 8)

	header := titleStyle.Render("OSH")
	rule := dimStyle.Render(strings.Repeat("\u2500", width))
	composer := m.renderComposer(width)
	info := dimStyle.Render("model " + m.modelName + "  \u00b7  context " + formatTokenCount(m.contextTokens) + " tokens")

	status := ""
	if m.responding {
		status = "    " + titleStyle.Render(spinnerFrames[m.spinnerFrame]) + dimStyle.Render(" Thinking\u2026 (esc to cancel)")
	}

	bottom := []string{composer, info}
	if status != "" {
		bottom = append([]string{status}, bottom...)
	}
	bodyHeight := max(height-2-len(bottom)-lipgloss.Height(composer)+1, 1)

	body := m.bodyLines
	if !m.started {
		body = []string{"", "    " + dimStyle.Render("No messages yet. Type below to start the conversation.")}
	}
	if len(body) > bodyHeight {
		body = body[len(body)-bodyHeight:]
	}

	filler := max(bodyHeight-len(body), 0)
	lines := append([]string{header, rule}, make([]string, filler)...)
	lines = append(lines, body...)
	lines = append(lines, bottom...)
	return strings.Join(lines, "\n")
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
