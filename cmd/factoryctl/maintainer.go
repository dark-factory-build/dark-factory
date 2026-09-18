package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// The installed stdio adapter has only the current attempt credential. The
// host daemon owns GitHub credentials and all project/repository decisions.
func runMaintainerMCP(ctx context.Context, input io.Reader, output io.Writer, getenv func(string) string) int {
	client, err := api.NewAttemptClientFromEnvironment(getenv("DARK_FACTORY_SOCKET"))
	if err != nil {
		return exitFailure
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 512<<10)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.JSONRPC != "2.0" {
			return exitFailure
		}
		if len(request.ID) == 0 {
			continue
		}
		result, err := client.Maintainer(ctx, api.MaintainerInput{Request: append(json.RawMessage(nil), scanner.Bytes()...)})
		if err == nil && result.State == "ok" {
			if encoder.Encode(result.Response) != nil {
				return exitFailure
			}
			continue
		}
		message := "Maintainer unavailable; observe any ambiguous operation before retrying"
		if err == nil {
			switch result.State {
			case "denied":
				message = "Maintainer access denied for this attempt or project repository; refresh GitHub access"
			case "invalid":
				message = "Invalid Maintainer request"
			case "repository_unbound":
				message = "Repository GitHub identity is unbound; ask the operator to connect GitHub and bind the registered repository"
			}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": message}}
		if encoder.Encode(response) != nil {
			return exitFailure
		}
	}
	if scanner.Err() != nil {
		return exitFailure
	}
	return 0
}
