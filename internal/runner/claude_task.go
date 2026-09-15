package runner

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const (
	MaxClaudePrompt       = 8 << 10
	DiscoveryInstructions = "Scope file discovery to the task checkout and private runtime home. Locate tools with command -v and the checkout's documented setup. Never recursively search the user home, Library, Documents, Desktop, Music or Photos for tools or instructions. If a required path is not provided or present, report the missing prerequisite instead of widening the search."
	ClaudeTaskLead        = DiscoveryInstructions + " Complete this Dark Factory task. Before exiting, report the durable outcome with $DARK_FACTORY_FACTORYCTL attempt succeed, block, or fail. Task: "
)

var ErrClaudeTask = errors.New("runner: invalid Claude task")

// PrepareClaudeTask is the one encoding and size check for Claude's terminal
// delivery. Callers that need the result should pass these exact bytes on.
func PrepareClaudeTask(task []byte) ([]byte, error) {
	quoted, err := json.Marshal(string(task))
	if err != nil {
		return nil, ErrClaudeTask
	}
	payload := make([]byte, 0, len(ClaudeTaskLead)+len(quoted)+1)
	payload = append(payload, ClaudeTaskLead...)
	payload = appendTerminalSafeJSON(payload, quoted)
	payload = append(payload, '\r')
	if len(payload) > MaxClaudePrompt {
		return nil, ErrClaudeTask
	}
	return payload, nil
}

func appendTerminalSafeJSON(dst, quoted []byte) []byte {
	const hex = "0123456789abcdef"
	for len(quoted) > 0 {
		value, width := utf8.DecodeRune(quoted)
		if value >= 0x7f && value <= 0x9f {
			dst = append(dst, '\\', 'u', '0', '0', hex[value>>4], hex[value&0xf])
		} else {
			dst = append(dst, quoted[:width]...)
		}
		quoted = quoted[width:]
	}
	return dst
}
