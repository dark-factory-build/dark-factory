package kernel

import (
	"bytes"
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

// A current home opens untouched; a v39 home (with a continuations table), a
// v38 one (also with runs naming 'runner_exit' for 'transient') and a v37 one
// (also without the specialist columns) migrate, keep every row, and then
// record a transient failure.
func TestCurrentAndLegacyHomesOpenWithEveryRow(t *testing.T) {
	t.Parallel()
	for _, version := range []int{userVersion, v39UserVersion, v38UserVersion, v37UserVersion} {
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
func downgradeHome(t *testing.T, store *Store, version int, extra ...string) {
	t.Helper()
	var statements []string
	if version < userVersion {
		statements = append(v39Continuations,
			`INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, resolution_detail, revision, created_at_ms, updated_at_ms, resolved_at_ms)
			SELECT randomblob(16), r.project_id, r.task_id, r.task_incarnation_id, r.admitted_task_work_revision, randomblob(32), 'human_request', h.id, 1, h.continuation, coalesce(h.continuation_reply, iif(h.continuation = 'cancelled', 'cancelled', NULL)), 1, h.created_at_ms, h.updated_at_ms, iif(h.continuation = 'waiting', NULL, h.updated_at_ms)
			FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id WHERE h.continuation IS NOT NULL`,
			"ALTER TABLE human_requests DROP COLUMN continuation_reply", "ALTER TABLE human_requests DROP COLUMN continuation",
			"PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''peer_question''))', '''peer_question'', ''continuation''))') WHERE name = 'invalidations'`,
			"PRAGMA writable_schema = OFF", fmt.Sprintf("PRAGMA user_version = %d", version))
	}
	if version == v37UserVersion {
		statements = append(statements, "ALTER TABLE agents DROP COLUMN idle_wake_on", "ALTER TABLE projects DROP COLUMN specialist_open_proposals", "ALTER TABLE projects DROP COLUMN specialist_runs")
	}
	if version < v39UserVersion {
		statements = append(statements, "PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''transient''', '''runner_exit''') WHERE name = 'runs'`,
			"PRAGMA writable_schema = OFF", fmt.Sprintf("PRAGMA user_version = %d", version))
	}
	for _, statement := range append(statements, extra...) {
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

// A v39 continuation becomes its human request's: the stalled-item card still
// waits, its log entries are pruned, and a reply after the upgrade resumes
// the carrier with the question.
func TestV39ContinuationMovesOntoItsHumanRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, last, card, at, _ := stalledCard(t)
	var path string
	if err := store.writer.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	// The writer still holds the v40 check text until it reconnects.
	downgradeHome(t, store, v39UserVersion, "PRAGMA ignore_check_constraints = ON",
		`INSERT INTO invalidations SELECT next_invalidation_sequence, 1, 'continuation', (SELECT id FROM continuations), 1, 0 FROM factory`,
		`UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence + 1`, "PRAGMA ignore_check_constraints = OFF")
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open v39 home: %v", err)
	}
	defer reopened.Close()
	if request := requestForTest(t, reopened, card.ID); request.Continuation != ContinuationWaiting {
		t.Fatalf("migrated card continuation = %q", request.Continuation)
	}
	var stale bool
	if err := reopened.writer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invalidations WHERE entity_kind = 'continuation')`).Scan(&stale); err != nil || stale {
		t.Fatalf("continuation invalidations remain = %v, %v", stale, err)
	}
	delivery, _ := HumanRequestDeliveryIDFromBytes(bytes.Repeat([]byte{7}, IDBytes))
	if handled, err := reopened.ResolveHumanContinuationForOperator(ctx, card.ID, card.Revision, delivery, "close #7", mustTime(t, at+1)); err != nil || !handled {
		t.Fatalf("reply = %v, %v", handled, err)
	}
	if resumed, _, err := reopened.Task(ctx, last.ID); err != nil || resumed.Status != TaskQueued {
		t.Fatalf("resumed carrier = %+v, %v", resumed, err)
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
	if got := hex.EncodeToString(sum[:]); got != "e6aee06f7f4049f89af27e06bd73782d66e94ca34c6680074807038a9429aa08" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v39UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "3a54481cb3246bb70309c06135c4b61d5efc739bcf5c38b6432aa4b698f5f25b" {
		t.Errorf("v39 schema digest = %s", got)
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
