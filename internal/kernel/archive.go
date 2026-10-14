package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"syscall"
)

// HistoryRetention is how long, in ms, a terminal run's content-access
// receipts, a settled task tree and a settled production record stay in the
// hot database before ArchiveHistory moves them.
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

// archivedTree lists, child first, the hot tables a batch (?1, a JSON object
// of its cutoff and its tasks' hex ids) moves out of: the settled production
// records, deleted before the publications they read, then the rows of each
// table that the batch's tasks own, and the order a digest reads them in.
// Each archive table mirrors its hot table's columns, then the generation, so
// readers union the two with one column list (history.table). A user_version bump that changes
// one of these tables changes its archive table too.
var archivedTree = []struct{ table, rows, order string }{
	{"production_records", settledRecords, "project_id, repository, kind, identity"},
	{"terminal_diagnostics", batchRuns, "run_id"},
	{"run_tokens", batchRuns, "run_id"},
	{"human_requests", batchRuns, "id"},
	{"terminal_sessions", batchRuns, "id"},
	{"resources", batchRuns, "id"},
	{"task_interventions", "task_id " + inBatch, "operation_id"},
	{"peer_questions", "source_task_id " + inBatch, "id"},
	{"publication_tasks", "task_id " + inBatch, "project_id, repository, pull_number, task_id"},
	{"task_prerequisites", "task_id " + inBatch, "task_id, upstream_task_id"},
	{"task_conflict_paths", "task_id " + inBatch, "task_id, path"},
	{"task_repository_bindings", "task_id " + inBatch, "task_id"},
	{"task_content_references", "task_id " + inBatch, "task_id, task_work_revision, content_id, content_revision"},
	{"mission_task_bindings", "task_id " + inBatch, "mission_id, task_id"},
	{"task_attachments", "task_id " + inBatch, "task_id, position"},
	{"task_automatic_events", "task_id " + inBatch, "task_id, task_revision, kind, at_ms"},
	{"intake_task_bindings", "task_id " + inBatch, "task_id"},
	{"changes", "task_id " + inBatch, "id"},
	{"runs", "task_id " + inBatch, "id"},
	{"tasks", "id " + inBatch, "id"},
}

func init() {
	for _, tree := range archivedTree {
		archiveSchema = append(archiveSchema,
			`CREATE TABLE IF NOT EXISTS archive.`+tree.table+` AS SELECT *, 0 AS generation FROM main.`+tree.table+` WHERE 0`,
			`CREATE INDEX IF NOT EXISTS archive.`+tree.table+`_generation ON `+tree.table+`(generation)`)
	}
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
	nextGeneration    = `SELECT max(COALESCE((SELECT max(generation) FROM main.archive_batches), 0), COALESCE((SELECT max(generation) FROM archive.content_accesses), 0), COALESCE((SELECT max(generation) FROM archive.batches), 0)) + 1`

	inBatch   = `IN (SELECT unhex(value) FROM json_each(?1, '$.tasks'))`
	batchRuns = `run_id IN (SELECT id FROM main.runs WHERE task_id ` + inBatch + `)`
	// A tree is settled when its task ended, and each of its runs, before the
	// cutoff (?1); no run still holds receipts (the receipt pass moves them
	// first); its Change, if any, is abandoned, since reclaim and send-back act
	// on a kept one; no pull request it published is open; no attachment of
	// it still holds data, which retention or an operator may yet remove and
	// the append-only archive could never drop; it pins no proposal revision,
	// since that pin is the proposal's acceptance (attachedProposalSQL), which
	// specialist reads take from the hot store; and it is not one of its
	// agent's three newest finished carriers (?2 is their title), from which
	// specialistSchedule reads the last review and the quiet streak.
	settledTasks = `SELECT t.id FROM main.tasks AS t WHERE t.status IN ('succeeded', 'failed', 'cancelled') AND t.completed_at_ms < ?1
		AND NOT EXISTS (SELECT 1 FROM main.runs AS r WHERE r.task_id = t.id AND (r.phase <> 'terminal' OR r.terminal_at_ms >= ?1 OR EXISTS (SELECT 1 FROM main.content_accesses AS a WHERE a.run_id = r.id)))
		AND NOT EXISTS (SELECT 1 FROM main.changes AS c WHERE c.task_id = t.id AND c.phase <> 'abandoned')
		AND NOT EXISTS (SELECT 1 FROM main.publication_tasks AS p JOIN main.production_records AS x ON x.project_id = p.project_id AND x.repository = p.repository AND x.kind = 'pull_request' AND x.identity = CAST(p.pull_number AS TEXT)
			WHERE p.task_id = t.id AND json_extract(x.document, '$.state') = 'open')
		AND NOT EXISTS (SELECT 1 FROM main.task_attachments AS f WHERE f.task_id = t.id AND f.data IS NOT NULL)
		AND NOT EXISTS (SELECT 1 FROM main.task_content_references AS r JOIN main.project_content_revisions AS p ON p.id = r.content_id AND p.revision = r.content_revision
			WHERE r.task_id = t.id AND ` + proposalRevisionSQL + `)
		AND NOT (t.title = ?2 AND t.id IN (SELECT c.id FROM main.tasks AS c WHERE c.assigned_agent_id = t.assigned_agent_id AND c.title = ?2 AND c.status NOT IN ('queued', 'running', 'blocked') ORDER BY c.created_at_ms DESC, c.id DESC LIMIT 3))
		ORDER BY t.id`
	// A production record is settled once observed before the cutoff when
	// nothing hot still reads it: a pull request, a check, a review operation
	// that names a pull request and neither runs nor gates (recovery reads
	// those), or a release delivery but its project's newest (releases and
	// overseer wakes read that); and no pull request it names is open,
	// observed since the cutoff or named by a hot publication. Other review
	// operations stay with their Change, and repository records stay. At
	// most 1024 move per batch.
	settledRecords = `(project_id, repository, kind, identity) IN (SELECT x.project_id, x.repository, x.kind, x.identity FROM main.production_records AS x
		WHERE x.observed_at_ms < json_extract(?1, '$.cutoff') AND CASE x.kind
			WHEN 'reviewer' THEN json_extract(x.document, '$.request.PullNumber') > 0 AND json_extract(x.document, '$.state') NOT IN ('running', 'gating')
			WHEN 'delivery' THEN x.identity LIKE 'release:%' AND x.identity <> (SELECT identity FROM main.production_records WHERE project_id = x.project_id AND kind = 'delivery' AND identity LIKE 'release:%' ORDER BY observed_at_ms DESC LIMIT 1)
			ELSE x.kind IN ('pull_request', 'check') END
		AND NOT EXISTS (SELECT 1 FROM json_each(CASE x.kind WHEN 'pull_request' THEN json_array(x.identity) WHEN 'reviewer' THEN json_array(json_extract(x.document, '$.request.PullNumber')) ELSE json_extract(x.document, '$.pull_requests') END) AS n
			WHERE EXISTS (SELECT 1 FROM main.publication_tasks AS p WHERE p.project_id = x.project_id AND p.repository = x.repository AND p.pull_number = CAST(n.value AS INTEGER))
			OR EXISTS (SELECT 1 FROM main.production_records AS h WHERE h.project_id = x.project_id AND h.repository = x.repository AND h.kind = 'pull_request' AND h.identity = CAST(n.value AS TEXT)
				AND (json_extract(h.document, '$.state') = 'open' OR h.observed_at_ms >= json_extract(?1, '$.cutoff'))))
		ORDER BY 1, 2, 3, 4 LIMIT 1024)`
	// Every pair of tasks a hot row joins, which must move together.
	taskLinks = `SELECT task_id, upstream_task_id FROM main.task_prerequisites
		UNION ALL SELECT p.task_id, r.task_id FROM main.task_prerequisites AS p JOIN main.runs AS r ON r.id = p.consumed_run_id
		UNION ALL SELECT task_id, parent_task_id FROM main.mission_task_bindings WHERE parent_task_id IS NOT NULL
		UNION ALL SELECT source_task_id, target_task_id FROM main.peer_questions
		UNION ALL SELECT i.task_id, r.task_id FROM main.task_interventions AS i JOIN main.runs AS r ON r.id IN (i.run_id, i.actor_run_id)
		UNION ALL SELECT task_id, successor_task_id FROM main.task_interventions WHERE successor_task_id IS NOT NULL
		UNION ALL SELECT p.task_id, c.task_id FROM main.publication_tasks AS p JOIN main.changes AS c ON c.id = p.change_id`
)

// history is the set of archive tables a read unions with their hot tables;
// nil reads hot rows only.
type history map[string]bool

// table is the named table as a read sees it: its hot rows and, once archived,
// those of the committed archive generations, with a trailing generation
// column (NULL when hot).
func (h history) table(name string) string {
	if !h[name] {
		return name
	}
	rows := `(SELECT *, NULL AS generation FROM main.` + name + ` UNION ALL SELECT * FROM archive.` + name + ` WHERE generation IN (SELECT generation FROM main.archive_batches))`
	if name != "production_records" {
		return rows
	}
	// A poll can observe an archived record again, so a read keeps one copy
	// of each: the hot one, else the newest generation's.
	return `(SELECT project_id, repository, kind, identity, visual_id, document, observed_at_ms, generation FROM (SELECT *, row_number() OVER (PARTITION BY project_id, repository, kind, identity ORDER BY generation IS NULL DESC, generation DESC) AS copy FROM ` + rows + `) WHERE copy = 1)`
}

// contentAccessesSQL is every content-access receipt with its run's identity:
// the hot ones joined to their run and, when archived, those of the committed
// archive generations. Uncommitted generations are never read.
func contentAccessesSQL(h history) string {
	if !h["content_accesses"] {
		return "(" + hotContentAccesses + ")"
	}
	return "(" + hotContentAccesses + ` UNION ALL SELECT ` + archivedAccessColumns + ` FROM archive.content_accesses WHERE generation IN (SELECT generation FROM main.archive_batches))`
}

func archiveDataSource(path, mode string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode}).String()
}

// beginHistoryRead begins a read that may cross into the archive and returns
// the archive tables it unions. Once anything is archived, the archive is
// attached read-only and each committed generation is checked against it
// first, so a missing or damaged archive fails this read closed and leaves
// hot reads alone.
// ponytail: a read that finds the archive file absent and then the first
// generation ever committed fails once; the next read attaches it.
func (store *Store) beginHistoryRead(ctx context.Context) (*readTx, history, error) {
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return nil, nil, err
	}
	_, attachErr := connection.ExecContext(ctx, `ATTACH ? AS archive`, archiveDataSource(store.archivePath, "ro"))
	tx := &readTx{connection: connection, attached: attachErr == nil}
	if _, err := connection.ExecContext(ctx, "BEGIN"); err != nil {
		if tx.attached {
			if _, detachErr := connection.ExecContext(context.Background(), "DETACH archive"); detachErr != nil {
				discardConnection(connection)
				return nil, nil, errors.Join(err, detachErr)
			}
		}
		releaseUncertainConnection(connection)
		return nil, nil, fmt.Errorf("begin sqlite read: %w", err)
	}
	tx.active = true
	var archived bool
	var tables history
	err = connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.archive_batches)`).Scan(&archived)
	if err == nil && archived {
		if err = attachErr; err == nil {
			err = store.verifyArchive(ctx, connection)
		}
		if err == nil {
			tables, err = archivedTables(ctx, connection)
		}
		if err != nil {
			err = fmt.Errorf("%w: history archive %s: %w", ErrCorruptState, store.archivePath, err)
		}
	}
	if err != nil {
		return nil, nil, errors.Join(err, tx.Close())
	}
	return tx, tables, nil
}

func archivedTables(ctx context.Context, connection *sql.Conn) (history, error) {
	rows, err := connection.QueryContext(ctx, `SELECT name FROM archive.sqlite_schema WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	tables := history{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		tables[name] = true
	}
	return tables, errors.Join(rows.Err(), rows.Close())
}

// verifyArchive fails unless the rows of every generation the hot database
// committed hash, in the archive, to the digest and count it committed. It
// skips the rehash while the archive file and the newest committed generation
// are as it, or the pass that wrote them, last proved them (archiveStamp).
func (store *Store) verifyArchive(ctx context.Context, connection *sql.Conn) error {
	stamp, stamped := archiveStampOf(ctx, connection, store.archivePath)
	if last := store.archiveVerified.Load(); stamped && last != nil && *last == stamp {
		return nil
	}
	if err := verifyGenerations(ctx, connection); err != nil {
		return err
	}
	if stamped {
		store.archiveVerified.Store(&stamp)
	}
	return nil
}

// archiveStamp is the archive file, and the newest generation the hot
// database committed, as a verification proved them.
// ponytail: an edit to the archive that keeps its size, mtime and inode is
// unseen until the file next changes; keep a verified high-water mark of
// generation digests if that matters.
type archiveStamp struct {
	size, modified int64
	inode          uint64
	newest         int64
}

func archiveStampOf(ctx context.Context, connection *sql.Conn, path string) (archiveStamp, bool) {
	var stamp archiveStamp
	info, err := os.Stat(path)
	if err != nil || connection.QueryRowContext(ctx, `SELECT COALESCE(max(generation), 0) FROM main.archive_batches`).Scan(&stamp.newest) != nil {
		return stamp, false
	}
	stamp.size, stamp.modified = info.Size(), info.ModTime().UnixNano()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.inode = stat.Ino
	}
	return stamp, true
}

func verifyGenerations(ctx context.Context, connection *sql.Conn) error {
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
	tables, err := archivedTables(ctx, connection)
	if err != nil {
		return err
	}
	generation := []string{archiveGeneration}
	for _, tree := range archivedTree {
		if tables[tree.table] {
			generation = append(generation, `SELECT * FROM archive.`+tree.table+` WHERE generation = ?1 ORDER BY `+tree.order)
		}
	}
	for _, b := range batches {
		digest, count, err := digestRows(ctx, connection, generation, b.generation)
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
// terminal before at - HistoryRetention into the archive, then one batch of
// settled task trees and production records (archiveTrees), each under a
// generation of its own, and reports how many rows it moved. The copy commits
// in the archive first and is proven against the hot rows by digest; only
// then does one hot transaction delete them and record the generation. The
// archive only grows: a generation the hot database does not record, from a
// crash between the two commits or one a restored backup predates, is ignored by readers and never reused, and its
// rows, still hot, are copied again under a new one. A recorded generation
// the archive lacks fails every pass and every crossing read closed. Only
// beginHistoryRead reads the archive.
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
	if err == nil {
		var trees int
		trees, err = archiveTrees(ctx, tx, at.Int64()-HistoryRetention, at.Int64())
		moved += trees
	}
	// The pass verified the archive first and proved each generation it
	// wrote, so the file as it leaves it is verified.
	if stamp, stamped := archiveStampOf(ctx, tx.connection, store.archivePath); err == nil && stamped {
		store.archiveVerified.Store(&stamp)
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
		if err := tx.store.verifyArchive(ctx, connection); err != nil {
			return fmt.Errorf("%w: history archive: %w", ErrCorruptState, err)
		}
		if err := connection.QueryRowContext(ctx, nextGeneration).Scan(&generation); err != nil {
			return err
		}
		var err error
		if digest, rows, err = digestRows(ctx, connection, []string{archiveSelection}, cutoff); err != nil || rows == 0 {
			return err
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO archive.content_accesses(generation, `+archivedAccessColumns+`) SELECT ?2, `+archivedAccessColumns+` FROM (`+archiveSelection+`)`, cutoff, generation); err != nil {
			return err
		}
		copied, count, err := digestRows(ctx, connection, []string{archiveGeneration}, generation)
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
		hot, count, err := digestRows(ctx, connection, []string{archiveSelection}, cutoff)
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

// archiveTrees moves one batch of settled task trees (settledTrees) and
// production records (settledRecords) under a generation of its own, by
// archiveBatch's two commits: the copy commits in the archive and is proven
// by digest first, then one hot transaction proves the rows unchanged,
// deletes them child first, records each task in archived_tasks and commits
// the generation. Foreign keys are checked at that
// commit, so a reference made to the batch in between refuses the pass and
// the next one picks again. It reports how many rows it moved.
func archiveTrees(ctx context.Context, tx *writeTx, cutoff, at int64) (int, error) {
	connection := tx.connection
	var hot, archived []string
	for _, tree := range archivedTree {
		hot = append(hot, `SELECT *, ?2 FROM main.`+tree.table+` WHERE `+tree.rows+` ORDER BY `+tree.order)
		archived = append(archived, `SELECT * FROM archive.`+tree.table+` WHERE generation = ?1 ORDER BY `+tree.order)
	}
	var batch string
	var generation int64
	var digest []byte
	rows := 0
	err := tx.within(ctx, func() error {
		var err error
		if batch, err = settledTrees(ctx, connection, cutoff); err != nil {
			return err
		}
		if err := connection.QueryRowContext(ctx, nextGeneration).Scan(&generation); err != nil {
			return err
		}
		if digest, rows, err = digestRows(ctx, connection, hot, batch, generation); err != nil || rows == 0 {
			return err
		}
		for _, tree := range archivedTree {
			if _, err := connection.ExecContext(ctx, `INSERT INTO archive.`+tree.table+` SELECT *, ?2 FROM main.`+tree.table+` WHERE `+tree.rows, batch, generation); err != nil {
				return err
			}
		}
		copied, count, err := digestRows(ctx, connection, archived, generation)
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
		current, count, err := digestRows(ctx, connection, hot, batch, generation)
		if err != nil {
			return err
		}
		var archived []byte
		if err := connection.QueryRowContext(ctx, `SELECT digest FROM archive.batches WHERE generation = ? AND rows = ?`, generation, rows).Scan(&archived); err != nil {
			return err
		}
		if count != rows || !bytes.Equal(current, digest) || !bytes.Equal(archived, digest) {
			return fmt.Errorf("%w: archived generation %d differs from its hot rows", ErrCorruptState, generation)
		}
		// changes.settled_run_id and runs.change_id refer to each other.
		if _, err := connection.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
		var deleted int64
		for _, tree := range archivedTree {
			result, err := connection.ExecContext(ctx, `DELETE FROM main.`+tree.table+` WHERE `+tree.rows, batch)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted += n
		}
		if deleted != int64(rows) {
			return fmt.Errorf("%w: deleted %d of %d archived rows", ErrCorruptState, deleted, rows)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO main.archive_batches(generation, rows, digest, archived_at_ms) VALUES (?, ?, ?, ?)`, generation, rows, digest, at); err != nil {
			return err
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO main.archived_tasks(task_id, generation) SELECT unhex(value), ?2 FROM json_each(?1, '$.tasks')`, batch, generation)
		return err
	})
	if err != nil {
		return 0, err
	}
	return rows, nil
}

// settledTrees picks the next batch, as a JSON object of the cutoff and the
// hex ids of its tasks: whole groups of settled trees (settledTasks) that hot
// rows join (taskLinks), in id order, up to 256 tasks unless the first group
// is larger. A group joined to any task that is not settled stays hot.
func settledTrees(ctx context.Context, connection *sql.Conn, cutoff int64) (string, error) {
	batch := []string{}
	encode := func() (string, error) {
		encoded, err := json.Marshal(map[string]any{"cutoff": cutoff, "tasks": batch})
		return string(encoded), err
	}
	var settled []string
	group := map[string]string{} // Each settled task's parent in its group.
	rows, err := connection.QueryContext(ctx, settledTasks, cutoff, overseerWakeTitle)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		settled = append(settled, string(id))
		group[string(id)] = string(id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	if len(settled) == 0 {
		return encode()
	}
	root := func(id string) string {
		for group[id] != id {
			group[id] = group[group[id]]
			id = group[id]
		}
		return id
	}
	var held []string
	if rows, err = connection.QueryContext(ctx, taskLinks); err != nil {
		return "", err
	}
	for rows.Next() {
		var from, to []byte
		if err := rows.Scan(&from, &to); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		_, fromSettled := group[string(from)]
		_, toSettled := group[string(to)]
		switch {
		case fromSettled && toSettled:
			group[root(string(from))] = root(string(to))
		case fromSettled:
			held = append(held, string(from))
		case toSettled:
			held = append(held, string(to))
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	members := map[string][]string{}
	for _, id := range settled {
		members[root(id)] = append(members[root(id)], id)
	}
	for _, id := range held {
		delete(members, root(id))
	}
	for _, id := range settled {
		tasks, ok := members[root(id)]
		if !ok {
			continue
		}
		delete(members, root(id))
		if len(batch) > 0 && len(batch)+len(tasks) > 256 {
			break
		}
		for _, task := range tasks {
			batch = append(batch, hex.EncodeToString([]byte(task)))
		}
	}
	return encode()
}

// digestRows hashes the rows each query returns, in order, and counts them.
func digestRows(ctx context.Context, connection *sql.Conn, queries []string, args ...any) ([]byte, int, error) {
	hash, count := sha256.New(), 0
	for _, query := range queries {
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
		for rows.Next() {
			if err := rows.Scan(targets...); err != nil {
				return nil, 0, errors.Join(err, rows.Close())
			}
			fmt.Fprintf(hash, "%#v\n", values)
			count++
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, 0, err
		}
	}
	return hash.Sum(nil), count, nil
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
