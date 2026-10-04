//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestOpenRecoveredRuntimeValidatesPopulatedEvidenceWithoutMutation(t *testing.T) {
	before := openFDCensus(t)
	parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
	t.Cleanup(func() { _ = parent.Close() })
	if _, err := os.Lstat(filepath.Join(path, runner.InnerActivationMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly retained inner marker: %v", err)
	}
	recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if second, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity); !errors.Is(err, errRuntimeBusy) || second != nil {
		releaseUnexpectedRecovered(second)
		t.Fatalf("concurrent recovery = %+v, %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(path, attemptTokenName)); err != nil {
		t.Fatalf("rejected validation changed token: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
	if err != nil {
		t.Fatalf("repeat open = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	assertFDCensus(t, before)
}

func TestOpenRecoveredRuntimeAcceptsRootBeforeTokenPublicationWithoutMutation(t *testing.T) {
	beforeFDs := openFDCensus(t)
	parentPath := filepath.Join(runtimeTempDir(t), "private")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := createManagedParent(t, parentPath)
	runtime, err := CreateRuntime(parent, runtimeTestName)
	if err != nil {
		t.Fatal(err)
	}
	path, identity := mustRuntimeValues(t, runtime)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, runtimeRetainedSourceName, "22222222222222222222222222222222"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := snapshotRuntimeGraph(t, path)
	recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if after := snapshotRuntimeGraph(t, path); after != before {
		t.Fatalf("root-only recovery mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	assertFDCensus(t, beforeFDs)
}

func TestOpenRecoveredRuntimeAcceptsExactCrashCutsWithoutConfigurationPath(t *testing.T) {
	tests := []struct {
		name    string
		residue []string
	}{
		{name: "outer", residue: []string{runner.OuterActivationMarkerName}},
		{name: "gate scratch", residue: []string{runner.OuterActivationMarkerName, runner.GateConfigScratchName}},
		{name: "activated", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName}},
		{name: "terminal", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.TerminalSpoolName}},
		{name: "terminal scratch", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.TerminalScratchName}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeFDs := openFDCensus(t)
			parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
			configureRecoveredResidue(t, path, test.residue)
			before := snapshotRuntimeGraph(t, path)
			recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
			if err != nil {
				t.Fatal(err)
			}
			if err := recovered.Close(); err != nil {
				t.Fatal(err)
			}
			if after := snapshotRuntimeGraph(t, path); after != before {
				t.Fatalf("recovery mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if err := parent.Close(); err != nil {
				t.Fatal(err)
			}
			assertFDCensus(t, beforeFDs)
		})
	}
}

func TestOpenRecoveredRuntimeRejectsMalformedCensusAndReplacement(t *testing.T) {
	mutations := map[string]func(*testing.T, string){
		"missing home": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, runtimeHomeName)); err != nil {
				t.Fatal(err)
			}
		},
		"extra": func(t *testing.T, path string) {
			if err := os.WriteFile(filepath.Join(path, ".git"), []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"config without token": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, attemptTokenName)); err != nil {
				t.Fatal(err)
			}
		},
		"inner without outer": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, runner.OuterActivationMarkerName)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(path, runner.TerminalSpoolName)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, runner.InnerActivationMarkerName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"terminal without outer": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, runner.OuterActivationMarkerName)); err != nil {
				t.Fatal(err)
			}
		},
		"terminal scratch with spool": func(t *testing.T, path string) {
			if err := os.WriteFile(filepath.Join(path, runner.TerminalScratchName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"both gate scratch names": func(t *testing.T, path string) {
			for _, name := range []string{runner.GateConfigScratchName, runner.GateStdinScratchName} {
				if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		},
		"token mode": func(t *testing.T, path string) {
			if err := os.Chmod(filepath.Join(path, attemptTokenName), 0o640); err != nil {
				t.Fatal(err)
			}
		},
		"token hardlink": func(t *testing.T, path string) {
			if err := os.Link(filepath.Join(path, attemptTokenName), filepath.Join(path, "token-link")); err != nil {
				t.Fatal(err)
			}
		},
		"token symlink": func(t *testing.T, path string) {
			if err := os.Rename(filepath.Join(path, attemptTokenName), filepath.Join(path, "token-old")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("token-old", filepath.Join(path, attemptTokenName)); err != nil {
				t.Fatal(err)
			}
		},
		"terminal fifo": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, runner.TerminalSpoolName)); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(filepath.Join(path, runner.TerminalSpoolName), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"missing lifetime": func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, runner.RuntimeLifetimeLeaseName)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
			defer parent.Close()
			mutate(t, path)
			before := snapshotRuntimeGraph(t, path)
			if recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity); !errors.Is(err, errInvalidContract) || recovered != nil {
				releaseUnexpectedRecovered(recovered)
				t.Fatalf("malformed open = %+v, %v", recovered, err)
			}
			if after := snapshotRuntimeGraph(t, path); after != before {
				t.Fatalf("rejected open mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}

	t.Run("wrong root identity", func(t *testing.T) {
		parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
		defer parent.Close()
		identity.Inode++
		before := snapshotRuntimeGraph(t, path)
		if recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity); !errors.Is(err, errInvalidContract) || recovered != nil {
			releaseUnexpectedRecovered(recovered)
			t.Fatalf("wrong identity open = %+v, %v", recovered, err)
		}
		if after := snapshotRuntimeGraph(t, path); after != before {
			t.Fatal("wrong identity open mutated graph")
		}
	})

	t.Run("root replacement after descriptor open", func(t *testing.T) {
		parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
		defer parent.Close()
		moved := path + ".old"
		recovered, err := openRecoveredRuntime(context.Background(), parent, runtimeTestName, identity, func() {
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, errInvalidContract) || recovered != nil {
			releaseUnexpectedRecovered(recovered)
			t.Fatalf("root replacement open = %+v, %v", recovered, err)
		}
		if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("replacement changed: %+v, %v", info, statErr)
		}
		if _, statErr := os.Lstat(moved); statErr != nil {
			t.Fatalf("opened evidence changed: %v", statErr)
		}
	})
}

// releaseUnexpectedRecovered closes a handle a reject-path assertion did not
// expect to receive. RuntimeParent.Close blocks while any child is open, so
// leaving one behind turns a named assertion failure into a package timeout —
// the regression is still caught, but the signal naming it is destroyed.
func releaseUnexpectedRecovered(recovered *RecoveredRuntime) {
	if recovered != nil {
		_ = recovered.Close()
	}
}

func TestRecoveredRuntimeCensusGrammar(t *testing.T) {
	tests := []struct {
		name    string
		residue []string
		open    bool
	}{
		{name: "no residue", open: true},
		{name: "outer-start gate config", residue: []string{runner.GateConfigScratchName}, open: true},
		{name: "outer-start gate stdin", residue: []string{runner.GateStdinScratchName}, open: true},
		{name: "outer only", residue: []string{runner.OuterActivationMarkerName}, open: true},
		{name: "outer plus inner-start gate config", residue: []string{runner.OuterActivationMarkerName, runner.GateConfigScratchName}, open: true},
		{name: "outer plus inner", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName}, open: true},
		{name: "outer plus terminal scratch", residue: []string{runner.OuterActivationMarkerName, runner.TerminalScratchName}, open: true},
		{name: "outer plus inner plus terminal scratch", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.TerminalScratchName}, open: true},
		{name: "outer plus terminal spool", residue: []string{runner.OuterActivationMarkerName, runner.TerminalSpoolName}, open: true},
		{name: "outer plus inner plus terminal spool", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.TerminalSpoolName}, open: true},
		{name: "inner without outer", residue: []string{runner.InnerActivationMarkerName}},
		{name: "terminal scratch without outer", residue: []string{runner.TerminalScratchName}},
		{name: "terminal spool without outer", residue: []string{runner.TerminalSpoolName}},
		{name: "gate config and stdin", residue: []string{runner.GateConfigScratchName, runner.GateStdinScratchName}},
		{name: "gate config with inner", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.GateConfigScratchName}},
		{name: "gate stdin with inner", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.GateStdinScratchName}},
		{name: "gate config with terminal scratch", residue: []string{runner.OuterActivationMarkerName, runner.GateConfigScratchName, runner.TerminalScratchName}},
		{name: "gate config with terminal spool", residue: []string{runner.OuterActivationMarkerName, runner.GateConfigScratchName, runner.TerminalSpoolName}},
		{name: "gate stdin with terminal scratch", residue: []string{runner.OuterActivationMarkerName, runner.GateStdinScratchName, runner.TerminalScratchName}},
		{name: "gate stdin with terminal spool", residue: []string{runner.OuterActivationMarkerName, runner.GateStdinScratchName, runner.TerminalSpoolName}},
		{name: "gate stdin with outer", residue: []string{runner.OuterActivationMarkerName, runner.GateStdinScratchName}},
		{name: "terminal scratch and spool", residue: []string{runner.OuterActivationMarkerName, runner.TerminalScratchName, runner.TerminalSpoolName}},
		// The artifact writer is the outer attempt-runner target, so a result
		// implies the outer marker and excludes any pre-exec gate scratch.
		{name: "outer plus result", residue: []string{runner.OuterActivationMarkerName, runner.AttemptResultSpoolName}, open: true},
		{name: "outer plus inner plus result", residue: []string{runner.OuterActivationMarkerName, runner.InnerActivationMarkerName, runner.AttemptResultSpoolName}, open: true},
		{name: "result without outer", residue: []string{runner.AttemptResultSpoolName}},
		{name: "result with gate config", residue: []string{runner.OuterActivationMarkerName, runner.GateConfigScratchName, runner.AttemptResultSpoolName}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
			defer parent.Close()
			configureRecoveredResidue(t, path, test.residue)
			before := snapshotRuntimeGraph(t, path)
			recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
			if test.open {
				if err != nil || recovered == nil {
					releaseUnexpectedRecovered(recovered)
					t.Fatalf("reachable residue rejected: recovered=%+v err=%v", recovered, err)
				}
				if err := recovered.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, errInvalidContract) || recovered != nil {
				releaseUnexpectedRecovered(recovered)
				t.Fatalf("malformed residue accepted: recovered=%+v err=%v", recovered, err)
			}
			if after := snapshotRuntimeGraph(t, path); after != before {
				t.Fatalf("rejected residue mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func configureRecoveredResidue(t *testing.T, path string, residue []string) {
	t.Helper()
	all := []string{
		runner.OuterActivationMarkerName, runner.InnerActivationMarkerName,
		runner.GateConfigScratchName, runner.GateStdinScratchName,
		runner.TerminalScratchName, runner.TerminalSpoolName,
		runner.AttemptResultSpoolName,
	}
	for _, name := range all {
		if err := os.Remove(filepath.Join(path, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for _, name := range residue {
		var body []byte
		if name == runner.TerminalSpoolName {
			body = []byte("{}\n")
		}
		if err := os.WriteFile(filepath.Join(path, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecoveredResultArtifactSizeBound(t *testing.T) {
	// The publish contract bounds the canonical body at 1 KiB. A torn publish
	// may be shorter; anything past the bound is not a result this producer
	// could have written, so the census refuses it before authentication.
	tests := []struct {
		name string
		size int
		open bool
	}{
		{name: "torn empty", size: 0, open: true},
		{name: "at bound", size: 1024, open: true},
		{name: "past bound", size: 1025},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent, path, identity := populatedRecoveredRuntime(t, runtimeTestName)
			defer parent.Close()
			configureRecoveredResidue(t, path, []string{runner.OuterActivationMarkerName})
			artifact := filepath.Join(path, runner.AttemptResultSpoolName)
			if err := os.WriteFile(artifact, bytes.Repeat([]byte{'r'}, test.size), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotRuntimeGraph(t, path)
			recovered, err := OpenRecoveredRuntime(context.Background(), parent, runtimeTestName, identity)
			if test.open {
				if err != nil || recovered == nil {
					releaseUnexpectedRecovered(recovered)
					t.Fatalf("bounded artifact rejected: recovered=%+v err=%v", recovered, err)
				}
				if err := recovered.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, errInvalidContract) || recovered != nil {
				releaseUnexpectedRecovered(recovered)
				t.Fatalf("oversized artifact accepted: recovered=%+v err=%v", recovered, err)
			}
			if after := snapshotRuntimeGraph(t, path); after != before {
				t.Fatalf("rejected artifact mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestRecoveredRuntimeFilePolicyRejectsEveryAuthorityMutation(t *testing.T) {
	base := unix.Stat_t{Dev: 1, Ino: 2, Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid()), Mode: unix.S_IFREG | 0o600, Nlink: 1, Size: 32}
	mutations := map[string]func(*unix.Stat_t){
		"device":     func(stat *unix.Stat_t) { stat.Dev++ },
		"inode":      func(stat *unix.Stat_t) { stat.Ino = 0 },
		"owner":      func(stat *unix.Stat_t) { stat.Uid++ },
		"type":       func(stat *unix.Stat_t) { stat.Mode = unix.S_IFDIR | 0o700 },
		"mode":       func(stat *unix.Stat_t) { stat.Mode = unix.S_IFREG | 0o640 },
		"link count": func(stat *unix.Stat_t) { stat.Nlink++ },
		"size":       func(stat *unix.Stat_t) { stat.Size++ },
	}
	if !validRecoveredRuntimeFile(attemptTokenName, base, 1) {
		t.Fatal("valid token metadata rejected")
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if validRecoveredRuntimeFile(attemptTokenName, changed, 1) {
				t.Fatal("authority mutation accepted")
			}
		})
	}
}

func populatedRecoveredRuntime(t *testing.T, basename string) (*RuntimeParent, string, runner.FileIdentity) {
	t.Helper()
	parentPath := filepath.Join(runtimeTempDir(t), "private")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := createManagedParent(t, parentPath)
	runtime, err := CreateRuntime(parent, basename)
	if err != nil {
		parent.Close()
		t.Fatal(err)
	}
	path, identity := mustRuntimeValues(t, runtime)
	token := [32]byte{1, 2, 3, 4}
	if _, err := runtime.PublishAttemptToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	dir, lifetime, err := runtime.DuplicateRunnerFiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, runner.TerminalSpoolName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, runner.OuterActivationMarkerName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifetime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	return parent, path, identity
}
