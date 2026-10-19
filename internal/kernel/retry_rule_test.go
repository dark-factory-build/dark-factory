package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// retryCallers are the three callers of RetryTask: the overseer's task update
// --retry (a digest), the operator's (none), and an intake issue's own retry.
func retryCallers(t *testing.T, store *Store, project ProjectID, at int64) map[string]func(Task, int64) (Task, error) {
	t.Helper()
	ctx := context.Background()
	digest := runningOverseerDigest(t, store, project, at)
	return map[string]func(Task, int64) (Task, error){
		"overseer": func(task Task, at int64) (Task, error) {
			return store.RetryTask(ctx, &digest, task.ID, task.Revision, AgentID{}, mustTime(t, at), false)
		},
		"operator": func(task Task, at int64) (Task, error) {
			return store.RetryTask(ctx, nil, task.ID, task.Revision, AgentID{}, mustTime(t, at), false)
		},
		"intake": func(task Task, at int64) (Task, error) {
			return store.RetryTask(ctx, nil, task.ID, task.Revision, AgentID{}, mustTime(t, at), true)
		},
	}
}

// endRunAgain admits the queued worker task's next run and ends it as factoryd
// ends a run that failed before its session, with failure, abandoning its
// Change. Its IDs take seed to seed+26.
func endRunAgain(t *testing.T, store *Store, seed byte, at int64, failure Proposal) {
	t.Helper()
	ctx := context.Background()
	candidate := changeID(t, seed+6)
	keys := admissionKeys(t, seed, &candidate)
	keys.RuntimeRoot = fmt.Sprintf("/runtime/again-%d", seed)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, at))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	endAdmittedRun(t, store, *admission.Run, seed, at+1, failure)
}

// endAdmittedRun fails the admitted worker run before its session and settles
// it, abandoning its Change.
func endAdmittedRun(t *testing.T, store *Store, run Run, seed byte, at int64, failure Proposal) {
	t.Helper()
	ctx := context.Background()
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	identity, _ := NewPathResourceIdentity(300+int64(seed), 400+int64(seed))
	if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, identity, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	runtime = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, at+2)); err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeTestRun(t, store, finalizing, at+3); err != nil {
		t.Fatal(err)
	}
}

// A retained Change whose head advances after two identical ends is new code:
// its retry runs. While the head stands, the refusal stands.
func TestRetryRecoversWhenTheRetainedHeadAdvances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	failed, _ := NewFailureProposal(FailureInternal, "crashed")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, failed)
	defer store.Close()
	if _, err := finalizeTestRun(t, store, finalizing, 60); err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Task(ctx, finalizing.TaskID)
	if err != nil || task.Status != TaskFailed {
		t.Fatalf("failed task = %+v, %v", task, err)
	}
	if task, err = store.RetryTask(ctx, nil, task.ID, task.Revision, AgentID{}, mustTime(t, 70), false); err != nil || task.Status != TaskQueued {
		t.Fatalf("first retry = %+v, %v", task, err)
	}
	endRunAgain(t, store, 110, 80, failed)
	if task, _, err = store.Task(ctx, task.ID); err != nil || task.Status != TaskFailed || task.WorkRevision.Int64() != 2 {
		t.Fatalf("task after two identical ends = %+v, %v", task, err)
	}
	retained, _, err := store.Change(ctx, *finalizing.ChangeID)
	if err != nil || retained.Phase != ChangeRetained || retained.HeadCommit == nil {
		t.Fatalf("retained Change = %+v, %v", retained, err)
	}
	if _, err := store.RetryTask(ctx, nil, task.ID, task.Revision, AgentID{}, mustTime(t, 100), false); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry at the same head = %v, want ErrConflict", err)
	}
	newHead, _ := NewCommitID(retained.HeadCommit.format, bytes.Repeat([]byte{0x7a}, retained.HeadCommit.format.oidLength()))
	if _, err := store.RecordChangeRebased(ctx, retained.ID, retained.Revision, *retained.HeadCommit, newHead, mustTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	for name, retry := range retryCallers(t, store, task.ProjectID, 102) {
		if _, err := retry(task, 110); err != nil && !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("%s retry after the head advanced = %v", name, err)
		}
	}
	if task, _, err = store.Task(ctx, task.ID); err != nil || task.Status != TaskQueued || task.WorkRevision.Int64() != 3 {
		t.Fatalf("task after the head advanced = %+v, %v", task, err)
	}
}

// One rule for every retry: a task whose last two runs ended in the same
// failure class is not retried by the overseer, the operator or its intake
// issue, however the two details are worded (task b63dc3f5 ran 19 times on
// two such classes, #2102). A change of class is retried, and once the class
// changes back the rule applies again.
func TestRetryRefusesTheSameEndClassTwiceForEveryCaller(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	premise1, _ := NewFailureProposal(FailureInternal, "premise not reproduced: the task cites codexModelCapacity at live_attempt_darwin.go:835")
	premise2, _ := NewFailureProposal(FailureInternal, "  Premise not reproduced: live_attempt_darwin.go:835 holds no such symbol")
	transport, _ := NewFailureProposal(FailureProtocol, "local API transport failed: connection refused")
	for _, name := range []string{"overseer", "operator", "intake"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The first run failed "cleanup": the other class.
			store, terminal, _ := terminalPreRunningWorker(t)
			defer store.Close()
			retry := retryCallers(t, store, terminal.ProjectID, 35)[name]
			task, _, err := store.Task(ctx, terminal.TaskID)
			if err != nil || task.Status != TaskFailed {
				t.Fatalf("failed task = %+v, %v", task, err)
			}
			// A different class is retried.
			if task, err = retry(task, 50); err != nil || task.Status != TaskQueued || task.WorkRevision.Int64() != 2 {
				t.Fatalf("retry after the first failure = %+v, %v", task, err)
			}
			endRunAgain(t, store, 110, 60, premise1)
			if task, _, err = store.Task(ctx, task.ID); err != nil || task.Status != TaskFailed {
				t.Fatalf("task after premise dispute = %+v, %v", task, err)
			}
			if task, err = retry(task, 70); err != nil || task.Status != TaskQueued || task.WorkRevision.Int64() != 3 {
				t.Fatalf("retry after a change of class = %+v, %v", task, err)
			}
			// The same class again, worded differently: refused, by class, not text.
			endRunAgain(t, store, 131, 80, premise2)
			task, _, err = store.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retry(task, 90); !errors.Is(err, ErrConflict) {
				t.Fatalf("retry of a repeated premise dispute = %v, want ErrConflict", err)
			}
			if current, _, err := store.Task(ctx, task.ID); err != nil || current.Revision != task.Revision || current.Status != TaskFailed {
				t.Fatalf("refused task changed: %+v, %v", current, err)
			}
			// A send-back is new work: it is the way past the rule.
			if sent, err := store.SendBackTask(ctx, nil, task.ID, task.Revision, "", "the symbol moved to project_limits.go", mustTime(t, 95)); err != nil || sent.Status != TaskQueued || sent.WorkRevision.Int64() != 4 {
				t.Fatalf("send-back after two identical ends = %+v, %v", sent, err)
			}
			endRunAgain(t, store, 152, 100, transport)
			task, _, err = store.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The class changed from the premise dispute: retried once more.
			if task, err = retry(task, 110); err != nil || task.Status != TaskQueued || task.WorkRevision.Int64() != 5 {
				t.Fatalf("retry after a transport failure = %+v, %v", task, err)
			}
			endRunAgain(t, store, 173, 120, transport)
			task, _, err = store.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retry(task, 130); !errors.Is(err, ErrConflict) {
				t.Fatalf("retry of a repeated transport failure = %v, want ErrConflict", err)
			}
		})
	}
}

// A transient failure's automatic requeue follows the same rule: a second
// transient run of the same class, whatever its detail, ends the task, and no
// caller's retry starts a third.
func TestTransientRequeueStopsAtTheSameEndClass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	capacity, _ := NewFailureProposal(FailureTransient, ProviderCapacityRunDetail)
	neverStarted, _ := NewFailureProposal(FailureTransient, NeverStartedRunDetail)
	store, run, _ := admittedWorkerRun(t)
	defer store.Close()
	endAdmittedRun(t, store, run, 70, 20, capacity)
	task, _, err := store.Task(ctx, run.TaskID)
	if err != nil || task.Status != TaskQueued || task.WorkRevision.Int64() != 2 {
		t.Fatalf("task after one capacity failure = %+v, %v, want queued at work revision 2", task, err)
	}
	endRunAgain(t, store, 110, 30, neverStarted)
	if task, _, err = store.Task(ctx, task.ID); err != nil || task.Status != TaskFailed || task.WorkRevision.Int64() != 2 {
		t.Fatalf("task after two transient failures = %+v, %v, want failed at work revision 2", task, err)
	}
	for name, retry := range retryCallers(t, store, task.ProjectID, 35) {
		if _, err := retry(task, 80); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s retry after two transient failures = %v, want ErrConflict", name, err)
		}
	}
}

// Concurrent retries of one failed task admit exactly one next run: the
// losers see the revision they named gone, and the task moves one work
// revision, never two.
func TestConcurrentRetriesStartOneRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	task, _, err := store.Task(ctx, terminal.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	results := make([]error, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = store.RetryTask(ctx, nil, task.ID, task.Revision, AgentID{}, mustTime(t, 50), false)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrRevisionConflict), errors.Is(err, ErrConflict):
		default:
			t.Fatalf("concurrent retry = %v", err)
		}
	}
	retried, _, err := store.Task(ctx, task.ID)
	if err != nil || won != 1 || retried.Status != TaskQueued || retried.WorkRevision.Int64() != 2 || retried.Revision.Int64() != task.Revision.Int64()+1 {
		t.Fatalf("won=%d task=%+v, %v", won, retried, err)
	}
}
