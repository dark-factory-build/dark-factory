package kernel

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidValue     = errors.New("invalid kernel value")
	ErrBusy             = errors.New("sqlite writer busy")
	ErrCorruptState     = errors.New("corrupt kernel state")
	ErrForeignDatabase  = errors.New("foreign or incompatible database")
	ErrRevisionConflict = errors.New("revision conflict")
	ErrConflict         = errors.New("kernel entity conflicts with durable state")
	ErrNotFound         = errors.New("kernel entity not found")
	ErrUnauthorized     = errors.New("attempt credential is not authorized")
	ErrSnapshotTooLarge = errors.New("dashboard snapshot exceeds the entity bound")
	ErrStoreClosed      = errors.New("kernel store is closed")
)

// OutcomeRefusal is returned after an exact attempt bearer was found, but
// the durable finalization edge refused the proposal. It deliberately keeps
// the underlying bounded kernel cause so the exact live owner can reconcile
// without turning every unauthorized response into a kill signal.
type OutcomeRefusal struct {
	cause error
}

func (err *OutcomeRefusal) Error() string {
	return "attempt outcome refused: " + err.cause.Error()
}

func (err *OutcomeRefusal) Unwrap() error { return err.cause }

func NewOutcomeRefusal(cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: missing outcome refusal cause", ErrInvalidValue)
	}
	return &OutcomeRefusal{cause: cause}
}

type OutcomeUnknownError struct {
	cause error
}

func (err *OutcomeUnknownError) Error() string {
	return "sqlite transaction outcome is unknown: " + err.cause.Error()
}

func (err *OutcomeUnknownError) Unwrap() error { return err.cause }

// NewOutcomeUnknownError marks an operation whose durable result cannot be
// distinguished after bounded domain reconciliation. Callers must hand the
// entity to recovery rather than reporting success or replaying the write.
func NewOutcomeUnknownError(cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: missing outcome-unknown cause", ErrInvalidValue)
	}
	return &OutcomeUnknownError{cause: cause}
}

func corruptControl(kind, _ string) error {
	return fmt.Errorf("%w: unknown %s", ErrCorruptState, kind)
}
