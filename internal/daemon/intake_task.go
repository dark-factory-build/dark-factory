package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// currentTaskText is the shared task projection used by attempt delivery and
// provider launch. Retained feedback stays attached to the task but never
// replaces its accepted issue scope.
func (daemon *Daemon) currentTaskText(ctx context.Context, provider kernel.Provider, task kernel.Task) (string, error) {
	text := kernel.EffectiveTaskText(provider, task.Title, task.Body)
	accepted, found, err := daemon.store.IntakeAcceptanceForTask(ctx, task.ID)
	if err != nil || !found || accepted.Snapshot.LinearTeamID != "" || daemon.github == nil || !daemon.github.CustomerMode() {
		return text, err
	}
	issue, err := daemon.liveAcceptedIssue(ctx, accepted)
	if err != nil {
		return "", err
	}
	title, body, err := daemon.acceptedIssueScope(ctx, accepted, issue)
	if err != nil {
		return "", err
	}
	text, _ = kernel.TaskBodyWithInstruction(task, kernel.IntakeTaskBody(accepted, title, body))
	return text, nil
}
