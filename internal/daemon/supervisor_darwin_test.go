//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "--exec-gate":
			err = runner.RunExecGate()
		case "--attempt-runner":
			err = runner.RunAttemptRunner()
		case "--change-worker-shell":
			err = changeworker.RunShell(context.Background())
		case "--supervisor-attempt-succeed":
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
			_, err = client.Succeed(ctx, os.Args[2])
			cancel()
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

func TestSupervisorRunsRegisteredShellWorkerToTypedSuccess(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
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

func TestSupervisorCompletionBeforeProviderExitKeepsFirstOutcome(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, false))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	if run.RunnerExit == nil {
		t.Fatal("provider exit evidence missing")
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorProviderExitWithoutTypedOutcomeFails(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, true))
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeFailed)
	if run.Proposal == nil || run.Proposal.Code() != kernel.FailureRunnerExit {
		t.Fatalf("zero exit became lifecycle authority: %+v", run.Proposal)
	}
	late, _ := kernel.NewSuccessProposal("too late")
	if _, err := fixture.store.ProposeAttemptOutcome(context.Background(), run.CredentialDigest, late, supervisorTime()); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("completion after provider exit = %v", err)
	}
	fixture.assertOneWitness(t)
}

func TestSupervisorRereadsStoreWhenWakeIsDropped(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, true, true))
	// Keep the provider alive without an API request. This goroutine owns only
	// the test observation and is joined before the assertion returns.
	done := make(chan error, 1)
	go func() {
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
			time.Sleep(10 * time.Millisecond)
		}
	}()
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
	resources := fixture.resources(t, run.ID)
	for _, resource := range resources {
		if resource.Kind == kernel.ResourceRuntimeRoot {
			if resource.State != kernel.ResourceUnresolved {
				t.Fatalf("runtime cleanup state = %s", resource.State.String())
			}
			return
		}
	}
	t.Fatal("runtime resource missing")
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
	fixture.assertReleased(t, run)
}

func TestSupervisorSourceFailureCannotReleaseProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	fixture.spec.BaseRevision = "refs/heads/does-not-exist"
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err == nil {
		t.Fatal("invalid source revision unexpectedly ran")
	}
	if run.Phase != kernel.RunFinalizing || run.Terminal != nil {
		t.Fatalf("source failure run = %+v", run)
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
	fixture.spec.releaseProvider = func(controller *runner.AttemptController) error {
		if err := controller.Release(runner.StageProvider); err != nil {
			return err
		}
		return releaseErr
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, releaseErr) {
		t.Fatalf("RunNext partial release = %v", err)
	}
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Code() != kernel.FailureProtocol || run.CredentialRevokedAt == nil || run.Terminal != nil {
		t.Fatalf("partial release run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("partial release credential = %v", err)
	}
	for _, resource := range fixture.resources(t, run.ID) {
		if resource.State != kernel.ResourceReleasing {
			t.Fatalf("partial release %s state = %s", resource.Kind.String(), resource.State.String())
		}
		if resource.Kind == kernel.ResourceRuntimeRoot || resource.Identity.Empty() {
			continue
		}
		identity, identityErr := runnerIdentity(resource.Identity)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if observation := runner.ObserveProcess(identity); observation.Presence != runner.Absent {
			t.Fatalf("partial release left %s alive: %+v", resource.Kind.String(), observation)
		}
	}
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
		return marker, activationErr
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, activationErr) {
		t.Fatalf("RunNext activation ambiguity = %v", err)
	}
	if run.Phase != kernel.RunFinalizing || run.CredentialRevokedAt == nil || run.Terminal != nil {
		t.Fatalf("activation ambiguity run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("activation ambiguity credential = %v", err)
	}
	runtimePath := filepath.Join(fixture.runtimeParentPath, run.ID.String())
	runtimeDirectory, err := os.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	terminal, loadErr := runner.LoadTerminal(runtimeDirectory, runner.TerminalSpoolName)
	closeErr := runtimeDirectory.Close()
	if loadErr != nil || closeErr != nil {
		t.Fatalf("activation terminal spool = %+v, load=%v close=%v", terminal, loadErr, closeErr)
	}
	if terminal.Terminal.Process != observedInner {
		t.Fatalf("terminal inner = %+v, observed %+v", terminal.Terminal.Process, observedInner)
	}
	if observation := runner.ObserveProcess(observedInner); observation.Presence != runner.Absent {
		t.Fatalf("activation ambiguity left inner owner alive: %+v", observation)
	}
	for _, resource := range fixture.resources(t, run.ID) {
		if resource.Kind == kernel.ResourceRunnerProcess {
			identity, identityErr := runnerIdentity(resource.Identity)
			if identityErr != nil {
				t.Fatal(identityErr)
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
	fixture.spec.afterAdmission = func() error { return commitErr }
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if !errors.Is(err, commitErr) {
		t.Fatalf("RunNext admission ambiguity = %v", err)
	}
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Code() != kernel.FailureInternal || run.CredentialRevokedAt == nil || run.Terminal != nil {
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
	for _, resource := range fixture.resources(t, run.ID) {
		if resource.State != kernel.ResourceReleasing || !resource.Identity.Empty() {
			t.Fatalf("ambiguous admission resource = %+v", resource)
		}
	}
}

type supervisorFixture struct {
	root, witness, base, changeParent, runtimeParentPath string
	daemon                                               *Daemon
	store                                                *kernel.Store
	runtimeParent                                        *RuntimeParent
	spec                                                 SupervisorSpec
	listener                                             *api.Listener
	serverDone                                           chan error
	serverOnce                                           sync.Once
	t                                                    *testing.T
	baselineFDs                                          int
	baselineGoroutines                                   int
}

func newSupervisorFixture(t *testing.T, program string) *supervisorFixture {
	t.Helper()
	baselineFDs := supervisorFDCount(t)
	baselineGoroutines := runtime.NumGoroutine()
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &supervisorFixture{
		root: root, witness: filepath.Join(root, "provider.witness"), t: t,
		baselineFDs: baselineFDs, baselineGoroutines: baselineGoroutines,
	}
	program = strings.ReplaceAll(program, "__WITNESS__", quoteShell(fixture.witness))
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
	runtimeParentPath := filepath.Join(root, "runtimes")
	for _, path := range []string{changeParent, runtimeParentPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rawRuntimeParent, err := os.Open(runtimeParentPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeParent, err := CreateRuntimeParent(rawRuntimeParent)
	_ = rawRuntimeParent.Close()
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtimeParent, fixture.runtimeParentPath, fixture.changeParent = runtimeParent, runtimeParentPath, changeParent

	store, err := kernel.Create(context.Background(), filepath.Join(root, "factory.sqlite3"), kernel.FactoryConfig{Capacity: 1}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	projectID := supervisorProjectID(t, 1)
	agentID := supervisorAgentID(t, 2)
	taskID := supervisorTaskID(t, 3)
	project, err := store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "project", Root: repository, VerificationPolicy: kernel.VerificationNone}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(context.Background(), kernel.NewAgent{
		ID: agentID, ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker,
		Provider: kernel.ProviderShell, ExecutionMode: kernel.ExecutionUnrestricted, ToolBudgetLimit: 20,
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
	operatorToken := filepath.Join(root, "operator.token")
	if err := os.WriteFile(operatorToken, bytes.Repeat([]byte{'o'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "api.sock")
	listener, err := api.Listen(socket, operatorToken)
	if err != nil {
		t.Fatal(err)
	}
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
	fixture.spec = SupervisorSpec{
		AgentID: agentID, RuntimeParent: runtimeParent, ChangeParent: changeParent,
		GitExecutable: git, BaseRevision: base, AttemptSocket: socket, RunnerExecutable: executable,
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
		fixture.hardSafetyCleanup()
		if fixture.runtimeParent != nil {
			if err := fixture.runtimeParent.Close(); err != nil {
				fixture.t.Errorf("runtime parent close: %v", err)
			}
		}
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				fixture.t.Errorf("Store close: %v", err)
			}
		}
		fixture.assertResourceCensus()
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
	for _, recovered := range runs {
		for _, resource := range recovered.Resources {
			if resource.Kind == kernel.ResourceRuntimeRoot || resource.Identity.Empty() {
				continue
			}
			identity, identityErr := runnerIdentity(resource.Identity)
			if identityErr != nil {
				fixture.t.Errorf("hard safety identity: %v", identityErr)
				continue
			}
			identities[identity] = struct{}{}
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
		if err := unix.Kill(-identity.PGID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
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

func (fixture *supervisorFixture) assertResourceCensus() {
	deadline := time.Now().Add(4 * time.Second)
	for {
		fds := supervisorFDCount(fixture.t)
		goroutines := runtime.NumGoroutine()
		if fds == fixture.baselineFDs && goroutines == fixture.baselineGoroutines {
			fixture.t.Logf("fixture census stable: fds=%d goroutines=%d", fds, goroutines)
			return
		}
		if time.Now().After(deadline) {
			fixture.t.Errorf("fixture census: fds %d -> %d; goroutines %d -> %d", fixture.baselineFDs, fds, fixture.baselineGoroutines, goroutines)
			return
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}

func (fixture *supervisorFixture) assertTerminal(t *testing.T, run kernel.Run, kind kernel.OutcomeKind) {
	t.Helper()
	if run.Phase != kernel.RunTerminal || run.Proposal == nil || run.Terminal == nil || run.Proposal.Kind() != kind || run.Terminal.Kind() != kind {
		t.Fatalf("terminal run = %+v", run)
	}
	task, found, err := fixture.store.Task(context.Background(), run.TaskID)
	if err != nil || !found {
		t.Fatalf("terminal task read = %+v, found=%v, err=%v", task, found, err)
	}
	want := kernel.TaskFailed
	if kind == kernel.OutcomeSucceeded {
		want = kernel.TaskSucceeded
	}
	if task.Status != want {
		t.Fatalf("task status = %s, want %s", task.Status.String(), want.String())
	}
}

func (fixture *supervisorFixture) assertOneWitness(t *testing.T) {
	t.Helper()
	body, err := os.ReadFile(fixture.witness)
	if err != nil || string(body) != "x" {
		t.Fatalf("provider execution witness = %q, %v", body, err)
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
		if resource.Kind != kernel.ResourceRuntimeRoot {
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
	resources, err := fixture.store.Resources(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return resources
}

func (fixture *supervisorFixture) changeName(t *testing.T, run kernel.Run) string {
	t.Helper()
	if run.ChangeID == nil {
		t.Fatal("worker run has no Change")
	}
	return run.ChangeID.String()
}

func supervisorProgram(t *testing.T, waitAfterRequest, noRequest bool) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := ""
	if !noRequest {
		request = quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
	}
	wait := ""
	if waitAfterRequest {
		wait = "sleep 30\n"
	}
	return "set -eu\nprintf x > __WITNESS__\n" + request + wait
}

func cleanupFailureProgram(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "set -eu\nprintf x > __WITNESS__\nmkfifo \"$TMPDIR/unsafe\"\n" + quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
}

func quoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

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
	command := exec.Command("/usr/bin/xcrun", "--find", "git")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	body, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
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

func supervisorFDCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
