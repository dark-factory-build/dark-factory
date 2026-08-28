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
	originalSync := syncDirectory
	defer func() { syncDirectory = originalSync }()
	calls := 0
	syncDirectory = func(fd int) error {
		calls++
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
	if err := os.Mkdir(inner, 0o700); err != nil {
		return err
	}
	return os.Rename(filepath.Join(oldOuter, filepath.Base(inner), filepath.Base(home)), home)
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
	if _, err := Doctor(context.Background(), home); !errors.Is(err, ErrInvalidHome) {
		t.Fatalf("doctor accepted hard-linked member: %v", err)
	}
	after, err := os.Lstat(filepath.Join(home, formatName))
	if err != nil {
		t.Fatal(err)
	}
	if after.Sys().(*syscall.Stat_t).Ino != before.Sys().(*syscall.Stat_t).Ino || after.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatalf("hardlink evidence changed: before=%+v after=%+v", before.Sys(), after.Sys())
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
		{name: "populated runtimes", mutate: func(home string) error {
			return os.WriteFile(filepath.Join(home, runtimesName, "unexpected"), []byte("entry"), 0o600)
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
			if _, err := Doctor(context.Background(), home); !errors.Is(err, ErrInvalidHome) {
				t.Fatalf("doctor accepted %s: %v", corruption.name, err)
			}
			if after := installDigest(t, home); after != before {
				t.Fatalf("doctor changed %s evidence", corruption.name)
			}
		})
	}
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
