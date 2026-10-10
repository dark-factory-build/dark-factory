package kernel

import (
	"context"
	"testing"
)

func TestProjectAdmissionHasNoLifetimeCeiling(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "one"}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	result, err := store.AdmitNext(ctx, admissionKeys(t, 203, nil), mustTime(t, 5))
	if err != nil || !result.Admitted() {
		t.Fatalf("admission = %+v, %v", result, err)
	}
	got, found, err := store.Project(ctx, project.ID)
	if err != nil || !found || got.RunsUsed != 1 {
		t.Fatalf("usage = %+v, found=%v, err=%v", got, found, err)
	}
}

func TestOverseerRunUsesFixedBackstop(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 220), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 221), Title: "work"}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	result, err := store.AdmitNext(ctx, admissionKeys(t, 222, nil), mustTime(t, 5))
	if err != nil || !result.Admitted() {
		t.Fatalf("admission = %+v, %v", result, err)
	}
	due, err := store.OverdueRuns(ctx, mustTime(t, 5+MaxOverseerRunSeconds*1000))
	if err != nil || len(due) != 1 || due[0].ID != result.Run.ID {
		t.Fatalf("overdue = %+v, %v", due, err)
	}
}
