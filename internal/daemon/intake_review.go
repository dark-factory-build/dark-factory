package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// The controller uses the operator domain, never an attempt's authority. The
// publication destination is its frozen migration route, not today's default.
func (daemon *Daemon) intakeReview(ctx context.Context, input api.IntakeInput) api.IntakeResult {
	if daemon.github == nil {
		return intakeFailure(maintainer.ErrUnavailable)
	}
	sourceID, _ := browserID(input.SourceID, kernel.IntakeSourceIDFromBytes)
	receipt, found, err := daemon.store.LegacyIntakeMigration(ctx, sourceID)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || hex.EncodeToString(receipt.PlanHash[:]) != input.Legacy.PlanHash || hex.EncodeToString(receipt.ConfigHash[:]) != input.Legacy.ConfigHash || hex.EncodeToString(receipt.JournalHash[:]) != input.Legacy.JournalHash {
		return api.IntakeResult{State: "conflict"}
	}
	project, _ := browserID(input.ProjectID, kernel.ProjectIDFromBytes)
	target, _ := browserID(input.Configuration.TargetRepositoryID, kernel.RepositoryIDFromBytes)
	repository, found, err := daemon.store.ProjectRepository(ctx, target)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || repository.ProjectID != project {
		return api.IntakeResult{State: "denied"}
	}
	identity, found, err := daemon.store.RepositorySourceIdentity(ctx, target)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || identity.PublicationRepository == "" {
		return api.IntakeResult{State: "repository_unbound"}
	}
	numericID, found, err := daemon.store.RepositoryGitHubID(ctx, target)
	if err != nil {
		return intakeFailure(err)
	}
	if !found {
		return api.IntakeResult{State: "repository_unbound"}
	}
	if strings.EqualFold(identity.PublicationRepository, input.Configuration.Repository) && numericID != receipt.RepositoryID {
		return api.IntakeResult{State: "denied"}
	}
	grants := map[string]uint64{identity.PublicationRepository: numericID, input.Configuration.Repository: receipt.RepositoryID}
	result := api.IntakeResult{State: "ok", Review: &api.IntakeReviewResult{Repository: identity.PublicationRepository, RepositoryID: numericID}}
	if input.Review.Tool == "configuration" {
		if err := daemon.github.AuthorizeRepositories(ctx, grants); err != nil {
			return intakeFailure(err)
		}
		return result
	}
	// Publication writes require live lineage, including a withdrawal recheck.
	// Old terminal imported tasks remain historical authority after cutover.
	if input.Review.Tool == "submit_pull_request_review" || input.Review.Tool == "enqueue_pull_request" {
		if input.IssueNumber == 0 {
			return api.IntakeResult{State: "denied"}
		}
		lineage := daemon.legacyIntakeLineage(ctx, input)
		if lineage.State != "imported" {
			if lineage.State != "not_found" {
				return lineage
			}
			page, err := daemon.readIntakeIssues(ctx, input.Configuration.Repository, receipt.RepositoryID, 1, "", input.IssueNumber)
			if err != nil {
				return intakeFailure(err)
			}
			if page.RepositoryID != receipt.RepositoryID || len(page.Issues) != 1 || page.Issues[0].Number != input.IssueNumber {
				return api.IntakeResult{State: "denied"}
			}
			source := kernel.IntakeSource{ProjectID: project, TargetRepositoryID: target, GitHubRepositoryID: receipt.RepositoryID}
			old, found, err := daemon.store.LegacyIntakeSuppression(ctx, source, intakeSnapshot(source, page.Issues[0]))
			if err != nil {
				return intakeFailure(err)
			}
			if !found || old.TaskID == (kernel.TaskID{}) {
				return api.IntakeResult{State: "denied"}
			}
		}
	}
	encoded, _ := json.Marshal(input.Review)
	arguments := map[string]any{}
	if json.Unmarshal(encoded, &arguments) != nil {
		return api.IntakeResult{State: "invalid"}
	}
	delete(arguments, "tool")
	arguments["repository"] = identity.PublicationRepository
	if input.Review.Tool == "list_pull_requests" {
		arguments["per_page"] = 2
	}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": input.Review.Tool, "arguments": arguments}})
	response, err := daemon.github.MCP(ctx, request, grants)
	if err != nil {
		return intakeFailure(err)
	}
	if !json.Valid(response) {
		return api.IntakeResult{State: "unavailable"}
	}
	result.Review.Response = string(response)
	// ponytail: two PRs per page bounds private body transport. Larger local
	// frames need an explicit protocol change; never truncate a successful read.
	framed, err := json.Marshal(result)
	if err != nil || len(framed) > (1<<20)-1024 {
		return api.IntakeResult{State: "unavailable"}
	}
	return result
}
