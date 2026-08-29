package kernel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"

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
}

func (plan *storeFaultPlan) arm(fault storeFaultKind) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.fault = fault
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
			if test.wantRetained {
				assertFaultWriterRetained(t, store, plan)
			} else {
				assertFaultWriterEvicted(t, store, plan)
			}
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

func TestConcreteStoreAmbiguousRollbackDiscardsWriterAndState(t *testing.T) {
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
			if test.wantRetained {
				assertFaultWriterRetained(t, store, plan)
			} else {
				assertFaultWriterEvicted(t, store, plan)
			}
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
			if test.wantRetained {
				assertFaultReaderRetained(t, store, plan)
			} else {
				assertFaultReaderEvicted(t, store, plan)
			}
		})
	}
}
