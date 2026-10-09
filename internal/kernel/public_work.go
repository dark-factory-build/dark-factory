package kernel

import (
	"context"
	"encoding/json"
)

// PublicProduction reads what the public world projects from production: each
// repository's health record, every pull request open or observed since
// since, the checks naming an open one and the deliveries naming any of them.
// Review findings and check jobs are left behind; nothing public reads them.
func (store *Store) PublicProduction(ctx context.Context, project ProjectID, since int64) ([]ProductionRecord, error) {
	if project.zero() {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `WITH pulls AS (SELECT repository, identity, json_extract(document, '$.state') = 'open' AS open FROM production_records
		WHERE project_id = ?1 AND kind = 'pull_request' AND (json_extract(document, '$.state') = 'open' OR observed_at_ms >= ?2))
	SELECT r.repository, r.kind, r.identity, json_remove(r.document, '$.review.findings', '$.jobs'), r.observed_at_ms FROM production_records r WHERE r.project_id = ?1 AND (r.kind = 'repository'
		OR (r.kind = 'pull_request' AND EXISTS (SELECT 1 FROM pulls p WHERE p.repository = r.repository AND p.identity = r.identity))
		OR (r.kind IN ('check', 'delivery') AND EXISTS (SELECT 1 FROM pulls p, json_each(r.document, '$.pull_requests') j
			WHERE p.repository = r.repository AND p.identity = CAST(j.value AS TEXT) AND (p.open OR r.kind = 'delivery'))))`, project.Bytes(), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ProductionRecord{}
	for rows.Next() {
		var item ProductionRecord
		var body string
		if err := rows.Scan(&item.Repository, &item.Kind, &item.ID, &body, &item.ObservedAt); err != nil {
			return nil, err
		}
		item.Document = json.RawMessage(body)
		records = append(records, item)
	}
	return records, rows.Err()
}

// IntakeIssue is a GitHub issue the factory accepted and has not finished.
type IntakeIssue struct {
	Repository string
	Number     uint64
	Title      string
}

// OpenIntakeIssues lists the project's accepted GitHub issues whose task is
// not finished, newest first.
func (store *Store) OpenIntakeIssues(ctx context.Context, project ProjectID) ([]IntakeIssue, error) {
	if project.zero() {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT a.source_repository, a.issue_number, a.title FROM intake_acceptances a JOIN tasks t ON t.id = a.task_id
		WHERE a.project_id = ? AND a.linear_team_id = '' AND a.withdrawn_at_ms IS NULL AND t.status IN ('queued', 'running', 'blocked')
		ORDER BY a.created_at_ms DESC, a.issue_number DESC`, project.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues := []IntakeIssue{}
	for rows.Next() {
		var issue IntakeIssue
		if err := rows.Scan(&issue.Repository, &issue.Number, &issue.Title); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
