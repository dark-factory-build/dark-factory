package daemon

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// The kernel test drives the worker lifecycle that creates this durable state.
// This companion drives the real merge-stage entry point against that state.
func TestMergePipelineRetriesBlockedMissingPrerequisite(t *testing.T) {
	fixture, project, producer, settle := publishedTask(t)
	ctx := context.Background()
	settle()
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(180)), ProjectID: project, Name: "consumer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 5}, mustKernelTime(t, 1100))
	if err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(181)))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(182)), ProjectID: project, AssignedAgentID: agent.ID, IncarnationID: incarnation, Title: "consumer"}, mustKernelTime(t, 1101))
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", "file:"+fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settleConsumer := admitPublished(t, fixture, 200, 1103)
	settleConsumer()
	if _, err := database.ExecContext(ctx, `UPDATE agents SET role = 'worker' WHERE id = ?`, agent.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE runs SET phase = 'terminal', proposal_kind = 'succeeded', proposal_code = NULL, proposal_detail = NULL, proposal_result = 'producer', terminal_kind = 'succeeded', terminal_code = NULL, terminal_detail = NULL, terminal_result = 'producer', terminal_at_ms = 1102, updated_at_ms = 1102 WHERE task_id = ?`, producer.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET status = 'succeeded', blocked_reason = NULL, completed_at_ms = 1102, updated_at_ms = 1102 WHERE id = ?`, producer.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO task_prerequisites(task_id, upstream_task_id, upstream_work_revision) VALUES(?, ?, 1)`, consumer.ID.Bytes(), producer.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE runs SET phase = 'terminal', finalizing_at_ms = 1115, credential_revoked_at_ms = 1115, proposal_kind = 'blocked', proposal_code = NULL, proposal_detail = 'missing prerequisite: producer correction is not in the assigned base', proposal_result = NULL, terminal_kind = 'blocked', terminal_code = NULL, terminal_detail = 'missing prerequisite: producer correction is not in the assigned base', terminal_result = NULL, terminal_at_ms = 1115, updated_at_ms = 1115 WHERE task_id = ?`, consumer.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET status = 'blocked', blocked_reason = 'missing prerequisite: producer correction is not in the assigned base', completed_at_ms = NULL, updated_at_ms = 1115 WHERE id = ?`, consumer.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if retried, err := fixture.store.RetryBlockedMissingPrerequisites(ctx, project, mustKernelTime(t, 1104)); err != nil || retried != 0 {
		t.Fatalf("retry before merge = %d, %v", retried, err)
	}
	merged := kernel.ProductionPullRequest{Number: 12, Title: "Ship it", URL: "https://github.com/team/repo/pull/12", Head: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Branch: "feature/ship", Base: "main", State: "merged"}
	if err := fixture.store.RecordProductionObservation(ctx, project, kernel.ProductionObservation{Repository: "team/repo", ObservedAt: 1105, PullRequests: []kernel.ProductionPullRequest{merged}}, mustKernelTime(t, 1105)); err != nil {
		t.Fatal(err)
	}
	customerMode(t, fixture)
	fixture.daemon.advanceMergePipeline(ctx)
	current, found, err := fixture.store.Task(ctx, consumer.ID)
	if err != nil || !found || current.Status != kernel.TaskQueued {
		t.Fatalf("consumer after merge pipeline = %+v, %t, %v", current, found, err)
	}
}
