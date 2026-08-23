package main

import (
	"fmt"
	"os"
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
	agent := newAgent()
	if err := runUI(agent.modelName, agent.respond); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
