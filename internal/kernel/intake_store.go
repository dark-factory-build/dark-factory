package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"math"
	"strings"
)

const intakeSourceColumns = `id, github_repository_id, github_repository_name, project_id, target_repository_id, overseer_agent_id, label_filter, enabled, policy, poll_seconds, admission_limit, revision, created_at_ms, updated_at_ms`
const intakeAcceptanceColumns = `source_repository, id, github_repository_id, issue_number, issue_node_id, title, body, body_hash, project_id, repository_id, overseer_agent_id, task_id, incarnation_id, withdrawn_at_ms, created_at_ms`

func scanIntakeSource(scanner rowScanner) (IntakeSource, bool, error) {
	var rawID, rawProject, rawRepository, rawAgent []byte
	var repositoryID, poll, limit, revision, created, updated int64
	var name, label, policy string
	var enabled int
	if err := scanner.Scan(&rawID, &repositoryID, &name, &rawProject, &rawRepository, &rawAgent, &label, &enabled, &policy, &poll, &limit, &revision, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return IntakeSource{}, false, nil
		}
		return IntakeSource{}, false, err
	}
	id, e1 := IntakeSourceIDFromBytes(rawID)
	project, e2 := ProjectIDFromBytes(rawProject)
	target, e3 := RepositoryIDFromBytes(rawRepository)
	var agent AgentID
	var e4 error
	if rawAgent != nil {
		agent, e4 = AgentIDFromBytes(rawAgent)
	}
	rev, e5 := NewRevision(revision)
	at, e6 := NewUnixMillis(created)
	changed, e7 := NewUnixMillis(updated)
	value := IntakeSource{ID: id, GitHubRepositoryID: uint64(repositoryID), GitHubRepositoryName: name, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, LabelFilter: label, Enabled: enabled == 1, Policy: IntakePolicy(policy), PollSeconds: uint32(poll), AdmissionLimit: uint16(limit), Revision: rev, CreatedAt: at, UpdatedAt: changed}
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || repositoryID < 1 || poll < 0 || limit < 0 || enabled < 0 || enabled > 1 || !validIntakeSource(value) {
		return IntakeSource{}, false, ErrCorruptState
	}
	return value, true, nil
}

func intakeSourceByID(ctx context.Context, connection *sql.Conn, id IntakeSourceID) (IntakeSource, bool, error) {
	if id.zero() {
		return IntakeSource{}, false, ErrInvalidValue
	}
	value, found, err := scanIntakeSource(connection.QueryRowContext(ctx, `SELECT `+intakeSourceColumns+` FROM intake_sources WHERE id = ?`, id.Bytes()))
	if err != nil || !found {
		return value, found, err
	}
	rows, err := connection.QueryContext(ctx, `SELECT login FROM intake_source_trusted_logins WHERE source_id = ? ORDER BY login COLLATE NOCASE`, id.Bytes())
	if err != nil {
		return IntakeSource{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var login string
		if err := rows.Scan(&login); err != nil {
			return IntakeSource{}, false, err
		}
		value.TrustedGitHubLogins = append(value.TrustedGitHubLogins, login)
	}
	if err := rows.Err(); err != nil {
		return IntakeSource{}, false, err
	}
	if !validIntakeSource(value) {
		return IntakeSource{}, false, ErrCorruptState
	}
	return value, true, nil
}

func (store *Store) IntakeSource(ctx context.Context, id IntakeSourceID) (IntakeSource, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return IntakeSource{}, false, err
	}
	defer read.Close()
	return intakeSourceByID(ctx, read.connection, id)
}

func (store *Store) IntakeSources(ctx context.Context) ([]IntakeSource, error) {
	return store.intakeSources(ctx, `SELECT id FROM intake_sources ORDER BY created_at_ms, id`)
}

func (store *Store) ProjectIntakeSources(ctx context.Context, projectID ProjectID) ([]IntakeSource, error) {
	if projectID.zero() {
		return nil, ErrInvalidValue
	}
	return store.intakeSources(ctx, `SELECT id FROM intake_sources WHERE project_id = ? ORDER BY created_at_ms, id`, projectID.Bytes())
}

func (store *Store) intakeSources(ctx context.Context, query string, arguments ...any) ([]IntakeSource, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	rows, err := read.connection.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IntakeSource{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := IntakeSourceIDFromBytes(raw)
		if err != nil {
			return nil, ErrCorruptState
		}
		source, found, err := intakeSourceByID(ctx, read.connection, id)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

func (store *Store) SetIntakeSourceEnabled(ctx context.Context, id IntakeSourceID, expected Revision, enabled bool, at UnixMillis) (IntakeSource, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeSource{}, err
	}
	defer tx.Close()
	source, found, err := intakeSourceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return IntakeSource{}, tx.Rollback(err)
	}
	if source.Revision != expected {
		return IntakeSource{}, tx.Rollback(ErrRevisionConflict)
	}
	if source.Enabled != enabled {
		result, err := tx.connection.ExecContext(ctx, `UPDATE intake_sources SET enabled = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, boolInt(enabled), at.Int64(), id.Bytes(), expected.Int64())
		if err = requireOneRow(result, err); err != nil {
			return IntakeSource{}, tx.Rollback(err)
		}
	}
	source, found, err = intakeSourceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return IntakeSource{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeSource{}, err
	}
	return source, nil
}

// UpdateIntakeSource replaces one source configuration at the revision the
// operator observed. Accepted snapshots keep their recorded destination.
func (store *Store) UpdateIntakeSource(ctx context.Context, id IntakeSourceID, expected Revision, spec NewIntakeSource, enabled bool, at UnixMillis) (IntakeSource, error) {
	if id.zero() || spec.ID != id {
		return IntakeSource{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeSource{}, err
	}
	defer tx.Close()
	existing, found, err := intakeSourceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return IntakeSource{}, tx.Rollback(err)
	}
	if existing.Revision != expected || at.Int64() < existing.UpdatedAt.Int64() {
		return IntakeSource{}, tx.Rollback(ErrRevisionConflict)
	}
	value := IntakeSource{ID: id, GitHubRepositoryID: spec.GitHubRepositoryID, GitHubRepositoryName: spec.GitHubRepositoryName, ProjectID: spec.ProjectID, TargetRepositoryID: spec.TargetRepositoryID, OverseerAgentID: spec.OverseerAgentID, LabelFilter: spec.LabelFilter, Enabled: enabled, Policy: spec.Policy, TrustedGitHubLogins: append([]string(nil), spec.TrustedGitHubLogins...), PollSeconds: spec.PollSeconds, AdmissionLimit: spec.AdmissionLimit, Revision: existing.Revision, CreatedAt: existing.CreatedAt, UpdatedAt: at}
	if !validIntakeSource(value) {
		return IntakeSource{}, tx.Rollback(ErrInvalidValue)
	}
	if err := validateIntakeSourceRoute(ctx, tx.connection, value); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE intake_sources SET github_repository_id = ?, github_repository_name = ?, project_id = ?, target_repository_id = ?, overseer_agent_id = ?, label_filter = ?, enabled = ?, policy = ?, poll_seconds = ?, admission_limit = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, int64(value.GitHubRepositoryID), value.GitHubRepositoryName, value.ProjectID.Bytes(), value.TargetRepositoryID.Bytes(), nullableAgentID(value.OverseerAgentID), value.LabelFilter, boolInt(value.Enabled), string(value.Policy), int64(value.PollSeconds), int64(value.AdmissionLimit), at.Int64(), id.Bytes(), expected.Int64()); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `DELETE FROM intake_source_trusted_logins WHERE source_id = ?`, id.Bytes()); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	for _, login := range value.TrustedGitHubLogins {
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_source_trusted_logins(source_id, login) VALUES(?, ?)`, id.Bytes(), strings.ToLower(login)); err != nil {
			return IntakeSource{}, tx.Rollback(err)
		}
	}
	value, found, err = intakeSourceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return IntakeSource{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeSource{}, err
	}
	return value, nil
}

func (store *Store) CreateIntakeSource(ctx context.Context, spec NewIntakeSource, at UnixMillis) (IntakeSource, error) {
	policy := spec.Policy
	if policy == "" {
		policy = IntakePolicyManual
	}
	value := IntakeSource{ID: spec.ID, GitHubRepositoryID: spec.GitHubRepositoryID, GitHubRepositoryName: spec.GitHubRepositoryName, ProjectID: spec.ProjectID, TargetRepositoryID: spec.TargetRepositoryID, OverseerAgentID: spec.OverseerAgentID, LabelFilter: spec.LabelFilter, Policy: policy, TrustedGitHubLogins: append([]string(nil), spec.TrustedGitHubLogins...), PollSeconds: spec.PollSeconds, AdmissionLimit: spec.AdmissionLimit, Revision: Revision{value: 1}, CreatedAt: at, UpdatedAt: at}
	if !validIntakeSource(value) {
		return IntakeSource{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeSource{}, err
	}
	defer tx.Close()
	if _, found, err := intakeSourceByID(ctx, tx.connection, value.ID); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	} else if found {
		return IntakeSource{}, tx.Rollback(ErrConflict)
	}
	if err := validateIntakeSourceRoute(ctx, tx.connection, value); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_sources(`+intakeSourceColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 1, ?, ?)`, value.ID.Bytes(), int64(value.GitHubRepositoryID), value.GitHubRepositoryName, value.ProjectID.Bytes(), value.TargetRepositoryID.Bytes(), nullableAgentID(value.OverseerAgentID), value.LabelFilter, string(value.Policy), int64(value.PollSeconds), int64(value.AdmissionLimit), at.Int64(), at.Int64()); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	for _, login := range value.TrustedGitHubLogins {
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_source_trusted_logins(source_id, login) VALUES(?, ?)`, value.ID.Bytes(), strings.ToLower(login)); err != nil {
			return IntakeSource{}, tx.Rollback(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeSource{}, err
	}
	result, found, err := store.IntakeSource(ctx, value.ID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return IntakeSource{}, err
	}
	return result, nil
}

func validateIntakeSourceRoute(ctx context.Context, connection *sql.Conn, source IntakeSource) error {
	repository, found, err := repositoryByID(ctx, connection, source.TargetRepositoryID)
	if err != nil || !found || repository.ProjectID != source.ProjectID {
		if err == nil {
			err = ErrConflict
		}
		return err
	}
	if source.OverseerAgentID.zero() {
		return nil
	}
	agent, found, err := agentByID(ctx, connection, source.OverseerAgentID)
	if err != nil || !found || agent.ProjectID != source.ProjectID || agent.Role != RoleOrchestrator || agent.Archived {
		if err == nil {
			err = ErrConflict
		}
		return err
	}
	return nil
}

func scanIntakeAcceptance(scanner rowScanner) (IntakeAcceptance, bool, error) {
	var rawID, rawProject, rawRepository, rawAgent, rawTask, rawIncarnation, hash []byte
	var repositoryID, number, created int64
	var withdrawn sql.NullInt64
	var sourceRepository, node, title, body string
	if err := scanner.Scan(&sourceRepository, &rawID, &repositoryID, &number, &node, &title, &body, &hash, &rawProject, &rawRepository, &rawAgent, &rawTask, &rawIncarnation, &withdrawn, &created); err != nil {
		if err == sql.ErrNoRows {
			return IntakeAcceptance{}, false, nil
		}
		return IntakeAcceptance{}, false, err
	}
	id, e1 := IntakeAcceptanceIDFromBytes(rawID)
	project, e2 := ProjectIDFromBytes(rawProject)
	repository, e3 := RepositoryIDFromBytes(rawRepository)
	task, e4 := TaskIDFromBytes(rawTask)
	incarnation, e5 := IncarnationIDFromBytes(rawIncarnation)
	at, e6 := NewUnixMillis(created)
	var agent AgentID
	var e7 error
	if rawAgent != nil {
		agent, e7 = AgentIDFromBytes(rawAgent)
	}
	var withdrawnAt *UnixMillis
	if withdrawn.Valid {
		value, err := NewUnixMillis(withdrawn.Int64)
		if err != nil {
			e7 = err
		} else {
			withdrawnAt = &value
		}
	}
	snapshot := IntakeIssueSnapshot{GitHubRepositoryID: uint64(repositoryID), IssueNumber: uint64(number), NodeID: node, Title: title, Body: body}
	value := IntakeAcceptance{SourceRepository: sourceRepository, ID: id, Snapshot: snapshot, BodyHash: sha256.Sum256([]byte(body)), ProjectID: project, RepositoryID: repository, OverseerAgentID: agent, TaskID: task, IncarnationID: incarnation, WithdrawnAt: withdrawnAt, CreatedAt: at}
	expectedID, expectedTask, expectedIncarnation, identityErr := intakeAcceptanceIDs(snapshot, project, repository)
	if !validGitHubRepositoryName(sourceRepository) || e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || identityErr != nil || repositoryID < 1 || number < 1 || len(hash) != DigestBytes || value.BodyHash != [DigestBytes]byte(hash) || id != expectedID || task != expectedTask || incarnation != expectedIncarnation || withdrawn.Valid && (withdrawn.Int64 < 0 || withdrawn.Int64 < created) {
		return IntakeAcceptance{}, false, ErrCorruptState
	}
	return value, true, nil
}

func intakeAcceptanceByID(ctx context.Context, connection *sql.Conn, id IntakeAcceptanceID) (IntakeAcceptance, bool, error) {
	if id.zero() {
		return IntakeAcceptance{}, false, ErrInvalidValue
	}
	return scanIntakeAcceptance(connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE id = ?`, id.Bytes()))
}

func (store *Store) IntakeAcceptance(ctx context.Context, id IntakeAcceptanceID) (IntakeAcceptance, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer read.Close()
	return intakeAcceptanceByID(ctx, read.connection, id)
}

func (store *Store) LatestIntakeAcceptance(ctx context.Context, repositoryID, number uint64, nodeID string, projectID ProjectID, targetRepositoryID RepositoryID) (IntakeAcceptance, bool, error) {
	if repositoryID == 0 || repositoryID > math.MaxInt64 || number == 0 || number > math.MaxInt64 || !validBoundedIntakeText(nodeID, 1, maxGitHubNodeIDBytes) || projectID.zero() || targetRepositoryID.zero() {
		return IntakeAcceptance{}, false, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer read.Close()
	return scanIntakeAcceptance(read.connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE github_repository_id = ? AND issue_number = ? AND issue_node_id = ? AND project_id = ? AND repository_id = ? ORDER BY created_at_ms DESC, id DESC LIMIT 1`, int64(repositoryID), int64(number), nodeID, projectID.Bytes(), targetRepositoryID.Bytes()))
}

// PendingIntakeAcceptances returns receipts that still need their first task
// insert. A newer receipt for the same issue suppresses an older one, even if
// the newer receipt was withdrawn: importing an old review after later review
// activity would evade the controller's exact-content check.
func (store *Store) PendingIntakeAcceptances(ctx context.Context, sourceID IntakeSourceID, limit uint16) ([]IntakeAcceptance, error) {
	if sourceID.zero() || limit < 1 || limit > 200 {
		return nil, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	source, found, err := intakeSourceByID(ctx, read.connection, sourceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	if !source.Enabled {
		return []IntakeAcceptance{}, nil
	}
	columns := "accepted." + strings.ReplaceAll(intakeAcceptanceColumns, ", ", ", accepted.")
	rows, err := read.connection.QueryContext(ctx, `SELECT `+columns+` FROM intake_acceptances accepted
		LEFT JOIN tasks task ON task.id = accepted.task_id
		WHERE accepted.github_repository_id = ? AND accepted.project_id = ? AND accepted.repository_id = ?
			AND accepted.withdrawn_at_ms IS NULL AND task.id IS NULL
			AND NOT EXISTS (
				SELECT 1 FROM intake_acceptances newer
				WHERE newer.github_repository_id = accepted.github_repository_id
					AND newer.issue_number = accepted.issue_number
					AND newer.issue_node_id = accepted.issue_node_id
					AND newer.project_id = accepted.project_id
					AND newer.repository_id = accepted.repository_id
					AND (newer.created_at_ms > accepted.created_at_ms OR newer.created_at_ms = accepted.created_at_ms AND newer.id > accepted.id)
			)
		ORDER BY accepted.created_at_ms, accepted.id LIMIT ?`, int64(source.GitHubRepositoryID), source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), int64(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IntakeAcceptance{}
	for rows.Next() {
		accepted, found, err := scanIntakeAcceptance(rows)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return nil, err
		}
		result = append(result, accepted)
	}
	return result, rows.Err()
}

// AcceptIntakeSnapshot records the exact bytes the operator reviewed. It does
// not enqueue; ImportIntakeAcceptance performs the acceptance-aware atomic task
// insert after the controller has persisted its reconciliation marker.
func (store *Store) AcceptIntakeSnapshot(ctx context.Context, sourceID IntakeSourceID, snapshot IntakeIssueSnapshot, at UnixMillis, expected ...Revision) (IntakeAcceptance, error) {
	if !validIntakeIssueSnapshot(snapshot) {
		return IntakeAcceptance{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeAcceptance{}, err
	}
	defer tx.Close()
	source, found, err := intakeSourceByID(ctx, tx.connection, sourceID)
	if err != nil || !found || source.GitHubRepositoryID != snapshot.GitHubRepositoryID {
		if err == nil {
			err = ErrConflict
		}
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	if len(expected) > 1 || len(expected) == 1 && source.Revision != expected[0] {
		return IntakeAcceptance{}, tx.Rollback(ErrRevisionConflict)
	}
	id, task, incarnation, err := intakeAcceptanceIDs(snapshot, source.ProjectID, source.TargetRepositoryID)
	if err != nil {
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	if existing, found, err := intakeAcceptanceByID(ctx, tx.connection, id); err != nil {
		return IntakeAcceptance{}, tx.Rollback(err)
	} else if found {
		if err := tx.Rollback(nil); err != nil {
			return IntakeAcceptance{}, err
		}
		return existing, nil
	}
	bodyHash := snapshot.BodyHash()
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_acceptances(`+intakeAcceptanceColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`, source.GitHubRepositoryName, id.Bytes(), int64(snapshot.GitHubRepositoryID), int64(snapshot.IssueNumber), snapshot.NodeID, snapshot.Title, snapshot.Body, bodyHash[:], source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), nullableAgentID(source.OverseerAgentID), task.Bytes(), incarnation.Bytes(), at.Int64()); err != nil {
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	value, found, err := intakeAcceptanceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeAcceptance{}, err
	}
	return value, nil
}

func (store *Store) WithdrawIntakeAcceptance(ctx context.Context, id IntakeAcceptanceID, at UnixMillis) (IntakeAcceptance, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeAcceptance{}, err
	}
	defer tx.Close()
	value, found, err := intakeAcceptanceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	if value.WithdrawnAt == nil {
		if _, err := tx.connection.ExecContext(ctx, `UPDATE intake_acceptances SET withdrawn_at_ms = ? WHERE id = ? AND withdrawn_at_ms IS NULL`, at.Int64(), id.Bytes()); err != nil {
			return IntakeAcceptance{}, tx.Rollback(err)
		}
	}
	value, found, err = intakeAcceptanceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeAcceptance{}, err
	}
	return value, nil
}

func (store *Store) ImportIntakeAcceptance(ctx context.Context, id IntakeAcceptanceID, at UnixMillis, expectedSource ...IntakeSource) (Task, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Close()
	accepted, found, err := intakeAcceptanceByID(ctx, tx.connection, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return Task{}, tx.Rollback(err)
	}
	if accepted.WithdrawnAt != nil {
		return Task{}, tx.Rollback(ErrConflict)
	}
	if len(expectedSource) > 1 {
		return Task{}, tx.Rollback(ErrInvalidValue)
	}
	if len(expectedSource) == 1 {
		source, found, err := intakeSourceByID(ctx, tx.connection, expectedSource[0].ID)
		if err != nil {
			return Task{}, tx.Rollback(err)
		}
		if !found || !source.Enabled || source.Revision != expectedSource[0].Revision || source.GitHubRepositoryID != accepted.Snapshot.GitHubRepositoryID || source.ProjectID != accepted.ProjectID || source.TargetRepositoryID != accepted.RepositoryID {
			return Task{}, tx.Rollback(ErrRevisionConflict)
		}
	}
	var permitted int
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intake_sources WHERE github_repository_id = ? AND project_id = ? AND target_repository_id = ? AND enabled = 1)`, int64(accepted.Snapshot.GitHubRepositoryID), accepted.ProjectID.Bytes(), accepted.RepositoryID.Bytes()).Scan(&permitted); err != nil || permitted == 0 {
		if err == nil {
			err = ErrConflict
		}
		return Task{}, tx.Rollback(err)
	}
	spec := NewTask{ID: accepted.TaskID, IncarnationID: accepted.IncarnationID, ProjectID: accepted.ProjectID, RepositoryID: accepted.RepositoryID, AssignedAgentID: accepted.OverseerAgentID, Title: accepted.Snapshot.Title, Body: accepted.Snapshot.Body}
	if err := validateNewTask(spec); err != nil {
		return Task{}, tx.Rollback(err)
	}
	existing, replay, err := intakeTaskReplay(ctx, tx.connection, spec)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if replay {
		if err := tx.Rollback(nil); err != nil {
			return Task{}, err
		}
		return existing, nil
	}
	value, err := insertTaskOnConnection(ctx, tx.connection, spec, at)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return value, nil
}

// intakeTaskReplay recognizes the immutable receipt even after ordinary task
// lifecycle changes. Replaying a controller journal record must never revive
// a completed or failed task.
func intakeTaskReplay(ctx context.Context, connection *sql.Conn, spec NewTask) (Task, bool, error) {
	existing, found, err := taskByID(ctx, connection, spec.ID)
	if err != nil || !found {
		return existing, found, err
	}
	if existing.ProjectID != spec.ProjectID || existing.IncarnationID != spec.IncarnationID {
		return Task{}, false, ErrConflict
	}
	var repository []byte
	if err := connection.QueryRowContext(ctx, `SELECT repository_id FROM task_repository_bindings WHERE task_id = ?`, spec.ID.Bytes()).Scan(&repository); err != nil {
		return Task{}, false, err
	}
	if string(repository) != string(spec.RepositoryID.Bytes()) {
		return Task{}, false, ErrConflict
	}
	return existing, true, nil
}

// PendingIntakeWithdrawals lets the existing controller finish an interrupted
// stop through the normal task/run controls, including while intake is paused.
func (store *Store) PendingIntakeWithdrawals(ctx context.Context, id IntakeSourceID, limit uint16) ([]IntakeAcceptance, error) {
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	source, found, err := intakeSourceByID(ctx, tx.connection, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE github_repository_id = ? AND project_id = ? AND repository_id = ? AND withdrawn_at_ms IS NOT NULL AND task_id IN (SELECT id FROM tasks WHERE status IN ('queued','running')) ORDER BY created_at_ms,id LIMIT ?`, int64(source.GitHubRepositoryID), source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), int(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IntakeAcceptance{}
	for rows.Next() {
		value, _, err := scanIntakeAcceptance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// IntakeAcceptanceForTask is the immutable source context of an imported task.
func (store *Store) IntakeAcceptanceForTask(ctx context.Context, id TaskID) (IntakeAcceptance, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer tx.Close()
	return scanIntakeAcceptance(tx.connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE task_id = ?`, id.Bytes()))
}
