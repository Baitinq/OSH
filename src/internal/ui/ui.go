package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	tui "github.com/grindlemire/go-tui"

	"osh/internal/agent"
)

type message struct{ role, text, sentAt string }
type response struct {
	id            int
	Text          string
	ContextTokens int64
	Err           error
}
type toolEvent = agent.ToolEvent
type pendingInput struct{ kind, text string }

type oshUI struct {
	modelName       string
	reasoningEffort string
	contextTokens   int64
	respond         func(string, func(toolEvent), context.Context) response
	textarea        *tui.TextArea
	textareaWidth   int
	messages        []message
	streamingText   string
	responding      bool
	spinnerFrame    int
	queued          []string
	pendingSteer    string
	pendingInputs   []pendingInput
	nextRequestID   int
	cancel          context.CancelFunc
	dispatch        func(func())
	invalidate      func()
	emit            func(message)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 80 * time.Millisecond

func newUI(modelName, reasoningEffort string, respond func(string, func(toolEvent), context.Context) response) *oshUI {
	s := &oshUI{modelName: modelName, reasoningEffort: reasoningEffort, respond: respond}
	s.ensureTextarea()
	s.emit = func(message) {}
	return s
}

func (s *oshUI) ensureTextarea() {
	s.setTextareaWidth(76)
}

func (s *oshUI) setTextareaWidth(width int) {
	width = max(width, 1)
	if s.textarea != nil && s.textareaWidth == width {
		return
	}
	text, cursor := "", 0
	if s.textarea != nil {
		text, cursor = s.textarea.Text(), s.textarea.CursorPos()
	}
	s.textarea = tui.NewTextArea(tui.WithTextAreaWidth(width), tui.WithTextAreaAutoFocus(true))
	s.textarea.SetText(text)
	s.textarea.SetCursorPos(cursor)
	s.textareaWidth = width
}

func (s *oshUI) submitInput(text string, queue bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.textarea.Clear()
	if !s.responding {
		s.startRequest(text, true)
		return
	}
	if queue {
		s.queued = append(s.queued, text)
		s.pendingInputs = append(s.pendingInputs, pendingInput{"queued", text})
		s.markDirty()
		return
	}
	if s.pendingSteer == "" {
		s.pendingSteer = text
	} else {
		s.pendingSteer += "\n\n" + text
	}
	s.pendingInputs = append(s.pendingInputs, pendingInput{"steer", text})
	s.markDirty()
}

func (s *oshUI) startRequest(text string, showUser bool) {
	if s.respond == nil || s.dispatch == nil {
		return
	}
	s.nextRequestID++
	id := s.nextRequestID
	ctx, cancel := context.WithCancel(context.Background())
	s.responding, s.spinnerFrame, s.cancel, s.streamingText = true, 0, cancel, ""
	if showUser {
		s.addMessage(message{"you", text, time.Now().Format("15:04")})
	}
	s.markDirty()
	finished := make(chan struct{})
	go s.spin(ctx, finished, id)
	go func() {
		emit := func(ev toolEvent) { s.dispatch(func() { s.handleToolEvent(id, ev) }) }
		resp := s.respond(text, emit, ctx)
		resp.id = id
		close(finished)
		s.dispatch(func() { s.finishResponse(resp) })
	}()
}

func (s *oshUI) spin(ctx context.Context, finished <-chan struct{}, id int) {
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-finished:
			return
		case <-t.C:
			s.dispatch(func() {
				if s.responding && s.nextRequestID == id {
					s.spinnerFrame = (s.spinnerFrame + 1) % len(spinnerFrames)
					s.markDirty()
				}
			})
		}
	}
}

func (s *oshUI) handleToolEvent(id int, ev toolEvent) {
	if id != s.nextRequestID || !s.responding {
		return
	}
	if ev.Phase == "text_reset" {
		s.streamingText = ""
		s.markDirty()
		return
	}
	if ev.Phase == "text_delta" {
		s.streamingText += ev.Detail
		s.markDirty()
		return
	}
	text, role := ev.Detail, "tool"
	if ev.Phase == "call" {
		text = "$ " + text
	} else if ev.Phase == "error" {
		role = "error"
	}
	s.addMessage(message{role: role, text: text})
}

func (s *oshUI) finishResponse(resp response) {
	if resp.id != s.nextRequestID {
		return
	}
	s.cancel, s.responding = nil, false
	if resp.Err == nil {
		s.contextTokens = resp.ContextTokens
	}
	if resp.Err != nil {
		s.addMessage(message{role: "error", text: resp.Err.Error()})
	}
	if resp.Text == "" {
		resp.Text = s.streamingText
	}
	s.streamingText = ""
	if resp.Text != "" {
		s.addMessage(message{role: "agent", text: resp.Text})
	}
	if s.pendingSteer != "" {
		text := s.pendingSteer
		s.pendingSteer = ""
		s.removePendingInputs("steer", -1)
		s.startRequest(text, true)
		return
	}
	if len(s.queued) > 0 {
		text := s.queued[0]
		s.queued = s.queued[1:]
		s.removePendingInputs("queued", 1)
		s.startRequest(text, true)
		return
	}
	s.markDirty()
}

func (s *oshUI) cancelRequest() {
	if !s.responding || s.cancel == nil {
		return
	}
	s.cancel()
	s.pendingSteer = ""
	s.removePendingInputs("steer", -1)
	s.nextRequestID++
	s.cancel = nil
	s.responding = false
	s.streamingText = ""
	s.addMessage(message{role: "system", text: "Cancelled."})
	s.markDirty()
}

func (s *oshUI) addMessage(msg message) {
	s.messages = append(s.messages, msg)
	if s.emit != nil {
		s.emit(msg)
	}
	s.markDirty()
}
func (s *oshUI) markDirty() {
	if s.invalidate != nil {
		s.invalidate()
	}
}

func (s *oshUI) removePendingInputs(kind string, limit int) {
	kept := s.pendingInputs[:0]
	removed := 0
	for _, item := range s.pendingInputs {
		if item.kind == kind && (limit < 0 || removed < limit) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	s.pendingInputs = kept
}

func (s *oshUI) moveWord(direction int) {
	raw := s.textarea.Text()
	text := []rune(raw)
	pos := clusterToRuneIndex(raw, s.textarea.CursorPos())
	if direction < 0 {
		for pos > 0 && unicode.IsSpace(text[pos-1]) {
			pos--
		}
		for pos > 0 && !unicode.IsSpace(text[pos-1]) {
			pos--
		}
	} else {
		for pos < len(text) && !unicode.IsSpace(text[pos]) {
			pos++
		}
		for pos < len(text) && unicode.IsSpace(text[pos]) {
			pos++
		}
	}
	s.textarea.SetCursorPos(pos)
	s.markDirty()
}
func (s *oshUI) deleteWord(direction int) {
	raw := s.textarea.Text()
	text := []rune(raw)
	start := clusterToRuneIndex(raw, s.textarea.CursorPos())
	end := start
	if direction < 0 {
		for start > 0 && unicode.IsSpace(text[start-1]) {
			start--
		}
		for start > 0 && !unicode.IsSpace(text[start-1]) {
			start--
		}
	} else {
		for end < len(text) && unicode.IsSpace(text[end]) {
			end++
		}
		for end < len(text) && !unicode.IsSpace(text[end]) {
			end++
		}
	}
	s.replaceTextRange(text, start, end)
}
func (s *oshUI) deleteToLineStart() {
	raw := s.textarea.Text()
	text := []rune(raw)
	end := clusterToRuneIndex(raw, s.textarea.CursorPos())
	start := end
	for start > 0 && text[start-1] != '\n' {
		start--
	}
	s.replaceTextRange(text, start, end)
}
func (s *oshUI) deleteToLineEnd() {
	raw := s.textarea.Text()
	text := []rune(raw)
	start := clusterToRuneIndex(raw, s.textarea.CursorPos())
	end := start
	for end < len(text) && text[end] != '\n' {
		end++
	}
	s.replaceTextRange(text, start, end)
}
func (s *oshUI) replaceTextRange(text []rune, start, end int) {
	next := string(append(append([]rune{}, text[:start]...), text[end:]...))
	s.textarea.SetText(next)
	s.textarea.SetCursorPos(runeToClusterIndex(next, start))
	s.markDirty()
}

func (s *oshUI) handleKey(k tui.KeyEvent) bool {
	if k.Key == tui.KeyRune && k.Rune == 'c' && k.Mod.Has(tui.ModCtrl) {
		if s.cancel != nil {
			s.cancel()
		}
		return false
	}
	if k.Key == tui.KeyEscape {
		s.cancelRequest()
		return true
	}
	if k.Key == tui.KeyEnter && k.Mod.Has(tui.ModShift) {
		s.submitInput(s.textarea.Text(), true)
		return true
	}
	if k.Key == tui.KeyEnter && k.Mod == tui.ModNone {
		s.submitInput(s.textarea.Text(), false)
		return true
	}
	if (k.Mod.Has(tui.ModAlt) || k.Mod.Has(tui.ModCtrl)) && k.Key == tui.KeyLeft || k.Mod.Has(tui.ModAlt) && k.Key == tui.KeyRune && k.Rune == 'b' {
		s.moveWord(-1)
		return true
	}
	if (k.Mod.Has(tui.ModAlt) || k.Mod.Has(tui.ModCtrl)) && k.Key == tui.KeyRight || k.Mod.Has(tui.ModAlt) && k.Key == tui.KeyRune && k.Rune == 'f' {
		s.moveWord(1)
		return true
	}
	if k.Key == tui.KeyRune && k.Rune == 'w' && k.Mod.Has(tui.ModCtrl) {
		s.deleteWord(-1)
		return true
	}
	if k.Key == tui.KeyRune && k.Rune == 'd' && k.Mod.Has(tui.ModAlt) {
		s.deleteWord(1)
		return true
	}
	if k.Key == tui.KeyRune && k.Rune == 'u' && k.Mod.Has(tui.ModCtrl) {
		s.deleteToLineStart()
		return true
	}
	if k.Key == tui.KeyRune && k.Rune == 'k' && k.Mod.Has(tui.ModCtrl) {
		s.deleteToLineEnd()
		return true
	}
	s.textarea.HandleEvent(k)
	s.markDirty()
	return true
}

func (s *oshUI) render(width int, viewportHeight ...int) ([]string, int, int) {
	width = max(width, 10)
	s.setTextareaWidth(max(width-4, 1))
	var lines []string
	for _, msg := range s.messages {
		lines = append(lines, renderedMessageLines(msg, width)...)
		lines = append(lines, "")
	}
	if s.streamingText != "" {
		lines = append(lines, renderedMessageLines(message{role: "agent", text: s.streamingText}, width)...)
		lines = append(lines, "")
	}
	if s.responding {
		lines = append(lines, "    "+ansi256FG(39, spinnerFrames[s.spinnerFrame])+ansi256FG(242, " Thinking…"))
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

func renderedMessageLines(msg message, width int) []string {
	return strings.Split(renderedMessage(msg, width), "\n")
}
func renderedMessage(msg message, width int) string {
	contentWidth := max(width-6, 8)
	var lines []string
	switch msg.role {
	case "you":
		prefix := "[" + msg.sentAt + "] "
		mw := max(contentWidth-lineWidth(prefix), 1)
		for i, line := range wrapPlain(msg.text, mw) {
			p := strings.Repeat(" ", lineWidth(prefix))
			if i == 0 {
				p = prefix
			}
			text := p + line
			text += strings.Repeat(" ", max(contentWidth-lineWidth(text), 0))
			lines = append(lines, ansi256FGBG(255, 236, " "+text+" "))
		}
	case "system":
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, "    "+ansi256FG(242, line))
		}
	case "error":
		for _, line := range wrapPlain(msg.text, contentWidth-2) {
			lines = append(lines, "    "+ansi256FGBG(255, 160, " "+line+" "))
		}
	case "tool":
		color := 247
		if strings.HasPrefix(msg.text, "$") {
			color = 71
		}
		for _, line := range wrapPlain(msg.text, contentWidth-2) {
			lines = append(lines, "    "+ansi256FG(color, "│ "+line))
		}
	default:
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, "    "+ansi256FG(252, line))
		}
	}
	return strings.Join(lines, "\n")
}
func renderPendingLines(p pendingInput, width int) []string {
	prefix := "    " + p.kind + "  "
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
func ansi256FGBG(f, b int, text string) string {
	return fmt.Sprintf("\x1b[0m\x1b[38;5;%d;48;5;%dm%s\x1b[0m", f, b, text)
}
func formatTokenCount(n int64) string {
	d := fmt.Sprintf("%d", n)
	for i := len(d) - 3; i > 0; i -= 3 {
		d = d[:i] + "," + d[i:]
	}
	return d
}

func Run(modelName, reasoningEffort string, respond func(string, func(agent.ToolEvent), context.Context) agent.Response) error {
	term, err := tui.NewANSITerminal(os.Stdout, os.Stdin)
	if err != nil {
		return err
	}
	if err = term.EnterRawMode(); err != nil {
		return err
	}
	defer term.ExitRawMode()
	// Request xterm extended-key mode as well as Kitty keyboard mode. tmux
	// recognizes the former and, when configured for CSI-u output, forwards
	// Shift+Enter in the same form understood by go-tui. Unsupported terminals
	// ignore both sequences.
	if _, err := io.WriteString(os.Stdout, "\x1b[>4;1m"); err != nil {
		return err
	}
	defer io.WriteString(os.Stdout, "\x1b[>4m")

	// Some multiplexers accept the Kitty keyboard push sequence but do not
	// answer the capability query. In that case negotiation disables the mode
	// again, so restore it with a direct push.
	if !term.NegotiateKittyKeyboard() {
		term.EnableKittyKeyboard()
	}
	defer term.DisableKittyKeyboard()
	term.HideCursor()
	defer term.ShowCursor()
	reader, err := tui.NewEventReader(os.Stdin)
	if err != nil {
		return err
	}
	defer reader.Close()
	updates := make(chan func(), 256)
	wake := make(chan struct{}, 1)
	root := newUI(modelName, reasoningEffort, func(input string, emit func(toolEvent), ctx context.Context) response {
		result := respond(input, emit, ctx)
		return response{Text: result.Text, ContextTokens: result.ContextTokens, Err: result.Err}
	})
	root.dispatch = func(fn func()) { updates <- fn }
	root.invalidate = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	w, h := term.Size()
	renderer := newMainScreenRenderer(os.Stdout, w, h)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	defer func() {
		if root.cancel != nil {
			root.cancel()
		}
		_ = renderer.stop()
	}()
	for {
		lines, cr, cc := root.render(w, h)
		if err := renderer.render(lines, cr, cc); err != nil {
			return err
		}
		select {
		case fn := <-updates:
			fn()
		case <-wake:
		case <-resize:
			w, h = term.Size()
			renderer.resize(w, h)
		default:
			ev, ok := reader.PollEvent(50 * time.Millisecond)
			if !ok {
				continue
			}
			if k, ok := ev.(tui.KeyEvent); ok && !root.handleKey(k) {
				return nil
			}
		}
	}
}
