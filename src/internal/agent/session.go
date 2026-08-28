package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/responses"
)

// ConversationMessage is a user or assistant message restored from a session.
type ConversationMessage struct {
	Role string
	Text string
}

// Conversation returns the displayable messages in the current session.
func (a *Agent) Conversation() []ConversationMessage {
	var conversation []ConversationMessage
	for _, item := range a.history {
		if item.OfMessage != nil && item.OfMessage.Role == responses.EasyInputMessageRoleUser {
			text := item.OfMessage.Content.OfString.Value
			if end := strings.Index(text, "]\n\n"); strings.HasPrefix(text, "[") && end >= 0 {
				if _, err := time.Parse(time.RFC3339, text[1:end]); err == nil {
					text = text[end+3:]
				}
			}
			conversation = append(conversation, ConversationMessage{Role: "user", Text: text})
		}
		if item.OfOutputMessage != nil {
			var text strings.Builder
			for _, content := range item.OfOutputMessage.Content {
				if content.OfOutputText != nil {
					text.WriteString(content.OfOutputText.Text)
				} else if content.OfRefusal != nil {
					text.WriteString(content.OfRefusal.Refusal)
				}
			}
			if text.Len() > 0 {
				conversation = append(conversation, ConversationMessage{Role: "assistant", Text: text.String()})
			}
		}
	}
	return conversation
}

const sessionVersion = 2

type sessionFile struct {
	Version int               `json:"version,omitempty"`
	CWD     string            `json:"cwd"`
	Summary string            `json:"summary,omitempty"`
	History []json.RawMessage `json:"history"`
	Usage   []Usage           `json:"usage,omitempty"`
}

func (a *Agent) StartSession(id, sessionsDir string) error {
	a.sessionID = id
	a.sessionDir = filepath.Join(sessionsDir, id)
	return a.SaveSession()
}

func (a *Agent) ResumeSession(id, sessionsDir string) error {
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
	a.sessionID, a.sessionDir = id, dir
	a.summary = saved.Summary
	a.usage = saved.Usage
	for _, usage := range saved.Usage {
		a.tokensUsed += usage.TotalTokens
	}
	for _, raw := range saved.History {
		var fields struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("load session %s: %w", id, err)
		}
		var item responses.ResponseInputItemUnionParam
		var target any
		switch fields.Type {
		case "":
			item.OfMessage = &responses.EasyInputMessageParam{}
			target = item.OfMessage
		case "message":
			item.OfOutputMessage = &responses.ResponseOutputMessageParam{}
			target = item.OfOutputMessage
		case "reasoning":
			item.OfReasoning = &responses.ResponseReasoningItemParam{}
			target = item.OfReasoning
		case "function_call":
			item.OfFunctionCall = &responses.ResponseFunctionToolCallParam{}
			target = item.OfFunctionCall
		case "function_call_output":
			item.OfFunctionCallOutput = &responses.ResponseInputItemFunctionCallOutputParam{}
			target = item.OfFunctionCallOutput
		default:
			return fmt.Errorf("load session %s: unsupported history item %q", id, fields.Type)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("load session %s: %w", id, err)
		}
		a.history = append(a.history, item)
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
	if err := os.MkdirAll(a.sessionDir, 0o700); err != nil {
		return err
	}
	saved := sessionFile{
		Version: sessionVersion, CWD: a.cwd, Summary: a.summary, Usage: a.Usage(),
	}
	for _, item := range a.history {
		data, err := json.Marshal(item)
		if err != nil {
			return err
		}
		saved.History = append(saved.History, data)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(a.sessionDir, "session.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
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
