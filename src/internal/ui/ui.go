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

type message struct {
	role, text                    string
	toolID, toolName, toolCommand string
	toolResult, toolState         string
	toolStartedAt, toolFinishedAt time.Time
}
type response struct {
	id            int
	Text          string
	ContextTokens int64
	Err           error
}
type toolEvent = agent.ToolEvent
type pendingInput struct{ kind, text string }

type oshUI struct {
	modelName           string
	reasoningEffort     string
	contextTokens       int64
	respond             func(string, <-chan string, func(toolEvent), context.Context) response
	textarea            *tui.TextArea
	textareaWidth       int
	messages            []message
	streamingText       string
	reasoningText       string
	responding          bool
	spinnerFrame        int
	retryAttempt        int
	retryMaxAttempts    int
	retryDeadline       time.Time
	requestStartedAt    time.Time
	retryMessageIndex   int
	attemptMessageStart int
	queued              []string
	pendingSteer        []string
	steer               chan string
	pendingInputs       []pendingInput
	inputHistory        []string
	historyIndex        int
	historyDraft        string
	nextRequestID       int
	lastCtrlC           time.Time
	cancel              context.CancelFunc
	dispatch            func(func())
	invalidate          func()
	emit                func(message)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	spinnerInterval          = 80 * time.Millisecond
	ctrlCDoublePressInterval = time.Second
	// Background streams can produce many updates per second. Rendering each
	// one is especially expensive for growing tool cards because the card pushes
	// the spinner and editor down. Ten frames per second still looks live while
	// substantially reducing terminal rewrites; direct input and resize events
	// bypass this limit below.
	backgroundRenderInterval = 100 * time.Millisecond
	eventPollInterval        = 10 * time.Millisecond
	maxUpdatesPerIteration   = 64
	toolPreviewLines         = 5
	maxToolDisplayBytes      = 50 * 1024
)

func newUI(modelName, reasoningEffort string, respond func(string, <-chan string, func(toolEvent), context.Context) response) *oshUI {
	s := &oshUI{
		modelName: modelName, reasoningEffort: reasoningEffort, respond: respond,
		historyIndex: -1, retryMessageIndex: -1,
	}
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
	s.inputHistory = append(s.inputHistory, text)
	s.historyIndex, s.historyDraft = -1, ""
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
	s.pendingSteer = append(s.pendingSteer, text)
	s.pendingInputs = append(s.pendingInputs, pendingInput{"steer", text})
	if s.steer != nil {
		select {
		case s.steer <- text:
		default:
		}
	}
	s.markDirty()
}

func (s *oshUI) startRequest(text string, showUser bool) {
	if s.respond == nil || s.dispatch == nil {
		return
	}
	s.nextRequestID++
	id := s.nextRequestID
	ctx, cancel := context.WithCancel(context.Background())
	steer := make(chan string, 256)
	s.responding, s.spinnerFrame, s.cancel, s.streamingText, s.reasoningText = true, 0, cancel, "", ""
	s.requestStartedAt = time.Now()
	s.retryAttempt, s.retryMaxAttempts, s.retryDeadline = 0, 0, time.Time{}
	s.retryMessageIndex = -1
	s.steer = steer
	if showUser {
		s.addMessage(message{role: "you", text: text})
	}
	s.markDirty()
	finished := make(chan struct{})
	go s.spin(ctx, finished, id)
	go func() {
		emit := func(ev toolEvent) { s.dispatch(func() { s.handleToolEvent(id, ev) }) }
		resp := s.respond(text, steer, emit, ctx)
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
	switch ev.Phase {
	case "attempt_failed":
		s.reasoningText, s.streamingText = "", ""
		if s.attemptMessageStart <= len(s.messages) {
			s.messages = s.messages[:s.attemptMessageStart]
		}
		s.markDirty()
		return
	case "retry":
		// A failed stream may have emitted partial reasoning, text, or tool
		// calls. Drop that partial attempt, but keep one persistent error card
		// and update it with the latest failure on every retry.
		s.reasoningText, s.streamingText = "", ""
		if s.attemptMessageStart <= len(s.messages) {
			s.messages = s.messages[:s.attemptMessageStart]
		}
		title := fmt.Sprintf("LLM error · retry %d/%d", ev.Attempt, ev.MaxAttempts)
		if s.retryMessageIndex >= 0 && s.retryMessageIndex < len(s.messages) {
			msg := &s.messages[s.retryMessageIndex]
			msg.toolName, msg.toolResult = title, ev.Detail
			msg.toolFinishedAt = time.Now()
		} else {
			s.retryMessageIndex = len(s.messages)
			s.addMessage(message{
				role: "tool", toolID: "llm-retry", toolName: title,
				toolResult: ev.Detail, toolState: "error", toolFinishedAt: time.Now(),
			})
		}
		s.retryAttempt, s.retryMaxAttempts = ev.Attempt, ev.MaxAttempts
		s.retryDeadline = time.Now().Add(ev.Delay)
		s.markDirty()
		return
	case "retry_done":
		s.retryAttempt, s.retryMaxAttempts, s.retryDeadline = 0, 0, time.Time{}
		s.markDirty()
		return
	case "text_reset":
		// The backoff has ended and a fresh request is now active.
		s.retryAttempt, s.retryMaxAttempts, s.retryDeadline = 0, 0, time.Time{}
		s.finishReasoning()
		s.finishStreamingText()
		s.attemptMessageStart = len(s.messages)
		return
	case "reasoning_delta":
		s.finishStreamingText()
		s.reasoningText += ev.Detail
		s.markDirty()
		return
	case "reasoning_done":
		s.finishReasoning()
		return
	case "steer_consumed":
		s.finishReasoning()
		s.finishStreamingText()
		s.consumePendingSteer(ev.Detail)
		s.addMessage(message{role: "you", text: ev.Detail})
		return
	case "text_delta":
		s.finishReasoning()
		s.streamingText += ev.Detail
		s.markDirty()
		return
	}
	s.finishReasoning()
	s.finishStreamingText()
	s.handleToolActivity(ev)
}

func trimToolDisplayTail(output string) string {
	if len(output) <= maxToolDisplayBytes {
		return output
	}
	start := len(output) - maxToolDisplayBytes
	for start < len(output) && output[start]&0xc0 == 0x80 {
		start++
	}
	return output[start:]
}

func (s *oshUI) handleToolActivity(ev toolEvent) {
	if ev.Phase == "call" {
		s.addMessage(message{
			role: "tool", toolID: ev.ID, toolName: ev.Name,
			toolCommand: ev.Detail, toolState: "pending", toolStartedAt: time.Now(),
		})
		return
	}
	if ev.Phase != "update" && ev.Phase != "result" && ev.Phase != "error" {
		return
	}
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := &s.messages[i]
		if msg.role != "tool" || msg.toolState != "pending" {
			continue
		}
		if ev.ID != "" && msg.toolID != ev.ID {
			continue
		}
		if ev.Phase == "update" {
			msg.toolResult = trimToolDisplayTail(msg.toolResult + ev.Detail)
		} else {
			msg.toolResult = ev.Detail
			msg.toolFinishedAt = time.Now()
			if ev.Phase == "error" {
				msg.toolState = "error"
			} else {
				msg.toolState = "success"
			}
		}
		s.markDirty()
		return
	}
	state := "success"
	if ev.Phase == "error" {
		state = "error"
	} else if ev.Phase == "update" {
		state = "pending"
	}
	msg := message{
		role: "tool", toolID: ev.ID, toolName: ev.Name,
		toolResult: ev.Detail, toolState: state,
	}
	if state == "pending" {
		msg.toolStartedAt = time.Now()
	}
	s.addMessage(msg)
}

func (s *oshUI) finishReasoning() {
	if s.reasoningText == "" {
		return
	}
	text := s.reasoningText
	s.reasoningText = ""
	s.addMessage(message{role: "reasoning", text: text})
}

func (s *oshUI) finishStreamingText() {
	if s.streamingText == "" {
		return
	}
	text := s.streamingText
	s.streamingText = ""
	s.addMessage(message{role: "agent", text: text})
}

func (s *oshUI) finishResponse(resp response) {
	if resp.id != s.nextRequestID {
		return
	}
	finishedAt := time.Now()
	startedAt := s.requestStartedAt
	s.cancel, s.responding, s.steer = nil, false, nil
	s.requestStartedAt = time.Time{}
	s.retryAttempt, s.retryMaxAttempts, s.retryDeadline = 0, 0, time.Time{}
	hadStreamingText := s.streamingText != ""
	s.finishReasoning()
	s.finishStreamingText()
	if resp.Err == nil {
		s.contextTokens = resp.ContextTokens
	}
	if resp.Err != nil {
		s.addMessage(message{role: "error", text: resp.Err.Error()})
	}
	if !hadStreamingText && resp.Text != "" {
		s.addMessage(message{role: "agent", text: resp.Text})
	}
	if status := completedRequestMessage(startedAt, finishedAt); status != "" {
		s.addMessage(message{role: "status", text: status})
	}
	if len(s.pendingSteer) > 0 {
		pending := append([]string(nil), s.pendingSteer...)
		s.pendingSteer = nil
		s.removePendingInputs("steer", -1)
		s.startRequest(pending[0], true)
		for _, text := range pending[1:] {
			s.pendingSteer = append(s.pendingSteer, text)
			s.pendingInputs = append(s.pendingInputs, pendingInput{"steer", text})
			s.steer <- text
		}
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
	s.pendingSteer = nil
	s.removePendingInputs("steer", -1)
	s.nextRequestID++
	s.cancel = nil
	s.steer = nil
	s.responding = false
	s.requestStartedAt = time.Time{}
	s.retryAttempt, s.retryMaxAttempts, s.retryDeadline = 0, 0, time.Time{}
	s.finishReasoning()
	s.finishStreamingText()
	for i := range s.messages {
		if s.messages[i].role == "tool" && s.messages[i].toolState == "pending" {
			s.messages[i].toolState = "error"
			s.messages[i].toolFinishedAt = time.Now()
			if s.messages[i].toolResult == "" {
				s.messages[i].toolResult = "Cancelled."
			}
		}
	}
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

func (s *oshUI) consumePendingSteer(text string) {
	for i, pending := range s.pendingSteer {
		if pending == text {
			s.pendingSteer = append(s.pendingSteer[:i], s.pendingSteer[i+1:]...)
			break
		}
	}
	for i, pending := range s.pendingInputs {
		if pending.kind == "steer" && pending.text == text {
			s.pendingInputs = append(s.pendingInputs[:i], s.pendingInputs[i+1:]...)
			break
		}
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

func (s *oshUI) navigateHistory(direction int) bool {
	if len(s.inputHistory) == 0 {
		return false
	}
	if s.historyIndex < 0 {
		if direction > 0 {
			return false
		}
		text := s.textarea.Text()
		cursor := clusterToRuneIndex(text, s.textarea.CursorPos())
		if strings.Contains(string([]rune(text)[:cursor]), "\n") {
			return false
		}
		s.historyDraft = s.textarea.Text()
		s.historyIndex = len(s.inputHistory)
	}

	s.historyIndex = min(max(s.historyIndex+direction, 0), len(s.inputHistory))
	text := s.historyDraft
	if s.historyIndex < len(s.inputHistory) {
		text = s.inputHistory[s.historyIndex]
	} else {
		s.historyIndex = -1
	}
	s.textarea.SetText(text)
	s.textarea.SetCursorPos(runeToClusterIndex(text, len([]rune(text))))
	s.markDirty()
	return true
}

func (s *oshUI) handleKey(k tui.KeyEvent) bool {
	if k.Key == tui.KeyRune && k.Rune == 'c' && k.Mod.Has(tui.ModCtrl) {
		now := time.Now()
		if !s.lastCtrlC.IsZero() && now.Sub(s.lastCtrlC) <= ctrlCDoublePressInterval {
			return false
		}
		s.lastCtrlC = now
		s.cancelRequest()
		return true
	}
	s.lastCtrlC = time.Time{}
	if k.Key == tui.KeyEscape {
		if s.retryAttempt > 0 {
			s.cancelRequest()
			return true
		}
		s.historyIndex, s.historyDraft = -1, ""
		s.textarea.Clear()
		s.markDirty()
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
	if k.Mod == tui.ModNone && k.Key == tui.KeyUp && s.navigateHistory(-1) {
		return true
	}
	if k.Mod == tui.ModNone && k.Key == tui.KeyDown && s.navigateHistory(1) {
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
	s.historyIndex, s.historyDraft = -1, ""
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
	if s.reasoningText != "" {
		lines = append(lines, renderedMessageLines(message{role: "reasoning", text: s.reasoningText}, width)...)
		lines = append(lines, "")
	}
	if s.streamingText != "" {
		lines = append(lines, renderedMessageLines(message{role: "agent", text: s.streamingText}, width)...)
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

func renderedMessageLines(msg message, width int) []string {
	return strings.Split(renderedMessage(msg, width), "\n")
}
func renderedMessage(msg message, width int) string {
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
		for _, line := range wrapPlain(msg.text, contentWidth) {
			lines = append(lines, " "+ansiRGBStyle(piText, "", false, false, line))
		}
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
		return renderedToolMessage(msg, width)
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
	return "Done in " + formatToolDuration(finishedAt.Sub(startedAt))
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
		end = now
	}
	return " (" + formatToolDuration(end.Sub(msg.toolStartedAt)) + ")"
}

func renderedToolMessage(msg message, width int) string {
	bg := piToolPendingBg
	if msg.toolState == "success" {
		bg = piToolSuccessBg
	} else if msg.toolState == "error" {
		bg = piToolErrorBg
	}
	inner := max(width-2, 1)
	lines := []string{piBoxLine("", width, piText, bg, false)}
	duration := toolDurationLabel(msg, time.Now())
	if msg.toolCommand != "" {
		if duration != "" {
			// Keep timing metadata on its own line above the command so it cannot
			// be mistaken for part of a long or wrapped invocation.
			lines = append(lines, piBoxLine(" "+strings.TrimSpace(duration), width, piText, bg, true))
		}
		command := "$ " + sanitizeTerminalText(msg.toolCommand)
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

func Run(modelName, reasoningEffort string, respond func(string, <-chan string, func(agent.ToolEvent), context.Context) agent.Response) error {
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

	// Enable Kitty keyboard mode directly instead of querying terminal
	// capabilities. Negotiation reads stdin during startup and can consume text
	// the user has already started typing; unsupported terminals ignore this.
	term.EnableKittyKeyboard()
	defer term.DisableKittyKeyboard()
	term.HideCursor()
	defer term.ShowCursor()
	reader, err := tui.NewEventReader(os.Stdin)
	if err != nil {
		return err
	}
	defer reader.Close()
	updates := make(chan func(), 256)
	root := newUI(modelName, reasoningEffort, func(input string, steer <-chan string, emit func(toolEvent), ctx context.Context) response {
		result := respond(input, steer, emit, ctx)
		return response{Text: result.Text, ContextTokens: result.ContextTokens, Err: result.Err}
	})
	root.dispatch = func(fn func()) { updates <- fn }
	// Run serializes every state mutation below and marks the corresponding
	// branch dirty, so state-level invalidation does not need a second wakeup.
	root.invalidate = func() {}
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
	dirty, urgentRender := true, true
	var lastRender time.Time
	for {
		now := time.Now()
		if dirty && (urgentRender || lastRender.IsZero() || now.Sub(lastRender) >= backgroundRenderInterval) {
			lines, cr, cc := root.render(w, h)
			if err := renderer.render(lines, cr, cc); err != nil {
				return err
			}
			dirty, urgentRender = false, false
			lastRender = now
		}

		// Check stdin before draining background work so a fast stream of model
		// or shell updates cannot prevent the editor from receiving input.
		if ev, ok := reader.PollEvent(0); ok {
			if k, ok := ev.(tui.KeyEvent); ok {
				if !root.handleKey(k) {
					return nil
				}
				dirty, urgentRender = true, true
			}
			continue
		}

		handledWork := false
	forWork:
		for range maxUpdatesPerIteration {
			select {
			case fn := <-updates:
				fn()
				dirty = true
				handledWork = true
			case <-resize:
				w, h = term.Size()
				renderer.resize(w, h)
				dirty, urgentRender = true, true
				handledWork = true
			default:
				break forWork
			}
		}
		if handledWork {
			continue
		}

		// Nothing is ready. Poll briefly for terminal input, but wake often
		// enough to render a dirty frame at the capped refresh rate.
		timeout := eventPollInterval
		if dirty {
			untilRender := backgroundRenderInterval - time.Since(lastRender)
			if untilRender < timeout {
				timeout = max(untilRender, 0)
			}
		}
		if ev, ok := reader.PollEvent(timeout); ok {
			if k, ok := ev.(tui.KeyEvent); ok {
				if !root.handleKey(k) {
					return nil
				}
				dirty, urgentRender = true, true
			}
		}
	}
}
