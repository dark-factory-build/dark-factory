package kernel

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"runtime"
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
	// held is the corruption the last whole-store validation found. Validated
	// writes and snapshot reads refuse while it is set (see Validate).
	held atomic.Pointer[error]

	repositoryBase string // Boot-owned initial binding default; protected by writerGate.
	archivePath    string // The history archive beside the database; see ArchiveHistory.

	// Writer-hold diagnostics (see LogWriterHolds). The holder fields are
	// protected by writerGate; the waiter count is read by the holder.
	writerLog     atomic.Pointer[func(string, ...any)]
	writerWaiters atomic.Int64
	writerHolder  [8]uintptr
	writerSince   time.Time
	longestWait   time.Duration
	longestWaiter [8]uintptr
	waitReported  time.Time
}

// slowWriter is the writer hold, or wait, that LogWriterHolds names.
const slowWriter = time.Second

// LogWriterHolds logs each writer hold over slowWriter with its holder and the
// writers queued behind it, and once a minute the longest wait over slowWriter.
func (store *Store) LogWriterHolds(log func(format string, args ...any)) {
	store.writerLog.Store(&log)
}

func (store *Store) logWriter(format string, args ...any) {
	if log := store.writerLog.Load(); log != nil {
		(*log)(format, args...)
	}
}

// writerCaller names the function that took the writer, skipping the write
// plumbing every mutation goes through.
func writerCaller(stack [8]uintptr) string {
	frames := runtime.CallersFrames(stack[:])
	for {
		frame, more := frames.Next()
		name := frame.Function[strings.LastIndex(frame.Function, "/")+1:]
		if more && (strings.HasSuffix(name, ".beginUncheckedWrite") || strings.HasSuffix(name, ".beginValidatedWrite")) {
			continue
		}
		return name
	}
}

func newStore() *Store {
	writerGate := make(chan struct{}, 1)
	writerGate <- struct{}{}
	return &Store{writerGate: writerGate}
}

func openFixedPools(ctx context.Context, store *Store, path string, recheck func() error) error {
	activationCtx, cancel := context.WithTimeout(ctx, 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	err := openFixedPool(activationCtx, path, "writer", 1, recheck, &store.writer)
	if err != nil {
		if store.writer != nil {
			store.retainCloseUncertainty(err)
		}
		return errors.Join(err, store.Close())
	}
	err = openFixedPool(activationCtx, path, "reader", maxReaders, recheck, &store.readers)
	if err != nil {
		if store.readers != nil {
			store.retainCloseUncertainty(err)
		}
		return errors.Join(err, store.Close())
	}
	return nil
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
	// Keep the raw object even if database/sql observes a Close error and
	// removes it from the pool. Store then retains this connector with the pool.
	connections []sqldriver.Conn
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
	connector.mu.Lock()
	connector.connections = append(connector.connections, connection)
	connector.mu.Unlock()
	driverConnection, ok := connection.(sqliteDriver.Conn)
	if !ok {
		return nil, closePhysicalConnection(connection, fmt.Errorf("%w: sqlite driver connection has type %T", ErrCorruptState, connection))
	}
	persistent, err := driverConnection.Raw().FileControl("", sqlite3.FCNTL_PERSIST_WAL, true)
	if err != nil {
		return nil, closePhysicalConnection(connection, fmt.Errorf("retain sqlite WAL sidecars: %w", err))
	}
	if enabled, ok := persistent.(bool); !ok || !enabled {
		return nil, closePhysicalConnection(connection, fmt.Errorf("%w: sqlite persistent WAL mode was not enabled", ErrCorruptState))
	}
	if sqliteConnectHook != nil {
		if err := sqliteConnectHook(connector.kind, opened); err != nil {
			return nil, closePhysicalConnection(connection, err)
		}
	}
	if err := connector.recheck(); err != nil {
		return nil, closePhysicalConnection(connection, err)
	}
	return connection, nil
}

var errPhysicalConnectionCloseUncertain = errors.New("physical sqlite connection close uncertain")

func closePhysicalConnection(connection sqldriver.Conn, cause error) error {
	if err := connection.Close(); err != nil {
		return errors.Join(cause, errPhysicalConnectionCloseUncertain, fmt.Errorf("close physical sqlite connection: %w", err))
	}
	return cause
}

func (connector *finiteConnector) seal() {
	connector.mu.Lock()
	connector.sealed = true
	connector.mu.Unlock()
}

func openFixedPool(ctx context.Context, path, kind string, limit int, recheck func() error, owner **sql.DB) (resultErr error) {
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		return fmt.Errorf("open sqlite pool: %w", err)
	}
	connector := &finiteConnector{Connector: base, kind: kind, remaining: limit, recheck: recheck, activationCtx: ctx}
	pool := sql.OpenDB(connector)
	*owner = pool
	pool.SetMaxOpenConns(limit)
	pool.SetMaxIdleConns(limit)
	connections := make([]*sql.Conn, 0, limit)
	checkoutReturnUncertain := false
	defer func() {
		if resultErr == nil {
			return
		}
		connector.seal()
		var closeErr error
		for _, connection := range connections {
			if connection == nil {
				continue
			}
			if err := connection.Close(); err != nil {
				checkoutReturnUncertain = true
				closeErr = errors.Join(closeErr, err)
			}
		}
		closeErr = errors.Join(closeErr, closeSQLitePool(kind, pool))
		resultErr = errors.Join(resultErr, closeErr)
		if closeErr == nil && !checkoutReturnUncertain && !errors.Is(resultErr, errPhysicalConnectionCloseUncertain) {
			*owner = nil
		}
	}()
	for len(connections) < limit {
		connection, err := pool.Conn(ctx)
		if err != nil {
			return fmt.Errorf("pin %s sqlite connection %d: %w", kind, len(connections)+1, err)
		}
		connections = append(connections, connection)
		if err := verifyConnection(ctx, connection); err != nil {
			return fmt.Errorf("verify pinned %s sqlite connection %d: %w", kind, len(connections), err)
		}
		if sqliteConnectHook != nil {
			if err := sqliteConnectHook(kind+" verified", len(connections)); err != nil {
				return err
			}
		}
		if err := recheck(); err != nil {
			return err
		}
	}
	if err := recheck(); err != nil {
		return err
	}
	connector.seal()
	for index, connection := range connections {
		connections[index] = nil
		if err := connection.Close(); err != nil {
			checkoutReturnUncertain = true
			return fmt.Errorf("return pinned %s sqlite connection %d: %w", kind, index+1, err)
		}
	}
	connections = nil
	stats := pool.Stats()
	if stats.OpenConnections != limit || stats.Idle != limit {
		return fmt.Errorf("%w: pinned %s sqlite pool has open=%d idle=%d, want %d", ErrCorruptState, kind, stats.OpenConnections, stats.Idle, limit)
	}
	return nil
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
		cancellation := ctx.Err()
		if cancellation != nil && (errors.Is(err, cancellation) || errors.Is(err, sqlite3.INTERRUPT)) {
			err = errors.Join(err, cancellation)
			releaseUncertainConnection(connection)
		} else {
			discardConnection(connection)
		}
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
	err = errors.Join(err, tx.Close())
	if sqliteValidationHook != nil {
		sqliteValidationHook(err)
	}
	return err
}

func (store *Store) validateOpenWithRetry(ctx context.Context) error {
	err := store.validateOpen(ctx)
	if errors.Is(err, ErrCorruptState) {
		err = store.validateOpen(ctx)
	}
	return err
}

// Validate checks one coherent durable snapshot without holding the writer.
// Corruption it finds becomes a hold that refuses validated writes and
// snapshot reads until a later validation passes; a timeout or interruption
// is not corruption and leaves the hold as it was.
func (store *Store) Validate(ctx context.Context) error {
	err := store.validateOpenWithRetry(ctx)
	switch {
	case err == nil:
		store.held.Store(nil)
	case errors.Is(err, ErrCorruptState):
		store.held.Store(&err)
	}
	return err
}

// heldCorruption is the hold Validate recorded, if any.
func (store *Store) heldCorruption() error {
	if err := store.held.Load(); err != nil {
		return *err
	}
	return nil
}

func validateDatabaseSnapshot(ctx context.Context, connection *sql.Conn) error {
	if err := validateExactSchema(ctx, connection); err != nil {
		return err
	}
	if err := validateDurableControls(ctx, connection); err != nil {
		return err
	}
	if err := validateIntegrity(ctx, connection); err != nil {
		return err
	}
	return validateContentGitPins(ctx, connection)
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
		// The existing binding fence must complete before checkpointing or
		// closing the pools; checkpointing is maintenance, not a new fence.
		// A reader outside this Store can make TRUNCATE return ErrBusy. That
		// does not make the resource closure uncertain, so checkpoint failure
		// must not poison the stable Close result.
		_ = store.checkpointWAL(context.Background(), "TRUNCATE")
		poolErr := errors.Join(closeSQLitePool("reader", store.readers), closeSQLitePool("writer", store.writer))
		if poolErr != nil {
			store.closeErr = fmt.Errorf("%w: close retained sqlite pools: %w", ErrCorruptState, poolErr)
			return
		}
		if store.pathBinding != nil {
			closeErr := store.pathBinding.Close()
			store.closeErr = errors.Join(store.closeErr, closeErr)
			if closeErr == nil {
				store.pathBinding = nil
			}
		}
	})
	return store.closeErr
}

// CheckpointWAL bounds the live WAL without changing durable application
// state. It shares the writer gate with normal mutations so checkpointing
// cannot race a commit or a close.
func (store *Store) CheckpointWAL(ctx context.Context, mode string) error {
	if err := store.acquireWriter(ctx); err != nil {
		return err
	}
	defer store.releaseWriter()
	return store.checkpointWAL(ctx, mode)
}

func (store *Store) checkpointWAL(ctx context.Context, mode string) error {
	if store.writer == nil {
		return nil
	}
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout writer connection: %w", err)
	}
	defer connection.Close()
	var busy, pages, checkpointed int
	if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &pages, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return ErrBusy
	}
	return nil
}

// WALSize returns the currently retained WAL size for health and maintenance
// decisions. A missing WAL is the normal zero-size state.
func (store *Store) WALSize() int64 {
	if store == nil {
		return 0
	}
	store.bindingMu.RLock()
	defer store.bindingMu.RUnlock()
	if store.pathBinding == nil || store.pathBinding.wal == nil {
		return 0
	}
	info, err := store.pathBinding.wal.file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func (store *Store) retainCloseUncertainty(cause error) {
	store.closed.Store(true)
	store.close.Do(func() {
		store.closeErr = fmt.Errorf("%w: close retained sqlite activation: %w", ErrCorruptState, cause)
	})
}

func closeSQLitePool(kind string, pool *sql.DB) error {
	if pool == nil {
		return nil
	}
	if sqlitePoolCloseHook != nil {
		if err := sqlitePoolCloseHook(kind); err != nil {
			return err
		}
	}
	return pool.Close()
}

func (store *Store) waitForCheckedOutConnections() {
	for poolInUse(store.writer) || poolInUse(store.readers) {
		time.Sleep(time.Millisecond)
	}
}

func poolInUse(pool *sql.DB) bool {
	return pool != nil && pool.Stats().InUse != 0
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
	if err := store.heldCorruption(); err != nil {
		return nil, err
	}
	return store.beginUncheckedWrite(ctx)
}

// beginUncheckedWrite reserves the writer. Mutations validate the rows and
// references they touch; retained history is checked at open and by the
// daemon's periodic health validation instead of on every write.
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
	// BEGIN writes nothing, so its failure is never an unknown outcome. A
	// cancelled one is the cancellation, whichever sqlite code reports it.
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if cancellation := ctx.Err(); cancellation != nil {
			tx.discard()
			tx.Close()
			return nil, fmt.Errorf("begin immediate: %w (%v)", cancellation, err)
		}
		if errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.LOCKED) {
			tx.Close()
			return nil, fmt.Errorf("%w: %v", ErrBusy, err)
		}
		tx.discard()
		tx.Close()
		return nil, fmt.Errorf("begin immediate: %w", err)
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
	// Destroying a connection is fail-closed but permanent: the retained
	// physical set is sealed at activation and cannot mint a replacement, and
	// the writer set holds exactly one connection, so one destroyed connection
	// ends every later write with errConnectionSetExhausted. A statement that
	// was interrupted before it took the write reservation leaves the
	// connection in autocommit and is provably clean, so resolve first and
	// destroy only what cannot be resolved.
	if resolveConnection(tx.connection) {
		return
	}
	discardConnection(tx.connection)
	tx.discarded = true
}

// resolveConnection reports whether the connection is provably free of a
// transaction of ours. SQLite answers exactly through autocommit mode: a
// lifecycle statement interrupted before it took the write reservation leaves
// autocommit on, which is the shutdown case, and a connection whose statement
// did open a transaction has already failed to close it, so it is destroyed
// rather than asked again.
func resolveConnection(connection *sql.Conn) bool {
	return connectionInAutocommit(connection)
}

func connectionInAutocommit(connection *sql.Conn) bool {
	autocommit := false
	if err := connection.Raw(func(driverConnection any) error {
		sqliteConnection, ok := driverConnection.(sqliteDriver.Conn)
		if !ok {
			return nil
		}
		autocommit = sqliteConnection.Raw().GetAutocommit()
		return nil
	}); err != nil {
		return false
	}
	return autocommit
}

// releaseUncertainConnection returns a connection to its retained pool when its
// state is provably clean and destroys it otherwise.
func releaseUncertainConnection(connection *sql.Conn) {
	if resolveConnection(connection) {
		_ = connection.Close()
		return
	}
	discardConnection(connection)
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
	var caller [8]uintptr
	runtime.Callers(2, caller[:])
	waiting := time.Now()
	store.writerWaiters.Add(1)
	select {
	case <-ctx.Done():
		store.writerWaiters.Add(-1)
		return ctx.Err()
	case <-store.writerGate:
	}
	store.writerWaiters.Add(-1)
	store.writerHolder, store.writerSince = caller, time.Now()
	if wait := store.writerSince.Sub(waiting); wait > store.longestWait {
		store.longestWait, store.longestWaiter = wait, caller
	}
	if store.writerSince.Sub(store.waitReported) >= time.Minute {
		if store.longestWait > slowWriter {
			store.logWriter("factoryd: longest sqlite writer wait in the last minute %s by %s\n", store.longestWait.Round(time.Millisecond), writerCaller(store.longestWaiter))
		}
		store.longestWait, store.waitReported = 0, store.writerSince
	}
	if err := ctx.Err(); err != nil {
		store.releaseWriter()
		return err
	}
	return nil
}

func (store *Store) releaseWriter() {
	if held := time.Since(store.writerSince); held > slowWriter {
		store.logWriter("factoryd: sqlite writer held %s by %s, %d waiting\n", held.Round(time.Millisecond), writerCaller(store.writerHolder), store.writerWaiters.Load())
	}
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
