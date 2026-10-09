package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// These tests pin the intake invariant: only the exact durable accepted
// revision of an external issue can reach a task body (and so a provider
// prompt). Later edits, replayed older snapshots and withdrawn receipts must
// never materialize or amend work.

const intakeExactSourceSuffix = "\n\nSource: https://github.com/owner/repository/issues/7\nFACTORY_SOURCE owner/repository#7"

func intakeExactRevisionStore(t *testing.T) (*Store, IntakeSource) {
	t.Helper()
	store, _, project, _, _ := newSharedQueueStore(t, 4)
	t.Cleanup(func() { _ = store.Close() })
	id, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{250}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	return store, source
}

func requireIntakeTaskUnchanged(t *testing.T, store *Store, want Task) {
	t.Helper()
	got, found, err := store.Task(context.Background(), want.ID)
	if err != nil || !found || got.Title != want.Title || got.Body != want.Body || got.IncarnationID != want.IncarnationID || got.WorkRevision != want.WorkRevision {
		t.Fatalf("accepted task changed: got %+v (found=%v, err=%v), want %+v", got, found, err, want)
	}
}

func TestIntakeEditAfterAcceptanceNeverChangesQueuedOrRunningTaskBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, source := intakeExactRevisionStore(t)
	reviewed := intakeSnapshotForTest()
	accepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, reviewed, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 11), source)
	if err != nil || queued.Status != TaskQueued || queued.Body != reviewed.Body+intakeExactSourceSuffix {
		t.Fatalf("imported task = %+v, %v", queued, err)
	}

	// An unreviewed edit is only ever reported as changed content.
	unreviewed := reviewed
	unreviewed.Body = "unreviewed edit"
	if got := PreviewIntake(source, unreviewed, &accepted); got != IntakeContentChanged {
		t.Fatalf("unreviewed edit preview = %q", got)
	}
	// Even an explicitly accepted edit is a new receipt for new work: the
	// queued task keeps the bytes it was created from, including on replay.
	edited := reviewed
	edited.Title, edited.Body = "Edited title", "explicitly accepted edit"
	newer, err := store.AcceptIntakeSnapshot(ctx, source.ID, edited, mustTime(t, 12))
	if err != nil || newer.TaskID == accepted.TaskID {
		t.Fatalf("accepted edit = %+v, %v", newer, err)
	}
	if replay, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 13), source); err != nil || replay.ID != queued.ID {
		t.Fatalf("queued replay = %+v, %v", replay, err)
	}
	requireIntakeTaskUnchanged(t, store, queued)

	admitted, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 14))
	if err != nil || !admitted.Admitted() || admitted.Run.TaskID != queued.ID {
		t.Fatalf("admission = %+v, %v", admitted, err)
	}
	running, _, err := store.Task(ctx, queued.ID)
	if err != nil || running.Status != TaskRunning {
		t.Fatalf("running task = %+v, %v", running, err)
	}
	later := edited
	later.Body = "accepted while the first revision runs"
	if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, later, mustTime(t, 15)); err != nil {
		t.Fatal(err)
	}
	if replay, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 16), source); err != nil || replay.ID != queued.ID || replay.Status != TaskRunning {
		t.Fatalf("running replay = %+v, %v", replay, err)
	}
	requireIntakeTaskUnchanged(t, store, running)
	if bound, found, err := store.IntakeAcceptanceForTask(ctx, running.ID); err != nil || !found || bound.ID != accepted.ID || bound.Snapshot.Body != reviewed.Body {
		t.Fatalf("running task bound to %+v (found=%v), %v", bound, found, err)
	}
}

func TestIntakeStaleSnapshotCannotReimportOrAmendAcceptedWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, source := intakeExactRevisionStore(t)
	older := intakeSnapshotForTest()
	first, err := store.AcceptIntakeSnapshot(ctx, source.ID, older, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	newerSnapshot := older
	newerSnapshot.Body = "newer reviewed body"
	newer, err := store.AcceptIntakeSnapshot(ctx, source.ID, newerSnapshot, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}

	// The older hash, never imported, is superseded and cannot materialize.
	if _, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 12), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded receipt imported: %v", err)
	}
	if _, found, err := store.Task(ctx, first.TaskID); err != nil || found {
		t.Fatalf("superseded receipt created a task: found=%v err=%v", found, err)
	}
	// Replaying the older snapshot, at any time, cannot make it latest again.
	for _, at := range []int64{9, 11, 20} {
		if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, older, mustTime(t, at)); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("replayed older snapshot at %d: %v", at, err)
		}
	}
	if latest, found, err := store.LatestIntakeAcceptance(ctx, older, source.ProjectID, source.TargetRepositoryID); err != nil || !found || latest.ID != newer.ID {
		t.Fatalf("latest after replay = %+v, %v", latest, err)
	}
	if pending, err := store.PendingIntakeAcceptances(ctx, source.ID, 25); err != nil || len(pending) != 1 || pending[0].ID != newer.ID {
		t.Fatalf("pending after replay = %+v, %v", pending, err)
	}

	task, err := store.ImportIntakeAcceptance(ctx, newer.ID, mustTime(t, 21), source)
	if err != nil || task.Body != newerSnapshot.Body+intakeExactSourceSuffix {
		t.Fatalf("latest import = %+v, %v", task, err)
	}
	// After the newer work exists, the stale receipt still cannot import or
	// amend it, and the stored receipt bytes are unchanged.
	if _, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 22), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale receipt imported after newer work: %v", err)
	}
	if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, older, mustTime(t, 23)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale snapshot accepted after newer work: %v", err)
	}
	requireIntakeTaskUnchanged(t, store, task)
	if stored, found, err := store.IntakeAcceptance(ctx, first.ID); err != nil || !found || stored.Snapshot.Body != older.Body || stored.WithdrawnAt != nil {
		t.Fatalf("stale receipt mutated: %+v, %v", stored, err)
	}
}

func TestIntakeWithdrawnAcceptanceCannotMaterialize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, source := intakeExactRevisionStore(t)
	snapshot := intakeSnapshotForTest()
	accepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 12)); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn receipt imported: %v", err)
	}
	if _, err := store.ImportIntakeAcceptanceWithPriority(ctx, accepted.ID, mustTime(t, 12), source, 5); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn receipt imported with priority: %v", err)
	}
	// Re-accepting identical content returns the withdrawn receipt, never a
	// fresh one that could import.
	again, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 13))
	if err != nil || again.ID != accepted.ID || again.WithdrawnAt == nil {
		t.Fatalf("re-accepted withdrawn snapshot = %+v, %v", again, err)
	}
	if pending, err := store.PendingIntakeAcceptances(ctx, source.ID, 25); err != nil || len(pending) != 0 {
		t.Fatalf("withdrawn receipt pending = %+v, %v", pending, err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 14), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-accepted withdrawn receipt imported: %v", err)
	}
	if _, found, err := store.Task(ctx, accepted.TaskID); err != nil || found {
		t.Fatalf("withdrawn receipt created a task: found=%v err=%v", found, err)
	}

	// Withdrawal after import keeps the queued task from ever being admitted.
	other := snapshot
	other.IssueNumber, other.NodeID = 8, "I_kwDOOther"
	imported, err := store.AcceptIntakeSnapshot(ctx, source.ID, other, mustTime(t, 15))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ImportIntakeAcceptance(ctx, imported.ID, mustTime(t, 16), source)
	if err != nil || task.Status != TaskQueued {
		t.Fatalf("import = %+v, %v", task, err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, imported.ID, mustTime(t, 17)); err != nil {
		t.Fatal(err)
	}
	if result, err := store.AdmitNext(ctx, admissionKeys(t, 40, nil), mustTime(t, 18)); err != nil || result.Admitted() {
		t.Fatalf("withdrawn task admitted: %+v, %v", result, err)
	}
	if replay, err := store.ImportIntakeAcceptance(ctx, imported.ID, mustTime(t, 19), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn imported receipt replayed: %+v, %v", replay, err)
	}
	requireIntakeTaskUnchanged(t, store, task)
}
