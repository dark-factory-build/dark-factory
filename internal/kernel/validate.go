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
	return validateInvalidations(ctx, connection, state)
}

func validateProjects(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, name, root, revision, created_at_ms, updated_at_ms FROM projects`)
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
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, task_id, task_incarnation_id, phase, source_root, object_format, selected_commit, repository_root, repository_dev, repository_inode, selected_at_ms, tree_digest, file_count, total_bytes, source_dev, source_inode, available_at_ms, revision, created_at_ms, updated_at_ms FROM changes`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID, taskID, incarnationID []byte
		var phase, sourceRoot string
		var objectFormat, repositoryRoot sql.NullString
		var selectedCommit, treeDigest []byte
		var repositoryDev, repositoryInode, selectedAt, fileCount, totalBytes, sourceDev, sourceInode, availableAt sql.NullInt64
		var revision, createdAt, updatedAt int64
		if err := rows.Scan(&id, &projectID, &taskID, &incarnationID, &phase, &sourceRoot, &objectFormat, &selectedCommit, &repositoryRoot, &repositoryDev, &repositoryInode, &selectedAt, &treeDigest, &fileCount, &totalBytes, &sourceDev, &sourceInode, &availableAt, &revision, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("scan change: %w", err)
		}
		if !validNonzeroID(id) || !validNonzeroID(projectID) || !validNonzeroID(taskID) || !validNonzeroID(incarnationID) || byteLen(sourceRoot) < 1 || byteLen(sourceRoot) > 4096 || sourceRoot[0] != '/' || revision < 1 || createdAt < 0 || updatedAt < createdAt {
			return fmt.Errorf("%w: invalid change row", ErrCorruptState)
		}
		switch phase {
		case "reserved":
			if objectFormat.Valid || selectedCommit != nil || repositoryRoot.Valid || anyNullIntValid(repositoryDev, repositoryInode, selectedAt, fileCount, totalBytes, sourceDev, sourceInode, availableAt) || treeDigest != nil {
				return fmt.Errorf("%w: inconsistent reserved change", ErrCorruptState)
			}
		case "selected", "available":
			commitLength := 0
			if objectFormat.Valid && objectFormat.String == "sha1" {
				commitLength = 20
			} else if objectFormat.Valid && objectFormat.String == "sha256" {
				commitLength = 32
			}
			if commitLength == 0 || len(selectedCommit) != commitLength || !repositoryRoot.Valid || byteLen(repositoryRoot.String) < 1 || byteLen(repositoryRoot.String) > 4096 || repositoryRoot.String[0] != '/' || !repositoryDev.Valid || repositoryDev.Int64 < 0 || !repositoryInode.Valid || repositoryInode.Int64 < 1 || !selectedAt.Valid || selectedAt.Int64 < 0 {
				return fmt.Errorf("%w: invalid selected change", ErrCorruptState)
			}
			availableFields := len(treeDigest) == DigestBytes && fileCount.Valid && fileCount.Int64 >= 0 && fileCount.Int64 <= 100000 && totalBytes.Valid && totalBytes.Int64 >= 0 && totalBytes.Int64 <= 1073741824 && sourceDev.Valid && sourceDev.Int64 >= 0 && sourceInode.Valid && sourceInode.Int64 > 0 && availableAt.Valid && availableAt.Int64 >= 0
			if phase == "selected" && (treeDigest != nil || anyNullIntValid(fileCount, totalBytes, sourceDev, sourceInode, availableAt)) || phase == "available" && !availableFields {
				return fmt.Errorf("%w: inconsistent change materialization", ErrCorruptState)
			}
		default:
			return corruptControl("change phase", phase)
		}
	}
	return rows.Err()
}

func validateRuns(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, agent_id, task_id, task_incarnation_id, change_id, role, provider, execution_mode, reasoning_effort, phase, proposal_kind, proposal_code, terminal_kind, terminal_code, credential_digest, revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, credential_revoked_at_ms, updated_at_ms FROM runs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID, agentID, taskID, incarnationID, changeID, credentialDigest []byte
		var role, provider, mode, phase string
		var effort, proposalKind, proposalCode, terminalKind, terminalCode sql.NullString
		var revision, admittedAt, updatedAt int64
		var runningAt, finalizingAt, terminalAt, revokedAt sql.NullInt64
		if err := rows.Scan(&id, &projectID, &agentID, &taskID, &incarnationID, &changeID, &role, &provider, &mode, &effort, &phase, &proposalKind, &proposalCode, &terminalKind, &terminalCode, &credentialDigest, &revision, &admittedAt, &runningAt, &finalizingAt, &terminalAt, &revokedAt, &updatedAt); err != nil {
			return fmt.Errorf("scan run controls: %w", err)
		}
		_, roleErr := parseAgentRole(role)
		_, providerErr := parseProvider(provider)
		executionMode, modeErr := parseExecutionMode(mode)
		if !validNonzeroID(id) || !validNonzeroID(projectID) || !validNonzeroID(agentID) || !validNonzeroID(taskID) || !validNonzeroID(incarnationID) || roleErr != nil || providerErr != nil || modeErr != nil || effort.Valid && !validReasoningEffort(effort.String) || len(credentialDigest) != DigestBytes || revision < 1 || admittedAt < 0 || updatedAt < admittedAt || provider == ProviderShell.String() && executionMode != ExecutionUnrestricted {
			return fmt.Errorf("%w: invalid run controls", ErrCorruptState)
		}
		if role == RoleWorker.String() && !validNonzeroID(changeID) || role == RoleOrchestrator.String() && changeID != nil {
			return fmt.Errorf("%w: invalid run change binding", ErrCorruptState)
		}
		if !validOutcomePair(proposalKind, proposalCode) || !validOutcomePair(terminalKind, terminalCode) {
			return fmt.Errorf("%w: invalid run outcome controls", ErrCorruptState)
		}
		switch phase {
		case "admitted":
			if anyNullIntValid(runningAt, finalizingAt, terminalAt, revokedAt) || proposalKind.Valid || terminalKind.Valid {
				return fmt.Errorf("%w: inconsistent admitted run", ErrCorruptState)
			}
		case "running":
			if !runningAt.Valid || runningAt.Int64 < admittedAt || anyNullIntValid(finalizingAt, terminalAt, revokedAt) || proposalKind.Valid || terminalKind.Valid {
				return fmt.Errorf("%w: inconsistent running run", ErrCorruptState)
			}
		case "finalizing":
			if !finalizingAt.Valid || finalizingAt.Int64 < admittedAt || terminalAt.Valid || !revokedAt.Valid || revokedAt.Int64 < 0 || !proposalKind.Valid || terminalKind.Valid {
				return fmt.Errorf("%w: inconsistent finalizing run", ErrCorruptState)
			}
		case "terminal":
			if !finalizingAt.Valid || !terminalAt.Valid || !revokedAt.Valid || !proposalKind.Valid || !terminalKind.Valid || proposalKind.String != terminalKind.String || nullableValue(proposalCode) != nullableValue(terminalCode) {
				return fmt.Errorf("%w: inconsistent terminal run", ErrCorruptState)
			}
		default:
			return corruptControl("run phase", phase)
		}
	}
	return rows.Err()
}

func validateResources(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, run_id, kind, state, path, path_dev, path_inode, pid, pgid, birth_digest, unresolved_reason, revision, declared_at_ms, updated_at_ms, released_at_ms FROM resources`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, runID, birthDigest []byte
		var kind, state string
		var path, reason sql.NullString
		var pathDev, pathInode, pid, pgid, releasedAt sql.NullInt64
		var revision, declaredAt, updatedAt int64
		if err := rows.Scan(&id, &runID, &kind, &state, &path, &pathDev, &pathInode, &pid, &pgid, &birthDigest, &reason, &revision, &declaredAt, &updatedAt, &releasedAt); err != nil {
			return fmt.Errorf("scan resource: %w", err)
		}
		if !validNonzeroID(id) || !validNonzeroID(runID) || revision < 1 || declaredAt < 0 || updatedAt < declaredAt {
			return fmt.Errorf("%w: invalid resource row", ErrCorruptState)
		}
		switch kind {
		case "runtime_root":
			if !path.Valid || byteLen(path.String) < 1 || byteLen(path.String) > 4096 || path.String[0] != '/' || pid.Valid || pgid.Valid || birthDigest != nil {
				return fmt.Errorf("%w: invalid runtime root", ErrCorruptState)
			}
		case "runner_process", "provider_process", "provider_group":
			if path.Valid || pathDev.Valid || pathInode.Valid {
				return fmt.Errorf("%w: invalid process resource", ErrCorruptState)
			}
		default:
			return corruptControl("resource kind", kind)
		}
		if pathDev.Valid != pathInode.Valid || pathDev.Valid && (pathDev.Int64 < 0 || pathInode.Int64 < 1) || pid.Valid != pgid.Valid || pid.Valid != (birthDigest != nil) || pid.Valid && (pid.Int64 <= 1 || pgid.Int64 <= 1 || len(birthDigest) != DigestBytes) {
			return fmt.Errorf("%w: inconsistent resource identity", ErrCorruptState)
		}
		switch state {
		case "declared", "releasing":
			if releasedAt.Valid {
				return fmt.Errorf("%w: released time on live resource", ErrCorruptState)
			}
		case "active":
			if releasedAt.Valid || kind == "runtime_root" && !pathDev.Valid || kind != "runtime_root" && !pid.Valid {
				return fmt.Errorf("%w: invalid active resource", ErrCorruptState)
			}
		case "unresolved":
			if releasedAt.Valid || !reason.Valid || byteLen(reason.String) < 1 || byteLen(reason.String) > 4096 {
				return fmt.Errorf("%w: invalid unresolved resource", ErrCorruptState)
			}
		case "released":
			if !releasedAt.Valid || releasedAt.Int64 < declaredAt {
				return fmt.Errorf("%w: invalid released resource", ErrCorruptState)
			}
		default:
			return corruptControl("resource state", state)
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

func validateInvalidationBounds(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, factory FactoryState) error {
	var count, minimum, maximum int64
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(sequence), 0), COALESCE(MAX(sequence), 0) FROM invalidations`).Scan(&count, &minimum, &maximum); err != nil {
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

func validOutcomePair(kind, code sql.NullString) bool {
	if !kind.Valid {
		return !code.Valid
	}
	switch kind.String {
	case "succeeded", "blocked", "cancelled":
		return !code.Valid
	case "failed":
		if !code.Valid {
			return false
		}
		switch code.String {
		case "spawn", "activation", "source", "runner_exit", "protocol", "internal":
			return true
		}
	}
	return false
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

func nullableValue(value sql.NullString) string {
	if !value.Valid {
		return "<NULL>"
	}
	return value.String
}
