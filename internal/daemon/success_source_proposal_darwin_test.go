//go:build darwin

package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProposeOutcomeRefusesBeforeDurableProposalForLiveWorker(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 0x36, testID(0x36), "worker")
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v found=%v err=%v", session, found, err)
	}
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	digest := sha256.Sum256(active.bearer)
	live.attemptDigest, _ = kernel.AttemptDigestFromBytes(digest[:])
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.daemon.unregisterLiveAttempt(active.run.ID, live) })
	parent := t.TempDir()
	fixture.daemon.RememberSupervisorAccount(parent, "", "git")
	inspections := 0
	fixture.daemon.successSourceInspect = func(context.Context, string, string, change.RepositoryIdentity, string) (change.WorktreeFacts, error) {
		inspections++
		if inspections == 2 {
			return change.WorktreeFacts{}, errors.New("injected source mutation after clean snapshot")
		}
		return change.WorktreeFacts{}, nil
	}

	done := fixture.serve(t)
	if _, err := active.client.Succeed(context.Background(), "dirty implementation"); err == nil {
		t.Fatal("dirty-source success unexpectedly accepted")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
			t.Fatalf("dirty-source refusal = %v", err)
		}
	}
	waitDispatch(t, done)
	observed, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || observed.Phase != kernel.RunRunning || observed.Proposal != nil {
		t.Fatalf("refusal changed durable run: %+v found=%v err=%v", observed, found, err)
	}
	fixture.daemon.successSourceInspect = func(context.Context, string, string, change.RepositoryIdentity, string) (change.WorktreeFacts, error) {
		return change.WorktreeFacts{}, nil
	}
	// The correction removes the uncommitted source claim; the same bearer and
	// live owner can retry, and the exact run can then settle normally.
	execSupervisorSQL(t, fixture.databasePath, `UPDATE changes SET head_commit = NULL WHERE id = ?`, active.run.ChangeID.Bytes())
	done = fixture.serve(t)
	if _, err := active.client.Succeed(context.Background(), "corrected no-change success"); err != nil {
		t.Fatalf("corrected success retry = %v", err)
	}
	waitDispatch(t, done)
	finalizing, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || finalizing.Phase != kernel.RunFinalizing || finalizing.Proposal == nil {
		t.Fatalf("retry did not durably propose: %+v found=%v err=%v", finalizing, found, err)
	}
	resources, err := fixture.store.Resources(context.Background(), active.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var runtime, provider, runner kernel.Resource
	for _, resource := range resources {
		switch resource.Kind {
		case kernel.ResourceRuntimeRoot:
			runtime = resource
		case kernel.ResourceProviderProcess:
			provider = resource
		case kernel.ResourceRunnerProcess:
			runner = resource
		}
	}
	providerExit, err := kernel.NewAttemptResultExitCode(0)
	if err != nil {
		t.Fatal(err)
	}
	attemptResult, err := kernel.NewInnerConvergedAttemptResult(active.run.ID, active.run.CredentialDigest, active.run.ResultProofDigest(), runtime.Identity, provider.Identity, providerExit)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.ConsumeAttemptResult(context.Background(), attemptResult, finalizing.Revision, mustKernelTime(t, 1501))
	if err != nil {
		t.Fatal(err)
	}
	runnerExit, err := kernel.NewProcessExitCode(1, 0, mustKernelTime(t, 1502))
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = fixture.store.RecordLiveRunnerExitAndRelease(context.Background(), active.run.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, runnerExit, mustKernelTime(t, 1503))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReleaseResource(context.Background(), active.run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustKernelTime(t, 1504)); err != nil {
		t.Fatal(err)
	}
	session, found, err = fixture.store.TerminalSessionForRun(context.Background(), active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v found=%v err=%v", session, found, err)
	}
	current, _, err = fixture.store.CloseTerminalAfterRunner(context.Background(), attemptResult, current.Revision, session.Revision, mustKernelTime(t, 1505))
	if err != nil {
		t.Fatal(err)
	}
	changeState, found, err := fixture.store.Change(context.Background(), *active.run.ChangeID)
	if err != nil || !found {
		t.Fatalf("retry Change = %+v found=%v err=%v", changeState, found, err)
	}
	settlement, err := kernel.NewRetainedChangeSettlement(changeState.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := fixture.store.FinalizeWorkerRun(context.Background(), active.run.ID, current.Revision, settlement, mustKernelTime(t, 1506))
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Kind() != kernel.OutcomeSucceeded {
		t.Fatalf("retry settlement = %+v err=%v", settled, err)
	}
	changeState, found, err = fixture.store.Change(context.Background(), *active.run.ChangeID)
	if err != nil || !found || changeState.Phase != kernel.ChangeRetained || changeState.SettledRunID == nil || *changeState.SettledRunID != active.run.ID {
		t.Fatalf("retry Change settlement = %+v found=%v err=%v", changeState, found, err)
	}
}
