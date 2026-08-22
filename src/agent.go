package main

import (
	"context"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const systemPrompt = "You're an extremely useful general purpose agent."

type agent struct {
	client  openai.Client
	context []string
}

func newAgent() agent {
	return agent{
		client:  openai.NewClient(),
		context: []string{},
	}
}

func (a *agent) respond(msg string) string {
	a.context = append(a.context, msg)
	resp, err := a.client.Responses.New(context.TODO(), responses.ResponseNewParams{
		Model: "gpt-5.6",
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(systemPrompt + strings.Join(a.context, "\n"))},
	})
	if err != nil {
		panic(err.Error())
	}

	return resp.OutputText()
}
