package main

import "testing"

func TestParseAgentPaths(t *testing.T) {
	command, help, ok := parseOperator([]string{"agent", "paths", "--agent", "0123456789abcdef0123456789abcdef"})
	if !ok || help || command.kind != commandAgentPaths || command.agent == "" {
		t.Fatalf("parsed command = %+v, help=%t, ok=%t", command, help, ok)
	}
	if _, _, ok := parseOperator([]string{"agent", "paths"}); ok {
		t.Fatal("agent paths without --agent accepted")
	}
}
