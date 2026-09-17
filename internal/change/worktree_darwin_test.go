//go:build darwin

package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const testChangeID = "0123456789abcdef0123456789abcdef"

func TestWorktreeIsMadeAtTheExactSelectedCommitOnItsOwnBranch(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("new commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "add", "README.md")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-m", "move HEAD")
	newCommit := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	runFixtureGit(t, fixture.git, fixture.repository, "update-ref", "refs/heads/moving", fixture.base.Hex())
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "moving", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "update-ref", "refs/heads/moving", newCommit)
	runFixtureGit(t, fixture.git, fixture.repository, "gc", "--prune=now")

	path := filepath.Join(secureTempDir(t), testChangeID)
	branch := BranchName(testChangeID)
	facts, err := AddWorktree(context.Background(), selected, path, branch)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Head().equal(fixture.base) || facts.Branch() != branch || facts.Dirty() {
		t.Fatalf("worktree facts = %+v", facts)
	}
	assertExactTree(t, path, fixture)
	if got := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD")); got != newCommit {
		t.Fatalf("project checkout moved to %s", got)
	}
	// The worker edits, commits on its branch, and the facts follow it.
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	facts, err = InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path)
	if err != nil || !facts.Dirty() || !facts.Head().equal(fixture.base) {
		t.Fatalf("dirty facts = %+v, %v", facts, err)
	}
	runFixtureGit(t, fixture.git, path, "add", "work.txt")
	runFixtureGit(t, fixture.git, path, "commit", "-m", "work")
	head := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "refs/heads/"+branch))
	facts, err = InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path)
	if err != nil || facts.Dirty() || facts.Head().Hex() != head || facts.Branch() != branch {
		t.Fatalf("committed facts = %+v, %v", facts, err)
	}
	// A worktree at the path that is not a worktree of this repository, or
	// no worktree at all, is not the Change's.
	other := newLocalGitFixture(t, "sha1")
	if _, err := InspectWorktree(context.Background(), fixture.git, other.repository, other.identity, path); err == nil {
		t.Fatal("another repository's inspection accepted the worktree")
	}
	plain := filepath.Join(secureTempDir(t), "plain")
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	var invalid *ValidationError
	if _, err := InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, plain); !errors.As(err, &invalid) {
		t.Fatalf("plain directory inspection = %v", err)
	}
}

func TestPrivateWorktreeDoesNotWriteCanonicalGit(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	before := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname)=%(objectname)")
	path := filepath.Join(secureTempDir(t), testChangeID)
	facts, err := AddPrivateWorktree(context.Background(), selected, path, BranchName(testChangeID))
	if err != nil {
		t.Fatal(err)
	}
	wantGit := GitDirectoryForChange(fixture.repository, path)
	if facts.GitDirectory() != wantGit || !facts.Head().equal(fixture.base) || facts.Branch() != BranchName(testChangeID) {
		t.Fatalf("private facts = %+v, git directory %q", facts, facts.GitDirectory())
	}
	if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname)=%(objectname)"); got != before {
		t.Fatalf("canonical refs changed: before %q after %q", before, got)
	}
	if err := os.WriteFile(filepath.Join(path, "private.txt"), []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, path, "add", "private.txt")
	runFixtureGit(t, fixture.git, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "private")
	facts, err = InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path)
	if err != nil || facts.Dirty() || facts.GitDirectory() != wantGit || facts.Head().equal(fixture.base) {
		t.Fatalf("private committed facts = %+v, %v", facts, err)
	}
}

func TestPrivateWorktreeRejectsWorkerGitIndirection(t *testing.T) {
	for _, attack := range []string{"fsmonitor", "filter", "config-symlink", "config-fifo", "commondir"} {
		t.Run(attack, func(t *testing.T) {
			fixture := newLocalGitFixture(t, "sha1")
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(secureTempDir(t), testChangeID)
			if _, err := AddPrivateWorktree(context.Background(), selected, path, BranchName(testChangeID)); err != nil {
				t.Fatal(err)
			}
			admin := GitDirectoryForChange(fixture.repository, path)
			config := filepath.Join(admin, "config")
			switch attack {
			case "fsmonitor", "filter":
				key := "core.fsmonitor"
				if attack == "filter" {
					key = "filter.hostile.clean"
				}
				runFixtureGit(t, fixture.git, path, "config", key, "/bin/false")
			case "config-symlink", "config-fifo":
				if err := os.Rename(config, config+".held"); err != nil {
					t.Fatal(err)
				}
				if attack == "config-symlink" {
					if err := os.Symlink(config+".held", config); err != nil {
						t.Fatal(err)
					}
				} else if err := unix.Mkfifo(config, 0600); err != nil {
					t.Fatal(err)
				}
			case "commondir":
				registration, err := privateGitfileTarget(filepath.Join(path, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(registration, "commondir"), []byte(filepath.Join(fixture.repository, ".git")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path); err == nil {
				t.Fatal("worker Git indirection accepted")
			}
		})
	}
}

func TestPrivateWorktreeReplacesOnlyAStaleCleanRegistration(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secureTempDir(t), testChangeID)
	branch := BranchName(testChangeID)
	if _, err := AddPrivateWorktree(context.Background(), selected, path, branch); err != nil {
		t.Fatal(err)
	}
	registration, err := privateGitfileTarget(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := AddPrivateWorktree(context.Background(), selected, path, branch); err != nil {
		t.Fatalf("clean stale registration was not replaced: %v", err)
	}
	if got, err := privateGitfileTarget(filepath.Join(path, ".git")); err != nil || got != registration {
		t.Fatalf("new worktree registration = %q, %v; want %q", got, err, registration)
	}
}

func TestPrivateWorktreeRefusesStaleRegistrationWithStagedWork(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secureTempDir(t), testChangeID)
	branch := BranchName(testChangeID)
	if _, err := AddPrivateWorktree(context.Background(), selected, path, branch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, path, "add", "staged.txt")
	registration, err := privateGitfileTarget(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(registration, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := AddPrivateWorktree(context.Background(), selected, path, branch); err == nil {
		t.Fatal("staged stale registration was removed")
	}
	indexAfter, err := os.ReadFile(filepath.Join(registration, "index"))
	if err != nil || !bytes.Equal(indexAfter, indexBefore) {
		t.Fatalf("staged index changed: err=%v", err)
	}
	if _, err := os.Stat(registration); err != nil {
		t.Fatalf("staged registration removed: %v", err)
	}
}

// An attempt that made its worktree but never ran a provider leaves the
// branch at the base; the next attempt remakes it. Anything with work in it
// is refused, never replaced.
func TestAddWorktreeReplacesOnlyAnUnusedLeftover(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	parent := secureTempDir(t)
	path := filepath.Join(parent, testChangeID)
	branch := BranchName(testChangeID)
	if _, err := AddWorktree(context.Background(), selected, path, branch); err != nil {
		t.Fatal(err)
	}
	t.Run("pristine worktree", func(t *testing.T) {
		if _, err := AddWorktree(context.Background(), selected, path, branch); err != nil {
			t.Fatalf("pristine leftover was not remade: %v", err)
		}
		assertExactTree(t, path, fixture)
	})
	t.Run("deleted directory with stale registration", func(t *testing.T) {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if _, err := AddWorktree(context.Background(), selected, path, branch); err != nil {
			t.Fatalf("stale registration was not remade: %v", err)
		}
		assertExactTree(t, path, fixture)
	})
	t.Run("dirty worktree", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(path, "left.txt"), []byte("left\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var invalid *ValidationError
		if _, err := AddWorktree(context.Background(), selected, path, branch); !errors.As(err, &invalid) {
			t.Fatalf("dirty worktree was replaced: %v", err)
		}
		if err := os.Remove(filepath.Join(path, "left.txt")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("branch with commits", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, fixture.git, path, "add", "work.txt")
		runFixtureGit(t, fixture.git, path, "commit", "-m", "work")
		var invalid *ValidationError
		if _, err := AddWorktree(context.Background(), selected, path, branch); !errors.As(err, &invalid) {
			t.Fatalf("branch with work was replaced: %v", err)
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if _, err := AddWorktree(context.Background(), selected, path, branch); !errors.As(err, &invalid) {
			t.Fatalf("branch with work was replaced after its directory went: %v", err)
		}
		if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "--verify", "refs/heads/"+branch); got == "" {
			t.Fatal("branch with work was deleted")
		}
	})
	t.Run("foreign directory", func(t *testing.T) {
		foreign := filepath.Join(parent, "fedcba9876543210fedcba9876543210")
		if err := os.Mkdir(foreign, 0o700); err != nil {
			t.Fatal(err)
		}
		var invalid *ValidationError
		if _, err := AddWorktree(context.Background(), selected, foreign, BranchName(filepath.Base(foreign))); !errors.As(err, &invalid) {
			t.Fatalf("foreign directory was taken: %v", err)
		}
	})
}

func TestConcurrentWorktreesOfOneRepositoryAllSucceed(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	parent := secureTempDir(t)
	results := make(chan error, 8)
	for index := range cap(results) {
		go func() {
			changeID := strings.Repeat(fmt.Sprintf("%x", index+1), 32)
			_, err := AddWorktree(context.Background(), selected, filepath.Join(parent, changeID), BranchName(changeID))
			results <- err
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatalf("concurrent worktree: %v", err)
		}
	}
	for index := range cap(results) {
		changeID := strings.Repeat(fmt.Sprintf("%x", index+1), 32)
		facts, err := InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, filepath.Join(parent, changeID))
		if err != nil || facts.Branch() != BranchName(changeID) || !facts.Head().equal(fixture.base) {
			t.Fatalf("worktree %d facts = %+v, %v", index, facts, err)
		}
	}
}

// A Change from before managed worktrees is a plain copy of the base with
// the worker's edits in it. Adoption keeps every byte and makes the edits
// the uncommitted work of the Change's branch at the base, at any step of
// a crash, and never twice.
func TestAdoptWorktreeKeepsAGitFreeTreeAndItsEditsAtTheExactBase(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	parent := secureTempDir(t)
	path := filepath.Join(parent, testChangeID)
	branch := BranchName(testChangeID)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range fixture.files {
		target := filepath.Join(path, string(file.path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if file.mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(target, file.data, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("edited by the worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A crash after the staging worktree was minted leaves it behind.
	runFixtureGit(t, fixture.git, fixture.repository, "worktree", "add", "--no-checkout", "-b", branch, path+".adopt", fixture.base.Hex())
	facts, err := AdoptWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Head().equal(fixture.base) || facts.Branch() != branch || !facts.Dirty() {
		t.Fatalf("adopted facts = %+v", facts)
	}
	if _, err := os.Lstat(path + ".adopt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging worktree survived adoption: %v", err)
	}
	status := runFixtureGitOutput(t, fixture.git, path, "status", "--porcelain", "--untracked-files=all")
	if status != " M README.md\n?? untracked.txt\n" {
		t.Fatalf("adopted status = %q", status)
	}
	if readme, err := os.ReadFile(filepath.Join(path, "README.md")); err != nil || string(readme) != "edited by the worker\n" {
		t.Fatalf("adoption changed the worker's edit: %q %v", readme, err)
	}
	again, err := AdoptWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, branch, fixture.base)
	if err != nil || again != facts {
		t.Fatalf("second adoption = %+v, %v", again, err)
	}
	var invalid *ValidationError
	if _, err := AdoptWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path, BranchName("fedcba9876543210fedcba9876543210"), fixture.base); !errors.As(err, &invalid) {
		t.Fatalf("adoption under another branch = %v", err)
	}
	// The worker's later commit moves the head; the base is still where it was.
	runFixtureGit(t, fixture.git, path, "add", "-A")
	runFixtureGit(t, fixture.git, path, "commit", "-m", "worker edits")
	facts, err = InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, path)
	if err != nil || facts.Dirty() || facts.Head().equal(fixture.base) {
		t.Fatalf("committed adopted facts = %+v, %v", facts, err)
	}
	if got := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, path, "rev-parse", "HEAD~1")); got != fixture.base.Hex() {
		t.Fatalf("adopted commit parent = %s, want base %s", got, fixture.base.Hex())
	}
}

func TestWorktreeInputsAreValidated(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	var invalid *ValidationError
	for _, branch := range []string{"", "main", "factory/", "factory/UPPER", "factory/a//b", "refs/heads/factory/x"} {
		if _, err := AddWorktree(context.Background(), selected, filepath.Join(secureTempDir(t), "w"), branch); !errors.As(err, &invalid) {
			t.Fatalf("branch %q accepted: %v", branch, err)
		}
	}
	for _, path := range []string{"relative", "/", filepath.Join(secureTempDir(t), ".git"), secureTempDir(t) + "/x/../y"} {
		if _, err := AddWorktree(context.Background(), selected, path, BranchName(testChangeID)); !errors.As(err, &invalid) {
			t.Fatalf("path %q accepted: %v", path, err)
		}
	}
	if got := BranchName(testChangeID); got != "factory/0123456789ab" {
		t.Fatalf("branch name = %s", got)
	}
}
