package main

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const systemPrompt = "You're an extremely useful general purpose agent."
const modelName = "gpt-5.6"

type agent struct {
	client  openai.Client
	history []json.RawMessage
}

type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func newAgent() agent {
	return agent{client: openai.NewClient()}
}

func (a *agent) respond(msg string) response {
	userMessage, err := json.Marshal(conversationMessage{Role: "user", Content: msg})
	if err != nil {
		panic(err)
	}
	a.history = append(a.history, userMessage)

	params := responses.ResponseNewParams{
		Model:        modelName,
		Instructions: openai.String(systemPrompt),
		Store:        openai.Bool(false),
	}
	params.SetExtraFields(map[string]any{"input": a.history})
	resp, err := a.client.Responses.New(context.TODO(), params)
	if err != nil {
		panic(err)
	}

	for _, output := range resp.Output {
		a.history = append(a.history, json.RawMessage(output.RawJSON()))
	}

	return response{
		text:          resp.OutputText(),
		contextTokens: resp.Usage.TotalTokens,
	}
}
