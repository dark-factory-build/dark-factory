package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	v37UserVersion = 37
	v38UserVersion = 38
	v39UserVersion = 39
	v40UserVersion = 40
)

var browserSecurityEventStatements = []string{
	`CREATE TABLE browser_security_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT CHECK (sequence >= 1),
    kind TEXT NOT NULL CHECK (kind IN ('challenge_minted', 'challenge_abandoned', 'client_paired', 'duplicate_fingerprint', 'client_revoked')),
    client_id BLOB CHECK (client_id IS NULL OR (length(client_id) = 16 AND client_id <> zeroblob(16))) REFERENCES browser_clients(id),
    occurred_at_ms INTEGER NOT NULL CHECK (occurred_at_ms >= 0),
    CHECK ((kind IN ('challenge_minted', 'challenge_abandoned') AND client_id IS NULL) OR (kind NOT IN ('challenge_minted', 'challenge_abandoned') AND client_id IS NOT NULL))
) STRICT`,
	`CREATE INDEX browser_security_events_client ON browser_security_events(client_id, sequence)`,
}

func legacySchemaStatements(version int) []string {
	replacer := strings.NewReplacer()
	if version == v38UserVersion {
		replacer = strings.NewReplacer("'transient'", "'runner_exit'", `length(CAST(body AS BLOB)) <= 148480`, `length(CAST(body AS BLOB)) <= 131072`)
	} else if version == v37UserVersion {
		replacer = strings.NewReplacer("'transient'", "'runner_exit'", agentWakeOnColumn, "", projectSpecialistColumns, "", `length(CAST(body AS BLOB)) <= 148480`, `length(CAST(body AS BLOB)) <= 131072`)
	} else if version == v39UserVersion {
		replacer = strings.NewReplacer(`length(CAST(body AS BLOB)) <= 148480`, `length(CAST(body AS BLOB)) <= 131072`)
	} else if version == v40UserVersion {
		replacer = strings.NewReplacer(`length(CAST(body AS BLOB)) <= 148480`, `length(CAST(body AS BLOB)) <= 131072`)
	}
	statements := slices.Clone(schemaStatements)
	if version <= v39UserVersion {
		at := slices.IndexFunc(statements, func(statement string) bool { return strings.HasPrefix(statement, "CREATE TABLE browser_clients ") }) + 1
		statements = slices.Insert(statements, at, browserSecurityEventStatements...)
	}
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
	} else if version >= v37UserVersion && version <= v40UserVersion {
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
	case v37UserVersion, v38UserVersion, v39UserVersion, v40UserVersion:
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

func migrateFrom(ctx context.Context, connection *sql.Conn, version int) error {
	if err := validateSchemaVersion(ctx, connection, version, legacySchemaStatements(version)); err != nil {
		return err
	}
	if version == v37UserVersion {
		columns := strings.Split(projectSpecialistColumns, ", ")[1:]
		for _, statement := range []string{
			"ALTER TABLE agents ADD COLUMN " + strings.TrimPrefix(agentWakeOnColumn, ", "),
			"ALTER TABLE projects ADD COLUMN " + columns[0],
			"ALTER TABLE projects ADD COLUMN " + columns[1],
		} {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if version <= v39UserVersion {
		if _, err := connection.ExecContext(ctx, "DROP TABLE browser_security_events"); err != nil {
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
		`UPDATE sqlite_schema SET sql = replace(sql, 'length(CAST(body AS BLOB)) <= 131072', 'length(CAST(body AS BLOB)) <= 148480') WHERE type = 'table' AND name = 'tasks'`,
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
