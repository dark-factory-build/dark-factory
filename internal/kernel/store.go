package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

var factoryEntityID = [IDBytes]byte{}

type pendingInvalidation struct {
	kind     EntityKind
	id       []byte
	revision int64
	deleted  bool
}

func (store *Store) CreateProject(ctx context.Context, spec NewProject, at UnixMillis) (Project, error) {
	if spec.VerificationPolicy == 0 {
		spec.VerificationPolicy = VerificationNone
	}
	if err := validateNewProject(spec); err != nil {
		return Project{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Close()

	existing, found, err := projectByID(ctx, tx.connection, spec.ID)
	if err != nil {
		return Project{}, tx.Rollback(err)
	}
	if found {
		if projectMatchesCreation(existing, spec) {
			if err := tx.Rollback(nil); err != nil {
				return Project{}, err
			}
			return existing, nil
		}
		return Project{}, tx.Rollback(ErrConflict)
	}
	var conflicting int
	if err := tx.connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE root = ?", spec.Root).Scan(&conflicting); err != nil {
		return Project{}, tx.Rollback(err)
	}
	if conflicting != 0 {
		return Project{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO projects(id, name, root, verification_policy, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 1, ?, ?)`, spec.ID.Bytes(), spec.Name, spec.Root, spec.VerificationPolicy.String(), at.Int64(), at.Int64()); err != nil {
		return Project{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityProject, id: spec.ID.Bytes(), revision: 1}}); err != nil {
		return Project{}, tx.Rollback(err)
	}
	result, found, err := projectByID(ctx, tx.connection, spec.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Project{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Project{}, err
	}
	return result, nil
}

func (store *Store) CreateAgent(ctx context.Context, spec NewAgent, at UnixMillis) (Agent, error) {
	if err := validateNewAgent(spec); err != nil {
		return Agent{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Close()
	existing, found, err := agentByID(ctx, tx.connection, spec.ID)
	if err != nil {
		return Agent{}, tx.Rollback(err)
	}
	if found {
		if agentMatchesCreation(existing, spec) {
			if err := tx.Rollback(nil); err != nil {
				return Agent{}, err
			}
			return existing, nil
		}
		return Agent{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO agents(
        id, project_id, name, role, provider, execution_mode, model, reasoning_effort,
        paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 0, 1, ?, ?)`,
		spec.ID.Bytes(), spec.ProjectID.Bytes(), spec.Name, spec.Role.String(), spec.Provider.String(), spec.ExecutionMode.String(), nullableString(spec.Model), nullableString(spec.ReasoningEffort), int64(spec.ToolBudgetLimit), at.Int64(), at.Int64()); err != nil {
		return Agent{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityAgent, id: spec.ID.Bytes(), revision: 1}}); err != nil {
		return Agent{}, tx.Rollback(err)
	}
	result, found, err := agentByID(ctx, tx.connection, spec.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Agent{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Agent{}, err
	}
	return result, nil
}

func (store *Store) EnqueueTask(ctx context.Context, spec NewTask, at UnixMillis) (Task, error) {
	if err := validateNewTask(spec); err != nil {
		return Task{}, err
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Close()
	existing, found, err := taskByID(ctx, tx.connection, spec.ID)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if found {
		if taskMatchesCreation(existing, spec) {
			if err := tx.Rollback(nil); err != nil {
				return Task{}, err
			}
			return existing, nil
		}
		return Task{}, tx.Rollback(ErrConflict)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO tasks(
        id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body,
        status, priority, blocked_reason, result, completed_at_ms, revision,
        created_at_ms, updated_at_ms
    ) VALUES(?, ?, ?, ?, 1, ?, ?, 'queued', ?, NULL, NULL, NULL, 1, ?, ?)`,
		spec.ID.Bytes(), spec.ProjectID.Bytes(), spec.AssignedAgentID.Bytes(), spec.IncarnationID.Bytes(), spec.Title, spec.Body, spec.Priority, at.Int64(), at.Int64()); err != nil {
		return Task{}, tx.Rollback(err)
	}
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityTask, id: spec.ID.Bytes(), revision: 1}}); err != nil {
		return Task{}, tx.Rollback(err)
	}
	result, found, err := taskByID(ctx, tx.connection, spec.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Task{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return result, nil
}

func (store *Store) SetDispatch(ctx context.Context, expected Revision, enabled bool, at UnixMillis) (FactoryState, error) {
	if expected.Int64() < 1 {
		return FactoryState{}, fmt.Errorf("%w: invalid expected factory revision", ErrInvalidValue)
	}
	return store.setFactory(ctx, expected, at, func(state FactoryState) (bool, int64, int64) {
		return state.DispatchEnabled != enabled, int64(boolInt(enabled)), int64(state.Capacity)
	})
}

func (store *Store) SetCapacity(ctx context.Context, expected Revision, capacity uint16, at UnixMillis) (FactoryState, error) {
	if expected.Int64() < 1 || capacity < 1 || capacity > MaxFactoryCapacity {
		return FactoryState{}, fmt.Errorf("%w: capacity %d outside 1..%d", ErrInvalidValue, capacity, MaxFactoryCapacity)
	}
	return store.setFactory(ctx, expected, at, func(state FactoryState) (bool, int64, int64) {
		return state.Capacity != capacity, int64(boolInt(state.DispatchEnabled)), int64(capacity)
	})
}

func (store *Store) setFactory(ctx context.Context, expected Revision, at UnixMillis, desired func(FactoryState) (bool, int64, int64)) (FactoryState, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return FactoryState{}, err
	}
	defer tx.Close()
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	changed, dispatch, capacity := desired(state)
	if state.Revision == expected && !changed {
		if err := tx.Rollback(nil); err != nil {
			return FactoryState{}, err
		}
		return state, nil
	}
	if state.Revision.Int64() == expected.Int64()+1 && !changed {
		if err := tx.Rollback(nil); err != nil {
			return FactoryState{}, err
		}
		return state, nil
	}
	if state.Revision != expected {
		return FactoryState{}, tx.Rollback(ErrRevisionConflict)
	}
	if at.Int64() < state.updatedAt.Int64() {
		return FactoryState{}, tx.Rollback(ErrRevisionConflict)
	}
	result, err := tx.connection.ExecContext(ctx, `UPDATE factory SET dispatch_enabled = ?, capacity = ?, revision = revision + 1, updated_at_ms = ? WHERE singleton = 1 AND revision = ?`, dispatch, capacity, at.Int64(), expected.Int64())
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	if rows != 1 {
		return FactoryState{}, tx.Rollback(ErrRevisionConflict)
	}
	newRevision := expected.Int64() + 1
	if err := appendInvalidations(ctx, tx.connection, at, []pendingInvalidation{{kind: EntityFactory, id: factoryEntityID[:], revision: newRevision}}); err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	state, err = factoryState(ctx, tx.connection)
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FactoryState{}, err
	}
	return state, nil
}

func appendInvalidations(ctx context.Context, connection *sql.Conn, at UnixMillis, pending []pendingInvalidation) error {
	if len(pending) == 0 {
		return nil
	}
	var next int64
	if err := connection.QueryRowContext(ctx, `SELECT next_invalidation_sequence FROM factory WHERE singleton = 1`).Scan(&next); err != nil {
		return fmt.Errorf("read invalidation head: %w", err)
	}
	if next < 1 || int64(len(pending)) > math.MaxInt64-next {
		return fmt.Errorf("%w: invalidation sequence overflow", ErrCorruptState)
	}
	result, err := connection.ExecContext(ctx, `UPDATE factory SET next_invalidation_sequence = next_invalidation_sequence + ? WHERE singleton = 1 AND next_invalidation_sequence = ?`, len(pending), next)
	if err != nil {
		return fmt.Errorf("reserve invalidation sequences: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%w: invalidation head changed inside writer transaction", ErrCorruptState)
	}
	for index, item := range pending {
		kind := item.kind.String()
		validID := len(item.id) == IDBytes && (item.kind == EntityFactory && string(item.id) == string(factoryEntityID[:]) || item.kind != EntityFactory && validNonzeroID(item.id))
		if kind == "" || !validID || item.revision < 1 {
			return fmt.Errorf("%w: invalid invalidation identity", ErrInvalidValue)
		}
		inserted, err := connection.ExecContext(ctx, `INSERT INTO invalidations(sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted) VALUES(?, ?, ?, ?, ?, ?)`, next+int64(index), at.Int64(), kind, item.id, item.revision, boolInt(item.deleted))
		if err := requireOneRow(inserted, err); err != nil {
			return fmt.Errorf("insert invalidation: %w", err)
		}
	}
	if _, err := connection.ExecContext(ctx, `WITH cutoff AS (
        SELECT sequence FROM invalidations ORDER BY sequence DESC LIMIT 1 OFFSET 4096
    )
    DELETE FROM invalidations
    WHERE sequence <= COALESCE((SELECT sequence FROM cutoff), 0)`); err != nil {
		return fmt.Errorf("prune invalidations: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `UPDATE factory
        SET invalidation_floor = COALESCE((SELECT MIN(sequence) FROM invalidations), next_invalidation_sequence)
        WHERE singleton = 1`); err != nil {
		return fmt.Errorf("advance invalidation floor: %w", err)
	}
	return nil
}

func validateNewProject(spec NewProject) error {
	if spec.ID.zero() || byteLen(spec.Name) < 1 || byteLen(spec.Name) > 128 || !validAbsolutePath(spec.Root) || spec.VerificationPolicy.String() == "" {
		return fmt.Errorf("%w: invalid project", ErrInvalidValue)
	}
	return nil
}

func validateNewAgent(spec NewAgent) error {
	if spec.ID.zero() || spec.ProjectID.zero() || byteLen(spec.Name) < 1 || byteLen(spec.Name) > 128 || !spec.Role.valid() || !spec.Provider.valid() || !spec.ExecutionMode.valid() {
		return fmt.Errorf("%w: invalid agent", ErrInvalidValue)
	}
	if spec.Provider == ProviderShell && spec.ExecutionMode != ExecutionUnrestricted {
		return fmt.Errorf("%w: shell requires unrestricted execution", ErrInvalidValue)
	}
	if byteLen(spec.Model) > 128 || (spec.Model != "" && byteLen(spec.Model) < 1) {
		return fmt.Errorf("%w: invalid model", ErrInvalidValue)
	}
	if !validReasoningEffort(spec.ReasoningEffort) {
		return fmt.Errorf("%w: invalid reasoning effort", ErrInvalidValue)
	}
	if spec.ToolBudgetLimit < 1 || spec.ToolBudgetLimit > 1_000_000_000 {
		return fmt.Errorf("%w: invalid tool budget", ErrInvalidValue)
	}
	return nil
}

func validateNewTask(spec NewTask) error {
	if spec.ID.zero() || spec.ProjectID.zero() || spec.AssignedAgentID.zero() || spec.IncarnationID.zero() || byteLen(spec.Title) < 1 || byteLen(spec.Title) > 1024 || byteLen(spec.Body) > 131072 || spec.Priority < -1_000_000 || spec.Priority > 1_000_000 {
		return fmt.Errorf("%w: invalid task", ErrInvalidValue)
	}
	return nil
}

func validReasoningEffort(value string) bool {
	switch value {
	case "", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func projectMatchesCreation(existing Project, spec NewProject) bool {
	return existing.Name == spec.Name && existing.Root == spec.Root && existing.VerificationPolicy == spec.VerificationPolicy && existing.Revision.Int64() == 1 && existing.UpdatedAt == existing.CreatedAt
}

func agentMatchesCreation(existing Agent, spec NewAgent) bool {
	return existing.ProjectID == spec.ProjectID && existing.Name == spec.Name && existing.Role == spec.Role && existing.Provider == spec.Provider && existing.ExecutionMode == spec.ExecutionMode && existing.Model == spec.Model && existing.ReasoningEffort == spec.ReasoningEffort && existing.ToolBudgetLimit == spec.ToolBudgetLimit && existing.ToolCallsUsed == 0 && !existing.Paused && existing.Revision.Int64() == 1 && existing.UpdatedAt == existing.CreatedAt
}

func taskMatchesCreation(existing Task, spec NewTask) bool {
	return existing.ProjectID == spec.ProjectID && existing.AssignedAgentID == spec.AssignedAgentID && existing.IncarnationID == spec.IncarnationID && existing.Title == spec.Title && existing.Body == spec.Body && existing.Status == TaskQueued && existing.Priority == spec.Priority && existing.WorkRevision.Int64() == 1 && existing.BlockedReason == "" && existing.Result == "" && existing.CompletedAt == nil && existing.Revision.Int64() == 1 && existing.UpdatedAt == existing.CreatedAt
}
