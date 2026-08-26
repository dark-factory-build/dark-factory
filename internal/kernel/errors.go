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
	ErrFutureCursor     = errors.New("watch cursor is ahead of durable head")
	ErrSnapshotTooLarge = errors.New("dashboard snapshot exceeds the entity bound")
	ErrStoreClosed      = errors.New("kernel store is closed")
)

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

type ResyncRequiredError struct {
	Head  EventSequence
	Floor EventSequence
}

func (err *ResyncRequiredError) Error() string {
	return fmt.Sprintf("watch resync required: head=%d floor=%d", err.Head.Int64(), err.Floor.Int64())
}

func corruptControl(kind, _ string) error {
	return fmt.Errorf("%w: unknown %s", ErrCorruptState, kind)
}
