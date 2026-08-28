package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func TestSessionRoundTrip(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{
		cwd: cwd, provider: "openai", modelName: "test",
		history: []historyItem{{Type: "message", Role: "user", Text: "hello"}, {Type: "message", Role: "assistant", Text: "hi"}},
		summary: "earlier context",
		usage:   []Usage{{InputTokens: 40, CachedInputTokens: 10, OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 42}},
	}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := loaded.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	if loaded.summary != a.summary || len(loaded.history) != 2 || loaded.history[0].Type != "message" || loaded.TokensUsed() != 42 {
		t.Fatalf("loaded session = summary %q, history %#v", loaded.summary, loaded.history)
	}
	if loaded.history[1].Text != "hi" {
		t.Fatalf("loaded output message = %#v", loaded.history[1])
	}
	sessionPath := filepath.Join(root, id, "session.json")
	if mode := fileMode(t, sessionPath); mode != 0o600 {
		t.Fatalf("session mode = %o", mode)
	}
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 7 || fields["version"] != float64(sessionVersion) || fields["cwd"] == nil || fields["summary"] == nil || fields["history"] == nil || fields["usage"] == nil {
		t.Fatalf("session fields = %#v", fields)
	}
}

func TestResumeMigratesVersionTwoSession(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldHistory := []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfMessage("hello", responses.EasyInputMessageRoleUser),
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: "call_1", Name: "repl", Arguments: `{"code":"print(1)"}`}},
		responses.ResponseInputItemParamOfFunctionCallOutput("call_1", "1\n"),
	}
	historyData, _ := json.Marshal(oldHistory)
	var rawHistory []json.RawMessage
	_ = json.Unmarshal(historyData, &rawHistory)
	data, _ := json.Marshal(legacySessionFile{Version: 2, CWD: cwd, History: rawHistory})
	if err := os.WriteFile(filepath.Join(dir, "session.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cwd: cwd, provider: "openai", modelName: "gpt-test"}
	if err := a.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	if len(a.history) != 3 || a.history[0].Text != "hello" || a.history[1].Name != "repl" || string(a.history[1].Arguments) != `{"code":"print(1)"}` || a.history[2].Text != "1\n" {
		t.Fatalf("migrated history = %#v", a.history)
	}
}

func TestSessionRejectsUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sessionFile{Version: sessionVersion + 1, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Agent{cwd: cwd}).ResumeSession(id, root); err == nil {
		t.Fatal("expected unsupported version")
	}
}

func TestSessionRestoresPythonState(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{cwd: cwd}
	if _, _, err := a.pythonREPL().execute(t.Context(), "items = [1, 2, 3]"); err != nil {
		t.Fatal(err)
	}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	a.Close()

	loaded := &Agent{cwd: cwd}
	defer loaded.Close()
	if err := loaded.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	output, failed, err := loaded.pythonREPL().execute(t.Context(), "items")
	if err != nil || failed || output != "[1, 2, 3]" {
		t.Fatalf("restored value = %q, failed %v, err %v", output, failed, err)
	}
}

func TestSessionRejectsDifferentWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{cwd: t.TempDir()}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	if err := (&Agent{cwd: t.TempDir()}).ResumeSession(id, root); err == nil {
		t.Fatal("expected working directory mismatch")
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestConversationRestoresDisplayableMessages(t *testing.T) {
	a := &Agent{history: []historyItem{
		{Type: "message", Role: "user", Text: "[2026-08-27T14:03:01+02:00]\n\nfix session restoring"},
		{Type: "tool_call", Name: "repl", Arguments: json.RawMessage(`{"code":"pwd"}`)},
		{Type: "message", Role: "assistant", Text: "Fixed."},
	}}
	got := a.Conversation()
	if len(got) != 2 || got[0].Role != "user" || got[0].Text != "fix session restoring" || got[1].Role != "assistant" || got[1].Text != "Fixed." {
		t.Fatalf("conversation = %#v", got)
	}
}
