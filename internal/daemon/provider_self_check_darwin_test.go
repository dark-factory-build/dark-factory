//go:build darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyProviderSelfCheckCommitRequiresOneExactFile(t *testing.T) {
	root := t.TempDir()
	git := supervisorNativeGit(t)
	repository := filepath.Join(root, "repository")
	supervisorGit(t, git, "init", repository)
	supervisorGit(t, git, "-C", repository, "config", "user.email", "test@example.invalid")
	supervisorGit(t, git, "-C", repository, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", repository, "add", "payload.txt")
	supervisorGit(t, git, "-C", repository, "commit", "-m", "base")
	base := strings.TrimSpace(supervisorGitOutput(t, git, "-C", repository, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repository, providerSelfCheckFile), []byte("provider self-check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", repository, "add", providerSelfCheckFile)
	supervisorGit(t, git, "-C", repository, "commit", "-m", providerSelfCheckCommit)
	branch := strings.TrimSpace(supervisorGitOutput(t, git, "-C", repository, "symbolic-ref", "--short", "HEAD"))
	supervisorGit(t, git, "-C", repository, "checkout", "--detach", "HEAD^")
	supervisorGit(t, git, "-C", repository, "update-ref", "-d", "refs/heads/"+branch)
	commit, err := verifyProviderSelfCheckCommit(context.Background(), git, repository, base)
	if err != nil || commit == "" {
		t.Fatalf("verifyProviderSelfCheckCommit = %q, %v", commit, err)
	}
}
