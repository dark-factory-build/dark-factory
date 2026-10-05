package daemon

import (
	"context"
	"os"
	"path/filepath"
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

// A stall after output fails the task. A stall with no output at all means the
// run never started: its task is queued again once, and a second such stall
// fails it.
func TestRunLivenessStopsStalledProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	run := stallNextRun(t, fixture, true)
	if !strings.HasPrefix(run.Proposal.Detail(), stalledRunDetail) {
		t.Fatalf("stalled run = %+v", run)
	}
	fixture.assertReleased(t, run)
	assertTaskStatus(t, fixture, kernel.TaskFailed)
}

func TestRunLivenessRequeuesRunThatNeverStarted(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	for _, want := range []kernel.TaskStatus{kernel.TaskQueued, kernel.TaskFailed} {
		if run := stallNextRun(t, fixture, false); run.Proposal.Detail() != kernel.NeverStartedRunDetail {
			t.Fatalf("silent stalled run = %+v", run)
		}
		assertTaskStatus(t, fixture, want)
	}
}

func assertTaskStatus(t *testing.T, fixture *supervisorFixture, want kernel.TaskStatus) {
	t.Helper()
	task, found, err := fixture.store.Task(context.Background(), fixture.taskID)
	if err != nil || !found || task.Status != want {
		t.Fatalf("stalled task = %+v, want %v, found=%v, err=%v", task, want, found, err)
	}
}

// stallNextRun admits the next run, waits for its provider to start (and, if output is
// set, records terminal output for it), then fails it as stalled.
func stallNextRun(t *testing.T, fixture *supervisorFixture, output bool) kernel.Run {
	t.Helper()
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
	if err := os.Remove(fixture.witness); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		started := false
		fixture.daemon.attemptMu.Lock()
		for _, attempt := range fixture.daemon.attempts {
			attempt.livenessMu.Lock()
			started = !attempt.startedAt.IsZero()
			attempt.livenessMu.Unlock()
			if started && output {
				attempt.markTerminalOutput(time.Now(), 1)
			}
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
	if result.err != nil || result.run.Phase != kernel.RunTerminal || result.run.Proposal == nil || result.run.Proposal.Code() != kernel.FailureProtocol {
		t.Fatalf("stalled run = %+v, err=%v", result.run, result.err)
	}
	return result.run
}

// Codex at model capacity idles at its prompt: the report ends the attempt at
// once, and the task is queued again once, failing on a second in a row.
// Quoted text without Codex's warning marker is not the report.
func TestCodexModelCapacityRequeuesOnce(t *testing.T) {
	var quoted liveAttempt
	quoted.scanUsageLimit(0, 40, []byte("Selected model is at capacity. Please try"))
	if quoted.usageLimit != "" {
		t.Fatalf("quoted capacity matched: %q", quoted.usageLimit)
	}
	// The carry edge splits "❝" (E2 9D 9D) after its lead byte; a stray 0x9D
	// would open an OSC string that swallows the marker in the next frame.
	var split liveAttempt
	first := strings.Repeat("x", 10) + "❝" + strings.Repeat("y", usageScanCarry-2)
	split.scanUsageLimit(0, uint64(len(first)), []byte(first))
	marker := "\x1b[33m⚠\x1b[39m Selected model is at capacity"
	split.scanUsageLimit(uint64(len(first)), uint64(len(first)+len(marker)), []byte(marker))
	if split.usageLimit != kernel.ProviderCapacityRunDetail {
		t.Fatalf("marker after a split rune = %q", split.usageLimit)
	}
	fixture := newSupervisorFixture(t, "unused shell task")
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET body = ? WHERE id = ?`, "capacity", fixture.taskID.Bytes())
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, supervisorTestExecutable(t), filepath.Join(tools, "codex"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath
	for _, want := range []kernel.TaskStatus{kernel.TaskQueued, kernel.TaskFailed} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		run, err := fixture.daemon.RunNext(ctx, fixture.spec)
		cancel()
		if err != nil || run.Proposal == nil || run.Proposal.Code() != kernel.FailureProviderExit || run.Proposal.Detail() != kernel.ProviderCapacityRunDetail {
			t.Fatalf("capacity run = %v, err=%v", run.Proposal, err)
		}
		fixture.assertReleased(t, run)
		assertTaskStatus(t, fixture, want)
	}
}
