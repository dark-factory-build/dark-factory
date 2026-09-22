//go:build darwin

package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// A pull request recorded before the daemon owned publication has a head but
// no source head. Upgrading must not republish onto its branch, and the
// scheduler must keep admitting unrelated work while such facts exist.
func TestSchedulerLeavesLegacyPublicationAloneAndKeepsScheduling(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x90, kernel.RoleWorker)
	ctx := context.Background()
	changeState, _ := fixture.settlementWorktree(t)
	fixture.failBeforeRuntime(t)
	if settled, err := fixture.daemon.settleRun(ctx, fixture.changeParent, fixture.run.ID); err != nil || settled.Phase != kernel.RunTerminal {
		t.Fatalf("settlement = %+v, %v", settled, err)
	}
	retained, found, err := fixture.store.Change(ctx, changeState.ID)
	if err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.HeadCommit == nil {
		t.Fatalf("retained change = %+v found=%v err=%v", retained, found, err)
	}
	head := hex.EncodeToString(retained.HeadCommit.Bytes())
	pr := kernel.ProductionPullRequest{Number: 7, Title: "Legacy publication", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + changeState.ID.String()[:12], Base: "main", State: "open", Review: kernel.ProductionReview{Head: head, State: "unknown"}}
	if err := fixture.store.RecordProductionObservation(ctx, fixture.run.ProjectID, kernel.ProductionObservation{Repository: "example/factory", ObservedAt: 400, PullRequests: []kernel.ProductionPullRequest{pr}}, mustKernelTime(t, 400)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RecordPublication(ctx, fixture.run.ProjectID, fixture.run.TaskID, "example/factory", pr, mustKernelTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	facts, err := fixture.store.ChangePublicationFacts(ctx)
	if err != nil || len(facts) != 1 || facts[0].PublishedHead != head || facts[0].PublishedSourceHead != "" || facts[0].PublishOperation != "" {
		t.Fatalf("legacy publication fact = %+v, %v", facts, err)
	}
	fixture.daemon.github = &maintainer.Host{}
	var remote atomic.Int64
	fixture.daemon.changePublicationActions.PublishAndRefresh = func(context.Context, ChangePublicationEvent, string) error {
		remote.Add(1)
		return nil
	}
	fixture.daemon.changePublicationActions.RequestReview = func(context.Context, ChangePublicationEvent) error {
		remote.Add(1)
		return nil
	}
	polls := make(chan time.Time)
	tick := func() {
		t.Helper()
		select {
		case polls <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler did not receive poll")
		}
	}
	var probes atomic.Int64
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	spec := SupervisorSpec{schedulerPoll: polls, scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
		probes.Add(1)
		spec.admissionObserved(false)
		return kernel.Run{}, fmt.Errorf("%w: fixture admission", kernel.ErrConflict)
	}}
	go func() { done <- fixture.daemon.RunScheduler(runCtx, spec) }()
	// Receiving the second unbuffered tick proves the first pass, including
	// its publication step, completed without stopping the scheduler.
	tick()
	tick()
	waitSchedulerCalls(t, &probes, 1)
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("scheduler stopped on a legacy publication: %v", err)
	}
	if remote.Load() != 0 {
		t.Fatalf("legacy publication reached the Maintainer %d times", remote.Load())
	}
}
