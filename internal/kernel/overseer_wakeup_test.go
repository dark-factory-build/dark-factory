package kernel

import (
	"bytes"
	"context"
	"encoding/hex"
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
	if bodies := wakeBodies(t, store, 1000+maxDelay); len(bodies) != 1 || !strings.Contains(bodies[0], "stuck 7\nEscalated: stuck 8") {
		t.Fatalf("starved wake = %q", bodies)
	}
}

// A Change factoryd could not publish has no pull request; its escalation
// wakes the overseer exactly once, and the unpublished Change it names is not
// re-woken while that refusal stands.
func TestOverseerPublishFailureWake(t *testing.T) {
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
	// The intake binding is all the rule reads; its acceptance row is not.
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`PRAGMA foreign_keys = OFF`, nil},
		{`INSERT INTO intake_task_bindings(task_id, acceptance_id) VALUES (?, zeroblob(16))`, []any{finalizing.TaskID.Bytes()}},
		{`PRAGMA foreign_keys = ON`, nil},
		{`INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'reviewer', ?, '', ?, 90)`, []any{finalizing.ProjectID.Bytes(), id,
			`{"id":"` + id + `","state":"publish_failed","handled":true,"request":{"PullNumber":0,"Head":""},"escalation":"factoryd cannot publish change x for task t: the created commit is not verified by GitHub"}`}},
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
		!strings.Contains(tasks[0].Body, "\nEscalated: factoryd cannot publish change x for task t: the created commit is not verified by GitHub") {
		t.Fatalf("wake = %+v, %v", tasks, err)
	}
	if _, err := store.UpdateTask(ctx, tasks[0].ID, tasks[0].Revision, TaskPatch{Cancel: true}, mustTime(t, at+1)); err != nil {
		t.Fatal(err)
	}
	for rewake := range 4 {
		if bodies := wakeBodies(t, store, at+1+int64(rewake+1)*OverseerRewakeAfter.Milliseconds()); len(bodies) != 0 {
			t.Fatalf("re-wake %d for the same refusal = %q", rewake+1, bodies)
		}
	}
}

// A cancelled worker task is informational; a blocked one wakes the overseer
// with one summary line and project counts.
func TestOverseerWakeSummarisesTasks(t *testing.T) {
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

// A succeeded intake task with a diff is factoryd's to publish; any other
// success still gets the overseer's one look.
func TestOverseerWakeSkipsPublishedIntakeSuccess(t *testing.T) {
	for _, intake := range []bool{false, true} {
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
		pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: head, State: "unknown"}}
		if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 84)); err != nil {
			t.Fatal(err)
		}
		if bodies := wakeBodies(t, store, 1_000_000); len(bodies) != map[bool]int{false: 1, true: 0}[intake] {
			t.Fatalf("intake=%v wake = %q", intake, bodies)
		}
	}
}

// An item's re-wakes are counted per item: targeted wakes about other items do
// not use them up, while a bare instruction (the causal record overflowed) or
// a full wake counts for every item.
func TestOverseerRewakesCountPerItem(t *testing.T) {
	for other, due := range map[string]bool{"Factory causal wake: mode=targeted; \nEscalated: stuck 8": true, "Supervise.": false} {
		ctx := context.Background()
		store, worker, overseer := wakeFixture(t)
		head := strings.Repeat("a", 40)
		escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
		at := 1000 + overseerWakeSettle.Milliseconds()
		for index := range 5 {
			var carrier Task
			if index == 0 {
				tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at))
				if err != nil || len(tasks) != 1 || !strings.Contains(tasks[0].Body, "Escalated: stuck 7") {
					t.Fatalf("first wake = %+v, %v", tasks, err)
				}
				carrier = tasks[0]
			} else {
				var err error
				if carrier, err = store.EnqueueTask(ctx, NewTask{ID: taskID(t, uint8(40+index)), IncarnationID: incarnationID(t, uint8(50+index)), ProjectID: overseer.ProjectID, AssignedAgentID: overseer.ID, Title: overseerWakeTitle, Body: other}, mustTime(t, at)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.UpdateTask(ctx, carrier.ID, carrier.Revision, TaskPatch{Cancel: true}, mustTime(t, at+1)); err != nil {
				t.Fatal(err)
			}
			at++
		}
		bodies := wakeBodies(t, store, at+OverseerRewakeAfter.Milliseconds())
		if got := len(bodies) == 1 && strings.Contains(bodies[0], "Escalated: stuck 7"); got != due {
			t.Fatalf("after four %q wakes: wake = %q, want due=%v", other, bodies, due)
		}
	}
}

// A carrier whose run never started was never delivered: its item stays due.
func TestOverseerNeverStartedWakeLeavesItemDue(t *testing.T) {
	for _, detail := range []string{NeverStartedRunDetail, "provider exited"} {
		ctx := context.Background()
		neverStarted, _ := NewFailureProposal(FailureProtocol, NeverStartedRunDetail)
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
		bodies := wakeBodies(t, store, 2+overseerWakeSettle.Milliseconds())
		if due := len(bodies) == 1 && strings.Contains(bodies[0], "Escalated: stuck 7"); due != (detail == NeverStartedRunDetail) {
			t.Fatalf("detail %q: wake = %q", detail, bodies)
		}
	}
}
