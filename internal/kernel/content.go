package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// ContentKind is a string so ordinary project content can grow without a
// closed Go enum or a daemon release.
type ContentKind string

const (
	ContentProcedure          ContentKind = "procedure"
	ContentAcceptanceScenario ContentKind = "acceptance_scenario"
)

type ContentRevision struct {
	ID                                           ContentID
	ProjectID                                    ProjectID
	Kind                                         ContentKind
	Revision, LatestRevision                     Revision
	Title, Description, Author, SourceReferences string
	ObjectFormat, Commit, Path                   string
	RepositoryDevice, RepositoryInode            int64
	Deprecated                                   bool
	CreatedAt                                    UnixMillis
}

type NewContent struct {
	ID                                                 ContentID
	ProjectID                                          ProjectID
	RepositoryID                                       RepositoryID
	Kind                                               ContentKind
	Title, Description, Body, Author, SourceReferences string
	ObjectFormat, Commit, Path                         string
	RepositoryDevice, RepositoryInode                  int64
}

type ContentPage struct {
	Items      []ContentRevision
	NextOffset int
}
type ContentBodyPage struct {
	ID         ContentID
	Revision   Revision
	Offset     int
	Body       string
	NextOffset int
	Complete   bool
}

type ContentEvidence struct {
	ID                                                               ContentEvidenceID
	ProjectID                                                        ProjectID
	ContentID                                                        ContentID
	ContentRevision                                                  Revision
	TestedSource, Environment, Result, Location, Evaluator, Judgment string
	CreatedAt                                                        UnixMillis
}

type NewContentEvidence struct {
	ID                                                               ContentEvidenceID
	ProjectID                                                        ProjectID
	ContentID                                                        ContentID
	ContentRevision                                                  Revision
	TestedSource, Environment, Result, Location, Evaluator, Judgment string
}

type ContentEvidencePage struct {
	Items      []ContentEvidence
	NextOffset int
}

type TaskContentReference struct {
	TaskID           TaskID
	ProjectID        ProjectID
	TaskWorkRevision Revision
	ContentID        ContentID
	ContentRevision  Revision
	AttachedAt       UnixMillis
}

const contentPageSize = 64

func validateContent(spec NewContent) error {
	if spec.ID.zero() || spec.ProjectID.zero() || byteLen(string(spec.Kind)) < 1 || byteLen(string(spec.Kind)) > 64 || byteLen(spec.Title) < 1 || byteLen(spec.Title) > 1024 || byteLen(spec.Description) > 4096 || spec.Body != "" || byteLen(spec.Author) < 1 || byteLen(spec.Author) > 256 || byteLen(spec.SourceReferences) > 32768 || !utf8.ValidString(string(spec.Kind)) || !utf8.ValidString(spec.Title) || !utf8.ValidString(spec.Description) || !utf8.ValidString(spec.Author) || !utf8.ValidString(spec.SourceReferences) || spec.Commit == "" || spec.ObjectFormat == "" || spec.Path == "" || spec.RepositoryDevice < 0 || spec.RepositoryInode <= 0 {
		return fmt.Errorf("%w: invalid project content", ErrInvalidValue)
	}
	return nil
}

func (store *Store) CreateContent(ctx context.Context, spec NewContent, at UnixMillis) (ContentRevision, error) {
	if err := validateContent(spec); err != nil {
		return ContentRevision{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	return createContentTx(ctx, tx, spec, at, nil)
}

func createContentTx(ctx context.Context, tx *writeTx, spec NewContent, at UnixMillis, authorTask *TaskID) (ContentRevision, error) {
	if err := validateContent(spec); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}

	var project []byte
	if err := tx.connection.QueryRowContext(ctx, "SELECT id FROM projects WHERE id = ?", spec.ProjectID.Bytes()).Scan(&project); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if existing, err := contentByRevision(ctx, tx.connection, spec.ID, 1); err == nil {
		if contentMatches(existing, spec, false) {
			if err := tx.Rollback(nil); err != nil {
				return ContentRevision{}, err
			}
			return existing, nil
		}
		return ContentRevision{}, tx.Rollback(ErrConflict)
	} else if err != ErrNotFound {
		return ContentRevision{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, object_format, commit_oid, path, repository_dev, repository_inode, deprecated, created_at_ms) VALUES(?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), string(spec.Kind), spec.Title, spec.Description, spec.Body, spec.Author, spec.SourceReferences, nullableString(spec.ObjectFormat), nullableString(spec.Commit), nullableString(spec.Path), spec.RepositoryDevice, spec.RepositoryInode, at.Int64()); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	var repository ProjectRepository
	var found bool
	var err error
	if authorTask != nil {
		repository, found, err = taskRepository(ctx, tx.connection, *authorTask)
		if err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		if found && (repository.ProjectID != spec.ProjectID || !spec.RepositoryID.zero() && spec.RepositoryID != repository.ID) {
			return ContentRevision{}, tx.Rollback(ErrUnauthorized)
		}
	}
	if !found {
		repository, err = resolveTaskRepository(ctx, tx.connection, spec.ProjectID, spec.RepositoryID)
		if err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO content_repository_bindings(content_id, content_revision, repository_id) VALUES(?, 1, ?)`, spec.ID.Bytes(), repository.ID.Bytes()); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	result, err := contentByRevision(ctx, tx.connection, spec.ID, 1)
	if err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if err := validateRepositoryBindings(ctx, tx.connection); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContentRevision{}, err
	}
	return result, nil
}

func (store *Store) ReviseContent(ctx context.Context, expected Revision, spec NewContent, at UnixMillis) (ContentRevision, error) {
	return store.reviseContent(ctx, expected, spec, false, at)
}

func (store *Store) reviseContent(ctx context.Context, expected Revision, spec NewContent, deprecated bool, at UnixMillis) (ContentRevision, error) {
	if err := validateContent(spec); err != nil {
		return ContentRevision{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	return reviseContentTx(ctx, tx, expected, spec, deprecated, at)
}

func reviseContentTx(ctx context.Context, tx *writeTx, expected Revision, spec NewContent, deprecated bool, at UnixMillis) (ContentRevision, error) {
	if err := validateContent(spec); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if expected.Int64() < 1 {
		return ContentRevision{}, tx.Rollback(ErrInvalidValue)
	}

	var project []byte
	var current int64
	if err := tx.connection.QueryRowContext(ctx, "SELECT project_id, MAX(revision) FROM project_content_revisions WHERE id = ? GROUP BY project_id", spec.ID.Bytes()).Scan(&project, &current); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return ContentRevision{}, tx.Rollback(err)
	}
	if string(project) != string(spec.ProjectID.Bytes()) {
		return ContentRevision{}, tx.Rollback(ErrConflict)
	}
	if current != expected.Int64() {
		// A retry observes its immutable inserted revision, even after later edits.
		if current > expected.Int64() {
			if existing, e := contentByRevision(ctx, tx.connection, spec.ID, expected.Int64()+1); e == nil && contentMatches(existing, spec, deprecated) {
				if e := tx.Rollback(nil); e != nil {
					return ContentRevision{}, e
				}
				return existing, nil
			}
		}
		return ContentRevision{}, tx.Rollback(ErrConflict)
	}
	next := current + 1
	deprecatedValue := 0
	if deprecated {
		deprecatedValue = 1
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, object_format, commit_oid, path, repository_dev, repository_inode, deprecated, created_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), string(spec.Kind), next, spec.Title, spec.Description, spec.Body, spec.Author, spec.SourceReferences, nullableString(spec.ObjectFormat), nullableString(spec.Commit), nullableString(spec.Path), spec.RepositoryDevice, spec.RepositoryInode, deprecatedValue, at.Int64()); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	var repositoryID []byte
	if err := tx.connection.QueryRowContext(ctx, `SELECT repository_id FROM content_repository_bindings WHERE content_id = ? AND content_revision = ?`, spec.ID.Bytes(), current).Scan(&repositoryID); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO content_repository_bindings(content_id, content_revision, repository_id) VALUES(?, ?, ?)`, spec.ID.Bytes(), next, repositoryID); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	result, err := contentByRevision(ctx, tx.connection, spec.ID, next)
	if err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if err := validateRepositoryBindings(ctx, tx.connection); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContentRevision{}, err
	}
	return result, nil
}

func (store *Store) DeprecateContent(ctx context.Context, id ContentID, project ProjectID, expected Revision, author string, at UnixMillis) (ContentRevision, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	return deprecateContentTx(ctx, tx, id, project, expected, author, at)
}

func deprecateContentTx(ctx context.Context, tx *writeTx, id ContentID, project ProjectID, expected Revision, author string, at UnixMillis) (ContentRevision, error) {
	current, err := contentByRevision(ctx, tx.connection, id, expected.Int64())
	if err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if current.ProjectID != project {
		return ContentRevision{}, tx.Rollback(ErrConflict)
	}
	if current.Commit == "" {
		var latest int64
		if err := tx.connection.QueryRowContext(ctx, `SELECT MAX(revision) FROM project_content_revisions WHERE id = ?`, id.Bytes()).Scan(&latest); err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		if latest != expected.Int64() {
			if latest > expected.Int64() {
				existing, readErr := contentByRevision(ctx, tx.connection, id, expected.Int64()+1)
				if readErr == nil && existing.Deprecated && existing.Author == author {
					if rollbackErr := tx.Rollback(nil); rollbackErr != nil {
						return ContentRevision{}, rollbackErr
					}
					return existing, nil
				}
			}
			return ContentRevision{}, tx.Rollback(ErrConflict)
		}
		next := latest + 1
		_, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms) SELECT id, project_id, kind, ?, title, description, body, ?, source_references, 1, ? FROM project_content_revisions WHERE id = ? AND revision = ? AND commit_oid IS NULL`, next, author, at.Int64(), id.Bytes(), expected.Int64())
		if err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		var repositoryID []byte
		if err := tx.connection.QueryRowContext(ctx, `SELECT repository_id FROM content_repository_bindings WHERE content_id = ? AND content_revision = ?`, id.Bytes(), expected.Int64()).Scan(&repositoryID); err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO content_repository_bindings(content_id, content_revision, repository_id) VALUES(?, ?, ?)`, id.Bytes(), next, repositoryID); err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		result, err := contentByRevision(ctx, tx.connection, id, next)
		if err != nil {
			return ContentRevision{}, tx.Rollback(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ContentRevision{}, err
		}
		return result, nil
	}
	return reviseContentTx(ctx, tx, expected, NewContent{ID: id, ProjectID: project, Kind: current.Kind, Title: current.Title, Description: current.Description, Author: author, SourceReferences: current.SourceReferences, ObjectFormat: current.ObjectFormat, Commit: current.Commit, Path: current.Path, RepositoryDevice: current.RepositoryDevice, RepositoryInode: current.RepositoryInode}, true, at)
}

func (store *Store) Content(ctx context.Context, id ContentID, revision int64) (ContentRevision, error) {
	if id.zero() || revision < 1 {
		return ContentRevision{}, fmt.Errorf("%w: invalid content revision", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	return contentMetadataByRevision(ctx, tx.connection, id, revision)
}

// LegacyContent returns the body only for a pre-Git revision awaiting export.
func (store *Store) LegacyContent(ctx context.Context, id ContentID, revision int64) (ContentRevision, string, error) {
	if id.zero() || revision < 1 {
		return ContentRevision{}, "", fmt.Errorf("%w: invalid content revision", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentRevision{}, "", err
	}
	defer tx.Close()
	content, err := contentMetadataByRevision(ctx, tx.connection, id, revision)
	if err != nil {
		return ContentRevision{}, "", err
	}
	if content.Commit != "" {
		return ContentRevision{}, "", ErrConflict
	}
	var body string
	if err := tx.connection.QueryRowContext(ctx, `SELECT body FROM project_content_revisions WHERE id = ? AND revision = ? AND commit_oid IS NULL`, id.Bytes(), revision).Scan(&body); err != nil {
		return ContentRevision{}, "", err
	}
	return content, body, nil
}

// CompleteContentExport installs an immutable Git pin and retires the legacy
// SQLite body. The body comparison makes replay safe without another journal.
func (store *Store) CompleteContentExport(ctx context.Context, id ContentID, revision int64, body, objectFormat, commit, path string, repositoryID RepositoryID, repositoryDevice, repositoryInode int64) error {
	if id.zero() || revision < 1 || repositoryID.zero() || objectFormat == "" || commit == "" || path == "" || repositoryDevice < 0 || repositoryInode <= 0 {
		return fmt.Errorf("%w: invalid content export", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	result, err := tx.connection.ExecContext(ctx, `UPDATE project_content_revisions SET body = '', object_format = ?, commit_oid = ?, path = ?, repository_dev = ?, repository_inode = ? WHERE id = ? AND revision = ? AND body = ? AND commit_oid IS NULL`, objectFormat, commit, path, repositoryDevice, repositoryInode, id.Bytes(), revision, body)
	if err != nil {
		return tx.Rollback(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return tx.Rollback(err)
	}
	if changed != 1 {
		var existingFormat, existingCommit, existingPath, existingBody string
		var existingDevice, existingInode int64
		err = tx.connection.QueryRowContext(ctx, `SELECT COALESCE(object_format, ''), COALESCE(commit_oid, ''), COALESCE(path, ''), body, COALESCE(repository_dev, -1), COALESCE(repository_inode, -1) FROM project_content_revisions WHERE id = ? AND revision = ?`, id.Bytes(), revision).Scan(&existingFormat, &existingCommit, &existingPath, &existingBody, &existingDevice, &existingInode)
		if err != nil || existingFormat != objectFormat || existingCommit != commit || existingPath != path || existingBody != "" || existingDevice != repositoryDevice || existingInode != repositoryInode {
			if err == nil {
				err = ErrConflict
			}
			return tx.Rollback(err)
		}
	}
	content, err := contentByRevision(ctx, tx.connection, id, revision)
	if err != nil {
		return tx.Rollback(err)
	}
	repository, found, err := repositoryByID(ctx, tx.connection, repositoryID)
	if err != nil || !found || repository.ProjectID != content.ProjectID {
		if err == nil {
			err = ErrConflict
		}
		return tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT OR IGNORE INTO content_repository_bindings(content_id, content_revision, repository_id) VALUES(?, ?, ?)`, id.Bytes(), revision, repositoryID.Bytes()); err != nil {
		return tx.Rollback(err)
	}
	if err := validateRepositoryBindings(ctx, tx.connection); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

func contentMetadataByRevision(ctx context.Context, connection *sql.Conn, id ContentID, revision int64) (ContentRevision, error) {
	return scanContent(connection.QueryRowContext(ctx, `SELECT id, project_id, kind, revision, title, description, '', author, source_references, object_format, commit_oid, path, repository_dev, repository_inode, deprecated, created_at_ms, (SELECT MAX(revision) FROM project_content_revisions WHERE id = ?) FROM project_content_revisions WHERE id = ? AND revision = ?`, id.Bytes(), id.Bytes(), revision))
}

func (store *Store) ListContent(ctx context.Context, project ProjectID, kind ContentKind, offset, limit int) (ContentPage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer tx.Close()
	return listContentOnConnection(ctx, tx.connection, project, kind, offset, limit)
}

func listContentOnConnection(ctx context.Context, connection *sql.Conn, project ProjectID, kind ContentKind, offset, limit int) (ContentPage, error) {
	if project.zero() || offset < 0 || limit < 0 || limit > contentPageSize {
		return ContentPage{}, fmt.Errorf("%w: invalid content page", ErrInvalidValue)
	}
	if limit == 0 {
		limit = contentPageSize
	}
	// Lists are metadata-only. Bodies live in the pinned Git source.
	query := `SELECT c.id, c.project_id, c.kind, c.revision, c.title, c.description, '', c.author, c.source_references, c.object_format, c.commit_oid, c.path, c.repository_dev, c.repository_inode, c.deprecated, c.created_at_ms, c.revision FROM project_content_revisions c JOIN (SELECT id, MAX(revision) revision FROM project_content_revisions WHERE project_id = ? GROUP BY id) latest ON latest.id = c.id AND latest.revision = c.revision WHERE c.project_id = ?`
	args := []any{project.Bytes(), project.Bytes()}
	if kind != "" {
		query += " AND c.kind = ?"
		args = append(args, string(kind))
	}
	query += " ORDER BY c.id LIMIT ? OFFSET ?"
	args = append(args, limit+1, offset)
	rows, err := connection.QueryContext(ctx, query, args...)
	if err != nil {
		return ContentPage{}, err
	}
	defer rows.Close()
	result := ContentPage{}
	for rows.Next() {
		item, err := scanContent(rows)
		if err != nil {
			return ContentPage{}, err
		}
		if len(result.Items) == limit {
			result.NextOffset = offset + limit
		} else {
			result.Items = append(result.Items, item)
		}
	}
	return result, rows.Err()
}

func contentByRevision(ctx context.Context, connection *sql.Conn, id ContentID, revision int64) (ContentRevision, error) {
	return contentMetadataByRevision(ctx, connection, id, revision)
}

func scanContent(scanner rowScanner) (ContentRevision, error) {
	var rawID, rawProject []byte
	var kind, title, description, body, author, refs string
	var objectFormat, commit_oid, path sql.NullString
	var revision, deprecated, created, latest int64
	var repositoryDevice, repositoryInode sql.NullInt64
	if err := scanner.Scan(&rawID, &rawProject, &kind, &revision, &title, &description, &body, &author, &refs, &objectFormat, &commit_oid, &path, &repositoryDevice, &repositoryInode, &deprecated, &created, &latest); err != nil {
		if err == sql.ErrNoRows {
			return ContentRevision{}, ErrNotFound
		}
		return ContentRevision{}, err
	}
	id, err := ContentIDFromBytes(rawID)
	if err != nil {
		return ContentRevision{}, err
	}
	project, err := ProjectIDFromBytes(rawProject)
	if err != nil {
		return ContentRevision{}, err
	}
	rev, err := NewRevision(revision)
	if err != nil {
		return ContentRevision{}, err
	}
	timestamp, err := NewUnixMillis(created)
	if err != nil {
		return ContentRevision{}, err
	}
	latestRevision, err := NewRevision(latest)
	if err != nil || latest < revision {
		return ContentRevision{}, ErrCorruptState
	}
	return ContentRevision{ID: id, ProjectID: project, Kind: ContentKind(kind), Revision: rev, LatestRevision: latestRevision, Title: title, Description: description, Author: author, SourceReferences: refs, ObjectFormat: objectFormat.String, Commit: commit_oid.String, Path: path.String, RepositoryDevice: repositoryDevice.Int64, RepositoryInode: repositoryInode.Int64, Deprecated: deprecated != 0, CreatedAt: timestamp}, nil
}

func contentMatches(existing ContentRevision, spec NewContent, deprecated bool) bool {
	return existing.ProjectID == spec.ProjectID && existing.Kind == spec.Kind && existing.Title == spec.Title && existing.Description == spec.Description && existing.Author == spec.Author && existing.SourceReferences == spec.SourceReferences && existing.ObjectFormat == spec.ObjectFormat && existing.Commit == spec.Commit && existing.Path == spec.Path && existing.RepositoryDevice == spec.RepositoryDevice && existing.RepositoryInode == spec.RepositoryInode && existing.Deprecated == deprecated
}

func (store *Store) beginContentAttemptWrite(ctx context.Context, digest AttemptDigest, at UnixMillis) (*writeTx, AttemptAuthority, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return nil, AttemptAuthority{}, err
	}
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err == nil && authority.Role != RoleWorker && authority.Role != RoleOrchestrator {
		err = ErrUnauthorized
	}
	if err != nil {
		err = tx.Rollback(err)
		tx.Close()
		return nil, AttemptAuthority{}, err
	}
	return tx, authority, nil
}

func contentProvenance(authority AttemptAuthority) string {
	return fmt.Sprintf("run:%s agent:%s role:%s", authority.RunID, authority.AgentID, authority.Role)
}

func evidenceProvenance(authority AttemptAuthority) string { return contentProvenance(authority) }

func (store *Store) AttachContentToTask(ctx context.Context, task TaskID, project ProjectID, content ContentID, revision Revision, at UnixMillis) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	var taskWorkRevision int64
	if err := tx.connection.QueryRowContext(ctx, "SELECT work_revision FROM tasks WHERE id = ? AND project_id = ?", task.Bytes(), project.Bytes()).Scan(&taskWorkRevision); err != nil {
		if err == sql.ErrNoRows {
			err = ErrConflict
		}
		return tx.Rollback(err)
	}
	if err := attachContentTx(ctx, tx, task, project, taskWorkRevision, content, revision, at); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// AttachContentForOrchestrator lets a live orchestrator pin a selected
// revision on a queued task in the same project. The target work revision is
// captured in the reference so later task revisions cannot retarget history.
func (store *Store) AttachContentForOrchestrator(ctx context.Context, digest AttemptDigest, task TaskID, content ContentID, revision Revision, at UnixMillis) error {
	tx, authority, err := store.beginContentAttemptWrite(ctx, digest, at)
	if err != nil {
		return err
	}
	defer tx.Close()
	if authority.Role != RoleOrchestrator {
		return tx.Rollback(ErrUnauthorized)
	}
	var taskProject []byte
	var taskWorkRevision int64
	var taskStatus string
	if err := tx.connection.QueryRowContext(ctx, `SELECT project_id, work_revision, status FROM tasks WHERE id = ?`, task.Bytes()).Scan(&taskProject, &taskWorkRevision, &taskStatus); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return tx.Rollback(err)
	}
	if string(taskProject) != string(authority.ProjectID.Bytes()) || taskStatus != TaskQueued.String() {
		return tx.Rollback(ErrUnauthorized)
	}
	if err := attachContentTx(ctx, tx, task, authority.ProjectID, taskWorkRevision, content, revision, at); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

func attachContentTx(ctx context.Context, tx *writeTx, task TaskID, project ProjectID, taskWorkRevision int64, content ContentID, revision Revision, at UnixMillis) error {
	var contentProject []byte
	if err := tx.connection.QueryRowContext(ctx, "SELECT project_id FROM project_content_revisions WHERE id = ? AND revision = ?", content.Bytes(), revision.Int64()).Scan(&contentProject); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return err
	}
	if string(contentProject) != string(project.Bytes()) {
		return ErrConflict
	}
	var existingProject []byte
	err := tx.connection.QueryRowContext(ctx, "SELECT project_id FROM task_content_references WHERE task_id = ? AND task_work_revision = ? AND content_id = ? AND content_revision = ?", task.Bytes(), taskWorkRevision, content.Bytes(), revision.Int64()).Scan(&existingProject)
	if err == nil {
		if string(existingProject) != string(project.Bytes()) {
			return ErrCorruptState
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	var references int
	if err := tx.connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_content_references WHERE task_id = ? AND task_work_revision = ?`, task.Bytes(), taskWorkRevision).Scan(&references); err != nil {
		return err
	}
	if references >= contentPageSize {
		return fmt.Errorf("%w: too many task content references", ErrInvalidValue)
	}
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO task_content_references(task_id, project_id, task_work_revision, content_id, content_revision, attached_at_ms) VALUES(?, ?, ?, ?, ?, ?)`, task.Bytes(), project.Bytes(), taskWorkRevision, content.Bytes(), revision.Int64(), at.Int64())
	return err
}

// ContentForAttempt is the shared non-browser read path. The project is
// derived from the live bearer, so a caller cannot browse another project.
func (store *Store) ContentForAttempt(ctx context.Context, digest AttemptDigest, id ContentID, revision int64) (ContentRevision, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return ContentRevision{}, err
	}
	content, err := contentMetadataByRevision(ctx, tx.connection, id, revision)
	if err != nil {
		return ContentRevision{}, err
	}
	if content.ProjectID != authority.ProjectID {
		return ContentRevision{}, ErrUnauthorized
	}
	return content, nil
}

// CreateContentForAttempt keeps ordinary content writes under the same live
// attempt/project authority as the other worker APIs.
func (store *Store) CreateContentForAttempt(ctx context.Context, digest AttemptDigest, spec NewContent, at UnixMillis) (ContentRevision, error) {
	tx, authority, err := store.beginContentAttemptWrite(ctx, digest, at)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	if spec.ProjectID != authority.ProjectID {
		return ContentRevision{}, tx.Rollback(ErrUnauthorized)
	}
	spec.Author = contentProvenance(authority)
	return createContentTx(ctx, tx, spec, at, &authority.TaskID)
}

func (store *Store) ListContentForAttempt(ctx context.Context, digest AttemptDigest, kind ContentKind, offset, limit int) (ContentPage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer tx.Close()
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return ContentPage{}, err
	}
	return listContentOnConnection(ctx, tx.connection, authority.ProjectID, kind, offset, limit)
}

func (store *Store) ReviseContentForAttempt(ctx context.Context, digest AttemptDigest, expected Revision, spec NewContent, at UnixMillis) (ContentRevision, error) {
	tx, authority, err := store.beginContentAttemptWrite(ctx, digest, at)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	if spec.ProjectID != authority.ProjectID {
		return ContentRevision{}, tx.Rollback(ErrUnauthorized)
	}
	spec.Author = contentProvenance(authority)
	return reviseContentTx(ctx, tx, expected, spec, false, at)
}

func (store *Store) DeprecateContentForAttempt(ctx context.Context, digest AttemptDigest, id ContentID, expected Revision, at UnixMillis) (ContentRevision, error) {
	tx, authority, err := store.beginContentAttemptWrite(ctx, digest, at)
	if err != nil {
		return ContentRevision{}, err
	}
	defer tx.Close()
	return deprecateContentTx(ctx, tx, id, authority.ProjectID, expected, contentProvenance(authority), at)
}

func (store *Store) CreateContentEvidenceForAttempt(ctx context.Context, digest AttemptDigest, spec NewContentEvidence, at UnixMillis) (ContentEvidence, error) {
	tx, authority, err := store.beginContentAttemptWrite(ctx, digest, at)
	if err != nil {
		return ContentEvidence{}, err
	}
	defer tx.Close()
	if spec.ProjectID != authority.ProjectID {
		return ContentEvidence{}, tx.Rollback(ErrUnauthorized)
	}
	spec.Evaluator = evidenceProvenance(authority)
	return createContentEvidenceTx(ctx, tx, spec, at)
}

func validateContentEvidence(spec NewContentEvidence) error {
	if spec.ID.zero() || spec.ProjectID.zero() || spec.ContentID.zero() || spec.ContentRevision.Int64() < 1 || byteLen(spec.TestedSource) < 1 || byteLen(spec.TestedSource) > 4096 || byteLen(spec.Environment) > 4096 || (spec.Result != "passed" && spec.Result != "failed" && spec.Result != "incomplete" && spec.Result != "not_run") || byteLen(spec.Location) > 4096 || byteLen(spec.Evaluator) < 1 || byteLen(spec.Evaluator) > 256 || byteLen(spec.Judgment) > 8192 {
		return fmt.Errorf("%w: invalid content evidence", ErrInvalidValue)
	}
	if !utf8.ValidString(spec.TestedSource) || !utf8.ValidString(spec.Environment) || !utf8.ValidString(spec.Location) || !utf8.ValidString(spec.Evaluator) || !utf8.ValidString(spec.Judgment) {
		return fmt.Errorf("%w: invalid content evidence", ErrInvalidValue)
	}
	return nil
}

func (store *Store) CreateContentEvidence(ctx context.Context, spec NewContentEvidence, at UnixMillis) (ContentEvidence, error) {
	if err := validateContentEvidence(spec); err != nil {
		return ContentEvidence{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ContentEvidence{}, err
	}
	defer tx.Close()
	return createContentEvidenceTx(ctx, tx, spec, at)
}

func createContentEvidenceTx(ctx context.Context, tx *writeTx, spec NewContentEvidence, at UnixMillis) (ContentEvidence, error) {
	if err := validateContentEvidence(spec); err != nil {
		return ContentEvidence{}, tx.Rollback(err)
	}

	var project []byte
	if err := tx.connection.QueryRowContext(ctx, "SELECT project_id FROM project_content_revisions WHERE id = ? AND revision = ?", spec.ContentID.Bytes(), spec.ContentRevision.Int64()).Scan(&project); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return ContentEvidence{}, tx.Rollback(err)
	}
	if string(project) != string(spec.ProjectID.Bytes()) {
		return ContentEvidence{}, tx.Rollback(ErrConflict)
	}
	var existing ContentEvidence
	var existingContentID, existingProjectID []byte
	var existingRevision, created int64
	err := tx.connection.QueryRowContext(ctx, `SELECT project_id, content_id, content_revision, tested_source, environment, result, location, evaluator, judgment, created_at_ms FROM project_content_evidence WHERE id = ?`, spec.ID.Bytes()).Scan(&existingProjectID, &existingContentID, &existingRevision, &existing.TestedSource, &existing.Environment, &existing.Result, &existing.Location, &existing.Evaluator, &existing.Judgment, &created)
	if err == nil {
		existingID, idErr := ContentIDFromBytes(existingContentID)
		existingRevisionValue, revisionErr := NewRevision(existingRevision)
		if idErr != nil || revisionErr != nil {
			return ContentEvidence{}, tx.Rollback(ErrCorruptState)
		}
		existing.ContentID, existing.ContentRevision = existingID, existingRevisionValue
		if string(existingProjectID) == string(spec.ProjectID.Bytes()) && existing.ContentID == spec.ContentID && existing.ContentRevision.Int64() == spec.ContentRevision.Int64() && existing.TestedSource == spec.TestedSource && existing.Environment == spec.Environment && existing.Result == spec.Result && existing.Location == spec.Location && existing.Evaluator == spec.Evaluator && existing.Judgment == spec.Judgment {
			if err := tx.Rollback(nil); err != nil {
				return ContentEvidence{}, err
			}
			existing.ID, existing.ProjectID = spec.ID, spec.ProjectID
			existing.CreatedAt, _ = NewUnixMillis(created)
			return existing, nil
		}
		return ContentEvidence{}, tx.Rollback(ErrConflict)
	}
	if err != sql.ErrNoRows {
		return ContentEvidence{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_evidence(id, project_id, content_id, content_revision, tested_source, environment, result, location, evaluator, judgment, created_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), spec.ContentID.Bytes(), spec.ContentRevision.Int64(), spec.TestedSource, spec.Environment, spec.Result, spec.Location, spec.Evaluator, spec.Judgment, at.Int64()); err != nil {
		return ContentEvidence{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContentEvidence{}, err
	}
	return ContentEvidence{ID: spec.ID, ProjectID: spec.ProjectID, ContentID: spec.ContentID, ContentRevision: spec.ContentRevision, TestedSource: spec.TestedSource, Environment: spec.Environment, Result: spec.Result, Location: spec.Location, Evaluator: spec.Evaluator, Judgment: spec.Judgment, CreatedAt: at}, nil
}

func (store *Store) ListContentEvidence(ctx context.Context, project ProjectID, content ContentID, revision Revision, offset, limit int) (ContentEvidencePage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentEvidencePage{}, err
	}
	defer tx.Close()
	return listContentEvidenceOnConnection(ctx, tx.connection, project, content, revision, offset, limit)
}

func listContentEvidenceOnConnection(ctx context.Context, connection *sql.Conn, project ProjectID, content ContentID, revision Revision, offset, limit int) (ContentEvidencePage, error) {
	if project.zero() || content.zero() || offset < 0 || limit < 0 || limit > contentPageSize {
		return ContentEvidencePage{}, fmt.Errorf("%w: invalid evidence page", ErrInvalidValue)
	}
	if limit == 0 {
		limit = contentPageSize
	}
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, content_id, content_revision, tested_source, environment, result, location, evaluator, judgment, created_at_ms FROM project_content_evidence WHERE project_id = ? AND content_id = ? AND content_revision = ? ORDER BY id LIMIT ? OFFSET ?`, project.Bytes(), content.Bytes(), revision.Int64(), limit+1, offset)
	if err != nil {
		return ContentEvidencePage{}, err
	}
	defer rows.Close()
	result := ContentEvidencePage{}
	for rows.Next() {
		item, err := scanContentEvidence(rows)
		if err != nil {
			return ContentEvidencePage{}, err
		}
		if len(result.Items) == limit {
			result.NextOffset = offset + limit
		} else {
			result.Items = append(result.Items, item)
		}
	}
	return result, rows.Err()
}

func scanContentEvidence(scanner rowScanner) (ContentEvidence, error) {
	var rawID, rawProject, rawContent []byte
	var revision, created int64
	var tested, environment, result, location, evaluator, judgment string
	if err := scanner.Scan(&rawID, &rawProject, &rawContent, &revision, &tested, &environment, &result, &location, &evaluator, &judgment, &created); err != nil {
		if err == sql.ErrNoRows {
			return ContentEvidence{}, ErrNotFound
		}
		return ContentEvidence{}, err
	}
	id, e1 := ContentEvidenceIDFromBytes(rawID)
	project, e2 := ProjectIDFromBytes(rawProject)
	content, e3 := ContentIDFromBytes(rawContent)
	rev, e4 := NewRevision(revision)
	at, e5 := NewUnixMillis(created)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return ContentEvidence{}, fmt.Errorf("%w: invalid content evidence row", ErrCorruptState)
	}
	return ContentEvidence{ID: id, ProjectID: project, ContentID: content, ContentRevision: rev, TestedSource: tested, Environment: environment, Result: result, Location: location, Evaluator: evaluator, Judgment: judgment, CreatedAt: at}, nil
}

func (store *Store) TaskContentReferences(ctx context.Context, project ProjectID, task TaskID, workRevision Revision) ([]TaskContentReference, error) {
	if project.zero() || task.zero() || workRevision.Int64() < 1 {
		return nil, fmt.Errorf("%w: invalid task content reference", ErrInvalidValue)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	return taskContentReferencesOnConnection(ctx, connection, project, task, workRevision)
}

func taskContentReferencesOnConnection(ctx context.Context, connection *sql.Conn, project ProjectID, task TaskID, workRevision Revision) ([]TaskContentReference, error) {
	if project.zero() || task.zero() || workRevision.Int64() < 1 {
		return nil, fmt.Errorf("%w: invalid task content reference", ErrInvalidValue)
	}
	rows, err := connection.QueryContext(ctx, `SELECT task_id, project_id, task_work_revision, content_id, content_revision, attached_at_ms FROM task_content_references WHERE task_id = ? AND project_id = ? AND task_work_revision = ? ORDER BY content_id, content_revision LIMIT ?`, task.Bytes(), project.Bytes(), workRevision.Int64(), contentPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskContentReference
	for rows.Next() {
		if len(result) == contentPageSize {
			return nil, fmt.Errorf("%w: task content references exceed bound", ErrInvalidValue)
		}
		var rawTask, rawProject, rawContent []byte
		var taskRev, contentRev, attached int64
		if err := rows.Scan(&rawTask, &rawProject, &taskRev, &rawContent, &contentRev, &attached); err != nil {
			return nil, err
		}
		tid, e1 := TaskIDFromBytes(rawTask)
		pid, e2 := ProjectIDFromBytes(rawProject)
		cid, e3 := ContentIDFromBytes(rawContent)
		tr, e4 := NewRevision(taskRev)
		cr, e5 := NewRevision(contentRev)
		at, e6 := NewUnixMillis(attached)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			return nil, fmt.Errorf("%w: invalid task content reference row", ErrCorruptState)
		}
		result = append(result, TaskContentReference{TaskID: tid, ProjectID: pid, TaskWorkRevision: tr, ContentID: cid, ContentRevision: cr, AttachedAt: at})
	}
	return result, rows.Err()
}

func (store *Store) ListContentEvidenceForAttempt(ctx context.Context, digest AttemptDigest, content ContentID, revision Revision, offset, limit int) (ContentEvidencePage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentEvidencePage{}, err
	}
	defer tx.Close()
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return ContentEvidencePage{}, err
	}
	return listContentEvidenceOnConnection(ctx, tx.connection, authority.ProjectID, content, revision, offset, limit)
}

func (store *Store) TaskContentReferencesForAttempt(ctx context.Context, digest AttemptDigest, task TaskID, workRevision Revision) ([]TaskContentReference, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	authority, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return nil, err
	}
	if task.zero() {
		task = authority.TaskID
	}
	if authority.Role != RoleOrchestrator && (task != authority.TaskID || workRevision != authority.AdmittedTaskWorkRevision) {
		return nil, ErrUnauthorized
	}
	selected, found, err := taskByID(ctx, tx.connection, task)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	if selected.ProjectID != authority.ProjectID {
		return nil, ErrUnauthorized
	}
	if workRevision.Int64() > selected.WorkRevision.Int64() {
		return nil, ErrConflict
	}
	return taskContentReferencesOnConnection(ctx, tx.connection, authority.ProjectID, task, workRevision)
}
