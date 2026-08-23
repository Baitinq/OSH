package agent

import (
	"strings"
	"testing"
	"time"
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

func TestPrefixUserMessageIncludesCurrentDateAndTime(t *testing.T) {
	now := time.Date(2026, time.August, 23, 19, 42, 17, 0, time.FixedZone("PDT", -7*60*60))
	got := prefixUserMessage("hello", now)
	want := "[2026-08-23T19:42:17-07:00]\n\nhello"
	if got != want {
		t.Fatalf("prefixed message = %q, want %q", got, want)
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
