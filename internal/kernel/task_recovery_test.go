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

func TestTaskRecoveryRefusesTopologyBeyondBound(t *testing.T) {
	store, terminal, retry := retryAdmittedWorker(t, 50, 60)
	defer store.Close()
	task, found, err := store.Task(context.Background(), terminal.TaskID)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}

	read, err := store.beginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if err := validateTaskRunTopologyBounded(context.Background(), read.connection, task, 1); !errors.Is(err, ErrRecoveryBounds) {
		t.Fatalf("bounded topology error = %v (retry=%s)", err, retry.ID)
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
