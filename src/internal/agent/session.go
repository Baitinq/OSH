package agent

import (
	"encoding/json"
	"fmt"
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
			if end := strings.Index(text, "]\n\n"); strings.HasPrefix(text, "[") && end >= 0 {
				if _, err := time.Parse(time.RFC3339, text[1:end]); err == nil {
					text = text[end+3:]
				}
			}
		}
		if text != "" {
			conversation = append(conversation, ConversationMessage{Role: item.Role, Text: text})
		}
	}
	return conversation
}

const sessionVersion = 3

type legacySessionFile struct {
	Version int               `json:"version"`
	CWD     string            `json:"cwd"`
	Summary string            `json:"summary,omitempty"`
	History []json.RawMessage `json:"history"`
	Usage   []Usage           `json:"usage,omitempty"`
}

func migrateLegacyHistory(items []json.RawMessage) []historyItem {
	history := make([]historyItem, 0, len(items))
	for _, raw := range items {
		var item struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Arguments string          `json:"arguments"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Output    string          `json:"output"`
		}
		_ = json.Unmarshal(raw, &item)
		switch {
		case item.Type == "function_call":
			history = append(history, historyItem{Type: "tool_call", CallID: item.CallID, Name: item.Name, Arguments: json.RawMessage(item.Arguments), Provider: "openai"})
		case item.Type == "function_call_output":
			history = append(history, historyItem{Type: "tool_result", CallID: item.CallID, Text: item.Output})
		case item.Type == "reasoning":
			history = append(history, historyItem{Type: "reasoning", Provider: "openai", ProviderData: raw})
		case item.Role != "":
			var text string
			if item.Content[0] == '"' {
				_ = json.Unmarshal(item.Content, &text)
			} else {
				var content []struct {
					Type    string `json:"type"`
					Text    string `json:"text"`
					Refusal string `json:"refusal"`
				}
				_ = json.Unmarshal(item.Content, &content)
				for _, part := range content {
					text += part.Text + part.Refusal
				}
			}
			historyItem := historyItem{Type: "message", Role: item.Role, Text: text}
			if item.Role == "assistant" {
				historyItem.Provider, historyItem.ProviderData = "openai", raw
			}
			history = append(history, historyItem)
		}
	}
	return history
}

type sessionFile struct {
	Version  int           `json:"version,omitempty"`
	CWD      string        `json:"cwd"`
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Summary  string        `json:"summary,omitempty"`
	History  []historyItem `json:"history"`
	Usage    []Usage       `json:"usage,omitempty"`
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
	if saved.Version == 2 {
		var legacy legacySessionFile
		if err := json.Unmarshal(data, &legacy); err != nil {
			return fmt.Errorf("load session %s: %w", id, err)
		}
		saved = sessionFile{Version: sessionVersion, CWD: legacy.CWD, Provider: "openai", Model: a.modelName, Summary: legacy.Summary, History: migrateLegacyHistory(legacy.History), Usage: legacy.Usage}
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
	a.summary = saved.Summary
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
	saved := sessionFile{Version: sessionVersion, CWD: a.cwd, Provider: a.provider, Model: a.modelName, Summary: a.summary, History: a.history, Usage: a.Usage()}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(a.sessionDir, "session.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(a.sessionDir, "session.json")); err != nil {
		return err
	}
	if a.repl != nil {
		if err := a.repl.snapshot(filepath.Join(a.sessionDir, "repl.pickle")); err != nil {
			return fmt.Errorf("save Python state: %w", err)
		}
	}
	return nil
}
