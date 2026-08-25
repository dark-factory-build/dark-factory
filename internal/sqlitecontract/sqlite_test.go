package sqlitecontract

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	applicationID = 0x4446474f
	userVersion   = 1
	helperEnv     = "DARK_FACTORY_SQLITE_CONTRACT_HELPER"
)

var errGuardedWrite = errors.New("guarded write changed an unexpected row count")

func TestSQLiteContract(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "every physical connection", run: testEveryPhysicalConnection},
		{name: "WAL reopen and pinned reader", run: testWALReopenAndPinnedReader},
		{name: "immediate writer busy bound", run: testImmediateWriterBusyBound},
		{name: "guarded rows and atomic footprint", run: testGuardedRowsAndAtomicFootprint},
		{name: "rollback and discard", run: testRollbackAndDiscard},
		{name: "cancellation cleanup", run: testCancellationCleanup},
		{name: "independent process immediate exclusion", run: testIndependentProcessImmediateExclusion},
		{name: "independent process admission", run: testIndependentProcessAdmission},
		{name: "SIGKILL before commit", run: testSIGKILLBeforeCommit},
		{name: "post-commit acknowledgement", run: testPostCommitAcknowledgement},
		{name: "lost reply reconciliation", run: testLostReplyReconciliation},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func testEveryPhysicalConnection(t *testing.T) {
	database, _ := newFixture(t)
	ctx := context.Background()

	writer, err := database.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	readers := make([]*sql.Conn, 0, maxReaders)
	for range maxReaders {
		connection, err := database.readers.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, connection)
	}
	defer func() {
		for _, connection := range readers {
			_ = connection.Close()
		}
	}()

	connections := append([]*sql.Conn{writer}, readers...)
	for index, connection := range connections {
		if err := verifyConnection(ctx, connection); err != nil {
			t.Fatalf("physical connection %d: %v", index, err)
		}
		if _, err := connection.ExecContext(ctx, "INSERT INTO fixture_child(id, parent_id) VALUES(?, 999)", index+1); err == nil {
			t.Fatalf("physical connection %d did not enforce its foreign key", index)
		}
	}
	if got := database.readers.Stats().OpenConnections; got != maxReaders {
		t.Fatalf("forced reader connections = %d, want %d", got, maxReaders)
	}

	discardConnection(readers[0])
	replacement, err := database.readers.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readers[0] = replacement
	if err := verifyConnection(ctx, replacement); err != nil {
		t.Fatalf("replacement connection: %v", err)
	}
	if _, err := replacement.ExecContext(ctx, "INSERT INTO fixture_child(id, parent_id) VALUES(99, 999)"); err == nil {
		t.Fatal("replacement connection did not enforce its foreign key")
	}

	var appID, version int
	if err := writer.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if appID != applicationID || version != userVersion {
		t.Fatalf("fixture identity = (%#x, %d), want (%#x, %d)", appID, version, applicationID, userVersion)
	}
	discardConnection(writer)
	writerReplacement, err := database.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerReplacement.Close()
	if err := verifyConnection(ctx, writerReplacement); err != nil {
		t.Fatalf("replacement writer connection: %v", err)
	}
	if _, err := writerReplacement.ExecContext(ctx, "INSERT INTO fixture_child(id, parent_id) VALUES(100, 999)"); err == nil {
		t.Fatal("replacement writer connection did not enforce its foreign key")
	}
}

func testWALReopenAndPinnedReader(t *testing.T) {
	database, path := newFixture(t)
	ctx := context.Background()

	reader, err := database.readers.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(context.Background(), "ROLLBACK")
	assertSnapshot(t, reader, "queued", 0)

	if err := database.immediate(ctx, func(writer *sql.Conn) error {
		if _, err := writer.ExecContext(ctx, "UPDATE fixture_queue SET status = 'running' WHERE id = 'task-1'"); err != nil {
			return err
		}
		if _, err := writer.ExecContext(ctx, "INSERT INTO fixture_events(entity_id) VALUES('run-wal')"); err != nil {
			return err
		}
		freshReader, err := database.readers.Conn(ctx)
		if err != nil {
			return err
		}
		defer freshReader.Close()
		assertSnapshot(t, freshReader, "queued", 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, reader, "queued", 0)
	if _, err := reader.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, database.readers, "running", 1)

	if err := database.close(); err != nil {
		t.Fatal(err)
	}
	observation, err := sql.Open(driverName, (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	var persistedJournal string
	if err := observation.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&persistedJournal); err != nil {
		_ = observation.Close()
		t.Fatal(err)
	}
	if err := observation.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(persistedJournal) != "wal" {
		t.Fatalf("journal mode on unconfigured reopen = %q, want wal", persistedJournal)
	}
	reopened, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	assertSnapshot(t, reopened.readers, "running", 1)
	if err := quickCheck(reopened); err != nil {
		t.Fatal(err)
	}
}

func testImmediateWriterBusyBound(t *testing.T) {
	first, path := newFixture(t)
	second, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()

	ctx := context.Background()
	owner, err := first.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer owner.ExecContext(context.Background(), "ROLLBACK")

	started := time.Now()
	err = second.immediate(ctx, func(*sql.Conn) error { return nil })
	elapsed := time.Since(started)
	if !errors.Is(err, errBusy) {
		t.Fatalf("second immediate writer error = %v, want typed busy", err)
	}
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Fatalf("busy wait = %s, want explicit bounded wait around 5s", elapsed)
	}

	if _, err := owner.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := second.immediate(ctx, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("second writer did not succeed after release: %v", err)
	}
}

func testGuardedRowsAndAtomicFootprint(t *testing.T) {
	database, _ := newFixture(t)
	ctx := context.Background()

	won, err := guardedAdmission(ctx, database, "run-rows")
	if err != nil || !won {
		t.Fatalf("first guarded admission = (%v, %v), want won", won, err)
	}
	won, err = guardedAdmission(ctx, database, "run-rows")
	if err != nil || won {
		t.Fatalf("replayed guarded admission = (%v, %v), want clean loss", won, err)
	}
	assertFootprint(t, database, "run-rows", 1, 1)

	err = database.immediate(ctx, func(connection *sql.Conn) error {
		result, err := connection.ExecContext(ctx, "UPDATE fixture_queue SET status = 'running' WHERE id = 'task-1' AND status = 'queued'")
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return errGuardedWrite
		}
		return nil
	})
	if !errors.Is(err, errGuardedWrite) {
		t.Fatalf("suppressed guarded write error = %v, want %v", err, errGuardedWrite)
	}
	assertFootprint(t, database, "run-rows", 1, 1)

	err = database.immediate(ctx, func(connection *sql.Conn) error {
		if _, err := connection.ExecContext(ctx, "INSERT INTO fixture_transitions(id, revision) VALUES('event-fails', 1)"); err != nil {
			return err
		}
		_, err := connection.ExecContext(ctx, "INSERT INTO fixture_events(entity_id) VALUES(NULL)")
		return err
	})
	if err == nil {
		t.Fatal("invalid event unexpectedly committed")
	}
	assertFootprint(t, database, "event-fails", 0, 0)

	err = database.immediate(ctx, func(connection *sql.Conn) error {
		if _, err := connection.ExecContext(ctx, "INSERT INTO fixture_events(entity_id) VALUES('state-fails')"); err != nil {
			return err
		}
		_, err := connection.ExecContext(ctx, "INSERT INTO fixture_transitions(id, revision) VALUES('state-fails', NULL)")
		return err
	})
	if err == nil {
		t.Fatal("invalid state unexpectedly committed")
	}
	assertFootprint(t, database, "state-fails", 0, 0)
}

func testRollbackAndDiscard(t *testing.T) {
	database, _ := newFixture(t)
	ctx := context.Background()
	bodyError := errors.New("body failed")

	err := database.immediate(ctx, func(connection *sql.Conn) error {
		if err := insertFootprint(ctx, connection, "body-error"); err != nil {
			return err
		}
		return bodyError
	})
	if !errors.Is(err, bodyError) {
		t.Fatalf("body error = %v, want %v", err, bodyError)
	}
	assertFootprint(t, database, "body-error", 0, 0)

	panicked := false
	func() {
		defer func() {
			panicked = recover() == "fixture panic"
		}()
		_ = database.immediate(ctx, func(connection *sql.Conn) error {
			if err := insertFootprint(ctx, connection, "panic"); err != nil {
				return err
			}
			panic("fixture panic")
		})
	}()
	if !panicked {
		t.Fatal("immediate helper swallowed the body panic")
	}
	assertFootprint(t, database, "panic", 0, 0)

	cancelContext, cancel := context.WithCancel(context.Background())
	err = database.immediate(cancelContext, func(connection *sql.Conn) error {
		if err := insertFootprint(cancelContext, connection, "cancel"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	assertFootprint(t, database, "cancel", 0, 0)

	err = database.immediate(ctx, func(connection *sql.Conn) error {
		if err := insertFootprint(ctx, connection, "rollback-discard"); err != nil {
			return err
		}
		discardConnection(connection)
		return bodyError
	})
	var unknown *outcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, bodyError) {
		t.Fatalf("discarded rollback error = %v, want outcomeUnknownError preserving body error", err)
	}
	assertFootprint(t, database, "rollback-discard", 0, 0)

	err = database.immediate(ctx, func(connection *sql.Conn) error {
		if err := insertFootprint(ctx, connection, "discard"); err != nil {
			return err
		}
		discardConnection(connection)
		return nil
	})
	unknown = nil
	if !errors.As(err, &unknown) {
		t.Fatalf("discarded commit error = %v, want outcomeUnknownError", err)
	}
	assertFootprint(t, database, "discard", 0, 0)
	if err := database.immediate(ctx, func(connection *sql.Conn) error {
		return insertFootprint(ctx, connection, "replacement")
	}); err != nil {
		t.Fatalf("replacement writer connection failed: %v", err)
	}
	assertFootprint(t, database, "replacement", 1, 1)
}

func testCancellationCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.sqlite3")
	database, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeFixture(database); err != nil {
		t.Fatal(err)
	}
	baseline := runtime.NumGoroutine()

	for index := range 32 {
		ctx, cancel := context.WithCancel(context.Background())
		err := database.immediate(ctx, func(connection *sql.Conn) error {
			if err := insertFootprint(ctx, connection, fmt.Sprintf("cancel-%d", index)); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d cancellation error = %v", index, err)
		}
	}
	if stats := database.writer.Stats(); stats.InUse != 0 || stats.OpenConnections > 1 {
		t.Fatalf("writer stats after cancellation: %+v", stats)
	}
	if err := database.close(); err != nil {
		t.Fatal(err)
	}
	if targets := fileDescriptorsFor(path); len(targets) != 0 {
		t.Fatalf("database descriptors survived close: %v", targets)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Fatalf("goroutines after scoped cancellations = %d, baseline %d", got, baseline)
	}
}

func testIndependentProcessAdmission(t *testing.T) {
	path := initializedFixturePath(t)
	first := startGatedHelper(t, "admit", path, "run-process")
	second := startGatedHelper(t, "admit", path, "run-process")
	first.ready(t)
	second.ready(t)
	first.release(t)
	second.release(t)
	if err := first.wait(t); err != nil {
		t.Fatalf("first helper: %v\n%s", err, first.stderr.String())
	}
	if err := second.wait(t); err != nil {
		t.Fatalf("second helper: %v\n%s", err, second.stderr.String())
	}
	results := []string{first.outputWord(t), second.outputWord(t)}
	sort.Strings(results)
	if strings.Join(results, ",") != "lost,won" {
		t.Fatalf("independent helper results = %v, want one loss and one win", results)
	}

	database := reopenFixture(t, path)
	defer database.close()
	assertQueueAndTotalFootprint(t, database, "running", 1, 1)
}

func testIndependentProcessImmediateExclusion(t *testing.T) {
	path := initializedFixturePath(t)
	owner := startGatedHelper(t, "hold-immediate", path, "owner")
	contender := startGatedHelper(t, "hold-immediate", path, "contender")
	owner.ready(t)
	contender.ready(t)

	owner.send(t, false)
	owner.ready(t)
	contender.send(t, false)
	contender.expectExitWithoutReady(t)
	if err := contender.wait(t); err == nil || !strings.Contains(contender.stderr.String(), errBusy.Error()) {
		t.Fatalf("contender = %v, stderr %q; want typed busy before body", err, contender.stderr.String())
	}

	owner.send(t, true)
	if err := owner.wait(t); err != nil {
		t.Fatalf("owner helper: %v\n%s", err, owner.stderr.String())
	}
}

func testSIGKILLBeforeCommit(t *testing.T) {
	path := initializedFixturePath(t)
	helper := startGatedHelper(t, "crash-before-commit", path, "run-crash")
	helper.ready(t)
	if err := helper.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := helper.wait(t); err == nil {
		t.Fatal("SIGKILLed helper unexpectedly succeeded")
	}

	database := reopenFixture(t, path)
	defer database.close()
	assertQueueAndTotalFootprint(t, database, "queued", 0, 0)
	if err := quickCheck(database); err != nil {
		t.Fatal(err)
	}
}

func testPostCommitAcknowledgement(t *testing.T) {
	path := initializedFixturePath(t)
	helper := startGatedHelper(t, "commit-ack", path, "run-ack")
	helper.ready(t)
	if err := helper.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := helper.wait(t); err == nil {
		t.Fatal("SIGKILLed helper unexpectedly succeeded")
	}

	database := reopenFixture(t, path)
	defer database.close()
	assertQueueAndTotalFootprint(t, database, "running", 1, 1)
	assertFootprint(t, database, "run-ack", 1, 1)
	if err := quickCheck(database); err != nil {
		t.Fatal(err)
	}
}

func testLostReplyReconciliation(t *testing.T) {
	path := initializedFixturePath(t)
	helper := startHelper(t, "lost-reply", path, "run-lost", nil)
	if err := helper.wait(t); err == nil {
		t.Fatal("lost-reply helper unexpectedly returned success")
	}
	if helper.stdout.Len() != 0 {
		t.Fatalf("lost-reply helper emitted a reply: %q", helper.stdout.String())
	}

	database := reopenFixture(t, path)
	defer database.close()
	assertFootprint(t, database, "run-lost", 1, 1)
	won, err := guardedAdmission(context.Background(), database, "run-lost")
	if err != nil || won {
		t.Fatalf("reconciled replay = (%v, %v), want no second transition", won, err)
	}
	assertQueueAndTotalFootprint(t, database, "running", 1, 1)
}

func newFixture(t *testing.T) (*database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contract.sqlite3")
	database, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.close() })
	if err := initializeFixture(database); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func initializedFixturePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contract.sqlite3")
	database, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeFixture(database); err != nil {
		_ = database.close()
		t.Fatal(err)
	}
	if err := database.close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func reopenFixture(t *testing.T, path string) *database {
	t.Helper()
	database, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func initializeFixture(database *database) error {
	ctx := context.Background()
	return database.immediate(ctx, func(connection *sql.Conn) error {
		statements := []string{
			fmt.Sprintf("PRAGMA application_id = %d", applicationID),
			fmt.Sprintf("PRAGMA user_version = %d", userVersion),
			"CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY) STRICT",
			"CREATE TABLE fixture_child(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES fixture_parent(id)) STRICT",
			"CREATE TABLE fixture_queue(id TEXT PRIMARY KEY, status TEXT NOT NULL CHECK(status IN ('queued', 'running'))) STRICT",
			"CREATE TABLE fixture_transitions(id TEXT PRIMARY KEY, revision INTEGER NOT NULL) STRICT",
			"CREATE TABLE fixture_events(sequence INTEGER PRIMARY KEY AUTOINCREMENT, entity_id TEXT NOT NULL UNIQUE) STRICT",
			"INSERT INTO fixture_queue(id, status) VALUES('task-1', 'queued')",
		}
		for _, statement := range statements {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
}

func guardedAdmission(ctx context.Context, database *database, runID string) (bool, error) {
	won := false
	err := database.immediate(ctx, func(connection *sql.Conn) error {
		var status string
		if err := connection.QueryRowContext(ctx, "SELECT status FROM fixture_queue WHERE id = 'task-1'").Scan(&status); err != nil {
			return err
		}
		if status != "queued" {
			return nil
		}
		result, err := connection.ExecContext(ctx, "UPDATE fixture_queue SET status = 'running' WHERE id = 'task-1' AND status = 'queued'")
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return errGuardedWrite
		}
		if err := insertFootprint(ctx, connection, runID); err != nil {
			return err
		}
		won = true
		return nil
	})
	return won, err
}

func insertFootprint(ctx context.Context, connection *sql.Conn, id string) error {
	if _, err := connection.ExecContext(ctx, "INSERT INTO fixture_transitions(id, revision) VALUES(?, 1)", id); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "INSERT INTO fixture_events(entity_id) VALUES(?)", id); err != nil {
		return err
	}
	return nil
}

type snapshotQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func assertSnapshot(t *testing.T, query snapshotQuerier, wantStatus string, wantEvents int) {
	t.Helper()
	ctx := context.Background()
	var status string
	var events int
	if err := query.QueryRowContext(ctx, "SELECT status FROM fixture_queue WHERE id = 'task-1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || events != wantEvents {
		t.Fatalf("snapshot = (%q, %d), want (%q, %d)", status, events, wantStatus, wantEvents)
	}
}

func assertFootprint(t *testing.T, database *database, id string, wantState, wantEvents int) {
	t.Helper()
	ctx := context.Background()
	var state, events int
	if err := database.readers.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_transitions WHERE id = ?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := database.readers.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_events WHERE entity_id = ?", id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if state != wantState || events != wantEvents {
		t.Fatalf("footprint %q = (%d, %d), want (%d, %d)", id, state, events, wantState, wantEvents)
	}
}

func assertQueueAndTotalFootprint(t *testing.T, database *database, wantStatus string, wantState, wantEvents int) {
	t.Helper()
	ctx := context.Background()
	var status string
	var state, events int
	if err := database.readers.QueryRowContext(ctx, "SELECT status FROM fixture_queue WHERE id = 'task-1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := database.readers.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_transitions").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := database.readers.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || state != wantState || events != wantEvents {
		t.Fatalf("durable footprint = (%q, %d, %d), want (%q, %d, %d)", status, state, events, wantStatus, wantState, wantEvents)
	}
}

func quickCheck(database *database) error {
	var result string
	if err := database.readers.QueryRowContext(context.Background(), "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check = %q", result)
	}
	return nil
}

func fileDescriptorsFor(path string) []string {
	fdRoot := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdRoot = "/proc/self/fd"
	}
	entries, err := os.ReadDir(fdRoot)
	if err != nil {
		return []string{"cannot inspect " + fdRoot + ": " + err.Error()}
	}
	var matches []string
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdRoot, entry.Name()))
		if err == nil && strings.Contains(target, path) {
			matches = append(matches, entry.Name()+"="+target)
		}
	}
	return matches
}

type ownedHelper struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	readyR  *os.File
	startW  *os.File
	waited  bool
}

func startGatedHelper(t *testing.T, mode, path, runID string) *ownedHelper {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	startR, startW, err := os.Pipe()
	if err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		t.Fatal(err)
	}
	helper := startHelper(t, mode, path, runID, []*os.File{readyW, startR})
	helper.readyR = readyR
	helper.startW = startW
	_ = readyW.Close()
	_ = startR.Close()
	return helper
}

func startHelper(t *testing.T, mode, path, runID string, extraFiles []*os.File) *ownedHelper {
	t.Helper()
	helper := &ownedHelper{}
	helper.command = exec.Command(os.Args[0], "-test.run=^TestSQLiteContractHelper$")
	helper.command.Env = []string{
		helperEnv + "=1",
		"SQLITE_CONTRACT_MODE=" + mode,
		"SQLITE_CONTRACT_PATH=" + path,
		"SQLITE_CONTRACT_RUN_ID=" + runID,
		"TMPDIR=" + t.TempDir(),
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
	}
	helper.command.ExtraFiles = extraFiles
	helper.command.Stdout = &helper.stdout
	helper.command.Stderr = &helper.stderr
	if err := helper.command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !helper.waited {
			_ = helper.command.Process.Kill()
			_ = helper.command.Wait()
		}
		if helper.readyR != nil {
			_ = helper.readyR.Close()
		}
		if helper.startW != nil {
			_ = helper.startW.Close()
		}
	})
	return helper
}

func (h *ownedHelper) ready(t *testing.T) {
	t.Helper()
	if err := h.readyR.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(h.readyR, signal[:]); err != nil {
		t.Fatalf("helper readiness: %v\n%s", err, h.stderr.String())
	}
}

func (h *ownedHelper) release(t *testing.T) {
	t.Helper()
	h.send(t, true)
}

func (h *ownedHelper) send(t *testing.T, closeAfter bool) {
	t.Helper()
	if _, err := h.startW.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if closeAfter {
		if err := h.startW.Close(); err != nil {
			t.Fatal(err)
		}
		h.startW = nil
	}
}

func (h *ownedHelper) expectExitWithoutReady(t *testing.T) {
	t.Helper()
	if err := h.readyR.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(h.readyR, signal[:]); err == nil {
		t.Fatal("contending process entered the transaction body")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("contending process neither returned busy nor entered the transaction body")
	}
}

func (h *ownedHelper) wait(t *testing.T) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.command.Wait() }()
	select {
	case err := <-done:
		h.waited = true
		return err
	case <-time.After(20 * time.Second):
		_ = h.command.Process.Kill()
		err := <-done
		h.waited = true
		return fmt.Errorf("helper timed out: %w", err)
	}
}

func (h *ownedHelper) outputWord(t *testing.T) string {
	t.Helper()
	fields := strings.Fields(h.stdout.String())
	if len(fields) == 0 {
		t.Fatalf("helper emitted no result\n%s", h.stderr.String())
	}
	return fields[0]
}

func TestSQLiteContractHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	mode := os.Getenv("SQLITE_CONTRACT_MODE")
	path := os.Getenv("SQLITE_CONTRACT_PATH")
	runID := os.Getenv("SQLITE_CONTRACT_RUN_ID")
	database, err := openDatabase(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch mode {
	case "admit":
		if err := helperSignalAndWait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		won, err := guardedAdmission(context.Background(), database, runID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if won {
			fmt.Println("won")
		} else {
			fmt.Println("lost")
		}
		if err := database.close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "hold-immediate":
		ready, start, err := inheritedHelperPipes()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer ready.Close()
		defer start.Close()
		if err := signalAndWait(ready, start); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		err = database.immediate(context.Background(), func(*sql.Conn) error {
			return signalAndWait(ready, start)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(75)
		}
		if err := database.close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "crash-before-commit":
		connection, err := database.writer.Conn(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := connection.ExecContext(context.Background(), "UPDATE fixture_queue SET status = 'running' WHERE id = 'task-1' AND status = 'queued'"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := insertFootprint(context.Background(), connection, runID); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := helperSignalAndWait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "commit-ack":
		won, err := guardedAdmission(context.Background(), database, runID)
		if err != nil || !won {
			fmt.Fprintf(os.Stderr, "admission = (%v, %v)\n", won, err)
			os.Exit(2)
		}
		if err := helperSignalAndWait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "lost-reply":
		won, err := guardedAdmission(context.Background(), database, runID)
		if err != nil || !won {
			fmt.Fprintf(os.Stderr, "admission = (%v, %v)\n", won, err)
			os.Exit(2)
		}
		os.Exit(86)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}
}

func helperSignalAndWait() error {
	ready, start, err := inheritedHelperPipes()
	if err != nil {
		return err
	}
	defer ready.Close()
	defer start.Close()
	return signalAndWait(ready, start)
}

func inheritedHelperPipes() (*os.File, *os.File, error) {
	ready := os.NewFile(3, "ready")
	start := os.NewFile(4, "start")
	if ready == nil || start == nil {
		return nil, nil, errors.New("missing inherited helper pipes")
	}
	return ready, start, nil
}

func signalAndWait(ready, start *os.File) error {
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	var signal [1]byte
	_, err := io.ReadFull(start, signal[:])
	return err
}
