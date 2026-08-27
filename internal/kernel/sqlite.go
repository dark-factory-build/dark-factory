package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/serdes"
	sqliteIO "github.com/ncruces/go-sqlite3/util/ioutil"
	"github.com/ncruces/go-sqlite3/vfs/readervfs"
)

const (
	busyMilliseconds          = 5000
	maxReaders                = 4
	driverName                = "sqlite3"
	maxFreshDatabaseImageSize = 8 << 20
)

var immutableReaderSequence atomic.Uint64

type Store struct {
	writer     *sql.DB
	readers    *sql.DB
	writerGate chan struct{}
	closed     atomic.Bool
	close      sync.Once
	closeErr   error
}

func Create(ctx context.Context, absolutePath string, config FactoryConfig, at UnixMillis) (*Store, error) {
	if err := validateDatabasePath(absolutePath); err != nil {
		return nil, err
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("reserve fresh sqlite database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close fresh sqlite reservation: %w", err)
	}

	store, err := openPools(absolutePath)
	if err != nil {
		return nil, err
	}
	if err := store.initialize(ctx, config, at); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.validateOpen(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// NewDatabaseImage creates one complete fresh database without touching the
// filesystem. The returned main-database image uses SQLite's rollback header;
// Open validates the complete image before promoting it to persistent WAL.
func NewDatabaseImage(ctx context.Context, config FactoryConfig, at UnixMillis) (image []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	pool, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open in-memory sqlite database: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		if err := pool.Close(); err != nil {
			image = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("close in-memory sqlite database: %w", err))
		}
	}()

	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout in-memory sqlite connection: %w", err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			image = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("return in-memory sqlite connection: %w", err))
		}
	}()
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("configure in-memory sqlite database: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		return nil, fmt.Errorf("configure in-memory sqlite temporary storage: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin in-memory sqlite initialization: %w", err)
	}
	if err := initializeFresh(ctx, connection, config, at); err != nil {
		_, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK")
		return nil, errors.Join(err, rollbackErr)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit in-memory sqlite initialization: %w", err)
	}
	if err := validateDatabaseSnapshot(ctx, connection); err != nil {
		return nil, err
	}
	if err := connection.Raw(func(raw any) error {
		driverConnection, ok := raw.(sqliteDriver.Conn)
		if !ok {
			return errors.New("sqlite driver does not expose its raw connection")
		}
		var serializeErr error
		image, serializeErr = serdes.Serialize(driverConnection.Raw(), "main")
		return serializeErr
	}); err != nil {
		return nil, fmt.Errorf("serialize fresh sqlite database: %w", err)
	}
	if len(image) == 0 || len(image) > maxFreshDatabaseImageSize {
		return nil, fmt.Errorf("%w: fresh sqlite image size %d outside 1..%d", ErrCorruptState, len(image), maxFreshDatabaseImageSize)
	}
	if err := InspectImmutable(ctx, bytes.NewReader(image), int64(len(image))); err != nil {
		return nil, fmt.Errorf("validate serialized sqlite database: %w", err)
	}
	return image, nil
}

// InspectImmutable validates an already-open, sidecar-free SQLite main
// database without taking ownership of reader and without any write-capable
// Store or filesystem path. The caller owns lifetime locking and sidecar
// policy and must keep reader's bytes stable until this call returns.
func InspectImmutable(ctx context.Context, reader io.ReaderAt, size int64) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reader == nil || size <= 0 {
		return fmt.Errorf("%w: immutable sqlite reader and positive size are required", ErrInvalidValue)
	}
	section := io.NewSectionReader(reader, 0, size)
	if err := validateDatabaseHeader(section, size, false); err != nil {
		return err
	}
	name := fmt.Sprintf("dark-factory-kernel-%d", immutableReaderSequence.Add(1))
	readervfs.Create(name, sqliteIO.NewSizeReaderAt(section))
	defer readervfs.Delete(name)

	query := url.Values{
		"_pragma": {"query_only(ON)", "temp_store(MEMORY)"},
		"vfs":     {"reader"},
	}
	pool, err := sql.Open(driverName, "file:"+name+"?"+query.Encode())
	if err != nil {
		return fmt.Errorf("open immutable sqlite reader: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		resultErr = errors.Join(resultErr, pool.Close())
	}()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout immutable sqlite reader: %w", err)
	}
	tx, err := beginPinnedRead(ctx, connection)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("begin immutable sqlite inspection: %w", err)
	}
	err = validateDatabaseSnapshot(ctx, tx.connection)
	return errors.Join(err, tx.Close())
}

func Open(ctx context.Context, absolutePath string) (*Store, error) {
	if err := validateDatabasePath(absolutePath); err != nil {
		return nil, err
	}
	if err := validateExistingFile(absolutePath); err != nil {
		return nil, err
	}
	if err := preflightExisting(ctx, absolutePath); err != nil {
		return nil, err
	}
	store, err := openPools(absolutePath)
	if err != nil {
		return nil, err
	}
	if err := store.validateOpen(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func validateDatabasePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: sqlite path must be absolute: %q", ErrInvalidValue, path)
	}
	return nil
}

func validateExistingFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect sqlite database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: sqlite database is not a regular file", ErrForeignDatabase)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: sqlite database mode is %#o, want 0600", ErrForeignDatabase, info.Mode().Perm())
	}
	return nil
}

func preflightExisting(ctx context.Context, path string) error {
	query := url.Values{}
	query.Set("mode", "ro")
	rollbackJournalPresent, err := pathExists(path + "-journal")
	if err != nil {
		return err
	}
	if rollbackJournalPresent {
		return fmt.Errorf("%w: rollback journal requires recovery", ErrCorruptState)
	}
	walPresent, err := pathExists(path + "-wal")
	if err != nil {
		return err
	}
	shmPresent, err := pathExists(path + "-shm")
	if err != nil {
		return err
	}
	if walPresent != shmPresent {
		return fmt.Errorf("%w: incomplete WAL sidecar pair", ErrCorruptState)
	}
	if !walPresent {
		query.Set("immutable", "1")
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
	pool, err := sql.Open(driverName, dsn)
	if err != nil {
		return fmt.Errorf("open sqlite preflight: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer pool.Close()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout sqlite preflight: %w", err)
	}
	tx, err := beginPinnedRead(ctx, connection)
	if err != nil {
		return fmt.Errorf("begin sqlite preflight: %w", err)
	}
	err = validateExactSchema(ctx, tx.connection)
	if err == nil {
		// A standalone rollback-header image is the filesystem-free bootstrap
		// form. It is accepted only through this complete read-only preflight;
		// openPools then promotes it to WAL and verifies WAL on every connection
		// before returning a Store. Existing WAL sidecars still require 2/2.
		err = validateJournalHeaderPath(path, walPresent)
	}
	if err == nil {
		err = validateDurableControls(ctx, tx.connection)
	}
	if err == nil {
		err = validateIntegrity(ctx, tx.connection)
	}
	return errors.Join(err, tx.Close())
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect sqlite sidecar: %w", err)
}

func validateJournalHeaderPath(path string, walRequired bool) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open sqlite header: %w", err)
	}
	defer file.Close()
	return validateJournalHeader(file, walRequired)
}

func validateDatabaseHeader(reader io.ReaderAt, size int64, walRequired bool) error {
	header := make([]byte, 100)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
	}
	if err := validateJournalHeaderBytes(header, walRequired); err != nil {
		return err
	}
	pageSize := int64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	pageCount := int64(binary.BigEndian.Uint32(header[28:32]))
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || pageCount < 1 || pageCount > (1<<63-1)/pageSize || pageCount*pageSize != size {
		return fmt.Errorf("%w: invalid sqlite page size or database length", ErrCorruptState)
	}
	return nil
}

func validateJournalHeader(reader io.ReaderAt, walRequired bool) error {
	header := make([]byte, 20)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
	}
	return validateJournalHeaderBytes(header, walRequired)
}

func validateJournalHeaderBytes(header []byte, walRequired bool) error {
	if string(header[:16]) != "SQLite format 3\x00" {
		return fmt.Errorf("%w: invalid sqlite header", ErrForeignDatabase)
	}
	if header[18] != header[19] || header[18] != 1 && header[18] != 2 {
		return fmt.Errorf("%w: invalid database journal header", ErrCorruptState)
	}
	if walRequired && header[18] != 2 {
		return fmt.Errorf("%w: database with WAL sidecars does not have a WAL header", ErrCorruptState)
	}
	return nil
}

func openPools(path string) (*Store, error) {
	writer, err := openPool(path, 1)
	if err != nil {
		return nil, err
	}
	readers, err := openPool(path, maxReaders)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	writerGate := make(chan struct{}, 1)
	writerGate <- struct{}{}
	store := &Store{writer: writer, readers: readers, writerGate: writerGate}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	writerConnection, err := store.writerConnection(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := writerConnection.Close(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("return initial writer connection: %w", err)
	}
	readerConnection, err := store.readerConnection(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := readerConnection.Close(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("return initial reader connection: %w", err)
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
		_ = pool.Close()
		return nil, fmt.Errorf("initialize sqlite pool: %w", err)
	}
	return pool, nil
}

func configuredDataSource(path string) string {
	query := url.Values{}
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
	if store.closed.Load() {
		return nil, ErrStoreClosed
	}
	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout %s connection: %w", kind, err)
	}
	if err := verifyConnection(ctx, connection); err != nil {
		discardConnection(connection)
		return nil, fmt.Errorf("verify %s connection: %w", kind, err)
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

func (store *Store) initialize(ctx context.Context, config FactoryConfig, at UnixMillis) error {
	for attempt := 0; attempt < 2; attempt++ {
		tx, err := store.beginUncheckedWrite(ctx)
		if err != nil {
			return err
		}
		err = initializeFresh(ctx, tx.connection, config, at)
		if err != nil {
			result := tx.Rollback(err)
			tx.Close()
			return result
		}
		err = tx.Commit(ctx)
		tx.Close()
		if err == nil {
			return nil
		}
		var unknown *OutcomeUnknownError
		if !errors.As(err, &unknown) {
			return err
		}
		connection, checkoutErr := store.readerConnection(ctx)
		if checkoutErr != nil {
			return errors.Join(err, checkoutErr)
		}
		exactErr := validateExactSchema(ctx, connection)
		if exactErr == nil {
			_ = connection.Close()
			return nil
		}
		empty, emptyErr := schemaIsEmpty(ctx, connection)
		_ = connection.Close()
		if emptyErr != nil || !empty || attempt == 1 {
			return errors.Join(err, exactErr, emptyErr)
		}
	}
	return errors.New("unreachable sqlite initialization state")
}

func initializeFresh(ctx context.Context, connection *sql.Conn, config FactoryConfig, at UnixMillis) error {
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
		var rawDaemonID [IDBytes]byte
		if _, err = rand.Read(rawDaemonID[:]); err == nil && rawDaemonID == [IDBytes]byte{} {
			err = fmt.Errorf("%w: generated zero daemon identifier", ErrCorruptState)
		}
		if err == nil {
			inserted, insertErr := connection.ExecContext(ctx,
				`INSERT INTO factory(singleton, daemon_id, dispatch_enabled, capacity, revision, next_invalidation_sequence, invalidation_floor, updated_at_ms) VALUES(1, ?, ?, ?, 1, 1, 1, ?)`,
				rawDaemonID[:], boolInt(config.DispatchEnabled), int64(config.Capacity), at.Int64())
			err = requireOneRow(inserted, insertErr)
		}
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
		// leave without touching SQLite.
		if err := store.acquireWriter(context.Background()); err != nil {
			store.closeErr = err
			return
		}
		defer store.releaseWriter()
		store.closeErr = errors.Join(store.readers.Close(), store.writer.Close())
	})
	return store.closeErr
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
