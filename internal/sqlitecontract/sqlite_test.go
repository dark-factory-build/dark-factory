package sqlitecontract

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

const (
	applicationID       = 0x4446474f
	userVersion         = 1
	expectedBusyTimeout = 5000
	helperEnv           = "DARK_FACTORY_SQLITE_CONTRACT_HELPER"
	helperWaitTimeout   = 12 * time.Second
)

var (
	errFaultResponse = errors.New("injected sqlite response failure")
	errGuardedWrite  = errors.New("guarded write changed an unexpected row count")
)

func TestSQLiteContract(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "every physical connection", run: testEveryPhysicalConnection},
		{name: "unsafe pooled connection rejected", run: testUnsafePooledConnectionRejected},
		{name: "WAL reopen and pinned reader", run: testWALReopenAndPinnedReader},
		{name: "immediate writer busy bound", run: testImmediateWriterBusyBound},
		{name: "busy cancellation", run: testBusyCancellation},
		{name: "guarded rows and atomic footprint", run: testGuardedRowsAndAtomicFootprint},
		{name: "rollback and discard", run: testRollbackAndDiscard},
		{name: "ambiguous begin and commit responses", run: testAmbiguousResponses},
		{name: "cancellation cleanup", run: testCancellationCleanup},
		{name: "helper start failure cleanup", run: testHelperStartFailureCleanup},
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
		assertExactConnectionPolicy(t, connection)
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
	assertExactConnectionPolicy(t, replacement)
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
	assertExactConnectionPolicy(t, writerReplacement)
	if _, err := writerReplacement.ExecContext(ctx, "INSERT INTO fixture_child(id, parent_id) VALUES(100, 999)"); err == nil {
		t.Fatal("replacement writer connection did not enforce its foreign key")
	}
}

func assertExactConnectionPolicy(t *testing.T, connection *sql.Conn) {
	t.Helper()
	var busy int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != expectedBusyTimeout {
		t.Fatalf("busy_timeout = %d, want literal 5000", busy)
	}
}

func testUnsafePooledConnectionRejected(t *testing.T) {
	database, _ := newFixture(t)
	ctx := context.Background()

	poisonedWriter, err := database.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poisonedWriter.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if err := poisonedWriter.Close(); err != nil {
		t.Fatal(err)
	}
	bodyEntered := false
	err = database.immediate(ctx, func(*sql.Conn) error {
		bodyEntered = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe sqlite connection configuration") {
		t.Fatalf("poisoned writer error = %v, want fail-closed verification", err)
	}
	if bodyEntered {
		t.Fatal("writer body ran before poisoned connection was rejected")
	}
	if got := database.writer.Stats().OpenConnections; got != 0 {
		t.Fatalf("poisoned writer remained pooled: %d connections", got)
	}
	if err := database.immediate(ctx, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("verified replacement writer failed: %v", err)
	}

	poisonedReader, err := database.readers.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poisonedReader.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if err := poisonedReader.Close(); err != nil {
		t.Fatal(err)
	}
	queryEntered := false
	reader, err := database.readerConnection(ctx)
	if err == nil {
		queryEntered = true
		_ = reader.QueryRowContext(ctx, "SELECT 1").Scan(new(int))
		_ = reader.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "unsafe sqlite connection configuration") {
		t.Fatalf("poisoned reader error = %v, want fail-closed verification", err)
	}
	if queryEntered {
		t.Fatal("reader query ran before poisoned connection was rejected")
	}
	if got := database.readers.Stats().OpenConnections; got != 0 {
		t.Fatalf("poisoned reader remained pooled: %d connections", got)
	}
	replacement, err := database.readerConnection(ctx)
	if err != nil {
		t.Fatalf("verified replacement reader failed: %v", err)
	}
	defer replacement.Close()
	var one int
	if err := replacement.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("replacement reader query = (%d, %v), want (1, nil)", one, err)
	}
}

func testWALReopenAndPinnedReader(t *testing.T) {
	database, path := newFixture(t)
	ctx := context.Background()

	reader, err := database.readerConnection(ctx)
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
		freshReader, err := database.readerConnection(ctx)
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
	assertDatabaseSnapshot(t, database, "running", 1)

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
	assertDatabaseSnapshot(t, reopened, "running", 1)
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
	owner, err := first.writerConnection(ctx)
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
	if elapsed < 4500*time.Millisecond || elapsed > 5750*time.Millisecond {
		t.Fatalf("busy wait = %s, want explicit bounded wait around 5s", elapsed)
	}

	if _, err := owner.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := second.immediate(ctx, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("second writer did not succeed after release: %v", err)
	}
}

func testBusyCancellation(t *testing.T) {
	ownerDatabase, path := newFixture(t)
	owner, err := ownerDatabase.writerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer owner.ExecContext(context.Background(), "ROLLBACK")

	plan := &faultPlan{beginEntered: make(chan struct{})}
	contender := openFaultDatabase(t, path, plan)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bodyEntered := false
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- contender.immediate(ctx, func(connection *sql.Conn) error {
			bodyEntered = true
			return insertFootprint(ctx, connection, "busy-cancel")
		})
	}()

	select {
	case <-plan.beginEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("contender never reached BEGIN IMMEDIATE")
	}
	select {
	case contenderErr := <-done:
		t.Fatalf("contender returned before cancellation instead of blocking: %v", contenderErr)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	var contenderErr error
	select {
	case contenderErr = <-done:
	case <-time.After(time.Second):
		t.Fatal("busy BEGIN IMMEDIATE ignored cancellation")
	}
	if !errors.Is(contenderErr, context.Canceled) {
		t.Fatalf("busy cancellation error = %v, want context.Canceled", contenderErr)
	}
	var unknown *outcomeUnknownError
	if !errors.As(contenderErr, &unknown) {
		t.Fatalf("busy cancellation error = %T %v, want outcomeUnknownError", contenderErr, contenderErr)
	}
	if bodyEntered {
		t.Fatal("contending body ran while another immediate writer held the lock")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("busy cancellation took %s, want under 1s", elapsed)
	}
	assertFootprint(t, ownerDatabase, "busy-cancel", 0, 0)
	contenderID, closed := plan.beginConnectionAndClosed()
	replacementID := assertConnectionEvicted(t, contender, contenderID, closed)

	if _, err := owner.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	usedID := 0
	if err := contender.immediate(context.Background(), func(connection *sql.Conn) error {
		usedID = faultConnectionID(t, connection)
		return insertFootprint(context.Background(), connection, "busy-cancel-reuse")
	}); err != nil {
		t.Fatalf("writer replacement after busy cancellation failed: %v", err)
	}
	if usedID != replacementID {
		t.Fatalf("later write used connection %d, want verified replacement %d", usedID, replacementID)
	}
	assertFootprint(t, contender, "busy-cancel-reuse", 1, 1)
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

func testAmbiguousResponses(t *testing.T) {
	t.Run("commit forwarded before response failure", func(t *testing.T) {
		testAmbiguousCommit(t, faultCommitAfterApply, "commit-after", 1)
	})
	t.Run("commit rejected before apply", func(t *testing.T) {
		testAmbiguousCommit(t, faultCommitBeforeApply, "commit-before", 0)
	})
	t.Run("rollback rejected before apply", func(t *testing.T) {
		testAmbiguousRollback(t, faultRollbackBeforeApply, "rollback-before")
	})
	t.Run("rollback forwarded before response failure", func(t *testing.T) {
		testAmbiguousRollback(t, faultRollbackAfterApply, "rollback-after")
	})
	t.Run("begin forwarded before response failure", func(t *testing.T) {
		path := initializedFixturePath(t)
		plan := &faultPlan{}
		database := openFaultDatabase(t, path, plan)
		plan.arm(faultBeginAfterApply)
		bodyInvocations := 0
		err := database.immediate(context.Background(), func(connection *sql.Conn) error {
			bodyInvocations++
			return insertFootprint(context.Background(), connection, "begin-after")
		})
		requireOutcomeUnknown(t, err)
		if bodyInvocations != 0 {
			t.Fatalf("body invocations after ambiguous begin = %d, want 0", bodyInvocations)
		}
		assertFaultedConnectionEvicted(t, database, plan)
		if err := database.close(); err != nil {
			t.Fatal(err)
		}
		reopened := reopenFixture(t, path)
		defer reopened.close()
		assertFootprint(t, reopened, "begin-after", 0, 0)
	})
}

func testAmbiguousRollback(t *testing.T, fault faultKind, id string) {
	t.Helper()
	path := initializedFixturePath(t)
	plan := &faultPlan{}
	database := openFaultDatabase(t, path, plan)
	plan.arm(fault)
	bodyError := errors.New("rollback body failed")
	bodyInvocations := 0
	err := database.immediate(context.Background(), func(connection *sql.Conn) error {
		bodyInvocations++
		if err := insertFootprint(context.Background(), connection, id); err != nil {
			return err
		}
		return bodyError
	})
	var unknown *outcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, bodyError) || !errors.Is(err, errFaultResponse) {
		t.Fatalf("ambiguous rollback error = %v, want outcomeUnknownError preserving body and response failures", err)
	}
	if bodyInvocations != 1 {
		t.Fatalf("body invocations after ambiguous rollback = %d, want exactly 1", bodyInvocations)
	}
	assertFaultedConnectionEvicted(t, database, plan)
	if err := database.close(); err != nil {
		t.Fatal(err)
	}

	reopened := reopenFixture(t, path)
	defer reopened.close()
	assertFootprint(t, reopened, id, 0, 0)
	reopenID := id + "-reopen"
	if err := reopened.immediate(context.Background(), func(connection *sql.Conn) error {
		return insertFootprint(context.Background(), connection, reopenID)
	}); err != nil {
		t.Fatalf("write after ambiguous rollback reopen: %v", err)
	}
	assertFootprint(t, reopened, reopenID, 1, 1)
}

func testAmbiguousCommit(t *testing.T, fault faultKind, id string, wantFootprint int) {
	t.Helper()
	path := initializedFixturePath(t)
	plan := &faultPlan{}
	database := openFaultDatabase(t, path, plan)
	plan.arm(fault)
	bodyInvocations := 0
	err := database.immediate(context.Background(), func(connection *sql.Conn) error {
		bodyInvocations++
		return insertFootprint(context.Background(), connection, id)
	})
	requireOutcomeUnknown(t, err)
	if bodyInvocations != 1 {
		t.Fatalf("body invocations after ambiguous commit = %d, want exactly 1", bodyInvocations)
	}
	assertFaultedConnectionEvicted(t, database, plan)
	if err := database.close(); err != nil {
		t.Fatal(err)
	}

	reopened := reopenFixture(t, path)
	defer reopened.close()
	assertFootprint(t, reopened, id, wantFootprint, wantFootprint)
	if got := footprintCount(t, reopened, id); got != wantFootprint {
		t.Fatalf("domain reconciliation for %q = %d, want %d without replay", id, got, wantFootprint)
	}
}

func requireOutcomeUnknown(t *testing.T, err error) {
	t.Helper()
	var unknown *outcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, errFaultResponse) {
		t.Fatalf("ambiguous response error = %v, want outcomeUnknownError preserving injected failure", err)
	}
}

func assertFaultedConnectionEvicted(t *testing.T, database *database, plan *faultPlan) {
	t.Helper()
	faultedID, closed := plan.faultedAndClosed()
	assertConnectionEvicted(t, database, faultedID, closed)
}

func assertConnectionEvicted(t *testing.T, database *database, faultedID int, closed bool) int {
	t.Helper()
	if faultedID == 0 || !closed {
		t.Fatalf("faulted connection = %d, closed = %v; want discarded physical connection", faultedID, closed)
	}
	replacement, err := database.writerConnection(context.Background())
	if err != nil {
		t.Fatalf("verified writer replacement: %v", err)
	}
	defer replacement.Close()
	replacementID := faultConnectionID(t, replacement)
	if replacementID == faultedID {
		t.Fatalf("ambiguous connection %d was reused", faultedID)
	}
	return replacementID
}

func testCancellationCleanup(t *testing.T) {
	baseline := runtime.NumGoroutine()
	path := filepath.Join(t.TempDir(), "cancel.sqlite3")
	database, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeFixture(database); err != nil {
		t.Fatal(err)
	}
	openDescriptors, err := fileDescriptorsFor(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(openDescriptors) == 0 {
		t.Fatal("file-identity descriptor census did not detect the known-open database")
	}

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
	targets, err := fileDescriptorsFor(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
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

func testHelperStartFailureCleanup(t *testing.T) {
	_, files, err := launchGatedHelper(t, filepath.Join(t.TempDir(), "missing-helper"), "admit", "unused", "unused")
	if err == nil {
		t.Fatal("missing helper executable unexpectedly started")
	}
	if len(files) != 6 {
		t.Fatalf("helper pipe ends = %d, want 6", len(files))
	}
	for index, file := range files {
		if _, statErr := file.Stat(); statErr == nil {
			t.Fatalf("helper pipe end %d remained open after Start failure", index)
		}
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
	results := []string{first.result(t), second.result(t)}
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
	contenderErr := contender.wait(t)
	requireExitCode(t, contenderErr, 75)
	if !strings.Contains(contender.stderr.String(), errBusy.Error()) {
		t.Fatalf("contender = %v, stderr %q; want typed busy before body", contenderErr, contender.stderr.String())
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
	requireSIGKILL(t, helper.wait(t))

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
	requireSIGKILL(t, helper.wait(t))

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
	requireExitCode(t, helper.wait(t), 86)
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

func requireSIGKILL(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("SIGKILLed helper unexpectedly succeeded")
	}
	var timeout *helperTimeoutError
	if errors.As(err, &timeout) {
		t.Fatalf("expected explicit SIGKILL, got helper timeout: %v", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("helper termination error = %T %v, want *exec.ExitError", err, err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("helper wait status = %#v, want signaled by SIGKILL", exitError.Sys())
	}
}

func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("helper unexpectedly succeeded, want exit %d", want)
	}
	var timeout *helperTimeoutError
	if errors.As(err, &timeout) {
		t.Fatalf("expected helper exit %d, got timeout: %v", want, err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != want {
		t.Fatalf("helper exit = %v, want code %d", err, want)
	}
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

func assertDatabaseSnapshot(t *testing.T, database *database, wantStatus string, wantEvents int) {
	t.Helper()
	connection := mustReaderConnection(t, database)
	defer connection.Close()
	assertSnapshot(t, connection, wantStatus, wantEvents)
}

func assertFootprint(t *testing.T, database *database, id string, wantState, wantEvents int) {
	t.Helper()
	ctx := context.Background()
	connection := mustReaderConnection(t, database)
	defer connection.Close()
	var state, events int
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_transitions WHERE id = ?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_events WHERE entity_id = ?", id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if state != wantState || events != wantEvents {
		t.Fatalf("footprint %q = (%d, %d), want (%d, %d)", id, state, events, wantState, wantEvents)
	}
}

func assertQueueAndTotalFootprint(t *testing.T, database *database, wantStatus string, wantState, wantEvents int) {
	t.Helper()
	ctx := context.Background()
	connection := mustReaderConnection(t, database)
	defer connection.Close()
	var status string
	var state, events int
	if err := connection.QueryRowContext(ctx, "SELECT status FROM fixture_queue WHERE id = 'task-1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_transitions").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || state != wantState || events != wantEvents {
		t.Fatalf("durable footprint = (%q, %d, %d), want (%q, %d, %d)", status, state, events, wantStatus, wantState, wantEvents)
	}
}

func footprintCount(t *testing.T, database *database, id string) int {
	t.Helper()
	connection := mustReaderConnection(t, database)
	defer connection.Close()
	var count int
	if err := connection.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM fixture_transitions WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mustReaderConnection(t *testing.T, database *database) *sql.Conn {
	t.Helper()
	connection, err := database.readerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func quickCheck(database *database) error {
	connection, err := database.readerConnection(context.Background())
	if err != nil {
		return err
	}
	defer connection.Close()
	var result string
	if err := connection.QueryRowContext(context.Background(), "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check = %q", result)
	}
	return nil
}

func fileDescriptorsFor(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat database for descriptor census: %w", err)
	}
	want, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("database stat has no syscall identity")
	}
	fdRoot := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdRoot = "/proc/self/fd"
	}
	entries, err := os.ReadDir(fdRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", fdRoot, err)
	}
	var matches []string
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var got syscall.Stat_t
		if err := syscall.Fstat(fd, &got); err == nil && got.Dev == want.Dev && got.Ino == want.Ino {
			matches = append(matches, entry.Name())
		}
	}
	sort.Strings(matches)
	return matches, nil
}

type faultKind int

const (
	faultNone faultKind = iota
	faultBeginAfterApply
	faultCommitBeforeApply
	faultCommitAfterApply
	faultRollbackBeforeApply
	faultRollbackAfterApply
)

type faultPlan struct {
	mu           sync.Mutex
	fault        faultKind
	nextID       int
	faultedID    int
	beginID      int
	closed       map[int]bool
	beginEntered chan struct{}
	beginOnce    sync.Once
}

func (p *faultPlan) arm(fault faultKind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fault = fault
}

func (p *faultPlan) opened() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	if p.closed == nil {
		p.closed = make(map[int]bool)
	}
	return p.nextID
}

func (p *faultPlan) closedConnection(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed[id] = true
}

func (p *faultPlan) intercept(id int, query string) faultKind {
	if query == "BEGIN IMMEDIATE" && p.beginEntered != nil {
		p.mu.Lock()
		p.beginID = id
		p.mu.Unlock()
		p.beginOnce.Do(func() { close(p.beginEntered) })
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	match := (query == "BEGIN IMMEDIATE" && p.fault == faultBeginAfterApply) ||
		(query == "COMMIT" && (p.fault == faultCommitBeforeApply || p.fault == faultCommitAfterApply)) ||
		(query == "ROLLBACK" && (p.fault == faultRollbackBeforeApply || p.fault == faultRollbackAfterApply))
	if !match {
		return faultNone
	}
	fault := p.fault
	p.fault = faultNone
	p.faultedID = id
	return fault
}

func (p *faultPlan) beginConnectionAndClosed() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.beginID, p.closed[p.beginID]
}

func (p *faultPlan) faultedAndClosed() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.faultedID, p.closed[p.faultedID]
}

type faultConnector struct {
	driver.Connector
	plan *faultPlan
}

func (c *faultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &faultConnection{Conn: connection, id: c.plan.opened(), plan: c.plan}, nil
}

type faultConnection struct {
	driver.Conn
	id   int
	plan *faultPlan
}

func (c *faultConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	fault := c.plan.intercept(c.id, query)
	if fault == faultCommitBeforeApply || fault == faultRollbackBeforeApply {
		return nil, errFaultResponse
	}
	result, err := execer.ExecContext(ctx, query, args)
	if err != nil {
		return result, err
	}
	if fault == faultBeginAfterApply || fault == faultCommitAfterApply || fault == faultRollbackAfterApply {
		return result, errFaultResponse
	}
	return result, nil
}

func (c *faultConnection) Close() error {
	c.plan.closedConnection(c.id)
	return c.Conn.Close()
}

func openFaultDatabase(t *testing.T, path string, plan *faultPlan) *database {
	t.Helper()
	openPool := func(limit int) *sql.DB {
		base, err := (&sqliteDriver.SQLite{}).OpenConnector(dataSource(path))
		if err != nil {
			t.Fatal(err)
		}
		pool := sql.OpenDB(&faultConnector{Connector: base, plan: plan})
		pool.SetMaxOpenConns(limit)
		pool.SetMaxIdleConns(limit)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(expectedBusyTimeout)*time.Millisecond)
		defer cancel()
		if err := pool.PingContext(ctx); err != nil {
			_ = pool.Close()
			t.Fatal(err)
		}
		return pool
	}
	database := &database{writer: openPool(1), readers: openPool(maxReaders)}
	if err := database.verifyInitialConnections(); err != nil {
		_ = database.close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.close() })
	return database
}

func faultConnectionID(t *testing.T, connection *sql.Conn) int {
	t.Helper()
	var id int
	if err := connection.Raw(func(raw any) error {
		faulted, ok := raw.(*faultConnection)
		if !ok {
			return fmt.Errorf("driver connection type = %T, want *faultConnection", raw)
		}
		id = faulted.id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

type ownedHelper struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	readyR  *os.File
	startW  *os.File
	resultR *os.File
	waited  bool
}

func startGatedHelper(t *testing.T, mode, path, runID string) *ownedHelper {
	t.Helper()
	helper, _, err := launchGatedHelper(t, os.Args[0], mode, path, runID)
	if err != nil {
		t.Fatal(err)
	}
	return helper
}

func launchGatedHelper(t *testing.T, executable, mode, path, runID string) (*ownedHelper, []*os.File, error) {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	startR, startW, err := os.Pipe()
	if err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		return nil, []*os.File{readyR, readyW}, err
	}
	resultR, resultW, err := os.Pipe()
	if err != nil {
		for _, file := range []*os.File{readyR, readyW, startR, startW} {
			_ = file.Close()
		}
		return nil, []*os.File{readyR, readyW, startR, startW}, err
	}
	files := []*os.File{readyR, readyW, startR, startW, resultR, resultW}
	helper, err := launchHelper(t, executable, mode, path, runID, []*os.File{readyW, startR, resultW})
	for _, childFile := range []*os.File{readyW, startR, resultW} {
		_ = childFile.Close()
	}
	if err != nil {
		for _, parentFile := range []*os.File{readyR, startW, resultR} {
			_ = parentFile.Close()
		}
		return helper, files, err
	}
	helper.readyR = readyR
	helper.startW = startW
	helper.resultR = resultR
	return helper, files, nil
}

func startHelper(t *testing.T, mode, path, runID string, extraFiles []*os.File) *ownedHelper {
	t.Helper()
	helper, err := launchHelper(t, os.Args[0], mode, path, runID, extraFiles)
	if err != nil {
		t.Fatal(err)
	}
	return helper
}

func launchHelper(t *testing.T, executable, mode, path, runID string, extraFiles []*os.File) (*ownedHelper, error) {
	t.Helper()
	helper := &ownedHelper{}
	helper.command = exec.Command(executable, "-test.run=^TestSQLiteContractHelper$")
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
		for _, file := range extraFiles {
			_ = file.Close()
		}
		return helper, err
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
		if helper.resultR != nil {
			_ = helper.resultR.Close()
		}
	})
	return helper, nil
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
	case <-time.After(helperWaitTimeout):
		_ = h.command.Process.Kill()
		err := <-done
		h.waited = true
		return &helperTimeoutError{cause: err}
	}
}

type helperTimeoutError struct {
	cause error
}

func (e *helperTimeoutError) Error() string {
	return fmt.Sprintf("helper timed out after %s: %v", helperWaitTimeout, e.cause)
}

func (e *helperTimeoutError) Unwrap() error {
	return e.cause
}

func (h *ownedHelper) result(t *testing.T) string {
	t.Helper()
	if h.resultR == nil {
		t.Fatal("helper has no result pipe")
	}
	if err := h.resultR.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	frame, err := io.ReadAll(h.resultR)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	if err := h.resultR.Close(); err != nil {
		t.Fatal(err)
	}
	h.resultR = nil
	switch string(frame) {
	case "result=won\n":
		return "won"
	case "result=lost\n":
		return "lost"
	default:
		t.Fatalf("invalid helper result frame %q", frame)
		return ""
	}
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
		ready, start, result, err := inheritedHelperPipes()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer ready.Close()
		defer start.Close()
		defer result.Close()
		if err := signalAndWait(ready, start); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		won, err := guardedAdmission(context.Background(), database, runID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if won {
			_, err = io.WriteString(result, "result=won\n")
		} else {
			_, err = io.WriteString(result, "result=lost\n")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := database.close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "hold-immediate":
		ready, start, result, err := inheritedHelperPipes()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer ready.Close()
		defer start.Close()
		defer result.Close()
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
		connection, err := database.writerConnection(context.Background())
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
	ready, start, result, err := inheritedHelperPipes()
	if err != nil {
		return err
	}
	defer ready.Close()
	defer start.Close()
	defer result.Close()
	return signalAndWait(ready, start)
}

func inheritedHelperPipes() (*os.File, *os.File, *os.File, error) {
	ready := os.NewFile(3, "ready")
	start := os.NewFile(4, "start")
	result := os.NewFile(5, "result")
	if ready == nil || start == nil || result == nil {
		return nil, nil, nil, errors.New("missing inherited helper pipes")
	}
	return ready, start, result, nil
}

func signalAndWait(ready, start *os.File) error {
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	var signal [1]byte
	_, err := io.ReadFull(start, signal[:])
	return err
}
