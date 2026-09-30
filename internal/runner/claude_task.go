package runner

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const (
	MaxClaudePrompt       = 8 << 10
	DiscoveryInstructions = "When assigned a writable task checkout, read and edit it directly, including corrections after send-back; attempt source is only for inspecting a settled retained Change, not a prerequisite for your own checkout. Never substitute another task or private Change path. Scope file discovery to the task checkout and private runtime home. Locate tools with command -v and the checkout's documented setup. Never recursively search the user home, Library, Documents, Desktop, Music or Photos for tools or instructions. If a required path is not provided or present, report the missing prerequisite instead of widening the search. In UI review, screenshots are illustrative only and never blocking evidence; judge correctness from render tests and source behavior."
	ClaudeTaskLead        = DiscoveryInstructions + " Complete this Dark Factory task. Shell commands cannot reach the attempt API: use the factory_attempt factory tool for every factoryctl attempt or overseer command, passing argv without the executable. Before exiting, report the durable outcome through it with attempt succeed, block, or fail. Task: "
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
