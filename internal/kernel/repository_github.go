package kernel

import (
	"context"
	"math"
)

// RepositoryGitHubID is an operator-pinned join to the broker's live numeric ID.
// A configured origin name alone does not establish this identity.
func (store *Store) RepositoryGitHubID(ctx context.Context, id RepositoryID) (uint64, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return 0, false, err
	}
	defer read.Close()
	var value *uint64
	if err := read.connection.QueryRowContext(ctx, `SELECT github_repository_id FROM repository_source_identities WHERE repository_id = ?`, id.Bytes()).Scan(&value); err != nil {
		return 0, false, err
	}
	if value == nil {
		return 0, false, nil
	}
	if *value == 0 || *value > math.MaxInt64 {
		return 0, false, ErrCorruptState
	}
	return *value, true, nil
}

// BindRepositoryGitHubID accepts only the host's explicit live broker join.
// Fresh launches and provider requests never learn or replace this value.
func (store *Store) BindRepositoryGitHubID(ctx context.Context, id RepositoryID, githubID uint64) error {
	if id.zero() || githubID == 0 || githubID > math.MaxInt64 {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	source, verified, err := repositorySourceIdentity(ctx, tx.connection, id)
	if err != nil {
		return tx.Rollback(err)
	}
	if !verified || source.PublicationRepository == "" {
		return tx.Rollback(ErrConflict)
	}
	result, err := tx.connection.ExecContext(ctx, `UPDATE repository_source_identities SET github_repository_id = ? WHERE repository_id = ? AND (github_repository_id IS NULL OR github_repository_id = ?)`, githubID, id.Bytes(), githubID)
	if err := requireOneRow(result, err); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
