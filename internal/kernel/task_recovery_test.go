package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestTaskRecoveryRefusesResourceCountBeyondBound(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()

	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err := resourcesForRunBounded(context.Background(), read.connection, run.ID, 3); !errors.Is(err, ErrRecoveryBounds) {
		t.Fatalf("bounded resources error = %v", err)
	}
}

// TestTaskRecoveryReadsHistoryBeyondSixtyFourRuns is the regression for the
// retired 64-run recovery ceiling: a real manager task reached work revision
// 65 and its operator recovery read failed while every other read still
// worked. Recovery must keep working at any history depth, keep the exact
// old/current run semantics, and still refuse a corrupted deep history.
func TestTaskRecoveryReadsHistoryBeyondSixtyFourRuns(t *testing.T) {
	const terminalRuns = 65
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	at := terminal.UpdatedAt.Int64()
	for revision := 2; revision <= terminalRuns; revision++ {
		at += 10
		queueRetrySameAgent(t, store, terminal.TaskID, at)
		admission, err := store.AdmitNext(context.Background(), historyKeys(t, revision), mustTime(t, at+1))
		if err != nil || !admission.Admitted() {
			t.Fatalf("revision %d admission = %+v, %v", revision, admission, err)
		}
		run := *admission.Run
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		identity, _ := NewPathResourceIdentity(1000+int64(revision), 302)
		if _, err := store.ActivateResource(context.Background(), run.ID, runtime.ID, runtime.Revision, identity, mustTime(t, at+2)); err != nil {
			t.Fatal(err)
		}
		finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, at+3))
		if err != nil {
			t.Fatal(err)
		}
		runtime = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		if _, err := store.ReleaseResource(context.Background(), run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, at+4)); err != nil {
			t.Fatal(err)
		}
		settlement, _ := NewAbandonedChangeSettlement(*run.AdmittedChangeRevision)
		terminal, err = store.FinalizeWorkerRun(context.Background(), run.ID, finalizing.Revision, settlement, mustTime(t, at+5))
		if err != nil {
			t.Fatal(err)
		}
	}
	if terminal.AdmittedTaskWorkRevision.Int64() != terminalRuns {
		t.Fatalf("history depth = %d", terminal.AdmittedTaskWorkRevision.Int64())
	}

	// A queued retry beyond the old ceiling reads the predecessor as its run.
	at += 10
	queueRetrySameAgent(t, store, terminal.TaskID, at)
	recovery, found, err := store.TaskRecovery(context.Background(), terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || !found || recovery.Task.Status != TaskQueued || recovery.Task.WorkRevision.Int64() != terminalRuns+1 || recovery.Run == nil || recovery.Run.ID != terminal.ID || recovery.Run.AdmittedTaskWorkRevision.Int64() != terminalRuns {
		t.Fatalf("queued retry recovery = %+v, found=%v, err=%v", recovery, found, err)
	}

	// The admitted retry becomes the current run at the same work revision.
	admission, err := store.AdmitNext(context.Background(), historyKeys(t, terminalRuns+1), mustTime(t, at+1))
	if err != nil || !admission.Admitted() {
		t.Fatalf("current admission = %+v, %v", admission, err)
	}
	recovery, found, err = store.TaskRecovery(context.Background(), terminal.TaskID, terminal.TaskIncarnationID)
	if err != nil || !found || recovery.Task.Status != TaskRunning || recovery.Run == nil || recovery.Run.ID != admission.Run.ID || recovery.Run.AdmittedTaskWorkRevision.Int64() != terminalRuns+1 || recovery.Task.WorkRevision != recovery.Run.AdmittedTaskWorkRevision {
		t.Fatalf("current run recovery = %+v, found=%v, err=%v", recovery, found, err)
	}

	// Corruption past the old ceiling is still refused, not skipped.
	corruptSQL(t, store, `DELETE FROM terminal_sessions WHERE run_id = ?`, terminal.ID.Bytes())
	if _, found, err := store.TaskRecovery(context.Background(), terminal.TaskID, terminal.TaskIncarnationID); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("corrupted history recovery = found=%v, err=%v", found, err)
	}
}

func queueRetrySameAgent(t *testing.T, store *Store, id TaskID, at int64) {
	t.Helper()
	task, found, err := store.Task(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
	if _, err := store.SendBackTask(context.Background(), id, task.Revision, "again", mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
}

func TestTaskRecoveryRefusesMismatchedIncarnation(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	wrong := incarnationID(t, 251)
	if wrong == run.TaskIncarnationID {
		t.Fatal("test incarnation unexpectedly matched")
	}
	if _, found, err := store.TaskRecovery(context.Background(), run.TaskID, wrong); err != nil || found {
		t.Fatalf("mismatched incarnation recovery = found=%v, err=%v", found, err)
	}
}

func TestTaskRecoveryReportsStaleHumanRequest(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	if _, err := store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, NewHumanQuestion{
		IdempotencyKey: humanKey(252), QuestionText: "operator recovery check",
	}, mustTime(t, 400)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(context.Background(), run.CredentialDigest, func() Proposal {
		proposal, _ := NewBlockedProposal("needs operator")
		return proposal
	}(), mustTime(t, 401)); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := store.TaskRecovery(context.Background(), run.TaskID, run.TaskIncarnationID)
	if err != nil || !found || !recovery.NeedsOperatorRecovery {
		t.Fatalf("stale human recovery = %+v, found=%v, err=%v", recovery, found, err)
	}
}

func TestLatestRunForTaskUsesWorkRevisionForEqualAdmissionTimes(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()

	// The retry has the lower ID, so an ID tie-breaker would incorrectly pick
	// the predecessor when both rows have the same admission timestamp.
	_, retryKeys := queueRetryForTerminalSeed(t, store, terminal, 45, 180)
	second, err := store.AdmitNext(context.Background(), retryKeys, mustTime(t, 45))
	if err != nil || !second.Admitted() {
		t.Fatalf("retry admission = %+v, %v", second, err)
	}
	task, found, err := store.Task(context.Background(), terminal.TaskID)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
	// Exercise the selection query directly with equal, scanner-valid run
	// timestamps. Other recovery tests cover complete lifecycle relationships.
	corruptSQL(t, store, `UPDATE runs SET admitted_at_ms = ?, updated_at_ms = ? WHERE id = ?`, terminal.AdmittedAt.Int64(), terminal.AdmittedAt.Int64(), second.Run.ID.Bytes())
	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	latest, found, err := latestRunForTask(context.Background(), read.connection, task)
	if err != nil || !found || latest.ID != second.Run.ID || latest.AdmittedTaskWorkRevision.Int64() != 2 {
		t.Fatalf("latest run = %+v, found=%v, err=%v", latest, found, err)
	}
}
