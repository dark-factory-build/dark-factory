//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// failBeforeRuntime moves the fixture's admitted run to finalizing with its
// whole declared footprint released, exactly as the runtime-absent failure
// edge does, leaving settlement as the only remaining authority.
func (fixture *recoveryFixture) failBeforeRuntime(t *testing.T) kernel.Run {
	t.Helper()
	ctx := context.Background()
	failure, err := kernel.NewFailureProposal(kernel.FailureSpawn, "settlement test failure")
	if err != nil {
		t.Fatal(err)
	}
	resource, found, err := fixture.store.Resource(ctx, fixture.keys.Resources.RuntimeRoot)
	if err != nil || !found {
		t.Fatalf("runtime resource: found=%v err=%v", found, err)
	}
	run, err := fixture.store.FailRunWithRuntimeAbsent(ctx, fixture.run.ID, resource.ID, fixture.currentRun(t).Revision, resource.Revision, failure, mustKernelTime(t, 400))
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = run
	return run
}

func TestSettleRunFinalizesOrchestratorAndReplaysTerminal(t *testing.T) {
	fixture := newRecoveryFixture(t, 0x60)
	if _, err := fixture.daemon.settleRun(fixture.changeParent, fixture.run.ID); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("admitted run settlement = %v", err)
	}
	fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSpawn {
		t.Fatalf("settled orchestrator run = %+v, %v", settled, err)
	}
	replay, err := fixture.daemon.settleRun(fixture.changeParent, fixture.run.ID)
	if err != nil || replay.Phase != kernel.RunTerminal || replay.Revision != settled.Revision {
		t.Fatalf("terminal replay = %+v, %v", replay, err)
	}
}

func TestSettleRunAbandonsUnpublishedWorkerChange(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x70, kernel.RoleWorker)
	fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(fixture.changeParent, fixture.run.ID)
	if err != nil || settled.Phase != kernel.RunTerminal || settled.Terminal == nil {
		t.Fatalf("settled worker run = %+v, %v", settled, err)
	}
	if settled.ChangeID == nil {
		t.Fatal("worker run lost its candidate change")
	}
	changeState, found, err := fixture.store.Change(context.Background(), *settled.ChangeID)
	if err != nil || !found || changeState.Phase != kernel.ChangeAbandoned || changeState.SettledRunID == nil || *changeState.SettledRunID != settled.ID {
		t.Fatalf("abandoned change = %+v found=%v err=%v", changeState, found, err)
	}
}

// TestSettleRunRefusesUnverifiablePublishedChange pins the retained arm's
// refusal: a durably published change whose tree cannot be re-read settles
// nothing, and the run stays finalizing and discoverable.
func TestSettleRunRefusesUnverifiablePublishedChange(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x80, kernel.RoleWorker)
	ctx := context.Background()
	run := fixture.currentRun(t)
	if run.ChangeID == nil {
		t.Fatal("worker run without candidate change")
	}
	changeState, found, err := fixture.store.Change(ctx, *run.ChangeID)
	if err != nil || !found {
		t.Fatalf("change: found=%v err=%v", found, err)
	}
	format, err := kernel.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := kernel.NewCommitID(format, bytes.Repeat([]byte{0x81}, 20))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.TreeDigestFromBytes(bytes.Repeat([]byte{0x82}, kernel.DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.NewFileIdentity(7, 8)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := kernel.NewChangeSelection(format, commit, digest, 1, 64, repository)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kernel.NewFileIdentity(9, 10)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.RecordChangePrepared(ctx, changeState.ID, changeState.Revision, selection, tree, mustKernelTime(t, 300))
	if err != nil {
		t.Fatal(err)
	}
	availability, err := kernel.NewChangeAvailability(digest, 1, 64, tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangeAvailable(ctx, changeState.ID, prepared.Revision, availability, mustKernelTime(t, 310)); err != nil {
		t.Fatal(err)
	}
	before := fixture.failBeforeRuntime(t)
	settled, err := fixture.daemon.settleRun(fixture.changeParent, fixture.run.ID)
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("unverifiable published change settlement = %+v, %v", settled, err)
	}
	after := fixture.currentRun(t)
	if after.Phase != kernel.RunFinalizing || after.Revision != before.Revision {
		t.Fatalf("refused settlement mutated the run: %+v -> %+v", before, after)
	}
}

func TestScheduledCompletionSettlesReturnedReleasedRun(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x88, kernel.RoleWorker)
	failed := fixture.failBeforeRuntime(t)
	if err := fixture.daemon.validateScheduledCompletion(fixture.changeParent, nil, failed); err != nil {
		t.Fatalf("released completion = %v", err)
	}
	settled := fixture.currentRun(t)
	if settled.Phase != kernel.RunTerminal || settled.Terminal == nil || settled.Terminal.Code() != kernel.FailureSpawn {
		t.Fatalf("released completion stayed nonterminal: %+v", settled)
	}
}

// TestScheduledCompletionSurfacesUnsettledRun pins the non-fatal refusal:
// residue that still cannot settle remains visible while scheduling continues.
func TestScheduledCompletionSurfacesUnsettledRun(t *testing.T) {
	fixture := newRecoveryFixtureWithRole(t, 0x90, kernel.RoleWorker)
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	ctx := context.Background()
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: 99996, PGID: 99996, Birth: runner.Birth{Seconds: 1700, Microseconds: 3}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustKernelTime(t, 450)); err != nil {
		t.Fatal(err)
	}
	// The run fails with its fully active footprint: finalizing with
	// releasing residue nothing can settle — the exact shape a dead
	// controller leaves behind.
	run := fixture.currentRun(t)
	failure, err := kernel.NewFailureProposal(kernel.FailureProtocol, "settlement test residue")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := fixture.store.FailRun(context.Background(), run.ID, run.Revision, failure, mustKernelTime(t, 500))
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = failed
	reported := kernel.RunID{}
	var reportedErr error
	err = fixture.daemon.validateScheduledCompletion(fixture.changeParent, func(id kernel.RunID, cause error) {
		reported, reportedErr = id, cause
	}, failed)
	if err != nil {
		t.Fatalf("unsettled completion was fatal: %v", err)
	}
	if reported != failed.ID || !errors.Is(reportedErr, kernel.ErrConflict) {
		t.Fatalf("unsettled report = %v, %v", reported, reportedErr)
	}
	after := fixture.currentRun(t)
	if after.Phase != kernel.RunFinalizing {
		t.Fatalf("surfaced run mutated to %v", after.Phase)
	}
}
