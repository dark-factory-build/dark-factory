//go:build darwin

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

var errRuntimeCleanupPending = errors.New("daemon: runtime cleanup pending")

// RecoveredRunAction is the bounded disposition of one run in one sweep pass.
type RecoveredRunAction string

const (
	// RecoveredRuntimeAbsent finalized an admitted run whose runtime was
	// positively never created.
	RecoveredRuntimeAbsent RecoveredRunAction = "runtime-absent-failed"
	// RecoveredUnregistered converged a starting runner with no activation or
	// result residue.
	RecoveredUnregistered RecoveredRunAction = "unregistered-converged"
	// RecoveredPreSessionAbsence converged an activated runner that is positively
	// absent with the provider pair still declared and no artifact.
	RecoveredPreSessionAbsence RecoveredRunAction = "pre-session-absence"
	// RecoveredResultConsumed authenticated and consumed the on-disk result,
	// released the runner by positive absence, closed the terminal and removed
	// the artifact and runtime.
	RecoveredResultConsumed RecoveredRunAction = "result-consumed"
	// RecoveredResultConsumedUnsettled marks a consumed result whose run
	// could not be settled to a terminal record; the run stays finalizing
	// and discoverable, and the refusal rides the disposition.
	RecoveredResultConsumedUnsettled RecoveredRunAction = "result-consumed-unsettled"
	// RecoveredNoResultUnresolved converged what could be proven for an
	// activated attempt with no trusted result: failure proposal, unresolved
	// provider pair, released-by-absence runner. Deliberately not terminal.
	RecoveredNoResultUnresolved RecoveredRunAction = "no-result-unresolved"
	// RecoveredLiveHolder observed a held runtime lifetime lease and concluded
	// nothing: the attempt tree is alive.
	RecoveredLiveHolder RecoveredRunAction = "live-holder"
	// RecoveredAdopted dialed a running attempt's protocol-2 takeover
	// endpoint, was granted its control capability, and registered a live
	// attempt owner for it in this daemon. The run stays running; a
	// background goroutine carries it to its normal terminal convergence.
	RecoveredAdopted RecoveredRunAction = "adopted"
	// RecoveredConverged found no actionable residue this pass.
	RecoveredConverged RecoveredRunAction = "already-converged"
	// RecoveredUncertain is the fail-closed disposition: evidence conflicted
	// or was insufficient, and nothing was mutated beyond durable unresolved
	// markers already permitted by the grammar.
	RecoveredUncertain RecoveredRunAction = "uncertain"
)

// RecoveredRunDisposition reports one run's sweep outcome.
type RecoveredRunDisposition struct {
	RunID  kernel.RunID
	Action RecoveredRunAction
	Err    error
}

// RecoverAbandonedRuns is the finite recovery sweep over every nonterminal
// run. Result authentication is always attempted before any absence
// conclusion; absence is only a positive exact-identity observation combined
// with runtime-lifetime-lease availability; a held lease concludes nothing.
// The sweep is idempotent: every durable edge it uses recognizes its own
// exact replay.
func (daemon *Daemon) RecoverAbandonedRuns(ctx context.Context, parent *RuntimeParent, changeParent string) ([]RecoveredRunDisposition, error) {
	if daemon == nil || daemon.store == nil || ctx == nil || parent == nil || changeParent == "" {
		return nil, fmt.Errorf("%w: invalid recovery sweep", kernel.ErrInvalidValue)
	}
	runs, err := daemon.store.RecoverableRuns(ctx)
	if err != nil {
		return nil, err
	}
	dispositions := make([]RecoveredRunDisposition, 0, len(runs))
	for _, recoverable := range runs {
		daemon.attemptMu.Lock()
		_, live := daemon.attempts[recoverable.Run.ID]
		daemon.attemptMu.Unlock()
		if live {
			// A registered live owner is not abandoned; recovery never races it.
			continue
		}
		action, runErr := daemon.recoverRun(ctx, parent, changeParent, recoverable)
		dispositions = append(dispositions, RecoveredRunDisposition{RunID: recoverable.Run.ID, Action: action, Err: runErr})
	}
	return dispositions, nil
}

// recoverReturnedRun applies the restart recovery grammar to one exact run
// after RunNext has joined every owner and closed its runtime handles. It is
// intentionally narrower than RecoverAbandonedRuns: a scheduler may have
// other admitted attempts that are still between admission and registration,
// and a global sweep here could misclassify those concurrent owners.
func (daemon *Daemon) recoverReturnedRun(ctx context.Context, parent *RuntimeParent, changeParent string, runID kernel.RunID) (kernel.Run, error) {
	if daemon == nil || daemon.store == nil || parent == nil || changeParent == "" || runID == (kernel.RunID{}) {
		return kernel.Run{}, fmt.Errorf("%w: invalid returned-run recovery", kernel.ErrInvalidValue)
	}
	recoverable, found, err := daemon.store.RecoverableRun(ctx, runID)
	if err != nil {
		return kernel.Run{}, err
	}
	if found {
		_, recoverErr := daemon.recoverRun(ctx, parent, changeParent, recoverable)
		current, currentFound, readErr := daemon.store.Run(ctx, runID)
		if readErr != nil || !currentFound {
			if readErr == nil {
				readErr = kernel.ErrCorruptState
			}
			return kernel.Run{}, errors.Join(recoverErr, readErr)
		}
		return current, recoverErr
	}
	current, found, err := daemon.store.Run(ctx, runID)
	if err != nil {
		return kernel.Run{}, err
	}
	if !found {
		return kernel.Run{}, kernel.ErrNotFound
	}
	return current, nil
}

func (daemon *Daemon) recoverRun(ctx context.Context, parent *RuntimeParent, changeParent string, recoverable kernel.RecoverableRun) (RecoveredRunAction, error) {
	run := recoverable.Run
	var runtimeRoot, runnerProcess, providerProcess, providerGroup kernel.Resource
	for _, resource := range recoverable.Resources {
		switch resource.Kind {
		case kernel.ResourceRuntimeRoot:
			runtimeRoot = resource
		case kernel.ResourceRunnerProcess:
			runnerProcess = resource
		case kernel.ResourceProviderProcess:
			providerProcess = resource
		case kernel.ResourceProviderGroup:
			providerGroup = resource
		}
	}
	if runtimeRoot.ID == (kernel.ResourceID{}) {
		return RecoveredUncertain, errInvalidContract
	}
	if runtimeRoot.Identity.Empty() {
		if run.Phase == kernel.RunFinalizing && runtimeRoot.State == kernel.ResourceReleased &&
			runnerProcess.State == kernel.ResourceReleased && providerProcess.State == kernel.ResourceReleased && providerGroup.State == kernel.ResourceReleased &&
			recoverable.TerminalSession.State == kernel.TerminalSessionClosed {
			_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
			return RecoveredConverged, settleErr
		}
		return daemon.recoverBeforeRuntime(ctx, parent, changeParent, run, runtimeRoot)
	}
	fileIdentity, err := runtimeFileIdentity(runtimeRoot.Identity)
	if err != nil {
		return RecoveredUncertain, err
	}
	if runtimeRoot.State == kernel.ResourceReleased {
		// The runtime is durably gone; only process/session residue can remain.
		if run.Phase == kernel.RunFinalizing && runnerProcess.State == kernel.ResourceReleased &&
			providerProcess.State == kernel.ResourceReleased && providerGroup.State == kernel.ResourceReleased &&
			recoverable.TerminalSession.State == kernel.TerminalSessionClosed {
			_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
			return RecoveredConverged, settleErr
		}
		return daemon.recoverReleasedRuntimeResidue(ctx, run, runnerProcess, providerProcess, providerGroup)
	}
	if run.Phase == kernel.RunFinalizing && runtimeRoot.State == kernel.ResourceUnresolved &&
		runnerProcess.State == kernel.ResourceReleased && providerProcess.State == kernel.ResourceReleased && providerGroup.State == kernel.ResourceReleased &&
		recoverable.TerminalSession.State == kernel.TerminalSessionClosed {
		// The supervisor consumed and removed the result, then could not
		// remove the runtime: what is left is a partly removed tree, which
		// cannot speak and may not open as a runtime. A held lifetime lease
		// still concludes nothing: something alive inherited it. Otherwise
		// try the removal again; what refused it may be gone, or removable
		// under a later rule.
		if presence, observeErr := ObserveRuntimeLifetime(parent, run.ID.String(), fileIdentity); observeErr == nil && presence == RuntimeLeaseHeld {
			return RecoveredLiveHolder, nil
		}
		if removeErr := daemon.removeRecordedRuntime(ctx, parent, run.ID, fileIdentity); removeErr != nil {
			return RecoveredUncertain, removeErr
		}
		_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
		return RecoveredConverged, settleErr
	}
	recovered, err := OpenRecoveredRuntime(ctx, parent, run.ID.String(), fileIdentity)
	if err != nil {
		if errors.Is(err, errRuntimeBusy) {
			if run.Phase == kernel.RunRunning && runnerProcess.State == kernel.ResourceActive {
				if adopted, adoptErr := daemon.adoptHandoverRun(ctx, parent, changeParent, recoverable, runnerProcess, runtimeRoot, fileIdentity); adoptErr != nil {
					return RecoveredUncertain, adoptErr
				} else if adopted {
					return RecoveredAdopted, nil
				}
			}
			return RecoveredLiveHolder, nil
		}
		if runtimeRoot.State == kernel.ResourceReleasing && errors.Is(err, errRecoveredRuntimeLayout) {
			if result, resultErr := recoveredConsumedAttemptResult(run, runtimeRoot, providerProcess); resultErr == nil {
				// The result may have been consumed before the runtime removal
				// pass stopped.  In that crash cut the runner can still be
				// releasing while its exit edge is absent; authorize removal only
				// after the same exact-identity absence edge used by the normal
				// authenticated-result path.  Otherwise the blocked proposal can
				// never advance to the terminal postcondition.
				if current, found, resourceErr := daemon.store.Resource(ctx, runnerProcess.ID); resourceErr != nil || !found {
					if resourceErr == nil {
						resourceErr = errInvalidContract
					}
					return RecoveredUncertain, resourceErr
				} else if current.State == kernel.ResourceReleasing || current.State == kernel.ResourceUnresolved {
					if run.RunnerExit != nil || !daemon.recoveredRunnerAbsent(current) {
						return RecoveredUncertain, errInvalidContract
					}
					if _, absenceErr := daemon.recordRecoveredRunnerAbsence(ctx, run.ID, current.ID, current.Identity); absenceErr != nil {
						return RecoveredUncertain, absenceErr
					}
				}
				_, authorizeErr := daemon.store.AuthorizeAttemptResultRemoval(ctx, result)
				if authorizeErr == nil {
					if removeErr := daemon.removeRecordedRuntime(ctx, parent, run.ID, fileIdentity); removeErr != nil {
						// The authenticated result is already consumed and the
						// runner absence edge is durable.  A bounded removal
						// refusal is therefore continuation work, not uncertainty;
						// retain the consumed-result action so boot schedules the
						// exact-run continuation.
						return RecoveredResultConsumed, removeErr
					}
					_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
					return RecoveredConverged, settleErr
				}
			}
		}
		return RecoveredUncertain, err
	}
	defer func() { _ = recovered.Close() }()
	// Ordering rule: the artifact always speaks before any absence edge.
	if recovered.HasAttemptResult() {
		record, authErr := recovered.AuthenticateResult(run.ID.String())
		if authErr != nil {
			// Torn publish or tampering: retain the file, conclude nothing.
			return RecoveredUncertain, authErr
		}
		return daemon.recoverAuthenticatedResult(ctx, parent, changeParent, run, recovered, record, runtimeRoot, runnerProcess, fileIdentity)
	}
	if run.Phase == kernel.RunFinalizing && runtimeRoot.State == kernel.ResourceReleasing &&
		runnerProcess.State == kernel.ResourceReleased && providerProcess.State == kernel.ResourceReleased && providerGroup.State == kernel.ResourceReleased &&
		recoverable.TerminalSession.State == kernel.TerminalSessionClosed {
		if removeErr := daemon.removeRecoveredRuntime(ctx, parent, run.ID, recovered, fileIdentity); removeErr != nil {
			return RecoveredConverged, removeErr
		}
		_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
		return RecoveredConverged, settleErr
	}
	switch {
	case runnerProcess.State == kernel.ResourceStarting:
		if recovered.OuterActivated() {
			// Impossible by construction: the marker exists only after
			// ActivateRunner commits. Corrupt or hostile; fail closed.
			return RecoveredUncertain, errInvalidContract
		}
		converged, convergeErr := daemon.recordUnregisteredRunnerConverged(ctx, run.ID, runnerProcess.ID)
		if convergeErr != nil {
			return RecoveredUncertain, convergeErr
		}
		_ = converged
		if removeErr := daemon.removeRecoveredRuntime(ctx, parent, run.ID, recovered, fileIdentity); removeErr != nil {
			return RecoveredUnregistered, removeErr
		}
		_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
		return RecoveredUnregistered, settleErr
	case runnerProcess.State == kernel.ResourceActive && providerProcess.State == kernel.ResourceDeclared && run.Phase == kernel.RunAdmitted:
		if !daemon.recoveredRunnerAbsent(runnerProcess) {
			return RecoveredUncertain, errInvalidContract
		}
		if _, absenceErr := daemon.recordPreSessionRunnerAbsence(ctx, run.ID, runnerProcess.ID, runnerProcess.Identity); absenceErr != nil {
			return RecoveredUncertain, absenceErr
		}
		if removeErr := daemon.removeRecoveredRuntime(ctx, parent, run.ID, recovered, fileIdentity); removeErr != nil {
			return RecoveredPreSessionAbsence, removeErr
		}
		_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
		return RecoveredPreSessionAbsence, settleErr
	default:
		return daemon.recoverWithoutResult(ctx, run, runnerProcess, providerProcess, providerGroup)
	}
}

// recoverBeforeRuntime handles an admitted run whose runtime resource never
// acquired an identity: either CreateRuntime never ran (positively absent by
// name) or its outcome is unknown and the run stays discoverable.
func (daemon *Daemon) recoverBeforeRuntime(ctx context.Context, parent *RuntimeParent, changeParent string, run kernel.Run, runtimeRoot kernel.Resource) (RecoveredRunAction, error) {
	if run.Phase != kernel.RunAdmitted || runtimeRoot.State != kernel.ResourceDeclared {
		return RecoveredUncertain, errInvalidContract
	}
	present, err := runtimeChildPresent(parent, run.ID.String())
	if err != nil {
		return RecoveredUncertain, err
	}
	if present {
		// A directory exists but was never durably bound; its create was
		// uncertain. Conclude nothing until an operator or a later pass with
		// stronger evidence resolves it.
		return RecoveredUncertain, nil
	}
	failed, failErr := daemon.failRunBeforeRuntime(ctx, run, runtimeRoot.ID, kernel.FailureSpawn, fmt.Errorf("daemon: runtime absent at recovery"))
	if failErr != nil && failed.Phase != kernel.RunFinalizing {
		return RecoveredUncertain, failErr
	}
	_, settleErr := daemon.settleRun(ctx, changeParent, run.ID)
	return RecoveredRuntimeAbsent, settleErr
}

func (daemon *Daemon) recoverAuthenticatedResult(ctx context.Context, parent *RuntimeParent, changeParent string, run kernel.Run, recovered *RecoveredRuntime, record *runner.AttemptResultRecord, runtimeRoot, runnerProcess kernel.Resource, fileIdentity runner.FileIdentity) (RecoveredRunAction, error) {
	result, err := kernelAttemptResult(record, run.ID, run.CredentialDigest, runtimeRoot.Identity)
	if err != nil {
		return RecoveredUncertain, err
	}
	// A prior recovery may have already reached the exact terminal-close
	// postcondition. Its removal authorization validates the same run, proof,
	// runtime, provider, runner, and session binding without replaying a
	// consumption edge whose predecessor revision is no longer available.
	_, authorizedErr := daemon.store.AuthorizeAttemptResultRemoval(ctx, result)
	alreadyAuthorized := authorizedErr == nil
	if authorizedErr != nil {
		if !errors.Is(authorizedErr, kernel.ErrConflict) {
			return RecoveredUncertain, authorizedErr
		}
		if _, err := daemon.consumeAttemptResult(ctx, result, true); err != nil {
			return RecoveredUncertain, err
		}
	}
	current, found, err := daemon.store.Resource(ctx, runnerProcess.ID)
	if err != nil || !found {
		return RecoveredUncertain, errors.Join(err, errInvalidContract)
	}
	if current.State == kernel.ResourceReleasing || current.State == kernel.ResourceUnresolved {
		if !daemon.recoveredRunnerAbsent(current) {
			return RecoveredUncertain, errInvalidContract
		}
		if _, absenceErr := daemon.recordRecoveredRunnerAbsence(ctx, run.ID, current.ID, current.Identity); absenceErr != nil {
			return RecoveredUncertain, absenceErr
		}
	}
	session, found, err := daemon.store.TerminalSessionForRun(ctx, run.ID)
	if err != nil || !found {
		return RecoveredUncertain, errors.Join(err, errInvalidContract)
	}
	if session.State != kernel.TerminalSessionClosed {
		if _, err := daemon.closeTerminalAfterRunner(ctx, result); err != nil {
			return RecoveredUncertain, err
		}
	}
	if !alreadyAuthorized {
		_, authorizeErr := daemon.store.AuthorizeAttemptResultRemoval(ctx, result)
		if authorizeErr != nil {
			return RecoveredUncertain, authorizeErr
		}
	}
	if err := recovered.RemoveResult(record); err != nil {
		return RecoveredUncertain, err
	}
	if removeErr := daemon.removeRecoveredRuntime(ctx, parent, run.ID, recovered, fileIdentity); removeErr != nil {
		return RecoveredResultConsumed, removeErr
	}
	// The consumed result's run settles to its terminal record: abandoned
	// for an unpublished change, retained for a verified published tree,
	// failed for a published tree whose own contents the inspection refuses.
	// Any other refusal keeps the run finalizing and discoverable and is
	// surfaced as its own disposition rather than logged indistinguishably
	// from success.
	if _, settleErr := daemon.settleRun(ctx, changeParent, run.ID); settleErr != nil {
		return RecoveredResultConsumedUnsettled, settleErr
	}
	return RecoveredResultConsumed, nil
}

// recoverWithoutResult converges what an activated attempt with no trusted
// result can prove: the run fails closed, the bound provider pair becomes
// durably unresolved, and the absent runner is released by observation. It is
// deliberately not terminal.
func (daemon *Daemon) recoverWithoutResult(ctx context.Context, run kernel.Run, runnerProcess, providerProcess, providerGroup kernel.Resource) (RecoveredRunAction, error) {
	acted := false
	if run.Phase == kernel.RunAdmitted || run.Phase == kernel.RunRunning {
		failure, err := kernel.NewFailureProposal(kernel.FailureInternal, "recovered active attempt without an attempt result")
		if err != nil {
			return RecoveredUncertain, err
		}
		at, err := daemon.timestamp()
		if err != nil {
			return RecoveredUncertain, err
		}
		failed, failErr := daemon.store.FailRun(ctx, run.ID, run.Revision, failure, at)
		if failErr != nil {
			return RecoveredUncertain, failErr
		}
		run = failed
		acted = true
	}
	current := func(id kernel.ResourceID) (kernel.Resource, error) {
		resource, found, err := daemon.store.Resource(ctx, id)
		if err != nil || !found {
			return kernel.Resource{}, errors.Join(err, errInvalidContract)
		}
		return resource, nil
	}
	process, err := current(providerProcess.ID)
	if err != nil {
		return RecoveredUncertain, err
	}
	group, err := current(providerGroup.ID)
	if err != nil {
		return RecoveredUncertain, err
	}
	if process.State == kernel.ResourceReleasing && !process.Identity.Empty() && run.ProviderExit == nil {
		if !daemon.recoveredProviderAbsent(process) {
			return RecoveredUncertain, errInvalidContract
		}
		freshRun, found, err := daemon.store.Run(ctx, run.ID)
		if err != nil || !found {
			return RecoveredUncertain, errors.Join(err, errInvalidContract)
		}
		at, err := daemon.timestamp()
		if err != nil {
			return RecoveredUncertain, err
		}
		_, _, _, markErr := daemon.store.MarkProviderResourcesUnresolved(ctx, run.ID, process.ID, group.ID, freshRun.Revision, process.Revision, group.Revision, process.Identity, "provider absent without an attempt result at recovery", at)
		if markErr != nil {
			return RecoveredUncertain, markErr
		}
		acted = true
	}
	runnerCurrent, err := current(runnerProcess.ID)
	if err != nil {
		return RecoveredUncertain, err
	}
	if (runnerCurrent.State == kernel.ResourceReleasing || runnerCurrent.State == kernel.ResourceUnresolved) && !runnerCurrent.Identity.Empty() {
		if !daemon.recoveredRunnerAbsent(runnerCurrent) {
			return RecoveredUncertain, errInvalidContract
		}
		if _, absenceErr := daemon.recordRecoveredRunnerAbsence(ctx, run.ID, runnerCurrent.ID, runnerCurrent.Identity); absenceErr != nil {
			return RecoveredUncertain, absenceErr
		}
		acted = true
	}
	if !acted {
		return RecoveredConverged, nil
	}
	return RecoveredNoResultUnresolved, nil
}

// recoverReleasedRuntimeResidue converges finalizing residue whose runtime is
// already durably released: an absent runner still awaiting its release edge.
func (daemon *Daemon) recoverReleasedRuntimeResidue(ctx context.Context, run kernel.Run, runnerProcess, providerProcess, providerGroup kernel.Resource) (RecoveredRunAction, error) {
	if (runnerProcess.State == kernel.ResourceReleasing || runnerProcess.State == kernel.ResourceUnresolved) && !runnerProcess.Identity.Empty() && run.RunnerExit == nil {
		if !daemon.recoveredRunnerAbsent(runnerProcess) {
			return RecoveredUncertain, errInvalidContract
		}
		if _, err := daemon.recordRecoveredRunnerAbsence(ctx, run.ID, runnerProcess.ID, runnerProcess.Identity); err != nil {
			return RecoveredUncertain, err
		}
		return RecoveredNoResultUnresolved, nil
	}
	return RecoveredConverged, nil
}

// recoveredRunnerAbsent is the positive exact-identity absence observation.
// The caller already holds the runtime lifetime lease, so a live tree cannot
// reach this check; the observation still refuses a present identity.
func (daemon *Daemon) recoveredRunnerAbsent(resource kernel.Resource) bool {
	identity, err := runnerIdentity(resource.Identity)
	if err != nil {
		return false
	}
	observation := runner.ObserveProcess(identity)
	return observation.Presence == runner.Absent || observation.Presence == runner.Reused
}

func (daemon *Daemon) recoveredProviderAbsent(resource kernel.Resource) bool {
	identity, err := runnerIdentity(resource.Identity)
	if err != nil {
		return false
	}
	observation := runner.ObserveProcessGroup(identity)
	return observation.Presence == runner.Absent || observation.Presence == runner.Reused
}

func (daemon *Daemon) recordUnregisteredRunnerConverged(ctx context.Context, runID kernel.RunID, runnerID kernel.ResourceID) (kernel.Run, error) {
	var lastErr error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		current, found, readErr := daemon.store.Run(ctx, runID)
		if readErr != nil || !found {
			lastErr = errors.Join(readErr, errInvalidContract)
			continue
		}
		resource, resourceFound, resourceErr := daemon.store.Resource(ctx, runnerID)
		if resourceErr != nil || !resourceFound {
			lastErr = errors.Join(resourceErr, errInvalidContract)
			continue
		}
		at, clockErr := daemon.timestamp()
		if clockErr != nil {
			lastErr = clockErr
			continue
		}
		converged, convergeErr := daemon.store.RecordUnregisteredRunnerConverged(ctx, runID, runnerID, current.Revision, resource.Revision, at)
		if convergeErr == nil {
			return converged, nil
		}
		lastErr = convergeErr
	}
	return kernel.Run{}, kernel.NewOutcomeUnknownError(fmt.Errorf("daemon: recovered unregistered convergence: %w", lastErr))
}

func (daemon *Daemon) recordRecoveredRunnerAbsence(ctx context.Context, runID kernel.RunID, resourceID kernel.ResourceID, identity kernel.ResourceIdentity) (kernel.Run, error) {
	var lastErr error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		current, found, readErr := daemon.store.Run(ctx, runID)
		if readErr != nil || !found {
			lastErr = errors.Join(readErr, errInvalidContract)
			continue
		}
		resource, resourceFound, resourceErr := daemon.store.Resource(ctx, resourceID)
		if resourceErr != nil || !resourceFound {
			lastErr = errors.Join(resourceErr, errInvalidContract)
			continue
		}
		at, clockErr := daemon.timestamp()
		if clockErr != nil {
			lastErr = clockErr
			continue
		}
		recorded, _, recordErr := daemon.store.RecordRecoveredRunnerAbsence(ctx, runID, resourceID, current.Revision, resource.Revision, identity, at)
		if recordErr == nil {
			return recorded, nil
		}
		lastErr = recordErr
	}
	return kernel.Run{}, kernel.NewOutcomeUnknownError(fmt.Errorf("daemon: recovered runner absence: %w", lastErr))
}

// removeRecoveredRuntime tears down the recovered runtime after durable
// convergence: the read capability closes first so the removal can rebind and
// prove the exact identity, then the runtime resource releases.
func (daemon *Daemon) removeRecoveredRuntime(ctx context.Context, parent *RuntimeParent, runID kernel.RunID, recovered *RecoveredRuntime, fileIdentity runner.FileIdentity) error {
	if err := recovered.Close(); err != nil {
		return err
	}
	return daemon.removeRecordedRuntime(ctx, parent, runID, fileIdentity)
}

func recoveredConsumedAttemptResult(run kernel.Run, runtimeRoot, providerProcess kernel.Resource) (kernel.AttemptResult, error) {
	if run.ProviderExit == nil {
		return kernel.NewInnerUnregisteredConvergedAttemptResult(run.ID, run.CredentialDigest, run.ResultProofDigest(), runtimeRoot.Identity)
	}
	if code, ok := run.ProviderExit.Code(); ok {
		exit, err := kernel.NewAttemptResultExitCode(code)
		if err != nil {
			return kernel.AttemptResult{}, err
		}
		return kernel.NewInnerConvergedAttemptResult(run.ID, run.CredentialDigest, run.ResultProofDigest(), runtimeRoot.Identity, providerProcess.Identity, exit)
	}
	signal, ok := run.ProviderExit.Signal()
	if !ok {
		return kernel.AttemptResult{}, errInvalidContract
	}
	exit, err := kernel.NewAttemptResultExitSignal(signal)
	if err != nil {
		return kernel.AttemptResult{}, err
	}
	return kernel.NewInnerConvergedAttemptResult(run.ID, run.CredentialDigest, run.ResultProofDigest(), runtimeRoot.Identity, providerProcess.Identity, exit)
}

func (daemon *Daemon) removeRecordedRuntime(ctx context.Context, parent *RuntimeParent, runID kernel.RunID, fileIdentity runner.FileIdentity) error {
	deadline := time.Now().Add(4 * time.Second)
	for {
		done, err := RemoveRecordedRuntime(ctx, parent, runID.String(), fileIdentity)
		if err != nil {
			return err
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			return errRuntimeCleanupPending
		}
		time.Sleep(25 * time.Millisecond)
	}
	return daemon.releaseResources(ctx, runID, kernel.ResourceRuntimeRoot)
}

// ContinueUnsettledRun retries one exact finalizing run after a bounded
// cleanup pass yielded progress but did not finish. Recovery re-reads the
// durable footprint on every pass, so identity, lifetime and ownership checks
// remain the authority; uncertainty stops the continuation.
func (daemon *Daemon) ContinueUnsettledRun(ctx context.Context, parent *RuntimeParent, changeParent string, runID kernel.RunID) error {
	if daemon == nil || ctx == nil || parent == nil || changeParent == "" || runID == (kernel.RunID{}) {
		return errInvalidContract
	}
	for {
		run, err := daemon.recoverReturnedRun(ctx, parent, changeParent, runID)
		if err == nil {
			if run.Phase == kernel.RunTerminal {
				return nil
			}
			return kernel.ErrConflict
		}
		// Cleanup progress and a retained-change settlement refusal are both
		// safe continuation points: the next pass re-reads the exact durable
		// run/resource/change authority. Identity, lifetime, and artifact
		// uncertainty remain terminal refusals and are never retried here.
		if !errors.Is(err, errRuntimeCleanupPending) && !errors.Is(err, kernel.ErrConflict) {
			return errors.Join(err, ctx.Err())
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// runtimeFileIdentity converts the durable runtime path identity back to the
// exact filesystem identity recovery must rebind.
func runtimeFileIdentity(identity kernel.ResourceIdentity) (runner.FileIdentity, error) {
	path, ok := identity.Path()
	if !ok || path.Device() < 0 || path.Inode() <= 0 {
		return runner.FileIdentity{}, errInvalidContract
	}
	return runner.FileIdentity{Device: uint64(path.Device()), Inode: uint64(path.Inode())}, nil
}

// recordPreSessionRunnerAbsence commits the atomic pre-session finalization for an
// activated runner that is positively absent with the provider pair declared.
func (daemon *Daemon) recordPreSessionRunnerAbsence(ctx context.Context, runID kernel.RunID, resourceID kernel.ResourceID, identity kernel.ResourceIdentity) (kernel.Run, error) {
	var lastErr error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		current, found, readErr := daemon.store.Run(ctx, runID)
		if readErr != nil || !found {
			lastErr = errors.Join(readErr, errInvalidContract)
			continue
		}
		resource, resourceFound, resourceErr := daemon.store.Resource(ctx, resourceID)
		if resourceErr != nil || !resourceFound {
			lastErr = errors.Join(resourceErr, errInvalidContract)
			continue
		}
		at, clockErr := daemon.timestamp()
		if clockErr != nil {
			lastErr = clockErr
			continue
		}
		converged, absenceErr := daemon.store.RecordRecoveredPreSessionRunnerAbsence(ctx, runID, resourceID, current.Revision, resource.Revision, identity, at)
		if absenceErr == nil {
			return converged, nil
		}
		lastErr = absenceErr
	}
	return kernel.Run{}, kernel.NewOutcomeUnknownError(fmt.Errorf("daemon: pre-session runner absence: %w", lastErr))
}

// runtimeChildPresent is the by-name presence probe for a runtime that never
// bound an identity. Presence alone is never treated as ownership.
func runtimeChildPresent(parent *RuntimeParent, basename string) (present bool, resultErr error) {
	if parent == nil || !validRuntimeName(basename) {
		return false, invalidContract(nil)
	}
	operation, err := parent.begin()
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, operation.Close()) }()
	directory, err := operation.directory()
	if err != nil {
		return false, err
	}
	var stat unix.Stat_t
	err = unix.Fstatat(int(directory.Fd()), basename, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, err
}

// handoverAdoptionPoll is the steady-state cadence for the adopted-run
// absence poll: the runner is reparented and no longer waitable, so its
// convergence can only be observed, not collected.
const handoverAdoptionPoll = 250 * time.Millisecond

// pollRunnerAbsence blocks until the exact identity is positively absent (or
// reused), polling at a fixed cadence. It never returns a false absence: an
// observation error or a still-present process just polls again.
func pollRunnerAbsence(ctx context.Context, identity runner.Identity) error {
	for {
		observation := runner.ObserveProcess(identity)
		if observation.Presence == runner.Absent || observation.Presence == runner.Reused {
			return nil
		}
		timer := time.NewTimer(handoverAdoptionPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// adoptHandoverRun dials a busy runtime's protocol-2 takeover endpoint and,
// on acceptance, registers this daemon as the run's live control owner: a
// released, terminal-ready liveAttempt exactly like one runNext would have
// built, started so it serves browser attach, human reply, and the attempt
// API through the same daemon.attempts lookup as any other running attempt.
// It returns (false, nil) for every pre-acceptance outcome — no socket, no
// grant, a dial or handshake failure, or an explicit refusal — so the caller
// keeps today's RecoveredLiveHolder behaviour unchanged. Once the runner has
// accepted, any further failure is returned as an error rather than a silent
// fallback: the runner already believes this daemon owns it.
func (daemon *Daemon) adoptHandoverRun(ctx context.Context, parent *RuntimeParent, changeParent string, recoverable kernel.RecoverableRun, runnerProcess, runtimeRoot kernel.Resource, fileIdentity runner.FileIdentity) (adopted bool, resultErr error) {
	run := recoverable.Run
	locator, err := parent.runtimeLocator(run.ID.String())
	if err != nil {
		return false, nil
	}
	dir, child, err := openAdoptedRuntimeDirectory(parent, run.ID.String(), fileIdentity)
	if err != nil {
		return false, nil
	}
	keep := false
	defer func() {
		if !keep {
			resultErr = errors.Join(resultErr, dir.Close(), child.Close())
		}
	}()
	file, dialErr := dialTakeoverGrant(dir, locator, run.ID.String())
	if dialErr != nil || file == nil {
		return false, nil
	}
	controller, err := runner.AdoptHandoverControl(file)
	if err != nil {
		_ = file.Close()
		return false, err
	}
	closeController := true
	defer func() {
		if closeController {
			resultErr = errors.Join(resultErr, controller.Close())
		}
	}()
	runnerIdent, err := runnerIdentity(runnerProcess.Identity)
	if err != nil {
		return false, err
	}
	session := recoverable.TerminalSession
	if session.ID == (kernel.TerminalSessionID{}) || session.State != kernel.TerminalSessionActive {
		return false, fmt.Errorf("%w: adopted run has no active terminal session", errInvalidContract)
	}
	live := newLiveAttempt(daemon, run.ID, session.ID, controller)
	if run.Role == kernel.RoleWorker && recoverable.Change != nil && recoverable.Change.AvailableAt != nil && run.RunningAt != nil {
		live.agentID, live.changeID = run.AgentID, recoverable.Change.ID
		live.pathsSince = *recoverable.Change.AvailableAt
		if run.RunningAt.Int64() > live.pathsSince.Int64() {
			live.pathsSince = *run.RunningAt
		}
	}
	live.attemptDigest = run.CredentialDigest
	live.releaseSent = true
	if err := daemon.registerLiveAttempt(live); err != nil {
		return false, err
	}
	closeController = false
	startLiveAttempt(live, ctx)
	keep = true
	go daemon.awaitAdoptedResult(ctx, parent, changeParent, run, runnerProcess, runtimeRoot, runnerIdent, live, dir, child)
	return true, nil
}

// awaitAdoptedResult is the adopted run's tail: the same authenticate,
// consume, close-terminal, remove, and settle convergence runNext runs after
// live.waitResult(), reached here in its own goroutine because nothing else
// is synchronously waiting for this recovered attempt. The runner is
// reparented and unwaitable, so its convergence is a bounded absence poll
// instead of an owned exit; runtimeDirectory and child are this run's
// lease-free opener, released once the tail is done with them.
func (daemon *Daemon) awaitAdoptedResult(ctx context.Context, parent *RuntimeParent, changeParent string, run kernel.Run, runnerProcess, runtimeRoot kernel.Resource, runnerIdent runner.Identity, live *liveAttempt, runtimeDirectory *os.File, child *runtimeParentChild) {
	released := false
	closeRuntime := func() error {
		if released {
			return nil
		}
		released = true
		return errors.Join(runtimeDirectory.Close(), child.Close())
	}
	// The adopted opener is this goroutine's alone, and RuntimeParent.Close
	// blocks until every child is released: it must be released on every
	// exit, not only the tail's own success.
	defer func() { _ = closeRuntime() }()
	resultOutcome := live.waitResult()
	if resultOutcome.handedOver {
		// This daemon is itself shutting down before the run finished; the
		// next daemon adopts it in turn. No Store mutation, and the runtime
		// directory stays exactly as this adoption found it.
		return
	}
	absenceConfirmed := false
	awaitConvergence := func() error {
		if absenceConfirmed {
			return nil
		}
		if err := pollRunnerAbsence(ctx, runnerIdent); err != nil {
			return err
		}
		absenceConfirmed = true
		return nil
	}
	recordConvergence := func() (kernel.Run, error) {
		if err := awaitConvergence(); err != nil {
			return kernel.Run{}, err
		}
		return daemon.recordRecoveredRunnerAbsence(ctx, run.ID, runnerProcess.ID, runnerProcess.Identity)
	}
	settled, tailErr := daemon.attemptResultTail(ctx, parent, changeParent, run, live, resultOutcome, runtimeDirectory, runtimeRoot.Identity, runnerProcess.ID, runtimeRoot.ID, awaitConvergence, recordConvergence, closeRuntime)
	if tailErr == nil && settled.Phase == kernel.RunTerminal {
		return
	}
	// A tail that stopped short leaves this run finalizing with a proposal and
	// no owner, and nothing else is waiting on it: a scheduled run's shortfall
	// reaches factoryd's UnsettledCompletion, but this one was never
	// scheduled. Continue it here through the same bounded continuation that
	// callback starts, after releasing the opener the continuation re-takes.
	_ = live.close()
	_ = closeRuntime()
	_ = daemon.ContinueUnsettledRun(ctx, parent, changeParent, run.ID)
}
