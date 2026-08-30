package change

import "fmt"

// ValidationError reports unsafe manifest or filesystem input.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "invalid Change input: " + e.Reason }

// LimitError reports a frozen manifest or materialization bound.
type LimitError struct{ Reason string }

func (e *LimitError) Error() string { return "Change limit exceeded: " + e.Reason }

// ConflictError reports that a create-only target or staging name exists.
type ConflictError struct{ Target string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("Change path already exists: %q", e.Target)
}

// LifecycleError reports Prepare/Populate/Close order or repeat misuse.
type LifecycleError struct{ Reason string }

func (e *LifecycleError) Error() string { return "invalid Change lifecycle: " + e.Reason }

// UnresolvedError retains the exact declared locator and any known identity
// when absence or safe removal cannot be proved.
type UnresolvedError struct {
	Stage       string
	Parent      string
	Name        string
	Identity    StageIdentity
	HasIdentity bool
	Cause       error
}

func (e *UnresolvedError) Error() string {
	return "Change is unresolved at " + e.Stage + ": " + e.Cause.Error()
}
func (e *UnresolvedError) Unwrap() error { return e.Cause }

// OutcomeUnknownError reports a post-publication validation or durability
// failure. Reconciliation uses InspectPublished; callers never delete or replay
// based only on this error.
type OutcomeUnknownError struct {
	Target     string
	Identity   StageIdentity
	Commitment Commitment
	Cause      error
}

func (e *OutcomeUnknownError) Error() string {
	return "Change publication outcome requires inspection: " + e.Cause.Error()
}
func (e *OutcomeUnknownError) Unwrap() error { return e.Cause }

// UnsupportedError reports a platform rejected before any filesystem effect.
type UnsupportedError struct{ Platform string }

func (e *UnsupportedError) Error() string {
	return "Change materialization is unsupported on " + e.Platform
}
