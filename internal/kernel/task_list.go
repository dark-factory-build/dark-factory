package kernel

import (
	"context"
	"fmt"
)

const TaskListPageSize = 10

type TaskList struct {
	Head    EventSequence
	Total   uint64
	Tasks   []TaskSummary
	HasMore bool
}

// ReadTaskList reads a bounded completion page in one transaction. The last
// (update time, ID) is a keyset cursor, so unrelated events do not invalidate it.
// ponytail: a project page only holds assigned tasks, so terminal tasks that
// never had an agent are excluded; widen the predicate if they must show.
func (store *Store) ReadTaskList(ctx context.Context, agentID AgentID, projectID ProjectID, beforeAt UnixMillis, beforeID TaskID) (TaskList, error) {
	if agentID.zero() == projectID.zero() {
		return TaskList{}, ErrInvalidValue
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return TaskList{}, err
	}
	defer tx.Close()
	scope, scopeID := `assigned_agent_id = ?`, agentID.Bytes()
	var found bool
	if agentID.zero() {
		scope, scopeID = `project_id = ? AND assigned_agent_id IS NOT NULL`, projectID.Bytes()
		_, found, err = projectByID(ctx, tx.connection, projectID)
	} else {
		_, found, err = agentByID(ctx, tx.connection, agentID)
	}
	if err != nil {
		return TaskList{}, err
	} else if !found {
		return TaskList{}, ErrNotFound
	}
	factory, err := factoryState(ctx, tx.connection)
	if err != nil {
		return TaskList{}, err
	}
	result := TaskList{Head: factory.Head}
	if err := tx.connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE `+scope+` AND status NOT IN ('queued', 'running')`, scopeID).Scan(&result.Total); err != nil {
		return TaskList{}, err
	}
	query := `SELECT ` + publicTaskColumns + ` FROM tasks WHERE ` + scope + ` AND status NOT IN ('queued', 'running')`
	args := []any{scopeID}
	if !beforeID.zero() {
		query += ` AND (updated_at_ms, id) < (?, ?)`
		args = append(args, beforeAt.Int64(), beforeID.Bytes())
	}
	rows, err := tx.connection.QueryContext(ctx, query+` ORDER BY updated_at_ms DESC, id DESC LIMIT ?`, append(args, TaskListPageSize+1)...)
	if err != nil {
		return TaskList{}, fmt.Errorf("read completed tasks: %w", err)
	}
	defer rows.Close()
	result.Tasks, err = scanPublicTasks(rows)
	if err != nil {
		return TaskList{}, err
	}
	result.HasMore = len(result.Tasks) > TaskListPageSize
	if result.HasMore {
		result.Tasks = result.Tasks[:TaskListPageSize]
	}
	return result, nil
}
