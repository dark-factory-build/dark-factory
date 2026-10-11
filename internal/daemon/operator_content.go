package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) projectContent(ctx context.Context, call api.Call) api.Reply {
	request, ok := call.ProjectContentInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	switch request.Operation {
	case "production":
		var input struct {
			ProjectID string `json:"project_id"`
			Offset    uint64 `json:"offset"`
			Limit     uint64 `json:"limit"`
		}
		if json.Unmarshal(request.Input, &input) != nil || input.ProjectID == "" || input.Limit == 0 || input.Limit > 8 || input.Offset > uint64(^uint(0)>>1) {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		project, err := decodeID(input.ProjectID, kernel.ProjectIDFromBytes)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		daemon.refreshProductionDetached(project)
		at, err := daemon.timestamp()
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		page, err := daemon.store.Production(ctx, project, int(input.Offset), int(input.Limit), at)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(struct {
			kernel.ProductionPage
			Runtime api.BuildIdentity    `json:"runtime"`
			Release api.PublishedRelease `json:"release,omitzero"`
		}{page, currentDaemonBuild(), latestPublishedRelease(daemon)})
	case "task_list":
		var input struct {
			AgentID           string `json:"agent_id"`
			ProjectID         string `json:"project_id"`
			BeforeUpdatedAtMS *int64 `json:"before_updated_at_ms"`
			BeforeTaskID      string `json:"before_task_id"`
		}
		if json.Unmarshal(request.Input, &input) != nil || (input.AgentID == "") == (input.ProjectID == "") || input.BeforeUpdatedAtMS == nil && input.BeforeTaskID != "" || input.BeforeUpdatedAtMS != nil && input.BeforeTaskID == "" {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var agentID kernel.AgentID
		var projectID kernel.ProjectID
		var err error
		if input.AgentID != "" {
			agentID, err = decodeID(input.AgentID, kernel.AgentIDFromBytes)
		} else {
			projectID, err = decodeID(input.ProjectID, kernel.ProjectIDFromBytes)
		}
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var beforeAt kernel.UnixMillis
		var beforeID kernel.TaskID
		if input.BeforeUpdatedAtMS != nil {
			beforeAt, err = kernel.NewUnixMillis(*input.BeforeUpdatedAtMS)
			if err == nil {
				beforeID, err = decodeID(input.BeforeTaskID, kernel.TaskIDFromBytes)
			}
			if err != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
		}
		page, err := daemon.store.ReadTaskList(ctx, agentID, projectID, beforeAt, beforeID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		tasks := make([]api.TaskSummary, 0, len(page.Tasks))
		for _, task := range page.Tasks {
			tasks = append(tasks, api.TaskSummary{ID: task.ID.String(), ProjectID: task.ProjectID.String(), AssignedAgentID: task.AssignedAgentID.String(), IncarnationID: task.IncarnationID.String(), WorkRevision: uint64(task.WorkRevision.Int64()), Title: task.Title, Status: task.Status, Priority: task.Priority, Revision: uint64(task.Revision.Int64())})
		}
		nextCursor := ""
		if page.HasMore && len(page.Tasks) > 0 {
			last := page.Tasks[len(page.Tasks)-1]
			nextCursor = fmt.Sprintf("%d:%s", last.UpdatedAt.Int64(), last.ID.String())
		}
		return api.NewContentReply(struct {
			AgentID    string            `json:"agent_id,omitempty"`
			ProjectID  string            `json:"project_id,omitempty"`
			Head       uint64            `json:"head"`
			Total      uint64            `json:"total"`
			Tasks      []api.TaskSummary `json:"tasks"`
			HasMore    bool              `json:"has_more"`
			NextCursor string            `json:"next_cursor,omitempty"`
		}{input.AgentID, input.ProjectID, uint64(page.Head.Int64()), page.Total, tasks, page.HasMore, nextCursor})
	case "task_history":
		var input struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(request.Input, &input) != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		taskID, err := decodeID(input.TaskID, kernel.TaskIDFromBytes)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		task, found, err := daemon.store.Task(ctx, taskID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if !found {
			return newErrorReply(api.RemoteNotFound)
		}
		history, err := daemon.store.TaskInterventions(ctx, task.ProjectID, taskID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		entries := make([]map[string]any, 0, len(history))
		for _, item := range history {
			body := item.Payload
			if item.Kind == kernel.TaskInterventionReplace && item.SuccessorTaskID != nil {
				body = "Replaced by task " + item.SuccessorTaskID.String()
			}
			entries = append(entries, map[string]any{"operation_id": item.OperationID.String(), "kind": item.Kind.String(), "actor": item.Actor.String(), "body": body, "status": item.State.String(), "created_at_ms": item.CreatedAt.Int64()})
		}
		return api.NewContentReply(map[string]any{"task_id": input.TaskID, "entries": entries})
	default:
		return newErrorReply(api.RemoteInvalidRequest)
	}
}
