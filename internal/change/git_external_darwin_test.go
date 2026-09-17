//go:build darwin

package change_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
)

func TestPublicSelectGitFortyCallsHaveExactZeroFDDeltaWithoutGC(t *testing.T) {
	root := externalSecureTempDir(t)
	repository := filepath.Join(root, "repository")
	git := externalGitExecutable(t)
	runExternalGit(t, root, git, root, "init", repository)
	runExternalGit(t, root, git, repository, "config", "user.name", "Dark Factory Test")
	runExternalGit(t, root, git, repository, "config", "user.email", "test@invalid")
	if err := os.WriteFile(filepath.Join(repository, "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	runExternalGit(t, root, git, repository, "add", "file")
	runExternalGit(t, root, git, repository, "commit", "-m", "fixture")
	info, err := os.Lstat(repository)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("repository identity unavailable")
	}
	identity, err := change.NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
	if err != nil {
		t.Fatal(err)
	}
	previousGC := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previousGC) })
	before := externalFDCount(t)
	for range 40 {
		if _, err := change.SelectGit(context.Background(), git, repository, "HEAD", identity); err != nil {
			t.Fatal(err)
		}
	}
	if after := externalFDCount(t); after != before {
		t.Fatalf("public SelectGit leaked descriptors without finalizers: before=%d after=%d", before, after)
	}
}

func TestPinContentSourceKeepsAnExactFileReachableAfterBranchCleanup(t *testing.T) {
	root := externalSecureTempDir(t)
	repository := filepath.Join(root, "repository")
	git := externalGitExecutable(t)
	runExternalGit(t, root, git, root, "init", repository)
	runExternalGit(t, root, git, repository, "config", "user.name", "Dark Factory Test")
	runExternalGit(t, root, git, repository, "config", "user.email", "test@invalid")
	if err := os.WriteFile(filepath.Join(repository, "procedure.md"), []byte("# release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runExternalGit(t, root, git, repository, "add", "procedure.md")
	runExternalGit(t, root, git, repository, "commit", "-m", "procedure")
	commit := runExternalGit(t, root, git, repository, "rev-parse", "HEAD")
	branch := runExternalGit(t, root, git, repository, "branch", "--show-current")
	info, err := os.Lstat(repository)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("repository identity unavailable")
	}
	identity, err := change.NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repository, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(git, "-C", repository, "update-ref", "refs/test/hook-refusal", commit)
	command.Env = []string{"HOME=" + root, "TMPDIR=" + root, "LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	if output, runErr := command.CombinedOutput(); runErr == nil {
		t.Fatalf("reference-transaction refusal hook did not reject ordinary update-ref: %s", output)
	}
	ref := "refs/dark-factory/content/00112233445566778899aabbccddeeff/1"
	source, err := change.PinContentSource(context.Background(), git, repository, identity, commit, "procedure.md", ref)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := change.ReadContentSource(context.Background(), git, repository, identity, source); err != nil || body != "# release\n" {
		t.Fatalf("pinned body = %q, %v", body, err)
	}
	generated, err := change.WriteContentSource(context.Background(), git, repository, identity, nil, ".dark-factory/content/00112233445566778899aabbccddeeff.md", "# generated\n", "refs/dark-factory/content/00112233445566778899aabbccddeeff/2")
	if err != nil {
		t.Fatal(err)
	}
	if body, err := change.ReadContentSource(context.Background(), git, repository, identity, generated); err != nil || body != "# generated\n" {
		t.Fatalf("generated body = %q, %v", body, err)
	}
	replayed, err := change.WriteContentSource(context.Background(), git, repository, identity, nil, generated.Path, "# generated\n", "refs/dark-factory/content/00112233445566778899aabbccddeeff/2")
	if err != nil || replayed.Commit.Hex() != generated.Commit.Hex() {
		t.Fatalf("generated replay = %s, %v", replayed.Commit.Hex(), err)
	}
	if _, err := change.WriteContentSource(context.Background(), git, repository, identity, nil, generated.Path, "different\n", "refs/dark-factory/content/00112233445566778899aabbccddeeff/2"); err == nil {
		t.Fatal("different body reused immutable content ref")
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if status := runExternalGit(t, root, git, repository, "status", "--porcelain"); status != "" {
		t.Fatalf("content writer changed worktree: %q", status)
	}
	if head := runExternalGit(t, root, git, repository, "rev-parse", "HEAD"); head != commit {
		t.Fatalf("content writer changed HEAD: %s != %s", head, commit)
	}
	runExternalGit(t, root, git, repository, "checkout", "--orphan", "replacement")
	runExternalGit(t, root, git, repository, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(repository, "replacement.md"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runExternalGit(t, root, git, repository, "add", "replacement.md")
	runExternalGit(t, root, git, repository, "commit", "-m", "replacement")
	runExternalGit(t, root, git, repository, "branch", "-D", branch)
	runExternalGit(t, root, git, repository, "gc", "--prune=now")
	if body, err := change.ReadContentSource(context.Background(), git, repository, identity, source); err != nil || body != "# release\n" {
		t.Fatalf("body after branch cleanup = %q, %v", body, err)
	}
}

func externalSecureTempDir(t testing.TB) string {
	t.Helper()
	path, err := os.MkdirTemp("/private/tmp", "dark-factory-change-external-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func externalGitExecutable(t testing.TB) string {
	t.Helper()
	if _, err := os.Stat(change.TrustedGitExecutable); err != nil {
		t.Fatalf("Command Line Tools Git is unavailable: %v", err)
	}
	return change.TrustedGitExecutable
}

func runExternalGit(t testing.TB, home, git, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", directory}, arguments...)...)
	command.Env = []string{
		"HOME=" + home, "TMPDIR=" + home, "LC_ALL=C", "LANG=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git failed: %v: %s", err, output)
	} else {
		return strings.TrimSpace(string(output))
	}
	return ""
}

func externalFDCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
