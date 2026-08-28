//go:build darwin

package install

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestOperationalHomeLeasesPopulatedGoHome(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	store, err := kernel.Open(context.Background(), filepath.Join(homePath, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtimeSentinel := []byte("runtime descendant must not be read")
	changeSentinel := []byte("change descendant must not be read")
	if err := os.WriteFile(filepath.Join(homePath, runtimesName, "sentinel"), runtimeSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, changesName, "sentinel"), changeSentinel, 0o600); err != nil {
		t.Fatal(err)
	}

	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := home.Database()
	if err != nil {
		t.Fatal(err)
	}
	runtimes, err := home.Runtimes()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := home.Changes()
	if err != nil {
		t.Fatal(err)
	}
	databaseFile, err := database.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := databaseFile.Close(); err != nil {
		t.Fatal(err)
	}
	runtimesFile, err := runtimes.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimesFile.Close(); err != nil {
		t.Fatal(err)
	}
	changesFile, err := changes.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := changesFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationalHome(context.Background(), homePath); !errors.Is(err, ErrBusy) {
		t.Fatalf("second operational opener = %v, want busy", err)
	}
	if got, err := os.ReadFile(filepath.Join(homePath, runtimesName, "sentinel")); err != nil || !bytes.Equal(got, runtimeSentinel) {
		t.Fatalf("runtime descendant changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(homePath, changesName, "sentinel")); err != nil || !bytes.Equal(got, changeSentinel) {
		t.Fatalf("change descendant changed: %q, %v", got, err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(); !errors.Is(err, ErrClosed) {
		t.Fatalf("database capability after close = %v, want closed", err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalHomeAcceptsValidWALRestartLayout(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(homePath, databaseName)
	raw, err := sql.Open("sqlite3", "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE factory SET updated_at_ms = updated_at_ms"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(databasePath+suffix, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(databasePath + "-wal"); err != nil {
		t.Fatalf("WAL sidecar was not retained: %v", err)
	}
	if _, err := os.Stat(databasePath + "-shm"); err != nil {
		t.Fatalf("SHM sidecar was not retained: %v", err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatalf("open valid WAL home: %v", err)
	}
	if err := home.Close(); err != nil {
		t.Fatalf("close valid WAL home: %v", err)
	}
}

func TestOperationalHomeRejectsMalformedSQLiteSidecarsWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "incomplete pair", setup: func(homePath string) error {
			return os.WriteFile(filepath.Join(homePath, databaseName+"-wal"), []byte("wal"), 0o600)
		}},
		{name: "malformed pair", setup: func(homePath string) error {
			if err := os.WriteFile(filepath.Join(homePath, databaseName+"-wal"), []byte("wal"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(homePath, databaseName+"-shm"), make([]byte, operationalMinSHMBytes), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			if err := test.setup(homePath); err != nil {
				t.Fatal(err)
			}
			before := installDigest(t, homePath)
			if _, err := OpenOperationalHome(context.Background(), homePath); err == nil {
				t.Fatal("malformed operational SQLite sidecars were accepted")
			}
			if after := installDigest(t, homePath); after != before {
				t.Fatal("malformed sidecars changed after refusal")
			}
		})
	}
}
func TestOperationalHomeHoldsLeaseThroughDatabaseOpen(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := kernel.Open(context.Background(), filepath.Join(homePath, databaseName))
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatalf("close after database open = %v", err)
	}
}

func TestOperationalHomeCloseReportsReplacementWithoutMutation(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := home.Database()
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(homePath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(homePath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(homePath, "replacement")
	if err := os.WriteFile(replacement, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("database capability after whole-home replacement = %v, want uncertain", err)
	}
	before, err := os.ReadFile(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("close after home replacement = %v, want uncertain", err)
	}
	after, err := os.ReadFile(replacement)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("replacement changed after uncertain close: %q, %v", after, err)
	}
}

func TestOperationalHomeLockReplacementStillContends(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	movedLock := filepath.Join(parent, "moved-lock")
	if err := os.Rename(filepath.Join(homePath, lockName), movedLock); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement-lock")
	if err := os.WriteFile(replacement, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(replacement, filepath.Join(homePath, lockName)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationalHome(context.Background(), homePath); err == nil {
		t.Fatal("opener after lock-name replacement was accepted")
	}
	if err := home.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("close after lock-name replacement = %v, want uncertain", err)
	}
}

func TestOperationalHomeRejectsUnknownRootEntry(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	hostile := filepath.Join(homePath, "hostile")
	if err := os.WriteFile(hostile, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationalHome(context.Background(), homePath); err == nil {
		t.Fatal("unknown root entry was accepted")
	}
	after, err := os.ReadFile(hostile)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("unknown root entry changed after refusal: %q, %v", after, err)
	}
}

func TestOperationalHomeCapabilityAndCloseRejectMemberReplacement(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability func(*OperationalHome) (MemberCapability, error)
		directory  bool
	}{
		{name: databaseName, capability: (*OperationalHome).Database},
		{name: runtimesName, capability: (*OperationalHome).Runtimes, directory: true},
		{name: changesName, capability: (*OperationalHome).Changes, directory: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			capability, err := test.capability(home)
			if err != nil {
				t.Fatal(err)
			}
			memberPath := filepath.Join(homePath, test.name)
			if err := os.Rename(memberPath, filepath.Join(parent, "moved-"+test.name)); err != nil {
				t.Fatal(err)
			}
			if test.directory {
				err = os.Mkdir(memberPath, 0o700)
			} else {
				err = os.WriteFile(memberPath, []byte("replacement"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := capability.Open(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("capability after %s replacement = %v, want uncertain", test.name, err)
			}
			if err := home.Close(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("close after %s replacement = %v, want uncertain", test.name, err)
			}
		})
	}
}

func TestOperationalHomeRetainedTokenRejectsReplacement(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := home.Database()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(homePath, tokenName), filepath.Join(parent, "moved-token")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, tokenName), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("database capability after token replacement = %v, want uncertain", err)
	}
	if err := home.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("close after token replacement = %v, want uncertain", err)
	}
}

func TestOperationalHomeRetainedAncestryRejectsReplacement(t *testing.T) {
	root := installTempDir(t)
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	homePath := filepath.Join(inner, "home")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := home.Database()
	if err != nil {
		t.Fatal(err)
	}
	moved := outer + ".moved"
	if err := os.Rename(outer, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(moved, "inner"), inner); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("database capability after ancestor replacement = %v, want uncertain", err)
	}
	if err := home.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("close after ancestor replacement = %v, want uncertain", err)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalHomeConcurrentCloseIsIdempotent(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- home.Close()
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent close = %v", err)
		}
	}
	if reopened, err := OpenOperationalHome(context.Background(), homePath); err != nil {
		t.Fatalf("open after concurrent close = %v", err)
	} else if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

const operationalLeaseHelperEnv = "DARK_FACTORY_OPERATIONAL_LEASE_HELPER"

func TestOperationalHomeLeaseHelper(t *testing.T) {
	if os.Getenv(operationalLeaseHelperEnv) != "1" {
		return
	}
	homePath := os.Getenv("DARK_FACTORY_OPERATIONAL_LEASE_HOME")
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	ready := os.NewFile(3, "operational-lease-ready")
	release := os.NewFile(4, "operational-lease-release")
	if ready == nil || release == nil {
		t.Fatal("missing lease helper pipes")
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(release, signal[:]); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalHomeContentionInSubprocess(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		t.Fatal(err)
	}
	defer readyRead.Close()
	defer releaseWrite.Close()
	command := exec.Command(os.Args[0], "-test.run", "^TestOperationalHomeLeaseHelper$", "-test.v")
	command.Env = append(os.Environ(),
		operationalLeaseHelperEnv+"=1",
		"DARK_FACTORY_OPERATIONAL_LEASE_HOME="+homePath,
	)
	command.ExtraFiles = []*os.File{readyWrite, releaseRead}
	if err := command.Start(); err != nil {
		_ = readyWrite.Close()
		_ = releaseRead.Close()
		t.Fatal(err)
	}
	_ = readyWrite.Close()
	_ = releaseRead.Close()
	ready := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, readErr := io.ReadFull(readyRead, signal[:])
		ready <- readErr
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("lease helper did not acquire home: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("lease helper did not acquire home")
	}
	if _, err := OpenOperationalHome(context.Background(), homePath); !errors.Is(err, ErrBusy) {
		_ = releaseWrite.Close()
		_ = command.Wait()
		t.Fatalf("subprocess contention = %v, want busy", err)
	}
	if _, err := releaseWrite.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lease helper exit = %v", err)
	}
}

func TestOperationalHomeDescriptorCensus(t *testing.T) {
	baseline, ok := descriptorCount()
	if !ok {
		t.Skip("/dev/fd is unavailable")
	}
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	if current, _ := descriptorCount(); current != baseline {
		t.Fatalf("descriptor count after init = %d, baseline %d", current, baseline)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	if current, _ := descriptorCount(); current <= baseline {
		t.Fatalf("descriptor count after operational open = %d, baseline %d", current, baseline)
	}
	database, err := home.Database()
	if err != nil {
		t.Fatal(err)
	}
	file, err := database.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	if current, _ := descriptorCount(); current != baseline {
		t.Fatalf("descriptor count after operational close = %d, baseline %d", current, baseline)
	}
}
