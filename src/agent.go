package main

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const systemPrompt = "You're an extremely useful general purpose agent."
const modelName = "gpt-5.6"

var shellTool = responses.ToolUnionParam{
	OfFunction: &responses.FunctionToolParam{
		Name:        "shell",
		Description: openai.String("Run a shell command and return its combined stdout/stderr."),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	},
}

type agent struct {
	client  openai.Client
	history []responses.ResponseInputItemUnionParam
}

func newAgent() agent {
	return agent{client: openai.NewClient()}
}

func runShell(command string) string {
	out, err := exec.Command("sh", "-c", command).CombinedOutput()
	if err != nil {
		return string(out) + "\nexit status: " + err.Error()
	}
	return string(out)
}

func (a *agent) respond(msg string, emit func(toolEvent)) response {
	a.history = append(a.history, responses.ResponseInputItemUnionParam{
		OfMessage: &responses.EasyInputMessageParam{
			Role:    responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(msg)},
		},
	})

	var text string
	var contextTokens int64
	for {
		params := responses.ResponseNewParams{
			Model:        modelName,
			Instructions: openai.String(systemPrompt),
			Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam(a.history)},
			Tools:        []responses.ToolUnionParam{shellTool},
			Store:        openai.Bool(false),
		}
		resp, err := a.client.Responses.New(context.TODO(), params)
		if err != nil {
			panic(err)
		}
		contextTokens = resp.Usage.TotalTokens

		var toolCalls []responses.ResponseFunctionToolCall
		for _, output := range resp.Output {
			var item responses.ResponseInputItemUnionParam
			switch {
			case output.Type == "message":
				msg := output.AsMessage().ToParam()
				item.OfOutputMessage = &msg
			case output.Type == "reasoning":
				r := output.AsReasoning().ToParam()
				item.OfReasoning = &r
			case output.Type == "function_call":
				fc := output.AsFunctionCall()
				param := fc.ToParam()
				item.OfFunctionCall = &param
				toolCalls = append(toolCalls, fc)
			default:
				panic("unhandled output item type: " + output.Type)
			}
			a.history = append(a.history, item)
		}
		if len(toolCalls) == 0 {
			text = resp.OutputText()
			break
		}

		for _, call := range toolCalls {
			var args struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				panic(err)
			}
			emit(toolEvent{phase: "call", name: call.Name, detail: args.Command})
			output := runShell(args.Command)
			emit(toolEvent{phase: "result", name: call.Name, detail: output})
			a.history = append(a.history, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: call.CallID,
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String(runShell(args.Command))},
				},
			})
		}
	}

	return response{text: text, contextTokens: contextTokens}
}
