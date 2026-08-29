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
	// No generic runner-exit observation exists: recovered absence can only
	// enter through the atomic release edges, so an unregistered absence has
	// no seam to probe and the run is asserted unchanged below.
	fresh, found, err := store.Run(context.Background(), run.ID)
	if err != nil || !found || fresh.Phase != RunAdmitted || fresh.Revision != run.Revision || fresh.RunnerExit != nil {
		t.Fatalf("rejected observation footprint = %+v, found=%v, err=%v", fresh, found, err)
	}
}

func TestRecoveredAbsenceCannotUseLiveTerminalClose(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	proposal, _ := NewFailureProposal(FailureInternal, "owner disappeared")
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	providerIdentity := registeredProcessIdentity(t, store, run.ID, ResourceProviderProcess)
	providerExit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 41))
	finalizing, err = store.ObserveProviderExit(context.Background(), run.ID, finalizing.Revision, providerIdentity, providerExit, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	finalizing, _, _, err = store.ReleaseProviderResources(context.Background(), run.ID, process.ID, group.ID, finalizing.Revision, process.Revision, group.Revision, process.Identity, mustTime(t, 42))
	if err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	finalizing, _, err = store.RecordRecoveredRunnerAbsence(context.Background(), run.ID, runner.ID, finalizing.Revision, runner.Revision, runner.Identity, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	// The generic live-close authority that once had to refuse recovered
	// evidence no longer exists; the only close is the exact result edge,
	// which is exercised elsewhere. Nothing remains to attack here.
	if _, _, err := store.Run(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeclaredNoStartFailureClosesWithoutInventedExit(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	failure, _ := NewFailureProposal(FailureSpawn, "did not start")
	if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20)); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic failure without runtime evidence = %v", err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	fresh, err := store.FailRunWithRuntimeAbsent(context.Background(), run.ID, runtime.ID, run.Revision, runtime.Revision, failure, mustTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	if session.State != TerminalSessionClosed || session.ActivatedAt != nil {
		t.Fatalf("no-start failure left session open = %+v", session)
	}
	if fresh.ProviderExit != nil || fresh.RunnerExit != nil {
		t.Fatalf("no-start failure invented exits: provider=%+v runner=%+v", fresh.ProviderExit, fresh.RunnerExit)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, fresh.Revision, mustTime(t, 41)); err != nil {
		t.Fatalf("no-start finalization = %v", err)
	}
}

func TestRecoveredRunnerAbsenceFromRunningRunRevokesAuthorityAndRoundTrips(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	path := storePath(t, store)
	if _, err := store.AuthenticateAttempt(context.Background(), keys.AttemptDigest); err != nil {
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureInternal, "runner absent in recovery")
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40))
	if err != nil || finalizing.Phase != RunFinalizing || finalizing.CredentialRevokedAt == nil {
		store.Close()
		t.Fatalf("finalize running disappearance = %+v, %v", finalizing, err)
	}
	runner := resourceOfKindTB(t, resourcesForRunTB(t, store, run.ID), ResourceRunnerProcess)
	observed, _, err := store.RecordRecoveredRunnerAbsence(context.Background(), run.ID, runner.ID, finalizing.Revision, runner.Revision, runner.Identity, mustTime(t, 41))
	if err != nil || observed.RunnerExit == nil || !observed.RunnerExit.RecoveredAbsence() {
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
	if err != nil || !found || fresh.RunnerExit == nil || !fresh.RunnerExit.RecoveredAbsence() {
		t.Fatalf("reopened recovered absence = %+v, found=%v, err=%v", fresh, found, err)
	}
	providerIdentity := registeredProcessIdentity(t, reopened, run.ID, ResourceProviderProcess)
	providerExit, err := NewProcessExitRecoveredAbsence(8, mustTime(t, 50))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err = reopened.ObserveProviderExit(context.Background(), run.ID, fresh.Revision, providerIdentity, providerExit, mustTime(t, 50))
	if err != nil {
		t.Fatal(err)
	}
	releaseAllRunResources(t, reopened, run.ID, 52)
	fresh, found, err = reopened.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("recovered running reload = %+v, found=%v, err=%v", fresh, found, err)
	}
	session := terminalSessionForRunTest(t, reopened, run.ID)
	unresolved, err := reopened.MarkTerminalSessionUnresolved(context.Background(), run.ID, session.ID, fresh.Revision, session.Revision, "runner recovered", mustTime(t, 60))
	if err != nil || unresolved.State != TerminalSessionUnresolved {
		t.Fatalf("mark recovered unresolved = %+v, err=%v", unresolved, err)
	}
	fresh, found, err = reopened.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("unresolved recovered reload = %+v, found=%v, err=%v", fresh, found, err)
	}
	// A no-result history deliberately cannot terminalize: the recovered
	// close authority is gone, the exact result edge cannot authenticate a
	// recovered-absence provider exit, and the finalizer refuses the open
	// session. The run stays finalizing and discoverable — the wedge is the
	// design, not a gap.
	if _, err := reopened.FinalizeRun(context.Background(), run.ID, fresh.Revision, mustTime(t, 60)); !errors.Is(err, ErrConflict) {
		t.Fatalf("no-result history terminalized without result authority = %v", err)
	}
	fresh, found, err = reopened.Run(context.Background(), run.ID)
	if err != nil || !found || fresh.Phase != RunFinalizing {
		t.Fatalf("wedged no-result run = %+v, found=%v, err=%v", fresh, found, err)
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
	runner := resourceOfKindTB(t, resourcesForRunTB(t, store, run.ID), ResourceRunnerProcess)
	observed, _, err := store.RecordRecoveredRunnerAbsence(context.Background(), run.ID, runner.ID, finalizing.Revision, runner.Revision, runner.Identity, mustTime(t, 42))
	if err != nil || observed.Proposal == nil || !observed.Proposal.equal(proposal) || observed.RunnerExit == nil || !observed.RunnerExit.RecoveredAbsence() {
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
			exit := recordRecoveredRunnerAbsenceForTest(t, store, run, 40)
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
			recordRecoveredRunnerAbsenceForTest(t, store, run, 40)
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

// recordRecoveredRunnerAbsenceForTest finalizes a running run and records the
// exact recovered-absence runner exit through the owned absence edge,
// returning the ProcessExit value the durable row must round-trip.
func recordRecoveredRunnerAbsenceForTest(t testing.TB, store *Store, run Run, at int64) ProcessExit {
	t.Helper()
	failure, err := NewFailureProposal(FailureInternal, "runner absent in recovery")
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTimeTB(t, at))
	if err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKindTB(t, resourcesForRunTB(t, store, run.ID), ResourceRunnerProcess)
	if _, _, err := store.RecordRecoveredRunnerAbsence(context.Background(), run.ID, runner.ID, finalizing.Revision, runner.Revision, runner.Identity, mustTimeTB(t, at+1)); err != nil {
		t.Fatal(err)
	}
	exit, err := NewProcessExitRecoveredAbsence(1, mustTimeTB(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	return exit
}

func releaseAllRunResources(t *testing.T, store *Store, runID RunID, at int64) {
	t.Helper()
	resources := resourcesForRunTest(t, store, runID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	run, found, err := store.Run(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("read run for provider release: found=%v err=%v", found, err)
	}
	if process.State != ResourceReleased {
		run, _, _, err = store.ReleaseProviderResources(context.Background(), runID, process.ID, group.ID, run.Revision, process.Revision, group.Revision, process.Identity, mustTime(t, at))
		if err != nil {
			t.Fatal(err)
		}
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, runID), ResourceRunnerProcess)
	if runner.State != ResourceReleased && !runner.Identity.Empty() && run.RunnerExit == nil {
		exit, exitErr := NewProcessExitCode(1, 0, mustTime(t, at+1))
		if exitErr != nil {
			t.Fatal(exitErr)
		}
		run, runner, err = store.RecordLiveRunnerExitAndRelease(context.Background(), runID, runner.ID, run.Revision, runner.Revision, runner.Identity, exit, mustTime(t, at+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	// The runner is released only through its exact exit/absence edges above
	// or by the caller; generic release authority no longer exists for it.
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, runID), ResourceRuntimeRoot)
	if runtime.State != ResourceReleased {
		if _, err := store.ReleaseResource(context.Background(), runID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, at+2)); err != nil {
			t.Fatal(err)
		}
	}
}

// closeTerminalSessionAtCurrent closes the fixture's session through the one
// close authority production retains: reauthenticating the run's exact
// attempt result. The result is reconstructed from the fixture's own durable
// facts (digests, runtime and provider identities, the recorded provider
// exit), so any fixture history the production grammar cannot close fails
// loudly here instead of borrowing deleted generic authority.
func closeTerminalSessionAtCurrent(t testing.TB, store *Store, runID RunID, at int64) Run {
	t.Helper()
	run, found, err := store.Run(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("read run for terminal close: %+v, found=%v, err=%v", run, found, err)
	}
	session := terminalSessionForRunTest(t, store, runID)
	if session.State == TerminalSessionClosed {
		return run
	}
	resources := resourcesForRunTB(t, store, runID)
	runtime := resourceOfKindTB(t, resources, ResourceRuntimeRoot)
	provider := resourceOfKindTB(t, resources, ResourceProviderProcess)
	if run.ProviderExit == nil {
		t.Fatalf("fixture history has no provider exit; the result close cannot authenticate: run=%+v", run)
	}
	var exit AttemptResultExit
	if code, hasCode := run.ProviderExit.Code(); hasCode {
		exit, err = NewAttemptResultExitCode(code)
	} else {
		signal, _ := run.ProviderExit.Signal()
		exit, err = NewAttemptResultExitSignal(signal)
	}
	if err != nil {
		t.Fatalf("fixture provider exit is not result-representable: %+v err=%v", run.ProviderExit, err)
	}
	result, err := NewInnerConvergedAttemptResult(runID, run.CredentialDigest, run.resultProofDigest, runtime.Identity, provider.Identity, exit)
	if err != nil {
		t.Fatalf("reconstruct fixture attempt result: %v", err)
	}
	if _, _, err := store.CloseTerminalAfterRunner(context.Background(), result, run.Revision, session.Revision, mustTimeTB(t, at)); err != nil {
		t.Fatalf("result close: %v; run=%+v session=%+v resources=%+v", err, run, session, resources)
	}
	closed, found, err := store.Run(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("read run after terminal close: %+v, found=%v, err=%v", closed, found, err)
	}
	return closed
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
	resources := resourcesForRunTB(t, store, runID)
	provider := resourceOfKindTB(t, resources, ResourceProviderProcess)
	group := resourceOfKindTB(t, resources, ResourceProviderGroup)
	if provider.State != ResourceReleased {
		run, _, _, err = store.ReleaseProviderResources(context.Background(), runID, provider.ID, group.ID, run.Revision, provider.Revision, group.Revision, provider.Identity, mustTimeTB(t, at+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.RunnerExit == nil {
		runner := resourceOfKindTB(t, resourcesForRunTB(t, store, runID), ResourceRunnerProcess)
		if !runner.Identity.Empty() {
			run, _, err = store.RecordLiveRunnerExitAndRelease(context.Background(), runID, runner.ID, run.Revision, runner.Revision, runner.Identity, exit, mustTimeTB(t, at+2))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return run
}

func recordRunnerExitForTest(t testing.TB, store *Store, runID RunID, exit ProcessExit, at int64) Run {
	t.Helper()
	run, found, err := store.Run(context.Background(), runID)
	if err != nil || !found {
		t.Fatalf("read run before runner exit: found=%v err=%v", found, err)
	}
	resources := resourcesForRunTB(t, store, runID)
	process := resourceOfKindTB(t, resources, ResourceProviderProcess)
	group := resourceOfKindTB(t, resources, ResourceProviderGroup)
	if process.State != ResourceReleased {
		if run.ProviderExit == nil && !process.Identity.Empty() {
			providerExit := exit
			if exit.RecoveredAbsence() {
				providerExit, err = NewProcessExitRecoveredAbsence(1, mustTimeTB(t, at))
			}
			if err == nil {
				run, err = store.ObserveProviderExit(context.Background(), runID, run.Revision, process.Identity, providerExit, mustTimeTB(t, at))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		run, _, _, err = store.ReleaseProviderResources(context.Background(), runID, process.ID, group.ID, run.Revision, process.Revision, group.Revision, process.Identity, mustTimeTB(t, at+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	runner := resourceOfKindTB(t, resourcesForRunTB(t, store, runID), ResourceRunnerProcess)
	if exit.RecoveredAbsence() {
		run, _, err = store.RecordRecoveredRunnerAbsence(context.Background(), runID, runner.ID, run.Revision, runner.Revision, runner.Identity, mustTimeTB(t, at+2))
	} else {
		run, _, err = store.RecordLiveRunnerExitAndRelease(context.Background(), runID, runner.ID, run.Revision, runner.Revision, runner.Identity, exit, mustTimeTB(t, at+2))
	}
	if err != nil {
		t.Fatal(err)
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
