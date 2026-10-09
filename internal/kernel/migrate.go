package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Open migrates the two earlier versions. v38 names the retryable failure
// code 'transient' where v37 named 'runner_exit', which nothing ever wrote;
// v37 added task_automatic_events to v36 and changed nothing else.
const (
	v36UserVersion = 36
	v37UserVersion = 37
)

func legacySchemaStatements(version int) []string {
	statements := slices.DeleteFunc(slices.Clone(schemaStatements), func(statement string) bool {
		return version == v36UserVersion && statement == taskAutomaticEventsTable
	})
	for index, statement := range statements {
		statements[index] = strings.ReplaceAll(statement, "'transient'", "'runner_exit'")
	}
	return statements
}

// validateOpenableSnapshot accepts a current database or an exact v36 or v37
// one, whose durable controls are checked inside the migration before it
// commits.
func validateOpenableSnapshot(ctx context.Context, connection *sql.Conn) error {
	if _, version, err := inspectIdentity(ctx, connection); err != nil {
		return err
	} else if version == v36UserVersion || version == v37UserVersion {
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

// migrateLegacy takes an exact v36 or v37 home to the current schema in one
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
	case v36UserVersion, v37UserVersion:
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

// migrateFrom rewrites the runs table's failure-code checks in place: the
// value it replaces was never written, so no row can violate the new text.
// Bumping schema_version makes every connection, this one and the open
// readers, load the new text, as SQLite's own ALTER TABLE procedure does.
func migrateFrom(ctx context.Context, connection *sql.Conn, version int) error {
	if err := validateSchemaVersion(ctx, connection, version, legacySchemaStatements(version)); err != nil {
		return err
	}
	if version == v36UserVersion {
		if _, err := connection.ExecContext(ctx, taskAutomaticEventsTable); err != nil {
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
		WHERE proposal_code = 'protocol' AND proposal_detail IN (?, ?) OR proposal_code = 'provider_exit' AND proposal_detail = ?`,
		NeverStartedRunDetail, OverseerRunLimitDetail, ProviderCapacityRunDetail); err != nil {
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
