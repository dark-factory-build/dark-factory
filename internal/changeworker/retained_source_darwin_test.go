//go:build darwin

package changeworker

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
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

func retainedSourceFixture(t *testing.T, parent, id string, body []byte) Result {
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
	entry, err := change.NewEntry([]byte("payload.txt"), "100644", uint64(len(body)), object)
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
