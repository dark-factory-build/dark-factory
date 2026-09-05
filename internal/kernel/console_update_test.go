package kernel

import (
	"context"
	"errors"
	"testing"
)

// Pausing an agent through the console must actually stop dispatch: the
// durable column existed before this and admission is the only place that can
// honour it.
func TestUpdateAgentPauseWithholdsTheAgentFromAdmission(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 45), ProjectID: project.ID, AssignedAgentID: agent.ID,
		IncarnationID: incarnationID(t, 46), Title: "queued while paused",
	}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	paused, resumed := true, false
	updated, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, AgentPatch{Paused: &paused}, mustTime(t, 6))
	if err != nil || !updated.Paused || updated.Revision.Int64() != agent.Revision.Int64()+1 || updated.UpdatedAt.Int64() != 6 {
		t.Fatalf("pause = %+v, %v", updated, err)
	}
	result, err := store.AdmitNext(ctx, admissionKeys(t, 47, nil), mustTime(t, 7))
	if err != nil || result.Admitted() || result.Reason != NoAdmissionNoEligibleWork {
		t.Fatalf("paused admission = %+v, %v", result, err)
	}
	if _, err := store.UpdateAgent(ctx, agent.ID, updated.Revision, AgentPatch{Paused: &resumed}, mustTime(t, 8)); err != nil {
		t.Fatal(err)
	}
	result, err = store.AdmitNext(ctx, admissionKeys(t, 48, nil), mustTime(t, 9))
	if err != nil || !result.Admitted() {
		t.Fatalf("resumed admission = %+v, %v", result, err)
	}
}

func TestUpdateAgentValidatesLaunchControlsAtTheObservedRevision(t *testing.T) {
	store, _, _, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	model, effort, bad := "gpt-5-codex", "high", "sideways"
	updated, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, AgentPatch{Model: &model, ReasoningEffort: &effort}, mustTime(t, 6))
	if err != nil || updated.Model != model || updated.ReasoningEffort != effort {
		t.Fatalf("controls = %+v, %v", updated, err)
	}
	if _, err := store.UpdateAgent(ctx, agent.ID, agent.Revision, AgentPatch{Model: &model}, mustTime(t, 7)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision = %v", err)
	}
	// The wire may carry any bounded string; the domain still owns the set.
	if _, err := store.UpdateAgent(ctx, agent.ID, updated.Revision, AgentPatch{ReasoningEffort: &bad}, mustTime(t, 8)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("invalid effort = %v", err)
	}
}

func TestUpdateTaskEditsAndCancelsOnlyWhileQueued(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 2)
	defer store.Close()
	ctx := context.Background()
	task, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 45), ProjectID: project.ID, AssignedAgentID: agent.ID,
		IncarnationID: incarnationID(t, 46), Title: "before", Priority: 1,
	}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	title, priority := "after", int64(9)
	edited, err := store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{Title: &title, Priority: &priority}, mustTime(t, 6))
	if err != nil || edited.Title != title || edited.Priority != priority || edited.Status != TaskQueued || edited.Revision.Int64() != task.Revision.Int64()+1 {
		t.Fatalf("edit = %+v, %v", edited, err)
	}
	// A foreign agent cannot be assigned: the durable key is (agent, project).
	foreign, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 8), Name: "other", Root: "/other"}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 9), ProjectID: foreign.ID, Name: "b", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTask(ctx, task.ID, edited.Revision, TaskPatch{AssignedAgentID: &stranger.ID}, mustTime(t, 7)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-project reassignment = %v", err)
	}
	cancelled, err := store.UpdateTask(ctx, task.ID, edited.Revision, TaskPatch{Cancel: true}, mustTime(t, 8))
	if err != nil || cancelled.Status != TaskCancelled || cancelled.CompletedAt == nil || cancelled.CompletedAt.Int64() != 8 {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	if _, err := store.UpdateTask(ctx, task.ID, cancelled.Revision, TaskPatch{Title: &title}, mustTime(t, 9)); !errors.Is(err, ErrConflict) {
		t.Fatalf("edit after cancel = %v", err)
	}
	result, err := store.AdmitNext(ctx, admissionKeys(t, 47, nil), mustTime(t, 10))
	if err != nil || result.Admitted() || result.Reason != NoAdmissionQueueEmpty {
		t.Fatalf("cancelled task admission = %+v, %v", result, err)
	}
}
