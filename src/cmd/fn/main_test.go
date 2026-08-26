package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"fn/internal/agent"
)

func TestRequireOpenAIAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if err := requireOpenAIAPIKey(); err == nil {
		t.Fatal("expected an error when OPENAI_API_KEY is empty")
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := requireOpenAIAPIKey(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseArgs(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		prompt    string
		printMode bool
		wantErr   string
	}{
		{name: "interactive"},
		{name: "short print", args: []string{"-p", "inspect", "this"}, prompt: "inspect this", printMode: true},
		{name: "long print", args: []string{"--print", "inspect this"}, prompt: "inspect this", printMode: true},
		{name: "stdin print", args: []string{"-p"}, printMode: true},
		{name: "unexpected argument", args: []string{"hello"}, wantErr: "use -p"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt, printMode, err := parseArgs(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil || prompt != test.prompt || printMode != test.printMode {
				t.Fatalf("parseArgs() = %q, %v, %v", prompt, printMode, err)
			}
		})
	}
}

func TestBuildPrintPrompt(t *testing.T) {
	for _, test := range []struct {
		name        string
		instruction string
		stdin       string
		want        string
		wantErr     bool
	}{
		{name: "instruction", instruction: "review this", want: "review this"},
		{name: "stdin", stdin: "review this\n", want: "review this"},
		{name: "instruction and stdin", instruction: "review this", stdin: "diff content\n", want: "review this\n\ndiff content"},
		{name: "empty", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildPrintPrompt(test.instruction, strings.NewReader(test.stdin))
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("buildPrintPrompt() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestPrintResponseWritesOnlyFinalText(t *testing.T) {
	var out bytes.Buffer
	respond := func(prompt string, _ <-chan string, emit func(agent.ToolEvent), _ context.Context) agent.Response {
		if prompt != "delegate this" {
			t.Fatalf("prompt = %q", prompt)
		}
		emit(agent.ToolEvent{Kind: agent.ToolEventUpdate, Detail: "tool noise"})
		return agent.Response{Text: "child result"}
	}
	if err := printResponse("delegate this", respond, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "child result\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestPrintResponseReturnsAgentError(t *testing.T) {
	want := errors.New("request failed")
	respond := func(string, <-chan string, func(agent.ToolEvent), context.Context) agent.Response {
		return agent.Response{Err: want}
	}
	if err := printResponse("hello", respond, &bytes.Buffer{}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
