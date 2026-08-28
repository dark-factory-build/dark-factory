package kernel

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestNullableDurableBlobsPreservePresenceAndFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		corrupt    func(*testing.T, *Store)
		directRead func(*testing.T, *Store) error
	}{
		{
			name: "Change base commit",
			corrupt: func(t *testing.T, store *Store) {
				change := seedReservedChange(t, store)
				corruptSQL(t, store, `UPDATE changes SET base_commit = zeroblob(0) WHERE id = ?`, change.ID.Bytes())
			},
			directRead: func(t *testing.T, store *Store) error {
				_, _, err := store.Change(context.Background(), changeID(t, 35))
				return err
			},
		},
		{
			name: "Change tree digest",
			corrupt: func(t *testing.T, store *Store) {
				change := seedReservedChange(t, store)
				corruptSQL(t, store, `UPDATE changes SET tree_digest = zeroblob(0) WHERE id = ?`, change.ID.Bytes())
			},
			directRead: func(t *testing.T, store *Store) error {
				_, _, err := store.Change(context.Background(), changeID(t, 35))
				return err
			},
		},
		{
			name: "run Change ID",
			corrupt: func(t *testing.T, store *Store) {
				seedDurableAuthority(t, store)
				corruptSQL(t, store, `UPDATE runs SET change_id = zeroblob(0) WHERE id = ?`, runID(t, 5).Bytes())
			},
			directRead: func(t *testing.T, store *Store) error {
				_, _, err := store.Run(context.Background(), runID(t, 5))
				return err
			},
		},
		{
			name: "resource birth digest",
			corrupt: func(t *testing.T, store *Store) {
				seedDurableAuthority(t, store)
				corruptSQL(t, store, `UPDATE resources SET birth_digest = zeroblob(0) WHERE id = ?`, resourceID(t, 6).Bytes())
			},
			directRead: func(t *testing.T, store *Store) error {
				_, _, err := store.Resource(context.Background(), resourceID(t, 6))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, path := newTestStore(t)
			test.corrupt(t, store)

			if err := test.directRead(t, store); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("direct read of present zero-length BLOB = %v", err)
			}
			beforeWrite := captureWriteFootprint(t, store)
			if _, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 100)); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("validated mutation over present zero-length BLOB = %v", err)
			}
			if afterWrite := captureWriteFootprint(t, store); afterWrite != beforeWrite {
				store.Close()
				t.Fatalf("validated refusal changed durable footprint: before=%+v after=%+v", beforeWrite, afterWrite)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			beforeOpen := captureDatabaseEvidence(t, path)
			if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("Open with present zero-length BLOB = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, beforeOpen)
		})
	}
}

func TestNullableBlobRejectsWrongSQLiteStorageClass(t *testing.T) {
	var value nullableBlob
	if err := value.Scan(nil); err != nil || value.valid {
		t.Fatalf("NULL scan = valid %v, error %v", value.valid, err)
	}
	if err := value.Scan([]byte{}); err != nil || !value.valid || len(value.bytes) != 0 {
		t.Fatalf("zero-length BLOB scan = valid %v, bytes %x, error %v", value.valid, value.bytes, err)
	}
	for _, source := range []any{"", int64(0), float64(0)} {
		if err := value.Scan(source); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Scan(%T) = %v", source, err)
		}
		if value.valid {
			t.Fatalf("Scan(%T) retained presence", source)
		}
	}
}

func TestEveryPublicMutationValidatesDurableGraphBeforeDecision(t *testing.T) {
	selection := testChangeSelection(t)
	stage, _ := NewFileIdentity(70, 80)
	digest, _ := TreeDigestFromBytes(bytes.Repeat([]byte{0x81}, DigestBytes))
	availability, _ := NewChangeAvailability(digest, 1, 1, stage)
	pathIdentity, _ := NewPathResourceIdentity(90, 100)
	processIdentity := processIdentity(t, 101)
	failure, _ := NewFailureProposal(FailureInternal, "failure")
	proposal, _ := NewSuccessProposal("result")
	exit, _ := NewProcessExitCode(1, 0, mustTime(t, 90))
	at := mustTime(t, 100)

	tests := []struct {
		name   string
		invoke func(*Store) error
	}{
		{name: "CreateProject", invoke: func(store *Store) error {
			_, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 220), Name: "new", Root: "/new-project"}, at)
			return err
		}},
		{name: "CreateAgent", invoke: func(store *Store) error {
			_, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 221), ProjectID: projectID(t, 1), Name: "new", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 1}, at)
			return err
		}},
		{name: "EnqueueTask", invoke: func(store *Store) error {
			_, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 222), ProjectID: projectID(t, 1), AssignedAgentID: agentID(t, 2), IncarnationID: incarnationID(t, 223), Title: "new"}, at)
			return err
		}},
		{name: "SetDispatch", invoke: func(store *Store) error {
			_, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, at)
			return err
		}},
		{name: "SetCapacity", invoke: func(store *Store) error {
			_, err := store.SetCapacity(context.Background(), mustRevision(t, 1), 2, at)
			return err
		}},
		{name: "AdmitNext", invoke: func(store *Store) error {
			_, err := store.AdmitNext(context.Background(), admissionKeys(t, 224, nil), at)
			return err
		}},
		{name: "RecordChangePrepared", invoke: func(store *Store) error {
			_, err := store.RecordChangePrepared(context.Background(), changeID(t, 225), mustRevision(t, 1), selection, stage, at)
			return err
		}},
		{name: "MarkChangeAvailable", invoke: func(store *Store) error {
			_, err := store.MarkChangeAvailable(context.Background(), changeID(t, 225), mustRevision(t, 1), availability, at)
			return err
		}},
		{name: "ActivateResource", invoke: func(store *Store) error {
			_, err := store.ActivateResource(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), pathIdentity, at)
			return err
		}},
		{name: "BeginResourceRelease", invoke: func(store *Store) error {
			_, err := store.BeginResourceRelease(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), ResourceIdentity{}, at)
			return err
		}},
		{name: "MarkResourceUnresolved", invoke: func(store *Store) error {
			_, err := store.MarkResourceUnresolved(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), ResourceIdentity{}, "reason", at)
			return err
		}},
		{name: "ReleaseResource", invoke: func(store *Store) error {
			_, err := store.ReleaseResource(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), ResourceIdentity{}, at)
			return err
		}},
		{name: "ActivateRun", invoke: func(store *Store) error {
			runIDValue := runID(t, 5)
			session, found, err := store.TerminalSession(context.Background(), terminalSessionID(t, 15))
			if err != nil {
				return err
			}
			if !found {
				return ErrCorruptState
			}
			_, err = store.ActivateRun(context.Background(), runIDValue, session.ID, mustRevision(t, 1), session.Revision, at)
			return err
		}},
		{name: "ProposeAttemptOutcome", invoke: func(store *Store) error {
			attempt, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{0x5a}, DigestBytes))
			_, err := store.ProposeAttemptOutcome(context.Background(), attempt, proposal, at)
			return err
		}},
		{name: "FailRun", invoke: func(store *Store) error {
			_, err := store.FailRun(context.Background(), runID(t, 5), mustRevision(t, 1), failure, at)
			return err
		}},
		{name: "CancelRun", invoke: func(store *Store) error {
			_, err := store.CancelRun(context.Background(), runID(t, 5), mustRevision(t, 1), "cancel", at)
			return err
		}},
		{name: "BeginRunnerStart", invoke: func(store *Store) error {
			_, _, err := store.BeginRunnerStart(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), at)
			return err
		}},
		{name: "ActivateRunner", invoke: func(store *Store) error {
			_, _, err := store.ActivateRunner(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), processIdentity, at)
			return err
		}},
		{name: "RecordUnregisteredRunnerConverged", invoke: func(store *Store) error {
			_, err := store.RecordUnregisteredRunnerConverged(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), at)
			return err
		}},
		{name: "RecordRecoveredPreExecRunnerAbsence", invoke: func(store *Store) error {
			_, err := store.RecordRecoveredPreExecRunnerAbsence(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), processIdentity, at)
			return err
		}},
		{name: "RecordRecoveredRunnerAbsence", invoke: func(store *Store) error {
			_, _, err := store.RecordRecoveredRunnerAbsence(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), processIdentity, at)
			return err
		}},
		{name: "RecordLiveRunnerExitAndRelease", invoke: func(store *Store) error {
			_, _, err := store.RecordLiveRunnerExitAndRelease(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), processIdentity, exit, at)
			return err
		}},
		{name: "ConsumeAttemptResult", invoke: func(store *Store) error {
			attempt, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{0x5b}, DigestBytes))
			proof, _ := ResultProofDigestFromBytes(bytes.Repeat([]byte{0x5c}, DigestBytes))
			result, err := NewInnerUnregisteredConvergedAttemptResult(runID(t, 5), attempt, proof, pathIdentity)
			if err != nil {
				return err
			}
			_, err = store.ConsumeAttemptResult(context.Background(), result, mustRevision(t, 1), at)
			return err
		}},
		{name: "CloseTerminalAfterRunner", invoke: func(store *Store) error {
			attempt, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{0x5b}, DigestBytes))
			proof, _ := ResultProofDigestFromBytes(bytes.Repeat([]byte{0x5c}, DigestBytes))
			result, err := NewInnerUnregisteredConvergedAttemptResult(runID(t, 5), attempt, proof, pathIdentity)
			if err != nil {
				return err
			}
			_, _, err = store.CloseTerminalAfterRunner(context.Background(), result, mustRevision(t, 1), mustRevision(t, 1), at)
			return err
		}},
		{name: "MarkProviderResourcesUnresolved", invoke: func(store *Store) error {
			_, _, _, err := store.MarkProviderResourcesUnresolved(context.Background(), runID(t, 5), resourceID(t, 6), resourceID(t, 7), mustRevision(t, 1), mustRevision(t, 1), mustRevision(t, 1), processIdentity, "reason", at)
			return err
		}},
		{name: "ReleaseProviderResources", invoke: func(store *Store) error {
			_, _, _, err := store.ReleaseProviderResources(context.Background(), runID(t, 5), resourceID(t, 6), resourceID(t, 7), mustRevision(t, 1), mustRevision(t, 1), mustRevision(t, 1), ResourceIdentity{}, at)
			return err
		}},
		{name: "FailRunWithRuntimeAbsent", invoke: func(store *Store) error {
			_, err := store.FailRunWithRuntimeAbsent(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), mustRevision(t, 1), failure, at)
			return err
		}},
		{name: "FinalizeRun", invoke: func(store *Store) error {
			_, err := store.FinalizeRun(context.Background(), runID(t, 5), mustRevision(t, 1), at)
			return err
		}},
	}
	corruptions := []struct {
		name   string
		mutate func(*Store)
	}{
		{name: "graph", mutate: func(store *Store) {
			corruptSQL(t, store, `UPDATE tasks SET status = 'queued' WHERE id = ?`, taskID(t, 3).Bytes())
		}},
		{name: "control", mutate: func(store *Store) {
			corruptSQL(t, store, `UPDATE agents SET provider = 'unknown' WHERE id = ?`, agentID(t, 2).Bytes())
		}},
		{name: "invalidation", mutate: func(store *Store) {
			corruptSQL(t, store, `UPDATE invalidations SET sequence = 100 WHERE sequence = 1`)
		}},
		{name: "runner exit kind", mutate: func(store *Store) {
			corruptSQL(t, store, `UPDATE runs SET runner_exit_kind = 'unknown', runner_exit_sequence = 1, runner_exit_at_ms = updated_at_ms WHERE id = ?`, runID(t, 5).Bytes())
		}},
		{name: "runner exit shape", mutate: func(store *Store) {
			corruptSQL(t, store, `UPDATE runs SET runner_exit_kind = 'recovered_absence', runner_exit_sequence = 1, runner_exit_code = 0, runner_exit_at_ms = updated_at_ms WHERE id = ?`, runID(t, 5).Bytes())
		}},
	}

	for _, test := range tests {
		for _, corruption := range corruptions {
			t.Run(test.name+"/"+corruption.name, func(t *testing.T) {
				store, _ := newTestStore(t)
				defer store.Close()
				seedDurableAuthority(t, store)
				corruption.mutate(store)
				before := captureWriteFootprint(t, store)
				if err := test.invoke(store); !errors.Is(err, ErrCorruptState) {
					t.Fatalf("mutation under corrupt durable state = %v", err)
				}
				if after := captureWriteFootprint(t, store); after != before {
					t.Fatalf("validation failure changed durable footprint: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestResourceActivationAndReplayRefusePreexistingGraphCorruption(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "first transition", true: "idempotent replay"}[replay], func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			seedDurableAuthority(t, store)
			identity, _ := NewPathResourceIdentity(101, 102)
			if replay {
				if _, err := store.ActivateResource(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), identity, mustTime(t, 10)); err != nil {
					t.Fatal(err)
				}
			}
			corruptSQL(t, store, `UPDATE tasks SET status = 'queued' WHERE id = ?`, taskID(t, 3).Bytes())
			before := captureWriteFootprint(t, store)
			if _, err := store.ActivateResource(context.Background(), runID(t, 5), resourceID(t, 6), mustRevision(t, 1), identity, mustTime(t, 20)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("ActivateResource = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				t.Fatalf("activation changed corrupt graph: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestChangePreparedAndReplayRefusePreexistingOwnershipCorruption(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "first checkpoint", true: "idempotent replay"}[replay], func(t *testing.T) {
			store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
			defer store.Close()
			_, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 230), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 231), Title: "worker"}, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			candidate := changeID(t, 232)
			keys := admissionKeys(t, 233, &candidate)
			keys.RuntimeRoot = "/checkpoint/runtime"
			if admission, err := store.AdmitNext(context.Background(), keys, mustTime(t, 10)); err != nil || !admission.Admitted() {
				t.Fatalf("admission = %+v, %v", admission, err)
			}
			selection := testChangeSelection(t)
			if replay {
				tree, _ := NewFileIdentity(70, 80)
				if _, err := store.RecordChangePrepared(context.Background(), candidate, mustRevision(t, 1), selection, tree, mustTime(t, 11)); err != nil {
					t.Fatal(err)
				}
			}
			corruptSQL(t, store, `UPDATE resources SET path = '/' WHERE kind = 'runtime_root'`)
			before := captureWriteFootprint(t, store)
			tree, _ := NewFileIdentity(70, 80)
			if _, err := store.RecordChangePrepared(context.Background(), candidate, mustRevision(t, 1), selection, tree, mustTime(t, 20)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("RecordChangePrepared = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				t.Fatalf("checkpoint changed corrupt graph: before=%+v after=%+v", before, after)
			}
		})
	}
}

type writeFootprint struct {
	dispatch, capacity, factoryRevision, next, floor, factoryUpdated int64
	projects, projectRevisions                                       int64
	agents, agentRevisions                                           int64
	tasks, taskRevisions                                             int64
	changes, changeRevisions                                         int64
	runs, runRevisions                                               int64
	resources, resourceRevisions                                     int64
	invalidations, invalidationSequences                             int64
}

func captureWriteFootprint(t *testing.T, store *Store) writeFootprint {
	t.Helper()
	var result writeFootprint
	err := store.readers.QueryRow(`SELECT
		dispatch_enabled, capacity, revision, next_invalidation_sequence, invalidation_floor, updated_at_ms,
		(SELECT COUNT(*) FROM projects), (SELECT COALESCE(SUM(revision), 0) FROM projects),
		(SELECT COUNT(*) FROM agents), (SELECT COALESCE(SUM(revision), 0) FROM agents),
		(SELECT COUNT(*) FROM tasks), (SELECT COALESCE(SUM(revision), 0) FROM tasks),
		(SELECT COUNT(*) FROM changes), (SELECT COALESCE(SUM(revision), 0) FROM changes),
		(SELECT COUNT(*) FROM runs), (SELECT COALESCE(SUM(revision), 0) FROM runs),
		(SELECT COUNT(*) FROM resources), (SELECT COALESCE(SUM(revision), 0) FROM resources),
		(SELECT COUNT(*) FROM invalidations), (SELECT COALESCE(SUM(sequence), 0) FROM invalidations)
		FROM factory WHERE singleton = 1`).Scan(
		&result.dispatch, &result.capacity, &result.factoryRevision, &result.next, &result.floor, &result.factoryUpdated,
		&result.projects, &result.projectRevisions, &result.agents, &result.agentRevisions,
		&result.tasks, &result.taskRevisions, &result.changes, &result.changeRevisions,
		&result.runs, &result.runRevisions, &result.resources, &result.resourceRevisions,
		&result.invalidations, &result.invalidationSequences,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testChangeSelection(t *testing.T) ChangeSelection {
	t.Helper()
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{0x71}, format.oidLength()))
	repository, _ := NewFileIdentity(61, 62)
	selection, err := NewChangeSelection(format, commit, changeTreeDigest(t, 0x81), 1, 1, repository)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func BenchmarkWriteEntryValidation(b *testing.B) {
	store := benchmarkValidationStore(b)
	defer store.Close()
	ctx := context.Background()
	for name, begin := range map[string]func(context.Context) (*writeTx, error){
		"unchecked": store.beginUncheckedWrite,
		"validated": store.beginValidatedWrite,
	} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				tx, err := begin(ctx)
				if err != nil {
					b.Fatal(err)
				}
				if err := tx.Rollback(nil); err != nil {
					b.Fatal(err)
				}
				tx.Close()
			}
		})
	}
}

func benchmarkValidationStore(b *testing.B) *Store {
	b.Helper()
	store, err := createTestStore(context.Background(), filepath.Join(b.TempDir(), "kernel.db"), FactoryConfig{DispatchEnabled: true, Capacity: 2}, UnixMillis{})
	if err != nil {
		b.Fatal(err)
	}
	id := func(seed byte) []byte { return bytes.Repeat([]byte{seed}, IDBytes) }
	projectID, _ := ProjectIDFromBytes(id(1))
	agentID, _ := AgentIDFromBytes(id(2))
	taskID, _ := TaskIDFromBytes(id(3))
	incarnationID, _ := IncarnationIDFromBytes(id(4))
	runID, _ := RunIDFromBytes(id(5))
	changeID, _ := ChangeIDFromBytes(id(6))
	resourceID := func(seed byte) ResourceID {
		value, _ := ResourceIDFromBytes(id(seed))
		return value
	}
	at, _ := NewUnixMillis(1)
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID, Name: "p", Root: "/project"}, at)
	if err != nil {
		b.Fatal(err)
	}
	agent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID, ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 1}, at)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: "t"}, at); err != nil {
		b.Fatal(err)
	}
	attempt, _ := AttemptDigestFromBytes(bytes.Repeat([]byte{7}, DigestBytes))
	keys := AdmissionKeys{
		RunID: runID, TerminalSessionID: func() TerminalSessionID { value, _ := TerminalSessionIDFromBytes(id(12)); return value }(), AttemptDigest: attempt,
		CandidateChangeID: changeID,
		RuntimeRoot:       "/runtime/run",
		Resources: AdmissionResourceIDs{
			RuntimeRoot: resourceID(8), RunnerProcess: resourceID(9),
			ProviderProcess: resourceID(10), ProviderGroup: resourceID(11),
		},
	}
	if result, err := store.AdmitNext(context.Background(), keys, at); err != nil || !result.Admitted() {
		b.Fatalf("admission = %+v, %v", result, err)
	}
	return store
}
