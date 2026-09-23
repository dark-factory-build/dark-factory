//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestReviewRetryCLILeavesDefaultProviderOutOfRequest(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	project := strings.Repeat("a", 32)
	operation := "12345678-1234-1234-1234-123456789abc"
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.IntakeInput()
		if !ok || input.Action != "review_pr" || input.ProjectID != project || input.ReviewRequest == nil || *input.ReviewRequest != (api.ReviewRequest{RetryOperation: operation}) || !api.ValidIntakeInput(input) {
			t.Errorf("retry review request = %+v, valid=%t", input, ok && api.ValidIntakeInput(input))
		}
		return api.NewContentReply(api.IntakeResult{State: "ok", ReviewOperation: operation})
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"review", "--project", project, "--retry-operation", operation}, webEnvironment(fixture), &stdout, &stderr)
	result := awaitServer(t, done)
	if exit != 0 || result.err != nil || stderr.Len() != 0 || strings.TrimSpace(stdout.String()) != operation {
		t.Fatalf("retry review CLI = exit %d server %v stdout=%q stderr=%q", exit, result.err, stdout.String(), stderr.String())
	}
}
