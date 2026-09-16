package kernel

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// EnqueueOverseerWakeups consumes worker activity from the durable
// invalidation journal. A cursor is deliberately left behind a queued or
// running overseer, so activity while it works causes one follow-up after it
// exits. Journal pruning is conservative: a cursor behind the floor wakes once.
func (store *Store) EnqueueOverseerWakeups(ctx context.Context, at UnixMillis) ([]Task, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	factory, err := factoryState(ctx, tx.connection)
	if err != nil {
		return nil, tx.Rollback(err)
	}
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE role = 'orchestrator' AND idle_policy = 'standing_instruction' AND paused = 0
		  AND tool_calls_used < tool_budget_limit
		  AND MAX(updated_at_ms, COALESCE((SELECT MAX(terminal_at_ms) FROM runs WHERE agent_id = agents.id), 0)) + idle_after_seconds * 1000 <= ?
		ORDER BY id`, at.Int64())
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
	changed := false
	for _, agent := range agents {
		cursor, found, err := overseerWakeCursor(ctx, tx.connection, agent.ID)
		if err != nil {
			return nil, tx.Rollback(err)
		}
		if !found {
			cursor = 0
		}
		var active int
		if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE assigned_agent_id = ? AND status IN ('queued', 'running'))`, agent.ID.Bytes()).Scan(&active); err != nil {
			return nil, tx.Rollback(err)
		}
		if active != 0 {
			continue
		}
		// A missing cursor is the one initial inspection for a newly enabled
		// rule. Thereafter only worker activity (or conservative prune recovery)
		// can start the instruction again.
		fullReconciliation := !found || cursor < factory.Floor.Int64()-1
		var targets []TaskID
		if !fullReconciliation {
			targets, err = workerInvalidationTargetsAfter(ctx, tx.connection, agent.ProjectID, cursor, factory.Head.Int64())
			if err != nil {
				return nil, tx.Rollback(err)
			}
		}
		if fullReconciliation || len(targets) != 0 {
			prior, err := latestOverseerTask(ctx, tx.connection, agent.ID)
			if err != nil {
				return nil, tx.Rollback(err)
			}
			if prior == nil {
				fullReconciliation = true
				targets = nil
			}
			body, fits := overseerWakeInstruction(agent.Provider, agent.Idle.Instruction, targets, prior, fullReconciliation)
			if !fits {
				// Retaining every causal identity would exceed the exact task-delivery
				// bound. Say so explicitly and make the next run reconcile instead of
				// silently dropping an event or failing a legal standing instruction.
				fullReconciliation = true
				body, fits = overseerWakeInstruction(agent.Provider, agent.Idle.Instruction, nil, prior, true)
				if !fits {
					return nil, tx.Rollback(fmt.Errorf("%w: overseer wake instruction exceeds task bound", ErrInvalidValue))
				}
			}
			task, err := enqueueStandingTaskWithBody(ctx, tx.connection, agent, body, at)
			if err != nil {
				return nil, tx.Rollback(err)
			}
			tasks = append(tasks, task)
		}
		if cursor != factory.Head.Int64() || !found {
			if err := setOverseerWakeCursor(ctx, tx.connection, agent, factory.Head.Int64()); err != nil {
				return nil, tx.Rollback(err)
			}
			changed = true
		}
	}
	if !changed {
		return nil, tx.Rollback(nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
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

func overseerWakeCursor(ctx context.Context, connection *sql.Conn, agentID AgentID) (int64, bool, error) {
	var sequence int64
	err := connection.QueryRowContext(ctx, `SELECT invalidation_sequence FROM overseer_wake_cursors WHERE agent_id = ?`, agentID.Bytes()).Scan(&sequence)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if sequence < 0 {
		return 0, false, fmt.Errorf("%w: invalid overseer wake cursor", ErrCorruptState)
	}
	return sequence, true, nil
}

func setOverseerWakeCursor(ctx context.Context, connection *sql.Conn, agent Agent, sequence int64) error {
	if sequence < 0 {
		return fmt.Errorf("%w: invalid overseer wake cursor", ErrInvalidValue)
	}
	_, err := connection.ExecContext(ctx, `INSERT INTO overseer_wake_cursors(agent_id, project_id, invalidation_sequence) VALUES(?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET project_id = excluded.project_id, invalidation_sequence = excluded.invalidation_sequence`, agent.ID.Bytes(), agent.ProjectID.Bytes(), sequence)
	return err
}

// workerInvalidationTargetsAfter keeps the actual affected task identities,
// rather than merely answering whether one exists. That is the causal input
// an ordinary overseer wake needs to avoid reconstructing every collection.
func workerInvalidationTargetsAfter(ctx context.Context, connection *sql.Conn, projectID ProjectID, cursor, head int64) ([]TaskID, error) {
	if cursor >= head {
		return nil, nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT task_id FROM (
		SELECT i.sequence, t.id AS task_id FROM invalidations AS i
		JOIN tasks AS t ON t.id = i.entity_id JOIN agents AS a ON a.id = t.assigned_agent_id
		WHERE i.sequence > ? AND i.sequence <= ? AND i.entity_kind = 'task' AND t.project_id = ? AND a.project_id = t.project_id AND a.role = 'worker'
		UNION ALL SELECT i.sequence, r.task_id FROM invalidations AS i JOIN runs AS r ON r.id = i.entity_id
		WHERE i.sequence > ? AND i.sequence <= ? AND i.entity_kind = 'run' AND r.project_id = ? AND r.role = 'worker'
		UNION ALL SELECT i.sequence, r.task_id FROM invalidations AS i JOIN human_requests AS h ON h.id = i.entity_id JOIN runs AS r ON r.id = h.run_id
		WHERE i.sequence > ? AND i.sequence <= ? AND i.entity_kind = 'human_request' AND r.project_id = ? AND r.role = 'worker'
		UNION ALL SELECT i.sequence, q.source_task_id FROM invalidations AS i JOIN peer_questions AS q ON q.id = i.entity_id
		WHERE i.sequence > ? AND i.sequence <= ? AND i.entity_kind = 'peer_question' AND q.project_id = ?
		UNION ALL SELECT i.sequence, q.target_task_id FROM invalidations AS i JOIN peer_questions AS q ON q.id = i.entity_id
		WHERE i.sequence > ? AND i.sequence <= ? AND i.entity_kind = 'peer_question' AND q.project_id = ?
	) ORDER BY sequence, task_id`, cursor, head, projectID.Bytes(), cursor, head, projectID.Bytes(), cursor, head, projectID.Bytes(), cursor, head, projectID.Bytes(), cursor, head, projectID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[TaskID]struct{})
	var tasks []TaskID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := TaskIDFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid worker wake task", ErrCorruptState)
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			tasks = append(tasks, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func overseerWakeInstruction(provider Provider, instruction string, targets []TaskID, prior *TaskID, full bool) (string, bool) {
	mode := "targeted"
	identities := make([]string, 0, len(targets))
	for _, target := range targets {
		identities = append(identities, target.String())
	}
	if full {
		mode = "full"
	}
	priorID := ""
	if prior != nil {
		priorID = prior.String()
	}
	body := instruction + "\n\nFactory causal wake: mode=" + mode + "; task_ids=" + strings.Join(identities, ",") + "; prior_task_id=" + priorID + ". Read prior_task_id first when present, then each named task, without --head; retain the first returned head for related paging/text reads. mode=full requires fixed-head reconciliation."
	if provider == ProviderClaudeCode {
		_, err := runner.PrepareClaudeTask([]byte(body))
		if err == nil || !full {
			return body, err == nil
		}
		_, err = runner.PrepareClaudeTask([]byte(instruction))
		return instruction, err == nil
	}
	limit := runner.MaxProviderTaskBytes
	if provider == ProviderCodex {
		limit = runner.MaxCodexTaskBytes
	}
	// Without room for optional context, retain the exact legal instruction.
	// The bootstrap treats an absent causal envelope as full reconciliation.
	if full && byteLen(body) > limit && byteLen(instruction) <= limit {
		return instruction, true
	}
	return body, byteLen(body) <= limit
}
