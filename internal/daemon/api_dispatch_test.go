//go:build darwin || linux

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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type dispatchFixture struct {
	databasePath string
	daemon       *Daemon
	store        *kernel.Store
	listener     *api.Listener
	socket       string
	operator     string
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	return newDispatchFixtureAt(t, "/private/tmp")
}

func newDispatchFixtureAt(t *testing.T, parent string) *dispatchFixture {
	t.Helper()
	directory, err := os.MkdirTemp(parent, "dark-factory-dispatch-")
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
	store, err := createTestStore(context.Background(), databasePath, kernel.FactoryConfig{Capacity: 2}, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	daemon, err := newDaemon(store, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	authHomePath := filepath.Join(directory, "home")
	if socket := install.LocalAPISocketPath(authHomePath); len(socket) > install.MaxSocketPathBytes {
		t.Fatalf("api socket path is %d bytes, over the %d-byte budget: %q", len(socket), install.MaxSocketPathBytes, socket)
	}
	if _, err := install.Init(context.Background(), authHomePath); err != nil {
		if errors.Is(err, install.ErrUnsupported) {
			t.Skip("operational local API is unsupported on this platform")
		}
		t.Fatal(err)
	}
	operatorToken := filepath.Join(authHomePath, "operator.token")
	if err := os.WriteFile(operatorToken, bytes.Repeat([]byte{'o'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(context.Background(), authHomePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := home.OpenLocalAPI(context.Background())
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	listener, err := api.Listen(authority)
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = home.Close()
	})
	socket := install.LocalAPISocketPath(authHomePath)
	return &dispatchFixture{databasePath: databasePath, daemon: daemon, store: store, listener: listener, socket: socket, operator: operatorToken}
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
	initialSnapshot, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	if initialSnapshot.Head != 0 || initialSnapshot.Projects == nil || initialSnapshot.Agents == nil || initialSnapshot.Tasks == nil || len(initialSnapshot.Projects) != 0 || len(initialSnapshot.Agents) != 0 || len(initialSnapshot.Tasks) != 0 {
		t.Fatalf("fresh snapshot = %+v", initialSnapshot)
	}
	done = fixture.serve(t)
	stop, err := client.SetDispatch(ctx, 1, false)
	if err != nil || stop.Head != 1 || stop.Revision != 2 {
		t.Fatalf("explicit stop mutation = %+v, %v", stop, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	health, err := client.Health(ctx)
	if err != nil || !health.Ready {
		t.Fatalf("health = %+v, %v", health, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	projectInput := api.CreateProjectInput{ID: testID(1), Name: "project", Root: filepath.Join(t.TempDir(), "source-root")}
	done = fixture.serve(t)
	projectResult, err := client.CreateProject(ctx, projectInput)
	if err != nil || projectResult.Revision != 1 || projectResult.Head != 2 {
		t.Fatalf("create project = %+v, %v", projectResult, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	limits, err := client.SetProjectLimits(ctx, api.ProjectLimitsInput{ProjectID: projectInput.ID, ExpectedRevision: projectResult.Revision, RunBudget: 3, MaxRunSeconds: 60})
	if err != nil || limits.Revision != projectResult.Revision+1 {
		t.Fatalf("set project limits = %+v, %v", limits, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	if _, err := client.SetProjectLimits(ctx, api.ProjectLimitsInput{ProjectID: projectInput.ID, ExpectedRevision: projectResult.Revision, RunBudget: 3, MaxRunSeconds: 60}); err == nil {
		t.Fatal("stale project limits accepted")
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	agentResult, err := client.CreateAgent(ctx, api.CreateAgentInput{
		ID: testID(2), ProjectID: projectInput.ID, Name: "agent", Role: "orchestrator",
		Provider: "codex", Model: "gpt-5.6-luna", ReasoningEffort: "medium", ToolBudgetLimit: 50,
	})
	if err != nil || agentResult.Revision != 1 || agentResult.Head != 4 {
		t.Fatalf("create agent = %+v, %v", agentResult, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	policyResult, err := client.SetAgentIdlePolicy(ctx, api.AgentIdlePolicyInput{AgentID: testID(2), ExpectedRevision: agentResult.Revision, Policy: "standing_instruction", AfterSeconds: 60, Instruction: "review retained changes", RunBudget: 3})
	if err != nil || policyResult.Revision != agentResult.Revision+1 {
		t.Fatalf("set idle policy = %+v, %v", policyResult, err)
	}
	waitDispatch(t, done)
	assertSchedulerWake(t, fixture.daemon)
	updated, found, err := fixture.store.Agent(ctx, mustAgentID(t, testID(2)))
	if err != nil || !found || updated.Idle.Policy != kernel.IdleStandingInstruction || updated.Idle.AfterSeconds != 60 || updated.Idle.Instruction != "review retained changes" || updated.Idle.RunBudget != 3 {
		t.Fatalf("stored idle policy = %+v, found=%v, err=%v", updated.Idle, found, err)
	}

	done = fixture.serve(t)
	if _, err := client.SetAgentIdlePolicy(ctx, api.AgentIdlePolicyInput{AgentID: testID(2), ExpectedRevision: agentResult.Revision, Policy: "wait"}); err == nil {
		t.Fatal("stale idle policy accepted")
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	_, err = client.EnqueueTask(ctx, api.EnqueueTaskInput{
		ID: testID(3), ProjectID: projectInput.ID, AssignedAgentID: testID(2), IncarnationID: testID(4),
		Title: "oversized", Body: strings.Repeat("x", 8193), Priority: 7,
	})
	var oversized *api.RemoteError
	if !errors.As(err, &oversized) || oversized.Code() != api.RemoteInvalidRequest {
		t.Fatalf("oversized Codex task must be refused before admission: %v", err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)
	if _, found, err := fixture.store.Task(ctx, mustTaskID(t, testID(3))); err != nil || found {
		t.Fatalf("refused task was persisted: found=%v, err=%v", found, err)
	}

	done = fixture.serve(t)
	taskResult, err := client.EnqueueTask(ctx, api.EnqueueTaskInput{
		ID: testID(3), ProjectID: projectInput.ID, AssignedAgentID: testID(2), IncarnationID: testID(4),
		Title: "public title", Body: "private task body sentinel", Priority: 7,
	})
	if err != nil || taskResult.Revision != 1 || taskResult.Head != 6 {
		t.Fatalf("enqueue task = %+v, %v", taskResult, err)
	}
	waitDispatch(t, done)
	assertSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	dispatchResult, err := client.SetDispatch(ctx, stop.Revision, true)
	if err != nil || dispatchResult.Revision != 3 || dispatchResult.Head != 7 {
		t.Fatalf("set dispatch = %+v, %v", dispatchResult, err)
	}
	waitDispatch(t, done)
	assertSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if snapshot.Head != 7 || len(snapshot.Projects) != 1 || len(snapshot.Agents) != 1 || len(snapshot.Tasks) != 1 {
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
	if err != nil || !found || agent.Provider != kernel.ProviderCodex || agent.Model != "gpt-5.6-luna" || agent.ReasoningEffort != "medium" {
		t.Fatalf("durable agent = %+v, found=%v, err=%v", agent, found, err)
	}
	task, found, err := fixture.store.Task(ctx, mustTaskID(t, testID(3)))
	if err != nil || !found || task.Body != "private task body sentinel" {
		t.Fatalf("durable task = %+v, found=%v, err=%v", task, found, err)
	}
}

func TestDaemonDispatchesContentCreateWithoutScheduling(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, api.CreateProjectInput{ID: testID(240), Name: "content", Root: contentRepositoryFixture(t)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	content, err := client.ContentCreate(ctx, api.ContentInput{ID: testID(241), ProjectID: testID(240), Kind: "procedure", Title: "procedure", Body: "steps"})
	if err != nil || content.Revision != 1 || content.Body != "" {
		t.Fatalf("content create = %+v, %v", content, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)
	snapshot, err := fixture.store.Snapshot(ctx)
	if err != nil || len(snapshot.Tasks) != 0 {
		t.Fatalf("content changed task admission: %+v, %v", snapshot, err)
	}
}

func assertSchedulerWake(t *testing.T, daemon *Daemon) {
	t.Helper()
	select {
	case <-daemon.schedulerWake:
	default:
		t.Fatal("durable runnable mutation did not wake scheduler")
	}
}

func assertNoSchedulerWake(t *testing.T, daemon *Daemon) {
	t.Helper()
	select {
	case <-daemon.schedulerWake:
		t.Fatal("non-runnable mutation woke scheduler")
	default:
	}
}

func TestDaemonDispatchesAttemptOutcomeAfterCommit(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 11)
	ctx := context.Background()
	done := fixture.serve(t)
	result, err := active.client.Succeed(ctx, "private result sentinel")
	if err != nil || result.Revision != uint64(active.run.Revision.Int64()+1) || result.Head != 11 {
		t.Fatalf("attempt succeed = %+v, %v", result, err)
	}
	waitDispatch(t, done)
	finalizing, found, err := fixture.store.Run(ctx, active.run.ID)
	if err != nil || !found || finalizing.Phase != kernel.RunFinalizing || finalizing.Proposal == nil || finalizing.Proposal.Kind() != kernel.OutcomeSucceeded || finalizing.Proposal.Result() != "private result sentinel" {
		t.Fatalf("durable outcome = %+v, found=%v, err=%v", finalizing, found, err)
	}
	done = fixture.serve(t)
	if _, err := active.client.Fail(ctx, "late"); err == nil {
		t.Fatal("revoked attempt credential remained usable")
	}
	waitDispatch(t, done)
}

func TestDaemonOutcomeWaitsForWriterWithoutLosingResult(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 11)
	lock, err := sql.Open("sqlite3", "file:"+fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	lock.SetMaxOpenConns(1)
	if _, err := lock.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer lock.Exec("ROLLBACK")
	entered := make(chan struct{})
	var once sync.Once
	fixture.daemon.now = func() time.Time { once.Do(func() { close(entered) }); return time.UnixMilli(1000) }
	const resultText = "Implemented the exact provider permission correction; focused tests passed and delivery remains pending."
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := fixture.serve(t)
	result := make(chan error, 1)
	go func() { _, err := active.client.Succeed(ctx, resultText); result <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("outcome did not reach daemon")
	}
	// Hold real writer contention past the live owner's polling timeout. A
	// complete authenticated mutation must use its request lifetime instead.
	select {
	case err := <-result:
		t.Fatalf("outcome abandoned before writer release: %v", err)
	case <-time.After(liveAttemptStoreTimeout + 300*time.Millisecond):
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("outcome after writer release: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("outcome remained blocked")
	}
	waitDispatch(t, done)
	run, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Result() != resultText {
		t.Fatalf("durable exact result = %+v, found=%v, err=%v", run, found, err)
	}
}

func TestDaemonContendedOutcomeHonorsRequestCancellation(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 11)
	lock, err := sql.Open("sqlite3", "file:"+fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	lock.SetMaxOpenConns(1)
	if _, err := lock.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer lock.Exec("ROLLBACK")
	entered := make(chan struct{})
	var once sync.Once
	fixture.daemon.now = func() time.Time { once.Do(func() { close(entered) }); return time.UnixMilli(1000) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		connection, err := fixture.listener.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- fixture.daemon.HandleConnection(ctx, connection)
	}()
	result := make(chan error, 1)
	go func() {
		_, err := active.client.Succeed(context.Background(), "cancelled request must not commit")
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("outcome did not reach daemon")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request remained blocked")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled handler remained blocked")
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	run, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || run.Phase != kernel.RunRunning || run.Proposal != nil {
		t.Fatalf("cancelled durable outcome = %+v, found=%v, err=%v", run, found, err)
	}
}

// A send-back reaches the kernel through both domains with the kernel's own
// refusals mapped to remote codes: an orchestrator may not send back its own
// task, a shell task takes no note, a queued task is a conflict, a task that
// does not exist is not found, a note the provider could not be handed is
// too large, and an unknown bearer learns none of that. The accepting paths
// are the kernel's tests.
func TestDaemonDispatchesSendBackThroughBothDomains(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 51)
	ctx := context.Background()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	own := active.run.TaskID.String()
	var remote *api.RemoteError
	done := fixture.serve(t)
	if _, err := active.client.SendBack(ctx, api.SendBackInput{TaskID: own, Note: "myself"}); !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("orchestrator sending back its own task = %v", err)
	}
	waitDispatch(t, done)
	// The fixture's orchestrator is a shell agent: its task is a program and
	// takes no note, whatever its status.
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: own, Note: "still running"}); !errors.As(err, &remote) || remote.Code() != api.RemoteInvalidRequest {
		t.Fatalf("operator sending back a shell task = %v", err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: testID(99), Note: "nobody"}); !errors.As(err, &remote) || remote.Code() != api.RemoteNotFound {
		t.Fatalf("operator sending back a missing task = %v", err)
	}
	waitDispatch(t, done)
	if _, err := active.client.SendBack(ctx, api.SendBackInput{TaskID: own, Note: ""}); !errors.Is(err, api.ErrInvalidInput) {
		t.Fatalf("empty note left the client = %v", err)
	}
	// A note the task's provider could not be handed is too large, whatever
	// the kernel would have said about the task.
	claude, claudeTask := testID(61), testID(62)
	done = fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: claude, ProjectID: active.run.ProjectID.String(), Name: "typist", Role: "worker", Provider: "claude_code", ToolBudgetLimit: 1}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: claudeTask, ProjectID: active.run.ProjectID.String(), AssignedAgentID: claude, IncarnationID: testID(63), Title: "typed", Body: strings.Repeat("x", 7000)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: claudeTask, Note: strings.Repeat("&", 1024)}); !errors.As(err, &remote) || remote.Code() != api.RemoteTooLarge {
		t.Fatalf("a note past the provider's prompt = %v", err)
	}
	waitDispatch(t, done)
	// A note that fits reaches the kernel, which refuses the queued task.
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: claudeTask, Note: "fits"}); !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("operator sending back a queued task = %v", err)
	}
	waitDispatch(t, done)
	// Another project's task is unauthorized before its provider is asked
	// anything, so a note's length cannot probe it.
	elsewhere, stranger, foreign := testID(71), testID(72), testID(73)
	done = fixture.serve(t)
	if _, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: elsewhere, Name: "elsewhere", Root: filepath.Join(filepath.Dir(fixture.socket), "elsewhere-root")}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: stranger, ProjectID: elsewhere, Name: "stranger", Role: "worker", Provider: "claude_code", ToolBudgetLimit: 1}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: foreign, ProjectID: elsewhere, AssignedAgentID: stranger, IncarnationID: testID(74), Title: "not yours", Body: strings.Repeat("x", 7000)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := active.client.SendBack(ctx, api.SendBackInput{TaskID: foreign, Note: strings.Repeat("&", 1024)}); !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
		t.Fatalf("another project's task with an oversized note = %v", err)
	}
	waitDispatch(t, done)
	// A credential the kernel would refuse learns nothing: an unknown bearer
	// naming a missing task is unauthorized, never not found.
	wrongToken := filepath.Join(filepath.Dir(fixture.socket), "wrong-send-back.token")
	if err := os.WriteFile(wrongToken, bytes.Repeat([]byte{'y'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	activeToken := os.Getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE")
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", wrongToken); err != nil {
		t.Fatal(err)
	}
	wrong, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", activeToken); err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	if _, err := wrong.SendBack(ctx, api.SendBackInput{TaskID: testID(98), Note: "who is there"}); !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
		t.Fatalf("unknown bearer naming a missing task = %v", err)
	}
	waitDispatch(t, done)
	task, found, err := fixture.store.Task(ctx, active.run.TaskID)
	if err != nil || !found || task.Status != kernel.TaskRunning || task.WorkRevision.Int64() != 1 {
		t.Fatalf("task after refused send-backs = %+v, found=%v, %v", task, found, err)
	}
}

func TestDaemonSetsWorkerCapacityWithRevisionGuard(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	done := fixture.serve(t)
	updated, err := client.SetCapacity(ctx, 1, 3)
	if err != nil || updated.Revision != 2 || updated.Head != 1 {
		t.Fatalf("set worker capacity = %+v, %v", updated, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	snapshot, err := client.Snapshot(ctx)
	if err != nil || snapshot.Factory.Capacity != 3 || snapshot.Factory.Revision != updated.Revision {
		t.Fatalf("worker capacity readback = %+v, %v", snapshot.Factory, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	if _, err := client.SetCapacity(ctx, 1, 4); err == nil {
		t.Fatal("stale worker capacity update succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
			t.Fatalf("stale worker capacity error = %v", err)
		}
	}
	waitDispatch(t, done)
}

func TestDaemonDispatchesHumanQuestionWithDurableIdempotency(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 31)
	ctx := context.Background()
	input := api.HumanQuestionInput{IdempotencyKey: testID(41), Question: "private human question sentinel"}
	before, err := fixture.store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}

	done := fixture.serve(t)
	created, err := active.client.RequestHuman(ctx, input)
	if err != nil || created.Revision != 1 || created.Head != uint64(before.Head.Int64()+1) {
		t.Fatalf("request human = %+v, %v", created, err)
	}
	waitDispatch(t, done)
	requests, err := fixture.store.Snapshot(ctx)
	if err != nil || len(requests.HumanRequests) != 1 {
		t.Fatalf("durable human requests = %+v, %v", requests.HumanRequests, err)
	}
	request := requests.HumanRequests[0]
	if request.Status != kernel.HumanRequestOpen {
		t.Fatalf("durable human request = %+v", request)
	}
	encoded, err := json.Marshal(created)
	if err != nil || bytes.Contains(encoded, []byte(input.Question)) || bytes.Contains(encoded, []byte(request.ID.String())) {
		t.Fatalf("mutation response exposed private request data: %s, %v", encoded, err)
	}

	beforeReplay, err := fixture.store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	replay, err := active.client.RequestHuman(ctx, input)
	if err != nil || replay.Revision != created.Revision || replay.Head != uint64(beforeReplay.Head.Int64()) {
		t.Fatalf("idempotent request human = %+v, %v", replay, err)
	}
	waitDispatch(t, done)
	afterReplay, err := fixture.store.Snapshot(ctx)
	if err != nil || len(afterReplay.HumanRequests) != 1 || afterReplay.HumanRequests[0].ID != request.ID {
		t.Fatalf("idempotent durable human requests = %+v, %v", afterReplay.HumanRequests, err)
	}

	conflicting := input
	conflicting.Question = "different private question"
	done = fixture.serve(t)
	if _, err := active.client.RequestHuman(ctx, conflicting); err == nil {
		t.Fatal("same human request key with different question succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
			t.Fatalf("conflicting human request error = %v", err)
		}
	}
	waitDispatch(t, done)
	afterConflict, err := fixture.store.Snapshot(ctx)
	if err != nil || len(afterConflict.HumanRequests) != 1 || afterConflict.HumanRequests[0].ID != request.ID {
		t.Fatalf("conflicting durable human requests = %+v, %v", afterConflict.HumanRequests, err)
	}
}

func TestDaemonRejectsForgedAndFinalizingHumanQuestion(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 51)
	ctx := context.Background()
	wrongToken := filepath.Join(filepath.Dir(fixture.socket), "wrong-human.token")
	if err := os.WriteFile(wrongToken, bytes.Repeat([]byte{'z'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	activeToken := os.Getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE")
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", wrongToken); err != nil {
		t.Fatal(err)
	}
	wrong, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", activeToken); err != nil {
		t.Fatal(err)
	}

	done := fixture.serve(t)
	if _, err := wrong.RequestHuman(ctx, api.HumanQuestionInput{IdempotencyKey: testID(61), Question: "forged question"}); err == nil {
		t.Fatal("forged human request succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("forged human request error = %v", err)
		}
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	if _, err := active.client.Succeed(ctx, "finalize attempt"); err != nil {
		t.Fatalf("finalize attempt = %v", err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	if _, err := active.client.RequestHuman(ctx, api.HumanQuestionInput{IdempotencyKey: testID(62), Question: "finalizing question"}); err == nil {
		t.Fatal("finalizing human request succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("finalizing human request error = %v", err)
		}
	}
	waitDispatch(t, done)
	requests, err := fixture.store.Snapshot(ctx)
	if err != nil || len(requests.HumanRequests) != 0 {
		t.Fatalf("unauthorized human requests became durable = %+v, %v", requests.HumanRequests, err)
	}
}

type activeAttempt struct {
	client *api.AttemptClient
	run    kernel.Run
	bearer []byte
}

func prepareActiveAttempt(t *testing.T, fixture *dispatchFixture, seed byte) activeAttempt {
	t.Helper()
	return prepareActiveAttemptInProject(t, fixture, seed, testID(seed), "orchestrator")
}

func prepareActiveAttemptInProject(t *testing.T, fixture *dispatchFixture, seed byte, projectID, role string) activeAttempt {
	t.Helper()
	ctx := context.Background()
	agentID, taskID, incarnationID := testID(seed+1), testID(seed+2), testID(seed+3)
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	call := func(invoke func() error) {
		done := fixture.serve(t)
		if err := invoke(); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	}
	id, err := parseProjectID(projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, foundProject, err := fixture.store.Project(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !foundProject {
		call(func() error {
			_, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "project", Root: filepath.Join(filepath.Dir(fixture.socket), "source-root")})
			return err
		})
	}
	call(func() error {
		_, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: agentID, ProjectID: projectID, Name: "agent", Role: role, Provider: "shell", ToolBudgetLimit: 10})
		return err
	})
	call(func() error {
		_, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: "task", Body: "private", Priority: 1})
		return err
	})
	factory, err := fixture.store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !factory.DispatchEnabled {
		call(func() error {
			_, err := operator.SetDispatch(ctx, uint64(factory.Revision.Int64()), true)
			return err
		})
	}
	bearer := bytes.Repeat([]byte{seed}, 32)
	digestBytes := sha256.Sum256(bearer)
	digest, err := kernel.AttemptDigestFromBytes(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	rawCandidate, err := hex.DecodeString(testID(seed + 9))
	if err != nil {
		t.Fatal(err)
	}
	candidateChange, err := kernel.ChangeIDFromBytes(rawCandidate)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := kernel.ResultProofDigestFromBytes(bytes.Repeat([]byte{seed + 20}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := kernel.AdmissionKeys{
		ResultProofDigest: proof,
		RunID:             mustRunID(t, testID(seed+4)), TerminalSessionID: mustTerminalSessionID(t, testID(seed+14)), AttemptDigest: digest, CandidateChangeID: candidateChange,
		RuntimeRoot: filepath.Join(filepath.Dir(fixture.socket), fmt.Sprintf("runtime-%d", seed)),
		Resources: kernel.AdmissionResourceIDs{
			RuntimeRoot: mustResourceID(t, testID(seed+5)), RunnerProcess: mustResourceID(t, testID(seed+6)),
			ProviderProcess: mustResourceID(t, testID(seed+7)), ProviderGroup: mustResourceID(t, testID(seed+8)),
		},
	}
	at := mustKernelTime(t, 1000)
	admission, err := fixture.store.AdmitNext(ctx, keys, at)
	if err != nil || !admission.Admitted() {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	run := admission.Run
	pathIdentity, err := kernel.NewPathResourceIdentity(1, int64(seed)+100)
	if err != nil {
		t.Fatal(err)
	}
	activate := func(id kernel.ResourceID, identity kernel.ResourceIdentity) {
		resource, found, readErr := fixture.store.Resource(ctx, id)
		if readErr != nil || !found {
			t.Fatalf("resource %s = %+v, found=%v, err=%v", id, resource, found, readErr)
		}
		if _, err := fixture.store.ActivateResource(ctx, run.ID, id, resource.Revision, identity, at); err != nil {
			t.Fatal(err)
		}
	}
	birthOne, _ := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{seed + 1}, 32))
	birthTwo, _ := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{seed + 2}, 32))
	processOne, _ := kernel.NewProcessResourceIdentity(int64(seed)+10, int64(seed)+11, birthOne)
	processTwo, _ := kernel.NewProcessResourceIdentity(int64(seed)+12, int64(seed)+13, birthTwo)
	activate(keys.Resources.RuntimeRoot, pathIdentity)
	run2 := startAndActivateRunner(t, fixture.store, run.ID, keys.Resources.RunnerProcess, processOne, at)
	providerProcess, found, err := fixture.store.Resource(ctx, keys.Resources.ProviderProcess)
	if err != nil || !found {
		t.Fatalf("provider process = %+v, found=%v, err=%v", providerProcess, found, err)
	}
	providerGroup, found, err := fixture.store.Resource(ctx, keys.Resources.ProviderGroup)
	if err != nil || !found {
		t.Fatalf("provider group = %+v, found=%v, err=%v", providerGroup, found, err)
	}
	if _, _, err := fixture.store.ActivateProviderResources(ctx, run.ID, providerProcess.ID, providerProcess.Revision, providerGroup.ID, providerGroup.Revision, processTwo, at); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	if role == "worker" {
		adapterPublishChange(t, fixture.store, *run)
	}
	active, err := fixture.store.ActivateRun(ctx, run.ID, session.ID, run2.Revision, session.Revision, at)
	if err != nil {
		t.Fatal(err)
	}
	attemptToken := filepath.Join(filepath.Dir(fixture.socket), fmt.Sprintf("attempt-%d.token", seed))
	if err := os.WriteFile(attemptToken, bearer, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE")
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", attemptToken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", previous) })
	client, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	return activeAttempt{client: client, run: active, bearer: append([]byte(nil), bearer...)}
}

func TestDaemonServesCurrentWorkerAssignmentReceipt(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 201, testID(201), "worker")
	ctx := context.Background()

	done := fixture.serve(t)
	receipt, err := active.client.Task(ctx)
	if err != nil {
		t.Fatalf("worker task receipt = %v", err)
	}
	waitDispatch(t, done)

	change, found, err := fixture.store.Change(ctx, *active.run.ChangeID)
	if err != nil || !found || change.Selection == nil {
		t.Fatalf("worker Change = %+v, found=%v, err=%v", change, found, err)
	}
	wantBase := hex.EncodeToString(change.Selection.Commit().Bytes())
	want := api.AttemptTask{
		Task:                   "private",
		TaskID:                 active.run.TaskID.String(),
		IncarnationID:          active.run.TaskIncarnationID.String(),
		WorkRevision:           uint64(active.run.AdmittedTaskWorkRevision.Int64()),
		ChangeID:               active.run.ChangeID.String(),
		AdmittedChangeRevision: uint64(active.run.AdmittedChangeRevision.Int64()),
		ChangeRevision:         uint64(change.Revision.Int64()),
		BaseCommit:             wantBase,
	}
	if receipt != want {
		t.Fatalf("worker assignment receipt = %+v, want %+v", receipt, want)
	}
	if receipt.AdmittedChangeRevision == receipt.ChangeRevision {
		t.Fatalf("worker receipt did not distinguish admitted/current Change revisions: %+v", receipt)
	}
}

func TestDaemonOverseerTaskUpdateAuthorizesBeforeProviderPreflight(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 171)
	ctx := context.Background()
	at := mustKernelTime(t, 1100)
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: mustProjectID(t, testID(181)), Name: "foreign", Root: filepath.Join(filepath.Dir(fixture.socket), "foreign")}, at)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(182)), ProjectID: project.ID, Name: "foreign worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(184)))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(183)), ProjectID: project.ID, AssignedAgentID: worker.ID, IncarnationID: incarnation, Title: "foreign", Body: "valid", Priority: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	valid := "valid"
	tooLargeForCodex := strings.Repeat("x", 8<<10+1)
	priority := int64(2)
	for _, input := range []api.OverseerTaskUpdateInput{
		{TaskID: foreign.ID.String(), ExpectedRevision: uint64(foreign.Revision.Int64()), Body: &valid},
		{TaskID: foreign.ID.String(), ExpectedRevision: uint64(foreign.Revision.Int64()), Body: &tooLargeForCodex},
		{TaskID: foreign.ID.String(), ExpectedRevision: uint64(foreign.Revision.Int64() + 1), Body: &valid},
		{TaskID: foreign.ID.String(), ExpectedRevision: uint64(foreign.Revision.Int64()), Priority: &priority},
		{TaskID: foreign.ID.String(), ExpectedRevision: uint64(foreign.Revision.Int64()), Cancel: true},
		{TaskID: testID(185), ExpectedRevision: 1, Body: &valid},
	} {
		done := fixture.serve(t)
		_, err := active.client.OverseerUpdateTask(ctx, input)
		waitDispatch(t, done)
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("foreign update %#v = %v", input, err)
		}
	}
}

func TestDaemonOverseerTaskRetryRejectsOrchestratorTask(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	caller := prepareActiveAttempt(t, fixture, 191)
	target, found, err := fixture.store.Task(ctx, caller.run.TaskID)
	if err != nil || !found {
		t.Fatalf("orchestrator task = %+v, found=%v, err=%v", target, found, err)
	}

	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	replacementID := testID(211)
	done := fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{
		ID: replacementID, ProjectID: caller.run.ProjectID.String(), Name: "replacement", Role: "worker", Provider: "shell", ToolBudgetLimit: 1,
	}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)

	beforeHistory, err := fixture.store.TaskInterventions(ctx, target.ProjectID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	_, err = caller.client.OverseerUpdateTask(ctx, api.OverseerTaskUpdateInput{
		TaskID: target.ID.String(), ExpectedRevision: uint64(target.Revision.Int64()), AssignedAgentID: &replacementID, Retry: true,
	})
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("orchestrator retry error = %v", err)
	}
	afterTask, found, err := fixture.store.Task(ctx, target.ID)
	if err != nil || !found || !reflect.DeepEqual(afterTask, target) {
		t.Fatalf("orchestrator retry changed task: before=%+v after=%+v found=%v err=%v", target, afterTask, found, err)
	}
	afterRun, found, err := fixture.store.Run(ctx, caller.run.ID)
	if err != nil || !found || !reflect.DeepEqual(afterRun, caller.run) {
		t.Fatalf("orchestrator retry changed caller run: before=%+v after=%+v found=%v err=%v", caller.run, afterRun, found, err)
	}
	afterHistory, err := fixture.store.TaskInterventions(ctx, target.ProjectID, target.ID)
	if err != nil || len(afterHistory) != len(beforeHistory) {
		t.Fatalf("orchestrator retry changed history: before=%d after=%d err=%v", len(beforeHistory), len(afterHistory), err)
	}
}

func TestDaemonOperatorTaskRetryAndAgentPauseUseExactRevisions(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 212, testID(212), "worker")
	ctx := context.Background()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}

	agent, found, err := fixture.store.Agent(ctx, active.run.AgentID)
	if err != nil || !found {
		t.Fatalf("agent = %+v, found=%v, err=%v", agent, found, err)
	}
	paused := true
	done := fixture.serve(t)
	pause, err := operator.UpdateAgent(ctx, api.OverseerAgentUpdateInput{AgentID: agent.ID.String(), ExpectedRevision: uint64(agent.Revision.Int64()), Paused: &paused})
	waitDispatch(t, done)
	if err != nil || pause.Revision != uint64(agent.Revision.Int64()+1) {
		t.Fatalf("pause = %+v, %v", pause, err)
	}
	done = fixture.serve(t)
	_, err = operator.UpdateAgent(ctx, api.OverseerAgentUpdateInput{AgentID: agent.ID.String(), ExpectedRevision: uint64(agent.Revision.Int64()), Paused: &paused})
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
		t.Fatalf("stale pause = %v", err)
	}

	taskID, incarnationID := testID(250), testID(251)
	done = fixture.serve(t)
	created, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: taskID, ProjectID: active.run.ProjectID.String(), AssignedAgentID: agent.ID.String(), IncarnationID: incarnationID, Title: "queued", Body: "work", Priority: 1})
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	priority := int64(9)
	done = fixture.serve(t)
	updated, err := operator.UpdateTask(ctx, api.OverseerTaskUpdateInput{TaskID: taskID, ExpectedRevision: created.Revision, Priority: &priority})
	waitDispatch(t, done)
	if err != nil || updated.Revision != created.Revision+1 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	done = fixture.serve(t)
	_, err = operator.UpdateTask(ctx, api.OverseerTaskUpdateInput{TaskID: taskID, ExpectedRevision: updated.Revision, Retry: true})
	waitDispatch(t, done)
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("queued retry = %v", err)
	}
}

func TestDaemonOperatorTaskReadBindsRevisionAndPagesUTF8(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	call := func(invoke func() error) {
		done := fixture.serve(t)
		if err := invoke(); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	}
	projectID, agentID, taskID, incarnationID := testID(220), testID(221), testID(222), testID(223)
	call(func() error {
		_, err := client.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "read", Root: filepath.Join(filepath.Dir(fixture.socket), "read-root")})
		return err
	})
	call(func() error {
		_, err := client.CreateAgent(ctx, api.CreateAgentInput{ID: agentID, ProjectID: projectID, Name: "reader", Role: "worker", Provider: "shell", ToolBudgetLimit: 1})
		return err
	})
	body := strings.Repeat("🙂", 2050)
	var created api.MutationResult
	call(func() error {
		created, err = client.EnqueueTask(ctx, api.EnqueueTaskInput{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: "paged", Body: body, Priority: 1})
		return err
	})
	read := func(offset uint64, revision uint64) (api.TaskText, error) {
		done := fixture.serve(t)
		value, err := client.ReadTask(ctx, api.TaskReadInput{TaskID: taskID, ExpectedRevision: revision, Offset: offset})
		waitDispatch(t, done)
		return value, err
	}
	first, err := read(0, created.Revision)
	if err != nil || utf8.RuneCountInString(first.Instruction) != 2048 || first.NextOffset == nil || *first.NextOffset != 2048 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := read(*first.NextOffset, created.Revision)
	if err != nil || second.Instruction != strings.Repeat("🙂", 2) || second.NextOffset != nil {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	_, err = read(0, created.Revision+1)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
		t.Fatalf("stale read = %v", err)
	}
}

func TestDaemonOperatorAgentPathsReturnsNoChangeOverseerRuntime(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 236)
	ctx := context.Background()
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("session: %v %v", found, err)
	}
	owner := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	owner.agentID = active.run.AgentID
	if err := fixture.daemon.registerLiveAttempt(owner); err != nil {
		t.Fatal(err)
	}
	defer fixture.daemon.unregisterLiveAttempt(active.run.ID, owner)
	resources, err := fixture.store.Resources(ctx, active.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantRuntime := ""
	for _, resource := range resources {
		if resource.Kind == kernel.ResourceRuntimeRoot {
			wantRuntime = resource.Path
		}
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	paths, err := operator.AgentPaths(ctx, api.AgentPathsInput{AgentID: active.run.AgentID.String()})
	waitDispatch(t, done)
	if err != nil || paths.RunID != active.run.ID.String() || paths.SourcePath != "" || paths.RuntimePath != wantRuntime || paths.Paths == nil {
		t.Fatalf("operator overseer paths = %+v, %v; want runtime %q", paths, err, wantRuntime)
	}
}

func TestDaemonDispatchesBlockAndFailCalls(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *api.AttemptClient) (api.MutationResult, error)
		kind kernel.OutcomeKind
	}{
		{name: "block", call: func(ctx context.Context, client *api.AttemptClient) (api.MutationResult, error) {
			return client.Block(ctx, "needs operator")
		}, kind: kernel.OutcomeBlocked},
		{name: "fail empty detail", call: func(ctx context.Context, client *api.AttemptClient) (api.MutationResult, error) {
			return client.Fail(ctx, "")
		}, kind: kernel.OutcomeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDispatchFixture(t)
			active := prepareActiveAttempt(t, fixture, byte(21+len(test.name)))
			done := fixture.serve(t)
			result, err := test.call(context.Background(), active.client)
			if err != nil || result.Revision != uint64(active.run.Revision.Int64()+1) || result.Head != 11 {
				t.Fatalf("outcome = %+v, %v", result, err)
			}
			waitDispatch(t, done)
			observed, found, err := fixture.store.Run(context.Background(), active.run.ID)
			if err != nil || !found || observed.Phase != kernel.RunFinalizing || observed.Proposal == nil || observed.Proposal.Kind() != test.kind {
				t.Fatalf("durable proposal = %+v, found=%v, err=%v", observed, found, err)
			}
		})
	}
}

func TestDaemonServesTaskOnlyToLiveAttempt(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 51)
	done := fixture.serve(t)
	task, err := active.client.Task(context.Background())
	if err != nil || task.Task != "private" {
		t.Fatalf("attempt task = %+v, %v", task, err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	if _, err := active.client.Succeed(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)

	done = fixture.serve(t)
	if _, err := active.client.Task(context.Background()); err == nil {
		t.Fatal("finalizing attempt read task")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("finalizing task error = %v", err)
		}
	}
	waitDispatch(t, done)
}

func TestDaemonRejectsForgedAttemptOutcome(t *testing.T) {
	fixture := newDispatchFixture(t)
	_ = prepareActiveAttempt(t, fixture, 61)
	wrongBearer := bytes.Repeat([]byte{'z'}, 32)
	wrongToken := filepath.Join(filepath.Dir(fixture.socket), "wrong.token")
	if err := os.WriteFile(wrongToken, wrongBearer, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE")
	if err := os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", wrongToken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", previous) })
	wrong, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	if _, err := wrong.Succeed(context.Background(), "forged"); err == nil {
		t.Fatal("forged attempt outcome succeeded")
	}
	waitDispatch(t, done)
}

func TestDaemonConcurrentAttemptOutcomesHaveOneDurableWinner(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 71)
	type outcomeResult struct {
		result api.MutationResult
		err    error
	}
	firstDone := fixture.serve(t)
	secondDone := fixture.serve(t)
	results := make(chan outcomeResult, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		result, err := active.client.Succeed(context.Background(), "winner")
		results <- outcomeResult{result: result, err: err}
	}()
	go func() {
		defer group.Done()
		result, err := active.client.Block(context.Background(), "loser")
		results <- outcomeResult{result: result, err: err}
	}()
	group.Wait()
	waitDispatch(t, firstDone)
	waitDispatch(t, secondDone)
	var accepted, rejected int
	for range 2 {
		outcome := <-results
		if outcome.err == nil {
			accepted++
			if outcome.result.Revision == 0 {
				t.Fatal("accepted outcome omitted revision")
			}
			continue
		}
		rejected++
		var remote *api.RemoteError
		if !errors.As(outcome.err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("losing outcome error = %v", outcome.err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("outcome counts accepted=%d rejected=%d", accepted, rejected)
	}
	observed, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || observed.Phase != kernel.RunFinalizing || observed.Proposal == nil {
		t.Fatalf("concurrent durable result = %+v, found=%v, err=%v", observed, found, err)
	}
}

// The first caller pauses before Store admission so the second caller is the
// first correlated refusal. Arrival order must not replace that proposal.
func TestDaemonOverlappingRefusalsRetainFirstCorrelatedOutcome(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 81)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session: found=%v err=%v", found, err)
	}
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	live.attemptDigest = active.run.CredentialDigest
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	defer fixture.daemon.unregisterLiveAttempt(active.run.ID, live)

	firstArrived, releaseFirst := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(releaseFirst) })
	var clockCalls atomic.Int32
	var now atomic.Int64
	now.Store(100) // Before the active run's revision: authenticated refusal.
	fixture.daemon.now = func() time.Time {
		if clockCalls.Add(1) == 1 {
			close(firstArrived)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		}
		return time.UnixMilli(now.Load())
	}
	assertRemote := func(err error, code api.RemoteErrorCode) {
		t.Helper()
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != code {
			t.Fatalf("outcome error = %v, want %s", err, code)
		}
	}
	assertPending := func() {
		t.Helper()
		proposal, ok := live.pendingOutcomeSnapshot()
		if !ok || proposal.Kind() != kernel.OutcomeBlocked || proposal.Detail() != "first actual refusal" {
			t.Fatalf("retained proposal = %+v, present=%v", proposal, ok)
		}
	}
	firstDone := fixture.serve(t)
	firstResult := make(chan error, 1)
	go func() {
		_, err := active.client.Succeed(ctx, "later actual refusal")
		firstResult <- err
	}()
	select {
	case <-firstArrived:
	case <-ctx.Done():
		t.Fatal("first request did not reach the clock gate")
	}
	secondDone := fixture.serve(t)
	_, err = active.client.Block(ctx, "first actual refusal")
	assertRemote(err, api.RemoteRevisionConflict)
	waitDispatch(t, secondDone)
	assertPending()
	release.Do(func() { close(releaseFirst) })
	assertRemote(<-firstResult, api.RemoteRevisionConflict)
	waitDispatch(t, firstDone)
	assertPending()

	wrongToken := filepath.Join(filepath.Dir(fixture.socket), "wrong.token")
	if err := os.WriteFile(wrongToken, bytes.Repeat([]byte{'z'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE", wrongToken)
	wrong, err := api.NewAttemptClientFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	_, err = wrong.Fail(ctx, "foreign proposal")
	assertRemote(err, api.RemoteUnauthorized)
	waitDispatch(t, done)
	assertPending()
	observed, found, err := fixture.store.Run(ctx, active.run.ID)
	if err != nil || !found || observed.Phase != kernel.RunRunning || observed.Proposal != nil {
		t.Fatalf("refused proposals changed durable run = %+v, found=%v err=%v", observed, found, err)
	}

	now.Store(2000)
	done = fixture.serve(t)
	if _, err := active.client.Succeed(ctx, "durable winner"); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	if proposal, ok := live.pendingOutcomeSnapshot(); ok {
		t.Fatalf("accepted outcome retained stale proposal: %+v", proposal)
	}
	observed, found, err = fixture.store.Run(ctx, active.run.ID)
	if err != nil || !found || observed.Phase != kernel.RunFinalizing || observed.Proposal == nil || observed.Proposal.Result() != "durable winner" {
		t.Fatalf("accepted durable proposal = %+v, found=%v err=%v", observed, found, err)
	}
}

func TestProjectionHasNoPrivateFieldsAndKeepsEmptySlices(t *testing.T) {
	projectID := mustProjectID(t, testID(51))
	agentID := mustAgentID(t, testID(52))
	taskID := mustTaskID(t, testID(53))
	incarnationID, err := parseIncarnationID(testID(54))
	if err != nil {
		t.Fatal(err)
	}
	revision := mustRevision(t, 3)
	head, err := kernel.NewEventSequence(0)
	if err != nil {
		t.Fatal(err)
	}
	projected := projectSnapshot(kernel.DashboardSnapshot{
		Head:     head,
		Factory:  kernel.FactorySummary{Capacity: 2, Revision: revision},
		Projects: []kernel.ProjectSummary{{ID: projectID, Name: "project", RunBudgetLimit: 8, RunsUsed: 3, MaxRunSeconds: 900, Revision: revision}},
		Agents:   []kernel.AgentSummary{{ID: agentID, ProjectID: projectID, Name: "agent", Role: "worker", Provider: "codex", Revision: revision}},
		Tasks:    []kernel.TaskSummary{{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, WorkRevision: revision, Title: "title", Status: "queued", Priority: 3, Revision: revision}},
	})
	if projected.Head != 0 || projected.Projects == nil || projected.Agents == nil || projected.Tasks == nil {
		t.Fatalf("projection emptiness/head = %+v", projected)
	}
	if projected.Projects[0].Name != "project" || projected.Projects[0].RunBudgetLimit != 8 || projected.Projects[0].RunsUsed != 3 || projected.Projects[0].MaxRunSeconds != 900 || projected.Agents[0].Provider != "codex" || projected.Tasks[0].Title != "title" {
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

func mustTerminalSessionID(t *testing.T, value string) kernel.TerminalSessionID {
	t.Helper()
	id, err := kernel.TerminalSessionIDFromBytes(mustIDBytes(t, value))
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

func TestTaskEnqueuePreflightPreservesReplayAndOverseerAuthority(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 201)
	ctx := context.Background()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	workerID := testID(210)
	done := fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: workerID, ProjectID: active.run.ProjectID.String(), Name: "worker", Role: "worker", Provider: "codex", ToolBudgetLimit: 10}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: testID(220), Name: "other", Root: filepath.Join(t.TempDir(), "other-source")}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: testID(221), ProjectID: testID(220), Name: "foreign", Role: "worker", Provider: "codex", ToolBudgetLimit: 10}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	// Simulate a task accepted by the old operator route before this check.
	incarnation, _ := parseIncarnationID(testID(212))
	spec := kernel.NewTask{ID: mustTaskID(t, testID(211)), ProjectID: active.run.ProjectID, AssignedAgentID: mustAgentID(t, workerID), IncarnationID: incarnation, Title: "legacy", Body: strings.Repeat("x", 8193)}
	at, _ := kernel.NewUnixMillis(1000)
	legacy, err := fixture.store.EnqueueTask(ctx, spec, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, overseer := range []bool{false, true} {
		for _, mismatch := range []bool{false, true} {
			body := spec.Body
			if mismatch {
				body += "x"
			}
			done = fixture.serve(t)
			var result api.MutationResult
			if overseer {
				result, err = active.client.OverseerEnqueueTask(ctx, api.OverseerTaskCreateInput{ID: spec.ID.String(), AssignedAgentID: workerID, IncarnationID: incarnation.String(), Title: spec.Title, Body: body})
			} else {
				result, err = operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: spec.ID.String(), ProjectID: spec.ProjectID.String(), AssignedAgentID: workerID, IncarnationID: incarnation.String(), Title: spec.Title, Body: body})
			}
			waitDispatch(t, done)
			var remote *api.RemoteError
			if mismatch {
				if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
					t.Fatalf("mismatched replay, overseer=%v: %v", overseer, err)
				}
			} else if err != nil || result.Revision != uint64(legacy.Revision.Int64()) {
				t.Fatalf("exact legacy replay, overseer=%v: %+v, %v", overseer, result, err)
			}
		}
	}
	for _, target := range []string{testID(250), testID(221), active.run.AgentID.String()} {
		done = fixture.serve(t)
		_, err := active.client.OverseerEnqueueTask(ctx, api.OverseerTaskCreateInput{ID: testID(213), AssignedAgentID: target, IncarnationID: testID(214), Title: "not authorized", Body: strings.Repeat("x", 8193)})
		waitDispatch(t, done)
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
			t.Fatalf("unauthorized target %s returned %v", target, err)
		}
	}
}

func TestDaemonSelectAgentModelRequiresWorkerRevisionAndCompatibleControls(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project := api.CreateProjectInput{ID: testID(50), Name: "project", Root: filepath.Join(t.TempDir(), "source")}
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	for _, input := range []api.CreateAgentInput{
		{ID: testID(51), ProjectID: project.ID, Name: "worker", Role: "worker", Provider: "codex", ToolBudgetLimit: 1},
		{ID: testID(52), ProjectID: project.ID, Name: "overseer", Role: "orchestrator", Provider: "codex", ToolBudgetLimit: 1},
	} {
		done = fixture.serve(t)
		if _, err := client.CreateAgent(ctx, input); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	}
	selectModel := api.AgentModelSelectInput{AgentID: testID(51), ExpectedRevision: 1, Model: "gpt-5.6-luna", ReasoningEffort: "medium"}
	done = fixture.serve(t)
	updated, err := client.SelectAgentModel(ctx, selectModel)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("select worker model = %+v, %v", updated, err)
	}
	waitDispatch(t, done)
	if agent, found, err := fixture.store.Agent(ctx, mustAgentID(t, testID(51))); err != nil || !found || agent.Model != selectModel.Model || agent.ReasoningEffort != selectModel.ReasoningEffort {
		t.Fatalf("stored model selection = %+v, found=%v, err=%v", agent, found, err)
	}
	for _, input := range []api.AgentModelSelectInput{
		{AgentID: testID(51), ExpectedRevision: 1, Model: "gpt-5.6-luna", ReasoningEffort: "medium"},
		{AgentID: testID(51), ExpectedRevision: 2, Model: "gpt-5.6-luna", ReasoningEffort: "extreme"},
		{AgentID: testID(52), ExpectedRevision: 1, Model: "gpt-5.6-luna", ReasoningEffort: "medium"},
	} {
		done = fixture.serve(t)
		if _, err := client.SelectAgentModel(ctx, input); err == nil {
			t.Fatalf("invalid selection accepted: %+v", input)
		}
		waitDispatch(t, done)
	}
}

func TestDaemonSourceRefusesProviderWithoutReadOnlyBoundary(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 71)
	done := fixture.serve(t)
	_, err := active.client.Source(context.Background(), testID(73))
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteUnavailable {
		t.Fatalf("unprotected source = %v", err)
	}
	waitDispatch(t, done)
}

// An unavailable notification transport must not turn a durable conversation
// into a failed or unreadable operation. No live terminal is registered here.
func TestPeerConversationSurvivesUnavailableNotification(t *testing.T) {
	fixture := newDispatchFixture(t)
	source := prepareActiveAttemptInProject(t, fixture, 11, testID(11), "worker")
	target := prepareActiveAttemptInProject(t, fixture, 41, testID(11), "worker")
	ctx := context.Background()
	done := fixture.serve(t)
	_, err := source.client.PeerAsk(ctx, api.PeerQuestionInput{TargetTaskID: target.run.TaskID.String(), IdempotencyKey: testID(91), Question: "review x.go"})
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	inbox, err := target.client.PeerInboxPage(ctx, 0, 0)
	waitDispatch(t, done)
	if err != nil || len(inbox.Questions) != 1 || inbox.Questions[0].Question != "review x.go" || inbox.Questions[0].RecipientDeliveryState != "unknown" {
		t.Fatalf("durable question=%+v err=%v", inbox, err)
	}
	question := inbox.Questions[0]
	done = fixture.serve(t)
	_, err = target.client.PeerAnswer(ctx, api.PeerAnswerInput{QuestionID: question.ID, ExpectedRevision: question.Revision, IdempotencyKey: testID(92), Answer: "reviewed"})
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	inbox, err = source.client.PeerInboxPage(ctx, 0, 0)
	waitDispatch(t, done)
	if err != nil || len(inbox.Questions) != 1 || inbox.Questions[0].Answer != "reviewed" || inbox.Questions[0].AnswerDeliveryState != "unknown" {
		t.Fatalf("durable answer=%+v err=%v", inbox, err)
	}
}

func TestOperatorTaskRecoveryReportsBoundedExactOutcome(t *testing.T) {
	for _, kind := range []string{"succeeded", "blocked", "failed"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newDispatchFixture(t)
			ctx := context.Background()
			initialRevision, _ := kernel.NewRevision(1)
			if _, err := fixture.store.SetDispatch(ctx, initialRevision, true, mustKernelTime(t, 101)); err != nil {
				t.Fatal(err)
			}
			run := adapterRunningRun(t, fixture.store, 180)
			result := strings.Repeat("x", api.MaxRecoveryResultBytes-1) + "😀tail"
			var proposal kernel.Proposal
			switch kind {
			case "succeeded":
				proposal, _ = kernel.NewSuccessProposal(result)
			case "blocked":
				proposal, _ = kernel.NewBlockedProposal("missing supported tool")
			case "failed":
				proposal, _ = kernel.NewFailureProposal(kernel.FailureInternal, "execution failed")
			}
			terminal := completeAdapterRunWithProposal(t, fixture.store, run, proposal)
			client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
			if err != nil {
				t.Fatal(err)
			}
			read := func() api.TaskRecovery {
				done := fixture.serve(t)
				value, err := client.TaskRecovery(ctx, api.TaskRecoveryInput{TaskID: run.TaskID.String(), IncarnationID: run.TaskIncarnationID.String()})
				if err != nil {
					t.Fatal(err)
				}
				waitDispatch(t, done)
				return value
			}
			value := read()
			if value.Status != kind || value.RunOutcome != kind || value.RunID != terminal.ID.String() || value.RunWorkRevision != 1 {
				t.Fatalf("identity/outcome = %+v", value)
			}
			if kind == "succeeded" {
				if !value.ResultTruncated || value.Result != strings.Repeat("x", api.MaxRecoveryResultBytes-1) {
					t.Fatalf("UTF8 excerpt len=%d truncated=%v", len(value.Result), value.ResultTruncated)
				}
			} else if value.RunDetail != proposal.Detail() || kind == "blocked" && value.BlockedReason != proposal.Detail() {
				t.Fatalf("diagnostic = %+v", value)
			}
			task, found, err := fixture.store.Task(ctx, run.TaskID)
			if err != nil || !found {
				t.Fatal(err)
			}
			if _, err := fixture.store.SendBackTask(ctx, task.ID, task.Revision, "correct this", mustKernelTime(t, 500)); err != nil {
				t.Fatal(err)
			}
			value = read()
			if value.Status != "queued" || value.WorkRevision != 2 || value.Result != "" || value.ResultTruncated || value.BlockedReason != "" || value.RunWorkRevision != 1 || value.RunOutcome != kind {
				t.Fatalf("prior outcome presented as current: %+v", value)
			}

		})
	}
}

func TestDaemonSelectOverseerAccountPreservesSelectionGuards(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	project := api.CreateProjectInput{ID: testID(40), Name: "project", Root: filepath.Join(t.TempDir(), "source")}
	done := fixture.serve(t)
	_, err = client.CreateProject(ctx, project)
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	_, err = client.CreateAgent(ctx, api.CreateAgentInput{ID: testID(41), ProjectID: project.ID, Name: "overseer", Role: "orchestrator", Provider: "codex", ToolBudgetLimit: 1})
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	for i, provider := range []kernel.Provider{kernel.ProviderCodex, kernel.ProviderClaudeCode} {
		id, err := kernel.AccountIDFromBytes(bytes.Repeat([]byte{byte(42 + i)}, kernel.IDBytes))
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.store.LinkAccount(ctx, kernel.NewAccount{ID: id, Provider: provider, Home: filepath.Join(t.TempDir(), "login"), Label: "account"}, mustKernelTime(t, 1000))
		if err != nil {
			t.Fatal(err)
		}
	}
	input := api.AgentAccountSelectInput{AgentID: testID(41), ExpectedRevision: 1, AccountID: testID(42)}
	done = fixture.serve(t)
	result, err := client.SelectAgentAccount(ctx, input)
	waitDispatch(t, done)
	if err != nil || result.Revision != 2 {
		t.Fatalf("overseer account = %+v, %v", result, err)
	}
	for _, tc := range []struct {
		input api.AgentAccountSelectInput
		code  api.RemoteErrorCode
	}{
		{input, api.RemoteRevisionConflict},
		{api.AgentAccountSelectInput{AgentID: testID(41), ExpectedRevision: 2, AccountID: testID(43)}, api.RemoteInvalidRequest},
		{api.AgentAccountSelectInput{AgentID: testID(99), ExpectedRevision: 1, AccountID: testID(42)}, api.RemoteNotFound},
	} {
		done = fixture.serve(t)
		_, err := client.SelectAgentAccount(ctx, tc.input)
		waitDispatch(t, done)
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != tc.code {
			t.Fatalf("selection %+v: %v, want %v", tc.input, err, tc.code)
		}
	}
	agent, found, err := fixture.store.Agent(ctx, mustAgentID(t, testID(41)))
	if err != nil || !found || agent.AccountID.String() != testID(42) || agent.Revision.Int64() != 2 {
		t.Fatalf("stored selection: %+v, %v", agent, err)
	}
	active := prepareActiveAttempt(t, fixture, 60)
	done = fixture.serve(t)
	_, err = client.SelectAgentAccount(ctx, api.AgentAccountSelectInput{AgentID: active.run.AgentID.String(), ExpectedRevision: 1, AccountID: testID(42)})
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("active account edit: %v", err)
	}
}
