//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/ncruces/go-sqlite3"
)

func TestSchedulerUsesOneUnobservedProbeAndJoinsAdmittedOwners(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var calls, unobserved, maxUnobserved atomic.Int64
	spec := SupervisorSpec{
		scheduledAttempt: func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
			call := calls.Add(1)
			present := unobserved.Add(1)
			for maximum := maxUnobserved.Load(); present > maximum && !maxUnobserved.CompareAndSwap(maximum, present); maximum = maxUnobserved.Load() {
			}
			if call <= 2 {
				spec.admissionObserved(true)
				unobserved.Add(-1)
				<-ctx.Done()
				return kernel.Run{ID: schedulerRunID(t, byte(call))}, ctx.Err()
			}
			spec.admissionObserved(false)
			unobserved.Add(-1)
			return kernel.Run{}, fmt.Errorf("%w: empty", kernel.ErrConflict)
		},
		scheduledCompletion: func(kernel.Run) error { return nil },
	}
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	waitSchedulerCalls(t, &calls, 3)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 3 {
		t.Fatalf("no-admission probe spun: calls=%d", got)
	}
	if got := maxUnobserved.Load(); got != 1 {
		t.Fatalf("concurrent unobserved probes=%d", got)
	}
	// Pausing new work must not detach the two already-admitted owners.
	state, err := daemon.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.store.SetDispatch(context.Background(), state.Revision, false, schedulerTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("joined scheduler = %v", err)
	}
}

func TestSchedulerWaitsAfterNoAdmissionAndWakesOnce(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var calls atomic.Int64
	spec := SupervisorSpec{scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
		calls.Add(1)
		spec.admissionObserved(false)
		return kernel.Run{}, fmt.Errorf("%w: empty", kernel.ErrConflict)
	}}
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	waitSchedulerCalls(t, &calls, 1)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("idle scheduler spun: calls=%d", got)
	}
	daemon.notifyScheduler()
	waitSchedulerCalls(t, &calls, 2)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("single wake created extra probes: calls=%d", got)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("scheduler shutdown = %v", err)
	}
}

func TestSchedulerWakeAndPollCannotRaceHeldUnobservedProbe(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	polls := make(chan time.Time, 16)
	started := make(chan struct{})
	var once sync.Once
	var calls, unobserved, maxUnobserved atomic.Int64
	spec := SupervisorSpec{
		schedulerPoll: polls,
		scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
			calls.Add(1)
			present := unobserved.Add(1)
			for maximum := maxUnobserved.Load(); present > maximum && !maxUnobserved.CompareAndSwap(maximum, present); maximum = maxUnobserved.Load() {
			}
			once.Do(func() { close(started) })
			<-ctx.Done()
			unobserved.Add(-1)
			return kernel.Run{}, ctx.Err()
		},
	}
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	<-started
	for index := 0; index < cap(polls); index++ {
		daemon.notifyScheduler()
		polls <- time.Now()
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("wake/poll raced held probe: calls=%d", got)
	}
	if got := maxUnobserved.Load(); got != 1 {
		t.Fatalf("concurrent unobserved probes=%d", got)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("scheduler shutdown = %v", err)
	}
}

func TestSchedulerCancellationBeforeAdmissionObservation(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	for iteration := 0; iteration < 32; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		polling, returning := make(chan struct{}), make(chan struct{})
		polls := make(chan time.Time, 1)
		polls <- time.Now()
		clockCalls := 0
		daemon.now = func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return time.Now()
			}
			close(polling)
			<-returning
			// Let the canceled probe enqueue completion while the scheduler
			// is still handling its poll, so both select inputs are ready.
			runtime.Gosched()
			return time.Now()
		}
		spec := SupervisorSpec{schedulerPoll: polls, scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
			<-polling
			cancel()
			close(returning)
			return kernel.Run{}, ctx.Err()
		}}
		err := daemon.RunScheduler(ctx, spec)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: canceled unobserved probe = %v", iteration, err)
		}
	}
}

func TestSchedulerUnobservedCompletionPreservesUnexpectedErrors(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopping=%t", stopping), func(t *testing.T) {
			daemon := newSchedulerTestDaemon(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := error(context.Canceled)
			if stopping {
				cause = errors.New("unrelated probe failure")
			}
			spec := SupervisorSpec{scheduledAttempt: func(context.Context, SupervisorSpec) (kernel.Run, error) {
				if stopping {
					cancel()
				}
				return kernel.Run{}, cause
			}}
			if err := daemon.RunScheduler(ctx, spec); !errors.Is(err, kernel.ErrCorruptState) || !errors.Is(err, cause) {
				t.Fatalf("unobserved completion lost unexpected failure: %v", err)
			}
		})
	}
}

func TestSchedulerUnobservedCompletionPreservesJoinedFailures(t *testing.T) {
	sentinel := errors.New("admission reconciliation failed")
	for _, test := range []struct {
		name    string
		outcome error
		failure bool
	}{
		{"joined cancellation and failure", errors.Join(context.Canceled, sentinel), true},
		{"wrapped unknown joined outcome", kernel.NewOutcomeUnknownError(errors.Join(context.Canceled, sentinel)), true},
		{"unknown pure cancellation", kernel.NewOutcomeUnknownError(context.Canceled), true},
		{"joined interrupt and failure", errors.Join(sqlite3.INTERRUPT, sentinel), true},
		{"wrapped pure interrupt", fmt.Errorf("sqlite: %w", sqlite3.INTERRUPT), false},
		{"joined cancellation and interrupt", errors.Join(context.Canceled, sqlite3.INTERRUPT), false},
		{"nested pure cancellation", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", context.Canceled)), false},
		{"joined pure cancellation", errors.Join(context.Canceled, fmt.Errorf("wrapped: %w", context.Canceled)), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			daemon := newSchedulerTestDaemon(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			spec := SupervisorSpec{scheduledAttempt: func(context.Context, SupervisorSpec) (kernel.Run, error) {
				cancel()
				return kernel.Run{}, test.outcome
			}}
			err := daemon.RunScheduler(ctx, spec)
			if test.failure {
				if !errors.Is(err, test.outcome) || !errors.Is(err, kernel.ErrCorruptState) {
					t.Fatalf("joined failure lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("pure cancellation refused: %v", err)
			}
		})
	}
}

func TestSchedulerRejectsDuplicateAdmissionObservation(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	spec := SupervisorSpec{scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
		spec.admissionObserved(false)
		spec.admissionObserved(false)
		return kernel.Run{}, fmt.Errorf("%w: empty", kernel.ErrConflict)
	}}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, kernel.ErrCorruptState) {
		t.Fatalf("duplicate observation = %v", err)
	}
}

func TestSchedulerRejectsUnexpectedNoAdmissionResult(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	sentinel := errors.New("unexpected attempt error")
	spec := SupervisorSpec{scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
		spec.admissionObserved(false)
		return kernel.Run{}, sentinel
	}}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, sentinel) || !errors.Is(err, kernel.ErrCorruptState) {
		t.Fatalf("unexpected no-admission result = %v", err)
	}
}

func TestSchedulerPreservesUnsettledAttemptError(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	cause := errors.New("attempt settlement failed")
	var calls atomic.Int64
	spec := SupervisorSpec{scheduledAttempt: func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
		if calls.Add(1) == 1 {
			spec.admissionObserved(true)
			return kernel.Run{}, kernel.NewOutcomeUnknownError(cause)
		}
		<-ctx.Done()
		return kernel.Run{}, ctx.Err()
	}}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, cause) || !schedulerOutcomeUnknown(err) {
		t.Fatalf("unsettled attempt cause lost: %v", err)
	}
}

func TestSchedulerStopsAndJoinsAfterNonterminalCompletion(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	sentinel := errors.New("durable run remained nonterminal")
	var calls atomic.Int64
	joined := make(chan struct{})
	spec := SupervisorSpec{
		scheduledAttempt: func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
			if calls.Add(1) == 1 {
				spec.admissionObserved(true)
				return kernel.Run{ID: schedulerRunID(t, 1)}, nil
			}
			<-ctx.Done()
			close(joined)
			return kernel.Run{}, ctx.Err()
		},
		scheduledCompletion: func(kernel.Run) error { return sentinel },
	}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, sentinel) {
		t.Fatalf("nonterminal completion = %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("scheduler returned before joining its next probe")
	}
}

func TestSchedulerStopsAfterPersistentUncertainCompletionRead(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	sentinel := errors.New("durable completion unreadable")
	var attempts atomic.Int64
	var completionReads atomic.Int64
	daemon.scheduledRun = func(context.Context, kernel.RunID) (kernel.Run, bool, error) {
		completionReads.Add(1)
		return kernel.Run{}, false, kernel.NewOutcomeUnknownError(sentinel)
	}
	joined := make(chan struct{})
	spec := SupervisorSpec{
		scheduledAttempt: func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
			if attempts.Add(1) == 1 {
				spec.admissionObserved(true)
				return kernel.Run{ID: schedulerRunID(t, 1)}, nil
			}
			<-ctx.Done()
			close(joined)
			return kernel.Run{}, ctx.Err()
		},
	}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, sentinel) || completionReads.Load() != supervisorReconcileAttempts {
		t.Fatalf("persistent completion = %v after %d reads", err, completionReads.Load())
	}
	select {
	case <-joined:
	default:
		t.Fatal("scheduler returned before joining its next probe")
	}
}

func TestSchedulerHasOneProcessOwner(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	spec := SupervisorSpec{scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
		close(started)
		<-ctx.Done()
		return kernel.Run{}, ctx.Err()
	}}
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	<-started
	if err := daemon.RunScheduler(context.Background(), spec); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("second scheduler = %v", err)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("first scheduler shutdown = %v", err)
	}
}

func TestScheduledCompletionReloadsDurableRun(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	if err := daemon.validateScheduledCompletion("/nonexistent-change-parent", nil, kernel.Run{}); !schedulerOutcomeUnknown(err) {
		t.Fatalf("zero result = %v", err)
	}
	missing := kernel.Run{ID: schedulerRunID(t, 99)}
	if err := daemon.validateScheduledCompletion("/nonexistent-change-parent", nil, missing); !schedulerOutcomeUnknown(err) {
		t.Fatalf("missing result = %v", err)
	}

	ctx := context.Background()
	at := schedulerTime(t, 10)
	projectID := schedulerProjectID(t, 1)
	agentID := schedulerAgentID(t, 2)
	taskID := schedulerTaskID(t, 3)
	incarnationID := schedulerIncarnationID(t, 4)
	project, err := daemon.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "scheduler", Root: t.TempDir(), VerificationPolicy: kernel.VerificationNone}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 1}, schedulerTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: "queued"}, schedulerTime(t, 12)); err != nil {
		t.Fatal(err)
	}
	admission, err := daemon.store.AdmitNext(ctx, schedulerAdmissionKeys(t, 10), schedulerTime(t, 13))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	if err := daemon.validateScheduledCompletion("/nonexistent-change-parent", nil, *admission.Run); !schedulerOutcomeUnknown(err) {
		t.Fatalf("admitted result accepted = %v", err)
	}
}

func newSchedulerTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	store, err := createTestStore(context.Background(), filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{DispatchEnabled: true, Capacity: 2}, schedulerTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	daemon, err := newDaemon(store, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	return daemon
}

func waitSchedulerCalls(t *testing.T, calls *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("scheduler calls=%d, want at least %d", calls.Load(), want)
}

func waitSchedulerDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not join")
		return nil
	}
}

func schedulerRunID(t *testing.T, seed byte) kernel.RunID {
	t.Helper()
	id, err := kernel.RunIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerProjectID(t *testing.T, seed byte) kernel.ProjectID {
	t.Helper()
	id, err := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerAgentID(t *testing.T, seed byte) kernel.AgentID {
	t.Helper()
	id, err := kernel.AgentIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerTaskID(t *testing.T, seed byte) kernel.TaskID {
	t.Helper()
	id, err := kernel.TaskIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerIncarnationID(t *testing.T, seed byte) kernel.IncarnationID {
	t.Helper()
	id, err := kernel.IncarnationIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerResourceID(t *testing.T, seed byte) kernel.ResourceID {
	t.Helper()
	id, err := kernel.ResourceIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerSessionID(t *testing.T, seed byte) kernel.TerminalSessionID {
	t.Helper()
	id, err := kernel.TerminalSessionIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerChangeID(t *testing.T, seed byte) kernel.ChangeID {
	t.Helper()
	id, err := kernel.ChangeIDFromBytes(bytes.Repeat([]byte{seed}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func schedulerDigest(t *testing.T, seed byte) kernel.AttemptDigest {
	t.Helper()
	digest, err := kernel.AttemptDigestFromBytes(bytes.Repeat([]byte{seed}, kernel.DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func schedulerAdmissionKeys(t *testing.T, seed byte) kernel.AdmissionKeys {
	t.Helper()
	return kernel.AdmissionKeys{
		RunID: schedulerRunID(t, seed), TerminalSessionID: schedulerSessionID(t, seed+1), AttemptDigest: schedulerDigest(t, seed+2), CandidateChangeID: schedulerChangeID(t, seed+3),
		Resources:   kernel.AdmissionResourceIDs{RuntimeRoot: schedulerResourceID(t, seed+4), RunnerProcess: schedulerResourceID(t, seed+5), ProviderProcess: schedulerResourceID(t, seed+6), ProviderGroup: schedulerResourceID(t, seed+7)},
		RuntimeRoot: filepath.Join(t.TempDir(), schedulerRunID(t, seed).String()),
	}
}

func schedulerTime(t *testing.T, value int64) kernel.UnixMillis {
	t.Helper()
	at, err := kernel.NewUnixMillis(value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func schedulerOutcomeUnknown(err error) bool {
	var unknown *kernel.OutcomeUnknownError
	return errors.As(err, &unknown)
}

// A poll tick enqueues an idle agent's standing instruction once its quiet
// spell has passed, and spends one of its idle runs for it; the probe that
// follows the tick is what admits it.
func TestSchedulerTickEnqueuesStandingInstructions(t *testing.T) {
	store, err := createTestStore(context.Background(), filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{DispatchEnabled: true, Capacity: 2}, schedulerTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var clock atomic.Int64
	clock.Store(100_000)
	daemon, err := newDaemon(store, func() time.Time { return time.UnixMilli(clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 0x31))
	agentID, _ := kernel.AgentIDFromBytes(adapterID(t, 0x32))
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "idle", Root: t.TempDir()}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "idle agent", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 4}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, budget, instruction := kernel.IdleStandingInstruction, uint32(60), uint32(1), "Tidy up."
	ruled, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction, IdleRunBudget: &budget}, adapterTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	polls := make(chan time.Time, 4)
	spec := SupervisorSpec{
		schedulerPoll: polls,
		scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
			<-ctx.Done()
			return kernel.Run{}, ctx.Err()
		},
	}
	go func() { done <- daemon.RunScheduler(runCtx, spec) }()
	// Too soon: the rule was set at 12 ms and waits a minute.
	clock.Store(30_000)
	polls <- time.Now()
	time.Sleep(50 * time.Millisecond)
	if current, _, _ := store.Agent(ctx, agent.ID); current.Idle.RunsUsed != 0 {
		t.Fatalf("early tick spent an idle run: %+v", current.Idle)
	}
	clock.Store(70_000)
	polls <- time.Now()
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, _, err := store.Agent(ctx, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Idle.RunsUsed == 1 && current.Revision.Int64() == ruled.Revision.Int64()+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("due tick did not enqueue the standing instruction: %+v", current.Idle)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler shutdown = %v", err)
	}
}

func TestSchedulerCancellationDuringLimitPollJoinsOwners(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The clock is read inside enforceRunLimits, after the poll has been selected.
	daemon.now = func() time.Time { cancel(); return time.UnixMilli(1000) }
	polls := make(chan time.Time, 1)
	polls <- time.Now()
	joined := make(chan struct{})
	spec := SupervisorSpec{
		schedulerPoll: polls,
		scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
			<-ctx.Done()
			close(joined)
			return kernel.Run{}, ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatalf("cancelled limit poll = %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("scheduler did not join its owner")
	}
}

func TestSchedulerCancellationDoesNotHideLimitPollFailure(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon.now = func() time.Time { cancel(); return time.UnixMilli(-1) }
	polls := make(chan time.Time, 1)
	polls <- time.Now()
	spec := SupervisorSpec{schedulerPoll: polls, scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
		<-ctx.Done()
		return kernel.Run{}, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- daemon.RunScheduler(ctx, spec) }()
	if err := waitSchedulerDone(t, done); !errors.Is(err, kernel.ErrInvalidValue) {
		t.Fatalf("invalid clock during cancellation = %v", err)
	}
}

func TestSchedulerPreservesAdmittedRunWhenAttemptCompletionIsUnknown(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	runID := schedulerRunID(t, 7)
	sentinel := errors.New("completion remains unknown")
	var completed kernel.Run
	spec := SupervisorSpec{
		scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
			spec.admissionObserved(true)
			return kernel.Run{ID: runID, Phase: kernel.RunFinalizing}, kernel.NewOutcomeUnknownError(sentinel)
		},
		scheduledCompletion: func(run kernel.Run) error {
			completed = run
			return sentinel
		},
	}
	err := daemon.RunScheduler(context.Background(), spec)
	if !errors.Is(err, sentinel) || completed.ID != runID || completed.Phase != kernel.RunFinalizing {
		t.Fatalf("unknown completion lost admitted run: run=%+v err=%v", completed, err)
	}
}

func TestSchedulerPausedDispatchDefersAutomaticWorkUntilResume(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	store := daemon.store
	ctx := context.Background()
	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.SetDispatch(ctx, state.Revision, false, schedulerTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 0x71))
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "paused", Root: t.TempDir()}, schedulerTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	for index, role := range []kernel.AgentRole{kernel.RoleWorker, kernel.RoleOrchestrator} {
		id, _ := kernel.AgentIDFromBytes(adapterID(t, byte(0x72+index)))
		agent, err := store.CreateAgent(ctx, kernel.NewAgent{ID: id, ProjectID: project.ID, Name: "automatic", Role: role, Provider: kernel.ProviderShell, ToolBudgetLimit: 4}, schedulerTime(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		policy, after, instruction := kernel.IdleStandingInstruction, uint32(1), "Inspect current work."
		if _, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, schedulerTime(t, 5)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clockCalls := make(chan struct{}, 16)
	daemon.now = func() time.Time { clockCalls <- struct{}{}; return time.UnixMilli(100_000) }
	polls := make(chan time.Time)
	tick := func() {
		t.Helper()
		select {
		case polls <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler did not receive poll")
		}
	}
	var attempts atomic.Int64
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	spec := SupervisorSpec{schedulerPoll: polls, scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
		attempts.Add(1)
		spec.admissionObserved(false)
		return kernel.Run{}, fmt.Errorf("%w: fixture admission", kernel.ErrConflict)
	}}
	go func() { done <- daemon.RunScheduler(runCtx, spec) }()
	// Receiving the next unbuffered tick proves the preceding paused pass
	// completed, including run-limit enforcement, without a sleep.
	tick()
	tick()
	for index := 0; index < 3; index++ {
		select {
		case <-clockCalls:
		case <-time.After(5 * time.Second):
			t.Fatal("paused scheduler stopped enforcing limits")
		}
	}
	after, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head != before.Head || attempts.Load() != 0 {
		t.Fatalf("paused scheduler mutated or probed: before=%+v after=%+v attempts=%d", before, after, attempts.Load())
	}
	if _, err := store.SetDispatch(ctx, after.Revision, true, schedulerTime(t, 90_000)); err != nil {
		t.Fatal(err)
	}
	daemon.notifyScheduler()
	waitSchedulerCalls(t, &attempts, 1)
	tick()
	tick() // prior enabled tick completed both original enqueue paths
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 2 {
		t.Fatalf("resume lost automatic worker/overseer work: %+v", snapshot.Tasks)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatal(err)
	}
}
