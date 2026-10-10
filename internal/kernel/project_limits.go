package kernel

import (
	"context"
	"fmt"
)

// The schema's bounds on a project's specialist limits.
const (
	MaxSpecialistRuns          = 16
	MaxSpecialistOpenProposals = 32
)

func (store *Store) SetProjectLimits(ctx context.Context, id ProjectID, expected Revision, _ uint64, _ uint32, at UnixMillis) (Project, error) {
	return store.SetProjectLimitsWithTokens(ctx, id, expected, 0, 0, nil, nil, nil, at)
}

// MaxOverseerRunSeconds bounds one non-shell overseer run. An overseer must checkpoint and exit
// when only an external event remains (#1096); this backstop stops one that
// waits instead from holding the project's single overseer lane. Events stay
// pending, so the next wake resumes from durable state.
const MaxOverseerRunSeconds = 1800

func (store *Store) SetProjectLimitsWithTokens(ctx context.Context, id ProjectID, expected Revision, _ uint64, _ uint32, tokenAllowance *uint64, specialistRuns, openProposals *uint32, at UnixMillis) (Project, error) {
	if id.zero() || expected.Int64() < 1 || tokenAllowance != nil && *tokenAllowance > maxProjectTokens ||
		specialistRuns != nil && *specialistRuns > MaxSpecialistRuns || openProposals != nil && *openProposals > MaxSpecialistOpenProposals {
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
	if specialistRuns == nil {
		specialistRuns = &project.SpecialistRuns
	}
	if openProposals == nil {
		openProposals = &project.SpecialistOpenProposals
	}
	updated, err := tx.connection.ExecContext(ctx, `UPDATE projects SET specialist_runs = ?, specialist_open_proposals = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, int64(*specialistRuns), int64(*openProposals), at.Int64(), id.Bytes(), expected.Int64())
	if err := requireOneRow(updated, err); err != nil {
		return Project{}, tx.Rollback(err)
	}
	if tokenAllowance != nil {
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO project_tokens(project_id, tokens_used) VALUES(?1, 0) ON CONFLICT(project_id) DO NOTHING`, id.Bytes()); err != nil {
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

// OverdueRuns returns admitted and running overseer or specialist runs whose
// fixed backstop has elapsed. The caller cancels each at its returned
// revision; a concurrent result or stop simply wins the CAS.
func (store *Store) OverdueRuns(ctx context.Context, at UnixMillis) ([]Run, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs AS r WHERE r.phase IN ('admitted', 'running') AND r.admitted_at_ms + 1000 * ?1 <= ?2 AND (r.role = 'orchestrator' AND r.provider <> 'shell' OR EXISTS (SELECT 1 FROM agents AS a JOIN tasks AS t ON t.id = r.task_id WHERE a.id = r.agent_id AND `+specialistCarrierSQL+`)) ORDER BY r.admitted_at_ms, r.id`, MaxOverseerRunSeconds, at.Int64())
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
