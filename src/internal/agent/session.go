package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn/internal/assert"
)

type ConversationMessage struct {
	Role string
	Text string
}

func (a *Agent) assertSessionInitialized() {
	assert.That(a.sessionID != "", "agent has no session ID")
	assert.That(a.sessionDir != "", "agent has no session directory")
	assert.That(filepath.IsAbs(a.sessionDir), "session directory is not absolute")
	assert.That(filepath.Base(a.sessionDir) == a.sessionID, "session directory does not match session ID")
}

func (a *Agent) assertSessionUninitialized() {
	assert.That(a.sessionID == "", "agent already has a session ID")
	assert.That(a.sessionDir == "", "agent already has a session directory")
}

func assertSessionArguments(id, sessionsDir string) {
	assert.That(id != "", "empty session ID")
	assert.That(filepath.Base(id) == id, "session ID contains a path separator")
	assert.That(sessionsDir != "", "empty sessions directory")
	assert.That(filepath.IsAbs(sessionsDir), "sessions directory is not absolute")
}

func (a *Agent) Conversation() []ConversationMessage {
	var conversation []ConversationMessage
	for _, item := range a.history {
		if item.Type != "message" {
			continue
		}
		text := item.Text
		if item.Role == "user" {
			text = userMessageText(text)
		}
		if text != "" {
			conversation = append(conversation, ConversationMessage{Role: item.Role, Text: text})
		}
	}
	return conversation
}

func userMessageText(text string) string {
	if end := strings.Index(text, "]\n\n"); strings.HasPrefix(text, "[") && end >= 0 {
		if _, err := time.Parse(time.RFC3339, text[1:end]); err == nil {
			return text[end+3:]
		}
	}
	return text
}

// Undo removes the selected user message and everything after it. steps is zero for the latest message.
func (a *Agent) Undo(steps int) (string, error) {
	a.assertSessionInitialized()
	assert.That(steps >= 0, "negative undo steps")
	a.respondMu.Lock()
	defer a.respondMu.Unlock()
	for i := len(a.history) - 1; i >= 0; i-- {
		if !isUserHistoryItem(a.history[i]) {
			continue
		}
		if steps > 0 {
			steps--
			continue
		}
		text := userMessageText(a.history[i].Text)
		checkpoint := a.history[i].REPLCheckpoint
		rollbackCheckpoint := ""
		if checkpoint != "" {
			var err error
			rollbackCheckpoint, err = a.snapshotREPL()
			if err != nil {
				return "", err
			}
			defer os.Remove(filepath.Join(a.sessionDir, rollbackCheckpoint))
			if err := a.pythonREPL().restore(filepath.Join(a.sessionDir, checkpoint)); err != nil {
				return "", fmt.Errorf("restore Python checkpoint: %w", err)
			}
		}
		history, compaction := a.history, a.compaction
		a.history = a.history[:i]
		if a.compaction != nil && i < a.compaction.FirstKeptItem {
			a.compaction = nil
		}
		if err := a.SaveSession(); err != nil {
			a.history, a.compaction = history, compaction
			if rollbackCheckpoint != "" {
				if rollbackErr := a.pythonREPL().restore(filepath.Join(a.sessionDir, rollbackCheckpoint)); rollbackErr != nil {
					return "", fmt.Errorf("%w; restore Python state after failed undo: %v", err, rollbackErr)
				}
				if rollbackErr := a.pythonREPL().snapshot(filepath.Join(a.sessionDir, "repl.pickle")); rollbackErr != nil {
					return "", fmt.Errorf("%w; save Python state after failed undo: %v", err, rollbackErr)
				}
			}
			return "", err
		}
		return text, nil
	}
	return "", fmt.Errorf("nothing to undo")
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

// Fork continues the current agent state in a new session.
func (a *Agent) Fork(id, sessionsDir string) error {
	a.assertSessionInitialized()
	assertSessionArguments(id, sessionsDir)
	assert.That(id != a.sessionID, "fork session ID matches current session")
	a.respondMu.Lock()
	defer a.respondMu.Unlock()
	oldID, oldDir := a.sessionID, a.sessionDir
	a.sessionID = id
	a.sessionDir = filepath.Join(sessionsDir, id)
	for _, item := range a.history {
		if item.REPLCheckpoint == "" {
			continue
		}
		if err := copyFile(filepath.Join(oldDir, item.REPLCheckpoint), filepath.Join(a.sessionDir, item.REPLCheckpoint)); err != nil {
			a.sessionID, a.sessionDir = oldID, oldDir
			return err
		}
	}
	if err := a.SaveSession(); err != nil {
		a.sessionID, a.sessionDir = oldID, oldDir
		return err
	}
	return nil
}

const sessionVersion = 4

type sessionFile struct {
	Version    int              `json:"version,omitempty"`
	CWD        string           `json:"cwd"`
	Provider   string           `json:"provider,omitempty"`
	Model      string           `json:"model,omitempty"`
	Compaction *compactionState `json:"compaction,omitempty"`
	History    []historyItem    `json:"history"`
	Usage      []Usage          `json:"usage,omitempty"`
}

func (a *Agent) StartSession(id, sessionsDir string) error {
	a.assertSessionUninitialized()
	assertSessionArguments(id, sessionsDir)
	a.sessionID = id
	a.sessionDir = filepath.Join(sessionsDir, id)
	return a.SaveSession()
}

func (a *Agent) ResumeSession(id, sessionsDir string) error {
	a.assertSessionUninitialized()
	assertSessionArguments(id, sessionsDir)
	dir := filepath.Join(sessionsDir, id)
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return fmt.Errorf("load session %s: %w", id, err)
	}
	var saved sessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("load session %s: %w", id, err)
	}
	if saved.Version != sessionVersion {
		return fmt.Errorf("load session %s: unsupported version %d (expected %d)", id, saved.Version, sessionVersion)
	}
	if saved.CWD != a.cwd {
		return fmt.Errorf("session %s belongs to %s", id, saved.CWD)
	}
	if saved.Provider != "" && (saved.Provider != a.provider || saved.Model != a.modelName) {
		return fmt.Errorf("session %s uses %s/%s, current model is %s/%s", id, saved.Provider, saved.Model, a.provider, a.modelName)
	}
	a.sessionID, a.sessionDir = id, dir
	a.compaction = saved.Compaction
	a.history = saved.History
	a.usage = saved.Usage
	for _, usage := range saved.Usage {
		a.tokensUsed += usage.TotalTokens
	}
	statePath := filepath.Join(dir, "repl.pickle")
	if _, err := os.Stat(statePath); err == nil {
		if err := a.pythonREPL().restore(statePath); err != nil {
			return fmt.Errorf("restore Python state: %w", err)
		}
	}
	return nil
}

func (a *Agent) SaveSession() error {
	a.assertSessionInitialized()
	if err := os.MkdirAll(a.sessionDir, 0700); err != nil {
		return err
	}
	saved := sessionFile{Version: sessionVersion, CWD: a.cwd, Provider: a.provider, Model: a.modelName, Compaction: a.compaction, History: a.history, Usage: a.Usage()}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	if a.repl != nil {
		if err := a.repl.snapshot(filepath.Join(a.sessionDir, "repl.pickle")); err != nil {
			return fmt.Errorf("save Python state: %w", err)
		}
	}
	tmp := filepath.Join(a.sessionDir, "session.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(a.sessionDir, "session.json")); err != nil {
		return err
	}
	return nil
}
