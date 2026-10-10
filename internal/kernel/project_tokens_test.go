package kernel

import (
	"context"
	"testing"
)

func TestProjectTokenUsageIsRetainedWithoutACeiling(t *testing.T) {
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
	if err := store.AddRunTokens(ctx, result.Run.ID, 100); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ProjectTokens(ctx, project.ID); err != nil || got != (ProjectTokens{TokensUsed: 100}) {
		t.Fatalf("tokens = %+v, %v", got, err)
	}
}
