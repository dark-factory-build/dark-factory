//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestDeliveryReconcileEnqueuesVerifiedSourcesIdempotently(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	projectID := mustProjectID(t, testID(240))
	agentID := mustAgentID(t, testID(241))
	at := mustKernelTime(t, 101)
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "delivery", Root: contentRepositoryFixture(t)}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: projectID, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 1}, at); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	receipt, _ := json.Marshal(map[string]any{"state": "verified", "sha": sha, "pr": 11, "verification": map[string]any{"healthy": true, "sha": sha}, "delivery_mode": "range", "delivery_sources": []map[string]any{{"pr": 10, "merge_sha": sha, "issue": 602, "reference": "refs"}}})
	input := api.DeliveryInput{ProjectID: projectID.String(), Repository: "example/factory", OverseerAgentID: agentID.String(), Release: api.DeliveryRelease{Repository: "example/factory"}, Receipt: receipt}
	first, err := fixture.daemon.reconcileDelivery(ctx, input)
	if err != nil || len(first.TaskIDs) != 1 {
		t.Fatalf("first delivery = %+v, %v", first, err)
	}
	second, err := fixture.daemon.reconcileDelivery(ctx, input)
	if err != nil || len(second.TaskIDs) != 1 || second.TaskIDs[0] != first.TaskIDs[0] {
		t.Fatalf("replayed delivery = %+v, %v", second, err)
	}
	id := mustTaskID(t, first.TaskIDs[0])
	incarnation := deliveryIncarnationID("incarnation", first.TaskIDs[0])
	if _, found, err := fixture.store.TaskRecovery(ctx, id, incarnation); err != nil || !found {
		t.Fatalf("delivery task recovery found=%t err=%v", found, err)
	}
}
