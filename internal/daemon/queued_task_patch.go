package daemon

import (
	"context"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
)

// prepareQueuedTaskPatch checks the effective task text only when an edit can
// change it or its provider. Priority and cancellation remain possible for a
// legacy queued body a current provider would refuse.
func prepareQueuedTaskPatch(ctx context.Context, store *kernel.Store, id kernel.TaskID, expected kernel.Revision, patch kernel.TaskPatch) error {
	if patch.Title == nil && patch.Body == nil && patch.AssignedAgentID == nil {
		return nil
	}
	task, found, err := store.Task(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return kernel.ErrNotFound
	}
	if task.Revision != expected {
		return kernel.ErrRevisionConflict
	}
	if task.Status != kernel.TaskQueued {
		return kernel.ErrConflict
	}
	if patch.Title != nil {
		task.Title = *patch.Title
	}
	if patch.Body != nil {
		task.Body, task.SentBackInstructionBytes = kernel.TaskBodyWithInstruction(task, *patch.Body)
	}
	if patch.AssignedAgentID != nil {
		task.AssignedAgentID = *patch.AssignedAgentID
	}
	agent, found, err := store.Agent(ctx, task.AssignedAgentID)
	if err != nil {
		return err
	}
	if !found || agent.ProjectID != task.ProjectID {
		return kernel.ErrConflict
	}
	return prepareTaskText(agent.Provider, task.Title, task.Body)
}

// Existing IDs and unauthorized targets go straight to the kernel's canonical
// replay and authority checks. Provider preparation applies only to new work.
func prepareTaskEnqueue(ctx context.Context, store *kernel.Store, task kernel.NewTask, workerOnly bool) error {
	if _, found, err := store.Task(ctx, task.ID); err != nil || found {
		return err
	}
	agent, found, err := store.Agent(ctx, task.AssignedAgentID)
	if err != nil {
		return err
	}
	if !found || agent.ProjectID != task.ProjectID || (workerOnly && agent.Role != kernel.RoleWorker) {
		return nil
	}
	return prepareTaskText(agent.Provider, task.Title, task.Body)
}

func prepareTaskText(kind kernel.Provider, title, body string) error {
	effectiveBody := body
	if effectiveBody == "" && kind != kernel.ProviderShell {
		effectiveBody = title
	}
	if _, _, err := provider.PrepareTask(kind, []byte(effectiveBody)); err != nil {
		return fmt.Errorf("%w: task does not fit provider", kernel.ErrInvalidValue)
	}
	return nil
}
