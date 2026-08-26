package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestChangeCheckpointsAreGuardedOneWayAndReplayable(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	change := seedReservedChange(t, store)
	format, err := NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewCommitID(format, bytes.Repeat([]byte{0x11}, 20))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := NewChangeSelection(format, commit, "/repository", repository)
	if err != nil {
		t.Fatal(err)
	}

	selected, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, selection, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	assertChangeCheckpoint(t, selected, ChangeSelected, 2)
	replayed, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, selection, mustTime(t, 99))
	if err != nil || replayed.Revision != selected.Revision || replayed.UpdatedAt != selected.UpdatedAt {
		t.Fatalf("selection replay = %+v, %v", replayed, err)
	}
	wrongCommit, _ := NewCommitID(format, bytes.Repeat([]byte{0x12}, 20))
	wrongSelection, _ := NewChangeSelection(format, wrongCommit, "/repository", repository)
	if _, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, wrongSelection, mustTime(t, 11)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting selection = %v", err)
	}

	stage, _ := NewFileIdentity(9, 10)
	prepared, err := store.RecordChangePrepared(context.Background(), change.ID, selected.Revision, stage, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	assertChangeCheckpoint(t, prepared, ChangePrepared, 3)
	preparedReplay, err := store.RecordChangePrepared(context.Background(), change.ID, selected.Revision, stage, mustTime(t, 100))
	if err != nil || preparedReplay.Revision != prepared.Revision || preparedReplay.UpdatedAt != prepared.UpdatedAt {
		t.Fatalf("prepared replay = %+v, %v", preparedReplay, err)
	}

	digest, _ := TreeDigestFromBytes(bytes.Repeat([]byte{0x22}, DigestBytes))
	availableFacts, err := NewChangeAvailability(digest, MaxChangeTreeEntries, MaxChangeTreeBlobBytes, stage)
	if err != nil {
		t.Fatal(err)
	}
	available, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, availableFacts, mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	assertChangeCheckpoint(t, available, ChangeAvailable, 4)
	availableReplay, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, availableFacts, mustTime(t, 101))
	if err != nil || availableReplay.Revision != available.Revision || availableReplay.UpdatedAt != available.UpdatedAt {
		t.Fatalf("available replay = %+v, %v", availableReplay, err)
	}
	wrongStage, _ := NewFileIdentity(9, 11)
	wrongAvailable, _ := NewChangeAvailability(digest, 1, 1, wrongStage)
	if _, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, wrongAvailable, mustTime(t, 13)); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement source identity = %v", err)
	}
	if _, err := store.RecordChangePrepared(context.Background(), change.ID, available.Revision, stage, mustTime(t, 14)); !errors.Is(err, ErrConflict) {
		t.Fatalf("reverse available->prepared = %v", err)
	}

	state, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Head.Int64() != 6 { // project, agent, task, and three Change transitions.
		t.Fatalf("invalidation head = %d, want 6", state.Head.Int64())
	}
}

func TestChangeCheckpointBoundsAndOrderingFailBeforeMutation(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	change := seedReservedChange(t, store)
	stage, _ := NewFileIdentity(1, 2)
	if _, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, stage, mustTime(t, 5)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("prepared before selection = %v", err)
	}
	digest, _ := TreeDigestFromBytes(bytes.Repeat([]byte{1}, DigestBytes))
	if _, err := NewChangeAvailability(digest, MaxChangeTreeEntries+1, 0, stage); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("entry bound = %v", err)
	}
	if _, err := NewChangeAvailability(digest, 0, MaxChangeTreeBlobBytes+1, stage); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("byte bound = %v", err)
	}
	if _, err := NewCommitID(ObjectSHA1, bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("object format bound = %v", err)
	}
	fresh, found, err := store.Change(context.Background(), change.ID)
	if err != nil || !found || fresh.Phase != ChangeReserved || fresh.Revision.Int64() != 1 {
		t.Fatalf("failed checkpoints changed durable Change = %+v, %v", fresh, err)
	}
}

func TestChangeLocatorsAreCanonicalAtConstructionAndRead(t *testing.T) {
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, 20))
	repository, _ := NewFileIdentity(1, 2)
	for _, locator := range []string{"/repository/.", "/"} {
		if _, err := NewChangeSelection(format, commit, locator, repository); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("selection locator %q = %v", locator, err)
		}
	}

	store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
	path := storePath(t, store)
	_, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "canonical"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	reservation := &ChangeReservation{ID: changeID(t, 203), SourceRoot: "/change/source", StagingRoot: "/change/stage"}
	if _, err := store.AdmitNext(context.Background(), agent.ID, admissionKeys(t, 204, reservation), mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE changes SET staging_root = '/change/stage/.' WHERE id = ?`, reservation.ID.Bytes())
	if _, _, err := store.Change(context.Background(), reservation.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("unclean durable Change read = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureDatabaseEvidence(t, path)
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open unclean Change = %v", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, before)
}

func seedReservedChange(t *testing.T, store *Store) Change {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 31), Name: "p", Root: "/p"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 32), ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 2}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 33), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 34), Title: "t"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	id := changeID(t, 35)
	if _, err := store.writer.Exec(`INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, source_root, staging_root, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'reserved', '/changes/source', '/changes/staging', 1, 4, 4)`, id.Bytes(), project.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes()); err != nil {
		t.Fatal(err)
	}
	change, found, err := store.Change(ctx, id)
	if err != nil || !found {
		t.Fatalf("read reserved Change = %+v, %v", change, err)
	}
	return change
}

func assertChangeCheckpoint(t *testing.T, change Change, phase ChangePhase, revision int64) {
	t.Helper()
	if change.Phase != phase || change.Revision.Int64() != revision {
		t.Fatalf("Change checkpoint = %+v, want phase=%s revision=%d", change, phase.String(), revision)
	}
}
