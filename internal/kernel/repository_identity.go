package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

// RepositorySourceIdentity is host-inspected checkout authority. The remote
// digest never stores URL credentials; PublicationRepository is only a name,
// not a claim of live GitHub access or numeric repository identity.
type RepositorySourceIdentity struct {
	RootDevice, RootInode, GitDevice, GitInode uint64
	OriginDigest                               [32]byte
	PublicationRepository                      string
}

func (value RepositorySourceIdentity) valid() bool {
	return value.RootDevice <= math.MaxInt64 && value.RootInode > 0 && value.RootInode <= math.MaxInt64 && value.GitDevice == value.RootDevice && value.GitInode > 0 && value.GitInode <= math.MaxInt64 && value.OriginDigest != [32]byte{} && byteLen(value.PublicationRepository) <= 140 && !strings.ContainsAny(value.PublicationRepository, "\x00\r\n")
}

const repositorySourceColumns = `root_dev, root_inode, git_dev, git_inode, origin_digest, publication_repository`

func scanRepositorySourceIdentity(scanner rowScanner) (RepositorySourceIdentity, bool, error) {
	var rootDev, rootInode, gitDev, gitInode *uint64
	var publication *string
	var digest []byte
	if err := scanner.Scan(&rootDev, &rootInode, &gitDev, &gitInode, &digest, &publication); err != nil {
		return RepositorySourceIdentity{}, false, fmt.Errorf("%w: repository source identity: %v", ErrCorruptState, err)
	}
	if rootDev == nil && rootInode == nil && gitDev == nil && gitInode == nil && digest == nil && publication == nil {
		return RepositorySourceIdentity{}, false, nil
	}
	if rootDev == nil || rootInode == nil || gitDev == nil || gitInode == nil || len(digest) != 32 || publication == nil {
		return RepositorySourceIdentity{}, false, ErrCorruptState
	}
	value := RepositorySourceIdentity{RootDevice: *rootDev, RootInode: *rootInode, GitDevice: *gitDev, GitInode: *gitInode, PublicationRepository: *publication}
	copy(value.OriginDigest[:], digest)
	if !value.valid() {
		return value, false, ErrCorruptState
	}
	return value, true, nil
}

func repositorySourceIdentity(ctx context.Context, connection *sql.Conn, id RepositoryID) (RepositorySourceIdentity, bool, error) {
	return scanRepositorySourceIdentity(connection.QueryRowContext(ctx, `SELECT `+repositorySourceColumns+` FROM repository_source_identities WHERE repository_id = ?`, id.Bytes()))
}

func (store *Store) RepositorySourceIdentity(ctx context.Context, id RepositoryID) (RepositorySourceIdentity, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return RepositorySourceIdentity{}, false, err
	}
	defer read.Close()
	return repositorySourceIdentity(ctx, read.connection, id)
}

// BindRepositorySource fills a legacy binding once; subsequent calls can only
// prove the same identity. Retained source facts cannot be silently adopted by
// a replacement checkout at the same pathname.
func (store *Store) BindRepositorySource(ctx context.Context, id RepositoryID, value RepositorySourceIdentity) error {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if err := bindRepositorySource(ctx, tx.connection, id, value); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

func bindRepositorySource(ctx context.Context, connection *sql.Conn, id RepositoryID, value RepositorySourceIdentity) error {
	if id.zero() || !value.valid() {
		return ErrInvalidValue
	}
	existing, found, err := repositorySourceIdentity(ctx, connection, id)
	if err != nil {
		return err
	}
	if found {
		if existing != value {
			return ErrConflict
		}
		return nil
	}
	var mismatch bool
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM changes c JOIN task_repository_bindings b ON b.task_id = c.task_id WHERE b.repository_id = ? AND c.repository_inode IS NOT NULL AND (c.repository_dev <> ? OR c.repository_inode <> ?)) OR EXISTS(SELECT 1 FROM project_content_revisions c JOIN content_repository_bindings b ON b.content_id = c.id AND b.content_revision = c.revision WHERE b.repository_id = ? AND c.repository_inode IS NOT NULL AND (c.repository_dev <> ? OR c.repository_inode <> ?))`, id.Bytes(), value.RootDevice, value.RootInode, id.Bytes(), value.RootDevice, value.RootInode).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch {
		return ErrConflict
	}
	_, err = connection.ExecContext(ctx, `UPDATE repository_source_identities SET root_dev = ?, root_inode = ?, git_dev = ?, git_inode = ?, origin_digest = ?, publication_repository = ? WHERE repository_id = ? AND root_dev IS NULL`, value.RootDevice, value.RootInode, value.GitDevice, value.GitInode, value.OriginDigest[:], value.PublicationRepository, id.Bytes())
	return err
}

func validateRepositoryBindings(ctx context.Context, connection *sql.Conn) error {
	var broken bool
	err := connection.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM repository_source_identities WHERE github_repository_id IS NOT NULL AND (github_repository_id <= 0 OR root_dev IS NULL OR publication_repository = '')) OR
 EXISTS(SELECT 1 FROM projects p WHERE (SELECT COUNT(*) FROM project_repositories r WHERE r.project_id = p.id AND r.is_default = 1 AND r.enabled = 1) <> 1) OR
 EXISTS(SELECT 1 FROM project_repositories r LEFT JOIN projects p ON p.id = r.project_id WHERE p.id IS NULL OR (r.is_default = 1 AND r.enabled = 0)) OR
 EXISTS(SELECT 1 FROM tasks t LEFT JOIN task_repository_bindings b ON b.task_id = t.id LEFT JOIN project_repositories r ON r.id = b.repository_id WHERE r.id IS NULL OR r.project_id <> t.project_id) OR
 EXISTS(SELECT 1 FROM task_repository_bindings b LEFT JOIN tasks t ON t.id = b.task_id WHERE t.id IS NULL) OR
 EXISTS(SELECT 1 FROM project_content_revisions c LEFT JOIN content_repository_bindings b ON b.content_id = c.id AND b.content_revision = c.revision LEFT JOIN project_repositories r ON r.id = b.repository_id WHERE r.id IS NULL OR r.project_id <> c.project_id) OR
 EXISTS(SELECT 1 FROM content_repository_bindings b LEFT JOIN project_content_revisions c ON c.id = b.content_id AND c.revision = b.content_revision LEFT JOIN project_repositories r ON r.id = b.repository_id WHERE c.id IS NULL OR r.id IS NULL OR c.project_id <> r.project_id) OR
 EXISTS(SELECT 1 FROM changes c JOIN task_repository_bindings b ON b.task_id = c.task_id JOIN repository_source_identities i ON i.repository_id = b.repository_id WHERE i.root_dev IS NOT NULL AND c.repository_dev IS NOT NULL AND (i.root_dev <> c.repository_dev OR i.root_inode <> c.repository_inode)) OR
 EXISTS(SELECT 1 FROM project_content_revisions c JOIN content_repository_bindings b ON b.content_id = c.id AND b.content_revision = c.revision JOIN repository_source_identities i ON i.repository_id = b.repository_id WHERE i.root_dev IS NOT NULL AND c.repository_dev IS NOT NULL AND (i.root_dev <> c.repository_dev OR i.root_inode <> c.repository_inode)) OR
 EXISTS(SELECT 1 FROM project_repositories r LEFT JOIN repository_source_identities i ON i.repository_id = r.id WHERE i.repository_id IS NULL) OR
 EXISTS(SELECT 1 FROM repository_source_identities i LEFT JOIN project_repositories r ON r.id = i.repository_id WHERE r.id IS NULL)`).Scan(&broken)
	if err != nil {
		return err
	}
	if broken {
		return fmt.Errorf("%w: repository relationships", ErrCorruptState)
	}
	rows, err := connection.QueryContext(ctx, `SELECT `+repositorySourceColumns+` FROM repository_source_identities`)
	if err != nil {
		return err
	}
	for rows.Next() {
		if _, _, err := scanRepositorySourceIdentity(rows); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = connection.QueryContext(ctx, `SELECT `+projectRepositoryColumns+` FROM project_repositories`)
	if err != nil {
		return err
	}
	for rows.Next() {
		if _, _, err := scanProjectRepository(rows); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = connection.QueryContext(ctx, `SELECT base_ref FROM task_repository_bindings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var base string
		if err := rows.Scan(&base); err != nil {
			return err
		}
		if base != inheritedRepositoryBase && !validRepositoryBase(base) {
			return ErrCorruptState
		}
	}
	return rows.Err()
}

// InitialRepositoryBase is the boot policy for a newly registered project.
func (store *Store) InitialRepositoryBase(ctx context.Context) (string, error) {
	if err := store.acquireWriter(ctx); err != nil {
		return "", err
	}
	defer store.releaseWriter()
	if store.repositoryBase == "" {
		return "", ErrConflict
	}
	return store.repositoryBase, nil
}
