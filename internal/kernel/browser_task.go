package kernel

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// BrowserTaskEnqueue is the result of the browser's bounded operator enqueue
// mutation. The agent revision is returned alongside the task revision so the
// caller can correlate the write with the exact observation it supplied.
type BrowserTaskEnqueue struct {
	Task          Task
	AgentRevision Revision
}

// BrowserEnqueueMode is how the console submits an instruction from an
// agent's pane: start it on that agent now, queue it for that agent, or queue
// it for any eligible worker in that agent's project.
type BrowserEnqueueMode uint8

const (
	BrowserEnqueueNow BrowserEnqueueMode = iota
	BrowserEnqueueQueue
	BrowserEnqueueAnyWorker
)

// EnqueueTaskForBrowserAgent atomically revalidates the browser client's
// authority, the selected agent's identity/revision and its runnable state,
// then inserts a normal-priority task. The browser never chooses project,
// title or priority: those are derived here from the durable agent and fixed
// operator semantics.
func (store *Store) EnqueueTaskForBrowserAgent(ctx context.Context, clientID BrowserClientID, taskID TaskID, incarnationID IncarnationID, agentID AgentID, expectedAgentRevision Revision, instruction string, at UnixMillis) (BrowserTaskEnqueue, error) {
	return store.EnqueueTaskForBrowserAgentMode(ctx, clientID, taskID, incarnationID, agentID, expectedAgentRevision, instruction, BrowserEnqueueNow, at)
}

// EnqueueTaskForBrowserAgentMode permits queued work while busy or paused;
// direct instructions still require an empty, unpaused agent. Any-worker
// work is queued with no assigned agent; admission claims it.
func (store *Store) EnqueueTaskForBrowserAgentMode(ctx context.Context, clientID BrowserClientID, taskID TaskID, incarnationID IncarnationID, agentID AgentID, expectedAgentRevision Revision, instruction string, mode BrowserEnqueueMode, at UnixMillis) (BrowserTaskEnqueue, error) {
	queue := mode != BrowserEnqueueNow
	if clientID.zero() || taskID.zero() || incarnationID.zero() || agentID.zero() || expectedAgentRevision.Int64() < 1 || strings.Trim(instruction, " \t\r\n") == "" || !utf8.ValidString(instruction) || byteLen(instruction) > 32768 {
		return BrowserTaskEnqueue{}, fmt.Errorf("%w: invalid browser task enqueue", ErrInvalidValue)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return BrowserTaskEnqueue{}, err
	}
	defer tx.Close()
	client, found, err := browserClientByID(ctx, tx.connection, clientID)
	if err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	if !found || client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityHumanActions) {
		return BrowserTaskEnqueue{}, tx.Rollback(ErrUnauthorized)
	}
	agent, found, err := agentByID(ctx, tx.connection, agentID)
	if err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	if !found {
		return BrowserTaskEnqueue{}, tx.Rollback(ErrNotFound)
	}
	if agent.Revision != expectedAgentRevision || agent.Archived || agent.Paused && !queue {
		return BrowserTaskEnqueue{}, tx.Rollback(ErrRevisionConflict)
	}
	spec := NewTask{ID: taskID, ProjectID: agent.ProjectID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: "Direct instruction", Body: instruction, Priority: 0}
	if mode == BrowserEnqueueAnyWorker {
		spec.AssignedAgentID = AgentID{}
	}
	if err := validateNewTask(spec); err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	existing, replay, err := taskCreationReplay(ctx, tx.connection, spec)
	if err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	if replay {
		if err = tx.Rollback(nil); err != nil {
			return BrowserTaskEnqueue{}, err
		}
		return BrowserTaskEnqueue{Task: existing, AgentRevision: agent.Revision}, nil
	}
	var active int
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE assigned_agent_id = ? AND status IN ('queued', 'running') LIMIT 1)`, agent.ID.Bytes()).Scan(&active); err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	if active != 0 && !queue {
		return BrowserTaskEnqueue{}, tx.Rollback(ErrRevisionConflict)
	}
	result, err := insertTaskOnConnection(ctx, tx.connection, spec, at)
	if err != nil {
		return BrowserTaskEnqueue{}, tx.Rollback(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return BrowserTaskEnqueue{}, err
	}
	return BrowserTaskEnqueue{Task: result, AgentRevision: agent.Revision}, nil
}
