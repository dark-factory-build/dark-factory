//go:build darwin

package change_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
)

func TestPublicSelectGitFortyCallsHaveExactZeroFDDeltaWithoutGC(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	git := "/usr/bin/git"
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

func runExternalGit(t testing.TB, home, git, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", directory}, arguments...)...)
	command.Env = []string{
		"HOME=" + home, "TMPDIR=" + home, "LC_ALL=C", "LANG=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git failed: %v: %s", err, output)
	}
}

func externalFDCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
