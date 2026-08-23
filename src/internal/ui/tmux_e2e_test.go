package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"osh/internal/agent"
	"time"
)

// TestTmuxHarness is a deterministic real-TTY target for tmux end-to-end tests.
// It is skipped during the normal suite and intentionally exercises the same
// Run entry point as production.
func TestTmuxHarness(t *testing.T) {
	if os.Getenv("OSH_TMUX_HARNESS") != "1" {
		t.Skip("tmux harness")
	}
	respond := func(input string, emit func(agent.ToolEvent), ctx context.Context) agent.Response {
		switch input {
		case "stream":
			for i := 1; i <= 32; i++ {
				select {
				case <-ctx.Done():
					return agent.Response{}
				case <-time.After(18 * time.Millisecond):
				}
				emit(toolEvent{Phase: "text_delta", Detail: fmt.Sprintf("STREAM-LINE-%02d abcdefghijklmnopqrstuvwxyz\n", i)})
			}
			return agent.Response{Text: strings.TrimSuffix(buildNumberedLines("STREAM-LINE", 32), "\n"), ContextTokens: 321}
		case "tools":
			emit(toolEvent{Phase: "call", Name: "shell", Detail: "printf tool-output"})
			time.Sleep(80 * time.Millisecond)
			emit(toolEvent{Phase: "result", Name: "shell", Detail: "tool-output"})
			for _, chunk := range []string{"tool ", "turn ", "complete"} {
				emit(toolEvent{Phase: "text_delta", Detail: chunk})
				time.Sleep(20 * time.Millisecond)
			}
			return agent.Response{Text: "tool turn complete", ContextTokens: 654}
		case "cancel":
			emit(toolEvent{Phase: "text_delta", Detail: "WAITING-FOR-CANCEL"})
			<-ctx.Done()
			return agent.Response{}
		default:
			for _, chunk := range []string{"ECHO<", input, ">"} {
				emit(toolEvent{Phase: "text_delta", Detail: chunk})
				time.Sleep(20 * time.Millisecond)
			}
			return agent.Response{Text: "ECHO<" + input + ">", ContextTokens: 777}
		}
	}
	if err := Run("tmux-e2e", "medium", respond); err != nil {
		t.Fatal(err)
	}
}

func buildNumberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s-%02d abcdefghijklmnopqrstuvwxyz\n", prefix, i)
	}
	return b.String()
}
