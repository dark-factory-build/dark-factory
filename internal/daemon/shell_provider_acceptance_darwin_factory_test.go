//go:build darwin && factory_test

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// TestShellProviderOperatorAndIssueOriginsThroughScheduler keeps the product
// path intact: operator enqueue and intake acceptance/import go through the
// authenticated API, the scheduler owns admission, and shell is only the
// deterministic external-provider boundary.
func TestShellProviderOperatorAndIssueOriginsThroughScheduler(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\n"+supervisorTestExecutable(t)+" --supervisor-attempt-succeed scheduled\n")
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	connectionID := hex.EncodeToString(digest[:])
	if err := fixture.apiHome.WriteMaintainerCredential([]byte(fmt.Sprintf(`{"id":%q,"credential":%q}`, connectionID, secret))); err != nil {
		t.Fatal(err)
	}
	broker := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("fake broker missing credential")
		}
		if strings.HasSuffix(request.URL.Path, "/mcp") {
			_, _ = out.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
			return
		}
		_ = json.NewEncoder(out).Encode(maintainer.Status{
			ConnectionID: connectionID,
			State:        "connected",
			User:         &maintainer.User{ID: 123, Login: "fixture-operator"},
			Repositories: []maintainer.Delegation{{InstallationID: 1, Repository: "fixture/repository", RepositoryID: 7}},
		})
	}))
	defer broker.Close()
	maintainerClient := maintainer.NewClientForFactoryTest(broker.URL, broker.Client().Transport)
	maintainerHost, err := maintainer.OpenHostForFactoryTest(fixture.apiHome, maintainerClient)
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.github = maintainerHost
	if response, callErr := maintainerHost.MCP(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`), map[string]uint64{"fixture/repository": 7}); callErr != nil || len(response) == 0 {
		t.Fatalf("fake broker MCP: %v response=%s", callErr, response)
	}
	factory, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetCapacity(context.Background(), factory.Revision, 2, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	project, found, err := fixture.store.Project(context.Background(), supervisorProjectID(t, 1))
	if err != nil || !found {
		t.Fatalf("fixture project: found=%v err=%v", found, err)
	}
	issueAgent := supervisorAgentID(t, 41)
	if _, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{ID: issueAgent, ProjectID: project.ID, Name: "issue-worker", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 20}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	operator, err := api.NewOperatorClient(fixture.spec.AttemptSocket, filepath.Join(fixture.root, "api-home", "operator.token"))
	if err != nil {
		t.Fatal(err)
	}
	operatorTask := supervisorTaskID(t, 42)
	if _, err := operator.EnqueueTask(context.Background(), api.EnqueueTaskInput{
		ID: operatorTask.String(), ProjectID: project.ID.String(), AssignedAgentID: fixture.agentID.String(),
		IncarnationID: supervisorIncarnationID(t, 43).String(), Title: "operator origin", Body: "set -eu\n" + supervisorTestExecutable(t) + " --supervisor-attempt-succeed operator\n", Priority: 1,
	}); err != nil {
		t.Fatalf("operator enqueue: %v", err)
	}

	sourceID, err := kernel.IntakeSourceIDFromBytes(bytes.Repeat([]byte{0x44}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.store.CreateIntakeSource(context.Background(), kernel.NewIntakeSource{
		ID: sourceID, ProjectID: project.ID, TargetRepositoryID: kernel.RepositoryID(project.ID), OverseerAgentID: issueAgent,
		GitHubRepositoryID: 42, GitHubRepositoryName: "fixture/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 2,
	}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	source, err = fixture.store.SetIntakeSourceEnabled(context.Background(), source.ID, source.Revision, true, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	issue := maintainer.Issue{ID: 42, NodeID: "I_fixture_42", Number: 42, Title: "issue origin", Body: "set -eu\n" + supervisorTestExecutable(t) + " --supervisor-attempt-succeed issue\n", State: "open", URL: "https://github.com/fixture/issues/issues/42"}
	issue.Author.Login, issue.Author.Type = "fixture", "User"
	fixture.daemon.intakeIssues = func(_ context.Context, repository string, repositoryID uint64, _ uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if repository != "fixture/issues" || repositoryID != 42 || number != 42 {
			return maintainer.IssuePage{}, fmt.Errorf("unexpected issue lookup repository=%q id=%d number=%d", repository, repositoryID, number)
		}
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{issue}}, nil
	}
	preview, err := operator.Intake(context.Background(), api.IntakeInput{Action: "preview", SourceID: source.ID.String(), Page: 1})
	if err != nil || preview.State != "ok" || len(preview.Candidates) != 1 {
		t.Fatalf("issue preview: %+v %v", preview, err)
	}
	snapshot := intakeSnapshot(source, issue)
	hash := snapshot.ContentHash()
	receipt, err := operator.Intake(context.Background(), api.IntakeInput{Action: "accept", SourceID: source.ID.String(), ExpectedRevision: uint64(source.Revision.Int64()), IssueNumber: issue.Number, ContentHash: hex.EncodeToString(hash[:])})
	if err != nil || receipt.State != "accepted" {
		t.Fatalf("issue accept: %+v %v", receipt, err)
	}
	imported, err := operator.Intake(context.Background(), api.IntakeInput{Action: "import", AcceptanceID: receipt.AcceptanceID})
	if err != nil || imported.State != "imported" || imported.TaskID == "" {
		t.Fatalf("issue import: %+v %v", imported, err)
	}
	if err := fixture.daemon.Close(); err != nil {
		t.Fatalf("daemon before restart: %v", err)
	}
	restarted, err := NewDaemon(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	restarted.github = maintainerHost
	fixture.daemon = restarted

	polls := make(chan time.Time, 32)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	spec := fixture.spec
	spec.schedulerPoll = polls
	go func() { done <- fixture.daemon.RunScheduler(ctx, spec) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		polls <- time.Now()
		operatorState, operatorFound, operatorErr := fixture.store.Task(context.Background(), operatorTask)
		issueID, parseErr := parseTaskID(imported.TaskID)
		if parseErr != nil {
			t.Fatalf("imported task id: %v", parseErr)
		}
		issueState, issueFound, issueErr := fixture.store.Task(context.Background(), issueID)
		if operatorErr != nil || issueErr != nil {
			t.Fatalf("scheduled task read: operator=%v issue=%v", operatorErr, issueErr)
		}
		if operatorFound && issueFound && operatorState.Status == kernel.TaskSucceeded && issueState.Status == kernel.TaskSucceeded {
			cancel()
			if schedulerErr := <-done; schedulerErr != nil && !errors.Is(schedulerErr, context.Canceled) {
				t.Fatalf("scheduler shutdown: %v", schedulerErr)
			}
			for _, id := range []kernel.TaskID{operatorTask, issueID} {
				task, taskFound, taskErr := fixture.store.Task(context.Background(), id)
				if taskErr != nil || !taskFound {
					t.Fatalf("task %s after scheduler: %+v found=%v err=%v", id, task, taskFound, taskErr)
				}
				run, runFound, runErr := fixture.store.LatestTaskRun(context.Background(), id, task.IncarnationID)
				if runErr != nil || !runFound || run.ChangeID == nil {
					t.Fatalf("task %s run/change: %+v found=%v err=%v", id, run, runFound, runErr)
				}
				if body, readErr := os.ReadFile(filepath.Join(fixture.changeParent, run.ChangeID.String(), "payload.txt")); readErr != nil || string(body) != "exact source\n" {
					t.Fatalf("task %s source worktree: %q %v", id, body, readErr)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("scheduler did not complete operator=%+v issue=%+v", operatorTask, imported)
}
