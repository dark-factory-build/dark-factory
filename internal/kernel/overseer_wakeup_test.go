package kernel

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// wakeFixture is a project with a worker and a standing overseer.
func wakeFixture(t testing.TB) (*Store, Agent, Agent) {
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

func wakeBodies(t testing.TB, store *Store, at int64) []string {
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
func escalate(t testing.TB, store *Store, project ProjectID, number, state, head, escalated string, at int64) {
	t.Helper()
	insert := `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', ?, ?, '', ?, ?)`
	if _, err := store.writer.ExecContext(context.Background(), insert, project.Bytes(), "pull_request", number, `{"number":`+number+`,"state":"`+state+`","head":"`+head+`"}`, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(context.Background(), insert, project.Bytes(), "reviewer", "op"+number, `{"request":{"PullNumber":`+number+`,"Head":"`+strings.ToUpper(escalated)+`"},"escalation":"stuck `+number+`"}`, at); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkOverseerItemsLiveShape keeps the combined wake/stalled pass
// measurable against the retained-store scale called out in #1881.
func BenchmarkOverseerItemsLiveShape(b *testing.B) {
	store, _, overseer := wakeFixture(b)
	head := strings.Repeat("a", 40)
	for i := 0; i < 1000; i++ {
		escalate(b, store, overseer.ProjectID, fmt.Sprintf("%d", i+1), "open", head, head, 1000+int64(i))
	}
	args := overseerItemArgs(overseer, 120_000, nil)
	connection, err := store.readerConnection(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = connection.Close() })
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := connection.QueryContext(context.Background(), overseerPassItems, args...)
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			b.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProductionEscalationReachesStalledCard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, overseer := wakeFixture(t)
	head := strings.Repeat("a", 40)
	if err := store.RecordProductionObservation(ctx, overseer.ProjectID, ProductionObservation{
		Repository: "example/factory", ObservedAt: 1000,
		PullRequests: []ProductionPullRequest{{Number: 7, Title: "Ship", Head: head, State: "open", Review: ProductionReview{Head: head, State: "unknown"}}},
	}, mustTime(t, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordReviewOperation(ctx, overseer.ProjectID, "example/factory", "api-op7", map[string]any{
		"request": map[string]any{"PullNumber": 7, "Head": strings.ToUpper(head)}, "escalation": "stuck 7",
	}, mustTime(t, 1000)); err != nil {
		t.Fatal(err)
	}
	at := int64(1000) + overseerWakeMaxDelay.Milliseconds()
	bodies := wakeBodies(t, store, at)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "[reviewer:api-op7]") {
		t.Fatalf("API escalation wake = %q", bodies)
	}
	settleCarrier(t, store, at+1, 31, NeverStartedRunDetail)
	settleCarrier(t, store, at+20, 52, NeverStartedRunDetail)
	at += 40 + OverseerRewakeAfter.Milliseconds()
	tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at))
	if err != nil || len(tasks) != 1 {
		t.Fatalf("API escalation re-wake = %+v, %v", tasks, err)
	}
	settleCarrier(t, store, at+1, 73, "ran")
	wakeBodies(t, store, at+10+OverseerRewakeAfter.Milliseconds())
	requests, err := store.OperatorHumanRequests(ctx)
	if err != nil || len(requests) != 1 || !strings.Contains(requests[0].QuestionText, "[reviewer:api-op7]") {
		t.Fatalf("API escalation stalled card = %+v, %v", requests, err)
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
	if bodies := wakeBodies(t, store, 1000+maxDelay); len(bodies) != 1 || !strings.Contains(bodies[0], "stuck 7 [reviewer:op7]\n  why: pull: example/factory#7 open") || !strings.Contains(bodies[0], "waits on: operator: factoryd escalated it to the overseer\nEscalated: stuck 8") {
		t.Fatalf("starved wake = %q", bodies)
	}
}

// #1698: a pull request waiting on its code owner wakes the overseer once,
// never re-wakes, and becomes one NEEDS YOU card naming it.
func TestOverseerOwnerApprovalEscalationIsOneCard(t *testing.T) {
	t.Parallel()
	store, worker, _ := wakeFixture(t)
	head := strings.Repeat("a", 40)
	escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
	if _, err := store.writer.ExecContext(context.Background(), `UPDATE production_records SET document = json_set(document, '$.owner_approval', json('true')) WHERE kind = 'reviewer'`); err != nil {
		t.Fatal(err)
	}
	at := 1000 + overseerWakeSettle.Milliseconds()
	if bodies := wakeBodies(t, store, at); len(bodies) != 1 || !strings.Contains(bodies[0], "Escalated: stuck 7") {
		t.Fatalf("wake = %q", bodies)
	}
	settleCarrier(t, store, at+1, 31, "ran")
	for rewake := range int64(3) {
		if bodies := wakeBodies(t, store, at+(rewake+1)*OverseerRewakeAfter.Milliseconds()+10); len(bodies) != 0 {
			t.Fatalf("re-wake %d = %q", rewake+1, bodies)
		}
	}
	if requests, err := store.OperatorHumanRequests(context.Background()); err != nil || len(requests) != 1 || !strings.Contains(requests[0].QuestionText, "Escalated: stuck 7 [reviewer:op7]") {
		t.Fatalf("owner card = %+v, %v", requests, err)
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

// #1949: a no-intake Change that settles after its task's pull request merged
// (its run admitted before the merge) or closed is one PublishableChanges
// skips without a refusal: the overseer's wake names it exactly once, and it
// becomes one NEEDS YOU card. A correction it does publish is no item
// (TestMergedIntakeSentBackPublishesItsCorrectionAnew).
func TestOverseerUnpublishableSettledChangeWake(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"merged", "closed"} {
		ctx := context.Background()
		succeeded, _ := NewSuccessProposal("done")
		store, finalizing := finalizingReleasedRun(t, RoleWorker, succeeded)
		defer store.Close()
		change, _, err := store.Change(ctx, *finalizing.ChangeID)
		if err != nil {
			t.Fatal(err)
		}
		moved, _ := NewCommitID(change.Selection.format, bytes.Repeat([]byte{0xd2}, change.Selection.format.oidLength()))
		head := hex.EncodeToString(moved.Bytes())
		pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Review: ProductionReview{Head: head, State: "unknown"}}
		if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 70)); err != nil {
			t.Fatal(err)
		}
		pr.State = mode
		if err := store.RecordProductionObservation(ctx, finalizing.ProjectID, ProductionObservation{Repository: "example/factory", ObservedAt: 75, PullRequests: []ProductionPullRequest{pr}, Checks: []ProductionCheck{}}, mustTime(t, 75)); err != nil {
			t.Fatal(err)
		}
		settlement, _ := NewRetainedChangeSettlement(change.Revision, &moved)
		if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
			t.Fatal(err)
		}
		if found, err := store.PublishableChanges(ctx, mustTime(t, 1<<40)); err != nil || len(found) != 0 {
			t.Fatalf("%s publishable = %+v, %v", mode, found, err)
		}
		overseer, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 3), ProjectID: finalizing.ProjectID, Name: "overseer", Role: RoleOrchestrator, Provider: ProviderCodex, ToolBudgetLimit: 2}, mustTime(t, 82))
		if err != nil {
			t.Fatal(err)
		}
		policy, after, instruction := IdleStandingInstruction, uint32(1), "Supervise."
		if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 83)); err != nil {
			t.Fatal(err)
		}
		item := "Settled, unpublishable: change " + change.ID.String() + " for task " + finalizing.TaskID.String() + ": branch already has PR #7"
		at := 80 + PublicationAttentionAfter.Milliseconds()
		if bodies := wakeBodies(t, store, at); len(bodies) != 1 || strings.Count(bodies[0], "Settled, unpublishable: ") != 1 || !strings.Contains(bodies[0], "\n"+item) {
			t.Fatalf("%s wake = %q", mode, bodies)
		}
		settleCarrier(t, store, at+1, 90, "ran")
		if bodies := wakeBodies(t, store, at+1+OverseerRewakeAfter.Milliseconds()); len(bodies) != 0 {
			t.Fatalf("%s re-wake = %q", mode, bodies)
		}
		if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 1 || strings.Count(requests[0].QuestionText, item) != 1 {
			t.Fatalf("%s operator escalation = %+v, %v", mode, requests, err)
		}
	}
}

// #1561: a failed self-release wakes the overseer once and becomes exactly one
// NEEDS YOU card naming the release, its phase and its reason; a retry failing
// for the same cause escalates nothing more, and a new cause wakes again.
func TestOverseerFailedReleaseEscalatesOncePerCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, _ := wakeFixture(t)
	release := func(sha byte, state, phase, reason string, at int64) {
		t.Helper()
		delivery := ProductionDelivery{ID: "release:" + strings.Repeat(string(sha), 40), Kind: "runtime", Destination: "factoryd", Revision: strings.Repeat(string(sha), 40), State: state, Phase: phase, Reason: reason, PullRequests: []uint64{}}
		if err := store.RecordDelivery(ctx, worker.ProjectID, "dark-factory-build/dark-factory", delivery, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
	}
	rewake, settle := OverseerRewakeAfter.Milliseconds(), overseerWakeSettle.Milliseconds()
	// An unrelated full wake that ran before the failure does not count for it.
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 88), IncarnationID: incarnationID(t, 89), ProjectID: worker.ProjectID, AssignedAgentID: agentID(t, 3), Title: overseerWakeTitle, Body: "Factory causal wake: mode=full"}, mustTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	settleCarrier(t, store, 501, 10, "ran")
	at := int64(1000)
	release('a', "failed", "drain", "drain_timeout: run 1 is admitted", at)
	at += settle
	escalation := "Escalated: factoryd release:" + strings.Repeat("a", 40) + " failed in phase drain: drain_timeout: run 1 is admitted"
	if bodies := wakeBodies(t, store, at); len(bodies) != 1 || !strings.Contains(bodies[0], escalation) {
		t.Fatalf("wake = %q", bodies)
	}
	settleCarrier(t, store, at+1, 31, "ran")
	// The retries, including a manual one of the same commit, which overwrites
	// its record, fail the same way behind other runs: no wake, one card.
	for round, sha := range []byte{'a', 'b', 'c'} {
		at += rewake
		release(sha, "running", "build", "", at)
		// A tick while the retry runs neither wakes nor drops the card.
		if bodies := wakeBodies(t, store, at); len(bodies) != 0 {
			t.Fatalf("running retry %d woke = %q", round+1, bodies)
		}
		release(sha, "failed", "drain", fmt.Sprintf("drain_timeout: run %d is admitted", round+2), at+1)
		if bodies := wakeBodies(t, store, at+settle); len(bodies) != 0 {
			t.Fatalf("retry %d re-woke = %q", round+1, bodies)
		}
	}
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 1 || !strings.Contains(requests[0].QuestionText, "failed in phase drain") {
		t.Fatalf("operator escalation = %+v, %v", requests, err)
	}
	at += rewake
	release('d', "failed", "trial", "the new build was not promoted (answered false): it exited", at)
	if bodies := wakeBodies(t, store, at+settle); len(bodies) != 1 || !strings.Contains(bodies[0], "factoryd release:"+strings.Repeat("d", 40)+" failed in phase trial") {
		t.Fatalf("new cause wake = %q", bodies)
	}
	// A manual retry of that commit fails with the first cause again: its
	// record no longer shows the change of cause, but the wake naming it does.
	settleCarrier(t, store, at+settle+1, 52, "ran")
	at += rewake
	release('d', "failed", "drain", "drain_timeout: run 4 is admitted", at)
	if bodies := wakeBodies(t, store, at+settle); len(bodies) != 1 || !strings.Contains(bodies[0], "factoryd release:"+strings.Repeat("d", 40)+" failed in phase drain") {
		t.Fatalf("returning cause wake = %q", bodies)
	}
	// Causes that differ only after a long shared prefix (a temporary
	// directory's generated name), or only in a bracket, are distinct, even
	// on a same-commit retry.
	for round, directory := range []string{"1", "2", "]", ")"} {
		settleCarrier(t, store, at+settle+1, byte(73+21*round), "ran")
		at += rewake
		release('d', "failed", "build", "mkdir "+strings.Repeat("/x", 50)+"/dark-factory-release-"+directory+": no space left on device", at)
		if bodies := wakeBodies(t, store, at+settle); len(bodies) != 1 || !strings.Contains(bodies[0], "dark-factory-release-"+directory+": no space") {
			t.Fatalf("long cause %s wake = %q", directory, bodies)
		}
	}
	// A full wake that did not print the failure (one overflowing with older
	// items) is not its look (#1972): the failure still wakes once.
	settleCarrier(t, store, at+settle+1, 157, "ran")
	at += rewake
	release('e', "failed", "stage", "backup: disk full", at)
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 90), IncarnationID: incarnationID(t, 91), ProjectID: worker.ProjectID, AssignedAgentID: agentID(t, 3), Title: overseerWakeTitle, Body: "Factory causal wake: mode=full"}, mustTime(t, at+1)); err != nil {
		t.Fatal(err)
	}
	settleCarrier(t, store, at+2, 178, "ran")
	at += rewake
	release('e', "running", "build", "", at)
	release('e', "failed", "stage", "backup: disk still full", at+1)
	if bodies := wakeBodies(t, store, at+1+settle); len(bodies) != 1 || !strings.Contains(bodies[0], "backup: disk still full") {
		t.Fatalf("failure a full wake left out = %q", bodies)
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
	mergeable := false
	pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", Mergeable: &mergeable, MergeState: "clean", Review: ProductionReview{Head: head, State: "unknown"}}
	if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 84)); err != nil {
		t.Fatal(err)
	}
	bodies := wakeBodies(t, store, 80+overseerWakeSettle.Milliseconds())
	want := "Supervise.\n\nFactory causal wake: mode=full; prior_task_id=; worker tasks queued=1 running=0; open factory PRs conflicting=1 failing=0 approved-not-queued=0. mode=full requires fixed-head reconciliation.\n- " +
		finalizing.TaskID.String() + ` "verify" blocked rev=1 change=` + change.ID.String()[:12] + `@` + strings.Repeat("d2", 20) + ` PR #7 open/unknown: needs a decision`
	if len(bodies) != 1 || bodies[0] != want {
		t.Fatalf("wake = %q\nwant %q", bodies, want)
	}
}

// Lines that overflow the provider bound are dropped from the end one at a
// time, before any escalation, and the wake becomes full.
func TestOverseerWakeInstructionFallsBackToFull(t *testing.T) {
	t.Parallel()
	line := "- " + strings.Repeat("x", 200)
	lines := make([]string, 50) // > 8 KiB with Codex
	for index := range lines {
		lines[index] = line
	}
	prior := taskID(t, 9)
	body := overseerWakeInstruction(ProviderCodex, "Supervise.", "counts", lines, []string{"Escalated: stuck"}, &prior, false)
	if kept := strings.Count(body, line); kept == 0 || wakeBodyFits(ProviderCodex, body+"\n"+line) ||
		!strings.Contains(body, "mode=full") || !strings.HasSuffix(body, "\nEscalated: stuck") {
		t.Fatalf("overflow body keeps %d lines = %q", kept, body)
	}
	if body := overseerWakeInstruction(ProviderCodex, "Supervise.", "counts", lines[:1], nil, &prior, false); !strings.Contains(body, "mode=targeted; prior_task_id="+prior.String()) || !strings.HasSuffix(body, "\n"+line) {
		t.Fatalf("targeted body = %q", body)
	}
}

// #1972: 100 items due at once behind a 4 KiB standing instruction overflow
// every wake, yet successive wakes name each exactly once: an item counts as
// seen only once a carrier printed its key, so none is lost.
func TestOverseerOverflowingWakesDrainEveryItem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, overseer := wakeFixture(t)
	policy, after, instruction := IdleStandingInstruction, uint32(1), strings.Repeat("Supervise. ", 372)
	if _, err := store.UpdateAgent(ctx, overseer.ID, overseer.Revision, AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	for number := range 100 {
		escalate(t, store, overseer.ProjectID, strconv.Itoa(number+1), "open", head, head, 1000)
	}
	named := map[int]int{}
	at := 1000 + overseerWakeSettle.Milliseconds()
	for wake := 0; ; wake++ {
		bodies := wakeBodies(t, store, at)
		if len(bodies) == 0 {
			break
		}
		if len(bodies) != 1 || wake == 100 || !wakeBodyFits(ProviderCodex, bodies[0]) || !strings.Contains(bodies[0], "[reviewer:op") {
			t.Fatalf("wake %d = %q", wake, bodies)
		}
		for number := range 100 {
			named[number+1] += strings.Count(bodies[0], "[reviewer:op"+strconv.Itoa(number+1)+"]")
		}
		settleCarrier(t, store, at+1, byte(10+wake*21), "ran")
		at += overseerWakeSettle.Milliseconds()
	}
	for number := range 100 {
		if named[number+1] != 1 {
			t.Fatalf("item %d named %d times: %v", number+1, named[number+1], named)
		}
	}
}

// A succeeded intake task with a diff is factoryd's to publish; once its pull
// request merged (here with its Change reclaimed) it is the overseer's to
// verify (TestOverseerVerifiesMergedIntakeOnceLive); any other success, an
// empty intake one whose Change was reclaimed included, gets one look.
func TestOverseerWakeSkipsPublishedIntakeSuccess(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"plain", "intake", "reclaimed", "empty"} {
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
		if mode == "empty" {
			moved = change.Selection.commit
		}
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
		pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/example/factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: map[bool]string{false: "open", true: "merged"}[mode == "reclaimed"], MergedAt: "2026-10-14T19:14:54Z", Review: ProductionReview{Head: head, State: "unknown"}}
		if mode != "empty" {
			if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "example/factory", pr, mustTime(t, 84)); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "reclaimed" || mode == "empty" {
			if _, err := store.ReclaimChange(ctx, change.ID, mustRevision(t, change.Revision.Int64()+1), false, mustTime(t, 85)); err != nil {
				t.Fatal(err)
			}
		}
		want := 0
		if mode == "reclaimed" {
			want = 1
		}
		if bodies := wakeBodies(t, store, 1_000_000); len(bodies) != want {
			t.Fatalf("%s wake = %q, want %d", mode, bodies, want)
		}
	}
}

// A succeeded intake task whose pull request merged wakes its overseer once,
// when the merge is live: at the first verified self-release that ships it,
// whether that release came before or after the merge was observed, and after
// an upgrade whose older release records name nothing. A full pass that ran
// before the merge was observed is not that look. The look verifies the
// issue's live signal (#1906); later releases do not wake it again, and
// nothing stalls into a card.
func TestOverseerVerifiesMergedIntakeOnceLive(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"release first", "merge first", "unshipped", "before receipt cutover"} {
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
		for _, statement := range []string{`PRAGMA foreign_keys = OFF`, `INSERT INTO intake_task_bindings(task_id, acceptance_id) VALUES (?, zeroblob(16))`, `PRAGMA foreign_keys = ON`} {
			args := []any{finalizing.TaskID.Bytes()}[:strings.Count(statement, "?")]
			if _, err := store.writer.ExecContext(ctx, statement, args...); err != nil {
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
		release := func(sha byte, verifiedAt int64, pulls ...uint64) {
			t.Helper()
			revision := strings.Repeat(string(sha), 40)
			delivery := ProductionDelivery{ID: "release:" + revision, Kind: "runtime", Destination: "factoryd", Revision: revision, State: "verified", VerifiedAt: verifiedAt, PullRequests: append([]uint64{}, pulls...)}
			if err := store.RecordDelivery(ctx, finalizing.ProjectID, "dark-factory-build/dark-factory", delivery, mustTime(t, verifiedAt)); err != nil {
				t.Fatal(err)
			}
		}
		fullPass := func(at int64, seed byte) {
			t.Helper()
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, seed), IncarnationID: incarnationID(t, seed+1), ProjectID: finalizing.ProjectID, AssignedAgentID: overseer.ID, Title: overseerWakeTitle, Body: "Factory causal wake: mode=full"}, mustTime(t, at)); err != nil {
				t.Fatal(err)
			}
			settleCarrier(t, store, at+1, seed, "ran")
		}
		head := hex.EncodeToString(moved.Bytes())
		mergedAt := "2026-10-14T19:14:54Z"
		if order == "before receipt cutover" {
			mergedAt = "2026-10-14T19:14:52Z"
		}
		pr := ProductionPullRequest{Number: 7, Title: "Ship", URL: "https://github.com/dark-factory-build/dark-factory/pull/7", Head: head, Branch: "factory/" + change.ID.String()[:12], Base: "main", State: "open", MergedAt: mergedAt, Review: ProductionReview{Head: head, State: "allow"}}
		if err := store.RecordPublication(ctx, finalizing.ProjectID, finalizing.TaskID, "dark-factory-build/dark-factory", pr, mustTime(t, 90)); err != nil {
			t.Fatal(err)
		}
		// What a release names comes from the store's outstanding intake
		// pulls, its merge observed or not (release.go keeps those its
		// history ships).
		outstanding := func() []uint64 {
			t.Helper()
			pulls, err := store.UnreleasedIntakePulls(ctx, finalizing.ProjectID, "dark-factory-build/dark-factory")
			if err != nil || !slices.Equal(pulls, []uint64{7}) {
				t.Fatalf("outstanding intake pulls = %v %v", pulls, err)
			}
			return pulls
		}
		merge := func(at int64) {
			t.Helper()
			pr.State = "merged"
			if err := store.RecordProductionObservation(ctx, finalizing.ProjectID, ProductionObservation{Repository: "dark-factory-build/dark-factory", ObservedAt: at, PullRequests: []ProductionPullRequest{pr}, Checks: []ProductionCheck{}}, mustTime(t, at)); err != nil {
				t.Fatal(err)
			}
		}
		settle := overseerWakeSettle.Milliseconds()
		release('f', 500) // recorded before releases named what they ship
		live := int64(5000)
		switch order {
		case "release first":
			// The release shipped it before refresh observed the merge.
			release('b', 2000, outstanding()...)
			fullPass(3000, 31)
			merge(live)
		case "merge first":
			fullPass(2000, 31)
			merge(3000)
			if bodies := wakeBodies(t, store, 3000+settle); len(bodies) != 0 {
				t.Fatalf("merged but not yet live woke = %q", bodies)
			}
			release('b', live, outstanding()...)
		case "unshipped":
			release('b', 2000, 9)
			merge(3000)
			if bodies := wakeBodies(t, store, 3000+settle); len(bodies) != 0 {
				t.Fatalf("a merge after the listing release woke before one shipped it = %q", bodies)
			}
			release('d', live, outstanding()...)
		case "before receipt cutover":
			merge(3000)
			release('b', live, outstanding()...)
			if bodies := wakeBodies(t, store, live+settle); len(bodies) != 0 {
				t.Fatalf("pre-cutover merge woke = %q", bodies)
			}
			continue
		}
		bodies := wakeBodies(t, store, live+settle)
		if len(bodies) != 1 || !strings.Contains(bodies[0], finalizing.TaskID.String()+` "verify" succeeded`) || !strings.Contains(bodies[0], "PR #7 merged/allow") {
			t.Fatalf("%s: live merge wake = %q", order, bodies)
		}
		settleCarrier(t, store, live+settle+1, 52, "ran")
		release('c', 9000, 7)
		for _, at := range []int64{9000 + settle, 9000 + OverseerRewakeAfter.Milliseconds()*5} {
			if bodies := wakeBodies(t, store, at); len(bodies) != 0 {
				t.Fatalf("verified merge re-woke at %d = %q", at, bodies)
			}
		}
		if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 0 {
			t.Fatalf("a verified merge stalled into a card: %+v %v", requests, err)
		}
		if items, err := store.OverseerItems(ctx, finalizing.ProjectID, mustTime(t, 1<<40)); err != nil || len(items) != 0 {
			t.Fatalf("a verified merge is still an item: %+v %v", items, err)
		}
	}
}

// An unchanged escalation is not re-woken, even when later targeted or full
// wakes run for other items.
func TestOverseerRewakesCountPerItem(t *testing.T) {
	t.Parallel()
	for other := range map[string]bool{"Factory causal wake: mode=targeted; \nEscalated: stuck 8": true, "Supervise.": false} {
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
		if got := len(bodies) == 1 && strings.Contains(bodies[0], "Escalated: stuck 7"); got {
			t.Fatalf("unchanged escalation re-woke after %q: %q", other, bodies)
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
			`UPDATE tasks SET title = ?, body = 'Factory causal wake: mode=targeted; \nEscalated: stuck 7 [reviewer:op7]', status = 'failed', work_revision = 1, completed_at_ms = updated_at_ms WHERE id = ?`: {overseerWakeTitle, finalizing.TaskID.Bytes()},
			`UPDATE runs SET terminal_detail = ?1, proposal_detail = ?1 WHERE id = ?2`: {detail, finalizing.ID.Bytes()},
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
		if err := read.connection.QueryRowContext(ctx, overseerItems+`SELECT wakes FROM counted WHERE item_key = '[reviewer:op7]' AND ?6 + ?7 + ?8 >= 0`, overseerItemArgs(agent, 2+overseerWakeSettle.Milliseconds(), nil)...).Scan(&wakes); err != nil {
			t.Fatal(err)
		}
		read.Close()
		if want := map[bool]int{true: 0, false: 1}[detail == NeverStartedRunDetail]; wakes != want {
			t.Fatalf("detail %q: counted wakes = %d, want %d", detail, wakes, want)
		}
		if bodies := wakeBodies(t, store, 2+overseerWakeSettle.Milliseconds()); len(bodies) != 0 {
			t.Fatalf("detail %q: woken again within the half hour = %q", detail, bodies)
		}
		bodies := wakeBodies(t, store, 4+OverseerRewakeAfter.Milliseconds())
		if want := detail == NeverStartedRunDetail; (len(bodies) == 1 && strings.Contains(bodies[0], "Escalated: stuck 7")) != want {
			t.Fatalf("detail %q: wake after the half hour = %q, want=%v", detail, bodies, want)
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

// An escalation is delivered once per head and reason, then becomes one NEEDS
// YOU card. A wake that never started remains eligible for its one delivery.
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
	if err := store.writer.QueryRowContext(ctx, `SELECT continuation_reply FROM human_requests WHERE id = ?`, card.ID.Bytes()).Scan(&resolution); err != nil ||
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

// A stalled item that is not ripe on the first quiet poll is raised when its
// clock deadline arrives, even though no invalidation advances the store head.
func TestStalledCardRipensWithAQuietStore(t *testing.T) {
	t.Parallel()
	store, worker, _ := wakeFixture(t)
	head := strings.Repeat("a", 40)
	escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
	at := 1000 + overseerWakeMaxDelay.Milliseconds()
	seed := byte(10)
	next := func() byte { seed += 21; return seed }
	if bodies := wakeBodies(t, store, at); len(bodies) != 1 {
		t.Fatalf("first wake = %q", bodies)
	}
	settleCarrier(t, store, at+1, next(), NeverStartedRunDetail)
	settleCarrier(t, store, at+20, next(), NeverStartedRunDetail)
	at += 40 + OverseerRewakeAfter.Milliseconds()
	tasks, err := store.EnqueueOverseerWakeups(context.Background(), mustTime(t, at))
	if err != nil || len(tasks) != 1 {
		t.Fatalf("started wake = %+v, %v", tasks, err)
	}
	settleCarrier(t, store, at+1, next(), "ran")
	if bodies := wakeBodies(t, store, at+10); len(bodies) != 0 {
		t.Fatalf("quiet pre-ripe wake = %q", bodies)
	}
	wakeBodies(t, store, at+10+OverseerRewakeAfter.Milliseconds())
	if requests, err := store.OperatorHumanRequests(context.Background()); err != nil || len(requests) != 1 {
		t.Fatalf("cards after quiet ripening = %d, %v", len(requests), err)
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
	if err := cancelHumanContinuationOnConnection(ctx, tx, request, mustTime(t, at+1)); err != nil {
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

// stalledCard drives escalation 7 through one never-started wake, one started
// wake, and wakes about other items to its NEEDS YOU card, raised once.
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
	tasks, err := store.EnqueueOverseerWakeups(context.Background(), mustTime(t, at))
	if err != nil || len(tasks) != 1 || !strings.Contains(tasks[0].Body, "Escalated: stuck 7") {
		t.Fatalf("started wake = %+v, %v", tasks, err)
	}
	last := tasks[0]
	settleCarrier(t, store, at+1, next(), "ran")
	// Stalled: no second wake for item 7, and one card however often the poll runs.
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

// A health condition factoryd holds only in memory wakes the overseer of its
// project (or of every project) once settled, with when it began; it re-wakes
// like an escalation, stalls into the operator's card, and the card closes
// once factoryd stops holding it.
func TestOverseerHealthWake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, _ := wakeFixture(t)
	health := []OverseerHealth{{Key: "github-quota", Detail: "GitHub quota 12/5000", Since: mustTime(t, 1000)},
		{Project: projectID(t, 9), Key: "intake:x", Detail: "another project's intake", Since: mustTime(t, 1000)}}
	at, seed := 1000+overseerWakeSettle.Milliseconds(), byte(10)
	if tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at-1), health...); err != nil || len(tasks) != 0 {
		t.Fatalf("unsettled wake = %+v, %v", tasks, err)
	}
	for wake := 0; wake <= 3; wake++ {
		tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at), health...)
		if err != nil || len(tasks) != 1 || !strings.Contains(tasks[0].Body, "\nHealth since 1970-01-01T00:00:01Z: GitHub quota 12/5000 [health:github-quota]") || strings.Contains(tasks[0].Body, "another project") {
			t.Fatalf("wake %d = %+v, %v", wake, tasks, err)
		}
		seed += 21
		settleCarrier(t, store, at+1, seed, "ran")
		at += OverseerRewakeAfter.Milliseconds()
	}
	if tasks, err := store.EnqueueOverseerWakeups(ctx, mustTime(t, at), health...); err != nil || len(tasks) != 0 {
		t.Fatalf("stalled condition woke again = %+v, %v", tasks, err)
	}
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 1 || !strings.Contains(requests[0].QuestionText, "GitHub quota 12/5000 [health:github-quota]") {
		t.Fatalf("stalled card = %+v, %v", requests, err)
	}
	wakeBodies(t, store, at+1)
	if requests, err := store.OperatorHumanRequests(ctx); err != nil || len(requests) != 0 {
		t.Fatalf("cleared condition kept its card = %+v, %v", requests, err)
	}
}

// The operator reads a due item as the overseer's next wake names it, and
// each stalled item (7, and 20 to 23 the re-wakes of 7 also named) with the
// carrier that last named it.
func TestOverseerItemsShowStalledAndDue(t *testing.T) {
	t.Parallel()
	store, worker, last, _, at, _ := stalledCard(t)
	head := strings.Repeat("a", 40)
	escalate(t, store, worker.ProjectID, "30", "open", head, head, at)
	items, err := store.OverseerItems(context.Background(), worker.ProjectID, mustTime(t, at+overseerWakeMaxDelay.Milliseconds()))
	if err != nil || len(items) != 2 || !items[0].Due || items[0].Line != "Escalated: stuck 30 [reviewer:op30]" {
		t.Fatalf("items = %+v, %v", items, err)
	}
	if items[1].Due || items[1].Line != "Escalated: stuck 7 [reviewer:op7]" || !bytes.Equal(items[1].Carrier, last.ID.Bytes()) {
		t.Fatalf("stalled item = %+v", items[1])
	}
}

// Identical escalations (same pull, head and reason) name one reviewer item:
// the newest. An older duplicate is not a second item.
func TestOverseerIdenticalEscalationsNameOnlyTheNewest(t *testing.T) {
	t.Parallel()
	store, worker, _ := wakeFixture(t)
	head := strings.Repeat("a", 40)
	escalate(t, store, worker.ProjectID, "7", "open", head, head, 1000)
	if _, err := store.writer.ExecContext(context.Background(), `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'reviewer', 'op7-again', '', ?, 1100)`,
		worker.ProjectID.Bytes(), `{"request":{"PullNumber":7,"Head":"`+strings.ToUpper(head)+`"},"escalation":"stuck 7"}`); err != nil {
		t.Fatal(err)
	}
	bodies := wakeBodies(t, store, 1100+overseerWakeSettle.Milliseconds())
	if len(bodies) != 1 || strings.Count(bodies[0], "Escalated: stuck 7") != 1 || !strings.Contains(bodies[0], "[reviewer:op7-again]") || strings.Contains(bodies[0], "[reviewer:op7]") {
		t.Fatalf("duplicate escalation wake = %q", bodies)
	}
}
