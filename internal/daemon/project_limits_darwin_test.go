//go:build darwin || linux

package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestRunLimitWatchdogTerminatesOwnedProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	project, found, err := fixture.store.Project(context.Background(), supervisorProjectID(t, 1))
	if err != nil || !found {
		t.Fatalf("project = %+v, found=%v, err=%v", project, found, err)
	}
	if _, err := fixture.store.SetProjectLimits(context.Background(), project.ID, project.Revision, 0, 1, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		done <- struct {
			run kernel.Run
			err error
		}{run, err}
	}()
	if err := waitForWitness(fixture.witness, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// Pausing future admissions must not disable the active-run watchdog.
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetDispatch(context.Background(), state.Revision, false, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := fixture.daemon.enforceRunLiveness(context.Background(), SupervisorSpec{}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		run kernel.Run
		err error
	}
	select {
	case result = <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("watchdog did not terminate owned provider")
	}
	if result.err != nil || result.run.Phase != kernel.RunTerminal || result.run.Proposal == nil || result.run.Proposal.Kind() != kernel.OutcomeCancelled || result.run.Proposal.Detail() != runLimitDetail {
		t.Fatalf("watchdog run = %+v, err=%v", result.run, result.err)
	}
	fixture.assertReleased(t, result.run)
	if err := fixture.daemon.enforceRunLiveness(context.Background(), SupervisorSpec{}); err != nil {
		t.Fatal(err)
	}
	project, found, err = fixture.store.Project(context.Background(), project.ID)
	if err != nil || !found || project.RunsUsed != 1 {
		t.Fatalf("run allowance after watchdog = %+v, found=%v, err=%v", project, found, err)
	}
}

func TestRunLivenessStopsStalledProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	var skew atomic.Int64
	fixture.daemon.livenessClock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	done := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		done <- struct {
			run kernel.Run
			err error
		}{run, err}
	}()
	if err := waitForWitness(fixture.witness, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		started := false
		fixture.daemon.attemptMu.Lock()
		for _, attempt := range fixture.daemon.attempts {
			attempt.livenessMu.Lock()
			started = !attempt.startedAt.IsZero()
			attempt.livenessMu.Unlock()
		}
		fixture.daemon.attemptMu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live attempt never started")
		}
	}
	skew.Store(int64(stalledRunLivenessThreshold))
	if err := fixture.daemon.enforceRunLiveness(context.Background(), SupervisorSpec{}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		run kernel.Run
		err error
	}
	select {
	case result = <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("stall did not stop the provider")
	}
	if result.err != nil || result.run.Phase != kernel.RunTerminal || result.run.Proposal == nil || result.run.Proposal.Code() != kernel.FailureProtocol || !strings.HasPrefix(result.run.Proposal.Detail(), stalledRunDetail) {
		t.Fatalf("stalled run = %+v, err=%v", result.run, result.err)
	}
	fixture.assertReleased(t, result.run)
	task, found, err := fixture.store.Task(context.Background(), fixture.taskID)
	if err != nil || !found || task.Status != kernel.TaskFailed {
		t.Fatalf("stalled task = %+v, found=%v, err=%v", task, found, err)
	}
}
