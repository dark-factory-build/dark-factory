package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestChangeCheckpointsAreGuardedOneWayAndReplayable(t *testing.T) {
	store, change := ownedReservedChange(t)
	defer store.Close()
	baseline, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
	digest := changeTreeDigest(t, 0x22)
	selection, err := NewChangeSelection(format, commit, digest, MaxChangeTreeEntries, MaxChangeTreeBlobBytes, "/repository", repository)
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
	wrongSelection, _ := NewChangeSelection(format, wrongCommit, digest, MaxChangeTreeEntries, MaxChangeTreeBlobBytes, "/repository", repository)
	if _, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, wrongSelection, mustTime(t, 11)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting selection = %v", err)
	}
	for name, conflicting := range map[string]ChangeSelection{
		"commitment":  mustChangeSelection(t, format, commit, changeTreeDigest(t, 0x23), MaxChangeTreeEntries, MaxChangeTreeBlobBytes, repository),
		"entry count": mustChangeSelection(t, format, commit, digest, MaxChangeTreeEntries-1, MaxChangeTreeBlobBytes, repository),
		"total bytes": mustChangeSelection(t, format, commit, digest, MaxChangeTreeEntries, MaxChangeTreeBlobBytes-1, repository),
	} {
		if _, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, conflicting, mustTime(t, 11)); !errors.Is(err, ErrConflict) {
			t.Fatalf("conflicting selection %s = %v", name, err)
		}
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
	if state.Head.Int64() != baseline.Head.Int64()+3 {
		t.Fatalf("invalidation head = %d, want %d", state.Head.Int64(), baseline.Head.Int64()+3)
	}
}

func TestSelectedManifestSurvivesRestartAndGuardsRecoveryAvailability(t *testing.T) {
	store, change := ownedReservedChange(t)
	path := storePath(t, store)
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{0x31}, format.oidLength()))
	repository, _ := NewFileIdentity(41, 42)
	digest := changeTreeDigest(t, 0x32)
	selection := mustChangeSelection(t, format, commit, digest, 7, 19, repository)
	selected, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, selection, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if selected.Selection == nil || !changeSelectionEqual(*selected.Selection, selection) {
		t.Fatalf("selected facts = %+v", selected.Selection)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, found, err := store.Change(context.Background(), change.ID)
	if err != nil || !found || reopened.Selection == nil || !changeSelectionEqual(*reopened.Selection, selection) {
		t.Fatalf("reopened selection = %+v found=%v err=%v", reopened, found, err)
	}
	replay, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, selection, mustTime(t, 99))
	if err != nil || replay.Revision != selected.Revision || replay.UpdatedAt != selected.UpdatedAt {
		t.Fatalf("reopened selection replay = %+v, %v", replay, err)
	}
	stage, _ := NewFileIdentity(43, 44)
	prepared, err := store.RecordChangePrepared(context.Background(), change.ID, reopened.Revision, stage, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for name, observation := range map[string]ChangeAvailability{
		"commitment":  mustChangeAvailability(t, changeTreeDigest(t, 0x33), 7, 19, stage),
		"entry count": mustChangeAvailability(t, digest, 8, 19, stage),
		"total bytes": mustChangeAvailability(t, digest, 7, 20, stage),
	} {
		before := captureWriteFootprint(t, store)
		if _, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, observation, mustTime(t, 12)); !errors.Is(err, ErrConflict) {
			t.Fatalf("mismatched %s = %v", name, err)
		}
		if after := captureWriteFootprint(t, store); after != before {
			t.Fatalf("mismatched %s mutated authority: before=%+v after=%+v", name, before, after)
		}
	}
	observed := mustChangeAvailability(t, digest, 7, 19, stage)
	available, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, observed, mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	if available.Availability == nil || !changeAvailabilityEqual(*available.Availability, observed) || available.Selection == nil || !changeSelectionEqual(*available.Selection, selection) {
		t.Fatalf("available Change lost selected facts: %+v", available)
	}
	var storedDigest []byte
	var storedEntries, storedBytes int64
	if err := store.readers.QueryRow(`SELECT tree_digest, entry_count, total_bytes FROM changes WHERE id = ?`, change.ID.Bytes()).Scan(&storedDigest, &storedEntries, &storedBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedDigest, digest.Bytes()) || storedEntries != 7 || storedBytes != 19 {
		t.Fatalf("availability changed selected facts: digest=%x entries=%d bytes=%d", storedDigest, storedEntries, storedBytes)
	}
}

func TestPartialOrCorruptSelectedManifestFailsReadMutationAndOpen(t *testing.T) {
	mutations := map[string]string{
		"missing digest":    `UPDATE changes SET tree_digest = NULL WHERE id = ?`,
		"short digest":      `UPDATE changes SET tree_digest = zeroblob(31) WHERE id = ?`,
		"missing entries":   `UPDATE changes SET entry_count = NULL WHERE id = ?`,
		"negative entries":  `UPDATE changes SET entry_count = -1 WHERE id = ?`,
		"missing bytes":     `UPDATE changes SET total_bytes = NULL WHERE id = ?`,
		"overflowing bytes": `UPDATE changes SET total_bytes = 1073741825 WHERE id = ?`,
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			store, change := ownedReservedChange(t)
			path := storePath(t, store)
			selection := testChangeSelection(t)
			if _, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, selection, mustTime(t, 10)); err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, mutation, change.ID.Bytes())
			if _, _, err := store.Change(context.Background(), change.ID); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("corrupt selected manifest read = %v", err)
			}
			beforeWrite := captureWriteFootprint(t, store)
			stage, _ := NewFileIdentity(50, 51)
			if _, err := store.RecordChangePrepared(context.Background(), change.ID, mustRevision(t, 2), stage, mustTime(t, 11)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("corrupt selected manifest mutation = %v", err)
			}
			if afterWrite := captureWriteFootprint(t, store); afterWrite != beforeWrite {
				t.Fatalf("corrupt selected manifest changed authority: before=%+v after=%+v", beforeWrite, afterWrite)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			beforeOpen := captureDatabaseEvidence(t, path)
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Open corrupt selected manifest = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, beforeOpen)
		})
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
	digest := changeTreeDigest(t, 1)
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, format.oidLength()))
	repository, _ := NewFileIdentity(3, 4)
	if _, err := NewChangeSelection(format, commit, digest, MaxChangeTreeEntries+1, 0, "/repository", repository); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("selected entry bound = %v", err)
	}
	if _, err := NewChangeSelection(format, commit, digest, 0, MaxChangeTreeBlobBytes+1, "/repository", repository); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("selected byte bound = %v", err)
	}
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

func TestSelectedPhaseRequiresManifestFactsInSchema(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	change := seedReservedChange(t, store)
	_, err := store.writer.Exec(`UPDATE changes SET phase = 'selected', object_format = 'sha1', selected_commit = ?, repository_root = '/repository', repository_dev = 1, repository_inode = 2, selected_at_ms = 10, revision = 2, updated_at_ms = 10 WHERE id = ?`, bytes.Repeat([]byte{1}, 20), change.ID.Bytes())
	if err == nil {
		t.Fatal("selected phase accepted missing manifest facts")
	}
	fresh, found, readErr := store.Change(context.Background(), change.ID)
	if readErr != nil || !found || fresh.Phase != ChangeReserved || fresh.Revision != change.Revision {
		t.Fatalf("failed selected schema mutation changed Change: %+v found=%v err=%v", fresh, found, readErr)
	}
}

func TestAvailablePhaseRequiresCompleteSourceIdentityInSchema(t *testing.T) {
	for name, source := range map[string]struct {
		device any
		inode  any
	}{
		"missing source device": {device: nil, inode: int64(10)},
		"missing source inode":  {device: int64(9), inode: nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, change := ownedReservedChange(t)
			defer store.Close()
			selected, err := store.RecordChangeSelection(context.Background(), change.ID, change.Revision, testChangeSelection(t), mustTime(t, 10))
			if err != nil {
				t.Fatal(err)
			}
			stage, _ := NewFileIdentity(9, 10)
			prepared, err := store.RecordChangePrepared(context.Background(), change.ID, selected.Revision, stage, mustTime(t, 11))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.writer.Exec(`UPDATE changes SET phase = 'available', source_dev = ?, source_inode = ?, available_at_ms = 12, revision = revision + 1, updated_at_ms = 12 WHERE id = ?`, source.device, source.inode, change.ID.Bytes()); err == nil {
				t.Fatal("available phase accepted incomplete source identity")
			}
			fresh, found, readErr := store.Change(context.Background(), change.ID)
			if readErr != nil || !found || fresh.Phase != ChangePrepared || fresh.Revision != prepared.Revision {
				t.Fatalf("failed available schema mutation changed Change: %+v found=%v err=%v", fresh, found, readErr)
			}
		})
	}
}

func TestChangeLocatorsAreCanonicalAtConstructionAndRead(t *testing.T) {
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, 20))
	repository, _ := NewFileIdentity(1, 2)
	digest := changeTreeDigest(t, 1)
	for _, locator := range []string{"/repository/.", "/"} {
		if _, err := NewChangeSelection(format, commit, digest, 1, 1, locator, repository); !errors.Is(err, ErrInvalidValue) {
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

func changeTreeDigest(t testing.TB, seed byte) TreeDigest {
	t.Helper()
	digest, err := TreeDigestFromBytes(bytes.Repeat([]byte{seed}, DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func mustChangeSelection(t testing.TB, format ObjectFormat, commit CommitID, digest TreeDigest, entries uint32, totalBytes uint64, repository FileIdentity) ChangeSelection {
	t.Helper()
	selection, err := NewChangeSelection(format, commit, digest, entries, totalBytes, "/repository", repository)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func mustChangeAvailability(t testing.TB, digest TreeDigest, entries uint32, totalBytes uint64, source FileIdentity) ChangeAvailability {
	t.Helper()
	availability, err := NewChangeAvailability(digest, entries, totalBytes, source)
	if err != nil {
		t.Fatal(err)
	}
	return availability
}

func seedReservedChange(t *testing.T, store *Store) Change {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 31), Name: "p", Root: "/p"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 32), ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 2))
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

func ownedReservedChange(t *testing.T) (*Store, Change) {
	t.Helper()
	store, run, _ := admittedWorkerRun(t)
	change, found, err := store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found {
		store.Close()
		t.Fatalf("read admitted Change = %+v, %v", change, err)
	}
	return store, change
}

func assertChangeCheckpoint(t *testing.T, change Change, phase ChangePhase, revision int64) {
	t.Helper()
	if change.Phase != phase || change.Revision.Int64() != revision {
		t.Fatalf("Change checkpoint = %+v, want phase=%s revision=%d", change, phase.String(), revision)
	}
}
