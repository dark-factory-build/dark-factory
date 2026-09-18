package kernel

import (
	"context"
	"database/sql"
	"fmt"
)

// TaskRecovery is the bounded operator read used by unattended controllers.
// It is deliberately derived from the canonical task/run/Change rows; callers
// never need to open the private SQLite database or reconstruct authority.
type TaskRecovery struct {
	Task                  Task
	Incarnation           IncarnationID
	NeedsOperatorRecovery bool
	Change                *Change
	Run                   *Run
	Artifacts             []Resource
	// Overseer is the project's standing-instruction orchestrator when one
	// exists. OverseerNotification is what its wake cursor proves: pending
	// while this task's newest event is still ahead of the cursor, scheduled
	// once EnqueueOverseerWakeups has consumed it. Scheduled is not seen and
	// not handled. OverseerTask is the task that orchestrator is running, else
	// its next queued one: the dependency a pending wake waits behind.
	// HumanRequest is the unresolved question raised by this task's own runs:
	// the one Needs You durable state ties to the task.
	Overseer             *AgentID
	OverseerNotification OverseerNotification
	OverseerTask         *Task
	HumanRequest         *HumanRequestID
	LastProgressAt       UnixMillis
}

type OverseerNotification string

const (
	OverseerNotificationNone      OverseerNotification = "none"
	OverseerNotificationPending   OverseerNotification = "pending"
	OverseerNotificationScheduled OverseerNotification = "scheduled"
)

// Disposition is the explicit decision recorded on the task after its latest
// run, derived from durable state only: an unresolved Needs You from its own
// run, a queued retry, running, a terminal outcome, an operator-recovery
// need, or none at all. A failed task whose wake is scheduled with no
// disposition is unhandled, whatever any overseer transcript says.
func (recovery TaskRecovery) Disposition() string {
	if recovery.HumanRequest != nil {
		return "needs_you"
	}
	switch recovery.Task.Status {
	case TaskQueued:
		if recovery.Run != nil {
			return "retry_queued"
		}
		return "queued"
	case TaskRunning, TaskSucceeded, TaskCancelled:
		return recovery.Task.Status.String()
	}
	if recovery.NeedsOperatorRecovery {
		return "needs_operator_recovery"
	}
	return "none"
}

func (store *Store) TaskRecovery(ctx context.Context, id TaskID, incarnation IncarnationID) (TaskRecovery, bool, error) {
	if id.zero() || incarnation.zero() {
		return TaskRecovery{}, false, ErrInvalidValue
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		return TaskRecovery{}, false, err
	}
	defer read.Close()
	task, found, err := taskByID(ctx, read.connection, id)
	if err != nil || !found {
		return TaskRecovery{}, found, err
	}
	if task.IncarnationID != incarnation {
		return TaskRecovery{}, false, nil
	}
	if err := validateTaskRunTopologyBounded(ctx, read.connection, task, MaxRecoveryRuns); err != nil {
		return TaskRecovery{}, false, err
	}
	var stale int
	if err := read.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM human_requests h JOIN runs r ON r.id=h.run_id WHERE r.task_id=? AND r.task_incarnation_id=? AND h.status='stale')`, id.Bytes(), incarnation.Bytes()).Scan(&stale); err != nil {
		return TaskRecovery{}, false, err
	}
	result := TaskRecovery{Task: task, Incarnation: incarnation, NeedsOperatorRecovery: stale != 0, Artifacts: []Resource{}}
	run, runFound, err := latestRunForTask(ctx, read.connection, task)
	if err != nil {
		return TaskRecovery{}, false, err
	}
	if runFound {
		result.Run = &run
		resources, err := resourcesForRunBounded(ctx, read.connection, run.ID, MaxRecoveryResources)
		if err != nil {
			return TaskRecovery{}, false, err
		}
		result.Artifacts = resources
	}
	change, changeFound, err := changeForTask(ctx, read.connection, task)
	if err != nil {
		return TaskRecovery{}, false, err
	}
	if changeFound {
		result.Change = &change
	}
	if err := lastProgressForTask(ctx, read.connection, task, &result); err != nil {
		return TaskRecovery{}, false, err
	}
	if err := overseerNotificationForTask(ctx, read.connection, task, &result); err != nil {
		return TaskRecovery{}, false, err
	}
	return result, true, nil
}

// lastProgressForTask is the newest transition among every record scoped to
// the task: the task row, its runs, human requests and peer questions on
// them, and interventions against it. A meaningful exchange on any of these
// counts; nothing outside them does.
func lastProgressForTask(ctx context.Context, connection *sql.Conn, task Task, result *TaskRecovery) error {
	var latest int64
	if err := connection.QueryRowContext(ctx, `SELECT MAX(at) FROM (
		SELECT updated_at_ms AS at FROM tasks WHERE id = ?
		UNION ALL SELECT updated_at_ms FROM runs WHERE task_id = ?
		UNION ALL SELECT h.updated_at_ms FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id WHERE r.task_id = ?
		UNION ALL SELECT updated_at_ms FROM peer_questions WHERE source_task_id = ? OR target_task_id = ?
		UNION ALL SELECT updated_at_ms FROM task_interventions WHERE task_id = ?)`,
		task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes()).Scan(&latest); err != nil {
		return err
	}
	at, err := NewUnixMillis(latest)
	if err != nil {
		return fmt.Errorf("%w: invalid task progress time", ErrCorruptState)
	}
	result.LastProgressAt = at
	return nil
}

// overseerNotificationForTask answers from the wake cursor and nothing else.
// The cursor consumes worker events in sequence order, so an event still
// ahead of it is pending and one behind it was scheduled into a wake; which
// wake, and whether it read the task, is not knowable from durable records
// and is not claimed. Human requests count only when this task's own run
// raised them: no prose, title or timestamp ever associates a request or a
// wake with a task.
func overseerNotificationForTask(ctx context.Context, connection *sql.Conn, task Task, result *TaskRecovery) error {
	result.OverseerNotification = OverseerNotificationNone
	var raw []byte
	err := connection.QueryRowContext(ctx, `SELECT h.id FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id
		WHERE h.status IN ('open', 'delivering', 'delivery_unknown') AND r.task_id = ? AND r.task_incarnation_id = ?
		ORDER BY h.created_at_ms DESC, h.id DESC LIMIT 1`, task.ID.Bytes(), task.IncarnationID.Bytes()).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		request, idErr := HumanRequestIDFromBytes(raw)
		if idErr != nil {
			return fmt.Errorf("%w: invalid human request identity", ErrCorruptState)
		}
		result.HumanRequest = &request
	}
	// The task-kind wake names worker and unassigned tasks only; an
	// orchestrator's own task has no task-kind wake and names no overseer.
	var orchestratorOwned int
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id = ? AND role = 'orchestrator')`, task.AssignedAgentID.Bytes()).Scan(&orchestratorOwned); err != nil {
		return err
	}
	if orchestratorOwned != 0 {
		return nil
	}
	err = connection.QueryRowContext(ctx, `SELECT id FROM agents WHERE project_id = ? AND role = 'orchestrator' AND idle_policy = 'standing_instruction' AND paused = 0 AND archived = 0 AND tool_calls_used < tool_budget_limit ORDER BY id LIMIT 1`, task.ProjectID.Bytes()).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	overseer, err := AgentIDFromBytes(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid overseer identity", ErrCorruptState)
	}
	result.Overseer = &overseer
	current, found, err := scanTask(connection.QueryRowContext(ctx, `SELECT `+taskSelectColumns+` FROM tasks
		WHERE assigned_agent_id = ? AND status IN ('running', 'queued') ORDER BY status = 'running' DESC, priority DESC, created_at_ms ASC, id ASC LIMIT 1`, overseer.Bytes()))
	if err != nil {
		return err
	}
	if found {
		result.OverseerTask = &current
	}
	// The same entities workerInvalidationTargetsAfter joins, in sequence order.
	var latest int64
	if err := connection.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM invalidations WHERE entity_kind = 'task' AND entity_id = ?
		OR entity_kind = 'run' AND entity_id IN (SELECT id FROM runs WHERE task_id = ?)
		OR entity_kind = 'human_request' AND entity_id IN (SELECT h.id FROM human_requests AS h JOIN runs AS r ON r.id = h.run_id WHERE r.task_id = ?)
		OR entity_kind = 'peer_question' AND entity_id IN (SELECT id FROM peer_questions WHERE source_task_id = ? OR target_task_id = ?)`,
		task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes(), task.ID.Bytes()).Scan(&latest); err != nil {
		return err
	}
	factory, err := factoryState(ctx, connection)
	if err != nil {
		return err
	}
	cursor, found, err := overseerWakeCursor(ctx, connection, overseer)
	if err != nil {
		return err
	}
	result.OverseerNotification = OverseerNotificationScheduled
	if !found || cursor < latest || cursor < factory.Floor.Int64()-1 {
		result.OverseerNotification = OverseerNotificationPending
	}
	return nil
}

func latestRunForTask(ctx context.Context, connection *sql.Conn, task Task) (Run, bool, error) {
	return scanRun(connection.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE project_id=? AND task_id=? AND task_incarnation_id=? ORDER BY admitted_task_work_revision DESC LIMIT 1`, task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes()))
}
