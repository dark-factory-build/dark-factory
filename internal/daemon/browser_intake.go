package daemon

import (
	"context"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (backend *browserBackend) Intake(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, request browserprotocol.Intake) (browserprotocol.IntakeResult, error) {
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
	if err != nil {
		return browserprotocol.IntakeResult{}, err
	}
	// A preview may wait on the backlog connection. It has no local effect, so let state reads
	// and revocation proceed while it is in flight. A new preview still needs
	// fresh authorization; writes retain the gate through their effect.
	readOnly := request.Action == "list" || request.Action == "preview" || request.Action == "refresh" || request.Action == "linear_teams"
	if readOnly {
		release()
	} else {
		defer release()
	}
	if backend.owner == nil {
		return browserprotocol.IntakeResult{}, fmt.Errorf("intake is unavailable")
	}
	result := backend.owner.Intake(ctx, browserIntakeInput(request))
	if readOnly {
		_, release, _, err = backend.authorize(ctx, rawClient, kernel.BrowserCapabilityAdministration)
		if err != nil {
			return browserprotocol.IntakeResult{}, err
		}
		release()
	}
	return browserIntakeResult(result), nil
}

func browserIntakeInput(value browserprotocol.Intake) api.IntakeInput {
	input := api.IntakeInput{APIKey: value.APIKey, Action: value.Action, SourceID: value.SourceID, ProjectID: value.ProjectID, ExpectedRevision: uint64(value.ExpectedRevision), ReviewedRevision: uint64(value.ReviewedRevision), Page: value.Page, IssueNumber: uint64(value.IssueNumber), ContentHash: value.ContentHash, AcceptanceID: value.AcceptanceID}
	if value.Configuration != nil {
		input.Configuration = &api.IntakeConfiguration{LinearTeamID: value.Configuration.LinearTeamID, PriorityDefault: value.Configuration.PriorityDefault, PriorityByLabel: value.Configuration.PriorityByLabel, Repository: value.Configuration.Repository, TargetRepositoryID: value.Configuration.TargetRepositoryID, OverseerAgentID: value.Configuration.OverseerAgentID, Label: value.Configuration.Label, Policy: value.Configuration.Policy, TrustedAuthors: append([]string(nil), value.Configuration.TrustedAuthors...), PollSeconds: value.Configuration.PollSeconds, AdmissionLimit: value.Configuration.AdmissionLimit}
	}
	return input
}

func browserIntakeResult(value api.IntakeResult) browserprotocol.IntakeResult {
	result := browserprotocol.IntakeResult{SourceID: value.SourceID, State: value.State, ImportedTasks: append([]string{}, value.ImportedTasks...), ReviewedRevision: browserprotocol.Decimal(value.ReviewedRevision), AcceptanceID: value.AcceptanceID, TaskID: value.TaskID}
	for _, team := range value.LinearTeams {
		result.LinearTeams = append(result.LinearTeams, browserprotocol.IntakeTeam{ID: team.ID, Name: team.Name, Key: team.Key})
	}
	if value.NextPage != nil {
		next := *value.NextPage
		result.NextPage = &next
	}
	if value.Sources != nil {
		result.Sources = make([]browserprotocol.IntakeSource, 0, len(value.Sources))
		for _, source := range value.Sources {
			view := browserprotocol.IntakeSource{LinearTeamID: source.LinearTeamID, PriorityDefault: source.PriorityDefault, PriorityByLabel: source.PriorityByLabel, Repository: source.Repository, TargetRepositoryID: source.TargetRepositoryID, OverseerAgentID: source.OverseerAgentID, Label: source.Label, Policy: source.Policy, TrustedAuthors: append([]string(nil), source.TrustedAuthors...), PollSeconds: source.PollSeconds, AdmissionLimit: source.AdmissionLimit, ID: source.ID, ProjectID: source.ProjectID, GitHubRepositoryID: browserprotocol.Decimal(source.GitHubRepositoryID), Enabled: browserprotocol.Bool(source.Enabled), Revision: browserprotocol.Decimal(source.Revision)}
			if source.Sync != nil {
				view.Sync = &browserprotocol.IntakeSync{LastAttemptAt: browserprotocol.Decimal(source.Sync.LastAttemptAt), LastSuccessAt: browserprotocol.Decimal(source.Sync.LastSuccessAt), ImportedTasks: source.Sync.ImportedTasks, State: source.Sync.State, Error: source.Sync.Error}
			}
			result.Sources = append(result.Sources, view)
		}
	}
	if value.Candidates != nil {
		result.Candidates = make([]browserprotocol.IntakeCandidate, 0, len(value.Candidates))
		for _, candidate := range value.Candidates {
			result.Candidates = append(result.Candidates, browserprotocol.IntakeCandidate{Number: browserprotocol.Decimal(candidate.Number), URL: candidate.URL, Title: candidate.Title, Body: candidate.Body, Author: candidate.Author, Labels: append([]string(nil), candidate.Labels...), ContentHash: candidate.ContentHash, Reason: candidate.Reason, AcceptanceID: candidate.AcceptanceID, TaskID: candidate.TaskID, Truncated: browserprotocol.Bool(candidate.Truncated)})
		}
	}
	return result
}
