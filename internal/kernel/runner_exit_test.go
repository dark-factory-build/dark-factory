package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveredRunnerAbsenceRequiresRegisteredRunner(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	exit, err := NewProcessExitRecoveredAbsence(1, mustTime(t, 20))
	if err != nil || !exit.RecoveredAbsence() {
		t.Fatalf("construct recovered absence = %+v, %v", exit, err)
	}
	if _, hasCode := exit.Code(); hasCode {
		t.Fatal("recovered absence invented an exit code")
	}
	if _, hasSignal := exit.Signal(); hasSignal {
		t.Fatal("recovered absence invented an exit signal")
	}
	if _, err := NewProcessExitRecoveredAbsence(0, mustTime(t, 20)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero sequence = %v", err)
	}
	if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, processIdentity(t, 91), exit, mustTime(t, 21)); !errors.Is(err, ErrConflict) {
		t.Fatalf("unregistered recovered absence = %v", err)
	}
	fresh, found, err := store.Run(context.Background(), run.ID)
	if err != nil || !found || fresh.Phase != RunAdmitted || fresh.Revision != run.Revision || fresh.RunnerExit != nil {
		t.Fatalf("rejected observation footprint = %+v, found=%v, err=%v", fresh, found, err)
	}
}

func TestRecoveredRunnerAbsenceFromAdmittedRunConvergesExactly(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	registered, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 91), mustTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 21))
	observed, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registered.Identity, exit, mustTime(t, 22))
	if err != nil || observed.Phase != RunFinalizing || observed.Proposal == nil || observed.Proposal.code != FailureRunnerExit || observed.RunnerExit == nil || !observed.RunnerExit.equal(exit) || observed.CredentialRevokedAt == nil {
		t.Fatalf("observed admitted disappearance = %+v, %v", observed, err)
	}
	if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("recovered admitted credential = %v", err)
	}
	currentRunner, _, _ := store.Resource(context.Background(), registered.ID)
	if currentRunner.State != ResourceReleasing || currentRunner.Revision.Int64() != registered.Revision.Int64()+1 {
		t.Fatalf("runner release transition = %+v", currentRunner)
	}
	beforeReplay, _ := store.Factory(context.Background())
	replay, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registered.Identity, exit, mustTime(t, 99))
	afterReplay, _ := store.Factory(context.Background())
	if err != nil || replay.Revision != observed.Revision || afterReplay.Head != beforeReplay.Head {
		t.Fatalf("replay = %+v, %v; heads %v -> %v", replay, err, beforeReplay.Head, afterReplay.Head)
	}
	code, _ := NewProcessExitCode(1, 0, mustTime(t, 21))
	if _, err := store.ObserveRunnerExit(context.Background(), run.ID, observed.Revision, registered.Identity, code, mustTime(t, 23)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting code after recovered absence = %v", err)
	}
	signal, _ := NewProcessExitSignal(1, 9, mustTime(t, 21))
	if _, err := store.ObserveRunnerExit(context.Background(), run.ID, observed.Revision, registered.Identity, signal, mustTime(t, 23)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting signal after recovered absence = %v", err)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, observed.Revision, mustTime(t, 24)); !errors.Is(err, ErrConflict) {
		t.Fatalf("finalize with unreleased resources = %v", err)
	}
	releaseAllRunResources(t, store, run.ID, 30)
	terminal, err := store.FinalizeRun(context.Background(), run.ID, observed.Revision, mustTime(t, 40))
	if err != nil || terminal.Phase != RunTerminal || terminal.Terminal == nil || terminal.Terminal.code != FailureRunnerExit {
		t.Fatalf("terminal after recovered absence = %+v, %v", terminal, err)
	}
}

func TestRecoveredRunnerAbsenceFromRunningRunRevokesAuthorityAndRoundTrips(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	path := storePath(t, store)
	if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitRecoveredAbsence(7, mustTime(t, 40))
	runnerIdentity := registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess)
	observed, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, runnerIdentity, exit, mustTime(t, 41))
	if err != nil || observed.Phase != RunFinalizing || observed.Revision.Int64() != run.Revision.Int64()+1 || observed.Proposal == nil || observed.Proposal.code != FailureRunnerExit || observed.RunnerExit == nil || !observed.RunnerExit.equal(exit) {
		store.Close()
		t.Fatalf("observed running disappearance = %+v, %v", observed, err)
	}
	if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		store.Close()
		t.Fatalf("recovered running credential = %v", err)
	}
	success, _ := NewSuccessProposal("too late")
	if _, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, success, mustTime(t, 42)); !errors.Is(err, ErrUnauthorized) {
		store.Close()
		t.Fatalf("outcome after recovered absence = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh, found, err := reopened.Run(context.Background(), run.ID)
	if err != nil || !found || fresh.RunnerExit == nil || !fresh.RunnerExit.equal(exit) || !fresh.RunnerExit.RecoveredAbsence() {
		t.Fatalf("reopened recovered absence = %+v, found=%v, err=%v", fresh, found, err)
	}
	fresh = observeMissingProcessExits(t, reopened, run.ID, 50)
	releaseAllRunResources(t, reopened, run.ID, 52)
	terminal, err := reopened.FinalizeRun(context.Background(), run.ID, fresh.Revision, mustTime(t, 60))
	if err != nil || terminal.Phase != RunTerminal || terminal.Terminal == nil || terminal.Terminal.code != FailureRunnerExit {
		t.Fatalf("running disappearance terminal = %+v, %v", terminal, err)
	}
}

func TestTypedOutcomeRemainsFirstWhenRecoveredAbsenceArrives(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	proposal, _ := NewSuccessProposal("first")
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 41))
	observed, err := store.ObserveRunnerExit(context.Background(), run.ID, finalizing.Revision, registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess), exit, mustTime(t, 42))
	if err != nil || observed.Proposal == nil || !observed.Proposal.equal(proposal) || observed.RunnerExit == nil || !observed.RunnerExit.equal(exit) {
		t.Fatalf("completion then recovered absence = %+v, %v", observed, err)
	}
}

func TestRunnerExitKindSchemaAndScannerFailClosed(t *testing.T) {
	updates := []struct {
		name      string
		statement string
	}{
		{"missing kind", `UPDATE runs SET runner_exit_kind = NULL WHERE id = ?`},
		{"unknown kind", `UPDATE runs SET runner_exit_kind = 'unknown' WHERE id = ?`},
		{"code without status", `UPDATE runs SET runner_exit_kind = 'code', runner_exit_code = NULL WHERE id = ?`},
		{"signal without status", `UPDATE runs SET runner_exit_kind = 'signal', runner_exit_signal = NULL WHERE id = ?`},
		{"absence with code", `UPDATE runs SET runner_exit_code = 0 WHERE id = ?`},
		{"absence without sequence", `UPDATE runs SET runner_exit_sequence = NULL WHERE id = ?`},
	}
	for _, test := range updates {
		t.Run("schema/"+test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 40))
			if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess), exit, mustTime(t, 41)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.writer.Exec(test.statement, run.ID.Bytes()); err == nil {
				t.Fatal("illegal runner exit update succeeded")
			}
			fresh, found, err := store.Run(context.Background(), run.ID)
			if err != nil || !found || fresh.RunnerExit == nil || !fresh.RunnerExit.equal(exit) {
				t.Fatalf("failed schema update changed exit = %+v, found=%v, err=%v", fresh, found, err)
			}
		})
	}

	corruptions := []struct {
		name      string
		statement string
	}{
		{"missing kind", `UPDATE runs SET runner_exit_kind = NULL WHERE id = ?`},
		{"unknown kind", `UPDATE runs SET runner_exit_kind = 'unknown' WHERE id = ?`},
		{"absence with code", `UPDATE runs SET runner_exit_code = 0 WHERE id = ?`},
		{"code without status", `UPDATE runs SET runner_exit_kind = 'code' WHERE id = ?`},
	}
	for _, test := range corruptions {
		t.Run("scanner/"+test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			path := storePath(t, store)
			exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 40))
			if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, registeredProcessIdentity(t, store, run.ID, ResourceRunnerProcess), exit, mustTime(t, 41)); err != nil {
				store.Close()
				t.Fatal(err)
			}
			factory, _ := store.Factory(context.Background())
			corruptSQL(t, store, test.statement, run.ID.Bytes())
			if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("direct corrupt runner exit read = %v", err)
			}
			before := captureWriteFootprint(t, store)
			if _, err := store.SetDispatch(context.Background(), factory.Revision, false, mustTime(t, 100)); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("validated write over corrupt runner exit = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				store.Close()
				t.Fatalf("validated refusal footprint = before %+v after %+v", before, after)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			beforeOpen := captureDatabaseEvidence(t, path)
			if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("open corrupt runner exit = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, beforeOpen)
		})
	}
}

func releaseAllRunResources(t *testing.T, store *Store, runID RunID, at int64) {
	t.Helper()
	for index, resource := range resourcesForRunTest(t, store, runID) {
		if _, err := store.ReleaseResource(context.Background(), runID, resource.ID, resource.Revision, resource.Identity, mustTime(t, at+int64(index))); err != nil {
			t.Fatal(err)
		}
	}
}

func observeMissingProcessExits(t testing.TB, store *Store, runID RunID, at int64) Run {
	t.Helper()
	run, found, err := store.Run(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("read run before process exits: found=%v err=%v", found, err)
	}
	exit, err := NewProcessExitCode(1, 0, mustTimeTB(t, at))
	if err != nil {
		t.Fatal(err)
	}
	if run.ProviderExit == nil {
		provider := resourceOfKindTB(t, resourcesForRunTB(t, store, runID), ResourceProviderProcess)
		if !provider.Identity.Empty() {
			run, err = store.ObserveProviderExit(context.Background(), runID, run.Revision, provider.Identity, exit, mustTimeTB(t, at))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if run.RunnerExit == nil {
		runner := resourceOfKindTB(t, resourcesForRunTB(t, store, runID), ResourceRunnerProcess)
		if !runner.Identity.Empty() {
			run, err = store.ObserveRunnerExit(context.Background(), runID, run.Revision, runner.Identity, exit, mustTimeTB(t, at+1))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return run
}

func mustTimeTB(t testing.TB, value int64) UnixMillis {
	t.Helper()
	result, err := NewUnixMillis(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
