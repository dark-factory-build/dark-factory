//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex" {
		if err := runSupervisorCodexFixture(); err != nil {
			fmt.Fprintln(os.Stderr, "supervisor Codex fixture failed")
			os.Exit(70)
		}
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) == "claude" {
		if err := runSupervisorClaudeFixture(); err != nil {
			fmt.Fprintln(os.Stderr, "supervisor Claude fixture failed")
			os.Exit(70)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "--exec-gate":
			err = runner.RunExecGate()
		case "--attempt-runner":
			err = runner.RunAttemptRunner()
		case "--change-worker":
			err = changeworker.Run(context.Background())
		case "--supervisor-attempt-succeed", "--supervisor-attempt-block", "--supervisor-attempt-fail":
			if len(os.Args) != 3 {
				err = errors.New("invalid attempt helper invocation")
				break
			}
			client, clientErr := api.NewAttemptClientFromEnvironment(os.Getenv("DARK_FACTORY_SOCKET"))
			if clientErr != nil {
				err = clientErr
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			switch os.Args[1] {
			case "--supervisor-attempt-block":
				_, err = client.Block(ctx, os.Args[2])
			case "--supervisor-attempt-fail":
				_, err = client.Fail(ctx, os.Args[2])
			default:
				_, err = client.Succeed(ctx, os.Args[2])
			}
			cancel()
		case "attempt":
			if len(os.Args) != 7 || os.Args[2] != "request-human" || os.Args[3] != "--idempotency-key" || os.Args[5] != "--question" {
				err = errors.New("invalid factoryctl fixture invocation")
				break
			}
			client, clientErr := api.NewAttemptClientFromEnvironment(os.Getenv("DARK_FACTORY_SOCKET"))
			if clientErr != nil {
				err = clientErr
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err = client.RequestHuman(ctx, api.HumanQuestionInput{IdempotencyKey: os.Args[4], Question: os.Args[6]})
			cancel()
		case "--supervisor-descendant-provider":
			receipt := os.Getenv("DARK_FACTORY_CHILD_RECEIPT")
			continuePath := os.Getenv("DARK_FACTORY_CONTINUE_RECEIPT")
			if receipt == "" || continuePath == "" {
				err = errors.New("invalid descendant provider environment")
				break
			}
			child := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done")
			if err = child.Start(); err != nil {
				break
			}
			if err = os.WriteFile(receipt, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
				_ = child.Process.Kill()
				break
			}
			for err == nil {
				if _, statErr := os.Stat(continuePath); statErr == nil {
					break
				} else if !errors.Is(statErr, os.ErrNotExist) {
					err = statErr
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err == nil {
				client, clientErr := api.NewAttemptClientFromEnvironment(os.Getenv("DARK_FACTORY_SOCKET"))
				if clientErr != nil {
					err = clientErr
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_, err = client.Succeed(ctx, "typed-success")
					cancel()
				}
			}
			for err == nil {
				time.Sleep(time.Second)
			}
		default:
			os.Exit(m.Run())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "supervisor helper failed")
			os.Exit(70)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexContinuationContextPreservesMaximumOriginalTask(t *testing.T) {
	var condition kernel.ContinuationConditionID
	copy(condition[:], supervisorIDBytes(240))
	revision, _ := kernel.NewRevision(1)
	task := bytes.Repeat([]byte{'x'}, runner.MaxCodexTaskBytes)
	contexts := []kernel.ContinuationContext{{ConditionKind: kernel.ConditionHumanRequest, ConditionID: condition, ConditionRevision: revision, ResolutionDetail: "continue"}}
	framed, err := providerTaskWithContinuationContext(kernel.ProviderCodex, task, contexts)
	metadata := []byte("condition=human_request condition_id=" + hex.EncodeToString(condition.Bytes()) + " condition_revision=1 context_digest=" + strings.Repeat("0", 64) + " resolution=continue")
	if err != nil || len(framed) <= len(task) || !bytes.Equal(framed[:len(task)], task) || bytes.Count(framed, []byte("Factory continuation context:")) != 1 || !bytes.Contains(framed, metadata) {
		t.Fatalf("maximum Codex API continuation framing: bytes=%d err=%v", len(framed), err)
	}
	launchTask, err := providerTaskForContinuationLaunch(kernel.ProviderCodex, task, contexts)
	if err != nil || string(launchTask) != continuationTaskFetchInstruction {
		t.Fatalf("maximum Codex launch task = %q, %v", launchTask, err)
	}
	if _, _, err := provider.PrepareTask(kernel.ProviderCodex, launchTask); err != nil {
		t.Fatalf("bounded Codex continuation launch: %v", err)
	}
	short := []byte("continue the exact task")
	want, err := providerTaskWithContinuationContext(kernel.ProviderClaudeCode, short, contexts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := providerTaskForContinuationLaunch(kernel.ProviderClaudeCode, short, contexts)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("short native continuation changed: %q, %v", got, err)
	}
	shell, err := providerTaskForContinuationLaunch(kernel.ProviderShell, []byte("printf ok"), contexts)
	if err != nil || !bytes.Contains(shell, []byte("# Factory continuation context:")) || !bytes.Contains(shell, []byte("# condition=")) {
		t.Fatalf("shell continuation comments = %q, %v", shell, err)
	}
}

// codexFixtureSessionID stands in for the id the real Codex CLI would assign
// itself at creation. provider.canonicalUUID requires an exact 36-character
// lowercase 8-4-4-4-12 hex UUID, so this is one, with this exact fixture
// invocation's own PID as its last group: deterministic per process, so a
// test reading two separate fixture processes' own rollouts back from disk
// can tell them apart without sharing a hardcoded id.
func codexFixtureSessionID() string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", uint64(os.Getpid()))
}

func runSupervisorCodexFixture() error {
	client, err := api.NewAttemptClientFromEnvironment(os.Getenv("DARK_FACTORY_SOCKET"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := client.Task(ctx)
	if err != nil {
		return err
	}
	// An explicit target request must cross the runner, Change worker, provider
	// profile and live daemon registry; task launch and status never select a
	// project-latest tree.
	target, overseer := "", false
	if target, overseer = strings.CutPrefix(task.Task, "handoff "); !overseer {
		var reviewer bool
		target, reviewer = strings.CutPrefix(task.Task, "review handoff ")
		if !reviewer {
			target = ""
		}
	}
	var sourcePaths []string
	for _, target := range strings.Split(target, ";") {
		if target == "" {
			continue
		}
		expected := strings.Fields(target)
		if len(expected) != 5 {
			return fmt.Errorf("source target = %q", target)
		}
		if overseer {
			before, err := client.OverseerTaskSnapshot(ctx, expected[0])
			if err != nil || len(before.Handoffs) != 1 || before.Handoffs[0].TaskID != expected[0] || before.Handoffs[0].SourcePath != "" || before.Handoffs[0].GitDirectory != "" || before.Handoffs[0].HeadCommit == "" {
				return fmt.Errorf("status before explicit request = %+v, %v", before.Handoffs, err)
			}
		}
		handoff, err := client.Source(ctx, expected[0])
		if err != nil {
			return err
		}
		if handoff.TaskID != expected[0] || handoff.ChangeID != expected[1] || handoff.BaseCommit != expected[2] || strconv.FormatUint(handoff.TaskWorkRevision, 10) != expected[3] || strconv.FormatUint(handoff.ChangeRevision, 10) != expected[4] {
			return fmt.Errorf("source receipt = %+v for expected identity %q", handoff, target)
		}
		if handoff.Branch != "factory/"+handoff.ChangeID[:12] || handoff.HeadCommit == "" || filepath.Base(handoff.GitDirectory) != ".git" || filepath.Base(handoff.SourcePath) != handoff.ChangeID {
			return fmt.Errorf("source receipt lacks Git identities: %+v", handoff)
		}
		if _, err := os.Stat(filepath.Join(handoff.GitDirectory, "worktrees", handoff.ChangeID)); err != nil {
			return fmt.Errorf("source receipt Git directory does not register the worktree: %w", err)
		}
		if overseer {
			snapshot, err := client.OverseerTaskSnapshot(ctx, expected[0])
			if err != nil {
				return err
			}
			if len(snapshot.Handoffs) != 1 || snapshot.Handoffs[0].TaskID != handoff.TaskID || snapshot.Handoffs[0].HeadCommit != handoff.HeadCommit {
				return fmt.Errorf("status handoff identity = %+v", snapshot.Handoffs)
			}
		} else if _, err := client.OverseerTaskSnapshot(ctx, expected[0]); err == nil {
			return errors.New("reviewer gained overseer source discovery")
		} else {
			var remote *api.RemoteError
			if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
				return fmt.Errorf("reviewer overseer refusal = %w", err)
			}
		}
		info, err := os.Stat(handoff.SourcePath)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("launch handoff source: %w", err)
		}
		if _, err := os.ReadFile(filepath.Join(handoff.SourcePath, "payload.txt")); err != nil {
			return fmt.Errorf("launch handoff source payload: %w", err)
		}
		again, err := client.Source(ctx, expected[0])
		if err != nil || again != handoff {
			return fmt.Errorf("repeat source receipt changed: %+v, %v", again, err)
		}
		sourcePaths = append(sourcePaths, handoff.SourcePath)
	}
	for i, path := range sourcePaths {
		for _, earlier := range sourcePaths[:i] {
			if earlier == path {
				return errors.New("distinct source targets shared a path")
			}
		}
		if body, err := os.ReadFile(filepath.Join(path, "payload.txt")); err != nil || string(body) != "exact source\n" {
			return fmt.Errorf("earlier source was lost after another request: %q, %v", body, err)
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
			return fmt.Errorf("source is not a worktree: %w", err)
		}
	}
	for _, value := range append(append([]string(nil), os.Args[1:]...), os.Environ()...) {
		if strings.Contains(value, task.Task) {
			return errors.New("private task crossed the Codex argv/environment boundary")
		}
	}
	size, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
	if err != nil {
		return err
	}
	// Stand in for the real CLI's own rollout write, so a later launch's
	// discovery (provider.codexSessionSelection) can find this exact cwd the
	// same way it would against the real tool.
	// resumed_from is this fixture's own diagnostic field, not one
	// codexSessionSelection reads: it records what this exact invocation's
	// own argv named, so a test can independently confirm what a later
	// launch actually discovered and resumed.
	resumedFrom := ""
	if len(os.Args) > 2 && os.Args[1] == "resume" {
		resumedFrom = os.Args[2]
		if !slices.Contains(os.Args, `tui.resume_cwd="current"`) {
			return errors.New("resumed Codex launch did not select its current authorized cwd")
		}
	}
	if codexHome, cwd := os.Getenv("CODEX_HOME"), func() string { path, _ := os.Getwd(); return path }(); codexHome != "" && cwd != "" {
		day := filepath.Join(codexHome, "sessions", time.Now().UTC().Format("2006/01/02"))
		if err := os.MkdirAll(day, 0o700); err == nil {
			line, marshalErr := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": codexFixtureSessionID(), "cwd": cwd, "resumed_from": resumedFrom}})
			if marshalErr == nil {
				// A PID suffix keeps this run's filename distinct from any
				// other rollout this fixture wrote in the same wall-clock
				// second; codexSessionSelection only reads the JSON content.
				name := fmt.Sprintf("rollout-%s-%d.jsonl", time.Now().UTC().Format("2006-01-02T15-04-05"), os.Getpid())
				_ = os.WriteFile(filepath.Join(day, name), append(line, '\n'), 0o600)
			}
		}
	}
	_, err = client.Succeed(ctx, fmt.Sprintf("%s\nPTY=%dx%d", task.Task, size.Col, size.Row))
	return err
}

func TestSupervisorRunsRegisteredShellWorkerToTypedSuccess(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	var observed kernel.RunID
	fixture.spec.beforeProviderRelease = func() {
		var err error
		observed, _, err = fixture.daemon.RunPaths(context.Background(), fixture.agentID)
		if err != nil || observed == (kernel.RunID{}) {
			t.Fatalf("registered source observation: %v %v", observed, err)
		}
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	if observed != run.ID {
		t.Fatalf("observed run %v, actual %v", observed, run.ID)
	}
	if current, paths, err := fixture.daemon.RunPaths(context.Background(), fixture.agentID); err != nil || current != (kernel.RunID{}) || len(paths) != 0 {
		t.Fatalf("finished owner observation: %v %v %v", current, paths, err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != "typed-success" {
		t.Fatalf("terminal proposal = %+v", run.Proposal)
	}
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
	if _, err := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime remains: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(fixture.changeParent, fixture.changeName(t, run), "payload.txt")); err != nil || string(body) != "exact source\n" {
		t.Fatalf("retained Change payload = %q, %v", body, err)
	}
	changeState, found, err := fixture.store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found || changeState.Selection == nil || fmt.Sprintf("%x", changeState.Selection.Commit().Bytes()) != fixture.base {
		t.Fatalf("Change exact base = %+v, found=%v, err=%v", changeState.Selection, found, err)
	}
}

func TestSupervisorOptionalCILeaseRefusalDoesNotFailFreshWork(t *testing.T) {
	program := "test -z \"${DARK_FACTORY_LOCAL_CI_DIRECTORY-}\" || exit 90\n" + supervisorProgram(t, false, false)
	fixture := newSupervisorFixture(t, program)
	legacyLock := filepath.Join(fixture.root, "repository", ".git", ".dark-factory-local-ci.lock")
	if err := os.Mkdir(legacyLock, 0700); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
	if info, err := os.Lstat(legacyLock); err != nil || !info.IsDir() {
		t.Fatalf("legacy lease changed: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(legacyLock), "dark-factory-local-ci")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal created a capability: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(fixture.changeParent, fixture.changeName(t, run), "payload.txt")); err != nil || string(body) != "exact source\n" {
		t.Fatalf("retained exact source = %q, %v", body, err)
	}
}

func TestSupervisorOptionalCILeaseDoesNotHideSourceFailure(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.BaseRevision = "refs/heads/nonexistent-source"
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Logf("source selection refusal: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Terminal == nil || run.Terminal.Code() != kernel.FailureSource {
		t.Fatalf("source failure = %+v", run.Terminal)
	}
}

// Exercise the actual provider API, runner result authentication, and retained
// proposal retry together. A backwards clock causes the first durable
// finalization to refuse after authenticating the exact attempt bearer.
func TestSupervisorConvergesRefusedProviderOutcome(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	var refuse atomic.Bool
	fixture.daemon.now = func() time.Time {
		if refuse.CompareAndSwap(true, false) {
			return time.UnixMilli(1)
		}
		return time.Now()
	}
	fixture.spec.beforeProviderRelease = func() { refuse.Store(true) }
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext after authenticated outcome refusal: %v", err)
	}
	if refuse.Load() {
		t.Fatal("provider never attempted its outcome")
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != "typed-success" {
		t.Fatalf("retained provider proposal = %+v", run.Proposal)
	}
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
}

func TestSupervisorCancelsRetainedOutcomeRetryWithWriterBlocked(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	lock, err := sql.Open("sqlite3", "file:"+fixture.storePath)
	if err != nil {
		t.Fatal(err)
	}
	lock.SetMaxOpenConns(1)
	defer lock.Close()
	defer lock.Exec("ROLLBACK")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	var released atomic.Bool
	retryReached := make(chan error, 1)
	fixture.daemon.now = func() time.Time {
		if released.Load() {
			switch calls.Add(1) {
			case 1:
				return time.UnixMilli(1)
			case 2:
				// The first call was the API refusal; this is the supervisor's
				// retry after authenticating the runner result. Keep the writer
				// unavailable until RunNext has returned from cancellation.
				_, lockErr := lock.Exec("BEGIN IMMEDIATE")
				retryReached <- lockErr
				cancel()
			}
		}
		return time.Now()
	}
	fixture.spec.beforeProviderRelease = func() { released.Store(true) }
	done := make(chan error, 1)
	go func() {
		_, runErr := fixture.daemon.RunNext(ctx, fixture.spec)
		done <- runErr
	}()
	select {
	case err := <-retryReached:
		if err != nil {
			t.Fatalf("hold retry writer: %v", err)
		}
	case err := <-done:
		t.Fatalf("RunNext returned before retained retry: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("retained retry was not reached")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "retained outcome proposal") {
			t.Fatalf("canceled retained retry = %v", err)
		}
	case <-time.After(2 * time.Second):
		// Release before failing so even a regressed background retry joins.
		_, _ = lock.Exec("ROLLBACK")
		<-done
		t.Fatal("cancellation waited for writer availability")
	}
}

// An orchestrator has no Change: it runs in its private runtime home, the
// project tree is never copied for it, and its run reaches the same typed
// success through the same attempt API.
func TestSupervisorRunsOrchestratorInItsPrivateHomeWithoutAChange(t *testing.T) {
	program := "set -eu\n[ \"$PWD\" = \"$HOME\" ]\n[ ! -e payload.txt ]\nprintf x >> __WITNESS__\n" + quoteShell(supervisorTestExecutable(t)) + " --supervisor-attempt-succeed typed-success\n"
	fixture := newSupervisorRoleFixture(t, program, kernel.RoleOrchestrator)
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Role != kernel.RoleOrchestrator || run.ChangeID != nil || run.Proposal == nil || run.Proposal.Result() != "typed-success" {
		t.Fatalf("orchestrator run = role %s change %v proposal %+v", run.Role, run.ChangeID, run.Proposal)
	}
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
	if entries, err := os.ReadDir(fixture.changeParent); err != nil || len(entries) != 0 {
		t.Fatalf("orchestrator left a Change: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime remains: %v", err)
	}
}

// runSupervisorClaudeFixture stands in for the Claude CLI: it takes its
// terminal out of canonical mode as the CLI does, reads the prompt the
// runner types until the keystroke that submits it, and reports whether the
// task quoted at the prompt's end is exactly the task the attempt holds.
func runSupervisorClaudeFixture() error {
	termios, err := unix.IoctlGetTermios(0, unix.TIOCGETA)
	if err != nil {
		return err
	}
	termios.Lflag &^= unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN], termios.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(0, unix.TIOCSETA, termios); err != nil {
		return err
	}
	var line []byte
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}
		line = append(line, buf[:n]...)
		if end := bytes.IndexAny(line, "\r\n"); end >= 0 {
			line = line[:end]
			break
		}
	}
	client, err := api.NewAttemptClientFromEnvironment(os.Getenv("DARK_FACTORY_SOCKET"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := client.Task(ctx)
	if err != nil {
		return err
	}
	var typed string
	if quote := bytes.IndexByte(line, '"'); quote < 0 {
		return fmt.Errorf("no quoted task in %d typed bytes", len(line))
	} else if err := json.Unmarshal(line[quote:], &typed); err != nil {
		return fmt.Errorf("quoted task in %d typed bytes: %w", len(line), err)
	}
	if typed != task.Task {
		return fmt.Errorf("typed task is %d bytes, the attempt's is %d", len(typed), len(task.Task))
	}
	// Stand in for the real CLI's own transcript write, so a later launch's
	// on-disk check (provider.claudeSessionSelection) can observe this exact
	// session the same way it would against the real tool. The escaping here
	// mirrors provider.escapeClaudeProjectPath.
	if home, cwd := os.Getenv("HOME"), func() string { path, _ := os.Getwd(); return path }(); home != "" && cwd != "" {
		for i, arg := range os.Args {
			if (arg == "--session-id" || arg == "--resume") && i+1 < len(os.Args) {
				dir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
				if err := os.MkdirAll(dir, 0o700); err == nil {
					_ = os.WriteFile(filepath.Join(dir, os.Args[i+1]+".jsonl"), []byte(`{"type":"session_meta"}`+"\n"), 0o600)
				}
				break
			}
		}
	}
	_, err = client.Succeed(ctx, "exact")
	return err
}

// A Claude task longer than a terminal line or a socket buffer reaches the
// CLI whole, through the real runner, worker and PTY.
func TestSupervisorClaudeReceivesALongTaskThroughTheTerminal(t *testing.T) {
	task := strings.Repeat("Codify the operator scripts and the deploy order. ", 128)
	fixture := newSupervisorFixture(t, "unused shell task")
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderClaudeCode, "", ""); err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET title = ?, body = ? WHERE id = ?`, "long task", task, fixture.taskID.Bytes())
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, supervisorTestExecutable(t), filepath.Join(tools, "claude"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != "exact" {
		t.Fatalf("Claude receipt = %+v", run.Proposal)
	}
	fixture.assertReleased(t, run)
}

// A send-back retry re-queues the same task incarnation and reuses the same
// retained Change directory (see TestSupervisorRetainedRetrySkipsGitAndPreservesPublishedTree),
// so a Claude Code worker's deterministic native session id is stable across
// it: the retry resumes the first attempt's own conversation on disk instead
// of starting an unrelated fresh one.
func TestSupervisorClaudeWorkerReusesNativeSessionAcrossSendBack(t *testing.T) {
	fixture := newSupervisorFixture(t, "unused shell task")
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderClaudeCode, "", ""); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, supervisorTestExecutable(t), filepath.Join(tools, "claude"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath

	first, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("first RunNext: %v", err)
	}
	fixture.assertTerminal(t, first, kernel.OutcomeSucceeded)
	changePath := filepath.Join(fixture.changeParent, fixture.changeName(t, first))
	projectDir := filepath.Join(fixture.spec.AccountHome, ".claude", "projects", strings.ReplaceAll(changePath, "/", "-"))
	firstSessions, err := os.ReadDir(projectDir)
	if err != nil || len(firstSessions) != 1 {
		t.Fatalf("first native session files = %v, err=%v, want exactly one", firstSessions, err)
	}

	queueSupervisorRetry(t, fixture, first)
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("retry RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeSucceeded)
	if first.ChangeID == nil || second.ChangeID == nil || *first.ChangeID != *second.ChangeID {
		t.Fatalf("send-back retry changed the retained Change: first=%v second=%v", first.ChangeID, second.ChangeID)
	}
	secondSessions, err := os.ReadDir(projectDir)
	if err != nil || len(secondSessions) != 1 || secondSessions[0].Name() != firstSessions[0].Name() {
		t.Fatalf("retry native session files = %v (want %q alone), err=%v", secondSessions, firstSessions[0].Name(), err)
	}
	fixture.assertReleased(t, first)
	fixture.assertReleased(t, second)
}

// The provider inherits the daemon's umask. The service runs under 077, so
// a file it makes is 0600 and a directory 0700; a run whose worker made any
// file must still settle, with those modes kept, not repaired.
func TestSupervisorWorkerFilesSettleUnderThePrivateServiceUmask(t *testing.T) {
	previous := unix.Umask(0o077)
	t.Cleanup(func() { unix.Umask(previous) })
	program := "set -eu\nprintf made > made.txt\nmkdir made\nprintf inner > made/inner.txt\ngit add made.txt made/inner.txt\ngit commit -q -m worker-files\nprintf x >> __WITNESS__\n" + quoteShell(supervisorTestExecutable(t)) + " --supervisor-attempt-succeed typed-success\n"
	fixture := newSupervisorFixture(t, program)
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	retained := filepath.Join(fixture.changeParent, fixture.changeName(t, run))
	if info, err := os.Stat(filepath.Join(retained, "made.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("retained file = %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(retained, "made")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("retained directory = %v, %v", info, err)
	}
	fixture.assertReleased(t, run)
}

// A worktree the worker destroyed cannot be retained and cannot come back:
// the run ends as a visible source failure naming it, the Change is
// abandoned, the task fails, and the task's own retry makes a fresh
// worktree on the same branch.
func TestSupervisorMissingWorktreeFailsTheRunVisibly(t *testing.T) {
	// The first run deletes its own worktree; the retry, which finds the
	// witness of the first, does not.
	program := "set -eu\n[ -s __WITNESS__ ] || { cd \"$HOME\" && rm -rf \"$OLDPWD\"; }\nprintf x >> __WITNESS__\n" + quoteShell(supervisorTestExecutable(t)) + " --supervisor-attempt-succeed typed-success\n"
	fixture := newSupervisorFixture(t, program)
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Terminal == nil || run.Terminal.Code() != kernel.FailureSource || !strings.Contains(run.Terminal.Detail(), "published tree refused") ||
		!strings.Contains(run.Terminal.Detail(), "worktree is gone") || !strings.Contains(run.Terminal.Detail(), "changes/"+fixture.changeName(t, run)) {
		t.Fatalf("refused run = %+v", run.Terminal)
	}
	task, found, err := fixture.store.Task(context.Background(), run.TaskID)
	if err != nil || !found || task.Status != kernel.TaskFailed {
		t.Fatalf("task after refusal = %+v, found=%v, %v", task, found, err)
	}
	changeState, found, err := fixture.store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found || changeState.Phase != kernel.ChangeAbandoned || changeState.SettledRunID == nil || *changeState.SettledRunID != run.ID {
		t.Fatalf("change after refusal = %+v, found=%v, %v", changeState, found, err)
	}
	fixture.assertReleased(t, run)
	queueSupervisorRetry(t, fixture, run)
	retry, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext for the retry: %v", err)
	}
	if retry.ID == run.ID || retry.TaskID != run.TaskID || retry.AdmittedTaskWorkRevision.Int64() != 2 || retry.ChangeID == nil || *retry.ChangeID != *run.ChangeID {
		t.Fatalf("retry run = %+v", retry)
	}
	fixture.assertTerminal(t, retry, kernel.OutcomeSucceeded)
	retained, found, err := fixture.store.Change(context.Background(), *run.ChangeID)
	if err != nil || !found || retained.Phase != kernel.ChangeRetained || retained.HeadCommit == nil {
		t.Fatalf("change after the retry = %+v, found=%v, %v", retained, found, err)
	}
	if body, err := os.ReadFile(filepath.Join(fixture.changeParent, fixture.changeName(t, run), "payload.txt")); err != nil || string(body) != "exact source\n" {
		t.Fatalf("fresh worktree payload = %q, %v", body, err)
	}
}

func TestSupervisorCodexRetrievesExactTaskWithUsablePTY(t *testing.T) {
	const privateTask = "exact private Codex task"
	fixture := newSupervisorFixture(t, "unused shell task")
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET title = ?, body = ? WHERE id = ?`, "fallback must not win", privateTask, fixture.taskID.Bytes())
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, executable, filepath.Join(tools, "codex"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath

	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != privateTask+"\nPTY=120x40" {
		t.Fatalf("Codex task/PTY receipt = %q", run.Proposal.Result())
	}
	fixture.assertReleased(t, run)
}

// An orchestrator's own working directory is a fresh runtime root every run
// (see internal/daemon/supervisor_darwin.go and
// internal/changeworker/worker_darwin.go) and could never itself carry a
// resumable Codex session. A second run of the same agent instead resumes
// the first run's own session, discovered by that prior run's own working
// directory (kernel.Store.LatestTerminalRuntimeRoot, joined with
// changeworker.HomeName), so its standing tasks share one continuing context.
func TestSupervisorCodexOrchestratorResumesFromPreviousRunsWorkingDirectory(t *testing.T) {
	fixture := newSupervisorRoleFixture(t, "unused shell task", kernel.RoleOrchestrator)
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, supervisorTestExecutable(t), filepath.Join(tools, "codex"))
	if err := os.WriteFile(filepath.Join(tools, "dark-factory-maintainer-mcp-bridge"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath
	sessionsRoot := filepath.Join(provider.ConfigHome(kernel.ProviderCodex, fixture.spec.AccountHome), "sessions")

	first, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("first RunNext: %v", err)
	}
	fixture.assertTerminal(t, first, kernel.OutcomeSucceeded)
	firstRollouts := codexRolloutSummaries(t, sessionsRoot)
	if len(firstRollouts) != 1 || firstRollouts[0].ResumedFrom != "" || firstRollouts[0].ID == "" {
		t.Fatalf("first run rollouts = %+v, want exactly one fresh rollout with an id", firstRollouts)
	}
	firstSessionID := firstRollouts[0].ID

	queueSupervisorRetry(t, fixture, first)
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("second RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeSucceeded)
	secondRollouts := codexRolloutSummaries(t, sessionsRoot)
	if len(secondRollouts) != 2 {
		t.Fatalf("second run rollouts = %+v, want two", secondRollouts)
	}
	resumedSomething := false
	for _, rollout := range secondRollouts {
		if rollout.ResumedFrom == firstSessionID {
			resumedSomething = true
		}
	}
	if !resumedSomething {
		t.Fatalf("second orchestrator run did not resume the first run's session %q: %+v", firstSessionID, secondRollouts)
	}
	fixture.assertReleased(t, first)
	fixture.assertReleased(t, second)
}

// codexRolloutSummary is one fake rollout's own recorded id and its own
// diagnostic resumed_from field (see runSupervisorCodexFixture): the exact
// session id, if any, that invocation's own argv named to resume.
type codexRolloutSummary struct {
	ID          string
	ResumedFrom string
}

func codexRolloutSummaries(t *testing.T, sessionsRoot string) []codexRolloutSummary {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(sessionsRoot, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	summaries := make([]codexRolloutSummary, 0, len(matches))
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		line := raw
		if index := bytes.IndexByte(raw, '\n'); index >= 0 {
			line = raw[:index]
		}
		var meta struct {
			Payload struct {
				ID          string `json:"id"`
				ResumedFrom string `json:"resumed_from"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &meta); err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, codexRolloutSummary{ID: meta.Payload.ID, ResumedFrom: meta.Payload.ResumedFrom})
	}
	return summaries
}

// One Codex overseer reads blocked and failed retained Changes in the same
// attempt. Each source receipt must agree with targeted status, and requesting
// the second tree must preserve the first tree and its cached receipt.
// This is intentionally not a projection-only test: it exercises the real
// supervisor, Change worker, provider permission profile and attempt API.
func TestSupervisorCodexOverseerReadsMultipleExactRetainedChanges(t *testing.T) {
	program := strings.Replace(supervisorProgram(t, false, false), "--supervisor-attempt-succeed", "--supervisor-attempt-block", 1)
	fixture := newSupervisorFixture(t, program)
	worker, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("worker RunNext: %v", err)
	}
	fixture.assertTerminal(t, worker, kernel.OutcomeBlocked)
	changeState, found, err := fixture.store.Change(context.Background(), *worker.ChangeID)
	if err != nil || !found || changeState.Selection == nil {
		t.Fatalf("retained worker Change = %+v, found=%v, err=%v", changeState, found, err)
	}

	workerTask, found, err := fixture.store.Task(context.Background(), worker.TaskID)
	if err != nil || !found {
		t.Fatalf("source worker task: found=%v, err=%v", found, err)
	}

	if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{
		ID: supervisorTaskID(t, 20), ProjectID: worker.ProjectID, AssignedAgentID: worker.AgentID,
		IncarnationID: supervisorIncarnationID(t, 21), Title: "second source", Body: strings.Replace(workerTask.Body, "--supervisor-attempt-block", "--supervisor-attempt-fail", 1), Priority: 1,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("second worker RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeFailed)
	secondChange, found, err := fixture.store.Change(context.Background(), *second.ChangeID)
	if err != nil || !found || secondChange.Selection == nil {
		t.Fatalf("second retained Change = %+v, found=%v, err=%v", secondChange, found, err)
	}

	overseerID := supervisorAgentID(t, 9)
	if _, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{
		ID: overseerID, ProjectID: worker.ProjectID, Name: "overseer", Role: kernel.RoleOrchestrator,
		Provider: kernel.ProviderCodex, ToolBudgetLimit: 20,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{
		ID: supervisorTaskID(t, 10), ProjectID: worker.ProjectID, AssignedAgentID: overseerID, IncarnationID: supervisorIncarnationID(t, 11),
		Title: "review retained Change", Body: fmt.Sprintf("handoff %s %s %x %d %d; %s %s %x %d %d", worker.TaskID, changeState.ID, changeState.Selection.Commit().Bytes(), worker.AdmittedTaskWorkRevision.Int64(), changeState.Revision.Int64(), second.TaskID, secondChange.ID, secondChange.Selection.Commit().Bytes(), second.AdmittedTaskWorkRevision.Int64(), secondChange.Revision.Int64()), Priority: 1,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, executable, filepath.Join(tools, "codex"))
	if err := os.WriteFile(filepath.Join(tools, "dark-factory-maintainer-mcp-bridge"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath

	overseer, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("overseer RunNext: %v", err)
	}
	fixture.assertTerminal(t, overseer, kernel.OutcomeSucceeded)
	if overseer.Role != kernel.RoleOrchestrator || overseer.Proposal == nil || !strings.Contains(overseer.Proposal.Result(), "handoff ") {
		t.Fatalf("overseer receipt = %+v", overseer)
	}
	fixture.assertReleased(t, overseer)
}

// A delegated reviewer is a worker, not an overseer. It must explicitly
// request the target receipt without gaining project supervision authority.
func TestSupervisorCodexReviewerLaunchReceivesExactRetainedChangeReceipt(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	worker, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("source worker RunNext: %v", err)
	}
	fixture.assertTerminal(t, worker, kernel.OutcomeSucceeded)
	changeState, found, err := fixture.store.Change(context.Background(), *worker.ChangeID)
	if err != nil || !found || changeState.Selection == nil {
		t.Fatalf("retained source Change = %+v, found=%v, err=%v", changeState, found, err)
	}
	reviewerID := supervisorAgentID(t, 12)
	if _, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{
		ID: reviewerID, ProjectID: worker.ProjectID, Name: "reviewer", Role: kernel.RoleWorker,
		Provider: kernel.ProviderCodex, ToolBudgetLimit: 20,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{
		ID: supervisorTaskID(t, 13), ProjectID: worker.ProjectID, AssignedAgentID: reviewerID, IncarnationID: supervisorIncarnationID(t, 14),
		Title: "review retained Change", Body: fmt.Sprintf(" \treview handoff %s %s %x %d %d  \r\nFACTORY_SOURCE owner/repo#1\nreview this exact source", worker.TaskID, changeState.ID, changeState.Selection.Commit().Bytes(), worker.AdmittedTaskWorkRevision.Int64(), changeState.Revision.Int64()), Priority: 1,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(fixture.root, "reviewer-tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, executable, filepath.Join(tools, "codex"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath

	reviewer, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("reviewer RunNext: %v", err)
	}
	fixture.assertTerminal(t, reviewer, kernel.OutcomeSucceeded)
	if reviewer.Role != kernel.RoleWorker || reviewer.Proposal == nil || !strings.Contains(reviewer.Proposal.Result(), "review handoff ") {
		t.Fatalf("reviewer receipt = %+v", reviewer)
	}
	fixture.assertReleased(t, reviewer)
}

func TestSupervisorReviewerRefusesMismatchedRetainedIdentityBeforeProvider(t *testing.T) {
	for _, field := range []string{"change", "base", "work revision", "change revision"} {
		t.Run(field, func(t *testing.T) {
			fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
			worker, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err != nil {
				t.Fatalf("source worker RunNext: %v", err)
			}
			fixture.assertTerminal(t, worker, kernel.OutcomeSucceeded)
			changeState, found, err := fixture.store.Change(context.Background(), *worker.ChangeID)
			if err != nil || !found || changeState.Selection == nil {
				t.Fatalf("retained source Change = %+v, found=%v, err=%v", changeState, found, err)
			}
			changeID, base := changeState.ID.String(), fmt.Sprintf("%x", changeState.Selection.Commit().Bytes())
			workRevision, changeRevision := worker.AdmittedTaskWorkRevision.Int64(), changeState.Revision.Int64()
			switch field {
			case "change":
				changeID = strings.Repeat("f", 32)
			case "base":
				base = strings.Repeat("f", len(base))
			case "work revision":
				workRevision++
			case "change revision":
				changeRevision++
			}
			reviewerID := supervisorAgentID(t, 12)
			if _, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{ID: reviewerID, ProjectID: worker.ProjectID, Name: "reviewer", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 20}, supervisorTime()); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{
				ID: supervisorTaskID(t, 13), ProjectID: worker.ProjectID, AssignedAgentID: reviewerID, IncarnationID: supervisorIncarnationID(t, 14), Title: "review retained Change",
				Body: fmt.Sprintf("review handoff %s %s %s %d %d", worker.TaskID, changeID, base, workRevision, changeRevision), Priority: 1,
			}, supervisorTime()); err != nil {
				t.Fatal(err)
			}
			reviewer, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err != nil && !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("reviewer RunNext: %v", err)
			}
			fixture.assertTerminal(t, reviewer, kernel.OutcomeFailed)
			if reviewer.Terminal == nil || reviewer.Terminal.Code() != kernel.FailureSource {
				t.Fatalf("mismatched handoff reached provider: %+v", reviewer.Terminal)
			}
			fixture.assertOneWitness(t)
		})
	}
}

// A retry reopens the same worktree: the worker's commit on the Change
// branch and its uncommitted edits are exactly as it left them, the
// settled head is what the daemon recorded, and no fresh selection or
// upstream refresh happens.
func TestSupervisorRetainedRetryReopensTheSameWorktree(t *testing.T) {
	program := "set -eu\nif [ ! -s __WITNESS__ ]; then printf committed > committed.txt; git add committed.txt; git commit -q -m committed; printf edited > edited.txt; fi\nprintf x >> __WITNESS__\nexit 0\n"
	fixture := newSupervisorFixture(t, program)
	first, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("first RunNext: %v", err)
	}
	fixture.assertTerminal(t, first, kernel.OutcomeFailed)
	changePath := filepath.Join(fixture.changeParent, fixture.changeName(t, first))
	before, err := os.Lstat(changePath)
	if err != nil {
		t.Fatal(err)
	}
	firstChange, found, err := fixture.store.Change(context.Background(), *first.ChangeID)
	if err != nil || !found || firstChange.Selection == nil || firstChange.HeadCommit == nil {
		t.Fatalf("first retained Change = %+v, found=%v, err=%v", firstChange, found, err)
	}
	head := strings.TrimSpace(supervisorGitOutput(t, supervisorNativeGit(t), "-C", changePath, "rev-parse", "HEAD"))
	if fmt.Sprintf("%x", firstChange.HeadCommit.Bytes()) != head || head == fixture.base {
		t.Fatalf("settled head = %x, worktree head = %s, base = %s", firstChange.HeadCommit.Bytes(), head, fixture.base)
	}
	if fmt.Sprintf("%x", firstChange.Selection.Commit().Bytes()) != fixture.base {
		t.Fatalf("settled base moved: %x", firstChange.Selection.Commit().Bytes())
	}
	queueSupervisorRetry(t, fixture, first)
	// A retained retry must not resolve a selector again.
	fixture.spec.BaseRevision = "refs/heads/retained-retry-must-not-resolve"
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("retained RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeFailed)
	after, err := os.Lstat(changePath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("retained worktree identity changed: %v", err)
	}
	for name, want := range map[string]string{"payload.txt": "exact source\n", "committed.txt": "committed", "edited.txt": "edited"} {
		if body, err := os.ReadFile(filepath.Join(changePath, name)); err != nil || string(body) != want {
			t.Fatalf("retained %s = %q, %v", name, body, err)
		}
	}
	if got := strings.TrimSpace(supervisorGitOutput(t, supervisorNativeGit(t), "-C", changePath, "rev-parse", "HEAD")); got != head {
		t.Fatalf("retry moved the branch to %s", got)
	}
	if witness, err := os.ReadFile(fixture.witness); err != nil || string(witness) != "xx" {
		t.Fatalf("retained provider witness = %q, %v", witness, err)
	}
	if first.ChangeID == nil || second.ChangeID == nil || *first.ChangeID != *second.ChangeID || second.AdmittedChangeRevision == nil {
		t.Fatalf("retained retry changed canonical Change: first=%+v second=%+v", first.ChangeID, second.ChangeID)
	}
	secondChange, found, err := fixture.store.Change(context.Background(), *second.ChangeID)
	if err != nil || !found || secondChange.Selection == nil || secondChange.Selection.RepositoryIdentity() != firstChange.Selection.RepositoryIdentity() || secondChange.HeadCommit == nil || fmt.Sprintf("%x", secondChange.HeadCommit.Bytes()) != head {
		t.Fatalf("retained retry changed identities: Change=%+v found=%v err=%v", secondChange, found, err)
	}
}

func TestSupervisorRetainedRetryFailsClosedOnDurableAuthorityMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *supervisorFixture, kernel.Run)
	}{
		{
			name: "repository identity",
			mutate: func(t *testing.T, fixture *supervisorFixture, _ kernel.Run) {
				repository := filepath.Join(fixture.root, "repository")
				if err := os.Rename(repository, repository+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(repository, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// Under the worktree model, settling a retained Change reads the
			// worktree through the repository, unlike the old published-tree
			// copy: a repository fault is one retainedSettlement documents as
			// transient ("may pass tomorrow and leaves the run finalizing"),
			// not a terminal refusal. So this case now behaves like the other
			// durable-authority mismatches above: RunNext surfaces the fault
			// rather than reaching the provider or settling gracefully.
			name: "repository replacement at provider release",
			mutate: func(t *testing.T, fixture *supervisorFixture, _ kernel.Run) {
				fixture.spec.beforeProviderRelease = func() {
					repository := filepath.Join(fixture.root, "repository")
					if err := os.Rename(repository, repository+".release-replaced"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(repository, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "base commit",
			mutate: func(t *testing.T, fixture *supervisorFixture, first kernel.Run) {
				execSupervisorSQL(t, fixture.storePath, `UPDATE changes SET base_commit = zeroblob(length(base_commit)) WHERE id = ?`, first.ChangeID.Bytes())
			},
		},
		{
			name: "tree identity",
			mutate: func(t *testing.T, fixture *supervisorFixture, first kernel.Run) {
				name := fixture.changeName(t, first)
				tree := filepath.Join(fixture.changeParent, name)
				if err := os.Rename(tree, tree+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(tree, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tree, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupervisorFixture(t, providerExitWithoutOutcomeProgram(t))
			first, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err != nil {
				t.Fatalf("first RunNext: %v", err)
			}
			fixture.assertTerminal(t, first, kernel.OutcomeFailed)
			retainedPath := filepath.Join(fixture.changeParent, fixture.changeName(t, first))
			before, err := os.Lstat(retainedPath)
			if err != nil {
				t.Fatal(err)
			}
			queueSupervisorRetry(t, fixture, first)
			test.mutate(t, fixture, first)
			if _, err := fixture.daemon.RunNext(context.Background(), fixture.spec); err == nil {
				t.Fatal("mismatched retained authority reached provider")
			}
			if witness, err := os.ReadFile(fixture.witness); err != nil || string(witness) != "x" {
				t.Fatalf("mismatched authority provider witness = %q, %v", witness, err)
			}
			preservedPath := retainedPath
			if test.name == "tree identity" {
				preservedPath += ".replaced"
			}
			after, err := os.Lstat(preservedPath)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("mismatched authority changed retained source: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestSupervisorShellProviderRequestsExactlyOneDurableHumanRequest(t *testing.T) {
	const (
		key      = "0123456789abcdef0123456789abcdef"
		question = "private-provider-question-sentinel"
	)
	fixture := newSupervisorFixture(t, supervisorHumanRequestProgram(t, key, question))
	releaseChecked := make(chan error, 1)
	fixture.spec.beforeProviderRelease = func() {
		if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
			releaseChecked <- fmt.Errorf("provider effect exists before StageProvider release: %v", err)
			return
		}
		snapshot, err := fixture.store.Snapshot(context.Background())
		if err != nil || len(snapshot.HumanRequests) != 0 {
			releaseChecked <- fmt.Errorf("human request exists before StageProvider release: %+v, %v", snapshot.HumanRequests, err)
			return
		}
		releaseChecked <- nil
	}
	type runResult struct {
		run kernel.Run
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		done <- runResult{run: run, err: err}
	}()
	select {
	case err := <-releaseChecked:
		if err != nil {
			t.Fatal(err)
		}
	case result := <-done:
		t.Fatalf("RunNext ended before provider release proof: %+v", result)
	case <-time.After(8 * time.Second):
		t.Fatal("provider release proof timed out")
	}
	if err := waitForWitness(fixture.childReceipt, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil || len(snapshot.HumanRequests) != 1 {
		t.Fatalf("durable human requests=%+v err=%v", snapshot.HumanRequests, err)
	}
	request := snapshot.HumanRequests[0]
	if request.Status != kernel.HumanRequestOpen || request.Revision.Int64() != 1 {
		t.Fatalf("durable human request=%+v", request)
	}
	public := fmt.Sprintf("%+v", snapshot)
	for _, private := range []string{key, question, fixture.spec.FactoryctlExecutable, fixture.spec.AttemptSocket} {
		if strings.Contains(public, private) {
			t.Fatalf("private provider request detail crossed public formatting: %q", public)
		}
	}
	if err := os.WriteFile(fixture.continueReceipt, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	var result runResult
	select {
	case result = <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("RunNext did not finish")
	}
	if result.err != nil {
		t.Fatalf("RunNext: %v", result.err)
	}
	fixture.assertTerminal(t, result.run, kernel.OutcomeSucceeded)
	fixture.assertOneWitness(t)
	durable, found, err := fixture.store.HumanRequest(context.Background(), request.ID)
	if err != nil || !found || durable.ID != request.ID || durable.Status != kernel.HumanRequestStale {
		t.Fatalf("final durable human request=%+v found=%v err=%v", durable, found, err)
	}
}

func TestSupervisorRevalidatesFactoryctlImmediatelyBeforeProviderRelease(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorHumanRequestProgram(t, "0123456789abcdef0123456789abcdef", "private-replacement-question"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.root, "factoryctl")
	copySupervisorExecutable(t, executable, target)
	fixture.spec.FactoryctlExecutable = target
	released := false
	fixture.spec.beforeProviderRelease = func() {
		replacement := filepath.Join(fixture.root, "factoryctl.replacement")
		copySupervisorExecutable(t, executable, replacement)
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
	}
	fixture.spec.afterProviderRelease = func() error {
		released = true
		return errors.New("replacement crossed provider release")
	}
	if _, err := fixture.daemon.RunNext(context.Background(), fixture.spec); err == nil {
		t.Fatal("replaced factoryctl reached provider release")
	} else if strings.Contains(err.Error(), target) {
		t.Fatalf("private factoryctl locator leaked: %v", err)
	}
	if released {
		t.Fatal("replaced factoryctl crossed StageProvider release")
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider witness exists after factoryctl replacement: %v", err)
	}
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil || len(snapshot.HumanRequests) != 0 {
		t.Fatalf("replacement created a human request: %+v, %v", snapshot.HumanRequests, err)
	}
}

func TestSupervisorRecordsInvalidFactoryctlAfterAdmissionWithoutProviderEffect(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		locator func(*testing.T, *supervisorFixture) string
	}{
		{name: "relative", locator: func(*testing.T, *supervisorFixture) string { return "factoryctl" }},
		{name: "missing", locator: func(t *testing.T, fixture *supervisorFixture) string {
			return filepath.Join(fixture.root, "missing-factoryctl")
		}},
		{name: "symlink", locator: func(t *testing.T, fixture *supervisorFixture) string {
			link := filepath.Join(fixture.root, "factoryctl.link")
			if err := os.Symlink(executable, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "unsafe mode", locator: func(t *testing.T, fixture *supervisorFixture) string {
			target := filepath.Join(fixture.root, "factoryctl.unsafe")
			copySupervisorExecutable(t, executable, target)
			if err := os.Chmod(target, 0o775); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{name: "non native", locator: func(t *testing.T, fixture *supervisorFixture) string {
			target := filepath.Join(fixture.root, "factoryctl.text")
			if err := os.WriteFile(target, []byte("not a native executable"), 0o700); err != nil {
				t.Fatal(err)
			}
			return target
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupervisorFixture(t, supervisorHumanRequestProgram(t, "0123456789abcdef0123456789abcdef", "private-invalid-question"))
			locator := test.locator(t, fixture)
			fixture.spec.FactoryctlExecutable = locator
			run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if err == nil || strings.Contains(err.Error(), locator) {
				t.Fatalf("invalid factoryctl result=%v", err)
			}
			fixture.trackRun(run.ID)
			if run.Phase != kernel.RunTerminal || run.CredentialRevokedAt == nil || run.Proposal == nil || run.Proposal.Kind() != kernel.OutcomeFailed || run.Proposal.Code() != kernel.FailureSpawn || run.Terminal == nil {
				t.Fatalf("invalid factoryctl durable run = %+v", run)
			}
			fixture.assertReleased(t, run)
			if _, statErr := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid factoryctl runtime remains: %v", statErr)
			}
			if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("provider witness exists: %v", err)
			}
			snapshot, err := fixture.store.Snapshot(context.Background())
			if err != nil || snapshot.Factory.ActiveRuns != 0 || len(snapshot.HumanRequests) != 0 {
				t.Fatalf("invalid factoryctl admitted state: active=%d requests=%+v err=%v", snapshot.Factory.ActiveRuns, snapshot.HumanRequests, err)
			}
		})
	}
}

func TestSupervisorUsesFrozenRunLaunchControlsAfterAdmission(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.afterAdmission = func() error {
		return replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "post-admission-model", "high")
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Provider != kernel.ProviderShell || run.Model != "" || run.ReasoningEffort != "" {
		t.Fatalf("frozen run controls = provider=%s model=%q effort=%q", run.Provider, run.Model, run.ReasoningEffort)
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorRecordsMissingNativeToolAfterAdmission(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.trackRun(run.ID)
	if run.Provider != kernel.ProviderCodex || run.Phase != kernel.RunTerminal || run.CredentialRevokedAt == nil || run.Proposal == nil || run.Proposal.Code() != kernel.FailureProviderExit || run.Terminal == nil {
		if run.Proposal == nil {
			t.Fatalf("missing native tool durable run = %+v", run)
		}
		t.Fatalf("missing native tool durable run provider=%s phase=%s code=%s detail=%q", run.Provider, run.Phase, run.Proposal.Code(), run.Proposal.Detail())
	}
	fixture.assertReleased(t, run)
	if _, statErr := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unavailable provider runtime remains: %v", statErr)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable provider reached execution: %v", err)
	}
}

func replaceSupervisorAgentLaunchControls(path string, agentID kernel.AgentID, providerKind kernel.Provider, model, effort string) error {
	database, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		return err
	}
	defer database.Close()
	var modelValue, effortValue any
	if model != "" {
		modelValue = model
	}
	if effort != "" {
		effortValue = effort
	}
	result, err := database.Exec(`UPDATE agents SET provider = ?, model = ?, reasoning_effort = ? WHERE id = ?`, providerKind.String(), modelValue, effortValue, agentID.Bytes())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("mutated agent rows = %d", rows)
	}
	return nil
}

func TestFailRunSharesOperationGateWithTerminalEffects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := createTestStore(ctx, filepath.Join(root, "kernel.sqlite"), kernel.FactoryConfig{Capacity: 1}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectID := supervisorProjectID(t, 210)
	agentID := supervisorAgentID(t, 211)
	taskID := supervisorTaskID(t, 212)
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "gate-project", Root: filepath.Join(root, "source")}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "gate-agent", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 1}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agentID, IncarnationID: supervisorIncarnationID(t, 213), Title: "gate-task"}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	factory, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDispatch(ctx, factory.Revision, true, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	runID, err := kernel.RunIDFromBytes(supervisorIDBytes(214))
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := kernel.TerminalSessionIDFromBytes(supervisorIDBytes(215))
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256([]byte("gate-attempt"))
	digest, err := kernel.AttemptDigestFromBytes(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	resource := func(seed byte) kernel.ResourceID {
		id, idErr := kernel.ResourceIDFromBytes(supervisorIDBytes(seed))
		if idErr != nil {
			t.Fatal(idErr)
		}
		return id
	}
	candidateChange, err := kernel.ChangeIDFromBytes(supervisorIDBytes(220))
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.AdmitNext(ctx, kernel.AdmissionKeys{
		RunID: runID, TerminalSessionID: sessionID, AttemptDigest: digest, CandidateChangeID: candidateChange,
		Resources:   kernel.AdmissionResourceIDs{RuntimeRoot: resource(216), RunnerProcess: resource(217), ProviderProcess: resource(218), ProviderGroup: resource(219)},
		RuntimeRoot: filepath.Join(root, "runtime"),
	}, supervisorTime())
	if err != nil || !admission.Admitted() || admission.Run == nil {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	// This is the live-owner failure edge, not the runtime-absent edge.
	runtime, found, err := store.Resource(ctx, resource(216))
	if err != nil || !found {
		t.Fatalf("runtime resource: found=%v err=%v", found, err)
	}
	runtimeIdentity, err := kernel.NewPathResourceIdentity(100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateResource(ctx, admission.Run.ID, runtime.ID, runtime.Revision, runtimeIdentity, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	daemon, err := newDaemon(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	daemon.operationMu.Lock()
	finished := make(chan struct{})
	failureCause := errors.New("infrastructure failure")
	var failed kernel.Run
	var failErr error
	go func() {
		failed, failErr = daemon.failRun(*admission.Run, kernel.FailureInternal, failureCause)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("failRun crossed the terminal operation gate")
	case <-time.After(50 * time.Millisecond):
	}
	observed, found, readErr := store.Run(ctx, runID)
	if readErr != nil || !found || observed.Phase != kernel.RunAdmitted {
		t.Fatalf("run changed while operation gate held: run=%+v found=%v err=%v", observed, found, readErr)
	}
	daemon.operationMu.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("failRun did not finish after operation gate release")
	}
	if !errors.Is(failErr, failureCause) {
		t.Fatalf("failRun error = %v, want infrastructure failure", failErr)
	}
	if failed.Phase != kernel.RunFinalizing {
		t.Fatalf("failRun phase = %s, want finalizing", failed.Phase)
	}
}

func TestDaemonCloseActivelyCancelsPreReleaseSupervisor(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, true))
	entered := make(chan struct{})
	continueHook := make(chan struct{})
	fixture.spec.beforeProviderRelease = func() {
		close(entered)
		<-continueHook
	}
	runDone := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		runDone <- struct {
			run kernel.Run
			err error
		}{run: run, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("supervisor did not reach pre-release owner seam")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.daemon.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while pre-release owner was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueHook)
	var result struct {
		run kernel.Run
		err error
	}
	select {
	case result = <-runDone:
	case <-time.After(12 * time.Second):
		t.Fatal("canceled supervisor did not return")
	}
	if result.err == nil || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled supervisor error = %v, want visible context cancellation", result.err)
	}
	select {
	case closeErr := <-closeDone:
		if closeErr == nil || !errors.Is(closeErr, context.Canceled) {
			t.Fatalf("Close error = %v, want joined context cancellation", closeErr)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Close did not join canceled supervisor")
	}
	fixture.assertRecoveredAfterClose(t, result.run)
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider executed before release: stat err=%v", err)
	}
}

func TestDaemonCloseActivelyCancelsBeforeLiveRegistration(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, true))
	entered := make(chan struct{})
	continueHook := make(chan struct{})
	fixture.spec.afterAdmission = func() error {
		close(entered)
		<-continueHook
		return nil
	}
	runDone := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		runDone <- struct {
			run kernel.Run
			err error
		}{run: run, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("supervisor did not reach post-admission/pre-live seam")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.daemon.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while pre-live owner was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueHook)
	var result struct {
		run kernel.Run
		err error
	}
	select {
	case result = <-runDone:
	case <-time.After(8 * time.Second):
		t.Fatal("canceled pre-live supervisor did not return")
	}
	if result.err == nil || !errors.Is(result.err, context.Canceled) {
		t.Fatal("canceled pre-live supervisor unexpectedly succeeded")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr == nil || !errors.Is(closeErr, context.Canceled) {
			t.Fatalf("Close error = %v, want joined context cancellation", closeErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close did not join canceled pre-live supervisor")
	}
	fixture.assertRecoveredAfterClose(t, result.run)
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider executed after pre-live cancellation: stat err=%v", err)
	}
}

func TestSupervisorCompletionBeforeProviderExitKeepsFirstOutcome(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, false))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.ProviderExit == nil || run.RunnerExit == nil {
		t.Fatalf("process exit evidence missing: provider=%+v runner=%+v", run.ProviderExit, run.RunnerExit)
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorDoesNotReleaseAfterFinalizationWinsBeforeProviderRelease(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.beforeProviderStateCheck = func() error {
		recoverable, err := fixture.store.RecoverableRuns(context.Background())
		if err != nil {
			return err
		}
		if len(recoverable) != 1 {
			return fmt.Errorf("recoverable run count = %d, want one", len(recoverable))
		}
		proposal, err := kernel.NewSuccessProposal("outcome-before-release")
		if err != nil {
			return err
		}
		_, err = fixture.store.ProposeAttemptOutcome(context.Background(), recoverable[0].Run.CredentialDigest, proposal, supervisorTime())
		return err
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil || !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("release after finalization error = %v, want conflict", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != "outcome-before-release" {
		t.Fatalf("run after finalization-before-release = %+v", run)
	}
	fixture.assertReleased(t, run)
	if _, statErr := os.Stat(fixture.witness); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("provider crossed finalization-before-release boundary: %v", statErr)
	}
}

func TestSupervisorProviderExitWithoutTypedOutcomeFails(t *testing.T) {
	fixture := newSupervisorFixture(t, providerExitWithoutOutcomeProgram(t))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Proposal == nil || run.Proposal.Code() != kernel.FailureProviderExit {
		t.Fatalf("zero exit became lifecycle authority: %+v", run.Proposal)
	}
	late, _ := kernel.NewSuccessProposal("too late")
	if _, err := fixture.store.ProposeAttemptOutcome(context.Background(), run.CredentialDigest, late, supervisorTime()); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("completion after provider exit = %v", err)
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorPersistsOuterRunnerExitNotProviderExit(t *testing.T) {
	fixture := newSupervisorFixture(t, providerExitAfterSuccessProgram(t, 7))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.ProviderExit == nil || run.RunnerExit == nil {
		t.Fatalf("process exit evidence missing: provider=%+v runner=%+v", run.ProviderExit, run.RunnerExit)
	}
	providerCode, providerCodeOK := run.ProviderExit.Code()
	runnerCode, runnerCodeOK := run.RunnerExit.Code()
	if !providerCodeOK || providerCode != 7 || !runnerCodeOK || runnerCode != 0 {
		t.Fatalf("durable exits provider=%+v runner=%+v, want provider 7 and outer factory-runner 0", run.ProviderExit, run.RunnerExit)
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorRereadsStoreAfterDirectOutcome(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, true))
	// Keep the provider alive without an API request. This goroutine owns only
	// the test observation and is joined before the assertion returns.
	watchCtx, cancelWatch := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		deadline := time.Now().Add(12 * time.Second)
		for {
			if _, err := os.Stat(fixture.witness); err == nil {
				runs, readErr := fixture.store.RecoverableRuns(context.Background())
				if readErr != nil || len(runs) != 1 {
					done <- errors.Join(readErr, fmt.Errorf("recoverable run count %d", len(runs)))
					return
				}
				proposal, _ := kernel.NewSuccessProposal("direct-store-success")
				_, writeErr := fixture.store.ProposeAttemptOutcome(context.Background(), runs[0].Run.CredentialDigest, proposal, supervisorTime())
				done <- writeErr
				return
			}
			if time.Now().After(deadline) {
				done <- errors.New("provider witness timeout")
				return
			}
			select {
			case <-watchCtx.Done():
				done <- watchCtx.Err()
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		cancelWatch()
		<-finished
	})
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	if proposalErr := <-done; proposalErr != nil {
		t.Fatal(proposalErr)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.Proposal == nil || run.Proposal.Result() != "direct-store-success" {
		t.Fatalf("dropped-wake proposal = %+v", run.Proposal)
	}
}

func TestSupervisorCleanupUncertaintyBlocksTerminal(t *testing.T) {
	fixture := newSupervisorFixture(t, cleanupFailureProgram(t))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("unsafe runtime cleanup unexpectedly succeeded")
	}
	if run.Phase != kernel.RunFinalizing || run.Terminal != nil {
		t.Fatalf("cleanup uncertainty terminalized run: %+v", run)
	}
	runtimeRoot := func() kernel.Resource {
		for _, resource := range fixture.resources(t, run.ID) {
			if resource.Kind == kernel.ResourceRuntimeRoot {
				return resource
			}
		}
		t.Fatal("runtime resource missing")
		return kernel.Resource{}
	}
	if state := runtimeRoot().State; state != kernel.ResourceUnresolved {
		t.Fatalf("runtime cleanup state = %s", state.String())
	}
	// The startup sweep tries the cleanup again. While the name is still
	// refused the run stays as it is, and while something alive holds the
	// runtime's lifetime lease the sweep concludes nothing; once a person
	// has made the name removable, the sweep removes the runtime, releases
	// it and settles the run.
	runtimePath := filepath.Join(fixture.runtimeParentPath, run.ID.String())
	unsafe := filepath.Join(runtimePath, "tmp", "unsafe")
	sweep := func(want RecoveredRunAction) {
		t.Helper()
		dispositions, sweepErr := fixture.daemon.RecoverAbandonedRuns(context.Background(), fixture.runtimeParent, fixture.changeParent)
		if sweepErr != nil || len(dispositions) != 1 || dispositions[0].Action != want {
			t.Fatalf("sweep = %+v, %v; want %s", dispositions, sweepErr, want)
		}
	}
	unchanged := func() {
		t.Helper()
		if current, _, _ := fixture.store.Run(context.Background(), run.ID); current.Phase != kernel.RunFinalizing || runtimeRoot().State != kernel.ResourceUnresolved {
			t.Fatalf("sweep settled a run whose runtime is still refused or held: %+v", current)
		}
	}
	sweep(RecoveredUncertain)
	unchanged()
	lease, err := os.OpenFile(filepath.Join(runtimePath, runner.RuntimeLifetimeLeaseName), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o600); err != nil {
		t.Fatal(err)
	}
	sweep(RecoveredLiveHolder)
	unchanged()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	sweep(RecoveredConverged)
	current, _, err := fixture.store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertTerminal(t, current, kernel.OutcomeSucceeded)
	if state := runtimeRoot().State; state != kernel.ResourceReleased {
		t.Fatalf("runtime state after sweep = %s", state.String())
	}
	if _, statErr := os.Lstat(filepath.Dir(filepath.Dir(unsafe))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runtime still present after sweep: %v", statErr)
	}
}

func TestSupervisorCancellationStillJoinsAndCleansOwnedProcesses(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, true))
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(12 * time.Second)
		for {
			if _, err := os.Stat(fixture.witness); err == nil {
				cancel()
				cancelled <- nil
				return
			}
			if time.Now().After(deadline) {
				cancelled <- errors.New("provider witness timeout")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	run, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if cancelErr := <-cancelled; cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunNext cancellation = %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
}

func TestSupervisorSourceFailureCannotReleaseProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.BaseRevision = "refs/heads/does-not-exist"
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("invalid source revision unexpectedly ran")
	}
	if run.Phase != kernel.RunTerminal || run.Terminal == nil {
		t.Fatalf("source failure run = %+v", run)
	}
	fixture.assertReleased(t, run)
	if _, statErr := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source failure runtime remains: %v", statErr)
	}
	session, found, sessionErr := fixture.store.TerminalSessionForRun(context.Background(), run.ID)
	if sessionErr != nil || !found || session.State != kernel.TerminalSessionClosed {
		t.Fatalf("source failure session = %+v found=%v err=%v", session, found, sessionErr)
	}
	if _, statErr := os.Stat(fixture.witness); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("provider executed before source registration: %v", statErr)
	}
	for _, resource := range fixture.resources(t, run.ID) {
		if resource.Kind == kernel.ResourceRuntimeRoot {
			continue
		}
		if resource.Identity.Empty() {
			continue
		}
		identity, identityErr := runnerIdentity(resource.Identity)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if observation := runner.ObserveProcess(identity); observation.Presence != runner.Absent {
			t.Fatalf("source failure left %s alive: %+v", resource.Kind.String(), observation)
		}
	}
}

func TestSupervisorPartialProviderReleaseRevokesAuthority(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, true))
	releaseErr := errors.New("injected ambiguous provider release")
	fixture.spec.afterProviderRelease = func() error {
		// The release frame is consumed only when the real provider creates this
		// witness. Return the injected acknowledgement loss after that external
		// effect, rather than relying on scheduling around the supervisor return.
		if err := waitForWitness(fixture.witness, 8*time.Second); err != nil {
			return err
		}
		return releaseErr
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, releaseErr) {
		t.Fatalf("RunNext partial release = %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Proposal == nil || run.Proposal.Code() != kernel.FailureProtocol || run.CredentialRevokedAt == nil {
		t.Fatalf("partial release run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("partial release credential = %v", err)
	}
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, run)
}

func TestSupervisorCancellationAfterProviderReleaseStillJoins(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, false))
	ctx, cancel := context.WithCancel(context.Background())
	proposalObserved := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(12 * time.Second)
		for {
			recoverable, err := fixture.store.RecoverableRuns(context.Background())
			if err != nil {
				cancel()
				proposalObserved <- err
				return
			}
			if len(recoverable) == 1 && recoverable[0].Run.Proposal != nil {
				// A durable typed success proves the provider consumed the real
				// release. Cancellation must not replace that first outcome.
				cancel()
				proposalObserved <- nil
				return
			}
			if time.Now().After(deadline) {
				cancel()
				proposalObserved <- errors.New("provider outcome timeout after release")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	result := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(ctx, fixture.spec)
		result <- struct {
			run kernel.Run
			err error
		}{run: run, err: err}
	}()
	var got struct {
		run kernel.Run
		err error
	}
	select {
	case got = <-result:
	case <-time.After(12 * time.Second):
		t.Fatal("post-release cancellation did not join")
	}
	if waitErr := <-proposalObserved; waitErr != nil {
		t.Fatal(waitErr)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("post-release cancellation = %v", got.err)
	}
	if got.run.Phase != kernel.RunTerminal || got.run.Proposal == nil || got.run.Proposal.Kind() != kernel.OutcomeSucceeded || got.run.Proposal.Result() != "typed-success" {
		t.Fatalf("post-release cancellation run = %+v", got.run)
	}
	fixture.assertOneWitness(t)
	fixture.assertReleased(t, got.run)
}

func TestSupervisorCancellationBeforeProviderReleaseKeepsProviderInert(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	ctx, cancel := context.WithCancel(context.Background())
	reached := make(chan struct{})
	proceed := make(chan struct{}, 1)
	fixture.spec.beforeProviderRelease = func() {
		close(reached)
		<-proceed
	}
	result := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(ctx, fixture.spec)
		result <- struct {
			run kernel.Run
			err error
		}{run: run, err: err}
	}()
	select {
	case <-reached:
	case <-time.After(12 * time.Second):
		cancel()
		proceed <- struct{}{}
		select {
		case <-result:
		case <-time.After(12 * time.Second):
		}
		t.Fatal("supervisor did not reach provider release barrier")
	}
	cancel()
	proceed <- struct{}{}
	var got struct {
		run kernel.Run
		err error
	}
	select {
	case got = <-result:
	case <-time.After(12 * time.Second):
		t.Fatal("cancelled supervisor did not join")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("pre-release cancellation = %v", got.err)
	}
	fixture.assertTerminal(t, got.run, kernel.OutcomeFailed)
	if got.run.CredentialRevokedAt == nil {
		t.Fatalf("pre-release cancellation run = %+v", got.run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), got.run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("pre-release cancellation credential = %v", err)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider crossed cancellation/release boundary: %v", err)
	}
	fixture.assertReleased(t, got.run)
}

func TestSupervisorActivationErrorAfterDurableMarkerJoinsInnerOwner(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	activationErr := errors.New("injected activation acknowledgement loss")
	var observedInner runner.Identity
	fixture.spec.activateOuter = func(child *runner.OwnedChild) (runner.FileIdentity, error) {
		marker, err := child.Activate()
		if err != nil {
			return marker, err
		}
		observedInner = supervisorWaitForDirectChild(t, child.Identity())
		// The exact inner receipt proves activation occurred. On the injected
		// acknowledgement loss, controller EOF makes the outer converge that
		// distinct group before FinishAfterExit returns; killing outer first is unsafe.
		return marker, activationErr
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, activationErr) {
		t.Fatalf("RunNext activation ambiguity = %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.CredentialRevokedAt == nil {
		t.Fatalf("activation ambiguity run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("activation ambiguity credential = %v", err)
	}
	// The converged evidence is the consumed result, not a terminal spool: the
	// run carries the exact provider exit, the released pair carries the exact
	// inner identity, and the consumed artifact has been removed.
	if run.Proposal.Code() != kernel.FailureActivation || run.ProviderExit == nil {
		t.Fatalf("activation ambiguity evidence = proposal %+v exit %+v", run.Proposal, run.ProviderExit)
	}
	runtimePath := filepath.Join(fixture.runtimeParentPath, run.ID.String())
	if _, statErr := os.Stat(filepath.Join(runtimePath, runner.AttemptResultSpoolName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("consumed attempt result was not removed: %v", statErr)
	}
	if observation := runner.ObserveProcess(observedInner); observation.Presence != runner.Absent {
		t.Fatalf("activation ambiguity left inner owner alive: %+v", observation)
	}
	for _, resource := range fixture.resources(t, run.ID) {
		switch resource.Kind {
		case kernel.ResourceProviderProcess:
			identity, identityErr := runnerIdentity(resource.Identity)
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			if resource.State != kernel.ResourceReleased || identity != observedInner {
				t.Fatalf("released provider = %+v, observed inner %+v", resource, observedInner)
			}
		case kernel.ResourceRunnerProcess:
			identity, identityErr := runnerIdentity(resource.Identity)
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			if resource.State != kernel.ResourceReleased {
				t.Fatalf("released runner = %+v", resource)
			}
			if observation := runner.ObserveProcess(identity); observation.Presence != runner.Absent {
				t.Fatalf("activation ambiguity left outer alive: %+v", observation)
			}
		}
	}
}

func TestSupervisorReconcilesAmbiguousAdmissionAndRevokesBearer(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	commitErr := errors.New("injected lost admission commit acknowledgement")
	var admissions []bool
	fixture.spec.admissionObserved = func(admitted bool) { admissions = append(admissions, admitted) }
	fixture.spec.afterAdmission = func() error { return commitErr }
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, commitErr) {
		t.Fatalf("RunNext admission ambiguity = %v", err)
	}
	if len(admissions) != 1 || !admissions[0] {
		t.Fatalf("durable admission observations = %v", admissions)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Proposal == nil || run.Proposal.Code() != kernel.FailureInternal || run.CredentialRevokedAt == nil {
		t.Fatalf("admission ambiguity run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("ambiguous admission bearer = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runtimeParentPath, run.ID.String())); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ambiguous admission created runtime: %v", statErr)
	}
	if _, statErr := os.Stat(fixture.witness); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ambiguous admission executed provider: %v", statErr)
	}
	// The runtime was never created, so the pre-runtime failure edge releases
	// every declared resource atomically instead of leaving releasing residue.
	for _, resource := range fixture.resources(t, run.ID) {
		if resource.State != kernel.ResourceReleased || !resource.Identity.Empty() {
			t.Fatalf("ambiguous admission resource = %+v", resource)
		}
	}
}

func TestSupervisorDoesNotRepeatNoAdmissionObservationAfterHookFailure(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	factory, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetDispatch(context.Background(), factory.Revision, false, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("injected no-admission hook failure")
	var admissions []bool
	fixture.spec.admissionObserved = func(admitted bool) { admissions = append(admissions, admitted) }
	fixture.spec.afterAdmission = func() error { return hookErr }
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, hookErr) || !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("RunNext no-admission hook failure = %v", err)
	}
	if run.ID != (kernel.RunID{}) || len(admissions) != 1 || admissions[0] {
		t.Fatalf("no-admission observations = run %+v observations %v", run, admissions)
	}
}

func TestSupervisorRetriesTransientAdmissionReconciliation(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	commitErr := errors.New("injected lost admission commit acknowledgement")
	transientErr := errors.New("injected transient reconciliation read")
	fixture.spec.afterAdmission = func() error { return commitErr }
	attempts := 0
	fixture.spec.reconcileAdmission = func(ctx context.Context, keys kernel.AdmissionKeys) (kernel.AdmissionResult, error) {
		attempts++
		if attempts < 3 {
			return kernel.AdmissionResult{}, transientErr
		}
		return fixture.store.ReconcileAdmission(ctx, keys)
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, commitErr) || attempts != 3 {
		t.Fatalf("RunNext reconciliation = attempts %d, err %v", attempts, err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.CredentialRevokedAt == nil {
		t.Fatalf("transient reconciliation run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("transient reconciliation bearer = %v", err)
	}
}

func TestSupervisorBoundsUnavailableAdmissionReconciliation(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	commitErr := errors.New("injected lost admission commit acknowledgement")
	fixture.spec.afterAdmission = func() error {
		return errors.Join(commitErr, fixture.store.Close())
	}
	started := time.Now()
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	elapsed := time.Since(started)
	var unknown *kernel.OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, commitErr) {
		t.Fatalf("permanent admission reconciliation = %v", err)
	}
	if run.Phase.String() != "" || run.Revision.Int64() != 0 {
		t.Fatalf("unknown admission returned a claimed durable run = %+v", run)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("permanent admission reconciliation took %s", elapsed)
	}
	fixture.reopenStore(t)
	recoverable, err := fixture.store.RecoverableRuns(context.Background())
	if err != nil || len(recoverable) != 1 || recoverable[0].Run.Phase != kernel.RunAdmitted {
		t.Fatalf("admission recovery handoff = %+v, %v", recoverable, err)
	}
	for _, resource := range recoverable[0].Resources {
		if resource.State != kernel.ResourceDeclared || !resource.Identity.Empty() {
			t.Fatalf("unknown admission resource = %+v", resource)
		}
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown admission executed provider: %v", err)
	}
}

func TestSupervisorBoundsUnavailableFailureHandoffAndJoinsOwner(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	ctx, cancel := context.WithCancel(context.Background())
	var closeErr error
	fixture.spec.beforeProviderRelease = func() {
		closeErr = fixture.store.Close()
		cancel()
	}
	started := time.Now()
	run, err := fixture.daemon.RunNext(ctx, fixture.spec)
	elapsed := time.Since(started)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var unknown *kernel.OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("permanent failure reconciliation = %v", err)
	}
	if run.Phase.String() != "" || run.Revision.Int64() != 0 {
		t.Fatalf("unknown revocation returned stale run = %+v", run)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("permanent failure reconciliation took %s", elapsed)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider crossed unknown pre-release handoff: %v", err)
	}
	fixture.reopenStore(t)
	recoverable, err := fixture.store.RecoverableRuns(context.Background())
	if err != nil || len(recoverable) != 1 || recoverable[0].Run.Phase != kernel.RunRunning {
		t.Fatalf("failure recovery handoff = %+v, %v", recoverable, err)
	}
	for _, resource := range recoverable[0].Resources {
		if resource.Kind == kernel.ResourceRuntimeRoot {
			continue
		}
		identity, identityErr := runnerIdentity(resource.Identity)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if observation := runner.ObserveProcess(identity); observation.Presence != runner.Absent {
			t.Fatalf("unknown failure left %s alive: %+v", resource.Kind.String(), observation)
		}
	}
	failure, _ := kernel.NewFailureProposal(kernel.FailureInternal, "recovery handoff")
	failed, err := fixture.store.FailRun(context.Background(), recoverable[0].Run.ID, recoverable[0].Run.Revision, failure, supervisorTime())
	if err != nil || failed.Phase != kernel.RunFinalizing || failed.CredentialRevokedAt == nil {
		t.Fatalf("recovery convergence = %+v, %v", failed, err)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), failed.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("recovery did not revoke credential: %v", err)
	}
}

func TestSupervisorReapsProviderDescendant(t *testing.T) {
	fixture := newSupervisorFixture(t, descendantProgram(t))
	ctx, cancel := context.WithCancel(context.Background())
	type runResult struct {
		run kernel.Run
		err error
	}
	done := make(chan runResult, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		run, err := fixture.daemon.RunNext(ctx, fixture.spec)
		done <- runResult{run: run, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(fixture.continueReceipt, []byte("cleanup"), 0o600)
		select {
		case <-joined:
		case <-time.After(12 * time.Second):
			t.Error("descendant supervisor owner did not join during safety cleanup")
		}
	})
	childPID := supervisorWaitForPIDReceipt(t, fixture.childReceipt)
	child := supervisorIdentityForPID(t, childPID)
	if observation := runner.ObserveProcess(child); observation.Presence != runner.Present {
		t.Fatalf("provider descendant was not live at cleanup boundary: %+v", observation)
	}
	if err := os.WriteFile(fixture.continueReceipt, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	var result runResult
	select {
	case result = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("descendant supervisor did not join")
	}
	if result.err != nil {
		t.Fatalf("descendant RunNext: %v", result.err)
	}
	fixture.assertTerminal(t, result.run, kernel.OutcomeSucceeded)
	if observation := runner.ObserveProcess(child); observation.Presence != runner.Absent {
		t.Fatalf("provider descendant remains: %+v", observation)
	}
}

func TestReleaseResourceRejectsForeignRunEvenWhenAlreadyReleased(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	resource := fixture.resources(t, run.ID)[0]
	foreign, err := kernel.RunIDFromBytes(supervisorIDBytes(99))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.daemon.releaseResource(context.Background(), foreign, resource.ID); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("foreign released-resource ownership = %v", err)
	}
}

type supervisorFixture struct {
	root, witness, childReceipt, continueReceipt string
	base, changeParent, runtimeParentPath        string
	storePath                                    string
	agentID                                      kernel.AgentID
	taskID                                       kernel.TaskID
	daemon                                       *Daemon
	store                                        *kernel.Store
	runtimeParent                                *RuntimeParent
	spec                                         SupervisorSpec
	listener                                     *api.Listener
	apiHome                                      *install.OperationalHome
	apiAuthority                                 *install.LocalAPIAuthority
	serverDone                                   chan error
	serverOnce                                   sync.Once
	runMu                                        sync.Mutex
	runIDs                                       map[kernel.RunID]struct{}
	t                                            *testing.T
	baselineFDs                                  int
}

func newSupervisorFixture(t *testing.T, program string) *supervisorFixture {
	t.Helper()
	return newSupervisorRoleFixture(t, program, kernel.RoleWorker)
}

func newSupervisorRoleFixture(t *testing.T, program string, role kernel.AgentRole) *supervisorFixture {
	t.Helper()
	baselineFDs := supervisorFDCount(t)
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &supervisorFixture{
		root: root, witness: filepath.Join(root, "provider.witness"),
		childReceipt: filepath.Join(root, "provider-child.pid"), continueReceipt: filepath.Join(root, "provider.continue"), t: t,
		baselineFDs: baselineFDs, runIDs: make(map[kernel.RunID]struct{}),
	}
	program = strings.ReplaceAll(program, "__WITNESS__", quoteShell(fixture.witness))
	program = strings.ReplaceAll(program, "__CHILD_RECEIPT__", quoteShell(fixture.childReceipt))
	program = strings.ReplaceAll(program, "__CONTINUE_RECEIPT__", quoteShell(fixture.continueReceipt))
	t.Cleanup(func() {
		fixture.close()
		_ = os.RemoveAll(root)
	})

	git := supervisorNativeGit(t)
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "init", repository)
	supervisorGit(t, git, "-C", repository, "config", "user.email", "test@example.invalid")
	supervisorGit(t, git, "-C", repository, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("exact source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supervisorGit(t, git, "-C", repository, "add", "payload.txt")
	supervisorGit(t, git, "-C", repository, "commit", "-m", "base")
	base := strings.TrimSpace(supervisorGitOutput(t, git, "-C", repository, "rev-parse", "HEAD"))
	fixture.base = base

	changeParent := filepath.Join(root, "changes")
	if err := os.Mkdir(changeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	apiHomePath := filepath.Join(root, "api-home")
	if socket := install.LocalAPISocketPath(apiHomePath); len(socket) > install.MaxSocketPathBytes {
		t.Fatalf("api socket path is %d bytes, over the %d-byte budget: %q", len(socket), install.MaxSocketPathBytes, socket)
	}
	if _, err := install.Init(context.Background(), apiHomePath); err != nil {
		t.Fatal(err)
	}
	operatorToken := filepath.Join(apiHomePath, "operator.token")
	if err := os.WriteFile(operatorToken, bytes.Repeat([]byte{'o'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	apiHome, err := install.OpenOperationalHome(context.Background(), apiHomePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.apiHome = apiHome
	runtimes, err := apiHome.Runtimes()
	if err != nil {
		t.Fatal(err)
	}
	runtimeParentPath := filepath.Join(apiHomePath, "runtimes")
	runtimeParent, err := OpenRuntimeParent(context.Background(), runtimes, runtimeParentPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtimeParent, fixture.runtimeParentPath, fixture.changeParent = runtimeParent, runtimeParentPath, changeParent

	storePath := filepath.Join(root, "factory.sqlite3")
	store, err := createTestStore(context.Background(), storePath, kernel.FactoryConfig{Capacity: 1}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	fixture.storePath = storePath
	projectID := supervisorProjectID(t, 1)
	agentID := supervisorAgentID(t, 2)
	fixture.agentID = agentID
	taskID := supervisorTaskID(t, 3)
	fixture.taskID = taskID
	project, err := store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "project", Root: repository, VerificationPolicy: kernel.VerificationNone}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(context.Background(), kernel.NewAgent{
		ID: agentID, ProjectID: project.ID, Name: role.String(), Role: role,
		Provider: kernel.ProviderShell, ToolBudgetLimit: 20,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(context.Background(), kernel.NewTask{
		ID: taskID, ProjectID: project.ID, AssignedAgentID: agentID, IncarnationID: supervisorIncarnationID(t, 4),
		Title: "shell lifecycle", Body: program, Priority: 1,
	}, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	factory, err := store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDispatch(context.Background(), factory.Revision, true, supervisorTime()); err != nil {
		t.Fatal(err)
	}

	daemon, err := NewDaemon(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon = daemon
	authority, err := apiHome.OpenLocalAPI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.apiAuthority = authority
	listener, err := api.Listen(authority)
	if err != nil {
		t.Fatal(err)
	}
	socket := install.LocalAPISocketPath(apiHomePath)
	fixture.listener = listener
	fixture.serverDone = make(chan error, 1)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				fixture.serverDone <- acceptErr
				return
			}
			if handleErr := daemon.HandleConnection(context.Background(), connection); handleErr != nil {
				fixture.serverDone <- handleErr
				return
			}
		}
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	factoryctl, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	fixture.spec = SupervisorSpec{
		RuntimeParent: runtimeParent, ChangeParent: changeParent,
		GitExecutable: git, BaseRevision: base, AttemptSocket: socket, RunnerExecutable: executable, FactoryctlExecutable: factoryctl,
		ToolPath: filepath.Join(runtime.GOROOT(), "bin") + ":/usr/bin:/bin", AccountHome: root,
	}
	return fixture
}

func (fixture *supervisorFixture) close() {
	if fixture == nil {
		return
	}
	fixture.serverOnce.Do(func() {
		if fixture.listener != nil {
			_ = fixture.listener.Close()
			select {
			case <-fixture.serverDone:
			case <-time.After(3 * time.Second):
				fixture.t.Errorf("API accept owner did not join")
			}
		}
		if fixture.apiAuthority != nil {
			if err := fixture.apiAuthority.Close(); err != nil {
				fixture.t.Errorf("API authority close: %v", err)
			}
			fixture.apiAuthority = nil
		}
		fixture.hardSafetyCleanup()
		if fixture.runtimeParent != nil {
			if err := fixture.runtimeParent.Close(); err != nil {
				fixture.t.Errorf("runtime parent close: %v", err)
			}
		}
		if fixture.apiHome != nil {
			if err := fixture.apiHome.Close(); err != nil {
				fixture.t.Errorf("API home close: %v", err)
			}
			fixture.apiHome = nil
		}
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				fixture.t.Errorf("Store close: %v", err)
			}
		}
		fixture.assertFDCensus()
	})
}

func (fixture *supervisorFixture) hardSafetyCleanup() {
	if fixture.store == nil {
		return
	}
	runs, err := fixture.store.RecoverableRuns(context.Background())
	if err != nil {
		fixture.t.Errorf("hard safety recovery read: %v", err)
		return
	}
	identities := make(map[runner.Identity]struct{})
	fixture.runMu.Lock()
	runIDs := make([]kernel.RunID, 0, len(fixture.runIDs))
	for runID := range fixture.runIDs {
		runIDs = append(runIDs, runID)
	}
	fixture.runMu.Unlock()
	for _, runID := range runIDs {
		resources, resourceErr := fixture.store.Resources(context.Background(), runID)
		if resourceErr != nil {
			fixture.t.Errorf("hard safety resource read: %v", resourceErr)
			continue
		}
		for _, resource := range resources {
			fixture.addSafetyIdentity(identities, resource)
		}
	}
	for _, recovered := range runs {
		for _, resource := range recovered.Resources {
			fixture.addSafetyIdentity(identities, resource)
		}
	}
	for identity := range identities {
		if observation := runner.ObserveProcess(identity); observation.Presence != runner.Present {
			continue
		}
		// Verify exact birth immediately before the test-only group kill. The
		// target comes only from this fixture's private Store, never a name scan.
		if observation := runner.ObserveProcess(identity); observation.Presence != runner.Present {
			continue
		}
		// Darwin can return EPERM for a zombie-only group. The errno proves
		// nothing: the bounded identity check below must still prove absence.
		if err := unix.Kill(-identity.PGID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.EPERM) {
			fixture.t.Errorf("hard safety kill %+v: %v", identity, err)
		}
	}
	deadline := time.Now().Add(4 * time.Second)
	for identity := range identities {
		for {
			observation := runner.ObserveProcess(identity)
			if observation.Presence == runner.Absent || observation.Presence == runner.Reused {
				break
			}
			if time.Now().After(deadline) {
				fixture.t.Errorf("hard safety residual %+v: %+v", identity, observation)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (fixture *supervisorFixture) addSafetyIdentity(identities map[runner.Identity]struct{}, resource kernel.Resource) {
	if resource.Kind == kernel.ResourceRuntimeRoot || resource.Identity.Empty() {
		return
	}
	identity, err := runnerIdentity(resource.Identity)
	if err != nil {
		fixture.t.Errorf("hard safety identity: %v", err)
		return
	}
	identities[identity] = struct{}{}
}

func (fixture *supervisorFixture) assertFDCensus() {
	deadline := time.Now().Add(4 * time.Second)
	for {
		fds := supervisorFDCount(fixture.t)
		if fds == fixture.baselineFDs {
			fixture.t.Logf("fixture fd census stable: fds=%d", fds)
			return
		}
		if time.Now().After(deadline) {
			fixture.t.Errorf("fixture fd census: %d -> %d", fixture.baselineFDs, fds)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (fixture *supervisorFixture) assertTerminal(t *testing.T, run kernel.Run, kind kernel.OutcomeKind) {
	t.Helper()
	fixture.trackRun(run.ID)
	if run.Phase != kernel.RunTerminal || run.Proposal == nil || run.Terminal == nil || run.Proposal.Kind() != kind || run.Terminal.Kind() != kind {
		t.Fatalf("terminal run = %+v", run)
	}
	task, found, err := fixture.store.Task(context.Background(), run.TaskID)
	if err != nil || !found {
		t.Fatalf("terminal task read = %+v, found=%v, err=%v", task, found, err)
	}
	want := kernel.TaskFailed
	switch kind {
	case kernel.OutcomeSucceeded:
		want = kernel.TaskSucceeded
	case kernel.OutcomeBlocked:
		want = kernel.TaskBlocked
	case kernel.OutcomeCancelled:
		want = kernel.TaskCancelled
	}
	if task.Status != want {
		t.Fatalf("task status = %s, want %s", task.Status.String(), want.String())
	}
}

// Close may cancel a durable writer wait after joining every process. The exact
// retained run must remain discoverable and converge through normal boot recovery.
func (fixture *supervisorFixture) assertRecoveredAfterClose(t *testing.T, run kernel.Run) {
	t.Helper()
	if run.ID == (kernel.RunID{}) {
		t.Fatal("shutdown lost admitted run identity")
	}
	recoveredDaemon, err := newDaemon(fixture.store, fixture.daemon.now)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredDaemon.Close()
	// Boot remembers the Git executable before the sweep runs, exactly as
	// cmd/factoryd does; a leftover retained-Change settlement must not need
	// a live attempt to have run first.
	recoveredDaemon.RememberSupervisorAccount(fixture.spec.ChangeParent, fixture.spec.AccountHome, fixture.spec.GitExecutable)
	if _, err := recoveredDaemon.RecoverAbandonedRuns(context.Background(), fixture.spec.RuntimeParent, fixture.spec.ChangeParent); err != nil {
		t.Fatal(err)
	}
	current, found, err := fixture.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("retained shutdown run: found=%v err=%v", found, err)
	}
	fixture.assertInterruptedTerminal(t, current)
}

func (fixture *supervisorFixture) assertInterruptedTerminal(t *testing.T, run kernel.Run) {
	t.Helper()
	if run.ID == (kernel.RunID{}) || run.Phase != kernel.RunTerminal || run.Terminal == nil || run.CredentialRevokedAt == nil || run.Proposal == nil {
		t.Fatalf("interrupted run = %+v, want revoked terminal state", run)
	}
	fixture.assertTerminal(t, run, run.Proposal.Kind())
	durable, found, err := fixture.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("durable interrupted run: found=%v err=%v", found, err)
	}
	if durable.Phase != kernel.RunTerminal || durable.Terminal == nil || durable.CredentialRevokedAt == nil || durable.Revision != run.Revision {
		t.Fatalf("durable interrupted run = %+v", durable)
	}
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("durable interrupted session: found=%v err=%v", found, err)
	}
	if session.State != kernel.TerminalSessionClosed || session.ClosedAt == nil {
		t.Fatalf("interrupted session = %+v, want closed", session)
	}
	fixture.assertReleased(t, run)
}

func (fixture *supervisorFixture) assertOneWitness(t *testing.T) {
	t.Helper()
	if err := exactOneWitness(fixture.witness); err != nil {
		t.Fatal(err)
	}
}

func (fixture *supervisorFixture) assertReleased(t *testing.T, run kernel.Run) {
	t.Helper()
	resources := fixture.resources(t, run.ID)
	if len(resources) != 4 {
		t.Fatalf("resource count = %d", len(resources))
	}
	for _, resource := range resources {
		if resource.State != kernel.ResourceReleased {
			t.Fatalf("resource %s state = %s", resource.Kind.String(), resource.State.String())
		}
		if resource.Kind != kernel.ResourceRuntimeRoot && !resource.Identity.Empty() {
			identity, err := runnerIdentity(resource.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if observation := runner.ObserveProcess(identity); observation.Presence != runner.Absent {
				t.Fatalf("released %s still present: %+v", resource.Kind.String(), observation)
			}
		}
	}
}

func (fixture *supervisorFixture) resources(t *testing.T, runID kernel.RunID) []kernel.Resource {
	t.Helper()
	fixture.trackRun(runID)
	resources, err := fixture.store.Resources(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return resources
}

func (fixture *supervisorFixture) trackRun(runID kernel.RunID) {
	fixture.runMu.Lock()
	fixture.runIDs[runID] = struct{}{}
	fixture.runMu.Unlock()
}

func (fixture *supervisorFixture) reopenStore(t *testing.T) {
	t.Helper()
	store, err := kernel.Open(context.Background(), fixture.storePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	fixture.daemon.store = store
}

func (fixture *supervisorFixture) changeName(t *testing.T, run kernel.Run) string {
	t.Helper()
	if run.ChangeID == nil {
		t.Fatal("worker run has no Change")
	}
	return run.ChangeID.String()
}

func queueSupervisorRetry(t *testing.T, fixture *supervisorFixture, terminal kernel.Run) {
	t.Helper()
	if terminal.TerminalAt == nil {
		t.Fatal("terminal retry predecessor has no terminal time")
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET work_revision = work_revision + 1, status = 'queued', blocked_reason = NULL, result = NULL, completed_at_ms = NULL, revision = revision + 1, updated_at_ms = ? WHERE id = ?`, terminal.TerminalAt.Int64()+1, terminal.TaskID.Bytes())
}

func execSupervisorSQL(t *testing.T, path, statement string, arguments ...any) {
	t.Helper()
	database, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

func supervisorTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func supervisorProgram(t *testing.T, waitAfterRequest, noRequest bool) string {
	t.Helper()
	executable := supervisorTestExecutable(t)
	request := ""
	if !noRequest {
		request = quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
	}
	wait := ""
	if waitAfterRequest {
		wait = "sleep 30\n"
	}
	return "set -eu\nprintf x >> __WITNESS__\n" + request + wait
}

func supervisorHumanRequestProgram(t *testing.T, key, question string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := "\"$DARK_FACTORY_FACTORYCTL\" attempt request-human --idempotency-key " + quoteShell(key) + " --question " + quoteShell(question) + "\n"
	toolPath := filepath.Join(runtime.GOROOT(), "bin") + ":/usr/bin:/bin"
	return "set -eu\n" +
		"test \"$PATH\" = " + quoteShell(toolPath) + "\n" +
		"case \"$DARK_FACTORY_FACTORYCTL\" in /*) ;; *) exit 83 ;; esac\n" +
		"test -x \"$DARK_FACTORY_FACTORYCTL\"\n" +
		"printf x >> __WITNESS__\n" + request + request +
		"printf ready > __CHILD_RECEIPT__\n" +
		"while [ ! -f __CONTINUE_RECEIPT__ ]; do sleep 0.01; done\n" +
		quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
}

func providerExitWithoutOutcomeProgram(t *testing.T) string {
	t.Helper()
	return "set -eu\nprintf x >> __WITNESS__\nexit 0\n"
}

func providerExitAfterSuccessProgram(t *testing.T, code int) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("set -eu\ntrap '' TERM\nprintf x >> __WITNESS__\nGORACE=atexit_sleep_ms=0 %s --supervisor-attempt-succeed typed-success\nexit %d\n", quoteShell(executable), code)
}

func descendantProgram(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The provider is intentionally interactive because it owns a PTY. Do not
	// use Bash history-sensitive `$!` syntax in this fixture; the deterministic
	// helper records its own PID while remaining in the provider's process
	// group, so this test still proves descendant cleanup without relying on
	// pipe-era shell semantics.
	helper := quoteShell(executable)
	return "set -eu\nprintf x >> __WITNESS__\n" +
		"DARK_FACTORY_CHILD_RECEIPT=__CHILD_RECEIPT__ DARK_FACTORY_CONTINUE_RECEIPT=__CONTINUE_RECEIPT__ " + helper + " --supervisor-descendant-provider\n" +
		"while [ ! -f __CONTINUE_RECEIPT__ ]; do sleep 0.01; done\n" +
		quoteShell(executable) + " --supervisor-attempt-succeed typed-success\nsleep 30\n"
}

func exactOneWitness(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("provider execution witness read: %w", err)
	}
	if string(body) != "x" {
		return fmt.Errorf("provider execution witness = %q, want exactly one append", body)
	}
	return nil
}

func waitForWitness(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("provider execution witness stat: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("provider execution witness timeout after %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExactOneWitnessRejectsDuplicateAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness")
	if err := os.WriteFile(path, []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exactOneWitness(path); err == nil {
		t.Fatal("duplicate execution append passed one-execution assertion")
	}
}

func cleanupFailureProgram(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "set -eu\nprintf x > __WITNESS__\nprintf x > \"$TMPDIR/unsafe\" && chmod 4600 \"$TMPDIR/unsafe\"\n" + quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
}

func quoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func copySupervisorExecutable(t testing.TB, from, to string) {
	t.Helper()
	source, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
}

func supervisorTime() kernel.UnixMillis {
	value, err := kernel.NewUnixMillis(time.Now().UnixMilli())
	if err != nil {
		panic(err)
	}
	return value
}

func supervisorIDBytes(seed byte) []byte { return bytes.Repeat([]byte{seed}, kernel.IDBytes) }
func supervisorProjectID(t *testing.T, seed byte) kernel.ProjectID {
	t.Helper()
	id, err := kernel.ProjectIDFromBytes(supervisorIDBytes(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func supervisorAgentID(t *testing.T, seed byte) kernel.AgentID {
	t.Helper()
	id, err := kernel.AgentIDFromBytes(supervisorIDBytes(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func supervisorTaskID(t *testing.T, seed byte) kernel.TaskID {
	t.Helper()
	id, err := kernel.TaskIDFromBytes(supervisorIDBytes(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func supervisorIncarnationID(t *testing.T, seed byte) kernel.IncarnationID {
	t.Helper()
	id, err := kernel.IncarnationIDFromBytes(supervisorIDBytes(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func supervisorNativeGit(t testing.TB) string {
	t.Helper()
	if _, err := os.Stat(change.TrustedGitExecutable); err != nil {
		t.Fatalf("Command Line Tools Git is unavailable: %v", err)
	}
	return change.TrustedGitExecutable
}

func supervisorGit(t testing.TB, git string, args ...string) {
	t.Helper()
	_ = supervisorGitOutput(t, git, args...)
}

func supervisorGitOutput(t testing.TB, git string, args ...string) string {
	t.Helper()
	command := exec.Command(git, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, body)
	}
	return string(body)
}

func supervisorWaitForDirectChild(t testing.TB, outer runner.Identity) runner.Identity {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.all", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if int(process.Eproc.Ppid) != outer.PID || process.Proc.P_stat == 5 {
				continue
			}
			identity := runner.Identity{
				PID: int(process.Proc.P_pid), PGID: int(process.Eproc.Pgid),
				Birth: runner.Birth{Seconds: process.Proc.P_starttime.Sec, Microseconds: process.Proc.P_starttime.Usec},
			}
			if identity.Valid() && identity.PID == identity.PGID {
				return identity
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("activated outer did not create an exact non-zombie inner group")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func supervisorWaitForPIDReceipt(t testing.TB, path string) int {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		body, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if parseErr != nil || pid < 1 {
				t.Fatalf("invalid child PID receipt %q: %v", body, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("provider child PID receipt timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func supervisorIdentityForPID(t testing.TB, pid int) runner.Identity {
	t.Helper()
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if int(process.Proc.P_pid) != pid || process.Proc.P_stat == 5 {
			continue
		}
		identity := runner.Identity{
			PID: pid, PGID: int(process.Eproc.Pgid),
			Birth: runner.Birth{Seconds: process.Proc.P_starttime.Sec, Microseconds: process.Proc.P_starttime.Usec},
		}
		if !identity.Valid() {
			t.Fatalf("invalid child identity %+v", identity)
		}
		return identity
	}
	t.Fatalf("child PID %d disappeared before exact identity capture", pid)
	return runner.Identity{}
}

func supervisorFDCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestSupervisorKeepsKnownRunWhenReturnedRecoveryIsCancelled(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.afterAdmission = func() error { return errors.New("admitted setup failure") }
	now := fixture.daemon.now
	calls := 0
	fixture.daemon.now = func() time.Time {
		calls++
		if calls == 3 {
			fixture.daemon.cleanupCancel()
		}
		return now()
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, context.Canceled) || run.ID == (kernel.RunID{}) || run.Phase != kernel.RunFinalizing {
		t.Fatalf("known finalizing run lost on cancelled recovery: run=%+v err=%v", run, err)
	}
	current, found, readErr := fixture.store.Run(context.Background(), run.ID)
	if readErr != nil || !found || current.ID != run.ID || current.Phase != kernel.RunFinalizing {
		t.Fatalf("retained run: %+v found=%v err=%v", current, found, readErr)
	}
}
