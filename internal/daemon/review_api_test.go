package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestReviewPRUsesDaemonOperationAndReturnsOperationID(t *testing.T) {
	want := "11111111-1111-4111-8111-111111111111"
	daemon := &Daemon{reviewOperation: func(context.Context, kernel.ProjectID, api.ReviewRequest) (string, error) { return want, nil }, now: time.Now}
	got, err := daemon.reviewPR(context.Background(), kernel.ProjectID{}, api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: "a" + "000000000000000000000000000000000000000", Base: "b" + "000000000000000000000000000000000000000", Body: "body", Provider: "codex"})
	if err != nil || got != want {
		t.Fatalf("review operation=%q err=%v", got, err)
	}
}

func TestReviewRetryIsReachableOnThePublicIntakePath(t *testing.T) {
	want := "22222222-2222-4222-8222-222222222222"
	called := false
	daemon := &Daemon{reviewOperation: func(_ context.Context, _ kernel.ProjectID, request api.ReviewRequest) (string, error) {
		called = request.RetryOperation != ""
		return want, nil
	}, now: time.Now}
	result := daemon.Intake(context.Background(), api.IntakeInput{Action: "review_pr", ProjectID: strings.Repeat("a", 32), ReviewRequest: &api.ReviewRequest{RetryOperation: "11111111-1111-4111-8111-111111111111"}})
	if result.State != "ok" || result.ReviewOperation != want || !called {
		t.Fatalf("retry result=%+v called=%v", result, called)
	}
}
