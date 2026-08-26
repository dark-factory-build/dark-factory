package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
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
	if err := validateOwnershipLocators(ctx, connection); err != nil {
		return err
	}
	if err := validateRunRelationships(ctx, connection); err != nil {
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

func validateRunRelationships(ctx context.Context, connection *sql.Conn) error {
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
		if _, err := loadRunRelationships(ctx, connection, run); err != nil {
			return err
		}
	}
	return nil
}

type runRelationships struct {
	task      Task
	change    *Change
	resources []Resource
}

func loadRunRelationships(ctx context.Context, connection *sql.Conn, run Run) (runRelationships, error) {
	task, found, err := taskByID(ctx, connection, run.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return runRelationships{}, err
	}
	if task.ProjectID != run.ProjectID || task.AssignedAgentID != run.AgentID || task.IncarnationID != run.TaskIncarnationID || task.WorkRevision != run.AdmittedTaskWorkRevision || !taskMatchesRun(task, run) {
		return runRelationships{}, fmt.Errorf("%w: task does not match run", ErrCorruptState)
	}

	var change *Change
	if run.ChangeID != nil {
		value, found, err := changeByID(ctx, connection, *run.ChangeID)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return runRelationships{}, err
		}
		if run.Role != RoleWorker || value.ProjectID != run.ProjectID || value.TaskID != run.TaskID || value.TaskIncarnationID != run.TaskIncarnationID || run.RunningAt != nil && value.Phase != ChangeAvailable {
			return runRelationships{}, fmt.Errorf("%w: Change does not match run", ErrCorruptState)
		}
		change = &value
	} else if run.Role != RoleOrchestrator {
		return runRelationships{}, fmt.Errorf("%w: missing worker Change", ErrCorruptState)
	}

	resources, err := resourcesForRun(ctx, connection, run.ID)
	if err != nil {
		return runRelationships{}, err
	}
	if !resourcesMatchRunPhase(run.Phase, resources) {
		return runRelationships{}, fmt.Errorf("%w: resources do not match run phase", ErrCorruptState)
	}
	for _, resource := range resources {
		if resource.State != ResourceReleased || resource.Identity.Empty() {
			continue
		}
		switch resource.Kind {
		case ResourceProviderProcess, ResourceProviderGroup:
			if run.ProviderExit == nil {
				return runRelationships{}, fmt.Errorf("%w: released provider resource lacks exit evidence", ErrCorruptState)
			}
		case ResourceRunnerProcess:
			if run.RunnerExit == nil {
				return runRelationships{}, fmt.Errorf("%w: released runner resource lacks exit evidence", ErrCorruptState)
			}
		}
	}
	return runRelationships{task: task, change: change, resources: resources}, nil
}

func taskMatchesRun(task Task, run Run) bool {
	if run.Phase != RunTerminal {
		return task.Status == TaskRunning
	}
	if run.Terminal == nil {
		return false
	}
	switch run.Terminal.kind {
	case OutcomeSucceeded:
		return task.Status == TaskSucceeded && task.Result == run.Terminal.result && terminalCompletionMatches(task, run)
	case OutcomeBlocked:
		return task.Status == TaskBlocked && task.BlockedReason == run.Terminal.detail && task.CompletedAt == nil
	case OutcomeFailed:
		return task.Status == TaskFailed && terminalCompletionMatches(task, run)
	case OutcomeCancelled:
		return task.Status == TaskCancelled && terminalCompletionMatches(task, run)
	default:
		return false
	}
}

func terminalCompletionMatches(task Task, run Run) bool {
	return task.CompletedAt != nil && run.TerminalAt != nil && *task.CompletedAt == *run.TerminalAt
}

type ownershipLocator struct {
	path     string
	changeID *ChangeID
	runID    *RunID
}

func ownershipLocators(ctx context.Context, connection *sql.Conn) ([]ownershipLocator, error) {
	changeRows, err := connection.QueryContext(ctx, `SELECT id, source_root, staging_root FROM changes`)
	if err != nil {
		return nil, err
	}
	var locators []ownershipLocator
	for changeRows.Next() {
		var rawID []byte
		var source, staging string
		if err := changeRows.Scan(&rawID, &source, &staging); err != nil {
			changeRows.Close()
			return nil, err
		}
		id, err := ChangeIDFromBytes(rawID)
		if err != nil || !validOwnedLocator(source) || !validOwnedLocator(staging) {
			changeRows.Close()
			return nil, fmt.Errorf("%w: invalid Change ownership locator", ErrCorruptState)
		}
		changeID := id
		locators = append(locators, ownershipLocator{path: source, changeID: &changeID}, ownershipLocator{path: staging, changeID: &changeID})
	}
	if err := changeRows.Close(); err != nil {
		return nil, err
	}

	resourceRows, err := connection.QueryContext(ctx, `SELECT run_id, path FROM resources WHERE kind = 'runtime_root'`)
	if err != nil {
		return nil, err
	}
	defer resourceRows.Close()
	for resourceRows.Next() {
		var rawRunID []byte
		var path string
		if err := resourceRows.Scan(&rawRunID, &path); err != nil {
			return nil, err
		}
		runID, err := RunIDFromBytes(rawRunID)
		if err != nil || !validOwnedLocator(path) {
			return nil, fmt.Errorf("%w: invalid runtime ownership locator", ErrCorruptState)
		}
		id := runID
		locators = append(locators, ownershipLocator{path: path, runID: &id})
	}
	if err := resourceRows.Err(); err != nil {
		return nil, err
	}
	return locators, nil
}

func validateOwnershipLocators(ctx context.Context, connection *sql.Conn) error {
	locators, err := ownershipLocators(ctx, connection)
	if err != nil {
		return err
	}
	return validateDistinctOwnershipLocators(locators)
}

func validateAdmissionLocatorOwnership(ctx context.Context, connection *sql.Conn, keys AdmissionKeys) error {
	locators, err := ownershipLocators(ctx, connection)
	if err != nil {
		return err
	}
	if err := validateDistinctOwnershipLocators(locators); err != nil {
		return err
	}
	candidates := []string{keys.RuntimeRoot}
	if keys.Change != nil {
		candidates = append(candidates, keys.Change.SourceRoot, keys.Change.StagingRoot)
	}
	for _, locator := range locators {
		if locator.runID != nil && *locator.runID == keys.RunID || locator.changeID != nil && keys.Change != nil && *locator.changeID == keys.Change.ID {
			continue
		}
		for _, candidate := range candidates {
			if pathsOverlap(locator.path, candidate) {
				return ErrConflict
			}
		}
	}
	return nil
}

func validateDistinctOwnershipLocators(locators []ownershipLocator) error {
	paths := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if _, duplicate := paths[locator.path]; duplicate {
			return fmt.Errorf("%w: durable ownership locators overlap", ErrCorruptState)
		}
		paths[locator.path] = struct{}{}
	}
	for path := range paths {
		for parent := filepath.Dir(path); parent != string(filepath.Separator); parent = filepath.Dir(parent) {
			if _, overlap := paths[parent]; overlap {
				return fmt.Errorf("%w: durable ownership locators overlap", ErrCorruptState)
			}
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
	return rows.Close()
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
