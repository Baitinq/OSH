package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt(t *testing.T) {
	prompt := buildSystemPrompt("/work/project")
	for _, want := range []string{
		"expert general-purpose assistant operating inside OSH",
		"Available tools:",
		"Do not run destructive or difficult-to-reverse commands",
		"Current working directory: /work/project",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt does not contain %q", want)
		}
	}
}

func TestRunShellReturnsCombinedOutputAndError(t *testing.T) {
	out, err := runShell(t.Context(), "printf stdout; printf stderr >&2; exit 7")
	if err == nil {
		t.Fatal("expected shell error")
	}
	if out != "stdoutstderr" {
		t.Fatalf("combined output = %q", out)
	}
}
