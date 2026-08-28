//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

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
	phaseHook = func(point phase) error {
		if point == phaseBeforeRename {
			return os.Mkdir(home, 0o700)
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
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("stage evidence after no-replace conflict: %v", err)
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
