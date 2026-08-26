package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const runColumns = `id, project_id, agent_id, task_id, task_incarnation_id,
	    admitted_task_work_revision, change_id, role, provider, execution_mode, model, reasoning_effort, verification_policy, phase,
    proposal_kind, proposal_code, proposal_detail, proposal_result,
    terminal_kind, terminal_code, terminal_detail, terminal_result,
    credential_digest, credential_revoked_at_ms,
    runner_exit_sequence, runner_exit_code, runner_exit_signal, runner_exit_at_ms,
    revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms`

const resourceColumns = `id, run_id, kind, state, path, path_dev, path_inode, pid, pgid, birth_digest,
    unresolved_reason, revision, declared_at_ms, updated_at_ms, released_at_ms`

func runByID(ctx context.Context, connection *sql.Conn, id RunID) (Run, bool, error) {
	if id.zero() {
		return Run{}, false, fmt.Errorf("%w: zero run identifier", ErrInvalidValue)
	}
	return scanRun(connection.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id.Bytes()))
}

func runByDigest(ctx context.Context, connection *sql.Conn, digest AttemptDigest) (Run, bool, error) {
	return scanRun(connection.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE credential_digest = ?`, digest.Bytes()))
}

func scanRun(scanner rowScanner) (Run, bool, error) {
	var rawID, rawProjectID, rawAgentID, rawTaskID, rawIncarnationID, rawChangeID, rawDigest []byte
	var roleValue, providerValue, modeValue, verificationValue, phaseValue string
	var model, effort sql.NullString
	var proposalKind, proposalCode, proposalDetail, proposalResult sql.NullString
	var terminalKind, terminalCode, terminalDetail, terminalResult sql.NullString
	var revokedAt, exitSequence, exitCode, exitSignal, exitAt sql.NullInt64
	var admittedWorkRevision, revision, admittedAt, updatedAt int64
	var runningAt, finalizingAt, terminalAt sql.NullInt64
	if err := scanner.Scan(
		&rawID, &rawProjectID, &rawAgentID, &rawTaskID, &rawIncarnationID, &admittedWorkRevision, &rawChangeID,
		&roleValue, &providerValue, &modeValue, &model, &effort, &verificationValue, &phaseValue,
		&proposalKind, &proposalCode, &proposalDetail, &proposalResult,
		&terminalKind, &terminalCode, &terminalDetail, &terminalResult,
		&rawDigest, &revokedAt, &exitSequence, &exitCode, &exitSignal, &exitAt,
		&revision, &admittedAt, &runningAt, &finalizingAt, &terminalAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, nil
		}
		return Run{}, false, fmt.Errorf("scan run: %w", err)
	}
	id, idErr := RunIDFromBytes(rawID)
	projectID, projectErr := ProjectIDFromBytes(rawProjectID)
	agentID, agentErr := AgentIDFromBytes(rawAgentID)
	taskID, taskErr := TaskIDFromBytes(rawTaskID)
	incarnationID, incarnationErr := IncarnationIDFromBytes(rawIncarnationID)
	workRevision, workErr := NewRevision(admittedWorkRevision)
	role, roleErr := parseAgentRole(roleValue)
	provider, providerErr := parseProvider(providerValue)
	mode, modeErr := parseExecutionMode(modeValue)
	verification, verificationErr := parseVerificationPolicy(verificationValue)
	phase, phaseErr := parseRunPhase(phaseValue)
	digest, digestErr := AttemptDigestFromBytes(rawDigest)
	rev, revisionErr := NewRevision(revision)
	admittedTime, admittedErr := NewUnixMillis(admittedAt)
	updatedTime, updatedErr := NewUnixMillis(updatedAt)
	if idErr != nil || projectErr != nil || agentErr != nil || taskErr != nil || incarnationErr != nil || workErr != nil || roleErr != nil || providerErr != nil || modeErr != nil || verificationErr != nil || phaseErr != nil || digestErr != nil || revisionErr != nil || admittedErr != nil || updatedErr != nil || updatedAt < admittedAt || model.Valid && (byteLen(model.String) < 1 || byteLen(model.String) > 128) || effort.Valid && (effort.String == "" || !validReasoningEffort(effort.String)) || provider == ProviderShell && mode != ExecutionUnrestricted {
		return Run{}, false, fmt.Errorf("%w: invalid run controls", ErrCorruptState)
	}
	result := Run{
		ID: id, ProjectID: projectID, AgentID: agentID, TaskID: taskID, TaskIncarnationID: incarnationID,
		AdmittedTaskWorkRevision: workRevision, Role: role, Provider: provider, ExecutionMode: mode,
		Model: nullStringValue(model), ReasoningEffort: nullStringValue(effort), VerificationPolicy: verification, Phase: phase,
		CredentialDigest: digest, Revision: rev, AdmittedAt: admittedTime, UpdatedAt: updatedTime,
	}
	if rawChangeID != nil {
		changeID, err := ChangeIDFromBytes(rawChangeID)
		if err != nil {
			return Run{}, false, fmt.Errorf("%w: invalid run Change binding", ErrCorruptState)
		}
		result.ChangeID = &changeID
	}
	if role == RoleWorker && result.ChangeID == nil || role == RoleOrchestrator && result.ChangeID != nil {
		return Run{}, false, fmt.Errorf("%w: invalid run Change binding", ErrCorruptState)
	}
	proposal, proposalPresent, proposalErr := decodeProposal(proposalKind, proposalCode, proposalDetail, proposalResult)
	terminal, terminalPresent, terminalErr := decodeProposal(terminalKind, terminalCode, terminalDetail, terminalResult)
	if proposalErr != nil || terminalErr != nil {
		return Run{}, false, fmt.Errorf("%w: invalid run outcome", ErrCorruptState)
	}
	if proposalPresent {
		if proposal.code == FailureUnverifiable {
			return Run{}, false, fmt.Errorf("%w: invalid proposed verification outcome", ErrCorruptState)
		}
		result.Proposal = &proposal
	}
	if terminalPresent {
		result.Terminal = &terminal
	}
	if revokedAt.Valid {
		value, err := NewUnixMillis(revokedAt.Int64)
		if err != nil {
			return Run{}, false, fmt.Errorf("%w: invalid credential revocation", ErrCorruptState)
		}
		result.CredentialRevokedAt = &value
	}
	if runningAt.Valid {
		value, err := NewUnixMillis(runningAt.Int64)
		if err != nil {
			return Run{}, false, fmt.Errorf("%w: invalid running time", ErrCorruptState)
		}
		result.RunningAt = &value
	}
	if finalizingAt.Valid {
		value, err := NewUnixMillis(finalizingAt.Int64)
		if err != nil {
			return Run{}, false, fmt.Errorf("%w: invalid finalizing time", ErrCorruptState)
		}
		result.FinalizingAt = &value
	}
	if terminalAt.Valid {
		value, err := NewUnixMillis(terminalAt.Int64)
		if err != nil {
			return Run{}, false, fmt.Errorf("%w: invalid terminal time", ErrCorruptState)
		}
		result.TerminalAt = &value
	}
	exit, exitPresent, exitErr := decodeRunnerExit(exitSequence, exitCode, exitSignal, exitAt)
	if exitErr != nil {
		return Run{}, false, fmt.Errorf("%w: invalid runner exit", ErrCorruptState)
	}
	if exitPresent {
		result.RunnerExit = &exit
	}
	if result.RunningAt != nil && result.RunningAt.Int64() < admittedAt || result.FinalizingAt != nil && result.FinalizingAt.Int64() < admittedAt || result.TerminalAt != nil && result.TerminalAt.Int64() < admittedAt || result.CredentialRevokedAt != nil && result.CredentialRevokedAt.Int64() < admittedAt {
		return Run{}, false, fmt.Errorf("%w: invalid run transition time", ErrCorruptState)
	}
	switch phase {
	case RunAdmitted:
		if result.RunningAt != nil || result.FinalizingAt != nil || result.TerminalAt != nil || result.CredentialRevokedAt != nil || result.Proposal != nil || result.Terminal != nil {
			return Run{}, false, fmt.Errorf("%w: inconsistent admitted run", ErrCorruptState)
		}
	case RunRunning:
		if result.RunningAt == nil || result.FinalizingAt != nil || result.TerminalAt != nil || result.CredentialRevokedAt != nil || result.Proposal != nil || result.Terminal != nil {
			return Run{}, false, fmt.Errorf("%w: inconsistent running run", ErrCorruptState)
		}
	case RunFinalizing:
		if result.FinalizingAt == nil || result.TerminalAt != nil || result.CredentialRevokedAt == nil || result.Proposal == nil || result.Terminal != nil {
			return Run{}, false, fmt.Errorf("%w: inconsistent finalizing run", ErrCorruptState)
		}
	case RunTerminal:
		if result.FinalizingAt == nil || result.TerminalAt == nil || result.CredentialRevokedAt == nil || result.Proposal == nil || result.Terminal == nil || !validTerminalRefinement(result) {
			return Run{}, false, fmt.Errorf("%w: inconsistent terminal run", ErrCorruptState)
		}
	}
	return result, true, nil
}

func decodeProposal(kind, code, detail, result sql.NullString) (Proposal, bool, error) {
	if !kind.Valid {
		if code.Valid || detail.Valid || result.Valid {
			return Proposal{}, false, ErrCorruptState
		}
		return Proposal{}, false, nil
	}
	parsedKind, err := parseOutcomeKind(kind.String)
	if err != nil {
		return Proposal{}, false, err
	}
	proposal := Proposal{kind: parsedKind, detail: nullStringValue(detail), result: nullStringValue(result)}
	if code.Valid {
		parsedCode, err := parseFailureCode(code.String)
		if err != nil {
			return Proposal{}, false, err
		}
		proposal.code = parsedCode
	}
	if !proposal.valid() {
		return Proposal{}, false, ErrCorruptState
	}
	return proposal, true, nil
}

func decodeRunnerExit(sequence, code, signal, at sql.NullInt64) (RunnerExit, bool, error) {
	if !sequence.Valid && !code.Valid && !signal.Valid && !at.Valid {
		return RunnerExit{}, false, nil
	}
	if !sequence.Valid || !at.Valid {
		return RunnerExit{}, false, ErrCorruptState
	}
	timeValue, err := NewUnixMillis(at.Int64)
	if err != nil {
		return RunnerExit{}, false, err
	}
	if code.Valid && !signal.Valid {
		exit, err := NewRunnerExitCode(uint64(sequence.Int64), code.Int64, timeValue)
		return exit, err == nil, err
	}
	if signal.Valid && !code.Valid {
		exit, err := NewRunnerExitSignal(uint64(sequence.Int64), signal.Int64, timeValue)
		return exit, err == nil, err
	}
	return RunnerExit{}, false, ErrCorruptState
}

func resourceByID(ctx context.Context, connection *sql.Conn, id ResourceID) (Resource, bool, error) {
	if id.zero() {
		return Resource{}, false, fmt.Errorf("%w: zero resource identifier", ErrInvalidValue)
	}
	return scanResource(connection.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM resources WHERE id = ?`, id.Bytes()))
}

func scanResource(scanner rowScanner) (Resource, bool, error) {
	var rawID, rawRunID, birth []byte
	var kindValue, stateValue string
	var path, reason sql.NullString
	var pathDev, pathInode, pid, pgid, releasedAt sql.NullInt64
	var revision, declaredAt, updatedAt int64
	if err := scanner.Scan(&rawID, &rawRunID, &kindValue, &stateValue, &path, &pathDev, &pathInode, &pid, &pgid, &birth, &reason, &revision, &declaredAt, &updatedAt, &releasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Resource{}, false, nil
		}
		return Resource{}, false, fmt.Errorf("scan resource: %w", err)
	}
	id, idErr := ResourceIDFromBytes(rawID)
	runID, runErr := RunIDFromBytes(rawRunID)
	kind, kindErr := parseResourceKind(kindValue)
	state, stateErr := parseResourceState(stateValue)
	rev, revisionErr := NewRevision(revision)
	declared, declaredErr := NewUnixMillis(declaredAt)
	updated, updatedErr := NewUnixMillis(updatedAt)
	if idErr != nil || runErr != nil || kindErr != nil || stateErr != nil || revisionErr != nil || declaredErr != nil || updatedErr != nil || updatedAt < declaredAt || reason.Valid && (byteLen(reason.String) < 1 || byteLen(reason.String) > 4096) {
		return Resource{}, false, fmt.Errorf("%w: invalid resource row", ErrCorruptState)
	}
	resource := Resource{ID: id, RunID: runID, Kind: kind, State: state, Path: nullStringValue(path), Identity: EmptyResourceIdentity(), UnresolvedReason: nullStringValue(reason), Revision: rev, DeclaredAt: declared, UpdatedAt: updated}
	if releasedAt.Valid {
		value, err := NewUnixMillis(releasedAt.Int64)
		if err != nil || releasedAt.Int64 < declaredAt {
			return Resource{}, false, fmt.Errorf("%w: invalid resource release time", ErrCorruptState)
		}
		resource.ReleasedAt = &value
	}
	if kind == ResourceRuntimeRoot {
		if !path.Valid || !validOwnedLocator(path.String) || pid.Valid || pgid.Valid || birth != nil {
			return Resource{}, false, fmt.Errorf("%w: invalid runtime-root resource", ErrCorruptState)
		}
		if pathDev.Valid || pathInode.Valid {
			if !pathDev.Valid || !pathInode.Valid {
				return Resource{}, false, fmt.Errorf("%w: partial path identity", ErrCorruptState)
			}
			identity, err := NewPathResourceIdentity(pathDev.Int64, pathInode.Int64)
			if err != nil {
				return Resource{}, false, fmt.Errorf("%w: invalid path identity", ErrCorruptState)
			}
			resource.Identity = identity
		}
	} else {
		if path.Valid || pathDev.Valid || pathInode.Valid {
			return Resource{}, false, fmt.Errorf("%w: path on process resource", ErrCorruptState)
		}
		if pid.Valid || pgid.Valid || birth != nil {
			if !pid.Valid || !pgid.Valid || len(birth) != DigestBytes {
				return Resource{}, false, fmt.Errorf("%w: partial process identity", ErrCorruptState)
			}
			birthDigest, err := BirthDigestFromBytes(birth)
			if err != nil {
				return Resource{}, false, fmt.Errorf("%w: invalid birth digest", ErrCorruptState)
			}
			identity, err := NewProcessResourceIdentity(pid.Int64, pgid.Int64, birthDigest)
			if err != nil {
				return Resource{}, false, fmt.Errorf("%w: invalid process identity", ErrCorruptState)
			}
			resource.Identity = identity
		}
	}
	switch state {
	case ResourceDeclared, ResourceReleasing:
		if resource.ReleasedAt != nil || reason.Valid {
			return Resource{}, false, fmt.Errorf("%w: inconsistent live resource", ErrCorruptState)
		}
	case ResourceActive:
		if resource.ReleasedAt != nil || reason.Valid || !resource.Identity.validFor(kind) {
			return Resource{}, false, fmt.Errorf("%w: inconsistent active resource", ErrCorruptState)
		}
	case ResourceUnresolved:
		if resource.ReleasedAt != nil || !reason.Valid {
			return Resource{}, false, fmt.Errorf("%w: inconsistent unresolved resource", ErrCorruptState)
		}
	case ResourceReleased:
		if resource.ReleasedAt == nil || reason.Valid {
			return Resource{}, false, fmt.Errorf("%w: inconsistent released resource", ErrCorruptState)
		}
	}
	return resource, true, nil
}

func (store *Store) Run(ctx context.Context, id RunID) (Run, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Close()
	run, found, err := runByID(ctx, tx.connection, id)
	if err != nil || !found {
		return run, found, err
	}
	resources, err := resourcesForRun(ctx, tx.connection, run.ID)
	if err != nil || !resourcesMatchRunPhase(run.Phase, resources) {
		if err == nil {
			err = ErrCorruptState
		}
		return Run{}, false, err
	}
	return run, true, nil
}

func (store *Store) Resource(ctx context.Context, id ResourceID) (Resource, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return Resource{}, false, err
	}
	defer tx.Close()
	resource, found, err := resourceByID(ctx, tx.connection, id)
	if err != nil || !found {
		return resource, found, err
	}
	run, runFound, err := runByID(ctx, tx.connection, resource.RunID)
	if err != nil || !runFound {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, false, err
	}
	resources, err := resourcesForRun(ctx, tx.connection, run.ID)
	if err != nil || !resourcesMatchRunPhase(run.Phase, resources) {
		if err == nil {
			err = ErrCorruptState
		}
		return Resource{}, false, err
	}
	return resource, true, nil
}

func resourcesForRun(ctx context.Context, connection *sql.Conn, runID RunID) ([]Resource, error) {
	rows, err := connection.QueryContext(ctx, `SELECT `+resourceColumns+` FROM resources WHERE run_id = ? ORDER BY kind, id`, runID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var resources []Resource
	for rows.Next() {
		resource, found, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrCorruptState
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

func (store *Store) RecoverableRuns(ctx context.Context) ([]RecoverableRun, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return nil, err
	}
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE phase <> 'terminal' ORDER BY admitted_at_ms ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	var runs []Run
	for rows.Next() {
		run, found, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if !found {
			rows.Close()
			return nil, ErrCorruptState
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]RecoverableRun, 0, len(runs))
	for _, run := range runs {
		recovered := RecoverableRun{Run: run}
		if run.ChangeID != nil {
			change, found, err := changeByID(ctx, tx.connection, *run.ChangeID)
			if err != nil || !found {
				if err == nil {
					err = ErrCorruptState
				}
				return nil, err
			}
			recovered.Change = &change
		}
		resources, err := resourcesForRun(ctx, tx.connection, run.ID)
		if err != nil {
			return nil, err
		}
		recovered.Resources = resources
		result = append(result, recovered)
	}
	return result, nil
}

func (store *Store) AuthenticateAttempt(ctx context.Context, digest AttemptDigest) (AttemptAuthority, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return AttemptAuthority{}, err
	}
	defer tx.Close()
	run, found, err := runByDigest(ctx, tx.connection, digest)
	if err != nil {
		return AttemptAuthority{}, err
	}
	if !found || run.Phase != RunRunning || run.CredentialRevokedAt != nil || !bytes.Equal(run.CredentialDigest.Bytes(), digest.Bytes()) {
		return AttemptAuthority{}, ErrUnauthorized
	}
	resources, err := resourcesForRun(ctx, tx.connection, run.ID)
	if err != nil {
		return AttemptAuthority{}, err
	}
	if !resourcesMatchRunPhase(run.Phase, resources) {
		return AttemptAuthority{}, ErrCorruptState
	}
	return AttemptAuthority{RunID: run.ID, ProjectID: run.ProjectID, AgentID: run.AgentID, TaskID: run.TaskID, TaskIncarnation: run.TaskIncarnationID, Role: run.Role, Provider: run.Provider, ExecutionMode: run.ExecutionMode, ChangeID: run.ChangeID}, nil
}
