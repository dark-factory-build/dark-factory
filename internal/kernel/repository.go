package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"
)

// A migration cannot know factoryd's --base-revision. This is an internal
// placeholder, never a caller-selectable Git revision or a runtime fallback.
const inheritedRepositoryBase = ":factoryd-base-revision"

func validRepositoryBase(base string) bool {
	return byteLen(base) >= 1 && byteLen(base) <= 4096 && utf8.ValidString(base) && !strings.ContainsRune(base, 0) && !strings.HasPrefix(base, "-") && base != inheritedRepositoryBase
}

// InitializeRepositoryBase pins legacy inheritance before recovery/listeners.
// Already resolved repositories and task bindings are immutable here. The
// boot default also applies to initial bindings of subsequently created projects.
func (store *Store) InitializeRepositoryBase(ctx context.Context, base string) error {
	if !validRepositoryBase(base) {
		return ErrInvalidValue
	}
	if err := store.acquireWriter(ctx); err != nil {
		return err
	}
	initialized := store.repositoryBase != ""
	store.releaseWriter()
	if initialized {
		return nil
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if store.repositoryBase != "" {
		return tx.Rollback(nil)
	}
	for _, table := range []string{"project_repositories", "task_repository_bindings"} {
		if _, err := tx.connection.ExecContext(ctx, `UPDATE `+table+` SET base_ref = ? WHERE base_ref = ?`, base, inheritedRepositoryBase); err != nil {
			return tx.Rollback(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// All readers of this process-owned default hold writerGate.
	store.repositoryBase = base
	return nil
}

// ProjectRepository is private operator configuration. Its root is never part
// of a public snapshot; task and content bindings retain this ID and base ref.
type ProjectRepository struct {
	ID        RepositoryID
	ProjectID ProjectID
	Name      string
	Root      string
	BaseRef   string
	Enabled   bool
	Default   bool
	Revision  Revision
	CreatedAt UnixMillis
	UpdatedAt UnixMillis
}

type NewProjectRepository struct {
	ID            RepositoryID
	ProjectID     ProjectID
	Name          string
	Root, BaseRef string
}

func scanProjectRepository(scanner rowScanner) (ProjectRepository, bool, error) {
	var rawID, rawProjectID []byte
	var name, root, base string
	var enabled, defaultValue, revision, created, updated int64
	if err := scanner.Scan(&rawID, &rawProjectID, &name, &root, &base, &enabled, &defaultValue, &revision, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return ProjectRepository{}, false, nil
		}
		return ProjectRepository{}, false, err
	}
	id, idErr := RepositoryIDFromBytes(rawID)
	projectID, projectErr := ProjectIDFromBytes(rawProjectID)
	rev, revErr := NewRevision(revision)
	createdAt, createdErr := NewUnixMillis(created)
	updatedAt, updatedErr := NewUnixMillis(updated)
	if idErr != nil || projectErr != nil || revErr != nil || createdErr != nil || updatedErr != nil || byteLen(name) < 1 || byteLen(name) > 128 || !validAbsolutePath(root) || !validRepositoryBase(base) || (enabled != 0 && enabled != 1) || (defaultValue != 0 && defaultValue != 1) || updated < created {
		return ProjectRepository{}, false, fmt.Errorf("%w: invalid project repository", ErrCorruptState)
	}
	return ProjectRepository{ID: id, ProjectID: projectID, Name: name, Root: root, BaseRef: base, Enabled: enabled == 1, Default: defaultValue == 1, Revision: rev, CreatedAt: createdAt, UpdatedAt: updatedAt}, true, nil
}

const projectRepositoryColumns = `id, project_id, name, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms`
const qualifiedProjectRepositoryColumns = `r.id, r.project_id, r.name, r.root, r.base_ref, r.enabled, r.is_default, r.revision, r.created_at_ms, r.updated_at_ms`

func repositoryByID(ctx context.Context, connection *sql.Conn, id RepositoryID) (ProjectRepository, bool, error) {
	if id.zero() {
		return ProjectRepository{}, false, ErrInvalidValue
	}
	return scanProjectRepository(connection.QueryRowContext(ctx, `SELECT `+projectRepositoryColumns+` FROM project_repositories WHERE id = ?`, id.Bytes()))
}

func defaultRepository(ctx context.Context, connection *sql.Conn, projectID ProjectID) (ProjectRepository, bool, error) {
	return scanProjectRepository(connection.QueryRowContext(ctx, `SELECT `+projectRepositoryColumns+` FROM project_repositories WHERE project_id = ? AND is_default = 1`, projectID.Bytes()))
}

// TaskRepository returns the immutable route selected when task was queued.
func (store *Store) TaskRepository(ctx context.Context, taskID TaskID) (ProjectRepository, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return ProjectRepository{}, false, err
	}
	defer read.Close()
	return scanProjectRepository(read.connection.QueryRowContext(ctx, `SELECT r.id, r.project_id, r.name, r.root, b.base_ref, r.enabled, r.is_default, r.revision, r.created_at_ms, r.updated_at_ms FROM project_repositories AS r JOIN task_repository_bindings AS b ON b.repository_id = r.id WHERE b.task_id = ?`, taskID.Bytes()))
}

func (store *Store) ContentRepository(ctx context.Context, id ContentID, revision Revision) (ProjectRepository, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return ProjectRepository{}, false, err
	}
	defer read.Close()
	return scanProjectRepository(read.connection.QueryRowContext(ctx, `SELECT `+qualifiedProjectRepositoryColumns+` FROM project_repositories AS r JOIN content_repository_bindings AS b ON b.repository_id = r.id WHERE b.content_id = ? AND b.content_revision = ?`, id.Bytes(), revision.Int64()))
}

func resolveTaskRepository(ctx context.Context, connection *sql.Conn, projectID ProjectID, requested RepositoryID) (ProjectRepository, error) {
	var repository ProjectRepository
	var found bool
	var err error
	if requested.zero() {
		repository, found, err = defaultRepository(ctx, connection, projectID)
	} else {
		repository, found, err = repositoryByID(ctx, connection, requested)
	}
	if err != nil {
		return ProjectRepository{}, err
	}
	if !found || repository.ProjectID != projectID || !repository.Enabled {
		return ProjectRepository{}, ErrConflict
	}
	return repository, nil
}

func (store *Store) ProjectRepositories(ctx context.Context, projectID ProjectID) ([]ProjectRepository, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	rows, err := read.connection.QueryContext(ctx, `SELECT `+projectRepositoryColumns+` FROM project_repositories WHERE project_id = ? ORDER BY created_at_ms, id`, projectID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectRepository{}
	for rows.Next() {
		value, found, err := scanProjectRepository(rows)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *Store) DefaultProjectRepository(ctx context.Context, projectID ProjectID) (ProjectRepository, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return ProjectRepository{}, false, err
	}
	defer read.Close()
	return defaultRepository(ctx, read.connection, projectID)
}

func (store *Store) AddProjectRepository(ctx context.Context, spec NewProjectRepository, at UnixMillis) (ProjectRepository, error) {
	if spec.ID.zero() || spec.ProjectID.zero() || byteLen(spec.Name) < 1 || byteLen(spec.Name) > 128 || !validAbsolutePath(spec.Root) || !validRepositoryBase(spec.BaseRef) {
		return ProjectRepository{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProjectRepository{}, err
	}
	defer tx.Close()
	if _, found, err := projectByID(ctx, tx.connection, spec.ProjectID); err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if existing, found, err := repositoryByID(ctx, tx.connection, spec.ID); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	} else if found {
		if existing.ProjectID == spec.ProjectID && existing.Name == spec.Name && existing.Root == spec.Root && existing.BaseRef == spec.BaseRef {
			if err := tx.Rollback(nil); err != nil {
				return ProjectRepository{}, err
			}
			return existing, nil
		}
		return ProjectRepository{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_repositories(id, project_id, name, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, 1, 0, 1, ?, ?)`, spec.ID.Bytes(), spec.ProjectID.Bytes(), spec.Name, spec.Root, spec.BaseRef, at.Int64(), at.Int64()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	value, found, err := repositoryByID(ctx, tx.connection, spec.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRepository{}, err
	}
	return value, nil
}

func (store *Store) SetProjectRepositoryDefault(ctx context.Context, id RepositoryID, expected Revision, at UnixMillis) (ProjectRepository, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProjectRepository{}, err
	}
	defer tx.Close()
	value, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if value.Revision != expected || !value.Enabled {
		return ProjectRepository{}, tx.Rollback(ErrRevisionConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE project_repositories SET is_default = 0 WHERE project_id = ? AND is_default = 1`, value.ProjectID.Bytes()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE project_repositories SET is_default = 1, revision = revision + 1, updated_at_ms = ? WHERE id = ?`, at.Int64(), id.Bytes()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	updated, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRepository{}, err
	}
	return updated, nil
}

func (store *Store) SetProjectRepositoryEnabled(ctx context.Context, id RepositoryID, expected Revision, enabled bool, at UnixMillis) (ProjectRepository, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProjectRepository{}, err
	}
	defer tx.Close()
	value, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if value.Revision != expected {
		return ProjectRepository{}, tx.Rollback(ErrRevisionConflict)
	}
	if !enabled && value.Default {
		return ProjectRepository{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE project_repositories SET enabled = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, boolInt(enabled), at.Int64(), id.Bytes(), expected.Int64()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	updated, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRepository{}, err
	}
	return updated, nil
}

// UpdateProjectRepositoryBase affects only future selection; bindings retain
// their copied base ref. Root and publication identity have no update path.
func (store *Store) UpdateProjectRepositoryBase(ctx context.Context, id RepositoryID, expected Revision, base string, at UnixMillis) (ProjectRepository, error) {
	if !validRepositoryBase(base) {
		return ProjectRepository{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProjectRepository{}, err
	}
	defer tx.Close()
	value, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if value.Revision != expected {
		return ProjectRepository{}, tx.Rollback(ErrRevisionConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE project_repositories SET base_ref = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, base, at.Int64(), id.Bytes(), expected.Int64()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	updated, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRepository{}, err
	}
	return updated, nil
}

func (store *Store) UpdateProjectRepositoryName(ctx context.Context, id RepositoryID, expected Revision, name string, at UnixMillis) (ProjectRepository, error) {
	if byteLen(name) < 1 || byteLen(name) > 128 {
		return ProjectRepository{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return ProjectRepository{}, err
	}
	defer tx.Close()
	value, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if value.Revision != expected {
		return ProjectRepository{}, tx.Rollback(ErrRevisionConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE project_repositories SET name = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, name, at.Int64(), id.Bytes(), expected.Int64()); err != nil {
		return ProjectRepository{}, tx.Rollback(err)
	}
	updated, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return ProjectRepository{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRepository{}, err
	}
	return updated, nil
}

func (store *Store) RemoveProjectRepository(ctx context.Context, id RepositoryID, expected Revision) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	value, found, err := repositoryByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return tx.Rollback(err)
	}
	if value.Revision != expected || value.Default {
		return tx.Rollback(ErrConflict)
	}
	var references int
	if err := tx.connection.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM task_repository_bindings WHERE repository_id = ?) + (SELECT COUNT(*) FROM content_repository_bindings WHERE repository_id = ?)`, id.Bytes(), id.Bytes()).Scan(&references); err != nil {
		return tx.Rollback(err)
	}
	if references != 0 {
		return tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `DELETE FROM project_repositories WHERE id = ? AND revision = ?`, id.Bytes(), expected.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
