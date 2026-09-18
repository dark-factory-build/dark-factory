//go:build darwin

package daemon

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// A retained worktree from before private administration must remain usable
// without an implicit migration. This fixture moves the test-owned worktree
// back to canonical Git administration, then retries it through the real
// supervisor and source receipt path.
func TestSupervisorRetainedCanonicalGitRetryPreservesWorktree(t *testing.T) {
	// Shell tasks are programs and the existing supervisor retry fixture uses
	// their deliberate provider-exit outcome; this test makes no typed API-
	// outcome claim. The marker after Git inspection proves this retry reached
	// the provider with canonical Git access.
	program := "set -eu\nif [ ! -s __WITNESS__ ]; then printf committed > committed.txt; git add committed.txt; git commit -q -m committed; printf edited > edited.txt; printf x >> __WITNESS__; exit 0; fi\nprintf second > second-marker.txt\nprintf x >> __WITNESS__\ngit rev-parse --path-format=absolute --git-common-dir > canonical-git-directory.txt\nprintf ok > canonical-git-ok.txt\nexit 0\n"
	fixture := newSupervisorFixture(t, program)
	first, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("first RunNext: %v", err)
	}
	fixture.assertTerminal(t, first, kernel.OutcomeFailed)
	if first.ChangeID == nil {
		t.Fatal("first run has no Change")
	}
	path := filepath.Join(fixture.changeParent, first.ChangeID.String())
	project := filepath.Join(fixture.root, "repository")
	git := supervisorNativeGit(t)
	mainHead := strings.TrimSpace(supervisorGitOutput(t, git, "-C", project, "rev-parse", "HEAD"))
	mainIndex, err := os.ReadFile(filepath.Join(project, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	changeState, found, err := fixture.store.Change(context.Background(), *first.ChangeID)
	if err != nil || !found || changeState.HeadCommit == nil {
		t.Fatalf("first Change = %+v, found=%v, err=%v", changeState, found, err)
	}
	head := strings.TrimSpace(supervisorGitOutput(t, git, "-C", path, "rev-parse", "HEAD"))
	if head != hex.EncodeToString(changeState.HeadCommit.Bytes()) {
		t.Fatalf("settled head = %s, Change head = %x", head, changeState.HeadCommit.Bytes())
	}
	privateIndex := strings.TrimSpace(supervisorGitOutput(t, git, "-C", path, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	privateIndexBytes, err := os.ReadFile(privateIndex)
	if err != nil {
		t.Fatal(err)
	}
	branch := change.BranchName(first.ChangeID.String())
	private := change.GitDirectoryForChange(project, path)
	legacy := path + ".legacy"
	if err := os.Rename(path, legacy); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(legacy)
	supervisorGit(t, git, "--git-dir", private, "worktree", "prune")
	supervisorGit(t, git, "-c", "protocol.file.allow=always", "-C", project, "fetch", "--no-tags", "--no-write-fetch-head", private, head)
	supervisorGit(t, git, "-C", project, "update-ref", "refs/heads/"+branch, head)
	supervisorGit(t, git, "-C", project, "worktree", "add", "--no-checkout", path, branch)
	canonicalIndex := strings.TrimSpace(supervisorGitOutput(t, git, "-C", path, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	if err := os.WriteFile(canonicalIndex, privateIndexBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := os.Rename(filepath.Join(legacy, entry.Name()), filepath.Join(path, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", path, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(canonicalIndex)
	if err != nil {
		t.Fatal(err)
	}

	queueSupervisorRetry(t, fixture, first)
	fixture.spec.BaseRevision = "refs/heads/retained-canonical-must-not-resolve"
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("retained RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeFailed)
	if second.Proposal == nil || second.Proposal.Code() != kernel.FailureProviderExit {
		t.Fatalf("retry proposal = %+v", second.Proposal)
	}
	if got := strings.TrimSpace(supervisorGitOutput(t, git, "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")); got != filepath.Join(project, ".git") {
		t.Fatalf("retry Git common directory = %q", got)
	}
	if body, err := os.ReadFile(filepath.Join(path, "canonical-git-ok.txt")); err != nil || string(body) != "ok" {
		t.Fatalf("provider Git inspection marker = %q, %v", body, err)
	}
	if witness, err := os.ReadFile(fixture.witness); err != nil || string(witness) != "xx" {
		t.Fatalf("retained provider witness = %q, %v", witness, err)
	}
	for name, want := range map[string]string{"committed.txt": "committed", "edited.txt": "edited", "staged.txt": "staged\n", "untracked.txt": "untracked\n"} {
		body, readErr := os.ReadFile(filepath.Join(path, name))
		if readErr != nil || string(body) != want {
			t.Fatalf("retained %s = %q, %v", name, body, readErr)
		}
	}
	indexAfter, err := os.ReadFile(canonicalIndex)
	if err != nil || string(indexAfter) != string(indexBefore) {
		t.Fatalf("retained worktree index changed: %v", err)
	}
	if got := strings.TrimSpace(supervisorGitOutput(t, git, "-C", project, "rev-parse", "HEAD")); got != mainHead {
		t.Fatalf("main ref changed from %s to %s", mainHead, got)
	}
	mainIndexAfter, err := os.ReadFile(filepath.Join(project, ".git", "index"))
	if err != nil || string(mainIndexAfter) != string(mainIndex) {
		t.Fatalf("main index changed: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(path, "canonical-git-directory.txt")); err != nil || strings.TrimSpace(string(body)) != filepath.Join(project, ".git") {
		t.Fatalf("provider Git grant = %q, %v", body, err)
	}
	projectID := changeState.ProjectID
	handoff, found, err := fixture.store.RetainedChangeHandoffForTask(context.Background(), projectID, second.TaskID)
	if err != nil || !found {
		t.Fatalf("retained handoff = %+v, found=%v, err=%v", handoff, found, err)
	}
	receipt, err := fixture.daemon.attemptSourceHandoff(context.Background(), handoff)
	if err != nil || receipt.GitDirectory != filepath.Join(project, ".git") {
		t.Fatalf("canonical source receipt = %+v, err=%v", receipt, err)
	}
}
