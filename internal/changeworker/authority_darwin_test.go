//go:build darwin

package changeworker

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestCaptureHelpersRejectEveryChildOnMetadataOrDeviceFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{HomeName, TempName} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, AttemptTokenName), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	rootID, err := privateDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	wrongDevice := rootID.Device + 1
	if wrongDevice == 0 {
		wrongDevice = rootID.Device - 1
	}

	for _, name := range []string{HomeName, TempName} {
		t.Run(name+" device", func(t *testing.T) {
			before := authorityFDCensus(t)
			file, _, err := openPrivateDirectoryAt(int(directory.Fd()), name, wrongDevice)
			if !errors.Is(err, ErrWorker) || file != nil {
				t.Fatalf("capture=%v err=%v", file, err)
			}
			if after := authorityFDCensus(t); !reflect.DeepEqual(after, before) {
				t.Fatalf("FD leak before=%v after=%v", before, after)
			}
		})
	}
	for _, item := range []struct {
		name    string
		maximum int
	}{{AttemptTokenName, 32}} {
		t.Run(item.name+" device", func(t *testing.T) {
			before := authorityFDCensus(t)
			file, _, _, _, err := openPrivateFile(int(directory.Fd()), item.name, item.maximum, wrongDevice)
			if !errors.Is(err, ErrWorker) || file != nil {
				t.Fatalf("capture=%v err=%v", file, err)
			}
			if after := authorityFDCensus(t); !reflect.DeepEqual(after, before) {
				t.Fatalf("FD leak before=%v after=%v", before, after)
			}
		})
	}
	closed, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := int(closed.Fd())
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{HomeName, TempName, AttemptTokenName} {
		t.Run(name+" open", func(t *testing.T) {
			if name == HomeName || name == TempName {
				if file, _, err := openPrivateDirectoryAt(raw, name, rootID.Device); !errors.Is(err, ErrWorker) || file != nil {
					t.Fatalf("closed-parent directory capture=%v err=%v", file, err)
				}
			} else {
				if file, _, _, _, err := openPrivateFile(raw, name, 32, rootID.Device); !errors.Is(err, ErrWorker) || file != nil {
					t.Fatalf("closed-parent file capture=%v err=%v", file, err)
				}
			}
		})
	}
}

func TestRuntimeDevicePredicateRequiresExactRootDevice(t *testing.T) {
	if err := runtimeDevice(runnerIdentityFixture(7, 9), 7); err != nil {
		t.Fatal(err)
	}
	for _, device := range []uint64{0, 6, 8} {
		if err := runtimeDevice(runnerIdentityFixture(7, 9), device); !errors.Is(err, ErrWorker) {
			t.Fatalf("root device %d accepted: %v", device, err)
		}
	}
}

func runnerIdentityFixture(device, inode uint64) runner.FileIdentity {
	return runner.FileIdentity{Device: device, Inode: inode}
}

func authorityFDCensus(t testing.TB) map[int][2]uint64 {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := map[int][2]uint64{}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil {
			result[fd] = [2]uint64{uint64(stat.Dev), stat.Ino}
		}
	}
	return result
}
