package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{
		cwd: cwd, provider: "openai", modelName: "test",
		history:    []historyItem{{Type: "message", Role: "user", Text: "hello"}, {Type: "message", Role: "assistant", Text: "hi"}},
		compaction: &compactionState{Summary: "earlier context", FirstKeptItem: 1},
		usage:      []Usage{{InputTokens: 40, CachedInputTokens: 10, OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 42}},
	}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := loaded.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	if loaded.compaction.Summary != a.compaction.Summary || len(loaded.history) != 2 || loaded.history[0].Type != "message" || loaded.TokensUsed() != 42 {
		t.Fatalf("loaded session = summary %q, history %#v", loaded.compaction.Summary, loaded.history)
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
	if len(fields) != 7 || fields["version"] != float64(sessionVersion) || fields["cwd"] == nil || fields["compaction"] == nil || fields["history"] == nil || fields["usage"] == nil {
		t.Fatalf("session fields = %#v", fields)
	}
}

func TestUndoRemovesLatestTurnAndSaves(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{cwd: cwd, provider: "openai", modelName: "test", history: []historyItem{
		{Type: "message", Role: "user", Text: "[2026-08-30T12:00:00+02:00]\n\nfirst"},
		{Type: "message", Role: "assistant", Text: "one"},
		{Type: "message", Role: "user", Text: "[2026-08-30T12:01:00+02:00]\n\nsecond"},
		{Type: "tool_call", CallID: "call-1", Name: "repl"},
		{Type: "tool_result", CallID: "call-1", Text: "result"},
		{Type: "message", Role: "assistant", Text: "two"},
	}}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	text, err := a.Undo(0)
	if err != nil || text != "second" || len(a.history) != 2 {
		t.Fatalf("Undo() = %q, %v; history = %#v", text, err, a.history)
	}
	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := loaded.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	if len(loaded.history) != 2 || loaded.history[1].Text != "one" {
		t.Fatalf("saved history = %#v", loaded.history)
	}
}

func TestUndoRestoresPythonCheckpoint(t *testing.T) {
	a := &Agent{}
	startTestSession(t, a)
	defer a.Close()
	if err := a.appendUserMessage("first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 1"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("second"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 2; added = 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(0); err != nil {
		t.Fatal(err)
	}
	output, failed, err := a.pythonREPL().execute(t.Context(), "value, 'added' in globals()")
	if err != nil || failed || output != "(1, False)" {
		t.Fatalf("restored state = %q, failed %v, err %v", output, failed, err)
	}
}

func TestUserMessageCheckpointsAreUnique(t *testing.T) {
	a := &Agent{}
	startTestSession(t, a)
	defer a.Close()
	if err := a.appendUserMessage("first"); err != nil {
		t.Fatal(err)
	}
	first := a.history[0].REPLCheckpoint
	a.history = nil
	if err := a.appendUserMessage("second"); err != nil {
		t.Fatal(err)
	}
	if second := a.history[0].REPLCheckpoint; second == first {
		t.Fatalf("checkpoint reused: %q", second)
	}
}

func TestPythonCheckpointsReuseUnchangedValues(t *testing.T) {
	a := &Agent{}
	startTestSession(t, a)
	defer a.Close()
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 'unchanged'"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("first"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("second"); err != nil {
		t.Fatal(err)
	}
	objects, err := os.ReadDir(filepath.Join(a.sessionDir, "repl-objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("stored objects = %d, want 1", len(objects))
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 'changed'"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("third"); err != nil {
		t.Fatal(err)
	}
	objects, err = os.ReadDir(filepath.Join(a.sessionDir, "repl-objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("stored objects after change = %d, want 2", len(objects))
	}
}

func TestFailedUndoRestoresPythonState(t *testing.T) {
	a := &Agent{}
	startTestSession(t, a)
	defer a.Close()
	if err := a.appendUserMessage("first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 1"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("second"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "value = 2"); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveSession(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.sessionDir, "session.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(0); err == nil {
		t.Fatal("expected undo to fail")
	}
	output, failed, err := a.pythonREPL().execute(t.Context(), "value")
	if err != nil || failed || output != "2" || len(a.history) != 2 {
		t.Fatalf("state after failed undo: output=%q failed=%v err=%v history=%#v", output, failed, err, a.history)
	}
	loaded := &Agent{cwd: a.cwd, provider: a.provider, modelName: a.modelName}
	defer loaded.Close()
	if err := loaded.ResumeSession(a.sessionID, filepath.Dir(a.sessionDir)); err != nil {
		t.Fatal(err)
	}
	output, failed, err = loaded.pythonREPL().execute(t.Context(), "value")
	if err != nil || failed || output != "2" || len(loaded.history) != 2 {
		t.Fatalf("saved state after failed undo: output=%q failed=%v err=%v history=%#v", output, failed, err, loaded.history)
	}
}

func TestUndoRejectsEmptyHistory(t *testing.T) {
	a := &Agent{}
	startTestSession(t, a)
	if _, err := a.Undo(0); err == nil {
		t.Fatal("expected nothing to undo")
	}
}

func TestForkCopiesSessionState(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	oldID := "550e8400-e29b-41d4-a716-446655440000"
	newID := "550e8400-e29b-41d4-a716-446655440001"
	a := &Agent{cwd: cwd, provider: "openai", modelName: "test", compaction: &compactionState{Summary: "checkpoint", FirstKeptItem: 1},
		usage: []Usage{{TotalTokens: 42}}, tokensUsed: 42}
	if _, _, err := a.pythonREPL().execute(t.Context(), "fork_value = 7"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.StartSession(oldID, root); err != nil {
		t.Fatal(err)
	}
	if err := a.appendUserMessage("hello"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.pythonREPL().execute(t.Context(), "fork_value = 8"); err != nil {
		t.Fatal(err)
	}
	if err := a.Fork(newID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, oldID, "session.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, newID, a.history[0].REPLCheckpoint)); err != nil {
		t.Fatal(err)
	}
	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	defer loaded.Close()
	if err := loaded.ResumeSession(newID, root); err != nil {
		t.Fatal(err)
	}
	output, failed, err := loaded.pythonREPL().execute(t.Context(), "fork_value")
	if err != nil || failed || output != "8" || loaded.compaction == nil || loaded.compaction.Summary != "checkpoint" || len(loaded.history) != 1 || loaded.TokensUsed() != 42 {
		t.Fatalf("forked state: output=%q failed=%v err=%v summary=%q history=%#v tokens=%d", output, failed, err, loaded.compaction.Summary, loaded.history, loaded.TokensUsed())
	}
	if _, err := loaded.Undo(0); err != nil {
		t.Fatal(err)
	}
	output, failed, err = loaded.pythonREPL().execute(t.Context(), "fork_value")
	if err != nil || failed || output != "7" {
		t.Fatalf("forked checkpoint state: output=%q failed=%v err=%v", output, failed, err)
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

func TestSessionSaveReplacesCurrentState(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	a.history = append(a.history, historyMessage("user", "hello"))
	if err := a.SaveSession(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, id, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved sessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.History) != 1 || saved.History[0].Text != "hello" {
		t.Fatalf("saved history = %#v", saved.History)
	}
}

func TestUndoCompactedTurnUsesCanonicalHistory(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := &Agent{
		cwd: cwd, provider: "openai", modelName: "test",
		history: []historyItem{
			historyMessage("user", "first"),
			historyMessage("assistant", "one"),
			historyMessage("user", "second"),
			historyMessage("assistant", "two"),
			historyMessage("user", "third"),
			historyMessage("assistant", "three"),
		},
		compaction: &compactionState{Summary: "first and second", FirstKeptItem: 4},
	}
	if err := a.StartSession(id, root); err != nil {
		t.Fatal(err)
	}
	text, err := a.Undo(1)
	if err != nil || text != "second" {
		t.Fatalf("Undo() = %q, %v", text, err)
	}
	if len(a.history) != 2 || a.compaction != nil {
		t.Fatalf("history = %#v, compaction = %#v", a.history, a.compaction)
	}

	loaded := &Agent{cwd: cwd, provider: "openai", modelName: "test"}
	if err := loaded.ResumeSession(id, root); err != nil {
		t.Fatal(err)
	}
	if len(loaded.history) != 2 || loaded.compaction != nil {
		t.Fatalf("loaded history = %#v, compaction = %#v", loaded.history, loaded.compaction)
	}
}
