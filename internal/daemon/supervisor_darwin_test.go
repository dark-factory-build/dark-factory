//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/install"
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

func TestSupervisorRetainedRetrySkipsGitAndPreservesPublishedTree(t *testing.T) {
	fixture := newSupervisorFixture(t, providerExitWithoutOutcomeProgram(t))
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
	if err != nil || !found || firstChange.Selection == nil {
		t.Fatalf("first retained Change = %+v, found=%v, err=%v", firstChange, found, err)
	}
	repositoryIdentity := firstChange.Selection.RepositoryIdentity()
	queueSupervisorRetry(t, fixture, first)
	// A retained retry must not resolve a selector or invoke Git again.
	fixture.spec.GitExecutable = "/private/retained-retry-must-not-run-git"
	fixture.spec.BaseRevision = "refs/heads/retained-retry-must-not-resolve"
	second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if err != nil {
		t.Fatalf("retained RunNext: %v", err)
	}
	fixture.assertTerminal(t, second, kernel.OutcomeFailed)
	after, err := os.Lstat(changePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	afterStat, afterOK := after.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK || beforeStat.Dev != afterStat.Dev || beforeStat.Ino != afterStat.Ino {
		t.Fatalf("retained tree identity changed: before=%+v after=%+v", beforeStat, afterStat)
	}
	if body, err := os.ReadFile(filepath.Join(changePath, "payload.txt")); err != nil || string(body) != "exact source\n" {
		t.Fatalf("retained payload = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.changeParent, "."+fixture.changeName(t, first)+".stage")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained retry created staging tree: %v", err)
	}
	if witness, err := os.ReadFile(fixture.witness); err != nil || string(witness) != "xx" {
		t.Fatalf("retained provider witness = %q, %v", witness, err)
	}
	if first.ChangeID == nil || second.ChangeID == nil || *first.ChangeID != *second.ChangeID || second.AdmittedChangeRevision == nil {
		t.Fatalf("retained retry changed canonical Change: first=%+v second=%+v", first.ChangeID, second.ChangeID)
	}
	secondChange, found, err := fixture.store.Change(context.Background(), *second.ChangeID)
	if err != nil || !found || secondChange.Selection == nil || secondChange.Selection.RepositoryIdentity() != repositoryIdentity {
		t.Fatalf("retained retry changed repository identity: Change=%+v found=%v err=%v", secondChange, found, err)
	}
}

func TestSupervisorRetainedRetryFailsClosedOnDurableAuthorityMismatch(t *testing.T) {
	for _, test := range []struct {
		name            string
		terminalFailure bool
		mutate          func(*testing.T, *supervisorFixture, kernel.Run)
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
			name:            "repository replacement at provider release",
			terminalFailure: true,
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
			second, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
			if test.terminalFailure {
				if err != nil {
					t.Fatalf("post-release retained verification = %v", err)
				}
				fixture.assertTerminal(t, second, kernel.OutcomeFailed)
			} else if err == nil {
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

func TestSupervisorRejectsInvalidFactoryctlBeforeAdmissionOrProviderEffect(t *testing.T) {
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
			if _, err := fixture.daemon.RunNext(context.Background(), fixture.spec); !errors.Is(err, kernel.ErrInvalidValue) || strings.Contains(err.Error(), locator) {
				t.Fatalf("invalid factoryctl result=%v", err)
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
	admission, err := store.AdmitNext(ctx, agentID, kernel.AdmissionKeys{
		RunID: runID, TerminalSessionID: sessionID, AttemptDigest: digest, CandidateChangeID: candidateChange,
		Resources:   kernel.AdmissionResourceIDs{RuntimeRoot: resource(216), RunnerProcess: resource(217), ProviderProcess: resource(218), ProviderGroup: resource(219)},
		RuntimeRoot: filepath.Join(root, "runtime"),
	}, supervisorTime())
	if err != nil || !admission.Admitted() || admission.Run == nil {
		t.Fatalf("admission = %+v, %v", admission, err)
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
	fixture.assertInterruptedFinalizing(t, result.run, kernel.TerminalSessionActive)
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
	fixture.assertInterruptedFinalizing(t, result.run, kernel.TerminalSessionDeclared)
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
	var hookErr error
	fixture.spec.beforeProviderRelease = func() {
		recoverable, err := fixture.store.RecoverableRuns(context.Background())
		if err != nil {
			hookErr = err
			return
		}
		if len(recoverable) != 1 {
			hookErr = fmt.Errorf("recoverable run count = %d, want one", len(recoverable))
			return
		}
		proposal, err := kernel.NewSuccessProposal("outcome-before-release")
		if err != nil {
			hookErr = err
			return
		}
		_, hookErr = fixture.store.ProposeAttemptOutcome(context.Background(), recoverable[0].Run.CredentialDigest, proposal, supervisorTime())
	}
	run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil || !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("release after finalization error = %v, want conflict", err)
	}
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Result() != "outcome-before-release" {
		t.Fatalf("run after finalization-before-release = %+v", run)
	}
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
	if run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Code() != kernel.FailureProtocol || run.CredentialRevokedAt == nil || run.Terminal != nil {
		t.Fatalf("partial release run = %+v", run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("partial release credential = %v", err)
	}
	fixture.assertOneWitness(t)
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
	if got.run.Phase != kernel.RunFinalizing || got.run.CredentialRevokedAt == nil || got.run.Terminal != nil {
		t.Fatalf("pre-release cancellation run = %+v", got.run)
	}
	if _, err := fixture.store.AuthenticateAttempt(context.Background(), got.run.CredentialDigest); !errors.Is(err, kernel.ErrUnauthorized) {
		t.Fatalf("pre-release cancellation credential = %v", err)
	}
	if _, err := os.Stat(fixture.witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider crossed cancellation/release boundary: %v", err)
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
		// The exact inner receipt proves activation occurred. On the injected
		// acknowledgement loss, controller EOF makes the outer converge that
		// distinct group before FinishAfterExit returns; killing outer first is unsafe.
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
	if run.Phase != kernel.RunFinalizing || run.CredentialRevokedAt == nil {
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
	baselineGoroutines                           int
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
		root: root, witness: filepath.Join(root, "provider.witness"),
		childReceipt: filepath.Join(root, "provider-child.pid"), continueReceipt: filepath.Join(root, "provider.continue"), t: t,
		baselineFDs: baselineFDs, baselineGoroutines: baselineGoroutines,
		runIDs: make(map[kernel.RunID]struct{}),
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
	taskID := supervisorTaskID(t, 3)
	project, err := store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "project", Root: repository, VerificationPolicy: kernel.VerificationNone}, supervisorTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(context.Background(), kernel.NewAgent{
		ID: agentID, ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker,
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
	socket := filepath.Join(apiHomePath, "runtimes", "factory.sock")
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
		AgentID: agentID, RuntimeParent: runtimeParent, ChangeParent: changeParent,
		GitExecutable: git, BaseRevision: base, AttemptSocket: socket, RunnerExecutable: executable, FactoryctlExecutable: factoryctl,
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
	fixture.trackRun(run.ID)
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

func (fixture *supervisorFixture) assertInterruptedFinalizing(t *testing.T, run kernel.Run, sessionState kernel.TerminalSessionState) {
	t.Helper()
	if run.ID == (kernel.RunID{}) || run.Phase != kernel.RunFinalizing || run.Terminal != nil || run.CredentialRevokedAt == nil || run.Proposal == nil {
		t.Fatalf("interrupted run = %+v, want revoked finalizing state", run)
	}
	fixture.trackRun(run.ID)
	durable, found, err := fixture.store.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("durable interrupted run: found=%v err=%v", found, err)
	}
	if durable.Phase != kernel.RunFinalizing || durable.Terminal != nil || durable.CredentialRevokedAt == nil || durable.Revision != run.Revision {
		t.Fatalf("durable interrupted run = %+v", durable)
	}
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("durable interrupted session: found=%v err=%v", found, err)
	}
	if session.State != sessionState || session.ClosedAt != nil {
		t.Fatalf("interrupted session = %+v, want state %s and not closed", session, sessionState)
	}
	resources := fixture.resources(t, run.ID)
	if len(resources) != 4 {
		t.Fatalf("interrupted resource count = %d, want 4", len(resources))
	}
	for _, resource := range resources {
		if resource.State != kernel.ResourceReleasing {
			t.Fatalf("interrupted resource %s = %s, want releasing", resource.Kind, resource.State)
		}
	}
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
	return "set -eu\nprintf x >> __WITNESS__\n" + request + wait
}

func supervisorHumanRequestProgram(t *testing.T, key, question string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := "\"$DARK_FACTORY_FACTORYCTL\" attempt request-human --idempotency-key " + quoteShell(key) + " --question " + quoteShell(question) + "\n"
	return "set -eu\n" +
		"test \"$PATH\" = /usr/bin:/bin\n" +
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
	return "set -eu\nprintf x > __WITNESS__\nmkfifo \"$TMPDIR/unsafe\"\n" + quoteShell(executable) + " --supervisor-attempt-succeed typed-success\n"
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
