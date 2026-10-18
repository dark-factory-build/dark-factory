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
	"sort"
	"strings"
	"testing"
)

// A current home opens untouched; a v45 home gains provider adoption state, a v44 home preserves archived_tasks, a v43
// home (without archived_tasks), a v42 one (also without the intake author columns), a v41 one (also without
// archive_batches), a v40
// one (also with a continuations table), a v39 one (also with
// browser_security_events), a
// v38 one (also with runs naming 'runner_exit' for 'transient') and a v37 one
// (also without the specialist columns) migrate, keep every row, and then
// record a transient failure.
func TestCurrentAndLegacyHomesOpenWithEveryRow(t *testing.T) {
	t.Parallel()
	for _, version := range []int{userVersion, v45UserVersion, v44UserVersion, v43UserVersion, v42UserVersion, v41UserVersion, v40UserVersion, v39UserVersion, v38UserVersion, v37UserVersion} {
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
	if version == v44UserVersion {
		if _, err := connection.ExecContext(ctx, `INSERT INTO archive_batches(generation, rows, digest, archived_at_ms) VALUES(1, 1, ?, 5)`, bytes.Repeat([]byte{0x44}, DigestBytes)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO archived_tasks(task_id, generation) VALUES(?, 1)`, bytes.Repeat([]byte{0x45}, IDBytes)); err != nil {
			t.Fatal(err)
		}
		before = snapshotRows(t, ctx, connection)
	}
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
		statements = []string{
			"ALTER TABLE projects ADD COLUMN run_budget_limit INTEGER NOT NULL DEFAULT 0 CHECK (run_budget_limit >= 0)",
			"ALTER TABLE projects ADD COLUMN runs_used INTEGER NOT NULL DEFAULT 0 CHECK (runs_used >= 0)",
			"ALTER TABLE projects ADD COLUMN max_run_seconds INTEGER NOT NULL DEFAULT 0 CHECK (max_run_seconds BETWEEN 0 AND 86400)",
			"ALTER TABLE project_tokens ADD COLUMN token_limit INTEGER NOT NULL DEFAULT 0 CHECK (token_limit >= 0)",
			fmt.Sprintf("PRAGMA user_version = %d", version),
		}
		if version < v44UserVersion {
			statements = append(statements, "DROP TABLE archived_tasks")
		}
		if version < userVersion {
			statements = append(statements, "DROP TABLE provider_adoptions")
		}
		if version < v43UserVersion {
			statements = append([]string{"ALTER TABLE intake_acceptances DROP COLUMN author_type", "ALTER TABLE intake_acceptances DROP COLUMN author_login"}, statements...)
		}
		if version < v42UserVersion {
			statements = append([]string{"DROP TABLE archive_batches"}, statements...)
		}
	}
	if version < v41UserVersion {
		statements = append(append(statements, v40Continuations...),
			`INSERT INTO continuations(id, project_id, task_id, task_incarnation_id, work_revision, context_digest, condition_kind, condition_id, condition_revision, state, resolution_detail, revision, created_at_ms, updated_at_ms, resolved_at_ms)
			SELECT randomblob(16), r.project_id, r.task_id, r.task_incarnation_id, r.admitted_task_work_revision, randomblob(32), 'human_request', h.id, 1, h.continuation, coalesce(h.continuation_reply, iif(h.continuation = 'cancelled', 'cancelled', NULL)), 1, h.created_at_ms, h.updated_at_ms, iif(h.continuation = 'waiting', NULL, h.updated_at_ms)
			FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id WHERE h.continuation IS NOT NULL`,
			"ALTER TABLE human_requests DROP COLUMN continuation_reply", "ALTER TABLE human_requests DROP COLUMN continuation",
			"PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''peer_question''))', '''peer_question'', ''continuation''))') WHERE name = 'invalidations'`,
			"PRAGMA writable_schema = OFF", fmt.Sprintf("PRAGMA user_version = %d", version))
	}
	if version < v40UserVersion {
		statements = append(append(statements, browserSecurityEventStatements...),
			`INSERT INTO browser_security_events(kind, client_id, occurred_at_ms) VALUES('challenge_minted', NULL, 1)`)
	}
	if version == v37UserVersion {
		statements = append(statements, "ALTER TABLE agents DROP COLUMN idle_wake_on", "ALTER TABLE projects DROP COLUMN specialist_open_proposals", "ALTER TABLE projects DROP COLUMN specialist_runs")
	}
	if version < v39UserVersion {
		statements = append(statements, "PRAGMA writable_schema = ON",
			`UPDATE sqlite_schema SET sql = replace(sql, '''transient''', '''runner_exit''') WHERE name = 'runs'`,
			"PRAGMA writable_schema = OFF")
	}
	for _, statement := range append(statements, extra...) {
		if _, err := store.writer.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if version == v44UserVersion {
		rebuildV44ProjectTables(t, store)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func rebuildV44ProjectTables(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	expected := expectedSchemaOf(legacySchemaStatements(v44UserVersion))
	projects := expected["projects"].sql
	tokens := expected["project_tokens"].sql
	statements := []string{
		"PRAGMA foreign_keys = OFF",
		"PRAGMA legacy_alter_table = ON",
		"DROP INDEX projects_root_unique",
		"ALTER TABLE projects RENAME TO projects_v44_source",
		projects,
		`INSERT INTO projects SELECT id, name, root, verification_policy, run_budget_limit, runs_used, max_run_seconds, revision, created_at_ms, updated_at_ms, specialist_runs, specialist_open_proposals FROM projects_v44_source`,
		"DROP TABLE projects_v44_source",
		"CREATE UNIQUE INDEX projects_root_unique ON projects(root)",
		"ALTER TABLE project_tokens RENAME TO project_tokens_v44_source",
		tokens,
		`INSERT INTO project_tokens SELECT project_id, token_limit, tokens_used FROM project_tokens_v44_source`,
		"DROP TABLE project_tokens_v44_source",
		"PRAGMA legacy_alter_table = OFF",
		"PRAGMA foreign_keys = ON",
	}
	for _, statement := range statements {
		if _, err := store.writer.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
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
		rows, err := connection.QueryContext(ctx, "SELECT * FROM "+name)
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
		sort.Strings(result[name])
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
	if got := hex.EncodeToString(sum[:]); got != "af90593f8cef04f89f5aae304c001520c901eaee79c87f1de40691c595a41c38" {
		t.Errorf("current schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v45UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "a5fd7c65ec3fdbe957980b5b936a6967a9e06440c6984bdef6ec33073e46ef14" {
		t.Errorf("v45 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v43UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "dd3a34e6e1e9838d35ab3f58a3f721ab50aae9fdba643f5deb63ea4cc27d7622" {
		t.Errorf("v43 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v42UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "d1a14bbc6ece50bd5663cddca622d47da816b51a745ea71a91a7c18453ed8c6c" {
		t.Errorf("v42 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v41UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "d386ae5811ef91b686f7e597b04e6e2e376a36e095f4f23cb94d58f3725f047e" {
		t.Errorf("v41 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v40UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "72b0fb536b67311d14405944e08376787f72a4cc5394c390df3b8ef97ef9dfe4" {
		t.Errorf("v40 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v39UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "591deabdaab5f0492e4e51dacb0369eb1429a2b84aa1175bb842eeeaa44e9d81" {
		t.Errorf("v39 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v38UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "cb29f8757bc5e68cc6ce18dc414f2e690349f838a74d06350c34ac597a2bbbf4" {
		t.Errorf("v38 schema digest = %s", got)
	}
	sum = sha256.Sum256([]byte(strings.Join(legacySchemaStatements(v37UserVersion), "\n")))
	if got := hex.EncodeToString(sum[:]); got != "006791c6a8e9f984bfe82a56e67134d307d7db04706392a9cfa31a4bc2552ef0" {
		t.Errorf("v37 schema digest = %s", got)
	}
}
