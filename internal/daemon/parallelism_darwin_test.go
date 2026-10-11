//go:build darwin

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// TestRealWorkersRespectCapacity uses the real runner, PTY and shell provider
// to prove two live children can occupy capacity two while a third stays queued.
func TestRealWorkersRespectCapacity(t *testing.T) {
	fixture := newSupervisorFixture(t, "unused")
	factory, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetCapacity(context.Background(), factory.Revision, 2, supervisorTime()); err != nil {
		t.Fatal(err)
	}

	ids := []struct {
		agent kernel.AgentID
		task  kernel.TaskID
		seed  int
	}{
		{fixture.agentID, fixture.taskID, 10},
	}
	for _, seed := range []int{20, 30} {
		agent := supervisorAgentID(t, byte(seed))
		task := supervisorTaskID(t, byte(seed+1))
		project, found, err := fixture.store.Project(context.Background(), supervisorProjectID(t, 1))
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("fixture project missing")
		}
		if _, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{ID: agent, ProjectID: project.ID, Name: fmt.Sprintf("worker-%d", seed), Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 20}, supervisorTime()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: task, ProjectID: project.ID, AssignedAgentID: agent, IncarnationID: supervisorIncarnationID(t, byte(seed+2)), Title: fmt.Sprintf("parallel-%d", seed), Body: "unused", Priority: 1}, supervisorTime()); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, struct {
			agent kernel.AgentID
			task  kernel.TaskID
			seed  int
		}{agent, task, seed})
	}

	for _, item := range ids {
		body := fmt.Sprintf("set -eu\nwhile [ ! -e \"$HOME/release\" ]; do sleep 0.01; done\n%s --supervisor-attempt-succeed parallel\n", quoteShell(supervisorTestExecutable(t)))
		execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET body = ? WHERE id = ?`, body, item.task.Bytes())
	}

	ctx, cancel := context.WithCancel(context.Background())
	schedulerDone := make(chan error, 1)
	defer func() {
		cancel()
		select {
		case err := <-schedulerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("scheduler shutdown: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("scheduler did not join its owners")
		}
	}()
	blocked := make(chan struct{}, 1)
	fixture.spec.scheduledAttempt = func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
		run, err := fixture.daemon.RunNext(ctx, spec)
		if run.ID == (kernel.RunID{}) && errors.Is(err, kernel.ErrConflict) {
			select {
			case blocked <- struct{}{}:
			default:
			}
		}
		return run, err
	}
	go func() { schedulerDone <- fixture.daemon.RunScheduler(ctx, fixture.spec) }()
	select {
	case <-blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("scheduler never observed full capacity")
	}
	third, found, err := fixture.store.Task(context.Background(), ids[2].task)
	if err != nil || !found || third.Status != kernel.TaskQueued {
		t.Fatalf("third task was not queued: %v, %v", third.Status, err)
	}
	recoverable, err := fixture.store.RecoverableRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recoverable) != 2 {
		t.Fatalf("live recoverable runs = %d, want 2", len(recoverable))
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		active := 0
		for _, run := range recoverable {
			for _, resource := range fixture.resources(t, run.Run.ID) {
				if resource.Kind == kernel.ResourceProviderProcess && resource.State == kernel.ResourceActive {
					active++
				}
			}
		}
		if active == 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
		recoverable, err = fixture.store.RecoverableRuns(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	providers := 0
	for _, run := range recoverable {
		resources := fixture.resources(t, run.Run.ID)
		for _, resource := range resources {
			if resource.Kind != kernel.ResourceProviderProcess || resource.State != kernel.ResourceActive {
				continue
			}
			providers++
			identity, err := runnerIdentity(resource.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if observation := runner.ObserveProcess(identity); observation.Presence != runner.Present {
				t.Fatalf("provider process for run %s = %+v, want present", run.Run.ID, observation)
			}
		}
	}
	if providers != 2 {
		t.Fatalf("active provider identities = %d, want 2", providers)
	}
	for _, run := range recoverable {
		release := filepath.Join(fixture.runtimeParentPath, run.Run.ID.String(), "home", "release")
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, item := range ids {
			task, found, err := fixture.store.Task(context.Background(), item.task)
			if err != nil || !found {
				t.Fatalf("task lookup = %+v, found=%v, err=%v", task, found, err)
			}
			if task.Status != kernel.TaskSucceeded {
				allDone = false
			}
		}
		if allDone {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("parallel workers did not settle")
}
