package kernel

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

const (
	busyMilliseconds = 5000
	maxReaders       = 4
	driverName       = "sqlite3"
)

type Store struct {
	writer      *sql.DB
	readers     *sql.DB
	writerGate  chan struct{}
	bindingMu   sync.RWMutex
	pathBinding *databaseFiles
	closed      atomic.Bool
	close       sync.Once
	closeErr    error
}

func openFixedPools(ctx context.Context, path string, recheck func() error) (*Store, error) {
	activationCtx, cancel := context.WithTimeout(ctx, 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	writer, err := openFixedPool(activationCtx, path, "writer", 1, recheck)
	if err != nil {
		return nil, err
	}
	readers, err := openFixedPool(activationCtx, path, "reader", maxReaders, recheck)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}
	writerGate := make(chan struct{}, 1)
	writerGate <- struct{}{}
	return &Store{writer: writer, readers: readers, writerGate: writerGate}, nil
}

func openPools(path string) (*Store, error) {
	writer, err := openPool(path, 1)
	if err != nil {
		return nil, err
	}
	readers, err := openPool(path, maxReaders)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}
	writerGate := make(chan struct{}, 1)
	writerGate <- struct{}{}
	store := &Store{writer: writer, readers: readers, writerGate: writerGate}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	writerConnection, err := store.writerConnection(ctx)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	if err := writerConnection.Close(); err != nil {
		return nil, errors.Join(fmt.Errorf("return initial writer connection: %w", err), store.Close())
	}
	readerConnection, err := store.readerConnection(ctx)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	if err := readerConnection.Close(); err != nil {
		return nil, errors.Join(fmt.Errorf("return initial reader connection: %w", err), store.Close())
	}
	return store, nil
}

func openPool(path string, limit int) (*sql.DB, error) {
	pool, err := sql.Open(driverName, configuredDataSource(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite pool: %w", err)
	}
	pool.SetMaxOpenConns(limit)
	pool.SetMaxIdleConns(limit)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize sqlite pool: %w", err), pool.Close())
	}
	return pool, nil
}

var errConnectionSetExhausted = fmt.Errorf("%w: retained sqlite connection set is exhausted", ErrCorruptState)

// sqliteConnectHook is package-local deterministic fault instrumentation. It
// runs after one physical connection is fully opened but before that
// connection can be returned to database/sql.
var sqliteConnectHook func(string, int) error

// sqliteConnectorConnect is package-local deterministic fault instrumentation
// around the physical driver open. Production always calls Connector.Connect.
var sqliteConnectorConnect = func(ctx context.Context, connector sqldriver.Connector) (sqldriver.Conn, error) {
	return connector.Connect(ctx)
}

// sqlitePoolCloseHook is package-local deterministic fault instrumentation at
// the point where database/sql owns physical connection shutdown.
var sqlitePoolCloseHook func(string) error

type finiteConnector struct {
	sqldriver.Connector
	mu            sync.Mutex
	kind          string
	remaining     int
	opened        int
	sealed        bool
	recheck       func() error
	activationCtx context.Context
}

func (connector *finiteConnector) Connect(context.Context) (sqldriver.Conn, error) {
	connector.mu.Lock()
	if connector.sealed || connector.remaining == 0 {
		connector.mu.Unlock()
		return nil, errConnectionSetExhausted
	}
	connector.remaining--
	connector.opened++
	opened := connector.opened
	connector.mu.Unlock()

	if err := connector.recheck(); err != nil {
		return nil, err
	}
	connection, err := sqliteConnectorConnect(connector.activationCtx, connector.Connector)
	if err != nil {
		return nil, err
	}
	driverConnection, ok := connection.(sqliteDriver.Conn)
	if !ok {
		return nil, errors.Join(fmt.Errorf("%w: sqlite driver connection has type %T", ErrCorruptState, connection), connection.Close())
	}
	persistent, err := driverConnection.Raw().FileControl("", sqlite3.FCNTL_PERSIST_WAL, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("retain sqlite WAL sidecars: %w", err), connection.Close())
	}
	if enabled, ok := persistent.(bool); !ok || !enabled {
		return nil, errors.Join(fmt.Errorf("%w: sqlite persistent WAL mode was not enabled", ErrCorruptState), connection.Close())
	}
	if sqliteConnectHook != nil {
		if err := sqliteConnectHook(connector.kind, opened); err != nil {
			return nil, errors.Join(err, connection.Close())
		}
	}
	if err := connector.recheck(); err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return connection, nil
}

func (connector *finiteConnector) seal() {
	connector.mu.Lock()
	connector.sealed = true
	connector.mu.Unlock()
}

func openFixedPool(ctx context.Context, path, kind string, limit int, recheck func() error) (_ *sql.DB, resultErr error) {
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite pool: %w", err)
	}
	connector := &finiteConnector{Connector: base, kind: kind, remaining: limit, recheck: recheck, activationCtx: ctx}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(limit)
	pool.SetMaxIdleConns(limit)
	connections := make([]*sql.Conn, 0, limit)
	defer func() {
		if resultErr == nil {
			return
		}
		connector.seal()
		for _, connection := range connections {
			resultErr = errors.Join(resultErr, connection.Close())
		}
		resultErr = errors.Join(resultErr, pool.Close())
	}()
	for len(connections) < limit {
		connection, err := pool.Conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("pin %s sqlite connection %d: %w", kind, len(connections)+1, err)
		}
		connections = append(connections, connection)
		if err := verifyConnection(ctx, connection); err != nil {
			return nil, fmt.Errorf("verify pinned %s sqlite connection %d: %w", kind, len(connections), err)
		}
		if sqliteConnectHook != nil {
			if err := sqliteConnectHook(kind+" verified", len(connections)); err != nil {
				return nil, err
			}
		}
		if err := recheck(); err != nil {
			return nil, err
		}
	}
	if err := recheck(); err != nil {
		return nil, err
	}
	connector.seal()
	for index, connection := range connections {
		if err := connection.Close(); err != nil {
			return nil, fmt.Errorf("return pinned %s sqlite connection %d: %w", kind, index+1, err)
		}
	}
	connections = nil
	stats := pool.Stats()
	if stats.OpenConnections != limit || stats.Idle != limit {
		return nil, fmt.Errorf("%w: pinned %s sqlite pool has open=%d idle=%d, want %d", ErrCorruptState, kind, stats.OpenConnections, stats.Idle, limit)
	}
	return pool, nil
}

func configuredDataSource(path string) string {
	query := url.Values{}
	query.Set("modeof", path)
	query["_pragma"] = []string{
		fmt.Sprintf("busy_timeout(%d)", busyMilliseconds),
		"foreign_keys(ON)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func (store *Store) writerConnection(ctx context.Context) (*sql.Conn, error) {
	return store.verifiedConnection(ctx, store.writer, "writer")
}

func (store *Store) readerConnection(ctx context.Context) (*sql.Conn, error) {
	return store.verifiedConnection(ctx, store.readers, "reader")
}

func (store *Store) verifiedConnection(ctx context.Context, pool *sql.DB, kind string) (*sql.Conn, error) {
	store.bindingMu.RLock()
	defer store.bindingMu.RUnlock()
	if store.closed.Load() {
		return nil, ErrStoreClosed
	}
	if store.pathBinding != nil {
		if err := store.pathBinding.recheckPaths(); err != nil {
			return nil, fmt.Errorf("recheck retained sqlite binding: %w", err)
		}
	}
	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout %s connection: %w", kind, err)
	}
	if err := verifyConnection(ctx, connection); err != nil {
		discardConnection(connection)
		return nil, fmt.Errorf("verify %s connection: %w", kind, err)
	}
	if store.pathBinding != nil {
		if err := store.pathBinding.recheckPaths(); err != nil {
			discardConnection(connection)
			return nil, fmt.Errorf("recheck retained sqlite binding: %w", err)
		}
	}
	return connection, nil
}

func verifyConnection(ctx context.Context, connection *sql.Conn) error {
	var foreignKeys, synchronous, busy int
	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		return err
	}
	if foreignKeys != 1 || strings.ToLower(journalMode) != "wal" || synchronous != 2 || busy != busyMilliseconds {
		return fmt.Errorf(
			"unsafe sqlite connection configuration: foreign_keys=%d journal_mode=%q synchronous=%d busy_timeout=%d",
			foreignKeys,
			journalMode,
			synchronous,
			busy,
		)
	}
	return nil
}

func initializeFresh(ctx context.Context, connection *sql.Conn, config FactoryConfig, at UnixMillis, daemonID DaemonID) error {
	empty, err := schemaIsEmpty(ctx, connection)
	if err == nil && !empty {
		err = fmt.Errorf("%w: fresh database is not empty", ErrForeignDatabase)
	}
	if err == nil {
		_, err = connection.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID))
	}
	if err == nil {
		_, err = connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", userVersion))
	}
	for _, statement := range schemaStatements {
		if err != nil {
			break
		}
		_, err = connection.ExecContext(ctx, statement)
	}
	if err == nil {
		inserted, insertErr := connection.ExecContext(ctx,
			`INSERT INTO factory(singleton, daemon_id, dispatch_enabled, capacity, revision, next_invalidation_sequence, invalidation_floor, updated_at_ms) VALUES(1, ?, ?, ?, 1, 1, 1, ?)`,
			daemonID.Bytes(), boolInt(config.DispatchEnabled), int64(config.Capacity), at.Int64())
		err = requireOneRow(inserted, insertErr)
	}
	return err
}

func (store *Store) validateOpen(ctx context.Context) error {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return err
	}
	err = validateDatabaseSnapshot(ctx, tx.connection)
	return errors.Join(err, tx.Close())
}

func validateDatabaseSnapshot(ctx context.Context, connection *sql.Conn) error {
	if err := validateExactSchema(ctx, connection); err != nil {
		return err
	}
	if err := validateDurableControls(ctx, connection); err != nil {
		return err
	}
	return validateIntegrity(ctx, connection)
}

func (store *Store) Close() error {
	store.close.Do(func() {
		store.closed.Store(true)
		// Wait for the one already-admitted writer, if any, before closing
		// either pool. New writers observe closed after acquiring the gate and
		// leave without touching SQLite. bindingMu excludes new checkouts, and
		// the in-use fence joins every checkout that escaped the read lock.
		if err := store.acquireWriter(context.Background()); err != nil {
			store.closeErr = err
			return
		}
		defer store.releaseWriter()
		store.bindingMu.Lock()
		defer store.bindingMu.Unlock()
		store.waitForCheckedOutConnections()
		poolErr := errors.Join(store.closePool("reader", store.readers), store.closePool("writer", store.writer))
		if poolErr != nil {
			store.closeErr = fmt.Errorf("%w: close retained sqlite pools: %w", ErrCorruptState, poolErr)
			return
		}
		if store.pathBinding != nil {
			store.closeErr = store.pathBinding.Close()
			if store.closeErr == nil {
				store.pathBinding = nil
			}
		}
	})
	return store.closeErr
}

func (store *Store) closePool(kind string, pool *sql.DB) error {
	if sqlitePoolCloseHook != nil {
		if err := sqlitePoolCloseHook(kind); err != nil {
			return err
		}
	}
	return pool.Close()
}

func (store *Store) waitForCheckedOutConnections() {
	for store.writer.Stats().InUse != 0 || store.readers.Stats().InUse != 0 {
		time.Sleep(time.Millisecond)
	}
}

type writeTx struct {
	store          *Store
	connection     *sql.Conn
	active         bool
	discarded      bool
	closed         bool
	writerAdmitted bool
}

func (store *Store) beginValidatedWrite(ctx context.Context) (*writeTx, error) {
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		err = tx.Rollback(err)
		tx.Close()
		return nil, err
	}
	return tx, nil
}

// beginUncheckedWrite exists only because a fresh database has no durable
// graph to validate until initialize creates it. Public mutations must enter
// through beginValidatedWrite.
func (store *Store) beginUncheckedWrite(ctx context.Context) (*writeTx, error) {
	if err := store.acquireWriter(ctx); err != nil {
		return nil, err
	}
	if store.closed.Load() {
		store.releaseWriter()
		return nil, ErrStoreClosed
	}
	connection, err := store.writerConnection(ctx)
	if err != nil {
		store.releaseWriter()
		return nil, err
	}
	tx := &writeTx{store: store, connection: connection, writerAdmitted: true}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if cancellation := ctx.Err(); cancellation != nil {
			tx.discard()
			tx.Close()
			return nil, &OutcomeUnknownError{cause: errors.Join(cancellation, fmt.Errorf("begin immediate: %w", err))}
		}
		if errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.LOCKED) {
			tx.Close()
			return nil, fmt.Errorf("%w: %v", ErrBusy, err)
		}
		tx.discard()
		tx.Close()
		return nil, &OutcomeUnknownError{cause: fmt.Errorf("begin immediate: %w", err)}
	}
	tx.active = true
	return tx, nil
}

func (tx *writeTx) Commit(ctx context.Context) error {
	if !tx.active {
		return errors.New("commit inactive sqlite write")
	}
	if err := ctx.Err(); err != nil {
		return tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, "COMMIT"); err != nil {
		tx.active = false
		tx.discard()
		return &OutcomeUnknownError{cause: fmt.Errorf("commit: %w", err)}
	}
	tx.active = false
	return nil
}

func (tx *writeTx) Rollback(cause error) error {
	if !tx.active {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	if _, err := tx.connection.ExecContext(ctx, "ROLLBACK"); err != nil {
		tx.active = false
		tx.discard()
		return &OutcomeUnknownError{cause: errors.Join(cause, fmt.Errorf("rollback: %w", err))}
	}
	tx.active = false
	return cause
}

func (tx *writeTx) discard() {
	if tx.discarded || tx.connection == nil {
		return
	}
	discardConnection(tx.connection)
	tx.discarded = true
}

func (tx *writeTx) Close() {
	if tx.closed {
		return
	}
	if tx.active {
		if err := tx.Rollback(errors.New("abandoned sqlite write")); err != nil {
			var unknown *OutcomeUnknownError
			if errors.As(err, &unknown) {
				tx.discard()
			}
		}
	}
	if !tx.discarded && tx.connection != nil {
		_ = tx.connection.Close()
	}
	tx.closed = true
	if tx.writerAdmitted {
		tx.writerAdmitted = false
		tx.store.releaseWriter()
	}
}

func (store *Store) acquireWriter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-store.writerGate:
	}
	if err := ctx.Err(); err != nil {
		store.releaseWriter()
		return err
	}
	return nil
}

func (store *Store) releaseWriter() {
	store.writerGate <- struct{}{}
}

func discardConnection(connection *sql.Conn) {
	_ = connection.Raw(func(any) error { return sqldriver.ErrBadConn })
	_ = connection.Close()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
