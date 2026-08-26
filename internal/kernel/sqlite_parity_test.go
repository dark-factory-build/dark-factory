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
)

type storeFaultPlan struct {
	mu        sync.Mutex
	fault     storeFaultKind
	nextID    int
	faultedID int
	closed    map[int]bool
}

func (plan *storeFaultPlan) arm(fault storeFaultKind) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.fault = fault
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
	defer plan.mu.Unlock()
	wanted := query == "BEGIN IMMEDIATE" && (plan.fault == storeFaultBeginBefore || plan.fault == storeFaultBeginAfter) ||
		query == "COMMIT" && (plan.fault == storeFaultCommitBefore || plan.fault == storeFaultCommitAfter) ||
		query == "ROLLBACK" && (plan.fault == storeFaultRollbackBefore || plan.fault == storeFaultRollbackAfter)
	if !wanted {
		return storeFaultNone
	}
	fault := plan.fault
	plan.fault = storeFaultNone
	plan.faultedID = id
	return fault
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

type storeFaultConnector struct {
	driver.Connector
	plan *storeFaultPlan
}

func (connector *storeFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &storeFaultConnection{Conn: connection, id: connector.plan.opened(), plan: connector.plan}, nil
}

type storeFaultConnection struct {
	driver.Conn
	id   int
	plan *storeFaultPlan
}

func (connection *storeFaultConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := connection.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	fault := connection.plan.intercept(connection.id, query)
	if fault == storeFaultBeginBefore || fault == storeFaultCommitBefore || fault == storeFaultRollbackBefore {
		return nil, errInjectedSQLiteResponse
	}
	result, err := execer.ExecContext(ctx, query, args)
	if err != nil {
		return result, err
	}
	if fault == storeFaultBeginAfter || fault == storeFaultCommitAfter || fault == storeFaultRollbackAfter {
		return result, errInjectedSQLiteResponse
	}
	return result, nil
}

func (connection *storeFaultConnection) Close() error {
	connection.plan.closedConnection(connection.id)
	return connection.Conn.Close()
}

func installFaultWriter(t *testing.T, store *Store, path string) *storeFaultPlan {
	t.Helper()
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	plan := &storeFaultPlan{}
	pool := sql.OpenDB(&storeFaultConnector{Connector: base, plan: plan})
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	if err := pool.PingContext(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	old := store.writer
	store.writer = pool
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
	t.Helper()
	faultedID, closed := plan.faultedAndClosed()
	if faultedID == 0 || !closed {
		t.Fatalf("faulted connection = %d closed=%v, want discarded", faultedID, closed)
	}
	if replacementID := faultWriterConnectionID(t, store); replacementID == faultedID {
		t.Fatalf("faulted connection %d was reused", faultedID)
	}
}

func TestConcreteStoreAmbiguousBeginAndCommitAreNotReplayed(t *testing.T) {
	tests := []struct {
		name             string
		fault            storeFaultKind
		wantRevision     int64
		wantDispatch     bool
		wantInvalidation int64
	}{
		{name: "begin before apply", fault: storeFaultBeginBefore, wantRevision: 1},
		{name: "begin after apply", fault: storeFaultBeginAfter, wantRevision: 1},
		{name: "commit before apply", fault: storeFaultCommitBefore, wantRevision: 1},
		{name: "commit after apply", fault: storeFaultCommitAfter, wantRevision: 2, wantDispatch: true, wantInvalidation: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/kernel.db"
			store, err := Create(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
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
			assertFaultWriterEvicted(t, store, plan)
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
		name  string
		fault storeFaultKind
	}{{"before apply", storeFaultRollbackBefore}, {"after apply", storeFaultRollbackAfter}} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/kernel.db"
			store, err := Create(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
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
			assertFaultWriterEvicted(t, store, plan)
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
