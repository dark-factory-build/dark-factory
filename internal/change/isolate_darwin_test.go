//go:build darwin

package change

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolateWorktreePreservesRetainedState(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secureTempDir(t), testChangeID)
	branch := BranchName(testChangeID)
	if _, err := AddWorktree(context.Background(), selected, path, branch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, path, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged plus unstaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("unstaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("untracked"), 0600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, path, "update-index", "--split-index")
	oldIndex := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "rev-parse", "--git-path", "index"))
	originalGitfile, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "rev-parse", "HEAD^{tree}"))
	dangling := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-p", fixture.base.Hex(), "-m", "operation-only commit"))
	for _, name := range []string{"ORIG_HEAD", "MERGE_HEAD"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(oldIndex), name), []byte(dangling+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	indexBefore, err := os.ReadFile(oldIndex)
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname)=%(objectname)")
	stagedBefore := runFixtureGitOutput(t, fixture.git, path, "diff", "--cached", "--binary")
	unstagedBefore := runFixtureGitOutput(t, fixture.git, path, "diff", "--binary")
	// A real Git writer's existing lock must be left intact and source usable.
	if err := os.WriteFile(oldIndex+".lock", []byte("another writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base); err == nil {
		t.Fatal("active Git lock was accepted")
	}
	if data, err := os.ReadFile(oldIndex + ".lock"); err != nil || string(data) != "another writer" {
		t.Fatal("another writer's lock was changed")
	}
	if err := os.Remove(oldIndex + ".lock"); err != nil {
		t.Fatal(err)
	}
	facts, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Branch() != branch || !facts.Head().equal(fixture.base) || facts.GitDirectory() != GitDirectoryForChange(fixture.repository, path) {
		t.Fatalf("facts=%+v", facts)
	}
	runFixtureGit(t, fixture.git, path, "cat-file", "-e", dangling+"^{commit}")
	indexAfter, err := os.ReadFile(strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "rev-parse", "--git-path", "index")))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("index bytes changed")
	}
	if runFixtureGitOutput(t, fixture.git, path, "diff", "--cached", "--name-only") != "staged.txt\n" {
		t.Fatal("staged state changed")
	}
	if runFixtureGitOutput(t, fixture.git, path, "diff", "--binary") != unstagedBefore {
		t.Fatal("unstaged state changed")
	}
	if runFixtureGitOutput(t, fixture.git, path, "diff", "--cached", "--binary") != stagedBefore {
		t.Fatal("staged bytes changed")
	}
	if runFixtureGitOutput(t, fixture.git, path, "ls-files", "--others", "--exclude-standard") != "untracked.txt\n" {
		t.Fatal("untracked state changed")
	}
	if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname)=%(objectname)"); got != refsBefore {
		t.Fatal("canonical refs changed")
	}
	// A death just after pointer replacement leaves marked legacy Git locks.
	old := filepath.Dir(oldIndex)
	legacyRefLock := filepath.Join(fixture.repository, ".git", "refs", "heads", branch) + ".lock"
	for _, lock := range []string{oldIndex + ".lock", legacyRefLock} {
		if err := os.WriteFile(lock, []byte("dark-factory-isolation "+old+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	originPath := filepath.Join(filepath.Dir(facts.GitDirectory()), "isolation-origin")
	if err := os.WriteFile(originPath, []byte(old+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base); err == nil {
		t.Fatal("malformed recovery provenance accepted")
	}
	for _, lock := range []string{oldIndex + ".lock", legacyRefLock} {
		if _, err := os.Stat(lock); err != nil {
			t.Fatal("malformed provenance removed a recovery lock")
		}
	}
	if err := os.WriteFile(originPath, originalGitfile, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base); err != nil {
		t.Fatalf("completed migration retry: %v", err)
	}
	for _, lock := range []string{oldIndex + ".lock", legacyRefLock} {
		if _, err := os.Lstat(lock); !os.IsNotExist(err) {
			t.Fatalf("recovered migration lock remains: %s", lock)
		}
	}
	// Model death after preparation, before pointer replacement. A metadata
	// change without a HEAD/index change must not resurrect stale merge state.
	if err := os.WriteFile(filepath.Join(path, ".git"), originalGitfile, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "ORIG_HEAD"), []byte(fixture.base.Hex()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base); err == nil {
		t.Fatal("changed operation state accepted on preparation retry")
	}
	if got, err := os.ReadFile(filepath.Join(path, ".git")); err != nil || string(got) != string(originalGitfile) {
		t.Fatal("refused migration changed the source pointer")
	}
	if err := os.WriteFile(filepath.Join(old, "ORIG_HEAD"), []byte(dangling+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base); err != nil {
		t.Fatalf("unchanged preparation retry: %v", err)
	}
	runFixtureGit(t, fixture.git, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "private staged state")
	if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname)=%(objectname)"); got != refsBefore {
		t.Fatal("private commit changed canonical refs")
	}
	if oldBytes, err := os.ReadFile(oldIndex); err != nil || string(oldBytes) != string(indexBefore) {
		t.Fatal("legacy index was changed")
	}
	// The existing publisher consumes the receipt's Git directory directly;
	// private commits need no import into the canonical project's refs.
	publication := filepath.Join(secureTempDir(t), "publication.git")
	runFixtureGit(t, fixture.git, fixture.repository, "init", "--bare", publication)
	privateHead := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "rev-parse", "HEAD"))
	runFixtureGit(t, fixture.git, fixture.repository, "--git-dir", publication, "fetch", facts.GitDirectory(), privateHead)
	if got := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "--git-dir", publication, "rev-parse", "FETCH_HEAD")); got != privateHead {
		t.Fatal("publication did not read the exact private head")
	}
}
