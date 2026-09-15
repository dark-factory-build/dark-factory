//go:build darwin

package daemon

import (
	"context"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestSupervisorKeepsAdmittedIdentityOnRuntimeFailure(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.afterAdmission = func() error {
		return fixture.runtimeParent.Close()
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("closed runtime parent unexpectedly succeeded")
	}
	if run.ID == (kernel.RunID{}) {
		t.Fatalf("lost proven admission identity: %v", err)
	}
	current, found, readErr := fixture.store.Run(context.Background(), run.ID)
	if readErr != nil || !found || current.TaskID != fixture.taskID {
		t.Fatalf("returned identity does not bind admitted task: found=%t err=%v", found, readErr)
	}
	if run.Phase != current.Phase {
		t.Fatalf("invented run phase: got %s want %s", run.Phase, current.Phase)
	}
}
