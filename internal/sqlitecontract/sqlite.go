// Package sqlitecontract freezes the SQLite connection and immediate-write
// pattern before the product Store is implemented.
package sqlitecontract

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const (
	busyMilliseconds = 5000
	maxReaders       = 4
	driverName       = "sqlite3"
)

var errBusy = errors.New("sqlite writer busy")

type outcomeUnknownError struct {
	cause error
}

func (e *outcomeUnknownError) Error() string {
	return "sqlite transaction outcome is unknown: " + e.cause.Error()
}

func (e *outcomeUnknownError) Unwrap() error {
	return e.cause
}

type database struct {
	writer  *sql.DB
	readers *sql.DB
}

func openDatabase(path string) (*database, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("sqlite path must be absolute: %q", path)
	}

	writer, err := openPool(path, 1)
	if err != nil {
		return nil, err
	}
	readers, err := openPool(path, maxReaders)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}

	database := &database{writer: writer, readers: readers}
	if err := database.verifyInitialConnections(); err != nil {
		_ = database.close()
		return nil, err
	}
	return database, nil
}

func openPool(path string, limit int) (*sql.DB, error) {
	pool, err := sql.Open(driverName, dataSource(path))
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

func dataSource(path string) string {
	query := url.Values{}
	query["_pragma"] = []string{
		fmt.Sprintf("busy_timeout(%d)", busyMilliseconds),
		"foreign_keys(ON)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func (d *database) verifyInitialConnections() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()

	for name, pool := range map[string]*sql.DB{"writer": d.writer, "reader": d.readers} {
		connection, err := pool.Conn(ctx)
		if err != nil {
			return fmt.Errorf("checkout %s connection: %w", name, err)
		}
		err = verifyConnection(ctx, connection)
		closeErr := connection.Close()
		if err != nil {
			return fmt.Errorf("verify %s connection: %w", name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("return %s connection: %w", name, closeErr)
		}
	}
	return nil
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

func (d *database) close() error {
	return errors.Join(d.readers.Close(), d.writer.Close())
}

// immediate is deliberately unexported. Domain methods must own this pattern;
// callers never receive a generic transaction object or callback API.
func (d *database) immediate(ctx context.Context, body func(*sql.Conn) error) (err error) {
	connection, err := d.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout writer connection: %w", err)
	}
	closed := false
	closeConnection := func() {
		if !closed {
			_ = connection.Close()
			closed = true
		}
	}
	defer closeConnection()

	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.LOCKED) {
			return fmt.Errorf("%w: %v", errBusy, err)
		}
		discardConnection(connection)
		closed = true
		return &outcomeUnknownError{cause: fmt.Errorf("begin immediate: %w", err)}
	}
	inTransaction := true
	defer func() {
		if recovered := recover(); recovered != nil {
			if inTransaction {
				if rollbackErr := rollback(connection); rollbackErr != nil {
					discardConnection(connection)
					closed = true
				}
			}
			panic(recovered)
		}
	}()

	if err := body(connection); err != nil {
		if rollbackErr := rollback(connection); rollbackErr != nil {
			discardConnection(connection)
			closed = true
			return &outcomeUnknownError{cause: errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))}
		}
		inTransaction = false
		return err
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := rollback(connection); rollbackErr != nil {
			discardConnection(connection)
			closed = true
			return &outcomeUnknownError{cause: errors.Join(err, fmt.Errorf("rollback after cancellation failed: %w", rollbackErr))}
		}
		inTransaction = false
		return err
	}

	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		discardConnection(connection)
		closed = true
		return &outcomeUnknownError{cause: err}
	}
	inTransaction = false
	return nil
}

func rollback(connection *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	_, err := connection.ExecContext(ctx, "ROLLBACK")
	return err
}

func discardConnection(connection *sql.Conn) {
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
	_ = connection.Close()
}
