package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestFinalizingRequiresExactResourceFootprint(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Store, Run, AdmissionKeys) error
	}{
		{name: "daemon failure", invoke: func(store *Store, run Run, _ AdmissionKeys) error {
			failure, _ := NewFailureProposal(FailureProtocol, "control failed")
			_, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40))
			return err
		}},
		{name: "attempt outcome", invoke: func(store *Store, _ Run, keys AdmissionKeys) error {
			proposal, _ := NewSuccessProposal("done")
			_, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
			return err
		}},
		{name: "operator cancellation", invoke: func(store *Store, run Run, _ AdmissionKeys) error {
			_, err := store.CancelRun(context.Background(), run.ID, run.Revision, "cancel", mustTime(t, 40))
			return err
		}},
		// Runner exit observation no longer enters finalizing: the runner
		// converges only through its owned exit/absence release edges, which
		// never move the provider footprint.
		{name: "provider exit", invoke: func(store *Store, run Run, _ AdmissionKeys) error {
			exit, _ := NewProcessExitCode(1, 0, mustTime(t, 39))
			_, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess), exit, mustTime(t, 40))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, run, keys := runningOrchestratorRun(t)
			defer store.Close()
			if _, err := store.writer.Exec(`CREATE TRIGGER suppress_one_resource_release BEFORE UPDATE OF state ON resources WHEN OLD.kind = 'provider_group' AND NEW.state = 'releasing' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := test.invoke(store, run, keys); !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("split finalizing write = %v", err)
			}
			fresh, found, err := store.Run(context.Background(), run.ID)
			if err != nil || !found || fresh.Phase != RunRunning || fresh.Revision != run.Revision || fresh.Proposal != nil || fresh.ProviderExit != nil || fresh.RunnerExit != nil || fresh.CredentialRevokedAt != nil {
				t.Fatalf("run changed despite resource rollback = %+v found=%v err=%v", fresh, found, err)
			}
			for _, resource := range resourcesForRunTest(t, store, run.ID) {
				if resource.State != ResourceActive {
					t.Fatalf("%s split state = %s", resource.Kind.String(), resource.State.String())
				}
			}
			if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); err != nil {
				t.Fatalf("rolled-back credential = %v", err)
			}
		})
	}
}

func TestFailRunAcceptsOnlyInfrastructureFailureCodes(t *testing.T) {
	for _, code := range []FailureCode{FailureAttempt, FailureProviderExit, FailureRunnerExit} {
		t.Run(code.String(), func(t *testing.T) {
			store, run, keys := runningOrchestratorRun(t)
			defer store.Close()
			failure, _ := NewFailureProposal(code, "forged semantic outcome")
			if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40)); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("FailRun semantic forgery = %v", err)
			}
			fresh, found, err := store.Run(context.Background(), run.ID)
			if err != nil || !found || fresh.Phase != RunRunning || fresh.Revision != run.Revision || fresh.Proposal != nil {
				t.Fatalf("semantic forgery footprint = %+v found=%v err=%v", fresh, found, err)
			}
			if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); err != nil {
				t.Fatalf("semantic forgery revoked credential: %v", err)
			}
		})
	}
}
