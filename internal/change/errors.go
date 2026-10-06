package change

// ValidationError reports unsafe repository, revision or worktree input, or
// a worktree that is not the one the Change's durable record names.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "invalid Change input: " + e.Reason }

// LimitError reports a bounded Git output that was exceeded.
type LimitError struct{ Reason string }

func (e *LimitError) Error() string { return "Change limit exceeded: " + e.Reason }
