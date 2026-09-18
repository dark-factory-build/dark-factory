//go:build darwin

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func readinessProject(t *testing.T, root, base string) (*dispatchFixture, kernel.RepositoryID) {
	t.Helper()
	fixture := newDispatchFixture(t)
	id := mustProjectID(t, testID(229))
	at, _ := kernel.NewUnixMillis(200)
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: id, Name: "fetch readiness", Root: root}, at); err != nil {
		t.Fatal(err)
	}
	repository := kernel.RepositoryID(id)
	revision, _ := kernel.NewRevision(1)
	if _, err := fixture.store.UpdateProjectRepositoryBase(context.Background(), repository, revision, base, at); err != nil {
		t.Fatal(err)
	}
	return fixture, repository
}

func TestRepositoryReadinessFetchesExactNondefaultBranchWithoutChangingCheckout(t *testing.T) {
	ctx := context.Background()
	git := change.TrustedGitExecutable
	seed := contentRepositoryFixture(t)
	parent, err := os.MkdirTemp("/private/tmp", "dark-factory-readiness-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	bare, root := filepath.Join(parent, "remote.git"), filepath.Join(parent, "checkout")
	supervisorGit(t, git, "clone", "--bare", seed, bare)
	supervisorGit(t, git, "-C", seed, "checkout", "-b", "release")
	supervisorGit(t, git, "-C", seed, "push", bare, "release")
	supervisorGit(t, git, "clone", bare, root)
	supervisorGit(t, git, "-C", root, "config", "protocol.file.allow", "always")
	fixture, id := readinessProject(t, root, "refs/remotes/origin/release")
	view, err := fixture.daemon.RepositoryReadiness(ctx, id, false)
	if err != nil || view.FetchState != "unchecked" || view.PublicationState != "unbound" {
		t.Fatalf("listing: %+v %v", view, err)
	}
	if _, verified, err := fixture.store.RepositorySourceIdentity(ctx, id); err != nil || verified {
		t.Fatalf("listing established identity: %v %v", verified, err)
	}
	head := supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD")
	tracking := supervisorGitOutput(t, git, "-C", root, "rev-parse", "refs/remotes/origin/release")
	if err := os.WriteFile(filepath.Join(seed, "release.txt"), []byte("release source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", seed, "add", "release.txt")
	supervisorGit(t, git, "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@invalid", "commit", "-m", "release advances")
	supervisorGit(t, git, "-C", seed, "push", bare, "release")
	wanted := strings.TrimSpace(supervisorGitOutput(t, git, "-C", seed, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("operator edits\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status := supervisorGitOutput(t, git, "-C", root, "status", "--porcelain")
	before := exec.Command(git, "-C", root, "cat-file", "-e", wanted+"^{commit}")
	if err := before.Run(); err == nil {
		t.Fatal("fixture already has remote advancement")
	}
	view, err = fixture.daemon.RepositoryReadiness(ctx, id, true)
	if err != nil || view.FetchState != "ready" {
		t.Fatalf("fetch readiness: %+v %v", view, err)
	}
	supervisorGit(t, git, "-C", root, "cat-file", "-e", wanted+"^{commit}")
	if got := supervisorGitOutput(t, git, "-C", root, "rev-parse", "HEAD"); got != head {
		t.Fatal("fetch moved checkout")
	}
	if got := supervisorGitOutput(t, git, "-C", root, "rev-parse", "refs/remotes/origin/release"); got != tracking {
		t.Fatal("fetch moved shared tracking ref")
	}
	if got := supervisorGitOutput(t, git, "-C", root, "status", "--porcelain"); got != status {
		t.Fatal("fetch altered operator work")
	}
	if _, verified, err := fixture.store.RepositorySourceIdentity(ctx, id); err != nil || !verified {
		t.Fatalf("explicit first claim: %v %v", verified, err)
	}
	supervisorGit(t, git, "-C", root, "remote", "set-url", "origin", filepath.Join(parent, "replacement.git"))
	view, err = fixture.daemon.RepositoryReadiness(ctx, id, true)
	if err != nil || view.FetchState != "setup_required" {
		t.Fatalf("changed origin accepted: %+v %v", view, err)
	}
}

func TestRepositoryReadinessPrivateRemoteDenialNeverInheritsCredentialsOrStderr(t *testing.T) {
	var requests atomic.Int32
	var authenticated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			authenticated.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("PRIVATE_GIT_STDERR"))
	}))
	defer server.Close()
	root := contentRepositoryFixture(t)
	git := change.TrustedGitExecutable
	supervisorGit(t, git, "-C", root, "remote", "add", "origin", server.URL+"/private.git")
	supervisorGit(t, git, "-C", root, "update-ref", "refs/remotes/origin/release", "HEAD")
	fixture, id := readinessProject(t, root, "refs/remotes/origin/release")
	t.Setenv("GH_TOKEN", "PRIVATE_TEST_CREDENTIAL")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: Bearer PRIVATE_TEST_CREDENTIAL")
	view, err := fixture.daemon.RepositoryReadiness(context.Background(), id, true)
	if err != nil || view.FetchState != "setup_required" || view.ReadinessMessage == "" {
		t.Fatalf("denial: %+v %v", view, err)
	}
	if requests.Load() == 0 || authenticated.Load() {
		t.Fatalf("request authority: requests=%d authenticated=%v", requests.Load(), authenticated.Load())
	}
	if strings.Contains(view.ReadinessMessage, "PRIVATE_") || strings.Contains(view.ReadinessMessage, server.URL) {
		t.Fatal("private Git detail escaped")
	}
}
