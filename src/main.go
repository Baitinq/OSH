package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
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
	if _, err := tea.NewProgram(newModel(agent.modelName, agent.respond)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
