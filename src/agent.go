package main

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const systemPrompt = "You're an extremely useful general purpose agent."
const modelName = "gpt-5.6"

type agent struct {
	client             openai.Client
	previousResponseID string
}

func newAgent() agent {
	return agent{client: openai.NewClient()}
}

func (a *agent) respond(msg string) response {
	params := responses.ResponseNewParams{
		Model:        modelName,
		Instructions: openai.String(systemPrompt),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(msg),
		},
	}
	if a.previousResponseID != "" {
		params.PreviousResponseID = openai.String(a.previousResponseID)
	}

	resp, err := a.client.Responses.New(context.TODO(), params)
	if err != nil {
		fmt.Println(err.Error())
		panic(err.Error())
	}

	a.previousResponseID = resp.ID
	return response{
		text:          resp.OutputText(),
		contextTokens: resp.Usage.TotalTokens,
	}
}
