//go:build darwin

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	supervisorReconcileAttempts  = 3
	supervisorStoreAttemptWindow = 250 * time.Millisecond
)

type supervisorKeys struct {
	run       kernel.RunID
	session   kernel.TerminalSessionID
	change    kernel.ChangeID
	resources kernel.AdmissionResourceIDs
	token     [32]byte
}

// supervisorAttemptOwner owns the outer runner until the live attempt is
// registered. After registration, the per-attempt owner loop owns all
// controller operations; this outer owner only joins that loop and then
// synchronously terminates/waits the outer child. A context or goroutine
// ending is never treated as cleanup.
type supervisorAttemptOwner struct {
	controller *runner.AttemptController
	live       *liveAttempt
	child      *runner.OwnedChild
	activated  bool
	reaped     bool
}

func (owner *supervisorAttemptOwner) close() error {
	if owner == nil {
		return nil
	}
	var terminationErr, controllerErr error
	if owner.live != nil {
		live := owner.live
		controllerErr = live.close()
		// live.close joins the owner before returning. If its close attempt was
		// uncertain, this outer owner still retains the original controller and
		// can make the final synchronous convergence attempt without a second
		// concurrent reader/writer.
		if !live.controllerClosed && owner.controller != nil {
			terminationErr = owner.controller.Terminate()
			if errors.Is(terminationErr, runner.ErrState) {
				terminationErr = nil
			}
			fallbackCloseErr := owner.controller.Close()
			controllerErr = errors.Join(controllerErr, fallbackCloseErr)
			if fallbackCloseErr == nil || errors.Is(fallbackCloseErr, runner.ErrState) {
				live.controllerClosed = true
			}
		}
		if live.controllerClosed {
			owner.live = nil
			owner.controller = nil
		}
		if !live.controllerClosed {
			// The outer child is not safe to reap while the released provider
			// capability remains open: its process group is distinct and could
			// outlive this owner. Leave the exact references intact so a caller
			// can retry cleanup and surface the unresolved condition.
			return errors.Join(terminationErr, controllerErr, fmt.Errorf("daemon: live attempt controller remains open"))
		}
	} else if owner.controller != nil {
		// A released provider belongs to the inner attempt runner's distinct
		// process group. Ask that live owner to converge it before dropping the
		// control capability; killing only the outer group cannot prove absence.
		terminationErr = owner.controller.Terminate()
		if errors.Is(terminationErr, runner.ErrState) {
			terminationErr = nil
		}
		closeErr := owner.controller.Close()
		controllerErr = closeErr
		if closeErr == nil || errors.Is(closeErr, runner.ErrState) {
			owner.controller = nil
		}
	}
	if owner.child != nil {
		if owner.activated && !owner.reaped {
			// Once activated, the outer runner is the only owner of the inner
			// provider group. Let it converge and exit; terminating the outer
			// first could orphan that distinct group.
			for {
				if _, err := owner.child.FinishAfterExit(8 * time.Second); err == nil {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
		// OwnedChild retains the exact outer identity. Any residual descriptor
		// cleanup or inert abort also completes before ownership is dropped.
		for {
			if err := owner.child.Close(); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		owner.child = nil
	}
	return errors.Join(terminationErr, controllerErr)
}

func (daemon *Daemon) runNext(ctx context.Context, spec SupervisorSpec) (_ kernel.Run, resultErr error) {
	if ctx == nil || daemon == nil || daemon.store == nil || spec.RuntimeParent == nil ||
		spec.ChangeParent == "" || !filepath.IsAbs(spec.ChangeParent) || filepath.Clean(spec.ChangeParent) != spec.ChangeParent ||
		spec.GitExecutable == "" || spec.BaseRevision == "" || spec.AttemptSocket == "" || spec.RunnerExecutable == "" || spec.FactoryctlExecutable == "" {
		return kernel.Run{}, fmt.Errorf("%w: invalid supervisor specification", kernel.ErrInvalidValue)
	}
	factoryctl, err := runner.CommitExecutableLocator(spec.FactoryctlExecutable)
	if err != nil {
		return kernel.Run{}, fmt.Errorf("%w: invalid supervisor specification", kernel.ErrInvalidValue)
	}

	agent, found, err := daemon.store.Agent(ctx, spec.AgentID)
	if err != nil {
		return kernel.Run{}, err
	}
	if !found {
		return kernel.Run{}, kernel.ErrNotFound
	}
	if agent.Role != kernel.RoleWorker || agent.Provider != kernel.ProviderShell {
		return kernel.Run{}, fmt.Errorf("%w: supervisor spike supports shell workers only", kernel.ErrInvalidValue)
	}
	project, found, err := daemon.store.Project(ctx, agent.ProjectID)
	if err != nil {
		return kernel.Run{}, err
	}
	if !found {
		return kernel.Run{}, kernel.ErrCorruptState
	}
	if project.VerificationPolicy != kernel.VerificationNone {
		return kernel.Run{}, fmt.Errorf("%w: verification is not part of the kernel spike", kernel.ErrInvalidValue)
	}
	repositoryIdentity, err := inspectRepositoryIdentity(project.Root)
	if err != nil {
		return kernel.Run{}, err
	}
	keys, err := newSupervisorKeys(rand.Reader)
	if err != nil {
		return kernel.Run{}, err
	}
	runtimeRoot, err := runtimeChildPath(spec.RuntimeParent, keys.run.String())
	if err != nil {
		return kernel.Run{}, err
	}
	finalName := keys.change.String()
	stagingName := "." + finalName + ".stage"
	changeReservation := kernel.ChangeReservation{
		ID: keys.change, SourceRoot: filepath.Join(spec.ChangeParent, finalName),
		StagingRoot: filepath.Join(spec.ChangeParent, stagingName),
	}
	digestBytes := sha256.Sum256(keys.token[:])
	digest, err := kernel.AttemptDigestFromBytes(digestBytes[:])
	if err != nil {
		return kernel.Run{}, err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return kernel.Run{}, err
	}
	admissionKeys := kernel.AdmissionKeys{
		RunID: keys.run, TerminalSessionID: keys.session, AttemptDigest: digest, Change: &changeReservation,
		Resources: keys.resources, RuntimeRoot: runtimeRoot,
	}
	admission, err := daemon.store.AdmitNext(ctx, spec.AgentID, admissionKeys, at)
	if err == nil && spec.afterAdmission != nil {
		err = spec.afterAdmission()
	}
	if err != nil {
		// Admission commit acknowledgement is ambiguous. Retain the freshly
		// generated bearer and exact keys until SQLite proves either that no
		// admission committed or that its authority has been revoked.
		reconcileAdmission := daemon.store.ReconcileAdmission
		if spec.reconcileAdmission != nil {
			reconcileAdmission = spec.reconcileAdmission
		}
		var reconcileErr error
		for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
			reconcileCtx, cancel := context.WithTimeout(context.Background(), supervisorStoreAttemptWindow)
			reconciled, readErr := reconcileAdmission(reconcileCtx, admissionKeys)
			cancel()
			reconcileErr = readErr
			if reconcileErr != nil {
				continue
			}
			if reconciled.Admitted() {
				return daemon.failRun(*reconciled.Run, kernel.FailureInternal, err)
			}
			if reconciled.Reason == kernel.NoAdmissionNotReconciled {
				return kernel.Run{}, err
			}
			reconcileErr = kernel.ErrCorruptState
		}
		return kernel.Run{}, kernel.NewOutcomeUnknownError(errors.Join(err, reconcileErr))
	}
	if !admission.Admitted() {
		return kernel.Run{}, fmt.Errorf("%w: no admission (%s)", kernel.ErrConflict, admission.Reason.String())
	}
	run := *admission.Run
	task, found, err := daemon.store.Task(ctx, run.TaskID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	initialInput, err := provider.InitialInput(run.Provider, []byte(task.Body))
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}

	runtimeValue, err := CreateRuntime(spec.RuntimeParent, keys.run.String())
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	runtimeOpen := true
	defer func() {
		if runtimeOpen {
			resultErr = errors.Join(resultErr, runtimeValue.Close())
		}
	}()
	binding, err := runtimeValue.Binding()
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	gotRuntimePath, runtimeFileIdentity, err := binding.Values()
	if err != nil || gotRuntimePath != runtimeRoot {
		return daemon.failRun(run, kernel.FailureSpawn, errors.Join(err, errInvalidContract))
	}
	runtimeIdentity, err := pathResourceIdentity(runtimeFileIdentity)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	if _, err := daemon.activateResource(ctx, run.ID, keys.resources.RuntimeRoot, runtimeIdentity); err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	if _, err := runtimeValue.PublishAttemptToken(ctx, keys.token); err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	config := changeworker.Config{
		Provider: run.Provider, Model: agent.Model, ReasoningEffort: agent.ReasoningEffort,
		RuntimePath: gotRuntimePath, RuntimeIdentity: runtimeFileIdentity,
		GitExecutable: spec.GitExecutable, FactoryctlExecutable: factoryctl.Path(), RepositoryRoot: project.Root, RepositoryIdentity: repositoryIdentity,
		Revision: spec.BaseRevision, ChangeParent: spec.ChangeParent, FinalName: finalName, StagingName: stagingName,
		AttemptSocket: spec.AttemptSocket, InitialTerminalInput: initialInput,
	}
	if _, err := runtimeValue.PublishWorkerConfig(ctx, config); err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}

	runtimeDirectory, lifetime, err := runtimeValue.DuplicateRunnerFiles()
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	filesOpen := true
	defer func() {
		if filesOpen {
			resultErr = errors.Join(resultErr, runtimeDirectory.Close(), lifetime.Close())
		}
	}()
	lease, _, err := runner.CreateGateLease(runtimeDirectory, lifetime, runner.OuterActivationMarkerName)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	leaseOpen := true
	defer func() {
		if leaseOpen {
			resultErr = errors.Join(resultErr, lease.Close())
		}
	}()
	controller, childControl, err := runner.NewAttemptController()
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	controllerOpen := true
	defer func() {
		if controllerOpen {
			resultErr = errors.Join(resultErr, controller.Close())
		}
	}()
	home, err := binding.ProviderHome()
	if err != nil {
		_ = childControl.Close()
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	wrapper, err := runner.PrepareExecSpec(runner.ExecSpec{
		Target: spec.RunnerExecutable, Args: []string{"--change-worker"},
		Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: home,
	})
	if err != nil {
		_ = childControl.Close()
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	if err := controller.Configure(runner.AttemptSpec{
		AttemptID: run.ID.String(), Wrapper: wrapper,
		MarkerName: runner.InnerActivationMarkerName, TerminalName: runner.TerminalSpoolName,
	}); err != nil {
		_ = childControl.Close()
		return daemon.failRun(run, kernel.FailureProtocol, err)
	}
	outer, err := runner.PrepareExecSpec(runner.ExecSpec{
		Target: spec.RunnerExecutable, Args: []string{"--attempt-runner"},
		Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: home, Control: childControl,
	})
	if err != nil {
		_ = childControl.Close()
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	child, err := runner.StartBlocked(lease, spec.RunnerExecutable, outer, true)
	_ = childControl.Close()
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	owner := &supervisorAttemptOwner{controller: controller, child: child}
	controllerOpen = false
	defer func() { resultErr = errors.Join(resultErr, owner.close()) }()
	runnerResourceIdentity, err := processResourceIdentity(child.Identity())
	if err != nil {
		return daemon.failRun(run, kernel.FailureSpawn, err)
	}
	if _, err := daemon.activateResource(ctx, run.ID, keys.resources.RunnerProcess, runnerResourceIdentity); err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	activateOuter := func(child *runner.OwnedChild) (runner.FileIdentity, error) { return child.Activate() }
	if spec.activateOuter != nil {
		activateOuter = spec.activateOuter
	}
	activationMarker, err := activateOuter(child)
	if activationMarker.Device != 0 && activationMarker.Inode != 0 {
		owner.activated = true
	}
	if err != nil {
		return daemon.failRun(run, kernel.FailureActivation, err)
	}
	if !owner.activated {
		return daemon.failRun(run, kernel.FailureActivation, errInvalidContract)
	}
	ready, err := controller.Next(8 * time.Second)
	if err != nil || ready.Kind != runner.AttemptInnerReady {
		return daemon.failRun(run, kernel.FailureProtocol, errors.Join(err, runner.ErrState))
	}
	providerIdentity, err := processResourceIdentity(ready.Identity)
	if err != nil {
		return daemon.failRun(run, kernel.FailureProtocol, err)
	}
	if err := daemon.activateProviderResources(ctx, run.ID, keys.resources.ProviderProcess, keys.resources.ProviderGroup, providerIdentity); err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}

	selectionEvent, err := releaseCheckpoint(controller, runner.StageSelection)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	selectionReport, err := changeworker.DecodeSelectionReport(selectionEvent.Payload)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	selection, err := kernelSelectionCheckpoint(project.Root, selectionReport)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	changeState, found, err := daemon.store.Change(ctx, keys.change)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	at, err = daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	changeState, err = daemon.store.RecordChangeSelection(ctx, keys.change, changeState.Revision, selection, at)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}

	preparationEvent, err := releaseCheckpoint(controller, runner.StagePreparation)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	preparation, err := changeworker.DecodePreparationReport(preparationEvent.Payload)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	stage, err := kernelStageIdentity(preparation.Stage)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	at, err = daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	changeState, err = daemon.store.RecordChangePrepared(ctx, keys.change, changeState.Revision, stage, at)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}

	populationEvent, err := releaseCheckpoint(controller, runner.StagePopulation)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	population, err := changeworker.DecodePopulationReport(populationEvent.Payload)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	facts, err := change.InspectPublished(ctx, spec.ChangeParent, finalName, preparation.Stage, selectionReport.Format, selectionReport.Base)
	if err != nil || !populationMatchesFacts(population, facts) {
		return daemon.failRun(run, kernel.FailureSource, errors.Join(err, errInvalidContract))
	}
	availability, err := kernelAvailability(facts)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	at, err = daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	changeState, err = daemon.store.MarkChangeAvailable(ctx, keys.change, changeState.Revision, availability, at)
	if err != nil {
		return daemon.failRun(run, kernel.FailureSource, err)
	}
	at, err = daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	session, found, err := daemon.store.TerminalSessionForRun(ctx, run.ID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	run, err = daemon.store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, at)
	if err != nil {
		return daemon.failRun(run, kernel.FailureActivation, err)
	}
	// Register the owner before provider release. The owner is not attachable
	// until it observes TerminalReady, but it already owns the controller and
	// will synchronously converge it if any later step fails.
	live := newLiveAttempt(daemon, run.ID, session.ID, controller)
	if err := daemon.registerLiveAttempt(live); err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	startLiveAttempt(live, ctx)
	owner.live = live
	// Retain the exact controller fallback until the live owner has joined and
	// proved Close. It is not used concurrently: supervisorAttemptOwner.close
	// only reaches it after live.close has synchronously joined the owner.
	if spec.beforeProviderRelease != nil {
		spec.beforeProviderRelease()
	}
	if err := factoryctl.Verify(); err != nil {
		return daemon.failRun(run, kernel.FailureActivation, err)
	}
	// This check is the cancellation/release linearization point. Cancellation
	// already visible here leaves the provider inert. Once it returns nil, the
	// release wins; cancellation becoming visible afterward may race the socket
	// write but cannot revoke that release, and the owner converges through
	// terminal evidence.
	if err := ctx.Err(); err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	// The controller write is the irreversible effect immediately following the
	// cancellation decision. Test-only acknowledgement loss is injected only
	// after this write, so a hook cannot stand in for or delay provider release.
	if err := live.releaseProvider(ctx); err != nil {
		return daemon.failRun(run, kernel.FailureProtocol, err)
	}
	if spec.afterProviderRelease != nil {
		if err := spec.afterProviderRelease(); err != nil {
			return daemon.failRun(run, kernel.FailureProtocol, err)
		}
	}

	terminalResult := live.waitTerminal()
	terminalEvent, waitErr := terminalResult.event, terminalResult.err
	if terminalEvent.Kind != runner.AttemptTerminal || terminalEvent.Terminal == nil {
		return daemon.failRun(run, kernel.FailureProtocol, waitErr)
	}
	current, found, err := daemon.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	run = current
	providerExitAt, err := daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	providerExit, err := kernelProcessExit(terminalEvent.Terminal.Terminal.Exit, providerExitAt)
	if err != nil {
		return daemon.failRun(run, kernel.FailureProtocol, err)
	}
	terminalIdentity, err := processResourceIdentity(terminalEvent.Terminal.Terminal.Process)
	if err != nil || terminalIdentity != providerIdentity {
		return daemon.failRun(run, kernel.FailureProtocol, errors.Join(err, errInvalidContract))
	}
	// The terminal spool is exact inner/provider wait evidence. Commit it
	// before acknowledging and deleting the spool, then release only the
	// provider resources that this live owner has positively reaped.
	run, err = daemon.observeProviderExit(run.ID, providerIdentity, providerExit, providerExitAt)
	if err != nil {
		return kernel.Run{}, errors.Join(waitErr, err)
	}
	if err := daemon.releaseResources(context.Background(), run.ID, kernel.ResourceProviderProcess, kernel.ResourceProviderGroup); err != nil {
		return run, err
	}
	if err := live.acknowledge(context.Background(), terminalEvent.Terminal); err != nil {
		return run, err
	}
	outerExit, err := child.FinishAfterExit(8 * time.Second)
	if err != nil {
		return run, err
	}
	owner.reaped = true
	exitAt, err := daemon.timestamp()
	if err != nil {
		return daemon.failRun(run, kernel.FailureInternal, err)
	}
	exit, err := kernelProcessExit(outerExit, exitAt)
	if err != nil {
		return daemon.failRun(run, kernel.FailureProtocol, err)
	}
	run, err = daemon.observeRunnerExit(run.ID, runnerResourceIdentity, exit, exitAt)
	if err != nil {
		return kernel.Run{}, errors.Join(waitErr, err)
	}
	if err := daemon.releaseResources(context.Background(), run.ID, kernel.ResourceRunnerProcess); err != nil {
		return run, err
	}
	if err := child.Close(); err != nil {
		return run, err
	}
	owner.child = nil
	if err := live.join(); err != nil {
		return run, err
	}
	owner.live = nil
	if err := lease.Close(); err != nil {
		return run, err
	}
	leaseOpen = false
	if err := errors.Join(runtimeDirectory.Close(), lifetime.Close()); err != nil {
		return run, err
	}
	filesOpen = false
	if err := runtimeValue.Close(); err != nil {
		return run, err
	}
	runtimeOpen = false

	presence, err := ObserveRuntimeLifetime(spec.RuntimeParent, keys.run.String(), runtimeFileIdentity)
	if err != nil || presence != RuntimeLeaseAvailable {
		return daemon.unresolvedRuntime(run, keys.resources.RuntimeRoot, errors.Join(err, errRetainedRuntime))
	}
	for {
		done, removeErr := RemoveRecordedRuntime(context.Background(), spec.RuntimeParent, keys.run.String(), runtimeFileIdentity)
		if removeErr != nil {
			return daemon.unresolvedRuntime(run, keys.resources.RuntimeRoot, removeErr)
		}
		if done {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := daemon.releaseResources(context.Background(), run.ID, kernel.ResourceRuntimeRoot); err != nil {
		return run, err
	}
	at, err = daemon.timestamp()
	if err != nil {
		return run, errors.Join(waitErr, err)
	}
	session, found, err = daemon.store.TerminalSessionForRun(context.Background(), run.ID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return run, err
	}
	current, found, err = daemon.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return run, err
	}
	if _, err := daemon.store.CloseActiveTerminalSession(context.Background(), run.ID, session.ID, current.Revision, session.Revision, at); err != nil {
		return run, err
	}
	current, found, err = daemon.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return run, err
	}
	at, err = daemon.timestamp()
	if err != nil {
		return run, errors.Join(waitErr, err)
	}
	final, err := daemon.store.FinalizeRun(context.Background(), run.ID, current.Revision, at)
	return final, errors.Join(waitErr, err, ctx.Err())
}

func newSupervisorKeys(reader io.Reader) (supervisorKeys, error) {
	if reader == nil {
		return supervisorKeys{}, fmt.Errorf("%w: missing random source", kernel.ErrInvalidValue)
	}
	readID := func() ([]byte, error) {
		value := make([]byte, kernel.IDBytes)
		_, err := io.ReadFull(reader, value)
		return value, err
	}
	var keys supervisorKeys
	var err error
	if raw, readErr := readID(); readErr != nil {
		return keys, readErr
	} else if keys.run, err = kernel.RunIDFromBytes(raw); err != nil {
		return keys, err
	}
	if raw, readErr := readID(); readErr != nil {
		return keys, readErr
	} else if keys.session, err = kernel.TerminalSessionIDFromBytes(raw); err != nil {
		return keys, err
	}
	if raw, readErr := readID(); readErr != nil {
		return keys, readErr
	} else if keys.change, err = kernel.ChangeIDFromBytes(raw); err != nil {
		return keys, err
	}
	ids := []*kernel.ResourceID{&keys.resources.RuntimeRoot, &keys.resources.RunnerProcess, &keys.resources.ProviderProcess, &keys.resources.ProviderGroup}
	for _, target := range ids {
		raw, readErr := readID()
		if readErr != nil {
			return keys, readErr
		}
		*target, err = kernel.ResourceIDFromBytes(raw)
		if err != nil {
			return keys, err
		}
	}
	if _, err := io.ReadFull(reader, keys.token[:]); err != nil {
		return supervisorKeys{}, err
	}
	return keys, nil
}

func inspectRepositoryIdentity(path string) (change.RepositoryIdentity, error) {
	var stat syscall.Stat_t
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || syscall.Lstat(path, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return change.RepositoryIdentity{}, fmt.Errorf("%w: invalid durable repository root", kernel.ErrInvalidValue)
	}
	identity, err := change.NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
	if err != nil {
		return change.RepositoryIdentity{}, err
	}
	return identity, nil
}

func runtimeChildPath(parent *RuntimeParent, basename string) (string, error) {
	return parent.runtimeLocator(basename)
}

func releaseCheckpoint(controller *runner.AttemptController, stage runner.AttemptStage) (runner.AttemptEvent, error) {
	if err := controller.Release(stage); err != nil {
		return runner.AttemptEvent{}, err
	}
	event, err := controller.Next(12 * time.Second)
	if err != nil || event.Kind != runner.AttemptCheckpoint || event.Stage != stage {
		return runner.AttemptEvent{}, errors.Join(err, runner.ErrState)
	}
	return event, nil
}

func populationMatchesFacts(report changeworker.PopulationReport, facts change.TreeFacts) bool {
	return report.Identity.Equal(facts.Identity()) && report.Commitment.Equal(facts.Commitment()) &&
		report.EntryCount == facts.EntryCount() && report.BlobBytes == facts.BlobBytes()
}

func (daemon *Daemon) activateResource(ctx context.Context, runID kernel.RunID, resourceID kernel.ResourceID, identity kernel.ResourceIdentity) (kernel.Resource, error) {
	resource, found, err := daemon.store.Resource(ctx, resourceID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return kernel.Resource{}, err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return kernel.Resource{}, err
	}
	return daemon.store.ActivateResource(ctx, runID, resourceID, resource.Revision, identity, at)
}

func (daemon *Daemon) activateProviderResources(ctx context.Context, runID kernel.RunID, processID, groupID kernel.ResourceID, identity kernel.ResourceIdentity) error {
	resources, err := daemon.store.Resources(ctx, runID)
	if err != nil {
		return err
	}
	var process, group kernel.Resource
	for _, resource := range resources {
		switch resource.ID {
		case processID:
			process = resource
		case groupID:
			group = resource
		}
	}
	if process.ID != processID || process.Kind != kernel.ResourceProviderProcess || group.ID != groupID || group.Kind != kernel.ResourceProviderGroup {
		return kernel.ErrCorruptState
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	_, _, err = daemon.store.ActivateProviderResources(ctx, runID, process.ID, process.Revision, group.ID, group.Revision, identity, at)
	return err
}

func (daemon *Daemon) releaseResource(ctx context.Context, runID kernel.RunID, resourceID kernel.ResourceID) error {
	resource, found, err := daemon.store.Resource(ctx, resourceID)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return err
	}
	if resource.RunID != runID {
		return kernel.ErrConflict
	}
	if resource.State == kernel.ResourceReleased {
		return nil
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	_, err = daemon.store.ReleaseResource(ctx, runID, resourceID, resource.Revision, resource.Identity, at)
	return err
}

func (daemon *Daemon) releaseResources(ctx context.Context, runID kernel.RunID, kinds ...kernel.ResourceKind) error {
	resources, err := daemon.store.Resources(ctx, runID)
	if err != nil {
		return err
	}
	byKind := make(map[kernel.ResourceKind]kernel.Resource, len(resources))
	for _, resource := range resources {
		byKind[resource.Kind] = resource
	}
	for _, kind := range kinds {
		resource, found := byKind[kind]
		if !found {
			return kernel.ErrCorruptState
		}
		if err := daemon.releaseResource(ctx, runID, resource.ID); err != nil {
			return err
		}
	}
	return nil
}

func kernelProcessExit(exit runner.Exit, at kernel.UnixMillis) (kernel.ProcessExit, error) {
	if exit.Signal > 0 {
		return kernel.NewProcessExitSignal(1, int64(exit.Signal), at)
	}
	if exit.Code < 0 {
		return kernel.ProcessExit{}, errInvalidContract
	}
	return kernel.NewProcessExitCode(1, int64(exit.Code), at)
}

func (daemon *Daemon) failRun(run kernel.Run, code kernel.FailureCode, cause error) (kernel.Run, error) {
	// Infrastructure failure is another path into finalizing. It must share
	// the same linearization gate as attach and later terminal effects: an
	// attach cannot validate Running and then send a controller command after
	// this transition has revoked durable authority.
	daemon.operationMu.Lock()
	defer daemon.operationMu.Unlock()
	failure, err := kernel.NewFailureProposal(code, "daemon attempt failure")
	if err != nil {
		return run, errors.Join(cause, err)
	}
	var lastErr error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		storeCtx, cancel := context.WithTimeout(context.Background(), supervisorStoreAttemptWindow)
		current, found, readErr := daemon.store.Run(storeCtx, run.ID)
		if readErr != nil || !found {
			cancel()
			lastErr = readErr
			if lastErr == nil {
				lastErr = kernel.ErrCorruptState
			}
			continue
		}
		if current.Phase != kernel.RunAdmitted && current.Phase != kernel.RunRunning {
			cancel()
			return current, cause
		}
		at, clockErr := daemon.timestamp()
		if clockErr != nil {
			cancel()
			lastErr = clockErr
			continue
		}
		failed, failErr := daemon.store.FailRun(storeCtx, current.ID, current.Revision, failure, at)
		cancel()
		if failErr == nil {
			return failed, cause
		}
		lastErr = failErr
	}
	// Live ownership is synchronously converged by the caller's deferred owner.
	// Returning no Run avoids presenting the last admitted/running observation
	// as revoked; durable recovery must reconcile it later.
	return kernel.Run{}, kernel.NewOutcomeUnknownError(errors.Join(cause, lastErr))
}

func (daemon *Daemon) observeProviderExit(runID kernel.RunID, identity kernel.ResourceIdentity, exit kernel.ProcessExit, at kernel.UnixMillis) (kernel.Run, error) {
	return daemon.observeProcessExit(runID, identity, exit, at, true)
}

func (daemon *Daemon) observeRunnerExit(runID kernel.RunID, identity kernel.ResourceIdentity, exit kernel.ProcessExit, at kernel.UnixMillis) (kernel.Run, error) {
	return daemon.observeProcessExit(runID, identity, exit, at, false)
}

func (daemon *Daemon) observeProcessExit(runID kernel.RunID, identity kernel.ResourceIdentity, exit kernel.ProcessExit, at kernel.UnixMillis, provider bool) (kernel.Run, error) {
	var lastErr error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		storeCtx, cancel := context.WithTimeout(context.Background(), supervisorStoreAttemptWindow)
		current, found, readErr := daemon.store.Run(storeCtx, runID)
		if readErr != nil || !found {
			cancel()
			lastErr = readErr
			if lastErr == nil {
				lastErr = kernel.ErrCorruptState
			}
			continue
		}
		var observed kernel.Run
		var observeErr error
		if provider {
			observed, observeErr = daemon.store.ObserveProviderExit(storeCtx, runID, current.Revision, identity, exit, at)
		} else {
			observed, observeErr = daemon.store.ObserveRunnerExit(storeCtx, runID, current.Revision, identity, exit, at)
		}
		cancel()
		if observeErr == nil {
			return observed, nil
		}
		lastErr = observeErr
	}
	return kernel.Run{}, kernel.NewOutcomeUnknownError(lastErr)
}

func (daemon *Daemon) unresolvedRuntime(run kernel.Run, resourceID kernel.ResourceID, cause error) (kernel.Run, error) {
	resource, found, err := daemon.store.Resource(context.Background(), resourceID)
	if err == nil && found && resource.State != kernel.ResourceReleased && resource.State != kernel.ResourceUnresolved {
		at, clockErr := daemon.timestamp()
		if clockErr != nil {
			err = clockErr
		} else {
			_, markErr := daemon.store.MarkResourceUnresolved(context.Background(), run.ID, resourceID, resource.Revision, resource.Identity, "runtime cleanup could not prove absence", at)
			err = markErr
		}
	}
	current, found, readErr := daemon.store.Run(context.Background(), run.ID)
	if readErr == nil && found {
		run = current
	}
	return run, errors.Join(cause, err, readErr)
}
