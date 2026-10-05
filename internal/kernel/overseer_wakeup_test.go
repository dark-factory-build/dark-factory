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
	store, finalizing := finalizingReleasedRun(t, RoleWorker, VerificationNone, blocked)
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
