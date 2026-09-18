package api

import (
	"context"
	"encoding/json"
	"time"
)

// MaintainerInput carries one bounded JSON-RPC message through attempt auth.
// Repository and operation authority remain daemon and broker decisions.
type MaintainerInput struct {
	Request json.RawMessage `json:"request"`
}
type MaintainerResult struct {
	Response json.RawMessage `json:"response,omitempty"`
	State    string          `json:"state"`
}

func (client *AttemptClient) Maintainer(ctx context.Context, input MaintainerInput) (MaintainerResult, error) {
	if len(input.Request) == 0 || len(input.Request) > 512<<10 || !json.Valid(input.Request) {
		return MaintainerResult{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var result MaintainerResult
	if err := client.client.call(ctx, "attempt_maintainer", input, &result); err != nil {
		return MaintainerResult{}, err
	}
	switch result.State {
	case "ok":
		if !json.Valid(result.Response) {
			return MaintainerResult{}, ErrProtocol
		}
	case "denied", "unavailable", "invalid", "repository_unbound":
	default:
		return MaintainerResult{}, ErrProtocol
	}
	return result, nil
}
