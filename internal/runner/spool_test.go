//go:build darwin

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalSpoolStoreBeforeExactAck(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	terminal := Terminal{AttemptID: "attempt-1", Process: Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}, Exit: Exit{Code: 0}, Message: "private"}
	record, err := PublishTerminal(dir, "terminal.json", terminal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishTerminal(dir, "terminal.json", terminal); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement=%v", err)
	}
	loaded, err := LoadTerminal(dir, "terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != record.Digest || loaded.Identity != record.Identity {
		t.Fatal("spool identity changed")
	}
	if err := AcknowledgeTerminal(dir, "terminal.json", record, false); !errors.Is(err, ErrState) {
		t.Fatalf("ack before Store=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "terminal.json")); err != nil {
		t.Fatal("ack-before-Store deleted spool")
	}
	forged := *record
	forged.Digest = "00"
	if err := AcknowledgeTerminal(dir, "terminal.json", &forged, true); !errors.Is(err, ErrIdentity) {
		t.Fatalf("forged ack=%v", err)
	}
	if err := AcknowledgeTerminal(dir, "terminal.json", record, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "terminal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ack retained spool")
	}
}

func TestTerminalLoadRejectsSymlinkAndTrailingData(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Symlink("missing", filepath.Join(root, "terminal.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTerminal(dir, "terminal.json"); err == nil {
		t.Fatal("symlink accepted")
	}
}
