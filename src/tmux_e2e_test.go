package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTmuxHarness is a deterministic real-TTY target for tmux end-to-end tests.
// It is skipped during the normal suite and intentionally exercises the same
// runUI entry point as production.
func TestTmuxHarness(t *testing.T) {
	if os.Getenv("OSH_TMUX_HARNESS") != "1" {
		t.Skip("tmux harness")
	}
	respond := func(input string, emit func(toolEvent), ctx context.Context) response {
		switch input {
		case "stream":
			for i := 1; i <= 32; i++ {
				select {
				case <-ctx.Done():
					return response{}
				case <-time.After(18 * time.Millisecond):
				}
				emit(toolEvent{phase: "text_delta", detail: fmt.Sprintf("STREAM-LINE-%02d abcdefghijklmnopqrstuvwxyz\n", i)})
			}
			return response{text: strings.TrimSuffix(buildNumberedLines("STREAM-LINE", 32), "\n"), contextTokens: 321}
		case "tools":
			emit(toolEvent{phase: "call", name: "shell", detail: "printf tool-output"})
			time.Sleep(80 * time.Millisecond)
			emit(toolEvent{phase: "result", name: "shell", detail: "tool-output"})
			for _, chunk := range []string{"tool ", "turn ", "complete"} {
				emit(toolEvent{phase: "text_delta", detail: chunk})
				time.Sleep(20 * time.Millisecond)
			}
			return response{text: "tool turn complete", contextTokens: 654}
		case "cancel":
			emit(toolEvent{phase: "text_delta", detail: "WAITING-FOR-CANCEL"})
			<-ctx.Done()
			return response{}
		default:
			for _, chunk := range []string{"ECHO<", input, ">"} {
				emit(toolEvent{phase: "text_delta", detail: chunk})
				time.Sleep(20 * time.Millisecond)
			}
			return response{text: "ECHO<" + input + ">", contextTokens: 777}
		}
	}
	if err := runUI("tmux-e2e", respond); err != nil {
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
