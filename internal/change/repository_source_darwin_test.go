//go:build darwin

package change

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisteredSourceRejectsCheckoutAndOriginReplacement(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{"root", "git", "origin"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newLocalGitFixture(t, "sha1")
			source, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SelectRegisteredGit(ctx, fixture.git, fixture.repository, "HEAD", source); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "root":
				if err := os.Rename(fixture.repository, fixture.repository+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.repository, 0700); err != nil {
					t.Fatal(err)
				}
			case "git":
				replacement := newLocalGitFixture(t, "sha1")
				old := filepath.Join(fixture.repository, ".git")
				if err := os.Rename(old, old+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(replacement.repository, ".git"), old); err != nil {
					t.Fatal(err)
				}
			case "origin":
				runFixtureGit(t, fixture.git, fixture.repository, "config", "remote.origin.url", "https://github.com/other/repository.git")
			}
			if _, err := SelectRegisteredGit(ctx, fixture.git, fixture.repository, "HEAD", source); err == nil {
				t.Fatal("replaced registered source selected")
			}
		})
	}
}

func TestRegistrationProvesBaseAndPublicationTargetWithoutFetch(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	runFixtureGit(t, fixture.git, fixture.repository, "config", "remote.origin.url", "https://github.com/Team/Repo.git")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "remote.origin.pushurl", "git@github.com:team/repo.git")
	source, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil || source.PublicationRepository != "team/repo" {
		t.Fatalf("source = %+v, %v", source, err)
	}
	if _, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "missing-base", fixture.identity); err == nil {
		t.Fatal("missing base registered")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "remote.origin.pushurl", "git@github.com:other/repo.git")
	if _, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("unrelated publication target registered")
	}
}
