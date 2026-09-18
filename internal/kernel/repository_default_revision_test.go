package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSetProjectRepositoryDefaultRevisesOutgoingRepository(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 240), Name: "defaults", Root: filepath.Join(t.TempDir(), "first")}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	first, found, err := store.ProjectRepository(ctx, RepositoryID(project.ID))
	if err != nil || !found || !first.Default {
		t.Fatalf("initial default = %+v, %v, %v", first, found, err)
	}
	second, err := store.AddProjectRepository(ctx, NewProjectRepository{ID: repositoryID(t, 241), ProjectID: project.ID, Name: "second", Root: filepath.Join(t.TempDir(), "second"), BaseRef: "HEAD"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	second, err = store.SetProjectRepositoryDefault(ctx, second.ID, second.Revision, mustTime(t, 3))
	if err != nil || !second.Default || second.Revision.Int64() != 2 {
		t.Fatalf("switch to second = %+v, %v", second, err)
	}
	firstAfter, found, err := store.ProjectRepository(ctx, first.ID)
	if err != nil || !found || firstAfter.Default || firstAfter.Revision.Int64() != 2 {
		t.Fatalf("outgoing default = %+v, %v, %v", firstAfter, found, err)
	}
	second, err = store.SetProjectRepositoryDefault(ctx, second.ID, second.Revision, mustTime(t, 4))
	if err != nil || second.Revision.Int64() != 3 {
		t.Fatalf("repeat default = %+v, %v", second, err)
	}
	firstAfter, found, err = store.ProjectRepository(ctx, first.ID)
	if err != nil || !found || firstAfter.Revision.Int64() != 2 {
		t.Fatalf("repeat changed outgoing default = %+v, %v, %v", firstAfter, found, err)
	}
	if _, err := store.SetProjectRepositoryDefault(ctx, first.ID, first.Revision, mustTime(t, 5)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale first default = %v", err)
	}
	firstAfter, err = store.SetProjectRepositoryDefault(ctx, first.ID, firstAfter.Revision, mustTime(t, 6))
	if err != nil || !firstAfter.Default || firstAfter.Revision.Int64() != 3 {
		t.Fatalf("fresh first default = %+v, %v", firstAfter, err)
	}
	secondAfter, found, err := store.ProjectRepository(ctx, second.ID)
	if err != nil || !found || secondAfter.Default || secondAfter.Revision.Int64() != 4 {
		t.Fatalf("final outgoing default = %+v, %v, %v", secondAfter, found, err)
	}
}
