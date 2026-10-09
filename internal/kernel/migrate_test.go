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

// A current home opens untouched; a v35 home (the same schema) migrates its
// 'enqueuing' review operations to 'enqueued' and keeps every other row.
func TestCurrentAndV35HomesOpenWithEveryRow(t *testing.T) {
	for _, v35 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v35=%v", v35), func(t *testing.T) { testHomeOpensWithEveryRow(t, v35) })
	}
}

func testHomeOpensWithEveryRow(t *testing.T, v35 bool) {
	ctx := context.Background()
	store, path := newTestStore(t)
	seedDurableAuthority(t, store)
	operation := map[string]any{"id": "allowed", "state": "enqueuing", "enqueue_id": "e", "request": map[string]any{"Repository": "example/factory", "PullNumber": 7, "Head": strings.Repeat("a", 40)}}
	if err := store.RecordReviewOperation(ctx, projectID(t, 1), "example/factory", "allowed", operation, mustTime(t, 6)); err != nil {
		t.Fatal(err)
	}
	if v35 {
		if _, err := store.writer.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v35UserVersion)); err != nil {
			t.Fatal(err)
		}
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotRows(t, ctx, connection)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
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
	if v35 {
		for index, row := range before["production_records"] {
			before["production_records"][index] = strings.Replace(row, `"state":"enqueuing"`, `"state":"enqueued"`, 1)
		}
	}
	if after := snapshotRows(t, ctx, again); !reflect.DeepEqual(before, after) {
		t.Fatalf("opening the home changed rows:\n%v\n%v", before["production_records"], after["production_records"])
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
	sum := sha256.Sum256([]byte(strings.Join(schemaStatements, "\n")))
	if got := hex.EncodeToString(sum[:]); got != "bfc5b62285b00148bc836d389f684eb3112e551952f477e27354cf35174460f0" {
		t.Errorf("current schema digest = %s", got)
	}
}
