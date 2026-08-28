package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (store *Store) AdmitNext(ctx context.Context, agentID AgentID, keys AdmissionKeys, at UnixMillis) (AdmissionResult, error) {
	if agentID.zero() || !keys.valid() {
		return AdmissionResult{}, fmt.Errorf("%w: invalid admission request", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return AdmissionResult{}, err
	}
	defer tx.Close()

	factory, err := factoryState(ctx, tx.connection)
	if err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	if result, found, err := reconcileAdmissionOnConnection(ctx, tx.connection, keys); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	} else if found {
		if err := tx.Rollback(nil); err != nil {
			return AdmissionResult{}, err
		}
		return result, nil
	}
	if at.Int64() < factory.updatedAt.Int64() {
		return AdmissionResult{}, tx.Rollback(ErrRevisionConflict)
	}
	if !factory.DispatchEnabled {
		return rollbackNoAdmission(tx, NoAdmissionDispatchDisabled)
	}
	var active int64
	if err := tx.connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE phase IN ('admitted', 'running', 'finalizing')`).Scan(&active); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	if active < 0 {
		return AdmissionResult{}, tx.Rollback(ErrCorruptState)
	}
	if active >= int64(factory.Capacity) {
		return rollbackNoAdmission(tx, NoAdmissionAtCapacity)
	}

	agent, found, err := agentByID(ctx, tx.connection, agentID)
	if err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	if !found {
		return AdmissionResult{}, tx.Rollback(ErrNotFound)
	}
	if agent.Paused {
		return rollbackNoAdmission(tx, NoAdmissionAgentPaused)
	}
	if agent.ToolCallsUsed >= agent.ToolBudgetLimit {
		return rollbackNoAdmission(tx, NoAdmissionBudgetExhausted)
	}
	var agentOpen int64
	if err := tx.connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_id = ? AND phase <> 'terminal'`, agentID.Bytes()).Scan(&agentOpen); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	if agentOpen != 0 {
		return rollbackNoAdmission(tx, NoAdmissionAgentBusy)
	}

	task, found, err := scanTask(tx.connection.QueryRowContext(ctx, `SELECT id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms FROM tasks WHERE assigned_agent_id = ? AND status = 'queued' ORDER BY priority DESC, created_at_ms ASC, id ASC LIMIT 1`, agentID.Bytes()))
	if err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	if !found {
		return rollbackNoAdmission(tx, NoAdmissionQueueEmpty)
	}
	if task.ProjectID != agent.ProjectID {
		return AdmissionResult{}, tx.Rollback(ErrCorruptState)
	}
	if at.Int64() < task.UpdatedAt.Int64() {
		return AdmissionResult{}, tx.Rollback(ErrRevisionConflict)
	}
	project, found, err := projectByID(ctx, tx.connection, task.ProjectID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return AdmissionResult{}, tx.Rollback(err)
	}
	if err := validateAdmissionLocatorOwnership(ctx, tx.connection, keys); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}

	var change *Change
	changeCreated := false
	switch agent.Role {
	case RoleWorker:
		if keys.Change == nil {
			return AdmissionResult{}, tx.Rollback(fmt.Errorf("%w: worker admission requires Change reservation", ErrInvalidValue))
		}
		value, created, err := reserveAdmissionChange(ctx, tx.connection, task, *keys.Change, at)
		if err != nil {
			return AdmissionResult{}, tx.Rollback(err)
		}
		change, changeCreated = &value, created
	case RoleOrchestrator:
		if keys.Change != nil {
			return AdmissionResult{}, tx.Rollback(fmt.Errorf("%w: orchestrator admission forbids Change reservation", ErrInvalidValue))
		}
	default:
		return AdmissionResult{}, tx.Rollback(ErrCorruptState)
	}

	updatedTaskRevision := task.Revision.Int64() + 1
	result, err := tx.connection.ExecContext(ctx, `UPDATE tasks SET status = 'running', revision = revision + 1, updated_at_ms = ? WHERE id = ? AND project_id = ? AND incarnation_id = ? AND work_revision = ? AND status = 'queued' AND revision = ?`,
		at.Int64(), task.ID.Bytes(), task.ProjectID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(), task.Revision.Int64())
	if err := requireOneRow(result, err); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	var changeID any
	if change != nil {
		changeID = change.ID.Bytes()
	}
	_, err = tx.connection.ExecContext(ctx, `INSERT INTO runs(
		id, project_id, agent_id, task_id, task_incarnation_id, admitted_task_work_revision,
		change_id, role, provider, model, reasoning_effort, verification_policy, phase,
		proposal_kind, proposal_code, proposal_detail, proposal_result,
		terminal_kind, terminal_code, terminal_detail, terminal_result,
		credential_digest, credential_revoked_at_ms,
		provider_exit_kind, provider_exit_sequence, provider_exit_code, provider_exit_signal, provider_exit_at_ms,
		runner_exit_kind, runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
		revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'admitted', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 1, ?, NULL, NULL, NULL, ?)`,
		keys.RunID.Bytes(), task.ProjectID.Bytes(), agent.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(),
		changeID, agent.Role.String(), agent.Provider.String(), nullableString(agent.Model), nullableString(agent.ReasoningEffort), project.VerificationPolicy.String(),
		keys.AttemptDigest.Bytes(), at.Int64(), at.Int64())
	if err != nil {
		return AdmissionResult{}, tx.Rollback(classifyAdmissionConflict(ctx, tx.connection, keys, err))
	}
	inserted, err := tx.connection.ExecContext(ctx, `INSERT INTO terminal_sessions(id, run_id, state, unresolved_reason, revision, declared_at_ms, activated_at_ms, closed_at_ms, updated_at_ms) VALUES(?, ?, 'declared', NULL, 1, ?, NULL, NULL, ?)`, keys.TerminalSessionID.Bytes(), keys.RunID.Bytes(), at.Int64(), at.Int64())
	if err := requireOneRow(inserted, err); err != nil {
		return AdmissionResult{}, tx.Rollback(classifyAdmissionConflict(ctx, tx.connection, keys, err))
	}
	resources := []struct {
		id   ResourceID
		kind ResourceKind
		path any
	}{
		{keys.Resources.RuntimeRoot, ResourceRuntimeRoot, keys.RuntimeRoot},
		{keys.Resources.RunnerProcess, ResourceRunnerProcess, nil},
		{keys.Resources.ProviderProcess, ResourceProviderProcess, nil},
		{keys.Resources.ProviderGroup, ResourceProviderGroup, nil},
	}
	for _, resource := range resources {
		inserted, err := tx.connection.ExecContext(ctx, `INSERT INTO resources(id, run_id, kind, state, path, revision, declared_at_ms, updated_at_ms) VALUES(?, ?, ?, 'declared', ?, 1, ?, ?)`, resource.id.Bytes(), keys.RunID.Bytes(), resource.kind.String(), resource.path, at.Int64(), at.Int64())
		if err := requireOneRow(inserted, err); err != nil {
			return AdmissionResult{}, tx.Rollback(classifyAdmissionConflict(ctx, tx.connection, keys, err))
		}
	}
	factoryRevision := factory.Revision.Int64() + 1
	result, err = tx.connection.ExecContext(ctx, `UPDATE factory SET revision = revision + 1, updated_at_ms = ? WHERE singleton = 1 AND revision = ?`, at.Int64(), factory.Revision.Int64())
	if err := requireOneRow(result, err); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	pending := []pendingInvalidation{
		{kind: EntityFactory, id: factoryEntityID[:], revision: factoryRevision},
		{kind: EntityTask, id: task.ID.Bytes(), revision: updatedTaskRevision},
	}
	if changeCreated {
		pending = append(pending, pendingInvalidation{kind: EntityChange, id: change.ID.Bytes(), revision: 1})
	}
	pending = append(pending, pendingInvalidation{kind: EntityRun, id: keys.RunID.Bytes(), revision: 1})
	if err := appendInvalidations(ctx, tx.connection, at, pending); err != nil {
		return AdmissionResult{}, tx.Rollback(err)
	}
	run, found, err := runByID(ctx, tx.connection, keys.RunID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return AdmissionResult{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AdmissionResult{}, err
	}
	return AdmissionResult{Run: &run}, nil
}

func rollbackNoAdmission(tx *writeTx, reason NoAdmissionReason) (AdmissionResult, error) {
	if err := tx.Rollback(nil); err != nil {
		return AdmissionResult{}, err
	}
	return AdmissionResult{Reason: reason}, nil
}

func reserveAdmissionChange(ctx context.Context, connection *sql.Conn, task Task, reservation ChangeReservation, at UnixMillis) (Change, bool, error) {
	existing, found, err := changeByID(ctx, connection, reservation.ID)
	if err != nil {
		return Change{}, false, err
	}
	if found {
		if reservation.ExpectedReuseRevision == nil || existing.ProjectID != task.ProjectID || existing.TaskID != task.ID || existing.TaskIncarnationID != task.IncarnationID || existing.SourceRoot != reservation.SourceRoot || existing.StagingRoot != reservation.StagingRoot || existing.Revision != *reservation.ExpectedReuseRevision {
			return Change{}, false, ErrConflict
		}
		return existing, false, nil
	}
	if reservation.ExpectedReuseRevision != nil {
		return Change{}, false, ErrConflict
	}
	_, err = connection.ExecContext(ctx, `INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, source_root, staging_root, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'reserved', ?, ?, 1, ?, ?)`, reservation.ID.Bytes(), task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), reservation.SourceRoot, reservation.StagingRoot, at.Int64(), at.Int64())
	if err != nil {
		return Change{}, false, err
	}
	created, found, err := changeByID(ctx, connection, reservation.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return Change{}, false, err
	}
	return created, true, nil
}

func classifyAdmissionConflict(ctx context.Context, connection *sql.Conn, keys AdmissionKeys, cause error) error {
	if _, found, reconcileErr := reconcileAdmissionOnConnection(ctx, connection, keys); found && reconcileErr == nil {
		return cause
	}
	return errors.Join(ErrConflict, cause)
}

func (store *Store) ReconcileAdmission(ctx context.Context, keys AdmissionKeys) (AdmissionResult, error) {
	if !keys.valid() {
		return AdmissionResult{}, fmt.Errorf("%w: invalid admission keys", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return AdmissionResult{}, err
	}
	defer tx.Close()
	result, found, err := reconcileAdmissionOnConnection(ctx, tx.connection, keys)
	if err != nil {
		return AdmissionResult{}, err
	}
	if !found {
		return AdmissionResult{Reason: NoAdmissionNotReconciled}, nil
	}
	return result, nil
}

func reconcileAdmissionOnConnection(ctx context.Context, connection *sql.Conn, keys AdmissionKeys) (AdmissionResult, bool, error) {
	run, found, err := runByID(ctx, connection, keys.RunID)
	if err != nil || !found {
		return AdmissionResult{}, found, err
	}
	if !bytes.Equal(run.CredentialDigest.Bytes(), keys.AttemptDigest.Bytes()) {
		return AdmissionResult{}, true, ErrConflict
	}
	relationships, err := loadRunRelationships(ctx, connection, run)
	if err != nil {
		return AdmissionResult{}, true, err
	}
	if err := validateAdmissionLocatorOwnership(ctx, connection, keys); err != nil {
		return AdmissionResult{}, true, err
	}
	if keys.Change == nil {
		if run.ChangeID != nil || run.Role != RoleOrchestrator {
			return AdmissionResult{}, true, ErrConflict
		}
	} else {
		if run.ChangeID == nil || *run.ChangeID != keys.Change.ID || run.Role != RoleWorker {
			return AdmissionResult{}, true, ErrConflict
		}
		change := relationships.change
		if change == nil {
			return AdmissionResult{}, true, ErrCorruptState
		}
		if change.ProjectID != run.ProjectID || change.TaskID != run.TaskID || change.TaskIncarnationID != run.TaskIncarnationID || change.SourceRoot != keys.Change.SourceRoot || change.StagingRoot != keys.Change.StagingRoot {
			return AdmissionResult{}, true, ErrConflict
		}
		if keys.Change.ExpectedReuseRevision != nil && change.Revision.Int64() < keys.Change.ExpectedReuseRevision.Int64() {
			return AdmissionResult{}, true, ErrConflict
		}
	}
	resources := relationships.resources
	expected := map[ResourceKind]ResourceID{
		ResourceRuntimeRoot:     keys.Resources.RuntimeRoot,
		ResourceRunnerProcess:   keys.Resources.RunnerProcess,
		ResourceProviderProcess: keys.Resources.ProviderProcess,
		ResourceProviderGroup:   keys.Resources.ProviderGroup,
	}
	if len(resources) != len(expected) {
		return AdmissionResult{}, true, ErrConflict
	}
	for _, resource := range resources {
		id, ok := expected[resource.Kind]
		if !ok || resource.ID != id || resource.RunID != run.ID || resource.Kind == ResourceRuntimeRoot && resource.Path != keys.RuntimeRoot {
			return AdmissionResult{}, true, ErrConflict
		}
	}
	session, sessionFound, err := terminalSessionByRunID(ctx, connection, run.ID)
	if err != nil {
		return AdmissionResult{}, true, err
	}
	if !sessionFound || session.ID != keys.TerminalSessionID || session.RunID != run.ID || session.State != TerminalSessionDeclared || session.Revision.Int64() != 1 || session.DeclaredAt.Int64() != run.AdmittedAt.Int64() {
		return AdmissionResult{}, true, ErrConflict
	}
	return AdmissionResult{Run: &run}, true, nil
}
