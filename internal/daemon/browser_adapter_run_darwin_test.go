package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestTaskDetailTextChunkPagesOutcomeWithTheExistingCursor(t *testing.T) {
	outcome := strings.Repeat("x", 2049)
	first, more := taskDetailTextChunk(outcome, 0)
	second, final := taskDetailTextChunk(outcome, 2048)
	if !more || final || first+second != outcome {
		t.Fatalf("outcome page = %q/%q, more=%v/%v", first, second, more, final)
	}
}

func completeAdapterRun(t *testing.T, store *kernel.Store, run kernel.Run, resultText string) kernel.Run {
	t.Helper()
	proposal, err := kernel.NewSuccessProposal(resultText)
	if err != nil {
		t.Fatal(err)
	}
	return completeAdapterRunWithProposal(t, store, run, proposal)
}

func completeAdapterRunWithProposal(t *testing.T, store *kernel.Store, run kernel.Run, proposal kernel.Proposal) kernel.Run {
	t.Helper()
	ctx := context.Background()
	at := max(int64(400), run.UpdatedAt.Int64()+1)
	current, err := store.ProposeAttemptOutcome(ctx, run.CredentialDigest, proposal, adapterTime(t, at+0))
	if err != nil {
		t.Fatal(err)
	}
	resources, err := store.Resources(ctx, run.ID)
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
	attemptResult, err := kernel.NewInnerConvergedAttemptResult(run.ID, run.CredentialDigest, run.ResultProofDigest(), runtime.Identity, provider.Identity, providerExit)
	if err != nil {
		t.Fatal(err)
	}
	current, err = store.ConsumeAttemptResult(ctx, attemptResult, current.Revision, adapterTime(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	resources, err = store.Resources(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		switch resource.Kind {
		case kernel.ResourceRuntimeRoot:
			runtime = resource
		case kernel.ResourceRunnerProcess:
			runner = resource
		}
	}
	runnerExit, err := kernel.NewProcessExitCode(1, 0, adapterTime(t, at+2))
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = store.RecordLiveRunnerExitAndRelease(ctx, run.ID, runner.ID, current.Revision, runner.Revision, runner.Identity, runnerExit, adapterTime(t, at+3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, adapterTime(t, at+4)); err != nil {
		t.Fatal(err)
	}
	session, found, err := store.TerminalSessionForRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	current, _, err = store.CloseTerminalAfterRunner(ctx, attemptResult, current.Revision, session.Revision, adapterTime(t, at+5))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.FinalizeRun(ctx, run.ID, current.Revision, adapterTime(t, at+6))
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

// Activity is invalidated by event head; the control revision remains usable
// across admission and settlement, while each snapshot has the current count.
func TestBrowserActivityRefreshDoesNotConsumeFactoryControlRevision(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	connection := fixture.pair(t)
	before, _ := adapterSnapshot(t, fixture, connection, "before")
	run := adapterRunningRun(t, fixture.store, 170)
	active, _ := adapterSnapshot(t, fixture, connection, "active")
	if active.Factory.Revision != before.Factory.Revision || active.Factory.ActiveRuns != 1 || active.Head <= before.Head {
		t.Fatalf("admission snapshot: before=%+v active=%+v", before, active)
	}
	completeAdapterRun(t, fixture.store, run, "finished")
	watch, err := testEncodeStateWatch("activity-watch", browserprotocol.StateWatch{AfterHead: active.Head})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, watch)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeStateChanged {
		t.Fatalf("activity invalidation = %+v", frame)
	}
	settled, _ := adapterSnapshot(t, fixture, connection, "settled")
	if settled.Factory.Revision != before.Factory.Revision || settled.Factory.ActiveRuns != 0 || settled.Head <= active.Head {
		t.Fatalf("settlement snapshot: active=%+v settled=%+v", active, settled)
	}
	revision, _ := kernel.NewRevision(int64(before.Factory.Revision))
	if _, err := fixture.store.SetDispatch(context.Background(), revision, false, adapterTime(t, 500)); err != nil {
		t.Fatalf("activity invalidated control authority: %v", err)
	}
}
