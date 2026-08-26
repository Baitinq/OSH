package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openai/openai-go/v3/responses"
)

const sessionVersion = 1

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type sessionFile struct {
	Version int               `json:"version"`
	History []json.RawMessage `json:"history"`
}

// TranscriptItem is a displayable item reconstructed from persisted model history.
type TranscriptItem struct {
	Role        string
	Text        string
	ToolID      string
	ToolName    string
	ToolCommand string
	ToolResult  string
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	raw := hex.EncodeToString(value[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}

func sessionPath(id string) (string, error) {
	if !sessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid session UUID %q", id)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".osh", "sessions", id+".json"), nil
}

// NewSession creates a new session when id is empty, or resumes the given one.
func NewSession(id string) (*Agent, error) {
	a := New()
	if id == "" {
		var err error
		id, err = newSessionID()
		if err != nil {
			return nil, err
		}
		a.sessionID = id
		if err := a.saveSession(); err != nil {
			return nil, err
		}
		return a, nil
	}

	path, err := sessionPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s not found", id)
		}
		return nil, err
	}
	var stored sessionFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("load session %s: %w", id, err)
	}
	if stored.Version != sessionVersion {
		return nil, fmt.Errorf("unsupported session version %d", stored.Version)
	}
	history, err := decodeHistory(stored.History)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", id, err)
	}
	a.sessionID, a.history = id, history
	return a, nil
}

func (a *Agent) SessionID() string { return a.sessionID }

func (a *Agent) saveSession() error {
	if a.sessionID == "" {
		return nil
	}
	path, err := sessionPath(a.sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		return err
	}
	history := make([]json.RawMessage, len(a.history))
	for i, item := range a.history {
		history[i], err = json.Marshal(item)
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(sessionFile{Version: sessionVersion, History: history}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp-" + strings.ReplaceAll(a.sessionID, "-", "")
	if err := os.WriteFile(tmp, data, 0666); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func decodeHistory(raw []json.RawMessage) ([]responses.ResponseInputItemUnionParam, error) {
	history := make([]responses.ResponseInputItemUnionParam, 0, len(raw))
	for _, data := range raw {
		var kind struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if err := json.Unmarshal(data, &kind); err != nil {
			return nil, err
		}
		var item responses.ResponseInputItemUnionParam
		switch {
		case kind.Role == "user":
			item.OfMessage = new(responses.EasyInputMessageParam)
			if err := json.Unmarshal(data, item.OfMessage); err != nil {
				return nil, err
			}
		case kind.Type == "message" && kind.Role == "assistant":
			item.OfOutputMessage = new(responses.ResponseOutputMessageParam)
			if err := json.Unmarshal(data, item.OfOutputMessage); err != nil {
				return nil, err
			}
		case kind.Type == "reasoning":
			item.OfReasoning = new(responses.ResponseReasoningItemParam)
			if err := json.Unmarshal(data, item.OfReasoning); err != nil {
				return nil, err
			}
		case kind.Type == "function_call":
			item.OfFunctionCall = new(responses.ResponseFunctionToolCallParam)
			if err := json.Unmarshal(data, item.OfFunctionCall); err != nil {
				return nil, err
			}
		case kind.Type == "function_call_output":
			item.OfFunctionCallOutput = new(responses.ResponseInputItemFunctionCallOutputParam)
			if err := json.Unmarshal(data, item.OfFunctionCallOutput); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported history item type %q", kind.Type)
		}
		history = append(history, item)
	}
	return history, nil
}

// Transcript reconstructs the stable visible conversation from model history.
func (a *Agent) Transcript() []TranscriptItem {
	var items []TranscriptItem
	toolIndexes := make(map[string]int)
	for _, item := range a.history {
		switch {
		case item.OfMessage != nil:
			text := item.OfMessage.Content.OfString.Value
			if end := strings.Index(text, "]\n\n"); strings.HasPrefix(text, "[") && end >= 0 {
				text = text[end+3:]
			}
			items = append(items, TranscriptItem{Role: "you", Text: text})
		case item.OfReasoning != nil:
			var text strings.Builder
			for _, summary := range item.OfReasoning.Summary {
				text.WriteString(summary.Text)
			}
			if text.Len() > 0 {
				items = append(items, TranscriptItem{Role: "reasoning", Text: text.String()})
			}
		case item.OfOutputMessage != nil:
			var text strings.Builder
			for _, content := range item.OfOutputMessage.Content {
				if content.OfOutputText != nil {
					text.WriteString(content.OfOutputText.Text)
				}
			}
			if text.Len() > 0 {
				items = append(items, TranscriptItem{Role: "agent", Text: text.String()})
			}
		case item.OfFunctionCall != nil:
			call := item.OfFunctionCall
			command := call.Arguments
			var args struct {
				Command string `json:"command"`
				Query   string `json:"query"`
			}
			if json.Unmarshal([]byte(call.Arguments), &args) == nil {
				if args.Command != "" {
					command = args.Command
				} else if args.Query != "" {
					command = args.Query
				}
			}
			toolIndexes[call.CallID] = len(items)
			items = append(items, TranscriptItem{Role: "tool", ToolID: call.CallID, ToolName: call.Name, ToolCommand: command})
		case item.OfFunctionCallOutput != nil:
			output := item.OfFunctionCallOutput
			if index, ok := toolIndexes[output.CallID]; ok {
				items[index].ToolResult = output.Output.OfString.Value
			}
		}
	}
	return items
}
