package kernel

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestProviderAndRunnerExitsRemainDistinctInEitherOrder(t *testing.T) {
	t.Parallel()
	t.Run("typed outcome then provider then runner", func(t *testing.T) {
		store, run, keys := runningOrchestratorRun(t)
		defer store.Close()
		success, _ := NewSuccessProposal("done")
		current, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, success, mustTime(t, 40))
		if err != nil {
			t.Fatal(err)
		}
		providerExit, _ := NewProcessExitCode(1, 7, mustTime(t, 41))
		providerIdentity := registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess)
		current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, providerIdentity, providerExit, mustTime(t, 42))
		if err != nil {
			t.Fatal(err)
		}
		resources := resourcesForRunTest(t, store, run.ID)
		process := resourceOfKind(t, resources, ResourceProviderProcess)
		group := resourceOfKind(t, resources, ResourceProviderGroup)
		current, _, _, err = store.ReleaseProviderResources(context.Background(), run.ID, process.ID, group.ID, current.Revision, process.Revision, group.Revision, process.Identity, mustTime(t, 43))
		if err != nil {
			t.Fatal(err)
		}
		runnerExit, _ := NewProcessExitCode(1, 0, mustTime(t, 44))
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		current, _, err = store.RecordLiveRunnerExitAndRelease(context.Background(), run.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, runnerExit, mustTime(t, 44))
		if err != nil || current.Proposal == nil || !current.Proposal.equal(success) || current.ProviderExit == nil || !current.ProviderExit.equal(providerExit) || current.RunnerExit == nil || !current.RunnerExit.equal(runnerExit) {
			t.Fatalf("distinct exits = %+v, %v", current, err)
		}
	})

	t.Run("runner first remains first outcome", func(t *testing.T) {
		store, run, _ := runningOrchestratorRun(t)
		defer store.Close()
		failure, _ := NewFailureProposal(FailureInternal, "runner absent in recovery")
		current, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40))
		if err != nil {
			t.Fatal(err)
		}
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		current, _, err = store.RecordRecoveredRunnerAbsence(context.Background(), run.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, mustTime(t, 41))
		if err != nil || current.Proposal == nil || !current.Proposal.equal(failure) || current.RunnerExit == nil || !current.RunnerExit.RecoveredAbsence() {
			t.Fatalf("runner-first outcome = %+v, %v", current, err)
		}
		providerExit, _ := NewProcessExitCode(1, 7, mustTime(t, 42))
		current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), providerExit, mustTime(t, 43))
		if err != nil || current.Proposal == nil || !current.Proposal.equal(failure) || current.ProviderExit == nil || current.RunnerExit == nil {
			t.Fatalf("runner-first convergence = %+v, %v", current, err)
		}
	})

	t.Run("provider first has provider failure", func(t *testing.T) {
		store, run, _ := runningOrchestratorRun(t)
		defer store.Close()
		exit, _ := NewProcessExitCode(1, 7, mustTime(t, 40))
		observed, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), exit, mustTime(t, 41))
		if err != nil || observed.Proposal == nil || observed.Proposal.Code() != FailureProviderExit || observed.ProviderExit == nil || observed.RunnerExit != nil {
			t.Fatalf("provider-first outcome = %+v, %v", observed, err)
		}
	})
}

func TestRecoveredProviderAbsenceRequiresExactRegisteredPair(t *testing.T) {
	t.Parallel()
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	resources := resourcesForRunTest(t, store, run.ID)
	identity := processIdentity(t, 91)
	provider := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	if _, _, err := store.ActivateProviderResources(context.Background(), run.ID, provider.ID, provider.Revision, group.ID, group.Revision, identity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 21))
	if _, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, processIdentity(t, 92), exit, mustTime(t, 22)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong provider identity = %v", err)
	}
	observed, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 22))
	if err != nil || observed.Proposal == nil || observed.Proposal.Code() != FailureProviderExit || observed.ProviderExit == nil || !observed.ProviderExit.RecoveredAbsence() {
		t.Fatalf("recovered provider absence = %+v, %v", observed, err)
	}
}

func TestReleaseOfNonemptyProcessResourceRequiresMatchingExit(t *testing.T) {
	t.Parallel()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	success, _ := NewSuccessProposal("done")
	current, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, success, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	provider := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	runnerResource := resourceOfKind(t, resources, ResourceRunnerProcess)
	for _, resource := range []Resource{provider, group, runnerResource} {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 41)); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s release without exit = %v", resource.Kind.String(), err)
		}
	}
	exit, _ := NewProcessExitCode(1, 0, mustTime(t, 42))
	// A nonempty pair release without the matching durable provider exit fails
	// closed even through the owned pair edge.
	if _, _, _, err := store.ReleaseProviderResources(context.Background(), run.ID, provider.ID, group.ID, current.Revision, provider.Revision, group.Revision, provider.Identity, mustTime(t, 42)); !errors.Is(err, ErrConflict) {
		t.Fatalf("pair release without provider exit = %v", err)
	}
	current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, provider.Identity, exit, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	current, _, _, err = store.ReleaseProviderResources(context.Background(), run.ID, provider.ID, group.ID, current.Revision, provider.Revision, group.Revision, provider.Identity, mustTime(t, 44))
	if err != nil {
		t.Fatalf("pair release after provider exit = %v", err)
	}
	if _, err := store.ReleaseResource(context.Background(), run.ID, runnerResource.ID, runnerResource.Revision, runnerResource.Identity, mustTime(t, 45)); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic runner release after only provider exit = %v", err)
	}
	runnerExit, _ := NewProcessExitCode(1, 0, mustTime(t, 45))
	if _, _, err := store.RecordLiveRunnerExitAndRelease(context.Background(), run.ID, runnerResource.ID, current.Revision, runnerResource.Revision, runnerResource.Identity, runnerExit, mustTime(t, 46)); err != nil {
		t.Fatalf("runner release with matching exit = %v", err)
	}
}

func TestRelationshipValidationRejectsReleasedProcessWithoutExit(t *testing.T) {
	t.Parallel()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	success, _ := NewSuccessProposal("done")
	if _, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, success, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE resources SET state = 'released', released_at_ms = 41, updated_at_ms = 41 WHERE run_id = ? AND kind = 'provider_process'`, run.ID.Bytes())
	if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("released provider without exit = %v", err)
	}
}

// prepareRunnerRelease builds the exact finalizing state where the outer
// runner is the only unreleased process owner: typed outcome proposed,
// provider exit observed and the provider pair released.
func prepareRunnerRelease(t *testing.T) (*Store, Run, Resource) {
	t.Helper()
	store, run, keys := runningOrchestratorRun(t)
	success, _ := NewSuccessProposal("done")
	current, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, success, mustTime(t, 40))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	exit, _ := NewProcessExitCode(1, 0, mustTime(t, 41))
	provider := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
	current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, provider.Identity, exit, mustTime(t, 41))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	current, _, _, err = store.ReleaseProviderResources(context.Background(), run.ID, process.ID, group.ID, current.Revision, process.Revision, group.Revision, process.Identity, mustTime(t, 42))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	return store, current, runner
}

func TestProcessExitTimeIsBoundedByRunLifetime(t *testing.T) {
	t.Parallel()
	t.Run("provider/input", func(t *testing.T) {
		store, run, _ := runningOrchestratorRun(t)
		defer store.Close()
		beforeAdmission := mustTime(t, run.AdmittedAt.Int64()-1)
		exit, _ := NewProcessExitCode(1, 0, beforeAdmission)
		if _, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), exit, mustTime(t, 40)); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("predating provider exit = %v", err)
		}
	})

	t.Run("provider/corruption", func(t *testing.T) {
		store, run, _ := runningOrchestratorRun(t)
		defer store.Close()
		exit, _ := NewProcessExitCode(1, 0, mustTime(t, 40))
		if _, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), exit, mustTime(t, 41)); err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE runs SET provider_exit_at_ms = updated_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
		if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("provider exit after updated_at = %v", err)
		}
	})

	t.Run("runner/input", func(t *testing.T) {
		store, current, runner := prepareRunnerRelease(t)
		defer store.Close()
		beforeActivation := mustTime(t, runner.ActivatedAt.Int64()-1)
		exit, _ := NewProcessExitCode(1, 0, beforeActivation)
		if _, _, err := store.RecordLiveRunnerExitAndRelease(context.Background(), current.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, exit, mustTime(t, 43)); !errors.Is(err, ErrConflict) {
			t.Fatalf("runner exit predating activation = %v", err)
		}
		fresh, _, err := store.Run(context.Background(), current.ID)
		if err != nil || fresh.RunnerExit != nil {
			t.Fatalf("rejected predating exit left residue = %+v, %v", fresh, err)
		}
	})

	t.Run("runner/corruption", func(t *testing.T) {
		store, current, runner := prepareRunnerRelease(t)
		defer store.Close()
		exit, _ := NewProcessExitCode(1, 0, mustTime(t, 43))
		if _, _, err := store.RecordLiveRunnerExitAndRelease(context.Background(), current.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, exit, mustTime(t, 43)); err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE runs SET runner_exit_at_ms = updated_at_ms + 1 WHERE id = ?`, current.ID.Bytes())
		if _, _, err := store.Run(context.Background(), current.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("runner exit after updated_at = %v", err)
		}
	})
}

// ObserveProviderExit is a test-only provider exit edge; production records
// provider exits only through the atomic attempt result and absence edges.
func (store *Store) ObserveProviderExit(ctx context.Context, runID RunID, expected Revision, identity ResourceIdentity, exit ProcessExit, at UnixMillis) (Run, error) {
	if runID.zero() || !identity.validFor(ResourceProviderProcess) || !exit.valid() || at.Int64() < exit.at.Int64() {
		return Run{}, fmt.Errorf("%w: invalid process exit observation", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	if !found {
		return Run{}, tx.Rollback(ErrNotFound)
	}
	if exit.at.Int64() < run.AdmittedAt.Int64() {
		return Run{}, tx.Rollback(ErrInvalidValue)
	}
	resources, err := resourcesForRun(ctx, tx.connection, runID)
	if err != nil {
		return Run{}, tx.Rollback(err)
	}
	activatedAt, matched := exitIdentityMatches(resources, identity)
	if !matched {
		return Run{}, tx.Rollback(ErrConflict)
	}
	if exit.at.Int64() < activatedAt.Int64() {
		return Run{}, tx.Rollback(ErrConflict)
	}
	existing := run.ProviderExit
	if existing != nil {
		if !existing.equal(exit) {
			return Run{}, tx.Rollback(ErrConflict)
		}
		if err := tx.Rollback(nil); err != nil {
			return Run{}, err
		}
		return run, nil
	}
	if run.Phase == RunTerminal || run.Revision != expected || at.Int64() < run.UpdatedAt.Int64() {
		return Run{}, tx.Rollback(ErrRevisionConflict)
	}
	exitKind, code, signal := exitSQL(exit)
	newRevision := expected.Int64() + 1
	pending := []pendingInvalidation{{kind: EntityRun, id: run.ID.Bytes(), revision: newRevision}}
	if run.Phase == RunAdmitted || run.Phase == RunRunning {
		if err := requireFinalizingTime(ctx, tx.connection, run, at); err != nil {
			return Run{}, tx.Rollback(err)
		}
		proposal, _ := NewFailureProposal(FailureProviderExit, "provider exited before an attempt outcome")
		kind, proposalCode, proposalDetail, result := proposalSQL(proposal)
		updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = ?, proposal_code = ?, proposal_detail = ?, proposal_result = ?, credential_revoked_at_ms = ?, finalizing_at_ms = ?, provider_exit_kind = ?, provider_exit_sequence = ?, provider_exit_code = ?, provider_exit_signal = ?, provider_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase IN ('admitted', 'running') AND proposal_kind IS NULL AND provider_exit_kind IS NULL AND revision = ?`,
			kind, proposalCode, proposalDetail, result, at.Int64(), at.Int64(), exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		if err := requireOneRow(updated, err); err != nil {
			return Run{}, tx.Rollback(err)
		}
		releasing, err := tx.connection.ExecContext(ctx, `UPDATE resources SET state = 'releasing', revision = revision + 1, updated_at_ms = ? WHERE run_id = ? AND state IN ('declared', 'active')`, at.Int64(), run.ID.Bytes())
		if err := requireRows(releasing, err, 4); err != nil {
			return Run{}, tx.Rollback(err)
		}
		session, found, sessionErr := terminalSessionByRunID(ctx, tx.connection, run.ID)
		if sessionErr != nil || !found {
			if sessionErr == nil {
				sessionErr = ErrCorruptState
			}
			return Run{}, tx.Rollback(sessionErr)
		}
		if err := moveTerminalToReleasing(ctx, tx.connection, session, at); err != nil {
			return Run{}, tx.Rollback(err)
		}
		requestInvalidations, transitionErr := transitionHumanRequestsForRun(ctx, tx.connection, run.ID, at, false, nil, nil)
		if transitionErr != nil {
			return Run{}, tx.Rollback(transitionErr)
		}
		pending = append(pending, requestInvalidations...)
	} else if run.Phase == RunFinalizing {
		updated, err := tx.connection.ExecContext(ctx, `UPDATE runs SET provider_exit_kind = ?, provider_exit_sequence = ?, provider_exit_code = ?, provider_exit_signal = ?, provider_exit_at_ms = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND phase = 'finalizing' AND provider_exit_kind IS NULL AND revision = ?`, exitKind, exit.sequence, code, signal, exit.at.Int64(), at.Int64(), run.ID.Bytes(), expected.Int64())
		if err := requireOneRow(updated, err); err != nil {
			return Run{}, tx.Rollback(err)
		}
	} else {
		return Run{}, tx.Rollback(ErrConflict)
	}
	if err := appendInvalidations(ctx, tx.connection, at, pending); err != nil {
		return Run{}, tx.Rollback(err)
	}
	run, found, err = runByID(ctx, tx.connection, run.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return run, nil
}

func exitIdentityMatches(resources []Resource, identity ResourceIdentity) (UnixMillis, bool) {
	providerProcessMatched := false
	providerGroupMatched := false
	var activatedAt UnixMillis
	var activationFound bool
	for _, resource := range resources {
		if resource.State == ResourceDeclared || !resourceIdentityEqual(resource.Identity, identity) {
			continue
		}
		switch resource.Kind {
		case ResourceProviderProcess:
			providerProcessMatched = true
		case ResourceProviderGroup:
			providerGroupMatched = true
		default:
			continue
		}
		if resource.ActivatedAt == nil || activationFound && *resource.ActivatedAt != activatedAt {
			return UnixMillis{}, false
		}
		activatedAt = *resource.ActivatedAt
		activationFound = true
	}
	return activatedAt, providerProcessMatched && providerGroupMatched && activationFound
}
