package kernel

import (
	"context"
	"database/sql"
)

// ContentAccess records bytes actually supplied or read, never an inferred full read.
type ContentAccess struct {
	RunID              RunID
	TaskID             TaskID
	TaskWorkRevision   Revision
	ContentID          ContentID
	ContentRevision    Revision
	Kind               string
	Offset, ByteLength int
	CreatedAt          UnixMillis
}

func recordContentAccess(ctx context.Context, c *sql.Conn, a ContentAccess) error {
	if a.RunID.zero() || a.ContentID.zero() || a.ContentRevision.Int64() < 1 || a.Offset < 0 || a.ByteLength < 0 || a.ByteLength > 65536 || (a.Kind != "read" && a.Kind != "supplied" && a.Kind != "selected") {
		return ErrInvalidValue
	}
	var n int
	err := c.QueryRowContext(ctx, `SELECT count(*) FROM runs r JOIN task_repository_bindings t ON t.task_id=r.task_id JOIN content_repository_bindings b JOIN project_content_revisions p ON p.id=b.content_id AND p.revision=b.content_revision WHERE r.id=? AND p.id=? AND p.revision=? AND p.project_id=r.project_id AND (b.repository_id=t.repository_id OR `+projectKnowledgeScopeSQL("p")+`)`, a.RunID.Bytes(), a.ContentID.Bytes(), a.ContentRevision.Int64()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	_, err = c.ExecContext(ctx, `INSERT OR IGNORE INTO content_accesses(run_id,content_id,content_revision,kind,byte_offset,byte_length,created_at_ms) VALUES(?,?,?,?,?,?,?)`, a.RunID.Bytes(), a.ContentID.Bytes(), a.ContentRevision.Int64(), a.Kind, a.Offset, a.ByteLength, a.CreatedAt.Int64())
	return err
}
func (store *Store) RecordContentAccessForAttempt(ctx context.Context, digest AttemptDigest, access ContentAccess) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return tx.Rollback(err)
	}
	if access.Kind != "read" {
		return tx.Rollback(ErrUnauthorized)
	}
	access.RunID = a.RunID
	if err = recordContentAccess(ctx, tx.connection, access); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
func listContentAccesses(ctx context.Context, c *sql.Conn, run RunID, selected bool) ([]ContentAccess, error) {
	query := `SELECT a.content_id,a.content_revision,a.kind,a.byte_offset,a.byte_length,a.created_at_ms,r.task_id,r.admitted_task_work_revision FROM content_accesses a JOIN runs r ON r.id=a.run_id WHERE a.run_id=? AND a.kind<>'snapshot'`
	if selected {
		query += ` AND kind='selected' ORDER BY a.byte_offset,a.content_id`
	} else {
		query += ` ORDER BY a.created_at_ms,a.content_id,a.content_revision,a.kind,a.byte_offset`
	}
	rows, err := c.QueryContext(ctx, query, run.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ContentAccess{}
	for rows.Next() {
		var b, task []byte
		var rev, at, work int64
		a := ContentAccess{RunID: run}
		if err := rows.Scan(&b, &rev, &a.Kind, &a.Offset, &a.ByteLength, &at, &task, &work); err != nil {
			return nil, err
		}
		a.ContentID, err = ContentIDFromBytes(b)
		if err != nil {
			return nil, err
		}
		a.ContentRevision, err = NewRevision(rev)
		if err != nil {
			return nil, err
		}
		a.CreatedAt, err = NewUnixMillis(at)
		if err != nil {
			return nil, err
		}
		a.TaskID, err = TaskIDFromBytes(task)
		if err != nil {
			return nil, err
		}
		a.TaskWorkRevision, err = NewRevision(work)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// FreezeKnowledgeContext retains even an empty first selection. Concurrent callers
// receive the first committed selection, preserving exact revisions across restarts.
func (store *Store) FreezeKnowledgeContext(ctx context.Context, run RunID, refs []ContentAccess, at UnixMillis) ([]ContentAccess, error) {
	if run.zero() || len(refs) > contentPageSize {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	var n int
	if err = tx.connection.QueryRowContext(ctx, `SELECT count(*) FROM content_accesses WHERE run_id=? AND kind='snapshot'`, run.Bytes()).Scan(&n); err != nil {
		return nil, tx.Rollback(err)
	}
	if n == 0 {
		if _, err = tx.connection.ExecContext(ctx, `INSERT INTO content_accesses(run_id,content_id,content_revision,kind,byte_offset,byte_length,created_at_ms) VALUES(?,NULL,NULL,'snapshot',0,0,?)`, run.Bytes(), at.Int64()); err != nil {
			return nil, tx.Rollback(err)
		}
		for _, a := range refs {
			a.RunID = run
			a.Kind = "selected"
			a.CreatedAt = at
			if err = recordContentAccess(ctx, tx.connection, a); err != nil {
				return nil, tx.Rollback(err)
			}
		}
	}
	result, err := listContentAccesses(ctx, tx.connection, run, true)
	if err != nil {
		return nil, tx.Rollback(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *Store) KnowledgeContext(ctx context.Context, run RunID) ([]ContentAccess, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Close()
	var n int
	if err = tx.connection.QueryRowContext(ctx, `SELECT count(*) FROM content_accesses WHERE run_id=? AND kind='snapshot'`, run.Bytes()).Scan(&n); err != nil {
		return nil, false, err
	}
	refs, err := listContentAccesses(ctx, tx.connection, run, true)
	return refs, n > 0, err
}

type ContentAccessPage struct {
	Items      []ContentAccess
	NextOffset int
}

func (store *Store) ListContentRevisionAccesses(ctx context.Context, project ProjectID, id ContentID, revision Revision, offset, limit int) (ContentAccessPage, error) {
	if project.zero() || id.zero() || revision.Int64() < 1 || offset < 0 || limit < 0 || limit > contentPageSize {
		return ContentAccessPage{}, ErrInvalidValue
	}
	if limit == 0 {
		limit = contentPageSize
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentAccessPage{}, err
	}
	defer tx.Close()
	content, err := contentByRevision(ctx, tx.connection, id, revision.Int64())
	if err != nil {
		return ContentAccessPage{}, err
	}
	if content.ProjectID != project {
		return ContentAccessPage{}, ErrUnauthorized
	}
	rows, err := tx.connection.QueryContext(ctx, `SELECT a.run_id,a.kind,a.byte_offset,a.byte_length,a.created_at_ms,r.task_id,r.admitted_task_work_revision FROM content_accesses a JOIN runs r ON r.id=a.run_id WHERE a.content_id=? AND a.content_revision=? AND a.kind IN ('supplied','read') ORDER BY a.created_at_ms DESC,a.run_id,a.kind,a.byte_offset LIMIT ? OFFSET ?`, id.Bytes(), revision.Int64(), limit+1, offset)
	if err != nil {
		return ContentAccessPage{}, err
	}
	defer rows.Close()
	result := ContentAccessPage{}
	for rows.Next() {
		var run, task []byte
		var at, work int64
		a := ContentAccess{ContentID: id, ContentRevision: revision}
		if err := rows.Scan(&run, &a.Kind, &a.Offset, &a.ByteLength, &at, &task, &work); err != nil {
			return ContentAccessPage{}, err
		}
		a.RunID, err = RunIDFromBytes(run)
		if err != nil {
			return ContentAccessPage{}, err
		}
		a.TaskID, err = TaskIDFromBytes(task)
		if err != nil {
			return ContentAccessPage{}, err
		}
		a.TaskWorkRevision, err = NewRevision(work)
		if err != nil {
			return ContentAccessPage{}, err
		}
		a.CreatedAt, err = NewUnixMillis(at)
		if err != nil {
			return ContentAccessPage{}, err
		}
		if len(result.Items) == limit {
			result.NextOffset = offset + limit
		} else {
			result.Items = append(result.Items, a)
		}
	}
	return result, rows.Err()
}

// RecordSuppliedContentForAttempt records one assembled context atomically after
// authenticating the live bearer; a failed assembly leaves no partial receipt.
func (store *Store) RecordSuppliedContentForAttempt(ctx context.Context, digest AttemptDigest, refs []ContentAccess, at UnixMillis) error {
	if len(refs) > contentPageSize+16 {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return tx.Rollback(err)
	}
	for _, ref := range refs {
		ref.RunID = a.RunID
		ref.Kind = "supplied"
		ref.CreatedAt = at
		if err := recordContentAccess(ctx, tx.connection, ref); err != nil {
			return tx.Rollback(err)
		}
	}
	return tx.Commit(ctx)
}

// ContentActivity is one recorded knowledge operation: a revision written, or
// bytes of one revision supplied to or read by a run. Reads are grouped per run
// and revision at their latest page; none establishes understanding.
type ContentActivity struct {
	Operation string // "revision", "supplied" or "read"
	Content   ContentRevision
	RunID     RunID
	AgentID   AgentID
	TaskID    TaskID
	At        UnixMillis
}

type ContentActivityPage struct {
	Items      []ContentActivity
	NextOffset int
}

// ListContentActivity reads existing revisions and access receipts, newest first,
// without recording anything. ponytail: neither table is indexed by time, so this
// scans the project's revisions and receipts; add a created_at index if polling grows slow.
func (store *Store) ListContentActivity(ctx context.Context, project ProjectID, offset, limit int) (ContentActivityPage, error) {
	if project.zero() || offset < 0 || limit < 1 || limit > contentPageSize {
		return ContentActivityPage{}, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentActivityPage{}, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT e.op,c.id,c.kind,c.revision,c.title,c.author,c.source_references,c.deprecated,e.run_id,e.agent_id,e.task_id,e.at FROM (
SELECT 'revision' AS op,id,revision,NULL AS run_id,NULL AS agent_id,NULL AS task_id,created_at_ms AS at FROM project_content_revisions WHERE project_id=?
UNION ALL
SELECT a.kind,a.content_id,a.content_revision,a.run_id,r.agent_id,r.task_id,max(a.created_at_ms) FROM content_accesses a JOIN runs r ON r.id=a.run_id WHERE r.project_id=? AND a.kind IN ('supplied','read') GROUP BY a.run_id,a.content_id,a.content_revision,a.kind
) e JOIN project_content_revisions c ON c.id=e.id AND c.revision=e.revision WHERE c.project_id=? ORDER BY e.at DESC,e.op,c.id,c.revision,e.run_id LIMIT ? OFFSET ?`, project.Bytes(), project.Bytes(), project.Bytes(), limit+1, offset)
	if err != nil {
		return ContentActivityPage{}, err
	}
	defer rows.Close()
	result := ContentActivityPage{Items: []ContentActivity{}}
	for rows.Next() {
		var id, run, agent, task []byte
		var revision, at int64
		a := ContentActivity{Content: ContentRevision{ProjectID: project}}
		if err := rows.Scan(&a.Operation, &id, &a.Content.Kind, &revision, &a.Content.Title, &a.Content.Author, &a.Content.SourceReferences, &a.Content.Deprecated, &run, &agent, &task, &at); err != nil {
			return ContentActivityPage{}, err
		}
		if len(result.Items) == limit {
			result.NextOffset = offset + limit
			break
		}
		if a.Content.ID, err = ContentIDFromBytes(id); err != nil {
			return ContentActivityPage{}, err
		}
		if a.Content.Revision, err = NewRevision(revision); err != nil {
			return ContentActivityPage{}, err
		}
		if a.At, err = NewUnixMillis(at); err != nil {
			return ContentActivityPage{}, err
		}
		if run != nil {
			if a.RunID, err = RunIDFromBytes(run); err != nil {
				return ContentActivityPage{}, err
			}
			if a.AgentID, err = AgentIDFromBytes(agent); err != nil {
				return ContentActivityPage{}, err
			}
			if a.TaskID, err = TaskIDFromBytes(task); err != nil {
				return ContentActivityPage{}, err
			}
		}
		result.Items = append(result.Items, a)
	}
	return result, rows.Err()
}
