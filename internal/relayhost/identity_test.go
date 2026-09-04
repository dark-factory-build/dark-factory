package relayhost

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadOrCreateWritesAnOwnerOnlyKeyAndABootOrderedGeneration(t *testing.T) {
	home := t.TempDir()
	boot := time.Unix(1_800_000_000, 0)
	first, err := loadOrCreateAt(home, boot)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	if first.Generation() != uint64(boot.Unix()) {
		t.Fatalf("first generation = %d, want the boot instant %d", first.Generation(), boot.Unix())
	}
	if length := len(first.NodeID()); length != 32 {
		t.Fatalf("node id %q is %d characters, want 32", first.NodeID(), length)
	}
	if strings.Trim(first.NodeID(), "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("node id %q is not lowercase base32", first.NodeID())
	}
	if first.NodeID() != NodeIDFromPublicKey(first.PublicKey()) {
		t.Fatal("node id is not derived from the loaded public key")
	}

	directory := filepath.Join(home, "relay")
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("relay directory mode = %v, want 0700", info.Mode().Perm())
	}
	fileInfo, err := os.Lstat(filepath.Join(directory, "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("node.key mode = %v, want 0600", fileInfo.Mode().Perm())
	}
	if names, err := os.ReadDir(directory); err != nil || len(names) != 1 {
		t.Fatalf("relay directory holds %v (%v), want only the node key", names, err)
	}

	// A later boot outranks the earlier one with nothing persisted between
	// them, so a home restored from backup is not locked out of the relay.
	second, err := loadOrCreateAt(home, boot.Add(time.Second))
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if second.Generation() <= first.Generation() {
		t.Fatalf("second generation = %d, want more than %d", second.Generation(), first.Generation())
	}
	if second.NodeID() != first.NodeID() || !bytes.Equal(second.PublicKey(), first.PublicKey()) {
		t.Fatal("reload produced a different node identity")
	}
}

func TestLoadOrCreateRefusesAKeyOtherAccountsCanRead(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(home, "relay", "node.key")
	if err := os.Chmod(key, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); !errors.Is(err, ErrIdentity) {
		t.Fatalf("group readable key = %v, want ErrIdentity", err)
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatalf("restored key: %v", err)
	}
}

func TestLoadOrCreateRefusesASymlinkedKey(t *testing.T) {
	home := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "planted.key")
	if err := os.WriteFile(elsewhere, []byte(strings.Repeat("ab", ed25519.SeedSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "relay"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "relay", "node.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); !errors.Is(err, ErrIdentity) {
		t.Fatalf("symlinked key = %v, want ErrIdentity", err)
	}
}
