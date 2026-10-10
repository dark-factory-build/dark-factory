package kernel

import (
	"context"
	"fmt"
)

// ProjectTokens is a project's provider token spend and its ceiling. Admission
// TokensUsed is retained for status and efficiency reporting.
type ProjectTokens struct {
	TokensUsed uint64
}

const maxProjectTokens = uint64(^uint64(0) >> 1)

// ProjectTokens reads the recorded spend. A project nothing was recorded for
// has spent nothing and has no ceiling.
func (store *Store) ProjectTokens(ctx context.Context, id ProjectID) (ProjectTokens, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ProjectTokens{}, err
	}
	defer tx.Close()
	var used int64
	err = tx.connection.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_used), 0) FROM project_tokens WHERE project_id = ?`, id.Bytes()).Scan(&used)
	return ProjectTokens{TokensUsed: uint64(used)}, err
}

// AddRunTokens records what one run spent, once: the run's own row is the
// receipt, so a caller may retry after any failure without counting twice.
// The project total saturates rather than wrapping, so a ceiling can only
// ever be reached, never escaped.
func (store *Store) AddRunTokens(ctx context.Context, runID RunID, tokens uint64) error {
	if runID == (RunID{}) {
		return fmt.Errorf("%w: run tokens", ErrInvalidValue)
	}
	tokens = min(tokens, maxProjectTokens)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	inserted, err := tx.connection.ExecContext(ctx, `INSERT INTO run_tokens(run_id, tokens) SELECT id, ?2 FROM runs WHERE id = ?1 ON CONFLICT(run_id) DO NOTHING`, runID.Bytes(), int64(tokens))
	if err != nil {
		return tx.Rollback(err)
	}
	if recorded, err := inserted.RowsAffected(); err != nil || recorded == 0 {
		// Already recorded, or no such run: nothing more to count.
		return tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_tokens(project_id, tokens_used) SELECT project_id, ?2 FROM runs WHERE id = ?1
		ON CONFLICT(project_id) DO UPDATE SET tokens_used = MIN(tokens_used + ?2, 9223372036854775807)`, runID.Bytes(), int64(tokens)); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}
