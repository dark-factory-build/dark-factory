//go:build darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

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
