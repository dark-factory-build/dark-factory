package kernel

import (
	"context"
	"database/sql"
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
