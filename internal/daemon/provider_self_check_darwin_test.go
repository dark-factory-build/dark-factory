//go:build darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
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
	worktree := filepath.Join(root, "change")
	supervisorGit(t, git, "-C", repository, "worktree", "add", "-b", "factory/self-check", worktree, base)
	if err := os.WriteFile(filepath.Join(worktree, providerSelfCheckFile), []byte("provider self-check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", worktree, "add", providerSelfCheckFile)
	supervisorGit(t, git, "-C", worktree, "commit", "-m", providerSelfCheckCommit)
	commit, err := verifyProviderSelfCheckCommit(context.Background(), git, worktree, base)
	if err != nil || commit == "" {
		t.Fatalf("verifyProviderSelfCheckCommit = %q, %v", commit, err)
	}
}

func TestRunProviderSelfCheckUsesRunIsolated(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     string
		tools    bool
		passed   bool
		evidence string
	}{
		{name: "success", tools: true, passed: true},
		{name: "launch failure", passed: false},
		{name: "checkout-read failure", mode: "checkout-read-failure", tools: true, evidence: "checkout"},
		{name: "commit failure", mode: "commit-failure", tools: true, evidence: "commit"},
		{name: "attempt API failure", mode: "attempt-api-failure", tools: true, evidence: "attempt"},
		{name: "bounded edit failure", mode: "bounded-edit-failure", tools: true, evidence: "bounded"},
		{name: "output larger than pipe capacity", mode: "noisy", tools: true, passed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.MkdirTemp(runner.ShortTempDir(), "provider-self-check-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			git := supervisorNativeGit(t)
			repository := filepath.Join(root, "repository")
			supervisorGit(t, git, "init", repository)
			supervisorGit(t, git, "-C", repository, "config", "user.email", "test@example.invalid")
			supervisorGit(t, git, "-C", repository, "config", "user.name", "test")
			if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			supervisorGit(t, git, "-C", repository, "add", "payload.txt")
			if test.mode != "" {
				if err := os.WriteFile(filepath.Join(repository, ".provider-self-check-mode"), []byte(test.mode+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				supervisorGit(t, git, "-C", repository, "add", ".provider-self-check-mode")
			}
			supervisorGit(t, git, "-C", repository, "commit", "-m", "base")
			base := strings.TrimSpace(supervisorGitOutput(t, git, "-C", repository, "rev-parse", "HEAD"))
			executable := supervisorTestExecutable(t)
			factoryctl, err := filepath.EvalSymlinks(executable)
			if err != nil {
				t.Fatal(err)
			}
			toolPath := filepath.Join(runtime.GOROOT(), "bin") + ":/usr/bin:/bin"
			if test.tools {
				tools := filepath.Join(root, "tools")
				if err := os.Mkdir(tools, 0o700); err != nil {
					t.Fatal(err)
				}
				copySupervisorExecutable(t, executable, filepath.Join(tools, "claude"))
				toolPath = tools + ":" + toolPath
			}
			result := RunProviderSelfCheck(context.Background(), SupervisorSpec{
				GitExecutable: git, BaseRevision: base, RunnerExecutable: executable, FactoryctlExecutable: factoryctl,
				ToolPath: toolPath, AccountHome: root,
			}, repository, kernel.ProviderClaudeCode)
			if result.Passed != test.passed || !test.passed && test.evidence != "" && !strings.Contains(result.Detail, test.evidence) {
				t.Fatalf("self-check = %+v", result)
			}
		})
	}
}
