package relayhost

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateWritesOwnerOnlyFilesAndAdvancesGenerationMonotonically(t *testing.T) {
	home := t.TempDir()
	first, err := LoadOrCreate(home)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	if first.Generation() != 1 {
		t.Fatalf("first generation = %d, want 1", first.Generation())
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
	for _, name := range []string{"node.key", "generation"} {
		fileInfo, err := os.Lstat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, fileInfo.Mode().Perm())
		}
	}

	second, err := LoadOrCreate(home)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if second.Generation() != 2 {
		t.Fatalf("second generation = %d, want 2", second.Generation())
	}
	if second.NodeID() != first.NodeID() || !bytes.Equal(second.PublicKey(), first.PublicKey()) {
		t.Fatal("reload produced a different node identity")
	}
	recorded, err := os.ReadFile(filepath.Join(directory, "generation"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(recorded)) != "2" {
		t.Fatalf("durable generation = %q, want 2", strings.TrimSpace(string(recorded)))
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

func TestLoadOrCreateRefusesACorruptGenerationCounter(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "relay", "generation"), []byte("not a number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); !errors.Is(err, ErrIdentity) {
		t.Fatalf("corrupt counter = %v, want ErrIdentity", err)
	}
}
