package main

import (
	"fmt"
	"os"

	"osh/internal/agent"
	"osh/internal/ui"
)

func requireOpenAIAPIKey() error {
	if os.Getenv("OPENAI_API_KEY") == "" {
		return fmt.Errorf("OPENAI_API_KEY must be set")
	}
	return nil
}

func main() {
	if err := requireOpenAIAPIKey(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a := agent.New()
	if err := ui.Run(a.ModelName(), a.Respond); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
