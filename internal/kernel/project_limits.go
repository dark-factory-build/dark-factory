package kernel

import "context"

// MaxOverseerRunSeconds bounds one non-shell overseer run even when the
// project ceiling is disabled or longer. An overseer must checkpoint and exit
// when only an external event remains (#1096); this backstop stops one that
// waits instead from holding the project's single overseer lane. Events stay
// pending, so the next wake resumes from durable state.
const MaxOverseerRunSeconds = 1800

// OverdueRuns returns admitted and running overseer runs whose fixed backstop
// has elapsed. A specialist's review shares the backstop. The caller cancels each at its returned
// revision; a concurrent result or stop simply wins the CAS.
func (store *Store) OverdueRuns(ctx context.Context, at UnixMillis) ([]Run, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs AS r WHERE r.phase IN ('admitted', 'running') AND (r.role = 'orchestrator' AND r.provider <> 'shell' OR EXISTS (SELECT 1 FROM agents AS a JOIN tasks AS t ON t.id = r.task_id WHERE a.id = r.agent_id AND `+specialistCarrierSQL+`)) AND r.admitted_at_ms + 1000 * ?1 <= ?2 ORDER BY r.admitted_at_ms, r.id`, MaxOverseerRunSeconds, at.Int64())
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
