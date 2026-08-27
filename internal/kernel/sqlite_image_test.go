package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

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

func (reader failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, reader.err }

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
