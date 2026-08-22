package main

import "github.com/openai/openai-go/v3"

const testResponse = "test1234"

type agent struct {
	client openai.Client
}

func newAgent() agent {
	return agent{client: openai.NewClient()}
}

func (agent) respond(string) string {
	return testResponse
}
