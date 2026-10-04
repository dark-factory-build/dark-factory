package kernel

import (
	"context"
	"database/sql"
	"fmt"
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
)

// overseerWakeItems is every work item of a project that needs its
// overseer, at the version it last changed. An item persists until the
// overseer acts on it (a blocked or failed worker task, an unanswered worker
// question, finished work not yet published or corrected behind its open pull
// request, a pull request factoryd escalated); a succeeded or cancelled worker
// task needs one look. It is due when no carrier was enqueued since that
// version, or, while it persists, when at most three were and the latest is
// OverseerRewakeAfter old: one wake and three re-wakes per item version.
const overseerWakeItems = `WITH carrier AS (SELECT created_at_ms AS at FROM tasks WHERE assigned_agent_id = ?4 AND title = ?5),
item AS (
	SELECT t.id, t.updated_at_ms AS version, t.status IN ('blocked', 'failed') AS persistent, '' AS detail
	FROM tasks AS t JOIN agents AS a ON a.id = t.assigned_agent_id
	WHERE t.project_id = ?1 AND a.role = 'worker' AND t.status IN ('succeeded', 'blocked', 'failed', 'cancelled')
	UNION ALL SELECT r.task_id, h.created_at_ms, 1, '' FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id
	WHERE r.project_id = ?1 AND r.role = 'worker' AND h.status IN ('open', 'delivering', 'delivery_unknown')
	UNION ALL SELECT c.task_id, c.updated_at_ms, 1, '' FROM changes AS c JOIN tasks AS t ON t.id = c.task_id
	WHERE c.project_id = ?1 AND t.status = 'succeeded' AND c.head_commit IS NOT NULL AND c.base_commit IS NOT NULL
	  AND c.head_commit <> c.base_commit AND c.updated_at_ms + ?2 <= ?3
	  AND (NOT EXISTS (SELECT 1 FROM publication_tasks AS p WHERE p.change_id = c.id)
	       OR EXISTS (SELECT 1 FROM publication_tasks AS p JOIN production_records AS r
	           ON r.project_id = p.project_id AND r.repository = p.repository AND r.kind = 'pull_request' AND r.identity = CAST(p.pull_number AS TEXT)
	           WHERE p.change_id = c.id AND json_extract(r.document, '$.state') = 'open' AND c.updated_at_ms > p.created_at_ms))
	UNION ALL SELECT NULL, observed_at_ms, 1, json_extract(document, '$.escalation') FROM production_records
	WHERE project_id = ?1 AND kind = 'reviewer' AND COALESCE(json_extract(document, '$.escalation'), '') <> ''
	  AND COALESCE(json_extract(document, '$.route_pending'), 0) = 0)
SELECT id, detail FROM item
WHERE version > COALESCE((SELECT at FROM carrier ORDER BY at DESC LIMIT 1 OFFSET 3), -1)
  AND (NOT EXISTS (SELECT 1 FROM carrier WHERE at > version) OR persistent AND (SELECT MAX(at) FROM carrier) + ?6 <= ?3)
ORDER BY version LIMIT 33`

// EnqueueOverseerWakeups applies one level-triggered rule to each standing
// overseer: while it has no unfinished wake carrier and some item is due, it
// gets one carrier naming the due items. An item is due when no carrier was
// enqueued since it last changed, or, while it still needs the overseer, when
// fewer than four were and the latest is OverseerRewakeAfter old.
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
	var tasks []Task
	for _, agent := range agents {
		body, due, err := overseerWake(ctx, tx.connection, agent, at.Int64())
		if err != nil {
			return nil, tx.Rollback(err)
		}
		if !due {
			continue
		}
		// Validate once, before the first write; a quiet poll rolls back
		// without scanning retained history.
		if len(tasks) == 0 {
			if err := validateDurableControls(ctx, tx.connection); err != nil {
				return nil, tx.Rollback(err)
			}
		}
		task, err := enqueueStandingTaskWithBody(ctx, tx.connection, agent, body, at)
		if err != nil {
			return nil, tx.Rollback(err)
		}
		tasks = append(tasks, task)
	}
	if len(tasks) == 0 {
		return nil, tx.Rollback(nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
}

// overseerWake returns the carrier body for agent's due items, if any.
func overseerWake(ctx context.Context, connection *sql.Conn, agent Agent, at int64) (string, bool, error) {
	rows, err := connection.QueryContext(ctx, overseerWakeItems, agent.ProjectID.Bytes(), PublicationAttentionAfter.Milliseconds(), at,
		agent.ID.Bytes(), overseerWakeTitle, OverseerRewakeAfter.Milliseconds())
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var targets []TaskID
	var escalations []string
	full := false
	for rows.Next() {
		var raw []byte
		var detail string
		if err := rows.Scan(&raw, &detail); err != nil {
			return "", false, err
		}
		if len(targets)+len(escalations) == 32 {
			full = true // more is due than one wake names
			break
		}
		if raw == nil {
			escalations = append(escalations, strings.ToValidUTF8(detail[:min(len(detail), 512)], ""))
			continue
		}
		id, err := TaskIDFromBytes(raw)
		if err != nil {
			return "", false, fmt.Errorf("%w: invalid overseer wake task", ErrCorruptState)
		}
		if !slices.Contains(targets, id) {
			targets = append(targets, id)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(targets)+len(escalations) == 0 {
		return "", false, nil
	}
	prior, err := latestOverseerTask(ctx, connection, agent.ID)
	if err != nil {
		return "", false, err
	}
	return overseerWakeInstruction(agent.Provider, agent.Idle.Instruction, targets, escalations, prior, full || prior == nil), true, nil
}

// latestOverseerTask is durable continuity, not a new conversation store. The
// next wake names this task so its result, decisions and operation IDs are one
// targeted status read away. Failure and cancellation detail comes from the
// exact settled run, rather than the task row, through that same status read.
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
// order (task identities, then escalations) and the wake becomes a full
// reconciliation; the bare instruction was checked against it when set.
func overseerWakeInstruction(provider Provider, instruction string, targets []TaskID, escalations []string, prior *TaskID, full bool) string {
	identities := make([]string, 0, len(targets))
	for _, target := range targets {
		identities = append(identities, target.String())
	}
	priorID := ""
	if prior != nil {
		priorID = prior.String()
	}
	for {
		mode := "targeted"
		if full {
			mode = "full"
		}
		body := instruction + "\n\nFactory causal wake: mode=" + mode + "; task_ids=" + strings.Join(identities, ",") + "; prior_task_id=" + priorID + ". Read prior_task_id first when present, then each named task, without --head; retain the first returned head for related paging/text reads. mode=full requires fixed-head reconciliation."
		for _, escalation := range escalations {
			body += "\nEscalated: " + escalation
		}
		switch {
		case wakeBodyFits(provider, body):
			return body
		case len(identities) != 0:
			identities, full = nil, true
		case len(escalations) != 0:
			escalations = nil
		default:
			return instruction
		}
	}
}

func wakeBodyFits(provider Provider, body string) bool {
	if provider == ProviderClaudeCode {
		_, err := runner.PrepareClaudeTask([]byte(body))
		return err == nil
	}
	limit := runner.MaxProviderTaskBytes
	if provider == ProviderCodex {
		limit = runner.MaxCodexTaskBytes
	}
	return byteLen(body) <= limit
}
