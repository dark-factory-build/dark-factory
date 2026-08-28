//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestOpenRecoveredRuntimeValidatesPopulatedEvidenceWithoutMutation(t *testing.T) {
	before := openFDCensus(t)
	parent, path, identity, token, config, terminal := populatedRecoveredRuntime(t, "run")
	t.Cleanup(func() { _ = parent.Close() })
	if _, err := os.Lstat(filepath.Join(path, runner.InnerActivationMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly retained inner marker: %v", err)
	}
	recovered, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity)
	if err != nil {
		t.Fatal(err)
	}
	digest := attemptDigestForToken(t, token)
	evidence, err := recovered.InspectEvidence(context.Background(), digest, &config, true)
	if err != nil || !evidence.AttemptToken || !evidence.WorkerConfig || evidence.Terminal == nil || evidence.Terminal.Terminal != terminal {
		t.Fatalf("evidence = %+v, %v", evidence, err)
	}
	if second, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity); !errors.Is(err, errRuntimeBusy) || second != nil {
		t.Fatalf("concurrent recovery = %+v, %v", second, err)
	}
	wrongDigest, _ := kernel.AttemptDigestFromBytes(bytes.Repeat([]byte{0xee}, kernel.DigestBytes))
	if _, err := recovered.InspectEvidence(context.Background(), wrongDigest, &config, true); !errors.Is(err, errInvalidContract) {
		t.Fatalf("wrong token digest = %v", err)
	}
	wrongConfig := config
	wrongConfig.RepositoryRoot = "/private/not-opened"
	if _, err := recovered.InspectEvidence(context.Background(), digest, &wrongConfig, true); !errors.Is(err, errInvalidContract) {
		t.Fatalf("wrong config = %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, changeworker.AttemptTokenName)); err != nil {
		t.Fatalf("rejected validation changed token: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity)
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
			parent, path, identity, _, _, _ := populatedRecoveredRuntime(t, "run")
			defer parent.Close()
			mutate(t, path)
			before := snapshotRuntimeGraph(t, path)
			if recovered, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity); !errors.Is(err, errInvalidContract) || recovered != nil {
				t.Fatalf("malformed open = %+v, %v", recovered, err)
			}
			if after := snapshotRuntimeGraph(t, path); after != before {
				t.Fatalf("rejected open mutated graph\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}

	t.Run("wrong root identity", func(t *testing.T) {
		parent, path, identity, _, _, _ := populatedRecoveredRuntime(t, "run")
		defer parent.Close()
		identity.Inode++
		before := snapshotRuntimeGraph(t, path)
		if recovered, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity); !errors.Is(err, errInvalidContract) || recovered != nil {
			t.Fatalf("wrong identity open = %+v, %v", recovered, err)
		}
		if after := snapshotRuntimeGraph(t, path); after != before {
			t.Fatal("wrong identity open mutated graph")
		}
	})

	t.Run("root replacement after descriptor open", func(t *testing.T) {
		parent, path, identity, _, _, _ := populatedRecoveredRuntime(t, "run")
		defer parent.Close()
		moved := path + ".old"
		recovered, err := openRecoveredRuntime(context.Background(), parent, "run", identity, func() {
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, errInvalidContract) || recovered != nil {
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

func TestRecoveredRuntimeTokenOnlyCutAndSnapshotReplacement(t *testing.T) {
	parentPath := filepath.Join(runtimeTempDir(t), "private")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := createManagedParent(t, parentPath)
	defer parent.Close()
	runtime, err := CreateRuntime(parent, "run")
	if err != nil {
		t.Fatal(err)
	}
	identity := mustRuntimeIdentity(t, runtime)
	token := [32]byte{7}
	if _, err := runtime.PublishAttemptToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	path := mustRuntimePath(t, runtime)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenRecoveredRuntime(context.Background(), parent, "run", identity)
	if err != nil {
		t.Fatal(err)
	}
	digest := attemptDigestForToken(t, token)
	evidence, err := recovered.InspectEvidence(context.Background(), digest, nil, false)
	if err != nil || !evidence.AttemptToken || evidence.WorkerConfig || evidence.Terminal != nil {
		t.Fatalf("token-only evidence = %+v, %v", evidence, err)
	}
	if _, err := recovered.InspectEvidence(context.Background(), digest, nil, true); !errors.Is(err, errInvalidContract) {
		t.Fatalf("bound runner accepted missing config = %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, runner.OuterActivationMarkerName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.InspectEvidence(context.Background(), digest, nil, false); !errors.Is(err, errInvalidContract) {
		t.Fatalf("post-open census extension = %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
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

func TestRecoveredRuntimeAcknowledgesOnlyExactDurableTerminalPostcondition(t *testing.T) {
	runID, err := kernel.RunIDFromBytes(bytes.Repeat([]byte{0x42}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T) (*RuntimeParent, string, *RecoveredRuntime, *runner.TerminalRecord, kernel.Run, kernel.Resource, kernel.Resource, kernel.Resource) {
		parent, path, identity, token, config, _ := populatedRecoveredRuntime(t, runID.String())
		recovered, err := OpenRecoveredRuntime(context.Background(), parent, runID.String(), identity)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := recovered.InspectEvidence(context.Background(), attemptDigestForToken(t, token), &config, true)
		if err != nil || evidence.Terminal == nil {
			t.Fatalf("terminal evidence = %+v, %v", evidence, err)
		}
		when, _ := kernel.NewUnixMillis(90)
		exit, _ := kernel.NewProcessExitCode(1, 0, when)
		run := kernel.Run{ID: runID, Phase: kernel.RunFinalizing, ProviderExit: &exit, CredentialRevokedAt: &when, FinalizingAt: &when}
		processIdentity, err := processResourceIdentity(evidence.Terminal.Terminal.Process)
		if err != nil {
			t.Fatal(err)
		}
		activated, released := when, when
		runtimeIdentity, err := pathResourceIdentity(identity)
		if err != nil {
			t.Fatal(err)
		}
		runtimeRoot := kernel.Resource{RunID: runID, Kind: kernel.ResourceRuntimeRoot, State: kernel.ResourceReleasing, Path: path, Identity: runtimeIdentity, ActivatedAt: &activated}
		process := kernel.Resource{RunID: runID, Kind: kernel.ResourceProviderProcess, State: kernel.ResourceReleased, Identity: processIdentity, ActivatedAt: &activated, ReleasedAt: &released}
		group := process
		group.Kind = kernel.ResourceProviderGroup
		return parent, path, recovered, evidence.Terminal, run, runtimeRoot, process, group
	}

	t.Run("same semantic exit ignores observation time", func(t *testing.T) {
		parent, path, recovered, record, run, runtimeRoot, process, group := newFixture(t)
		defer parent.Close()
		if !terminalCommitProven(path, recovered.runtime.identity, record.Terminal, run, runtimeRoot, process, group) {
			t.Fatal("durable semantic postcondition rejected")
		}
		if _, err := os.Stat(filepath.Join(path, runner.TerminalSpoolName)); err != nil {
			t.Fatal(err)
		}
		rootIdentity := recovered.runtime.identity
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}
		recovered, err = OpenRecoveredRuntime(context.Background(), parent, runID.String(), rootIdentity)
		if err != nil {
			t.Fatal(err)
		}
		defer recovered.Close()
		if err := recovered.AcknowledgeTerminal(record, run, runtimeRoot, process, group); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(path, runner.TerminalSpoolName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ack retained terminal: %v", err)
		}
	})

	mutations := map[string]func(*kernel.Run, *kernel.Resource, *kernel.Resource, *kernel.Resource){
		"sequence": func(run *kernel.Run, _, _, _ *kernel.Resource) {
			exit, _ := kernel.NewProcessExitCode(2, 0, run.ProviderExit.At())
			run.ProviderExit = &exit
		},
		"code": func(run *kernel.Run, _, _, _ *kernel.Resource) {
			exit, _ := kernel.NewProcessExitCode(1, 7, run.ProviderExit.At())
			run.ProviderExit = &exit
		},
		"kind": func(run *kernel.Run, _, _, _ *kernel.Resource) {
			exit, _ := kernel.NewProcessExitSignal(1, 15, run.ProviderExit.At())
			run.ProviderExit = &exit
		},
		"recovered absence": func(run *kernel.Run, _, _, _ *kernel.Resource) {
			exit, _ := kernel.NewProcessExitRecoveredAbsence(1, run.ProviderExit.At())
			run.ProviderExit = &exit
		},
		"wrong runtime binding": func(_ *kernel.Run, runtimeRoot, _, _ *kernel.Resource) { runtimeRoot.Path += ".other" },
		"released runtime root": func(_ *kernel.Run, runtimeRoot, _, _ *kernel.Resource) {
			runtimeRoot.State = kernel.ResourceReleased
		},
		"unreleased process": func(_ *kernel.Run, _, process, _ *kernel.Resource) { process.State = kernel.ResourceReleasing },
		"wrong group identity": func(_ *kernel.Run, _, _, group *kernel.Resource) {
			group.Identity = kernel.EmptyResourceIdentity()
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			parent, path, recovered, record, run, runtimeRoot, process, group := newFixture(t)
			defer parent.Close()
			defer recovered.Close()
			mutate(&run, &runtimeRoot, &process, &group)
			if err := recovered.AcknowledgeTerminal(record, run, runtimeRoot, process, group); !errors.Is(err, errInvalidContract) {
				t.Fatalf("contradictory proof = %v", err)
			}
			loaded, err := runner.LoadTerminal(recovered.runtime.dir, runner.TerminalSpoolName)
			if err != nil || loaded.Digest != record.Digest {
				t.Fatalf("contradictory proof changed spool at %s: %+v, %v", path, loaded, err)
			}
		})
	}

	t.Run("forged terminal binding", func(t *testing.T) {
		for name, forge := range map[string]func(*runner.TerminalRecord){
			"attempt": func(record *runner.TerminalRecord) { record.Terminal.AttemptID = "other" },
			"process": func(record *runner.TerminalRecord) { record.Terminal.Process.PID++ },
		} {
			t.Run(name, func(t *testing.T) {
				parent, _, recovered, record, run, runtimeRoot, process, group := newFixture(t)
				defer parent.Close()
				defer recovered.Close()
				forged := *record
				forge(&forged)
				if err := recovered.AcknowledgeTerminal(&forged, run, runtimeRoot, process, group); !errors.Is(err, errInvalidContract) {
					t.Fatalf("forged terminal proof = %v", err)
				}
				loaded, err := runner.LoadTerminal(recovered.runtime.dir, runner.TerminalSpoolName)
				if err != nil || loaded.Digest != record.Digest {
					t.Fatalf("forged terminal changed spool: %+v, %v", loaded, err)
				}
			})
		}
	})

	t.Run("spool replacement", func(t *testing.T) {
		parent, path, recovered, record, run, runtimeRoot, process, group := newFixture(t)
		defer parent.Close()
		defer recovered.Close()
		moved := filepath.Join(filepath.Dir(path), "old-terminal")
		if err := os.Rename(filepath.Join(path, runner.TerminalSpoolName), moved); err != nil {
			t.Fatal(err)
		}
		dir := openDirectory(t, path)
		replacementTerminal := record.Terminal
		replacementTerminal.Message = "replacement"
		replacement, err := runner.PublishTerminal(dir, runner.TerminalSpoolName, replacementTerminal)
		closeErr := dir.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("replacement publish = %v, close = %v", err, closeErr)
		}
		if err := recovered.AcknowledgeTerminal(record, run, runtimeRoot, process, group); !errors.Is(err, errInvalidContract) {
			t.Fatalf("replacement spool ack = %v", err)
		}
		loaded, err := runner.LoadTerminal(recovered.runtime.dir, runner.TerminalSpoolName)
		if err != nil || loaded.Identity != replacement.Identity || loaded.Digest != replacement.Digest {
			t.Fatalf("replacement spool changed: %+v, %v", loaded, err)
		}
	})
}

func populatedRecoveredRuntime(t *testing.T, basename string) (*RuntimeParent, string, runner.FileIdentity, [32]byte, changeworker.Config, runner.Terminal) {
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
	config := workerConfigForRuntime(t, runtime)
	if _, err := runtime.PublishWorkerConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	dir, lifetime, err := runtime.DuplicateRunnerFiles()
	if err != nil {
		t.Fatal(err)
	}
	terminal := runner.Terminal{AttemptID: basename, Process: runner.Identity{PID: 22, PGID: 22, Birth: runner.Birth{Seconds: 3, Microseconds: 4}}, Exit: runner.Exit{Code: 0}, Message: "private"}
	if _, err := runner.PublishTerminal(dir, runner.TerminalSpoolName, terminal); err != nil {
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
	return parent, path, identity, token, config, terminal
}

func attemptDigestForToken(t *testing.T, token [32]byte) kernel.AttemptDigest {
	t.Helper()
	digest := sha256.Sum256(token[:])
	result, err := kernel.AttemptDigestFromBytes(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return result
}
