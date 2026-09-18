package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOverseerSnapshotIsProjectScopedAndTaskSelected(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	other, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 241), Name: "other", Root: "/other", VerificationPolicy: VerificationNone}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 242), ProjectID: other.ID, Name: "other", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 1}, mustTime(t, 41))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 243), ProjectID: other.ID, AssignedAgentID: otherAgent.ID, IncarnationID: incarnationID(t, 244), Title: "foreign", Body: "must not appear"}, mustTime(t, 42)); err != nil {
		t.Fatal(err)
	}
	secondAgent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 245), ProjectID: run.ProjectID, Name: "second", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 1}, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	secondTask, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 246), ProjectID: run.ProjectID, AssignedAgentID: secondAgent.ID, IncarnationID: incarnationID(t, 247), Title: "second"}, mustTime(t, 44))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{})
	if err != nil || snapshot.ProjectID != run.ProjectID || len(snapshot.Tasks) < 1 {
		t.Fatalf("scoped snapshot = %+v, %v", snapshot, err)
	}
	var selectedOverview OverseerTask
	for _, task := range snapshot.Tasks {
		if task.ID == run.TaskID {
			selectedOverview = task
			break
		}
	}
	if selectedOverview.ID != run.TaskID {
		t.Fatalf("scoped snapshot omitted running task = %+v", snapshot.Tasks)
	}
	selected := run.TaskID
	detail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &selected})
	if err != nil || len(detail.Tasks) != 1 || detail.Tasks[0].ID != selected || detail.Tasks[0].ObjectiveTruncated || detail.Tasks[0].ResultTruncated || len(detail.Agents) != 1 || detail.Agents[0].ID != run.AgentID {
		t.Fatalf("selected snapshot = %+v, %v", detail, err)
	}
	fullBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	detailBytes, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailBytes) >= len(fullBytes) || detail.Tasks[0].Objective != selectedOverview.Objective {
		t.Fatalf("real Store snapshot bytes full=%d targeted=%d", len(fullBytes), len(detailBytes))
	}
	secondDetail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &secondTask.ID})
	if err != nil || len(secondDetail.Agents) != 1 || secondDetail.Agents[0].ID != secondAgent.ID {
		t.Fatalf("second selected snapshot = %+v, %v", secondDetail, err)
	}
	corruptSQL(t, store, `UPDATE tasks SET assigned_agent_id = NULL WHERE id = ?`, secondTask.ID.Bytes())
	pooledDetail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &secondTask.ID})
	if err != nil || len(pooledDetail.Agents) == 0 || pooledDetail.Agents[0].ID != run.AgentID {
		t.Fatalf("pooled selected snapshot = %+v, %v", pooledDetail, err)
	}
	foreign := taskID(t, 243)
	if _, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &foreign}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign selected task = %v", err)
	}
}

func TestOverseerSnapshotPagesWithHeadFenceAndTaskTextChunks(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	for index := 0; index < OverseerSnapshotPageSize; index++ {
		if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, byte(250+index)), ProjectID: run.ProjectID, AssignedAgentID: run.AgentID, IncarnationID: incarnationID(t, byte(250+index)), Title: "queued"}, mustTime(t, int64(40+index))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{})
	if err != nil || len(first.Tasks) != OverseerSnapshotPageSize || first.NextOffset == nil || *first.NextOffset != OverseerSnapshotPageSize {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if _, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{ExpectedHead: first.Head, TextOffset: 1}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unselected text page = %v", err)
	}
	second, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{Offset: *first.NextOffset, ExpectedHead: first.Head})
	if err != nil || len(second.Tasks) != 1 || second.NextOffset != nil {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	priority := int64(1)
	if _, err := store.UpdateTask(ctx, second.Tasks[0].ID, second.Tasks[0].Revision, TaskPatch{Priority: &priority}, mustTime(t, 50)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{Offset: *first.NextOffset, ExpectedHead: first.Head}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale page = %v", err)
	}
	chunked, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 240), ProjectID: run.ProjectID, AssignedAgentID: run.AgentID, IncarnationID: incarnationID(t, 241), Title: "chunked", Body: strings.Repeat("界", 4097)}, mustTime(t, 51))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &chunked.ID})
	if err != nil || len(detail.Tasks) != 1 || len([]rune(detail.Tasks[0].Objective)) != 4096 || !detail.Tasks[0].ObjectiveTruncated || detail.NextTextOffset == nil || *detail.NextTextOffset != 4096 {
		t.Fatalf("first text chunk = %+v, %v", detail, err)
	}
	tail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &chunked.ID, ExpectedHead: detail.Head, TextOffset: *detail.NextTextOffset})
	if err != nil || tail.Tasks[0].Objective != "界" || !tail.Tasks[0].ObjectiveTruncated || tail.NextTextOffset != nil {
		t.Fatalf("text tail = %+v, %v", tail, err)
	}
}

func TestOverseerSnapshotIncludesAllActionableTasksBeyondHistoryWindow(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	insertTerminal := func(id TaskID, incarnation IncarnationID, status string, blockedReason any, completedAt any, at int64) {
		_, err := store.writer.ExecContext(ctx, `INSERT INTO tasks(
			id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body,
			sent_back_instruction_bytes, status, priority, blocked_reason, result,
			completed_at_ms, revision, created_at_ms, updated_at_ms
		) VALUES(?, ?, ?, ?, 1, 'historical', '', NULL, ?, 0, ?, NULL, ?, 1, ?, ?)`,
			id.Bytes(), run.ProjectID.Bytes(), run.AgentID.Bytes(), incarnation.Bytes(), status, blockedReason, completedAt, at, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for index := byte(0); index < 32; index++ {
		at := int64(100 + index)
		insertTerminal(taskID(t, 20+index), incarnationID(t, 60+index), "succeeded", nil, at, at)
	}
	blockedID := taskID(t, 200)
	failedID := taskID(t, 201)
	insertTerminal(blockedID, incarnationID(t, 202), "blocked", "needs operator", nil, 1)
	insertTerminal(failedID, incarnationID(t, 203), "failed", nil, int64(2), 2)

	seen := map[TaskID]bool{}
	var head EventSequence
	var offset uint64
	for {
		page, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{Offset: offset, ExpectedHead: head})
		if err != nil {
			t.Fatal(err)
		}
		if head.Int64() == 0 {
			head = page.Head
		}
		if len(page.Tasks) > OverseerSnapshotPageSize {
			t.Fatalf("page exceeded bound: %d", len(page.Tasks))
		}
		for _, task := range page.Tasks {
			seen[task.ID] = true
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if !seen[blockedID] || !seen[failedID] {
		t.Fatalf("actionable history omitted: blocked=%v failed=%v", seen[blockedID], seen[failedID])
	}
	detail, err := store.OverseerSnapshotForAttempt(ctx, run.CredentialDigest, OverseerSnapshotRequest{TaskID: &blockedID})
	if err != nil || len(detail.Tasks) != 1 || detail.Tasks[0].Status != TaskBlocked {
		t.Fatalf("selected blocked task = %+v, %v", detail.Tasks, err)
	}
}

func TestOverseerCannotAnswerItsOwnHumanRequest(t *testing.T) {
	ctx := context.Background()
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	request, err := store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(245), QuestionText: "question"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginHumanReplyForAttempt(ctx, run.CredentialDigest, request.ID, request.Revision, humanDeliveryID(t, 246), "answer", mustTime(t, 41)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("self answer = %v", err)
	}
}

func TestOverseerHumanReplyTargetsOnlyWorkers(t *testing.T) {
	ctx := context.Background()
	store, worker, overseer, _ := runningWorkerAndOverseer(t)
	defer store.Close()
	request, err := store.CreateHumanQuestionForAttempt(ctx, worker.CredentialDigest, NewHumanQuestion{IdempotencyKey: humanKey(247), QuestionText: "worker question"}, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	withLegacyOrchestratorTarget(t, store, worker.ID, func(tx *writeTx) {
		_, err := store.beginHumanReplyTx(ctx, tx, request.ID, request.Revision, humanDeliveryID(t, 248), "answer", mustTime(t, 41), overseer.ProjectID, overseer.ID)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("legacy orchestrator target reply = %v", err)
		}
	})
	delivery, err := store.BeginHumanReplyForAttempt(ctx, overseer.CredentialDigest, request.ID, request.Revision, humanDeliveryID(t, 249), "answer", mustTime(t, 42))
	if err != nil || delivery.RunID != worker.ID {
		t.Fatalf("worker reply = %+v, %v", delivery, err)
	}
}

func TestRetainedChangeHandoffsInspectCurrentSettledOutcomes(t *testing.T) {
	succeeded, err := NewSuccessProposal("finished")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := NewBlockedProposal("needs input")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := NewFailureProposal(FailureInternal, "worker failure")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := NewCancelledProposal("operator stopped")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		proposal Proposal
		sendBack bool
		want     bool
	}{
		{name: "current succeeded", proposal: succeeded, want: true},
		{name: "blocked", proposal: blocked, want: true},
		{name: "failed", proposal: failed, want: true},
		{name: "cancelled", proposal: cancelled, want: true},
		{name: "blocked sent back", proposal: blocked, sendBack: true},
		{name: "failed sent back", proposal: failed, sendBack: true},
		{name: "sent back", proposal: succeeded, sendBack: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, test.proposal)
			defer store.Close()
			if _, found, err := store.RetainedChangeHandoffForTask(context.Background(), finalizing.ProjectID, finalizing.TaskID); err != nil || found {
				t.Fatalf("unsettled source handoff: found=%v err=%v", found, err)
			}
			terminal, err := finalizeTestRun(t, store, finalizing, 70)
			if err != nil {
				t.Fatal(err)
			}
			if test.sendBack {
				task, found, err := store.Task(context.Background(), terminal.TaskID)
				if err != nil || !found {
					t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
				}
				if _, err := store.SendBackTask(context.Background(), task.ID, task.Revision, "repair this", mustTime(t, 80)); err != nil {
					t.Fatal(err)
				}
			}
			direct, directFound, err := store.RetainedChangeHandoffForTask(context.Background(), terminal.ProjectID, terminal.TaskID)
			if err != nil {
				t.Fatalf("direct handoff = %v", err)
			}
			if !test.want {
				if directFound {
					t.Fatalf("ineligible handoff = %+v", direct)
				}
				return
			}
			change, found, err := store.Change(context.Background(), *terminal.ChangeID)
			if err != nil || !found || !directFound || direct.ChangeID != change.ID || direct.TaskID != terminal.TaskID || direct.TaskWorkRevision != terminal.AdmittedTaskWorkRevision || direct.ChangeRevision != change.Revision {
				t.Fatalf("direct current handoff = %+v, found=%v, err=%v", direct, directFound, err)
			}
		})
	}
}
