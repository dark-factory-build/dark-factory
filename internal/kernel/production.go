package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxProductionReviewFindings = 16000

var productionRepository = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

type ProductionRecord struct {
	Repository    string          `json:"repository"`
	Kind          string          `json:"kind"`
	ID            string          `json:"id"`
	VisualID      string          `json:"visual_id"`
	ObservedAt    int64           `json:"observed_at"`
	Document      json.RawMessage `json:"document"`
	Tasks         []string        `json:"tasks"`
	Missions      []string        `json:"missions"`
	LinksOverflow bool            `json:"links_overflow"`
}

type ProductionPage struct {
	Records    []ProductionRecord `json:"records"`
	NextOffset int                `json:"next_offset"`
	Total      int                `json:"total"`
}

func productionSHA(value string) bool {
	if value == "" {
		return true
	}
	_, err := hex.DecodeString(value)
	return err == nil && (len(value) == 40 || len(value) == 64) && strings.ToLower(value) == value
}

func productionNumbers(values []uint64) bool {
	if len(values) > 256 {
		return false
	}
	for _, value := range values {
		if value == 0 || value > 1<<53-1 {
			return false
		}
	}
	return true
}

func productionURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func canonicalProductionRuntime(value string) string {
	if !strings.HasPrefix(value, "runtime:/") {
		return value
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(value, "runtime:")))
	return "runtime:host-" + hex.EncodeToString(digest[:8])
}

// Normalize legacy records at the private read boundary as well as on writes.
// Keeping unknown document fields avoids dropping evidence during an upgrade.
func canonicalProductionRecord(item *ProductionRecord) error {
	if (item.Kind != "delivery" && item.Kind != "repository") || !strings.Contains(string(item.Document), "runtime:/") {
		return nil
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(item.Document))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	target := document
	if item.Kind == "repository" {
		// Repository rows written before the maintenance panel was removed still
		// carry that object; normalizing it converges the legacy migration below.
		target, _ = document["maintenance"].(map[string]any)
	}
	old, _ := target["destination"].(string)
	destination := canonicalProductionRuntime(old)
	if old == destination {
		return nil
	}
	target["destination"] = destination
	if item.Kind == "delivery" {
		item.ID = strings.Replace(item.ID, old, destination, 1)
		if id, ok := document["id"].(string); ok {
			document["id"] = strings.Replace(id, old, destination, 1)
		}
	}
	body, err := json.Marshal(document)
	item.Document = body
	return err
}

func migrateProductionRuntimeRecords(ctx context.Context, c *sql.Conn, project ProjectID, repository string) error {
	// ponytail: migrate at most 128 legacy rows per observation; private reads
	// normalize immediately and subsequent controller batches finish the rest.
	rows, err := c.QueryContext(ctx, `SELECT kind, identity, visual_id, document, observed_at_ms FROM production_records
        WHERE project_id = ? AND repository = ? AND kind IN ('delivery', 'repository')
        AND document LIKE '%runtime:/%' LIMIT 128`, project.Bytes(), repository)
	if err != nil {
		return err
	}
	var records []ProductionRecord
	for rows.Next() {
		var item ProductionRecord
		var body string
		if err := rows.Scan(&item.Kind, &item.ID, &item.VisualID, &body, &item.ObservedAt); err != nil {
			rows.Close()
			return err
		}
		item.Document = json.RawMessage(body)
		records = append(records, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range records {
		oldID := item.ID
		if err := canonicalProductionRecord(&item); err != nil {
			return err
		}
		if err := productionRecordOnConnection(ctx, c, project, repository, item.Kind, item.ID, item.VisualID, item.Document, item.ObservedAt); err != nil {
			return err
		}
		if oldID != item.ID {
			if _, err := c.ExecContext(ctx, `DELETE FROM production_records WHERE project_id = ? AND repository = ? AND kind = ? AND identity = ?`, project.Bytes(), repository, item.Kind, oldID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validProductionPull(pr ProductionPullRequest) bool {
	return pr.Number > 0 && pr.Number <= 1<<53-1 && validOutcomeText(pr.Title, 1024) && pr.Title != "" && productionURL(pr.URL) && productionSHA(pr.Head) && productionSHA(pr.Merge) && productionSHA(pr.Review.Head) && (pr.HeadRepository == "" || productionRepository.MatchString(pr.HeadRepository)) && validOutcomeText(pr.Branch, 256) && validOutcomeText(pr.Base, 256) && validOutcomeText(pr.MergeQueue, 64) && validOutcomeText(pr.MergedAt, 64) && validOutcomeText(pr.NextAction, 2048) && validOutcomeText(pr.Review.State, 64) && validOutcomeText(pr.Review.Findings, maxProductionReviewFindings) && productionURL(pr.Review.URL) && (pr.State == "open" || pr.State == "closed" || pr.State == "merged")
}

func productionRecordOnConnection(ctx context.Context, c *sql.Conn, project ProjectID, repo, kind, id, visual string, value any, at int64) error {
	if id == "" || !validOutcomeText(id, 256) {
		return ErrInvalidValue
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > 32768 {
		return ErrInvalidValue
	}
	item := ProductionRecord{Kind: kind, ID: id, Document: body}
	if err := canonicalProductionRecord(&item); err != nil {
		return err
	}
	id, body = item.ID, item.Document
	_, err = c.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(project_id, repository, kind, identity) DO UPDATE SET document = excluded.document, observed_at_ms = excluded.observed_at_ms
        WHERE excluded.observed_at_ms >= production_records.observed_at_ms`, project.Bytes(), repo, kind, id, visual, string(body), at)
	return err
}

type ProductionReviewOperation struct {
	ID       string
	Document any
}

// PendingReviewOperation is a completed REQUEST_CHANGES operation whose task
// feedback has not yet been durably acknowledged by the daemon.
type PendingReviewOperation struct {
	Project    ProjectID
	Repository string
	ID         string
	Document   []byte
}

func validProductionObservation(project ProjectID, observation ProductionObservation, at UnixMillis) bool {
	return !project.zero() && productionRepository.MatchString(observation.Repository) && observation.ObservedAt >= 1 && observation.ObservedAt <= at.Int64()+5000 && observation.Overflow >= 0 && validOutcomeText(observation.Unavailable, 256) && len(observation.PullRequests) <= 256 && len(observation.Checks) <= 256 && len(observation.Reviewers) <= 256 && len(observation.Deliveries) <= 128
}

// RecordProductionObservation accepts facts only from the operator authority.
// An unavailable read updates the source's health without erasing prior work.
func (store *Store) RecordProductionObservation(ctx context.Context, project ProjectID, observation ProductionObservation, at UnixMillis) error {
	if !validProductionObservation(project, observation, at) {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if err := store.recordProductionObservation(ctx, tx.connection, project, observation, at); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// RecordProductionObservationWithReviewOperations atomically stores a changed
// published head and its prepared review claims. A refresh can therefore not
// make a corrected head durable and then lose the review in the launch window.
func (store *Store) RecordProductionObservationWithReviewOperations(ctx context.Context, project ProjectID, observation ProductionObservation, claims []ProductionReviewOperation, at UnixMillis) error {
	if !validProductionObservation(project, observation, at) {
		return ErrInvalidValue
	}
	for _, claim := range claims {
		if !validOutcomeText(claim.ID, 128) {
			return ErrInvalidValue
		}
		body, err := json.Marshal(claim.Document)
		if err != nil || len(body) < 2 || len(body) > 32768 {
			return ErrInvalidValue
		}
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if err := store.recordProductionObservation(ctx, tx.connection, project, observation, at); err != nil {
		return tx.Rollback(err)
	}
	repo := strings.ToLower(observation.Repository)
	for _, claim := range claims {
		if err := productionRecordOnConnection(ctx, tx.connection, project, repo, "reviewer", claim.ID, "", claim.Document, at.Int64()); err != nil {
			return tx.Rollback(err)
		}
	}
	return tx.Commit(ctx)
}

func (store *Store) recordProductionObservation(ctx context.Context, c *sql.Conn, project ProjectID, observation ProductionObservation, at UnixMillis) error {
	observation.Repository = strings.ToLower(observation.Repository)
	if err := migrateProductionRuntimeRecords(ctx, c, project, observation.Repository); err != nil {
		return err
	}
	write := func(kind, id, visual string, value any) error {
		return productionRecordOnConnection(ctx, c, project, observation.Repository, kind, id, visual, value, observation.ObservedAt)
	}
	for _, pr := range observation.PullRequests {
		if !validProductionPull(pr) {
			return ErrInvalidValue
		}
		visual, err := linkProductionChange(ctx, c, project, observation.Repository, pr, observation.ObservedAt)
		if err != nil {
			return err
		}
		// A verdict recorded meanwhile wins only when it covers this exact
		// head. A corrected PR invalidates older review authority.
		if stored, ok := storedProductionReview(ctx, c, project, observation.Repository, pr.Number); ok && strings.EqualFold(stored.Head, pr.Head) {
			pr.Review = stored
		} else if !strings.EqualFold(pr.Review.Head, pr.Head) {
			pr.Review = ProductionReview{Head: pr.Head, State: "unknown"}
		}
		if err := write("pull_request", strconv.FormatUint(pr.Number, 10), visual, pr); err != nil {
			return err
		}
	}
	for _, check := range observation.Checks {
		if !productionSHA(check.Revision) || !productionURL(check.URL) || !validOutcomeText(check.Name, 256) || !validOutcomeText(check.State, 64) || !validOutcomeText(check.Conclusion, 64) || (check.Scope != "head" && check.Scope != "merge_group") || !productionNumbers(check.PullRequests) || len(check.Jobs) > 32 || check.Overflow < 0 {
			return ErrInvalidValue
		}
		for _, job := range check.Jobs {
			if !validOutcomeText(job.ID, 256) || !validOutcomeText(job.Name, 256) || !validOutcomeText(job.State, 64) || !validOutcomeText(job.Conclusion, 64) || !productionURL(job.URL) {
				return ErrInvalidValue
			}
		}
		if err := write("check", check.ID, "", check); err != nil {
			return err
		}
	}
	for _, reviewer := range observation.Reviewers {
		if reviewer.Number == 0 || reviewer.Number > 1<<53-1 || !productionSHA(reviewer.Head) || !validOutcomeText(reviewer.Name, 128) || !validOutcomeText(reviewer.Provider, 64) || !validOutcomeText(reviewer.State, 64) || !validOutcomeText(reviewer.Findings, 8192) || !productionURL(reviewer.URL) {
			return ErrInvalidValue
		}
		if err := write("reviewer", reviewer.ID, "", reviewer); err != nil {
			return err
		}
	}
	for _, delivery := range observation.Deliveries {
		if !productionSHA(delivery.Revision) || !validOutcomeText(delivery.Kind, 64) || !validOutcomeText(delivery.Destination, 256) || !validOutcomeText(delivery.State, 64) || !productionURL(delivery.URL) || !productionNumbers(delivery.PullRequests) || delivery.UpdatedAt < 0 || delivery.UpdatedAt > at.Int64()+5000 || !validOutcomeText(delivery.Phase, 64) || !validOutcomeText(delivery.Reason, 2048) || delivery.Overflow < 0 || delivery.VerifiedAt < 0 || delivery.VerifiedAt > at.Int64()+5000 {
			return ErrInvalidValue
		}
		if err := write("delivery", delivery.ID, "", delivery); err != nil {
			return err
		}
	}
	if err := write("repository", observation.Repository, "", map[string]any{"unavailable": observation.Unavailable, "overflow": observation.Overflow}); err != nil {
		return err
	}
	return nil
}

// A recorded branch and exact settled commit identify a Change. A successful
// orchestrator receipt or verified same-repository branch also links a transformed head.
// Titles and transient worker locations are not identity evidence.
func linkProductionChange(ctx context.Context, c *sql.Conn, project ProjectID, repo string, pr ProductionPullRequest, at int64) (string, error) {
	key := repo + "#" + strconv.FormatUint(pr.Number, 10)
	var existing string
	err := c.QueryRowContext(ctx, `SELECT visual_id FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'pull_request' AND identity = ?`, project.Bytes(), repo, strconv.FormatUint(pr.Number, 10)).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	fallback := key
	if existing != "" && existing != fallback {
		return existing, nil
	}
	var change, task []byte
	if strings.HasPrefix(pr.Branch, "factory/") && len(pr.Branch) == 20 && pr.Head != "" {
		err = c.QueryRowContext(ctx, `SELECT id, task_id FROM changes WHERE project_id = ? AND substr(lower(hex(id)), 1, 12) = ? AND lower(hex(head_commit)) = ? GROUP BY project_id HAVING count(*) = 1`, project.Bytes(), strings.TrimPrefix(pr.Branch, "factory/"), pr.Head).Scan(&change, &task)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if err == sql.ErrNoRows {
			prefix := strings.TrimPrefix(pr.Branch, "factory/")
			err = c.QueryRowContext(ctx, `SELECT c.id, c.task_id FROM changes c
				WHERE c.project_id = ? AND substr(lower(hex(c.id)), 1, 12) = ?
				  AND (SELECT count(*) FROM changes c2
				       WHERE c2.project_id = c.project_id
				         AND substr(lower(hex(c2.id)), 1, 12) = ?) = 1
				  AND (EXISTS (SELECT 1 FROM publication_tasks p
				              JOIN tasks t ON t.id = p.task_id AND t.project_id = p.project_id
				              JOIN agents a ON a.id = t.assigned_agent_id
				                         AND a.project_id = t.project_id
				                         AND a.role = 'orchestrator'
				              WHERE p.project_id = ? AND p.repository = ? AND p.pull_number = ?)
				       OR (? <> '' AND lower(?) = lower(?) AND EXISTS (
				              SELECT 1 FROM task_repository_bindings b
				              JOIN repository_source_identities i ON i.repository_id = b.repository_id
				              WHERE b.task_id = c.task_id
				                AND lower(i.publication_repository) = lower(?))))`,
				project.Bytes(), prefix, prefix, project.Bytes(), repo, pr.Number,
				pr.HeadRepository, pr.HeadRepository, repo, repo).Scan(&change, &task)
			if err != nil && err != sql.ErrNoRows {
				return "", err
			}
		}
		if err == nil {
			if _, err = c.ExecContext(ctx, `INSERT INTO publication_tasks (project_id, repository, pull_number, task_id, change_id, created_at_ms) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, repository, pull_number, task_id) DO UPDATE SET change_id = excluded.change_id WHERE publication_tasks.change_id IS NULL`, project.Bytes(), repo, pr.Number, task, change, at); err != nil {
				return "", err
			}
			candidate := "change:" + hex.EncodeToString(change)
			var used int
			if err = c.QueryRowContext(ctx, `SELECT count(*) FROM production_records WHERE project_id = ? AND kind = 'pull_request' AND visual_id = ?`, project.Bytes(), candidate).Scan(&used); err != nil {
				return "", err
			}
			if used == 0 {
				key = candidate
			}
		}
	}
	if existing != "" && key != existing {
		if _, err := c.ExecContext(ctx, `UPDATE production_records SET visual_id = ? WHERE project_id = ? AND repository = ? AND kind = 'pull_request' AND identity = ? AND visual_id = ?`, key, project.Bytes(), repo, strconv.FormatUint(pr.Number, 10), existing); err != nil {
			return "", err
		}
	}
	return key, nil
}

// RecordPublication captures normal overseer publication without asking the
// operator to copy IDs. A replay attaches provenance but never rewinds a live PR.
func (store *Store) RecordPublication(ctx context.Context, project ProjectID, task TaskID, repo string, pr ProductionPullRequest, at UnixMillis) error {
	if !productionRepository.MatchString(repo) || !validProductionPull(pr) {
		return ErrInvalidValue
	}
	repo = strings.ToLower(repo)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	owner, found, err := taskByID(ctx, tx.connection, task)
	if err != nil {
		return tx.Rollback(err)
	}
	if !found || owner.ProjectID != project {
		return tx.Rollback(ErrUnauthorized)
	}
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO publication_tasks (project_id, repository, pull_number, task_id, created_at_ms) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, project.Bytes(), repo, pr.Number, task.Bytes(), at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	visual, err := linkProductionChange(ctx, tx.connection, project, repo, pr, at.Int64())
	if err != nil {
		return tx.Rollback(err)
	}
	body, err := json.Marshal(pr)
	if err != nil {
		return tx.Rollback(err)
	}
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, ?, 'pull_request', ?, ?, ?, ?) ON CONFLICT DO NOTHING`, project.Bytes(), repo, strconv.FormatUint(pr.Number, 10), visual, string(body), at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// RecordPublicationWithReviewOperation claims the independent review in the
// same transaction as publication. This closes the shutdown window between
// the publication record and review operation creation.
func (store *Store) RecordPublicationWithReviewOperation(ctx context.Context, project ProjectID, task TaskID, repo string, pr ProductionPullRequest, operationID string, operation any, at UnixMillis) error {
	if !productionRepository.MatchString(repo) || !validProductionPull(pr) || !validOutcomeText(operationID, 128) {
		return ErrInvalidValue
	}
	body, err := json.Marshal(operation)
	if err != nil || len(body) < 2 || len(body) > 65536 {
		return ErrInvalidValue
	}
	repo = strings.ToLower(repo)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	owner, found, err := taskByID(ctx, tx.connection, task)
	if err != nil {
		return tx.Rollback(err)
	}
	if !found || owner.ProjectID != project {
		return tx.Rollback(ErrUnauthorized)
	}
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO publication_tasks (project_id, repository, pull_number, task_id, created_at_ms) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, project.Bytes(), repo, pr.Number, task.Bytes(), at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	visual, err := linkProductionChange(ctx, tx.connection, project, repo, pr, at.Int64())
	if err != nil {
		return tx.Rollback(err)
	}
	publication, err := json.Marshal(pr)
	if err != nil {
		return tx.Rollback(err)
	}
	if _, err = tx.connection.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, ?, 'pull_request', ?, ?, ?, ?) ON CONFLICT DO NOTHING`, project.Bytes(), repo, strconv.FormatUint(pr.Number, 10), visual, string(publication), at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	if err := productionRecordOnConnection(ctx, tx.connection, project, repo, "reviewer", operationID, "", operation, at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// RecordProductionReview preserves the exact commit covered by a review.
// Refreshes may move the live PR to a newer head; that older head is evidence,
// not permission to rewrite the review onto the new source.
func storedProductionReview(ctx context.Context, c *sql.Conn, project ProjectID, repo string, number uint64) (ProductionReview, bool) {
	var body string
	if c.QueryRowContext(ctx, `SELECT document FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'pull_request' AND identity = ?`, project.Bytes(), repo, strconv.FormatUint(number, 10)).Scan(&body) != nil {
		return ProductionReview{}, false
	}
	var pr ProductionPullRequest
	if json.Unmarshal([]byte(body), &pr) != nil || pr.Review.Head == "" {
		return ProductionReview{}, false
	}
	return pr.Review, true
}

func (store *Store) RecordProductionReview(ctx context.Context, project ProjectID, repo string, number uint64, review ProductionReview, at UnixMillis) error {
	if project.zero() || !productionRepository.MatchString(repo) || number == 0 || number > 1<<53-1 || !productionSHA(review.Head) || !validOutcomeText(review.State, 64) || !validOutcomeText(review.Findings, maxProductionReviewFindings) || !productionURL(review.URL) {
		return ErrInvalidValue
	}
	repo = strings.ToLower(repo)
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	var visual, body string
	identity := strconv.FormatUint(number, 10)
	err = tx.connection.QueryRowContext(ctx, `SELECT visual_id, document FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'pull_request' AND identity = ?`, project.Bytes(), repo, identity).Scan(&visual, &body)
	if err == sql.ErrNoRows {
		pr := ProductionPullRequest{Number: number, Title: "Pull request #" + identity, Head: review.Head, State: "open", Review: review}
		if err := productionRecordOnConnection(ctx, tx.connection, project, repo, "pull_request", identity, repo+"#"+identity, pr, at.Int64()); err != nil {
			return tx.Rollback(err)
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return tx.Rollback(err)
	}
	var pr ProductionPullRequest
	if json.Unmarshal([]byte(body), &pr) != nil || pr.Number != number || !validProductionPull(pr) {
		return tx.Rollback(ErrCorruptState)
	}
	pr.Review = review
	if err := productionRecordOnConnection(ctx, tx.connection, project, repo, "pull_request", identity, visual, pr, at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// RecordReviewOperation keeps review lifecycle state in the same durable
// production projection as the published pull request. The caller writes the
// initial running record before launching an untrusted provider.
func (store *Store) RecordReviewOperation(ctx context.Context, project ProjectID, repo, operationID string, document any, at UnixMillis) error {
	if project.zero() || !productionRepository.MatchString(repo) || !validOutcomeText(operationID, 128) {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	body, err := json.Marshal(document)
	if err != nil {
		return tx.Rollback(err)
	}
	if len(body) < 2 || len(body) > 65536 {
		return tx.Rollback(ErrInvalidValue)
	}
	if err := productionRecordOnConnection(ctx, tx.connection, project, strings.ToLower(repo), "reviewer", operationID, "", document, at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// ReviewOperation returns the last durable state for a daemon-owned review.
func (store *Store) ReviewOperation(ctx context.Context, project ProjectID, operationID string) ([]byte, bool, error) {
	if project.zero() || !validOutcomeText(operationID, 128) {
		return nil, false, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Close()
	var document string
	err = tx.connection.QueryRowContext(ctx, `SELECT document FROM production_records WHERE project_id = ? AND kind = 'reviewer' AND identity = ?`, project.Bytes(), operationID).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(document), true, nil
}

// PendingReviewOperations returns completed REQUEST_CHANGES operations that
// still need their exactly-once task routing. The operation document remains
// the source of the exact pull-request head and idempotency marker.
func (store *Store) PendingReviewOperations(ctx context.Context) ([]PendingReviewOperation, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT project_id, repository, identity, document FROM production_records
        WHERE kind = 'reviewer' AND json_extract(document, '$.state') = 'completed'
          AND json_extract(document, '$.verdict') = 'request_changes'
          AND json_extract(document, '$.route_pending') = 1
          AND json_type(document, '$.request') = 'object'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []PendingReviewOperation
	for rows.Next() {
		var projectBytes []byte
		var repository, operationID, document string
		if err := rows.Scan(&projectBytes, &repository, &operationID, &document); err != nil {
			return nil, err
		}
		project, err := ProjectIDFromBytes(projectBytes)
		if err != nil {
			return nil, err
		}
		if !json.Valid([]byte(document)) {
			return nil, fmt.Errorf("%w: review operation", ErrCorruptState)
		}
		pending = append(pending, PendingReviewOperation{Project: project, Repository: repository, ID: operationID, Document: []byte(document)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pending, nil
}

// RecoverRunningReviewOperations reconciles review claims left by a stopped
// daemon. A REQUEST_CHANGES verdict without an enqueue receipt remains a
// completed, route-pending operation; other interrupted claims become failures.
// An external reviewer observation uses the same projection kind but does not
// have the durable operation request object.
func (store *Store) RecoverRunningReviewOperations(ctx context.Context, at UnixMillis) (int, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT project_id, repository, identity, document FROM production_records
        WHERE kind = 'reviewer' AND json_extract(document, '$.state') = 'running'
          AND json_type(document, '$.request') = 'object'`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		project    ProjectID
		repository string
		identity   string
		document   map[string]json.RawMessage
	}
	var candidates []candidate
	for rows.Next() {
		var projectBytes []byte
		var repository, identity, document string
		if err := rows.Scan(&projectBytes, &repository, &identity, &document); err != nil {
			rows.Close()
			return 0, tx.Rollback(err)
		}
		project, err := ProjectIDFromBytes(projectBytes)
		if err != nil {
			rows.Close()
			return 0, tx.Rollback(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(document), &fields); err != nil {
			rows.Close()
			return 0, tx.Rollback(fmt.Errorf("%w: review operation", ErrCorruptState))
		}
		var operationID string
		if err := json.Unmarshal(fields["id"], &operationID); err != nil || operationID == "" {
			continue
		}
		var verdict, enqueueID string
		_ = json.Unmarshal(fields["verdict"], &verdict)
		_ = json.Unmarshal(fields["enqueue_id"], &enqueueID)
		retryable, _ := json.Marshal(verdict == "" && enqueueID == "")
		fields["retryable"] = retryable
		candidates = append(candidates, candidate{project: project, repository: repository, identity: identity, document: fields})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, tx.Rollback(err)
	}
	if err := rows.Close(); err != nil {
		return 0, tx.Rollback(err)
	}
	updatedAt, err := json.Marshal(time.UnixMilli(at.Int64()).UTC())
	if err != nil {
		return 0, tx.Rollback(err)
	}
	detail, err := json.Marshal("review interrupted before resume; retry the recorded operation")
	if err != nil {
		return 0, tx.Rollback(err)
	}
	for _, item := range candidates {
		var verdict, enqueueID string
		_ = json.Unmarshal(item.document["verdict"], &verdict)
		_ = json.Unmarshal(item.document["enqueue_id"], &enqueueID)
		if verdict == "request_changes" && enqueueID == "" {
			// The provider write and completed state may already be durable,
			// while task routing was interrupted immediately afterward.
			// Preserve a recoverable route marker instead of converting this
			// exact result into an unretryable failure.
			item.document["state"] = json.RawMessage(`"completed"`)
			item.document["route_pending"] = json.RawMessage(`true`)
			delete(item.document, "detail")
		} else {
			item.document["state"] = json.RawMessage(`"failed"`)
			if verdict == "" && enqueueID == "" {
				item.document["detail"] = detail
			} else {
				item.document["detail"] = json.RawMessage(`"review interrupted after an external write may have occurred; observe the original operation"`)
			}
		}
		item.document["updated_at"] = updatedAt
		if err := productionRecordOnConnection(ctx, tx.connection, item.project, item.repository, "reviewer", item.identity, "", item.document, at.Int64()); err != nil {
			return 0, tx.Rollback(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(candidates), nil
}

// Current construction comes from Changes even after its worker finishes. The
// projection replaces it only when normal publication records the association.
const productionRows = `SELECT repository, kind, identity, visual_id, document, observed_at_ms FROM production_records WHERE project_id = ?
 UNION ALL SELECT COALESCE(r.name, ''), 'construction', lower(hex(c.id)), 'change:' || lower(hex(c.id)),
		json_object('title', t.title, 'phase', c.phase, 'status', t.status, 'head', lower(hex(c.head_commit)), 'task_id', lower(hex(t.id)), 'blocked_reason', t.blocked_reason,
			'has_changes', CASE WHEN c.head_commit IS NULL OR c.base_commit IS NULL THEN NULL WHEN c.head_commit = c.base_commit THEN json('false') ELSE json('true') END), c.updated_at_ms
 FROM changes c JOIN tasks t ON t.id = c.task_id
 LEFT JOIN task_repository_bindings b ON b.task_id = t.id
 LEFT JOIN project_repositories r ON r.id = b.repository_id
 WHERE c.project_id = ? AND NOT EXISTS (SELECT 1 FROM publication_tasks p WHERE p.change_id = c.id)`

func (store *Store) Production(ctx context.Context, project ProjectID, offset, limit int) (ProductionPage, error) {
	if project.zero() || offset < 0 || limit < 1 || limit > 8 {
		return ProductionPage{}, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ProductionPage{}, err
	}
	defer tx.Close()
	page := ProductionPage{Records: []ProductionRecord{}}
	if err := tx.connection.QueryRowContext(ctx, "SELECT count(*) FROM ("+productionRows+")", project.Bytes(), project.Bytes()).Scan(&page.Total); err != nil {
		return page, err
	}
	// Load delivery evidence before merged PRs so bounded reads can identify completed work.
	rows, err := tx.connection.QueryContext(ctx, "SELECT * FROM ("+productionRows+") ORDER BY CASE\n"+
		" WHEN kind = 'repository' THEN 0\n"+
		" WHEN kind = 'pull_request' AND json_extract(document, '$.state') = 'open' THEN 1\n"+" WHEN kind = 'construction' AND json_extract(document, '$.status') IN ('queued', 'running', 'blocked') THEN 2\n"+" WHEN kind = 'delivery' THEN 3\n"+" WHEN kind IN ('pull_request', 'check', 'reviewer') THEN 4\n"+" WHEN kind = 'construction' THEN 5\n"+" ELSE 6 END, repository, identity LIMIT ? OFFSET ?", project.Bytes(), project.Bytes(), limit, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var item ProductionRecord
		var body string
		if err := rows.Scan(&item.Repository, &item.Kind, &item.ID, &item.VisualID, &body, &item.ObservedAt); err != nil {
			return page, err
		}
		if !json.Valid([]byte(body)) {
			return page, fmt.Errorf("%w: production record", ErrCorruptState)
		}
		if size+len(body) > 48000 {
			break
		}
		size += len(body)
		item.Document = json.RawMessage(body)
		if err := canonicalProductionRecord(&item); err != nil {
			return page, err
		}
		page.Records = append(page.Records, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	for i := range page.Records {
		item := &page.Records[i]
		item.Tasks, item.Missions = []string{}, []string{}
		if item.Kind != "pull_request" && item.Kind != "construction" {
			continue
		}
		links, err := tx.connection.QueryContext(ctx, `SELECT DISTINCT lower(hex(t.id)), COALESCE(lower(hex(b.mission_id)), '') FROM tasks t LEFT JOIN mission_task_bindings b ON b.task_id = t.id WHERE t.project_id = ? AND (t.id IN (SELECT task_id FROM publication_tasks WHERE project_id = ? AND repository = ? AND pull_number = ?) OR t.id IN (SELECT task_id FROM changes WHERE lower(hex(id)) = ? AND ? = 'construction')) LIMIT 33`, project.Bytes(), project.Bytes(), item.Repository, item.ID, item.ID, item.Kind)
		if err != nil {
			return page, err
		}
		for links.Next() {
			var task, mission string
			if err := links.Scan(&task, &mission); err != nil {
				links.Close()
				return page, err
			}
			if len(item.Tasks) == 32 {
				item.LinksOverflow = true
				break
			}
			item.Tasks = append(item.Tasks, task)
			if mission != "" {
				item.Missions = append(item.Missions, mission)
			}
		}
		err = links.Err()
		links.Close()
		if err != nil {
			return page, err
		}
	}
	if offset+len(page.Records) < page.Total {
		page.NextOffset = offset + len(page.Records)
	}
	return page, nil
}
