package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
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
	if _, err := tea.NewProgram(newModel(agent.respond), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
