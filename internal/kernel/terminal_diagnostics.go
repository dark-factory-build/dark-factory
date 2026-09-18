package kernel

import (
	"context"
	"database/sql"
	"fmt"
)

const MaxTerminalDiagnosticsBytes = 1 << 20

type TerminalDiagnostics struct {
	RunID      RunID
	Floor      uint64
	Head       uint64
	Payload    []byte
	CapturedAt UnixMillis
}

func (store *Store) SaveTerminalDiagnostics(ctx context.Context, value TerminalDiagnostics) error {
	if store == nil || ctx == nil || value.RunID.zero() || value.Floor > value.Head || value.Head > uint64(^uint64(0)>>1) || uint64(len(value.Payload)) > uint64(MaxTerminalDiagnosticsBytes) || uint64(len(value.Payload)) > value.Head-value.Floor {
		return fmt.Errorf("%w: invalid terminal diagnostics", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	var phase string
	if err := tx.connection.QueryRowContext(ctx, `SELECT phase FROM runs WHERE id = ?`, value.RunID.Bytes()).Scan(&phase); err != nil {
		if err == sql.ErrNoRows {
			return tx.Rollback(ErrNotFound)
		}
		return tx.Rollback(err)
	}
	if phase != RunFinalizing.String() && phase != RunTerminal.String() {
		return tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO terminal_diagnostics(run_id, floor, head, payload, captured_at_ms) VALUES(?, ?, ?, ?, ?) ON CONFLICT(run_id) DO UPDATE SET floor = excluded.floor, head = excluded.head, payload = excluded.payload, captured_at_ms = excluded.captured_at_ms`, value.RunID.Bytes(), int64(value.Floor), int64(value.Head), value.Payload, value.CapturedAt.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

func (store *Store) TerminalDiagnostics(ctx context.Context, runID RunID) (TerminalDiagnostics, bool, error) {
	if store == nil || ctx == nil || runID.zero() {
		return TerminalDiagnostics{}, false, fmt.Errorf("%w: invalid terminal diagnostics lookup", ErrInvalidValue)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		return TerminalDiagnostics{}, false, err
	}
	defer connection.Close()
	var floor, head, captured int64
	var payload []byte
	err = connection.QueryRowContext(ctx, `SELECT floor, head, payload, captured_at_ms FROM terminal_diagnostics WHERE run_id = ?`, runID.Bytes()).Scan(&floor, &head, &payload, &captured)
	if err == sql.ErrNoRows {
		return TerminalDiagnostics{}, false, nil
	}
	if err != nil {
		return TerminalDiagnostics{}, false, err
	}
	if floor < 0 || head < floor || uint64(len(payload)) > uint64(MaxTerminalDiagnosticsBytes) || uint64(len(payload)) > uint64(head-floor) {
		return TerminalDiagnostics{}, false, fmt.Errorf("%w: invalid terminal diagnostics", ErrCorruptState)
	}
	at, err := NewUnixMillis(captured)
	if err != nil {
		return TerminalDiagnostics{}, false, fmt.Errorf("%w: invalid terminal diagnostics timestamp", ErrCorruptState)
	}
	return TerminalDiagnostics{RunID: runID, Floor: uint64(floor), Head: uint64(head), Payload: append([]byte(nil), payload...), CapturedAt: at}, true, nil
}
