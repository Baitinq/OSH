package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestRespondStreamsContextTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":300,\"output_tokens\":21,\"total_tokens\":321,\"input_tokens_details\":{},\"output_tokens_details\":{}}}}\n\n")
	}))
	defer server.Close()

	a := &Agent{
		client:       openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
		modelName:    "test",
		instructions: "test",
	}
	startTestSession(t, a)
	var streamed int64
	response := a.Respond("hello", nil, func(event ToolEvent) {
		if event.Kind == ToolEventContextTokens {
			streamed = event.ContextTokens
		}
	}, t.Context())

	if response.Err != nil {
		t.Fatal(response.Err)
	}
	if streamed != 321 || response.ContextTokens != 321 {
		t.Fatalf("context tokens: streamed=%d response=%d, want 321", streamed, response.ContextTokens)
	}
}
