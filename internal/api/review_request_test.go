package api

import (
	"strings"
	"testing"
)

func TestReviewRequestIsClosedAndExactHeadBound(t *testing.T) {
	input := IntakeInput{Action: "review_pr", ProjectID: strings.Repeat("a", 32), ReviewRequest: &ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Body: "body", Provider: "codex"}}
	if !ValidIntakeInput(input) {
		t.Fatal("valid review request rejected")
	}
	input.ReviewRequest.Provider = "shell"
	if ValidIntakeInput(input) {
		t.Fatal("unknown provider accepted")
	}
}
