package kernel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// An overseer's wake carrier is its standing instruction at the head of its
// queue; an item it left unhandled is re-woken OverseerRewakeAfter apart.
const (
	overseerWakeTitle    = "Standing instruction"
	overseerWakePriority = 1000
	OverseerRewakeAfter  = 30 * time.Minute
	overseerWakeSettle   = time.Minute
	overseerWakeMaxDelay = 5 * time.Minute
)

// overseerWakeItems is every work item of a project that needs its
// overseer, at the version it last changed. An item persists until the
// overseer acts on it (a blocked or failed worker task, an unanswered worker
// question, finished work not yet published or corrected behind its open pull
// request, a pull request factoryd escalated while it stays open at the
// escalated head); a succeeded worker task, and a Change factoryd could not
// publish (it has no pull request), need one look. An accepted intake task
// that succeeded with a diff needs none: factoryd publishes it, and the
// Change item covers a publication that never happens. An intake task the
// overseer could retry (an automatic end) is an item too; an operator's cancel
// and a withdrawn issue's task are not. An item is due when
// no carrier named it since that version; after that, at most once per
// OverseerRewakeAfter, while no carrier that named it started (one look), or
// while it persists and fewer than four did: one wake and three re-wakes per
// item version, once the newest due item is overseerWakeSettle old or the
// oldest overseerWakeMaxDelay old. A persistent item past its re-wakes is
// stalled (overseerStalledItems).
const overseerWakeItems = overseerItems + `SELECT id IS NULL, CASE WHEN id IS NULL THEN detail ELSE (` + overseerWakeLine + `) END, item_key, (` + overseerWakeCounts + `)
FROM (SELECT *, MIN(version) OVER () AS oldest, MAX(version) OVER () AS newest FROM counted
	WHERE named = 0 OR named_at + ?6 <= ?3 AND (wakes = 0 OR persistent AND wakes < 4)) AS due
WHERE ?3 - newest >= ?7 OR ?3 - oldest >= ?8
ORDER BY version LIMIT 33`

// overseerStalledItems is each persistent item past its re-wakes, with the
// carrier task that last named it and whether OverseerRewakeAfter has passed
// since any wake named it. It takes the wake query's arguments; ?7 and ?8 are
// named only so the counts agree.
const overseerStalledItems = overseerItems + `SELECT last, CASE WHEN id IS NULL THEN 'Escalated: ' || detail || ' ' || item_key ELSE (` + overseerWakeLine + `) END,
	named_at + ?6 <= ?3 FROM counted AS due WHERE persistent AND wakes >= 4 AND ?7 + ?8 >= 0 ORDER BY last, version`

const overseerItems = `WITH carrier AS (SELECT t.id AS task, t.created_at_ms AS at, t.body,
	EXISTS (SELECT 1 FROM runs AS r WHERE r.task_id = t.id AND r.terminal_detail IS NOT ?9 AND r.terminal_detail IS NOT ?10) AS started
	FROM tasks AS t WHERE t.assigned_agent_id = ?4 AND t.title = ?5),
item AS (
	SELECT t.id, t.updated_at_ms AS version, t.status IN ('blocked', 'failed') AS persistent, '' AS detail, lower(hex(t.id)) AS item_key
	FROM tasks AS t JOIN agents AS a ON a.id = t.assigned_agent_id
	WHERE t.project_id = ?1 AND a.role = 'worker' AND t.status IN ('succeeded', 'blocked', 'failed') AND NOT ` + taskIssueWithdrawn + `
	  AND NOT (t.status = 'succeeded' AND EXISTS (SELECT 1 FROM intake_task_bindings AS b WHERE b.task_id = t.id)
	      AND EXISTS (SELECT 1 FROM changes AS c WHERE c.task_id = t.id AND c.head_commit <> c.base_commit))
	UNION ALL SELECT r.task_id, h.created_at_ms, 1, '', lower(hex(r.task_id)) FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id
	WHERE r.project_id = ?1 AND r.role = 'worker' AND h.status IN ('open', 'delivering', 'delivery_unknown')
	UNION ALL SELECT c.task_id, c.updated_at_ms, 1, '', lower(hex(c.task_id)) FROM changes AS c JOIN tasks AS t ON t.id = c.task_id
	WHERE c.project_id = ?1 AND t.status = 'succeeded' AND c.head_commit IS NOT NULL AND c.base_commit IS NOT NULL
	  AND c.head_commit <> c.base_commit AND c.updated_at_ms + ?2 <= ?3
	  AND (NOT EXISTS (SELECT 1 FROM publication_tasks AS p WHERE p.change_id = c.id)
	       OR EXISTS (SELECT 1 FROM publication_tasks AS p JOIN production_records AS r
	           ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)
	           WHERE p.change_id = c.id AND json_extract(r.document, '$.state') = 'open' AND c.updated_at_ms > p.created_at_ms))
	UNION ALL SELECT t.id, t.updated_at_ms, 1, '', lower(hex(t.id)) FROM tasks AS t
	JOIN intake_task_bindings AS b ON b.task_id = t.id
	JOIN intake_acceptances AS i ON i.id = b.acceptance_id
	JOIN agents AS o ON o.id = i.overseer_agent_id
	WHERE t.project_id = ?1 AND o.id = ?4 AND ` + taskEndedAutomatically + ` AND NOT ` + taskIssueWithdrawn + `
	UNION ALL SELECT NULL, e.observed_at_ms, 1, json_extract(e.document, '$.escalation'), '[reviewer:' || e.identity || ']' FROM production_records AS e
	JOIN production_records AS p ON p.project_id = e.project_id AND p.repository = e.repository AND p.kind = 'pull_request'
	  AND p.identity = CAST(json_extract(e.document, '$.request.PullNumber') AS TEXT)
	WHERE e.project_id = ?1 AND e.kind = 'reviewer' AND COALESCE(json_extract(e.document, '$.escalation'), '') <> ''
	  AND COALESCE(json_extract(e.document, '$.route_pending'), 0) = 0 AND json_extract(p.document, '$.state') = 'open'
	  AND lower(json_extract(p.document, '$.head')) = lower(json_extract(e.document, '$.request.Head'))
	UNION ALL SELECT NULL, observed_at_ms, 0, json_extract(document, '$.escalation'), '[reviewer:' || identity || ']' FROM production_records
	WHERE project_id = ?1 AND kind = 'reviewer' AND json_extract(document, '$.state') = 'publish_failed'
	  AND COALESCE(json_extract(document, '$.escalation'), '') <> ''),
counted AS (SELECT item.*, (SELECT count(*) FROM carrier WHERE ` + overseerWakeNames + `) AS named,
	(SELECT MAX(at) FROM carrier WHERE ` + overseerWakeNames + `) AS named_at,
	(SELECT count(*) FROM carrier WHERE started AND ` + overseerWakeNames + `) AS wakes,
	(SELECT task FROM carrier WHERE started AND ` + overseerWakeNames + ` ORDER BY at DESC LIMIT 1) AS last FROM item)
`

// overseerWakeNames is a carrier since the item's version that named it, by
// its key (a task identity, or an escalation's reviewer record), or that named
// no item at all (a full reconciliation, or a bare instruction when the causal
// record overflowed).
const overseerWakeNames = `at > version AND (NOT instr(body, 'wake: mode=targeted;') OR instr(body, item_key))`

// EnqueueOverseerWakeups applies one level-triggered rule to each standing
// overseer: while it has no unfinished wake carrier and some item is due, it
// gets one carrier naming the due items (see overseerWakeItems), and the items
// it left stalled become the operator's NEEDS YOU card (raiseStalledItems).
func (store *Store) EnqueueOverseerWakeups(ctx context.Context, at UnixMillis) ([]Task, error) {
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents AS a
		WHERE role = 'orchestrator' AND idle_policy = 'standing_instruction' AND paused = 0 AND archived = 0
		  AND NOT EXISTS (SELECT 1 FROM tasks WHERE assigned_agent_id = a.id AND title = ? AND status IN ('queued', 'running'))
		ORDER BY id`, overseerWakeTitle)
	if err != nil {
		return nil, tx.Rollback(err)
	}
	var agents []Agent
	for rows.Next() {
		agent, found, err := scanAgent(rows)
		if err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return nil, tx.Rollback(err)
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, tx.Rollback(err)
	}
	if err := rows.Close(); err != nil {
		return nil, tx.Rollback(err)
	}
	// Validate once, before the first write; a quiet poll rolls back without
	// scanning retained history.
	validated := false
	validate := func() error {
		if validated {
			return nil
		}
		validated = true
		return validateDurableControls(ctx, tx.connection)
	}
	var tasks []Task
	for _, agent := range agents {
		if err := raiseStalledItems(ctx, tx, agent, at, validate); err != nil {
			return nil, tx.Rollback(err)
		}
		body, due, err := overseerWake(ctx, tx.connection, agent, at.Int64())
		if err != nil {
			return nil, tx.Rollback(err)
		}
		if !due {
			continue
		}
		if err := validate(); err != nil {
			return nil, tx.Rollback(err)
		}
		task, err := enqueueStandingTaskWithBody(ctx, tx.connection, agent, body, at)
		if err != nil {
			return nil, tx.Rollback(err)
		}
		tasks = append(tasks, task)
	}
	if !validated {
		return nil, tx.Rollback(nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
}

func overseerItemArgs(agent Agent, at int64) []any {
	return []any{agent.ProjectID.Bytes(), PublicationAttentionAfter.Milliseconds(), at, agent.ID.Bytes(), overseerWakeTitle,
		OverseerRewakeAfter.Milliseconds(), overseerWakeSettle.Milliseconds(), overseerWakeMaxDelay.Milliseconds(), NeverStartedRunDetail, ProviderCapacityRunDetail}
}

// stalledItemKey is the idempotency key of the one stalled-item card a
// carrier run can carry. Only a human answers such a card: the overseer's
// snapshot omits it and its reply path refuses it.
var stalledItemKey = [IDBytes]byte([]byte("stalled item key"))

// raiseStalledItems opens, for each carrier that last named items the
// overseer left stalled, one human request naming them, waiting on that
// carrier's settled run as a yield would: a reply resumes the carrier with
// it, a cancel leaves the items to the operator. A carrier run carries at
// most one such card; one still unfinished, or already carrying a request,
// waits. A card closes only once none of its carrier's items is stalled, so
// a later wake that moves an item's timing never strands it uncarded.
func raiseStalledItems(ctx context.Context, tx *writeTx, agent Agent, at UnixMillis, validate func() error) error {
	connection := tx.connection
	rows, err := connection.QueryContext(ctx, overseerStalledItems, overseerItemArgs(agent, at.Int64())...)
	if err != nil {
		return err
	}
	lines, stalled := map[string][]string{}, map[string]bool{}
	for rows.Next() {
		var carrier []byte
		var line string
		var ripe bool
		if err := rows.Scan(&carrier, &line, &ripe); err != nil {
			return errors.Join(err, rows.Close())
		}
		stalled[string(carrier)] = true
		if ripe {
			lines[string(carrier)] = append(lines[string(carrier)], line)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, carrier := range slices.Sorted(maps.Keys(lines)) {
		id, err := TaskIDFromBytes([]byte(carrier))
		if err != nil {
			return err
		}
		task, found, err := taskByID(ctx, connection, id)
		if err != nil {
			return err
		}
		if !found || task.Status == TaskQueued || task.Status == TaskRunning {
			continue
		}
		var run []byte
		var skip bool
		err = connection.QueryRowContext(ctx, `SELECT r.id, EXISTS(SELECT 1 FROM human_requests WHERE run_id = r.id AND (idempotency_key = ?4 OR status IN ('open', 'delivering', 'delivery_unknown')))
			OR (SELECT count(*) FROM human_requests WHERE status IN ('open', 'delivering', 'delivery_unknown')) >= ?5
			FROM runs AS r WHERE r.task_id = ?1 AND r.task_incarnation_id = ?2 AND r.admitted_task_work_revision = ?3 AND r.phase = 'terminal'`,
			id.Bytes(), task.IncarnationID.Bytes(), task.WorkRevision.Int64(), stalledItemKey[:], MaxOpenHumanRequests).Scan(&run, &skip)
		if errors.Is(err, sql.ErrNoRows) || err == nil && skip {
			continue
		}
		if err != nil {
			return err
		}
		if err := validate(); err != nil {
			return err
		}
		question := "The overseer was woken four times and left these unresolved. Reply to wake it with your decision, or cancel to leave them to you.\n" + strings.Join(lines[carrier], "\n")
		var request [IDBytes]byte
		if _, err := rand.Read(request[:]); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO human_requests(id, run_id, idempotency_key, kind, reason_code, question_text, options_json, status, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, 'question', 'provider_question', ?, '[]', 'open', 1, ?, ?)`,
			request[:], run, stalledItemKey[:], strings.ToValidUTF8(question[:min(len(question), MaxHumanRequestQuestionBytes)], ""), at.Int64(), at.Int64()); err != nil {
			return err
		}
		if err := appendInvalidations(ctx, connection, at, []pendingInvalidation{{kind: EntityHumanRequest, id: request[:], revision: 1}}); err != nil {
			return err
		}
		if _, err := insertWaitingContinuation(ctx, connection, task, ConditionHumanRequest, ContinuationConditionID(request), Revision{value: 1}, at); err != nil {
			return err
		}
	}
	cards, err := connection.QueryContext(ctx, `SELECT h.id, r.task_id FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id JOIN tasks AS t ON t.id = r.task_id
		WHERE h.idempotency_key = ? AND h.status = 'open' AND t.assigned_agent_id = ?`, stalledItemKey[:], agent.ID.Bytes())
	if err != nil {
		return err
	}
	var resolved []HumanRequestID
	for cards.Next() {
		var raw, carrier []byte
		if err := cards.Scan(&raw, &carrier); err != nil {
			return errors.Join(err, cards.Close())
		}
		if !stalled[string(carrier)] {
			id, err := HumanRequestIDFromBytes(raw)
			if err != nil {
				return errors.Join(err, cards.Close())
			}
			resolved = append(resolved, id)
		}
	}
	if err := errors.Join(cards.Err(), cards.Close()); err != nil {
		return err
	}
	for _, id := range resolved {
		request, found, err := humanRequestByID(ctx, connection, id)
		if err != nil || !found {
			return errors.Join(err, ErrCorruptState)
		}
		run, found, err := runByID(ctx, connection, request.RunID)
		if err != nil || !found {
			return errors.Join(err, ErrCorruptState)
		}
		continuation, found, err := humanRequestContinuation(ctx, connection, request, run)
		if err != nil || !found {
			return errors.Join(err, ErrCorruptState)
		}
		if err := validate(); err != nil {
			return err
		}
		if err := cancelHumanContinuationOnConnection(ctx, tx, request, continuation, at); err != nil {
			return err
		}
	}
	return nil
}

// overseerWake returns the carrier body for agent's due items, if any.
func overseerWake(ctx context.Context, connection *sql.Conn, agent Agent, at int64) (string, bool, error) {
	rows, err := connection.QueryContext(ctx, overseerWakeItems, overseerItemArgs(agent, at)...)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var lines, escalations []string
	var counts string
	full := false
	for rows.Next() {
		var escalation bool
		var detail, key string
		if err := rows.Scan(&escalation, &detail, &key, &counts); err != nil {
			return "", false, err
		}
		if len(lines)+len(escalations) == 32 {
			full = true // more is due than one wake names
			break
		}
		if escalation {
			escalations = append(escalations, "Escalated: "+strings.ToValidUTF8(detail[:min(len(detail), 256)], "")+" "+key)
		} else if !slices.Contains(lines, detail) {
			lines = append(lines, detail)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(lines)+len(escalations) == 0 {
		return "", false, nil
	}
	prior, err := latestOverseerTask(ctx, connection, agent.ID)
	if err != nil {
		return "", false, err
	}
	return overseerWakeInstruction(agent.Provider, agent.Idle.Instruction, counts, lines, escalations, prior, full || prior == nil), true, nil
}

// overseerWakeLine summarises one due task in a line: identity, title, status,
// work revision, its latest Change head, its pull request and why it waits.
const overseerWakeLine = `SELECT printf('- %s "%s" %s rev=%d', lower(hex(t.id)), replace(substr(t.title, 1, 60), char(10), ' '), t.status, t.work_revision)
	|| COALESCE((SELECT printf(' change=%s@%s', substr(lower(hex(c.id)), 1, 12), substr(lower(hex(c.head_commit)), 1, 8)) FROM changes AS c
		WHERE c.task_id = t.id AND c.head_commit IS NOT NULL ORDER BY c.updated_at_ms DESC LIMIT 1), '')
	|| COALESCE((SELECT printf(' PR #%d %s/%s', p.pull_number, json_extract(r.document, '$.state'), json_extract(r.document, '$.review.state'))
		FROM publication_tasks AS p JOIN production_records AS r ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)
		WHERE p.task_id = t.id OR p.change_id IN (SELECT id FROM changes WHERE task_id = t.id) ORDER BY p.created_at_ms DESC LIMIT 1), '')
	|| COALESCE(': ' || replace(substr(COALESCE(t.blocked_reason, (SELECT h.question_text FROM human_requests AS h JOIN runs AS u ON u.id = h.run_id
		WHERE u.task_id = t.id AND h.status IN ('open', 'delivering', 'delivery_unknown') LIMIT 1)), 1, 120), char(10), ' '), '')
FROM tasks AS t WHERE t.id = due.id`

const overseerWakeCounts = `SELECT printf('worker tasks queued=%d running=%d; open PRs=%d', COALESCE(SUM(t.status = 'queued'), 0), COALESCE(SUM(t.status = 'running'), 0),
	(SELECT count(*) FROM production_records WHERE project_id = ?1 AND kind = 'pull_request' AND json_extract(document, '$.state') = 'open'))
FROM tasks AS t LEFT JOIN agents AS a ON a.id = t.assigned_agent_id
WHERE t.project_id = ?1 AND t.status IN ('queued', 'running') AND COALESCE(a.role, 'worker') = 'worker'`

// latestOverseerTask is the prior overseer task a wake names for reference:
// its result, decisions and operation IDs are one status read away.
func latestOverseerTask(ctx context.Context, connection *sql.Conn, agentID AgentID) (*TaskID, error) {
	var raw []byte
	err := connection.QueryRowContext(ctx, `SELECT t.id FROM runs AS r JOIN tasks AS t ON t.id = r.task_id
		WHERE r.agent_id = ? AND r.role = 'orchestrator' AND r.phase = 'terminal'
		AND r.task_incarnation_id = t.incarnation_id AND r.admitted_task_work_revision = t.work_revision AND (
			(t.status = 'succeeded' AND t.result IS NOT NULL AND length(trim(t.result)) > 0) OR
			(t.status = 'blocked' AND t.blocked_reason IS NOT NULL AND length(trim(t.blocked_reason)) > 0) OR
			(t.status IN ('failed', 'cancelled') AND r.terminal_detail IS NOT NULL AND length(trim(r.terminal_detail)) > 0)
		)
		ORDER BY r.terminal_at_ms DESC, r.id DESC LIMIT 1`, agentID.Bytes()).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := TaskIDFromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid prior overseer task", ErrCorruptState)
	}
	return &id, nil
}

// overseerWakeInstruction appends the causal record to the standing
// instruction. What does not fit the provider's delivery bound is dropped in
// order (task lines, then escalations) and the wake becomes a full
// reconciliation; the bare instruction was checked against it when set.
func overseerWakeInstruction(provider Provider, instruction, counts string, lines, escalations []string, prior *TaskID, full bool) string {
	priorID := ""
	if prior != nil {
		priorID = prior.String()
	}
	for {
		mode := "targeted"
		if full {
			mode = "full"
		}
		body := instruction + "\n\nFactory causal wake: mode=" + mode + "; prior_task_id=" + priorID + "; " + counts + ". mode=full requires fixed-head reconciliation."
		for _, line := range slices.Concat(lines, escalations) {
			body += "\n" + line
		}
		switch {
		case wakeBodyFits(provider, body):
			return body
		case len(lines) != 0:
			lines, full = nil, true
		case len(escalations) != 0:
			escalations = nil
		default:
			return instruction
		}
	}
}

func wakeBodyFits(provider Provider, body string) bool {
	limit := runner.MaxProviderTaskBytes
	if provider != ProviderShell {
		limit = runner.MaxNativeTaskBytes
	}
	return byteLen(body) <= limit
}
