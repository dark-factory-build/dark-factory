package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestChangePreparedAndAvailableAreExactReplayableCheckpoints(t *testing.T) {
	store, change := ownedReservedChange(t)
	defer store.Close()
	selection := testChangeSelection(t)
	tree, _ := NewFileIdentity(9, 10)
	prepared, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, selection, tree, mustTime(t, 10))
	if err != nil || prepared.Phase != ChangePrepared || prepared.Revision.Int64() != 2 || prepared.Selection == nil || prepared.TreeIdentity == nil {
		t.Fatalf("prepared = %+v, %v", prepared, err)
	}
	replay, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, selection, tree, mustTime(t, 99))
	if err != nil || replay.Revision != prepared.Revision || replay.UpdatedAt != prepared.UpdatedAt {
		t.Fatalf("prepared replay = %+v, %v", replay, err)
	}
	wrong := selection
	wrong.commitment = changeTreeDigest(t, 0x44)
	if _, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, wrong, tree, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflicting prepared replay = %v", err)
	}
	wrong = selection
	wrong.repository, _ = NewFileIdentity(91, 92)
	if _, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, wrong, tree, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflicting repository replay = %v", err)
	}
	availableFacts := mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes, tree)
	available, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, availableFacts, mustTime(t, 12))
	if err != nil || available.Phase != ChangeAvailable || available.Revision.Int64() != 3 || available.AvailableAt == nil {
		t.Fatalf("available = %+v, %v", available, err)
	}
	replay, err = store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, availableFacts, mustTime(t, 100))
	if err != nil || replay.Revision != available.Revision || replay.UpdatedAt != available.UpdatedAt {
		t.Fatalf("available replay = %+v, %v", replay, err)
	}
	if _, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, selection, tree, mustTime(t, 13)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("intermediate replay after progress = %v", err)
	}
}

func TestChangePreparedFactsSurviveRestartAndGuardAvailability(t *testing.T) {
	store, change := ownedReservedChange(t)
	path := storePath(t, store)
	selection := testChangeSelection(t)
	tree, _ := NewFileIdentity(43, 44)
	prepared, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, selection, tree, mustTime(t, 10))
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
	reopened, found, err := store.Change(context.Background(), change.ID)
	if err != nil || !found || reopened.Selection == nil || !changeSelectionEqual(*reopened.Selection, selection) || reopened.TreeIdentity == nil || *reopened.TreeIdentity != tree {
		t.Fatalf("reopened = %+v, found=%v, err=%v", reopened, found, err)
	}
	for name, observation := range map[string]ChangeAvailability{
		"commitment":  mustChangeAvailability(t, changeTreeDigest(t, 0x33), selection.entries, selection.bytes, tree),
		"entry count": mustChangeAvailability(t, selection.commitment, selection.entries+1, selection.bytes, tree),
		"total bytes": mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes+1, tree),
	} {
		before := captureWriteFootprint(t, store)
		if _, err := store.MarkChangeAvailable(context.Background(), change.ID, prepared.Revision, observation, mustTime(t, 12)); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("mismatched %s = %v", name, err)
		}
		if after := captureWriteFootprint(t, store); after != before {
			t.Fatalf("mismatched %s mutated authority: before=%+v after=%+v", name, before, after)
		}
	}
}

func TestChangeSchemaIsPathFreeCanonicalAndCircularlyBound(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	rows, err := store.readers.Query(`PRAGMA table_info(changes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, forbidden := range []string{"source_root", "staging_root", "selected_commit", "repository_root", "selected_at_ms", "stage_dev", "source_dev"} {
		if columns[forbidden] {
			t.Fatalf("obsolete Change column survived: %s", forbidden)
		}
	}
	for _, required := range []string{"base_commit", "repository_dev", "repository_inode", "tree_dev", "tree_inode", "settled_run_id"} {
		if !columns[required] {
			t.Fatalf("missing Change column: %s", required)
		}
	}
	var canonical int
	if err := store.readers.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('changes') WHERE name = 'changes_task_incarnation_unique' AND "unique" = 1`).Scan(&canonical); err != nil || canonical != 1 {
		t.Fatalf("canonical index = %d, err=%v", canonical, err)
	}
	settlementFK := map[string]string{}
	foreignKeys, err := store.readers.Query(`PRAGMA foreign_key_list(changes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignKeys.Close()
	for foreignKeys.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := foreignKeys.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "runs" {
			settlementFK[from] = to
		}
	}
	wantSettlementFK := map[string]string{"settled_run_id": "id", "id": "change_id", "project_id": "project_id", "task_id": "task_id", "task_incarnation_id": "task_incarnation_id"}
	if len(settlementFK) != len(wantSettlementFK) {
		t.Fatalf("settlement Run FK = %+v", settlementFK)
	}
	for from, to := range wantSettlementFK {
		if settlementFK[from] != to {
			t.Fatalf("settlement Run FK = %+v", settlementFK)
		}
	}
}

func TestPartialPreparedFactsFailClosed(t *testing.T) {
	for name, mutation := range map[string]string{
		"missing digest":              `UPDATE changes SET tree_digest = NULL WHERE id = ?`,
		"short digest":                `UPDATE changes SET tree_digest = zeroblob(31) WHERE id = ?`,
		"missing tree identity":       `UPDATE changes SET tree_inode = NULL WHERE id = ?`,
		"missing repository identity": `UPDATE changes SET repository_inode = NULL WHERE id = ?`,
		"invalid repository identity": `UPDATE changes SET repository_dev = -1 WHERE id = ?`,
		"negative entries":            `UPDATE changes SET entry_count = -1 WHERE id = ?`,
	} {
		t.Run(name, func(t *testing.T) {
			store, change := ownedReservedChange(t)
			path := storePath(t, store)
			tree, _ := NewFileIdentity(50, 51)
			if _, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, testChangeSelection(t), tree, mustTime(t, 10)); err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, mutation, change.ID.Bytes())
			if _, _, err := store.Change(context.Background(), change.ID); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("corrupt read = %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			before := captureDatabaseEvidence(t, path)
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("Open = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, before)
		})
	}
}

func TestRepositoryIdentitySchemaRequiresExactPairedStoreIntegers(t *testing.T) {
	store, change := ownedReservedChange(t)
	defer store.Close()
	tree, _ := NewFileIdentity(50, 51)
	prepared, err := store.RecordChangePrepared(context.Background(), change.ID, change.Revision, testChangeSelection(t), tree, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"missing inode":   `UPDATE changes SET repository_inode = NULL WHERE id = ?`,
		"negative device": `UPDATE changes SET repository_dev = -1 WHERE id = ?`,
		"zero inode":      `UPDATE changes SET repository_inode = 0 WHERE id = ?`,
	} {
		if _, err := store.writer.Exec(mutation, change.ID.Bytes()); err == nil {
			t.Fatalf("%s repository identity passed schema", name)
		}
	}
	unchanged, found, err := store.Change(context.Background(), change.ID)
	if err != nil || !found || unchanged.Selection == nil || prepared.Selection == nil || !changeSelectionEqual(*unchanged.Selection, *prepared.Selection) {
		t.Fatalf("rejected repository corruption changed Change: %+v found=%v err=%v", unchanged, found, err)
	}
}

func TestRetryAdmissionUsesCanonicalChangeAndIgnoresFreshCandidate(t *testing.T) {
	store, terminal, _, keys := retryQueuedWorker(t, 40)
	defer store.Close()
	before, found, err := store.Change(context.Background(), *terminal.ChangeID)
	if err != nil || !found || before.Phase != ChangeAbandoned || before.SettledRunID == nil || *before.SettledRunID != terminal.ID {
		t.Fatalf("settled predecessor Change = %+v, found=%v, err=%v", before, found, err)
	}
	if keys.CandidateChangeID == before.ID {
		t.Fatal("test candidate unexpectedly equals canonical Change")
	}
	admission, err := store.AdmitNext(context.Background(), keys, mustTime(t, 40))
	if err != nil || !admission.Admitted() || admission.Run.ChangeID == nil || admission.Run.AdmittedChangeRevision == nil {
		t.Fatalf("retry admission = %+v, %v", admission, err)
	}
	if *admission.Run.ChangeID != before.ID || admission.Run.AdmittedChangeRevision.Int64() != before.Revision.Int64()+1 {
		t.Fatalf("retry Change binding = %+v, prior=%+v", admission.Run, before)
	}
	if _, found, err := store.Change(context.Background(), keys.CandidateChangeID); err != nil || found {
		t.Fatalf("ignored candidate exists: found=%v err=%v", found, err)
	}
	after, found, err := store.Change(context.Background(), before.ID)
	if err != nil || !found || after.Phase != ChangeReserved || after.SettledRunID != nil || after.Revision != *admission.Run.AdmittedChangeRevision {
		t.Fatalf("reopened canonical Change = %+v, found=%v, err=%v", after, found, err)
	}
}

func TestOrchestratorAdmissionIgnoresCandidateWithoutCreatingChange(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 180), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 181), Title: "orchestrate"}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 182, nil)
	admission, err := store.AdmitNext(context.Background(), keys, mustTime(t, 10))
	if err != nil || !admission.Admitted() || admission.Run.ChangeID != nil || admission.Run.AdmittedChangeRevision != nil {
		t.Fatalf("orchestrator admission = %+v, %v", admission, err)
	}
	if _, found, err := store.Change(context.Background(), keys.CandidateChangeID); err != nil || found {
		t.Fatalf("orchestrator candidate exists: found=%v err=%v", found, err)
	}
}

func TestWorkerSettlementIsExactAndHistoricalFinalizationReplaySurvivesRetry(t *testing.T) {
	blocked, _ := NewBlockedProposal("retry")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, blocked)
	defer store.Close()
	change, found, err := store.Change(context.Background(), *finalizing.ChangeID)
	if err != nil || !found || change.Selection == nil || change.TreeIdentity == nil || change.Phase != ChangeAvailable {
		t.Fatalf("available Change = %+v, found=%v, err=%v", change, found, err)
	}
	availability := mustChangeAvailability(t, change.Selection.commitment, change.Selection.entries, change.Selection.bytes, *change.TreeIdentity)
	wrongRevision, _ := NewRetainedChangeSettlement(mustRevision(t, change.Revision.Int64()+1), availability)
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeWorkerRun(context.Background(), finalizing.ID, finalizing.Revision, wrongRevision, mustTime(t, 80)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong Change revision settlement = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("wrong settlement mutated state: before=%+v after=%+v", before, after)
	}
	settlement, _ := NewRetainedChangeSettlement(change.Revision, availability)
	terminal, err := store.FinalizeWorkerRun(context.Background(), finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80))
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("terminal settlement = %+v, %v", terminal, err)
	}
	_, retryKeys := queueRetryForTerminal(t, store, terminal, 81)
	retry, err := store.AdmitNext(context.Background(), retryKeys, mustTime(t, 81))
	if err != nil || !retry.Admitted() {
		t.Fatalf("retry admission = %+v, %v", retry, err)
	}
	replayed, err := store.FinalizeWorkerRun(context.Background(), terminal.ID, finalizing.Revision, settlement, mustTime(t, 1))
	if err != nil || replayed.ID != terminal.ID || replayed.Revision != terminal.Revision {
		t.Fatalf("historical finalization replay = %+v, %v", replayed, err)
	}
}

func TestNonterminalWorkerCannotSettleChange(t *testing.T) {
	failed, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, failed)
	defer store.Close()
	if _, err := store.writer.Exec(`UPDATE changes SET phase = 'retained', settled_run_id = ?, revision = revision + 1, updated_at_ms = 70 WHERE id = ?`, finalizing.ID.Bytes(), finalizing.ChangeID.Bytes()); err != nil {
		t.Fatal(err)
	}
	connection, err := store.readerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := validateChanges(context.Background(), connection); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("nonterminal Change settlement validation = %v", err)
	}
}

func changeTreeDigest(t testing.TB, seed byte) TreeDigest {
	t.Helper()
	digest, err := TreeDigestFromBytes(bytes.Repeat([]byte{seed}, DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	return digest
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
	if _, err := store.writer.Exec(`INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'reserved', 1, 4, 4)`, id.Bytes(), project.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes()); err != nil {
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
