package kernel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	MaxIdleAfterSeconds = 604800
	MaxIdleRunBudget    = 1000000
	maxIdleInstruction  = 32768
)

// validateIdleRule mirrors the schema. A zero standing budget is retained as
// legacy configuration and does not create an admission ceiling.
func validateIdleRule(rule IdleRule) error {
	if _, err := ParseIdlePolicy(string(rule.Policy)); err != nil {
		return err
	}
	if rule.AfterSeconds > MaxIdleAfterSeconds || rule.RunBudget > MaxIdleRunBudget ||
		!utf8.ValidString(rule.Instruction) || byteLen(rule.Instruction) > maxIdleInstruction {
		return fmt.Errorf("%w: idle rule out of bounds", ErrInvalidValue)
	}
	if rule.Policy == IdleStandingInstruction && (rule.AfterSeconds < 1 || strings.Trim(rule.Instruction, " \t\r\n") == "") {
		return fmt.Errorf("%w: a standing instruction needs a wait and text", ErrInvalidValue)
	}
	return nil
}

func validateIdleRuleForProvider(provider Provider, rule IdleRule) error {
	if err := validateIdleRule(rule); err != nil {
		return err
	}
	if rule.Policy == IdleStandingInstruction {
		switch provider {
		case ProviderCodex:
			if byteLen(rule.Instruction) > runner.MaxCodexTaskBytes {
				return fmt.Errorf("%w: Codex standing instruction exceeds provider delivery bound", ErrInvalidValue)
			}
		case ProviderClaudeCode:
			if _, err := runner.PrepareClaudeTask([]byte(rule.Instruction)); err != nil {
				return fmt.Errorf("%w: Claude standing instruction exceeds provider delivery bound", ErrInvalidValue)
			}
		}
	}
	return nil
}

func idleRuleFromRow(policy string, after int64, instruction string, budget, used int64) (IdleRule, error) {
	if after < 0 || after > MaxIdleAfterSeconds || budget < 0 || budget > MaxIdleRunBudget || used < 0 {
		return IdleRule{}, fmt.Errorf("%w: idle rule out of bounds", ErrInvalidValue)
	}
	rule := IdleRule{Policy: IdlePolicy(policy), AfterSeconds: uint32(after), Instruction: instruction, RunBudget: uint32(budget), RunsUsed: uint32(used)}
	return rule, validateIdleRule(rule)
}

// EnqueueIdleInstructions enqueues each idle agent's standing instruction to
// itself once its quiet spell has passed, and spends one of its idle runs
// for it, in one transaction. Idle means the agent itself could take work
// (not paused, tool budget left; the factory's dispatch switch and capacity
// stay admission's to apply once the task is queued; the daemon defers
// automatic enqueue calls while dispatch is paused) and has no queued or
// running task, so the rule never stacks on work; a run in flight is a
// running task, which the durable checks enforce. The quiet spell starts at
// the later of the agent's last edit and its
// last run's end, so editing the rule restarts the clock. An agent with any
// queued or running task is left alone, and so is a paused one; a budget
// is retained only as legacy configuration, never as an admission ceiling.
// The agent revision is the same CAS the console's enqueue uses, so a human
// instruction landing in the same window wins or loses cleanly.
func (store *Store) EnqueueIdleInstructions(ctx context.Context, at UnixMillis) ([]Task, error) {
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.connection.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents
	WHERE role = 'worker' AND idle_policy = 'standing_instruction' AND paused = 0 AND archived = 0 AND tool_calls_used < tool_budget_limit
		  AND NOT EXISTS (SELECT 1 FROM tasks WHERE assigned_agent_id = agents.id AND status IN ('queued', 'running'))
		  AND MAX(updated_at_ms, COALESCE((SELECT MAX(terminal_at_ms) FROM runs WHERE agent_id = agents.id), 0)) + idle_after_seconds * 1000 <= ?
		ORDER BY id`, at.Int64())
	if err != nil {
		return nil, tx.Rollback(err)
	}
	var due []Agent
	for rows.Next() {
		agent, found, err := scanAgent(rows)
		if err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return nil, tx.Rollback(err)
		}
		due = append(due, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, tx.Rollback(err)
	}
	if len(due) == 0 {
		return nil, tx.Rollback(nil)
	}
	// A no-op poll needs no full history scan. The reserved transaction keeps
	// these candidates unchanged until validation precedes the first write.
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return nil, tx.Rollback(err)
	}
	var tasks []Task
	for _, agent := range due {
		task, err := enqueueStandingTask(ctx, tx.connection, agent, at)
		if err != nil {
			return nil, tx.Rollback(err)
		}
		tasks = append(tasks, task)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
}

// standingTaskTitle names every idle wake; the public snapshot filters on it.
const standingTaskTitle = "Standing instruction"

func enqueueStandingTask(ctx context.Context, connection *sql.Conn, agent Agent, at UnixMillis) (Task, error) {
	return enqueueStandingTaskWithBody(ctx, connection, agent, agent.Idle.Instruction, at)
}

func enqueueStandingTaskWithBody(ctx context.Context, connection *sql.Conn, agent Agent, body string, at UnixMillis) (Task, error) {
	var ids [2][IDBytes]byte
	for index := range ids {
		if _, err := rand.Read(ids[index][:]); err != nil || ids[index] == [IDBytes]byte{} {
			if err == nil {
				err = fmt.Errorf("%w: generated zero task identifier", ErrCorruptState)
			}
			return Task{}, err
		}
	}
	taskID, _ := TaskIDFromBytes(ids[0][:])
	incarnationID, _ := IncarnationIDFromBytes(ids[1][:])
	spec := NewTask{ID: taskID, ProjectID: agent.ProjectID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: standingTaskTitle, Body: body, Priority: 0}
	if err := validateNewTask(spec); err != nil {
		return Task{}, err
	}
	task, err := insertTaskOnConnection(ctx, connection, spec, at)
	if err != nil {
		return Task{}, err
	}
	result, err := connection.ExecContext(ctx, `UPDATE agents SET idle_runs_used = idle_runs_used + 1, revision = revision + 1, updated_at_ms = ? WHERE id = ? AND revision = ?`, at.Int64(), agent.ID.Bytes(), agent.Revision.Int64())
	if err := requireOneRow(result, err); err != nil {
		return Task{}, err
	}
	if err := appendInvalidations(ctx, connection, at, []pendingInvalidation{{kind: EntityAgent, id: agent.ID.Bytes(), revision: agent.Revision.Int64() + 1}}); err != nil {
		return Task{}, err
	}
	return task, nil
}
