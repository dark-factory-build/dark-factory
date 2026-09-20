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

func TestV24MigrationPreservesSourceProofWithoutInventingGitHubID(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	proof := RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "team/repo"}
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 209), Name: "migration", Root: "/migration", SourceIdentity: &proof}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP TABLE run_tokens", "DROP TABLE project_tokens", "DROP TABLE attachment_retention", "DROP TABLE task_attachments", "DROP TABLE intake_acceptance_reviews", "DROP TABLE intake_source_priorities", "DROP TABLE intake_legacy_suppressions", "DROP TABLE intake_legacy_migrations"} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	old := expectedSchemaOf(v24SchemaStatements())["repository_source_identities"].sql
	columns := "repository_id, " + repositorySourceColumns
	for _, statement := range []string{
		"CREATE TABLE saved_identity AS SELECT " + columns + " FROM repository_source_identities",
		"DROP TABLE repository_source_identities", old,
		"INSERT INTO repository_source_identities(" + columns + ") SELECT " + columns + " FROM saved_identity",
		"DROP TABLE saved_identity", "PRAGMA user_version = 24",
	} {
		if _, err := store.writer.Exec(statement); err != nil {
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
	defer store.Close()
	actual, verified, err := store.RepositorySourceIdentity(ctx, RepositoryID(project.ID))
	if err != nil || !verified || actual != proof {
		t.Fatalf("migrated proof: %+v %v %v", actual, verified, err)
	}
	if _, pinned, err := store.RepositoryGitHubID(ctx, RepositoryID(project.ID)); err != nil || pinned {
		t.Fatalf("migration invented numeric identity: %v %v", pinned, err)
	}
}
