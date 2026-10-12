package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
)

// HistoryRetention is how long, in ms, a terminal run's content-access
// receipts stay in the hot database before ArchiveHistory moves them.
const HistoryRetention = 7 * 24 * 60 * 60 * 1000

// archiveCopiedHook is package-local deterministic fault instrumentation
// between the archive commit and the hot one. Production has no hook.
var archiveCopiedHook func() error

// The archive is an append-only SQLite file beside the hot database. Its rows
// carry the run identity readers join on, so they never need the hot run and
// nothing in either file holds a foreign key across the boundary.
var archiveSchema = []string{
	`CREATE TABLE IF NOT EXISTS archive.batches (generation INTEGER PRIMARY KEY, rows INTEGER NOT NULL, digest BLOB NOT NULL, archived_at_ms INTEGER NOT NULL) STRICT`,
	`CREATE TABLE IF NOT EXISTS archive.content_accesses (generation INTEGER NOT NULL, ` + archivedAccessDefinitions + `,
    UNIQUE(generation, run_id, content_id, content_revision, kind, byte_offset, byte_length)) STRICT`,
	`CREATE INDEX IF NOT EXISTS archive.content_accesses_generation ON content_accesses(generation)`,
	`CREATE INDEX IF NOT EXISTS archive.content_accesses_content ON content_accesses(content_id, content_revision, created_at_ms DESC)`,
}

const (
	archivedAccessDefinitions = `run_id BLOB NOT NULL, content_id BLOB, content_revision INTEGER, kind TEXT NOT NULL, byte_offset INTEGER NOT NULL, byte_length INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, project_id BLOB NOT NULL, agent_id BLOB NOT NULL, task_id BLOB NOT NULL, task_work_revision INTEGER NOT NULL`
	archivedAccessColumns     = `run_id, content_id, content_revision, kind, byte_offset, byte_length, created_at_ms, project_id, agent_id, task_id, task_work_revision`
	archivedAccessOrder       = ` ORDER BY run_id, kind, content_id, content_revision, byte_offset, byte_length`
	hotContentAccesses        = `SELECT a.run_id, a.content_id, a.content_revision, a.kind, a.byte_offset, a.byte_length, a.created_at_ms, r.project_id, r.agent_id, r.task_id, r.admitted_task_work_revision AS task_work_revision FROM main.content_accesses AS a JOIN main.runs AS r ON r.id = a.run_id`
	// One batch is every receipt of up to 1024 runs terminal before the
	// cutoff (?1), which is far more than a day produces.
	archiveBatchRuns  = `SELECT r.id FROM main.runs AS r WHERE r.phase = 'terminal' AND r.terminal_at_ms < ?1 AND EXISTS (SELECT 1 FROM main.content_accesses AS x WHERE x.run_id = r.id) ORDER BY r.id LIMIT 1024`
	archiveSelection  = `SELECT * FROM (` + hotContentAccesses + ` WHERE a.run_id IN (` + archiveBatchRuns + `))` + archivedAccessOrder
	archiveGeneration = `SELECT ` + archivedAccessColumns + ` FROM archive.content_accesses WHERE generation = ?1` + archivedAccessOrder
)

// contentAccessesSQL is every content-access receipt with its run's identity:
// the hot ones joined to their run and, when archived, those of the committed
// archive generations. Uncommitted generations are never read.
func contentAccessesSQL(archived bool) string {
	if !archived {
		return "(" + hotContentAccesses + ")"
	}
	return "(" + hotContentAccesses + ` UNION ALL SELECT ` + archivedAccessColumns + ` FROM archive.content_accesses WHERE generation IN (SELECT generation FROM main.archive_batches))`
}

func archiveDataSource(path, mode string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode}).String()
}

// beginHistoryRead begins a read that may cross into the archive and reports
// whether anything is archived. If so, the archive is attached read-only and
// each committed generation is checked against it first, so a missing or
// damaged archive fails this read closed and leaves hot reads alone.
// ponytail: a read that finds the archive file absent and then the first
// generation ever committed fails once; the next read attaches it.
func (store *Store) beginHistoryRead(ctx context.Context) (*readTx, bool, error) {
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return nil, false, err
	}
	_, attachErr := connection.ExecContext(ctx, `ATTACH ? AS archive`, archiveDataSource(store.archivePath, "ro"))
	tx := &readTx{connection: connection, attached: attachErr == nil}
	if _, err := connection.ExecContext(ctx, "BEGIN"); err != nil {
		if tx.attached {
			if _, detachErr := connection.ExecContext(context.Background(), "DETACH archive"); detachErr != nil {
				discardConnection(connection)
				return nil, false, errors.Join(err, detachErr)
			}
		}
		releaseUncertainConnection(connection)
		return nil, false, fmt.Errorf("begin sqlite read: %w", err)
	}
	tx.active = true
	var archived bool
	err = connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.archive_batches)`).Scan(&archived)
	if err == nil && archived {
		if err = attachErr; err == nil {
			err = verifyArchive(ctx, connection)
		}
		if err != nil {
			err = fmt.Errorf("%w: history archive %s: %w", ErrCorruptState, store.archivePath, err)
		}
	}
	if err != nil {
		return nil, false, errors.Join(err, tx.Close())
	}
	return tx, archived, nil
}

// verifyArchive fails unless the rows of every generation the hot database
// committed hash, in the archive, to the digest and count it committed.
// ponytail: this rehashes the whole archive per crossing read, as those reads
// already scan the receipts; keep a verified high-water mark if it grows slow.
func verifyArchive(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT generation, rows, digest FROM main.archive_batches ORDER BY generation`)
	if err != nil {
		return err
	}
	type batch struct {
		generation, rows int64
		digest           []byte
	}
	var batches []batch
	for rows.Next() {
		var b batch
		if err := rows.Scan(&b.generation, &b.rows, &b.digest); err != nil {
			return errors.Join(err, rows.Close())
		}
		batches = append(batches, b)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, b := range batches {
		digest, count, err := digestRows(ctx, connection, archiveGeneration, b.generation)
		if err != nil {
			return err
		}
		if int64(count) != b.rows || !bytes.Equal(digest, b.digest) {
			return fmt.Errorf("committed generation %d is missing or changed", b.generation)
		}
	}
	return nil
}

// ArchiveHistory moves one batch of the content-access receipts of runs
// terminal before at - HistoryRetention into the archive and reports how
// many it moved. The copy commits in the archive first and is proven against
// the hot rows by digest; only then does one hot transaction delete them and
// record the generation. The archive only grows: a generation the hot
// database does not record, from a crash between the two commits or one a
// restored backup predates, is ignored by readers and never reused, and its
// rows, still hot, are copied again under a new one. A recorded generation
// the archive lacks fails every pass and every crossing read closed. Nothing
// else reads the archive.
func (store *Store) ArchiveHistory(ctx context.Context, at UnixMillis) (int, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Close()
	// ATTACH and the journal mode change only outside a transaction.
	if err := tx.Rollback(nil); err != nil {
		return 0, err
	}
	var mode string
	if _, err := tx.connection.ExecContext(ctx, `ATTACH ? AS archive`, archiveDataSource(store.archivePath, "rwc")); err != nil {
		return 0, fmt.Errorf("attach history archive %s: %w", store.archivePath, err)
	}
	moved, err := 0, tx.connection.QueryRowContext(ctx, `PRAGMA archive.journal_mode = DELETE`).Scan(&mode)
	if err == nil {
		moved, err = archiveBatch(ctx, tx, at.Int64()-HistoryRetention, at.Int64())
	}
	if !tx.discarded {
		if _, detachErr := tx.connection.ExecContext(context.Background(), "DETACH archive"); detachErr != nil {
			err = errors.Join(err, detachErr)
		}
	}
	return moved, err
}

func archiveBatch(ctx context.Context, tx *writeTx, cutoff, at int64) (int, error) {
	connection := tx.connection
	var generation int64
	var digest []byte
	rows := 0
	err := tx.within(ctx, func() error {
		for _, statement := range archiveSchema {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if err := verifyArchive(ctx, connection); err != nil {
			return fmt.Errorf("%w: history archive: %w", ErrCorruptState, err)
		}
		if err := connection.QueryRowContext(ctx, `SELECT max(COALESCE((SELECT max(generation) FROM main.archive_batches), 0), COALESCE((SELECT max(generation) FROM archive.content_accesses), 0), COALESCE((SELECT max(generation) FROM archive.batches), 0)) + 1`).Scan(&generation); err != nil {
			return err
		}
		var err error
		if digest, rows, err = digestRows(ctx, connection, archiveSelection, cutoff); err != nil || rows == 0 {
			return err
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO archive.content_accesses(generation, `+archivedAccessColumns+`) SELECT ?2, `+archivedAccessColumns+` FROM (`+archiveSelection+`)`, cutoff, generation); err != nil {
			return err
		}
		copied, count, err := digestRows(ctx, connection, archiveGeneration, generation)
		if err != nil {
			return err
		}
		if count != rows || !bytes.Equal(copied, digest) {
			return fmt.Errorf("%w: archived generation %d differs from its hot rows", ErrCorruptState, generation)
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO archive.batches(generation, rows, digest, archived_at_ms) VALUES (?, ?, ?, ?)`, generation, rows, digest, at)
		return err
	})
	if err != nil || rows == 0 {
		return 0, err
	}
	if archiveCopiedHook != nil {
		if err := archiveCopiedHook(); err != nil {
			return 0, err
		}
	}
	err = tx.within(ctx, func() error {
		hot, count, err := digestRows(ctx, connection, archiveSelection, cutoff)
		if err != nil {
			return err
		}
		var archived []byte
		if err := connection.QueryRowContext(ctx, `SELECT digest FROM archive.batches WHERE generation = ? AND rows = ?`, generation, rows).Scan(&archived); err != nil {
			return err
		}
		if count != rows || !bytes.Equal(hot, digest) || !bytes.Equal(archived, digest) {
			return fmt.Errorf("%w: archived generation %d differs from its hot rows", ErrCorruptState, generation)
		}
		deleted, err := connection.ExecContext(ctx, `DELETE FROM main.content_accesses WHERE run_id IN (`+archiveBatchRuns+`)`, cutoff)
		if err != nil {
			return err
		}
		if n, err := deleted.RowsAffected(); err != nil || n != int64(rows) {
			return errors.Join(err, fmt.Errorf("%w: deleted %d of %d archived rows", ErrCorruptState, n, rows))
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO main.archive_batches(generation, rows, digest, archived_at_ms) VALUES (?, ?, ?, ?)`, generation, rows, digest, at)
		return err
	})
	if err != nil {
		return 0, err
	}
	return rows, nil
}

// digestRows hashes the rows query returns in order and counts them.
func digestRows(ctx context.Context, connection *sql.Conn, query string, args ...any) ([]byte, int, error) {
	rows, err := connection.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	columns, err := rows.Columns()
	if err != nil {
		return nil, 0, errors.Join(err, rows.Close())
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for index := range values {
		targets[index] = &values[index]
	}
	hash, count := sha256.New(), 0
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return nil, 0, errors.Join(err, rows.Close())
		}
		fmt.Fprintf(hash, "%#v\n", values)
		count++
	}
	return hash.Sum(nil), count, errors.Join(rows.Err(), rows.Close())
}

// within runs step in an immediate transaction of its own on a writer held
// outside one, as BackupTo and ArchiveHistory hold it.
func (tx *writeTx) within(ctx context.Context, step func() error) error {
	if _, err := tx.connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	tx.active = true
	if err := step(); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
