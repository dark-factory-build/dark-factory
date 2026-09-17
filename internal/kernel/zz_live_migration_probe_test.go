package kernel

import (
	"context"
	"os"
	"testing"
)

// Temporary probe: opens a copy of an operator home named by
// DARK_FACTORY_MIGRATION_PROBE, migrates it, and reports the Change rows.
func TestLiveMigrationProbe(t *testing.T) {
	path := os.Getenv("DARK_FACTORY_MIGRATION_PROBE")
	if path == "" {
		t.Skip("no probe database")
	}
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, version, err := inspectIdentity(ctx, connection); err != nil || version != userVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
	if err := validateExactSchema(ctx, connection); err != nil {
		t.Fatal(err)
	}
	rows, err := connection.QueryContext(ctx, `SELECT phase, COUNT(*), SUM(head_commit IS NULL), SUM(base_commit IS NOT NULL) FROM changes GROUP BY phase`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var phase string
		var count, headless, based int
		if err := rows.Scan(&phase, &count, &headless, &based); err != nil {
			t.Fatal(err)
		}
		t.Logf("phase=%s rows=%d headless=%d with_base=%d", phase, count, headless, based)
	}
	ids, err := connection.QueryContext(ctx, `SELECT id FROM changes`)
	if err != nil {
		t.Fatal(err)
	}
	defer ids.Close()
	read := 0
	for ids.Next() {
		var raw []byte
		if err := ids.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		id, err := ChangeIDFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.Change(ctx, id); err != nil || !found {
			t.Fatalf("read Change %s: found=%v err=%v", id, found, err)
		}
		read++
	}
	t.Logf("read %d Change rows through the store", read)
}
