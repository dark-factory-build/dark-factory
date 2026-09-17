package kernel

import (
	"context"
	"database/sql"
)

// TaskRecovery is the operator read used by unattended controllers. It is
// deliberately derived from the canonical task/run/Change rows; callers never
// need to open the private SQLite database or reconstruct authority. The
// reply is bounded to the latest run, MaxRecoveryResources artifacts, and a
// truncated result; the run history behind it is validated in full, exactly
// as the Task read does, however many retries the task has accumulated.
type TaskRecovery struct {
	Task                  Task
	Incarnation           IncarnationID
	NeedsOperatorRecovery bool
	Change                *Change
	Run                   *Run
	Artifacts             []Resource
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
	if err := validateTaskRunTopology(ctx, read.connection, task); err != nil {
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
	return result, true, nil
}

func latestRunForTask(ctx context.Context, connection *sql.Conn, task Task) (Run, bool, error) {
	return scanRun(connection.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE project_id=? AND task_id=? AND task_incarnation_id=? ORDER BY admitted_task_work_revision DESC LIMIT 1`, task.ProjectID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes()))
}
