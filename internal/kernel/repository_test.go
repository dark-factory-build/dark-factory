package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTaskRepositoryBindingSnapshotsDefaultAndScopesConflict(t *testing.T) {
	store, _ := newTestStore(t)
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 80), Name: "repositories", Root: filepath.Join(t.TempDir(), "first")}, mustTime(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID(t, 81), ProjectID: project.ID, Name: "worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	second, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 90), ProjectID: project.ID, Root: filepath.Join(t.TempDir(), "second"), BaseRef: "release"}, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	first, found, err := store.TaskRepository(ctx, taskID(t, 91))
	if err != nil || found {
		t.Fatalf("missing binding = %#v/%v/%v", first, found, err)
	}
	initial, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 92), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 93), Title: "first", ConflictPaths: []string{"same"}}, mustTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	route, found, err := store.TaskRepository(ctx, initial.ID)
	if err != nil || !found || route.ID != RepositoryID(project.ID) || route.BaseRef != "HEAD" {
		t.Fatalf("initial route = %#v/%v/%v", route, found, err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, second.ID, second.Revision, mustTime(t, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 94), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 95), Title: "second", ConflictPaths: []string{"same"}}, mustTime(t, 13)); err != nil {
		t.Fatal(err)
	}
	secondTask, found, err := store.TaskRepository(ctx, taskID(t, 94))
	if err != nil || !found || secondTask.ID != second.ID || secondTask.BaseRef != "release" {
		t.Fatalf("second route = %#v/%v/%v", secondTask, found, err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 96), ProjectID: project.ID, RepositoryID: RepositoryID(project.ID), AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 97), Title: "explicit", ConflictPaths: []string{"same"}}, mustTime(t, 14)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 98), ProjectID: project.ID, RepositoryID: repositoryID(t, 99), AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 100), Title: "foreign"}, mustTime(t, 15)); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign binding error = %v", err)
	}
}

func TestRepositoryDisableAndRemovalRespectBindings(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 110), Name: "routes", Root: filepath.Join(t.TempDir(), "first")}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 111), ProjectID: project.ID, Root: filepath.Join(t.TempDir(), "second"), BaseRef: "HEAD"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProjectRepository(ctx, second.ID, second.Revision); err != nil {
		t.Fatalf("unreferenced remove: %v", err)
	}
	second, err = store.AddProjectRepository(ctx, NewProjectRepository{ID: second.ID, ProjectID: project.ID, Root: second.Root, BaseRef: "HEAD"}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, second.ID, second.Revision, mustTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryEnabled(ctx, RepositoryID(project.ID), Revision{value: 1}, false, mustTime(t, 5)); err != nil {
		t.Fatalf("disable nondefault = %v", err)
	}
	repositories, err := store.ProjectRepositories(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectRepositoryEnabled(ctx, second.ID, repositories[1].Revision, false, mustTime(t, 5)); !errors.Is(err, ErrConflict) {
		t.Fatalf("disable default = %v", err)
	}
}

func repositoryID(t *testing.T, value byte) RepositoryID {
	t.Helper()
	raw := [IDBytes]byte{}
	raw[0] = value
	id, err := RepositoryIDFromBytes(raw[:])
	if err != nil {
		t.Fatal(err)
	}
	return id
}
