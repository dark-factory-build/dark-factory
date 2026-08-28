//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	if result, err := Doctor(context.Background(), home); err != nil || result.State != Ready {
		t.Fatalf("doctor = %+v, err=%v", result, err)
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
	phaseHook = func(point phase) error {
		if point == phaseBeforeParentFsync {
			return errors.New("injected parent sync failure")
		}
		return nil
	}
	defer func() { phaseHook = nil }()
	if _, err := Init(context.Background(), home); err == nil {
		t.Fatal("init accepted an injected parent sync failure")
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final home after parent sync failure: %v", err)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("stage evidence after parent sync failure: info=%v err=%v", info, err)
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
	if info.Mode().Perm() != mode || info.IsDir() != directory {
		t.Fatalf("%s mode/type = %v/%t, want %o/%t", path, info.Mode().Perm(), info.IsDir(), mode, directory)
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
		_, _ = io.WriteString(hash, current+"\x00")
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
