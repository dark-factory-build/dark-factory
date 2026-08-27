package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	sqliteIO "github.com/ncruces/go-sqlite3/util/ioutil"
	"github.com/ncruces/go-sqlite3/vfs/readervfs"
	"golang.org/x/sys/unix"
)

const walFrameHeaderSizeForTest = 24

func TestNewDatabaseImageIsExactAndOpens(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, 42)
	before := directoryEntryNames(t, ".")
	first, err := NewDatabaseImage(ctx, FactoryConfig{DispatchEnabled: true, Capacity: 7}, at)
	if err != nil {
		t.Fatalf("NewDatabaseImage: %v", err)
	}
	second, err := NewDatabaseImage(ctx, FactoryConfig{}, mustTime(t, 43))
	if err != nil {
		t.Fatalf("second NewDatabaseImage: %v", err)
	}
	after := directoryEntryNames(t, ".")
	if !equalStrings(before, after) {
		t.Fatalf("in-memory image creation changed cwd: before=%v after=%v", before, after)
	}
	for index, image := range [][]byte{first, second} {
		if len(image) == 0 || len(image) > maxFreshDatabaseImageSize {
			t.Fatalf("image %d size = %d", index, len(image))
		}
		if len(image) < 100 || string(image[:16]) != "SQLite format 3\x00" {
			t.Fatalf("image %d has invalid SQLite header", index)
		}
		if image[18] != 1 || image[19] != 1 {
			t.Fatalf("image %d journal header = %d/%d, want rollback 1/1", index, image[18], image[19])
		}
		pageSize := int(binary.BigEndian.Uint16(image[16:18]))
		if pageSize == 1 {
			pageSize = 65536
		}
		if pageSize < 512 || len(image)%pageSize != 0 {
			t.Fatalf("image %d size %d is not aligned to SQLite page size %d", index, len(image), pageSize)
		}
		if got := binary.BigEndian.Uint32(image[68:72]); got != uint32(applicationID) {
			t.Fatalf("image %d application id = %#x, want %#x", index, got, applicationID)
		}
		if err := InspectImmutable(ctx, bytes.NewReader(image), int64(len(image))); err != nil {
			t.Fatalf("InspectImmutable image %d: %v", index, err)
		}
	}

	firstState, firstPath := openImageStore(t, first)
	if firstState.DaemonID.zero() || !firstState.DispatchEnabled || firstState.Capacity != 7 || firstState.Revision.Int64() != 1 || firstState.updatedAt != at {
		t.Fatalf("first factory state = %+v", firstState)
	}
	secondState, _ := openImageStore(t, second)
	if firstState.DaemonID == secondState.DaemonID {
		t.Fatal("two fresh images reused one daemon identity")
	}
	header, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("Open did not promote image to WAL: %d/%d", header[18], header[19])
	}
	reopened, err := Open(ctx, firstPath)
	if err != nil {
		t.Fatalf("reopen promoted image: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewDatabaseImageConcurrentConfigurationsDoNotCrossWire(t *testing.T) {
	type result struct {
		index int
		image []byte
		err   error
	}
	const count = 8
	results := make(chan result, count)
	var wait sync.WaitGroup
	for index := range count {
		at := mustTime(t, int64(100+index))
		wait.Add(1)
		go func() {
			defer wait.Done()
			image, err := NewDatabaseImage(context.Background(), FactoryConfig{DispatchEnabled: index%2 == 0, Capacity: uint16(index + 1)}, at)
			results <- result{index: index, image: image, err: err}
		}()
	}
	wait.Wait()
	close(results)
	seenDaemonIDs := make(map[DaemonID]bool, count)
	for result := range results {
		if result.err != nil {
			t.Fatalf("image %d: %v", result.index, result.err)
		}
		state, _ := openImageStore(t, result.image)
		if state.DispatchEnabled != (result.index%2 == 0) || state.Capacity != uint16(result.index+1) || state.updatedAt.Int64() != int64(100+result.index) {
			t.Fatalf("image %d state = %+v", result.index, state)
		}
		if seenDaemonIDs[state.DaemonID] {
			t.Fatalf("image %d reused daemon identity", result.index)
		}
		seenDaemonIDs[state.DaemonID] = true
	}
}

func TestNewDatabaseImageRejectsInvalidInput(t *testing.T) {
	if image, err := NewDatabaseImage(context.Background(), FactoryConfig{Capacity: MaxFactoryCapacity + 1}, UnixMillis{}); !errors.Is(err, ErrInvalidValue) || image != nil {
		t.Fatalf("invalid config image=%d error=%v", len(image), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if image, err := NewDatabaseImage(ctx, FactoryConfig{}, UnixMillis{}); !errors.Is(err, context.Canceled) || image != nil {
		t.Fatalf("cancelled image=%d error=%v", len(image), err)
	}
}

func TestOpenRefusesRollbackImageWithHotJournalWithoutMutation(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "factory.sqlite3")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	journalPath := path + "-journal"
	journal := []byte("HOT_ROLLBACK_JOURNAL_SENTINEL")
	if err := os.WriteFile(journalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	before := captureDatabaseEvidence(t, path)
	journalInfo, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open hot rollback image error = %v", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, before)
	afterJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	afterJournalInfo, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterJournal, journal) || !os.SameFile(journalInfo, afterJournalInfo) || afterJournalInfo.Size() != journalInfo.Size() || afterJournalInfo.Mode() != journalInfo.Mode() || !afterJournalInfo.ModTime().Equal(journalInfo.ModTime()) {
		t.Fatal("hot rollback refusal modified journal evidence")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("hot rollback refusal created %q: %v", suffix, err)
		}
	}
}

func TestOpenValidatesWALOnDisposableCopy(t *testing.T) {
	t.Run("valid committed state is recovered", func(t *testing.T) {
		path, project := walSnapshotFixture(t, "")
		store, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open valid WAL snapshot: %v", err)
		}
		defer store.Close()
		if retained, found, err := store.Project(context.Background(), project.ID); err != nil || !found || retained.Name != project.Name {
			t.Fatalf("WAL project found=%v project=%+v err=%v", found, retained, err)
		}
		info, err := os.Stat(path + "-shm")
		if err != nil || info.Size() == 0 {
			t.Fatalf("successful WAL open did not rebuild disposable SHM: size=%d err=%v", info.Size(), err)
		}
	})

	for name, mutation := range map[string]string{
		"invalid controls never touch originals": `UPDATE factory SET next_invalidation_sequence = 3`,
		"invalid schema never touches originals": `DROP INDEX tasks_canonical_queue`,
	} {
		t.Run(name, func(t *testing.T) {
			path, _ := walSnapshotFixture(t, mutation)
			before := captureSQLiteSet(t, path)
			if _, err := Open(context.Background(), path); err == nil {
				t.Fatal("Open accepted invalid WAL state")
			}
			assertSQLiteSetUnchanged(t, path, before)
			info, err := os.Stat(path + "-shm")
			if err != nil || info.Size() != walIndexRegionSize {
				size := int64(-1)
				if info != nil {
					size = info.Size()
				}
				t.Fatalf("rejected WAL changed zero-cache SHM: size=%d err=%v", size, err)
			}
		})
	}
}

func TestOpenAcceptsSQLiteWALCrashTails(t *testing.T) {
	for name, tail := range map[string]func([]byte, uint32) []byte{
		"partial": func(wal []byte, _ uint32) []byte {
			return append(wal, []byte("partial crash tail")...)
		},
		"checksum invalid frame": func(wal []byte, pageSize uint32) []byte {
			return append(wal, make([]byte, walFrameHeaderSizeForTest+int(pageSize))...)
		},
		"salt mismatched frame": func(wal []byte, pageSize uint32) []byte {
			frame := make([]byte, walFrameHeaderSizeForTest+int(pageSize))
			binary.BigEndian.PutUint32(frame[0:4], 1)
			binary.BigEndian.PutUint32(frame[8:12], ^binary.BigEndian.Uint32(wal[16:20]))
			binary.BigEndian.PutUint32(frame[12:16], ^binary.BigEndian.Uint32(wal[20:24]))
			return append(wal, frame...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path, project := walSnapshotFixture(t, "")
			main, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			pageSize, err := databasePageSize(main)
			main.Close()
			if err != nil {
				t.Fatal(err)
			}
			wal, err := os.ReadFile(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+"-wal", tail(wal, pageSize), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := Open(context.Background(), path)
			if err != nil {
				t.Fatalf("Open WAL with legitimate tail: %v", err)
			}
			if _, found, err := store.Project(context.Background(), project.ID); err != nil || !found {
				store.Close()
				t.Fatalf("recovered committed prefix found=%v err=%v", found, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenRejectsUnsafeWALSidecarsWithoutMutation(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string){
		"malformed pair": func(t *testing.T, path string) {
			writeSidecar(t, path+"-wal", []byte("thirteen-byte!"), 0o600)
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"wrong WAL mode": func(t *testing.T, path string) {
			path, _ = walSnapshotFixtureAt(t, filepath.Dir(path), filepath.Base(path), "")
			if err := os.Chmod(path+"-wal", 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"WAL special mode bits": func(t *testing.T, path string) {
			path, _ = walSnapshotFixtureAt(t, filepath.Dir(path), filepath.Base(path), "")
			if err := os.Chmod(path+"-wal", os.ModeSetuid|0o600); err != nil {
				t.Fatal(err)
			}
		},
		"wrong WAL checksum": func(t *testing.T, path string) {
			path, _ = walSnapshotFixtureAt(t, filepath.Dir(path), filepath.Base(path), "")
			wal, err := os.ReadFile(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			wal[24] ^= 0x80
			writeSidecar(t, path+"-wal", wal, 0o600)
		},
		"wrong WAL version": func(t *testing.T, path string) {
			path, _ = walSnapshotFixtureAt(t, filepath.Dir(path), filepath.Base(path), "")
			wal, err := os.ReadFile(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint32(wal[4:8], walFormatVersion+1)
			writeSidecar(t, path+"-wal", wal, 0o600)
		},
		"WAL without SHM": func(t *testing.T, path string) {
			path, _ = walSnapshotFixtureAt(t, filepath.Dir(path), filepath.Base(path), "")
			if err := os.Remove(path + "-shm"); err != nil {
				t.Fatal(err)
			}
		},
		"SHM without WAL": func(t *testing.T, path string) {
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"WAL symlink": func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target-wal")
			writeSidecar(t, target, make([]byte, walHeaderSize), 0o600)
			if err := os.Symlink(target, path+"-wal"); err != nil {
				t.Fatal(err)
			}
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"WAL fifo": func(t *testing.T, path string) {
			if err := unix.Mkfifo(path+"-wal", 0o600); err != nil {
				t.Fatal(err)
			}
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"WAL hard link": func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target-wal")
			writeSidecar(t, target, make([]byte, walHeaderSize), 0o600)
			if err := os.Link(target, path+"-wal"); err != nil {
				t.Fatal(err)
			}
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"oversized WAL": func(t *testing.T, path string) {
			file, err := os.OpenFile(path+"-wal", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxSQLiteWALSize + 1); err != nil {
				t.Fatal(err)
			}
			file.Close()
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize), 0o600)
		},
		"mis-sized SHM": func(t *testing.T, path string) {
			writeSidecar(t, path+"-wal", make([]byte, walHeaderSize), 0o600)
			writeSidecar(t, path+"-shm", make([]byte, walIndexRegionSize+1), 0o600)
		},
		"oversized SHM": func(t *testing.T, path string) {
			writeSidecar(t, path+"-wal", make([]byte, walHeaderSize), 0o600)
			file, err := os.OpenFile(path+"-shm", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxSQLiteSHMSize + walIndexRegionSize); err != nil {
				t.Fatal(err)
			}
			file.Close()
		},
	} {
		t.Run(name, func(t *testing.T) {
			image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "kernel.db")
			if err := os.WriteFile(path, image, 0o600); err != nil {
				t.Fatal(err)
			}
			mutate(t, path)
			before := captureSQLiteSet(t, path)
			if _, err := Open(context.Background(), path); err == nil {
				t.Fatal("unsafe WAL sidecars passed Open")
			}
			assertSQLiteSetUnchanged(t, path, before)
		})
	}
}

func TestWALPreflightAlwaysRemovesPrivateCopy(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)

	validPath, _ := walSnapshotFixture(t, "")
	valid, err := Open(context.Background(), validPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	assertDirectoryEmpty(t, temporaryRoot)

	invalidPath, _ := walSnapshotFixture(t, `UPDATE factory SET next_invalidation_sequence = 3`)
	if _, err := Open(context.Background(), invalidPath); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("invalid WAL Open error = %v", err)
	}
	assertDirectoryEmpty(t, temporaryRoot)
}

func TestValidateWALHeaderFailsClosed(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	main, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pageSize, err := databasePageSize(main)
	main.Close()
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWAL(bytes.NewReader(wal), int64(len(wal)), pageSize); err != nil {
		t.Fatalf("valid WAL header: %v", err)
	}
	if err := validateWAL(bytes.NewReader(nil), 0, pageSize); err != nil {
		t.Fatalf("valid empty WAL: %v", err)
	}
	if err := validateWAL(bytes.NewReader([]byte("malformed WAL")), 13, pageSize); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("malformed WAL error = %v", err)
	}
	corrupt := append([]byte(nil), wal...)
	corrupt[24] ^= 0x80
	if err := validateWAL(bytes.NewReader(corrupt), int64(len(corrupt)), pageSize); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("checksum-corrupt WAL error = %v", err)
	}
}

func TestDatabaseFileBindingsAreRecheckedAfterPreflight(t *testing.T) {
	newStandalone := func(t *testing.T) string {
		t.Helper()
		image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "kernel.db")
		writeSidecar(t, path, image, 0o600)
		return path
	}

	t.Run("main rebound", func(t *testing.T) {
		path := newStandalone(t)
		files, err := openDatabaseFiles(path)
		if err != nil {
			t.Fatal(err)
		}
		defer files.Close()
		if err := preflightExisting(context.Background(), files); err != nil {
			t.Fatal(err)
		}
		moved := path + ".old"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		writeSidecar(t, path, make([]byte, 4096), 0o600)
		if err := files.recheckPaths(); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("rebound main recheck error = %v", err)
		}
	})

	t.Run("journal appeared", func(t *testing.T) {
		path := newStandalone(t)
		files, err := openDatabaseFiles(path)
		if err != nil {
			t.Fatal(err)
		}
		defer files.Close()
		if err := preflightExisting(context.Background(), files); err != nil {
			t.Fatal(err)
		}
		writeSidecar(t, path+"-journal", []byte("appeared"), 0o600)
		if err := files.recheckPaths(); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("appeared journal recheck error = %v", err)
		}
	})

	t.Run("standalone main changed in place", func(t *testing.T) {
		path := newStandalone(t)
		files, err := openDatabaseFiles(path)
		if err != nil {
			t.Fatal(err)
		}
		defer files.Close()
		if err := preflightExisting(context.Background(), files); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte{0xff}, 4096); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
		if err := files.recheckPaths(); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("in-place main mutation recheck error = %v", err)
		}
	})
}

func TestOpenFreshRollbackRequiresExactChronology(t *testing.T) {
	for name, statement := range map[string]string{
		"revision":              `UPDATE factory SET revision = 2`,
		"head":                  `UPDATE factory SET next_invalidation_sequence = 2`,
		"floor":                 `UPDATE factory SET next_invalidation_sequence = 2, invalidation_floor = 2`,
		"retained sequence row": `INSERT INTO sqlite_sequence(name, seq) VALUES('browser_security_events', 1)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := mutatedRollbackPath(t, statement)
			before := captureSQLiteSet(t, path)
			if _, err := Open(context.Background(), path); err == nil {
				t.Fatal("non-pristine rollback database passed Open")
			}
			assertSQLiteSetUnchanged(t, path, before)
		})
	}
}

func TestInspectImmutableValidStateIsReadOnlyAndKeepsReaderOpen(t *testing.T) {
	ctx := context.Background()
	image, err := NewDatabaseImage(ctx, FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), image...)
	reader := bytes.NewReader(image)
	if err := InspectImmutable(ctx, reader, int64(len(image))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(image, before) {
		t.Fatal("immutable inspection modified caller bytes")
	}
	probe := make([]byte, 16)
	if _, err := reader.ReadAt(probe, 0); err != nil || string(probe) != "SQLite format 3\x00" {
		t.Fatalf("caller reader was consumed or closed: %q, %v", probe, err)
	}

	store, path := newTestStore(t)
	project := NewProject{ID: projectID(t, 51), Name: "retained", Root: filepath.Join(t.TempDir(), "project")}
	if _, err := store.CreateProject(ctx, project, mustTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	evidence := captureDatabaseEvidence(t, path)
	if err := InspectImmutable(ctx, file, info.Size()); err != nil {
		t.Fatalf("InspectImmutable mutated database: %v", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, evidence)
	if _, err := file.ReadAt(probe, 0); err != nil {
		t.Fatalf("InspectImmutable took reader ownership: %v", err)
	}
}

func TestInspectImmutableRejectsForeignTruncatedAndCorruptState(t *testing.T) {
	ctx := context.Background()
	image, err := NewDatabaseImage(ctx, FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}

	foreign := append([]byte(nil), image...)
	clear(foreign[68:72])
	if err := InspectImmutable(ctx, bytes.NewReader(foreign), int64(len(foreign))); !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("foreign application error = %v", err)
	}
	truncated := image[:len(image)/2]
	if err := InspectImmutable(ctx, bytes.NewReader(truncated), int64(len(truncated))); err == nil {
		t.Fatal("truncated image passed immutable inspection")
	}
	if err := InspectImmutable(ctx, bytes.NewReader(image), int64(len(image))+4096); err == nil {
		t.Fatal("larger declared size passed immutable inspection")
	}
	withTrailingBytes := append(append([]byte(nil), image...), make([]byte, 512)...)
	if err := InspectImmutable(ctx, bytes.NewReader(withTrailingBytes), int64(len(withTrailingBytes))); err == nil {
		t.Fatal("trailing bytes passed immutable inspection")
	}
	for name, pair := range map[string][2]byte{"mismatch": {1, 2}, "unknown": {3, 3}} {
		t.Run("header_"+name, func(t *testing.T) {
			changed := append([]byte(nil), image...)
			changed[18], changed[19] = pair[0], pair[1]
			if err := InspectImmutable(ctx, bytes.NewReader(changed), int64(len(changed))); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("header %d/%d error = %v", pair[0], pair[1], err)
			}
		})
	}
	if err := validateDatabaseHeader(bytes.NewReader(image), int64(len(image)), true); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("rollback header with WAL sidecars error = %v", err)
	}
	if err := InspectImmutable(ctx, nil, int64(len(image))); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("nil reader error = %v", err)
	}
	if err := InspectImmutable(ctx, bytes.NewReader(image), 0); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero size error = %v", err)
	}
	if err := InspectImmutable(ctx, bytes.NewReader(image), -1); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("negative size error = %v", err)
	}
	readerFailure := errors.New("injected ReaderAt failure")
	if err := InspectImmutable(ctx, failingReaderAt{err: readerFailure}, int64(len(image))); !errors.Is(err, readerFailure) || !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("ReaderAt failure error = %v", err)
	}
	shortReader := &shortNilReaderAt{reader: bytes.NewReader(image), after: 0}
	if err := InspectImmutable(ctx, shortReader, int64(len(image))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short nil ReaderAt error = %v", err)
	}
	laterFailure := errors.New("injected later ReaderAt failure")
	laterReader := &failAfterReaderAt{reader: bytes.NewReader(image), after: 100, err: laterFailure}
	if err := InspectImmutable(ctx, laterReader, int64(len(image))); !errors.Is(err, laterFailure) {
		t.Fatalf("later ReaderAt failure error = %v", err)
	}

	for name, mutation := range map[string]string{
		"schema":    `DROP INDEX tasks_canonical_queue`,
		"controls":  `UPDATE factory SET next_invalidation_sequence = 2`,
		"integrity": `PRAGMA writable_schema = ON; UPDATE sqlite_schema SET rootpage = 2147483647 WHERE type = 'index' AND name = 'projects_root_unique'; PRAGMA writable_schema = OFF`,
	} {
		t.Run(name, func(t *testing.T) {
			contents := mutatedDatabaseBytes(t, mutation)
			if err := InspectImmutable(ctx, bytes.NewReader(contents), int64(len(contents))); err == nil {
				t.Fatalf("%s corruption passed immutable inspection", name)
			}
		})
	}
}

func TestInspectImmutableRejectsOversizeBeforeReading(t *testing.T) {
	reader := &readExtentRecorder{reader: bytes.NewReader(nil)}
	if err := InspectImmutable(context.Background(), reader, maxImmutableDatabaseImageSize+1); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("oversized immutable image error = %v", err)
	}
	if reader.MaxEnd() != 0 {
		t.Fatal("oversized immutable image touched reader")
	}
}

func TestInspectImmutableExactSizeBoundReachesReader(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	reader := &readExtentRecorder{reader: bytes.NewReader(image)}
	if err := InspectImmutable(context.Background(), reader, maxImmutableDatabaseImageSize); err == nil {
		t.Fatal("declared exact immutable bound unexpectedly passed short image")
	}
	if reader.MaxEnd() == 0 || reader.MaxEnd() > maxImmutableDatabaseImageSize {
		t.Fatalf("exact-bound reader extent = %d", reader.MaxEnd())
	}
}

func TestCreateUsesPrivateSQLiteSidecars(t *testing.T) {
	store, path := newTestStore(t)
	defer store.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("sqlite sidecar %s mode = %v", suffix, info.Mode())
		}
	}
}

func TestInspectImmutableEnforcesDeclaredSizeBeforeReader(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	declared := int64(len(image) / 2)
	reader := &readExtentRecorder{reader: bytes.NewReader(image)}
	if err := InspectImmutable(context.Background(), reader, declared); err == nil {
		t.Fatal("smaller declared size passed immutable inspection")
	}
	if got := reader.MaxEnd(); got > declared {
		t.Fatalf("underlying reader was accessed through %d beyond declared size %d", got, declared)
	}
}

func TestInspectImmutableRegistrationsAreUniqueAndAlwaysDeleted(t *testing.T) {
	ctx := context.Background()
	image, err := NewDatabaseImage(ctx, FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	start := immutableReaderSequence.Load()
	const inspections = 24
	errorsByInspection := make(chan error, inspections)
	var wait sync.WaitGroup
	for range inspections {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByInspection <- InspectImmutable(ctx, bytes.NewReader(image), int64(len(image)))
		}()
	}
	wait.Wait()
	close(errorsByInspection)
	for err := range errorsByInspection {
		if err != nil {
			t.Fatalf("concurrent inspection: %v", err)
		}
	}
	end := immutableReaderSequence.Load()
	if end-start != inspections {
		t.Fatalf("immutable reader names consumed = %d, want %d", end-start, inspections)
	}
	for sequence := start + 1; sequence <= end; sequence++ {
		assertImmutableRegistrationAbsent(t, sequence)
	}

	preCancelled, cancel := context.WithCancel(ctx)
	cancel()
	preCancelledReader := &readExtentRecorder{reader: bytes.NewReader(image)}
	beforeCancelled := immutableReaderSequence.Load()
	if err := InspectImmutable(preCancelled, preCancelledReader, int64(len(image))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection error = %v", err)
	}
	if immutableReaderSequence.Load() != beforeCancelled || preCancelledReader.MaxEnd() != 0 {
		t.Fatal("pre-cancelled inspection touched reader or global registration")
	}

	midCancelled, cancelMid := context.WithCancel(ctx)
	midCancelledReader := cancelAfterRead{reader: bytes.NewReader(image), cancel: cancelMid}
	beforeMidCancelled := immutableReaderSequence.Load()
	if err := InspectImmutable(midCancelled, midCancelledReader, int64(len(image))); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-inspection cancellation error = %v", err)
	}
	if immutableReaderSequence.Load() != beforeMidCancelled+1 {
		t.Fatal("mid-inspection cancellation did not reserve exactly one reader name")
	}
	assertImmutableRegistrationAbsent(t, beforeMidCancelled+1)

	corrupt := append([]byte(nil), image...)
	clear(corrupt[68:72])
	beforeCorrupt := immutableReaderSequence.Load()
	if err := InspectImmutable(ctx, bytes.NewReader(corrupt), int64(len(corrupt))); !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("corrupt inspection error = %v", err)
	}
	assertImmutableRegistrationAbsent(t, beforeCorrupt+1)
}

func TestInspectImmutableRegistrationSequenceFailsBeforeWrap(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	previous := immutableReaderSequence.Load()
	immutableReaderSequence.Store(math.MaxUint64 - 1)
	t.Cleanup(func() { immutableReaderSequence.Store(previous) })

	if err := InspectImmutable(context.Background(), bytes.NewReader(image), int64(len(image))); err != nil {
		t.Fatalf("last immutable reader name: %v", err)
	}
	assertImmutableRegistrationAbsent(t, math.MaxUint64)

	wrappedName := "dark-factory-kernel-0"
	wrappedSentinel := sqliteIO.NewSizeReaderAt(bytes.NewReader(image))
	readervfs.Create(wrappedName, wrappedSentinel)
	t.Cleanup(func() { readervfs.Delete(wrappedName) })
	if err := InspectImmutable(context.Background(), bytes.NewReader(image), int64(len(image))); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("exhausted immutable reader sequence error = %v", err)
	}
	query := url.Values{"vfs": {"reader"}}
	pool, err := sql.Open(driverName, "file:"+wrappedName+"?"+query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatalf("exhaustion overwrote or deleted existing registration: %v", err)
	}
}

func openImageStore(t *testing.T, image []byte) (FactoryState, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factory.sqlite3")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open image: %v", err)
	}
	state, err := store.Factory(context.Background())
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return state, path
}

func mutatedDatabaseBytes(t *testing.T, mutation string) []byte {
	t.Helper()
	store, path := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw := openRaw(t, path)
	if _, err := raw.Exec(mutation); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func walSnapshotFixture(t *testing.T, mutation string) (string, Project) {
	t.Helper()
	directory := t.TempDir()
	return walSnapshotFixtureAt(t, directory, "kernel.db", mutation)
}

func walSnapshotFixtureAt(t *testing.T, directory, name, mutation string) (string, Project) {
	t.Helper()
	store, sourcePath := newTestStore(t)
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID(t, 81), Name: "wal-only", Root: filepath.Join(t.TempDir(), "project")}, mustTime(t, 2))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if mutation != "" {
		if _, err := store.writer.ExecContext(context.Background(), mutation); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	mainBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	walBytes, err := os.ReadFile(sourcePath + "-wal")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, name)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(target + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	writeSidecar(t, target, mainBytes, 0o600)
	writeSidecar(t, target+"-wal", walBytes, 0o600)
	writeSidecar(t, target+"-shm", make([]byte, walIndexRegionSize), 0o600)
	return target, project
}

func writeSidecar(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func mutatedRollbackPath(t *testing.T, statement string) string {
	t.Helper()
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kernel.db")
	writeSidecar(t, path, image, 0o600)
	raw := openRaw(t, path)
	if _, err := raw.Exec(statement); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory retained entries: %v", entries)
	}
}

type sqliteFileEvidence struct {
	exists bool
	info   os.FileInfo
	hash   [sha256.Size]byte
	hashed bool
}

type sqliteSetEvidence struct {
	files      map[string]sqliteFileEvidence
	dirEntries []string
}

func captureSQLiteSet(t *testing.T, path string) sqliteSetEvidence {
	t.Helper()
	result := sqliteSetEvidence{files: make(map[string]sqliteFileEvidence)}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		candidate := path + suffix
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			result.files[suffix] = sqliteFileEvidence{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		evidence := sqliteFileEvidence{exists: true, info: info}
		if info.Mode().IsRegular() && info.Size() <= 2<<20 {
			contents, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			evidence.hash = sha256.Sum256(contents)
			evidence.hashed = true
		}
		result.files[suffix] = evidence
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		result.dirEntries = append(result.dirEntries, entry.Name()+":"+entry.Type().String())
	}
	return result
}

func assertSQLiteSetUnchanged(t *testing.T, path string, before sqliteSetEvidence) {
	t.Helper()
	after := captureSQLiteSet(t, path)
	if !equalStrings(before.dirEntries, after.dirEntries) {
		t.Fatalf("sqlite directory entries changed: before=%v after=%v", before.dirEntries, after.dirEntries)
	}
	for suffix, want := range before.files {
		got := after.files[suffix]
		if got.exists != want.exists {
			t.Fatalf("sqlite file %q existence changed", suffix)
		}
		if !want.exists {
			continue
		}
		if !os.SameFile(want.info, got.info) || want.info.Size() != got.info.Size() || want.info.Mode() != got.info.Mode() || !want.info.ModTime().Equal(got.info.ModTime()) || want.hashed != got.hashed || want.hashed && want.hash != got.hash {
			t.Fatalf("sqlite file %q changed", suffix)
		}
	}
}

func directoryEntryNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name()
	}
	return result
}

type readExtentRecorder struct {
	mu     sync.Mutex
	reader *bytes.Reader
	maxEnd int64
}

type cancelAfterRead struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

type failingReaderAt struct{ err error }

type shortNilReaderAt struct {
	reader io.ReaderAt
	after  int64
}

type failAfterReaderAt struct {
	reader io.ReaderAt
	after  int64
	err    error
}

func (reader failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, reader.err }

func (reader *shortNilReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset >= reader.after && len(buffer) > 1 {
		read, _ := reader.reader.ReadAt(buffer[:len(buffer)-1], offset)
		return read, nil
	}
	return reader.reader.ReadAt(buffer, offset)
}

func (reader *failAfterReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < reader.after && offset+int64(len(buffer)) <= reader.after {
		return reader.reader.ReadAt(buffer, offset)
	}
	read, _ := reader.reader.ReadAt(buffer, offset)
	return read, reader.err
}

func (reader cancelAfterRead) ReadAt(buffer []byte, offset int64) (int, error) {
	read, err := reader.reader.ReadAt(buffer, offset)
	reader.cancel()
	return read, err
}

func (reader *readExtentRecorder) ReadAt(buffer []byte, offset int64) (int, error) {
	reader.mu.Lock()
	if end := offset + int64(len(buffer)); end > reader.maxEnd {
		reader.maxEnd = end
	}
	reader.mu.Unlock()
	return reader.reader.ReadAt(buffer, offset)
}

func (reader *readExtentRecorder) MaxEnd() int64 {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.maxEnd
}

func assertImmutableRegistrationAbsent(t *testing.T, sequence uint64) {
	t.Helper()
	name := fmt.Sprintf("dark-factory-kernel-%d", sequence)
	query := url.Values{"vfs": {"reader"}}
	pool, err := sql.Open(driverName, "file:"+name+"?"+query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.PingContext(context.Background()); err == nil {
		t.Fatalf("immutable reader registration %q remained usable", name)
	}
}
