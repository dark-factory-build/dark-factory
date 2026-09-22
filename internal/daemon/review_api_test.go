package daemon

import (
	"context"
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
