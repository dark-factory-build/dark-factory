//go:build darwin

package changeworker

import (
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestGoModuleCachePreparationOnlyServesNativeWorkers(t *testing.T) {
	for _, test := range []struct {
		name     string
		role     kernel.AgentRole
		provider kernel.Provider
		want     bool
	}{
		{name: "native worker", role: kernel.RoleWorker, provider: kernel.ProviderCodex, want: true},
		{name: "native orchestrator", role: kernel.RoleOrchestrator, provider: kernel.ProviderCodex},
		{name: "shell worker", role: kernel.RoleWorker, provider: kernel.ProviderShell},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldPrepareGoModuleCache(test.role, test.provider); got != test.want {
				t.Fatalf("shouldPrepareGoModuleCache(%s, %s) = %t, want %t", test.role, test.provider, got, test.want)
			}
		})
	}
}
