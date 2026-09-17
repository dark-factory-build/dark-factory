//go:build darwin

package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

// handoverTicks is how many one-second numbered lines the provider prints
// before it reports its own success: long enough that one whole daemon
// generation ends and the next adopts it mid-sequence.
const handoverTicks = 40

// terminalObservation mirrors the one display document `factoryctl terminal
// observe` prints. It is the only terminal evidence this test takes, and it
// comes through the real local API against the live runner's ring.
type terminalObservation struct {
	RunID      string `json:"run_id"`
	Cursor     uint64 `json:"cursor"`
	NextCursor uint64 `json:"next_cursor"`
	Floor      uint64 `json:"floor"`
	Head       uint64 `json:"head"`
	Source     string `json:"source"`
	Gap        bool   `json:"gap"`
	Omitted    uint64 `json:"omitted"`
	Payload    string `json:"payload"`
}

// TestBlackBoxDaemonHandoverReplacesFactorydUnderALiveProvider is the seamless
// replacement proof: one daemon generation ends with SIGTERM while a provider
// is mid-task, the next adopts the same runner, and the task the first daemon
// admitted reports its own success to the second. Nothing about the provider
// restarts: not its process, not its PTY, not its output.
func TestBlackBoxDaemonHandoverReplacesFactorydUnderALiveProvider(t *testing.T) {
	if os.Getenv("DARK_FACTORY_DAEMON_E2E") != "1" {
		t.Skip("run through scripts/go-daemon-e2e.sh")
	}
	fixture := newBlackBoxFixture(t)

	daemonA, outputA := fixture.startFactoryd(t)
	client := fixture.waitClient(t, outputA.String)
	projectID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "project", "create", "--name", "handover", "--root", fixture.repo))
	agentID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "agent", "create", "--project", projectID, "--name", "builder", "--provider", "shell", "--tool-budget", "4"))
	fixture.runFactoryctl(t, 0, "dispatch", "on")
	taskID := fixture.operatorID(t, fixture.runFactoryctl(t, 0, "task", "add", "--project", projectID, "--agent", agentID, "--title", "survive a daemon replacement", "--body", fixture.tickingBody(handoverTicks)))
	fixture.awaitTaskStatus(t, client, taskID, "running", 90*time.Second)

	// The provider records its own PID before its first tick, so the exact
	// process — not merely "a provider" — is what the assertions follow.
	providerPID := fixture.awaitProviderPID(t, 30*time.Second)
	runID := fixture.soleRuntimeName(t)
	runnersBefore := fixture.runnerPIDs(t)
	if len(runnersBefore) != 1 {
		t.Fatalf("live runners before the handover = %v, want exactly one", runnersBefore)
	}
	before := fixture.observeTerminal(t, projectID, taskID, runID, 4)
	if before.Gap || before.Floor != 0 || before.Omitted != 0 {
		t.Fatalf("terminal before the handover = %+v", before)
	}
	assertTickSequence(t, "before the handover", before.Payload, 1)

	// Generation A ends. The runner keeps the provider; only the daemon goes.
	if err := daemonA.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := awaitProcessExit(daemonA, 30*time.Second); err != nil {
		t.Fatalf("factoryd A did not converge on SIGTERM: %v (output %q)", err, outputA.String())
	}
	if daemonA.ProcessState.ExitCode() != 0 {
		t.Fatalf("factoryd A SIGTERM exit = %d (output %q)", daemonA.ProcessState.ExitCode(), outputA.String())
	}
	if !processAlive(providerPID) {
		t.Fatalf("provider %d died with the daemon that started it", providerPID)
	}
	if got := fixture.providerPID(t); got != providerPID {
		t.Fatalf("provider pid across the daemon's exit = %d, want %d", got, providerPID)
	}
	if got := fixture.runnerPIDs(t); !equalInts(got, runnersBefore) {
		t.Fatalf("runners across the daemon's exit = %v, want %v", got, runnersBefore)
	}

	// Generation B adopts what generation A handed over.
	daemonB, outputB := fixture.startFactoryd(t)
	client = fixture.waitClient(t, outputB.String)
	boot := outputB.String()
	if !strings.Contains(boot, "recovered run "+runID+": adopted") {
		t.Fatalf("boot after the handover did not adopt the running run: %q", boot)
	}
	if status := fixture.taskStatus(t, client, taskID); status != "running" {
		t.Fatalf("adopted task = %q, want running", status)
	}
	if got := fixture.providerPID(t); got != providerPID || !processAlive(got) {
		t.Fatalf("provider under the adopting daemon = %d (alive=%v), want %d", got, processAlive(got), providerPID)
	}
	if got := fixture.runnerPIDs(t); !equalInts(got, runnersBefore) {
		t.Fatalf("runners under the adopting daemon = %v, want %v", got, runnersBefore)
	}
	if got := fixture.soleRuntimeName(t); got != runID {
		t.Fatalf("runtime after adoption = %q, want %q", got, runID)
	}

	// The terminal is the same ring, still filling: everything generation A
	// saw is still there byte for byte, nothing was replayed before it, and
	// the sequence continues past where A stopped watching.
	after := fixture.observeTerminal(t, projectID, taskID, runID, countTicks(before.Payload)+2)
	if after.Gap || after.Floor != 0 || after.Omitted != 0 {
		t.Fatalf("terminal after the handover = %+v", after)
	}
	if !strings.HasPrefix(after.Payload, before.Payload) {
		t.Fatalf("adopted terminal replaced or replayed its history:\nbefore %q\nafter  %q", before.Payload, after.Payload)
	}
	assertTickSequence(t, "after the handover", after.Payload, countTicks(before.Payload)+1)
	t.Logf("handover: provider pid %d and runner pids %v unchanged across factoryd %d -> %d; terminal ticks %d -> %d, head %d -> %d, no gap, no replay",
		providerPID, runnersBefore, daemonA.Process.Pid, daemonB.Process.Pid,
		countTicks(before.Payload), countTicks(after.Payload), before.Head, after.Head)

	// The task reports its own success to the daemon that adopted it.
	fixture.awaitTaskStatus(t, client, taskID, "succeeded", 120*time.Second)
	entries, err := os.ReadDir(install.RuntimesPath(fixture.home))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("runtime %q survived the settled adopted run", entry.Name())
		}
	}
	if processAlive(providerPID) {
		t.Fatalf("provider %d survived its own settled run", providerPID)
	}

	if err := daemonB.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := awaitProcessExit(daemonB, 30*time.Second); err != nil {
		t.Fatalf("factoryd B did not converge on SIGTERM: %v (output %q)", err, outputB.String())
	}
	if daemonB.ProcessState.ExitCode() != 0 {
		t.Fatalf("factoryd B SIGTERM exit = %d (output %q)", daemonB.ProcessState.ExitCode(), outputB.String())
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		output, err := exec.Command("/usr/bin/pgrep", "-f", fixture.root).CombinedOutput()
		if err != nil {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("processes survived the handover lifecycle: %s", output)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// tickingBody is a provider task that publishes its own PID, prints one
// numbered line a second, and then declares its outcome through the local API
// — which by then belongs to a different daemon process than the one that
// started it.
func (fixture *blackBoxFixture) tickingBody(ticks int) string {
	return fmt.Sprintf(`set -eu
printf '%%s\n' "$$" > '%s'
i=1
while [ "$i" -le %d ]; do
	printf 'tick %%d\n' "$i"
	i=$((i + 1))
	sleep 1
done
"$DARK_FACTORY_FACTORYCTL" attempt succeed --result 'handover survived'
`, fixture.providerPIDPath(), ticks)
}

func (fixture *blackBoxFixture) providerPIDPath() string {
	return filepath.Join(fixture.root, "provider.pid")
}

func (fixture *blackBoxFixture) providerPID(t *testing.T) int {
	t.Helper()
	body, err := os.ReadFile(fixture.providerPIDPath())
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		t.Fatalf("provider pid file = %q: %v", body, err)
	}
	return pid
}

func (fixture *blackBoxFixture) awaitProviderPID(t *testing.T, patience time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(fixture.providerPIDPath()); err == nil {
			return fixture.providerPID(t)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the provider never published its pid")
	return 0
}

// soleRuntimeName returns the one runtime child's name, which is the run's ID.
func (fixture *blackBoxFixture) soleRuntimeName(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(install.RuntimesPath(fixture.home))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("runtime children = %v, want exactly one", names)
	}
	return names[0]
}

func (fixture *blackBoxFixture) runnerPIDs(t *testing.T) []int {
	t.Helper()
	output, err := exec.Command("/usr/bin/pgrep", "-f", os.Getenv("DARK_FACTORY_E2E_RUNNER")).CombinedOutput()
	if err != nil {
		return nil
	}
	pids := []int{}
	for _, line := range strings.Fields(string(output)) {
		pid, convErr := strconv.Atoi(line)
		if convErr != nil {
			t.Fatalf("pgrep output %q: %v", output, convErr)
		}
		pids = append(pids, pid)
	}
	return pids
}

// observeTerminal reads the run's terminal from its floor through the real
// CLI, using the attempt's own durable credential exactly as the provider
// does. It retries until the ring holds at least wantTicks numbered lines, so
// the assertion is about continuity rather than about timing.
func (fixture *blackBoxFixture) observeTerminal(t *testing.T, projectID, taskID, runID string, wantTicks int) terminalObservation {
	t.Helper()
	deadline := time.Now().Add(time.Duration(wantTicks+30) * time.Second)
	var last terminalObservation
	for time.Now().Before(deadline) {
		command := exec.Command(fixture.factoryctl, "attempt", "terminal", "observe",
			"--project", projectID, "--task", taskID, "--run", runID, "--cursor", "0", "--max-bytes", "65536")
		command.Env = []string{
			"DARK_FACTORY_SOCKET=" + install.LocalAPISocketPath(fixture.home),
			"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + filepath.Join(install.RuntimesPath(fixture.home), runID, changeworker.AttemptTokenName),
		}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("factoryctl attempt terminal observe: %v (%s)", err, output)
		}
		if err := json.Unmarshal(output, &last); err != nil {
			t.Fatalf("terminal observation %q: %v", output, err)
		}
		if countTicks(last.Payload) >= wantTicks {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("terminal never reached %d ticks: %+v", wantTicks, last)
	return terminalObservation{}
}

// countTicks reports how many numbered lines the payload holds.
func countTicks(payload string) int {
	count := 0
	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "tick ") {
			count++
		}
	}
	return count
}

// assertTickSequence proves the numbered lines run 1, 2, 3 … with no repeat
// and no hole: a replayed or re-run provider shows up as either.
func assertTickSequence(t *testing.T, when, payload string, atLeast int) {
	t.Helper()
	expected := 1
	for _, line := range strings.Split(payload, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "tick ") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimPrefix(trimmed, "tick "))
		if err != nil {
			t.Fatalf("%s: malformed terminal line %q", when, trimmed)
		}
		if number != expected {
			t.Fatalf("%s: terminal line %d is %q, so the sequence broke:\n%q", when, expected, trimmed, payload)
		}
		expected++
	}
	if expected-1 < atLeast {
		t.Fatalf("%s: terminal held %d ticks, want at least %d:\n%q", when, expected-1, atLeast, payload)
	}
}

// processAlive reports whether the exact PID still exists. Signal 0 asks the
// kernel without delivering anything.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
