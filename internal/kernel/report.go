package kernel

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
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
	walBytes := store.WALSize()
	read, archived, err := store.beginHistoryRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	// A nil []byte binds as an empty blob, not NULL, so an unscoped read must
	// pass a nil interface for "?1 IS NULL" to match every project.
	var scope any
	if project != nil {
		scope = project.Bytes()
	}
	since -= since % bucket
	// One row per bucket; merges come from the pull requests factoryd
	// observed merged, dated as specialistEvents dates them.
	rows, err := read.connection.QueryContext(ctx, `WITH spend AS (
		SELECT r.admitted_at_ms / ?3 * ?3 AS bucket, 1 AS runs, r.terminal_kind = 'failed' AS failed,
			CASE WHEN r.role = 'orchestrator' THEN 'overseer' WHEN `+specialistCarrierSQL+` THEN 'specialist' ELSE 'worker' END AS who, COALESCE(k.tokens, 0) AS tokens
		FROM `+archived.table("runs")+` AS r JOIN `+archived.table("tasks")+` AS t ON t.id = r.task_id JOIN agents AS a ON a.id = r.agent_id LEFT JOIN `+archived.table("run_tokens")+` AS k ON k.run_id = r.id
		WHERE (?1 IS NULL OR r.project_id = ?1) AND r.admitted_at_ms >= ?2),
	merged AS (
		SELECT COALESCE(unixepoch(json_extract(document, '$.merged_at')) * 1000, observed_at_ms) / ?3 * ?3 AS bucket, count(*) AS merges
		FROM `+archived.table("production_records")+` WHERE (?1 IS NULL OR project_id = ?1) AND kind = 'pull_request' AND json_extract(document, '$.state') = 'merged'
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
			FROM ` + archived.table("runs") + ` AS r JOIN ` + archived.table("run_tokens") + ` AS k ON k.run_id = r.id JOIN ` + archived.table("tasks") + ` AS t ON t.id = r.task_id
			WHERE (?1 IS NULL OR r.project_id = ?1) AND r.admitted_at_ms >= ?2 GROUP BY t.id ORDER BY 2 DESC LIMIT 5`},
		{"pull", `SELECT p.repository || '#' || p.pull_number, SUM(k.tokens)
			FROM ` + archived.table("publication_tasks") + ` AS p JOIN ` + archived.table("runs") + ` AS r ON r.task_id = p.task_id JOIN ` + archived.table("run_tokens") + ` AS k ON k.run_id = r.id
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
	// Issue #1774 adds this lesson-reuse projection: it describes only each
	// lesson's latest immutable revision; historical revisions must not inflate
	// its counters.
	var lessons, reads, citations, stale int64
	if err := read.connection.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE((SELECT COUNT(DISTINCT a.run_id) FROM `+contentAccessesSQL(archived)+` a JOIN project_content_revisions x ON x.id=a.content_id AND x.revision=a.content_revision WHERE a.kind IN ('read','supplied') AND (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson' AND x.deprecated=0 AND x.revision=(SELECT MAX(revision) FROM project_content_revisions WHERE id=x.id)), 0),
		COALESCE((SELECT SUM(json_array_length(json_extract(x.source_references, '$.evidence'))) FROM project_content_revisions x WHERE (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson' AND x.deprecated=0 AND x.revision=(SELECT MAX(revision) FROM project_content_revisions WHERE id=x.id) AND json_valid(x.source_references) AND json_type(x.source_references, '$.evidence')='array'), 0),
		COALESCE((SELECT COUNT(*) FROM project_content_revisions x WHERE (?1 IS NULL OR x.project_id=?1) AND x.kind='lesson' AND x.deprecated=0 AND x.revision=(SELECT MAX(revision) FROM project_content_revisions WHERE id=x.id) AND json_extract(CASE WHEN json_valid(x.source_references) THEN x.source_references ELSE '{}' END, '$.status') IN ('needs_revalidation','superseded') ), 0)
		FROM project_content_revisions c WHERE (?1 IS NULL OR c.project_id=?1) AND c.kind='lesson' AND c.deprecated=0 AND c.revision=(SELECT MAX(revision) FROM project_content_revisions WHERE id=c.id)`, scope).Scan(&lessons, &reads, &citations, &stale); err != nil {
		return nil, err
	}
	var projectBytes []byte
	if project != nil {
		projectBytes = project.Bytes()
	}
	ledger, err := failureLedger(ctx, read.connection, archived, projectBytes, since)
	if err != nil {
		return nil, err
	}
	for _, entry := range ledger {
		age := "known"
		if entry.first >= since {
			age = "new"
		}
		lines = append(lines, fmt.Sprintf("failure %s x%d last=%s %s: repository=%s run=%s %s", age, entry.count, time.UnixMilli(entry.last).UTC().Format("2006-01-02T15:04Z"), entry.signature, entry.repository, entry.url, entry.example))
	}
	lines = append(lines, fmt.Sprintf("lessons=%d reads=%d citations=%d stale=%d", lessons, reads, citations, stale))
	// Each release updated in the window reports its state, phase and reason.
	rows, err = read.connection.QueryContext(ctx, `SELECT identity, COALESCE(json_extract(document, '$.state'), '-'), COALESCE(json_extract(document, '$.phase'), '-'), COALESCE(json_extract(document, '$.reason'), '-')
		FROM `+archived.table("production_records")+` WHERE (?1 IS NULL OR project_id = ?1) AND kind = 'delivery' AND identity LIKE 'release:%' AND observed_at_ms >= ?2 ORDER BY observed_at_ms`, scope, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var release, state, phase, reason string
		if err := rows.Scan(&release, &state, &phase, &reason); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		lines = append(lines, fmt.Sprintf("restart %s %s phase=%s reason=%s", release[:min(len(release), 20)], state, phase, oneLine(reason, 160)))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	lines = append(lines, "factoryd since="+store.opened.UTC().Format("2006-01-02T15:04Z")+" source="+buildinfo.Current().Source(), fmt.Sprintf("wal_bytes=%d", walBytes))
	return lines, nil
}

// RunOutcomes counts the runs admitted since since (Unix ms) that ended, and
// those that failed.
func (store *Store) RunOutcomes(ctx context.Context, since int64) (ended, failed int64, err error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Close()
	err = tx.connection.QueryRowContext(ctx, `SELECT COUNT(terminal_kind), COALESCE(SUM(terminal_kind = 'failed'), 0) FROM runs WHERE admitted_at_ms >= ?`, since).Scan(&ended, &failed)
	return ended, failed, err
}

// Why says, one line each, where a pull request (pull > 0) or a task stands
// and whom it waits on: its exact head, checks, review at that head, merge
// queue, task, the reconciler's last action and why, then "waits on". It reads
// the reconciler's own record (the review operation at the head); project
// nil reads every project.
func (store *Store) Why(ctx context.Context, project *ProjectID, pull uint64, task TaskID) ([]string, error) {
	return store.whyInRepository(ctx, project, "", pull, task)
}

func (store *Store) whyInRepository(ctx context.Context, project *ProjectID, repository string, pull uint64, task TaskID) ([]string, error) {
	if pull == 0 && task.zero() || pull > 1<<53-1 {
		return nil, ErrInvalidValue
	}
	read, archived, err := store.beginHistoryRead(ctx)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	if project == nil {
		project, err = singletonProject(ctx, read.connection)
		if err != nil {
			return nil, err
		}
	}
	return why(ctx, read.connection, archived, project, repository, pull, task)
}

func singletonProject(ctx context.Context, c *sql.Conn) (*ProjectID, error) {
	rows, err := c.QueryContext(ctx, `SELECT id FROM projects ORDER BY id LIMIT 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []ProjectID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := ProjectIDFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid singleton project", ErrCorruptState)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != 1 {
		return nil, nil
	}
	return &ids[0], nil
}

// why reads pull in repository, or in any repository of the project when it is
// empty, and tasks, their runs and publications across archived (nil reads
// hot rows only).
func why(ctx context.Context, c *sql.Conn, archived history, project *ProjectID, repository string, pull uint64, taskID TaskID) ([]string, error) {
	// A nil []byte binds as an empty blob, not NULL, so an unscoped read must
	// pass a nil interface for "?1 IS NULL" to match every project.
	var scope any
	if project != nil {
		scope = project.Bytes()
	}
	var lines []string
	var task Task
	var found bool
	var err error
	if !taskID.zero() {
		if task, found, err = historyTask(ctx, c, archived, taskID); err != nil {
			return nil, err
		}
		if !found || project != nil && task.ProjectID != *project {
			return nil, ErrNotFound
		}
		lines = append(lines, whyTask(task))
		var phase, kind, detail, agent sql.NullString
		var at int64
		err := c.QueryRowContext(ctx, `SELECT phase, terminal_kind, terminal_detail, lower(hex(agent_id)), updated_at_ms FROM `+archived.table("runs")+` WHERE task_id = ? ORDER BY admitted_at_ms DESC, id DESC LIMIT 1`, taskID.Bytes()).Scan(&phase, &kind, &detail, &agent, &at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			lines = append(lines, fmt.Sprintf("run: %s %s agent=%s at %s %s", phase.String, kind.String, agent.String, time.UnixMilli(at).UTC().Format(time.RFC3339), oneLine(detail.String, 160)))
		}
	}
	// The pull request: the one asked for, or the newest the task published.
	var projectBytes []byte
	var document string
	records := archived.table("production_records")
	query, args := `SELECT r.project_id, r.repository, r.document FROM `+records+` AS r WHERE (?1 IS NULL OR r.project_id = ?1) AND r.kind = 'pull_request' AND r.identity = ?2 AND (?3 = '' OR r.repository = ?3) ORDER BY r.observed_at_ms DESC LIMIT 1`, []any{scope, strconv.FormatUint(pull, 10), repository}
	if pull == 0 {
		query, args = `SELECT r.project_id, r.repository, r.document FROM `+archived.table("publication_tasks")+` AS p JOIN `+records+` AS r ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)
			WHERE p.task_id = ? ORDER BY p.created_at_ms DESC LIMIT 1`, []any{taskID.Bytes()}
	}
	err = c.QueryRowContext(ctx, query, args...).Scan(&projectBytes, &repository, &document)
	if errors.Is(err, sql.ErrNoRows) {
		if pull != 0 {
			return nil, ErrNotFound
		}
		waits, err := whyTaskWaits(ctx, c, task)
		return append(lines, "pull: none published", "waits on: "+waits), err
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
		err := c.QueryRowContext(ctx, `SELECT task_id FROM `+archived.table("publication_tasks")+` WHERE project_id = ? AND repository = ? AND pull_number = ? ORDER BY change_id IS NOT NULL DESC, created_at_ms DESC LIMIT 1`, projectBytes, repository, int64(pr.Number)).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if raw != nil {
			if taskID, err = TaskIDFromBytes(raw); err != nil {
				return nil, err
			}
			if task, found, err = historyTask(ctx, c, archived, taskID); err != nil {
				return nil, err
			}
		}
	}
	head := strings.ToLower(pr.Head)
	lines = append(lines, fmt.Sprintf("pull: %s#%d %s %q", repository, pr.Number, pr.State, oneLine(pr.Title, 80)), "head: "+head)
	// The required checks at the exact head that name this pull request: the
	// reconciler (requiredChecks) ignores optional ones.
	var passed, pending, failed int
	var failing, details []string
	rows, err := c.QueryContext(ctx, `SELECT json_extract(c.document, '$.name'), json_extract(c.document, '$.state'), COALESCE(json_extract(c.document, '$.conclusion'), ''), c.document
		FROM `+records+` AS c, json_each(c.document, '$.pull_requests') AS n
		WHERE c.project_id = ? AND c.repository = ? AND c.kind = 'check' AND json_extract(c.document, '$.scope') = 'head'
		  AND lower(json_extract(c.document, '$.revision')) = ? AND CAST(n.value AS INTEGER) = ? AND json_extract(c.document, '$.required')`, projectBytes, repository, head, int64(pr.Number))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, state, conclusion, document string
		if err := rows.Scan(&name, &state, &conclusion, &document); err != nil {
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
			var check ProductionCheck
			if json.Unmarshal([]byte(document), &check) == nil {
				for _, job := range check.Jobs {
					for _, annotation := range job.Annotations {
						details = append(details, name+": "+oneLine(annotation, 240))
					}
				}
			}
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
	if pr.Review.Findings != "" {
		lines = append(lines, "review-of-record: "+oneLine(pr.Review.Findings, 16000))
	}
	mergeable := "unknown"
	if pr.Mergeable != nil {
		mergeable = strconv.FormatBool(*pr.Mergeable)
	}
	lines = append(lines, checks, verdict, fmt.Sprintf("queue: %s merge_state=%s mergeable=%s", cmp.Or(pr.MergeQueue, "none"), cmp.Or(pr.MergeState, "unknown"), mergeable))
	if len(details) > 0 {
		lines = append(lines, "check failure details: "+strings.Join(details, "; "))
	}
	if found {
		lines = append(lines, whyTask(task))
	} else {
		lines = append(lines, "task: none (no factory task published it)")
	}
	// The reconciler's newest operation at this head.
	var op *review.Operation
	var operation string
	err = c.QueryRowContext(ctx, `SELECT document FROM `+records+` WHERE project_id = ? AND repository = ? AND kind = 'reviewer' AND json_type(document, '$.request') = 'object'
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
		// A verdict's detail is its review of record (daemon review.go), already printed.
		if op.Detail != "" && op.Detail != pr.Review.Findings {
			lines = append(lines, "review detail: "+oneLine(op.Detail, 16000))
		}
		if op.Conversation != "" {
			lines = append(lines, "pull request comments: "+oneLine(op.Conversation, 16000))
		}
		last := cmp.Or(op.LastAction, "none yet")
		if op.Escalation != "" {
			last = "escalated: " + op.Escalation
		}
		lines = append(lines, fmt.Sprintf("reconciler: operation %s %s, failures=%d, enqueues=%d, at %s; last action: %s", op.ID, op.State, op.Failures, op.Enqueues, op.UpdatedAt.UTC().Format(time.RFC3339), oneLine(last, 300)))
	}
	taskWaits := ""
	if found {
		if taskWaits, err = whyTaskWaits(ctx, c, task); err != nil {
			return nil, err
		}
	}
	return append(lines, "waits on: "+whyPullWaits(pr, found, task, taskWaits, op, pending, failed)), nil
}

// whyPullWaits names whom an open pull request waits on, first match wins.
func whyPullWaits(pr ProductionPullRequest, hasTask bool, task Task, taskWaits string, op *review.Operation, pending, failed int) string {
	switch {
	case pr.State != "open":
		return "nobody: it is " + pr.State
	case hasTask && task.Status == TaskCancelled:
		// reconcilePull leaves it: an operator's cancel is final.
		return "operator: its task was cancelled, so factoryd no longer advances it"
	case hasTask && (task.Status == TaskQueued || task.Status == TaskRunning):
		return taskWaits
	case op != nil && op.OwnerApproval:
		return "owner: the merge queue requires CODEOWNERS approval"
	case op != nil && op.Escalation != "":
		return "operator: factoryd escalated it to the overseer"
	case hasTask && (task.Status == TaskBlocked || task.Status == TaskFailed):
		return "operator: its task is " + task.Status.String()
	case op != nil && op.State == "ejected" && op.Failures >= review.FailuresBeforeEscalation:
		return "factoryd: gave up; last action: " + oneLine(cmp.Or(op.LastAction, op.Detail, "none recorded"), 300)
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

// whyTaskWaits names whom a task waits on; a queued one, the first admission
// blocker AdmitNext applies to it (factoryd's health holds are why --project's).
func whyTaskWaits(ctx context.Context, c *sql.Conn, task Task) (string, error) {
	switch task.Status {
	case TaskQueued:
		factory, err := factoryState(ctx, c)
		if err != nil {
			return "", err
		}
		const workers = `(SELECT COUNT(*) FROM runs WHERE role = 'worker' AND phase <> 'terminal')`
		var blocker string
		err = c.QueryRowContext(ctx, `SELECT COALESCE(
			(SELECT 'factoryd: dispatch is disabled' FROM factory WHERE dispatch_enabled = 0),
			(SELECT 'intake: its issue was withdrawn' WHERE `+taskIssueWithdrawn+`),
			(SELECT 'prerequisite: task ' || lower(hex(u.id)) || ' is ' || u.status || ' at work revision ' || u.work_revision || '; it needs revision ' || prerequisite.upstream_work_revision || ' or later to succeed and merge'
				FROM task_prerequisites AS prerequisite JOIN tasks AS u ON u.id = prerequisite.upstream_task_id
				WHERE prerequisite.task_id = t.id AND prerequisite.consumed_run_id IS NULL AND NOT EXISTS (`+prerequisiteSourceRun+`)),
			(SELECT 'conflict path: ' || mine.path || ' is held by running task ' || lower(hex(a.id))
				FROM task_conflict_paths AS mine JOIN task_conflict_paths AS theirs ON theirs.path = mine.path JOIN tasks AS a ON a.id = theirs.task_id AND a.project_id = t.project_id AND a.status = 'running'
				JOIN task_repository_bindings AS mr ON mr.task_id = t.id JOIN task_repository_bindings AS ar ON ar.task_id = a.id AND ar.repository_id = mr.repository_id
				WHERE mine.task_id = t.id),
			(SELECT CASE WHEN COUNT(g.id) = 0 THEN 'agent: its project has no worker that takes shared work'
				WHEN COUNT(g.id) > 1 THEN 'agent: every worker that takes shared work is archived, paused or running'
				ELSE 'agent: ' || g.name || CASE WHEN g.archived THEN ' is archived' WHEN g.paused THEN ' is paused' ELSE ' has a run in progress' END END
				FROM tasks AS c LEFT JOIN agents AS g ON g.project_id = c.project_id AND (g.id = c.assigned_agent_id OR c.assigned_agent_id IS NULL AND g.role = 'worker' AND g.idle_policy = 'wait')
				WHERE c.id = t.id HAVING COUNT(g.id) = COUNT(CASE WHEN g.archived OR g.paused OR EXISTS (SELECT 1 FROM runs AS r WHERE r.agent_id = g.id AND r.phase <> 'terminal') THEN 1 END)),
			(SELECT CASE WHEN a.role = 'orchestrator' THEN 'capacity: its project has an overseer run in progress'
				WHEN `+workers+` >= ?1 THEN 'capacity: every worker slot is in use'
				ELSE 'capacity: a specialist waits while it would take the last free worker slot' END
				FROM tasks AS c LEFT JOIN agents AS a ON a.id = c.assigned_agent_id WHERE c.id = t.id
				  AND (a.role = 'orchestrator' AND (SELECT COUNT(*) FROM runs WHERE role = 'orchestrator' AND project_id = t.project_id AND phase <> 'terminal') >= 1
				    OR a.role IS NOT 'orchestrator' AND (`+workers+` >= ?1 OR `+specialistCarrierSQL+` AND `+specialistCarrierHeldSQL+`))),
			'worker: its task is queued for the next admission')
			FROM tasks AS t WHERE t.id = ?2`, factory.Capacity, task.ID.Bytes()).Scan(&blocker)
		return blocker, err
	case TaskRunning:
		return "worker: its task is running", nil
	case TaskBlocked, TaskFailed:
		return "operator: its task is " + task.Status.String(), nil
	case TaskSucceeded:
		return "factoryd: publication of its settled Change", nil
	}
	return "nobody: its task is " + task.Status.String(), nil
}

// historyTask reads a task, hot or archived.
func historyTask(ctx context.Context, c *sql.Conn, archived history, id TaskID) (Task, bool, error) {
	return scanTask(c.QueryRowContext(ctx, `SELECT `+taskSelectColumns+` FROM `+archived.table("tasks")+` WHERE id = ?`, id.Bytes()))
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

// escalationWhy names, on its own line, the pull request an escalation keyed
// [reviewer:ID] follows and whom it waits on; nothing when the key names no
// pull request. The rest of why (head, checks, review text) is unbounded for a
// wake and one `attempt why --pull` away.
func escalationWhy(ctx context.Context, c *sql.Conn, project ProjectID, key string) string {
	id, ok := strings.CutPrefix(key, "[reviewer:")
	var repository string
	var pull int64
	if !ok || c.QueryRowContext(ctx, `SELECT repository, json_extract(document, '$.request.PullNumber') FROM production_records WHERE project_id = ? AND kind = 'reviewer' AND identity = ?`,
		project.Bytes(), strings.TrimSuffix(id, "]")).Scan(&repository, &pull) != nil || pull <= 0 {
		return ""
	}
	lines, err := why(ctx, c, nil, &project, repository, uint64(pull), TaskID{})
	if err != nil {
		return ""
	}
	return "\n  why: " + lines[0] + "; " + lines[len(lines)-1]
}
