package kernel

import (
	"context"
	"testing"
)

func TestRunTokenTelemetryRemainsReadableWithoutProjectCeilings(t *testing.T) {
	t.Parallel()
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "telemetry"}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitNext(ctx, admissionKeys(t, 203, nil), mustTime(t, 5))
	if err != nil || !admitted.Admitted() {
		t.Fatalf("admission = %+v, %v", admitted, err)
	}
	if err := store.AddRunTokens(ctx, admitted.Run.ID, 100); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.RunTokens(ctx, admitted.Run.ID); err != nil || !found || got != 100 {
		t.Fatalf("run tokens = %d, found=%v, err=%v", got, found, err)
	}
	latest, found, err := store.LatestRunForAgent(ctx, admitted.Run.AgentID)
	if err != nil || !found || latest.ID != admitted.Run.ID {
		t.Fatalf("latest run = %+v, found=%v, err=%v", latest, found, err)
	}
}
