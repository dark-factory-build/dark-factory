package browserprotocol

import (
	"bytes"
	"encoding/json"
)

// ProjectContent is one bounded, lazy project-library operation. Keeping one
// finite operation union avoids adding a service or exposing API credentials.
type ProjectContent struct {
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
}

type ProjectContentResult struct {
	Operation string          `json:"operation"`
	Output    json.RawMessage `json:"output"`
}

func EncodeProjectContent(id string, value ProjectContent) ([]byte, error) {
	return encodeControl(TypeProjectContent, id, value)
}
func EncodeProjectContentResult(id string, value ProjectContentResult) ([]byte, error) {
	return encodeControl(TypeProjectContentResult, id, value)
}

func validProjectContent(kind MessageType, body any) error {
	var operation string
	var raw json.RawMessage
	if kind == TypeProjectContent {
		value, ok := indirect(body).(ProjectContent)
		if !ok {
			return ErrMalformed
		}
		operation, raw = value.Operation, value.Input
	} else {
		value, ok := indirect(body).(ProjectContentResult)
		if !ok {
			return ErrMalformed
		}
		operation, raw = value.Operation, value.Output
	}
	if !validProjectOperation(operation) {
		return ErrMalformed
	}
	if len(raw) > MaxControlBytes-1024 {
		return ErrOversized
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(raw) {
		return ErrMalformed
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	// Use the same integer, depth and collection bounds as the complete wire
	// decoder. Unrepresentable stored metadata is an explicit bounded error.
	if err := scanJSONValue(decoder, 2, MaxArrayItems); err != nil {
		if kind == TypeProjectContentResult {
			return ErrOversized
		}
		return ErrMalformed
	}
	return nil
}

func validProjectOperation(operation string) bool {
	switch operation {
	case "list", "read", "body", "create", "revise", "deprecate", "evidence", "evidence_list", "attach", "attachments", "outcome_list", "outcome_read", "outcome_write":
		return true
	default:
		return false
	}
}
