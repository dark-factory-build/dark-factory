package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskRepositoryBindingSnapshotsDefaultAndScopesConflict(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.InitializeRepositoryBase(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
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
	second, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 90), ProjectID: project.ID, Name: "second", Root: filepath.Join(t.TempDir(), "second"), BaseRef: "release"}, mustTime(t, 10))
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
	if _, err := store.UpdateProjectRepositoryBase(ctx, second.ID, secondTask.Revision, "develop", mustTime(t, 14)); err != nil {
		t.Fatal(err)
	}
	retained, found, err := store.TaskRepository(ctx, taskID(t, 94))
	if err != nil || !found || retained.BaseRef != "release" {
		t.Fatalf("base change retargeted queued work: %#v/%v/%v", retained, found, err)
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
	second, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 111), ProjectID: project.ID, Name: "second", Root: filepath.Join(t.TempDir(), "second"), BaseRef: "HEAD"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProjectRepository(ctx, second.ID, second.Revision); err != nil {
		t.Fatalf("unreferenced remove: %v", err)
	}
	second, err = store.AddProjectRepository(ctx, NewProjectRepository{ID: second.ID, ProjectID: project.ID, Name: "second", Root: second.Root, BaseRef: "HEAD"}, mustTime(t, 3))
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

func TestLegacyRepositoryBasePinsOnceAcrossMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 130), Name: "legacy", Root: filepath.Join(t.TempDir(), "legacy")}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 131), ProjectID: project.ID, Name: "worker", Role: RoleWorker, Provider: ProviderShell, ToolBudgetLimit: 10}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 132), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 133), Title: "queued before migration"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the exact v20 schema, which had no repository/base setting.
	for _, statement := range []string{"DROP TABLE intake_source_trusted_logins", "DROP TABLE intake_acceptances", "DROP TABLE intake_sources", "DROP TABLE repository_source_identities", "DROP TABLE content_repository_bindings", "DROP TABLE task_repository_bindings", "DROP TABLE project_repositories", "PRAGMA user_version = 20"} {
		if _, err := store.writer.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	route, found, err := store.TaskRepository(ctx, task.ID)
	if err != nil || !found || route.BaseRef != inheritedRepositoryBase {
		t.Fatalf("migration guessed a base: %+v %v %v", route, found, err)
	}
	if err := store.InitializeRepositoryBase(ctx, "refs/heads/release"); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeRepositoryBase(ctx, "refs/heads/ignored"); err != nil {
		t.Fatal(err)
	}
	route, found, err = store.TaskRepository(ctx, task.ID)
	if err != nil || !found || route.ID != RepositoryID(project.ID) || route.BaseRef != "refs/heads/release" {
		t.Fatalf("legacy route: %+v %v %v", route, found, err)
	}
	if _, err := store.UpdateProjectRepositoryBase(ctx, route.ID, route.Revision, "refs/heads/develop", mustTime(t, 5)); err != nil {
		t.Fatal(err)
	}
	next, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 134), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 135), Title: "queued after update"}, mustTime(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.InitializeRepositoryBase(ctx, "refs/heads/new-boot-default"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[TaskID]string{task.ID: "refs/heads/release", next.ID: "refs/heads/develop"} {
		route, found, err := store.TaskRepository(ctx, id)
		if err != nil || !found || route.BaseRef != want {
			t.Fatalf("task %v retargeted: %+v %v %v", id, route, found, err)
		}
	}
	retainedDefault, found, err := store.DefaultProjectRepository(ctx, project.ID)
	if err != nil || !found || retainedDefault.BaseRef != "refs/heads/develop" {
		t.Fatalf("old project default retargeted: %+v %v %v", retainedDefault, found, err)
	}
	fresh, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 136), Name: "new", Root: filepath.Join(t.TempDir(), "new")}, mustTime(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	newDefault, found, err := store.DefaultProjectRepository(ctx, fresh.ID)
	if err != nil || !found || newDefault.BaseRef != "refs/heads/new-boot-default" {
		t.Fatalf("new project did not inherit boot policy: %+v %v %v", newDefault, found, err)
	}
	if original, found, err := store.Task(ctx, task.ID); err != nil || !found || original.IncarnationID != task.IncarnationID || original.Revision != task.Revision {
		t.Fatalf("migration changed task identity: %+v %v %v", original, found, err)
	}
}

func TestRepositoryBaseInitializationPreservesBootArgumentBounds(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	for _, invalid := range []string{"", inheritedRepositoryBase, "-option", "bad\x00ref", strings.Repeat("a", 4097)} {
		if err := store.InitializeRepositoryBase(ctx, invalid); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("invalid base %q: %v", invalid, err)
		}
	}
	base := strings.Repeat("a", 4096)
	if err := store.InitializeRepositoryBase(ctx, base); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 140), Name: "long base", Root: filepath.Join(t.TempDir(), "repo")}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	repository, found, err := store.DefaultProjectRepository(ctx, project.ID)
	if err != nil || !found || repository.BaseRef != base {
		t.Fatalf("boot base did not fit durable binding: %+v %v %v", repository, found, err)
	}
}

// Raw population fixtures need the same routes that public writers create.
const fixtureProjectRepositorySQL = `INSERT INTO project_repositories(id, project_id, name, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms) SELECT p.id, p.id, p.name, p.root, ':factoryd-base-revision', 1, 1, 1, p.created_at_ms, p.updated_at_ms FROM projects p WHERE NOT EXISTS(SELECT 1 FROM project_repositories r WHERE r.project_id = p.id)`
const fixtureTaskRepositorySQL = `INSERT INTO task_repository_bindings(task_id, repository_id, base_ref) SELECT t.id, r.id, r.base_ref FROM tasks t JOIN project_repositories r ON r.project_id = t.project_id AND r.is_default = 1 WHERE NOT EXISTS(SELECT 1 FROM task_repository_bindings b WHERE b.task_id = t.id)`

const fixtureRepositoryIdentitySQL = `INSERT INTO repository_source_identities(repository_id) SELECT r.id FROM project_repositories r WHERE NOT EXISTS(SELECT 1 FROM repository_source_identities i WHERE i.repository_id = r.id)`
