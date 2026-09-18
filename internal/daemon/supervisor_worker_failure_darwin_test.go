//go:build darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestSupervisorPersistsEarlyWorkerFailure(t *testing.T) {
	for _, stage := range []string{"selection", "preparation"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
			want := "Git process failed"
			var conflictPath string
			if stage == "selection" {
				fixture.spec.BaseRevision = strings.Repeat("0", 40)
			} else {
				fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
					runs, err := fixture.store.RecoverableRuns(context.Background())
					if err != nil || len(runs) != 1 {
						t.Fatalf("runs=%v err=%v", runs, err)
					}
					conflictPath = filepath.Join(fixture.changeParent, runs[0].Run.ChangeID.String())
					if err := os.Mkdir(conflictPath, 0700); err != nil {
						t.Fatal(err)
					}
					return child.Activate()
				}
				want = "private Change path is already taken"
			}
			run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("failure lost: %v", err)
			}
			stored, found, err := fixture.store.Run(context.Background(), run.ID)
			if err != nil || !found || stored.Proposal == nil || !strings.Contains(stored.Proposal.Detail(), want) {
				t.Fatalf("durable failure=%+v found=%v err=%v", stored, found, err)
			}
			if _, err := os.Stat(fixture.witness); !os.IsNotExist(err) {
				t.Fatalf("provider ran after refusal: %v", err)
			}
			fixture.assertTerminal(t, run, kernel.OutcomeFailed)
			if conflictPath != "" {
				if err := os.Remove(conflictPath); err != nil {
					t.Fatal(err)
				}
			}
			fixture.spec.activateOuter = nil
			fixture.spec.BaseRevision = fixture.base
			// Repair the durable default for new work. A boot flag change must not
			// retarget the failed task's already-bound source.
			repository, found, err := fixture.store.TaskRepository(context.Background(), fixture.taskID)
			if err != nil || !found {
				t.Fatalf("repository: found=%v err=%v", found, err)
			}
			if _, err := fixture.store.UpdateProjectRepositoryBase(context.Background(), repository.ID, repository.Revision, fixture.base, supervisorTime()); err != nil {
				t.Fatal(err)
			}
			task, found, err := fixture.store.Task(context.Background(), fixture.taskID)
			if err != nil || !found {
				t.Fatalf("task: found=%v err=%v", found, err)
			}
			// Shell tasks are programs and cannot receive send-back prose.
			// A new explicit task proves the repaired prerequisite can launch.
			fixture.taskID = supervisorTaskID(t, 90)
			if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{ID: fixture.taskID, ProjectID: task.ProjectID, AssignedAgentID: fixture.agentID, IncarnationID: supervisorIncarnationID(t, 91), Title: task.Title, Body: task.Body, Priority: 1}, supervisorTime()); err != nil {
				t.Fatal(err)
			}

			retry, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			fixture.assertTerminal(t, retry, kernel.OutcomeSucceeded)
		})
	}
}
