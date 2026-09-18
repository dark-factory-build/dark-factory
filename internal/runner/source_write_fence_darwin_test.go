//go:build darwin

package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectSourceWritesFencesSharedGitAndKeepsPrivateChangeUsable(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "separate-roots"
		if nested {
			name = "nested-roots"
		}
		t.Run(name, func(t *testing.T) { checkSourceWriteFence(t, nested) })
	}
}

func checkSourceWriteFence(t *testing.T, nested bool) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository, changes := filepath.Join(root, "repository"), filepath.Join(root, "changes")
	if nested {
		changes = filepath.Join(repository, "changes")
	}
	source, sibling := filepath.Join(changes, "a"), filepath.Join(changes, "b")
	gitDirectory := filepath.Join(repository, ".git", "dark-factory-changes", "a", ".git")
	lease := filepath.Join(repository, ".git", "dark-factory-local-ci")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "--initial-branch=main", repository)
	if err := os.WriteFile(filepath.Join(repository, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repository, "add", "README")
	run(repository, "-c", "user.name=owner", "-c", "user.email=owner@example", "commit", "-qm", "base")
	run(repository, "-c", "user.name=owner", "-c", "user.email=owner@example.invalid", "commit", "--allow-empty", "-qm", "second")
	if err := os.MkdirAll(filepath.Dir(gitDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "-q", "--bare", gitDirectory)
	run(root, "--git-dir", gitDirectory, "fetch", "-q", repository, "main:refs/heads/main")
	run(root, "--git-dir", gitDirectory, "worktree", "add", "-qb", "factory/a", source, "main")
	if err := os.MkdirAll(lease, 0700); err != nil {
		t.Fatal(err)
	}

	shell, err := CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	canonGit := filepath.Join(repository, ".git")
	mainBefore, err := os.ReadFile(filepath.Join(canonGit, "refs", "heads", "main"))
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(canonGit, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(canonGit, "refs", "heads", "main"), filepath.Join(source, "protected-hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(canonGit, "index"), filepath.Join(source, "protected-symlink")); err != nil {
		t.Fatal(err)
	}
	script := strings.Join([]string{
		"set -eu",
		"printf own >\"$3/own.txt\"",
		"printf change >\"$3/change.txt\"",
		"git -C \"$3\" add change.txt",
		"git -C \"$3\" -c user.name=worker -c user.email=worker@example commit -qm change",
		"printf lease >\"$5/proof\"",
		"git -C \"$3\" update-ref refs/heads/private-proof HEAD",
		"if printf x >\"$1/refs/heads/main\" 2>/dev/null; then exit 40; fi",
		"if git -C \"$2\" update-ref refs/heads/main HEAD^ 2>/dev/null; then exit 40; fi",
		"if git --git-dir=\"$1\" update-ref refs/heads/main HEAD^ 2>/dev/null; then exit 40; fi",
		"if GIT_DIR=\"$1\" GIT_WORK_TREE=\"$3\" git update-ref refs/heads/main HEAD^ 2>/dev/null; then exit 40; fi",
		"if printf x >\"$1/objects/hostile-object\" 2>/dev/null; then exit 40; fi",
		"if printf x >\"$1/index\" 2>/dev/null; then exit 40; fi",
		"if printf x >\"$4/blocked\" 2>/dev/null; then exit 40; fi",
		"if ln \"$1/refs/heads/main\" \"$3/new-hardlink\" 2>/dev/null; then exit 40; fi",
		"if ln \"$1/refs/heads/main\" \"$5/new-hardlink\" 2>/dev/null; then exit 40; fi",
		"if ln \"$3/change.txt\" \"$5/own-export\" 2>/dev/null; then exit 40; fi",
		"if printf x >\"$4/internal-hardlink\" 2>/dev/null; then exit 40; fi",
		"if printf x >\"$3/protected-symlink\" 2>/dev/null; then exit 40; fi",
	}, "\n") + "\n"
	args := []string{"/bin/sh", "-c", script, "sh", canonGit, repository, source, sibling, lease, gitDirectory}
	spec, err := PrepareCommittedExecSpec(shell, args, []string{"PATH=/usr/bin:/bin", "LANG=C"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err == nil {
		t.Fatal("pre-existing hardlink into writable source was accepted")
	}
	if err := os.Remove(filepath.Join(source, "protected-hardlink")); err != nil {
		t.Fatal(err)
	}
	sourceOwned := filepath.Join(source, "source-owned")
	if err := os.WriteFile(sourceOwned, []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceOutside := filepath.Join(root, "source-outside-hardlink")
	if err := os.Link(sourceOwned, sourceOutside); err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err == nil {
		t.Fatal("pre-existing hardlink from writable source was accepted")
	}
	if err := os.Remove(sourceOutside); err != nil {
		t.Fatal(err)
	}
	adminOutside := filepath.Join(root, "admin-outside-hardlink")
	if err := os.Link(filepath.Join(gitDirectory, "refs", "heads", "main"), adminOutside); err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err == nil {
		t.Fatal("pre-existing hardlink from private Git administration was accepted")
	}
	if err := os.Remove(adminOutside); err != nil {
		t.Fatal(err)
	}
	leaseOwned := filepath.Join(lease, "lease-owned")
	if err := os.WriteFile(leaseOwned, []byte("lease\n"), 0600); err != nil {
		t.Fatal(err)
	}
	leaseOutside := filepath.Join(root, "lease-outside-hardlink")
	if err := os.Link(leaseOwned, leaseOutside); err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err == nil {
		t.Fatal("pre-existing hardlink from CI lease was accepted")
	}
	if err := os.Remove(leaseOutside); err != nil {
		t.Fatal(err)
	}
	outsideAlias := filepath.Join(root, "outside-hardlink")
	if err := os.Link(filepath.Join(canonGit, "refs", "heads", "main"), outsideAlias); err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err == nil {
		t.Fatal("pre-existing hardlink outside protected roots was accepted")
	}
	if err := os.Remove(outsideAlias); err != nil {
		t.Fatal(err)
	}
	// Compiler caches may hardlink files wholly inside the protected union.
	if err := os.Link(filepath.Join(canonGit, "refs", "heads", "main"), filepath.Join(sibling, "internal-hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := spec.ProtectSourceWrites(repository, changes, gitDirectory, lease); err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-p", spec.sandboxProfile}, spec.commit.Argv...)
	cmd := exec.Command(spec.sandbox.Path(), argv...)
	cmd.Dir, cmd.Env = source, spec.commit.Env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("protected worker: %v\n%s", err, out)
	}
	for _, path := range []string{filepath.Join(source, "own.txt"), filepath.Join(lease, "proof"), filepath.Join(gitDirectory, "refs", "heads", "private-proof")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected private write %s: %v", path, err)
		}
	}
	for path, before := range map[string][]byte{filepath.Join(canonGit, "refs", "heads", "main"): mainBefore, filepath.Join(canonGit, "index"): indexBefore} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("canonical data changed: %s (%v)", path, err)
		}
	}
	// Prove the hostile Git command is otherwise valid and host-authorized.
	run(repository, "update-ref", "refs/heads/main", "HEAD^")
	if err := os.WriteFile(filepath.Join(canonGit, "objects", "hostile-object"), []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
}
