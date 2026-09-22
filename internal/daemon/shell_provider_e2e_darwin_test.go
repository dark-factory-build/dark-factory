//go:build darwin

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

const shellProviderDiagnosticLimit = 2048

// TestShellProviderDelegateFanInPRProposal exercises the production shell
// provider boundary with only local processes. The proposal is deliberately a
// result marker: publishing is the next production step and would require a
// live GitHub provider.
func TestShellProviderDelegateFanInPRProposal(t *testing.T) {
	workerA, workerB := supervisorAgentID(t, 4), supervisorAgentID(t, 5)
	workerTaskA, workerTaskB := supervisorTaskID(t, 6), supervisorTaskID(t, 7)
	orchestratorTask := supervisorTaskID(t, 8)
	orchestratorID := supervisorAgentID(t, 2)

	program := fmt.Sprintf(`set -eu
"$DARK_FACTORY_FACTORYCTL" overseer task add --agent %s --title worker-a --body 'printf worker-a; "$DARK_FACTORY_FACTORYCTL" attempt succeed --result worker-a' --priority 1 --task-id %s --incarnation-id %s
"$DARK_FACTORY_FACTORYCTL" overseer task add --agent %s --title worker-b --body 'printf worker-b; "$DARK_FACTORY_FACTORYCTL" attempt succeed --result worker-b' --priority 1 --task-id %s --incarnation-id %s
"$DARK_FACTORY_FACTORYCTL" attempt succeed --result delegated
`, workerA, workerTaskA, supervisorIncarnationID(t, 9), workerB, workerTaskB, supervisorIncarnationID(t, 10))
	fixture := newSupervisorRoleFixture(t, program, kernel.RoleOrchestrator)
	buildFactoryctl(t, fixture)

	ctx := context.Background()
	project, found, err := fixture.store.Project(ctx, supervisorProjectID(t, 1))
	if err != nil || !found {
		t.Fatalf("fixture project: found=%v err=%v", found, err)
	}
	for id, name := range map[kernel.AgentID]string{workerA: "worker-a", workerB: "worker-b"} {
		if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: id, ProjectID: project.ID, Name: name, Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 4}, supervisorTime()); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	orchestrator, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if err != nil {
		shellProviderFatal(t, "delegate", orchestrator, err)
	}
	fixture.assertTerminal(t, orchestrator, kernel.OutcomeSucceeded)
	if orchestrator.Proposal == nil || orchestrator.Proposal.Result() != "delegated" {
		shellProviderFatal(t, "delegate result", orchestrator, nil)
	}

	workerRuns := make([]kernel.Run, 0, 2)
	for _, name := range []string{"worker-a", "worker-b"} {
		run, runErr := fixture.daemon.RunNext(ctx, fixture.spec)
		if runErr != nil {
			shellProviderFatal(t, name, run, runErr)
		}
		fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
		if run.Proposal == nil || run.Proposal.Result() != name {
			shellProviderFatal(t, name+" result", run, nil)
		}
		if run.ChangeID == nil {
			shellProviderFatal(t, name+" Change", run, fmt.Errorf("worker completed without a persisted Change"))
		}
		changeState, found, changeErr := fixture.store.Change(ctx, *run.ChangeID)
		if changeErr != nil || !found || changeState.Selection == nil {
			shellProviderFatal(t, name+" persisted Change", run, fmt.Errorf("found=%v err=%v Change=%+v", found, changeErr, changeState))
		}
		changePath := filepath.Join(fixture.changeParent, run.ChangeID.String())
		if body, readErr := os.ReadFile(filepath.Join(changePath, "payload.txt")); readErr != nil || string(body) != "exact source\n" {
			shellProviderFatal(t, name+" source worktree", run, fmt.Errorf("payload=%q err=%v", body, readErr))
		}
		workerRuns = append(workerRuns, run)
	}

	faninBody := fmt.Sprintf(`set -eu
worker_a=$("$DARK_FACTORY_FACTORYCTL" overseer status --task %s | sed -n 's/.*"result":"\([^\"]*\)".*/\1/p')
worker_b=$("$DARK_FACTORY_FACTORYCTL" overseer status --task %s | sed -n 's/.*"result":"\([^\"]*\)".*/\1/p')
test "$worker_a" = worker-a
test "$worker_b" = worker-b
proposal="PR proposal: $worker_a + $worker_b"
"$DARK_FACTORY_FACTORYCTL" attempt succeed --result "$proposal"
`, workerTaskA, workerTaskB)
	if _, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: orchestratorTask, ProjectID: project.ID, AssignedAgentID: orchestratorID, IncarnationID: supervisorIncarnationID(t, 11), Title: "fan-in and propose PR", Body: faninBody, Priority: 1}, supervisorTime()); err != nil {
		t.Fatalf("enqueue fan-in: %v", err)
	}
	final, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if err != nil {
		shellProviderFatal(t, "fan-in", final, err)
	}
	fixture.assertTerminal(t, final, kernel.OutcomeSucceeded)
	if final.Proposal == nil || final.Proposal.Result() != "PR proposal: worker-a + worker-b" {
		shellProviderFatal(t, "PR proposal", final, nil)
	}
	for _, run := range workerRuns {
		fixture.assertReleased(t, run)
	}
	fixture.assertReleased(t, final)
}

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
		_ = json.NewEncoder(out).Encode(maintainer.Status{ConnectionID: connectionID, State: "connected", Repositories: []maintainer.Delegation{{Repository: "fixture/repository", RepositoryID: 7}}})
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

func buildFactoryctl(t *testing.T, fixture *supervisorFixture) {
	t.Helper()
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	target := filepath.Join(fixture.root, "factoryctl")
	cmd := exec.Command(goTool, "build", "-o", target, "./cmd/factoryctl")
	cmd.Dir = repositoryRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build factoryctl: %v\n%s", err, boundedShellDiagnostic(string(output)))
	}
	fixture.spec.FactoryctlExecutable = target
}

func repositoryRoot(t testing.TB) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		directory = filepath.Dir(directory)
	}
	t.Fatal("go.mod not found from test directory")
	return ""
}

func shellProviderFatal(t *testing.T, stage string, run kernel.Run, cause error) {
	t.Helper()
	diagnostic := "<no proposal>"
	if run.Proposal != nil {
		diagnostic = run.Proposal.Result()
		if diagnostic == "" {
			diagnostic = run.Proposal.Detail()
		}
	}
	if cause != nil {
		diagnostic += ": " + cause.Error()
	}
	t.Fatalf("shell-provider %s failed: %s", stage, boundedShellDiagnostic(diagnostic))
}

func boundedShellDiagnostic(value string) string {
	if len(value) <= shellProviderDiagnosticLimit {
		return value
	}
	return value[:shellProviderDiagnosticLimit] + "…"
}
