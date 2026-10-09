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

// A current home opens untouched; a v37 home (runs naming 'runner_exit' for
// 'transient') and a v36 one (also without task_automatic_events) migrate,
// keep every row, and then record a transient failure.
func TestCurrentAndLegacyHomesOpenWithEveryRow(t *testing.T) {
	for _, version := range []int{userVersion, v37UserVersion, v36UserVersion} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) { testHomeOpensWithEveryRow(t, version) })
	}
}

func testHomeOpensWithEveryRow(t *testing.T, version int) {
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
	var statements []string
	if version == v36UserVersion {
		statements = append(statements, "DROP TABLE task_automatic_events")
	}
	if version != userVersion {
		statements = append(statements, "PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''transient''', '''runner_exit''') WHERE name = 'runs'`,
			"PRAGMA writable_schema = OFF", fmt.Sprintf("PRAGMA user_version = %d", version))
	}
	for _, statement := range statements {
		if _, err := store.writer.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
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
	// The migrating writer itself enforces the new check.
	tx, err := reopened.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET phase = 'finalizing', proposal_kind = 'failed', proposal_code = 'transient', proposal_detail = 'x', credential_revoked_at_ms = 5, finalizing_at_ms = 5 WHERE id = ?`, runID(t, 5).Bytes()); err != nil {
		t.Fatalf("record a transient failure: %v", err)
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
	if got := hex.EncodeToString(sum[:]); got != "0fd358c97036e55d36f6805eb8e0d3999d57b7ddeb1576211e44fb7b0596d24a" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v36UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "bfc5b62285b00148bc836d389f684eb3112e551952f477e27354cf35174460f0" {
		t.Errorf("v36 schema digest = %s", got)
	}
}
