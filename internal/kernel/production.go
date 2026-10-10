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

// PublicationAttentionAfter is the quiet period before a successful Change
// without a publication receipt needs another operator/overseer look.
const PublicationAttentionAfter = 3 * time.Minute

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
	return pr.Number > 0 && pr.Number <= 1<<53-1 && validOutcomeText(pr.Title, 1024) && pr.Title != "" && productionURL(pr.URL) && productionSHA(pr.Head) && productionSHA(pr.BaseSHA) && productionSHA(pr.Merge) && productionSHA(pr.Review.Head) && (pr.HeadRepository == "" || productionRepository.MatchString(pr.HeadRepository)) && validOutcomeText(pr.Branch, 256) && validOutcomeText(pr.Base, 256) && validOutcomeText(pr.MergeQueue, 64) && validOutcomeText(pr.MergedAt, 64) && validOutcomeText(pr.NextAction, 2048) && validOutcomeText(pr.Review.State, 64) && validOutcomeText(pr.Review.Findings, maxProductionReviewFindings) && validOutcomeText(pr.Review.OperationID, 128) && productionURL(pr.Review.URL) && (pr.State == "open" || pr.State == "closed" || pr.State == "merged")
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

// PendingReviewOperation is one unfinished daemon-owned review operation.
type PendingReviewOperation struct {
	Project    ProjectID
	Repository string
	ID         string
	Document   []byte
	Ended      bool // its pull request is recorded merged, closed or at a new head
}

// PublishingProjects lists the projects that have published a pull request.
func (store *Store) PublishingProjects(ctx context.Context) ([]ProjectID, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT DISTINCT project_id FROM publication_tasks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []ProjectID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		project, err := ProjectIDFromBytes(raw)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// MaxObservationChecks is the most check records one production observation
// may write.
const MaxObservationChecks = 256

func validProductionObservation(project ProjectID, observation ProductionObservation, at UnixMillis) bool {
	return !project.zero() && productionRepository.MatchString(observation.Repository) && observation.ObservedAt >= 1 && observation.ObservedAt <= at.Int64()+5000 && observation.Overflow >= 0 && validOutcomeText(observation.Unavailable, 256) && len(observation.PullRequests) <= 256 && len(observation.Checks) <= MaxObservationChecks && len(observation.Reviewers) <= 256 && len(observation.Deliveries) <= 128 && (observation.DeployedAt == nil || *observation.DeployedAt >= 0)
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
	// One live review operation per pull request head: an in-flight operation
	// for an older head of an open pull is terminal, never resumed, retried or
	// escalated, since the Maintainer refuses its writes forever.
	for _, pr := range observation.PullRequests {
		if pr.State != "open" {
			continue
		}
		if _, err := c.ExecContext(ctx, `UPDATE production_records SET document = json_set(document, '$.state', 'superseded'), observed_at_ms = ?
			WHERE project_id = ? AND repository = ? AND kind = 'reviewer' AND json_extract(document, '$.request.PullNumber') = ?
			  AND lower(json_extract(document, '$.request.Head')) <> lower(?) AND json_extract(document, '$.state') IN ('running', 'submitting', 'enqueued')`,
			at.Int64(), project.Bytes(), observation.Repository, int64(pr.Number), pr.Head); err != nil {
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
		if !validProductionDelivery(delivery, at) {
			return ErrInvalidValue
		}
		if err := write("delivery", delivery.ID, "", delivery); err != nil {
			return err
		}
	}
	health := map[string]any{"unavailable": observation.Unavailable, "overflow": observation.Overflow}
	// Deployment records are kept once seen, and only move forward: a refresh
	// that could not read them never ships or unships a merged pull request.
	var deployed sql.NullInt64
	if err := c.QueryRowContext(ctx, `SELECT json_extract(document, '$.deployed_at') FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'repository' AND identity = ?`,
		project.Bytes(), observation.Repository, observation.Repository).Scan(&deployed); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if observation.DeployedAt != nil {
		deployed.Int64, deployed.Valid = max(deployed.Int64, *observation.DeployedAt), true
	}
	if deployed.Valid {
		health["deployed_at"] = deployed.Int64
	}
	if err := write("repository", observation.Repository, "", health); err != nil {
		return err
	}
	return nil
}

func validProductionDelivery(delivery ProductionDelivery, at UnixMillis) bool {
	return productionSHA(delivery.Revision) && validOutcomeText(delivery.Kind, 64) && validOutcomeText(delivery.Destination, 256) && validOutcomeText(delivery.State, 64) && productionURL(delivery.URL) && productionNumbers(delivery.PullRequests) && delivery.UpdatedAt >= 0 && delivery.UpdatedAt <= at.Int64()+5000 && validOutcomeText(delivery.Phase, 64) && validOutcomeText(delivery.Reason, 2048) && delivery.Overflow >= 0 && delivery.VerifiedAt >= 0 && delivery.VerifiedAt <= at.Int64()+5000
}

// RecordDelivery writes one delivery row, such as a runtime release.
func (store *Store) RecordDelivery(ctx context.Context, project ProjectID, repository string, delivery ProductionDelivery, at UnixMillis) error {
	repository = strings.ToLower(repository)
	if project.zero() || !productionRepository.MatchString(repository) || !validProductionDelivery(delivery, at) {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	// Releases run one at a time, so the newest release record is this one's
	// predecessor, or itself once it has started.
	if strings.HasPrefix(delivery.ID, "release:") {
		var state string
		err := tx.connection.QueryRowContext(ctx, `SELECT json_extract(document, '$.state'), COALESCE(json_extract(document, '$.cause'), ''), COALESCE(json_extract(document, '$.failed_at'), 0)
			FROM production_records WHERE project_id = ? AND kind = 'delivery' AND identity LIKE 'release:%' ORDER BY observed_at_ms DESC LIMIT 1`, project.Bytes()).Scan(&state, &delivery.Cause, &delivery.FailedAt)
		if errors.Is(err, sql.ErrNoRows) || state == "verified" {
			err, delivery.Cause, delivery.FailedAt = nil, "", 0
		}
		if err != nil {
			return tx.Rollback(err)
		}
		if head, _, _ := strings.Cut(delivery.Reason, ":"); delivery.State == "failed" && delivery.Cause != delivery.Phase+" "+head {
			delivery.Cause, delivery.FailedAt = delivery.Phase+" "+head, at.Int64()
		}
	}
	if err := productionRecordOnConnection(ctx, tx.connection, project, repository, "delivery", delivery.ID, "", delivery, at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// Delivery reads the delivery row id and the project that recorded it.
func (store *Store) Delivery(ctx context.Context, id string) (ProjectID, ProductionDelivery, bool, error) {
	var delivery ProductionDelivery
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ProjectID{}, delivery, false, err
	}
	defer tx.Close()
	var projectBytes []byte
	var document string
	err = tx.connection.QueryRowContext(ctx, `SELECT project_id, document FROM production_records WHERE kind = 'delivery' AND identity = ? ORDER BY observed_at_ms DESC LIMIT 1`, id).Scan(&projectBytes, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectID{}, delivery, false, nil
	}
	project, idErr := ProjectIDFromBytes(projectBytes)
	if err = errors.Join(err, idErr); err == nil && json.Unmarshal([]byte(document), &delivery) != nil {
		err = fmt.Errorf("%w: delivery", ErrCorruptState)
	}
	return project, delivery, err == nil, err
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

// PublishableChange is a succeeded intake worker task's settled Change that
// factoryd publishes itself: as a new pull request, or, when Pull is set, as
// the correction of the open pull request it published for that task.
type PublishableChange struct {
	Task       Task
	Change     ChangeID
	Revision   Revision
	Base, Head string
	Accepted   IntakeAcceptance
	Repository RepositoryID
	Repair     bool
	Branch     string
	Pull       uint64
}

// factorydPublishesAcceptance is the invariant for intake work factoryd
// publishes itself (a is its intake_acceptances row): one issue, one worker
// task, and a GitHub source issue in the destination repository itself. A
// Linear or cross-repository source (whose private title must never reach a
// public commit) and work split over several tasks are the overseer's.
// An imported task its overseer owned (before intake went straight to
// workers) is not a worker task.
const factorydPublishesAcceptance = `(SELECT count(*) FROM intake_task_bindings s JOIN tasks st ON st.id = s.task_id LEFT JOIN agents sa ON sa.id = st.assigned_agent_id
	WHERE s.acceptance_id = a.id AND sa.role IS NOT 'orchestrator') = 1
	AND EXISTS (SELECT 1 FROM repository_source_identities i WHERE i.repository_id = a.repository_id AND i.github_repository_id = a.github_repository_id)`

// PublishRetryAfter is how long a recorded publish failure holds back its
// Change revision; then the next pass retries it, so a refusal fixed outside
// factoryd heals itself.
const PublishRetryAfter = time.Hour

// PublishFailureID names the one reviewer record of factoryd failing to
// publish a Change revision.
func PublishFailureID(change ChangeID, revision Revision) string {
	return fmt.Sprintf("publish-%s-%d", change, revision.Int64())
}

// PublishableChanges lists, oldest first, the current settled Changes of
// succeeded tasks bound to a live intake acceptance factoryd publishes, whose
// head differs from their base, with no publish failure at that Change
// revision recorded within PublishRetryAfter of at: with no publication of
// that Change or task, at any work revision, or above work revision 1
// settled since factoryd last published that Change on its still-open pull
// request (publication_tasks.created_at_ms).
func (store *Store) PublishableChanges(ctx context.Context, at UnixMillis) ([]PublishableChange, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT c.id, c.task_id, c.revision, lower(hex(c.base_commit)), lower(hex(c.head_commit)), COALESCE(p.pull_number, 0), COALESCE((SELECT lower(hex(repository_id)) FROM task_repository_bindings WHERE task_id = c.task_id), ''), COALESCE((SELECT json_extract(r.document, '$.branch') FROM production_records r WHERE r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)), '') FROM changes c
		JOIN tasks t ON t.id = c.task_id AND t.incarnation_id = c.task_incarnation_id
		LEFT JOIN intake_task_bindings b ON b.task_id = c.task_id LEFT JOIN intake_acceptances a ON a.id = b.acceptance_id
		LEFT JOIN publication_tasks p ON p.task_id = c.task_id AND c.updated_at_ms > p.created_at_ms
		  AND EXISTS (SELECT 1 FROM production_records r WHERE r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request'
		      AND r.identity = CAST(p.pull_number AS TEXT) AND json_extract(r.document, '$.state') = 'open')
		WHERE c.phase = 'retained' AND t.status = 'succeeded' AND a.withdrawn_at_ms IS NULL AND c.head_commit <> c.base_commit
		  AND ((a.id IS NOT NULL AND `+factorydPublishesAcceptance+`) OR (a.id IS NULL AND EXISTS (SELECT 1 FROM publication_tasks repair WHERE repair.project_id = c.project_id AND repair.task_id = c.task_id)))
		  AND (p.pull_number IS NOT NULL OR NOT EXISTS (SELECT 1 FROM publication_tasks q WHERE q.change_id = c.id OR q.task_id = c.task_id))
		  AND NOT EXISTS (SELECT 1 FROM production_records r WHERE r.project_id = c.project_id AND r.kind = 'reviewer'
		      AND r.identity = 'publish-' || lower(hex(c.id)) || '-' || c.revision
		      AND r.observed_at_ms <= ?
		      AND (p.pull_number IS NULL OR r.observed_at_ms >= p.created_at_ms)
		      AND (json_extract(r.document, '$.retryable') = 0 OR
		          ((json_extract(r.document, '$.retryable') = 1 OR json_extract(r.document, '$.retryable') IS NULL)
		              AND r.observed_at_ms + ? > ?)))
		ORDER BY c.updated_at_ms`, at.Int64(), PublishRetryAfter.Milliseconds(), at.Int64())
	if err != nil {
		return nil, err
	}
	var found []PublishableChange
	for rows.Next() {
		var change, task []byte
		var revision int64
		var repository string
		var branch string
		var value PublishableChange
		if err := rows.Scan(&change, &task, &revision, &value.Base, &value.Head, &value.Pull, &repository, &branch); err != nil {
			rows.Close()
			return nil, err
		}
		if repository != "" {
			raw, decodeErr := hex.DecodeString(repository)
			if decodeErr != nil {
				rows.Close()
				return nil, decodeErr
			}
			value.Repository, err = RepositoryIDFromBytes(raw)
		}
		value.Branch = branch
		if value.Change, err = ChangeIDFromBytes(change); err == nil {
			if value.Task.ID, err = TaskIDFromBytes(task); err == nil {
				value.Revision, err = NewRevision(revision)
			}
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range found {
		var ok, bound bool
		if found[i].Task, ok, err = taskByID(ctx, tx.connection, found[i].Task.ID); err == nil {
			found[i].Accepted, bound, err = intakeAcceptanceForTask(ctx, tx.connection, found[i].Task.ID)
		}
		if err == nil && !ok {
			err = ErrCorruptState
		}
		if err == nil {
			found[i].Repair = !bound
			if !bound && found[i].Repository.zero() {
				err = ErrCorruptState
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// RecordCorrectionPublished marks the Change's correction as published on
// the pull request factoryd opened for its worker task: neither factoryd nor
// the overseer's wake treats that settlement as unpublished again.
// It moves one timestamp no retained-history rule reads, so it is unchecked.
func (store *Store) RecordCorrectionPublished(ctx context.Context, c PublishableChange, at UnixMillis) error {
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if _, err := tx.connection.ExecContext(ctx, `UPDATE publication_tasks SET change_id = ?, created_at_ms = ? WHERE pull_number = ? AND task_id = ? AND (change_id = ? OR change_id IS NULL)`, c.Change.Bytes(), at.Int64(), c.Pull, c.Task.ID.Bytes(), c.Change.Bytes()); err != nil {
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

// KnownProductionPulls reads the repository's pull requests last seen open,
// and which of them have publication tasks, in one query: a refresh paging the
// UI-ordered Production view cost a sorted UNION per eight rows.
func (store *Store) KnownProductionPulls(ctx context.Context, project ProjectID, repo string, limit int) ([]ProductionPullRequest, map[uint64]bool, error) {
	if project.zero() || limit < 1 {
		return nil, nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT r.document, EXISTS (SELECT 1 FROM publication_tasks p WHERE p.project_id = r.project_id AND p.repository = r.repository AND p.pull_number = CAST(r.identity AS INTEGER))
		FROM production_records r WHERE r.project_id = ? AND r.repository = ? AND r.kind = 'pull_request' AND json_extract(r.document, '$.state') = 'open'
		ORDER BY CAST(r.identity AS INTEGER) LIMIT ?`, project.Bytes(), strings.ToLower(repo), limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	pulls, published := []ProductionPullRequest{}, map[uint64]bool{}
	for rows.Next() {
		var body string
		var tasks bool
		if err := rows.Scan(&body, &tasks); err != nil {
			return nil, nil, err
		}
		var pull ProductionPullRequest
		if json.Unmarshal([]byte(body), &pull) != nil {
			continue
		}
		pulls = append(pulls, pull)
		if tasks {
			published[pull.Number] = true
		}
	}
	return pulls, published, rows.Err()
}

// ProductionHead is one pull request at one head commit.
type ProductionHead struct {
	Number uint64
	Head   string // lower-case
}

// SettledProductionChecks is every pull head whose stored head checks have
// all completed without failing: a refresh need not read them again. A
// failed head stays unsettled, since re-runs mostly follow failures.
func (store *Store) SettledProductionChecks(ctx context.Context, project ProjectID, repo string) (map[ProductionHead]bool, error) {
	if project.zero() {
		return nil, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT CAST(j.value AS INTEGER), lower(json_extract(r.document, '$.revision'))
		FROM production_records r, json_each(r.document, '$.pull_requests') j
		WHERE r.project_id = ? AND r.repository = ? AND r.kind = 'check' AND json_extract(r.document, '$.scope') = 'head'
		GROUP BY 1, 2 HAVING MIN(json_extract(r.document, '$.state') = 'completed'
			AND COALESCE(json_extract(r.document, '$.conclusion'), '') NOT IN ('failure', 'timed_out', 'cancelled', 'action_required', 'startup_failure')) = 1`, project.Bytes(), strings.ToLower(repo))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settled := map[ProductionHead]bool{}
	for rows.Next() {
		var head ProductionHead
		var number int64
		if err := rows.Scan(&number, &head.Head); err != nil {
			return nil, err
		}
		head.Number = uint64(number)
		settled[head] = true
	}
	return settled, rows.Err()
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

// ProductionReviewBlocks reports a block of record at this exact head: no
// plain ALLOW clears it (RecordProductionReview), and neither does the gate.
// TaskOpenPullRequest reports the open pull request task's work is published
// on, the one SendBackPublishedReview would route to, and its observed head:
// a fresh Change for that task starts there, never at the base.
func (store *Store) TaskOpenPullRequest(ctx context.Context, task TaskID) (ProductionPullRequest, bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ProductionPullRequest{}, false, err
	}
	defer tx.Close()
	var document string
	err = tx.connection.QueryRowContext(ctx, `SELECT r.document FROM publication_tasks p JOIN production_records r ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT) WHERE p.task_id = ? AND json_extract(r.document, '$.state') = 'open' ORDER BY p.created_at_ms DESC LIMIT 1`, task.Bytes()).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return ProductionPullRequest{}, false, nil
	}
	var pr ProductionPullRequest
	if err == nil && (json.Unmarshal([]byte(document), &pr) != nil || pr.Number == 0 || !productionSHA(pr.Head)) {
		err = ErrCorruptState
	}
	return pr, err == nil, err
}

func (store *Store) ProductionReviewBlocks(ctx context.Context, project ProjectID, repo string, number uint64, head string) (bool, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Close()
	review, ok := storedProductionReview(ctx, tx.connection, project, strings.ToLower(repo), number)
	return ok && review.State == "block" && strings.EqualFold(review.Head, head), nil
}

func (store *Store) RecordProductionReview(ctx context.Context, project ProjectID, repo string, number uint64, review ProductionReview, at UnixMillis) error {
	if project.zero() || !productionRepository.MatchString(repo) || number == 0 || number > 1<<53-1 || !productionSHA(review.Head) || !validOutcomeText(review.State, 64) || !validOutcomeText(review.Findings, maxProductionReviewFindings) || !validOutcomeText(review.OperationID, 128) || !productionURL(review.URL) {
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
	if review.State == "allow" && pr.Review.Head == review.Head && pr.Review.State == "block" {
		// Nothing clears a block at the same head.
		return tx.Commit(ctx)
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
	// A superseded operation is terminal: its still-running goroutine cannot
	// revive it.
	var state sql.NullString
	if err := tx.connection.QueryRowContext(ctx, `SELECT json_extract(document, '$.state') FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'reviewer' AND identity = ?`,
		project.Bytes(), strings.ToLower(repo), operationID).Scan(&state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return tx.Rollback(err)
	}
	if state.String == "superseded" {
		return tx.Rollback(ErrSuperseded)
	}
	if err := productionRecordOnConnection(ctx, tx.connection, project, strings.ToLower(repo), "reviewer", operationID, "", document, at.Int64()); err != nil {
		return tx.Rollback(err)
	}
	return tx.Commit(ctx)
}

// RecordReviewRetry consumes the original pre-submit retry allowance and
// creates the replacement operation atomically. A concurrent retry therefore
// observes the consumed allowance instead of creating a duplicate reviewer.
func (store *Store) RecordReviewRetry(ctx context.Context, project ProjectID, repo, originalID, retryID string, retry any, at UnixMillis) error {
	if project.zero() || !productionRepository.MatchString(repo) || !validOutcomeText(originalID, 128) || !validOutcomeText(retryID, 128) || originalID == retryID {
		return ErrInvalidValue
	}
	body, err := json.Marshal(retry)
	if err != nil || len(body) < 2 || len(body) > 65536 {
		return ErrInvalidValue
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	result, err := tx.connection.ExecContext(ctx, `UPDATE production_records
		SET document = json_set(document, '$.retryable', json('false'), '$.handled', json('true')), observed_at_ms = ?
		WHERE project_id = ? AND repository = ? AND kind = 'reviewer' AND identity = ?
		  AND observed_at_ms <= ? AND json_extract(document, '$.state') = 'failed'
		  AND json_extract(document, '$.retryable') = 1
		  AND COALESCE(json_extract(document, '$.retry_of'), '') = ''`, at.Int64(), project.Bytes(), strings.ToLower(repo), originalID, at.Int64())
	if err != nil {
		return tx.Rollback(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return tx.Rollback(err)
	} else if affected != 1 {
		return tx.Rollback(ErrConflict)
	}
	if err := productionRecordOnConnection(ctx, tx.connection, project, strings.ToLower(repo), "reviewer", retryID, "", json.RawMessage(body), at.Int64()); err != nil {
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

// InFlightReviewOperations returns every unfinished review operation: reviews
// to relaunch, verdict writes whose external receipts may have been lost,
// allowed heads awaiting merge, results whose task routing has not landed,
// and unhandled failures of a still-open pull request at the same head.
func (store *Store) InFlightReviewOperations(ctx context.Context) ([]PendingReviewOperation, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT project_id, repository, identity, document, (SELECT json_extract(p.document, '$.state') = 'open' AND lower(json_extract(p.document, '$.head')) = lower(json_extract(r.document, '$.request.Head'))
                FROM production_records p WHERE p.project_id = r.project_id AND p.repository = r.repository AND p.kind = 'pull_request' AND p.identity = CAST(json_extract(r.document, '$.request.PullNumber') AS TEXT)) AS live
        FROM production_records r WHERE kind = 'reviewer' AND (json_extract(document, '$.state') IN ('running', 'submitting', 'enqueued') OR json_extract(document, '$.route_pending') = 1
            OR (json_extract(document, '$.state') = 'failed' AND json_extract(document, '$.handled') IS NOT 1 AND live))
          AND json_type(document, '$.request') = 'object'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []PendingReviewOperation
	for rows.Next() {
		var projectBytes []byte
		var repository, operationID, document string
		var live sql.NullBool
		if err := rows.Scan(&projectBytes, &repository, &operationID, &document, &live); err != nil {
			return nil, err
		}
		project, err := ProjectIDFromBytes(projectBytes)
		if err != nil {
			return nil, err
		}
		if !json.Valid([]byte(document)) {
			return nil, fmt.Errorf("%w: review operation", ErrCorruptState)
		}
		pending = append(pending, PendingReviewOperation{Project: project, Repository: repository, ID: operationID, Document: []byte(document), Ended: live.Valid && !live.Bool})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pending, nil
}

// RecoverRunningReviewOperations reconciles review claims left by a stopped
// daemon. A submitted REQUEST_CHANGES verdict remains a completed,
// route-pending operation; a claim with no verdict stays running for startup
// to relaunch; others fail.
// A 'gating' claim from before the pre-review gate was removed is the same.
// An external reviewer observation uses the same projection kind but does not
// have the durable operation request object.
func (store *Store) RecoverRunningReviewOperations(ctx context.Context, at UnixMillis) (int, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT project_id, repository, identity, document FROM production_records
        WHERE kind = 'reviewer' AND json_extract(document, '$.state') IN ('running', 'gating')
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
		var verdict, retryOf, state string
		_ = json.Unmarshal(fields["verdict"], &verdict)
		if _ = json.Unmarshal(fields["state"], &state); state == "running" && verdict == "" {
			continue // startup relaunches it as it stands
		}
		_ = json.Unmarshal(fields["retry_of"], &retryOf)
		retryable, _ := json.Marshal(verdict == "" && retryOf == "")
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
	for _, item := range candidates {
		var verdict string
		var submitted bool
		_ = json.Unmarshal(item.document["verdict"], &verdict)
		_ = json.Unmarshal(item.document["submitted"], &submitted)
		if verdict == "request_changes" && submitted {
			// The provider write and completed state may already be durable,
			// while task routing was interrupted immediately afterward.
			// Preserve a recoverable route marker instead of converting this
			// exact result into an unretryable failure.
			item.document["state"] = json.RawMessage(`"completed"`)
			item.document["route_pending"] = json.RawMessage(`true`)
			delete(item.document, "detail")
		} else if verdict == "" {
			// Nothing external was written: startup relaunches it, so a
			// daemon restart (every release) never fails a review.
			item.document["state"] = json.RawMessage(`"running"`)
			delete(item.document, "detail")
		} else {
			item.document["state"] = json.RawMessage(`"failed"`)
			item.document["detail"] = json.RawMessage(`"review interrupted after an external write may have occurred; observe the original operation"`)
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
		json_object('title', t.title, 'phase', c.phase, 'status', t.status, 'base', lower(hex(c.base_commit)), 'head', lower(hex(c.head_commit)), 'task_id', lower(hex(t.id)), 'blocked_reason', t.blocked_reason,
			'has_changes', CASE WHEN c.head_commit IS NULL OR c.base_commit IS NULL THEN NULL WHEN c.head_commit = c.base_commit THEN json('false') ELSE json('true') END,
			'needs_you', CASE WHEN t.status = 'succeeded' AND c.head_commit IS NOT NULL AND c.base_commit IS NOT NULL AND c.head_commit <> c.base_commit
				AND c.updated_at_ms + ? <= CAST(strftime('%s','now') AS INTEGER) * 1000
				THEN json('true') ELSE json('false') END), c.updated_at_ms
 FROM changes c JOIN tasks t ON t.id = c.task_id
 LEFT JOIN task_repository_bindings b ON b.task_id = t.id
 LEFT JOIN project_repositories r ON r.id = b.repository_id
 WHERE c.project_id = ? AND NOT EXISTS (SELECT 1 FROM publication_tasks p WHERE p.change_id = c.id)`

// ProductionRecent matches the floor's SHIPPED window: work settled longer ago
// is history, not production.
const ProductionRecent = 24 * time.Hour

// Production reads what is relevant now, defined by state rather than a count:
// repository rows; open PRs; queued, running, blocked or needs-you
// constructions; deliveries that are not terminal, including blocked ones no
// later verified delivery of the same kind and destination supersedes;
// anything observed within ProductionRecent before at; the PRs those
// deliveries name; PRs whose tasks belong to a mission not yet accepted; and
// every check, reviewer and delivery of each of those PRs.
const productionRelevant = `WITH recs AS (` + productionRows + `),
 flagged AS (SELECT *, kind = 'repository' OR observed_at_ms >= ?
	OR (kind = 'pull_request' AND json_extract(document, '$.state') = 'open')
	OR (kind = 'construction' AND (json_extract(document, '$.status') IN ('queued', 'running', 'blocked') OR json_extract(document, '$.needs_you')))
	OR (kind = 'delivery' AND COALESCE(json_extract(document, '$.state'), '') NOT IN ('verified', 'failed', 'blocked'))
	OR (kind = 'delivery' AND json_extract(document, '$.state') = 'blocked' AND NOT EXISTS (SELECT 1 FROM recs v WHERE v.kind = 'delivery' AND v.repository = r.repository
		AND json_extract(v.document, '$.kind') IS json_extract(r.document, '$.kind') AND json_extract(v.document, '$.destination') IS json_extract(r.document, '$.destination')
		AND json_extract(v.document, '$.state') = 'verified' AND COALESCE(json_extract(v.document, '$.updated_at'), v.observed_at_ms) > COALESCE(json_extract(r.document, '$.updated_at'), r.observed_at_ms))) AS live FROM recs r),
 missions AS (SELECT o.id FROM project_outcome_revisions o WHERE o.project_id = ? AND json_extract(o.document, '$.kind') = 'mission' AND json_extract(o.document, '$.state') <> 'accepted'
	AND o.revision = (SELECT MAX(revision) FROM project_outcome_revisions WHERE id = o.id)),
 pulls AS (SELECT repository, CAST(identity AS INTEGER) AS number FROM flagged WHERE kind = 'pull_request' AND live
	UNION SELECT f.repository, j.value FROM flagged f, json_each(f.document, '$.pull_requests') j WHERE f.kind = 'delivery' AND f.live
	UNION SELECT t.repository, t.pull_number FROM publication_tasks t JOIN mission_task_bindings b ON b.task_id = t.task_id WHERE b.mission_id IN (SELECT id FROM missions))
 SELECT repository, kind, identity, visual_id, document, observed_at_ms FROM flagged f WHERE live
	OR (kind = 'pull_request' AND EXISTS (SELECT 1 FROM pulls p WHERE p.repository = f.repository AND p.number = CAST(f.identity AS INTEGER)))
	OR (kind IN ('check', 'delivery') AND EXISTS (SELECT 1 FROM pulls p, json_each(f.document, '$.pull_requests') j WHERE p.repository = f.repository AND p.number = j.value))
	OR (kind = 'reviewer' AND EXISTS (SELECT 1 FROM pulls p WHERE p.repository = f.repository AND p.number = COALESCE(json_extract(f.document, '$.number'), json_extract(f.document, '$.request.PullNumber'))))`

func (store *Store) Production(ctx context.Context, project ProjectID, offset, limit int, at UnixMillis) (ProductionPage, error) {
	if project.zero() || offset < 0 || limit < 1 || limit > 8 {
		return ProductionPage{}, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ProductionPage{}, err
	}
	defer tx.Close()
	page := ProductionPage{Records: []ProductionRecord{}}
	args := []any{project.Bytes(), PublicationAttentionAfter.Milliseconds(), project.Bytes(), at.Int64() - ProductionRecent.Milliseconds(), project.Bytes()}
	if err := tx.connection.QueryRowContext(ctx, "SELECT count(*) FROM ("+productionRelevant+")", args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.connection.QueryContext(ctx, "SELECT * FROM ("+productionRelevant+") ORDER BY kind, repository, identity LIMIT ? OFFSET ?", append(args, limit, offset)...)
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
