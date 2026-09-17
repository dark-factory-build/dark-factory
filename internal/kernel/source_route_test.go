package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedSourceReviewRouteUsesInstalledProviderCapability(t *testing.T) {
	task := "review handoff " + strings.Repeat("a", 32)
	for _, test := range []struct {
		name     string
		role     AgentRole
		provider Provider
		wantErr  bool
	}{
		{name: "codex worker", role: RoleWorker, provider: ProviderCodex},
		{name: "claude worker", role: RoleWorker, provider: ProviderClaudeCode, wantErr: true},
		{name: "shell worker", role: RoleWorker, provider: ProviderShell, wantErr: true},
		{name: "orchestrator", role: RoleOrchestrator, provider: ProviderCodex, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRetainedSourceReviewRoute(task, Agent{Role: test.role, Provider: test.provider})
			if test.wantErr != (err != nil) {
				t.Fatalf("route error = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr && test.role == RoleWorker && (!errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "supported routes: codex")) {
				t.Fatalf("route error lacks durable unavailable proof: %v", err)
			}
		})
	}
}

func TestRetainedSourceReviewRouteLeavesOrdinaryTasksProviderAgnostic(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCode, ProviderCodex, ProviderShell} {
		if err := validateRetainedSourceReviewRoute("ordinary worker task", Agent{Role: RoleWorker, Provider: provider}); err != nil {
			t.Fatalf("ordinary %s task rejected: %v", provider, err)
		}
	}
}

// TestRetainedSourceReviewRouteBlocksNonCodexTaskCreation exercises the
// production guard through the real durable entry point (Store.EnqueueTask
// -> insertTaskOnConnection), not the private validator directly. Deleting
// or bypassing the validateRetainedSourceReviewRoute call at
// internal/kernel/store.go's insertTaskOnConnection would make this test
// fail: a review-handoff task would be accepted for a non-Codex worker.
func TestRetainedSourceReviewRouteBlocksNonCodexTaskCreation(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "project", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 2), ProjectID: project.ID, Name: "claude-worker", Role: RoleWorker,
		Provider: ProviderClaudeCode, Model: "private-model", ReasoningEffort: "high", ToolBudgetLimit: 100,
	}, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	codex, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 3), ProjectID: project.ID, Name: "codex-worker", Role: RoleWorker,
		Provider: ProviderCodex, Model: "private-model", ReasoningEffort: "high", ToolBudgetLimit: 100,
	}, mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	body := "review handoff " + strings.Repeat("a", 32)
	if _, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 4), ProjectID: project.ID, AssignedAgentID: claude.ID, IncarnationID: incarnationID(t, 4),
		Title: "task", Body: body, Priority: 0,
	}, mustTime(t, 13)); !errors.Is(err, ErrConflict) {
		t.Fatalf("review handoff task assigned to non-codex worker: err=%v, want ErrConflict", err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 5), ProjectID: project.ID, AssignedAgentID: codex.ID, IncarnationID: incarnationID(t, 5),
		Title: "task", Body: body, Priority: 0,
	}, mustTime(t, 14))
	if err != nil {
		t.Fatalf("review handoff task assigned to codex worker rejected: %v", err)
	}
	if task.Status != TaskQueued {
		t.Fatalf("unexpected task: %+v", task)
	}
}
