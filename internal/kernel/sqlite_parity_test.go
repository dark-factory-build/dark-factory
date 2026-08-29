package kernel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

var errInjectedSQLiteResponse = errors.New("injected sqlite response failure")

type storeFaultKind uint8

const (
	storeFaultNone storeFaultKind = iota
	storeFaultBeginBefore
	storeFaultBeginAfter
	storeFaultCommitBefore
	storeFaultCommitAfter
	storeFaultRollbackBefore
	storeFaultRollbackAfter
	storeFaultReadBeginBefore
	storeFaultReadBeginAfter
)

type storeFaultPlan struct {
	mu           sync.Mutex
	fault        storeFaultKind
	nextID       int
	faultedID    int
	closed       map[int]bool
	beginEntered chan struct{}
	verifyArmed  bool
	verifyCancel context.CancelFunc
	verifyErr    error
}

func (plan *storeFaultPlan) arm(fault storeFaultKind) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.fault = fault
}

func (plan *storeFaultPlan) armVerifyCancellation(cancel context.CancelFunc) {
	plan.armVerifyFailure(cancel, sqlite3.INTERRUPT)
}

func (plan *storeFaultPlan) armVerifyFailure(cancel context.CancelFunc, err error) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.verifyArmed = true
	plan.verifyCancel = cancel
	plan.verifyErr = err
}

func (plan *storeFaultPlan) watchBegin() <-chan struct{} {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.beginEntered = make(chan struct{})
	return plan.beginEntered
}

func (plan *storeFaultPlan) opened() int {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.nextID++
	if plan.closed == nil {
		plan.closed = make(map[int]bool)
	}
	return plan.nextID
}

func (plan *storeFaultPlan) intercept(id int, query string) storeFaultKind {
	plan.mu.Lock()
	wanted := query == "BEGIN IMMEDIATE" && (plan.fault == storeFaultBeginBefore || plan.fault == storeFaultBeginAfter) ||
		query == "BEGIN" && (plan.fault == storeFaultReadBeginBefore || plan.fault == storeFaultReadBeginAfter) ||
		query == "COMMIT" && (plan.fault == storeFaultCommitBefore || plan.fault == storeFaultCommitAfter) ||
		query == "ROLLBACK" && (plan.fault == storeFaultRollbackBefore || plan.fault == storeFaultRollbackAfter)
	if !wanted {
		plan.mu.Unlock()
		return storeFaultNone
	}
	fault := plan.fault
	plan.fault = storeFaultNone
	plan.faultedID = id
	plan.mu.Unlock()
	return fault
}

func (plan *storeFaultPlan) interceptVerify(id int, query string) (bool, error) {
	if query != "PRAGMA foreign_keys" {
		return false, nil
	}
	plan.mu.Lock()
	armed := plan.verifyArmed
	cancel := plan.verifyCancel
	err := plan.verifyErr
	plan.verifyArmed = false
	plan.verifyCancel = nil
	plan.verifyErr = nil
	if armed {
		plan.faultedID = id
	}
	plan.mu.Unlock()
	if !armed {
		return false, nil
	}
	if cancel != nil {
		cancel()
	}
	return true, err
}

func (plan *storeFaultPlan) signalBeginEntered() {
	plan.mu.Lock()
	entered := plan.beginEntered
	plan.beginEntered = nil
	plan.mu.Unlock()
	if entered != nil {
		close(entered)
	}
}

func (plan *storeFaultPlan) closedConnection(id int) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.closed[id] = true
}

func (plan *storeFaultPlan) faultedAndClosed() (int, bool) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return plan.faultedID, plan.closed[plan.faultedID]
}

func (plan *storeFaultPlan) wasClosed(id int) bool {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return plan.closed[id]
}

type storeFaultConnector struct {
	driver.Connector
	plan *storeFaultPlan
}

func (connector *storeFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	sqliteConnection, ok := connection.(sqliteDriver.Conn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("sqlite connection type = %T", connection)
	}
	return &storeFaultConnection{Conn: sqliteConnection, id: connector.plan.opened(), plan: connector.plan}, nil
}

type storeFaultConnection struct {
	sqliteDriver.Conn
	id   int
	plan *storeFaultPlan
}

func (connection *storeFaultConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := connection.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	fault := connection.plan.intercept(connection.id, query)
	if fault == storeFaultBeginBefore || fault == storeFaultReadBeginBefore || fault == storeFaultCommitBefore || fault == storeFaultRollbackBefore {
		return nil, errInjectedSQLiteResponse
	}
	if query == "BEGIN IMMEDIATE" {
		connection.plan.signalBeginEntered()
	}
	result, err := execer.ExecContext(ctx, query, args)
	if err != nil {
		return result, err
	}
	if fault == storeFaultBeginAfter || fault == storeFaultReadBeginAfter || fault == storeFaultCommitAfter || fault == storeFaultRollbackAfter {
		return result, errInjectedSQLiteResponse
	}
	return result, nil
}

func (connection *storeFaultConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		statement driver.Stmt
		err       error
	)
	if preparer, ok := connection.Conn.(driver.ConnPrepareContext); ok {
		statement, err = preparer.PrepareContext(ctx, query)
	} else {
		statement, err = connection.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &storeFaultStatement{Stmt: statement, connection: connection, query: query}, nil
}

type storeFaultStatement struct {
	driver.Stmt
	connection *storeFaultConnection
	query      string
}

func (statement *storeFaultStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if intercepted, err := statement.connection.plan.interceptVerify(statement.connection.id, statement.query); intercepted {
		return nil, err
	}
	queryer, ok := statement.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, args)
}

func (connection *storeFaultConnection) Close() error {
	connection.plan.closedConnection(connection.id)
	return connection.Conn.Close()
}

func installFaultWriter(t *testing.T, store *Store, path string) *storeFaultPlan {
	return installFaultPool(t, path, 1, &store.writer)
}

func installFaultReader(t *testing.T, store *Store, path string) *storeFaultPlan {
	return installFaultPool(t, path, 1, &store.readers)
}

func installFaultPool(t *testing.T, path string, limit int, owner **sql.DB) *storeFaultPlan {
	t.Helper()
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	plan := &storeFaultPlan{}
	pool := sql.OpenDB(&storeFaultConnector{Connector: base, plan: plan})
	pool.SetMaxOpenConns(limit)
	pool.SetMaxIdleConns(limit)
	if err := pool.PingContext(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	old := *owner
	*owner = pool
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func installSealedFaultWriter(t *testing.T, store *Store, path string) *storeFaultPlan {
	t.Helper()
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	plan := &storeFaultPlan{}
	activationCtx := context.Background()
	connector := &finiteConnector{
		Connector:     &storeFaultConnector{Connector: base, plan: plan},
		kind:          "writer",
		remaining:     1,
		recheck:       func() error { return nil },
		activationCtx: activationCtx,
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	connection, err := pool.Conn(activationCtx)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := verifyConnection(activationCtx, connection); err != nil {
		connection.Close()
		pool.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	connector.seal()
	old := store.writer
	store.writer = pool
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestVerifiedConnectionCancellationRetainsSealedWriter(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatalf("open operational store: %v", err)
	}
	defer store.Close()
	plan := installSealedFaultWriter(t, store, path)
	if open := store.writer.Stats().OpenConnections; open != 1 {
		t.Fatalf("sealed writer set = %d physical connections, want 1", open)
	}

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	plan.armVerifyCancellation(cancelCaller)
	if _, err := store.writerConnection(callerCtx); !errors.Is(err, sqlite3.INTERRUPT) {
		t.Fatalf("cancelled verification error = %v, want sqlite3.INTERRUPT", err)
	}
	if callerCtx.Err() == nil {
		t.Fatal("verification fault did not cancel its context")
	}
	faultedID, closed := plan.faultedAndClosed()
	if faultedID == 0 || closed {
		t.Fatalf("cancelled verification connection = %d closed=%v, want retained", faultedID, closed)
	}
	if currentID := faultWriterConnectionID(t, store); currentID != faultedID {
		t.Fatalf("cancelled verification replaced connection %d with %d", faultedID, currentID)
	}
}

func TestVerifiedConnectionNonCancellationFailuresDiscardSealedWriter(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "interrupt with live context", err: sqlite3.INTERRUPT},
		{name: "driver failure", err: errInjectedSQLiteResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, _ := walSnapshotFixture(t, "")
			store, err := openOperationalTestStore(path)
			if err != nil {
				t.Fatalf("open operational store: %v", err)
			}
			defer store.Close()
			plan := installSealedFaultWriter(t, store, path)
			plan.armVerifyFailure(nil, test.err)
			if _, err := store.writerConnection(context.Background()); !errors.Is(err, test.err) {
				t.Fatalf("verification error = %v, want %v", err, test.err)
			}
			faultedID, closed := plan.faultedAndClosed()
			if faultedID == 0 || !closed {
				t.Fatalf("failed verification connection = %d closed=%v, want discarded", faultedID, closed)
			}
			if _, err := store.writerConnection(context.Background()); !errors.Is(err, errConnectionSetExhausted) {
				t.Fatalf("checkout after discarded verification connection = %v, want exhaustion", err)
			}
		})
	}
}

func TestVerifiedConnectionConfigurationMismatchDiscardsSealedWriter(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatalf("open operational store: %v", err)
	}
	defer store.Close()
	plan := installSealedFaultWriter(t, store, path)
	connection, err := store.writer.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout raw writer: %v", err)
	}
	faultedID := faultConnectionID(t, connection)
	if _, err := connection.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
		connection.Close()
		t.Fatalf("poison writer configuration: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("return poisoned writer: %v", err)
	}
	if _, err := store.writerConnection(context.Background()); err == nil {
		t.Fatal("poisoned writer verification succeeded")
	}
	if !plan.wasClosed(faultedID) {
		t.Fatalf("poisoned writer connection %d was retained", faultedID)
	}
	if _, err := store.writerConnection(context.Background()); !errors.Is(err, errConnectionSetExhausted) {
		t.Fatalf("checkout after poisoned writer discard = %v, want exhaustion", err)
	}
}

func faultWriterConnectionID(t *testing.T, store *Store) int {
	t.Helper()
	connection, err := store.writerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	return faultConnectionID(t, connection)
}

func faultReaderConnectionID(t *testing.T, store *Store) int {
	t.Helper()
	connection, err := store.readerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	return faultConnectionID(t, connection)
}

func faultConnectionID(t *testing.T, connection *sql.Conn) int {
	t.Helper()
	var id int
	if err := connection.Raw(func(raw any) error {
		faulted, ok := raw.(*storeFaultConnection)
		if !ok {
			return fmt.Errorf("driver connection type = %T", raw)
		}
		id = faulted.id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func requireStoreOutcomeUnknown(t *testing.T, err error) {
	t.Helper()
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, errInjectedSQLiteResponse) {
		t.Fatalf("error = %v, want OutcomeUnknownError preserving injected response failure", err)
	}
}

func assertFaultWriterEvicted(t *testing.T, store *Store, plan *storeFaultPlan) {
	assertFaultConnectionEvicted(t, plan, func() int { return faultWriterConnectionID(t, store) })
}

func assertFaultWriterRetained(t *testing.T, store *Store, plan *storeFaultPlan) {
	assertFaultConnectionRetained(t, plan, func() int { return faultWriterConnectionID(t, store) })
}

func assertFaultReaderEvicted(t *testing.T, store *Store, plan *storeFaultPlan) {
	assertFaultConnectionEvicted(t, plan, func() int { return faultReaderConnectionID(t, store) })
}

func assertFaultReaderRetained(t *testing.T, store *Store, plan *storeFaultPlan) {
	assertFaultConnectionRetained(t, plan, func() int { return faultReaderConnectionID(t, store) })
}

func assertFaultWriterDisposition(t *testing.T, store *Store, plan *storeFaultPlan, wantRetained bool) {
	t.Helper()
	if wantRetained {
		assertFaultWriterRetained(t, store, plan)
	} else {
		assertFaultWriterEvicted(t, store, plan)
	}
}

func assertFaultReaderDisposition(t *testing.T, store *Store, plan *storeFaultPlan, wantRetained bool) {
	t.Helper()
	if wantRetained {
		assertFaultReaderRetained(t, store, plan)
	} else {
		assertFaultReaderEvicted(t, store, plan)
	}
}

func assertFaultConnectionEvicted(t *testing.T, plan *storeFaultPlan, currentID func() int) {
	t.Helper()
	faultedID, closed := plan.faultedAndClosed()
	if faultedID == 0 || !closed {
		t.Fatalf("faulted connection = %d closed=%v, want discarded", faultedID, closed)
	}
	if replacementID := currentID(); replacementID == faultedID {
		t.Fatalf("faulted connection %d was reused", faultedID)
	}
}

func assertFaultConnectionRetained(t *testing.T, plan *storeFaultPlan, currentID func() int) {
	t.Helper()
	faultedID, closed := plan.faultedAndClosed()
	if faultedID == 0 || closed {
		t.Fatalf("faulted connection = %d closed=%v, want retained", faultedID, closed)
	}
	if retainedID := currentID(); retainedID != faultedID {
		t.Fatalf("faulted connection %d was replaced by %d", faultedID, retainedID)
	}
}

func TestConcreteStoreAmbiguousBeginAndCommitAreNotReplayed(t *testing.T) {
	tests := []struct {
		name             string
		fault            storeFaultKind
		wantRetained     bool
		wantRevision     int64
		wantDispatch     bool
		wantInvalidation int64
	}{
		{name: "begin before apply", fault: storeFaultBeginBefore, wantRetained: true, wantRevision: 1},
		{name: "begin after apply", fault: storeFaultBeginAfter, wantRevision: 1},
		{name: "commit before apply", fault: storeFaultCommitBefore, wantRevision: 1},
		{name: "commit after apply", fault: storeFaultCommitAfter, wantRetained: true, wantRevision: 2, wantDispatch: true, wantInvalidation: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/kernel.db"
			store, err := createTestStore(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			plan := installFaultWriter(t, store, path)
			plan.arm(test.fault)
			if _, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2)); err == nil {
				t.Fatal("faulted SetDispatch succeeded")
			} else {
				requireStoreOutcomeUnknown(t, err)
			}
			assertFaultWriterDisposition(t, store, plan, test.wantRetained)
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if state.Revision.Int64() != test.wantRevision || state.DispatchEnabled != test.wantDispatch || state.Head.Int64() != test.wantInvalidation {
				t.Fatalf("durable state = %+v, want revision=%d dispatch=%v head=%d", state, test.wantRevision, test.wantDispatch, test.wantInvalidation)
			}
			if _, err := store.SetCapacity(context.Background(), state.Revision, 2, mustTime(t, 3)); err != nil {
				t.Fatalf("verified replacement write: %v", err)
			}
		})
	}
}

func TestConcreteStoreAmbiguousRollbackResolvesByAutocommitWithoutStateFootprint(t *testing.T) {
	for _, test := range []struct {
		name         string
		fault        storeFaultKind
		wantRetained bool
	}{{"before apply", storeFaultRollbackBefore, false}, {"after apply", storeFaultRollbackAfter, true}} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/kernel.db"
			store, err := createTestStore(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err := store.writer.Exec(`CREATE TRIGGER reject_invalidation BEFORE INSERT ON invalidations BEGIN SELECT RAISE(ABORT, 'forced body failure'); END`); err != nil {
				t.Fatal(err)
			}
			plan := installFaultWriter(t, store, path)
			plan.arm(test.fault)
			_, err = store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 2))
			requireStoreOutcomeUnknown(t, err)
			assertFaultWriterDisposition(t, store, plan, test.wantRetained)
			state, err := store.Factory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if state.Revision.Int64() != 1 || state.DispatchEnabled || state.Head.Int64() != 0 {
				t.Fatalf("rollback left state footprint: %+v", state)
			}
			if _, err := store.writer.Exec(`DROP TRIGGER reject_invalidation`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SetDispatch(context.Background(), mustRevision(t, 1), true, mustTime(t, 3)); err != nil {
				t.Fatalf("verified replacement write: %v", err)
			}
		})
	}
}

func TestConcreteStoreAmbiguousReadLifecycleResolvesByAutocommit(t *testing.T) {
	for _, test := range []struct {
		name         string
		fault        storeFaultKind
		rollback     bool
		wantRetained bool
	}{
		{name: "begin before apply", fault: storeFaultReadBeginBefore, wantRetained: true},
		{name: "begin after apply", fault: storeFaultReadBeginAfter},
		{name: "rollback before apply", fault: storeFaultRollbackBefore, rollback: true},
		{name: "rollback after apply", fault: storeFaultRollbackAfter, rollback: true, wantRetained: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/kernel.db"
			store, err := createTestStore(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			plan := installFaultReader(t, store, path)

			var lifecycleErr error
			if test.rollback {
				connection, err := store.readerConnection(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				readTx, err := beginPinnedRead(context.Background(), connection)
				if err != nil {
					t.Fatal(err)
				}
				plan.arm(test.fault)
				lifecycleErr = readTx.Close()
			} else {
				plan.arm(test.fault)
				connection, err := store.readerConnection(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				readTx, err := beginPinnedRead(context.Background(), connection)
				if readTx != nil {
					if closeErr := readTx.Close(); closeErr != nil {
						t.Fatalf("unexpected successful read BEGIN cleanup = %v", closeErr)
					}
					t.Fatal("faulted read BEGIN succeeded")
				}
				lifecycleErr = err
			}
			if !errors.Is(lifecycleErr, errInjectedSQLiteResponse) {
				t.Fatalf("lifecycle error = %v, want injected response", lifecycleErr)
			}
			assertFaultReaderDisposition(t, store, plan, test.wantRetained)
		})
	}
}
