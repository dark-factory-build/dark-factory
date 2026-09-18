//go:build darwin

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestSupervisorCompletesTasksFromEachRegisteredRepository(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	ctx := context.Background()
	if err := fixture.store.InitializeRepositoryBase(ctx, fixture.spec.BaseRevision); err != nil {
		t.Fatal(err)
	}
	first, found, err := fixture.store.TaskRepository(ctx, fixture.taskID)
	if err != nil || !found {
		t.Fatalf("first route: found=%v err=%v", found, err)
	}
	project, found, err := fixture.store.Project(ctx, first.ProjectID)
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}
	if _, err := registerProject(ctx, fixture.store, kernel.NewProject{ID: project.ID, Name: project.Name, Root: first.Root, VerificationPolicy: project.VerificationPolicy}, supervisorTime()); err != nil {
		t.Fatal(err)
	}

	git := supervisorNativeGit(t)
	secondRoot := contentRepositoryFixture(t)
	supervisorGit(t, git, "-C", secondRoot, "checkout", "-q", "-b", "release")
	if err := os.WriteFile(filepath.Join(secondRoot, "release.txt"), []byte("release source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", secondRoot, "add", "release.txt")
	supervisorGit(t, git, "-C", secondRoot, "commit", "-q", "-m", "release")
	secondBase := strings.TrimSpace(supervisorGitOutput(t, git, "-C", secondRoot, "rev-parse", "refs/heads/release"))
	secondID, err := kernel.RepositoryIDFromBytes(supervisorIDBytes(5))
	if err != nil {
		t.Fatal(err)
	}
	second, err := registerProjectRepository(ctx, fixture.store, kernel.NewProjectRepository{ID: secondID, ProjectID: project.ID, Name: "release", Root: secondRoot, BaseRef: "refs/heads/release"}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetProjectRepositoryDefault(ctx, second.ID, second.Revision, supervisorTime()); err != nil {
		t.Fatal(err)
	}

	firstRun, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if err != nil {
		t.Fatalf("first RunNext: %v", err)
	}
	fixture.assertTerminal(t, firstRun, kernel.OutcomeSucceeded)
	assertRepositoryRun(t, fixture, firstRun, first, fixture.base, "payload.txt", "exact source\n")

	secondTask := supervisorTaskID(t, 6)
	if _, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: secondTask, ProjectID: project.ID, AssignedAgentID: fixture.agentID, IncarnationID: supervisorIncarnationID(t, 7), Title: "release task", Body: strings.ReplaceAll(supervisorProgram(t, false, false), "__WITNESS__", quoteShell(fixture.witness)), Priority: 1}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	secondRun, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if err != nil {
		t.Fatalf("second RunNext: %v", err)
	}
	fixture.assertTerminal(t, secondRun, kernel.OutcomeSucceeded)
	assertRepositoryRun(t, fixture, secondRun, second, secondBase, "release.txt", "release source\n")
}

func assertRepositoryRun(t *testing.T, fixture *supervisorFixture, run kernel.Run, want kernel.ProjectRepository, base, name, contents string) {
	t.Helper()
	route, found, err := fixture.store.TaskRepository(context.Background(), run.TaskID)
	if err != nil || !found || route.ID != want.ID || route.Root != want.Root || route.BaseRef != want.BaseRef {
		t.Fatalf("run route = %#v found=%v err=%v, want %#v", route, found, err, want)
	}
	changeState, found, err := fixture.store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found || changeState.Selection == nil || fmt.Sprintf("%x", changeState.Selection.Commit().Bytes()) != base {
		t.Fatalf("run Change base = %+v found=%v err=%v, want %s", changeState.Selection, found, err, base)
	}
	body, err := os.ReadFile(filepath.Join(fixture.changeParent, fixture.changeName(t, run), name))
	if err != nil || string(body) != contents {
		t.Fatalf("run source %s = %q, %v", name, body, err)
	}
}

func TestRegisteredRepositoryReplacementCannotLaunchProvider(t *testing.T) {
	for _, target := range []string{"root", "git", "origin"} {
		t.Run(target, func(t *testing.T) {
			fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
			ctx := context.Background()
			if err := fixture.store.InitializeRepositoryBase(ctx, fixture.spec.BaseRevision); err != nil {
				t.Fatal(err)
			}
			repository, found, err := fixture.store.TaskRepository(ctx, fixture.taskID)
			if err != nil || !found {
				t.Fatalf("route: %v %v", found, err)
			}
			project, found, err := fixture.store.Project(ctx, repository.ProjectID)
			if err != nil || !found {
				t.Fatalf("project: %v %v", found, err)
			}
			if _, err := registerProject(ctx, fixture.store, kernel.NewProject{ID: project.ID, Name: project.Name, Root: repository.Root, VerificationPolicy: project.VerificationPolicy}, supervisorTime()); err != nil {
				t.Fatal(err)
			}
			switch target {
			case "root", "git":
				replacement := contentRepositoryFixture(t)
				original := repository.Root
				if target == "git" {
					original = filepath.Join(original, ".git")
					replacement = filepath.Join(replacement, ".git")
				}
				if err := os.Rename(original, original+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, original); err != nil {
					t.Fatal(err)
				}
			case "origin":
				supervisorGit(t, fixture.spec.GitExecutable, "-C", repository.Root, "config", "remote.origin.url", "https://github.com/other/repo.git")
			}
			run, err := fixture.daemon.RunNext(ctx, fixture.spec)
			if err == nil {
				t.Fatal("replacement source launched")
			}
			fixture.assertTerminal(t, run, kernel.OutcomeFailed)
			if _, err := os.Stat(fixture.witness); !os.IsNotExist(err) {
				t.Fatalf("provider ran with replacement source: %v", err)
			}
		})
	}
}
