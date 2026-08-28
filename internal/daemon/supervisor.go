package daemon

import (
	"context"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	supervisorReconcileAttempts  = 3
	supervisorStoreAttemptWindow = 250 * time.Millisecond
)

// SupervisorSpec supplies the external installation and filesystem
// capabilities that do not belong in SQLite. RunNext derives the project,
// task, provider, source root, provider task, run identity and credential from
// durable state or fresh daemon-generated values.
type SupervisorSpec struct {
	RuntimeParent        *RuntimeParent
	ChangeParent         string
	GitExecutable        string
	BaseRevision         string
	AttemptSocket        string
	RunnerExecutable     string
	FactoryctlExecutable string
	ToolPath             string

	// These unexported hooks are package-test-only ambiguity seams. Production
	// callers outside daemon cannot set them. afterProviderRelease runs only
	// after the real provider release frame has been written; it can report a
	// lost acknowledgement without replacing that irreversible write.
	activateOuter         func(*runner.OwnedChild) (runner.FileIdentity, error)
	afterAdmission        func() error
	afterProviderRelease  func() error
	reconcileAdmission    func(context.Context, kernel.AdmissionKeys) (kernel.AdmissionResult, error)
	beforeProviderRelease func()

	// admissionObserved is a package-private scheduling hint. The Darwin
	// supervisor invokes it exactly once after AdmitNext commits successfully;
	// it carries no selected identity or transition authority.
	admissionObserved func(bool)

	// scheduledAttempt is a package-test-only seam for the joined coordinator.
	// Production always calls the concrete synchronous RunNext method.
	scheduledAttempt func(context.Context, SupervisorSpec) (kernel.Run, error)
	// scheduledCompletion is the matching package-test-only durable-read seam.
	// Production always reloads the returned run from the concrete Store.
	scheduledCompletion func(kernel.Run) error
	// schedulerPoll replaces the wall-clock ticker only in package tests.
	schedulerPoll <-chan time.Time
}

// RunNext admits and synchronously owns one complete shell-worker attempt.
// The Darwin implementation does not return while it still owns a child.
func (daemon *Daemon) RunNext(ctx context.Context, spec SupervisorSpec) (run kernel.Run, resultErr error) {
	registration, err := daemon.registerSupervisor(ctx)
	if err != nil {
		return kernel.Run{}, err
	}
	defer func() { daemon.endSupervisor(registration, resultErr) }()
	return daemon.runNext(registration.ctx, spec)
}
