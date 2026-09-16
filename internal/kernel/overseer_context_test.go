package kernel

import (
	"context"
	"testing"
)

func TestOverseerPriorContextRemainsReadableAfterUnproductivePass(t *testing.T) {
	for _, outcome := range []string{"blocked", "failed", "cancelled", "sent-back", "empty-success"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			store, first, firstKeys := runningOrchestratorRun(t)
			defer store.Close()
			finish := func(run Run, keys AdmissionKeys, proposal Proposal, at int64) {
				t.Helper()
				if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, at)); err != nil {
					t.Fatal(err)
				}
				observeMissingProcessExits(t, store, run.ID, at+1)
				releaseAllRunResources(t, store, run.ID, at+5)
				closed := closeTerminalSessionAtCurrent(t, store, run.ID, at+10)
				if _, err := store.FinalizeRun(ctx, closed.ID, closed.Revision, mustTime(t, at+11)); err != nil {
					t.Fatal(err)
				}
			}
			start := func(id byte, at int64) (Run, AdmissionKeys) {
				t.Helper()
				_, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, id), ProjectID: first.ProjectID, AssignedAgentID: first.AgentID, IncarnationID: incarnationID(t, id+1), Title: "next supervision", Priority: 10}, mustTime(t, at))
				if err != nil {
					t.Fatal(err)
				}
				keys := admissionKeys(t, id+2, nil)
				keys.RuntimeRoot = "/runtime/" + keys.RunID.String()
				admitted, err := store.AdmitNext(ctx, keys, mustTime(t, at+1))
				if err != nil || admitted.Run == nil {
					t.Fatalf("admit: %+v %v", admitted, err)
				}
				active := activateAllResourcesUnique(t, store, *admitted.Run, at+2, int64(id))
				session := terminalSessionForRunTest(t, store, active.ID)
				active, err = store.ActivateRun(ctx, active.ID, session.ID, active.Revision, session.Revision, mustTime(t, at+15))
				if err != nil {
					t.Fatal(err)
				}
				return active, keys
			}
			const decision = "Review PR42 at its recorded head; next action is observe the merge operation."
			success, _ := NewSuccessProposal(decision)
			finish(first, firstKeys, success, 100)
			second, secondKeys := start(180, 200)
			var proposal Proposal
			switch outcome {
			case "failed":
				proposal, _ = NewFailureProposal(FailureAttempt, "provider failed")
			case "cancelled":
				proposal, _ = NewCancelledProposal("operator cancelled")
			case "blocked":
				proposal, _ = NewBlockedProposal("review rejected; inspect the recorded head")
			case "sent-back":
				proposal, _ = NewSuccessProposal("later result cleared by send-back")
			case "empty-success":
				proposal, _ = NewSuccessProposal("")
			}
			finish(second, secondKeys, proposal, 300)
			if outcome == "sent-back" {
				task, found, err := store.Task(ctx, second.TaskID)
				if err != nil || !found {
					t.Fatalf("task: %v", err)
				}
				if _, err = store.SendBackTask(ctx, task.ID, task.Revision, "correction", mustTime(t, 320)); err != nil {
					t.Fatal(err)
				}
			}
			_, readerKeys := start(200, 400)
			read, err := store.beginRead(ctx)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := latestOverseerTask(ctx, read.connection, first.AgentID)
			read.Close()
			wantPrior := first.TaskID
			if outcome == "blocked" {
				wantPrior = second.TaskID
			}
			if err != nil || prior == nil || *prior != wantPrior {
				t.Fatalf("readable prior: %v %v, want %v", prior, err, wantPrior)
			}
			snapshot, err := store.OverseerSnapshotForAttempt(ctx, readerKeys.AttemptDigest, OverseerSnapshotRequest{TaskID: prior})
			wantResult := decision
			if outcome == "blocked" {
				if err != nil || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].BlockedReason != "review rejected; inspect the recorded head" {
					t.Fatalf("blocked context inaccessible through supported read: %+v %v", snapshot, err)
				}
				return
			}
			if err != nil || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Result != wantResult {
				t.Fatalf("context inaccessible through supported read: %+v %v", snapshot, err)
			}
		})
	}
}
