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

// A current home opens untouched; a v38 home (runs naming 'runner_exit' for
// 'transient') and a v37 one (also without the specialist columns) migrate,
// keep every row, and then record a transient failure.
func TestCurrentAndLegacyHomesOpenWithEveryRow(t *testing.T) {
	t.Parallel()
	for _, version := range []int{userVersion, v38UserVersion, v37UserVersion} {
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
	downgradeHome(t, store, version)

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

// downgradeHome turns a current home into an exact earlier one and closes it.
func downgradeHome(t *testing.T, store *Store, version int) {
	t.Helper()
	var statements []string
	if version == v37UserVersion {
		statements = append(statements, "ALTER TABLE agents DROP COLUMN idle_wake_on", "ALTER TABLE projects DROP COLUMN specialist_open_proposals", "ALTER TABLE projects DROP COLUMN specialist_runs")
	}
	if version == v37UserVersion || version == v38UserVersion {
		statements = append(statements,
			"ALTER TABLE projects ADD COLUMN run_budget_limit INTEGER NOT NULL DEFAULT 0 CHECK (run_budget_limit >= 0)",
			"ALTER TABLE projects ADD COLUMN max_run_seconds INTEGER NOT NULL DEFAULT 0 CHECK (max_run_seconds BETWEEN 0 AND 86400)",
			"ALTER TABLE project_tokens ADD COLUMN token_limit INTEGER NOT NULL CHECK (token_limit >= 0)",
			"PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, 'tokens_used INTEGER NOT NULL CHECK (tokens_used >= 0), token_limit INTEGER NOT NULL CHECK (token_limit >= 0)', 'token_limit INTEGER NOT NULL CHECK (token_limit >= 0),\n    tokens_used INTEGER NOT NULL CHECK (tokens_used >= 0)') WHERE name = 'project_tokens'`,
			"PRAGMA writable_schema = OFF")
	}
	if version != userVersion {
		statements = append(statements, "PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''transient''', '''runner_exit''') WHERE name = 'runs'`,
			"PRAGMA writable_schema = OFF", fmt.Sprintf("PRAGMA user_version = %d", version))
	}
	for _, statement := range statements {
		if _, err := store.writer.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// A v38 or v37 never-started failure keeps its retry across the migration: a run
// finalizing at the upgrade still requeues its task, and a terminal one still
// counts as the previous run that ended the same way.
func TestLegacyRetryableFailuresMigrate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version  int
		terminal bool
	}{{v38UserVersion, false}, {v38UserVersion, true}, {v37UserVersion, false}, {v37UserVersion, true}} {
		version, terminal := test.version, test.terminal
		t.Run(fmt.Sprintf("v%d/terminal=%v", version, terminal), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			legacy, _ := NewFailureProposal(FailureProtocol, NeverStartedRunDetail)
			store, run := finalizingReleasedRun(t, RoleOrchestrator, legacy)
			var path string
			if err := store.writer.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
				t.Fatal(err)
			}
			if terminal {
				if _, err := finalizeTestRun(t, store, run, 60); err != nil {
					t.Fatal(err)
				}
			}
			downgradeHome(t, store, version)
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open home: %v", err)
			}
			defer reopened.Close()
			if !terminal {
				if run, err = finalizeTestRun(t, reopened, run, 60); err != nil {
					t.Fatal(err)
				}
				if task, _, err := reopened.Task(ctx, run.TaskID); err != nil || task.Status != TaskQueued {
					t.Fatalf("task after finalization = %v, %v, want queued", task.Status, err)
				}
			}
			var codes string
			if err := reopened.writer.QueryRowContext(ctx, `SELECT proposal_code || '/' || terminal_code FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&codes); err != nil || codes != "transient/transient" {
				t.Fatalf("run codes = %q, %v", codes, err)
			}
		})
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
	if got := hex.EncodeToString(sum[:]); got != "6985bf991c091fccffe066b70f516840837116f81d4ed256c7b4c810c281d7d7" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v38UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "29c9a3043f03be927336f6331f7c7ce24bcab875fb4607fc55003b688739fc2e" {
		t.Errorf("v38 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v37UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "819c191d4e411ad35a2f7cf19db739d0492d0f8cf1c9c5fe0bd50a5197b5bb6c" {
		t.Errorf("v37 schema digest = %s", got)
	}
}
