package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func runningIntakeOverseer(t *testing.T) (*Store, Run, IntakeSource, IntakeAcceptance) {
	t.Helper()
	ctx := context.Background()
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 4)
	t.Cleanup(func() { store.Close() })
	sourceID, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{220}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: sourceID, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/source", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), OverseerAgentID: agent.ID, Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	source, err = store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, intakeSnapshotForTest(), mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 7)); err != nil {
		t.Fatal(err)
	}
	keys := admissionKeys(t, 152, nil)
	result, err := store.AdmitNext(ctx, keys, mustTime(t, 10))
	if err != nil || !result.Admitted() {
		t.Fatalf("admit: %+v %v", result, err)
	}
	_, run := activateAllResources(t, store, *result.Run, keys, 20)
	session := terminalSessionForRunTest(t, store, run.ID)
	run, err = store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	return store, run, source, accepted
}

func TestIntakeDescendantsRetainSourceAndDestinationAndCannotAdoptForeignReplay(t *testing.T) {
	ctx := context.Background()
	store, run, source, accepted := runningIntakeOverseer(t)
	other, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 221), ProjectID: run.ProjectID, Name: "other", Root: "/other-intake", BaseRef: "release"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, other.ID, other.Revision, mustTime(t, 41)); err != nil {
		t.Fatal(err)
	}
	spec := NewTask{ID: taskID(t, 222), IncarnationID: incarnationID(t, 222), ProjectID: run.ProjectID, Title: "delegated"}
	child, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, spec, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	route, found, err := store.TaskRepository(ctx, child.ID)
	if err != nil || !found || route.ID != accepted.RepositoryID {
		t.Fatalf("child target: %+v %v", route, err)
	}
	frozen, found, err := store.IntakeAcceptanceForTask(ctx, child.ID)
	if err != nil || !found || frozen.ID != accepted.ID || frozen.SourceRepository != "owner/source" {
		t.Fatalf("child source: %+v %v", frozen, err)
	}
	if replay, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, spec, mustTime(t, 43)); err != nil || replay.ID != child.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	conflict := spec
	conflict.RepositoryID = other.ID
	if _, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, conflict, mustTime(t, 44)); !errors.Is(err, ErrConflict) {
		t.Fatalf("retargeted replay: %v", err)
	}
	foreign := NewTask{ID: taskID(t, 223), IncarnationID: incarnationID(t, 223), ProjectID: run.ProjectID, RepositoryID: accepted.RepositoryID, Title: "unrelated"}
	if _, err := store.EnqueueTask(ctx, foreign, mustTime(t, 45)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, foreign, mustTime(t, 46)); !errors.Is(err, ErrConflict) {
		t.Fatalf("adopted unrelated task: %v", err)
	}
	if _, found, err := store.IntakeAcceptanceForTask(ctx, foreign.ID); err != nil || found {
		t.Fatalf("foreign binding: %v %v", found, err)
	}
	updated := NewIntakeSource{ID: source.ID, GitHubRepositoryID: 99, GitHubRepositoryName: "new/source", ProjectID: run.ProjectID, TargetRepositoryID: other.ID, OverseerAgentID: run.AgentID, Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}
	if _, err := store.UpdateIntakeSource(ctx, source.ID, source.Revision, updated, false, mustTime(t, 47)); err != nil {
		t.Fatal(err)
	}
	frozen, found, err = store.IntakeAcceptanceForTask(ctx, child.ID)
	if err != nil || !found || frozen.SourceRepository != accepted.SourceRepository || frozen.RepositoryID != accepted.RepositoryID {
		t.Fatalf("settings retargeted child: %+v %v", frozen, err)
	}
	tasks, err := store.IntakeTasksForAcceptance(ctx, accepted.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("linked tasks: %+v %v", tasks, err)
	}
	path := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	frozen, found, err = reopened.IntakeAcceptanceForTask(ctx, child.ID)
	if err != nil || !found || frozen.ID != accepted.ID {
		t.Fatalf("reopened source: %+v %v", frozen, err)
	}
}

func TestIntakeWithdrawalBlocksDelegationAndAdmissionAndFindsChildren(t *testing.T) {
	ctx := context.Background()
	store, run, source, accepted := runningIntakeOverseer(t)
	worker, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 230), ProjectID: run.ProjectID, Name: "worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	spec := NewTask{ID: taskID(t, 231), IncarnationID: incarnationID(t, 231), ProjectID: run.ProjectID, AssignedAgentID: worker.ID, Title: "linked"}
	if _, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, spec, mustTime(t, 41)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, spec, mustTime(t, 43)); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn delegation replay: %v", err)
	}
	spec.ID = taskID(t, 232)
	spec.IncarnationID = incarnationID(t, 232)
	if _, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, spec, mustTime(t, 44)); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn new delegation: %v", err)
	}
	candidate := changeID(t, 233)
	result, err := store.AdmitNext(ctx, admissionKeys(t, 234, &candidate), mustTime(t, 45))
	if err != nil || result.Admitted() {
		t.Fatalf("withdrawn child admitted: %+v %v", result, err)
	}
	if _, err := store.UpdateIntakeSource(ctx, source.ID, source.Revision, NewIntakeSource{ID: source.ID, GitHubRepositoryID: 99, GitHubRepositoryName: "other/changed", ProjectID: source.ProjectID, TargetRepositoryID: source.TargetRepositoryID, OverseerAgentID: source.OverseerAgentID, Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, false, mustTime(t, 46)); err != nil {
		t.Fatal(err)
	}
	proposal, err := NewFailureProposal(FailureAttempt, "source supervisor finished")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, run.CredentialDigest, proposal, mustTime(t, 50)); err != nil {
		t.Fatal(err)
	}
	observeMissingProcessExits(t, store, run.ID, 51)
	for index, resource := range resourcesForRunTest(t, store, run.ID) {
		if resource.State == ResourceReleased {
			continue
		}
		if _, err := store.ReleaseResource(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, int64(60+index))); err != nil {
			t.Fatal(err)
		}
	}
	finalizing := closeTerminalSessionAtCurrent(t, store, run.ID, 70)
	if _, err := store.FinalizeRun(ctx, run.ID, finalizing.Revision, mustTime(t, 70)); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingIntakeWithdrawals(ctx, source.ID, 25)
	if err != nil || len(pending) != 1 || pending[0].ID != accepted.ID {
		t.Fatalf("pending descendants: %+v %v", pending, err)
	}
}

func TestIntakeReplacementInheritsBinding(t *testing.T) {
	ctx := context.Background()
	store, run, _, accepted := runningIntakeOverseer(t)
	task, _, err := store.Task(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := TaskInterventionIDFromBytes(bytes.Repeat([]byte{240}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	next := NewTask{ID: taskID(t, 241), IncarnationID: incarnationID(t, 241), Body: "replacement"}
	request := TaskInterventionRequest{OperationID: operation, TaskID: task.ID, RunID: run.ID, ExpectedTaskRevision: task.Revision, ExpectedRunRevision: run.Revision, Kind: TaskInterventionReplace}
	if _, err := store.StopRunForOperator(ctx, request, &next, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.IntakeAcceptanceForTask(ctx, next.ID)
	if err != nil || !found || binding.ID != accepted.ID {
		t.Fatalf("replacement binding: %+v %v", binding, err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustTime(t, 41)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StopRunForOperator(ctx, request, &next, mustTime(t, 42)); err != nil {
		t.Fatalf("replacement replay: %v", err)
	}
}

func TestIntakeBindingCorruptionRefusedBeforeMutation(t *testing.T) {
	for _, statement := range []string{
		`DELETE FROM intake_task_bindings`,
		`UPDATE task_repository_bindings SET repository_id = ? WHERE task_id = ?`,
	} {
		t.Run(statement, func(t *testing.T) {
			store, run, _, accepted := runningIntakeOverseer(t)
			if statement[0] == 'D' {
				corruptSQL(t, store, statement)
			} else {
				other, err := store.AddProjectRepository(context.Background(), NewProjectRepository{ID: repositoryID(t, 250), ProjectID: run.ProjectID, Name: "other", Root: "/other-binding", BaseRef: "HEAD"}, mustTime(t, 40))
				if err != nil {
					t.Fatal(err)
				}
				corruptSQL(t, store, statement, other.ID.Bytes(), accepted.TaskID.Bytes())
			}
			if _, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 251), Name: "probe", Root: "/probe"}, mustTime(t, 50)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("corrupt binding accepted: %v", err)
			}
		})
	}
}
