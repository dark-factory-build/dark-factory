package change

import (
	"errors"
	"fmt"
)

// ValidationError reports a manifest or target that is unsafe before publication.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "invalid Change input: " + e.Reason }

// LimitError reports a hard manifest or materialization bound.
type LimitError struct{ Reason string }

func (e *LimitError) Error() string { return "Change limit exceeded: " + e.Reason }

// ConflictError reports that the no-replace target already exists.
type ConflictError struct{ Target string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("Change target already exists: %q", e.Target)
}

// UnresolvedError reports that exact owned staging cleanup could not be proved.
type UnresolvedError struct {
	Stage string
	Cause error
}

func (e *UnresolvedError) Error() string {
	return "Change materialization is unresolved at " + e.Stage + ": " + e.Cause.Error()
}
func (e *UnresolvedError) Unwrap() error { return e.Cause }

// OutcomeUnknownError reports possible publication. Callers reconcile through
// the returned commitment and expected filesystem identity; they never delete
// or replay from this error.
type OutcomeUnknownError struct {
	Target     string
	Commitment Commitment
	Device     uint64
	Inode      uint64
	Cause      error
}

func (e *OutcomeUnknownError) Error() string {
	return "Change publication outcome is unknown: " + e.Cause.Error()
}
func (e *OutcomeUnknownError) Unwrap() error { return e.Cause }

// UnsupportedError reports a platform rejected before any filesystem effect.
type UnsupportedError struct{ Platform string }

func (e *UnsupportedError) Error() string {
	return "Change materialization is unsupported on " + e.Platform
}

func joinFailure(original error, cleanup error) error {
	if cleanup == nil {
		return original
	}
	return &UnresolvedError{Stage: "owned staging cleanup", Cause: errors.Join(original, cleanup)}
}
