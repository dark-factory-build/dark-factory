package kernel

import (
	"fmt"
	"strings"
)

// RetainedSourceReviewSupported reports the installed provider routes that
// can obtain an exact, read-only retained-source receipt. Claude's launch is
// fixture-proven but lacks the protected local-command boundary required for
// this capability, so it remains unavailable until that proof exists.
func RetainedSourceReviewSupported(provider Provider) bool {
	return provider == ProviderCodex
}

// validateRetainedSourceReviewRoute is the installed capability table for
// exact retained-source reads.
func validateRetainedSourceReviewRoute(taskBody string, agent Agent) error {
	if !strings.HasPrefix(strings.TrimSpace(taskBody), "review handoff ") {
		return nil
	}
	if agent.Role != RoleWorker {
		return fmt.Errorf("%w: retained-source review requires an independent worker", ErrConflict)
	}
	if !RetainedSourceReviewSupported(agent.Provider) {
		return fmt.Errorf("%w: retained-source review unavailable for %s; supported routes: codex", ErrConflict, agent.Provider)
	}
	return nil
}
