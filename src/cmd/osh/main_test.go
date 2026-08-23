package main

import "testing"

func TestRequireOpenAIAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if err := requireOpenAIAPIKey(); err == nil {
		t.Fatal("expected an error when OPENAI_API_KEY is empty")
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := requireOpenAIAPIKey(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
