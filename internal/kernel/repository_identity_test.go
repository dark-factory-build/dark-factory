package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestRepositoryBindingsRefuseCorruptionBeforeMutation(t *testing.T) {
	for name, statement := range map[string]string{
		"missing identity":        `DELETE FROM repository_source_identities`,
		"missing default":         `UPDATE project_repositories SET is_default = 0`,
		"disabled default":        `UPDATE project_repositories SET enabled = 0`,
		"missing content binding": `DELETE FROM content_repository_bindings`,
		"foreign content binding": `UPDATE content_repository_bindings SET repository_id = (SELECT id FROM project_repositories WHERE root = '/foreign')`,
		"missing task binding":    `DELETE FROM task_repository_bindings`,
		"foreign task binding":    `UPDATE task_repository_bindings SET repository_id = (SELECT id FROM project_repositories WHERE root = '/foreign')`,
		"invalid copied base":     `UPDATE task_repository_bindings SET base_ref = '-bad'`,
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestStore(t)
			defer store.Close()
			seedDurableAuthority(t, store)
			if _, err := store.CreateContent(context.Background(), contentSpec(t, projectID(t, 1), 203, "body"), mustTime(t, 19)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 200), Name: "foreign", Root: "/foreign"}, mustTime(t, 20)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.writer.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 201), Name: "next", Root: "/next"}, mustTime(t, 21)); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("mutation after corruption = %v", err)
			}
		})
	}
}

func TestRepositorySourceIdentityPinsOnceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 202), Name: "source", Root: "/source"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	identity := RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "team/repo"}
	if err := store.BindRepositorySource(ctx, RepositoryID(project.ID), identity); err != nil {
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
	if err := store.BindRepositorySource(ctx, RepositoryID(project.ID), identity); err != nil {
		t.Fatal(err)
	}
	identity.GitInode++
	if err := store.BindRepositorySource(ctx, RepositoryID(project.ID), identity); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement = %v", err)
	}
}

func TestV22MigrationKeepsExplicitUnverifiedSourceUntilHostProof(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 204), Name: "legacy", Root: "/legacy"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP TABLE run_tokens", "DROP TABLE project_tokens", "DROP TABLE attachment_retention", "DROP TABLE task_attachments", "DROP TABLE intake_acceptance_reviews", "DROP TABLE intake_source_priorities", "DROP TABLE intake_legacy_suppressions", "DROP TABLE intake_legacy_migrations", "DROP TABLE intake_task_bindings", "DROP TABLE intake_source_trusted_logins", "DROP TABLE intake_acceptances", "DROP TABLE intake_sources", "DROP TABLE repository_source_identities"} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.writer.Exec(`PRAGMA user_version = 22`); err != nil {
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
	if _, verified, err := store.RepositorySourceIdentity(ctx, RepositoryID(project.ID)); err != nil || verified {
		t.Fatalf("migration invented proof: %v, %v", verified, err)
	}
	if _, err := store.writer.Exec(`DELETE FROM repository_source_identities`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RepositorySourceIdentity(ctx, RepositoryID(project.ID)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("missing proof placeholder = %v", err)
	}
}

func TestRegisteredContentSourceMismatchRollsBack(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	identity := RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}}
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 205), Name: "source", Root: "/source", SourceIdentity: &identity}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	spec := contentSpec(t, project.ID, 206, "body")
	spec.RepositoryDevice, spec.RepositoryInode = 1, 99
	if _, err := store.CreateContent(ctx, spec, mustTime(t, 3)); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("foreign source = %v", err)
	}
	spec.RepositoryInode = 2
	if _, err := store.CreateContent(ctx, spec, mustTime(t, 3)); err != nil {
		t.Fatalf("correct source after refusal = %v", err)
	}
}
