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

// ValidMaintainerJSON checks an opaque MCP document without imposing the
// local API's lower-snake-case member convention.
func ValidMaintainerJSON(value []byte) bool { return validateJSON(value, false) == nil }

// MarshalJSON keeps the foreign MCP document opaque to the local protocol's
// lower-snake-case member rule while retaining its exact JSON bytes.
func (input MaintainerInput) MarshalJSON() ([]byte, error) {
	if err := validateJSON(input.Request, false); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Request string `json:"request"`
	}{Request: string(input.Request)})
}

func (input *MaintainerInput) UnmarshalJSON(encoded []byte) error {
	var value struct {
		Request string `json:"request"`
	}
	if err := json.Unmarshal(encoded, &value); err != nil || !ValidMaintainerJSON([]byte(value.Request)) {
		return ErrProtocol
	}
	input.Request = json.RawMessage(value.Request)
	return nil
}

// MarshalJSON keeps the broker's MCP response opaque for the same reason as
// MaintainerInput. The daemon sanitizes accepted observations before reply.
func (result MaintainerResult) MarshalJSON() ([]byte, error) {
	if result.State == "ok" {
		if err := validateJSON(result.Response, false); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		Response string `json:"response,omitempty"`
		State    string `json:"state"`
	}{Response: string(result.Response), State: result.State})
}

func (result *MaintainerResult) UnmarshalJSON(encoded []byte) error {
	var value struct {
		Response string `json:"response,omitempty"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(encoded, &value); err != nil {
		return err
	}
	if value.State == "ok" && !ValidMaintainerJSON([]byte(value.Response)) {
		return ErrProtocol
	}
	result.Response, result.State = json.RawMessage(value.Response), value.State
	return nil
}

func (client *AttemptClient) Maintainer(ctx context.Context, input MaintainerInput) (MaintainerResult, error) {
	if len(input.Request) == 0 || len(input.Request) > 512<<10 || !ValidMaintainerJSON(input.Request) {
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
		if !ValidMaintainerJSON(result.Response) {
			return MaintainerResult{}, ErrProtocol
		}
	case "denied", "unavailable", "invalid", "repository_unbound", "accepted_snapshot_required":
	default:
		return MaintainerResult{}, ErrProtocol
	}
	return result, nil
}
