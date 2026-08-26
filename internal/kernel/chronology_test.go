package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestActivateRunRejectsCausallyEarlyResources(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	activateResourcesAt(t, store, run, 20)
	before := captureWriteFootprint(t, store)
	if _, err := store.ActivateRun(context.Background(), run.ID, run.Revision, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early activation = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early activation footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.ActivateRun(context.Background(), run.ID, run.Revision, mustTime(t, 20)); err != nil {
		t.Fatalf("activation boundary = %v", err)
	}
}

func TestFactoryTimestampGuardsWritesButNotExactReplay(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	initial, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := captureWriteFootprint(t, store)
	if _, err := store.SetCapacity(context.Background(), initial.Revision, 2, mustTime(t, 0)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale factory update = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("stale factory update footprint before=%+v after=%+v", before, after)
	}
	updated, err := store.SetCapacity(context.Background(), initial.Revision, 2, initial.updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	before = captureWriteFootprint(t, store)
	replay, err := store.SetCapacity(context.Background(), initial.Revision, 2, mustTime(t, 0))
	if err != nil || replay.Revision != updated.Revision || replay.updatedAt != updated.updatedAt {
		t.Fatalf("older factory replay = %+v, %v", replay, err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("older factory replay footprint before=%+v after=%+v", before, after)
	}
}

func TestAdmitNextRejectsBeforeFactoryTimestamp(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 220), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 221), Title: "factory"}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	state, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.SetCapacity(context.Background(), state.Revision, 1, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 222, nil)
	before := captureWriteFootprint(t, store)
	if result, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 9)); !errors.Is(err, ErrRevisionConflict) || result.Admitted() {
		t.Fatalf("stale admission = %+v, %v", result, err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("stale admission footprint before=%+v after=%+v", before, after)
	}
	if result, err := store.AdmitNext(context.Background(), agent.ID, keys, mustTime(t, 10)); err != nil || !result.Admitted() {
		t.Fatalf("admission boundary = %+v, %v", result, err)
	}
}

func TestFinalizeRunRejectsBeforeFactoryTimestamp(t *testing.T) {
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, finalizing := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
	defer store.Close()
	factory, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCapacity(context.Background(), factory.Revision, 1, mustTime(t, 60)); err != nil {
		t.Fatal(err)
	}
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeRun(context.Background(), finalizing.ID, finalizing.Revision, mustTime(t, 53)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale finalization = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("stale finalization footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.FinalizeRun(context.Background(), finalizing.ID, finalizing.Revision, mustTime(t, 60)); err != nil {
		t.Fatalf("finalization boundary = %v", err)
	}
}

func TestFinalizingRejectsCausallyEarlyResourceUpdate(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	if _, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 699), mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureActivation, "cleanup")
	before := captureWriteFootprint(t, store)
	if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalizing = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early finalizing footprint before=%+v after=%+v", before, after)
	}
}

func TestExitDrivenFinalizingRejectsCausallyEarlyResourceUpdate(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	active, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 701), mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(701, 1701)
	if _, err := store.ActivateResource(context.Background(), run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitCode(1, 1, mustTime(t, 10))
	before := captureWriteFootprint(t, store)
	if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, active.Identity, exit, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early exit-driven finalizing = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early exit-driven footprint before=%+v after=%+v", before, after)
	}
}

func TestReleaseProviderRejectsBeforeExit(t *testing.T) {
	proposal, _ := NewSuccessProposal("done")
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	provider := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.MarkResourceUnresolved(context.Background(), run.ID, runtime.ID, runtime.Revision, runtime.Identity, "later cleanup", mustTime(t, 50)); err != nil {
		t.Fatalf("advance independent resource cleanup = %v", err)
	}
	exit, _ := NewProcessExitCode(1, 0, mustTime(t, 42))
	observed, err := store.ObserveProviderExit(context.Background(), run.ID, finalizing.Revision, provider.Identity, exit, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	provider = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
	before := captureWriteFootprint(t, store)
	if _, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 41)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("release before exit = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("release-before-exit footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 42)); err != nil {
		t.Fatalf("release at exit = %v", err)
	}
	replay, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 40))
	if err != nil || replay.State != ResourceReleased {
		t.Fatalf("older release replay = %+v, %v", replay, err)
	}
	_ = observed
}

func TestFinalizeRunRejectsBeforeResourceCleanupTime(t *testing.T) {
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
	defer store.Close()
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 45)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalization = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early finalization footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 53)); err != nil {
		t.Fatalf("finalization boundary = %v", err)
	}
}

func TestFinalizeRunRejectsBeforeLateWorkerChangeCheckpoint(t *testing.T) {
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, run, _ := admittedWorkerRun(t)
	defer store.Close()
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	selection := testChangeSelection(t)
	selected, err := store.RecordChangeSelection(context.Background(), *run.ChangeID, mustRevision(t, 1), selection, mustTime(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := NewFileIdentity(70, 80)
	prepared, err := store.RecordChangePrepared(context.Background(), *run.ChangeID, selected.Revision, stage, mustTime(t, 22))
	if err != nil {
		t.Fatal(err)
	}
	availability := mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes, stage)
	available, err := store.MarkChangeAvailable(context.Background(), *run.ChangeID, prepared.Revision, availability, mustTime(t, 23))
	if err != nil {
		t.Fatal(err)
	}
	if available.Phase != ChangeAvailable {
		t.Fatalf("late Change phase = %s", available.Phase)
	}
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeRun(context.Background(), run.ID, finalizing.Revision, mustTime(t, 22)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalization before late Change = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("late Change footprint before=%+v after=%+v", before, after)
	}
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, int64(30+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, finalizing.Revision, mustTime(t, 33)); err != nil {
		t.Fatalf("late Change boundary finalization = %v", err)
	}
}

func TestRunningWorkerChangeCausalitySurvivesLaterPhases(t *testing.T) {
	t.Run("finalizing", func(t *testing.T) {
		store, run, keys := runningWorkerRun(t)
		path := storePath(t, store)
		proposal, _ := NewSuccessProposal("done")
		if _, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE changes SET available_at_ms = 31, updated_at_ms = 31 WHERE id = ?`, run.ChangeID.Bytes())
		if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("finalizing Run accepted late Change = %v", err)
		}
		if _, err := store.RecoverableRuns(context.Background()); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("finalizing RecoverableRuns accepted late Change = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Open accepted finalizing late Change = %v", err)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		proposal, _ := NewSuccessProposal("done")
		store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, proposal)
		defer store.Close()
		terminal, err := store.FinalizeRun(context.Background(), finalizing.ID, finalizing.Revision, mustTime(t, 80))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE changes SET available_at_ms = 70, updated_at_ms = 70 WHERE id = ?`, terminal.ChangeID.Bytes())
		if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("terminal Run accepted late Change = %v", err)
		}
	})
}

func TestTerminalRunRejectsNonReplayChangeMutationWithoutOwner(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	before := captureWriteFootprint(t, store)
	if _, err := store.RecordChangeSelection(context.Background(), *terminal.ChangeID, mustRevision(t, 1), testChangeSelection(t), mustTime(t, 34)); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-terminal non-replay Change transition = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("post-terminal Change transition footprint before=%+v after=%+v", before, after)
	}
}

func TestTerminalChangeReplayRemainsIdempotentWithoutOwner(t *testing.T) {
	store, run, _ := admittedWorkerRun(t)
	defer store.Close()
	selection := testChangeSelection(t)
	selected, err := store.RecordChangeSelection(context.Background(), *run.ChangeID, mustRevision(t, 1), selection, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, int64(30+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, finalizing.Revision, mustTime(t, 33)); err != nil {
		t.Fatal(err)
	}
	before := captureWriteFootprint(t, store)
	replayed, err := store.RecordChangeSelection(context.Background(), *run.ChangeID, mustRevision(t, selected.Revision.Int64()-1), selection, mustTime(t, 5))
	if err != nil || replayed.Revision != selected.Revision || replayed.UpdatedAt != selected.UpdatedAt {
		t.Fatalf("post-terminal Change replay = %+v, %v", replayed, err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("post-terminal Change replay footprint before=%+v after=%+v", before, after)
	}
}

func TestHistoricalTerminalRunAllowsLaterRetainedChangeCheckpoint(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	project, found, err := store.Project(context.Background(), terminal.ProjectID)
	if err != nil || !found {
		t.Fatalf("project = %+v, %v", project, err)
	}
	secondAgent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 214), ProjectID: project.ID, Name: "retry", Role: RoleWorker, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 4}, mustTime(t, 34))
	if err != nil {
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE tasks SET assigned_agent_id = ?, work_revision = work_revision + 1, status = 'queued', result = NULL, completed_at_ms = NULL, revision = revision + 1, updated_at_ms = 34 WHERE id = ?`, secondAgent.ID.Bytes(), terminal.TaskID.Bytes())
	reuseRevision := mustRevision(t, 1)
	reservation := &ChangeReservation{ID: *terminal.ChangeID, SourceRoot: "/worker/source", StagingRoot: "/worker/staging", ExpectedReuseRevision: &reuseRevision}
	keys := admissionKeys(t, 220, reservation)
	keys.RuntimeRoot = "/retry/runtime"
	admission, err := store.AdmitNext(context.Background(), secondAgent.ID, keys, mustTime(t, 40))
	if err != nil || !admission.Admitted() {
		t.Fatalf("retry admission = %+v, %v", admission, err)
	}
	selection := testChangeSelection(t)
	selected, err := store.RecordChangeSelection(context.Background(), *terminal.ChangeID, reuseRevision, selection, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := NewFileIdentity(70, 80)
	prepared, err := store.RecordChangePrepared(context.Background(), *terminal.ChangeID, selected.Revision, stage, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	availability := mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes, stage)
	if _, err := store.MarkChangeAvailable(context.Background(), *terminal.ChangeID, prepared.Revision, availability, mustTime(t, 43)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Run(context.Background(), terminal.ID); err != nil {
		t.Fatalf("historical terminal Run rejected later Change checkpoint = %v", err)
	}
	if _, _, err := store.Run(context.Background(), admission.Run.ID); err != nil {
		t.Fatalf("retry Run rejected retained Change checkpoint = %v", err)
	}
}

func TestTaskRunTopologyRejectsRevisionSkip(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	corruptSQL(t, store, `UPDATE tasks SET work_revision = work_revision + 2, status = 'queued', result = NULL, completed_at_ms = NULL WHERE id = ?`, terminal.TaskID.Bytes())
	if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("revision skip accepted = %v", err)
	}
}

func TestRunsRejectDuplicateAdmittedTaskWorkRevision(t *testing.T) {
	store, terminal, _ := terminalPreRunningWorker(t)
	defer store.Close()
	_, err := store.writer.Exec(`INSERT INTO runs(
		id, project_id, agent_id, task_id, task_incarnation_id, admitted_task_work_revision,
		change_id, role, provider, execution_mode, model, reasoning_effort, verification_policy, phase,
		proposal_kind, proposal_code, proposal_detail, proposal_result,
		terminal_kind, terminal_code, terminal_detail, terminal_result,
		credential_digest, credential_revoked_at_ms,
		provider_exit_kind, provider_exit_sequence, provider_exit_code, provider_exit_signal, provider_exit_at_ms,
		runner_exit_kind, runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
		revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms
	) SELECT ?, project_id, agent_id, task_id, task_incarnation_id, admitted_task_work_revision,
		change_id, role, provider, execution_mode, model, reasoning_effort, verification_policy, phase,
		proposal_kind, proposal_code, proposal_detail, proposal_result,
		terminal_kind, terminal_code, terminal_detail, terminal_result,
		zeroblob(32), credential_revoked_at_ms,
		provider_exit_kind, provider_exit_sequence, provider_exit_code, provider_exit_signal, provider_exit_at_ms,
		runner_exit_kind, runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
		revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms
	FROM runs WHERE id = ?`, runID(t, 215).Bytes(), terminal.ID.Bytes())
	if err == nil {
		t.Fatal("duplicate terminal run accepted")
	}
}

func TestTerminalRunRejectsSameRevisionLateChangeCheckpoint(t *testing.T) {
	store, terminal := terminalPreRunningAvailableWorker(t)
	defer store.Close()
	corruptSQL(t, store, `UPDATE changes SET available_at_ms = 90, updated_at_ms = 90 WHERE id = ?`, terminal.ChangeID.Bytes())
	if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("same-revision late Change accepted = %v", err)
	}
}

func terminalPreRunningAvailableWorker(t *testing.T) (*Store, Run) {
	t.Helper()
	store, run, _ := admittedWorkerRun(t)
	selection := testChangeSelection(t)
	selected, err := store.RecordChangeSelection(context.Background(), *run.ChangeID, mustRevision(t, 1), selection, mustTime(t, 11))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	stage, _ := NewFileIdentity(70, 80)
	prepared, err := store.RecordChangePrepared(context.Background(), *run.ChangeID, selected.Revision, stage, mustTime(t, 12))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	availability := mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes, stage)
	if _, err := store.MarkChangeAvailable(context.Background(), *run.ChangeID, prepared.Revision, availability, mustTime(t, 13)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, int64(30+index))); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	terminal, err := store.FinalizeRun(context.Background(), run.ID, finalizing.Revision, mustTime(t, 33))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, terminal
}

func terminalPreRunningWorker(t *testing.T) (*Store, Run, AdmissionKeys) {
	t.Helper()
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, run, keys := admittedWorkerRun(t)
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, int64(30+index))); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	terminal, err := store.FinalizeRun(context.Background(), run.ID, finalizing.Revision, mustTime(t, 33))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, terminal, keys
}

func admittedWorkerRun(t *testing.T) (*Store, Run, AdmissionKeys) {
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
	return store, *admission.Run, keys
}

func TestChronologyScannersRejectImpossibleRows(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*Store, func() error)
	}{
		{name: "admitted update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := admittedOrchestratorRun(t)
			corruptSQL(t, store, `UPDATE runs SET updated_at_ms = admitted_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "declared resource update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := admittedOrchestratorRun(t)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = declared_at_ms + 1 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "active resource update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = activated_at_ms + 1 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "running after updated", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			corruptSQL(t, store, `UPDATE runs SET running_at_ms = updated_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "finalizing credential mismatch", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			corruptSQL(t, store, `UPDATE runs SET credential_revoked_at_ms = finalizing_at_ms - 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "terminal before finalizing", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE runs SET terminal_at_ms = finalizing_at_ms - 1 WHERE id = ?`, terminal.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), terminal.ID); return err }
		}},
		{name: "resource activation after running", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET activated_at_ms = 31, updated_at_ms = 31 WHERE id = ?`, runner.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "finalizing resource before finalizing", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = 39 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "released provider before exit", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
			corruptSQL(t, store, `UPDATE resources SET released_at_ms = 40, updated_at_ms = 40 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "terminal before Change", setup: func(t *testing.T) (*Store, func() error) {
			success, _ := NewSuccessProposal("done")
			store, run := finalizingReleasedRun(t, RoleWorker, VerificationNone, success)
			terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE changes SET available_at_ms = 90, updated_at_ms = 90 WHERE id = ?`, terminal.ChangeID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), terminal.ID); return err }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, read := test.setup(t)
			defer store.Close()
			if !errors.Is(read(), ErrCorruptState) {
				t.Fatalf("read accepted corruption: %v", read())
			}
		})
	}

	t.Run("change checkpoint", func(t *testing.T) {
		store, run, _ := runningWorkerRun(t)
		defer store.Close()
		corruptSQL(t, store, `UPDATE changes SET updated_at_ms = available_at_ms - 1 WHERE id = ?`, run.ChangeID.Bytes())
		if _, _, err := store.Change(context.Background(), *run.ChangeID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Change accepted corruption: %v", err)
		}
	})
	t.Run("reserved Change update changed", func(t *testing.T) {
		store, run, _ := runningWorkerRun(t)
		defer store.Close()
		corruptSQL(t, store, `UPDATE changes SET phase = 'reserved', object_format = NULL, selected_commit = NULL, repository_root = NULL, repository_dev = NULL, repository_inode = NULL, selected_at_ms = NULL, stage_dev = NULL, stage_inode = NULL, prepared_at_ms = NULL, tree_digest = NULL, entry_count = NULL, total_bytes = NULL, source_dev = NULL, source_inode = NULL, available_at_ms = NULL, updated_at_ms = created_at_ms + 1 WHERE id = ?`, run.ChangeID.Bytes())
		if _, _, err := store.Change(context.Background(), *run.ChangeID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("reserved Change accepted corruption: %v", err)
		}
	})
	t.Run("task completion checkpoint", func(t *testing.T) {
		failure, _ := NewFailureProposal(FailureInternal, "cleanup")
		store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
		defer store.Close()
		terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE tasks SET completed_at_ms = updated_at_ms - 1 WHERE id = ?`, terminal.TaskID.Bytes())
		if _, _, err := store.Task(context.Background(), terminal.TaskID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("task completion corruption accepted: %v", err)
		}
	})
	t.Run("blocked task terminal checkpoint", func(t *testing.T) {
		blocked, _ := NewBlockedProposal("blocked")
		store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, blocked)
		defer store.Close()
		terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE tasks SET incarnation_id = X'91919191919191919191919191919191' WHERE id = ?`, terminal.TaskID.Bytes())
		if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("blocked task/run mismatch accepted: %v", err)
		}
	})
}

func activateResourcesAt(t *testing.T, store *Store, run Run, at int64) {
	t.Helper()
	resources := resourcesForRunTest(t, store, run.ID)
	provider := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	if _, _, err := store.ActivateProviderResources(context.Background(), run.ID, provider.ID, provider.Revision, group.ID, group.Revision, processIdentity(t, 700), mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.Kind == ResourceProviderProcess || resource.Kind == ResourceProviderGroup {
			continue
		}
		var identity ResourceIdentity
		if resource.Kind == ResourceRuntimeRoot {
			identity, _ = NewPathResourceIdentity(700, 1700)
		} else {
			identity = processIdentity(t, 702)
		}
		if _, err := store.ActivateResource(context.Background(), run.ID, resource.ID, resource.Revision, identity, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
	}
}
