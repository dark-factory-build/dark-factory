package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestProviderAndRunnerExitsRemainDistinctInEitherOrder(t *testing.T) {
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
		runnerExit, _ := NewProcessExitCode(1, 0, mustTime(t, 43))
		runnerIdentity := registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess)
		current, err = store.ObserveRunnerExit(context.Background(), run.ID, current.Revision, runnerIdentity, runnerExit, mustTime(t, 44))
		if err != nil || current.Proposal == nil || !current.Proposal.equal(success) || current.ProviderExit == nil || !current.ProviderExit.equal(providerExit) || current.RunnerExit == nil || !current.RunnerExit.equal(runnerExit) {
			t.Fatalf("distinct exits = %+v, %v", current, err)
		}
	})

	t.Run("runner first remains first outcome", func(t *testing.T) {
		store, run, _ := runningOrchestratorRun(t)
		defer store.Close()
		runnerExit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 40))
		current, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess), runnerExit, mustTime(t, 41))
		if err != nil || current.Proposal == nil || current.Proposal.Code() != FailureRunnerExit {
			t.Fatalf("runner-first outcome = %+v, %v", current, err)
		}
		providerExit, _ := NewProcessExitCode(1, 7, mustTime(t, 42))
		current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), providerExit, mustTime(t, 43))
		if err != nil || current.Proposal == nil || current.Proposal.Code() != FailureRunnerExit || current.ProviderExit == nil || current.RunnerExit == nil {
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
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	resources := resourcesForRunTest(t, store, run.ID)
	identity := processIdentity(t, 91)
	for _, kind := range []ResourceKind{ResourceProviderProcess, ResourceProviderGroup} {
		resource := resourceOfKind(t, resources, kind)
		if _, err := store.ActivateResource(context.Background(), run.ID, resource.ID, resource.Revision, identity, mustTime(t, 20)); err != nil {
			t.Fatal(err)
		}
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
	current, err = store.ObserveProviderExit(context.Background(), run.ID, current.Revision, provider.Identity, exit, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []Resource{provider, group} {
		if _, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 44)); err != nil {
			t.Fatalf("%s release after provider exit = %v", resource.Kind.String(), err)
		}
	}
	if _, err := store.ReleaseResource(context.Background(), run.ID, runnerResource.ID, runnerResource.Revision, runnerResource.Identity, mustTime(t, 44)); !errors.Is(err, ErrConflict) {
		t.Fatalf("runner release after only provider exit = %v", err)
	}
	current, err = store.ObserveRunnerExit(context.Background(), run.ID, current.Revision, runnerResource.Identity, exit, mustTime(t, 45))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseResource(context.Background(), run.ID, runnerResource.ID, runnerResource.Revision, runnerResource.Identity, mustTime(t, 46)); err != nil {
		t.Fatalf("runner release after runner exit = %v", err)
	}
}

func TestRelationshipValidationRejectsReleasedProcessWithoutExit(t *testing.T) {
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

func TestProcessExitTimeIsBoundedByRunLifetime(t *testing.T) {
	for _, test := range []struct {
		name    string
		observe func(*Store, Run, ProcessExit, UnixMillis) error
		column  string
	}{
		{name: "provider", column: "provider_exit_at_ms", observe: func(store *Store, run Run, exit ProcessExit, at UnixMillis) error {
			_, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), exit, at)
			return err
		}},
		{name: "runner", column: "runner_exit_at_ms", observe: func(store *Store, run Run, exit ProcessExit, at UnixMillis) error {
			_, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess), exit, at)
			return err
		}},
	} {
		t.Run(test.name+"/input", func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			beforeAdmission := mustTime(t, run.AdmittedAt.Int64()-1)
			exit, _ := NewProcessExitCode(1, 0, beforeAdmission)
			if err := test.observe(store, run, exit, mustTime(t, 40)); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("predating exit = %v", err)
			}
		})

		t.Run(test.name+"/corruption", func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			exit, _ := NewProcessExitCode(1, 0, mustTime(t, 40))
			if err := test.observe(store, run, exit, mustTime(t, 41)); err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE runs SET `+test.column+` = updated_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
			if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("exit after updated_at = %v", err)
			}
		})
	}
}
