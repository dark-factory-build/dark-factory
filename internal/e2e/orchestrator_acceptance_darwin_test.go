//go:build darwin

package e2e_test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBlackBoxOrchestratorAcceptance is the repeatable product acceptance
// scenario for orchestrated work (#38) on the deterministic shell provider,
// against a throwaway home, socket and factoryd: an orchestrator delegates
// two workers, one completes and one blocks, delegation stays inside the
// orchestrator's project and role, factoryd is killed and rebooted between
// fan-out and fan-in, and the orchestrator fans the durable results into a
// PR proposal. A failed wait names the task, its status and its bounded
// durable outcome; the factoryd stderr tail is logged on any failure.
func TestBlackBoxOrchestratorAcceptance(t *testing.T) {
	if os.Getenv("DARK_FACTORY_DAEMON_E2E") != "1" {
		t.Skip("run through scripts/go-e2e.sh daemon")
	}
	fixture := newBlackBoxFixture(t)
	foreignRepo := filepath.Join(fixture.root, "foreign")
	initRepository(t, foreignRepo)

	daemonA, outputA := fixture.startFactoryd(t)
	client := fixture.waitClient(t, outputA.String)
	projectID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "project", "create", "--name", "acceptance", "--root", fixture.repo))
	foreignProject := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "project", "create", "--name", "foreign", "--root", foreignRepo))
	agent := func(project, name, role string) string {
		return fixture.operatorID(t, fixture.runFactoryctl(t, 0, "agent", "create", "--project", project, "--name", name, "--provider", "shell", "--tool-budget", "4", "--role", role))
	}
	orchestrator := agent(projectID, "orchestrator", "orchestrator")
	workerA, workerB := agent(projectID, "worker-a", "worker"), agent(projectID, "worker-b", "worker")
	foreignWorker := agent(foreignProject, "foreign", "worker")

	// Fan-out. Worker A proves a worker holds no overseer authority before it
	// completes; worker B blocks. The orchestrator cannot delegate outside its
	// project.
	taskA, taskB := randomID(t), randomID(t)
	fanOut := fmt.Sprintf(`set -eu
f="$DARK_FACTORY_FACTORYCTL"
"$f" overseer task add --agent %s --title worker-a --body 'if "$DARK_FACTORY_FACTORYCTL" overseer status; then exit 1; fi; "$DARK_FACTORY_FACTORYCTL" attempt succeed --result worker-a' --task-id %s --incarnation-id %s
"$f" overseer task add --agent %s --title worker-b --body '"$DARK_FACTORY_FACTORYCTL" attempt block --detail needs-operator' --task-id %s --incarnation-id %s
if "$f" overseer task add --agent %s --title trespass --body 'exit 1'; then exit 1; fi
"$f" attempt succeed --result delegated
`, workerA, taskA, randomID(t), workerB, taskB, randomID(t), foreignWorker)
	delegate := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", projectID, "--agent", orchestrator, "--title", "delegate", "--body", fanOut))
	fixture.runFactoryctl(t, 0, "dispatch", "on")
	fixture.awaitTaskStatus(t, client, delegate, "succeeded", 90*time.Second)
	fixture.awaitTaskStatus(t, client, taskA, "succeeded", 90*time.Second)
	fixture.awaitTaskStatus(t, client, taskB, "blocked", 90*time.Second)
	if outcome := fixture.taskOutcome(t, client, taskB); outcome != "needs-operator" {
		t.Fatalf("blocked worker outcome = %q", outcome)
	}

	// Restart at the fan-in boundary: the delegated outcomes are durable.
	fixture.sigkill(t, daemonA)
	_, outputB := fixture.startFactoryd(t)
	client = fixture.waitClient(t, outputB.String)

	// Fan-in from the orchestrator's own project-scoped view.
	fanIn := fmt.Sprintf(`set -eu
f="$DARK_FACTORY_FACTORYCTL"
a=$("$f" overseer status --task %s | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')
b=$("$f" overseer status --task %s | sed -n 's/.*"blocked_reason":"\([^"]*\)".*/\1/p')
test "$a" = worker-a
test "$b" = needs-operator
"$f" attempt succeed --result "PR proposal: $a; worker-b blocked: $b"
`, taskA, taskB)
	proposal := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", projectID, "--agent", orchestrator, "--title", "fan in and propose", "--body", fanIn))
	fixture.awaitTaskStatus(t, client, proposal, "succeeded", 90*time.Second)
	if outcome := fixture.taskOutcome(t, client, proposal); outcome != "PR proposal: worker-a; worker-b blocked: needs-operator" {
		t.Fatalf("PR proposal = %q", outcome)
	}
}

func randomID(t *testing.T) string {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(id)
}
