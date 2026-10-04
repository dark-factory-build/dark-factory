package kernel

import (
	"context"
	"fmt"
)

const maxProjectRunSeconds = 86400

// MaxOverseerRunSeconds bounds one non-shell overseer run even when the
// project ceiling is disabled or longer. An overseer must checkpoint and exit
// when only an external event remains (#1096); this backstop stops one that
// waits instead from holding the project's single overseer lane. Events stay
// pending, so the next wake resumes from durable state.
const MaxOverseerRunSeconds = 1800

// SetProjectLimits replaces a project's future run allowance and per-run
// wall-clock ceiling. An allowance is additional to the lifetime count already
// recorded; zero disables the count ceiling without erasing that history.
func (store *Store) SetProjectLimits(ctx context.Context, id ProjectID, expected Revision, allowance uint64, maxRunSeconds uint32, at UnixMillis) (Project, error) {
	return store.SetProjectLimitsWithTokens(ctx, id, expected, allowance, maxRunSeconds, nil, at)
}

// SetProjectLimitsWithTokens also replaces the token allowance when one is
// given, in the same transaction and under the same revision check, so an
// operator edit lands whole or not at all. Like the run allowance it is
// additional to recorded spend, and zero removes the ceiling.
func (store *Store) SetProjectLimitsWithTokens(ctx context.Context, id ProjectID, expected Revision, allowance uint64, maxRunSeconds uint32, tokenAllowance *uint64, at UnixMillis) (Project, error) {
	if id.zero() || expected.Int64() < 1 || allowance > uint64(^uint64(0)>>1) || maxRunSeconds > maxProjectRunSeconds || tokenAllowance != nil && *tokenAllowance > maxProjectTokens {
		return Project{}, fmt.Errorf("%w: project limits", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Close()
	project, found, err := projectByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Project{}, tx.Rollback(err)
	}
	if project.Revision != expected || at.Int64() < project.UpdatedAt.Int64() {
		return Project{}, tx.Rollback(ErrRevisionConflict)
	}
	limit := uint64(0)
	if allowance != 0 {
		if project.RunsUsed > uint64(^uint64(0)>>1)-allowance {
			return Project{}, tx.Rollback(ErrInvalidValue)
		}
		limit = project.RunsUsed + allowance
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE projects SET run_budget_limit = ?, max_run_seconds = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, int64(limit), int64(maxRunSeconds), at.Int64(), id.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Project{}, tx.Rollback(err)
	}
	if tokenAllowance != nil {
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_tokens(project_id, token_limit, tokens_used) VALUES(?1, ?2, 0)
			ON CONFLICT(project_id) DO UPDATE SET token_limit = CASE WHEN ?2 = 0 THEN 0 ELSE MIN(tokens_used + ?2, 9223372036854775807) END`, id.Bytes(), int64(*tokenAllowance)); err != nil {
			return Project{}, tx.Rollback(err)
		}
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityProject, id: id.Bytes(), revision: expected.Int64() + 1}}); err != nil {
		return Project{}, tx.Rollback(err)
	}
	project, found, err = projectByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Project{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Project{}, err
	}
	return project, nil
}

// OverdueRuns returns admitted and running runs whose project ceiling, or the
// overseer backstop, has elapsed. The caller cancels each at its returned
// revision; a concurrent result or stop simply wins the CAS.
func (store *Store) OverdueRuns(ctx context.Context, at UnixMillis) ([]Run, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs AS r WHERE r.phase IN ('admitted', 'running') AND r.admitted_at_ms + 1000 * (SELECT CASE
		WHEN r.role = 'orchestrator' AND r.provider <> 'shell' AND (p.max_run_seconds = 0 OR p.max_run_seconds > ?1) THEN ?1
		ELSE NULLIF(p.max_run_seconds, 0) END FROM projects AS p WHERE p.id = r.project_id) <= ?2 ORDER BY r.admitted_at_ms, r.id`, MaxOverseerRunSeconds, at.Int64())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		run, found, err := scanRun(rows)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
