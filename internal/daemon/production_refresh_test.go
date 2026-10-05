package daemon

import (
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestRefreshRereadsOnlyPullsLastSeenOpen(t *testing.T) {
	known := []kernel.ProductionPullRequest{{Number: 1, State: "open"}, {Number: 2, State: "open"}, {Number: 3, State: "merged"}, {Number: 4, State: "closed"}}
	got := rereadPulls(known, map[uint64]bool{1: true})
	if len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("reread %+v, want only #2", got)
	}
}
