package kernel

import (
	"context"
	"fmt"
)

// AlertSignals is the small durable view used by the operator alert rules.
// It is deliberately an observation: evaluating it never changes factory
// state.
type AlertSignals struct {
	ReadyWork        bool
	LastMergeAtMS    int64
	RunsLastHour     int
	FailuresLastHour int
}

func (store *Store) AlertSignals(ctx context.Context, nowMS int64) (AlertSignals, error) {
	read, archived, err := store.beginHistoryRead(ctx)
	if err != nil {
		return AlertSignals{}, err
	}
	defer read.Close()
	var signal AlertSignals
	if err := read.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE status = 'queued')`).Scan(&signal.ReadyWork); err != nil {
		return AlertSignals{}, err
	}
	if err := read.connection.QueryRowContext(ctx, `SELECT COALESCE(MAX(COALESCE(unixepoch(json_extract(document, '$.merged_at')) * 1000, observed_at_ms)), 0) FROM `+archived.table("production_records")+` WHERE kind = 'pull_request' AND json_extract(document, '$.state') = 'merged'`).Scan(&signal.LastMergeAtMS); err != nil {
		return AlertSignals{}, err
	}
	if err := read.connection.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(terminal_kind = 'failed'), 0) FROM `+archived.table("runs")+` WHERE admitted_at_ms >= ? AND phase = 'terminal'`, nowMS-60*60*1000).Scan(&signal.RunsLastHour, &signal.FailuresLastHour); err != nil {
		return AlertSignals{}, fmt.Errorf("read run alert signal: %w", err)
	}
	return signal, nil
}
