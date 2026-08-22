package main

import (
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

type model struct {
	width    int
	height   int
	input    []rune
	cursor   int
	messages []message
	agent    agent
}

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
	cursorStyle = lipgloss.NewStyle().Reverse(true)
)

func newModel() model {
	return model{
		width:  80,
		height: 24,
		agent:  newAgent(),
	}
}

func (model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(string(m.input))
			if text != "" {
				m.messages = append(m.messages,
					message{role: "you", text: text, sentAt: time.Now().Format("15:04")},
					message{role: "agent", text: m.agent.respond(text)},
				)
				m.input = nil
				m.cursor = 0
			}
		case tea.KeyEsc, tea.KeyCtrlU:
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
	rule := dimStyle.Render(strings.Repeat("─", width))
	composer := m.renderComposer(width)
	hint := dimStyle.Render("enter send  ·  esc clear  ·  ctrl+c quit")
	logHeight := height - 6

	return strings.Join([]string{
		header,
		rule,
		m.renderLog(width, logHeight),
		composer,
		hint,
	}, "\n")
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

func (m model) renderLog(width, height int) string {
	if len(m.messages) == 0 {
		empty := dimStyle.Render("No messages yet. Type below to start the conversation.")
		return padLines([]string{empty}, height)
	}

	contentWidth := max(width-6, 8)
	lines := make([]string, 0, len(m.messages)*2)
	for i, msg := range m.messages {
		if i > 0 {
			lines = append(lines, "")
		}
		if msg.role == "you" {
			prefix := "[" + msg.sentAt + "] "
			messageWidth := max(contentWidth-runesWidth([]rune(prefix)), 1)
			for lineIndex, line := range wrapText(msg.text, messageWidth) {
				linePrefix := strings.Repeat(" ", runesWidth([]rune(prefix)))
				if lineIndex == 0 {
					linePrefix = prefix
				}
				text := linePrefix + line
				bubble := " " + text + strings.Repeat(" ", max(contentWidth-runesWidth([]rune(text)), 0)) + " "
				lines = append(lines, "  "+userMessageStyle.Render(bubble))
			}
			continue
		}
		for _, line := range wrapText(msg.text, contentWidth) {
			lines = append(lines, "    "+bodyStyle.Render(line))
		}
	}

	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return padLines(lines, height)
}

func padLines(lines []string, height int) string {
	if height <= 0 {
		return ""
	}
	padding := make([]string, max(height-len(lines), 0))
	return strings.Join(append(padding, lines...), "\n")
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
