package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// A current home opens untouched; a v37 home (the current schema without the
// specialist columns) migrates and keeps every row.
func TestCurrentAndV37HomesOpenWithEveryRow(t *testing.T) {
	t.Parallel()
	for _, v37 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v37=%v", v37), func(t *testing.T) { testHomeOpensWithEveryRow(t, v37) })
	}
}

func testHomeOpensWithEveryRow(t *testing.T, v37 bool) {
	ctx := context.Background()
	store, path := newTestStore(t)
	seedDurableAuthority(t, store)
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotRows(t, ctx, connection)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if v37 {
		for _, statement := range []string{"ALTER TABLE agents DROP COLUMN idle_wake_on", "ALTER TABLE projects DROP COLUMN specialist_open_proposals", "ALTER TABLE projects DROP COLUMN specialist_runs", fmt.Sprintf("PRAGMA user_version = %d", v37UserVersion)} {
			if _, err := store.writer.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open home: %v", err)
	}
	defer reopened.Close()
	again, err := reopened.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, version, err := inspectIdentity(ctx, again); err != nil || version != userVersion {
		t.Fatalf("user_version = %d, %v, want %d", version, err, userVersion)
	}
	if after := snapshotRows(t, ctx, again); !reflect.DeepEqual(before, after) {
		t.Fatal("opening the home changed rows")
	}
}

// snapshotRows reads every table of the current schema.
func snapshotRows(t *testing.T, ctx context.Context, connection *sql.Conn) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	for name, object := range expectedSchemaOf(schemaStatements) {
		if object.kind != "table" {
			continue
		}
		rows, err := connection.QueryContext(ctx, "SELECT * FROM "+name+" ORDER BY 1")
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		names, err := rows.Columns()
		if err != nil {
			t.Fatal(errors.Join(err, rows.Close()))
		}
		values := make([]any, len(names))
		targets := make([]any, len(names))
		for index := range values {
			targets[index] = &values[index]
		}
		result[name] = []string{}
		for rows.Next() {
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(errors.Join(err, rows.Close()))
			}
			result[name] = append(result[name], fmt.Sprintf("%v", values))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
	}
	return result
}

// TestSchemaDigestsArePinned trips on any schema edit. Open refuses a home
// whose schema text differs from schemaStatements, so a schema change bumps
// userVersion, adds the migration step from the version before it, and
// re-pins here.
func TestSchemaDigestsArePinned(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte(strings.Join(schemaStatements, "\n")))
	if got := hex.EncodeToString(sum[:]); got != "29c9a3043f03be927336f6331f7c7ce24bcab875fb4607fc55003b688739fc2e" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(v37SchemaStatements(), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "819c191d4e411ad35a2f7cf19db739d0492d0f8cf1c9c5fe0bd50a5197b5bb6c" {
		t.Errorf("v37 schema digest = %s", got)
	}
}
