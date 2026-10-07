//go:build darwin

package change

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveSourceUsesExactRevisionsAndRenameEvidence(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	identity, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fixture.repository, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("old.go", "package source\nconst Old=\"move me with distinctive source\"\n")
	write("modify.go", "package source\n")
	write("remove.go", "package source\nconst Remove=\"remove this different content\"\n")
	runFixtureGit(t, fixture.git, fixture.repository, "add", ".")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-qm", "base")
	base := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	runFixtureGit(t, fixture.git, fixture.repository, "mv", "old.go", "new.go")
	runFixtureGit(t, fixture.git, fixture.repository, "rm", "remove.go")
	write("modify.go", "package source\nconst Modified=true\n")
	write("added.go", "package source\n")
	runFixtureGit(t, fixture.git, fixture.repository, "add", ".")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-qm", "proposed")
	head := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	observation, err := ObserveSource(ctx, fixture.git, fixture.repository, "", base, head, identity)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]SourcePath{}
	for _, item := range observation.Paths {
		status[item.Path] = item
	}
	if observation.Kind != "committed" || status["new.go"].OldPath != "old.go" || status["added.go"].Status != "added" || status["modify.go"].Status != "modified" || status["remove.go"].Status != "deleted" {
		t.Fatalf("observed = %+v", observation)
	}
	write("uncommitted.go", "package source\n")
	revision, archive, err := ArchiveSource(ctx, fixture.git, fixture.repository, base, identity)
	if err != nil || revision != base || strings.Contains(string(archive), "uncommitted.go") || strings.Contains(string(archive), "added.go") {
		t.Fatalf("archive mixed working source: %s %v", revision, err)
	}
	write(".gitattributes", "added.go export-ignore\n")
	runFixtureGit(t, fixture.git, fixture.repository, "add", ".gitattributes")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-qm", "archive attributes")
	if _, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, "HEAD", identity); err == nil {
		t.Fatal("export-ignore silently removed integrated source")
	}
	if _, err := ObserveSource(ctx, fixture.git, fixture.repository, "", strings.Repeat("f", 40), head, identity); err == nil {
		t.Fatal("missing base accepted")
	}
}

func TestObserveSourceDirtyFingerprintCannotInheritCommitEvidence(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	identity, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectGit(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(secureTempDir(t), "worktree")
	if _, err := AddWorktree(ctx, selected, worktree, "factory/112233445566"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("changed work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "new.go"), []byte("package first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := ObserveSource(ctx, fixture.git, fixture.repository, worktree, fixture.base.Hex(), fixture.base.Hex(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "working-tree" || len(first.Fingerprint) != 64 || len(first.Paths) != 2 {
		t.Fatalf("dirty evidence = %+v", first)
	}
	if err := os.WriteFile(filepath.Join(worktree, "new.go"), []byte("package other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := ObserveSource(ctx, fixture.git, fixture.repository, worktree, fixture.base.Hex(), fixture.base.Hex(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("untracked edit inherited old observation")
	}
	if _, err := ObserveSource(ctx, fixture.git, fixture.repository, worktree, fixture.base.Hex(), strings.Repeat("a", 40), identity); err == nil {
		t.Fatal("different PR head accepted")
	}
}

func TestObserveSourceDoesNotInventRemovalsWhenIntegratedTargetAdvances(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	identity, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "proposed.go"), []byte("package proposed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "add", "proposed.go")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-qm", "proposal")
	head := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	runFixtureGit(t, fixture.git, fixture.repository, "checkout", "-b", "integrated", fixture.base.Hex())
	if err := os.WriteFile(filepath.Join(fixture.repository, "integrated.go"), []byte("package integrated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "add", "integrated.go")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-qm", "integration")
	target := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	source, err := ObserveSource(ctx, fixture.git, fixture.repository, "", target, head, identity)
	if err != nil {
		t.Fatal(err)
	}
	if source.Base != fixture.base.Hex() || source.Target != target || len(source.Paths) != 1 || source.Paths[0].Path != "proposed.go" || source.Paths[0].Status != "added" {
		t.Fatalf("invented target reversal: %+v", source)
	}
}

func TestObserveSourceFramesUntrackedFiles(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	identity, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectGit(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(secureTempDir(t), "worktree")
	if _, err := AddWorktree(ctx, selected, worktree, "factory/223344556677"); err != nil {
		t.Fatal(err)
	}
	observe := func(a, b string) SourceObservation {
		t.Helper()
		if err := os.WriteFile(filepath.Join(worktree, "a.go"), []byte(a), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, "b.go"), []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := ObserveSource(ctx, fixture.git, fixture.repository, worktree, fixture.base.Hex(), fixture.base.Hex(), identity)
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	first, second := observe("package a\n", "package b\n"), observe("package a\npackage ", "b\n")
	t.Logf("first=%+v second=%+v", first, second)
	if first.Fingerprint == second.Fingerprint {
		t.Fatalf("distinct two-file working trees share fingerprint %s", first.Fingerprint)
	}
	if err := os.Chmod(filepath.Join(worktree, "a.go"), 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := ObserveSource(ctx, fixture.git, fixture.repository, worktree, fixture.base.Hex(), fixture.base.Hex(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if executable.Fingerprint == second.Fingerprint {
		t.Fatal("executable mode inherited old observation")
	}
}

func TestArchiveSourceFollowsUpstreamNotLaggingBranch(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	ctx := context.Background()
	git := func(arguments ...string) string {
		t.Helper()
		return strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, arguments...))
	}
	identity, err := InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, "HEAD", identity); err != nil || revision != fixture.base.Hex() {
		t.Fatalf("no upstream: %s %v", revision, err)
	}
	branch := git("symbolic-ref", "--short", "HEAD")
	upstream := git("commit-tree", "-p", "HEAD", "-m", "integrated", "HEAD^{tree}")
	git("config", "remote.origin.url", filepath.Join(t.TempDir(), "absent"))
	git("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	git("config", "branch."+branch+".remote", "origin")
	git("config", "branch."+branch+".merge", "refs/heads/"+branch)
	identity, err = InspectRepositorySource(ctx, fixture.git, fixture.repository, "", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	// Configured but never fetched: the local branch is all there is.
	if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, "HEAD", identity); err != nil || revision != fixture.base.Hex() {
		t.Fatalf("unfetched upstream: %s %v", revision, err)
	}
	git("update-ref", "refs/remotes/origin/"+branch, upstream)
	for _, target := range []string{"HEAD", branch, "origin/" + branch} {
		if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, target, identity); err != nil || revision != upstream {
			t.Fatalf("%s read %s, want upstream %s: %v", target, revision, upstream, err)
		}
	}
	// Parked on a feature branch whose upstream is ahead: HEAD is still
	// origin's default branch, as for change starts; the named branch is not.
	feature := git("commit-tree", "-p", upstream, "-m", "unmerged", "HEAD^{tree}")
	git("checkout", "-q", "-b", "feature")
	git("update-ref", "refs/remotes/origin/feature", feature)
	git("config", "branch.feature.remote", "origin")
	git("config", "branch.feature.merge", "refs/heads/feature")
	if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, "HEAD", identity); err != nil || revision != feature {
		t.Fatalf("without origin/HEAD, HEAD read %s, want its upstream %s: %v", revision, feature, err)
	}
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
	for target, want := range map[string]string{"HEAD": upstream, "feature": feature} {
		if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, target, identity); err != nil || revision != want {
			t.Fatalf("%s read %s, want %s: %v", target, revision, want, err)
		}
	}
	// A change start's fetch is the integrated target when strictly newer.
	fetched := git("commit-tree", "-p", upstream, "-m", "fetched", "HEAD^{tree}")
	git("update-ref", factoryBaseRef("origin", "refs/heads/"+branch), fetched)
	for _, target := range []string{"HEAD", "origin/" + branch} {
		if revision, _, err := ArchiveSource(ctx, fixture.git, fixture.repository, target, identity); err != nil || revision != fetched {
			t.Fatalf("%s read %s, want factory fetch %s: %v", target, revision, fetched, err)
		}
	}
	if git("rev-parse", "HEAD") != fixture.base.Hex() {
		t.Fatal("archive moved the checkout")
	}
}
