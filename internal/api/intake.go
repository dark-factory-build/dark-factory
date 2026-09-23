//go:build darwin || linux

package api

import (
	"context"
	"encoding/hex"
	"strings"
)

// Intake uses the same private operator actions for CLI, console and the
// installed controller. Candidates cannot supply their own author or content.
type IntakeConfiguration struct {
	LinearTeamID       string           `json:"linear_team_id,omitempty"`
	PriorityDefault    int64            `json:"priority_default,omitempty"`
	PriorityByLabel    map[string]int64 `json:"priority_by_label,omitempty"`
	Repository         string           `json:"repository"`
	TargetRepositoryID string           `json:"target_repository_id"`
	OverseerAgentID    string           `json:"overseer_agent_id"`
	Label              string           `json:"label"`
	Policy             string           `json:"policy"`
	TrustedAuthors     []string         `json:"trusted_authors"`
	PollSeconds        uint32           `json:"poll_seconds"`
	AdmissionLimit     uint16           `json:"admission_limit"`
}
type IntakeInput struct {
	APIKey           string               `json:"api_key,omitempty"`
	Review           *IntakeReviewInput   `json:"review,omitempty"`
	ReviewRequest    *ReviewRequest       `json:"review_request,omitempty"`
	Legacy           *LegacyIntakeInput   `json:"legacy,omitempty"`
	AcceptanceCursor string               `json:"acceptance_cursor,omitempty"`
	Action           string               `json:"action"`
	SourceID         string               `json:"source_id,omitempty"`
	ProjectID        string               `json:"project_id,omitempty"`
	Configuration    *IntakeConfiguration `json:"configuration,omitempty"`
	ExpectedRevision uint64               `json:"expected_revision,omitempty"`
	ReviewedRevision uint64               `json:"reviewed_revision,omitempty"`
	Page             uint32               `json:"page,omitempty"`
	IssueNumber      uint64               `json:"issue_number,omitempty"`
	ContentHash      string               `json:"content_hash,omitempty"`
	AcceptanceID     string               `json:"acceptance_id,omitempty"`
}

type ReviewRequest struct {
	Repository     string `json:"repository"`
	PullNumber     uint64 `json:"pull_number"`
	Head           string `json:"head"`
	Base           string `json:"base"`
	BaseRef        string `json:"base_ref"`
	Body           string `json:"body"`
	Provider       string `json:"provider"`
	RetryOperation string `json:"retry_operation,omitempty"`
}
type IntakeSync struct {
	LastAttemptAt int64  `json:"last_attempt_at"`
	LastSuccessAt int64  `json:"last_success_at"`
	ImportedTasks uint16 `json:"imported_tasks"`
	State         string `json:"state"`
	Error         string `json:"error"`
}
type IntakeSource struct {
	LinearTeamID    string           `json:"linear_team_id,omitempty"`
	PriorityDefault int64            `json:"priority_default,omitempty"`
	PriorityByLabel map[string]int64 `json:"priority_by_label,omitempty"`
	Sync            *IntakeSync      `json:"sync,omitempty"`

	Repository         string   `json:"repository"`
	TargetRepositoryID string   `json:"target_repository_id"`
	OverseerAgentID    string   `json:"overseer_agent_id"`
	Label              string   `json:"label"`
	Policy             string   `json:"policy"`
	TrustedAuthors     []string `json:"trusted_authors"`
	PollSeconds        uint32   `json:"poll_seconds"`
	AdmissionLimit     uint16   `json:"admission_limit"`
	ID                 string   `json:"id"`
	ProjectID          string   `json:"project_id"`
	GitHubRepositoryID uint64   `json:"github_repository_id"`
	Enabled            bool     `json:"enabled"`
	Revision           uint64   `json:"revision"`
}
type IntakeCandidate struct {
	Number       uint64   `json:"number"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Author       string   `json:"author"`
	Labels       []string `json:"labels"`
	ContentHash  string   `json:"content_hash"`
	Reason       string   `json:"reason"`
	AcceptanceID string   `json:"acceptance_id,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}
type IntakeTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type IntakeResult struct {
	SourceID           string              `json:"source_id,omitempty"`
	LinearTeams        []IntakeTeam        `json:"linear_teams,omitempty"`
	Review             *IntakeReviewResult `json:"review,omitempty"`
	ReviewOperation    string              `json:"review_operation,omitempty"`
	Legacy             *LegacyIntakePlan   `json:"legacy,omitempty"`
	AcceptanceProgress bool                `json:"acceptance_progress,omitempty"`
	AcceptanceCursor   string              `json:"acceptance_cursor,omitempty"`
	State              string              `json:"state"`
	ImportedTasks      []string            `json:"imported_tasks,omitempty"`
	Sources            []IntakeSource      `json:"sources,omitempty"`
	Candidates         []IntakeCandidate   `json:"candidates,omitempty"`
	NextPage           *uint32             `json:"next_page,omitempty"`
	ReviewedRevision   uint64              `json:"reviewed_revision,omitempty"`
	AcceptanceID       string              `json:"acceptance_id,omitempty"`
	TaskID             string              `json:"task_id,omitempty"`
}

func ValidIntakeInput(input IntakeInput) bool {
	if input.Page > 1000 || input.IssueNumber > 9007199254740991 {
		return false
	}
	allowed := IntakeInput{Action: input.Action}
	valid := false
	switch input.Action {
	case "linear_connect":
		allowed.APIKey = input.APIKey
		valid = validText(input.APIKey, 10, 512)
	case "linear_disconnect", "linear_teams":
		valid = true
	case "review_pr":
		allowed.ProjectID, allowed.ReviewRequest = input.ProjectID, input.ReviewRequest
		valid = validID(input.ProjectID) && input.ReviewRequest != nil && validReviewRequest(*input.ReviewRequest)
	case "legacy_preview", "legacy_commit", "legacy_lineage", "review":
		allowed.SourceID, allowed.ProjectID, allowed.Configuration, allowed.Legacy = input.SourceID, input.ProjectID, input.Configuration, input.Legacy
		valid = validID(input.SourceID) && validID(input.ProjectID) && input.Configuration != nil && input.Legacy != nil && validLegacyIntakeInput(*input.Legacy, input.Action == "legacy_commit")
		if input.Action == "review" {
			allowed.Review = input.Review
			if input.Review != nil && (input.Review.Tool == "submit_pull_request_review" || input.Review.Tool == "enqueue_pull_request") {
				allowed.IssueNumber = input.IssueNumber
				valid = valid && input.IssueNumber > 0
			}
			valid = valid && input.Review != nil && validIntakeReview(*input.Review) && validLegacyDigest(input.Legacy.PlanHash) && validID(input.Configuration.TargetRepositoryID)
		}
		if input.Action == "legacy_lineage" {
			allowed.IssueNumber = input.IssueNumber
			valid = valid && input.IssueNumber > 0 && validLegacyDigest(input.Legacy.PlanHash) && validID(input.Configuration.TargetRepositoryID)
		}
	case "list":
		allowed.ProjectID = input.ProjectID
		valid = input.ProjectID == "" || validID(input.ProjectID)
	case "create", "update":
		allowed.SourceID, allowed.ProjectID, allowed.Configuration = input.SourceID, input.ProjectID, input.Configuration
		valid = validID(input.SourceID) && validID(input.ProjectID) && input.Configuration != nil
		if input.Action == "update" {
			allowed.ExpectedRevision = input.ExpectedRevision
			valid = valid && input.ExpectedRevision > 0
		}
	case "preview", "refresh", "tick":
		allowed.SourceID, allowed.Page = input.SourceID, input.Page
		valid = validID(input.SourceID) && input.Page > 0
		if input.Action == "tick" {
			allowed.AcceptanceCursor = input.AcceptanceCursor
			valid = valid && (input.AcceptanceCursor == "" || validID(input.AcceptanceCursor))
		}
	case "enable", "pause":
		allowed.SourceID, allowed.ExpectedRevision = input.SourceID, input.ExpectedRevision
		valid = validID(input.SourceID) && input.ExpectedRevision > 0
		if input.Action == "enable" {
			allowed.ReviewedRevision = input.ReviewedRevision
			valid = valid && input.ReviewedRevision == input.ExpectedRevision
		}
	case "accept":
		allowed.SourceID, allowed.ExpectedRevision = input.SourceID, input.ExpectedRevision
		allowed.IssueNumber, allowed.ContentHash = input.IssueNumber, input.ContentHash
		hash, err := hex.DecodeString(input.ContentHash)
		valid = validID(input.SourceID) && input.ExpectedRevision > 0 && input.IssueNumber > 0 && err == nil && len(hash) == 32 && hex.EncodeToString(hash) == input.ContentHash
	case "withdraw", "import":
		allowed.AcceptanceID = input.AcceptanceID
		valid = validID(input.AcceptanceID)
	}
	return valid && allowed == input
}

func validReviewRequest(value ReviewRequest) bool {
	if value.RetryOperation != "" {
		return reviewUUID(value.RetryOperation) && value.Repository == "" && value.PullNumber == 0 && value.Head == "" && value.Base == "" && value.BaseRef == "" && value.Body == "" && value.Provider == ""
	}
	return validText(value.Repository, 3, 140) && value.PullNumber > 0 && validHex(value.Head, 20) && validHex(value.Base, 20) && validText(value.BaseRef, 1, 240) && !strings.ContainsAny(value.BaseRef, "\x00\r\n") && validText(value.Body, 1, 65536) && (value.Provider == "codex" || value.Provider == "claude")
}

func (client *OperatorClient) Intake(ctx context.Context, input IntakeInput) (IntakeResult, error) {
	if !ValidIntakeInput(input) {
		return IntakeResult{}, ErrInvalidInput
	}
	var result IntakeResult
	if err := client.client.call(ctx, "intake", input, &result); err != nil {
		return IntakeResult{}, err
	}
	if result.Review != nil && result.Review.Response != "" && validateJSON([]byte(result.Review.Response), false) != nil {
		return IntakeResult{}, ErrProtocol
	}
	return result, nil
}
