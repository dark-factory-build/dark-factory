package daemon

import (
	"context"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const intakeEditedSinceAcceptance = "\n\n# Issue edited since acceptance: re-accept to apply."

func acceptedIssueInstruction(accepted kernel.IntakeAcceptance) string {
	instruction := accepted.Snapshot.Body
	if strings.TrimSpace(instruction) == "" {
		instruction = accepted.Snapshot.Title
	}
	return instruction + intakeEditedSinceAcceptance
}

func (daemon *Daemon) acceptedIssueScope(ctx context.Context, accepted kernel.IntakeAcceptance, issue frozenIssue) (string, string, error) {
	current := accepted.Snapshot
	current.Title, current.Body = issue.Title, issue.Body
	changed := current.Title != accepted.Snapshot.Title || current.BodyHash() != accepted.BodyHash
	sources, err := daemon.store.ProjectIntakeSources(ctx, accepted.ProjectID)
	if err != nil {
		return "", "", err
	}
	for _, source := range sources {
		if source.LinearTeamID != accepted.Snapshot.LinearTeamID ||
			source.GitHubRepositoryID != accepted.Snapshot.GitHubRepositoryID ||
			source.TargetRepositoryID != accepted.RepositoryID ||
			!strings.EqualFold(source.GitHubRepositoryName, accepted.SourceRepository) {
			continue
		}
		if changed {
			if kernel.PreviewIntake(source, current, &accepted) == kernel.IntakeContentChanged && kernel.TrustedIntakeAuthor(source, accepted.Snapshot) {
				return issue.Title, issue.Body, nil
			}
			return accepted.Snapshot.Title, acceptedIssueInstruction(accepted), nil
		}
		break
	}
	if changed {
		return accepted.Snapshot.Title, acceptedIssueInstruction(accepted), nil
	}
	return issue.Title, issue.Body, nil
}
