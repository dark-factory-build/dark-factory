//go:build darwin

package change_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
)

func TestStageIdentityReconstructsRecoveryAuthorityAcrossClose(t *testing.T) {
	for _, invalid := range [][2]uint64{{0, 0}, {1 << 63, 1}, {0, 1 << 63}} {
		if _, err := change.NewStageIdentity(invalid[0], invalid[1]); err == nil {
			t.Fatalf("invalid Store identity (%d,%d) accepted", invalid[0], invalid[1])
		}
	}
	if _, err := change.NewStageIdentity(0, 1); err != nil {
		t.Fatalf("minimum Store identity rejected: %v", err)
	}
	if _, err := change.NewStageIdentity(1<<63-1, 1<<63-1); err != nil {
		t.Fatalf("maximum Store identity rejected: %v", err)
	}

	ctx := context.Background()
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{0x42}, format.OIDLength()))
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("recovery")
	header := []byte(fmt.Sprintf("blob %d\x00", len(blob)))
	sum := sha1.Sum(append(header, blob...))
	oid, err := change.NewObjectID(format, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	entry, err := change.NewEntry([]byte("source.txt"), "100644", uint64(len(blob)), oid)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := change.NewManifest(format, base, []change.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared, err := change.Prepare(ctx, parent, "published", "recorded-stage")
	if err != nil {
		t.Fatal(err)
	}
	identity := prepared.Identity()
	if _, err := prepared.PopulateAndPublish(ctx, manifest, func(context.Context, change.ObjectID) ([]byte, error) {
		return bytes.Clone(blob), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	reconstructed, err := change.NewStageIdentity(identity.Device(), identity.Inode())
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := change.NewStageIdentity(identity.Device(), identity.Inode()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := change.InspectPublished(ctx, parent, "published", wrong, format, base); err == nil {
		t.Fatal("wrong reconstructed identity authorized inspection")
	}
	if err := change.RemoveRecordedTree(ctx, parent, "published", wrong); err == nil {
		t.Fatal("wrong reconstructed identity authorized removal")
	}
	if _, err := os.Stat(filepath.Join(parent, "published", "source.txt")); err != nil {
		t.Fatalf("wrong reconstructed identity changed target: %v", err)
	}
	facts, err := change.InspectPublished(ctx, parent, "published", reconstructed, format, base)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Identity().Equal(reconstructed) || !facts.Commitment().Equal(manifest.Commitment()) {
		t.Fatalf("reconstructed recovery facts differ: %+v", facts)
	}
	if err := change.RemoveRecordedTree(ctx, parent, "published", reconstructed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "published")); !os.IsNotExist(err) {
		t.Fatalf("recorded tree survived reconstructed removal: %v", err)
	}
}
