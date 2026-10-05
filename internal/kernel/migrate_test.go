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

// A current home opens untouched; a v34 home (the current schema plus the
// four tables v35 dropped) migrates and keeps every surviving row.
func TestCurrentAndV34HomesOpenWithEveryRow(t *testing.T) {
	for _, v34 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v34=%v", v34), func(t *testing.T) { testHomeOpensWithEveryRow(t, v34) })
	}
}

func testHomeOpensWithEveryRow(t *testing.T, v34 bool) {
	ctx := context.Background()
	store, path := newTestStore(t)
	seedDurableAuthority(t, store)
	if v34 {
		for _, statement := range append(v34Dropped, fmt.Sprintf("PRAGMA user_version = %d", v34UserVersion)) {
			if _, err := store.writer.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
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
	sum := sha256.Sum256([]byte(strings.Join(schemaStatements, "\n")))
	if got := hex.EncodeToString(sum[:]); got != "bfc5b62285b00148bc836d389f684eb3112e551952f477e27354cf35174460f0" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(v34SchemaStatements(), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "a335c0acf8d7e6926d5f27b016106a118649dfeb3efe5da787687e94fb961738" {
		t.Errorf("v34 schema digest = %s", got)
	}
}
