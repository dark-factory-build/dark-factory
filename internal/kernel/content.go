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
	ID                                                 ContentID
	ProjectID                                          ProjectID
	Kind                                               ContentKind
	Revision                                           Revision
	Title, Description, Body, Author, SourceReferences string
	Deprecated                                         bool
	CreatedAt                                          UnixMillis
}

type NewContent struct {
	ID                                                 ContentID
	ProjectID                                          ProjectID
	Kind                                               ContentKind
	Title, Description, Body, Author, SourceReferences string
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

const contentPageSize = 64
const contentBodyPageSize = 64 * 1024

func validateContent(spec NewContent) error {
	if spec.ID.zero() || spec.ProjectID.zero() || (spec.Kind != ContentProcedure && spec.Kind != ContentAcceptanceScenario) || byteLen(spec.Title) < 1 || byteLen(spec.Title) > 1024 || byteLen(spec.Description) > 4096 || byteLen(spec.Body) > 1<<20 || byteLen(spec.Author) < 1 || byteLen(spec.Author) > 256 || byteLen(spec.SourceReferences) > 32768 {
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
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms) VALUES(?, ?, ?, 1, ?, ?, ?, ?, ?, 0, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), string(spec.Kind), spec.Title, spec.Description, spec.Body, spec.Author, spec.SourceReferences, at.Int64()); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	result, err := contentByRevision(ctx, tx.connection, spec.ID, 1)
	if err != nil {
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
		// A retried request observes the revision it inserted.  The adjacent
		// revision check is durable replay detection; older revisions remain CAS
		// conflicts and can never be silently overwritten.
		if current == expected.Int64()+1 {
			if existing, e := contentByRevision(ctx, tx.connection, spec.ID, current); e == nil && contentMatches(existing, spec, deprecated) {
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
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), string(spec.Kind), next, spec.Title, spec.Description, spec.Body, spec.Author, spec.SourceReferences, deprecatedValue, at.Int64()); err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	result, err := contentByRevision(ctx, tx.connection, spec.ID, next)
	if err != nil {
		return ContentRevision{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContentRevision{}, err
	}
	return result, nil
}

func (store *Store) DeprecateContent(ctx context.Context, id ContentID, project ProjectID, expected Revision, author string, at UnixMillis) (ContentRevision, error) {
	current, err := store.Content(ctx, id, expected.Int64())
	if err != nil {
		return ContentRevision{}, err
	}
	return store.reviseContent(ctx, expected, NewContent{ID: id, ProjectID: project, Kind: current.Kind, Title: current.Title, Description: current.Description, Body: current.Body, Author: author, SourceReferences: current.SourceReferences}, true, at)
}

func (store *Store) Content(ctx context.Context, id ContentID, revision int64) (ContentRevision, error) {
	if id.zero() || revision < 1 {
		return ContentRevision{}, fmt.Errorf("%w: invalid content revision", ErrInvalidValue)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer connection.Close()
	return contentByRevision(ctx, connection, id, revision)
}

func (store *Store) ListContent(ctx context.Context, project ProjectID, kind ContentKind, offset, limit int) (ContentPage, error) {
	if project.zero() || offset < 0 || limit < 0 || limit > contentPageSize {
		return ContentPage{}, fmt.Errorf("%w: invalid content page", ErrInvalidValue)
	}
	if limit == 0 {
		limit = contentPageSize
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer connection.Close()
	// Lists are metadata-only. Complete bodies are available only through the
	// explicit bounded ReadContentBody path.
	query := `SELECT c.id, c.project_id, c.kind, c.revision, c.title, c.description, '', c.author, c.source_references, c.deprecated, c.created_at_ms FROM project_content_revisions c JOIN (SELECT id, MAX(revision) revision FROM project_content_revisions WHERE project_id = ? GROUP BY id) latest ON latest.id = c.id AND latest.revision = c.revision WHERE c.project_id = ?`
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

func (store *Store) ReadContentBody(ctx context.Context, id ContentID, revision, offset, limit int) (ContentBodyPage, error) {
	if offset < 0 || limit <= 0 || limit > contentBodyPageSize {
		return ContentBodyPage{}, fmt.Errorf("%w: invalid body page", ErrInvalidValue)
	}
	content, err := store.Content(ctx, id, int64(revision))
	if err != nil {
		return ContentBodyPage{}, err
	}
	if offset > len(content.Body) {
		return ContentBodyPage{}, fmt.Errorf("%w: body offset past end", ErrInvalidValue)
	}
	if !utf8.ValidString(content.Body) || (offset < len(content.Body) && !utf8.RuneStart(content.Body[offset])) {
		return ContentBodyPage{}, fmt.Errorf("%w: body offset is not a UTF-8 boundary", ErrInvalidValue)
	}
	end := offset + limit
	if end > len(content.Body) {
		end = len(content.Body)
	}
	for end > offset && end < len(content.Body) && !utf8.RuneStart(content.Body[end]) {
		end--
	}
	next := 0
	if end < len(content.Body) {
		next = end
	}
	return ContentBodyPage{ID: id, Revision: content.Revision, Offset: offset, Body: content.Body[offset:end], NextOffset: next, Complete: next == 0}, nil
}

func contentByRevision(ctx context.Context, connection *sql.Conn, id ContentID, revision int64) (ContentRevision, error) {
	return scanContent(connection.QueryRowContext(ctx, `SELECT id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms FROM project_content_revisions WHERE id = ? AND revision = ?`, id.Bytes(), revision))
}

func scanContent(scanner rowScanner) (ContentRevision, error) {
	var rawID, rawProject []byte
	var kind, title, description, body, author, refs string
	var revision, deprecated, created int64
	if err := scanner.Scan(&rawID, &rawProject, &kind, &revision, &title, &description, &body, &author, &refs, &deprecated, &created); err != nil {
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
	return ContentRevision{ID: id, ProjectID: project, Kind: ContentKind(kind), Revision: rev, Title: title, Description: description, Body: body, Author: author, SourceReferences: refs, Deprecated: deprecated != 0, CreatedAt: timestamp}, nil
}

func contentMatches(existing ContentRevision, spec NewContent, deprecated bool) bool {
	return existing.ProjectID == spec.ProjectID && existing.Kind == spec.Kind && existing.Title == spec.Title && existing.Description == spec.Description && existing.Body == spec.Body && existing.Author == spec.Author && existing.SourceReferences == spec.SourceReferences && existing.Deprecated == deprecated
}

func (store *Store) AttachContentToTask(ctx context.Context, task TaskID, project ProjectID, content ContentID, revision Revision, at UnixMillis) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	var count int
	if err := tx.connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE id = ? AND project_id = ?", task.Bytes(), project.Bytes()).Scan(&count); err != nil {
		return tx.Rollback(err)
	}
	if count != 1 {
		return tx.Rollback(ErrConflict)
	}
	var contentProject []byte
	if err := tx.connection.QueryRowContext(ctx, "SELECT project_id FROM project_content_revisions WHERE id = ? AND revision = ?", content.Bytes(), revision.Int64()).Scan(&contentProject); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return tx.Rollback(err)
	}
	if string(contentProject) != string(project.Bytes()) {
		return tx.Rollback(ErrConflict)
	}
	var existingProject []byte
	err = tx.connection.QueryRowContext(ctx, "SELECT project_id FROM task_content_references WHERE task_id = ? AND content_id = ? AND content_revision = ?", task.Bytes(), content.Bytes(), revision.Int64()).Scan(&existingProject)
	if err == nil {
		if string(existingProject) != string(project.Bytes()) {
			return tx.Rollback(ErrCorruptState)
		}
		return tx.Rollback(nil)
	}
	if err != sql.ErrNoRows {
		return tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO task_content_references(task_id, project_id, content_id, content_revision, attached_at_ms) VALUES(?, ?, ?, ?, ?)`, task.Bytes(), project.Bytes(), content.Bytes(), revision.Int64(), at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// ContentForAttempt is the shared non-browser read path. The project is
// derived from the live bearer, so a caller cannot browse another project.
func (store *Store) ContentForAttempt(ctx context.Context, digest AttemptDigest, id ContentID, revision int64) (ContentRevision, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentRevision{}, err
	}
	content, err := store.Content(ctx, id, revision)
	if err != nil {
		return ContentRevision{}, err
	}
	if content.ProjectID != authority.ProjectID {
		return ContentRevision{}, ErrUnauthorized
	}
	return content, nil
}

// AttachContentForAttempt permits only an authenticated live attempt to
// freeze a revision on its own task. It never changes task admission state.
func (store *Store) AttachContentForAttempt(ctx context.Context, digest AttemptDigest, content ContentID, revision Revision, at UnixMillis) error {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return err
	}
	return store.AttachContentToTask(ctx, authority.TaskID, authority.ProjectID, content, revision, at)
}

// CreateContentForAttempt keeps ordinary content writes under the same live
// attempt/project authority as the other worker APIs.
func (store *Store) CreateContentForAttempt(ctx context.Context, digest AttemptDigest, spec NewContent, at UnixMillis) (ContentRevision, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentRevision{}, err
	}
	if spec.ProjectID != authority.ProjectID {
		return ContentRevision{}, ErrUnauthorized
	}
	return store.CreateContent(ctx, spec, at)
}

func (store *Store) ListContentForAttempt(ctx context.Context, digest AttemptDigest, kind ContentKind, offset, limit int) (ContentPage, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentPage{}, err
	}
	return store.ListContent(ctx, authority.ProjectID, kind, offset, limit)
}

func (store *Store) ReadContentBodyForAttempt(ctx context.Context, digest AttemptDigest, id ContentID, revision, offset, limit int) (ContentBodyPage, error) {
	content, err := store.ContentForAttempt(ctx, digest, id, int64(revision))
	if err != nil {
		return ContentBodyPage{}, err
	}
	return store.ReadContentBody(ctx, content.ID, int(content.Revision.Int64()), offset, limit)
}

func (store *Store) ReviseContentForAttempt(ctx context.Context, digest AttemptDigest, expected Revision, spec NewContent, at UnixMillis) (ContentRevision, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentRevision{}, err
	}
	if spec.ProjectID != authority.ProjectID {
		return ContentRevision{}, ErrUnauthorized
	}
	return store.ReviseContent(ctx, expected, spec, at)
}

func (store *Store) DeprecateContentForAttempt(ctx context.Context, digest AttemptDigest, id ContentID, expected Revision, author string, at UnixMillis) (ContentRevision, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentRevision{}, err
	}
	current, err := store.Content(ctx, id, expected.Int64())
	if err != nil {
		return ContentRevision{}, err
	}
	if current.ProjectID != authority.ProjectID {
		return ContentRevision{}, ErrUnauthorized
	}
	return store.DeprecateContent(ctx, id, authority.ProjectID, expected, author, at)
}

func (store *Store) CreateContentEvidenceForAttempt(ctx context.Context, digest AttemptDigest, spec NewContentEvidence, at UnixMillis) (ContentEvidence, error) {
	authority, err := store.AuthenticateAttempt(ctx, digest)
	if err != nil {
		return ContentEvidence{}, err
	}
	if spec.ProjectID != authority.ProjectID {
		return ContentEvidence{}, ErrUnauthorized
	}
	return store.CreateContentEvidence(ctx, spec, at)
}

func (store *Store) CreateContentEvidence(ctx context.Context, spec NewContentEvidence, at UnixMillis) (ContentEvidence, error) {
	if spec.ID.zero() || spec.ProjectID.zero() || spec.ContentID.zero() || spec.ContentRevision.Int64() < 1 || byteLen(spec.TestedSource) < 1 || byteLen(spec.TestedSource) > 4096 || byteLen(spec.Environment) > 4096 || (spec.Result != "passed" && spec.Result != "failed" && spec.Result != "incomplete" && spec.Result != "not_run") || byteLen(spec.Location) > 4096 || byteLen(spec.Evaluator) < 1 || byteLen(spec.Evaluator) > 256 || byteLen(spec.Judgment) > 8192 {
		return ContentEvidence{}, fmt.Errorf("%w: invalid content evidence", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ContentEvidence{}, err
	}
	defer tx.Close()
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
	err = tx.connection.QueryRowContext(ctx, `SELECT project_id, content_id, content_revision, tested_source, environment, result, location, evaluator, judgment, created_at_ms FROM project_content_evidence WHERE id = ?`, spec.ID.Bytes()).Scan(&existingProjectID, &existingContentID, &existingRevision, &existing.TestedSource, &existing.Environment, &existing.Result, &existing.Location, &existing.Evaluator, &existing.Judgment, &created)
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
