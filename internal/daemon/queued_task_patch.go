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
	if task.AssignedAgentID == (kernel.AgentID{}) {
		return prepareSharedTaskText(task.Title, task.Body)
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

// prepareSharedTaskText bounds work any eligible worker may claim: the text
// must fit every native provider, since the claimant is not known yet.
// ponytail: fit-all bound; a per-provider eligibility predicate in admission
// is the upgrade if a project needs shared work only some providers can hold.
func prepareSharedTaskText(title, body string) error {
	for _, kind := range []kernel.Provider{kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		if err := prepareTaskText(kind, title, body); err != nil {
			return err
		}
	}
	return nil
}

// prepareTaskRetry applies the provider-fit check used by ordinary queue
// edits, while leaving terminal-state and authority decisions to the atomic
// kernel mutation.
func prepareTaskRetry(ctx context.Context, store *kernel.Store, id kernel.TaskID, expected kernel.Revision, assigned kernel.AgentID) error {
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
	if assigned == (kernel.AgentID{}) {
		assigned = task.AssignedAgentID
	}
	agent, found, err := store.Agent(ctx, assigned)
	if err != nil {
		return err
	}
	if !found || agent.ProjectID != task.ProjectID || agent.Role != kernel.RoleWorker {
		return kernel.ErrUnauthorized
	}
	return prepareTaskText(agent.Provider, task.Title, task.Body)
}

// Existing IDs and unauthorized targets go straight to the kernel's canonical
// replay and authority checks. Provider preparation applies only to new work.
func prepareTaskEnqueue(ctx context.Context, store *kernel.Store, task kernel.NewTask, workerOnly bool) error {
	if _, found, err := store.Task(ctx, task.ID); err != nil || found {
		return err
	}
	if task.AssignedAgentID == (kernel.AgentID{}) {
		return prepareSharedTaskText(task.Title, task.Body)
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
