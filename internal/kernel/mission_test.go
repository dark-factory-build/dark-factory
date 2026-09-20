package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestCreateMissionForBrowserIsAtomicAndRequiresCompleteBrief(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 201), BrowserCapabilityObserve|BrowserCapabilityHumanActions|BrowserCapabilityPrivateHumanRequestDetail)
	ctx := context.Background()
	spec := MissionCreate{ID: outcomeID(t, 202), ProjectID: project.ID, OwnerAgentID: agent.ID, ExpectedAgentRevision: agent.Revision, Objective: "ship the floor", Criteria: "the production check passes"}
	created, err := store.CreateMissionForBrowser(ctx, client.ID, spec, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if created.Mission.Document.Kind != "mission" || created.Mission.Document.AnchorTaskID != created.Anchor.ID.String() {
		t.Fatalf("mission = %+v", created.Mission)
	}
	replay, err := store.CreateMissionForBrowser(ctx, client.ID, spec, mustTime(t, 11))
	if err != nil || replay.Anchor.ID != created.Anchor.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := store.Outcome(ctx, project.ID, spec.ID, 0); err != nil {
		t.Fatal(err)
	}
	bad := spec
	bad.ID = outcomeID(t, 203)
	bad.Criteria = ""
	if _, err := store.CreateMissionForBrowser(ctx, client.ID, bad, mustTime(t, 12)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("bad brief = %v", err)
	}
	if _, err := store.Outcome(ctx, project.ID, bad.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial mission = %v", err)
	}
	badAnchor, _, _ := missionAnchorIDs(bad.ID)
	if _, found, err := store.Task(ctx, badAnchor); err != nil || found {
		t.Fatalf("partial anchor found=%t err=%v", found, err)
	}
}

func TestCreateMissionForBrowserRejectsWrongProjectAndRole(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 204), BrowserCapabilityObserve|BrowserCapabilityHumanActions|BrowserCapabilityPrivateHumanRequestDetail)
	spec := MissionCreate{ID: outcomeID(t, 205), ProjectID: project.ID, OwnerAgentID: agent.ID, ExpectedAgentRevision: agent.Revision, Objective: "objective", Criteria: "criteria"}
	other, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 206), Name: "other", Root: "/other"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	spec.ProjectID = other.ID
	if _, err := store.CreateMissionForBrowser(context.Background(), client.ID, spec, mustTime(t, 2)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong project = %v", err)
	}
	worker, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 209), ProjectID: project.ID, Name: "worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 1}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	roleSpec := spec
	roleSpec.ProjectID = project.ID
	roleSpec.OwnerAgentID = worker.ID
	roleSpec.ExpectedAgentRevision = worker.Revision
	if _, err := store.CreateMissionForBrowser(context.Background(), client.ID, roleSpec, mustTime(t, 4)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong role = %v", err)
	}
	noCapability := terminalTargetClient(t, store, browserTestID(t, 210), BrowserCapabilityObserve)
	if _, err := store.CreateMissionForBrowser(context.Background(), noCapability.ID, spec, mustTime(t, 5)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing capability = %v", err)
	}
}

func TestMissionDelegationInheritsBindingAndReplayIgnoresOwnerRevision(t *testing.T) {
	ctx := context.Background()
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 207), BrowserCapabilityObserve|BrowserCapabilityHumanActions|BrowserCapabilityPrivateHumanRequestDetail)
	spec := MissionCreate{ID: outcomeID(t, 208), ProjectID: project.ID, OwnerAgentID: agent.ID, ExpectedAgentRevision: agent.Revision, Objective: "coordinate delivery", Criteria: "child work is reviewed"}
	created, err := store.CreateMissionForBrowser(ctx, client.ID, spec, mustTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	model := "replay-check"
	if _, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, AgentPatch{Model: &model}, mustTime(t, 21)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMissionForBrowser(ctx, client.ID, spec, mustTime(t, 22)); err != nil {
		t.Fatalf("revision replay = %v", err)
	}
	admitted, err := store.AdmitNext(ctx, admissionKeys(t, 211, nil), mustTime(t, 23))
	if err != nil || !admitted.Admitted() || admitted.Run.TaskID != created.Anchor.ID {
		t.Fatalf("mission anchor admission = %+v, %v", admitted, err)
	}
	_, run := activateAllResources(t, store, *admitted.Run, admissionKeys(t, 211, nil), 24)
	session := terminalSessionForRunTest(t, store, run.ID)
	run, err = store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 28))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 215), ProjectID: project.ID, Name: "worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 1}, mustTime(t, 29))
	if err != nil {
		t.Fatal(err)
	}
	childSpec := NewTask{ID: taskID(t, 216), ProjectID: project.ID, AssignedAgentID: worker.ID, IncarnationID: incarnationID(t, 217), Title: "review child"}
	child, err := store.EnqueueTaskForOverseer(ctx, run.CredentialDigest, childSpec, mustTime(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := store.ListMissionTasks(ctx, run.ProjectID, spec.ID, 0, 16)
	if err != nil || len(items) != 2 || items[1].ID != child.ID {
		t.Fatalf("mission tasks = %+v, %v", items, err)
	}
	mission, err := store.Outcome(ctx, run.ProjectID, spec.ID, 0)
	if err != nil || mission.Document.State != "open" {
		t.Fatalf("mission state = %+v, %v", mission.Document, err)
	}
}

func TestCreateMissionForBrowserRollsBackAfterOutcomeValidation(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	client := terminalTargetClient(t, store, browserTestID(t, 212), BrowserCapabilityObserve|BrowserCapabilityHumanActions|BrowserCapabilityPrivateHumanRequestDetail)
	spec := MissionCreate{ID: outcomeID(t, 213), ProjectID: project.ID, OwnerAgentID: agent.ID, ExpectedAgentRevision: agent.Revision, Objective: "objective", Criteria: string(make([]byte, 40000))}
	if _, err := store.CreateMissionForBrowser(context.Background(), client.ID, spec, mustTime(t, 30)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("oversized criteria = %v", err)
	}
	if _, err := store.Outcome(context.Background(), project.ID, spec.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outcome survived rollback = %v", err)
	}
	anchor, _, _ := missionAnchorIDs(spec.ID)
	if _, found, err := store.Task(context.Background(), anchor); err != nil || found {
		t.Fatalf("anchor survived rollback found=%t err=%v", found, err)
	}
}
