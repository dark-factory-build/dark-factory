package kernel

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// wakeFixture is a project with a worker and a standing overseer.
func wakeFixture(t *testing.T) (*Store, Agent, Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := createTestStore(ctx, filepath.Join(t.TempDir(), "kernel.db"), FactoryConfig{DispatchEnabled: true, Capacity: 2}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "wakes", Root: "/wakes"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "worker", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: project.ID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
	if overseer, err = store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	return store, worker, overseer
}

func wakeBodies(t *testing.T, store *Store, at int64) []string {
	t.Helper()
	tasks, err := store.EnqueueOverseerWakeups(context.Background(), mustTime(t, at))
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, task := range tasks {
		bodies = append(bodies, task.Body)
	}
	return bodies
}

// escalate records pull request number at state and head, and an escalated
// review of it at the head escalated.
func escalate(t *testing.T, store *Store, project ProjectID, number, state, head, escalated string, at int64) {
	t.Helper()
	insert := `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', ?, ?, '', ?, ?)`
	if _, err := store.writer.ExecContext(context.Background(), insert, project.Bytes(), "pull_request", number, `{"number":`+number+`,"state":"`+state+`","head":"`+head+`"}`, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(context.Background(), insert, project.Bytes(), "reviewer", "op"+number, `{"request":{"PullNumber":`+number+`,"Head":"`+strings.ToUpper(escalated)+`"},"escalation":"stuck `+number+`"}`, at); err != nil {
		t.Fatal(err)
	}
}

// An escalation wakes the overseer only while its pull request is open at the
// escalated head, and only once settled: its newest item a minute old, or its
// oldest five minutes old.
func TestOverseerEscalationWake(t *testing.T) {
	t.Parallel()
	head, moved := strings.Repeat("a", 40), strings.Repeat("b", 40)
	settle, maxDelay := overseerWakeSettle.Milliseconds(), overseerWakeMaxDelay.Milliseconds()
	for _, test := range []struct {
		name, state, head string
		due               bool
	}{
		{"open same head", "open", head, true},
		{"merged", "merged", head, false},
		{"closed", "closed", head, false},
		{"head moved", "open", moved, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, worker, _ := wakeFixture(t)
			escalate(t, store, worker.ProjectID, "7", test.state, test.head, head, 1000)
			if test.due {
				if bodies := wakeBodies(t, store, 1000+settle-1); len(bodies) != 0 {
					t.Fatalf("unsettled wake = %q", bodies)
				}
			}
			bodies := wakeBodies(t, store, 1000+settle)
			if due := len(bodies) == 1 && strings.Contains(bodies[0], "\nEscalated: stuck 7"); due != test.due {
				t.Fatalf("wake = %q, want due=%v", bodies, test.due)
			}
		})
	}
	store, worker, _ := wakeFixture(t)
	escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
	escalate(t, store, worker.ProjectID, "8", "open", head, head, 1000+maxDelay-1)
	if bodies := wakeBodies(t, store, 1000+maxDelay-1); len(bodies) != 0 {
		t.Fatalf("early wake = %q", bodies)
	}
	if bodies := wakeBodies(t, store, 1000+maxDelay); len(bodies) != 1 || !strings.Contains(bodies[0], "stuck 7 [reviewer:op7]\nEscalated: stuck 8") {
		t.Fatalf("starved wake = %q", bodies)
	}
}

// A Change factoryd could not publish has no pull request; its escalation
// wakes the overseer exactly once, the unpublished Change it names is not
// re-woken while that refusal stands, and it becomes one NEEDS YOU card.
func TestOverseerPublishFailureWake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	succeeded, _ := NewSuccessProposal("done")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, succeeded)
	defer store.Close()
	change, _, err := store.Change(ctx, *finalizing.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	moved, _ := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	settlement, _ := NewRetainedChangeSettlement(change.Revision, &moved)
	if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	if change, _, err = store.Change(ctx, change.ID); err != nil {
		t.Fatal(err)
	}
	id := PublishFailureID(change.ID, change.Revision)
	refusal := "factoryd cannot publish change " + change.ID.String() + " for task " + finalizing.TaskID.String() + ": the created commit is not verified by GitHub"
	// factoryd's record of the refusal; its hourly repeat drops the escalation.
	failed := func(escalation string) string {
		return `{"id":"` + id + `","state":"publish_failed","handled":true,"detail":"the created commit is not verified by GitHub","request":{"PullNumber":0,"Head":""}` + escalation + `}`
	}
	// The intake binding is all the rule reads; its acceptance row is not.
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`PRAGMA foreign_keys = OFF`, nil},
		{`INSERT INTO intake_task_bindings(task_id, acceptance_id) VALUES (?, zeroblob(16))`, []any{finalizing.TaskID.Bytes()}},
		{`PRAGMA foreign_keys = ON`, nil},
		{`INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'reviewer', ?, '', ?, 90)`, []any{finalizing.ProjectID.Bytes(), id, failed(`,"escalation":"` + refusal + `"`)}},
	} {
		if _, err := store.writer.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: finalizing.ProjectID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 82))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
	if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 83)); err != nil {
		t.Fatal(err)
	}
	at := 80 + PublicationAttentionAfter.Milliseconds()
	tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at))
	if err != nil || len(tasks) != 1 || strings.Count(tasks[0].Body, "Escalated: ") != 1 || strings.Contains(tasks[0].Body, "\n- ") ||
		!strings.Contains(tasks[0].Body, "\nEscalated: "+refusal) {
		t.Fatalf("wake = %+v, %v", tasks, err)
	}
	settleCarrier(t, store, at+1, 90, "ran")
	for rewake := range 4 {
		if _, err := store.writer.ExecContext(ctx, `UPDATE production_records SET document = ?, observed_at_ms = ? WHERE identity = ?`, failed(""), at+int64(rewake+1)*OverseerRewakeAfter.Milliseconds(), id); err != nil {
			t.Fatal(err)
		}
		if bodies := wakeBodies(t, store, at+1+int64(rewake+1)*OverseerRewakeAfter.Milliseconds()); len(bodies) != 0 {
			t.Fatalf("re-wake %d for the same refusal = %q", rewake+1, bodies)
		}
	}
	// The refusal is the operator's: one NEEDS YOU card naming it.
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 1 || requests[0].TaskID != tasks[0].ID ||
		!strings.Contains(requests[0].QuestionText, "Escalated: "+refusal+" [reviewer:"+id+"]") {
		t.Fatalf("operator escalation = %+v, %v", requests, err)
	}
	// A later retry publishes it: the card closes by itself.
	head := hex.EncodeToString(moved.Bytes())
	pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: head, State: "unknown"}}
	if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, at+5*OverseerRewakeAfter.Milliseconds())); err != nil {
		t.Fatal(err)
	}
	wakeBodies(t, store, at+5*OverseerRewakeAfter.Milliseconds()+1)
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 0 {
		t.Fatalf("published Change kept its card = %+v, %v", requests, err)
	}
}

// A cancelled worker task is informational; a blocked one wakes the overseer
// with one summary line and project counts.
func TestOverseerWakeSummarisesTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, _ := wakeFixture(t)
	cancelled, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 10), IncarnationID: incarnationID(t, 11), ProjectID: worker.ProjectID, AssignedAgentID: worker.ID, Title: "cancelled"}, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTask(ctx, cancelled.ID, cancelled.Revision, TaskPatch{Cancel: true}, mustTime(t, 100)); err != nil {
		t.Fatal(err)
	}
	if bodies := wakeBodies(t, store, 1_000_000); len(bodies) != 0 {
		t.Fatalf("cancelled task woke the overseer: %q", bodies)
	}

	blocked, _ := NewBlockedProposal("needs a decision")
	store, finalizing := finalizingReleasedRun(t, RoleWorker, blocked)
	defer store.Close()
	change, _, err := store.Change(ctx, *finalizing.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	moved, _ := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
	settlement, _ := NewRetainedChangeSettlement(change.Revision, &moved)
	if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 20), IncarnationID: incarnationID(t, 21), ProjectID: finalizing.ProjectID, AssignedAgentID: finalizing.AgentID, Title: "queued"}, mustTime(t, 81)); err != nil {
		t.Fatal(err)
	}
	overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: finalizing.ProjectID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 82))
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
	if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 83)); err != nil {
		t.Fatal(err)
	}
	head := hex.EncodeToString(moved.Bytes())
	pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: head, State: "unknown"}}
	if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 84)); err != nil {
		t.Fatal(err)
	}
	bodies := wakeBodies(t, store, 80+overseerWakeSettle.Milliseconds())
	want := "Supervise.\n\nFactory causal wake: mode=full; prior_task_id=; worker tasks queued=1 running=0; open PRs=1. mode=full requires fixed-head reconciliation.\n- " +
		finalizing.TaskID.String() + ` "verify" blocked rev=1 change=` + change.ID.String()[:12] + `@d2d2d2d2 PR #7 open/unknown: needs a decision`
	if len(bodies) != 1 || bodies[0] != want {
		t.Fatalf("wake = %q\nwant %q", bodies, want)
	}
}

// Lines that overflow the provider bound are dropped first, then escalations.
func TestOverseerWakeInstructionFallsBackToFull(t *testing.T) {
	t.Parallel()
	line := "- " + strings.Repeat("x", 200)
	lines := make([]string, 50) // > 8 KiB with Codex
	for index := range lines {
		lines[index] = line
	}
	prior := taskID(t, 9)
	body := overseerWakeInstruction(ProviderCodex, "Supervise.", "counts", lines, []string{"Escalated: stuck"}, &prior, false)
	if strings.Contains(body, line) || !strings.Contains(body, "mode=full") || !strings.HasSuffix(body, "\nEscalated: stuck") {
		t.Fatalf("overflow body = %q", body)
	}
	if body := overseerWakeInstruction(ProviderCodex, "Supervise.", "counts", lines[:1], nil, &prior, false); !strings.Contains(body, "mode=targeted; prior_task_id="+prior.String()) || !strings.HasSuffix(body, "\n"+line) {
		t.Fatalf("targeted body = %q", body)
	}
}

// A succeeded intake task with a diff is factoryd's to publish, and stays so
// once its merged Change is reclaimed; any other success still gets the
// overseer's one look.
func TestOverseerWakeSkipsPublishedIntakeSuccess(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"plain", "intake", "reclaimed"} {
		intake := mode != "plain"
		ctx := context.Background()
		succeeded, _ := NewSuccessProposal("done")
		store, finalizing := finalizingReleasedRun(t, RoleWorker, succeeded)
		defer store.Close()
		change, _, err := store.Change(ctx, *finalizing.ChangeID)
		if err != nil {
			t.Fatal(err)
		}
		moved, _ := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
		settlement, _ := NewRetainedChangeSettlement(change.Revision, &moved)
		if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
			t.Fatal(err)
		}
		if intake {
			// The binding is all the rule reads; its acceptance row is not.
			for _, statement := range []string{`PRAGMA foreign_keys = OFF`, `INSERT INTO intake_task_bindings(task_id, acceptance_id) VALUES (?, zeroblob(16))`, `PRAGMA foreign_keys = ON`} {
				args := []any{finalizing.TaskID.Bytes()}[:strings.Count(statement, "?")]
				if _, err := store.writer.ExecContext(ctx, statement, args...); err != nil {
					t.Fatal(err)
				}
			}
		}
		overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: finalizing.ProjectID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 82))
		if err != nil {
			t.Fatal(err)
		}
		policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
		if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 83)); err != nil {
			t.Fatal(err)
		}
		head := hex.EncodeToString(moved.Bytes())
		pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: map[bool]string{false: "open", true: "merged"}[mode == "reclaimed"], Review: ProductionReview{Head: head, State: "unknown"}}
		if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 84)); err != nil {
			t.Fatal(err)
		}
		if mode == "reclaimed" {
			if _, err := store.ReclaimChange(ctx, change.ID, mustRevision(t, change.Revision.Int64()+1), mustTime(t, 85)); err != nil {
				t.Fatal(err)
			}
		}
		if bodies := wakeBodies(t, store, 1_000_000); len(bodies) != map[bool]int{false: 1, true: 0}[intake] {
			t.Fatalf("%s wake = %q", mode, bodies)
		}
	}
}

// An item's re-wakes are counted per item: targeted wakes about other items do
// not use them up, while a bare instruction (the causal record overflowed) or
// a full wake counts for every item. Each wake here ran.
func TestOverseerRewakesCountPerItem(t *testing.T) {
	t.Parallel()
	for other, due := range map[string]bool{"Factory causal wake: mode=targeted; \nEscalated: stuck 8": true, "Supervise.": false} {
		ctx := context.Background()
		store, worker, overseer := wakeFixture(t)
		head := strings.Repeat("a", 40)
		escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
		at := 1000 + overseerWakeSettle.Milliseconds()
		seed := byte(10)
		for index := range 5 {
			if index == 0 {
				tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at))
				if err != nil || len(tasks) != 1 || !strings.Contains(tasks[0].Body, "Escalated: stuck 7") {
					t.Fatalf("first wake = %+v, %v", tasks, err)
				}
			} else {
				if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, uint8(40+index)), IncarnationID: incarnationID(t, uint8(50+index)), ProjectID: overseer.ProjectID, AssignedAgentID: overseer.ID, Title: overseerWakeTitle, Body: other}, mustTime(t, at)); err != nil {
					t.Fatal(err)
				}
			}
			seed += 21
			settleCarrier(t, store, at+1, seed, "ran")
			at += 20
		}
		bodies := wakeBodies(t, store, at+OverseerRewakeAfter.Milliseconds())
		if got := len(bodies) == 1 && strings.Contains(bodies[0], "Escalated: stuck 7"); got != due {
			t.Fatalf("after four %q wakes: wake = %q, want due=%v", other, bodies, due)
		}
	}
}

// A carrier whose run never started holds its item off for
// OverseerRewakeAfter like any wake, but only one that started counts toward
// the item's re-wakes (stalledCard drives that count to the item's card).
func TestOverseerNeverStartedWakeLeavesItemDue(t *testing.T) {
	t.Parallel()
	for _, detail := range []string{NeverStartedRunDetail, "provider exited"} {
		ctx := context.Background()
		neverStarted, _ := NewFailureProposal(FailureTransient, NeverStartedRunDetail)
		store, finalizing := finalizingReleasedRun(t, RoleOrchestrator, neverStarted)
		defer store.Close()
		if _, err := finalizeTestRun(t, store, finalizing, 60); err != nil {
			t.Fatal(err)
		}
		head := strings.Repeat("a", 40)
		escalate(t, store, finalizing.ProjectID, "7", "open", head, head, 2)
		// The carrier failed without starting (or, for contrast,
		// after its run started).
		for statement, args := range map[string][]any{
			`UPDATE tasks SET title = ?, body = 'Escalated: stuck 7', status = 'failed', work_revision = 1, completed_at_ms = updated_at_ms WHERE id = ?`: {overseerWakeTitle, finalizing.TaskID.Bytes()},
			`UPDATE runs SET terminal_detail = ?1, proposal_detail = ?1 WHERE id = ?2`:                                                                    {detail, finalizing.ID.Bytes()},
		} {
			if _, err := store.writer.ExecContext(ctx, statement, args...); err != nil {
				t.Fatal(err)
			}
		}
		overseer, _, err := store.Agent(ctx, finalizing.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
		if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 100)); err != nil {
			t.Fatal(err)
		}
		read, err := store.beginRead(ctx)
		if err != nil {
			t.Fatal(err)
		}
		agent, _, err := agentByID(ctx, read.connection, overseer.ID)
		if err != nil {
			t.Fatal(err)
		}
		var wakes int
		if err := read.connection.QueryRowContext(ctx, overseerItems+`SELECT wakes FROM counted WHERE item_key = '[reviewer:op7]' AND ?6 + ?7 + ?8 >= 0`, overseerItemArgs(agent, 2+overseerWakeSettle.Milliseconds())...).Scan(&wakes); err != nil {
			t.Fatal(err)
		}
		read.Close()
		if want := map[bool]int{true: 0, false: 1}[detail == NeverStartedRunDetail]; wakes != want {
			t.Fatalf("detail %q: counted wakes = %d, want %d", detail, wakes, want)
		}
		if bodies := wakeBodies(t, store, 2+overseerWakeSettle.Milliseconds()); len(bodies) != 0 {
			t.Fatalf("detail %q: woken again within the half hour = %q", detail, bodies)
		}
		if bodies := wakeBodies(t, store, 4+OverseerRewakeAfter.Milliseconds()); len(bodies) != 1 || !strings.Contains(bodies[0], "Escalated: stuck 7") {
			t.Fatalf("detail %q: wake after the half hour = %q", detail, bodies)
		}
	}
}

// settleCarrier admits the overseer's queued carrier and ends its run failed
// with detail, as the daemon fails a run that never started.
func settleCarrier(t *testing.T, store *Store, at int64, seed byte, detail string) {
	t.Helper()
	ctx := context.Background()
	admission, err := store.AdmitNext(ctx, admissionKeys(t, seed, nil), mustTime(t, at))
	if err != nil || !admission.Admitted() {
		t.Fatalf("carrier admission = %+v, %v", admission, err)
	}
	run := activateAllResourcesUnique(t, store, *admission.Run, at+1, int64(seed))
	session := terminalSessionForRunTest(t, store, run.ID)
	if run, err = store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, at+5)); err != nil {
		t.Fatal(err)
	}
	code := FailureProtocol
	if detail == NeverStartedRunDetail {
		code = FailureTransient
	}
	failure, err := NewFailureProposal(code, detail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, at+6)); err != nil {
		t.Fatal(err)
	}
	observeMissingProcessExits(t, store, run.ID, at+7)
	for _, resource := range resourcesForRunTest(t, store, run.ID) {
		if resource.State != ResourceReleased {
			if _, err := store.ReleaseResource(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, at+8)); err != nil {
				t.Fatal(err)
			}
		}
	}
	run = closeTerminalSessionAtCurrent(t, store, run.ID, at+9)
	if _, err := store.FinalizeRun(ctx, run.ID, run.Revision, mustTime(t, at+10)); err != nil {
		t.Fatal(err)
	}
}

// Re-wakes are counted per item, and only for wakes whose run started: wakes
// naming other items, or that never started, leave an item its re-wakes. Any
// wake that named an item, started or not, holds its next one off for
// OverseerRewakeAfter. An item past its re-wakes becomes one NEEDS YOU card,
// raised once, whose reply resumes the last carrier with the question.
func TestOverseerRewakesCountPerItemAndStartedOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, last, card, at, _ := stalledCard(t)
	delivery, _ := HumanRequestDeliveryIDFromBytes(bytes.Repeat([]byte{7}, IDBytes))
	if handled, err := store.ResolveHumanContinuationForOperator(ctx, card.ID, card.Revision, delivery, "close #7", mustTime(t, at+1)); err != nil || !handled {
		t.Fatalf("reply = %v, %v", handled, err)
	}
	if resumed, _, err := store.Task(ctx, last.ID); err != nil || resumed.Status != TaskQueued {
		t.Fatalf("resumed carrier = %+v, %v", resumed, err)
	}
	var resolution string
	if err := store.writer.QueryRowContext(ctx, `SELECT resolution_detail FROM continuations WHERE condition_id = ?`, card.ID.Bytes()).Scan(&resolution); err != nil ||
		!strings.HasPrefix(resolution, "Operator reply to: "+card.QuestionText) || !strings.HasSuffix(resolution, "\nclose #7") {
		t.Fatalf("resumed context = %q, %v", resolution, err)
	}
}

// Only a human answers a stalled-item card: the overseer neither sees it in
// its snapshot nor resolves it.
func TestStalledCardIsTheOperatorsAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, _, card, at, next := stalledCard(t)
	overseer, _, err := store.Agent(ctx, agentID(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 77), IncarnationID: incarnationID(t, 78), ProjectID: worker.ProjectID, AssignedAgentID: overseer.ID, Title: "other"}, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	seed := next()
	keys := admissionKeys(t, seed, nil)
	admission, err := store.AdmitNext(ctx, keys, mustTime(t, at+1))
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	run := activateAllResourcesUnique(t, store, *admission.Run, at+2, int64(seed))
	session := terminalSessionForRunTest(t, store, run.ID)
	if _, err := store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, at+6)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.OverseerSnapshotForAttempt(ctx, keys.AttemptDigest, OverseerSnapshotRequest{})
	if err != nil || len(snapshot.Questions) != 0 {
		t.Fatalf("overseer snapshot questions = %+v, %v", snapshot.Questions, err)
	}
	if _, err := store.ResolveHumanContinuationForAttempt(ctx, keys.AttemptDigest, card.ID, card.Revision, "resolved it myself", mustTime(t, at+7)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("overseer answered its own card: %v", err)
	}
}

// A stalled-item card closes once its items (every escalated PR it names)
// resolve on their own.
func TestStalledCardClosesWhenItsItemResolves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, last, _, at, _ := stalledCard(t)
	if _, err := store.writer.ExecContext(ctx, `UPDATE production_records SET document = json_set(document, '$.state', 'closed') WHERE kind = 'pull_request'`); err != nil {
		t.Fatal(err)
	}
	wakeBodies(t, store, at+1)
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 0 {
		t.Fatalf("resolved item kept its card = %+v, %v", requests, err)
	}
	if carrier, _, err := store.Task(ctx, last.ID); err != nil || carrier.Status == TaskQueued {
		t.Fatalf("closed card resumed its carrier = %+v, %v", carrier, err)
	}
}

// A wake that never started moves when an item may next be woken, not
// whether it is stalled: the item's card stays open rather than closing and
// leaving the item neither woken nor carded.
func TestStalledCardSurvivesANeverStartedWake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, _, card, at, next := stalledCard(t)
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 88), IncarnationID: incarnationID(t, 89), ProjectID: worker.ProjectID, AssignedAgentID: agentID(t, 3), Title: overseerWakeTitle, Body: "Factory causal wake: mode=full"}, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	settleCarrier(t, store, at+1, next(), NeverStartedRunDetail)
	settleCarrier(t, store, at+20, next(), NeverStartedRunDetail)
	wakeBodies(t, store, at+40)
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 1 || requests[0].ID != card.ID {
		t.Fatalf("card after a never-started wake = %+v, %v", requests, err)
	}
}

// A card the operator cancelled is not raised again on the same wake run.
func TestCancelledStalledCardStaysCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, _, card, at, _ := stalledCard(t)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := humanRequestByID(ctx, tx.connection, card.ID)
	if err != nil {
		t.Fatal(tx.Rollback(err))
	}
	run, _, err := runByID(ctx, tx.connection, request.RunID)
	if err != nil {
		t.Fatal(tx.Rollback(err))
	}
	continuation, _, err := humanRequestContinuation(ctx, tx.connection, request, run)
	if err != nil {
		t.Fatal(tx.Rollback(err))
	}
	if err := cancelHumanContinuationOnConnection(ctx, tx, request, continuation, mustTime(t, at+1)); err != nil {
		t.Fatal(tx.Rollback(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx.Close()
	wakeBodies(t, store, at+2)
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 0 {
		t.Fatalf("cancelled card raised again = %+v, %v", requests, err)
	}
}

// stalledCard drives escalation 7 through one never-started wake, wakes about
// other items, and its four counted wakes to its NEEDS YOU card, raised once.
func stalledCard(t *testing.T) (*Store, Agent, Task, OperatorHumanRequest, int64, func() byte) {
	t.Helper()
	head := strings.Repeat("a", 40)
	rewake := OverseerRewakeAfter.Milliseconds()
	store, worker, _ := wakeFixture(t)
	escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
	at := 1000 + overseerWakeMaxDelay.Milliseconds()
	seed := byte(10)
	next := func() byte { seed += 21; return seed } // distinct runtime roots and resource ids
	wake := func(want string) {
		t.Helper()
		bodies := wakeBodies(t, store, at)
		if len(bodies) != 1 || !strings.Contains(bodies[0], want) {
			t.Fatalf("wake at %d = %q, want %q", at, bodies, want)
		}
	}
	// A wake that never started (twice: the first is requeued) does not count,
	// but no second carrier follows it within the half hour.
	wake("Escalated: stuck 7 [reviewer:op7]")
	settleCarrier(t, store, at+1, next(), NeverStartedRunDetail)
	settleCarrier(t, store, at+20, next(), NeverStartedRunDetail)
	at += 40
	if bodies := wakeBodies(t, store, at); len(bodies) != 0 {
		t.Fatalf("never-started wake re-woke at once = %q", bodies)
	}
	at += rewake
	wake("Escalated: stuck 7")
	settleCarrier(t, store, at+1, next(), "ran")
	// Wakes about another item do not use up item 7's re-wakes.
	for round := range 4 {
		escalate(t, store, worker.ProjectID, fmt.Sprint(20+round), "open", head, head, at+10)
		at += 10 + overseerWakeMaxDelay.Milliseconds()
		bodies := wakeBodies(t, store, at)
		if len(bodies) != 1 || strings.Contains(bodies[0], "stuck 7") {
			t.Fatalf("round %d wake = %q", round, bodies)
		}
		settleCarrier(t, store, at+1, next(), "ran")
	}
	var last Task
	for rewakes := 1; rewakes <= 3; rewakes++ {
		at += rewake
		tasks, err := store.EnqueueOverseerWakeups(context.Background(), mustTime(t, at))
		if err != nil || len(tasks) != 1 || !strings.Contains(tasks[0].Body, "Escalated: stuck 7") {
			t.Fatalf("re-wake %d = %+v, %v", rewakes, tasks, err)
		}
		last = tasks[0]
		settleCarrier(t, store, at+1, next(), "ran")
	}
	// Stalled: no fifth wake, and one card however often the poll runs.
	at += 10 * rewake
	for range 2 {
		if bodies := wakeBodies(t, store, at); len(bodies) != 0 {
			t.Fatalf("exhausted item woke again = %q", bodies)
		}
	}
	ctx := context.Background()
	requests, err := store.OperatorHumanRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].TaskID != last.ID || !strings.Contains(requests[0].QuestionText, "Escalated: stuck 7 [reviewer:op7]") {
		t.Fatalf("stalled card = %+v, %v", requests, err)
	}
	if projection, found, err := store.HumanRequest(ctx, requests[0].ID); err != nil || !found || !projection.CanReply {
		t.Fatalf("stalled card projection = %+v, %v, %v", projection, found, err)
	}
	return store, worker, last, requests[0], at, next
}
