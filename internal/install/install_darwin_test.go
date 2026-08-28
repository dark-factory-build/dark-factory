//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
)

func TestInitPublishesExactHomeAndReplaysReadOnly(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	first, err := Init(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != Published {
		t.Fatalf("first state = %d, want published", first.State)
	}
	for _, name := range []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName} {
		if _, err := os.Lstat(filepath.Join(home, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	assertInstallMode(t, home, 0o700, true)
	for _, name := range []string{formatName, databaseName, tokenName, lockName} {
		assertInstallMode(t, filepath.Join(home, name), 0o600, false)
	}
	for _, name := range []string{runtimesName, changesName} {
		assertInstallMode(t, filepath.Join(home, name), 0o700, true)
		entries, err := os.ReadDir(filepath.Join(home, name))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s entries = %v, err=%v", name, entries, err)
		}
	}
	format, err := os.ReadFile(filepath.Join(home, formatName))
	if err != nil || !bytes.Equal(format, []byte(formatBytes)) {
		t.Fatalf("format = %q, err=%v", format, err)
	}
	token, err := os.ReadFile(filepath.Join(home, tokenName))
	if err != nil || len(token) != 32 || bytes.Equal(token, make([]byte, 32)) {
		t.Fatalf("token length/entropy invalid: len=%d err=%v", len(token), err)
	}
	database, err := os.Open(filepath.Join(home, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	info, err := database.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InspectPristine(context.Background(), database, info.Size()); err != nil {
		t.Fatalf("pristine database = %v", err)
	}
	_ = database.Close()
	before := installDigest(t, home)
	second, err := Init(context.Background(), home)
	if err != nil || second.State != Ready {
		t.Fatalf("second init = %+v, err=%v", second, err)
	}
	if after := installDigest(t, home); after != before {
		t.Fatal("ready replay changed home")
	}
	doctorBefore := installDigest(t, home)
	if result, err := Doctor(context.Background(), home); err != nil || result.State != Ready {
		t.Fatalf("doctor = %+v, err=%v", result, err)
	}
	if after := installDigest(t, home); after != doctorBefore {
		t.Fatal("doctor changed exact home")
	}
}

func TestInitRefusesStageAlongsideReadyHome(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, ".home"+stageSuffix)
	if err := os.WriteFile(stage, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted ready home with stage evidence")
	}
}

func TestInitRefusesStageAndPartialHomeUnchanged(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	if err := os.WriteFile(stage, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := installDigest(t, stage)
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted pre-existing stage")
	}
	if after := installDigest(t, stage); after != before {
		t.Fatal("stage changed after refusal")
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted partial home")
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
}

func TestPreexistingStageObjectTypesRefusedUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		setup func(parent, stage string) error
	}{
		{name: "file", setup: func(parent, stage string) error {
			return os.WriteFile(stage, []byte("stage evidence"), 0o600)
		}},
		{name: "directory", setup: func(parent, stage string) error {
			if err := os.Mkdir(stage, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(stage, "evidence"), []byte("stage evidence"), 0o600)
		}},
		{name: "symlink", setup: func(parent, stage string) error {
			target := filepath.Join(parent, "stage-target")
			if err := os.WriteFile(target, []byte("stage evidence"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, stage)
		}},
		{name: "hardlink", setup: func(parent, stage string) error {
			target := filepath.Join(parent, "stage-target")
			if err := os.WriteFile(target, []byte("stage evidence"), 0o600); err != nil {
				return err
			}
			return os.Link(target, stage)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			if err := test.setup(parent, stage); err != nil {
				t.Fatal(err)
			}
			before := installDigest(t, parent)
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
				{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					if err := operation.call(); err == nil {
						t.Fatalf("%s accepted pre-existing %s stage", operation.name, test.name)
					}
					if after := installDigest(t, parent); after != before {
						t.Fatalf("%s changed pre-existing %s stage", operation.name, test.name)
					}
					if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("final home after pre-existing %s stage: %v", test.name, err)
					}
				})
			}
		})
	}
}

func TestInitSecondStageInspectionRejectsMemberMutation(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	phaseHook = func(point phase) error {
		if point == phaseAfterStageInspect {
			return os.WriteFile(filepath.Join(stage, formatName), []byte("tampered\n"), 0o600)
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted a stage changed between inspections")
	} else if errors.Is(err, ErrUncertain) {
		t.Fatalf("pre-publication mutation became uncertain: %v", err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after rejected stage mutation: %v", err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("stage evidence disappeared: %v", err)
	}
}

func TestFinalStageInspectionRejectsValidMemberAndCensusMutations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(stage string) error
	}{
		{name: "token overwrite", mutate: func(stage string) error {
			return os.WriteFile(filepath.Join(stage, tokenName), bytes.Repeat([]byte{'R'}, 32), 0o600)
		}},
		{name: "member replacement", mutate: func(stage string) error {
			path := filepath.Join(stage, formatName)
			backup := filepath.Join(filepath.Dir(stage), "format-before-replacement")
			if err := os.Rename(path, backup); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(formatBytes), 0o600)
		}},
		{name: "unknown entry", mutate: func(stage string) error {
			return os.WriteFile(filepath.Join(stage, "unexpected"), []byte("evidence"), 0o600)
		}},
		{name: "mode", mutate: func(stage string) error {
			return os.Chmod(filepath.Join(stage, formatName), 0o640)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			phaseHook = func(point phase) error {
				if point == phaseBeforeFinalStage {
					return mutation.mutate(stage)
				}
				return nil
			}
			defer func() { phaseHook = nil }()
			if _, err := Init(context.Background(), home); err == nil {
				t.Fatal("init accepted a stage changed after its first inspection")
			} else if errors.Is(err, ErrUncertain) {
				t.Fatalf("pre-publication mutation became uncertain: %v", err)
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final home after rejected stage mutation: %v", err)
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatalf("stage evidence disappeared: %v", err)
			}
		})
	}
}

func TestPublishedProofRejectsFinalMutationAsUncertain(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	phaseHook = func(point phase) error {
		if point == phaseAfterRename {
			return os.WriteFile(filepath.Join(home, formatName), []byte("tampered\n"), 0o600)
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); !errors.Is(err, ErrUncertain) {
		t.Fatalf("final mutation error = %v, want uncertain", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("published home disappeared after uncertain proof: %v", err)
	}
}

func TestDoctorSecondScanRejectsMemberMutationWithoutRepair(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	before := installDigest(t, home)
	phaseHook = func(point phase) error {
		if point == phaseBeforeDoctorSecond {
			return os.WriteFile(filepath.Join(home, tokenName), bytes.Repeat([]byte{'Z'}, 32), 0o600)
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Doctor(context.Background(), home); !errors.Is(err, ErrUncertain) {
		t.Fatalf("doctor mutation error = %v, want uncertain", err)
	}
	if after := installDigest(t, home); after == before {
		t.Fatal("doctor unexpectedly repaired the mutated home")
	}
}

func TestInitParentSyncFailureLeavesFixedStageEvidence(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	parentStat := parentInfo.Sys().(*syscall.Stat_t)
	originalSync := syncDirectory
	defer func() { syncDirectory = originalSync }()
	calls := 0
	syncDirectory = func(fd int) error {
		calls++
		if calls == 1 {
			var current unix.Stat_t
			if err := unix.Fstat(fd, &current); err != nil {
				t.Fatalf("inspect first sync descriptor: %v", err)
			}
			if uint64(current.Dev) != uint64(parentStat.Dev) || uint64(current.Ino) != uint64(parentStat.Ino) {
				t.Fatalf("first directory sync descriptor is not retained home parent")
			}
		}
		return errors.New("injected parent sync failure")
	}
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted an injected parent sync failure")
	}
	if calls != 1 {
		t.Fatalf("directory sync calls = %d, want one immediate parent sync", calls)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after parent sync failure: %v", err)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("stage evidence after parent sync failure: info=%v err=%v", info, err)
	}
}

func TestDirectorySyncFailureCutsKeepStageOrBecomeUncertain(t *testing.T) {
	for failureCall := 1; failureCall <= 5; failureCall++ {
		t.Run(fmt.Sprintf("directory sync %d", failureCall), func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			originalSync := syncDirectory
			defer func() { syncDirectory = originalSync }()
			calls := 0
			syncDirectory = func(fd int) error {
				calls++
				if calls == failureCall {
					return errors.New("injected directory sync failure")
				}
				return originalSync(fd)
			}
			_, err := Init(context.Background(), home)
			if calls != failureCall {
				t.Fatalf("directory sync calls = %d, want failure call %d", calls, failureCall)
			}
			if failureCall < 5 {
				if err == nil || errors.Is(err, ErrUncertain) {
					t.Fatalf("pre-publication sync error = %v, want definite failure", err)
				}
				if _, statErr := os.Stat(home); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("final home after pre-publication sync failure: %v", statErr)
				}
				if _, statErr := os.Stat(stage); statErr != nil {
					t.Fatalf("stage evidence after pre-publication sync failure: %v", statErr)
				}
			} else {
				if !errors.Is(err, ErrUncertain) {
					t.Fatalf("post-publication sync error = %v, want uncertain", err)
				}
				if _, statErr := os.Stat(home); statErr != nil {
					t.Fatalf("final home after post-publication sync failure: %v", statErr)
				}
				if _, statErr := os.Stat(stage); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("stage after publication: %v", statErr)
				}
			}
		})
	}
}

func TestRegularMemberSyncFailureCutsKeepStageEvidence(t *testing.T) {
	memberNames := []string{formatName, databaseName, tokenName, lockName}
	for callIndex, memberName := range memberNames {
		failureCall := callIndex + 1
		t.Run(memberName, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			originalSync := syncFile
			defer func() { syncFile = originalSync }()
			calls := 0
			syncFile = func(fd int) error {
				calls++
				var stat unix.Stat_t
				if err := unix.Fstat(fd, &stat); err != nil {
					t.Fatalf("inspect member fsync descriptor: %v", err)
				}
				if stat.Mode&unix.S_IFMT != unix.S_IFREG {
					t.Fatalf("member fsync descriptor mode = %o, want regular", stat.Mode&unix.S_IFMT)
				}
				currentName := memberNames[calls-1]
				expected, err := os.Stat(filepath.Join(stage, currentName))
				if err != nil {
					t.Fatalf("stat expected member: %v", err)
				}
				expectedStat := expected.Sys().(*syscall.Stat_t)
				if uint64(stat.Dev) != uint64(expectedStat.Dev) || uint64(stat.Ino) != uint64(expectedStat.Ino) {
					t.Fatalf("member fsync descriptor is not %s", currentName)
				}
				if calls == failureCall {
					return errors.New("injected member fsync failure")
				}
				return originalSync(fd)
			}
			if _, err := Init(context.Background(), home); err == nil {
				t.Fatal("init accepted an injected member fsync failure")
			}
			if calls != failureCall {
				t.Fatalf("member fsync calls = %d, want failure call %d", calls, failureCall)
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final home after member fsync failure: %v", err)
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatalf("stage evidence after member fsync failure: %v", err)
			}
		})
	}
}

func TestConstructionPhaseCutsRetainReachableStagePrefix(t *testing.T) {
	memberNames := []string{formatName, databaseName, tokenName, lockName}
	cases := []struct {
		name  string
		point phase
		check func(*testing.T, string)
	}{
		{name: "after stage parent sync", point: phaseAfterStageParentSync, check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, nil)
		}},
		{name: "after runtimes directory sync", point: phase("after " + runtimesName + " directory sync"), check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, []string{formatName, databaseName, tokenName, lockName, runtimesName})
		}},
		{name: "after changes directory sync", point: phase("after " + changesName + " directory sync"), check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName})
		}},
		{name: "after database pristine proof", point: phase("after database pristine proof"), check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName})
		}},
		{name: "after stage sync", point: phaseAfterStageSync, check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName})
		}},
		{name: "immediately before publish rename", point: phaseBeforePublishRename, check: func(t *testing.T, stage string) {
			assertStageEntries(t, stage, []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName})
		}},
	}
	for memberIndex, memberName := range memberNames {
		name := memberName
		expectedEntries := append([]string(nil), memberNames[:memberIndex+1]...)
		cases = append(cases,
			struct {
				name  string
				point phase
				check func(*testing.T, string)
			}{name: "after " + name + " create", point: phase("after " + name + " create"), check: func(t *testing.T, stage string) {
				assertStageEntries(t, stage, expectedEntries)
				assertStageMemberExists(t, filepath.Join(stage, name))
			}},
			struct {
				name  string
				point phase
				check func(*testing.T, string)
			}{name: "after " + name + " write", point: phase("after " + name + " write"), check: func(t *testing.T, stage string) {
				assertStageEntries(t, stage, expectedEntries)
				assertStageMemberExists(t, filepath.Join(stage, name))
			}},
			struct {
				name  string
				point phase
				check func(*testing.T, string)
			}{name: "after " + name + " fsync", point: phase("after " + name + " fsync"), check: func(t *testing.T, stage string) {
				assertStageEntries(t, stage, expectedEntries)
				assertStageMemberExists(t, filepath.Join(stage, name))
			}},
		)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			hit := false
			phaseHook = func(point phase) error {
				if point == test.point {
					hit = true
					return errors.New("injected construction phase failure")
				}
				return nil
			}
			defer func() { phaseHook = nil }()
			if _, err := Init(context.Background(), home); err == nil {
				t.Fatal("init accepted an injected construction phase failure")
			} else if errors.Is(err, ErrUncertain) {
				t.Fatalf("pre-publication construction failure became uncertain: %v", err)
			}
			if !hit {
				t.Fatalf("phase %q was not invoked", test.point)
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final home after construction failure: %v", err)
			}
			if info, err := os.Stat(stage); err != nil || !info.IsDir() {
				t.Fatalf("stage evidence after construction failure: info=%v err=%v", info, err)
			}
			test.check(t, stage)
		})
	}
}

func TestAfterParentSyncFailureIsExplicitlyUncertain(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	phaseHook = func(point phase) error {
		if point == phaseAfterParentSync {
			return errors.New("injected post-sync proof failure")
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); !errors.Is(err, ErrUncertain) {
		t.Fatalf("post-parent-sync failure = %v, want uncertain", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("final home after post-parent-sync failure: %v", err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage after post-parent-sync failure: %v", err)
	}
}

func assertStageEntries(t *testing.T, stage string, expected []string) {
	t.Helper()
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(expected) {
		t.Fatalf("stage entries = %d, want %d", len(entries), len(expected))
	}
	for _, name := range expected {
		if _, err := os.Lstat(filepath.Join(stage, name)); err != nil {
			t.Fatalf("missing reachable stage member %s: %v", name, err)
		}
	}
}

func assertStageMemberExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("stage member %s is %v, want regular", path, info.Mode())
	}
}

const (
	crashHelperEnv = "DARK_FACTORY_INSTALL_CRASH_HELPER"
	crashHomeEnv   = "DARK_FACTORY_INSTALL_CRASH_HOME"
	crashPhaseEnv  = "DARK_FACTORY_INSTALL_CRASH_PHASE"
	crashSignalEnv = "DARK_FACTORY_INSTALL_CRASH_SIGNAL_FD"
)

func TestInstallCrashHelper(t *testing.T) {
	if os.Getenv(crashHelperEnv) != "1" {
		return
	}
	home := os.Getenv(crashHomeEnv)
	target := phase(os.Getenv(crashPhaseEnv))
	fd, err := strconv.Atoi(os.Getenv(crashSignalEnv))
	if err != nil || fd < 0 || home == "" || target == "" {
		t.Fatalf("invalid crash helper configuration")
	}
	phaseHook = func(point phase) error {
		if point == target {
			_, _ = unix.Write(fd, []byte{1})
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			for {
				runtime.Gosched()
			}
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	_, _ = Init(context.Background(), home)
	t.Fatal("crash helper reached Init return")
}

func TestInitCrashCutsLeaveReopenableEvidence(t *testing.T) {
	cases := []struct {
		name  string
		point phase
		post  bool
	}{
		{name: "after stage parent fsync", point: phaseAfterStageParentSync},
		{name: "after format fsync", point: phase("after " + formatName + " fsync")},
		{name: "after database fsync", point: phase("after " + databaseName + " fsync")},
		{name: "after token fsync", point: phase("after " + tokenName + " fsync")},
		{name: "after lock fsync", point: phase("after " + lockName + " fsync")},
		{name: "after runtimes fsync", point: phase("after " + runtimesName + " directory sync")},
		{name: "after changes fsync", point: phase("after " + changesName + " directory sync")},
		{name: "after stage fsync", point: phaseAfterStageSync},
		{name: "pre-publish rename", point: phaseBeforePublishRename},
		{name: "after rename before parent fsync", point: phaseAfterRename, post: true},
		{name: "after parent fsync before final proof", point: phaseAfterParentSync, post: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			readPipe, writePipe, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run", "^TestInstallCrashHelper$", "-test.v")
			cmd.Env = append(os.Environ(),
				crashHelperEnv+"=1",
				crashHomeEnv+"="+home,
				crashPhaseEnv+"="+string(test.point),
				crashSignalEnv+"=3",
			)
			cmd.ExtraFiles = []*os.File{writePipe}
			if err := cmd.Start(); err != nil {
				_ = readPipe.Close()
				_ = writePipe.Close()
				t.Fatal(err)
			}
			_ = writePipe.Close()
			signalReceived := make(chan error, 1)
			go func() {
				var signal [1]byte
				_, readErr := io.ReadFull(readPipe, signal[:])
				signalReceived <- readErr
			}()
			select {
			case readErr := <-signalReceived:
				_ = readPipe.Close()
				if readErr != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("crash helper did not signal at %q: %v", test.point, readErr)
				}
			case <-time.After(10 * time.Second):
				_ = readPipe.Close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("crash helper did not reach %q", test.point)
			}
			waitErr := cmd.Wait()
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok || exitErr.ProcessState == nil {
				t.Fatalf("crash helper wait error = %v, want SIGKILL", waitErr)
			}
			status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("crash helper status = %v, want SIGKILL", exitErr.ProcessState.Sys())
			}
			if test.post {
				if info, err := os.Stat(home); err != nil || !info.IsDir() {
					t.Fatalf("post-publication final home: info=%v err=%v", info, err)
				}
				if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("post-publication stage = %v, want absent", err)
				}
				result, doctorErr := Doctor(context.Background(), home)
				if doctorErr != nil && !errors.Is(doctorErr, ErrUncertain) {
					t.Fatalf("post-publication doctor = %+v, err=%v", result, doctorErr)
				}
				if doctorErr == nil && result.State != Ready {
					t.Fatalf("post-publication doctor state = %d, want ready", result.State)
				}
				return
			}
			if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-publication final home = %v, want absent", err)
			}
			if info, err := os.Stat(stage); err != nil || !info.IsDir() {
				t.Fatalf("pre-publication stage: info=%v err=%v", info, err)
			}
			if _, err := os.ReadDir(stage); err != nil {
				t.Fatalf("reopen retained stage: %v", err)
			}
		})
	}
}

func TestBoundedSnapshotRejectsOversizedMemberBeforeRead(t *testing.T) {
	parent := installTempDir(t)
	file, err := os.CreateTemp(parent, "member-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := digestMember(context.Background(), file, maxDatabaseBytes+1, 1, maxDatabaseBytes); !errors.Is(err, ErrInvalidHome) {
		t.Fatalf("oversized digest error = %v, want invalid home", err)
	}
}

func TestInitRejectsMemberGrowthBeforeBoundedSnapshot(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	mutated := false
	phaseHook = func(point phase) error {
		if point == phaseBeforeSnapshot && !mutated {
			mutated = true
			return os.Truncate(filepath.Join(stage, databaseName), maxDatabaseBytes+1)
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted an oversized member before bounded snapshot")
	} else if errors.Is(err, ErrUncertain) {
		t.Fatalf("pre-publication oversized member became uncertain: %v", err)
	}
	if !mutated {
		t.Fatal("snapshot mutation hook was not reached")
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after oversized member: %v", err)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("stage evidence after oversized member: info=%v err=%v", info, err)
	}
}

func TestInitNoReplaceConflictLeavesStageAndFinalUnchanged(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	stage := filepath.Join(parent, ".home"+stageSuffix)
	var stageBefore, finalBefore [32]byte
	phaseHook = func(point phase) error {
		if point == phaseBeforeRename {
			stageBefore = installDigest(t, stage)
			if err := os.Mkdir(home, 0o700); err != nil {
				return err
			}
			finalBefore = installDigest(t, home)
			return nil
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init replaced an existing final path")
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("no-replace conflict changed the final path: info=%v err=%v", info, err)
	}
	if after := installDigest(t, stage); after != stageBefore {
		t.Fatal("no-replace conflict changed staged evidence")
	}
	if after := installDigest(t, home); after != finalBefore {
		t.Fatal("no-replace conflict changed final evidence")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("stage evidence after no-replace conflict: %v", err)
	}
}

func TestPublishBoundaryStageRemovalOrReplacementCannotPublish(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, stage string) (string, [32]byte, error)
	}{
		{name: "removal", mutate: func(t *testing.T, stage string) (string, [32]byte, error) {
			if err := os.RemoveAll(stage); err != nil {
				return "", [32]byte{}, err
			}
			return "", [32]byte{}, nil
		}},
		{name: "replacement", mutate: func(t *testing.T, stage string) (string, [32]byte, error) {
			old := stage + ".old"
			if err := os.Rename(stage, old); err != nil {
				return "", [32]byte{}, err
			}
			if err := os.Mkdir(stage, 0o700); err != nil {
				return "", [32]byte{}, err
			}
			return old, installDigest(t, stage), nil
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			var detached string
			var detachedBefore [32]byte
			var replacementBefore [32]byte
			phaseHook = func(point phase) error {
				if point != phaseBeforePublishRename {
					return nil
				}
				var err error
				detached, replacementBefore, err = test.mutate(t, stage)
				if err == nil && detached != "" {
					detachedBefore = installDigest(t, detached)
				}
				return err
			}
			defer func() { phaseHook = nil }()
			if _, err := Init(context.Background(), home); err == nil {
				t.Fatal("init published a removed or replaced stage")
			} else if errors.Is(err, ErrUncertain) {
				t.Fatalf("pre-publication stage replacement became uncertain: %v", err)
			}
			if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final home after stage replacement: %v", err)
			}
			if test.name == "replacement" {
				if after := installDigest(t, detached); after != detachedBefore {
					t.Fatal("detached original stage changed")
				}
				if after := installDigest(t, stage); after != replacementBefore {
					t.Fatal("foreign replacement stage changed")
				}
			} else if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed stage was recreated: %v", err)
			}
		})
	}
}

func TestStableProofBindsReplacedAncestors(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, home string)
	}{
		{
			name: "doctor",
			check: func(t *testing.T, home string) {
				phaseHook = func(point phase) error {
					if point == phaseBeforeDoctorSecond {
						return replaceHomeAncestors(home)
					}
					return nil
				}
				defer func() { phaseHook = nil }()
				if _, err := Doctor(context.Background(), home); !errors.Is(err, ErrUncertain) {
					t.Fatalf("doctor ancestor replacement error = %v, want uncertain", err)
				}
			},
		},
		{
			name: "existing init",
			check: func(t *testing.T, home string) {
				phaseHook = func(point phase) error {
					if point == phaseBeforeExistingSecond {
						return replaceHomeAncestors(home)
					}
					return nil
				}
				defer func() { phaseHook = nil }()
				if _, err := Init(context.Background(), home); !errors.Is(err, ErrUncertain) {
					t.Fatalf("existing init ancestor replacement error = %v, want uncertain", err)
				}
			},
		},
		{
			name: "published init",
			check: func(t *testing.T, home string) {
				phaseHook = func(point phase) error {
					if point == phaseAfterRename {
						return replaceHomeAncestors(home)
					}
					return nil
				}
				defer func() { phaseHook = nil }()
				if _, err := Init(context.Background(), home); !errors.Is(err, ErrUncertain) {
					t.Fatalf("published init ancestor replacement error = %v, want uncertain", err)
				}
				if _, err := os.Stat(home); err != nil {
					t.Fatalf("published home after ancestor replacement: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			outer := filepath.Join(parent, "outer")
			inner := filepath.Join(outer, "inner")
			home := filepath.Join(inner, "home")
			if err := os.MkdirAll(inner, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.name != "published init" {
				if _, err := Init(context.Background(), home); err != nil {
					t.Fatal(err)
				}
			}
			test.check(t, home)
		})
	}
}

func TestStableProofBindsReplacedImmediateParent(t *testing.T) {
	tests := []struct {
		name  string
		phase phase
		call  func(context.Context, string) (Result, error)
	}{
		{name: "doctor", phase: phaseBeforeDoctorSecond, call: func(ctx context.Context, home string) (Result, error) {
			return Doctor(ctx, home)
		}},
		{name: "existing init", phase: phaseBeforeExistingSecond, call: func(ctx context.Context, home string) (Result, error) {
			return Init(ctx, home)
		}},
		{name: "published init", phase: phaseAfterRename, call: func(ctx context.Context, home string) (Result, error) {
			return Init(ctx, home)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			outer := filepath.Join(parent, "outer")
			inner := filepath.Join(outer, "inner")
			home := filepath.Join(inner, "home")
			if err := os.MkdirAll(inner, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.name != "published init" {
				if _, err := Init(context.Background(), home); err != nil {
					t.Fatal(err)
				}
			}
			phaseHook = func(point phase) error {
				if point == test.phase {
					return replaceHomeImmediateParent(home)
				}
				return nil
			}
			defer func() { phaseHook = nil }()
			if _, err := test.call(context.Background(), home); !errors.Is(err, ErrUncertain) {
				t.Fatalf("immediate parent replacement error = %v, want uncertain", err)
			}
		})
	}
}

func replaceHomeImmediateParent(home string) error {
	inner := filepath.Dir(home)
	oldInner := inner + ".old"
	if err := os.Rename(inner, oldInner); err != nil {
		return err
	}
	if err := os.Mkdir(inner, 0o700); err != nil {
		return err
	}
	return os.Rename(filepath.Join(oldInner, filepath.Base(home)), home)
}

func TestPreRenameImmediateParentReplacementRefusesUnchangedFinal(t *testing.T) {
	parent := installTempDir(t)
	outer := filepath.Join(parent, "outer")
	inner := filepath.Join(outer, "inner")
	home := filepath.Join(inner, "home")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(inner, ".home"+stageSuffix)
	oldInner := inner + ".old"
	phaseHook = func(point phase) error {
		if point == phaseBeforeRename {
			return replaceHomeImmediateParent(home)
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted a replaced immediate parent before publication")
	} else if errors.Is(err, ErrUncertain) {
		t.Fatalf("pre-publication immediate parent replacement became uncertain: %v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after immediate parent replacement: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected stage at replacement path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldInner, filepath.Base(stage))); err != nil {
		t.Fatalf("detached stage evidence disappeared: %v", err)
	}
}

func TestPreRenameAncestorReplacementRefusesUnchangedFinal(t *testing.T) {
	parent := installTempDir(t)
	outer := filepath.Join(parent, "outer")
	inner := filepath.Join(outer, "inner")
	home := filepath.Join(inner, "home")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(inner, ".home"+stageSuffix)
	oldOuter := outer + ".old"
	phaseHook = func(point phase) error {
		if point != phaseBeforeRename {
			return nil
		}
		if err := os.Rename(outer, oldOuter); err != nil {
			return err
		}
		if err := os.MkdirAll(inner, 0o700); err != nil {
			return err
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted a replaced ancestor before publication")
	} else if errors.Is(err, ErrUncertain) {
		t.Fatalf("pre-publication ancestor replacement became uncertain: %v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after ancestor replacement: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected stage at replacement path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldOuter, "inner", filepath.Base(stage))); err != nil {
		t.Fatalf("detached stage evidence disappeared: %v", err)
	}
}

func TestSymlinkFinalPathAndMemberRefusedUnchanged(t *testing.T) {
	t.Run("final path", func(t *testing.T) {
		parent := installTempDir(t)
		home := filepath.Join(parent, "home")
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, home); err != nil {
			t.Fatal(err)
		}
		before := installDigest(t, parent)
		for _, operation := range []struct {
			name string
			call func() error
		}{
			{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
			{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
		} {
			t.Run(operation.name, func(t *testing.T) {
				if err := operation.call(); err == nil {
					t.Fatal("accepted final symlink")
				}
				if after := installDigest(t, parent); after != before {
					t.Fatal("final symlink changed after refusal")
				}
			})
		}
	})
	t.Run("member symlink", func(t *testing.T) {
		parent := installTempDir(t)
		home := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), home); err != nil {
			t.Fatal(err)
		}
		member := filepath.Join(home, formatName)
		backup := filepath.Join(parent, "format-target")
		if err := os.Rename(member, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(backup, member); err != nil {
			t.Fatal(err)
		}
		before := installDigest(t, parent)
		for _, operation := range []struct {
			name string
			call func() error
		}{
			{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
			{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
		} {
			t.Run(operation.name, func(t *testing.T) {
				if err := operation.call(); err == nil {
					t.Fatal("accepted member symlink")
				}
				if after := installDigest(t, parent); after != before {
					t.Fatal("member symlink changed after refusal")
				}
			})
		}
	})
}

func TestPathComponentSymlinkRefusedUnchanged(t *testing.T) {
	parent := installTempDir(t)
	actual := filepath.Join(parent, "actual")
	link := filepath.Join(parent, "link")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(link, "home")
	before := installDigest(t, parent)
	for _, operation := range []struct {
		name string
		call func() error
	}{
		{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
		{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); err == nil {
				t.Fatal("accepted path component symlink")
			}
			if after := installDigest(t, parent); after != before {
				t.Fatal("path component symlink changed after refusal")
			}
		})
	}
}

func replaceHomeAncestors(home string) error {
	inner := filepath.Dir(home)
	outer := filepath.Dir(inner)
	oldOuter := outer + ".old"
	if err := os.Rename(outer, oldOuter); err != nil {
		return err
	}
	if err := os.Mkdir(outer, 0o700); err != nil {
		return err
	}
	return os.Rename(filepath.Join(oldOuter, filepath.Base(inner)), inner)
}

func TestInitAndDoctorRejectSpecialModeBitsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		path string
		mode os.FileMode
	}{
		{name: "home", mode: 0o700 | os.ModeSticky},
		{name: formatName, path: formatName, mode: 0o600 | os.ModeSetuid},
		{name: databaseName, path: databaseName, mode: 0o600 | os.ModeSetuid},
		{name: tokenName, path: tokenName, mode: 0o600 | os.ModeSticky},
		{name: lockName, path: lockName, mode: 0o600 | os.ModeSetuid},
		{name: runtimesName, path: runtimesName, mode: 0o700 | os.ModeSticky},
		{name: changesName, path: changesName, mode: 0o700 | os.ModeSticky},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			path := home
			if test.path != "" {
				path = filepath.Join(home, test.path)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
				{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.call()
					if !errors.Is(err, ErrInvalidHome) && !errors.Is(err, ErrUncertain) {
						t.Fatalf("operation error = %v, want invalid or uncertain", err)
					}
					after, statErr := os.Lstat(path)
					if statErr != nil {
						t.Fatal(statErr)
					}
					if after.Mode() != before.Mode() || after.Size() != before.Size() {
						t.Fatalf("operation changed special mode metadata: before=%v after=%v", before.Mode(), after.Mode())
					}
				})
			}
		})
	}
}

func TestExactModeRejectsEverySpecialBit(t *testing.T) {
	for _, bit := range []uint32{0o4000, 0o2000, 0o1000} {
		if exactMode(0o600|bit, 0o600) {
			t.Fatalf("regular special mode %04o was accepted", bit)
		}
		if exactMode(0o700|bit, 0o700) {
			t.Fatalf("directory special mode %04o was accepted", bit)
		}
	}
}

func TestExactOwnerRejectsForeignMetadata(t *testing.T) {
	owner := uint32(os.Geteuid())
	if !exactOwner(owner) {
		t.Fatal("effective owner was rejected")
	}
	if exactOwner(^owner) {
		t.Fatal("foreign owner was accepted")
	}
}

func TestExactDirectoryLinkCountRejectsInvalidMetadata(t *testing.T) {
	for _, nlink := range []uint64{0, 1} {
		if exactDirectoryLinkCount(nlink) {
			t.Fatalf("directory link count %d was accepted", nlink)
		}
	}
	if !exactDirectoryLinkCount(2) {
		t.Fatal("minimum directory link count was rejected")
	}
}

func TestForeignOwnerCorruptionIsRefusedWhenPermitted(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("foreign-owner filesystem mutation requires an effective root test process")
	}
	cases := []struct {
		name string
		path func(parent, home string) string
	}{
		{name: "member", path: func(parent, home string) string { return filepath.Join(home, formatName) }},
		{name: "home", path: func(parent, home string) string { return home }},
		{name: "parent", path: func(parent, home string) string { return parent }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			path := test.path(parent, home)
			if err := os.Chown(path, 1, -1); err != nil {
				t.Fatal(err)
			}
			before := installDigest(t, parent)
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
				{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					if err := operation.call(); err == nil {
						t.Fatal("accepted foreign owner metadata")
					}
					if after := installDigest(t, parent); after != before {
						t.Fatal("foreign owner metadata changed after refusal")
					}
				})
			}
		})
	}
}

func TestHomePathBoundsRefuseBeforeFilesystemTraversal(t *testing.T) {
	tooLong := "/" + strings.Repeat("a", maxHomeBytes)
	if _, err := Init(context.Background(), tooLong); !errors.Is(err, ErrInvalidHome) {
		t.Fatalf("overlong home error = %v, want invalid home", err)
	}
	tooDeep := "/" + strings.Repeat("a/", maxPathDepth+1) + "home"
	if _, err := Init(context.Background(), tooDeep); !errors.Is(err, ErrInvalidHome) {
		t.Fatalf("deep home error = %v, want invalid home", err)
	}
}

func TestConcurrentInitializersPublishAtMostOnce(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	const attempts = 2
	results := make(chan Result, attempts)
	errorsSeen := make(chan error, attempts)
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			result, err := Init(context.Background(), home)
			results <- result
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	published := 0
	for result := range results {
		if result.State == Published {
			published++
		}
	}
	if published > 1 {
		t.Fatalf("concurrent initializers published %d homes", published)
	}
	for err := range errorsSeen {
		if err != nil && errors.Is(err, ErrUncertain) {
			t.Fatalf("concurrent loser became uncertain: %v", err)
		}
	}
	if result, err := Doctor(context.Background(), home); err != nil || result.State != Ready {
		t.Fatalf("concurrent final home = %+v, err=%v", result, err)
	}
}

func TestConcurrentExactReadyInitializersDoNotMutate(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	before := installDigest(t, home)
	results := make(chan Result, 2)
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for range 2 {
		go func() {
			defer wait.Done()
			result, err := Init(context.Background(), home)
			results <- result
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for result := range results {
		if result.State != Ready {
			t.Fatalf("concurrent exact init state = %d, want ready", result.State)
		}
	}
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent exact init error = %v", err)
		}
	}
	if after := installDigest(t, home); after != before {
		t.Fatal("concurrent exact init mutated home")
	}
}

func TestConcurrentPartialAndForeignStageRefusalsAreUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, parent, home, stage string)
	}{
		{name: "partial final", setup: func(t *testing.T, parent, home, stage string) {
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "legacy.db"), []byte("legacy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "foreign stage", setup: func(t *testing.T, parent, home, stage string) {
			if err := os.WriteFile(stage, []byte("foreign stage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "exact final and foreign stage", setup: func(t *testing.T, parent, home, stage string) {
			if _, err := Init(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage, []byte("foreign stage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			stage := filepath.Join(parent, ".home"+stageSuffix)
			test.setup(t, parent, home, stage)
			before := installDigest(t, parent)
			results := make(chan Result, 2)
			errorsSeen := make(chan error, 2)
			var wait sync.WaitGroup
			wait.Add(2)
			for range 2 {
				go func() {
					defer wait.Done()
					result, err := Init(context.Background(), home)
					results <- result
					errorsSeen <- err
				}()
			}
			wait.Wait()
			close(results)
			close(errorsSeen)
			for range results {
				// Every result is expected to be a refusal; the error channel below
				// carries the definitive assertion without relying on zero State.
			}
			for err := range errorsSeen {
				if err == nil {
					t.Fatal("concurrent invalid-home init succeeded")
				}
			}
			if after := installDigest(t, parent); after != before {
				t.Fatal("concurrent invalid-home init changed evidence")
			}
		})
	}
}

func TestRegularMemberHardlinkIsRejectedUnchanged(t *testing.T) {
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "format-alias")
	if err := os.Link(filepath.Join(home, formatName), alias); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(filepath.Join(home, formatName))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		call func() error
	}{
		{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
		{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); !errors.Is(err, ErrInvalidHome) {
				t.Fatalf("%s accepted hard-linked member: %v", operation.name, err)
			}
			after, err := os.Lstat(filepath.Join(home, formatName))
			if err != nil {
				t.Fatal(err)
			}
			if after.Sys().(*syscall.Stat_t).Ino != before.Sys().(*syscall.Stat_t).Ino || after.Sys().(*syscall.Stat_t).Nlink != 2 {
				t.Fatalf("hardlink evidence changed: before=%+v after=%+v", before.Sys(), after.Sys())
			}
		})
	}
}

func TestDoctorRejectsCorruptionsWithoutMutation(t *testing.T) {
	corruptions := []struct {
		name   string
		mutate func(home string) error
	}{
		{name: "format bytes", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, formatName), []byte("wrong\n"), 0o600)
		}},
		{name: "zero token", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, tokenName), make([]byte, 32), 0o600)
		}},
		{name: "nonempty lock", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, lockName), []byte("held"), 0o600)
		}},
		{name: "database append", mutate: func(home string) error {
			file, err := os.OpenFile(filepath.Join(home, databaseName), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return err
			}
			_, writeErr := file.Write([]byte("corrupt"))
			return errors.Join(writeErr, file.Close())
		}},
		{name: "sqlite sidecar", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, databaseName+"-wal"), []byte("sidecar"), 0o600)
		}},
		{name: "sqlite journal sidecar", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, databaseName+"-journal"), []byte("sidecar"), 0o600)
		}},
		{name: "sqlite shared memory sidecar", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, databaseName+"-shm"), []byte("sidecar"), 0o600)
		}},
		{name: "populated runtimes", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, runtimesName, "unexpected"), []byte("entry"), 0o600)
		}},
		{name: "populated changes", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, changesName, "unexpected"), []byte("entry"), 0o600)
		}},
		{name: "unknown home entry", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, "unexpected"), []byte("entry"), 0o600)
		}},
		{name: "nonregular member", mutate: func(home string) error {
			member := filepath.Join(home, formatName)
			backup := filepath.Join(filepath.Dir(home), "format-evidence")
			if err := os.Rename(member, backup); err != nil {
				return err
			}
			return os.Mkdir(member, 0o700)
		}},
		{name: "socket entry", mutate: func(home string) error {
			path := filepath.Join(home, "socket")
			fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				return err
			}
			if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
				_ = unix.Close(fd)
				return err
			}
			return unix.Close(fd)
		}},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			if err := corruption.mutate(home); err != nil {
				t.Fatal(err)
			}
			before := installDigest(t, home)
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
				{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					if err := operation.call(); !errors.Is(err, ErrInvalidHome) {
						t.Fatalf("%s accepted %s: %v", operation.name, corruption.name, err)
					}
					if after := installDigest(t, home); after != before {
						t.Fatalf("%s changed %s evidence", operation.name, corruption.name)
					}
				})
			}
		})
	}
}

func TestLegacyRustMixedAndPartialLayoutsRefusedUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{name: "unmarked", entry: "config"},
		{name: "legacy database", entry: "factory.db"},
		{name: "rust marker", entry: "daemon.lock"},
		{name: "mixed marker", entry: "manifest.json"},
		{name: "partial format", entry: formatName},
		{name: "partial token", entry: tokenName},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			home := filepath.Join(parent, "home")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, test.entry), []byte("legacy evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := installDigest(t, parent)
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{name: "init", call: func() error { _, err := Init(context.Background(), home); return err }},
				{name: "doctor", call: func() error { _, err := Doctor(context.Background(), home); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					if err := operation.call(); err == nil {
						t.Fatal("accepted unsupported home layout")
					}
					if after := installDigest(t, parent); after != before {
						t.Fatal("unsupported home layout changed after refusal")
					}
				})
			}
		})
	}
}

func TestInitAndDoctorReturnWithBaselineDescriptorCount(t *testing.T) {
	baseline, ok := descriptorCount()
	if !ok {
		t.Skip("/dev/fd is unavailable")
	}
	parent := installTempDir(t)
	home := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if current, _ := descriptorCount(); current != baseline {
		t.Fatalf("descriptor count after init = %d, baseline %d", current, baseline)
	}
	if _, err := Doctor(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if current, _ := descriptorCount(); current != baseline {
		t.Fatalf("descriptor count after doctor = %d, baseline %d", current, baseline)
	}

	originalSync := syncFile
	defer func() { syncFile = originalSync }()
	syncFile = func(fd int) error {
		return errors.New("injected member fsync failure")
	}
	if _, err := Init(context.Background(), filepath.Join(parent, "failed")); err == nil {
		t.Fatal("init accepted descriptor-count failure cut")
	}
	if current, _ := descriptorCount(); current != baseline {
		t.Fatalf("descriptor count after failed init = %d, baseline %d", current, baseline)
	}
}

func descriptorCount() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func installTempDir(t *testing.T) string {
	t.Helper()
	parent, err := os.MkdirTemp("/private/tmp", "dark-factory-install-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	return parent
}

func assertInstallMode(t *testing.T, path string, mode os.FileMode, directory bool) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	rawMode := uint32(info.Sys().(*syscall.Stat_t).Mode)
	if rawMode&0o7777 != uint32(mode) || info.IsDir() != directory {
		t.Fatalf("%s mode/type = %o/%t, want %o/%t", path, rawMode&0o7777, info.IsDir(), mode, directory)
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 && !directory {
		t.Fatalf("%s has hard links", path)
	}
}

func installDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	hash := sha256.New()
	_ = filepath.Walk(path, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		_, _ = fmt.Fprintf(hash, "%s\x00%d:%d:%d:%d:%d:%d:%d\x00", current, info.Mode(), info.Size(), stat.Dev, stat.Ino, stat.Uid, stat.Nlink, stat.Mode)
		if info.Mode().IsRegular() {
			contents, readErr := os.ReadFile(current)
			if readErr == nil {
				_, _ = hash.Write(contents)
			}
		}
		return nil
	})
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}
