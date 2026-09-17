package kernel

import (
	"errors"
	"strings"
	"testing"
)

func TestRetainedSourceReviewRouteUsesInstalledProviderCapability(t *testing.T) {
	task := "review handoff " + strings.Repeat("a", 32)
	for _, test := range []struct {
		name     string
		role     AgentRole
		provider Provider
		wantErr  bool
	}{
		{name: "codex worker", role: RoleWorker, provider: ProviderCodex},
		{name: "claude worker", role: RoleWorker, provider: ProviderClaudeCode, wantErr: true},
		{name: "shell worker", role: RoleWorker, provider: ProviderShell, wantErr: true},
		{name: "orchestrator", role: RoleOrchestrator, provider: ProviderCodex, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRetainedSourceReviewRoute(task, Agent{Role: test.role, Provider: test.provider})
			if test.wantErr != (err != nil) {
				t.Fatalf("route error = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr && test.role == RoleWorker && (!errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "supported routes: codex")) {
				t.Fatalf("route error lacks durable unavailable proof: %v", err)
			}
		})
	}
}

func TestRetainedSourceReviewRouteLeavesOrdinaryTasksProviderAgnostic(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCode, ProviderCodex, ProviderShell} {
		if err := validateRetainedSourceReviewRoute("ordinary worker task", Agent{Role: RoleWorker, Provider: provider}); err != nil {
			t.Fatalf("ordinary %s task rejected: %v", provider, err)
		}
	}
}
