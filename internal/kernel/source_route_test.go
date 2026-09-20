package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func reviewHandoffTask() string {
	return "review handoff " + strings.Repeat("a", 32) + " " + strings.Repeat("b", 32) + " " + strings.Repeat("c", 40) + " 3 7"
}

func TestParseRetainedSourceReviewTaskBindsFirstLine(t *testing.T) {
	want := reviewHandoffTask()
	parsed, review, err := ParseRetainedSourceReviewTask(want + "  \r\nFACTORY_SOURCE owner/repo#1\nreview this exact source")
	if err != nil || !review || parsed.TaskID.String() != strings.Repeat("a", 32) || parsed.ChangeID.String() != strings.Repeat("b", 32) || parsed.BaseCommit != strings.Repeat("c", 40) || parsed.TaskWorkRevision.Int64() != 3 || parsed.ChangeRevision.Int64() != 7 {
		t.Fatalf("parsed handoff = %+v, review=%v, err=%v", parsed, review, err)
	}
	for _, body := range []string{
		"ordinary worker task",
		// Admission matches this prefix in SQL, so the parser must not see more.
		" \t" + want,
		"review  handoff" + strings.TrimPrefix(want, "review handoff"),
		"FACTORY_SOURCE owner/repo#1\nreview handoff is prose below the first line",
	} {
		if _, review, err := ParseRetainedSourceReviewTask(body); err != nil || review {
			t.Fatalf("ordinary task classified as review: %q review=%v err=%v", body, review, err)
		}
	}
	for _, body := range []string{
		"review handoff " + strings.Repeat("a", 32),
		strings.Replace(want, strings.Repeat("b", 32), strings.Repeat("B", 32), 1),
		strings.Replace(want, " 3 7", " 0 7", 1),
	} {
		if _, review, err := ParseRetainedSourceReviewTask(body); !review || err == nil {
			t.Fatalf("invalid handoff accepted: %q review=%v err=%v", body, review, err)
		}
	}
}

func TestRetainedSourceReviewRouteUsesInstalledProviderCapability(t *testing.T) {
	task := reviewHandoffTask()
	for _, test := range []struct {
		name     string
		role     AgentRole
		provider Provider
		wantErr  bool
	}{
		{name: "codex worker", role: RoleWorker, provider: ProviderCodex},
		{name: "claude worker", role: RoleWorker, provider: ProviderClaudeCode},
		{name: "shell worker", role: RoleWorker, provider: ProviderShell, wantErr: true},
		{name: "orchestrator", role: RoleOrchestrator, provider: ProviderCodex, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRetainedSourceReviewRoute(task, Agent{Role: test.role, Provider: test.provider})
			if test.wantErr != (err != nil) {
				t.Fatalf("route error = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr && test.role == RoleWorker && (!errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "supported routes: codex, claude_code")) {
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

// TestRetainedSourceReviewRouteBlocksUnsupportedTaskCreation exercises the
// production guard through the real durable entry point (Store.EnqueueTask
// -> insertTaskOnConnection), not the private validator directly. Deleting
// or bypassing the validateRetainedSourceReviewRoute call at
// internal/kernel/store.go's insertTaskOnConnection would make this test
// fail: a review-handoff task would be accepted for an unsupported worker.
func TestRetainedSourceReviewRouteBlocksUnsupportedTaskCreation(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "project", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 3), ProjectID: project.ID, Name: "claude-worker", Role: RoleWorker,
		Provider: ProviderClaudeCode, ToolBudgetLimit: 100,
	}, mustTime(t, 13))
	if err != nil {
		t.Fatal(err)
	}
	shell, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 2), ProjectID: project.ID, Name: "shell-worker", Role: RoleWorker,
		Provider: ProviderShell, Model: "", ReasoningEffort: "", ToolBudgetLimit: 100,
	}, mustTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	body := reviewHandoffTask()
	if _, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 4), ProjectID: project.ID, AssignedAgentID: shell.ID, IncarnationID: incarnationID(t, 4),
		Title: "task", Body: body, Priority: 0,
	}, mustTime(t, 13)); !errors.Is(err, ErrConflict) {
		t.Fatalf("review handoff task assigned to shell worker: err=%v, want ErrConflict", err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 5), ProjectID: project.ID, AssignedAgentID: claude.ID, IncarnationID: incarnationID(t, 5),
		Title: "task", Body: body, Priority: 0,
	}, mustTime(t, 14))
	if err != nil || task.Status != TaskQueued {
		t.Fatalf("review handoff task assigned to Claude worker = %+v, err=%v", task, err)
	}
}

// TestRetainedSourceReviewRouteResolvesLegacyQueuedTasks proves the route
// guard in updateTask (internal/kernel/console_update.go) does not strand a
// queued review-handoff task that predates the capability-aware validator: a
// row like that, once admission also excludes it, must still be cancellable
// and reassignable to a Codex worker through the normal Store update paths,
// not stuck forever because the guard now runs on every queued edit.
func TestRetainedSourceReviewRouteResolvesLegacyQueuedTasks(t *testing.T) {
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
	body := reviewHandoffTask()
	// Bypass insertTaskOnConnection's route guard entirely, simulating a row a
	// pre-upgrade daemon persisted before validateRetainedSourceReviewRoute
	// existed: queued, non-Codex, with a review-handoff body.
	insertLegacyReviewHandoff := func(seed byte) TaskID {
		t.Helper()
		id, incarnation := taskID(t, seed), incarnationID(t, seed)
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO tasks(
			id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body,
			sent_back_instruction_bytes,
			status, priority, blocked_reason, result, completed_at_ms, revision,
			created_at_ms, updated_at_ms
		    ) VALUES(?, ?, ?, ?, 1, ?, ?, NULL, 'queued', 0, NULL, NULL, NULL, 1, ?, ?)`,
			id.Bytes(), project.ID.Bytes(), claude.ID.Bytes(), incarnation.Bytes(), "legacy review", body, int64(20), int64(20)); err != nil {
			t.Fatalf("insert legacy review handoff: %v", err)
		}
		if _, err := store.writer.Exec(fixtureTaskRepositorySQL); err != nil {
			t.Fatal(err)
		}
		return id
	}

	cancelID := insertLegacyReviewHandoff(4)
	task, found, err := store.Task(ctx, cancelID)
	if err != nil || !found || task.AssignedAgentID != claude.ID || task.Status != TaskQueued {
		t.Fatalf("legacy task before cancel: found=%v err=%v task=%+v", found, err, task)
	}
	cancelled, err := store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{Cancel: true}, mustTime(t, 30))
	if err != nil {
		t.Fatalf("cancel legacy review handoff task: %v", err)
	}
	if cancelled.Status != TaskCancelled {
		t.Fatalf("legacy task not cancelled: %+v", cancelled)
	}

	reassignID := insertLegacyReviewHandoff(6)
	task, found, err = store.Task(ctx, reassignID)
	if err != nil || !found {
		t.Fatalf("read legacy task: found=%v err=%v", found, err)
	}
	reassigned, err := store.UpdateTask(ctx, task.ID, task.Revision, TaskPatch{AssignedAgentID: &codex.ID}, mustTime(t, 31))
	if err != nil {
		t.Fatalf("reassign legacy review handoff task to codex worker: %v", err)
	}
	if reassigned.AssignedAgentID != codex.ID || reassigned.Status != TaskQueued {
		t.Fatalf("legacy task not reassigned to codex: %+v", reassigned)
	}
}

// TestRetainedSourceReviewRouteAdmissionRequiresWorkerRole proves AdmitNext's
// SQL predicate mirrors validateRetainedSourceReviewRoute's full requirement
// (Codex provider AND worker role), not just the provider half: a queued
// review-handoff task assigned to a Codex orchestrator must not be admitted,
// while the same task body assigned to a Codex worker is admitted normally.
// The orchestrator row is inserted directly, bypassing insertTaskOnConnection's
// guard, since that guard already refuses this pair at creation; admission
// must independently refuse a legacy row that reached the queue some other
// way (e.g. before the guard existed).
func TestRetainedSourceReviewRouteAdmissionRequiresWorkerRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.db")
	store, err := createTestStore(context.Background(), path, FactoryConfig{DispatchEnabled: true, Capacity: 5}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "project", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 2), ProjectID: project.ID, Name: "codex-orchestrator", Role: RoleOrchestrator,
		Provider: ProviderCodex, Model: "private-model", ReasoningEffort: "high", ToolBudgetLimit: 100,
	}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.CreateAgent(ctx, NewAgent{
		ID: agentID(t, 3), ProjectID: project.ID, Name: "claude-worker", Role: RoleWorker,
		Provider: ProviderClaudeCode, Model: "private-model", ReasoningEffort: "high", ToolBudgetLimit: 100,
	}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	body := reviewHandoffTask()
	// Bypass the Go-side guard entirely: a task like this could never be
	// created through insertTaskOnConnection (it requires RoleWorker), so the
	// only way it reaches the queue is as legacy data.
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO tasks(
		id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body,
		sent_back_instruction_bytes,
		status, priority, blocked_reason, result, completed_at_ms, revision,
		created_at_ms, updated_at_ms
	    ) VALUES(?, ?, ?, ?, 1, ?, ?, NULL, 'queued', 0, NULL, NULL, NULL, 1, ?, ?)`,
		taskID(t, 4).Bytes(), project.ID.Bytes(), orchestrator.ID.Bytes(), incarnationID(t, 4).Bytes(), "legacy review", body, int64(5), int64(5)); err != nil {
		t.Fatalf("insert legacy orchestrator review handoff: %v", err)
	}
	if _, err := store.writer.Exec(fixtureTaskRepositorySQL); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{
		ID: taskID(t, 5), ProjectID: project.ID, AssignedAgentID: worker.ID, IncarnationID: incarnationID(t, 5),
		Title: "task", Body: body, Priority: 0,
	}, mustTime(t, 6)); err != nil {
		t.Fatalf("review handoff task assigned to Claude worker rejected: %v", err)
	}

	result, err := store.AdmitNext(ctx, admissionKeys(t, 7, nil), mustTime(t, 7))
	if err != nil || !result.Admitted() || result.Run.AgentID != worker.ID {
		t.Fatalf("Claude worker review handoff not admitted: %+v, %v", result, err)
	}

	result, err = store.AdmitNext(ctx, admissionKeys(t, 8, nil), mustTime(t, 8))
	if err != nil || result.Admitted() {
		t.Fatalf("codex orchestrator review handoff wrongly admitted: %+v, %v", result, err)
	}
	if result.Reason != NoAdmissionSourceRouteUnavailable {
		t.Fatalf("unexpected no-admission reason for orchestrator review handoff: %v", result.Reason)
	}

	orchestratorTask, found, err := store.Task(ctx, taskID(t, 4))
	if err != nil || !found || orchestratorTask.Status != TaskQueued || orchestratorTask.AssignedAgentID != orchestrator.ID {
		t.Fatalf("orchestrator review handoff task unexpectedly changed: %+v found=%v err=%v", orchestratorTask, found, err)
	}
}

func TestRetainedSourceReviewAdmissionUsesDeclaredProviderSet(t *testing.T) {
	for index, provider := range retainedSourceReviewProviders {
		t.Run(provider.String(), func(t *testing.T) {
			store, err := createTestStore(context.Background(), filepath.Join(t.TempDir(), "kernel.db"), FactoryConfig{DispatchEnabled: true, Capacity: 2}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "project", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 2))
			if err != nil {
				t.Fatal(err)
			}
			agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "reviewer", Role: RoleWorker, Provider: provider, ToolBudgetLimit: 10}, mustTime(t, 3))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 3), Title: "review", Body: reviewHandoffTask()}, mustTime(t, 4)); err != nil {
				t.Fatal(err)
			}
			result, err := store.AdmitNext(ctx, admissionKeys(t, byte(20+index), nil), mustTime(t, 5))
			if err != nil || !result.Admitted() || result.Run.AgentID != agent.ID {
				t.Fatalf("declared provider %s was not admitted: %+v, %v", provider, result, err)
			}
		})
	}

	t.Run("unsupported shell", func(t *testing.T) {
		store, err := createTestStore(context.Background(), filepath.Join(t.TempDir(), "kernel.db"), FactoryConfig{DispatchEnabled: true, Capacity: 2}, mustTime(t, 1))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		ctx := context.Background()
		project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 11), Name: "project", Root: filepath.Join(t.TempDir(), "root")}, mustTime(t, 2))
		if err != nil {
			t.Fatal(err)
		}
		agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 12), ProjectID: project.ID, Name: "shell", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 3))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO tasks(
			id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body,
			sent_back_instruction_bytes, status, priority, blocked_reason, result, completed_at_ms, revision,
			created_at_ms, updated_at_ms
		) VALUES(?, ?, ?, ?, 1, ?, ?, NULL, 'queued', 0, NULL, NULL, NULL, 1, ?, ?)`, taskID(t, 13).Bytes(), project.ID.Bytes(), agent.ID.Bytes(), incarnationID(t, 13).Bytes(), "legacy review", reviewHandoffTask(), int64(4), int64(4)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.writer.Exec(fixtureTaskRepositorySQL); err != nil {
			t.Fatal(err)
		}
		result, err := store.AdmitNext(ctx, admissionKeys(t, 31, nil), mustTime(t, 5))
		if err != nil || result.Admitted() || result.Reason != NoAdmissionSourceRouteUnavailable {
			t.Fatalf("unsupported provider admission = %+v, %v", result, err)
		}
	})
}
