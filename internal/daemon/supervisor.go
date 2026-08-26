package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// SupervisorSpec supplies the external installation and filesystem
// capabilities that do not belong in SQLite. RunNext derives the project,
// task, provider, source root, startup input, run identity and credential from
// durable state or fresh daemon-generated values.
type SupervisorSpec struct {
	AgentID          kernel.AgentID
	RuntimeParent    *RuntimeParent
	ChangeParent     string
	GitExecutable    string
	BaseRevision     string
	AttemptSocket    string
	RunnerExecutable string

	// releaseProvider is a package-test-only seam for the irreversible control
	// write. Production callers outside daemon cannot set it.
	releaseProvider func(*runner.AttemptController) error
	activateOuter   func(*runner.OwnedChild) (runner.FileIdentity, error)
	afterAdmission  func() error
}

// RunNext admits and synchronously owns one complete shell-worker attempt.
// The Darwin implementation does not return while it still owns a child.
func (daemon *Daemon) RunNext(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
	return daemon.runNext(ctx, spec)
}
