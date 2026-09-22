package api

import (
	"strings"
	"testing"
)

func TestIntakeReviewClosedPublicationVocabulary(t *testing.T) {
	base := IntakeInput{Action: "review", SourceID: strings.Repeat("a", 32), ProjectID: strings.Repeat("b", 32), Configuration: &IntakeConfiguration{Repository: "team/source", TargetRepositoryID: strings.Repeat("c", 32)}, Legacy: &LegacyIntakeInput{ConfigHash: strings.Repeat("d", 64), JournalHash: strings.Repeat("e", 64), PlanHash: strings.Repeat("f", 64)}}
	for _, tool := range []string{"configuration", "maintainer_status", "list_pull_requests", "observe_operation", "submit_pull_request_review"} {
		input := base
		value := IntakeReviewInput{Tool: tool}
		if tool == "list_pull_requests" {
			value.Page = 1
		}
		if tool == "observe_operation" || tool == "submit_pull_request_review" {
			value.OperationID = "12345678-1234-1234-1234-123456789abc"
		}
		if tool == "submit_pull_request_review" {
			value.PullNumber = 7
			value.HeadSHA = strings.Repeat("a", 40)
			value.Event = "ALLOW"
			value.Body = "reviewed"
			input.IssueNumber = 9
		}
		input.Review = &value
		if !ValidIntakeInput(input) {
			t.Fatalf("valid %s refused", tool)
		}
		value.ReviewID = 99
		if ValidIntakeInput(input) {
			t.Fatalf("unrelated argument accepted for %s", tool)
		}
	}
	base.Review = &IntakeReviewInput{Tool: "http_proxy"}
	if ValidIntakeInput(base) {
		t.Fatal("arbitrary proxy allowed")
	}
	base.Action = "list"
	if ValidIntakeInput(base) {
		t.Fatal("review object accepted outside fixed action")
	}
}

func TestReviewRequestIsClosedAndExactHeadBound(t *testing.T) {
	input := IntakeInput{Action: "review_pr", ProjectID: strings.Repeat("a", 32), ReviewRequest: &ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Body: "body", Provider: "codex"}}
	if !ValidIntakeInput(input) {
		t.Fatal("valid review request rejected")
	}
	input.ReviewRequest.Provider = "shell"
	if ValidIntakeInput(input) {
		t.Fatal("unknown provider accepted")
	}
}
