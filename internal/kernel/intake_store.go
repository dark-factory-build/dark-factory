package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"maps"
	"math"
	"strings"
)

const intakeSourceColumns = `id, github_repository_id, github_repository_name, project_id, target_repository_id, overseer_agent_id, label_filter, enabled, policy, poll_seconds, admission_limit, revision, created_at_ms, updated_at_ms, linear_team_id`
const intakeAcceptanceColumns = `source_repository, id, github_repository_id, issue_number, issue_node_id, title, body, body_hash, project_id, repository_id, overseer_agent_id, task_id, incarnation_id, withdrawn_at_ms, created_at_ms, linear_team_id, source_url`

func scanIntakeSource(scanner rowScanner) (IntakeSource, bool, error) {
	var rawID, rawProject, rawRepository, rawAgent []byte
	var repositoryID, poll, limit, revision, created, updated int64
	var name, label, policy, linear string
	var enabled int
	if err := scanner.Scan(&rawID, &repositoryID, &name, &rawProject, &rawRepository, &rawAgent, &label, &enabled, &policy, &poll, &limit, &revision, &created, &updated, &linear); err != nil {
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
	value := IntakeSource{LinearTeamID: linear, ID: id, GitHubRepositoryID: uint64(repositoryID), GitHubRepositoryName: name, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, LabelFilter: label, Enabled: enabled == 1, Policy: IntakePolicy(policy), PollSeconds: uint32(poll), AdmissionLimit: uint16(limit), Revision: rev, CreatedAt: at, UpdatedAt: changed}
	// The complete source is validated after its trusted-login child rows load.
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || !validIntakeOrigin(uint64(repositoryID), linear) || poll < 5 || poll > 86400 || limit < 1 || limit > 200 || enabled < 0 || enabled > 1 {
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
	if err := loadIntakePriorities(ctx, connection, &value); err != nil {
		return IntakeSource{}, false, err
	}
	if !ValidIntakeSource(value) {
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
	value := IntakeSource{LinearTeamID: spec.LinearTeamID, PriorityDefault: spec.PriorityDefault, PriorityByLabel: spec.PriorityByLabel, ID: id, GitHubRepositoryID: spec.GitHubRepositoryID, GitHubRepositoryName: spec.GitHubRepositoryName, ProjectID: spec.ProjectID, TargetRepositoryID: spec.TargetRepositoryID, OverseerAgentID: spec.OverseerAgentID, LabelFilter: spec.LabelFilter, Enabled: enabled, Policy: spec.Policy, TrustedGitHubLogins: append([]string(nil), spec.TrustedGitHubLogins...), PollSeconds: spec.PollSeconds, AdmissionLimit: spec.AdmissionLimit, Revision: existing.Revision, CreatedAt: existing.CreatedAt, UpdatedAt: at}
	if !ValidIntakeSource(value) {
		return IntakeSource{}, tx.Rollback(ErrInvalidValue)
	}
	if err := validateIntakeSourceRoute(ctx, tx.connection, value); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `UPDATE intake_sources SET linear_team_id = ?, github_repository_id = ?, github_repository_name = ?, project_id = ?, target_repository_id = ?, overseer_agent_id = ?, label_filter = ?, enabled = ?, policy = ?, poll_seconds = ?, admission_limit = ?, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, value.LinearTeamID, int64(value.GitHubRepositoryID), value.GitHubRepositoryName, value.ProjectID.Bytes(), value.TargetRepositoryID.Bytes(), nullableAgentID(value.OverseerAgentID), value.LabelFilter, boolInt(value.Enabled), string(value.Policy), int64(value.PollSeconds), int64(value.AdmissionLimit), at.Int64(), id.Bytes(), expected.Int64()); err != nil {
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
	if err := writeIntakePriorities(ctx, tx.connection, value); err != nil {
		return IntakeSource{}, tx.Rollback(err)
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
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeSource{}, err
	}
	defer tx.Close()
	value, err := createIntakeSource(ctx, tx.connection, spec, at)
	if err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeSource{}, err
	}
	return value, nil
}

func createIntakeSource(ctx context.Context, connection *sql.Conn, spec NewIntakeSource, at UnixMillis) (IntakeSource, error) {
	policy := spec.Policy
	if policy == "" {
		policy = IntakePolicyManual
	}
	value := IntakeSource{LinearTeamID: spec.LinearTeamID, PriorityDefault: spec.PriorityDefault, PriorityByLabel: spec.PriorityByLabel, ID: spec.ID, GitHubRepositoryID: spec.GitHubRepositoryID, GitHubRepositoryName: spec.GitHubRepositoryName, ProjectID: spec.ProjectID, TargetRepositoryID: spec.TargetRepositoryID, OverseerAgentID: spec.OverseerAgentID, LabelFilter: spec.LabelFilter, Policy: policy, TrustedGitHubLogins: append([]string(nil), spec.TrustedGitHubLogins...), PollSeconds: spec.PollSeconds, AdmissionLimit: spec.AdmissionLimit, Revision: Revision{value: 1}, CreatedAt: at, UpdatedAt: at}
	if !ValidIntakeSource(value) {
		return IntakeSource{}, ErrInvalidValue
	}
	existing, found, err := intakeSourceByID(ctx, connection, value.ID)
	if err != nil {
		return IntakeSource{}, err
	}
	if found {
		if intakeSourceMatchesCreation(existing, value) {
			return existing, nil
		}
		return IntakeSource{}, ErrConflict
	}
	var sourceCount int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM intake_sources`).Scan(&sourceCount); err != nil {
		return IntakeSource{}, err
	}
	if sourceCount >= globalMaxIntakeSources {
		return IntakeSource{}, ErrConflict
	}
	if err := validateIntakeSourceRoute(ctx, connection, value); err != nil {
		return IntakeSource{}, err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO intake_sources(`+intakeSourceColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, value.ID.Bytes(), int64(value.GitHubRepositoryID), value.GitHubRepositoryName, value.ProjectID.Bytes(), value.TargetRepositoryID.Bytes(), nullableAgentID(value.OverseerAgentID), value.LabelFilter, boolInt(value.Policy == IntakePolicyManual), string(value.Policy), int64(value.PollSeconds), int64(value.AdmissionLimit), at.Int64(), at.Int64(), value.LinearTeamID); err != nil {
		return IntakeSource{}, err
	}
	for _, login := range value.TrustedGitHubLogins {
		if _, err := connection.ExecContext(ctx, `INSERT INTO intake_source_trusted_logins(source_id, login) VALUES(?, ?)`, value.ID.Bytes(), strings.ToLower(login)); err != nil {
			return IntakeSource{}, err
		}
	}
	if err := writeIntakePriorities(ctx, connection, value); err != nil {
		return IntakeSource{}, err
	}
	result, _, err := intakeSourceByID(ctx, connection, value.ID)
	return result, err
}

func intakeSourceMatchesCreation(existing, value IntakeSource) bool {
	if existing.LinearTeamID != value.LinearTeamID || existing.PriorityDefault != value.PriorityDefault || !maps.Equal(existing.PriorityByLabel, value.PriorityByLabel) || existing.GitHubRepositoryID != value.GitHubRepositoryID || existing.GitHubRepositoryName != value.GitHubRepositoryName || existing.ProjectID != value.ProjectID || existing.TargetRepositoryID != value.TargetRepositoryID || existing.OverseerAgentID != value.OverseerAgentID || existing.LabelFilter != value.LabelFilter || existing.Enabled != (value.Policy == IntakePolicyManual) || existing.Policy != value.Policy || existing.PollSeconds != value.PollSeconds || existing.AdmissionLimit != value.AdmissionLimit || existing.Revision.Int64() != 1 || existing.UpdatedAt != existing.CreatedAt || len(existing.TrustedGitHubLogins) != len(value.TrustedGitHubLogins) {
		return false
	}
	seen := make(map[string]bool, len(existing.TrustedGitHubLogins))
	for _, login := range existing.TrustedGitHubLogins {
		seen[strings.ToLower(login)] = true
	}
	for _, login := range value.TrustedGitHubLogins {
		if !seen[strings.ToLower(login)] {
			return false
		}
	}
	return true
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
	var sourceRepository, node, title, body, linear, sourceURL string
	if err := scanner.Scan(&sourceRepository, &rawID, &repositoryID, &number, &node, &title, &body, &hash, &rawProject, &rawRepository, &rawAgent, &rawTask, &rawIncarnation, &withdrawn, &created, &linear, &sourceURL); err != nil {
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
	snapshot := IntakeIssueSnapshot{LinearTeamID: linear, URL: sourceURL, GitHubRepositoryID: uint64(repositoryID), IssueNumber: uint64(number), NodeID: node, Title: title, Body: body}
	value := IntakeAcceptance{SourceRepository: sourceRepository, ID: id, Snapshot: snapshot, BodyHash: sha256.Sum256([]byte(body)), ProjectID: project, RepositoryID: repository, OverseerAgentID: agent, TaskID: task, IncarnationID: incarnation, WithdrawnAt: withdrawnAt, CreatedAt: at}
	expectedID, expectedTask, expectedIncarnation, identityErr := intakeAcceptanceIDs(snapshot, project, repository)
	if !validSourceName(sourceRepository, linear) || e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || identityErr != nil || !validIntakeOrigin(uint64(repositoryID), linear) || number < 1 || len(hash) != DigestBytes || value.BodyHash != [DigestBytes]byte(hash) || id != expectedID || task != expectedTask || incarnation != expectedIncarnation || withdrawn.Valid && (withdrawn.Int64 < 0 || withdrawn.Int64 < created) {
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

func (store *Store) LatestIntakeAcceptance(ctx context.Context, snapshot IntakeIssueSnapshot, projectID ProjectID, targetRepositoryID RepositoryID) (IntakeAcceptance, bool, error) {
	if !validIntakeOrigin(snapshot.GitHubRepositoryID, snapshot.LinearTeamID) || snapshot.IssueNumber == 0 || snapshot.IssueNumber > math.MaxInt64 || !validBoundedIntakeText(snapshot.NodeID, 1, maxGitHubNodeIDBytes) || projectID.zero() || targetRepositoryID.zero() {
		return IntakeAcceptance{}, false, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer read.Close()
	return latestIntakeAcceptance(ctx, read.connection, snapshot, projectID, targetRepositoryID)
}

// Review events promote previously accepted content without changing its receipt
// or task identity. Historical content cannot become duplicate executable work.
func intakeReviewTime(alias string) string {
	return "COALESCE((SELECT MAX(reviewed_at_ms) FROM intake_acceptance_reviews WHERE acceptance_id = " + alias + ".id), " + alias + ".created_at_ms)"
}

func latestIntakeAcceptance(ctx context.Context, connection *sql.Conn, snapshot IntakeIssueSnapshot, projectID ProjectID, targetRepositoryID RepositoryID) (IntakeAcceptance, bool, error) {
	return scanIntakeAcceptance(connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE github_repository_id = ? AND linear_team_id = ? AND issue_number = ? AND issue_node_id = ? AND project_id = ? AND repository_id = ? ORDER BY `+intakeReviewTime("intake_acceptances")+` DESC, id DESC LIMIT 1`, int64(snapshot.GitHubRepositoryID), snapshot.LinearTeamID, int64(snapshot.IssueNumber), snapshot.NodeID, projectID.Bytes(), targetRepositoryID.Bytes()))
}

// PendingIntakeAcceptances returns receipts that still need their first task
// insert. A newer receipt for the same issue suppresses an older one, even if
// the newer receipt was withdrawn: importing an old review after later review
// activity would evade the controller's exact-content check.
func (store *Store) PendingIntakeAcceptances(ctx context.Context, sourceID IntakeSourceID, limit uint16) ([]IntakeAcceptance, error) {
	return store.PendingIntakeAcceptancesAfter(ctx, sourceID, limit, IntakeAcceptanceID{})
}

// PendingIntakeAcceptancesAfter scans a bounded receipt-ID page. A zero cursor
// starts a new sweep; callers retain the last inspected receipt and wrap after
// exhaustion so stale content cannot starve later reviewed work.
func (store *Store) PendingIntakeAcceptancesAfter(ctx context.Context, sourceID IntakeSourceID, limit uint16, after IntakeAcceptanceID) ([]IntakeAcceptance, error) {
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
		WHERE accepted.github_repository_id = ? AND accepted.linear_team_id = ? AND accepted.project_id = ? AND accepted.repository_id = ?
			AND accepted.withdrawn_at_ms IS NULL AND task.id IS NULL AND accepted.id > ?
			AND NOT EXISTS (
				SELECT 1 FROM intake_acceptances newer
				WHERE newer.github_repository_id = accepted.github_repository_id AND newer.linear_team_id = accepted.linear_team_id
					AND newer.issue_number = accepted.issue_number
					AND newer.issue_node_id = accepted.issue_node_id
					AND newer.project_id = accepted.project_id
					AND newer.repository_id = accepted.repository_id
					AND (`+intakeReviewTime("newer")+` > `+intakeReviewTime("accepted")+` OR `+intakeReviewTime("newer")+` = `+intakeReviewTime("accepted")+` AND newer.id > accepted.id)
			)
		ORDER BY accepted.id LIMIT ?`, int64(source.GitHubRepositoryID), source.LinearTeamID, source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), after.Bytes(), int64(limit))
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
	if err != nil || !found || source.LinearTeamID != snapshot.LinearTeamID || source.GitHubRepositoryID != snapshot.GitHubRepositoryID {
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
	latest, latestFound, err := latestIntakeAcceptance(ctx, tx.connection, snapshot, source.ProjectID, source.TargetRepositoryID)
	if err != nil {
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	existing, exists, err := intakeAcceptanceByID(ctx, tx.connection, id)
	if err != nil {
		return IntakeAcceptance{}, tx.Rollback(err)
	}
	// Exact retries remain stable, including withdrawn receipts: explicit
	// acceptance never revives work withdrawn from this receipt.
	if exists && (existing.WithdrawnAt != nil || latestFound && latest.ID == existing.ID) {
		if err := tx.Rollback(nil); err != nil {
			return IntakeAcceptance{}, err
		}
		return existing, nil
	}
	if latestFound {
		var reviewedAt int64
		if err := tx.connection.QueryRowContext(ctx, `SELECT `+intakeReviewTime("intake_acceptances")+` FROM intake_acceptances WHERE id = ?`, latest.ID.Bytes()).Scan(&reviewedAt); err != nil {
			return IntakeAcceptance{}, tx.Rollback(err)
		}
		if at.Int64() <= reviewedAt {
			return IntakeAcceptance{}, tx.Rollback(ErrRevisionConflict)
		}
	}
	if exists {
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_acceptance_reviews(acceptance_id, reviewed_at_ms) VALUES(?, ?)`, existing.ID.Bytes(), at.Int64()); err != nil {
			return IntakeAcceptance{}, tx.Rollback(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return IntakeAcceptance{}, err
		}
		return existing, nil
	}
	bodyHash := snapshot.BodyHash()
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_acceptances(`+intakeAcceptanceColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`, source.GitHubRepositoryName, id.Bytes(), int64(snapshot.GitHubRepositoryID), int64(snapshot.IssueNumber), snapshot.NodeID, snapshot.Title, snapshot.Body, bodyHash[:], source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), nullableAgentID(source.OverseerAgentID), task.Bytes(), incarnation.Bytes(), at.Int64(), snapshot.LinearTeamID, snapshot.URL); err != nil {
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
	return store.importIntakeAcceptance(ctx, id, at, 0, expectedSource...)
}

func (store *Store) ImportIntakeAcceptanceWithPriority(ctx context.Context, id IntakeAcceptanceID, at UnixMillis, source IntakeSource, priority int64) (Task, error) {
	if priority < -1000000 || priority > 1000000 {
		return Task{}, ErrInvalidValue
	}
	return store.importIntakeAcceptance(ctx, id, at, priority, source)
}

func (store *Store) importIntakeAcceptance(ctx context.Context, id IntakeAcceptanceID, at UnixMillis, priority int64, expectedSource ...IntakeSource) (Task, error) {
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
		if !found || !source.Enabled || source.Revision != expectedSource[0].Revision || source.LinearTeamID != accepted.Snapshot.LinearTeamID || source.GitHubRepositoryID != accepted.Snapshot.GitHubRepositoryID || source.ProjectID != accepted.ProjectID || source.TargetRepositoryID != accepted.RepositoryID {
			return Task{}, tx.Rollback(ErrRevisionConflict)
		}
	}
	var permitted int
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intake_sources WHERE github_repository_id = ? AND linear_team_id = ? AND project_id = ? AND target_repository_id = ? AND enabled = 1)`, int64(accepted.Snapshot.GitHubRepositoryID), accepted.Snapshot.LinearTeamID, accepted.ProjectID.Bytes(), accepted.RepositoryID.Bytes()).Scan(&permitted); err != nil || permitted == 0 {
		if err == nil {
			err = ErrConflict
		}
		return Task{}, tx.Rollback(err)
	}
	spec := NewTask{Priority: priority, ID: accepted.TaskID, IncarnationID: accepted.IncarnationID, ProjectID: accepted.ProjectID, RepositoryID: accepted.RepositoryID, AssignedAgentID: accepted.OverseerAgentID, Title: accepted.Snapshot.Title, Body: accepted.Snapshot.Body}
	if err := validateNewTask(spec); err != nil {
		return Task{}, tx.Rollback(err)
	}
	existing, replay, err := intakeTaskReplay(ctx, tx.connection, spec)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if replay {
		binding, found, err := intakeAcceptanceForTask(ctx, tx.connection, existing.ID)
		if err != nil || !found || binding.ID != accepted.ID {
			if err == nil {
				err = ErrConflict
			}
			return Task{}, tx.Rollback(err)
		}
		if err := tx.Rollback(nil); err != nil {
			return Task{}, err
		}
		return existing, nil
	}
	latest, found, err := latestIntakeAcceptance(ctx, tx.connection, accepted.Snapshot, accepted.ProjectID, accepted.RepositoryID)
	if err != nil || !found || latest.ID != accepted.ID {
		if err == nil {
			err = ErrConflict
		}
		return Task{}, tx.Rollback(err)
	}
	value, err := insertTaskOnConnection(ctx, tx.connection, spec, at)
	if err != nil {
		return Task{}, tx.Rollback(err)
	}
	if err := bindIntakeTask(ctx, tx.connection, value.ID, accepted.ID); err != nil {
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
// Reconciliation is global: editing a source cannot orphan retained withdrawals.
func (store *Store) PendingIntakeWithdrawals(ctx context.Context, id IntakeSourceID, limit uint16) ([]IntakeAcceptance, error) {
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	_, found, err := intakeSourceByID(ctx, tx.connection, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE withdrawn_at_ms IS NOT NULL AND id IN (SELECT binding.acceptance_id FROM intake_task_bindings AS binding JOIN tasks AS task ON task.id = binding.task_id WHERE task.status IN ('queued','running')) ORDER BY created_at_ms,id LIMIT ?`, int(limit))
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

// IntakeAcceptanceForTask returns the immutable source of imported or delegated work.
func (store *Store) IntakeAcceptanceForTask(ctx context.Context, id TaskID) (IntakeAcceptance, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer tx.Close()
	return intakeAcceptanceForTask(ctx, tx.connection, id)
}

func intakeAcceptanceForTask(ctx context.Context, connection *sql.Conn, id TaskID) (IntakeAcceptance, bool, error) {
	return scanIntakeAcceptance(connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances WHERE id = (SELECT acceptance_id FROM intake_task_bindings WHERE task_id = ?)`, id.Bytes()))
}

func bindIntakeTask(ctx context.Context, connection *sql.Conn, task TaskID, acceptance IntakeAcceptanceID) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO intake_task_bindings(task_id, acceptance_id) VALUES(?, ?)`, task.Bytes(), acceptance.Bytes())
	return err
}

// IntakeTasksForAcceptance includes the source supervisor and every delegated
// or replacement task, including terminal tasks, for withdrawal reconciliation.
func (store *Store) IntakeTasksForAcceptance(ctx context.Context, id IntakeAcceptanceID) ([]Task, error) {
	if id.zero() {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id IN (SELECT task_id FROM intake_task_bindings WHERE acceptance_id = ?) ORDER BY created_at_ms, id`, id.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		task, _, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}
