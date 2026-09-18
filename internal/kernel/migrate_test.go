package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

func TestLegacyHomeMigratesAndKeepsEveryRow(t *testing.T) {
	for _, version := range []int{legacyUserVersion, previousUserVersion, priorUserVersion, v4UserVersion, v5UserVersion, v6UserVersion, v7UserVersion, v8UserVersion, v9UserVersion, v10UserVersion, v11UserVersion, v12UserVersion, v13UserVersion, v14UserVersion, v15UserVersion, v16UserVersion, v17UserVersion, v18UserVersion, v19UserVersion} {
		for _, persistWAL := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/wal=%v", version, persistWAL), func(t *testing.T) {
				testLegacyHomeMigratesAndKeepsEveryRow(t, version, persistWAL)
			})
		}
	}
}

func testLegacyHomeMigratesAndKeepsEveryRow(t *testing.T, version int, persistWAL bool) {
	ctx := context.Background()
	path, before := newLegacyDatabase(t, persistWAL, version)

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open legacy home: %v", err)
	}
	defer store.Close()
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, got, err := inspectIdentity(ctx, connection); err != nil || got != userVersion {
		t.Fatalf("user_version = %d, %v, want %d", got, err, userVersion)
	}
	if err := validateExactSchema(ctx, connection); err != nil {
		t.Fatalf("validateExactSchema after migration: %v", err)
	}
	if after := snapshotRows(t, ctx, connection); !reflect.DeepEqual(before, after) {
		for table := range before {
			if !reflect.DeepEqual(before[table], after[table]) {
				t.Errorf("%s changed:\nbefore %v\nafter  %v", table, before[table], after[table])
			}
		}
		t.Fatal("migration did not preserve every row")
	}
	var accounts, adopted int
	if err := connection.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM accounts), (SELECT COUNT(*) FROM agents WHERE account_id IS NOT NULL)`).Scan(&accounts, &adopted); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 || adopted != 0 {
		t.Fatalf("migration invented %d accounts and %d agent links", accounts, adopted)
	}
	// Every migrated agent keeps the rule that was always true: wait, with
	// nothing spent.
	var ruled int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE idle_policy <> 'wait' OR idle_after_seconds <> 0 OR idle_instruction <> '' OR idle_run_budget <> 0 OR idle_runs_used <> 0`).Scan(&ruled); err != nil {
		t.Fatal(err)
	}
	if ruled != 0 {
		t.Fatalf("migration invented an idle rule on %d agents", ruled)
	}
	var marked int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE sent_back_instruction_bytes IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Fatalf("migration invented %d sent-back boundaries", marked)
	}
	var archived int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE archived <> 0`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 0 {
		t.Fatalf("migration archived %d existing agents", archived)
	}
	if version == v10UserVersion {
		var appearances int
		if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE appearance = '1/2/3/4/5/6/7/8/9'`).Scan(&appearances); err != nil {
			t.Fatal(err)
		}
		if appearances != 1 {
			t.Fatalf("migration preserved %d v10 custom appearances, want 1", appearances)
		}
	}
	// Loopback pairings (the ones with terminal_input) gained
	// administration; the relay pairing did not. Every pending
	// challenge is treated the same way.
	for _, table := range []string{"browser_clients", "browser_pairing_challenges"} {
		var loopback, relay int
		if err := connection.QueryRowContext(ctx, `SELECT MIN(capability_mask), MAX(capability_mask) FROM `+table).Scan(&relay, &loopback); err != nil {
			t.Fatal(err)
		}
		if want := int(BrowserCapabilityKnownMask); loopback != want || relay != int(testRelayMask) {
			t.Fatalf("%s masks after migration: loopback=%d relay=%d, want %d and %d", table, loopback, relay, want, testRelayMask)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second open must find nothing to migrate.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated home: %v", err)
	}
	defer reopened.Close()
	again, err := reopened.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, got, err := inspectIdentity(ctx, again); err != nil || got != userVersion {
		t.Fatalf("reopened user_version = %d, %v, want %d", got, err, userVersion)
	}
	if after := snapshotRows(t, ctx, again); !reflect.DeepEqual(before, after) {
		t.Fatal("reopening a migrated home changed rows")
	}
}

func TestV15LibraryMigrationPreservesDelegation(t *testing.T) {
	path, _ := newLegacyDatabase(t, false, v15UserVersion)
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var prerequisite, conflict int
	if err := store.readers.QueryRow(`SELECT
		(SELECT COUNT(*) FROM task_prerequisites WHERE task_id = ? AND upstream_task_id = ? AND upstream_work_revision = 1 AND consumed_run_id IS NULL),
		(SELECT COUNT(*) FROM task_conflict_paths WHERE task_id = ? AND path = 'src/example.go')`, taskID(t, 210).Bytes(), taskID(t, 3).Bytes(), taskID(t, 210).Bytes()).Scan(&prerequisite, &conflict); err != nil {
		t.Fatal(err)
	}
	if prerequisite != 1 || conflict != 1 {
		t.Fatalf("migration lost delegation: prerequisite=%d conflict=%d", prerequisite, conflict)
	}
	for _, table := range []string{"project_content_revisions", "project_content_evidence", "task_content_references"} {
		var count int
		if err := store.readers.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("new optional table %s: count=%d error=%v", table, count, err)
		}
	}
}

func TestV16OutcomeMigrationPreservesLibrary(t *testing.T) {
	ctx := context.Background()
	path, _ := newLegacyDatabase(t, false, v16UserVersion)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, firstBody, err := store.LegacyContent(ctx, contentID(t, 212), 1)
	if err != nil || firstBody != "original definition" {
		t.Fatalf("original revision: %+v %v", first, err)
	}
	second, secondBody, err := store.LegacyContent(ctx, contentID(t, 212), 2)
	if err != nil || secondBody != "corrected definition" {
		t.Fatalf("corrected revision: %+v %v", second, err)
	}
	evidence, err := store.ListContentEvidence(ctx, projectID(t, 1), contentID(t, 212), mustRevision(t, 1), 0, 4)
	if err != nil || len(evidence.Items) != 1 || evidence.Items[0].Result != "passed" {
		t.Fatalf("retained evidence: %+v %v", evidence, err)
	}
	refs, err := store.TaskContentReferences(ctx, projectID(t, 1), taskID(t, 210), mustRevision(t, 1))
	if err != nil || len(refs) != 1 || refs[0].ContentRevision.Int64() != 1 {
		t.Fatalf("retained attachment: %+v %v", refs, err)
	}
	var count int
	if err := store.readers.QueryRow("SELECT COUNT(*) FROM project_outcome_revisions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("optional outcome table: %d %v", count, err)
	}
}

func TestCurrentHomeOpensWithoutMigration(t *testing.T) {
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open current home: %v", err)
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
		t.Fatal("opening a current home changed rows")
	}
}

func TestLegacyHomeWithUnknownObjectRefusesToMigrate(t *testing.T) {
	path, _ := newLegacyDatabase(t, false, legacyUserVersion, `CREATE TABLE stowaway (id INTEGER PRIMARY KEY)`)
	store, err := Open(context.Background(), path)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("Open = %v, want ErrForeignDatabase", err)
	}
	requireUnmigrated(t, path, legacyUserVersion)
}

func TestLegacyHomeWithForeignKeyViolationRefusesToMigrate(t *testing.T) {
	orphan := `INSERT INTO tasks(id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms)
		VALUES(X'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', X'01010101010101010101010101010101', X'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', X'abababababababababababababababab', 1, 't', '', 'queued', 0, NULL, NULL, NULL, 1, 6, 6)`
	path, _ := newLegacyDatabase(t, false, legacyUserVersion, orphan)
	store, err := Open(context.Background(), path)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open = %v, want ErrCorruptState", err)
	}
	requireUnmigrated(t, path, legacyUserVersion)
}

// requireUnmigrated proves a refused open left the home exactly as it was.
func requireUnmigrated(t *testing.T, path string, wantVersion int) {
	t.Helper()
	ctx := context.Background()
	pool, connection := openRawDatabase(t, path, false)
	defer pool.Close()
	defer connection.Close()
	if _, version, err := inspectIdentity(ctx, connection); err != nil || version != wantVersion {
		t.Fatalf("refused open left user_version = %d, %v, want %d", version, err, wantVersion)
	}
	// The objects each step rewrites still carry their earlier text.
	var stale, accounts, ruled int
	if err := connection.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('browser_clients', 'browser_pairing_challenges') AND sql LIKE '%BETWEEN 1 AND 15%'),
		(SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('accounts', 'accounts_provider_home_unique')),
		(SELECT COUNT(*) FROM sqlite_schema WHERE name = 'agents' AND sql LIKE '%idle_policy%')`).Scan(&stale, &accounts, &ruled); err != nil {
		t.Fatal(err)
	}
	wantRuled := 0
	if wantVersion >= v4UserVersion {
		wantRuled = 1
	}
	if ruled != wantRuled {
		t.Fatalf("refused open rewrote agents with the idle rule for v%d: got %d, want %d", wantVersion, ruled, wantRuled)
	}
	wantAccounts := 2 // the table and its unique index, absent from a v1 home
	if wantVersion == legacyUserVersion {
		wantAccounts = 0
	}
	wantStale := 2 // both browser tables still bound at 15, until v3
	if wantVersion >= priorUserVersion {
		wantStale = 0
	}
	if stale != wantStale || accounts != wantAccounts {
		t.Fatalf("refused open rewrote objects: pre-v3 browser tables=%d accounts objects=%d for v%d, want %d and %d", stale, accounts, wantVersion, wantStale, wantAccounts)
	}
}

// The relay pairing mask: every bit but terminal_input and administration.
const testRelayMask = BrowserCapabilityObserve | BrowserCapabilityPrivateHumanRequestDetail | BrowserCapabilityHumanActions

// newLegacyDatabase builds a populated home in the exact shape of an earlier
// user_version by downgrading a real one: every row is written through the
// public API, then the objects later versions changed are put back the way
// that version had them. The returned snapshot is every row, for comparison
// after the migration.
func newLegacyDatabase(t *testing.T, persistWAL bool, version int, extra ...string) (string, map[string][]string) {
	t.Helper()
	ctx := context.Background()
	store, path := newTestStore(t)
	seedDurableAuthority(t, store)
	// One loopback pairing and one relay pairing, each redeemed and each
	// with a second challenge still pending, written the way the daemon
	// writes them. Before v3 the loopback mask was every bit but
	// administration; from v3 it is the full mask.
	loopback := BrowserCapabilityObserve | BrowserCapabilityPrivateHumanRequestDetail | BrowserCapabilityHumanActions | BrowserCapabilityTerminalInput
	if version >= priorUserVersion {
		loopback = BrowserCapabilityKnownMask
	}
	boot := browserTestBoot(t, 1)
	for index, mask := range []BrowserCapabilityMask{loopback, testRelayMask} {
		redeemed := HashBrowserChallenge([]byte(fmt.Sprintf("redeemed %d", index)))
		pending := HashBrowserChallenge([]byte(fmt.Sprintf("pending %d", index)))
		for _, digest := range []BrowserChallengeDigest{redeemed, pending} {
			if _, err := store.CreateBrowserPairingChallenge(ctx, digest, boot, "https://app.example", mask, mustTime(t, 1), mustTime(t, 2)); err != nil {
				t.Fatalf("mint pairing challenge: %v", err)
			}
		}
		if _, err := store.RedeemBrowserPairingChallenge(ctx, redeemed, boot, "https://app.example", browserTestID(t, byte(10+index)), browserKey(t), mustTime(t, 1)); err != nil {
			t.Fatalf("redeem pairing challenge: %v", err)
		}
	}
	project := projectID(t, 1)
	if version >= v15UserVersion {
		_, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 210), ProjectID: project, AssignedAgentID: agentID(t, 2), IncarnationID: incarnationID(t, 211), Title: "dependent", Prerequisites: []TaskPrerequisite{{TaskID: taskID(t, 3), WorkRevision: mustRevision(t, 1)}}, ConflictPaths: []string{"src/example.go"}}, mustTime(t, 6))
		if err != nil {
			t.Fatal(err)
		}
	}
	if version >= v16UserVersion {
		id := contentID(t, 212)
		corruptSQL(t, store, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms) VALUES(?, ?, ?, 1, ?, '', 'original definition', 'operator:local', '', 0, 9)`, id.Bytes(), project.Bytes(), string(ContentAcceptanceScenario), "retained scenario")
		corruptSQL(t, store, `INSERT INTO project_content_revisions(id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms) VALUES(?, ?, ?, 2, ?, '', 'corrected definition', 'operator:local', '', 0, 10)`, id.Bytes(), project.Bytes(), string(ContentAcceptanceScenario), "retained scenario")
		content := ContentRevision{ID: id, ProjectID: project, Revision: mustRevision(t, 1)}
		evidence, err := ContentEvidenceIDFromBytes(bytes.Repeat([]byte{213}, IDBytes))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateContentEvidence(ctx, NewContentEvidence{ID: evidence, ProjectID: project, ContentID: content.ID, ContentRevision: content.Revision, TestedSource: "retained-source", Result: "passed", Evaluator: "operator:local"}, mustTime(t, 11)); err != nil {
			t.Fatal(err)
		}
		if err := store.AttachContentToTask(ctx, taskID(t, 210), project, content.ID, content.Revision, mustTime(t, 12)); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []struct {
		seed     byte
		provider Provider
	}{{20, ProviderShell}, {21, ProviderClaudeCode}} {
		spec := NewAgent{ID: agentID(t, agent.seed), ProjectID: project, Name: agent.provider.String() + "-agent", Role: RoleWorker, Provider: agent.provider, ToolBudgetLimit: 4}
		if agent.provider != ProviderShell {
			spec.Model = "opus"
			spec.ReasoningEffort = "high"
		}
		created, err := store.CreateAgent(ctx, spec, mustTime(t, 6))
		if err != nil {
			t.Fatalf("create %s agent: %v", agent.provider, err)
		}
		if version == v10UserVersion && agent.seed == 20 {
			appearance := AgentAppearance{Skin: 1, Hair: 2, HairColour: 3, Face: 4, Outfit: 5, ClothesColour: 6, Shoes: 7, Tool: 8, Headwear: 9}
			if _, err := store.UpdateAgent(ctx, created.ID, created.Revision, AgentPatch{Appearance: &appearance}, mustTime(t, 6)); err != nil {
				t.Fatalf("set v10 appearance: %v", err)
			}
		}
	}
	if _, err := store.SetDispatch(ctx, mustRevision(t, 1), true, mustTime(t, 7)); err != nil {
		t.Fatalf("SetDispatch: %v", err)
	}
	// The remaining invalidation kinds have no public writer that reaches them
	// from this fixture, so append them the way the log itself is written.
	for _, kind := range []string{"change", "run", "human_request"} {
		if _, err := store.writer.Exec(`INSERT INTO invalidations(sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted)
			SELECT next_invalidation_sequence, 8, ?, X'0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c', 1, 0 FROM factory`, kind); err != nil {
			t.Fatalf("append %s invalidation: %v", kind, err)
		}
		if _, err := store.writer.Exec(`UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence + 1, updated_at_ms = 8 WHERE singleton = 1`); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close seeded store: %v", err)
	}

	pool, connection := openRawDatabase(t, path, persistWAL)
	if err := setForeignKeys(ctx, connection, false); err != nil {
		t.Fatal(err)
	}
	wantStatements, migrates := migratableSchema(version)
	if !migrates {
		t.Fatalf("no migratable schema for v%d", version)
	}
	legacy := expectedSchemaOf(wantStatements)
	statements := []string{"BEGIN IMMEDIATE"}
	statements = append(statements, extra...)
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare legacy home: %v", err)
		}
	}
	// Every version before v4 has agents without the idle rule; the columns
	// the fixture carries down are the version's own.
	agentColumnsFor := testAgentColumnsV3
	if version == legacyUserVersion {
		agentColumnsFor = testAgentColumns
	} else if version >= v4UserVersion {
		agentColumnsFor = testAgentColumnsV4
	}
	if version == v9UserVersion {
		agentColumnsFor = v7AgentColumns
	} else if version == v10UserVersion {
		agentColumnsFor = v10AgentColumns
	} else if version >= v11UserVersion {
		agentColumnsFor = v11AgentColumns
	}
	if version != legacyUserVersion {
		if err := rebuildTable(ctx, connection, legacy, "invalidations", testInvalidationColumns, "invalidations_entity_revision_unique", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := rebuildTable(ctx, connection, legacy, "agents", agentColumnsFor, "agents_id_project_unique", "", ""); err != nil {
		t.Fatal(err)
	}
	taskColumnsFor := testTaskColumns
	if version >= v5UserVersion {
		taskColumnsFor = testTaskColumnsV5
	}
	if err := rebuildTable(ctx, connection, legacy, "tasks", taskColumnsFor, "tasks_id_project_incarnation_unique", "tasks_incarnation_unique", "tasks_canonical_queue", "", ""); err != nil {
		t.Fatal(err)
	}
	// Every version before v14 bound a Git-free tree by its manifest facts,
	// which a prepared, available or retained row had to carry.
	if version < v14UserVersion {
		if err := rebuildTable(ctx, connection, legacy, "changes", testChangeColumns, "changes_id_project_task_incarnation_unique", "changes_task_incarnation_unique", "tree_digest, entry_count, total_bytes, tree_dev, tree_inode",
			"CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE zeroblob(32) END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 1 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 1 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 0 END, CASE WHEN prepared_at_ms IS NULL THEN NULL ELSE 2 END"); err != nil {
			t.Fatal(err)
		}
	}

	if version < priorUserVersion {
		if err := rebuildTable(ctx, connection, legacy, "browser_pairing_challenges", previousPairingChallengeColumns, "", "", ""); err != nil {
			t.Fatal(err)
		}
		if err := rebuildTable(ctx, connection, legacy, "browser_clients", previousBrowserClientColumns, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if version <= v8UserVersion {
		columns := "id, run_id, idempotency_key, kind, reason_code, question_text, status, delivery_id, delivery_started_at_ms, resolution_kind, closed_at_ms, revision, created_at_ms, updated_at_ms"
		if err := rebuildTable(ctx, connection, legacy, "human_requests", columns, "human_requests_one_unresolved_per_run", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if version <= v7UserVersion {
		if err := rebuildTable(ctx, connection, legacy, "projects", "id, name, root, verification_policy, revision, created_at_ms, updated_at_ms", "projects_root_unique", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if version >= v16UserVersion && version < v18UserVersion {
		columns := "id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms"
		if err := rebuildTable(ctx, connection, legacy, "project_content_revisions", columns, "project_content_revisions_project_kind", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	downgrade := []string{"DROP TABLE intake_task_bindings", "DROP TABLE intake_source_trusted_logins", "DROP TABLE intake_acceptances", "DROP TABLE intake_sources", "DROP TABLE repository_source_identities", "DROP TABLE content_repository_bindings", "DROP TABLE task_repository_bindings", "DROP TABLE project_repositories", "DROP TABLE continuations", fmt.Sprintf("PRAGMA user_version = %d", version), "COMMIT"}
	if version < v19UserVersion {
		downgrade = append([]string{"DROP TABLE terminal_diagnostics"}, downgrade...)
	}
	if version < v17UserVersion {
		downgrade = append([]string{"DROP TABLE project_outcome_revisions"}, downgrade...)
	}
	if version < v16UserVersion {
		downgrade = append([]string{"DROP TABLE task_content_references", "DROP TABLE project_content_evidence", "DROP TABLE project_content_revisions"}, downgrade...)
	}
	if version < v15UserVersion {
		downgrade = append([]string{"DROP TABLE task_conflict_paths", "DROP INDEX task_prerequisites_upstream", "DROP TABLE task_prerequisites"}, downgrade...)
	}
	if version < v7UserVersion {
		downgrade = append([]string{"DROP TABLE peer_questions"}, downgrade...)
	}
	if version < v6UserVersion {
		downgrade = append([]string{"DROP TABLE task_interventions", "DROP TABLE overseer_wake_cursors"}, downgrade...)
	}
	if version == legacyUserVersion {
		if err := rebuildTable(ctx, connection, legacy, "invalidations", testInvalidationColumns, "invalidations_entity_revision_unique", "", ""); err != nil {
			t.Fatal(err)
		}
		downgrade = append([]string{"DROP TABLE accounts"}, downgrade...)
	}
	for _, statement := range downgrade {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatalf("downgrade to v%d: %v", version, err)
		}
	}
	if len(extra) == 0 {
		if err := validateSchemaVersion(ctx, connection, version, wantStatements); err != nil {
			t.Fatalf("fixture is not an exact v%d home: %v", version, err)
		}
	}
	requireLegacyPopulation(t, ctx, connection)
	before := snapshotRows(t, ctx, connection)
	if err := errors.Join(connection.Close(), pool.Close()); err != nil {
		t.Fatal(err)
	}
	// SQLite creates sidecars from the process umask; a real home has them 0600.
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			if err := os.Chmod(sidecar, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path, before
}

func openRawDatabase(t *testing.T, path string, persistWAL bool) (*sql.DB, *sql.Conn) {
	t.Helper()
	pool, err := sql.Open(driverName, configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	connection, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(errors.Join(err, pool.Close()))
	}
	if !persistWAL {
		return pool, connection
	}
	// The operational store keeps its WAL sidecars across shutdown, so a home
	// that a stopped daemon left behind still has them.
	if err := connection.Raw(func(driverConnection any) error {
		_, err := driverConnection.(sqliteDriver.Conn).Raw().FileControl("", sqlite3.FCNTL_PERSIST_WAL, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return pool, connection
}

// testAgentColumns is the v1 agents column list spelled out, used to build the
// fixture and to read it back. Both have to stay independent of
// legacyAgentColumns: sharing that constant would drop a column from the
// fixture and from the comparison at the same time, hiding a column the
// migration stopped copying.
const testAgentColumns = `id, project_id, name, role, provider, model, reasoning_effort, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`

// testAgentColumnsV3 is the v2/v3 agent row: v1 plus account_id, before the idle rule.
const testAgentColumnsV3 = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`

const testAgentColumnsV4 = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used`

const v10AgentColumns = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, appearance, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`

const v11AgentColumns = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, archived, appearance, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`

// testTaskColumns is the v4 task row, before a send-back boundary was stored.
const testTaskColumns = `id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms`

const testTaskColumnsV5 = `id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, sent_back_instruction_bytes, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms`

// testChangeColumns is the Change row every version has had, spelled here so
// the fixture and its comparison cannot lose a column with the migration's
// list. The v13 tree fact columns are added by the fixture and dropped by v14.
const testChangeColumns = `id, project_id, task_id, task_incarnation_id, phase, object_format, base_commit, repository_dev, repository_inode, prepared_at_ms, available_at_ms, settled_run_id, revision, created_at_ms, updated_at_ms`

// testInvalidationColumns is the invalidation row every version has had,
// spelled here so the fixture cannot lose a column with the migration's list.
const testInvalidationColumns = `sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted`

var testBrowserColumns = map[string]string{
	"browser_pairing_challenges": strings.ReplaceAll(previousPairingChallengeColumns, "capability_mask, ", ""),
	"browser_clients":            strings.ReplaceAll(previousBrowserClientColumns, "capability_mask, ", ""),
}

// snapshotRows reads every v1 table, agents through the list above because the
// added account_id makes SELECT * differ either side of the migration.
func snapshotRows(t *testing.T, ctx context.Context, connection *sql.Conn) map[string][]string {
	return snapshotSchemaRows(t, ctx, connection, legacySchemaStatements(), true)
}

func snapshotSchemaRows(t *testing.T, ctx context.Context, connection *sql.Conn, statements []string, legacyColumns bool) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	for name, object := range expectedSchemaOf(statements) {
		if object.kind != "table" {
			continue
		}
		columns := "*"
		switch name {
		case "agents":
			columns = testAgentColumns
		case "tasks":
			columns = testTaskColumns
		case "browser_pairing_challenges", "browser_clients":
			// The v3 migration rewrites masks on purpose; the migration test
			// asserts them separately.
			columns = testBrowserColumns[name]
		}
		if !legacyColumns {
			columns = "*"
			if name == "agents" {
				columns = v7AgentColumns
			}
		}
		if name == "projects" {
			columns = "id, name, root, verification_policy, revision, created_at_ms, updated_at_ms"
		}
		if name == "human_requests" {
			columns = "id, run_id, idempotency_key, kind, reason_code, question_text, status, delivery_id, delivery_started_at_ms, resolution_kind, closed_at_ms, revision, created_at_ms, updated_at_ms"
		}
		if name == "changes" {
			columns = testChangeColumns
		}
		rows, err := connection.QueryContext(ctx, "SELECT "+columns+" FROM "+name+" ORDER BY 1")
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

// requireLegacyPopulation keeps the migration proof from passing on an empty
// database: every table the migration touches has to carry real rows.
func requireLegacyPopulation(t *testing.T, ctx context.Context, connection *sql.Conn) {
	t.Helper()
	var projects, agents, providers, tasks, runs, kinds, clients, challenges, events, resources, sessions int
	if err := connection.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM projects), (SELECT COUNT(*) FROM agents), (SELECT COUNT(DISTINCT provider) FROM agents),
		(SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM runs), (SELECT COUNT(DISTINCT entity_kind) FROM invalidations),
		(SELECT COUNT(DISTINCT capability_mask) FROM browser_clients), (SELECT COUNT(DISTINCT capability_mask) FROM browser_pairing_challenges),
		(SELECT COUNT(*) FROM browser_security_events WHERE client_id IS NOT NULL),
		(SELECT COUNT(*) FROM resources), (SELECT COUNT(*) FROM terminal_sessions)`).
		Scan(&projects, &agents, &providers, &tasks, &runs, &kinds, &clients, &challenges, &events, &resources, &sessions); err != nil {
		t.Fatal(err)
	}
	if projects < 1 || agents < 3 || providers != 3 || tasks < 1 || runs < 1 || kinds != 7 || clients != 2 || challenges != 2 || events < 1 || resources < 1 || sessions < 1 {
		t.Fatalf("thin fixture: projects=%d agents=%d providers=%d tasks=%d runs=%d invalidation kinds=%d client masks=%d challenge masks=%d client events=%d resources=%d terminal sessions=%d",
			projects, agents, providers, tasks, runs, kinds, clients, challenges, events, resources, sessions)
	}
}

// TestSchemaDigestsArePinned trips on any schema edit. Both sets need a pin:
// legacySchemaStatements derives every unchanged statement live from
// schemaStatements, so an edit there rewrites what v1 is claimed to have been
// and real v1 homes stop opening, while an edit to an object the v1 derivation
// drops or replaces (accounts, agents, invalidations) moves the target without
// touching the v1 digest and every migrated home is refused on the next start.
// A schema change re-pins both, and re-pinning without freezing the replaced
// text and extending the migration ships the outage this migration exists to
// fix.
func TestSchemaDigestsArePinned(t *testing.T) {
	for _, pin := range []struct {
		name       string
		statements []string
		digest     string
	}{
		{"current", schemaStatements, "eeb13a93e6195caed12237d22d706fd176a1964734d3a25b6eebcfe386bf2fb4"},
		{"v24", v24SchemaStatements(), "063bf2d0c978fc630bf929104a8def8f4abca76fd17fbdaf44d130a397a77c48"},
		{"v23", v23SchemaStatements(), "301b6c8046552e1c5fb669c92001a449d215315c1a565e6526826b7fc5b0fa12"},
		{"v22", v22SchemaStatements(), "3dd09f64e28fcb94ba6999129defe92286a10907e8ae5b6e4be4b37fb3281478"},
		{"v21", v21SchemaStatements(), "17306bf8a7cae30e75dc0e3d85574a1ef96a45c3abfd63d2ca902fbb79c10092"},
		{"v20", v20SchemaStatements(), "c6b2b517bc127ee3bad157cd51b0b072eed009403c319c7f4f10cd6fe1b65036"},
		{"v19", v19SchemaStatements(), "d0334df36c119ed0311c1728656742999f4583366075dc78dc9a7b76dee4c35d"},
		{"v18", v18SchemaStatements(), "a3af3c17a532d6b7b324507554a00080ec1c26d61831e3c51f1f1a6ac5279457"},
		{"v17", v17SchemaStatements(), "8e566e2483f36de3f9b9d13722bbab6fb58293787b77f201ab16d98c7084ff54"},
		{"v16", v16SchemaStatements(), "2547d01bcfd2878245cb3e6d27c0116cb6ebb9a032bf4816834b2138892d8705"},
		{"v15", v15SchemaStatements(), "4657aab650b20fbf6ff2dac4e57d334d954321da14b5e14224aab200247cb4dc"},
		{"v13", v13SchemaStatements(), "f38d4c5ac959eb2c3b23e3c0ace78faa1859201688cb12314c0c4fa721db56db"},
		{"v12", v12SchemaStatements(), "78ff7808dc146c35383329f73c94559172e824e0b72484bc56db48291dbadefa"},
		{"v11", v11SchemaStatements(), "06e43f9cc643630e66b9f2606549735d9d170cee25da54fb072b8df14ba48bcd"},
		{"v10", v10SchemaStatements(), "af5c61224274d2c62e8b78036e911239aa98d8b40154811cb9c4788ad224a603"},
		{"v9", v9SchemaStatements(), "049dc8ff317e31a86157fd5366579954581a4468caaca63c5dd95ee2958ab4cb"},
		{"v8", v8SchemaStatements(), "45606d5fa2b054c4ccee79c55f20b184f25ee0860ec5817099c0582174df676b"},
		{"v7", v7SchemaStatements(), "c6793e1552a878dfff3fb4efc4179ba6343a26f122337580b6ac558e8a6bfedf"},
		{"v6", v6SchemaStatements(), "4063acf5233e3aaf29fe932259283622df543733b56a7e78a359bd73ce85da8c"},
		{"v4", v4SchemaStatements(), "6eb8be2af2f3efc8ed7d40ecf9bd1ec316675e39ad11fb8b0827a228e9232cf1"},
		{"v3", priorSchemaStatements(), "2d5319a0afce6206d963631465833bc5f25d0f2261537f4f33c92a8e38a36009"},
		{"v2", previousSchemaStatements(), "6a1de54c3fcad5f6770c6d80b91fda3f914e8b236f34d62bb875a7f4efde347c"},
		{"v1", legacySchemaStatements(), "63a444a2fe57a994b712bfe5b56764d684b2cb3ed73d7324465d894107f96f33"},
	} {
		sum := sha256.Sum256([]byte(strings.Join(pin.statements, "\n")))
		if got := hex.EncodeToString(sum[:]); got != pin.digest {
			t.Errorf("%s schema digest = %s, want %s", pin.name, got, pin.digest)
		}
	}
}

// TestLegacyHomeWithBrokenDurableStateRollsBackAndRefuses is the only refusal
// that reaches inside the migration transaction: the two above are rejected by
// the preflight, on its disposable copy, before any pool exists.
func TestLegacyHomeWithBrokenDurableStateRollsBackAndRefuses(t *testing.T) {
	for _, version := range []int{legacyUserVersion, previousUserVersion, priorUserVersion, v4UserVersion, v5UserVersion, v6UserVersion, v7UserVersion, v8UserVersion, v9UserVersion, v10UserVersion, v12UserVersion, v13UserVersion, v14UserVersion, v15UserVersion, v16UserVersion, v17UserVersion, v18UserVersion, v19UserVersion} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			testLegacyHomeWithBrokenDurableStateRollsBackAndRefuses(t, version)
		})
	}
}

func testLegacyHomeWithBrokenDurableStateRollsBackAndRefuses(t *testing.T, version int) {
	ctx := context.Background()
	// An invalidation head that no longer matches the log passes the exact
	// schema, the integrity check and foreign_key_check that the preflight
	// runs, and fails only the durable-control pass inside the transaction.
	path, before := newLegacyDatabase(t, false, version, `UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence + 5 WHERE singleton = 1`)
	evidence := captureDatabaseEvidence(t, path)
	store, err := Open(ctx, path)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Open = %v, want ErrCorruptState", err)
	}
	assertDatabaseEvidenceUnchanged(t, path, evidence)
	requireUnmigrated(t, path, version)

	// Repaired, the same home migrates and keeps the rows it always had.
	pool, connection := openRawDatabase(t, path, false)
	if _, err := connection.ExecContext(ctx, `UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence - 5 WHERE singleton = 1`); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(connection.Close(), pool.Close()); err != nil {
		t.Fatal(err)
	}
	repaired, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open repaired home: %v", err)
	}
	defer repaired.Close()
	reader, err := repaired.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, version, err := inspectIdentity(ctx, reader); err != nil || version != userVersion {
		t.Fatalf("repaired user_version = %d, %v, want %d", version, err, userVersion)
	}
	after := snapshotRows(t, ctx, reader)
	before["factory"] = after["factory"] // the repair rewrote the invalidation head
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration after repair did not preserve every row")
	}
}

// TestRefusedMigrationReturnsTheWriterConnection covers what the rollback is
// for. Closing the connection would roll the transaction back anyway, but a
// connection left mid-transaction is destroyed rather than returned, and the
// operational writer set is sealed at activation and cannot mint a
// replacement.
func TestRefusedMigrationReturnsTheWriterConnection(t *testing.T) {
	ctx := context.Background()
	path, _ := newLegacyDatabase(t, false, legacyUserVersion, `UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence + 5 WHERE singleton = 1`)
	store, err := openPools(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.migrateLegacy(ctx); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("migrateLegacy = %v, want ErrCorruptState", err)
	}
	if stats := store.writer.Stats(); stats.OpenConnections != 1 || stats.Idle != 1 {
		t.Fatalf("refused migration did not return the writer connection: open=%d idle=%d", stats.OpenConnections, stats.Idle)
	}
}
