//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type dispatchFixture struct {
	daemon   *Daemon
	store    *kernel.Store
	listener *api.Listener
	socket   string
	operator string
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	directory, err := os.MkdirTemp(".", "dark-factory-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	databasePath := filepath.Join(directory, "kernel.sqlite")
	initial, err := kernel.NewUnixMillis(100)
	if err != nil {
		t.Fatal(err)
	}
	store, err := kernel.Create(context.Background(), databasePath, kernel.FactoryConfig{Capacity: 2}, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	daemon, err := newDaemon(store, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	operatorToken := filepath.Join(directory, "operator.token")
	if err := os.WriteFile(operatorToken, bytes.Repeat([]byte{'o'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "api.sock")
	listener, err := api.Listen(socket, operatorToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return &dispatchFixture{daemon: daemon, store: store, listener: listener, socket: socket, operator: operatorToken}
}

func (fixture *dispatchFixture) serve(t *testing.T) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		connection, err := fixture.listener.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- fixture.daemon.HandleConnection(context.Background(), connection)
	}()
	return done
}

func waitDispatch(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon API handler did not finish")
	}
}

func testID(value byte) string {
	return hex.EncodeToString(bytes.Repeat([]byte{value}, kernel.IDBytes))
}

func TestDaemonDispatchesOperatorCallsAndBoundsProjection(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	done := fixture.serve(t)
	health, err := client.Health(ctx)
	if err != nil || !health.Ready {
		t.Fatalf("health = %+v, %v", health, err)
	}
	waitDispatch(t, done)

	projectInput := api.CreateProjectInput{ID: testID(1), Name: "project", Root: filepath.Join(t.TempDir(), "source-root")}
	done = fixture.serve(t)
	projectResult, err := client.CreateProject(ctx, projectInput)
	if err != nil || projectResult.Revision != 1 || projectResult.Head != 1 {
		t.Fatalf("create project = %+v, %v", projectResult, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	agentResult, err := client.CreateShellAgent(ctx, api.CreateShellAgentInput{
		ID: testID(2), ProjectID: projectInput.ID, Name: "agent", Role: "orchestrator", ToolBudgetLimit: 50,
	})
	if err != nil || agentResult.Revision != 1 || agentResult.Head != 2 {
		t.Fatalf("create shell agent = %+v, %v", agentResult, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	taskResult, err := client.EnqueueTask(ctx, api.EnqueueTaskInput{
		ID: testID(3), ProjectID: projectInput.ID, AssignedAgentID: testID(2), IncarnationID: testID(4),
		Title: "public title", Body: "private task body sentinel", Priority: 7,
	})
	if err != nil || taskResult.Revision != 1 || taskResult.Head != 3 {
		t.Fatalf("enqueue task = %+v, %v", taskResult, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	dispatchResult, err := client.SetDispatch(ctx, 1, true)
	if err != nil || dispatchResult.Revision != 2 || dispatchResult.Head != 4 {
		t.Fatalf("set dispatch = %+v, %v", dispatchResult, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if snapshot.Head != 4 || len(snapshot.Projects) != 1 || len(snapshot.Agents) != 1 || len(snapshot.Tasks) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Projects[0].ID != projectInput.ID || snapshot.Projects[0].Name != projectInput.Name || snapshot.Agents[0].Role != "orchestrator" || snapshot.Tasks[0].Title != "public title" {
		t.Fatalf("projection lost public fields: %+v", snapshot)
	}
	if strings.Contains(snapshot.Tasks[0].Title, "private task body sentinel") {
		t.Fatal("task body crossed public projection")
	}

	project, found, err := fixture.store.Project(ctx, mustProjectID(t, projectInput.ID))
	if err != nil || !found || project.Root != projectInput.Root || project.VerificationPolicy != kernel.VerificationNone {
		t.Fatalf("durable project = %+v, found=%v, err=%v", project, found, err)
	}
	agent, found, err := fixture.store.Agent(ctx, mustAgentID(t, testID(2)))
	if err != nil || !found || agent.Provider != kernel.ProviderShell || agent.ExecutionMode != kernel.ExecutionUnrestricted || agent.Model != "" || agent.ReasoningEffort != "" {
		t.Fatalf("durable shell agent = %+v, found=%v, err=%v", agent, found, err)
	}
	task, found, err := fixture.store.Task(ctx, mustTaskID(t, testID(3)))
	if err != nil || !found || task.Body != "private task body sentinel" {
		t.Fatalf("durable task = %+v, found=%v, err=%v", task, found, err)
	}
}

func TestDaemonDispatchesAttemptOutcomeAndWakesAfterCommit(t *testing.T) {
	fixture := newDispatchFixture(t)
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectID := testID(11)
	agentID := testID(12)
	taskID := testID(13)
	incarnationID := testID(14)
	for _, invoke := range []func() error{
		func() error {
			done := fixture.serve(t)
			_, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "project", Root: filepath.Join(t.TempDir(), "root")})
			waitDispatch(t, done)
			return err
		},
		func() error {
			done := fixture.serve(t)
			_, err := operator.CreateShellAgent(ctx, api.CreateShellAgentInput{ID: agentID, ProjectID: projectID, Name: "agent", Role: "orchestrator", ToolBudgetLimit: 10})
			waitDispatch(t, done)
			return err
		},
		func() error {
			done := fixture.serve(t)
			_, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: "task", Body: "private", Priority: 1})
			waitDispatch(t, done)
			return err
		},
		func() error {
			done := fixture.serve(t)
			_, err := operator.SetDispatch(ctx, 1, true)
			waitDispatch(t, done)
			return err
		},
	} {
		if err := invoke(); err != nil {
			t.Fatal(err)
		}
	}

	bearer := bytes.Repeat([]byte{'a'}, 32)
	digestBytes := sha256.Sum256(bearer)
	digest, err := kernel.AttemptDigestFromBytes(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	runID := mustRunID(t, testID(15))
	keys := kernel.AdmissionKeys{
		RunID: runID, AttemptDigest: digest, RuntimeRoot: filepath.Join(t.TempDir(), "runtime"),
		Resources: kernel.AdmissionResourceIDs{
			RuntimeRoot: mustResourceID(t, testID(16)), RunnerProcess: mustResourceID(t, testID(17)),
			ProviderProcess: mustResourceID(t, testID(18)), ProviderGroup: mustResourceID(t, testID(19)),
		},
	}
	at := mustKernelTime(t, 1000)
	admission, err := fixture.store.AdmitNext(ctx, mustAgentID(t, agentID), keys, at)
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	run := admission.Run
	pathIdentity, err := kernel.NewPathResourceIdentity(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	activate := func(id kernel.ResourceID, identity kernel.ResourceIdentity) error {
		resource, found, readErr := fixture.store.Resource(ctx, id)
		if readErr != nil || !found {
			return errors.Join(readErr, errors.New("resource not found"))
		}
		_, activateErr := fixture.store.ActivateResource(ctx, run.ID, id, resource.Revision, identity, at)
		return activateErr
	}
	birthOne, _ := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{1}, 32))
	birthTwo, _ := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{2}, 32))
	processOne, _ := kernel.NewProcessResourceIdentity(10, 11, birthOne)
	processTwo, _ := kernel.NewProcessResourceIdentity(12, 13, birthTwo)
	if err := activate(keys.Resources.RuntimeRoot, pathIdentity); err != nil {
		t.Fatal(err)
	}
	if err := activate(keys.Resources.RunnerProcess, processOne); err != nil {
		t.Fatal(err)
	}
	if err := activate(keys.Resources.ProviderProcess, processTwo); err != nil {
		t.Fatal(err)
	}
	if err := activate(keys.Resources.ProviderGroup, processTwo); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.store.ActivateRun(ctx, run.ID, run.Revision, at)
	if err != nil {
		t.Fatal(err)
	}
	wake, ok := fixture.daemon.registerRunWake(active.ID)
	if !ok || wake == nil {
		t.Fatal("run wake registration failed")
	}

	attemptToken := filepath.Join(t.TempDir(), "attempt.token")
	if err := os.WriteFile(attemptToken, bearer, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE")
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", attemptToken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", previous) })
	attempt, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	result, err := attempt.Succeed(ctx, "private result sentinel")
	if err != nil || result.Revision != uint64(active.Revision.Int64()+1) || result.Head != 5 {
		t.Fatalf("attempt succeed = %+v, %v", result, err)
	}
	waitDispatch(t, done)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("durable outcome did not notify registered supervisor")
	}
	finalizing, found, err := fixture.store.Run(ctx, active.ID)
	if err != nil || !found || finalizing.Phase != kernel.RunFinalizing || finalizing.Proposal == nil || finalizing.Proposal.Kind() != kernel.OutcomeSucceeded || finalizing.Proposal.Result() != "private result sentinel" {
		t.Fatalf("durable outcome = %+v, found=%v, err=%v", finalizing, found, err)
	}
	fixture.daemon.unregisterRunWake(active.ID)

	done = fixture.serve(t)
	if _, err := attempt.Fail(ctx, "late"); err == nil {
		t.Fatal("revoked attempt credential remained usable")
	}
	waitDispatch(t, done)
}

func TestWakeHintsAreCapacityOneAndUnregisterIsHarmless(t *testing.T) {
	daemon := &Daemon{wakes: make(map[kernel.RunID]chan struct{})}
	runID := mustRunID(t, testID(41))
	wake, ok := daemon.registerRunWake(runID)
	if !ok || wake == nil {
		t.Fatal("wake registration failed")
	}
	duplicate, ok := daemon.registerRunWake(runID)
	if !ok || duplicate != wake {
		t.Fatal("duplicate registration did not retain the same hint")
	}
	daemon.notifyRun(runID)
	daemon.notifyRun(runID)
	select {
	case <-wake:
	default:
		t.Fatal("wake was not delivered")
	}
	select {
	case <-wake:
		t.Fatal("wake channel exceeded capacity one")
	default:
	}
	daemon.unregisterRunWake(runID)
	daemon.notifyRun(runID)
}

func TestProjectionHasNoPrivateFieldsAndKeepsEmptySlices(t *testing.T) {
	projectID := mustProjectID(t, testID(51))
	agentID := mustAgentID(t, testID(52))
	taskID := mustTaskID(t, testID(53))
	revision := mustRevision(t, 3)
	head, err := kernel.NewEventSequence(0)
	if err != nil {
		t.Fatal(err)
	}
	projected := projectSnapshot(kernel.DashboardSnapshot{
		Head:     head,
		Factory:  kernel.FactorySummary{Capacity: 2, Revision: revision},
		Projects: []kernel.ProjectSummary{{ID: projectID, Name: "project", Revision: revision}},
		Agents:   []kernel.AgentSummary{{ID: agentID, ProjectID: projectID, Name: "agent", Role: "worker", Revision: revision}},
		Tasks:    []kernel.TaskSummary{{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, Title: "title", Status: "queued", Priority: 3, Revision: revision}},
	})
	if projected.Head != 0 || projected.Projects == nil || projected.Agents == nil || projected.Tasks == nil {
		t.Fatalf("projection emptiness/head = %+v", projected)
	}
	if projected.Projects[0].Name != "project" || projected.Tasks[0].Title != "title" {
		t.Fatalf("projection fields = %+v", projected)
	}
}

func TestRemoteErrorMappingNeverUsesPrivateStoreText(t *testing.T) {
	tests := []struct {
		err  error
		code api.RemoteErrorCode
	}{
		{fmt.Errorf("private body: %w", kernel.ErrInvalidValue), api.RemoteInvalidRequest},
		{fmt.Errorf("private token: %w", kernel.ErrUnauthorized), api.RemoteUnauthorized},
		{fmt.Errorf("private ID: %w", kernel.ErrNotFound), api.RemoteNotFound},
		{fmt.Errorf("private revision: %w", kernel.ErrRevisionConflict), api.RemoteRevisionConflict},
		{fmt.Errorf("private state: %w", kernel.ErrConflict), api.RemoteConflict},
		{fmt.Errorf("private snapshot: %w", kernel.ErrSnapshotTooLarge), api.RemoteTooLarge},
		{fmt.Errorf("private busy: %w", kernel.ErrBusy), api.RemoteUnavailable},
		{errors.New("private unexpected failure"), api.RemoteInternal},
	}
	for _, test := range tests {
		if got := remoteErrorCode(test.err); got != test.code {
			t.Fatalf("remote code = %q, want %q", got, test.code)
		}
		if strings.Contains(newErrorReply(test.code).String(), "private") {
			t.Fatal("private error text entered reply")
		}
	}
}

func mustKernelTime(t *testing.T, value int64) kernel.UnixMillis {
	t.Helper()
	at, err := kernel.NewUnixMillis(value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func mustRevision(t *testing.T, value int64) kernel.Revision {
	t.Helper()
	revision, err := kernel.NewRevision(value)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func mustProjectID(t *testing.T, value string) kernel.ProjectID {
	t.Helper()
	id, err := kernel.ProjectIDFromBytes(mustIDBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAgentID(t *testing.T, value string) kernel.AgentID {
	t.Helper()
	id, err := kernel.AgentIDFromBytes(mustIDBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTaskID(t *testing.T, value string) kernel.TaskID {
	t.Helper()
	id, err := kernel.TaskIDFromBytes(mustIDBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustRunID(t *testing.T, value string) kernel.RunID {
	t.Helper()
	id, err := kernel.RunIDFromBytes(mustIDBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustResourceID(t *testing.T, value string) kernel.ResourceID {
	t.Helper()
	id, err := kernel.ResourceIDFromBytes(mustIDBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustIDBytes(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
