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
