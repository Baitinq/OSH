package ui

import "testing"

func TestContextTokensUpdateWhileResponding(t *testing.T) {
	s, _ := newState(nil)
	s.responding = true
	s.nextRequestID = 1

	s.handleToolEvent(1, toolEvent{Kind: toolEventContextTokens, ContextTokens: 123456})

	if s.contextTokens != 123456 {
		t.Fatalf("context tokens = %d, want 123456", s.contextTokens)
	}
}
