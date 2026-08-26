package kernel

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRecoverableRunExactLookupMatchesPluralEntry(t *testing.T) {
	success, _ := NewSuccessProposal("recoverable")
	store, run := finalizingReleasedRun(t, RoleWorker, VerificationNone, success)
	defer store.Close()

	all, err := store.RecoverableRuns(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("RecoverableRuns = %+v, %v", all, err)
	}
	exact, found, err := store.RecoverableRun(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("RecoverableRun = %+v, found=%v, err=%v", exact, found, err)
	}
	if !reflect.DeepEqual(exact, all[0]) {
		t.Fatalf("exact lookup differs from plural entry: exact=%+v plural=%+v", exact, all[0])
	}
	if exact.Change == nil || len(exact.Resources) != 4 {
		t.Fatalf("exact lookup omitted durable relationships: %+v", exact)
	}
}

func TestRecoverableRunExactLookupReturnsNotFoundForTerminalAndUnknownRuns(t *testing.T) {
	failure, _ := NewFailureProposal(FailureInternal, "terminal")
	store, finalizing := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
	defer store.Close()
	if _, err := store.FinalizeRun(context.Background(), finalizing.ID, finalizing.Revision, mustTime(t, 70)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []RunID{finalizing.ID, runID(t, 99)} {
		if recovered, found, err := store.RecoverableRun(context.Background(), id); err != nil || found || !reflect.DeepEqual(recovered, RecoverableRun{}) {
			t.Fatalf("RecoverableRun(%s) = %+v, found=%v, err=%v", id, recovered, found, err)
		}
	}
	before := captureWriteFootprint(t, store)
	if _, found, err := store.RecoverableRun(context.Background(), RunID{}); !errors.Is(err, ErrInvalidValue) || found {
		t.Fatalf("zero RecoverableRun = found=%v, err=%v", found, err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("zero RecoverableRun changed footprint: before=%+v after=%+v", before, after)
	}
}

func TestRecoverableRunExactLookupValidatesExactRelationships(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	corruptSQL(t, store, `UPDATE resources SET state = 'released', released_at_ms = updated_at_ms WHERE run_id = ? AND kind = 'runtime_root'`, run.ID.Bytes())
	if _, found, err := store.RecoverableRun(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("corrupt exact relationship = found=%v, err=%v", found, err)
	}
}

func TestRecoverableRunExactLookupRejectsUnrelatedOwnershipCorruption(t *testing.T) {
	store, _, project, firstAgent := newAdmissionStore(t, RoleOrchestrator, 4)
	defer store.Close()
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 180), ProjectID: project.ID, AssignedAgentID: firstAgent.ID, IncarnationID: incarnationID(t, 181), Title: "first"}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	first, err := store.AdmitNext(context.Background(), firstAgent.ID, admissionKeys(t, 182, nil), mustTime(t, 10))
	if err != nil || !first.Admitted() {
		t.Fatalf("first admission = %+v, %v", first, err)
	}
	secondAgent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 183), ProjectID: project.ID, Name: "second", Role: RoleOrchestrator, Provider: ProviderCodex, ExecutionMode: ExecutionWorkspaceWrite, ToolBudgetLimit: 5}, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(context.Background(), NewTask{ID: taskID(t, 184), ProjectID: project.ID, AssignedAgentID: secondAgent.ID, IncarnationID: incarnationID(t, 185), Title: "second"}, mustTime(t, 12)); err != nil {
		t.Fatal(err)
	}
	second, err := store.AdmitNext(context.Background(), secondAgent.ID, admissionKeys(t, 186, nil), mustTime(t, 13))
	if err != nil || !second.Admitted() {
		t.Fatalf("second admission = %+v, %v", second, err)
	}
	firstRuntime := resourceOfKind(t, resourcesForRunTest(t, store, first.Run.ID), ResourceRuntimeRoot)
	corruptSQL(t, store, `UPDATE resources SET path = ? WHERE run_id = ? AND kind = 'runtime_root'`, firstRuntime.Path, second.Run.ID.Bytes())
	if _, found, err := store.RecoverableRun(context.Background(), first.Run.ID); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("unrelated ownership corruption = found=%v, err=%v", found, err)
	}
}

func TestRecoverableRunExactLookupReloadsDurableState(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	first, found, err := store.RecoverableRun(context.Background(), run.ID)
	if err != nil || !found || first.Run.Revision != run.Revision || first.Run.Phase != RunRunning {
		t.Fatalf("initial exact lookup = %+v, found=%v, err=%v", first, found, err)
	}
	proposal, _ := NewSuccessProposal("fresh")
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	second, found, err := store.RecoverableRun(context.Background(), run.ID)
	if err != nil || !found || second.Run.Revision != finalizing.Revision || second.Run.Phase != RunFinalizing || second.Run.Proposal == nil || !second.Run.Proposal.equal(proposal) {
		t.Fatalf("reloaded exact lookup = %+v, found=%v, err=%v", second, found, err)
	}
}
