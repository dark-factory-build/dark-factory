//go:build darwin

package changeworker

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
)

func TestMaterializeRetainedSourcesBindsCopyToSelectedTreeAndRejectsEscape(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{parent, runtime} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "11111111111111111111111111111111"
	result := retainedSourceFixture(t, parent, id, []byte("selected"))
	source := RetainedSource{ID: id, Result: result}

	// This is the send-back race: the receipt selected the old tree, then its
	// bytes changed before materialization. The private tree must not be issued
	// under the old receipt.
	if err := os.WriteFile(filepath.Join(parent, id, "payload.txt"), []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeRetainedSource(context.Background(), parent, runtime, source); err == nil {
		t.Fatal("changed retained source materialized under old facts")
	}

	// A selected source produces a stable private copy: later shared-tree
	// mutation cannot alter the accepted reader snapshot.
	result = retainedSourceFixture(t, parent, "22222222222222222222222222222222", []byte("selected"))
	source = RetainedSource{ID: "22222222222222222222222222222222", Result: result}
	path, err := MaterializeRetainedSource(context.Background(), parent, runtime, source)
	if err != nil || path == "" {
		t.Fatalf("materialize selected source = %v, %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(parent, source.ID, "payload.txt"), []byte("later"), 0o600); err != nil {
		t.Fatal(err)
	}
	if copied, err := os.ReadFile(filepath.Join(path, "payload.txt")); err != nil || string(copied) != "selected" {
		t.Fatalf("private copy changed after source mutation: %q, %v", copied, err)
	}

	// This exercises the real materializer rather than asserting a comment:
	// a source symlink is refused by the verified-tree scan before CopyFS.
	result = retainedSourceFixture(t, parent, "33333333333333333333333333333333", []byte("selected"))
	source = RetainedSource{ID: "33333333333333333333333333333333", Result: result}
	payload := filepath.Join(parent, source.ID, "payload.txt")
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeRetainedSource(context.Background(), parent, runtime, source); err == nil {
		t.Fatal("symlink source was copied")
	}
}

func TestMaterializeRetainedSourcesRetriesAfterInterruptedCopy(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{parent, runtime} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const id = "44444444444444444444444444444444"
	result := retainedSourceFixture(t, parent, id, []byte("selected"))
	source := RetainedSource{ID: id, Result: result}
	original := copyRetainedFS
	copyRetainedFS = func(_ context.Context, destination string, _ fs.FS) error {
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, "partial.txt"), []byte("partial"), 0o600); err != nil {
			return err
		}
		return errors.New("interrupted copy")
	}
	t.Cleanup(func() { copyRetainedFS = original })
	if _, err := MaterializeRetainedSource(context.Background(), parent, runtime, source); err == nil {
		t.Fatal("interrupted copy unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(runtime, "retained-source", id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed copy left final target: %v", err)
	}
	copyRetainedFS = original
	path, err := MaterializeRetainedSource(context.Background(), parent, runtime, source)
	if err != nil || path == "" {
		t.Fatalf("retry after interrupted copy = %v, %v", path, err)
	}
}

func TestMaterializeRetainedSourcesRejectsVerificationFailure(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{parent, runtime} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const id = "55555555555555555555555555555555"
	result := retainedSourceFixture(t, parent, id, []byte("selected"))
	source := RetainedSource{ID: id, Result: result}
	original := copyRetainedFS
	copyRetainedFS = func(ctx context.Context, destination string, source fs.FS) error {
		if err := original(ctx, destination, source); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, "payload.txt"), []byte("tampered"), 0o600)
	}
	t.Cleanup(func() { copyRetainedFS = original })
	if _, err := MaterializeRetainedSource(context.Background(), parent, runtime, source); err == nil {
		t.Fatal("verification failure unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(runtime, "retained-source", id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verification failure left final target: %v", err)
	}
}

func TestSecureCopiedDirectoriesUsesOwnerOnlyModes(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureCopiedDirectories(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "nested"), nested} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", path, got)
		}
	}
}

func retainedSourceFixture(t *testing.T, parent, id string, body []byte) Result {
	return retainedSourceFixtureWithMode(t, parent, id, body, "100644")
}

func retainedSourceFixtureWithMode(t *testing.T, parent, id string, body []byte, mode string) Result {
	t.Helper()
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, make([]byte, sha1.Size))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(body))), body...))
	object, err := change.NewObjectID(format, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := change.NewEntry([]byte("payload.txt"), mode, uint64(len(body)), object)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := change.NewManifest(format, base, []change.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := change.Prepare(context.Background(), parent, id, "."+id+".stage")
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(context.Background(), manifest, func(context.Context, change.ObjectID) ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	facts := published.Facts()
	return Result{Format: format, Base: base, Commitment: facts.Commitment(), EntryCount: facts.EntryCount(), BlobBytes: facts.BlobBytes(), Tree: facts.Identity()}
}

func TestMaterializeRetainedSourceExecutableAndPostCopyCancellation(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "77777777777777777777777777777777"
	source := RetainedSource{ID: id, Result: retainedSourceFixtureWithMode(t, parent, id, []byte("#!/bin/sh\nexit 0\n"), "100755")}
	original := copyRetainedFS
	t.Cleanup(func() { copyRetainedFS = original })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	copyRetainedFS = func(ctx context.Context, destination string, source fs.FS) error {
		err := original(ctx, destination, source)
		cancel()
		return err
	}
	if _, err := MaterializeRetainedSource(ctx, parent, runtime, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime, "retained-source", id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel left target: %v", err)
	}
	copyRetainedFS = original
	target, err := MaterializeRetainedSource(context.Background(), parent, runtime, source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(target, "payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Fatal("lost executable bit")
	}
}
