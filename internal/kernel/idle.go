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
	spec := NewTask{ID: taskID, ProjectID: agent.ProjectID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: overseerWakeTitle, Body: body, Priority: overseerWakePriority}
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
