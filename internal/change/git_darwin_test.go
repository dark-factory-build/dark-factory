//go:build darwin

package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type localGitFixture struct {
	git        string
	repository string
	identity   RepositoryIdentity
	format     ObjectFormat
	base       ObjectID
	manifest   Manifest
	blobs      map[string][]byte
}

func TestSelectGitRealSHA1AndSHA256WithoutBlobReads(t *testing.T) {
	for _, formatName := range []string{"sha1", "sha256"} {
		t.Run(formatName, func(t *testing.T) {
			fixture := newLocalGitFixture(t, formatName)
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			if !selected.RepositoryIdentity().Equal(fixture.identity) || selected.ObjectFormat() != fixture.format ||
				!selected.Base().equal(fixture.base) || !selected.Commitment().Equal(fixture.manifest.Commitment()) ||
				selected.EntryCount() != fixture.manifest.EntryCount() || selected.BlobBytes() != fixture.manifest.BlobBytes() ||
				!manifestsEqual(selected.Manifest(), fixture.manifest) {
				t.Fatalf("selection differs: %+v", selected)
			}
		})
	}
}

func TestSelectionSurvivesRefAndWorktreeMovementAndMaterializesExactOldCommit(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("new commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "add", "README.md")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-m", "move HEAD")
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("uncommitted worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changeParent := secureTempDir(t)
	prepared := mustPrepare(t, changeParent, "published", "declared-stage")
	blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(context.Background(), selected.Manifest(), blobs.Read)
	if err != nil {
		_ = blobs.Abort()
		t.Fatal(err)
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if !published.Facts().Commitment().Equal(selected.Commitment()) {
		t.Fatalf("published commitment=%s selected=%s", published.Facts().Commitment().Hex(), selected.Commitment().Hex())
	}
	assertExactTree(t, published.Path(), changeFixture{manifest: fixture.manifest, blobs: fixture.blobs})
	readme, err := os.ReadFile(filepath.Join(published.Path(), "README.md"))
	if err != nil || string(readme) != "old commit\n" {
		t.Fatalf("published moving state: %q %v", readme, err)
	}
	if _, err := os.Lstat(filepath.Join(published.Path(), ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published Change contains Git authority: %v", err)
	}
}

func TestSelectGitRepositoryShapesAndReplacement(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	alias := fixture.repository + "/../" + filepath.Base(fixture.repository)
	if _, err := SelectGit(context.Background(), fixture.git, alias, "HEAD", fixture.identity); err == nil {
		t.Fatal(".. repository alias accepted")
	}
	symlinkRoot := filepath.Join(filepath.Dir(fixture.repository), "repository-link")
	if err := os.Symlink(fixture.repository, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectGit(context.Background(), fixture.git, symlinkRoot, "HEAD", fixture.identity); err == nil {
		t.Fatal("symlink repository root accepted")
	}

	parentRepository := secureTempDir(t)
	runFixtureGit(t, fixture.git, parentRepository, "init")
	nested := filepath.Join(parentRepository, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedIdentity := mustRepositoryIdentity(t, nested)
	if _, err := SelectGit(context.Background(), fixture.git, nested, "HEAD", nestedIdentity); err == nil {
		t.Fatal("upward repository discovery accepted")
	}

	bare := filepath.Join(secureTempDir(t), "bare.git")
	runFixtureGit(t, fixture.git, filepath.Dir(bare), "init", "--bare", bare)
	if _, err := SelectGit(context.Background(), fixture.git, bare, "HEAD", mustRepositoryIdentity(t, bare)); err == nil {
		t.Fatal("bare repository accepted")
	}

	linked := filepath.Join(filepath.Dir(fixture.repository), "linked")
	runFixtureGit(t, fixture.git, fixture.repository, "worktree", "add", "-b", "linked-test", linked, "HEAD")
	if _, err := SelectGit(context.Background(), fixture.git, linked, "HEAD", mustRepositoryIdentity(t, linked)); err == nil {
		t.Fatal("linked worktree .git file accepted")
	}

	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.repository + "-original"
	if err := os.Rename(fixture.repository, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture.repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected); err == nil {
		t.Fatal("replacement repository served an old selection")
	}
}

func TestSelectGitRejectsPartialCloneAlternatesAndNonCommitRevision(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "core.repositoryformatversion", "1")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "extensions.partialClone", "origin")
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("partial clone accepted")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "--unset", "extensions.partialClone")
	alternates := filepath.Join(fixture.repository, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte("/private/tmp/objects\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("object alternates accepted")
	}
	if err := os.Remove(alternates); err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "hash-object", "README.md"))
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, blob, fixture.identity); err == nil {
		t.Fatal("non-commit revision accepted")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "tag", "-a", "-m", "fixture tag", "release", "HEAD")
	tagged, err := SelectGit(context.Background(), fixture.git, fixture.repository, "release", fixture.identity)
	if err != nil {
		t.Fatalf("annotated tag peeling to a commit was refused: %v", err)
	}
	if !tagged.Base().equal(fixture.base) {
		t.Fatalf("tag selected %s, want commit %s", tagged.Base().Hex(), fixture.base.Hex())
	}
}

func TestGitAuthorityIsRecheckedAtProcessBoundaries(t *testing.T) {
	repository := fakeRepository(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x31}, format.OIDLength()))
	entry := mustEntry(t, format, []byte("file"), "100644", []byte("secret"))
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *" config "*) exit 1 ;;
  *" rev-parse "*) printf '%%s\nsha1\n%%s\n' %q %q ;;
  *" ls-tree "*) printf '100644 blob %%s 6\tfile\0' %q ;;
esac
`, repository, base.Hex(), entry.oid.Hex())
	git := writeFakeGit(t, script)
	original := repository + "-original"
	mutated := false
	_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), func(event gitProcessEvent) {
		if event != gitProcessWaited || mutated {
			return
		}
		mutated = true
		if renameErr := os.Rename(repository, original); renameErr != nil {
			panic(renameErr)
		}
		if mkdirErr := os.Mkdir(repository, 0o700); mkdirErr != nil {
			panic(mkdirErr)
		}
		if mkdirErr := os.Mkdir(filepath.Join(repository, ".git"), 0o700); mkdirErr != nil {
			panic(mkdirErr)
		}
	})
	if err == nil {
		t.Fatal("repository replacement after metadata child exit was accepted")
	}

	repository = fakeRepository(t)
	selection, _ := fakeSelection(t, repository, "", []byte("secret"))
	git = writeFakeGit(t, "#!/bin/sh\nwhile IFS= read -r request; do exit 0; done\n")
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	original = git + "-original"
	_, err = openGitBlobs(context.Background(), git, repository, selection, func(event gitProcessEvent) {
		if event != gitProcessStarted {
			return
		}
		if renameErr := os.Rename(git, original); renameErr != nil {
			panic(renameErr)
		}
		if writeErr := os.WriteFile(git, []byte("#!/bin/sh\nexit 0\n"), 0o700); writeErr != nil {
			panic(writeErr)
		}
	})
	if err == nil {
		t.Fatal("Git executable replacement after blob child start was accepted")
	}
}

func TestParseGitTreeRejectsUnsupportedMalformedAndUnboundedMetadata(t *testing.T) {
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{1}, format.OIDLength()))
	oid := strings.Repeat("a", format.OIDLength()*2)
	invalid := map[string][]byte{
		"symlink":      []byte(fmt.Sprintf("120000 blob %s 1\tlink\x00", oid)),
		"gitlink":      []byte(fmt.Sprintf("160000 commit %s -\tsubmodule\x00", oid)),
		"tree":         []byte(fmt.Sprintf("040000 tree %s -\tdirectory\x00", oid)),
		"git path":     []byte(fmt.Sprintf("100644 blob %s 1\t.GIT/config\x00", oid)),
		"invalid utf8": append([]byte(fmt.Sprintf("100644 blob %s 1\tbad", oid)), 0xff, 0),
		"malformed":    []byte("not-a-record\x00"),
		"truncated":    []byte(fmt.Sprintf("100644 blob %s 1\tpath", oid)),
		"wrong oid":    []byte("100644 blob aa 1\tpath\x00"),
		"size":         []byte(fmt.Sprintf("100644 blob %s %d\tpath\x00", oid, MaxBlobBytes+1)),
	}
	for name, output := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitTree(format, base, output); err == nil {
				t.Fatal("unsafe tree metadata accepted")
			}
		})
	}
	duplicate := []byte(fmt.Sprintf("100644 blob %s 1\tpath\x00100644 blob %s 1\tpath\x00", oid, oid))
	if _, err := parseGitTree(format, base, duplicate); err == nil {
		t.Fatal("duplicate path accepted")
	}
	deep := strings.Repeat("d/", MaxDepth) + "f"
	if _, err := parseGitTree(format, base, []byte(fmt.Sprintf("100644 blob %s 1\t%s\x00", oid, deep))); err == nil {
		t.Fatal("over-depth path accepted")
	}
	total := make([]byte, 0)
	for index := range 5 {
		total = append(total, []byte(fmt.Sprintf("100644 blob %s %d\tf%d\x00", oid, MaxBlobBytes, index))...)
	}
	if _, err := parseGitTree(format, base, total); err == nil {
		t.Fatal("over-total-size tree accepted")
	}
}

func newLocalGitFixture(t testing.TB, formatName string) localGitFixture {
	t.Helper()
	git := "/usr/bin/git"
	root := secureTempDir(t)
	repository := filepath.Join(root, "repository")
	command := exec.Command(git, "init", "--object-format="+formatName, repository)
	command.Env = fixtureGitEnvironment(root)
	if output, err := command.CombinedOutput(); err != nil {
		message := string(output)
		if formatName == "sha256" && (strings.Contains(message, "unknown value") || strings.Contains(message, "unsupported")) {
			t.Skipf("installed Git explicitly lacks SHA-256 repositories: %s", strings.TrimSpace(message))
		}
		t.Fatalf("git init %s: %v: %s", formatName, err, output)
	}
	runFixtureGit(t, git, repository, "config", "user.name", "Dark Factory Test")
	runFixtureGit(t, git, repository, "config", "user.email", "test@invalid")
	files := []fixtureFile{
		{[]byte("README.md"), "100644", []byte("old commit\n")},
		{[]byte("empty"), "100644", nil},
		{[]byte("nested/run"), "100755", []byte("#!/bin/sh\nexit 0\n")},
	}
	for _, file := range files {
		path := filepath.Join(repository, string(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if file.mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(path, file.data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, git, repository, "add", "--all")
	runFixtureGit(t, git, repository, "commit", "-m", "fixture")
	format := mustFormat(t, formatName)
	base, err := parseGitOID(format, []byte(strings.TrimSpace(runFixtureGitOutput(t, git, repository, "rev-parse", "HEAD"))))
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]Entry, len(files))
	blobs := make(map[string][]byte, len(files))
	for index, file := range files {
		entries[index] = mustEntry(t, format, file.path, file.mode, file.data)
		blobs[entries[index].oid.Hex()] = bytes.Clone(file.data)
	}
	return localGitFixture{
		git: git, repository: repository, identity: mustRepositoryIdentity(t, repository),
		format: format, base: base, manifest: mustManifest(t, format, base, entries), blobs: blobs,
	}
}

func mustRepositoryIdentity(t testing.TB, path string) RepositoryIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repositoryIdentityOf(info)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func runFixtureGit(t testing.TB, git, repository string, arguments ...string) {
	t.Helper()
	_ = runFixtureGitOutput(t, git, repository, arguments...)
}

func runFixtureGitOutput(t testing.TB, git, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", repository}, arguments...)...)
	command.Env = fixtureGitEnvironment(filepath.Dir(repository))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func fixtureGitEnvironment(home string) []string {
	return []string{
		"HOME=" + home, "TMPDIR=" + home, "LC_ALL=C", "LANG=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
	}
}

func TestGitBoundaryResourceCensus(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	runtime.GC()
	beforeFDs := descriptorCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	for range 10 {
		selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
		if err != nil {
			t.Fatal(err)
		}
		blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range selected.Manifest().Entries() {
			if _, err := blobs.Read(context.Background(), entry.ObjectID()); err != nil {
				t.Fatal(err)
			}
		}
		if err := blobs.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("Git boundary descriptor leak: before=%d after=%d", beforeFDs, after)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines+1 {
		t.Fatalf("Git boundary goroutine leak: before=%d after=%d", beforeGoroutines, after)
	}
	homes, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil || len(homes) != 0 {
		t.Fatalf("Git HOME census=%v err=%v", homes, err)
	}
}
