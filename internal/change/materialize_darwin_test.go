//go:build darwin

package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var injectedFailure = errors.New("injected Change failure")

type changeFixture struct {
	manifest Manifest
	blobs    map[string][]byte
}

func TestPrepareRetainsDeclaredEmptyStageBeforeExplicitPopulate(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("a")}})
	sourceCalled := false
	prepared, err := Prepare(context.Background(), parent, "target", "declared-stage")
	if err != nil {
		t.Fatal(err)
	}
	identity := prepared.Identity()
	if identity == (StageIdentity{}) {
		t.Fatal("Prepare returned an empty identity")
	}
	assertEmptyExactDirectory(t, filepath.Join(parent, "declared-stage"), identity)
	if _, err := os.Lstat(filepath.Join(parent, "target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare published target: %v", err)
	}
	if sourceCalled {
		t.Fatal("Prepare read a blob")
	}

	published, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, func(ctx context.Context, oid ObjectID) ([]byte, error) {
		sourceCalled = true
		return fixture.source(ctx, oid)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sourceCalled || !published.Facts().Identity().Equal(identity) || published.Path() != filepath.Join(parent, "target") {
		t.Fatalf("unexpected publication: source=%v path=%q facts=%+v", sourceCalled, published.Path(), published.Facts())
	}
	if _, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source); !isLifecycle(err) {
		t.Fatalf("repeat Populate = %T %v, want LifecycleError", err, err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); !isLifecycle(err) {
		t.Fatalf("repeat Close = %T %v, want LifecycleError", err, err)
	}
	assertExactTree(t, published.Path(), fixture)

	closed, err := Prepare(context.Background(), parent, "never-target", "closed-stage")
	if err != nil {
		t.Fatal(err)
	}
	closedID := closed.Identity()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source); !isLifecycle(err) {
		t.Fatalf("Populate after Close = %T %v, want LifecycleError", err, err)
	}
	removeRecorded(t, parent, "closed-stage", closedID)
}

func TestPrepareCrashReopenAndPostMkdirFailureRemainLocatable(t *testing.T) {
	parent := secureTempDir(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{3}, format.OIDLength()))
	empty := mustManifest(t, format, base, nil)

	prepared, err := Prepare(context.Background(), parent, "target", "crash-stage")
	if err != nil {
		t.Fatal(err)
	}
	identity := prepared.Identity()
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	facts, err := InspectPublished(context.Background(), parent, "crash-stage", identity, format, base)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Identity().Equal(identity) || facts.EntryCount() != 0 || facts.BlobBytes() != 0 || !facts.Commitment().Equal(empty.Commitment()) {
		t.Fatalf("reopened prepared facts differ: %+v", facts)
	}
	removeRecorded(t, parent, "crash-stage", identity)

	for i, step := range []materializeStep{stepAfterPrepareMkdir, stepBeforePrepareFsync} {
		name := fmt.Sprintf("failed-stage-%d", i)
		_, err = prepare(context.Background(), parent, "target", name, failAt(step))
		var unresolved *UnresolvedError
		if !errors.As(err, &unresolved) || !errors.Is(err, injectedFailure) {
			t.Fatalf("%s failure = %T %v, want retained UnresolvedError", step, err, err)
		}
		if unresolved.Parent != parent || unresolved.Name != name || !unresolved.HasIdentity || unresolved.Identity == (StageIdentity{}) {
			t.Fatalf("failure lost declared locator/identity: %+v", unresolved)
		}
		assertEmptyExactDirectory(t, filepath.Join(parent, name), unresolved.Identity)
		entries, readErr := os.ReadDir(parent)
		if readErr != nil || len(entries) != 1 || entries[0].Name() != name {
			t.Fatalf("Prepare created an undeclared/random artifact: %v %v", entries, readErr)
		}
		removeRecorded(t, parent, name, unresolved.Identity)
	}
}

func TestPrepareRejectsNonCanonicalLocatorsBeforeEffect(t *testing.T) {
	parent := secureTempDir(t)
	parents := []string{"relative", parent + "/", parent + "/child/..", parent + "//"}
	for _, candidate := range parents {
		t.Run(fmt.Sprintf("parent-%x", candidate), func(t *testing.T) {
			if _, err := Prepare(context.Background(), candidate, "target", "stage"); err == nil {
				t.Fatal("noncanonical parent accepted")
			}
			assertDirectoryEmpty(t, parent)
		})
	}
	for _, names := range [][2]string{
		{"", "stage"}, {".", "stage"}, {"..", "stage"}, {"a/b", "stage"}, {"a\\b", "stage"},
		{".GIT", "stage"}, {string([]byte{'b', 'a', 'd', 0xfe}), "stage"}, {"same", "same"}, {"target", "a/b"},
	} {
		t.Run(fmt.Sprintf("names-%x-%x", names[0], names[1]), func(t *testing.T) {
			if _, err := Prepare(context.Background(), parent, names[0], names[1]); err == nil {
				t.Fatal("unsafe target/stage accepted")
			}
			assertDirectoryEmpty(t, parent)
		})
	}
}

func TestPopulateAndInspectExactSHA1AndSHA256(t *testing.T) {
	for _, formatName := range []string{"sha1", "sha256"} {
		t.Run(formatName, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, formatName, []fixtureFile{
				{[]byte("README.md"), "100644", []byte("hello\n")},
				{[]byte("bin/run"), "100755", []byte("#!/bin/sh\nexit 0\n")},
				{[]byte("empty"), "100644", nil},
				{[]byte("raw/e\u0301"), "100644", []byte("raw-name")},
			})
			prepared := mustPrepare(t, parent, "change", "stage")
			identity := prepared.Identity()
			published, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepared.Close(); err != nil {
				t.Fatal(err)
			}
			facts := published.Facts()
			if !facts.Identity().Equal(identity) || !facts.Commitment().Equal(fixture.manifest.Commitment()) || facts.EntryCount() != fixture.manifest.EntryCount() || facts.BlobBytes() != fixture.manifest.BlobBytes() {
				t.Fatalf("publication facts differ: %+v", facts)
			}
			inspected, err := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base)
			if err != nil {
				t.Fatal(err)
			}
			if !inspected.Commitment().Equal(facts.Commitment()) || inspected.EntryCount() != facts.EntryCount() || inspected.BlobBytes() != facts.BlobBytes() {
				t.Fatalf("inspection facts differ: %+v vs %+v", inspected, facts)
			}
			assertExactTree(t, published.Path(), fixture)
		})
	}
}

func TestWideInspectionRefusesAtGlobalBudgetAndRemovalStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real over-limit recovery tree")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parent := secureTempDir(t)
	prepared := mustPrepare(t, parent, "target", "wide-recorded")
	identity := prepared.Identity()
	root := filepath.Join(parent, "wide-recorded")
	const extra = 17
	for i := 0; i < int(MaxEntryCount)+extra; i++ {
		path := filepath.Join(root, fmt.Sprintf("entry-%05d", i))
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	beforeFDs := descriptorCount(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{7}, format.OIDLength()))
	observed := 0
	_, err := inspectPublished(ctx, parent, "wide-recorded", identity, format, base, func(point materializePoint) error {
		if point.step == stepDuringDirectoryRead {
			observed++
		}
		return nil
	})
	var limit *LimitError
	if !errors.As(err, &limit) || observed != int(MaxEntryCount)+1 {
		t.Fatalf("wide inspection = %T %v observed=%d, want LimitError at %d", err, err, observed, MaxEntryCount+1)
	}
	if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("entry-%05d", int(MaxEntryCount)+extra-1))); err != nil {
		t.Fatalf("bounded inspection mutated an unread suffix: %v", err)
	}

	census := &removalCensus{}
	if err := removeRecordedTreeWithCensus(ctx, parent, "wide-recorded", identity, nil, census); err != nil {
		t.Fatal(err)
	}
	if census.open != 0 || census.maximum > 3 {
		t.Fatalf("wide removal descriptor census open=%d maximum=%d, want 0 and <=3", census.open, census.maximum)
	}
	if afterFDs := descriptorCount(t); afterFDs != beforeFDs {
		t.Fatalf("oversized recovery leaked descriptors: before=%d after=%d", beforeFDs, afterFDs)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wide recorded tree survived removal: %v", err)
	}
}

func TestDeepRecoveryRemovalDoesNotRetainOneFDPerLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	parent := secureTempDir(t)
	prepared := mustPrepare(t, parent, "target", "deep-recorded")
	identity := prepared.Identity()
	root := filepath.Join(parent, "deep-recorded")
	directory := root
	for depth := 0; depth < MaxDepth+32; depth++ {
		directory = filepath.Join(directory, "d")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "leaf"), []byte("provider-expanded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	census := &removalCensus{}
	if err := removeRecordedTreeWithCensus(ctx, parent, "deep-recorded", identity, nil, census); err != nil {
		t.Fatal(err)
	}
	if census.open != 0 || census.maximum != 3 {
		t.Fatalf("deep removal descriptor census open=%d maximum=%d, want 0 and 3", census.open, census.maximum)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deep recorded tree survived removal: %v", err)
	}
}

func TestBeforeRenameCheckpointFollowsFsyncAndFinalRootVerification(t *testing.T) {
	t.Run("cancellation is the final pre-publication edge", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sawScan := false
		sawFsync := false
		renameChecks := 0
		hook := func(point materializePoint) error {
			switch point.step {
			case stepDuringTreeScan:
				sawScan = true
			case stepDuringTreeFsync:
				sawFsync = true
			case stepBeforeRename:
				renameChecks++
				if !sawScan || !sawFsync {
					t.Fatalf("rename edge preceded scan/fsync: scan=%v fsync=%v", sawScan, sawFsync)
				}
				cancel()
			}
			return nil
		}
		_, err := prepared.populateAndPublish(ctx, fixture.manifest, fixture.source, hook)
		var unknown *OutcomeUnknownError
		if !errors.Is(err, context.Canceled) || errors.As(err, &unknown) || renameChecks != 1 {
			t.Fatalf("rename-edge cancellation = %T %v checks=%d", err, err, renameChecks)
		}
		if _, err := os.Lstat(filepath.Join(parent, "change")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rename-edge cancellation published target: %v", err)
		}
		assertEmptyOrPopulatedDirectory(t, filepath.Join(parent, "stage"), prepared.Identity())
		removeRecorded(t, parent, "stage", prepared.Identity())
		_ = prepared.Close()
	})

	t.Run("root recheck separates fsync from rename edge", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		renameReached := false
		mutated := false
		hook := func(point materializePoint) error {
			if point.step == stepDuringTreeFsync && len(point.entryPath) == 0 && !mutated {
				mutated = true
				return unix.Chmod(filepath.Join(parent, "stage"), 0o777)
			}
			if point.step == stepBeforeRename {
				renameReached = true
			}
			return nil
		}
		if _, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook); err == nil || !mutated || renameReached {
			t.Fatalf("final root recheck missing: err=%v mutated=%v renameReached=%v", err, mutated, renameReached)
		}
		if _, err := os.Lstat(filepath.Join(parent, "change")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid root was published: %v", err)
		}
		_ = unix.Chmod(filepath.Join(parent, "stage"), 0o700)
		removeRecorded(t, parent, "stage", prepared.Identity())
		_ = prepared.Close()
	})
}

func TestInspectPublishedDetectsIdentityBaseBytesModesAndRootAuthority(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("right")}})
	prepared := mustPrepare(t, parent, "change", "stage")
	identity := prepared.Identity()
	if _, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	wrongIdentity := StageIdentity{device: identity.device, inode: identity.inode + 1}
	if _, err := InspectPublished(context.Background(), parent, "change", wrongIdentity, fixture.manifest.format, fixture.manifest.base); err == nil {
		t.Fatal("wrong recorded identity accepted")
	}
	wrongBase := mustID(t, fixture.manifest.format, bytes.Repeat([]byte{8}, fixture.manifest.format.OIDLength()))
	wrongBaseFacts, err := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, wrongBase)
	if err != nil {
		t.Fatal(err)
	}
	if wrongBaseFacts.Commitment().Equal(fixture.manifest.Commitment()) {
		t.Fatal("wrong base did not change reconstructed commitment")
	}

	file := filepath.Join(parent, "change", "nested", "a")
	if err := os.WriteFile(file, []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	tampered, err := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base)
	if err != nil {
		t.Fatal(err)
	}
	if tampered.Commitment().Equal(fixture.manifest.Commitment()) {
		t.Fatal("same-size byte tamper trusted stored commitment")
	}
	if err := unix.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base); err == nil {
		t.Fatal("file mode tamper accepted")
	}
	if err := unix.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(filepath.Join(parent, "change"), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base); err == nil {
		t.Fatal("root authority tamper accepted")
	}
}

func TestPopulateRechecksRootAndNestedMetadataBeforeAndAfterRename(t *testing.T) {
	t.Run("root before blob", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		if err := unix.Chmod(filepath.Join(parent, "stage"), 0o777); err != nil {
			t.Fatal(err)
		}
		called := false
		_, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatalf("root mode was not rejected before blob read: err=%v called=%v", err, called)
		}
		_ = unix.Chmod(filepath.Join(parent, "stage"), 0o700)
		removeRecorded(t, parent, "stage", prepared.Identity())
		_ = prepared.Close()
	})

	t.Run("root special bits before blob", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		if err := unix.Chmod(filepath.Join(parent, "stage"), 0o1700); err != nil {
			t.Fatal(err)
		}
		called := false
		_, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatalf("root special bits were not rejected before blob read: err=%v called=%v", err, called)
		}
		_ = unix.Chmod(filepath.Join(parent, "stage"), 0o700)
		removeRecorded(t, parent, "stage", prepared.Identity())
		_ = prepared.Close()
	})

	for _, mutation := range []struct {
		name string
		path string
		mode uint32
	}{{"nested sticky", "nested", 0o1700}} {
		t.Run(mutation.name, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("a")}})
			prepared := mustPrepare(t, parent, "change", "stage")
			hook := func(point materializePoint) error {
				if point.step == stepBeforeTreeFsync {
					return unix.Chmod(filepath.Join(point.parent, point.stagingName, mutation.path), mutation.mode)
				}
				return nil
			}
			if _, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook); err == nil {
				t.Fatal("special-bit mutation was published")
			}
			_ = unix.Chmod(filepath.Join(parent, "stage", mutation.path), mutation.mode&0o777)
			removeRecorded(t, parent, "stage", prepared.Identity())
			_ = prepared.Close()
		})
	}

	t.Run("root after rename", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		hook := func(point materializePoint) error {
			if point.step == stepAfterRename {
				return unix.Chmod(filepath.Join(point.parent, point.targetName), 0o777)
			}
			return nil
		}
		_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook)
		var unknown *OutcomeUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("post-rename root mutation = %T %v, want OutcomeUnknownError", err, err)
		}
		_ = prepared.Close()
		if _, statErr := os.Stat(filepath.Join(parent, "change")); statErr != nil {
			t.Fatalf("ambiguous published target was deleted: %v", statErr)
		}
	})

	t.Run("nested special bits after rename", func(t *testing.T) {
		parent := secureTempDir(t)
		fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", []byte("a")}})
		prepared := mustPrepare(t, parent, "change", "stage")
		hook := func(point materializePoint) error {
			if point.step == stepAfterRename {
				return unix.Chmod(filepath.Join(point.parent, point.targetName, "nested"), 0o1700)
			}
			return nil
		}
		_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook)
		var unknown *OutcomeUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("post-rename nested mutation = %T %v, want OutcomeUnknownError", err, err)
		}
		_ = prepared.Close()
	})
}

func TestRootAuthorityPredicateRejectsOwnerModeSpecialBitsTypeAndIdentity(t *testing.T) {
	uid := uint32(os.Geteuid())
	identity := StageIdentity{device: 11, inode: 22}
	valid := unix.Stat_t{Dev: int32(identity.device), Ino: identity.inode, Uid: uid, Mode: unix.S_IFDIR | 0o700}
	if !rootAuthority(valid, identity, uid) {
		t.Fatal("valid root authority rejected")
	}
	mutations := map[string]func(*unix.Stat_t){
		"owner":        func(stat *unix.Stat_t) { stat.Uid++ },
		"mode":         func(stat *unix.Stat_t) { stat.Mode = unix.S_IFDIR | 0o777 },
		"special bits": func(stat *unix.Stat_t) { stat.Mode = unix.S_IFDIR | 0o1700 },
		"type":         func(stat *unix.Stat_t) { stat.Mode = unix.S_IFREG | 0o700 },
		"identity":     func(stat *unix.Stat_t) { stat.Ino++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if rootAuthority(candidate, identity, uid) {
				t.Fatalf("%s mutation accepted", name)
			}
		})
	}
}

func TestCancellationInsideHashScanAndFsyncIsBounded(t *testing.T) {
	tests := []struct {
		name string
		step materializeStep
		data []byte
	}{{"blob hash", stepDuringBlobHash, bytes.Repeat([]byte("h"), 4<<20)}, {"tree scan", stepDuringTreeScan, bytes.Repeat([]byte("s"), 1<<20)}, {"directory fsync", stepDuringTreeFsync, []byte("f")}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a"), "100644", test.data}})
			prepared := mustPrepare(t, parent, "change", "stage")
			ctx, cancel := context.WithCancel(context.Background())
			entered := 0
			hook := func(point materializePoint) error {
				if point.step == test.step {
					entered++
					if entered == 1 {
						cancel()
					}
				}
				return nil
			}
			started := time.Now()
			_, err := prepared.populateAndPublish(ctx, fixture.manifest, fixture.source, hook)
			if !errors.Is(err, context.Canceled) || entered != 1 || time.Since(started) > 2*time.Second {
				t.Fatalf("loop cancellation failed: err=%v entered=%d duration=%s", err, entered, time.Since(started))
			}
			if _, statErr := os.Lstat(filepath.Join(parent, "change")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("canceled population published: %v", statErr)
			}
			removeRecorded(t, parent, "stage", prepared.Identity())
			_ = prepared.Close()
		})
	}
}

func TestPopulateRejectsWrongBlobBeforeFilesystemCreation(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	prepared := mustPrepare(t, parent, "change", "stage")
	createReached := false
	_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, func(context.Context, ObjectID) ([]byte, error) {
		return []byte("wrong"), nil
	}, func(point materializePoint) error {
		if point.step == stepBeforeEntryParentOpen || point.step == stepBeforeFileCreate {
			createReached = true
		}
		return nil
	})
	if err == nil || createReached {
		t.Fatalf("wrong blob reached filesystem creation: err=%v reached=%v", err, createReached)
	}
	assertEmptyExactDirectory(t, filepath.Join(parent, "stage"), prepared.Identity())
	removeRecorded(t, parent, "stage", prepared.Identity())
	_ = prepared.Close()
}

func TestPopulateRejectsNonPlainEntriesAndSymlinkEscape(t *testing.T) {
	injections := map[string]func(string) error{
		"symlink":         func(stage string) error { return os.Symlink("/private/tmp", filepath.Join(stage, "extra")) },
		"hardlink":        func(stage string) error { return os.Link(filepath.Join(stage, "a"), filepath.Join(stage, "extra")) },
		"fifo":            func(stage string) error { return unix.Mkfifo(filepath.Join(stage, "extra"), 0o600) },
		"empty directory": func(stage string) error { return os.Mkdir(filepath.Join(stage, "extra"), 0o700) },
		"socket": func(stage string) error {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(stage, "extra"), Net: "unix"})
			if err != nil {
				return err
			}
			listener.SetUnlinkOnClose(false)
			return listener.Close()
		},
	}
	for name, inject := range injections {
		t.Run(name, func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			prepared := mustPrepare(t, parent, "change", "stage")
			hook := func(point materializePoint) error {
				if point.step == stepBeforeTreeVerify {
					return inject(filepath.Join(point.parent, point.stagingName))
				}
				return nil
			}
			if _, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook); err == nil {
				t.Fatalf("injected %s was accepted", name)
			}
			removeRecorded(t, parent, "stage", prepared.Identity())
			_ = prepared.Close()
		})
	}

	parent := secureTempDir(t)
	outside := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("nested/a-seed"), "100644", []byte("seed")}, {[]byte("nested/z-payload"), "100644", []byte("secret")}})
	prepared := mustPrepare(t, parent, "change", "stage")
	swapped := false
	hook := func(point materializePoint) error {
		if point.step != stepBeforeEntryParentOpen || string(point.entryPath) != "nested/z-payload" || swapped {
			return nil
		}
		swapped = true
		stage := filepath.Join(point.parent, point.stagingName)
		if err := os.Rename(filepath.Join(stage, "nested"), filepath.Join(stage, "owned-moved")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(stage, "nested"))
	}
	if _, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook); err == nil {
		t.Fatal("intermediate symlink swap was accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "z-payload")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped staging root: %v", err)
	}
	removeRecorded(t, parent, "stage", prepared.Identity())
	_ = prepared.Close()
}

func TestCreateOnlyTargetsAndConcurrentPublish(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := secureTempDir(t)
			target := filepath.Join(parent, "change")
			switch kind {
			case "file":
				if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("sentinel", target); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.Lstat(target)
			if _, err := Prepare(context.Background(), parent, "change", "stage"); err == nil {
				t.Fatal("existing target accepted")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing target changed: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(parent, "stage")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("conflict created staging: %v", err)
			}
		})
	}

	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("winner"), "100644", []byte("exact")}})
	first := mustPrepare(t, parent, "change", "stage-one")
	second := mustPrepare(t, parent, "change", "stage-two")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, prepared := range []*Prepared{first, second} {
		wait.Add(1)
		go func(prepared *Prepared) {
			defer wait.Done()
			<-start
			_, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source)
			results <- err
		}(prepared)
	}
	close(start)
	wait.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var conflictError *ConflictError
		if errors.As(err, &conflictError) {
			conflict++
			continue
		}
		t.Fatalf("unexpected concurrent error: %T %v", err, err)
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want one each", success, conflict)
	}
	assertExactTree(t, filepath.Join(parent, "change"), fixture)
	removeRecorded(t, parent, "stage-one", first.Identity())
	removeRecorded(t, parent, "stage-two", second.Identity())
	_ = first.Close()
	_ = second.Close()
}

func TestPostPublicationFailureIsInspectedNeverDeleted(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("right")}})
	prepared := mustPrepare(t, parent, "change", "stage")
	identity := prepared.Identity()
	_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, failAt(stepAfterRename))
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, injectedFailure) || !unknown.Identity.Equal(identity) || !unknown.Commitment.Equal(fixture.manifest.Commitment()) {
		t.Fatalf("post-publication cut = %T %v, want exact OutcomeUnknownError", err, err)
	}
	facts, inspectErr := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base)
	if inspectErr != nil || !facts.Commitment().Equal(fixture.manifest.Commitment()) {
		t.Fatalf("reconciliation failed: facts=%+v err=%v", facts, inspectErr)
	}
	_ = prepared.Close()

	parent = secureTempDir(t)
	prepared = mustPrepare(t, parent, "change", "stage")
	identity = prepared.Identity()
	hook := func(point materializePoint) error {
		if point.step == stepAfterRename {
			return os.WriteFile(filepath.Join(point.parent, point.targetName, "a"), []byte("wrong"), 0o644)
		}
		return nil
	}
	_, err = prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, hook)
	if !errors.As(err, &unknown) {
		t.Fatalf("post-publication tamper = %T %v, want OutcomeUnknownError", err, err)
	}
	tampered, inspectErr := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base)
	if inspectErr != nil || tampered.Commitment().Equal(fixture.manifest.Commitment()) {
		t.Fatalf("inspection trusted expected digest: facts=%+v err=%v", tampered, inspectErr)
	}
	_ = prepared.Close()
}

func TestPopulateFailureCutsLeaveOnlyTheDeclaredRecordedStage(t *testing.T) {
	prePublication := []materializeStep{
		stepBeforeFileCreate, stepBeforeFileWrite, stepBeforeFileFsync,
		stepBeforeTreeVerify, stepBeforeTreeFsync, stepBeforeRename,
	}
	for _, step := range prePublication {
		t.Run(string(step), func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			prepared := mustPrepare(t, parent, "change", "declared-stage")
			_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, failAt(step))
			if !errors.Is(err, injectedFailure) {
				t.Fatalf("cut = %T %v, want injected failure", err, err)
			}
			if _, err := os.Lstat(filepath.Join(parent, "change")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-publication cut created target: %v", err)
			}
			entries, readErr := os.ReadDir(parent)
			if readErr != nil || len(entries) != 1 || entries[0].Name() != "declared-stage" {
				t.Fatalf("cut path census=%v err=%v", entries, readErr)
			}
			removeRecorded(t, parent, "declared-stage", prepared.Identity())
			_ = prepared.Close()
		})
	}
	for _, step := range []materializeStep{stepAfterRename, stepBeforeParentFsync} {
		t.Run(string(step), func(t *testing.T) {
			parent := secureTempDir(t)
			fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
			prepared := mustPrepare(t, parent, "change", "declared-stage")
			identity := prepared.Identity()
			_, err := prepared.populateAndPublish(context.Background(), fixture.manifest, fixture.source, failAt(step))
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || !errors.Is(err, injectedFailure) || !unknown.Identity.Equal(identity) {
				t.Fatalf("post-publication cut = %T %v, want OutcomeUnknownError", err, err)
			}
			facts, inspectErr := InspectPublished(context.Background(), parent, "change", identity, fixture.manifest.format, fixture.manifest.base)
			if inspectErr != nil || !facts.Commitment().Equal(fixture.manifest.Commitment()) {
				t.Fatalf("post-cut reconciliation facts=%+v err=%v", facts, inspectErr)
			}
			_ = prepared.Close()
		})
	}
}

func TestRemoveRecordedTreeExactMismatchAndSwap(t *testing.T) {
	parent := secureTempDir(t)
	prepared := mustPrepare(t, parent, "target", "stage")
	identity := prepared.Identity()
	if err := os.WriteFile(filepath.Join(parent, "stage", "sentinel"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := StageIdentity{device: identity.device, inode: identity.inode + 1}
	if err := RemoveRecordedTree(context.Background(), parent, "stage", wrong); err == nil {
		t.Fatal("identity mismatch authorized removal")
	}
	if _, err := os.Stat(filepath.Join(parent, "stage", "sentinel")); err != nil {
		t.Fatalf("mismatch changed artifact: %v", err)
	}
	if err := RemoveRecordedTree(context.Background(), parent, "stage", identity); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRecordedTree(context.Background(), parent, "stage", identity); err != nil {
		t.Fatalf("positive absence was not idempotent: %v", err)
	}
	_ = prepared.Close()

	prepared = mustPrepare(t, parent, "target", "swap-stage")
	identity = prepared.Identity()
	preserved := filepath.Join(parent, "preserved")
	hook := func(point materializePoint) error {
		if point.step == stepBeforeRecordedRemoval {
			if err := os.Rename(filepath.Join(parent, "swap-stage"), preserved); err != nil {
				return err
			}
			return os.Mkdir(filepath.Join(parent, "swap-stage"), 0o700)
		}
		return nil
	}
	err := removeRecordedTree(context.Background(), parent, "swap-stage", identity, hook)
	var unresolved *UnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("identity swap = %T %v, want UnresolvedError", err, err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("original artifact removed after swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "swap-stage")); err != nil {
		t.Fatalf("replacement artifact removed after swap: %v", err)
	}
	_ = prepared.Close()
}

func TestPublishedChangeCarriesNoRepositoryAuthority(t *testing.T) {
	parent := secureTempDir(t)
	git(t, parent, "init", "--quiet")
	changes := filepath.Join(parent, "changes")
	if err := os.Mkdir(changes, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
	prepared := mustPrepare(t, changes, "change", "stage")
	published, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(published.Path(), ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published Change has Git metadata: %v", err)
	}
	command := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	command.Dir = published.Path()
	command.Env = append(safeGitEnv(), "GIT_CEILING_DIRECTORIES="+changes)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("controlled ceiling discovered ancestor repository: %q", output)
	}
	command = exec.Command("git", "rev-parse", "--is-inside-work-tree")
	command.Dir = published.Path()
	command.Env = safeGitEnv()
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("fixture did not prove ancestor discovery without ceiling: %q %v", output, err)
	}
}

func TestPreparedLifecycleDoesNotLeakResources(t *testing.T) {
	parent := secureTempDir(t)
	fixture := newFixture(t, "sha1", []fixtureFile{{[]byte("a"), "100644", []byte("a")}})
	beforeFDs := descriptorCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	for i := range 20 {
		prepared := mustPrepare(t, parent, fmt.Sprintf("change-%d", i), fmt.Sprintf("stage-%d", i))
		if _, err := prepared.PopulateAndPublish(context.Background(), fixture.manifest, fixture.source); err != nil {
			t.Fatal(err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("descriptor count changed: before=%d after=%d", beforeFDs, after)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines+1 {
		t.Fatalf("goroutine count changed: before=%d after=%d", beforeGoroutines, after)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 20 {
		t.Fatalf("unexpected path census: count=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "stage-") {
			t.Fatalf("staging locator survived publication: %q", entry.Name())
		}
	}
}

type fixtureFile struct {
	path []byte
	mode string
	data []byte
}

func newFixture(t testing.TB, formatName string, files []fixtureFile) changeFixture {
	t.Helper()
	format := mustFormat(t, formatName)
	base := mustID(t, format, bytes.Repeat([]byte{0x55}, format.OIDLength()))
	entries := make([]Entry, len(files))
	blobs := make(map[string][]byte, len(files))
	for i, file := range files {
		entries[i] = mustEntry(t, format, file.path, file.mode, file.data)
		blobs[entries[i].oid.Hex()] = bytes.Clone(file.data)
	}
	return changeFixture{manifest: mustManifest(t, format, base, entries), blobs: blobs}
}

func (f changeFixture) source(_ context.Context, oid ObjectID) ([]byte, error) {
	data, ok := f.blobs[oid.Hex()]
	if !ok {
		return nil, errors.New("missing fixture blob")
	}
	return bytes.Clone(data), nil
}

func mustPrepare(t testing.TB, parent, target, staging string) *Prepared {
	t.Helper()
	prepared, err := Prepare(context.Background(), parent, target, staging)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func removeRecorded(t testing.TB, parent, name string, identity StageIdentity) {
	t.Helper()
	if err := RemoveRecordedTree(context.Background(), parent, name, identity); err != nil {
		t.Fatal(err)
	}
}

func secureTempDir(t testing.TB) string {
	t.Helper()
	path, err := os.MkdirTemp(os.Getenv("TMPDIR"), "dark-factory-change-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func failAt(step materializeStep) materializeHook {
	return func(point materializePoint) error {
		if point.step == step {
			return injectedFailure
		}
		return nil
	}
}

func isLifecycle(err error) bool { var lifecycle *LifecycleError; return errors.As(err, &lifecycle) }

func assertEmptyExactDirectory(t testing.TB, path string, identity StageIdentity) {
	t.Helper()
	assertExactDirectoryIdentity(t, path, identity)
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("prepared stage not empty: %v %v", entries, err)
	}
}

func assertEmptyOrPopulatedDirectory(t testing.TB, path string, identity StageIdentity) {
	t.Helper()
	assertExactDirectoryIdentity(t, path, identity)
}

func assertExactDirectoryIdentity(t testing.TB, path string, identity StageIdentity) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared stage mode=%v", info.Mode())
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		t.Fatal(err)
	}
	if !identityOf(stat).Equal(identity) {
		t.Fatalf("stage identity differs: %+v", identityOf(stat))
	}
}

func assertDirectoryEmpty(t testing.TB, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory has effects: %v %v", entries, err)
	}
}

func assertExactTree(t testing.TB, root string, fixture changeFixture) {
	t.Helper()
	actualDirs := make([]string, 0)
	actualFiles := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
			return fmt.Errorf("non-plain entry %q", relative)
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("directory mode %q=%o", relative, info.Mode().Perm())
			}
			actualDirs = append(actualDirs, relative)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file %q", relative)
		}
		actualFiles[relative] = struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(actualDirs)
	if !slices.Equal(actualDirs, fixture.manifest.directories) {
		t.Fatalf("directories=%q want=%q", actualDirs, fixture.manifest.directories)
	}
	if len(actualFiles) != len(fixture.manifest.entries) {
		t.Fatalf("file count=%d", len(actualFiles))
	}
	for _, entry := range fixture.manifest.entries {
		path := filepath.Join(root, string(entry.path))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, fixture.blobs[entry.oid.Hex()]) {
			t.Fatalf("bytes differ for %x", entry.path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o644)
		if entry.mode == "100755" {
			want = 0o755
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode for %x=%o want=%o", entry.path, info.Mode().Perm(), want)
		}
	}
}

func git(t testing.TB, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = safeGitEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func safeGitEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
}

func descriptorCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
