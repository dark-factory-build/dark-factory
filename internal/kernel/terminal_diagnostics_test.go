package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

func TestTerminalDiagnosticsSurviveRestartAndRemainBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	path := storePath(t, store)
	proposal, err := NewFailureProposal(FailureInternal, "provider exited before an attempt outcome")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("first\nprivate /Users/example/.secret\nlast\n")
	if err := store.SaveTerminalDiagnostics(ctx, TerminalDiagnostics{RunID: run.ID, Floor: 4, Head: 4 + uint64(len(payload)), Payload: payload, CapturedAt: mustTime(t, 41)}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.TerminalDiagnostics(ctx, run.ID)
	if err != nil || !found || got.Floor != 4 || got.Head != 4+uint64(len(payload)) || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("diagnostics = %+v found=%v err=%v", got, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err = reopened.TerminalDiagnostics(ctx, run.ID)
	if err != nil || !found || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("reopened diagnostics = %+v found=%v err=%v", got, found, err)
	}
}

func TestTerminalDiagnosticsKeepOnlyTheNewestRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	proposal, err := NewFailureProposal(FailureInternal, "provider exited before an attempt outcome")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	// The validator refuses synthetic runs, so the 64 older rows are seeded directly for run ids that have no runs row.
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	seeded := func(i int) []byte {
		id := bytes.Repeat([]byte{0xee}, IDBytes)
		binary.BigEndian.PutUint64(id[IDBytes-8:], uint64(i))
		return id
	}
	for i := range 64 {
		if _, err := connection.ExecContext(ctx, `INSERT INTO terminal_diagnostics(run_id, floor, head, payload, captured_at_ms) VALUES(?, 0, 1, x'78', ?)`, seeded(i), 100+i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTerminalDiagnostics(ctx, TerminalDiagnostics{RunID: run.ID, Floor: 0, Head: 1, Payload: []byte("x"), CapturedAt: mustTime(t, 200)}); err != nil {
		t.Fatal(err)
	}
	var count, oldest int
	if err := store.writer.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE run_id = ?) FROM terminal_diagnostics`, seeded(0)).Scan(&count, &oldest); err != nil || count != 64 || oldest != 0 {
		t.Fatalf("rows = %d oldest = %d err=%v, want 64 rows without the oldest", count, oldest, err)
	}
	if _, found, err := store.TerminalDiagnostics(ctx, run.ID); err != nil || !found {
		t.Fatalf("newest diagnostics found=%v err=%v", found, err)
	}
}
