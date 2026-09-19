package kernel

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

var retainedSourceReviewProviders = [...]Provider{ProviderCodex, ProviderClaudeCode}

// RetainedSourceReviewSupported reports the installed provider routes that
// can obtain an exact, read-only retained-source receipt.
func RetainedSourceReviewSupported(provider Provider) bool {
	for _, supported := range retainedSourceReviewProviders {
		if provider == supported {
			return true
		}
	}
	return false
}

func retainedSourceReviewProviderSQL() (string, []any) {
	placeholders := make([]string, len(retainedSourceReviewProviders))
	args := make([]any, len(retainedSourceReviewProviders))
	for i, provider := range retainedSourceReviewProviders {
		placeholders[i], args[i] = "?", provider.String()
	}
	return strings.Join(placeholders, ", "), args
}

// validateRetainedSourceReviewRoute is the installed capability table for
// exact retained-source reads.
func validateRetainedSourceReviewRoute(taskBody string, agent Agent) error {
	_, review, err := ParseRetainedSourceReviewTask(taskBody)
	if err != nil {
		return fmt.Errorf("%w: invalid retained-source review handoff", ErrConflict)
	}
	if !review {
		return nil
	}
	if agent.Role != RoleWorker {
		return fmt.Errorf("%w: retained-source review requires an independent worker", ErrConflict)
	}
	if !RetainedSourceReviewSupported(agent.Provider) {
		return fmt.Errorf("%w: retained-source review unavailable for %s; supported routes: codex, claude_code", ErrConflict, agent.Provider)
	}
	return nil
}

// ParseRetainedSourceReviewTask reads the exact retained identity from the
// task's first line. Later lines are reviewer instructions, never authority.
func ParseRetainedSourceReviewTask(taskBody string) (RetainedChangeHandoff, bool, error) {
	line, _, _ := strings.Cut(taskBody, "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "review" || fields[1] != "handoff" {
		return RetainedChangeHandoff{}, false, nil
	}
	if len(fields) != 7 {
		return RetainedChangeHandoff{}, true, ErrInvalidValue
	}
	decodeID := func(value string) ([]byte, error) {
		decoded, err := hex.DecodeString(value)
		if err != nil || hex.EncodeToString(decoded) != value {
			return nil, ErrInvalidValue
		}
		return decoded, nil
	}
	taskBytes, err := decodeID(fields[2])
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	taskID, err := TaskIDFromBytes(taskBytes)
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	changeBytes, err := decodeID(fields[3])
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	changeID, err := ChangeIDFromBytes(changeBytes)
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	base, err := hex.DecodeString(fields[4])
	if err != nil || len(base) != 20 && len(base) != 32 || hex.EncodeToString(base) != fields[4] {
		return RetainedChangeHandoff{}, true, ErrInvalidValue
	}
	work, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return RetainedChangeHandoff{}, true, ErrInvalidValue
	}
	workRevision, err := NewRevision(work)
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	change, err := strconv.ParseInt(fields[6], 10, 64)
	if err != nil {
		return RetainedChangeHandoff{}, true, ErrInvalidValue
	}
	changeRevision, err := NewRevision(change)
	if err != nil {
		return RetainedChangeHandoff{}, true, err
	}
	return RetainedChangeHandoff{TaskID: taskID, ChangeID: changeID, BaseCommit: fields[4], TaskWorkRevision: workRevision, ChangeRevision: changeRevision}, true, nil
}
