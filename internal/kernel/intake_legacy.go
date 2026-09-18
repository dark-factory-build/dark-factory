package kernel

import (
	"context"
	"database/sql"
	"math"
)

// LegacyIntakeRecord suppresses automatic acceptance of pre-cutover work. A
// historical task link is evidence only; it never adopts that task as accepted.
type LegacyIntakeRecord struct {
	HasHistory            bool
	HistoricalContentHash *[DigestBytes]byte
	IssueNumber           uint64
	NodeID                string
	ContentHash           [DigestBytes]byte
	TaskID                TaskID
	IncarnationID         IncarnationID
	TaskRevision          Revision
}

type LegacyIntakeReceipt struct {
	RepositoryID                      uint64
	PlanHash, ConfigHash, JournalHash [DigestBytes]byte
}

func (store *Store) LegacyIntakeMigration(ctx context.Context, id IntakeSourceID) (LegacyIntakeReceipt, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return LegacyIntakeReceipt{}, false, err
	}
	defer read.Close()
	return legacyIntakeMigration(ctx, read.connection, id)
}

func legacyIntakeMigration(ctx context.Context, connection *sql.Conn, id IntakeSourceID) (LegacyIntakeReceipt, bool, error) {
	var plan, config, journal []byte
	var repository int64
	err := connection.QueryRowContext(ctx, `SELECT github_repository_id, plan_hash, config_hash, journal_hash FROM intake_legacy_migrations WHERE source_id = ?`, id.Bytes()).Scan(&repository, &plan, &config, &journal)
	if err == sql.ErrNoRows {
		return LegacyIntakeReceipt{}, false, nil
	}
	if err != nil {
		return LegacyIntakeReceipt{}, false, err
	}
	if repository < 1 || len(plan) != DigestBytes || len(config) != DigestBytes || len(journal) != DigestBytes {
		return LegacyIntakeReceipt{}, false, ErrCorruptState
	}
	return LegacyIntakeReceipt{RepositoryID: uint64(repository), PlanHash: [DigestBytes]byte(plan), ConfigHash: [DigestBytes]byte(config), JournalHash: [DigestBytes]byte(journal)}, true, nil
}

// CommitLegacyIntakeMigration atomically creates a paused source, its complete
// baseline, and the replay receipt. The controller owns scheduler cutover.
// ponytail: one atomic cutover is capped at 200 issues; larger histories need
// explicit partitioned migration planning before expanding this bound.
func (store *Store) CommitLegacyIntakeMigration(ctx context.Context, spec NewIntakeSource, receipt LegacyIntakeReceipt, records []LegacyIntakeRecord, at UnixMillis) (IntakeSource, error) {
	// ponytail: one bounded transaction supports up to 200 historical issues;
	// larger histories need a staged import protocol before raising this bound.
	if receipt.RepositoryID != spec.GitHubRepositoryID || len(records) > 200 || receipt.PlanHash == [DigestBytes]byte{} || receipt.ConfigHash == [DigestBytes]byte{} || receipt.JournalHash == [DigestBytes]byte{} {
		return IntakeSource{}, ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return IntakeSource{}, err
	}
	defer tx.Close()
	if prior, found, err := legacyIntakeMigration(ctx, tx.connection, spec.ID); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	} else if found {
		if prior != receipt {
			return IntakeSource{}, tx.Rollback(ErrConflict)
		}
		source, _, err := intakeSourceByID(ctx, tx.connection, spec.ID)
		return source, tx.Rollback(err)
	}
	if _, exists, err := intakeSourceByID(ctx, tx.connection, spec.ID); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	} else if exists {
		return IntakeSource{}, tx.Rollback(ErrConflict)
	}
	// Legacy enqueue omitted a repository, so cutover must preserve the reviewed
	// default. Check under the same write gate as creating the frozen source.
	destination, found, err := defaultRepository(ctx, tx.connection, spec.ProjectID)
	if err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if !found || !destination.Enabled || destination.ID != spec.TargetRepositoryID {
		return IntakeSource{}, tx.Rollback(ErrConflict)
	}
	settled, err := legacyIntakeProjectSettled(ctx, tx.connection, spec.ProjectID)
	if err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if !settled {
		return IntakeSource{}, tx.Rollback(ErrConflict)
	}
	source, err := createIntakeSource(ctx, tx.connection, spec, at)
	if err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_legacy_migrations(source_id, github_repository_id, plan_hash, config_hash, journal_hash, created_at_ms) VALUES (?, ?, ?, ?, ?, ?)`, source.ID.Bytes(), int64(receipt.RepositoryID), receipt.PlanHash[:], receipt.ConfigHash[:], receipt.JournalHash[:], at.Int64()); err != nil {
		return IntakeSource{}, tx.Rollback(err)
	}
	seen := map[uint64]bool{}
	for _, record := range records {
		if record.IssueNumber == 0 || record.IssueNumber > math.MaxInt64 || !validBoundedIntakeText(record.NodeID, 1, maxGitHubNodeIDBytes) || record.ContentHash == [DigestBytes]byte{} || seen[record.IssueNumber] || record.TaskID.zero() != record.IncarnationID.zero() || (record.TaskID.zero() && (record.HistoricalContentHash != nil || record.TaskRevision.Int64() != 0)) || (!record.HasHistory && (record.HistoricalContentHash != nil || !record.TaskID.zero())) {
			return IntakeSource{}, tx.Rollback(ErrInvalidValue)
		}
		seen[record.IssueNumber] = true
		var taskID, incarnation any
		var revision any
		var historicalHash any
		if record.HistoricalContentHash != nil {
			historicalHash = record.HistoricalContentHash[:]
		}
		if !record.TaskID.zero() {
			task, found, err := taskByID(ctx, tx.connection, record.TaskID)
			if err != nil {
				return IntakeSource{}, tx.Rollback(err)
			}
			if !found || task.IncarnationID != record.IncarnationID || task.Revision != record.TaskRevision || task.ProjectID != source.ProjectID || task.AssignedAgentID != source.OverseerAgentID || (task.Status != TaskSucceeded && task.Status != TaskFailed && task.Status != TaskCancelled) {
				return IntakeSource{}, tx.Rollback(ErrConflict)
			}
			bound, found, err := taskRepository(ctx, tx.connection, task.ID)
			if err != nil {
				return IntakeSource{}, tx.Rollback(err)
			}
			if !found || bound.ID != source.TargetRepositoryID {
				return IntakeSource{}, tx.Rollback(ErrConflict)
			}
			var unsettled int
			if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id = ? AND phase <> 'terminal') OR EXISTS(SELECT 1 FROM human_requests h JOIN runs r ON r.id=h.run_id WHERE r.task_id = ? AND h.status = 'stale')`, task.ID.Bytes(), task.ID.Bytes()).Scan(&unsettled); err != nil {
				return IntakeSource{}, tx.Rollback(err)
			}
			if unsettled != 0 {
				return IntakeSource{}, tx.Rollback(ErrConflict)
			}
			taskID, incarnation, revision = task.ID.Bytes(), task.IncarnationID.Bytes(), task.Revision.Int64()
		}
		prior, exists, err := legacyIntakeSuppression(ctx, tx.connection, source, IntakeIssueSnapshot{GitHubRepositoryID: source.GitHubRepositoryID, IssueNumber: record.IssueNumber, NodeID: record.NodeID})
		if err != nil {
			return IntakeSource{}, tx.Rollback(err)
		}
		if exists && prior.HasHistory {
			if !prior.TaskID.zero() && !record.TaskID.zero() && prior.TaskID != record.TaskID {
				return IntakeSource{}, tx.Rollback(ErrConflict)
			}
			if !prior.TaskID.zero() || record.TaskID.zero() {
				continue
			}
		}
		if _, err := tx.connection.ExecContext(ctx, `INSERT INTO intake_legacy_suppressions(github_repository_id, issue_number, issue_node_id, project_id, repository_id, content_hash, migration_source_id, has_history, historical_content_hash, legacy_task_id, legacy_incarnation_id, legacy_task_revision) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(github_repository_id, issue_number, issue_node_id, project_id, repository_id) DO UPDATE SET content_hash=excluded.content_hash, migration_source_id=excluded.migration_source_id, has_history=excluded.has_history, historical_content_hash=excluded.historical_content_hash, legacy_task_id=excluded.legacy_task_id, legacy_incarnation_id=excluded.legacy_incarnation_id, legacy_task_revision=excluded.legacy_task_revision`, int64(source.GitHubRepositoryID), int64(record.IssueNumber), record.NodeID, source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes(), record.ContentHash[:], source.ID.Bytes(), boolInt(record.HasHistory), historicalHash, taskID, incarnation, revision); err != nil {
			return IntakeSource{}, tx.Rollback(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IntakeSource{}, err
	}
	return source, nil
}

func (store *Store) LegacyIntakeSuppression(ctx context.Context, source IntakeSource, snapshot IntakeIssueSnapshot) (LegacyIntakeRecord, bool, error) {
	read, err := store.beginRead(ctx)
	if err != nil {
		return LegacyIntakeRecord{}, false, err
	}
	defer read.Close()
	return legacyIntakeSuppression(ctx, read.connection, source, snapshot)
}

func legacyIntakeSuppression(ctx context.Context, connection *sql.Conn, source IntakeSource, snapshot IntakeIssueSnapshot) (LegacyIntakeRecord, bool, error) {
	var hash, historicalHash, task, incarnation []byte
	var history int
	var revision sql.NullInt64
	err := connection.QueryRowContext(ctx, `SELECT content_hash, has_history, historical_content_hash, legacy_task_id, legacy_incarnation_id, legacy_task_revision FROM intake_legacy_suppressions WHERE github_repository_id = ? AND issue_number = ? AND issue_node_id = ? AND project_id = ? AND repository_id = ?`, int64(snapshot.GitHubRepositoryID), int64(snapshot.IssueNumber), snapshot.NodeID, source.ProjectID.Bytes(), source.TargetRepositoryID.Bytes()).Scan(&hash, &history, &historicalHash, &task, &incarnation, &revision)
	if err == sql.ErrNoRows {
		return LegacyIntakeRecord{}, false, nil
	}
	if err != nil {
		return LegacyIntakeRecord{}, false, err
	}
	if len(hash) != DigestBytes {
		return LegacyIntakeRecord{}, false, ErrCorruptState
	}
	value := LegacyIntakeRecord{IssueNumber: snapshot.IssueNumber, NodeID: snapshot.NodeID, ContentHash: [DigestBytes]byte(hash), HasHistory: history == 1}
	if historicalHash != nil {
		if len(historicalHash) != DigestBytes || !value.HasHistory {
			return LegacyIntakeRecord{}, false, ErrCorruptState
		}
		value.HistoricalContentHash = (*[DigestBytes]byte)(historicalHash)
	}
	if task != nil {
		var e1, e2, e3 error
		value.TaskID, e1 = TaskIDFromBytes(task)
		value.IncarnationID, e2 = IncarnationIDFromBytes(incarnation)
		value.TaskRevision, e3 = NewRevision(revision.Int64)
		if e1 != nil || e2 != nil || e3 != nil || !revision.Valid {
			return LegacyIntakeRecord{}, false, ErrCorruptState
		}
	}
	return value, true, nil
}

func validateLegacyIntake(ctx context.Context, connection *sql.Conn) error {
	var invalid int
	err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intake_legacy_suppressions s JOIN project_repositories r ON r.id=s.repository_id LEFT JOIN tasks t ON t.id=s.legacy_task_id LEFT JOIN task_repository_bindings b ON b.task_id=t.id WHERE r.project_id <> s.project_id OR (s.historical_content_hash IS NOT NULL AND s.legacy_task_id IS NULL) OR (s.legacy_task_id IS NOT NULL AND (t.id IS NULL OR t.project_id <> s.project_id OR t.incarnation_id <> s.legacy_incarnation_id OR b.task_id IS NULL OR b.repository_id <> s.repository_id)))`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return ErrCorruptState
	}
	return nil
}

// LegacyIntakeProjectSettled is conservative because legacy worker descendants
// have no durable intake lineage. Existing task controls must settle the project.
func (store *Store) LegacyIntakeProjectSettled(ctx context.Context, project ProjectID) (bool, error) {
	if project.zero() {
		return false, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return false, err
	}
	defer read.Close()
	return legacyIntakeProjectSettled(ctx, read.connection, project)
}
func legacyIntakeProjectSettled(ctx context.Context, connection *sql.Conn, project ProjectID) (bool, error) {
	var active int
	err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=? AND status IN ('queued','running','blocked')) OR EXISTS(SELECT 1 FROM runs WHERE project_id=? AND phase<>'terminal')`, project.Bytes(), project.Bytes()).Scan(&active)
	return active == 0, err
}

// ImportedIntakeIssue finds retained work on one canonical source and frozen
// destination. Source configuration edits do not change an accepted task's route.
func (store *Store) ImportedIntakeIssue(ctx context.Context, repositoryID, number uint64, nodeID string, projectID ProjectID, targetID RepositoryID) (IntakeAcceptance, bool, error) {
	if repositoryID == 0 || repositoryID > math.MaxInt64 || number == 0 || number > math.MaxInt64 || nodeID == "" || projectID.zero() || targetID.zero() {
		return IntakeAcceptance{}, false, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return IntakeAcceptance{}, false, err
	}
	defer read.Close()
	return scanIntakeAcceptance(read.connection.QueryRowContext(ctx, `SELECT `+intakeAcceptanceColumns+` FROM intake_acceptances
		WHERE github_repository_id = ? AND issue_number = ? AND issue_node_id = ? AND project_id = ? AND repository_id = ?
		AND EXISTS (SELECT 1 FROM intake_task_bindings binding JOIN tasks task ON task.id = binding.task_id WHERE binding.acceptance_id = intake_acceptances.id AND binding.task_id = intake_acceptances.task_id)
		ORDER BY `+intakeReviewTime("intake_acceptances")+` DESC, id DESC LIMIT 1`, int64(repositoryID), int64(number), nodeID, projectID.Bytes(), targetID.Bytes()))
}
