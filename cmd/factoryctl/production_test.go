//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"testing"
)

func TestProductionObserveCommandAndBoundedJSONInput(t *testing.T) {
	command, help, ok := parse([]string{"production", "observe", "--json-stdin"})
	if !ok || help || command.kind != commandProductionObserve {
		t.Fatalf("production observe parse = %+v, help=%v, ok=%v", command, help, ok)
	}
	if got := runProductionObserveInput(context.Background(), bytes.NewBufferString(`{"project_id":"bad"}`), func(string) string { return "" }, &bytes.Buffer{}, &bytes.Buffer{}); got != exitFailure {
		t.Fatalf("invalid production input exit = %d", got)
	}
	if got := runProductionObserveInput(context.Background(), bytes.NewBufferString(`{"project_id":"`+idForTest+`","observation":{"repository":"team/repo"}}`), func(key string) string {
		if key == "DARK_FACTORY_ATTEMPT_TOKEN_FILE" {
			return "/private/tmp/attempt-token"
		}
		return ""
	}, &bytes.Buffer{}, &bytes.Buffer{}); got != exitFailure {
		t.Fatalf("attempt credential was not refused = %d", got)
	}
}

const idForTest = "11111111111111111111111111111111"
