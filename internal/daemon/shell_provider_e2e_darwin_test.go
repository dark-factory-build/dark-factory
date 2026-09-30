//go:build darwin

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
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
