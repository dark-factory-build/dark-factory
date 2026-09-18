package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestRepositoryReadinessActionsUseOperatorWithoutRevision(t *testing.T) {
	for _, action := range []string{"fetch", "github"} {
		t.Run(action, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			id := strings.Repeat("ab", 16)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				input, ok := call.ProjectRepositoryInput()
				if !ok || input.Action != action || input.ID != id || input.ExpectedRevision != 0 {
					t.Errorf("readiness action: %+v", input)
				}
				return api.NewContentReply(api.ProjectRepository{ID: id, FetchState: "ready", PublicationState: "unbound"})
			})
			var out, errout bytes.Buffer
			code := run(context.Background(), []string{"project", "repository", action, "--id", id}, webEnvironment(fixture), &out, &errout)
			result := awaitServer(t, done)
			if result.err != nil || code != 0 || !strings.Contains(out.String(), `"fetch_state":"ready"`) {
				t.Fatalf("code=%d err=%v stderr=%s stdout=%s", code, result.err, errout.String(), out.String())
			}
			if _, _, valid := parseOperator([]string{"project", "repository", action, "--id", id, "--revision", "1"}); valid {
				t.Fatal("unneeded mutation arguments accepted")
			}
		})
	}
}
