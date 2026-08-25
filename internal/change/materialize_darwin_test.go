//go:build darwin

package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var injectedFailure = errors.New("injected materialization failure")

type changeFixture struct {
	manifest Manifest
	blobs    map[string][]byte
}

func TestMaterializeReconstructsExactPlainTreeSHA1AndSHA256(t *testing.T) {
	for _, formatName := range []string{"sha1", "sha256"} {
		t.Run(formatName, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, formatName, []fixtureFile{
				{[]byte("README.md"), "100644", []byte("hello\n")},
				{[]byte("bin/run"), "100755", []byte("#!/bin/sh\nexit 0\n")},
				{[]byte("empty"), "100644", nil},
				{[]byte("raw/e\u0301"), "100644", []byte("raw-name")},
			})
			preselected := fixture.manifest.Commitment()
			result, err := Materialize(context.Background(), parent, "change", fixture.manifest, fixture.source)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Commitment().Equal(preselected) || result.Path() != filepath.Join(parent, "change") || result.FileCount() != 4 || result.BlobBytes() != fixture.manifest.BlobBytes() || result.Device() == 0 || result.Inode() == 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
			assertExactTree(t, result.Path(), fixture)

			rootFD, rootID := openTestTree(t, result.Path())
			actual, directories, err := scanTree(rootFD, rootID.device, fixture.manifest.format, fixture.manifest.base)
			_ = unix.Close(rootFD)
			if err != nil {
				t.Fatal(err)
			}
			if !manifestsEqual(fixture.manifest, actual) || !preselected.Equal(actual.Commitment()) || !slices.Equal(directories, expectedDirectories(fixture.manifest)) {
				t.Fatal("post-materialization reconstruction did not equal preselection")
			}
		})
	}
}

func TestMaterializeRejectsWrongBlobBeforePublication(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	fileCreateReached := false
	_, err := materialize(context.Background(), parent, "change", fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
		return []byte("wrong"), nil
	}, func(point materializePoint) error {
		if point.step == stepBeforeEntryParentOpen || point.step == stepBeforeFileCreate {
			fileCreateReached = true
		}
		return nil
	})
	if err == nil {
		t.Fatal("wrong blob bytes were trusted")
	}
	if fileCreateReached {
		t.Fatal("wrong blob bytes reached filesystem creation")
	}
	assertNoTargetOrStaging(t, parent, "change")
}

func TestMaterializeRejectsUnsafeBoundaryBeforeBlobOrTargetEffect(t *testing.T) {
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
	for _, target := range []string{"", ".", "..", "a/b", ".GIT", string([]byte{'b', 'a', 'd', 0xfe})} {
		t.Run(fmt.Sprintf("target-%x", target), func(t *testing.T) {
			parent := secureTempDir(t)
			called := false
			_, err := Materialize(context.Background(), parent, target, fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
				called = true
				return nil, nil
			})
			if err == nil || called {
				t.Fatalf("unsafe target was not rejected before blob effect: err=%v called=%v", err, called)
			}
			assertNoStaging(t, parent)
		})
	}

	realParent := secureTempDir(t)
	linkedParent := filepath.Join(secureTempDir(t), "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := Materialize(context.Background(), linkedParent, "change", fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatalf("symlinked parent was not rejected before blob effect: err=%v called=%v", err, called)
	}
	assertNoTargetOrStaging(t, realParent, "change")
}

func TestMaterializeRejectsInjectedNonPlainEntries(t *testing.T) {
	injections := map[string]func(string) error{
		"symlink": func(stage string) error { return os.Symlink("/private/tmp", filepath.Join(stage, "extra")) },
		"hardlink": func(stage string) error {
			return os.Link(filepath.Join(stage, "a"), filepath.Join(stage, "extra"))
		},
		"fifo": func(stage string) error { return unix.Mkfifo(filepath.Join(stage, "extra"), 0o600) },
		"socket": func(stage string) error {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(stage, "extra"), Net: "unix"})
			if err != nil {
				return err
			}
			listener.SetUnlinkOnClose(false)
			return listener.Close()
		},
		"extra directory": func(stage string) error { return os.Mkdir(filepath.Join(stage, "extra"), 0o700) },
	}
	for name, inject := range injections {
		t.Run(name, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			hook := func(point materializePoint) error {
				if point.step == stepBeforeTreeVerify {
					return inject(filepath.Join(point.parent, point.stagingName))
				}
				return nil
			}
			if _, err := materialize(context.Background(), parent, "change", fixture.manifest, fixture.source, hook); err == nil {
				t.Fatalf("injected %s was accepted", name)
			}
			assertNoTargetOrStaging(t, parent, "change")
		})
	}
}

func TestMaterializeDirFDResistsIntermediateSymlinkSwap(t *testing.T) {
	parent := secureTempDir(t)
	outside := secureTempDir(t)
	sentinel := filepath.Join(outside, "z-payload")
	fixture := newFixture(t, "sha1", []fixtureFile{
		{[]byte("nested/a-seed"), "100644", []byte("seed")},
		{[]byte("nested/z-payload"), "100644", []byte("secret")},
	})
	var swapped bool
	hook := func(point materializePoint) error {
		if point.step != stepBeforeEntryParentOpen || string(point.entryPath) != "nested/z-payload" || swapped {
			return nil
		}
		swapped = true
		stage := filepath.Join(point.parent, point.stagingName)
		if err := os.Rename(filepath.Join(stage, "nested"), filepath.Join(stage, "owned-moved")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(stage, "nested"))
	}
	if _, err := materialize(context.Background(), parent, "change", fixture.manifest, fixture.source, hook); err == nil {
		t.Fatal("intermediate symlink swap was accepted")
	}
	if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped staging root: %v", err)
	}
	assertNoTargetOrStaging(t, parent, "change")
}

func TestMaterializeNeverReplacesExistingTarget(t *testing.T) {
	tests := map[string]func(*testing.T, string) func(*testing.T, string){
		"file": func(t *testing.T, path string) func(*testing.T, string) {
			if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "sentinel" {
					t.Fatalf("file target changed: %q %v", data, err)
				}
			}
		},
		"directory": func(t *testing.T, path string) func(*testing.T, string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func(t *testing.T, path string) {
				data, err := os.ReadFile(filepath.Join(path, "sentinel"))
				if err != nil || string(data) != "keep" {
					t.Fatalf("directory target changed: %q %v", data, err)
				}
			}
		},
		"symlink": func(t *testing.T, path string) func(*testing.T, string) {
			if err := os.Symlink("sentinel-destination", path); err != nil {
				t.Fatal(err)
			}
			return func(t *testing.T, path string) {
				destination, err := os.Readlink(path)
				if err != nil || destination != "sentinel-destination" {
					t.Fatalf("symlink target changed: %q %v", destination, err)
				}
			}
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			parent := secureTempDir(t)
			target := filepath.Join(parent, "change")
			check := create(t, target)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			_, err := Materialize(context.Background(), parent, "change", fixture.manifest, fixture.source)
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("got %T %v, want ConflictError", err, err)
			}
			check(t, target)
			assertNoStaging(t, parent)
		})
	}
}

func TestConcurrentMaterializersPublishOneExactWinner(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("winner"), "100644", []byte("exact")}})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := Materialize(context.Background(), parent, "change", fixture.manifest, fixture.source)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var conflictError *ConflictError
		if errors.As(err, &conflictError) {
			conflict++
			continue
		}
		t.Fatalf("unexpected loser error: %T %v", err, err)
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want one each", success, conflict)
	}
	assertExactTree(t, filepath.Join(parent, "change"), fixture)
	assertNoStaging(t, parent)
}

func TestMaterializeFailureCutsCleanupOrRetainPublishedTarget(t *testing.T) {
	prePublication := []materializeStep{
		stepBeforeFileCreate, stepBeforeFileWrite, stepBeforeFileFsync,
		stepBeforeTreeVerify, stepBeforeTreeFsync, stepBeforeRename,
	}
	for _, step := range prePublication {
		t.Run(string(step), func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			hook := failAt(step)
			_, err := materialize(context.Background(), parent, "change", fixture.manifest, fixture.source, hook)
			if !errors.Is(err, injectedFailure) {
				t.Fatalf("got %T %v, want injected failure", err, err)
			}
			assertNoTargetOrStaging(t, parent, "change")
		})
	}
	for _, step := range []materializeStep{stepAfterRename, stepBeforeParentFsync} {
		t.Run(string(step), func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			_, err := materialize(context.Background(), parent, "change", fixture.manifest, fixture.source, failAt(step))
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || !errors.Is(err, injectedFailure) || !unknown.Commitment.Equal(fixture.manifest.Commitment()) || unknown.Device == 0 || unknown.Inode == 0 {
				t.Fatalf("got %T %v, want exact outcome-unknown", err, err)
			}
			assertExactTree(t, filepath.Join(parent, "change"), fixture)
			assertNoStaging(t, parent)
		})
	}
}

func TestMaterializePostPublicationReconstructionDetectsTamper(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	hook := func(point materializePoint) error {
		if point.step == stepAfterRename {
			return os.WriteFile(filepath.Join(point.parent, point.targetName, "a"), []byte("wrong"), 0o644)
		}
		return nil
	}
	_, err := materialize(context.Background(), parent, "change", fixture.manifest, fixture.source, hook)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Commitment.Equal(fixture.manifest.Commitment()) {
		t.Fatalf("post-publication mismatch was not outcome-unknown: %T %v", err, err)
	}
	data, readErr := os.ReadFile(filepath.Join(parent, "change", "a"))
	if readErr != nil || string(data) != "wrong" {
		t.Fatalf("ambiguous target was deleted or changed again: %q %v", data, readErr)
	}
	assertNoStaging(t, parent)
}

func TestMaterializeCancellationIsBoundedAndDoesNotPublishEarly(t *testing.T) {
	steps := []materializeStep{
		stepBeforeFileCreate, stepBeforeFileWrite, stepBeforeFileFsync,
		stepBeforeTreeVerify, stepBeforeTreeFsync, stepBeforeRename,
	}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", bytes.Repeat([]byte("a"), 1024)}})
			ctx, cancel := context.WithCancel(context.Background())
			hook := func(point materializePoint) error {
				if point.step == step {
					cancel()
				}
				return nil
			}
			started := time.Now()
			_, err := materialize(ctx, parent, "change", fixture.manifest, fixture.source, hook)
			if !errors.Is(err, context.Canceled) || time.Since(started) > 2*time.Second {
				t.Fatalf("cancellation was not prompt: %T %v after %s", err, err, time.Since(started))
			}
			assertNoTargetOrStaging(t, parent, "change")
		})
	}

	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
	ctx, cancel := context.WithCancel(context.Background())
	hook := func(point materializePoint) error {
		if point.step == stepAfterRename {
			cancel()
		}
		return nil
	}
	_, err := materialize(ctx, parent, "change", fixture.manifest, fixture.source, hook)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-publish cancellation was not outcome-unknown: %T %v", err, err)
	}
	assertExactTree(t, filepath.Join(parent, "change"), fixture)
}

func TestMaterializeIdentityMismatchRefusesCleanup(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	var preserved string
	hook := func(point materializePoint) error {
		if point.step != stepBeforeOwnedStageCleanup {
			return nil
		}
		original := filepath.Join(point.parent, point.stagingName)
		preserved = original + "-preserved"
		if err := os.Rename(original, preserved); err != nil {
			return err
		}
		return os.Mkdir(original, 0o700)
	}
	_, err := materialize(context.Background(), parent, "change", fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
		return []byte("wrong"), nil
	}, hook)
	var unresolved *UnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("got %T %v, want UnresolvedError", err, err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("owned artifact was not left visible: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 2 {
		t.Fatalf("identity mismatch artifacts = %v, err=%v", entries, err)
	}
}

func TestPublishedChangeCarriesNoRepositoryAuthority(t *testing.T) {
	parent := secureTempDir(t)
	git(t, parent, "init", "--quiet")
	changes := filepath.Join(parent, "changes")
	if err := os.Mkdir(changes, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
	result, err := Materialize(context.Background(), changes, "change", fixture.manifest, fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(result.Path(), ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published Change has Git metadata: %v", err)
	}
	command := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	command.Dir = result.Path()
	command.Env = append(safeGitEnv(), "GIT_CEILING_DIRECTORIES="+changes)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("controlled ceiling discovered ancestor repository: %q", output)
	}
	command = exec.Command("git", "rev-parse", "--is-inside-work-tree")
	command.Dir = result.Path()
	command.Env = safeGitEnv()
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("fixture did not prove ancestor discovery without ceiling: %q %v", output, err)
	}
}

func TestMaterializeDoesNotLeakDescriptorsGoroutinesOrStaging(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	beforeFDs := descriptorCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	for i := range 20 {
		target := fmt.Sprintf("change-%d", i)
		if _, err := Materialize(context.Background(), parent, target, fixture.manifest, fixture.source); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("descriptor count changed: before=%d after=%d", beforeFDs, after)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines+1 {
		t.Fatalf("goroutine count changed: before=%d after=%d", beforeGoroutines, after)
	}
	assertNoStaging(t, parent)
}

type fixtureFile struct {
	path []byte
	mode string
	data []byte
}

func newFixture(t testing.TB, formatName string, files []fixtureFile) changeFixture {
	t.Helper()
	format := mustFormat(t, formatName)
	base := mustID(t, format, bytes.Repeat([]byte{0x55}, format.OIDLength()))
	entries := make([]Entry, len(files))
	blobs := make(map[string][]byte, len(files))
	for i, file := range files {
		entries[i] = mustEntry(t, format, file.path, file.mode, file.data)
		blobs[entries[i].oid.Hex()] = bytes.Clone(file.data)
	}
	return changeFixture{manifest: mustManifest(t, format, base, entries), blobs: blobs}
}

func (f changeFixture) source(_ context.Context, oid ObjectID) ([]byte, error) {
	data, ok := f.blobs[oid.Hex()]
	if !ok {
		return nil, errors.New("missing fixture blob")
	}
	return bytes.Clone(data), nil
}

func secureTempDir(t testing.TB) string {
	t.Helper()
	path, err := os.MkdirTemp(os.Getenv("TMPDIR"), "dark-factory-change-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func failAt(step materializeStep) materializeHook {
	return func(point materializePoint) error {
		if point.step == step {
			return injectedFailure
		}
		return nil
	}
}

func assertExactTree(t testing.TB, root string, fixture changeFixture) {
	t.Helper()
	expectedDirs := expectedDirectories(fixture.manifest)
	actualDirs := make([]string, 0)
	actualFiles := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
			return fmt.Errorf("non-plain entry %q: %v", relative, info.Mode())
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("directory mode %q = %o", relative, info.Mode().Perm())
			}
			actualDirs = append(actualDirs, relative)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file %q", relative)
		}
		actualFiles[relative] = struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(actualDirs)
	if !slices.Equal(actualDirs, expectedDirs) {
		t.Fatalf("directories = %q, want %q", actualDirs, expectedDirs)
	}
	if len(actualFiles) != len(fixture.manifest.entries) {
		t.Fatalf("file count = %d, want %d", len(actualFiles), len(fixture.manifest.entries))
	}
	for _, entry := range fixture.manifest.entries {
		path := filepath.Join(root, string(entry.path))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, fixture.blobs[entry.oid.Hex()]) {
			t.Fatalf("bytes differ for %x", entry.path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		wantMode := os.FileMode(0o644)
		if entry.mode == "100755" {
			wantMode = 0o755
		}
		if info.Mode().Perm() != wantMode {
			t.Fatalf("mode for %x = %o, want %o", entry.path, info.Mode().Perm(), wantMode)
		}
		if _, ok := actualFiles[string(entry.path)]; !ok {
			t.Fatalf("missing walked file %x", entry.path)
		}
	}
}

func assertNoTargetOrStaging(t testing.TB, parent, target string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(parent, target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after pre-publication failure: %v", err)
	}
	assertNoStaging(t, parent)
}

func assertNoStaging(t testing.TB, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dark-factory-change-") {
			t.Fatalf("staging artifact leaked: %q", entry.Name())
		}
	}
}

func openTestTree(t testing.TB, path string) (int, fileIdentity) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	return fd, identityOf(stat)
}

func git(t testing.TB, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = safeGitEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func safeGitEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	}
}

func descriptorCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
