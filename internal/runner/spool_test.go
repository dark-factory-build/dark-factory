//go:build darwin

package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testTerminal(attempt, message string) Terminal {
	return Terminal{AttemptID: attempt, Process: Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}, Exit: Exit{Code: 0}, Message: message}
}

func openTestSpool(t *testing.T) (string, *os.File) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return root, dir
}

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
	terminal := testTerminal("attempt-1", "private")
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

func TestTerminalSpoolExactAckBindsCompleteTerminal(t *testing.T) {
	root, dir := openTestSpool(t)
	record, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt-exact", "authentic"))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Terminal){
		"process": func(terminal *Terminal) {
			terminal.Process = Identity{PID: 23, PGID: 23, Birth: Birth{Seconds: 3, Microseconds: 4}}
		},
		"exit": func(terminal *Terminal) {
			terminal.Exit = Exit{Code: -1, Signal: int(unix.SIGKILL)}
		},
		"message": func(terminal *Terminal) {
			terminal.Message = "forged"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			forged := *record
			mutate(&forged.Terminal)
			if err := AcknowledgeTerminal(dir, "terminal.json", &forged, true); !errors.Is(err, ErrIdentity) {
				t.Fatalf("forged terminal ack=%v", err)
			}
			retained, err := LoadTerminal(dir, "terminal.json")
			if err != nil || retained.Digest != record.Digest || retained.Identity != record.Identity || retained.Terminal != record.Terminal {
				t.Fatalf("forged ack mutated spool: retained=%+v err=%v", retained, err)
			}
		})
	}
	if err := AcknowledgeTerminal(dir, "terminal.json", record, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "terminal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact committed ack retained spool: %v", err)
	}
}

func TestTerminalSpoolRequiresPrivateDirectoryForEveryOperation(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o770} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, mode); err != nil {
				t.Fatal(err)
			}
			dir, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			if _, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private")); !errors.Is(err, ErrIdentity) {
				t.Fatalf("publish in mode %o: %v", mode, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected publish changed directory: entries=%v err=%v", entries, err)
			}
		})
	}

	root, dir := openTestSpool(t)
	record, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTerminal(dir, "terminal.json"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("load from shared directory: %v", err)
	}
	if err := AcknowledgeTerminal(dir, "terminal.json", record, true); !errors.Is(err, ErrIdentity) {
		t.Fatalf("ack from shared directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "terminal.json")); err != nil {
		t.Fatalf("rejected shared-directory ack mutated spool: %v", err)
	}
}

func TestTerminalSpoolRejectsUnsafeFileAndSwapWithoutAck(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		root, dir := openTestSpool(t)
		record, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "terminal.json")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTerminal(dir, "terminal.json"); !errors.Is(err, ErrIdentity) {
			t.Fatalf("unsafe mode loaded: %v", err)
		}
		if err := AcknowledgeTerminal(dir, "terminal.json", record, true); !errors.Is(err, ErrIdentity) {
			t.Fatalf("unsafe mode acked: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unsafe file removed: %v", err)
		}
	})

	t.Run("link-count", func(t *testing.T) {
		root, dir := openTestSpool(t)
		record, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(root, "terminal.json"), filepath.Join(root, "second-link")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTerminal(dir, "terminal.json"); !errors.Is(err, ErrIdentity) {
			t.Fatalf("multiply linked file loaded: %v", err)
		}
		if err := AcknowledgeTerminal(dir, "terminal.json", record, true); !errors.Is(err, ErrIdentity) {
			t.Fatalf("multiply linked file acked: %v", err)
		}
	})

	t.Run("swap", func(t *testing.T) {
		root, dir := openTestSpool(t)
		original, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt-1", "one"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "terminal.json"), filepath.Join(root, "old-terminal")); err != nil {
			t.Fatal(err)
		}
		replacement, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt-2", "two"))
		if err != nil {
			t.Fatal(err)
		}
		if err := AcknowledgeTerminal(dir, "terminal.json", original, true); !errors.Is(err, ErrIdentity) {
			t.Fatalf("swapped terminal acked: %v", err)
		}
		loaded, err := LoadTerminal(dir, "terminal.json")
		if err != nil || loaded.Identity != replacement.Identity || loaded.Digest != replacement.Digest {
			t.Fatalf("replacement mutated: loaded=%+v err=%v", loaded, err)
		}
	})
}

func TestTerminalSpoolRejectsForeignOwnershipWherePermitted(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing ownership safely requires a root test process")
	}
	t.Run("directory", func(t *testing.T) {
		root, dir := openTestSpool(t)
		if err := os.Chown(root, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private")); !errors.Is(err, ErrIdentity) {
			t.Fatalf("foreign-owned directory accepted: %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		root, dir := openTestSpool(t)
		record, err := PublishTerminal(dir, "terminal.json", testTerminal("attempt", "private"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "terminal.json")
		if err := os.Chown(path, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTerminal(dir, "terminal.json"); !errors.Is(err, ErrIdentity) {
			t.Fatalf("foreign-owned file loaded: %v", err)
		}
		if err := AcknowledgeTerminal(dir, "terminal.json", record, true); !errors.Is(err, ErrIdentity) {
			t.Fatalf("foreign-owned file acked: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("foreign-owned file removed: %v", err)
		}
	})
}

func TestPrivateMetadataPoliciesRejectForeignOwnership(t *testing.T) {
	directoryStat := unix.Stat_t{Dev: 1, Ino: 2, Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid()), Mode: unix.S_IFDIR | 0o700, Nlink: 2}
	directory := fileCommitment{
		FileIdentity: FileIdentity{Device: 1, Inode: 2}, UID: directoryStat.Uid, GID: directoryStat.Gid, Mode: uint32(directoryStat.Mode), Size: directoryStat.Size,
		MtimeSec: directoryStat.Mtim.Sec, MtimeNsec: directoryStat.Mtim.Nsec, CtimeSec: directoryStat.Ctim.Sec, CtimeNsec: directoryStat.Ctim.Nsec,
	}
	if !validPrivateDirectoryMetadata(directory, directoryStat) {
		t.Fatal("valid private directory metadata rejected")
	}
	foreignDirectory := directory
	foreignDirectoryStat := directoryStat
	foreignDirectory.UID++
	foreignDirectoryStat.Uid++
	if validPrivateDirectoryMetadata(foreignDirectory, foreignDirectoryStat) {
		t.Fatal("foreign directory owner accepted")
	}
	sharedDirectory := directory
	sharedDirectory.Mode = uint32(unix.S_IFDIR | 0o770)
	directoryStat.Mode = unix.S_IFDIR | 0o770
	if validPrivateDirectoryMetadata(sharedDirectory, directoryStat) {
		t.Fatal("shared directory mode accepted")
	}

	file := unix.Stat_t{Dev: 1, Ino: 3, Uid: uint32(os.Geteuid()), Mode: unix.S_IFREG | 0o600, Nlink: 1, Size: 10}
	if !validTerminalFile(file, 10) {
		t.Fatal("valid private terminal metadata rejected")
	}
	file.Uid++
	if validTerminalFile(file, 10) {
		t.Fatal("foreign terminal owner accepted")
	}
}

func TestTerminalSpoolFinalEncodingBoundRoundTripsExactly(t *testing.T) {
	root, dir := openTestSpool(t)
	terminal := testTerminal("attempt", "")
	empty, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Message = "x"
	one, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	messageFieldOverhead := len(one) - len(empty) - 1
	available := maxTerminalBytes - 1 - len(empty) - messageFieldOverhead
	if available <= 0 {
		t.Fatal("terminal bound cannot encode a message")
	}
	terminal.Message = strings.Repeat("\x00", available/6) + strings.Repeat("x", available%6)
	exact, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact)+1 != maxTerminalBytes || len(terminal.Message) > 8192 {
		t.Fatalf("constructed final frame=%d message=%d", len(exact)+1, len(terminal.Message))
	}
	record, err := PublishTerminal(dir, "terminal.json", terminal)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTerminal(dir, "terminal.json")
	if err != nil || loaded.Digest != record.Digest || loaded.Terminal.Message != terminal.Message {
		t.Fatalf("exact maximum did not roundtrip: loaded=%+v err=%v", loaded, err)
	}
	over := terminal
	over.Message += "x"
	if _, err := PublishTerminal(dir, "over.json", over); err == nil {
		t.Fatal("one-byte-over terminal published")
	}
	if _, err := os.Stat(filepath.Join(root, "over.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized publication left final effect: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "terminal.json" {
		t.Fatalf("oversized publication left temporary effect: entries=%v err=%v", entries, err)
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
	if err := AcknowledgeTerminal(dir, "terminal.json", &TerminalRecord{Terminal: testTerminal("attempt", "private")}, true); err == nil {
		t.Fatal("symlink acknowledged")
	}
	if info, err := os.Lstat(filepath.Join(root, "terminal.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rejected symlink ack mutated entry: info=%v err=%v", info, err)
	}
}
