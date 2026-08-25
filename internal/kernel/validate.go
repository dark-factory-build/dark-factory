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
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, task_id, task_incarnation_id, phase, source_root, object_format, selected_commit, repository_root, repository_dev, repository_inode, selected_at_ms, tree_digest, entry_count, total_bytes, source_dev, source_inode, available_at_ms, revision, created_at_ms, updated_at_ms FROM changes`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID, taskID, incarnationID []byte
		var phase, sourceRoot string
		var objectFormat, repositoryRoot sql.NullString
		var selectedCommit, treeDigest []byte
		var repositoryDev, repositoryInode, selectedAt, entryCount, totalBytes, sourceDev, sourceInode, availableAt sql.NullInt64
		var revision, createdAt, updatedAt int64
		if err := rows.Scan(&id, &projectID, &taskID, &incarnationID, &phase, &sourceRoot, &objectFormat, &selectedCommit, &repositoryRoot, &repositoryDev, &repositoryInode, &selectedAt, &treeDigest, &entryCount, &totalBytes, &sourceDev, &sourceInode, &availableAt, &revision, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("scan change: %w", err)
		}
		if !validNonzeroID(id) || !validNonzeroID(projectID) || !validNonzeroID(taskID) || !validNonzeroID(incarnationID) || byteLen(sourceRoot) < 1 || byteLen(sourceRoot) > 4096 || sourceRoot[0] != '/' || revision < 1 || createdAt < 0 || updatedAt < createdAt {
			return fmt.Errorf("%w: invalid change row", ErrCorruptState)
		}
		switch phase {
		case "reserved":
			if objectFormat.Valid || selectedCommit != nil || repositoryRoot.Valid || anyNullIntValid(repositoryDev, repositoryInode, selectedAt, entryCount, totalBytes, sourceDev, sourceInode, availableAt) || treeDigest != nil {
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
			availableFields := len(treeDigest) == DigestBytes && entryCount.Valid && entryCount.Int64 >= 0 && entryCount.Int64 <= MaxChangeTreeEntries && totalBytes.Valid && totalBytes.Int64 >= 0 && totalBytes.Int64 <= MaxChangeTreeBlobBytes && sourceDev.Valid && sourceDev.Int64 >= 0 && sourceInode.Valid && sourceInode.Int64 > 0 && availableAt.Valid && availableAt.Int64 >= 0
			if phase == "selected" && (treeDigest != nil || anyNullIntValid(entryCount, totalBytes, sourceDev, sourceInode, availableAt)) || phase == "available" && !availableFields {
				return fmt.Errorf("%w: inconsistent change materialization", ErrCorruptState)
			}
		default:
			return corruptControl("change phase", phase)
		}
	}
	return rows.Err()
}

func validateRuns(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, agent_id, task_id, task_incarnation_id,
        admitted_task_work_revision, change_id, role, provider, execution_mode, model, reasoning_effort, phase,
        proposal_kind, proposal_code, proposal_detail, proposal_result,
        terminal_kind, terminal_code, terminal_detail, terminal_result,
        credential_digest, credential_revoked_at_ms,
        runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
        revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms
        FROM runs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID, agentID, taskID, incarnationID, changeID, credentialDigest []byte
		var role, provider, mode, phase string
		var model, effort sql.NullString
		var proposalKind, proposalCode, proposalDetail, proposalResult sql.NullString
		var terminalKind, terminalCode, terminalDetail, terminalResult sql.NullString
		var admittedWorkRevision, revision, admittedAt, updatedAt int64
		var runningAt, finalizingAt, terminalAt, revokedAt sql.NullInt64
		var exitSequence, exitCode, exitSignal, exitAt sql.NullInt64
		if err := rows.Scan(
			&id, &projectID, &agentID, &taskID, &incarnationID, &admittedWorkRevision, &changeID,
			&role, &provider, &mode, &model, &effort, &phase,
			&proposalKind, &proposalCode, &proposalDetail, &proposalResult,
			&terminalKind, &terminalCode, &terminalDetail, &terminalResult,
			&credentialDigest, &revokedAt, &exitSequence, &exitCode, &exitSignal, &exitAt,
			&revision, &admittedAt, &runningAt, &finalizingAt, &terminalAt, &updatedAt,
		); err != nil {
			return fmt.Errorf("scan run controls: %w", err)
		}
		_, roleErr := parseAgentRole(role)
		_, providerErr := parseProvider(provider)
		executionMode, modeErr := parseExecutionMode(mode)
		if !validNonzeroID(id) || !validNonzeroID(projectID) || !validNonzeroID(agentID) || !validNonzeroID(taskID) || !validNonzeroID(incarnationID) || admittedWorkRevision < 1 || roleErr != nil || providerErr != nil || modeErr != nil || model.Valid && (byteLen(model.String) < 1 || byteLen(model.String) > 128) || effort.Valid && (effort.String == "" || !validReasoningEffort(effort.String)) || len(credentialDigest) != DigestBytes || revision < 1 || admittedAt < 0 || updatedAt < admittedAt || provider == ProviderShell.String() && executionMode != ExecutionUnrestricted {
			return fmt.Errorf("%w: invalid run controls", ErrCorruptState)
		}
		if role == RoleWorker.String() && !validNonzeroID(changeID) || role == RoleOrchestrator.String() && changeID != nil {
			return fmt.Errorf("%w: invalid run change binding", ErrCorruptState)
		}
		if runningAt.Valid && runningAt.Int64 < admittedAt || finalizingAt.Valid && finalizingAt.Int64 < admittedAt || terminalAt.Valid && terminalAt.Int64 < admittedAt || revokedAt.Valid && revokedAt.Int64 < 0 {
			return fmt.Errorf("%w: invalid run transition time", ErrCorruptState)
		}
		if !validOutcome(proposalKind, proposalCode, proposalDetail, proposalResult) || !validOutcome(terminalKind, terminalCode, terminalDetail, terminalResult) || !validRunnerExit(exitSequence, exitCode, exitSignal, exitAt) {
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
			if !finalizingAt.Valid || finalizingAt.Int64 < admittedAt || !terminalAt.Valid || terminalAt.Int64 < admittedAt || !revokedAt.Valid || revokedAt.Int64 < 0 || !proposalKind.Valid || !terminalKind.Valid || !sameNullable(proposalKind, terminalKind) || !sameNullable(proposalCode, terminalCode) || !sameNullable(proposalDetail, terminalDetail) || !sameNullable(proposalResult, terminalResult) {
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
		if !validNonzeroID(id) || !validNonzeroID(runID) || revision < 1 || declaredAt < 0 || updatedAt < declaredAt || reason.Valid && (byteLen(reason.String) < 1 || byteLen(reason.String) > 4096) {
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

func validOutcome(kind, code, detail, result sql.NullString) bool {
	if !kind.Valid {
		return !code.Valid && !detail.Valid && !result.Valid
	}
	switch kind.String {
	case "succeeded":
		return !code.Valid && !detail.Valid && result.Valid && byteLen(result.String) <= 131072
	case "blocked", "cancelled":
		return !code.Valid && detail.Valid && byteLen(detail.String) >= 1 && byteLen(detail.String) <= 4096 && !result.Valid
	case "failed":
		if !code.Valid || result.Valid || detail.Valid && (byteLen(detail.String) < 1 || byteLen(detail.String) > 4096) {
			return false
		}
		switch code.String {
		case "spawn", "activation", "source", "runner_exit", "protocol", "internal":
			return true
		}
	}
	return false
}

func validRunnerExit(sequence, code, signal, at sql.NullInt64) bool {
	if !sequence.Valid && !code.Valid && !signal.Valid && !at.Valid {
		return true
	}
	return sequence.Valid && sequence.Int64 >= 1 && at.Valid && at.Int64 >= 0 &&
		(code.Valid && code.Int64 >= 0 && !signal.Valid || !code.Valid && signal.Valid && signal.Int64 > 0)
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

func sameNullable(left, right sql.NullString) bool {
	return left.Valid == right.Valid && (!left.Valid || left.String == right.String)
}
