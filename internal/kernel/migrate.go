package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Open migrates the three earlier versions. v40 folds the continuations
// table onto the human request each row awaited; v39 names the retryable
// failure code 'transient' where v38 named 'runner_exit', which nothing ever
// wrote; v38 added the specialist columns (agents.idle_wake_on,
// projects.specialist_runs and projects.specialist_open_proposals) to v37
// and changed nothing else.
const (
	v37UserVersion = 37
	v38UserVersion = 38
	v39UserVersion = 39
)

// v39Continuations are the statements v40 dropped, which stood before the
// invalidations table.
var v39Continuations = []string{
	`CREATE TABLE continuations (
    id BLOB PRIMARY KEY CHECK (length(id) = 16 AND id <> zeroblob(16)),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    task_id BLOB NOT NULL CHECK (length(task_id) = 16) REFERENCES tasks(id),
    task_incarnation_id BLOB NOT NULL CHECK (length(task_incarnation_id) = 16),
    work_revision INTEGER NOT NULL CHECK (work_revision >= 1),
    context_digest BLOB NOT NULL CHECK (length(context_digest) = 32),
    condition_kind TEXT NOT NULL CHECK (condition_kind IN ('human_request', 'peer_question', 'handoff', 'dependency', 'invalidation')),
    condition_id BLOB NOT NULL CHECK (length(condition_id) = 16 AND condition_id <> zeroblob(16)),
    condition_revision INTEGER NOT NULL CHECK (condition_revision >= 1),
    state TEXT NOT NULL CHECK (state IN ('waiting', 'queued', 'resolved', 'cancelled')),
    resolution_detail TEXT CHECK (resolution_detail IS NULL OR length(CAST(resolution_detail AS BLOB)) BETWEEN 1 AND 8192),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    resolved_at_ms INTEGER CHECK (resolved_at_ms IS NULL OR resolved_at_ms = updated_at_ms),
    FOREIGN KEY (task_id, project_id, task_incarnation_id) REFERENCES tasks(id, project_id, incarnation_id),
    CHECK ((state = 'waiting' AND resolution_detail IS NULL AND resolved_at_ms IS NULL) OR
           (state = 'queued' AND resolution_detail IS NOT NULL AND resolved_at_ms IS NOT NULL) OR
           (state IN ('resolved', 'cancelled') AND resolution_detail IS NOT NULL AND resolved_at_ms IS NOT NULL))
) STRICT, WITHOUT ROWID`,
	`CREATE UNIQUE INDEX continuations_one_waiting_per_condition ON continuations(task_id, task_incarnation_id, work_revision, condition_kind, condition_id) WHERE state = 'waiting'`,
	`CREATE INDEX continuations_admission_queue ON continuations(state, updated_at_ms, id)`,
}

func legacySchemaStatements(version int) []string {
	pairs := []string{humanContinuationColumns, "", "'peer_question')),", "'peer_question', 'continuation')),"}
	if version < v39UserVersion {
		pairs = append(pairs, "'transient'", "'runner_exit'")
	}
	if version == v37UserVersion {
		pairs = append(pairs, agentWakeOnColumn, "", projectSpecialistColumns, "")
	}
	replacer := strings.NewReplacer(pairs...)
	at := slices.IndexFunc(schemaStatements, func(statement string) bool { return strings.HasPrefix(statement, "CREATE TABLE invalidations") })
	statements := slices.Insert(slices.Clone(schemaStatements), at, v39Continuations...)
	for index, statement := range statements {
		statements[index] = replacer.Replace(statement)
	}
	return statements
}

// validateOpenableSnapshot accepts a current database or an exact v37, v38 or
// v39 one, whose durable controls are checked inside the migration before it
// commits.
func validateOpenableSnapshot(ctx context.Context, connection *sql.Conn) error {
	if _, version, err := inspectIdentity(ctx, connection); err != nil {
		return err
	} else if version >= v37UserVersion && version < userVersion {
		if err := validateSchemaVersion(ctx, connection, version, legacySchemaStatements(version)); err != nil {
			return err
		}
		if err := validateIntegrity(ctx, connection); err != nil {
			return err
		}
		return validateContentGitPins(ctx, connection)
	}
	return validateDatabaseSnapshot(ctx, connection)
}

func validateContentGitPins(ctx context.Context, connection *sql.Conn) error {
	var invalid bool
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_content_revisions WHERE commit_oid IS NULL OR body <> '')`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("%w: project content contains a non-Git revision", ErrCorruptState)
	}
	return nil
}

// migrateLegacy takes an exact v37, v38 or v39 home to the current schema in one
// transaction, or leaves it byte-untouched and refuses; it refuses any other
// earlier version. Open calls it with the writer before the store is
// published, and before refreshing its pinned sidecar facts, which the
// sidecar binding relies on. The migration is one way: the rollback plan for
// an operator home is the pre-upgrade copy docs/install.md tells them to take.
func (store *Store) migrateLegacy(ctx context.Context) error {
	connection, err := store.writerConnection(ctx)
	if err != nil {
		return err
	}
	appID, version, err := inspectIdentity(ctx, connection)
	if err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	switch version {
	case userVersion:
		return connection.Close()
	case v37UserVersion, v38UserVersion, v39UserVersion:
	default:
		cause := ErrForeignDatabase
		if appID == applicationID && version > userVersion {
			cause = ErrNewerSchema
		}
		return errors.Join(fmt.Errorf("%w: home is at user_version %d, this build requires %d", cause, version, userVersion), connection.Close())
	}
	if err := migrateTransaction(ctx, connection, func(ctx context.Context, connection *sql.Conn) error {
		return migrateFrom(ctx, connection, version)
	}); err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	return connection.Close()
}

// migrateFrom adds columns and rewrites checks in place: no row can violate
// a rewritten check, since the value it drops was never written or its rows
// are moved or pruned first. Bumping schema_version makes every connection,
// this one and the open readers, load the new text, as SQLite's own ALTER
// TABLE procedure does.
func migrateFrom(ctx context.Context, connection *sql.Conn, version int) error {
	if err := validateSchemaVersion(ctx, connection, version, legacySchemaStatements(version)); err != nil {
		return err
	}
	// Every continuation awaited a human request of its own run's task work
	// revision, which is all the request needs to stand in for it.
	var stray bool
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM continuations AS c WHERE c.condition_kind <> 'human_request'
		OR (SELECT count(*) FROM continuations AS d WHERE d.condition_id = c.condition_id) > 1
		OR NOT EXISTS(SELECT 1 FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id WHERE h.id = c.condition_id AND r.task_id = c.task_id AND r.task_incarnation_id = c.task_incarnation_id AND r.admitted_task_work_revision = c.work_revision))`).Scan(&stray); err != nil {
		return err
	}
	if stray {
		return fmt.Errorf("%w: a continuation does not match its human request", ErrCorruptState)
	}
	var statements []string
	if version == v37UserVersion {
		columns := strings.Split(projectSpecialistColumns, ", ")[1:]
		statements = append(statements,
			"ALTER TABLE agents ADD COLUMN "+strings.TrimPrefix(agentWakeOnColumn, ", "),
			"ALTER TABLE projects ADD COLUMN "+columns[0],
			"ALTER TABLE projects ADD COLUMN "+columns[1])
	}
	continuation, reply, _ := strings.Cut(strings.TrimPrefix(humanContinuationColumns, ", "), ", continuation_reply ")
	statements = append(statements,
		"ALTER TABLE human_requests ADD COLUMN "+continuation,
		"ALTER TABLE human_requests ADD COLUMN continuation_reply "+reply,
		`UPDATE human_requests SET continuation = c.state, continuation_reply = iif(c.state IN ('queued', 'resolved'), c.resolution_detail, NULL) FROM continuations AS c WHERE c.condition_id = human_requests.id`,
		"DROP TABLE continuations",
		// Only the log's sequences are read. A continuation entry keeps its
		// sequence as a human request entry under the continuation's own
		// identifier, which no request shares.
		`UPDATE invalidations SET entity_kind = 'human_request' WHERE entity_kind = 'continuation'`)
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var schemaVersion int
	if err := connection.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		return err
	}
	for _, statement := range []string{
		"PRAGMA writable_schema = ON",
		`UPDATE sqlite_schema SET sql = replace(sql, '''runner_exit''', '''transient''') WHERE type = 'table' AND name = 'runs'`,
		`UPDATE sqlite_schema SET sql = replace(sql, ', ''continuation''))', '))') WHERE type = 'table' AND name = 'invalidations'`,
		"PRAGMA writable_schema = OFF",
		fmt.Sprintf("PRAGMA schema_version = %d", schemaVersion+1),
		fmt.Sprintf("PRAGMA user_version = %d", userVersion),
	} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	// The earlier retryable failures take the code, so finalization and the
	// retry-history match treat them as before. A terminal run's code is its
	// proposal's.
	if _, err := connection.ExecContext(ctx, `UPDATE runs SET proposal_code = 'transient', terminal_code = iif(terminal_code IS NULL, NULL, 'transient')
		WHERE ? AND (proposal_code = 'protocol' AND proposal_detail IN (?, ?) OR proposal_code = 'provider_exit' AND proposal_detail = ?)`,
		version < v39UserVersion, NeverStartedRunDetail, OverseerRunLimitDetail, ProviderCapacityRunDetail); err != nil {
		return err
	}
	if err := validateExactSchema(ctx, connection); err != nil {
		return err
	}
	return validateDurableControls(ctx, connection)
}

func migrateTransaction(ctx context.Context, connection *sql.Conn, step func(context.Context, *sql.Conn) error) (resultErr error) {
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("roll back schema migration: %w", err))
		}
	}()
	if err := step(ctx, connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}
