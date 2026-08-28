//go:build darwin || linux

package daemon

import (
	"context"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// startAndActivateRunner drives the exact runner-start grammar for fixtures:
// BeginRunnerStart records the durable Start permission, ActivateRunner binds
// the identity. Generic resource activation deliberately refuses runners.
func startAndActivateRunner(t *testing.T, store *kernel.Store, runID kernel.RunID, resourceID kernel.ResourceID, identity kernel.ResourceIdentity, at kernel.UnixMillis) kernel.Run {
	t.Helper()
	ctx := context.Background()
	run, found, err := store.Run(ctx, runID)
	if err != nil || !found {
		t.Fatalf("runner start run = %+v, found=%v, err=%v", run, found, err)
	}
	resource, found, err := store.Resource(ctx, resourceID)
	if err != nil || !found {
		t.Fatalf("runner start resource = %+v, found=%v, err=%v", resource, found, err)
	}
	run, resource, err = store.BeginRunnerStart(ctx, runID, resourceID, run.Revision, resource.Revision, at)
	if err != nil {
		t.Fatalf("begin runner start: %v", err)
	}
	run, _, err = store.ActivateRunner(ctx, runID, resourceID, run.Revision, resource.Revision, identity, at)
	if err != nil {
		t.Fatalf("activate runner: %v", err)
	}
	return run
}
