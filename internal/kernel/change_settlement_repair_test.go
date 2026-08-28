package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerActivationRequiresExactChangeOwnershipRevision(t *testing.T) {
	for _, test := range []struct {
		name     string
		revision func(Run) int64
	}{
		{name: "fresh available A", revision: func(run Run) int64 { return run.AdmittedChangeRevision.Int64() }},
		{name: "forged available A+3", revision: func(run Run) int64 { return run.AdmittedChangeRevision.Int64() + 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, keys := admittedWorkerRun(t)
			defer store.Close()
			materializeAdmittedWorkerChange(t, store, run, 12)
			activateAllResources(t, store, run, keys, 20)
			session := terminalSessionForRunTest(t, store, run.ID)
			corruptSQL(t, store, `UPDATE changes SET revision = ? WHERE id = ?`, test.revision(run), run.ChangeID.Bytes())
			before := captureWriteFootprint(t, store)
			if _, err := store.ActivateRun(context.Background(), run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 30)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("ActivateRun = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				t.Fatalf("rejected activation footprint before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("fresh available A+2", func(t *testing.T) {
		store, run, keys := admittedWorkerRun(t)
		defer store.Close()
		change := materializeAdmittedWorkerChange(t, store, run, 12)
		if change.Revision.Int64() != run.AdmittedChangeRevision.Int64()+2 {
			t.Fatalf("fresh Change revision = %d, A = %d", change.Revision.Int64(), run.AdmittedChangeRevision.Int64())
		}
		activateAllResources(t, store, run, keys, 20)
		session := terminalSessionForRunTest(t, store, run.ID)
		if running, err := store.ActivateRun(context.Background(), run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 30)); err != nil || running.Phase != RunRunning {
			t.Fatalf("ActivateRun = %+v, %v", running, err)
		}
	})

	t.Run("proven retained available A", func(t *testing.T) {
		store, predecessor := terminalPreRunningAvailableWorker(t)
		defer store.Close()
		agentID, keys := queueRetryForTerminal(t, store, predecessor, 40)
		admission, err := store.AdmitNext(context.Background(), agentID, keys, mustTime(t, 40))
		if err != nil || !admission.Admitted() {
			t.Fatalf("retained retry admission = %+v, %v", admission, err)
		}
		change, found, err := store.Change(context.Background(), *admission.Run.ChangeID)
		if err != nil || !found || change.Phase != ChangeAvailable || change.Revision != *admission.Run.AdmittedChangeRevision {
			t.Fatalf("retained retry Change = %+v, found=%v, err=%v", change, found, err)
		}
		activateAllResources(t, store, *admission.Run, keys, 50)
		session := terminalSessionForRunTest(t, store, admission.Run.ID)
		if running, err := store.ActivateRun(context.Background(), admission.Run.ID, session.ID, admission.Run.Revision, session.Revision, mustTime(t, 60)); err != nil || running.Phase != RunRunning {
			t.Fatalf("ActivateRun = %+v, %v", running, err)
		}
	})
}

func TestPreRunningWorkerSettlementCannotBlessChangedContent(t *testing.T) {
	for _, retained := range []bool{false, true} {
		name := "fresh A+2"
		if retained {
			name = "retained A"
		}
		t.Run(name, func(t *testing.T) {
			for _, field := range []string{"digest", "entry count", "total bytes"} {
				t.Run(field, func(t *testing.T) {
					store, finalizing, change := finalizingPreRunningAvailableWorker(t, retained)
					defer store.Close()
					if retained && change.Revision != *finalizing.AdmittedChangeRevision || !retained && change.Revision.Int64() != finalizing.AdmittedChangeRevision.Int64()+2 {
						t.Fatalf("Change revision = %d, A = %d", change.Revision.Int64(), finalizing.AdmittedChangeRevision.Int64())
					}
					availability := availabilityForChange(t, change)
					switch field {
					case "digest":
						availability.commitment = changeTreeDigest(t, 0xd1)
					case "entry count":
						availability.entries++
					case "total bytes":
						availability.bytes++
					}
					settlement, _ := NewRetainedChangeSettlement(change.Revision, availability)
					before := captureWriteFootprint(t, store)
					if _, err := store.FinalizeWorkerRun(context.Background(), finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); !errors.Is(err, ErrConflict) {
						t.Fatalf("FinalizeWorkerRun = %v", err)
					}
					if after := captureWriteFootprint(t, store); after != before {
						t.Fatalf("rejected settlement footprint before=%+v after=%+v", before, after)
					}
				})
			}
		})
	}
}

func TestRunningWorkerSettlementMayUpdateContentOnStableTree(t *testing.T) {
	blocked, _ := NewBlockedProposal("retain edits")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, blocked)
	defer store.Close()
	change, found, err := store.Change(context.Background(), *finalizing.ChangeID)
	if err != nil || !found {
		t.Fatalf("Change = %+v, found=%v, err=%v", change, found, err)
	}
	availability := availabilityForChange(t, change)
	availability.commitment = changeTreeDigest(t, 0xd2)
	availability.entries++
	availability.bytes++
	settlement, _ := NewRetainedChangeSettlement(change.Revision, availability)
	terminal, err := store.FinalizeWorkerRun(context.Background(), finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80))
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("FinalizeWorkerRun = %+v, %v", terminal, err)
	}
	retained, found, err := store.Change(context.Background(), change.ID)
	if err != nil || !found || retained.Phase != ChangeRetained || !changeAvailabilityMatches(retained, availability) {
		t.Fatalf("retained Change = %+v, found=%v, err=%v", retained, found, err)
	}
}

func TestRetainedRetryHistoryLoadsAndHistoricalFinalizationReplays(t *testing.T) {
	store, first := terminalPreRunningAvailableWorker(t)
	defer store.Close()
	agentID, secondKeys := queueRetryForTerminal(t, store, first, 40)
	secondAdmission, err := store.AdmitNext(context.Background(), agentID, secondKeys, mustTime(t, 40))
	if err != nil || !secondAdmission.Admitted() {
		t.Fatalf("second admission = %+v, %v", secondAdmission, err)
	}
	activateAllResources(t, store, *secondAdmission.Run, secondKeys, 50)
	session := terminalSessionForRunTest(t, store, secondAdmission.Run.ID)
	secondRunning, err := store.ActivateRun(context.Background(), secondAdmission.Run.ID, session.ID, secondAdmission.Run.Revision, session.Revision, mustTime(t, 60))
	if err != nil {
		t.Fatal(err)
	}
	blocked, _ := NewBlockedProposal("retry again")
	secondFinalizing, err := store.ProposeAttemptOutcome(context.Background(), secondKeys.AttemptDigest, blocked, mustTime(t, 70))
	if err != nil {
		t.Fatal(err)
	}
	secondFinalizing = observeMissingProcessExits(t, store, secondRunning.ID, 71)
	releaseAllRunResources(t, store, secondRunning.ID, 72)
	secondFinalizing = closeTerminalSessionAtCurrent(t, store, secondRunning.ID, 80)
	secondChange, found, err := store.Change(context.Background(), *secondRunning.ChangeID)
	if err != nil || !found {
		t.Fatalf("second Change = %+v, found=%v, err=%v", secondChange, found, err)
	}
	secondSettlement, _ := NewRetainedChangeSettlement(secondChange.Revision, availabilityForChange(t, secondChange))
	second, err := store.FinalizeWorkerRun(context.Background(), secondRunning.ID, secondFinalizing.Revision, secondSettlement, mustTime(t, 80))
	if err != nil {
		t.Fatal(err)
	}
	thirdAgent, thirdKeys := queueRetryForTerminalSeed(t, store, second, 81, 230)
	third, err := store.AdmitNext(context.Background(), thirdAgent, thirdKeys, mustTime(t, 81))
	if err != nil || !third.Admitted() {
		t.Fatalf("third admission = %+v, %v", third, err)
	}
	for _, run := range []Run{first, second, *third.Run} {
		if _, found, err := store.Run(context.Background(), run.ID); err != nil || !found {
			t.Fatalf("Run(%s) found=%v, err=%v", run.ID, found, err)
		}
	}
	firstChangeSettlement, _ := NewRetainedChangeSettlement(mustRevision(t, first.AdmittedChangeRevision.Int64()+2), availabilityForChange(t, secondChange))
	if replay, err := store.FinalizeWorkerRun(context.Background(), first.ID, mustRevision(t, first.Revision.Int64()-1), firstChangeSettlement, mustTime(t, 1)); err != nil || replay.Revision != first.Revision {
		t.Fatalf("first replay = %+v, %v", replay, err)
	}
	if replay, err := store.FinalizeWorkerRun(context.Background(), second.ID, secondFinalizing.Revision, secondSettlement, mustTime(t, 1)); err != nil || replay.Revision != second.Revision {
		t.Fatalf("second replay = %+v, %v", replay, err)
	}
}

func materializeAdmittedWorkerChange(t *testing.T, store *Store, run Run, at int64) Change {
	t.Helper()
	selection := testChangeSelection(t)
	tree, _ := NewFileIdentity(70, 80)
	prepared, err := store.RecordChangePrepared(context.Background(), *run.ChangeID, *run.AdmittedChangeRevision, selection, tree, mustTime(t, at))
	if err != nil {
		t.Fatal(err)
	}
	availability := mustChangeAvailability(t, selection.commitment, selection.entries, selection.bytes, tree)
	available, err := store.MarkChangeAvailable(context.Background(), *run.ChangeID, prepared.Revision, availability, mustTime(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	return available
}

func finalizingPreRunningAvailableWorker(t *testing.T, retained bool) (*Store, Run, Change) {
	t.Helper()
	var store *Store
	var run Run
	if retained {
		var predecessor Run
		store, predecessor = terminalPreRunningAvailableWorker(t)
		agentID, keys := queueRetryForTerminal(t, store, predecessor, 40)
		admission, err := store.AdmitNext(context.Background(), agentID, keys, mustTime(t, 40))
		if err != nil || !admission.Admitted() {
			store.Close()
			t.Fatalf("retained retry admission = %+v, %v", admission, err)
		}
		run = *admission.Run
	} else {
		store, run, _ = admittedWorkerRun(t)
		materializeAdmittedWorkerChange(t, store, run, 12)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	runner, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 390), mustTime(t, 50))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureInternal, "pre-running cleanup")
	finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 60))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	runnerExit, _ := NewProcessExitCode(1, 0, mustTime(t, 61))
	finalizing, err = store.ObserveRunnerExit(context.Background(), run.ID, finalizing.Revision, runner.Identity, runnerExit, mustTime(t, 61))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	releaseAllRunResources(t, store, run.ID, 62)
	finalizing = closeTerminalSessionAtCurrent(t, store, run.ID, 70)
	change, found, err := store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found {
		store.Close()
		t.Fatalf("Change = %+v, found=%v, err=%v", change, found, err)
	}
	return store, finalizing, change
}

func availabilityForChange(t *testing.T, change Change) ChangeAvailability {
	t.Helper()
	if change.Selection == nil || change.TreeIdentity == nil {
		t.Fatal("available Change has no content facts")
	}
	return mustChangeAvailability(t, change.Selection.commitment, change.Selection.entries, change.Selection.bytes, *change.TreeIdentity)
}
