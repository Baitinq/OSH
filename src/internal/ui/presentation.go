package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	tui "github.com/grindlemire/go-tui"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func (s *oshUI) render(width int, viewportHeight ...int) ([]string, int, int) {
	width = max(width, 10)
	s.setTextareaWidth(max(width-4, 1))
	var lines []string
	for i := range s.messages {
		lines = append(lines, s.renderedMessageLines(&s.messages[i], width)...)
		lines = append(lines, "")
	}
	if s.reasoningText.Len() > 0 {
		lines = append(lines, renderedMessageLines(message{role: "reasoning", text: s.reasoningText.String()}, width)...)
		lines = append(lines, "")
	}
	if s.streamingText.Len() > 0 {
		lines = append(lines, s.renderedStreamingText(width)...)
		lines = append(lines, "")
	}
	if s.responding {
		if s.retryAttempt > 0 {
			remaining := max(time.Until(s.retryDeadline), 0)
			seconds := int((remaining + time.Second - 1) / time.Second)
			status := fmt.Sprintf(" Retrying (%d/%d) in %ds... (Esc to cancel)", s.retryAttempt, s.retryMaxAttempts, seconds)
			status += workingDurationLabel(s.requestStartedAt, time.Now())
			lines = append(lines, ansi256FG(214, spinnerFrames[s.spinnerFrame])+ansi256FG(242, status))
		} else {
			status := " Working…" + workingDurationLabel(s.requestStartedAt, time.Now())
			lines = append(lines, ansi256FG(39, spinnerFrames[s.spinnerFrame])+ansi256FG(242, status))
		}
	}
	for _, p := range s.pendingInputs {
		lines = append(lines, renderPendingLines(p, width)...)
	}
	editor, crow, ccol := renderEditor(s.textarea.Text(), s.textarea.CursorPos(), width)
	cursorRow := len(lines) + crow
	lines = append(lines, editor...)
	info := fmt.Sprintf("%s (%s)  ·  context %s tokens", s.modelName, s.reasoningEffort, formatTokenCount(s.contextTokens))
	lines = append(lines, ansi256FG(242, truncateCells(info, max(width-2, 0))))
	if len(viewportHeight) > 0 {
		filler := max(viewportHeight[0]-len(lines), 0)
		if filler > 0 {
			lines = append(make([]string, filler), lines...)
			cursorRow += filler
		}
	}
	return lines, cursorRow, ccol
}

func (s *oshUI) renderedMessageLines(msg *message, width int) []string {
	now := time.Now()
	duration := ""
	if msg.toolState == "pending" {
		duration = toolDurationLabel(*msg, now)
	}
	if msg.renderedWidth == width && msg.renderedLines != nil && msg.renderedToolDuration == duration {
		return msg.renderedLines
	}
	lines := strings.Split(renderedMessageAt(*msg, width, now), "\n")
	msg.renderedWidth, msg.renderedLines, msg.renderedToolDuration = width, lines, duration
	return lines
}

func renderedMessageLines(msg message, width int) []string {
	return strings.Split(renderedMessage(msg, width), "\n")
}
func renderedMessage(msg message, width int) string {
	return renderedMessageAt(msg, width, time.Now())
}

func renderedMessageAt(msg message, width int, now time.Time) string {
	width = max(width, 10)
	contentWidth := max(width-2, 8)
	var lines []string
	switch msg.role {
	case "you":
		lines = append(lines, piBoxLine("", width, piText, piUserMessageBg, false))
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, piBoxLine(" "+line, width, piText, piUserMessageBg, false))
		}
		lines = append(lines, piBoxLine("", width, piText, piUserMessageBg, false))
	case "reasoning":
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansiRGBStyle(piGray, "", false, true, line))
		}
	case "agent":
		lines = append(lines, renderedMarkdownLines(msg.text, contentWidth)...)
	case "system":
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansi256FG(242, line))
		}
	case "status":
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansi256FG(70, "✓")+ansi256FG(242, " "+line))
		}
	case "error":
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansiRGBStyle(piError, "", false, false, line))
		}
	case "tool":
		return renderedToolMessage(msg, width, now)
	default:
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansiRGBStyle(piText, "", false, false, line))
		}
	}
	return strings.Join(lines, "\n")
}

const (
	piText          = "212;212;212"
	piGray          = "128;128;128"
	piError         = "204;102;102"
	piUserMessageBg = "52;53;65"
	piToolPendingBg = "40;40;50"
	piToolSuccessBg = "40;50;40"
	piToolErrorBg   = "60;40;40"
)

func workingDurationLabel(startedAt, now time.Time) string {
	if startedAt.IsZero() {
		return ""
	}
	elapsed := max(now.Sub(startedAt), 0)
	seconds := int(elapsed / time.Second)
	if seconds < 60 {
		return fmt.Sprintf(" (%ds)", seconds)
	}
	return fmt.Sprintf(" (%dm %02ds)", seconds/60, seconds%60)
}

func completedRequestMessage(startedAt, finishedAt time.Time) string {
	if startedAt.IsZero() {
		return ""
	}
	return "Done in " + formatToolDuration(finishedAt.Sub(startedAt)) + " at " + finishedAt.Format("15:04")
}

func formatToolDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Round(time.Millisecond).Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d / time.Minute)
	seconds := int(d/time.Second) % 60
	return fmt.Sprintf("%dm %02ds", minutes, seconds)
}

func toolDurationLabel(msg message, now time.Time) string {
	if msg.toolStartedAt.IsZero() {
		return ""
	}
	end := msg.toolFinishedAt
	if end.IsZero() {
		elapsed := max(now.Sub(msg.toolStartedAt), 0).Truncate(100 * time.Millisecond)
		return " (" + formatToolDuration(elapsed) + ")"
	}
	return " (" + formatToolDuration(end.Sub(msg.toolStartedAt)) + ")"
}

func renderedMarkdownLines(text string, width int) []string {
	style := styles.ASCIIStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	style.Document.Color = stringPointer("#D4D4D4")
	style.Heading.Color = stringPointer("#F0C674")
	style.Heading.Bold = boolPointer(true)
	style.H1.Prefix, style.H2.Prefix = "", ""
	style.H1.Underline = boolPointer(true)
	style.Strikethrough.BlockPrefix, style.Strikethrough.BlockSuffix = "", ""
	style.Strikethrough.CrossedOut = boolPointer(true)
	style.Emph.BlockPrefix, style.Emph.BlockSuffix = "", ""
	style.Emph.Italic = boolPointer(true)
	style.Strong.BlockPrefix, style.Strong.BlockSuffix = "", ""
	style.Strong.Bold = boolPointer(true)
	style.Code.Color = stringPointer("#8ABEB7")
	style.Code.BlockPrefix, style.Code.BlockSuffix = "", ""
	style.Link.Color = stringPointer("#666666")
	style.LinkText.Color = stringPointer("#81A2BE")
	style.LinkText.Underline = boolPointer(true)
	style.List.Color = stringPointer("#8ABEB7")
	style.BlockQuote.Color = stringPointer("#808080")
	style.BlockQuote.Italic = boolPointer(true)
	style.HorizontalRule.Color = stringPointer("#808080")
	style.CodeBlock.Margin = &zero
	style.CodeBlock.Color = stringPointer("#B5BD68")
	style.CodeBlock.Chroma = styles.DarkStyleConfig.CodeBlock.Chroma
	style.CodeBlock.Chroma.Text.Color = stringPointer("#B5BD68")
	style.CodeBlock.Chroma.Comment.Color = stringPointer("#6A9955")
	style.CodeBlock.Chroma.Keyword.Color = stringPointer("#569CD6")
	style.CodeBlock.Chroma.NameFunction.Color = stringPointer("#DCDCAA")
	style.CodeBlock.Chroma.Name.Color = stringPointer("#9CDCFE")
	style.CodeBlock.Chroma.LiteralString.Color = stringPointer("#CE9178")
	style.CodeBlock.Chroma.LiteralNumber.Color = stringPointer("#B5CEA8")
	style.CodeBlock.Chroma.KeywordType.Color = stringPointer("#4EC9B0")
	style.CodeBlock.Chroma.Operator.Color = stringPointer("#D4D4D4")
	style.CodeBlock.Chroma.Punctuation.Color = stringPointer("#D4D4D4")
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(max(width-1, 1)),
		glamour.WithPreservedNewLines(),
		glamour.WithChromaFormatter("terminal16m"),
	)
	if err != nil {
		panic(err)
	}
	rendered, err := renderer.Render(escapeMarkdownHTML(text))
	if err != nil {
		panic(err)
	}
	rendered = strings.Trim(rendered, "\n")
	if rendered == "" {
		return nil
	}
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = " " + trimMarkdownPadding(line)
	}
	return lines
}

func trimMarkdownPadding(line string) string {
	end := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			i = ansiSequenceEnd(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		if !unicode.IsSpace(r) {
			end = i
		}
	}
	if end == 0 {
		return ""
	}
	return line[:end] + "\x1b[0m\x1b]8;;\x1b\\"
}

func stringPointer(s string) *string { return &s }
func boolPointer(b bool) *bool       { return &b }

func escapeMarkdownHTML(source string) string {
	marked := make([]bool, len(source))
	mark := func(segments *text.Segments) {
		for i := range segments.Len() {
			segment := segments.At(i)
			for j := segment.Start; j < segment.Stop; j++ {
				marked[j] = true
			}
		}
	}
	document := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader([]byte(source)))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.RawHTML:
			mark(node.Segments)
		case *ast.HTMLBlock:
			mark(node.Lines())
			if node.HasClosure() {
				for i := node.ClosureLine.Start; i < node.ClosureLine.Stop; i++ {
					marked[i] = true
				}
			}
		}
		return ast.WalkContinue, nil
	})

	var escaped strings.Builder
	for i := 0; i < len(source); i++ {
		if marked[i] && source[i] == '<' {
			escaped.WriteString("&lt;")
		} else if marked[i] && source[i] == '>' {
			escaped.WriteString("&gt;")
		} else {
			escaped.WriteByte(source[i])
		}
	}
	return escaped.String()
}

func renderedToolMessage(msg message, width int, now time.Time) string {
	bg := piToolPendingBg
	if msg.toolState == "success" {
		bg = piToolSuccessBg
	} else if msg.toolState == "error" {
		bg = piToolErrorBg
	}
	inner := max(width-2, 1)
	lines := []string{piBoxLine("", width, piText, bg, false)}
	duration := toolDurationLabel(msg, now)
	if msg.toolCommand != "" {
		if duration != "" {
			// Keep timing metadata on its own line above the command so it cannot
			// be mistaken for part of a long or wrapped invocation.
			lines = append(lines, piBoxLine(" "+strings.TrimSpace(duration), width, piText, bg, true))
		}
		command := "$ " + sanitizeTerminalText(msg.toolCommand)
		if msg.toolName == "web_search" {
			command = `web_search "` + sanitizeTerminalText(msg.toolCommand) + `"`
		} else if msg.toolName == "repl" {
			command = ">>> " + sanitizeTerminalText(msg.toolCommand)
		}
		for _, line := range wrapPlain(command, inner) {
			lines = append(lines, piBoxLine(" "+line, width, piText, bg, true))
		}
	} else if msg.toolName != "" {
		lines = append(lines, piBoxLine(" "+sanitizeTerminalText(msg.toolName)+duration, width, piText, bg, true))
	}
	if msg.toolResult != "" {
		outputLines := wrapPlain(strings.TrimSuffix(sanitizeTerminalText(msg.toolResult), "\n"), inner)
		skipped := max(len(outputLines)-toolPreviewLines, 0)
		if skipped > 0 {
			outputLines = outputLines[skipped:]
		}
		lines = append(lines, piBoxLine("", width, piGray, bg, false))
		if skipped > 0 {
			hint := fmt.Sprintf(" ... (%d earlier lines)", skipped)
			lines = append(lines, piBoxLine(hint, width, piGray, bg, false))
		}
		for _, line := range outputLines {
			lines = append(lines, piBoxLine(" "+line, width, piGray, bg, false))
		}
	}
	lines = append(lines, piBoxLine("", width, piText, bg, false))
	return strings.Join(lines, "\n")
}

func piBoxLine(text string, width int, fg, bg string, bold bool) string {
	text += strings.Repeat(" ", max(width-lineWidth(text), 0))
	return ansiRGBStyle(fg, bg, bold, false, text)
}

func renderPendingLines(p pendingInput, width int) []string {
	label := "Queued:"
	if p.kind == "steer" {
		label = "Steering:"
	}
	prefix := label + " "
	avail := max(width-lineWidth(prefix)-2, 1)
	wrapped := wrapPlain(strings.ReplaceAll(p.text, "\n", " "), avail)
	out := make([]string, len(wrapped))
	for i, l := range wrapped {
		q := strings.Repeat(" ", lineWidth(prefix))
		if i == 0 {
			q = prefix
		}
		out[i] = ansi256FG(245, q+l)
	}
	return out
}

func renderEditor(text string, cursor, width int) ([]string, int, int) {
	inner := max(width-4, 1)
	rs := []rune(text)
	cursor = clusterToRuneIndex(text, cursor)
	var rows []string
	var curRow, curCol int
	start := 0
	paragraphs := strings.Split(text, "\n")
	runeBase := 0
	for pi, p := range paragraphs {
		pr := []rune(p)
		if len(pr) == 0 {
			rows = append(rows, "")
			if cursor == runeBase {
				curRow = len(rows) - 1
				curCol = 0
			}
		} else {
			for len(pr) > 0 {
				n := fittingRunes(pr, inner)
				chunk := string(pr[:n])
				rowStart := runeBase + start
				rowEnd := rowStart + n
				if cursor >= rowStart && cursor <= rowEnd {
					curRow = len(rows)
					curCol = lineWidth(string(rs[rowStart:cursor]))
				}
				rows = append(rows, chunk)
				pr = pr[n:]
				start += n
			}
		}
		runeBase += len([]rune(p))
		start = 0
		if pi < len(paragraphs)-1 {
			runeBase++
		}
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	top := "╭" + strings.Repeat("─", max(width-2, 1)) + "╮"
	bottom := "╰" + strings.Repeat("─", max(width-2, 1)) + "╯"
	out := []string{ansi256FG(39, top)}
	for _, row := range rows {
		shown, color := row, 252
		if text == "" && row == "" {
			shown, color = truncateCells("Type a message…", inner), 242
		}
		out = append(out, ansi256FG(39, "│")+" "+ansi256FG(color, shown)+strings.Repeat(" ", max(inner-lineWidth(shown), 0))+" "+ansi256FG(39, "│"))
	}
	out = append(out, ansi256FG(39, bottom))
	return out, curRow + 1, curCol + 2
}

func wrapPlain(text string, width int) []string {
	width = max(width, 1)
	var out []string
	for _, p := range strings.Split(text, "\n") {
		if p == "" {
			out = append(out, "")
			continue
		}
		r := []rune(p)
		for len(r) > 0 {
			n := fittingRunes(r, width)
			if n < len(r) {
				for i := n; i > 0; i-- {
					if unicode.IsSpace(r[i-1]) {
						n = i - 1
						break
					}
				}
				if n == 0 {
					n = fittingRunes(r, width)
				}
			}
			out = append(out, strings.TrimSpace(string(r[:n])))
			r = r[n:]
			for len(r) > 0 && unicode.IsSpace(r[0]) {
				r = r[1:]
			}
		}
	}
	return out
}
func fittingRunes(r []rune, width int) int {
	if len(r) == 0 {
		return 0
	}
	text, used, consumed := string(r), 0, 0
	for len(text) > 0 {
		_, w, size, runes := tui.NextClusterRunes(text)
		if size == 0 {
			break
		}
		if used+w > width {
			if consumed == 0 {
				return runes
			}
			return consumed
		}
		used += w
		consumed += runes
		text = text[size:]
	}
	return consumed
}

func clusterToRuneIndex(text string, clusterPos int) int {
	if clusterPos <= 0 {
		return 0
	}
	clusters, runes := 0, 0
	for len(text) > 0 && clusters < clusterPos {
		_, _, size, n := tui.NextClusterRunes(text)
		if size == 0 {
			break
		}
		text = text[size:]
		runes += n
		clusters++
	}
	return runes
}

func runeToClusterIndex(text string, runePos int) int {
	if runePos <= 0 {
		return 0
	}
	clusters, runes := 0, 0
	for len(text) > 0 && runes < runePos {
		_, _, size, n := tui.NextClusterRunes(text)
		if size == 0 {
			break
		}
		text = text[size:]
		runes += n
		clusters++
	}
	return clusters
}

func truncateCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	return string(r[:fittingRunes(r, width)])
}
func ansi256FG(c int, text string) string {
	return fmt.Sprintf("\x1b[0m\x1b[38;5;%dm%s\x1b[0m", c, text)
}
func ansiRGBStyle(fg, bg string, bold, italic bool, text string) string {
	var codes []string
	if bold {
		codes = append(codes, "1")
	}
	if italic {
		codes = append(codes, "3")
	}
	if fg != "" {
		codes = append(codes, "38;2;"+fg)
	}
	if bg != "" {
		codes = append(codes, "48;2;"+bg)
	}
	return "\x1b[0m\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}
func formatTokenCount(n int64) string {
	d := fmt.Sprintf("%d", n)
	for i := len(d) - 3; i > 0; i -= 3 {
		d = d[:i] + "," + d[i:]
	}
	return d
}
