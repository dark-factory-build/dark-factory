package kernel

import (
	"context"
	"database/sql"
	"fmt"
)

func validateDurableControls(ctx context.Context, connection *sql.Conn) error {
	state, err := factoryState(ctx, connection)
	if err != nil {
		return err
	}
	if err := validateProjects(ctx, connection); err != nil {
		return err
	}
	if err := validateAgents(ctx, connection); err != nil {
		return err
	}
	if err := validateTasks(ctx, connection); err != nil {
		return err
	}
	if err := validateChanges(ctx, connection); err != nil {
		return err
	}
	if err := validateRuns(ctx, connection); err != nil {
		return err
	}
	if err := validateResources(ctx, connection); err != nil {
		return err
	}
	if err := validateRunResourceCoupling(ctx, connection); err != nil {
		return err
	}
	if err := validateResourceIdentityCollisions(ctx, connection); err != nil {
		return err
	}
	return validateInvalidations(ctx, connection, state)
}

func validateResourceIdentityCollisions(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT left_resource.run_id, left_resource.kind, right_resource.run_id, right_resource.kind
		FROM resources AS left_resource
		JOIN resources AS right_resource ON left_resource.id < right_resource.id AND (
			(left_resource.path_dev IS NOT NULL AND left_resource.path_dev = right_resource.path_dev AND left_resource.path_inode = right_resource.path_inode) OR
			(left_resource.pid IS NOT NULL AND left_resource.pid = right_resource.pid AND left_resource.pgid = right_resource.pgid AND left_resource.birth_digest = right_resource.birth_digest)
		)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var leftRunRaw, rightRunRaw []byte
		var leftKindRaw, rightKindRaw string
		if err := rows.Scan(&leftRunRaw, &leftKindRaw, &rightRunRaw, &rightKindRaw); err != nil {
			return err
		}
		leftRun, leftRunErr := RunIDFromBytes(leftRunRaw)
		rightRun, rightRunErr := RunIDFromBytes(rightRunRaw)
		leftKind, leftKindErr := parseResourceKind(leftKindRaw)
		rightKind, rightKindErr := parseResourceKind(rightKindRaw)
		providerPair := leftRun == rightRun && (leftKind == ResourceProviderProcess && rightKind == ResourceProviderGroup || leftKind == ResourceProviderGroup && rightKind == ResourceProviderProcess)
		if leftRunErr != nil || rightRunErr != nil || leftKindErr != nil || rightKindErr != nil || !providerPair {
			return fmt.Errorf("%w: resource identities collide", ErrCorruptState)
		}
	}
	return rows.Err()
}

func validateRunResourceCoupling(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs`)
	if err != nil {
		return err
	}
	var runs []Run
	for rows.Next() {
		run, found, err := scanRun(rows)
		if err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, run := range runs {
		resources, err := resourcesForRun(ctx, connection, run.ID)
		if err != nil || !resourcesMatchRunPhase(run.Phase, resources) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: resources do not match run phase", ErrCorruptState)
		}
	}
	return nil
}

func resourcesMatchRunPhase(phase RunPhase, resources []Resource) bool {
	if !exactResourceSet(resources, false) {
		return false
	}
	for _, resource := range resources {
		switch phase {
		case RunAdmitted:
			if resource.State != ResourceDeclared && resource.State != ResourceActive {
				return false
			}
		case RunRunning:
			if resource.State != ResourceActive {
				return false
			}
		case RunFinalizing:
			if resource.State != ResourceReleasing && resource.State != ResourceUnresolved && resource.State != ResourceReleased {
				return false
			}
		case RunTerminal:
			if resource.State != ResourceReleased {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validateProjects(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, name, root, verification_policy, revision, created_at_ms, updated_at_ms FROM projects`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanProject(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateAgents(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, name, role, provider, execution_mode, model, reasoning_effort, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms FROM agents`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanAgent(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateTasks(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms FROM tasks`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanTask(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateChanges(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+changeColumns+` FROM changes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		if _, _, err := scanChange(rows); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var collisions int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM changes AS left_change JOIN changes AS right_change ON left_change.source_root = right_change.staging_root`).Scan(&collisions); err != nil {
		return err
	}
	if collisions != 0 {
		return fmt.Errorf("%w: Change locators collide across source and staging authority", ErrCorruptState)
	}
	return nil
}

func validateRuns(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanRun(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateResources(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+resourceColumns+` FROM resources`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanResource(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateInvalidations(ctx context.Context, connection *sql.Conn, factory FactoryState) error {
	if err := validateInvalidationBounds(ctx, connection, factory); err != nil {
		return err
	}
	if factory.Head.Int64() == 0 {
		return nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted FROM invalidations ORDER BY sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	expected := factory.Floor.Int64()
	for rows.Next() {
		var sequence, at, revision, deleted int64
		var kind string
		var id []byte
		if err := rows.Scan(&sequence, &at, &kind, &id, &revision, &deleted); err != nil {
			return err
		}
		entityKind, kindErr := parseEntityKind(kind)
		validID := len(id) == IDBytes
		if entityKind == EntityFactory {
			validID = validID && string(id) == string(factoryEntityID[:])
		} else {
			validID = validID && validNonzeroID(id)
		}
		if sequence != expected || at < 0 || revision < 1 || deleted != 0 && deleted != 1 || kindErr != nil || !validID {
			return fmt.Errorf("%w: invalid invalidation row", ErrCorruptState)
		}
		expected++
	}
	return rows.Err()
}

func validateInvalidationBounds(ctx context.Context, connection *sql.Conn, factory FactoryState) error {
	var count, minimum, maximum int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(sequence), 0), COALESCE(MAX(sequence), 0) FROM invalidations`).Scan(&count, &minimum, &maximum); err != nil {
		return err
	}
	if count < 0 || count > EventRetentionLimit {
		return fmt.Errorf("%w: invalid invalidation count", ErrCorruptState)
	}
	if count == 0 {
		if factory.Head.Int64() != 0 || factory.Floor.Int64() != 1 {
			return fmt.Errorf("%w: empty invalidation log has advanced metadata", ErrCorruptState)
		}
		return nil
	}
	if minimum != factory.Floor.Int64() || maximum != factory.Head.Int64() || maximum-minimum+1 != count {
		return fmt.Errorf("%w: invalidation metadata or gap", ErrCorruptState)
	}
	return nil
}

func validNonzeroID(value []byte) bool {
	_, err := identifierFromBytes(value)
	return err == nil
}

func anyNullIntValid(values ...sql.NullInt64) bool {
	for _, value := range values {
		if value.Valid {
			return true
		}
	}
	return false
}
