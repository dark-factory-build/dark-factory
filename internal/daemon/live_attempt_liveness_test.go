package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestStalledRunLivenessRequiresBothQuietSignals(t *testing.T) {
	started := time.UnixMilli(1_000)
	threshold := 2 * time.Minute
	now := started.Add(threshold)
	if !stalledRunLiveness(now, started, time.Time{}, time.Time{}, threshold) {
		t.Fatal("threshold did not report a silent run")
	}
	if stalledRunLiveness(now.Add(-time.Millisecond), started, time.Time{}, time.Time{}, threshold) {
		t.Fatal("reported before the explicit threshold")
	}
	if stalledRunLiveness(now, started, now.Add(-time.Second), time.Time{}, threshold) {
		t.Fatal("terminal output growth did not clear the report")
	}
	if stalledRunLiveness(now, started, time.Time{}, now.Add(-time.Second), threshold) {
		t.Fatal("attempt API activity did not clear the report")
	}
}

func TestRunLivenessFailsOnlyAQuietAttempt(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 241)
	ctx := context.Background()
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session: found=%v err=%v", found, err)
	}
	attempt := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	started := time.UnixMilli(10_000)
	attempt.markStarted(started)
	attempt.retainDiagnosticOutput(0, 18, []byte("Login expired\r\n> "))
	if err := fixture.daemon.registerLiveAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.daemon.unregisterLiveAttempt(active.run.ID, attempt) })
	tick := func(at time.Time) kernel.Run {
		t.Helper()
		fixture.daemon.livenessClock = func() time.Time { return at }
		if err := fixture.daemon.enforceRunLiveness(ctx, SupervisorSpec{}); err != nil {
			t.Fatal(err)
		}
		run, found, err := fixture.store.Run(ctx, active.run.ID)
		if err != nil || !found {
			t.Fatalf("run: found=%v err=%v", found, err)
		}
		return run
	}
	if run := tick(started.Add(stalledRunLivenessThreshold - time.Millisecond)); run.Phase != kernel.RunRunning {
		t.Fatalf("failed before the stall budget: %+v", run)
	}
	attempt.markTerminalOutput(started.Add(5*time.Minute), 19)
	if run := tick(started.Add(stalledRunLivenessThreshold)); run.Phase != kernel.RunRunning {
		t.Fatalf("terminal output within the window did not keep the run: %+v", run)
	}
	attempt.markAttemptAPICall(started.Add(10 * time.Minute))
	if run := tick(started.Add(15 * time.Minute)); run.Phase != kernel.RunRunning {
		t.Fatalf("attempt call within the window did not keep the run: %+v", run)
	}
	run := tick(started.Add(20 * time.Minute))
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Kind() != kernel.OutcomeFailed || run.Proposal.Code() != kernel.FailureProtocol || run.Proposal.Detail() != stalledRunDetail+"Login expired >" {
		t.Fatalf("stalled run = %+v proposal=%+v", run, run.Proposal)
	}
}

// Concurrent attempt API calls can record their timestamps out of order. An
// older one must not move liveness backwards and report a false stall.
func TestLivenessTimestampsNeverMoveBackwards(t *testing.T) {
	attempt := &liveAttempt{wake: make(chan struct{}, 1)}
	newer := time.Unix(2000, 0)
	attempt.markAttemptAPICall(newer)
	attempt.markAttemptAPICall(newer.Add(-time.Minute))
	attempt.markTerminalOutput(newer, 10)
	attempt.markTerminalOutput(newer.Add(-time.Minute), 20)
	if !attempt.lastAttemptAPICallAt.Equal(newer) || !attempt.lastTerminalOutputAt.Equal(newer) {
		t.Fatalf("liveness moved backwards: api=%v output=%v", attempt.lastAttemptAPICallAt, attempt.lastTerminalOutputAt)
	}
}
