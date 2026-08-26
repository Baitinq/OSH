package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"osh/internal/agent"
	"osh/internal/ui"
)

func requireOpenAIAPIKey() error {
	if os.Getenv("OPENAI_API_KEY") == "" {
		return fmt.Errorf("OPENAI_API_KEY must be set")
	}
	return nil
}

func parseArgs(args []string) (string, bool, error) {
	flags := flag.NewFlagSet("osh", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	printMode := flags.Bool("p", false, "print the response without starting the UI")
	flags.BoolVar(printMode, "print", false, "print the response without starting the UI")
	if err := flags.Parse(args); err != nil {
		return "", false, err
	}
	if !*printMode {
		if flags.NArg() > 0 {
			return "", false, fmt.Errorf("unexpected arguments; use -p to run in print mode")
		}
		return "", false, nil
	}
	if flags.NArg() == 0 {
		return "", false, fmt.Errorf("print mode requires a prompt")
	}
	return strings.Join(flags.Args(), " "), true, nil
}

func printResponse(prompt string, respond func(string, <-chan string, func(agent.ToolEvent), context.Context) agent.Response, out io.Writer) error {
	resp := respond(prompt, nil, func(agent.ToolEvent) {}, context.Background())
	if resp.Err != nil {
		return resp.Err
	}
	if _, err := io.WriteString(out, resp.Text); err != nil {
		return err
	}
	if !strings.HasSuffix(resp.Text, "\n") {
		_, err := io.WriteString(out, "\n")
		return err
	}
	return nil
}

func run(args []string, stdout io.Writer) error {
	prompt, printMode, err := parseArgs(args)
	if err != nil {
		return err
	}
	if err := requireOpenAIAPIKey(); err != nil {
		return err
	}
	a := agent.New()
	defer a.Close()
	if printMode {
		return printResponse(prompt, a.Respond, stdout)
	}
	return ui.Run(a.ModelName(), a.ReasoningEffort(), a.Respond)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
