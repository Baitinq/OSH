package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, err := NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if !sessionIDPattern.MatchString(a.SessionID()) {
		t.Fatalf("session ID = %q", a.SessionID())
	}

	a.history = []responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{
			Role: responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{
				OfString: openai.String("[2026-08-26T11:16:04+02:00]\n\nhello"),
			},
		}},
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{
			CallID: "call-1", Name: "shell", Arguments: `{"command":"pwd"}`,
		}},
		{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: "call-1",
			Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: openai.String("/tmp\n"),
			},
		}},
		{OfOutputMessage: &responses.ResponseOutputMessageParam{
			Content: []responses.ResponseOutputMessageContentUnionParam{{
				OfOutputText: &responses.ResponseOutputTextParam{Text: "done"},
			}},
		}},
	}
	if err := a.saveSession(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(os.Getenv("HOME"), ".osh", "sessions", a.SessionID()+".json")
	resumed, err := NewSession(a.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.history) != len(a.history) {
		t.Fatalf("history length = %d, want %d", len(resumed.history), len(a.history))
	}
	transcript := resumed.Transcript()
	if len(transcript) != 3 {
		t.Fatalf("transcript = %#v", transcript)
	}
	if transcript[0].Role != "you" || transcript[0].Text != "hello" {
		t.Fatalf("user item = %#v", transcript[0])
	}
	if transcript[1].ToolCommand != "pwd" || transcript[1].ToolResult != "/tmp\n" {
		t.Fatalf("tool item = %#v", transcript[1])
	}
	if transcript[2].Role != "agent" || transcript[2].Text != "done" {
		t.Fatalf("agent item = %#v", transcript[2])
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMustExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := NewSession("8cf10b8d-6b16-49d2-a9d2-40566bb7e620")
	if err == nil {
		t.Fatal("expected missing session error")
	}
}
