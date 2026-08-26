package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestRunningAuthorityRejectsCorruptTaskRelationships(t *testing.T) {
	tests := map[string]string{
		"queued state":        `UPDATE tasks SET status = 'queued'`,
		"terminal state":      `UPDATE tasks SET status = 'failed', completed_at_ms = 99`,
		"project":             `UPDATE tasks SET project_id = X'91919191919191919191919191919191'`,
		"incarnation":         `UPDATE tasks SET incarnation_id = X'92929292929292929292929292929292'`,
		"work revision":       `UPDATE tasks SET work_revision = work_revision + 1`,
		"resource membership": `UPDATE resources SET run_id = X'93939393939393939393939393939393' WHERE kind = 'runtime_root'`,
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			store, run, keys := runningOrchestratorRun(t)
			path := storePath(t, store)
			corruptSQL(t, store, mutation)
			assertCorruptRunningAuthority(t, store, run, keys)
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

func TestRunningAuthorityRejectsCorruptChangeRelationships(t *testing.T) {
	tests := map[string]string{
		"project":     `UPDATE changes SET project_id = X'94949494949494949494949494949494'`,
		"task":        `UPDATE changes SET task_id = X'95959595959595959595959595959595'`,
		"incarnation": `UPDATE changes SET task_incarnation_id = X'96969696969696969696969696969696'`,
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			store, run, keys := runningWorkerRun(t)
			defer store.Close()
			corruptSQL(t, store, mutation)
			assertCorruptRunningAuthority(t, store, run, keys)
		})
	}
}

func TestTerminalRunRejectsMismatchedTaskOutcome(t *testing.T) {
	proposal, _ := NewSuccessProposal("verified result")
	store, finalizing := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, proposal)
	path := storePath(t, store)
	terminal, err := store.FinalizeRun(context.Background(), finalizing.ID, finalizing.Revision, mustTime(t, 80))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE tasks SET status = 'failed', result = NULL WHERE id = ?`, terminal.TaskID.Bytes())
	if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Snapshot = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureDatabaseEvidence(t, path)
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open = %v", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, before)
}

func TestInjectedConfiguredWorkerSuccessTerminalFailsClosed(t *testing.T) {
	proposal, _ := NewSuccessProposal("unverified")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationGoWorkspaceTest, proposal)
	defer store.Close()
	corruptSQL(t, store, `UPDATE runs SET phase = 'terminal', terminal_kind = proposal_kind, terminal_code = proposal_code, terminal_detail = proposal_detail, terminal_result = proposal_result, terminal_at_ms = 90, revision = revision + 1, updated_at_ms = 90 WHERE id = ?`, finalizing.ID.Bytes())
	corruptSQL(t, store, `UPDATE tasks SET status = 'succeeded', result = 'unverified', completed_at_ms = 90, revision = revision + 1, updated_at_ms = 90 WHERE id = ?`, finalizing.TaskID.Bytes())
	if _, _, err := store.Run(context.Background(), finalizing.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Snapshot = %v", err)
	}
}

func TestAdmissionRejectsAllOwnershipLocatorOverlap(t *testing.T) {
	for name, mutate := range map[string]func(*AdmissionKeys){
		"source contains staging": func(keys *AdmissionKeys) { keys.Change.StagingRoot = keys.Change.SourceRoot + "/stage" },
		"runtime equals source":   func(keys *AdmissionKeys) { keys.RuntimeRoot = keys.Change.SourceRoot },
		"runtime contains staging": func(keys *AdmissionKeys) {
			keys.RuntimeRoot = "/owned"
			keys.Change.StagingRoot = "/owned/stage"
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, _, project, agent := newAdmissionStore(t, RoleWorker, 4)
			defer store.Close()
			_, _ = store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 180), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 181), Title: "overlap"}, mustTime(t, 5))
			keys := admissionKeys(t, 182, &ChangeReservation{ID: changeID(t, 183), SourceRoot: "/owned/source", StagingRoot: "/owned/staging"})
			mutate(&keys)
			before := admissionFootprint(t, store)
			if _, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10)); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("AdmitNext = %v", err)
			}
			if after := admissionFootprint(t, store); after != before {
				t.Fatalf("footprint before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAdmissionRejectsOverlapWithDurableRuntimeAndRetainedChange(t *testing.T) {
	store, _, project, firstAgent := newAdmissionStore(t, RoleWorker, 4)
	defer store.Close()
	_, _ = store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 190), ProjectID: project.ID, AssignedAgentID: firstAgent.ID, IncarnationID: incarnationID(t, 191), Title: "first"}, mustTime(t, 5))
	firstKeys := admissionKeys(t, 192, &ChangeReservation{ID: changeID(t, 193), SourceRoot: "/retained/source", StagingRoot: "/retained/staging"})
	firstKeys.RuntimeRoot = "/runtime/retained"
	first, err := store.AdmitNext(context.Background(), firstAgent.ID, firstKeys, mustTime(t, 10))
	if err != nil || !first.Admitted() {
		t.Fatalf("first admission = %+v, %v", first, err)
	}
	failure, _ := NewFailureProposal(FailureSpawn, "startup failed")
	finalizing, err := store.FailAdmitted(context.Background(), first.Run.ID, first.Run.Revision, failure, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resourcesForRunTest(t, store, first.Run.ID) {
		if _, err := store.ReleaseResource(context.Background(), first.Run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 12)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinalizeRun(context.Background(), first.Run.ID, finalizing.Revision, mustTime(t, 13)); err != nil {
		t.Fatal(err)
	}
	secondAgent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 194), ProjectID: project.ID, Name: "second", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 5}, mustTime(t, 14))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 195), ProjectID: project.ID, AssignedAgentID: secondAgent.ID, IncarnationID: incarnationID(t, 196), Title: "second"}, mustTime(t, 15))

	for name, mutate := range map[string]func(*AdmissionKeys){
		"duplicate runtime": func(keys *AdmissionKeys) { keys.RuntimeRoot = firstKeys.RuntimeRoot },
		"nested runtime":    func(keys *AdmissionKeys) { keys.RuntimeRoot = firstKeys.RuntimeRoot + "/child" },
		"nested retained source": func(keys *AdmissionKeys) {
			keys.Change.SourceRoot = firstKeys.Change.SourceRoot + "/child"
		},
		"ancestor retained staging": func(keys *AdmissionKeys) {
			keys.Change.StagingRoot = "/retained"
		},
	} {
		t.Run(name, func(t *testing.T) {
			keys := admissionKeys(t, 197, &ChangeReservation{ID: changeID(t, 198), SourceRoot: "/second/source", StagingRoot: "/second/staging"})
			mutate(&keys)
			before := admissionFootprint(t, store)
			if _, err := store.AdmitNext(context.Background(), secondAgent.ID, keys, mustTime(t, 20)); !errors.Is(err, ErrConflict) {
				t.Fatalf("AdmitNext = %v", err)
			}
			if after := admissionFootprint(t, store); after != before {
				t.Fatalf("footprint before=%+v after=%+v", before, after)
			}
		})
	}
	secondKeys := admissionKeys(t, 199, &ChangeReservation{ID: changeID(t, 200), SourceRoot: "/second/source", StagingRoot: "/second/staging"})
	secondKeys.RuntimeRoot = "/runtime/second"
	second, err := store.AdmitNext(context.Background(), secondAgent.ID, secondKeys, mustTime(t, 21))
	if err != nil || !second.Admitted() {
		t.Fatalf("disjoint second admission = %+v, %v", second, err)
	}
	reconcileOverlap := firstKeys
	reconcileOverlap.RuntimeRoot = secondKeys.RuntimeRoot + "/child"
	if _, err := store.ReconcileAdmission(context.Background(), reconcileOverlap); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping reconciliation = %v", err)
	}
}

func TestInjectedOwnershipOverlapFailsReadsAndReadOnlyOpen(t *testing.T) {
	store, run, _ := runningWorkerRun(t)
	path := storePath(t, store)
	corruptSQL(t, store, `UPDATE resources SET path = '/worker/source/nested' WHERE run_id = ? AND kind = 'runtime_root'`, run.ID.Bytes())
	if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Run = %v", err)
	}
	if _, _, err := store.Change(context.Background(), *run.ChangeID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Change = %v", err)
	}
	if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Snapshot = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureDatabaseEvidence(t, path)
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open = %v", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, before)
}

func assertCorruptRunningAuthority(t *testing.T, store *Store, run Run, keys AdmissionKeys) {
	t.Helper()
	if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("AuthenticateAttempt = %v", err)
	}
	proposal, _ := NewSuccessProposal("must not commit")
	if _, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 90)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("ProposeAttemptOutcome = %v", err)
	}
	if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := store.Snapshot(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Snapshot = %v", err)
	}
	if _, err := store.RecoverableRuns(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("RecoverableRuns = %v", err)
	}
}

func runningWorkerRun(t *testing.T) (*Store, Run, AdmissionKeys) {
	t.Helper()
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 4)
	_, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 210), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 211), Title: "worker"}, mustTime(t, 5))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	reservation := &ChangeReservation{ID: changeID(t, 212), SourceRoot: "/worker/source", StagingRoot: "/worker/staging"}
	keys := admissionKeys(t, 213, reservation)
	keys.RuntimeRoot = "/worker/runtime"
	admission, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	format, _ := NewObjectFormat("sha1")
	commit, _ := NewCommitID(format, bytes.Repeat([]byte{1}, format.oidLength()))
	repository, _ := NewFileIdentity(50, 60)
	digest := changeTreeDigest(t, 2)
	selection, _ := NewChangeSelection(format, commit, digest, 1, 1, "/repository", repository)
	selected, err := store.RecordChangeSelection(context.Background(), reservation.ID, mustRevision(t, 1), selection, mustTime(t, 11))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	stage, _ := NewFileIdentity(70, 80)
	prepared, err := store.RecordChangePrepared(context.Background(), reservation.ID, selected.Revision, stage, mustTime(t, 12))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	availability, _ := NewChangeAvailability(digest, 1, 1, stage)
	if _, err := store.MarkChangeAvailable(context.Background(), reservation.ID, prepared.Revision, availability, mustTime(t, 13)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	activateAllResources(t, store, *admission.Run, keys, 20)
	run, err := store.ActivateRun(context.Background(), admission.Run.ID, admission.Run.Revision, mustTime(t, 30))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, run, keys
}
