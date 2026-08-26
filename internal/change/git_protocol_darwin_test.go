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
	"sync"
	"testing"
	"time"
)

func TestRepositoryIdentityUsesExactStoreIntegerBounds(t *testing.T) {
	for _, invalid := range [][2]uint64{{0, 0}, {1 << 63, 1}, {0, 1 << 63}} {
		if _, err := NewRepositoryIdentity(invalid[0], invalid[1]); err == nil {
			t.Fatalf("invalid Store identity (%d,%d) accepted", invalid[0], invalid[1])
		}
	}
	for _, valid := range [][2]uint64{{0, 1}, {1<<63 - 1, 1<<63 - 1}} {
		identity, err := NewRepositoryIdentity(valid[0], valid[1])
		if err != nil || identity.Device() != valid[0] || identity.Inode() != valid[1] {
			t.Fatalf("valid Store identity (%d,%d) reconstructed as %+v: %v", valid[0], valid[1], identity, err)
		}
	}
}

func TestSelectGitUsesSealedMetadataOnlyBoundary(t *testing.T) {
	repository := fakeRepository(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x11}, format.OIDLength()))
	blob := mustEntry(t, format, []byte("file"), "100644", []byte("secret"))
	logPath := filepath.Join(filepath.Dir(repository), "commands")
	environmentPath := filepath.Join(filepath.Dir(repository), "environment")
	blobWitness := filepath.Join(filepath.Dir(repository), "blob-read")
	os.Setenv("DARK_FACTORY_AMBIENT_SENTINEL", "must-not-cross")
	t.Cleanup(func() { os.Unsetenv("DARK_FACTORY_AMBIENT_SENTINEL") })
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *" config "*) /usr/bin/env > %q; exit 1 ;;
  *" rev-parse "*) printf '%%s\nsha1\n%%s\n' %q %q ;;
  *" ls-tree "*) printf '100644 blob %%s       6\tfile\0' %q ;;
  *" cat-file "*) : > %q; exit 91 ;;
  *) exit 92 ;;
esac
`, logPath, environmentPath, repository, base.Hex(), blob.oid.Hex(), blobWitness)
	git := writeFakeGit(t, script)
	selected, err := SelectGit(context.Background(), git, repository, "refs/heads/main", mustRepositoryIdentity(t, repository))
	if err != nil {
		t.Fatal(err)
	}
	if !selected.Base().equal(base) || !manifestsEqual(selected.Manifest(), mustManifest(t, format, base, []Entry{blob})) {
		t.Fatal("fake metadata selection did not produce the exact manifest")
	}
	if _, err := os.Lstat(blobWitness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selection crossed the blob boundary: %v", err)
	}
	commands := mustReadFile(t, logPath)
	if bytes.Contains(commands, []byte("cat-file")) || !bytes.Contains(commands, []byte("rev-parse")) || !bytes.Contains(commands, []byte("ls-tree")) {
		t.Fatalf("selection commands=%q", commands)
	}
	environment := mustReadFile(t, environmentPath)
	for _, required := range []string{
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
		"GIT_PROTOCOL_FROM_USER=0", "GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
	} {
		if !bytes.Contains(environment, []byte(required+"\n")) {
			t.Fatalf("sealed environment lacks %q: %q", required, environment)
		}
	}
	if bytes.Contains(environment, []byte("DARK_FACTORY_AMBIENT_SENTINEL")) {
		t.Fatal("ambient environment crossed the Git boundary")
	}
}

func TestGitBlobsValidatesExactOrderAndFramingWithoutLeakingOutput(t *testing.T) {
	cases := map[string]func(string) string{
		"wrong oid":  func(oid string) string { return strings.Repeat("0", len(oid)) + " blob 6\\nsecret\\n" },
		"wrong type": func(oid string) string { return oid + " tree 6\\nsecret\\n" },
		"wrong size": func(oid string) string { return oid + " blob 5\\nsecret\\n" },
		"oversize":   func(oid string) string { return fmt.Sprintf("%s blob %d\\n", oid, MaxBlobBytes+1) },
		"truncated":  func(oid string) string { return oid + " blob 6\\nsec" },
		"delimiter":  func(oid string) string { return oid + " blob 6\\nsecretX" },
		"missing":    func(oid string) string { return oid + " missing\\n" },
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			repository := fakeRepository(t)
			selection, entry := fakeSelection(t, repository, "", []byte("secret"))
			responseText := response(entry.oid.Hex())
			script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nprintf '%%b' %q\n", responseText)
			git := writeFakeGit(t, script)
			selection.gitExecutable = git
			selection.gitIdentity = mustGitFileIdentity(t, git)
			blobs, err := OpenGitBlobs(context.Background(), git, repository, selection)
			if err != nil {
				t.Fatal(err)
			}
			_, err = blobs.Read(context.Background(), entry.oid)
			if err == nil {
				t.Fatal("malformed batch response accepted")
			}
			var protocol *GitProtocolError
			if !errors.As(err, &protocol) {
				t.Fatalf("error=%T %v, want GitProtocolError", err, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("private output leaked: %v", err)
			}
		})
	}

	repository := fakeRepository(t)
	selection, first := fakeSelection(t, repository, "a", []byte("first"))
	format := selection.format
	second := mustEntry(t, format, []byte("b"), "100644", []byte("second"))
	selection.manifest = mustManifest(t, format, selection.base, []Entry{first, second})
	logPath := filepath.Join(filepath.Dir(repository), "requests")
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r request; do
  printf '%%s\n' "$request" >> %q
  case "$request" in
    %s) printf '%%s blob 5\nfirst\n' "$request" ;;
    %s) printf '%%s blob 6\nsecond\n' "$request" ;;
    *) exit 3 ;;
  esac
done
`, logPath, first.oid.Hex(), second.oid.Hex())
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	blobs, err := OpenGitBlobs(context.Background(), git, repository, selection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Read(context.Background(), second.oid); err == nil {
		t.Fatal("out-of-order object request accepted")
	}
	if got := mustReadFile(t, logPath); len(got) != 0 {
		t.Fatalf("out-of-order request reached Git: %q", got)
	}
}

func TestGitBlobsBoundsAndRedactsStderrAndRejectsIncompleteClose(t *testing.T) {
	const sentinel = "PRIVATE-BLOB-TOKEN"
	repository := fakeRepository(t)
	selection, entry := fakeSelection(t, repository, "", []byte("secret"))
	script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nexec /usr/bin/perl -e 'print STDERR q(%s) x %d; while(1){}'\n", sentinel, maxGitStderrBytes)
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	recorder := &gitEventRecorder{}
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = blobs.Read(ctx, entry.oid)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stderr bound did not terminate the child: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("private stderr/blob leaked: %v", err)
	}
	recorder.require(t, 1, 1, 0, 1)

	repository = fakeRepository(t)
	selection, _ = fakeSelection(t, repository, "", []byte("secret"))
	git = writeFakeGit(t, "#!/bin/sh\nwhile IFS= read -r request; do exit 4; done\n")
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	blobs, err = OpenGitBlobs(context.Background(), git, repository, selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.Close(); err == nil {
		t.Fatal("incomplete blob source closed successfully")
	}
}

func TestGitBlobsReadsOnlySelectedObjectsInOrderAndWaitsOnce(t *testing.T) {
	repository := fakeRepository(t)
	selection, first := fakeSelection(t, repository, "a", []byte("first"))
	second := mustEntry(t, selection.format, []byte("b"), "100644", []byte("second"))
	selection.manifest = mustManifest(t, selection.format, selection.base, []Entry{first, second})
	logPath := filepath.Join(filepath.Dir(repository), "requests")
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r request; do
  printf '%%s\n' "$request" >> %q
  case "$request" in
    %s) printf '%%s blob 5\nfirst\n' "$request" ;;
    %s) printf '%%s blob 6\nsecond\n' "$request" ;;
    *) exit 3 ;;
  esac
done
`, logPath, first.oid.Hex(), second.oid.Hex())
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	recorder := &gitEventRecorder{}
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		entry Entry
		data  string
	}{{first, "first"}, {second, "second"}} {
		data, err := blobs.Read(context.Background(), expected.entry.oid)
		if err != nil || string(data) != expected.data {
			t.Fatalf("read=%q err=%v", data, err)
		}
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(mustReadFile(t, logPath)), first.oid.Hex()+"\n"+second.oid.Hex()+"\n"; got != want {
		t.Fatalf("requests=%q want=%q", got, want)
	}
	recorder.require(t, 1, 0, 0, 1)
}

func TestGitChildCancellationKillsAndWaitsExactlyOnce(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		repository := fakeRepository(t)
		ready := filepath.Join(filepath.Dir(repository), "ready")
		blocking := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/perl -e '$SIG{TERM}=q(IGNORE); open(F,q(>%s)); print F q(x); close(F); while(1){}'\n", ready)
		git := writeFakeGit(t, blocking)
		recorder := &gitEventRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := selectGit(ctx, git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
			result <- err
		}()
		waitForFile(t, ready)
		cancel()
		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 1, 1, 1, 1)
	})
	t.Run("blob", func(t *testing.T) {
		repository := fakeRepository(t)
		selection, entry := fakeSelection(t, repository, "", []byte("secret"))
		ready := filepath.Join(filepath.Dir(repository), "ready")
		script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nexec /usr/bin/perl -e '$SIG{TERM}=q(IGNORE); open(F,q(>%s)); print F q(x); close(F); while(1){}'\n", ready)
		git := writeFakeGit(t, script)
		selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
		recorder := &gitEventRecorder{}
		blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := blobs.Read(ctx, entry.oid)
			result <- err
		}()
		waitForFile(t, ready)
		cancel()
		err = <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 1, 1, 1, 1)
	})
	t.Run("before start", func(t *testing.T) {
		repository := fakeRepository(t)
		git := writeFakeGit(t, "#!/bin/sh\nexit 90\n")
		recorder := &gitEventRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := selectGit(ctx, git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 0, 0, 0, 0)
	})
}

func waitForFile(t testing.TB, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for child witness %s", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSelectedLooseObjectRemovalFailsWithoutFallback(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	first := selected.manifest.entries[0]
	objectPath := filepath.Join(fixture.repository, ".git", "objects", first.oid.Hex()[:2], first.oid.Hex()[2:])
	quarantine := objectPath + ".quarantine"
	if err := os.Rename(objectPath, quarantine); err != nil {
		t.Fatalf("quarantine selected loose object: %v", err)
	}
	blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Read(context.Background(), first.oid); err == nil {
		t.Fatal("missing selected object received fallback content")
	}
}

func TestGitStartFailureLeavesNoOwnedProcessDescriptorOrHome(t *testing.T) {
	repository := fakeRepository(t)
	git := filepath.Join(secureTempDir(t), "invalid-git")
	if err := os.WriteFile(git, []byte("not an executable image"), 0o700); err != nil {
		t.Fatal(err)
	}
	beforeFDs := descriptorCount(t)
	homesBefore, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &gitEventRecorder{}
	_, err = selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
	if err == nil {
		t.Fatal("invalid executable image started")
	}
	var process *GitProcessError
	if !errors.As(err, &process) {
		t.Fatalf("error=%T %v, want GitProcessError", err, err)
	}
	recorder.require(t, 0, 0, 0, 0)
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("start failure leaked descriptors: before=%d after=%d", beforeFDs, after)
	}
	homesAfter, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil || len(homesAfter) != len(homesBefore) {
		t.Fatalf("start failure HOME census before=%v after=%v err=%v", homesBefore, homesAfter, err)
	}
}

type gitEventRecorder struct {
	mu     sync.Mutex
	events []gitProcessEvent
}

func (r *gitEventRecorder) record(event gitProcessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *gitEventRecorder) require(t testing.TB, started, termed, killed, waited int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[gitProcessEvent]int)
	for _, event := range r.events {
		counts[event]++
	}
	if counts[gitProcessStarted] != started || counts[gitProcessTermed] != termed || counts[gitProcessKilled] != killed || counts[gitProcessWaited] != waited {
		t.Fatalf("process events=%v", r.events)
	}
}

func fakeRepository(t testing.TB) string {
	t.Helper()
	repository := filepath.Join(secureTempDir(t), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return repository
}

func fakeSelection(t testing.TB, repository, path string, data []byte) (Selection, Entry) {
	t.Helper()
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x22}, format.OIDLength()))
	if path == "" {
		path = "file"
	}
	entry := mustEntry(t, format, []byte(path), "100644", data)
	return Selection{
		repositoryRoot: repository, repositoryIdentity: mustRepositoryIdentity(t, repository),
		format: format, base: base, manifest: mustManifest(t, format, base, []Entry{entry}),
	}, entry
}

func writeFakeGit(t testing.TB, contents string) string {
	t.Helper()
	path := filepath.Join(secureTempDir(t), "git")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustGitFileIdentity(t testing.TB, path string) gitFileIdentity {
	t.Helper()
	identity, err := validateGitExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func mustReadFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	return data
}
