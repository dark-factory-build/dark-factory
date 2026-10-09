package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// v35UserVersion is the one earlier version Open migrates. v36 has the same
// schema; it drops the review-operation state 'enqueuing', so a v35 home's
// operations in it become 'enqueued', where each pass ensures the exact head
// is queued.
const v35UserVersion = 35

// validateOpenableSnapshot accepts a current database or an exact v35 one.
func validateOpenableSnapshot(ctx context.Context, connection *sql.Conn) error {
	if _, version, err := inspectIdentity(ctx, connection); err != nil {
		return err
	} else if version == v35UserVersion {
		if err := validateSchemaVersion(ctx, connection, version, schemaStatements); err != nil {
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

// migrateLegacy takes an exact v35 home to the current schema in one
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
	case v35UserVersion:
	default:
		return errors.Join(fmt.Errorf("%w: home is at user_version %d, this build requires %d", ErrForeignDatabase, version, userVersion), connection.Close())
	}
	if err := migrateTransaction(ctx, connection, migrateV35); err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	return connection.Close()
}

func migrateV35(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v35UserVersion, schemaStatements); err != nil {
		return err
	}
	for _, statement := range []string{`UPDATE production_records SET document = json_set(document, '$.state', 'enqueued')
        WHERE kind = 'reviewer' AND json_extract(document, '$.state') = 'enqueuing'`, fmt.Sprintf("PRAGMA user_version = %d", userVersion)} {
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
