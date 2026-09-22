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

func TestDeliveryReconcileRejectsMissingSourceMapping(t *testing.T) {
	fixture := newDispatchFixture(t)
	sha := strings.Repeat("a", 40)
	receipt, _ := json.Marshal(map[string]any{"state": "verified", "sha": sha, "pr": 11, "verification": map[string]any{"healthy": true, "sha": sha}, "delivery_mode": "range"})
	_, err := fixture.daemon.reconcileDelivery(context.Background(), api.DeliveryInput{ProjectID: testID(250), Repository: "example/factory", OverseerAgentID: testID(251), Release: api.DeliveryRelease{Repository: "example/factory"}, Receipt: receipt})
	if err == nil || err != kernel.ErrInvalidValue {
		t.Fatalf("missing delivery sources error = %v", err)
	}
}
