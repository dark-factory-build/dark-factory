package change

// ValidationError reports unsafe repository, revision or worktree input, or
// a worktree that is not the one the Change's durable record names.
// Worktree marks a fault of the worktree itself, found while the repository
// and Git still verify: it will not pass by waiting.
type ValidationError struct {
	Reason   string
	Worktree bool
}

func (e *ValidationError) Error() string { return "invalid Change input: " + e.Reason }

// LimitError reports a bounded Git output that was exceeded.
type LimitError struct{ Reason string }

func (e *LimitError) Error() string { return "Change limit exceeded: " + e.Reason }
