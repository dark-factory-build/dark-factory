package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// v34UserVersion is the one earlier version Open migrates: v35 dropped four
// tables nothing read or wrote. v34Dropped is their text exactly as v34 had
// it, so a v34 home is recognised by its whole schema.
const v34UserVersion = 34

var v34Dropped = []string{
	`CREATE TABLE intake_legacy_migrations (
    source_id BLOB PRIMARY KEY REFERENCES intake_sources(id),
    github_repository_id INTEGER NOT NULL CHECK (github_repository_id > 0),
    plan_hash BLOB NOT NULL CHECK (length(plan_hash) = 32),
    config_hash BLOB NOT NULL CHECK (length(config_hash) = 32),
    journal_hash BLOB NOT NULL CHECK (length(journal_hash) = 32),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0)
) STRICT, WITHOUT ROWID`,
	`CREATE TABLE intake_legacy_suppressions (
    github_repository_id INTEGER NOT NULL CHECK (github_repository_id > 0),
    issue_number INTEGER NOT NULL CHECK (issue_number > 0),
    issue_node_id TEXT NOT NULL CHECK (length(CAST(issue_node_id AS BLOB)) BETWEEN 1 AND 256),
    project_id BLOB NOT NULL REFERENCES projects(id),
    repository_id BLOB NOT NULL REFERENCES project_repositories(id),
    content_hash BLOB NOT NULL CHECK (length(content_hash) = 32),
    migration_source_id BLOB NOT NULL REFERENCES intake_legacy_migrations(source_id),
    has_history INTEGER NOT NULL CHECK (has_history IN (0, 1)),
    historical_content_hash BLOB CHECK (historical_content_hash IS NULL OR (length(historical_content_hash) = 32 AND has_history = 1)),
    legacy_task_id BLOB REFERENCES tasks(id),
    legacy_incarnation_id BLOB CHECK (legacy_incarnation_id IS NULL OR length(legacy_incarnation_id) = 16),
    legacy_task_revision INTEGER CHECK (legacy_task_revision IS NULL OR legacy_task_revision >= 1),
    CHECK ((legacy_task_id IS NULL AND legacy_incarnation_id IS NULL AND legacy_task_revision IS NULL) OR (legacy_task_id IS NOT NULL AND legacy_incarnation_id IS NOT NULL AND legacy_task_revision IS NOT NULL)),
    PRIMARY KEY (github_repository_id, issue_number, issue_node_id, project_id, repository_id)
) STRICT, WITHOUT ROWID`,
	`CREATE TABLE intake_acceptance_reviews (
    acceptance_id BLOB NOT NULL REFERENCES intake_acceptances(id),
    reviewed_at_ms INTEGER NOT NULL CHECK (reviewed_at_ms >= 0),
    PRIMARY KEY(acceptance_id, reviewed_at_ms)
) STRICT, WITHOUT ROWID`,
	`CREATE TABLE project_content_evidence (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    content_id BLOB NOT NULL CHECK (length(content_id) = 16),
    content_revision INTEGER NOT NULL CHECK (content_revision >= 1),
    tested_source TEXT NOT NULL CHECK (length(CAST(tested_source AS BLOB)) BETWEEN 1 AND 4096),
    environment TEXT NOT NULL CHECK (length(CAST(environment AS BLOB)) <= 4096),
    result TEXT NOT NULL CHECK (result IN ('passed', 'failed', 'incomplete', 'not_run')),
    location TEXT NOT NULL CHECK (length(CAST(location AS BLOB)) <= 4096),
    evaluator TEXT NOT NULL CHECK (length(CAST(evaluator AS BLOB)) BETWEEN 1 AND 256),
    judgment TEXT NOT NULL CHECK (length(CAST(judgment AS BLOB)) <= 8192),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    FOREIGN KEY (content_id, content_revision) REFERENCES project_content_revisions(id, revision)
) STRICT, WITHOUT ROWID`,
	`CREATE INDEX project_content_evidence_content ON project_content_evidence(project_id, content_id, content_revision, id)`,
}

func v34SchemaStatements() []string {
	return append(slices.Clone(schemaStatements), v34Dropped...)
}

// validateOpenableSnapshot accepts a current database or an exact v34 one,
// whose durable controls are checked inside the migration before it commits.
func validateOpenableSnapshot(ctx context.Context, connection *sql.Conn) error {
	if _, version, err := inspectIdentity(ctx, connection); err != nil {
		return err
	} else if version == v34UserVersion {
		if err := validateSchemaVersion(ctx, connection, version, v34SchemaStatements()); err != nil {
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

// migrateLegacy takes an exact v34 home to the current schema in one
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
	_, version, err := inspectIdentity(ctx, connection)
	if err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	switch version {
	case userVersion:
		return connection.Close()
	case v34UserVersion:
	default:
		return errors.Join(fmt.Errorf("%w: home is at user_version %d, this build requires %d", ErrForeignDatabase, version, userVersion), connection.Close())
	}
	if err := migrateTransaction(ctx, connection, migrateV34); err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	return connection.Close()
}

func migrateV34(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v34UserVersion, v34SchemaStatements()); err != nil {
		return err
	}
	// The suppressions reference intake_legacy_migrations, so they go first.
	for _, statement := range []string{"DROP TABLE intake_legacy_suppressions", "DROP TABLE intake_legacy_migrations", "DROP TABLE project_content_evidence", "DROP TABLE intake_acceptance_reviews", fmt.Sprintf("PRAGMA user_version = %d", userVersion)} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return err
		}
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
