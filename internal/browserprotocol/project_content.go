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
	bad := func() error { return ErrMalformed }
	if kind == TypeProjectContent {
		v, ok := indirect(body).(ProjectContent)
		if !ok || !validProjectOperation(v.Operation) {
			return bad()
		}
		trimmed := bytes.TrimSpace(v.Input)
		if len(v.Input) > MaxControlBytes-1024 {
			return ErrOversized
		}
		if len(v.Input) == 0 || string(trimmed) == "null" || !json.Valid(v.Input) || len(trimmed) == 0 || trimmed[0] != '{' {
			return bad()
		}
		return nil
	}
	v, ok := indirect(body).(ProjectContentResult)
	if !ok || !validProjectOperation(v.Operation) || len(v.Output) == 0 || len(v.Output) > MaxControlBytes-1024 || string(bytes.TrimSpace(v.Output)) == "null" || !json.Valid(v.Output) || len(bytes.TrimSpace(v.Output)) == 0 || bytes.TrimSpace(v.Output)[0] != '{' {
		return bad()
	}
	return nil
}

func validProjectOperation(operation string) bool {
	switch operation {
	case "list", "read", "body", "create", "revise", "deprecate", "evidence", "evidence_list", "attach", "attachments":
		return true
	default:
		return false
	}
}
