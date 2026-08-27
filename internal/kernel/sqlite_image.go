package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/serdes"
)

const (
	maxFreshDatabaseImageSize     = 8 << 20
	maxImmutableDatabaseImageSize = 256 << 20
)

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
		rawConnection := driverConnection.Raw()
		previousInterrupt := rawConnection.SetInterrupt(ctx)
		defer rawConnection.SetInterrupt(previousInterrupt)
		var serializeErr error
		image, serializeErr = serdes.Serialize(rawConnection, "main")
		return errors.Join(serializeErr, ctx.Err())
	}); err != nil {
		return nil, fmt.Errorf("serialize fresh sqlite database: %w", err)
	}
	if len(image) == 0 || len(image) > maxFreshDatabaseImageSize {
		return nil, fmt.Errorf("%w: fresh sqlite image size %d outside 1..%d", ErrCorruptState, len(image), maxFreshDatabaseImageSize)
	}
	if err := InspectPristine(ctx, bytes.NewReader(image), int64(len(image))); err != nil {
		return nil, fmt.Errorf("validate serialized sqlite database: %w", err)
	}
	return image, nil
}

// InspectImmutable validates an already-open, sidecar-free SQLite main file
// without taking ownership of reader or opening a filesystem path. The caller
// must hold the database lifetime lock, prove that no sidecar exists, and keep
// the declared bytes stable until this call returns.
func InspectImmutable(ctx context.Context, reader io.ReaderAt, size int64) error {
	return inspectImmutable(ctx, reader, size, false)
}

// InspectPristine validates the exact sidecar-free rollback image produced by
// NewDatabaseImage. In addition to InspectImmutable's caller obligations, it
// rejects retained product rows, sequence/free-page state, and noncanonical
// SQLite internal objects.
func InspectPristine(ctx context.Context, reader io.ReaderAt, size int64) error {
	return inspectImmutable(ctx, reader, size, true)
}

func inspectImmutable(ctx context.Context, reader io.ReaderAt, size int64, requirePristine bool) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reader == nil || size <= 0 {
		return fmt.Errorf("%w: immutable sqlite reader and positive size are required", ErrInvalidValue)
	}
	if size > maxImmutableDatabaseImageSize {
		return fmt.Errorf("%w: immutable sqlite image size %d exceeds %d", ErrInvalidValue, size, maxImmutableDatabaseImageSize)
	}
	image, err := readImmutableImage(ctx, reader, size)
	if err != nil {
		return err
	}
	if err := validateDatabaseHeader(bytes.NewReader(image), size, false); err != nil {
		return err
	}
	if requirePristine && (image[18] != 1 || image[19] != 1) {
		return fmt.Errorf("%w: pristine sqlite image must use rollback header 1/1", ErrForeignDatabase)
	}
	pool, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return fmt.Errorf("open private immutable sqlite database: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		if err := pool.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close private immutable sqlite database: %w", err))
		}
	}()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout private immutable sqlite connection: %w", err)
	}
	if err := connection.Raw(func(raw any) error {
		driverConnection, ok := raw.(sqliteDriver.Conn)
		if !ok {
			return errors.New("sqlite driver does not expose its raw connection")
		}
		rawConnection := driverConnection.Raw()
		previousInterrupt := rawConnection.SetInterrupt(ctx)
		defer rawConnection.SetInterrupt(previousInterrupt)
		return errors.Join(serdes.Deserialize(rawConnection, "main", image), ctx.Err())
	}); err != nil {
		return errors.Join(fmt.Errorf("restore immutable sqlite image: %w", err), connection.Close())
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return errors.Join(fmt.Errorf("make immutable sqlite connection read-only: %w", err), connection.Close())
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		return errors.Join(fmt.Errorf("configure immutable sqlite temporary storage: %w", err), connection.Close())
	}
	err = validateDatabaseSnapshot(ctx, connection)
	if err == nil && requirePristine {
		err = validatePristineBootstrap(ctx, connection)
	}
	return errors.Join(err, connection.Close())
}

func readImmutableImage(ctx context.Context, reader io.ReaderAt, size int64) ([]byte, error) {
	image := make([]byte, int(size))
	const chunkSize = 128 << 10
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := offset + chunkSize
		if end > size {
			end = size
		}
		read, err := reader.ReadAt(image[int(offset):int(end)], offset)
		want := int(end - offset)
		if read < 0 || read > want || read != want {
			return nil, fmt.Errorf("read immutable sqlite image at %d: %w", offset, errors.Join(io.ErrUnexpectedEOF, err))
		}
		if err != nil {
			return nil, fmt.Errorf("read immutable sqlite image at %d: %w", offset, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset = end
	}
	return image, nil
}

func validatePristineBootstrap(ctx context.Context, connection *sql.Conn) error {
	var rows int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM factory WHERE singleton = 1 AND revision = 1 AND next_invalidation_sequence = 1 AND invalidation_floor = 1`).Scan(&rows); err != nil {
		return fmt.Errorf("inspect bootstrap factory state: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: rollback database is not an exact fresh bootstrap", ErrForeignDatabase)
	}
	for name, object := range expectedSchema() {
		if object.kind != "table" || name == "factory" {
			continue
		}
		if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM "`+name+`" LIMIT 1)`).Scan(&rows); err != nil {
			return fmt.Errorf("inspect bootstrap table %s: %w", name, err)
		}
		if rows != 0 {
			return fmt.Errorf("%w: rollback database contains retained %s state", ErrForeignDatabase, name)
		}
	}
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_sequence LIMIT 1)`).Scan(&rows); err != nil {
		return fmt.Errorf("inspect bootstrap sqlite sequence: %w", err)
	}
	if rows != 0 {
		return fmt.Errorf("%w: rollback database contains retained sequence state", ErrForeignDatabase)
	}
	internal, err := connection.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_schema WHERE name LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return fmt.Errorf("inspect bootstrap internal schema: %w", err)
	}
	defer internal.Close()
	type internalObject struct {
		kind       string
		table      string
		definition sql.NullString
	}
	expectedInternal := map[string]internalObject{
		"sqlite_autoindex_browser_clients_2": {kind: "index", table: "browser_clients"},
		"sqlite_autoindex_human_requests_2":  {kind: "index", table: "human_requests"},
		"sqlite_autoindex_human_requests_3":  {kind: "index", table: "human_requests"},
		"sqlite_sequence": {kind: "table", table: "sqlite_sequence", definition: sql.NullString{
			String: "CREATE TABLE sqlite_sequence(name,seq)", Valid: true,
		}},
	}
	seenInternal := make(map[string]bool, len(expectedInternal))
	for internal.Next() {
		var kind, name, table string
		var definition sql.NullString
		if err := internal.Scan(&kind, &name, &table, &definition); err != nil {
			return fmt.Errorf("scan bootstrap internal schema: %w", err)
		}
		expected, ok := expectedInternal[name]
		if !ok || kind != expected.kind || table != expected.table || definition != expected.definition {
			return fmt.Errorf("%w: rollback database contains unexpected internal object %s %s", ErrForeignDatabase, kind, name)
		}
		seenInternal[name] = true
	}
	if err := internal.Err(); err != nil {
		return fmt.Errorf("iterate bootstrap internal schema: %w", err)
	}
	if len(seenInternal) != len(expectedInternal) {
		return fmt.Errorf("%w: rollback database has %d canonical internal objects, want %d", ErrForeignDatabase, len(seenInternal), len(expectedInternal))
	}
	var freePages int64
	if err := connection.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return fmt.Errorf("inspect bootstrap free pages: %w", err)
	}
	if freePages != 0 {
		return fmt.Errorf("%w: rollback database contains %d free pages", ErrForeignDatabase, freePages)
	}
	return nil
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
