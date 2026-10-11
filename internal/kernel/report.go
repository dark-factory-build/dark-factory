package kernel

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/review"
)

// Report is the factory's efficiency per bucket of bucket ms since since (Unix
// ms), for project or every project when it is nil: runs, failures, tokens by
// who spent them, merges and tokens per merge, then the five tasks and pull
// requests that spent the most. It reads runs, run_tokens and the production
// projection as they are; it records nothing.
func (store *Store) Report(ctx context.Context, project *ProjectID, since, bucket int64) ([]string, error) {
	if since < 0 || bucket <= 0 {
		return nil, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	var scope []byte
	if project != nil {
		scope = project.Bytes()
	}
	since -= since % bucket
	// One row per bucket; merges come from the pull requests factoryd
	// observed merged, dated as specialistEvents dates them.
	rows, err := read.connection.QueryContext(ctx, `WITH spend AS (
		SELECT r.admitted_at_ms / ?3 * ?3 AS bucket, 1 AS runs, r.terminal_kind = 'failed' AS failed,
			CASE WHEN r.role = 'orchestrator' THEN 'overseer' WHEN `+specialistCarrierSQL+` THEN 'specialist' ELSE 'worker' END AS who, COALESCE(k.tokens, 0) AS tokens
		FROM runs AS r JOIN tasks AS t ON t.id = r.task_id JOIN agents AS a ON a.id = r.agent_id LEFT JOIN run_tokens AS k ON k.run_id = r.id
		WHERE (?1 IS NULL OR r.project_id = ?1) AND r.admitted_at_ms >= ?2),
	merged AS (
		SELECT COALESCE(unixepoch(json_extract(document, '$.merged_at')) * 1000, observed_at_ms) / ?3 * ?3 AS bucket, count(*) AS merges
		FROM production_records WHERE (?1 IS NULL OR project_id = ?1) AND kind = 'pull_request' AND json_extract(document, '$.state') = 'merged'
		  AND COALESCE(unixepoch(json_extract(document, '$.merged_at')) * 1000, observed_at_ms) >= ?2 GROUP BY 1),
	buckets AS (SELECT bucket FROM spend UNION SELECT bucket FROM merged)
	SELECT b.bucket, COALESCE(SUM(s.runs), 0), COALESCE(SUM(s.failed), 0),
		COALESCE(SUM(CASE s.who WHEN 'worker' THEN s.tokens END), 0), COALESCE(SUM(CASE s.who WHEN 'overseer' THEN s.tokens END), 0),
		COALESCE(SUM(CASE s.who WHEN 'specialist' THEN s.tokens END), 0), COALESCE((SELECT merges FROM merged WHERE merged.bucket = b.bucket), 0)
	FROM buckets AS b LEFT JOIN spend AS s ON s.bucket = b.bucket GROUP BY b.bucket ORDER BY b.bucket`, scope, since, bucket)
	if err != nil {
		return nil, err
	}
	var lines []string
	for rows.Next() {
		var at, runs, failed, worker, overseer, specialist, merges int64
		if err := rows.Scan(&at, &runs, &failed, &worker, &overseer, &specialist, &merges); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		total, perMerge := worker+overseer+specialist, "-"
		if merges > 0 {
			perMerge = strconv.FormatInt(total/merges, 10)
		}
		failure := int64(0)
		if runs > 0 {
			failure = failed * 100 / runs
		}
		lines = append(lines, fmt.Sprintf("%s runs=%d failed=%d%% tokens=%d worker=%d overseer=%d specialist=%d merges=%d tokens/merge=%s",
			time.UnixMilli(at).UTC().Format("2006-01-02T15:04Z"), runs, failure, total, worker, overseer, specialist, merges, perMerge))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, top := range []struct{ label, query string }{
		{"task", `SELECT lower(hex(t.id)) || ' ' || t.status || ' "' || replace(substr(t.title, 1, 60), char(10), ' ') || '"', SUM(k.tokens)
			FROM runs AS r JOIN run_tokens AS k ON k.run_id = r.id JOIN tasks AS t ON t.id = r.task_id
			WHERE (?1 IS NULL OR r.project_id = ?1) AND r.admitted_at_ms >= ?2 GROUP BY t.id ORDER BY 2 DESC LIMIT 5`},
		{"pull", `SELECT p.repository || '#' || p.pull_number, SUM(k.tokens)
			FROM publication_tasks AS p JOIN runs AS r ON r.task_id = p.task_id JOIN run_tokens AS k ON k.run_id = r.id
			WHERE (?1 IS NULL OR p.project_id = ?1) AND r.admitted_at_ms >= ?2 GROUP BY p.repository, p.pull_number ORDER BY 2 DESC LIMIT 5`},
	} {
		rows, err := read.connection.QueryContext(ctx, top.query, scope, since)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			var tokens int64
			if err := rows.Scan(&name, &tokens); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			lines = append(lines, fmt.Sprintf("top %s tokens=%d %s", top.label, tokens, name))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	var lessons, reads, citations, stale int64
	if err := read.connection.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE((SELECT COUNT(DISTINCT a.run_id) FROM content_accesses a JOIN project_content_revisions x ON x.id=a.content_id AND x.revision=a.content_revision WHERE a.kind IN ('read','supplied') AND (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson'), 0),
		COALESCE((SELECT SUM(json_array_length(json_extract(x.source_references, '$.evidence'))) FROM project_content_revisions x WHERE (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson' AND x.deprecated=0 AND json_valid(x.source_references) AND json_type(x.source_references, '$.evidence')='array'), 0),
		COALESCE((SELECT COUNT(*) FROM project_content_revisions x WHERE (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson' AND x.deprecated=0 AND json_extract(CASE WHEN json_valid(x.source_references) THEN x.source_references ELSE '{}' END, '$.status') IN ('needs_revalidation','superseded') ), 0)
		FROM project_content_revisions c WHERE (?1 IS NULL OR c.project_id=?1) AND c.kind='lesson' AND c.deprecated=0 AND c.revision=(SELECT MAX(revision) FROM project_content_revisions WHERE id=c.id)`, scope).Scan(&lessons, &reads, &citations, &stale); err != nil {
		return nil, err
	}
	lines = append(lines, fmt.Sprintf("lessons=%d reads=%d citations=%d stale=%d", lessons, reads, citations, stale))
	return lines, nil
}

// Why says, one line each, where a pull request (pull > 0) or a task stands
// and whom it waits on: its exact head, checks, review at that head, merge
// queue, task, the reconciler's last action and why, then "waits on". It reads
// the reconciler's own record (the review operation at the head); project
// nil reads every project.
func (store *Store) Why(ctx context.Context, project *ProjectID, pull uint64, task TaskID) ([]string, error) {
	if pull == 0 && task.zero() || pull > 1<<53-1 {
		return nil, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	return why(ctx, read.connection, project, "", pull, task)
}

// why reads pull in repository, or in any repository of the project when it is
// empty.
func why(ctx context.Context, c *sql.Conn, project *ProjectID, repository string, pull uint64, taskID TaskID) ([]string, error) {
	var scope []byte
	if project != nil {
		scope = project.Bytes()
	}
	var lines []string
	var task Task
	var found bool
	var err error
	if !taskID.zero() {
		if task, found, err = taskByID(ctx, c, taskID); err != nil {
			return nil, err
		}
		if !found || project != nil && task.ProjectID != *project {
			return nil, ErrNotFound
		}
		lines = append(lines, whyTask(task))
		var phase, kind, detail sql.NullString
		var at int64
		err := c.QueryRowContext(ctx, `SELECT phase, terminal_kind, terminal_detail, updated_at_ms FROM runs WHERE task_id = ? ORDER BY admitted_at_ms DESC, id DESC LIMIT 1`, taskID.Bytes()).Scan(&phase, &kind, &detail, &at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			lines = append(lines, fmt.Sprintf("run: %s %s at %s %s", phase.String, kind.String, time.UnixMilli(at).UTC().Format(time.RFC3339), oneLine(detail.String, 160)))
		}
	}
	// The pull request: the one asked for, or the newest the task published.
	var projectBytes []byte
	var document string
	query, args := `SELECT r.project_id, r.repository, r.document FROM production_records AS r WHERE (?1 IS NULL OR r.project_id = ?1) AND r.kind = 'pull_request' AND r.identity = ?2 AND (?3 = '' OR r.repository = ?3) ORDER BY r.observed_at_ms DESC LIMIT 1`, []any{scope, strconv.FormatUint(pull, 10), repository}
	if pull == 0 {
		query, args = `SELECT r.project_id, r.repository, r.document FROM publication_tasks AS p JOIN production_records AS r ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)
			WHERE p.task_id = ? ORDER BY p.created_at_ms DESC LIMIT 1`, []any{taskID.Bytes()}
	}
	err = c.QueryRowContext(ctx, query, args...).Scan(&projectBytes, &repository, &document)
	if errors.Is(err, sql.ErrNoRows) {
		if pull != 0 {
			return nil, ErrNotFound
		}
		return append(lines, "pull: none published", "waits on: "+whyTaskWaits(task)), nil
	}
	var pr ProductionPullRequest
	if err == nil && json.Unmarshal([]byte(document), &pr) != nil {
		err = ErrCorruptState
	}
	if err != nil {
		return nil, err
	}
	if !found {
		var raw []byte
		err := c.QueryRowContext(ctx, `SELECT task_id FROM publication_tasks WHERE project_id = ? AND repository = ? AND pull_number = ? ORDER BY change_id IS NOT NULL DESC, created_at_ms DESC LIMIT 1`, projectBytes, repository, int64(pr.Number)).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if raw != nil {
			if taskID, err = TaskIDFromBytes(raw); err != nil {
				return nil, err
			}
			if task, found, err = taskByID(ctx, c, taskID); err != nil {
				return nil, err
			}
		}
	}
	head := strings.ToLower(pr.Head)
	lines = append(lines, fmt.Sprintf("pull: %s#%d %s %q", repository, pr.Number, pr.State, oneLine(pr.Title, 80)), "head: "+head)
	// The required checks at the exact head that name this pull request: the
	// reconciler (requiredChecks) ignores optional ones.
	var passed, pending, failed int
	var failing []string
	rows, err := c.QueryContext(ctx, `SELECT json_extract(c.document, '$.name'), json_extract(c.document, '$.state'), COALESCE(json_extract(c.document, '$.conclusion'), '')
		FROM production_records AS c, json_each(c.document, '$.pull_requests') AS n
		WHERE c.project_id = ? AND c.repository = ? AND c.kind = 'check' AND json_extract(c.document, '$.scope') = 'head'
		  AND lower(json_extract(c.document, '$.revision')) = ? AND CAST(n.value AS INTEGER) = ? AND json_extract(c.document, '$.required')`, projectBytes, repository, head, int64(pr.Number))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, state, conclusion string
		if err := rows.Scan(&name, &state, &conclusion); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		switch {
		case state != "completed":
			pending++
		case conclusion == "success" || conclusion == "neutral" || conclusion == "skipped":
			passed++
		default:
			failed++
			failing = append(failing, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	checks := fmt.Sprintf("required checks: %d passed, %d pending, %d failed", passed, pending, failed)
	if len(failing) > 0 {
		checks += " (" + strings.Join(failing, ", ") + ")"
	}
	verdict := "review: none at head"
	if strings.EqualFold(pr.Review.Head, head) {
		verdict = "review: " + pr.Review.State + " at head"
	}
	mergeable := "unknown"
	if pr.Mergeable != nil {
		mergeable = strconv.FormatBool(*pr.Mergeable)
	}
	lines = append(lines, checks, verdict, fmt.Sprintf("queue: %s merge_state=%s mergeable=%s", cmp.Or(pr.MergeQueue, "none"), cmp.Or(pr.MergeState, "unknown"), mergeable))
	if found {
		lines = append(lines, whyTask(task))
	} else {
		lines = append(lines, "task: none (no factory task published it)")
	}
	// The reconciler's newest operation at this head.
	var op *review.Operation
	var operation string
	err = c.QueryRowContext(ctx, `SELECT document FROM production_records WHERE project_id = ? AND repository = ? AND kind = 'reviewer' AND json_type(document, '$.request') = 'object'
		AND json_extract(document, '$.request.PullNumber') = ? AND lower(json_extract(document, '$.request.Head')) = ? ORDER BY json_extract(document, '$.created_at') DESC LIMIT 1`,
		projectBytes, repository, int64(pr.Number), head).Scan(&operation)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		lines = append(lines, "reconciler: no operation at this head")
	case err != nil:
		return nil, err
	default:
		op = &review.Operation{}
		if json.Unmarshal([]byte(operation), op) != nil {
			return nil, ErrCorruptState
		}
		last := cmp.Or(op.LastAction, "none yet")
		if op.Escalation != "" {
			last = "escalated: " + op.Escalation
		}
		lines = append(lines, fmt.Sprintf("reconciler: operation %s %s, failures=%d, enqueues=%d, at %s; last action: %s", op.ID, op.State, op.Failures, op.Enqueues, op.UpdatedAt.UTC().Format(time.RFC3339), oneLine(last, 300)))
	}
	return append(lines, "waits on: "+whyPullWaits(pr, found, task, op, pending, failed)), nil
}

// whyPullWaits names whom an open pull request waits on, first match wins.
func whyPullWaits(pr ProductionPullRequest, hasTask bool, task Task, op *review.Operation, pending, failed int) string {
	switch {
	case pr.State != "open":
		return "nobody: it is " + pr.State
	case hasTask && task.Status == TaskCancelled:
		// reconcilePull leaves it: an operator's cancel is final.
		return "operator: its task was cancelled, so factoryd no longer advances it"
	case hasTask && (task.Status == TaskQueued || task.Status == TaskRunning):
		return "worker: its task is " + task.Status.String()
	case op != nil && op.OwnerApproval:
		return "owner: the merge queue requires CODEOWNERS approval"
	case op != nil && op.Escalation != "":
		return "operator: factoryd escalated it to the overseer"
	case hasTask && (task.Status == TaskBlocked || task.Status == TaskFailed):
		return "operator: its task is " + task.Status.String()
	case failed > 0:
		// review.Decide (and a review's wait for checks) acts on a failing check
		// before a pending one or a missing verdict.
		return "factoryd: failing checks send it back on its next pass"
	case op == nil || op.State == "running" || op.State == "submitting":
		return "reviewer: no verdict at this head yet"
	case pending > 0:
		return "CI: checks at the head are pending"
	case pr.MergeQueue != "" && pr.MergeQueue != "none" && pr.MergeQueue != "unknown":
		return "CI: it is in the merge queue (" + pr.MergeQueue + ")"
	case op.Verdict == "request_changes":
		return "factoryd: its next pass sends it back"
	}
	return "factoryd: its next pass acts (every 5 minutes)"
}

func whyTaskWaits(task Task) string {
	switch task.Status {
	case TaskQueued, TaskRunning:
		return "worker: its task is " + task.Status.String()
	case TaskBlocked, TaskFailed:
		return "operator: its task is " + task.Status.String()
	case TaskSucceeded:
		return "factoryd: publication of its settled Change"
	}
	return "nobody: its task is " + task.Status.String()
}

func whyTask(task Task) string {
	line := fmt.Sprintf("task: %s %s rev=%d %q", task.ID, task.Status, task.WorkRevision.Int64(), oneLine(task.Title, 60))
	if task.BlockedReason != "" {
		line += ": " + oneLine(task.BlockedReason, 160)
	}
	return line
}

func oneLine(value string, limit int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ToValidUTF8(value[:min(len(value), limit)], "")
}

// escalationWhy is the why line, on its own line, of the pull request an
// escalation keyed [reviewer:ID] names, so the overseer reads where it stands
// and whom it waits on; nothing when the key names no pull request. The
// escalation it follows is the reconciler's last action, so it is left out.
func escalationWhy(ctx context.Context, c *sql.Conn, project ProjectID, key string) string {
	id, ok := strings.CutPrefix(key, "[reviewer:")
	var repository string
	var pull int64
	if !ok || c.QueryRowContext(ctx, `SELECT repository, json_extract(document, '$.request.PullNumber') FROM production_records WHERE project_id = ? AND kind = 'reviewer' AND identity = ?`,
		project.Bytes(), strings.TrimSuffix(id, "]")).Scan(&repository, &pull) != nil || pull <= 0 {
		return ""
	}
	lines, err := why(ctx, c, &project, repository, uint64(pull), TaskID{})
	if err != nil {
		return ""
	}
	lines = slices.DeleteFunc(lines, func(line string) bool { return strings.HasPrefix(line, "reconciler: ") })
	return "\n  why: " + strings.Join(lines, "; ")
}
