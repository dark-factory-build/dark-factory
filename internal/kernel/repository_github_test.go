package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestRepositoryGitHubIdentityRequiresProofAndPinsOnce(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 208), Name: "github", Root: "/github"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	id := RepositoryID(project.ID)
	if err := store.BindRepositoryGitHubID(ctx, id, 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("unverified binding: %v", err)
	}
	proof := RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "team/repo"}
	if err := store.BindRepositorySource(ctx, id, proof); err != nil {
		t.Fatal(err)
	}
	if err := store.BindRepositoryGitHubID(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.BindRepositoryGitHubID(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.BindRepositoryGitHubID(ctx, id, 8); err == nil {
		t.Fatal("numeric identity replaced")
	}
	if err := store.BindRepositorySource(ctx, id, proof); err != nil {
		t.Fatal("ordinary proof recheck overwrote numeric binding")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actual, pinned, err := store.RepositoryGitHubID(ctx, id)
	if err != nil || !pinned || actual != 7 {
		t.Fatalf("restarted numeric binding: %d %v %v", actual, pinned, err)
	}
}
