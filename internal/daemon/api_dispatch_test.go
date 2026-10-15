package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

type dispatchFixture struct {
	home         *install.OperationalHome
	databasePath string
	daemon       *Daemon
	store        *kernel.Store
	listener     *api.Listener
	socket       string
	operator     string
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	return newDispatchFixtureAt(t, runner.ShortTempDir())
}

func TestProfileOperatorAPIWritesProfiles(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.daemon.home = filepath.Dir(filepath.Dir(fixture.socket))
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	result, err := operator.Profile(context.Background(), api.ProfileInput{Seconds: 1})
	waitDispatch(t, done)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if result.Seconds != 1 {
		t.Fatalf("profile seconds = %d", result.Seconds)
	}
	for _, path := range []string{result.CPUPath, result.HeapPath, result.GoroutinePath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("profile %q: %v", path, statErr)
		}
		if info.Size() == 0 || info.Mode().Perm() != 0o600 {
			t.Fatalf("profile %q metadata = size %d mode %o", path, info.Size(), info.Mode().Perm())
		}
	}
}

func TestAttachmentRetentionOperatorAPIUsesDaemonStore(t *testing.T) {
	fixture := newDispatchFixture(t)
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	enabled := false
	result, err := operator.AttachmentRetention(context.Background(), &enabled)
	waitDispatch(t, done)
	if err != nil {
		t.Fatalf("attachment retention: %v", err)
	}
	if result.Enabled {
		t.Fatalf("attachment retention remained enabled: %+v", result)
	}
}

func TestAccountUpdateOperatorAPIUsesDaemonStore(t *testing.T) {
	fixture := newDispatchFixture(t)
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	accountID := testID(61)
	rawID, err := hex.DecodeString(accountID)
	if err != nil {
		t.Fatal(err)
	}
	accountKey, err := kernel.AccountIDFromBytes(rawID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LinkAccount(context.Background(), kernel.NewAccount{ID: accountKey, Provider: kernel.ProviderCodex, Home: filepath.Join(t.TempDir(), "login"), Label: "before"}, mustKernelTime(t, 1000)); err != nil {
		t.Fatal(err)
	}
	label := "after"
	done := fixture.serve(t)
	result, err := operator.UpdateAccount(context.Background(), api.AccountUpdateInput{AccountID: accountID, ExpectedRevision: 1, Label: &label})
	waitDispatch(t, done)
	if err != nil || result.Revision != 2 {
		t.Fatalf("account update = %+v, %v", result, err)
	}
	accounts, err := fixture.store.ListAccounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].Label != label {
		t.Fatalf("stored accounts = %+v, %v", accounts, err)
	}
}

func TestTelemetryIngestOperatorAPIUsesRelayHandler(t *testing.T) {
	fixture := newDispatchFixture(t)
	identity, err := relayhost.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.browserMu.Lock()
	fixture.daemon.relay = &RelayRuntime{connector: &relayhost.Connector{}, identity: identity, relayOrigin: "ws://relay.example", browserAddress: "127.0.0.1:43123", ingestPath: filepath.Join(t.TempDir(), "ingest.sha256")}
	fixture.daemon.browserMu.Unlock()
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	minted, err := operator.TelemetryIngest(context.Background(), "mint")
	waitDispatch(t, done)
	if err != nil || minted.Secret == "" || !minted.Active {
		t.Fatalf("telemetry mint = %+v, %v", minted, err)
	}
	done = fixture.serve(t)
	status, err := operator.TelemetryIngest(context.Background(), "status")
	waitDispatch(t, done)
	if err != nil || !status.Active || status.Secret != "" {
		t.Fatalf("telemetry status = %+v, %v", status, err)
	}
	done = fixture.serve(t)
	revoked, err := operator.TelemetryIngest(context.Background(), "revoke")
	waitDispatch(t, done)
	if err != nil || revoked.Active || revoked.Secret != "" {
		t.Fatalf("telemetry revoke = %+v, %v", revoked, err)
	}
}

func TestBackupCreateOperatorAPI(t *testing.T) {
	fixture := newDispatchFixture(t)
	backup := filepath.Join(t.TempDir(), "factory.sqlite3")
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	result, err := operator.BackupCreate(context.Background(), backup)
	waitDispatch(t, done)
	if err != nil {
		t.Fatalf("backup create: %v", err)
	}
	if result.Revision == 0 {
		t.Fatalf("backup create returned zero revision: %+v", result)
	}
	if _, err := os.Stat(kernel.BackupManifestPath(backup)); err != nil {
		t.Fatalf("backup manifest: %v", err)
	}
}

func TestBackupCreateRejectsLiveDatabaseDestination(t *testing.T) {
	fixture := newDispatchFixture(t)
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	_, err = operator.BackupCreate(context.Background(), fixture.databasePath)
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("live database destination error: %v", err)
	}
	if _, statErr := os.Stat(fixture.databasePath); statErr != nil {
		t.Fatalf("live database removed: %v", statErr)
	}
}

func TestBackupCreateRejectsAliasedLiveDatabaseDestination(t *testing.T) {
	fixture := newDispatchFixture(t)
	aliased := strings.TrimPrefix(fixture.databasePath, "/private")
	if filepath.Clean(aliased) == filepath.Clean(fixture.databasePath) {
		t.Skip("test environment has no distinct /tmp alias")
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	_, err = operator.BackupCreate(context.Background(), aliased)
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
		t.Fatalf("aliased live database destination error: %v", err)
	}
	if _, statErr := os.Stat(fixture.databasePath); statErr != nil {
		t.Fatalf("live database removed through alias: %v", statErr)
	}
}

func TestBackupManifestTempFileDoesNotTruncateLiveDatabase(t *testing.T) {
	fixture := newDispatchFixture(t)
	backup := filepath.Join(t.TempDir(), "factory.sqlite3")
	tmp := kernel.BackupManifestPath(backup) + ".tmp"
	if err := os.Link(fixture.databasePath, tmp); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	_, err = operator.BackupCreate(context.Background(), backup)
	waitDispatch(t, done)
	if err == nil {
		t.Fatal("backup unexpectedly succeeded with an existing manifest temp file")
	}
	after, err := os.ReadFile(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing manifest temp alias changed the live database")
	}
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
	if err := store.InitializeRepositoryBase(context.Background(), "HEAD"); err != nil {
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
	return &dispatchFixture{home: home, databasePath: databasePath, daemon: daemon, store: store, listener: listener, socket: socket, operator: operatorToken}
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
	if snapshot, err := client.Snapshot(ctx); err != nil || snapshot.Head < 1 {
		t.Fatalf("health check snapshot = %+v, %v", snapshot, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	projectInput := api.CreateProjectInput{ID: testID(1), Name: "project", Root: contentRepositoryFixture(t)}
	done = fixture.serve(t)
	projectResult, err := client.CreateProject(ctx, projectInput)
	if err != nil || projectResult.Revision != 1 || projectResult.Head != 2 {
		t.Fatalf("create project = %+v, %v", projectResult, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	agentResult, err := client.CreateAgent(ctx, api.CreateAgentInput{
		ID: testID(2), ProjectID: projectInput.ID, Name: "agent", Role: "orchestrator",
		Provider: "codex", Model: "gpt-5.6-luna", ReasoningEffort: "medium",
	})
	if err != nil || agentResult.Revision != 1 || agentResult.Head != 3 {
		t.Fatalf("create agent = %+v, %v", agentResult, err)
	}
	waitDispatch(t, done)
	assertNoSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	policyResult, err := client.SetAgentIdlePolicy(ctx, api.AgentIdlePolicyInput{AgentID: testID(2), ExpectedRevision: agentResult.Revision, Policy: "standing_instruction", AfterSeconds: 60, Instruction: "review retained changes"})
	if err != nil || policyResult.Revision != agentResult.Revision+1 {
		t.Fatalf("set idle policy = %+v, %v", policyResult, err)
	}
	waitDispatch(t, done)
	assertSchedulerWake(t, fixture.daemon)
	updated, found, err := fixture.store.Agent(ctx, mustAgentID(t, testID(2)))
	if err != nil || !found || updated.Idle.Policy != kernel.IdleStandingInstruction || updated.Idle.AfterSeconds != 60 || updated.Idle.Instruction != "review retained changes" {
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
	if err != nil || taskResult.Revision != 1 || taskResult.Head != 5 {
		t.Fatalf("enqueue task = %+v, %v", taskResult, err)
	}
	waitDispatch(t, done)
	assertSchedulerWake(t, fixture.daemon)

	done = fixture.serve(t)
	dispatchResult, err := client.SetDispatch(ctx, stop.Revision, true)
	if err != nil || dispatchResult.Revision != 3 || dispatchResult.Head != 6 {
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
	if snapshot.Head != 6 || len(snapshot.Projects) != 1 || len(snapshot.Agents) != 1 || len(snapshot.Tasks) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Projects[0].ID != projectInput.ID || snapshot.Projects[0].Name != projectInput.Name || snapshot.Agents[0].Role != "orchestrator" || snapshot.Tasks[0].Title != "public title" {
		t.Fatalf("projection lost public fields: %+v", snapshot)
	}
	if strings.Contains(snapshot.Tasks[0].Title, "private task body sentinel") {
		t.Fatal("task body crossed public projection")
	}

	project, found, err := fixture.store.Project(ctx, mustProjectID(t, projectInput.ID))
	if err != nil || !found || project.Root != projectInput.Root {
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

func TestOperatorStatusUsesBoundedSummaryBeyondSnapshotLimit(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	at := mustKernelTime(t, 2)
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: mustProjectID(t, testID(210)), Name: "status", Root: "/status"}, at)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(211)), ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", "file:"+fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	const tasks = kernel.SnapshotEntityLimit
	for index := 1; index <= tasks; index++ {
		id := make([]byte, kernel.IDBytes)
		binary.BigEndian.PutUint64(id[8:], uint64(index))
		incarnation := make([]byte, kernel.IDBytes)
		binary.BigEndian.PutUint64(incarnation[8:], uint64(index+tasks))
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, completed_at_ms, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 1, ?, '', 'queued', 0, NULL, 1, ?, ?)`, id, project.ID.Bytes(), agent.ID.Bytes(), incarnation, fmt.Sprintf("task-%d", index), index, index); err != nil {
			tx.Rollback()
			database.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	status, err := operator.Health(ctx, "")
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	if status.Summary == nil || status.Summary.Counts.Tasks != tasks || len(status.Summary.Recent) != kernel.StatusRecentLimit || status.Summary.Recent[0].Title != fmt.Sprintf("task-%d", tasks) || status.Summary.Why == "" || status.Summary.Report == "" {
		t.Fatalf("bounded status = %+v", status)
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

// TestAttemptCallsShareTheClientRetryWindow holds real dispatch work past the
// default budget but within the attempt client's RestartRetryWindow: content
// create waits on the live-attempt operation gate, and attempt task on its
// live issue observation. Both attempt calls complete once; an operator
// content create behind the same gate is still cut off at the default.
func TestAttemptCallsShareTheClientRetryWindow(t *testing.T) {
	ctx := context.Background()
	project := testID(110)
	withProject := func() *dispatchFixture {
		fixture := newDispatchFixture(t)
		operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
		if err != nil {
			t.Fatal(err)
		}
		done := fixture.serve(t)
		if _, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: project, Name: "slow dispatch", Root: contentRepositoryFixture(t)}); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
		return fixture
	}
	contentFixture := withProject()
	contentAttempt := prepareActiveAttemptInProject(t, contentFixture, 111, project, "worker")
	operatorFixture := withProject()
	operator, err := api.NewOperatorClient(operatorFixture.socket, operatorFixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	taskFixture := newDispatchFixture(t)
	issue := maintainer.Issue{ID: 81, NodeID: "I_slow", Number: 9, Title: "Slow scope", Body: "Slow body", State: "open", URL: "https://github.com/team/issues/issues/9"}
	issue.Author.Login, issue.Author.Type = "outsider", "User"
	taskAttempt, _ := prepareCustomerIntakeAttempt(t, taskFixture, &issue)
	release := make(chan struct{})
	live := taskFixture.daemon.githubMCP
	taskFixture.daemon.githubMCP = func(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return live(ctx, request, repositories)
	}
	contentFixture.daemon.operationMu.Lock()
	operatorFixture.daemon.operationMu.Lock()
	contentInput := api.ContentInput{ID: testID(141), ProjectID: project, Kind: "custom", Title: "lesson", Body: "slow write"}

	var wg sync.WaitGroup
	var contentErr, taskErr, operatorErr error
	var contentTook, taskTook time.Duration
	var created api.Content
	for _, call := range []struct {
		fixture *dispatchFixture
		send    func()
	}{
		{contentFixture, func() {
			started := time.Now()
			created, contentErr = contentAttempt.client.ContentCreate(ctx, contentInput)
			contentTook = time.Since(started)
		}},
		{taskFixture, func() {
			started := time.Now()
			_, taskErr = taskAttempt.client.Task(ctx)
			taskTook = time.Since(started)
		}},
		{operatorFixture, func() {
			bounded, cancel := context.WithTimeout(ctx, api.RestartRetryWindow)
			defer cancel()
			_, operatorErr = operator.ContentCreate(bounded, contentInput)
		}},
	} {
		done := call.fixture.serve(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			call.send()
			<-done
		}()
	}
	time.Sleep(defaultDispatchTimeout + time.Second)
	contentFixture.daemon.operationMu.Unlock()
	operatorFixture.daemon.operationMu.Unlock()
	close(release)
	wg.Wait()
	if contentErr != nil || created.Revision != 1 || contentTook <= defaultDispatchTimeout || contentTook >= api.RestartRetryWindow {
		t.Fatalf("attempt content create = %+v, %v after %v", created, contentErr, contentTook)
	}
	if taskErr != nil || taskTook <= defaultDispatchTimeout || taskTook >= api.RestartRetryWindow {
		t.Fatalf("attempt task = %v after %v", taskErr, taskTook)
	}
	if operatorErr == nil {
		t.Fatal("operator call outlived the default dispatch budget")
	}
	for fixture, want := range map[*dispatchFixture]int{contentFixture: 1, operatorFixture: 0} {
		listed, err := fixture.store.ListContent(ctx, mustProjectID(t, project), "custom", 0, 10)
		if err != nil || len(listed.Items) != want {
			t.Fatalf("content rows = %d, %v; want %d", len(listed.Items), err, want)
		}
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
	if err != nil || result.Revision != uint64(active.run.Revision.Int64()+1) || result.Head != 10 {
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
	// An undeadlined call gets the client's 5s default, the same as the waits
	// below, so a client that never hears the daemon would race them instead of
	// failing. A longer explicit deadline leaves the daemon to end the call.
	request, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	result := make(chan error, 1)
	go func() {
		_, err := active.client.Succeed(request, "cancelled request must not commit")
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
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: claude, ProjectID: active.run.ProjectID.String(), Name: "typist", Role: "worker", Provider: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: claudeTask, ProjectID: active.run.ProjectID.String(), AssignedAgentID: claude, IncarnationID: testID(63), Title: "typed", Body: strings.Repeat("x", 7000)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.SendBackTask(ctx, api.SendBackInput{TaskID: claudeTask, Note: strings.Repeat("&", 1300)}); !errors.As(err, &remote) || remote.Code() != api.RemoteTooLarge {
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
	if _, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: elsewhere, Name: "elsewhere", Root: contentRepositoryFixture(t)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: stranger, ProjectID: elsewhere, Name: "stranger", Role: "worker", Provider: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: foreign, ProjectID: elsewhere, AssignedAgentID: stranger, IncarnationID: testID(74), Title: "not yours", Body: strings.Repeat("x", 7000)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := active.client.SendBack(ctx, api.SendBackInput{TaskID: foreign, Note: strings.Repeat("&", 1300)}); !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
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

func prepareActiveAttemptInProject(t *testing.T, fixture *dispatchFixture, seed byte, projectID, role string, enqueue ...func()) activeAttempt {
	return prepareActiveAttemptInProjectWithProvider(t, fixture, seed, projectID, role, "shell", enqueue...)
}

func prepareActiveAttemptInProjectWithProvider(t *testing.T, fixture *dispatchFixture, seed byte, projectID, role, provider string, enqueue ...func()) activeAttempt {
	return prepareActiveAttemptInProjectWithProviderAndTask(t, fixture, seed, projectID, role, provider, testID(seed+2), enqueue...)
}

func prepareActiveAttemptInProjectWithProviderAndTask(t *testing.T, fixture *dispatchFixture, seed byte, projectID, role, provider, taskID string, enqueue ...func()) activeAttempt {
	t.Helper()
	ctx := context.Background()
	agentID, incarnationID := testID(seed+1), testID(seed+3)
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
	id, err := decodeID(projectID, kernel.ProjectIDFromBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, foundProject, err := fixture.store.Project(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !foundProject {
		call(func() error {
			_, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "project", Root: contentRepositoryFixture(t)})
			return err
		})
	}
	if _, foundAgent, err := fixture.store.Agent(ctx, mustAgentID(t, agentID)); err != nil {
		t.Fatal(err)
	} else if !foundAgent {
		call(func() error {
			_, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: agentID, ProjectID: projectID, Name: "agent", Role: role, Provider: provider})
			return err
		})
	}
	if len(enqueue) == 1 {
		enqueue[0]()
	} else {
		call(func() error {
			_, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, Title: "task", Body: "private", Priority: 1})
			return err
		})
	}
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
	library, _ := renderKnowledgeContext(active.run.Provider, active.run.ProjectID, nil, knowledgeContextBytes)
	want := api.AttemptTask{
		Task:                   "private" + string(library),
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

func TestAttemptTaskUsesCurrentAcceptedIssueBody(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	issue := maintainer.Issue{ID: 81, NodeID: "I_attempt", Number: 9, Title: "Original scope", Body: "", State: "open", URL: "https://github.com/team/issues/issues/9"}
	issue.Author.Login, issue.Author.Type = "outsider", "User"
	active, queued := prepareCustomerIntakeAttempt(t, fixture, &issue)
	issue.Body = "Edited scope"
	liveShell, err := fixture.daemon.currentTaskText(ctx, kernel.ProviderShell, queued)
	if err != nil || !strings.Contains(liveShell, "Original scope") || !strings.Contains(liveShell, "Issue edited since acceptance: re-accept to apply") || strings.Contains(liveShell, "Edited scope") {
		t.Fatalf("shell task text = %q, err=%v", liveShell, err)
	}
	done := fixture.serve(t)
	assignment, err := active.client.Task(ctx)
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(assignment.Task, "Original scope") || !strings.Contains(assignment.Task, "Issue edited since acceptance: re-accept to apply") || strings.Contains(assignment.Task, "Edited scope") {
		t.Fatalf("attempt task = %q", assignment.Task)
	}
}

// prepareCustomerIntakeAttempt accepts and imports issue in customer mode and
// gives its task to a live orchestrator attempt; the task fetch observes the
// live issue through githubMCP, which serves *issue as it is at that call.
func prepareCustomerIntakeAttempt(t *testing.T, fixture *dispatchFixture, issue *maintainer.Issue) (activeAttempt, kernel.Task) {
	t.Helper()
	ctx := context.Background()
	project := mustProjectID(t, testID(220))
	agent := mustAgentID(t, testID(221))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "intake", Root: "/intake/220"}, mustKernelTime(t, 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agent, ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	sourceID, err := kernel.IntakeSourceIDFromBytes(bytes.Repeat([]byte{222}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: sourceID, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), OverseerAgentID: agent, GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, mustKernelTime(t, 102))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustKernelTime(t, 103)); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.intakeIssues = func(_ context.Context, repository string, repositoryID uint64, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
		return maintainer.IssuePage{RepositoryID: repositoryID, Issues: []maintainer.Issue{*issue}}, nil
	}
	snapshot := intakeSnapshot(source, *issue)
	hash := snapshot.ContentHash()
	preview := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "preview", SourceID: source.ID.String(), Page: 1})
	if preview.State != "ok" || len(preview.Candidates) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	receipt := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "accept", SourceID: source.ID.String(), ExpectedRevision: uint64(source.Revision.Int64()), IssueNumber: issue.Number, ContentHash: hex.EncodeToString(hash[:])})
	if receipt.State != "accepted" {
		t.Fatalf("acceptance = %+v", receipt)
	}
	if imported := fixture.daemon.Intake(ctx, api.IntakeInput{Action: "tick", SourceID: source.ID.String(), Page: 1}); imported.State != "ok" || len(imported.ImportedTasks) != 1 {
		t.Fatalf("import = %+v", imported)
	}
	fixture.daemon.githubMCP = func(_ context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
		var call struct {
			Params struct {
				Arguments struct {
					Repository string `json:"repository"`
					Issue      uint64 `json:"issue_number"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request, &call); err != nil || call.Params.Arguments.Repository != "team/issues" || call.Params.Arguments.Issue != issue.Number || repositories["team/issues"] != 42 {
			t.Fatalf("live issue request = %s repositories=%v", request, repositories)
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"structuredContent": map[string]any{
			"number": issue.Number, "url": issue.URL, "title": issue.Title, "body": issue.Body, "labels": []string{}, "updated_at": "2026-10-13T00:00:00Z", "state": "open", "state_reason": nil,
		}, "content": []map[string]string{{"type": "text", "text": issue.Body}}, "isError": false}})
	}
	customerMode(t, fixture)
	queuedID, err := decodeID(receipt.TaskID, kernel.TaskIDFromBytes)
	if err != nil {
		t.Fatal(err)
	}
	queued, found, err := fixture.store.Task(ctx, queuedID)
	if err != nil || !found {
		t.Fatalf("queued task = %+v found=%v err=%v", queued, found, err)
	}
	assigned := mustAgentID(t, testID(221))
	queued, err = fixture.store.UpdateTask(ctx, queued.ID, queued.Revision, kernel.TaskPatch{AssignedAgentID: &assigned}, mustKernelTime(t, 1000))
	if err != nil {
		t.Fatal(err)
	}
	return prepareActiveAttemptInProjectWithProviderAndTask(t, fixture, 220, project.String(), "orchestrator", "shell", receipt.TaskID, func() {}), queued
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
		ID: replacementID, ProjectID: caller.run.ProjectID.String(), Name: "replacement", Role: "worker", Provider: "shell",
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

func TestDaemonOperatorStopUsesOperatorAuthorityAndExactRevisions(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 213, testID(213), "worker")
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	task, found, err := fixture.store.Task(context.Background(), active.run.TaskID)
	if err != nil || !found {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
	input := api.OverseerRunStopInput{OperationID: testID(214), TaskID: task.ID.String(), ExpectedTaskRevision: uint64(task.Revision.Int64()), RunID: active.run.ID.String(), ExpectedRunRevision: uint64(active.run.Revision.Int64())}
	stale := input
	stale.ExpectedRunRevision++
	done := fixture.serve(t)
	_, err = client.StopRun(context.Background(), stale)
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
		t.Fatalf("stale operator stop = %v", err)
	}
	done = fixture.serve(t)
	result, err := client.StopRun(context.Background(), input)
	waitDispatch(t, done)
	if err != nil || result.Intervention == nil || result.Intervention.OperationID != input.OperationID || result.Intervention.State != "delivered" {
		t.Fatalf("operator stop = %+v, %v", result, err)
	}
	done = fixture.serve(t)
	replay, err := client.StopRun(context.Background(), input)
	waitDispatch(t, done)
	if err != nil || !reflect.DeepEqual(replay.Intervention, result.Intervention) {
		t.Fatalf("operator stop replay = %+v, %v", replay, err)
	}
	done = fixture.serve(t)
	observed, err := client.WorkerOperation(context.Background(), input.OperationID)
	waitDispatch(t, done)
	if err != nil || observed.OperationID != input.OperationID || observed.TaskID != input.TaskID || observed.RunID != input.RunID || observed.State != "delivered" || observed.Detail != "" {
		t.Fatalf("operator receipt lookup = %+v, %v", observed, err)
	}
	done = fixture.serve(t)
	_, err = client.WorkerOperation(context.Background(), testID(215))
	waitDispatch(t, done)
	if !errors.As(err, &remote) || remote.Code() != api.RemoteNotFound {
		t.Fatalf("missing operator receipt = %v", err)
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
		_, err := client.CreateProject(ctx, api.CreateProjectInput{ID: projectID, Name: "read", Root: contentRepositoryFixture(t)})
		return err
	})
	call(func() error {
		_, err := client.CreateAgent(ctx, api.CreateAgentInput{ID: agentID, ProjectID: projectID, Name: "reader", Role: "worker", Provider: "shell"})
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
	if err := fixture.store.AddRunTokens(ctx, active.run.ID, 123); err != nil {
		t.Fatal(err)
	}
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
	fixture.daemon.unregisterLiveAttempt(active.run.ID, owner)
	done = fixture.serve(t)
	paths, err = operator.AgentPaths(ctx, api.AgentPathsInput{AgentID: active.run.AgentID.String()})
	waitDispatch(t, done)
	if err != nil || paths.RunID != active.run.ID.String() || paths.Telemetry == nil || paths.Telemetry.Tokens != 123 {
		t.Fatalf("durable agent paths after owner exit = %+v, %v", paths, err)
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
			if err != nil || result.Revision != uint64(active.run.Revision.Int64()+1) || result.Head != 10 {
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
	library, _ := renderKnowledgeContext(active.run.Provider, active.run.ProjectID, nil, knowledgeContextBytes)
	if err != nil || task.Task != "private"+string(library) {
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

func TestDaemonOverseerBackstopFreesLaneWhenProjectLimitIsDisabled(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProjectWithProvider(t, fixture, 53, testID(53), "orchestrator", "codex")
	ctx := context.Background()
	// An overseer that keeps waiting on an external gate instead of
	// checkpointing is cancelled at the backstop, releasing the lane (#1096).
	bound := active.run.AdmittedAt.Int64() + kernel.MaxOverseerRunSeconds*1000
	for _, at := range []int64{bound - 1, bound} {
		fixture.daemon.now = func() time.Time { return time.UnixMilli(at) }
		if err := fixture.daemon.enforceRunLiveness(ctx, SupervisorSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	run, found, err := fixture.store.Run(ctx, active.run.ID)
	if err != nil || !found || run.Phase != kernel.RunFinalizing || run.Proposal == nil || run.Proposal.Kind() != kernel.OutcomeFailed || run.Proposal.Detail() != kernel.OverseerRunLimitDetail || run.UpdatedAt.Int64() != bound {
		t.Fatalf("overseer at backstop = %+v, found=%v, err=%v", run, found, err)
	}
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
	incarnationID, err := decodeID(testID(54), kernel.IncarnationIDFromBytes)
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
		Projects: []kernel.ProjectSummary{{ID: projectID, Name: "project", Revision: revision}},
		Agents:   []kernel.AgentSummary{{ID: agentID, ProjectID: projectID, Name: "agent", Role: "worker", Provider: "codex", Revision: revision}},
		Tasks:    []kernel.TaskSummary{{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, IncarnationID: incarnationID, WorkRevision: revision, Title: "title", Status: "queued", Priority: 3, Revision: revision}},
	})
	if projected.Head != 0 || projected.Projects == nil || projected.Agents == nil || projected.Tasks == nil {
		t.Fatalf("projection emptiness/head = %+v", projected)
	}
	if projected.Projects[0].Name != "project" || projected.Projects[0].TokensUsed != 0 || projected.Agents[0].Provider != "codex" || projected.Tasks[0].Title != "title" {
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
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: workerID, ProjectID: active.run.ProjectID.String(), Name: "worker", Role: "worker", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateProject(ctx, api.CreateProjectInput{ID: testID(220), Name: "other", Root: contentRepositoryFixture(t)}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	done = fixture.serve(t)
	if _, err := operator.CreateAgent(ctx, api.CreateAgentInput{ID: testID(221), ProjectID: testID(220), Name: "foreign", Role: "worker", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	// Simulate a task accepted by the old operator route before this check.
	incarnation, _ := decodeID(testID(212), kernel.IncarnationIDFromBytes)
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

func TestOverseerEnqueuePinsContentWhileSchedulerRunsAndRejectsPostHocAttach(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttempt(t, fixture, 230)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		select {
		case <-fixture.daemon.schedulerWake:
		default:
			goto drained
		}
	}
drained:
	started := make(chan struct{})
	var startedOnce sync.Once
	var probes atomic.Int64
	admitted := make(chan struct{})
	var admittedOnce sync.Once
	var admissionErr atomic.Value
	keysReady := make(chan struct{})
	var admissionKeys kernel.AdmissionKeys
	at, err := kernel.NewUnixMillis(2000)
	if err != nil {
		t.Fatal(err)
	}
	schedulerDone := make(chan error, 1)
	// The stub ends probes as RunNext does: no admission is an ErrConflict, and
	// an admitted run is held until shutdown, so its completion is always
	// validated while the scheduler stops rather than only when CI is slow.
	schedulerSpec := SupervisorSpec{scheduledAttempt: func(ctx context.Context, spec SupervisorSpec) (kernel.Run, error) {
		probe := probes.Add(1)
		if probe == 1 {
			startedOnce.Do(func() { close(started) })
		}
		<-keysReady
		admission, err := fixture.store.AdmitNext(context.Background(), admissionKeys, at)
		if err != nil {
			admissionErr.Store(err)
			spec.admissionObserved(false)
			return kernel.Run{}, err
		}
		spec.admissionObserved(admission.Admitted())
		if !admission.Admitted() {
			return kernel.Run{}, fmt.Errorf("%w: no admission (%s)", kernel.ErrConflict, admission.Reason.String())
		}
		admittedOnce.Do(func() { close(admitted) })
		<-ctx.Done()
		return *admission.Run, ctx.Err()
	}, scheduledCompletion: func(run kernel.Run) error {
		// The stub never launches its run, so it stays admitted; production
		// validation would rightly call that outcome-unknown. Require exactly
		// this admission instead.
		if run.ID != admissionKeys.RunID || run.Phase != kernel.RunAdmitted {
			return fmt.Errorf("%w: unexpected scheduled completion %+v", kernel.ErrCorruptState, run)
		}
		return nil
	}}
	go func() { schedulerDone <- fixture.daemon.RunScheduler(ctx, schedulerSpec) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not start")
	}

	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	if _, err := operator.CreateAgent(context.Background(), api.CreateAgentInput{ID: testID(237), ProjectID: active.run.ProjectID.String(), Name: "worker", Role: "worker", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	bearer := bytes.Repeat([]byte{240}, 32)
	digestBytes := sha256.Sum256(bearer)
	digest, err := kernel.AttemptDigestFromBytes(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := kernel.ChangeIDFromBytes(mustIDBytes(t, testID(249)))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := kernel.ResultProofDigestFromBytes(bytes.Repeat([]byte{251}, 32))
	if err != nil {
		t.Fatal(err)
	}
	admissionKeys = kernel.AdmissionKeys{ResultProofDigest: proof, RunID: mustRunID(t, testID(241)), TerminalSessionID: mustTerminalSessionID(t, testID(242)), AttemptDigest: digest, CandidateChangeID: candidate, RuntimeRoot: filepath.Join(filepath.Dir(fixture.socket), "runtime-240"), Resources: kernel.AdmissionResourceIDs{RuntimeRoot: mustResourceID(t, testID(245)), RunnerProcess: mustResourceID(t, testID(246)), ProviderProcess: mustResourceID(t, testID(247)), ProviderGroup: mustResourceID(t, testID(248))}}
	close(keysReady)
	done = fixture.serve(t)
	content, err := operator.ContentCreate(context.Background(), api.ContentInput{ID: testID(234), ProjectID: active.run.ProjectID.String(), Kind: "observation", Title: "accepted proposal", Body: "body", SourceReferences: `{"status":"tentative","record_type":"proposal"}`})
	waitDispatch(t, done)
	if err != nil || content.Revision != 1 {
		t.Fatalf("proposal content = %+v, %v", content, err)
	}
	taskID := testID(235)
	done = fixture.serve(t)
	_, err = active.client.OverseerEnqueueTask(context.Background(), api.OverseerTaskCreateInput{ID: taskID, AssignedAgentID: testID(237), IncarnationID: testID(236), Title: "accepted proposal", Content: []api.ContentPinInput{{ContentID: content.ID, ContentRevision: content.Revision}}})
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		if err, ok := admissionErr.Load().(error); ok {
			t.Fatalf("admission = %v", err)
		}
		t.Fatal("scheduler did not admit enqueued task")
	}
	task, found, err := fixture.store.Task(context.Background(), mustTaskID(t, taskID))
	if err != nil || !found {
		t.Fatal(err)
	}
	references, err := fixture.store.TaskContentReferences(context.Background(), active.run.ProjectID, task.ID, task.WorkRevision, false)
	if err != nil || len(references) != 1 || references[0].TaskWorkRevision.Int64() != 1 {
		t.Fatalf("daemon enqueue references = %+v, %v", references, err)
	}
	done = fixture.serve(t)
	err = active.client.ContentAttach(context.Background(), api.ContentAttachInput{TaskID: taskID, ProjectID: active.run.ProjectID.String(), ContentID: content.ID, ContentRevision: 1})
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
		t.Fatalf("post-hoc overseer attach = %v", err)
	}
	cancel()
	if err := waitSchedulerDone(t, schedulerDone); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler shutdown = %v", err)
	}
}

func TestDaemonSelectAgentModelRequiresWorkerRevisionAndCompatibleControls(t *testing.T) {
	fixture := newDispatchFixture(t)
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project := api.CreateProjectInput{ID: testID(50), Name: "project", Root: contentRepositoryFixture(t)}
	done := fixture.serve(t)
	if _, err := client.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	for _, input := range []api.CreateAgentInput{
		{ID: testID(51), ProjectID: project.ID, Name: "worker", Role: "worker", Provider: "codex"},
		{ID: testID(52), ProjectID: project.ID, Name: "overseer", Role: "orchestrator", Provider: "codex"},
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
	if !errors.As(err, &remote) || remote.Code() != api.RemoteForbidden {
		t.Fatalf("unprotected source = %v", err)
	}
	waitDispatch(t, done)
}

func registerDispatchLiveAttempt(t *testing.T, fixture *dispatchFixture, active activeAttempt) {
	t.Helper()
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.daemon.unregisterLiveAttempt(active.run.ID, live) })
}

func TestDaemonSourceAllowsClaudeOrchestratorRoute(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProjectWithProvider(t, fixture, 72, testID(72), "orchestrator", "claude_code")
	registerDispatchLiveAttempt(t, fixture, active)
	done := fixture.serve(t)
	_, err := active.client.Source(context.Background(), testID(73))
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteNotFound {
		t.Fatalf("Claude orchestrator source = %v", err)
	}
	waitDispatch(t, done)
}

// A task text starting "review handoff" is ordinary: its worker gets no
// source receipt.
func TestDaemonSourceRefusesWorkerEvenWithReviewHandoffText(t *testing.T) {
	fixture := newDispatchFixture(t)
	const seed = 74
	target := testID(73)
	title := "review handoff " + target + " " + testID(75) + " " + strings.Repeat("c", 40) + " 1 1"
	active := prepareActiveAttemptInProjectWithProvider(t, fixture, seed, testID(seed), "worker", "claude_code", func() {
		incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(seed+3)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.EnqueueTask(context.Background(), kernel.NewTask{
			ID: mustTaskID(t, testID(seed+2)), ProjectID: mustProjectID(t, testID(seed)), AssignedAgentID: mustAgentID(t, testID(seed+1)), IncarnationID: incarnation, Title: title,
		}, mustKernelTime(t, 10)); err != nil {
			t.Fatal(err)
		}
	})
	registerDispatchLiveAttempt(t, fixture, active)
	done := fixture.serve(t)
	_, err := active.client.Source(context.Background(), target)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
		t.Fatalf("worker source = %v", err)
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

func TestDaemonSelectOverseerAccountPreservesSelectionGuards(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	client, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	project := api.CreateProjectInput{ID: testID(40), Name: "project", Root: contentRepositoryFixture(t)}
	done := fixture.serve(t)
	_, err = client.CreateProject(ctx, project)
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	_, err = client.CreateAgent(ctx, api.CreateAgentInput{ID: testID(41), ProjectID: project.ID, Name: "overseer", Role: "orchestrator", Provider: "codex"})
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

// TestRefusalRepliesCarryABoundedReason pins the one normalization a refusal
// reason needs before it can cross the local API: without it a reason the
// grammar rejects would silently fall back to a bare code, which is the
// failure this detail exists to end.
func TestRefusalRepliesCarryABoundedReason(t *testing.T) {
	refusal := kernel.NewOutcomeRefusal(fmt.Errorf("%w: %s (%w)", errDirtyWorkerChange, testID(0x21), kernel.ErrConflict))
	var unwrapped *kernel.OutcomeRefusal
	if !errors.As(refusal, &unwrapped) {
		t.Fatalf("refusal = %v", refusal)
	}
	detail := boundedDetail(unwrapped.Unwrap())
	if !strings.HasPrefix(detail, errDirtyWorkerChange.Error()) || !strings.Contains(detail, testID(0x21)) {
		t.Fatalf("dirty refusal detail = %q", detail)
	}
	if _, err := api.NewErrorDetailReply(api.RemoteConflict, detail); err != nil {
		t.Fatalf("dirty refusal detail rejected by the wire grammar: %v", err)
	}
	// A cause with controls or over the bound still reaches the caller.
	for name, cause := range map[string]error{
		"controls":  errors.New("refused\nfor\ttwo\x9breasons"),
		"unbounded": errors.New(strings.Repeat("reason ", api.MaxRemoteErrorDetail)),
	} {
		bounded := boundedDetail(cause)
		if _, err := api.NewErrorDetailReply(api.RemoteConflict, bounded); err != nil {
			t.Fatalf("%s cause left the caller with a bare code: %v", name, err)
		}
	}
	if boundedDetail(nil) != "" {
		t.Fatalf("nil cause detail = %q", boundedDetail(nil))
	}
}

// Only a specialist's review run (its carrier) reads the operator's views
// with its attempt credential, and of intake only its list.
func TestDaemonOperatorReadsAdmitOnlySpecialistCarrier(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	project, seed := testID(211), byte(211)
	enqueueCarrier := func() {
		operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
		if err != nil {
			t.Fatal(err)
		}
		done := fixture.serve(t)
		if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: testID(seed + 2), ProjectID: project, AssignedAgentID: testID(seed + 1), IncarnationID: testID(seed + 3), Title: "Standing instruction", Body: "Review.", Priority: 1}); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	}
	active := prepareActiveAttemptInProject(t, fixture, seed, project, "worker", enqueueCarrier)
	reader, err := api.NewAttemptReaderFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := func(name string, invoke func() error) {
		t.Helper()
		done := fixture.serve(t)
		var remote *api.RemoteError
		if err := invoke(); !errors.As(err, &remote) || remote.Code() != api.RemoteForbidden {
			t.Fatalf("%s = %v, want forbidden", name, err)
		}
		waitDispatch(t, done)
	}
	humanRequests := func() error { _, err := reader.HumanRequests(ctx); return err }
	forbidden("ordinary worker human list", humanRequests)
	done := fixture.serve(t)
	if report, err := reader.Report(ctx, api.ReportInput{TaskID: active.run.TaskID.String()}); err != nil || len(report.Lines) == 0 {
		t.Fatalf("ordinary worker own why = %+v, %v", report, err)
	}
	waitDispatch(t, done)
	forbidden("ordinary worker sibling why", func() error {
		_, err := reader.Report(ctx, api.ReportInput{TaskID: testID(212)})
		return err
	})

	agent, _, err := fixture.store.Agent(ctx, active.run.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := kernel.IdleStandingInstruction, uint32(3600), "Review the factory."
	if _, err := fixture.store.UpdateAgent(ctx, agent.ID, agent.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, agent.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	for name, invoke := range map[string]func() error{
		"human list":  humanRequests,
		"status":      func() error { _, err := reader.Snapshot(ctx); return err },
		"intake list": func() error { _, err := reader.Intake(ctx, api.IntakeInput{Action: "list"}); return err },
		"task read": func() error {
			task, _, err := fixture.store.Task(ctx, active.run.TaskID)
			if err == nil {
				_, err = reader.ReadTask(ctx, api.TaskReadInput{TaskID: task.ID.String(), ExpectedRevision: uint64(task.Revision.Int64())})
			}
			return err
		},
	} {
		done := fixture.serve(t)
		if err := invoke(); err != nil {
			t.Fatalf("specialist %s = %v", name, err)
		}
		waitDispatch(t, done)
	}
	forbidden("specialist intake write", func() error {
		_, err := reader.Intake(ctx, api.IntakeInput{Action: "linear_disconnect"})
		return err
	})
}

// A specialist's operator views are its own project's; the operator still
// reads the whole factory.
func TestDaemonSpecialistOperatorReadsStayInItsProject(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	other := prepareActiveAttemptInProject(t, fixture, 231, testID(231), "worker")
	done := fixture.serve(t)
	if _, err := other.client.RequestHuman(ctx, api.HumanQuestionInput{IdempotencyKey: testID(241), Question: "other project question"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	specialist := prepareActiveAttemptInProject(t, fixture, 211, testID(211), "worker", func() {
		operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
		if err != nil {
			t.Fatal(err)
		}
		done := fixture.serve(t)
		if _, err := operator.EnqueueTask(ctx, api.EnqueueTaskInput{ID: testID(213), ProjectID: testID(211), AssignedAgentID: testID(212), IncarnationID: testID(214), Title: "Standing instruction", Body: "Review.", Priority: 1}); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
	})
	done = fixture.serve(t)
	if _, err := specialist.client.RequestHuman(ctx, api.HumanQuestionInput{IdempotencyKey: testID(242), Question: "own project question"}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, done)
	agent, _, err := fixture.store.Agent(ctx, specialist.run.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	policy, after, instruction := kernel.IdleStandingInstruction, uint32(3600), "Review the factory."
	if _, err := fixture.store.UpdateAgent(ctx, agent.ID, agent.Revision, kernel.AgentPatch{IdlePolicy: &policy, IdleAfterSeconds: &after, IdleInstruction: &instruction}, agent.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	for i, run := range []kernel.Run{specialist.run, other.run} {
		raw, _ := hex.DecodeString(testID(byte(251 + i)))
		id, _ := kernel.IntakeSourceIDFromBytes(raw)
		if _, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: id, ProjectID: run.ProjectID, TargetRepositoryID: kernel.RepositoryID(run.ProjectID), GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", Policy: kernel.IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 1}, mustKernelTime(t, 1001)); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := api.NewAttemptReaderFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	// projects lists the projects each view reveals, wanting only own or both.
	projects := func(client *api.OperatorClient) map[string]bool {
		seen := map[string]bool{}
		call := func(invoke func() error) {
			t.Helper()
			done := fixture.serve(t)
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			waitDispatch(t, done)
		}
		call(func() error {
			snapshot, err := client.Snapshot(ctx)
			if client == reader && (len(snapshot.Accounts) != 0 || len(snapshot.PeerQuestions) != 0 || len(snapshot.HumanRequests) != 0) {
				t.Fatalf("scoped snapshot leaked factory collections: accounts=%d peer_questions=%d human_requests=%d", len(snapshot.Accounts), len(snapshot.PeerQuestions), len(snapshot.HumanRequests))
			}
			for _, project := range snapshot.Projects {
				seen["project "+project.ID] = true
			}
			for _, agent := range snapshot.Agents {
				seen["agent "+agent.ProjectID] = true
			}
			for _, task := range snapshot.Tasks {
				seen["task "+task.ProjectID] = true
			}
			return err
		})
		call(func() error {
			requests, err := client.HumanRequests(ctx)
			for _, request := range requests.Requests {
				seen["human "+map[string]string{specialist.run.TaskID.String(): testID(211), other.run.TaskID.String(): testID(231)}[request.TaskID]] = true
			}
			return err
		})
		call(func() error {
			intake, err := client.Intake(ctx, api.IntakeInput{Action: "list"})
			for _, source := range intake.Sources {
				seen["intake "+source.ProjectID] = true
			}
			return err
		})
		return seen
	}
	want := func(ids ...string) map[string]bool {
		seen := map[string]bool{}
		for _, id := range ids {
			for _, view := range []string{"project", "agent", "task", "human", "intake"} {
				seen[view+" "+id] = true
			}
		}
		return seen
	}
	if got := projects(reader); !maps.Equal(got, want(testID(211))) {
		t.Fatalf("specialist views = %v", got)
	}
	if got := projects(operator); !maps.Equal(got, want(testID(211), testID(231))) {
		t.Fatalf("operator views = %v", got)
	}
	for name, client := range map[string]*api.OperatorClient{"specialist": reader, "operator": operator} {
		task, _, err := fixture.store.Task(ctx, other.run.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		done := fixture.serve(t)
		_, err = client.ReadTask(ctx, api.TaskReadInput{TaskID: task.ID.String(), ExpectedRevision: uint64(task.Revision.Int64())})
		var remote *api.RemoteError
		if forbidden := errors.As(err, &remote) && remote.Code() == api.RemoteForbidden; forbidden != (name == "specialist") || !forbidden && err != nil {
			t.Fatalf("%s other-project task read = %v", name, err)
		}
		waitDispatch(t, done)
	}
	done = fixture.serve(t)
	if intake, err := reader.Intake(ctx, api.IntakeInput{Action: "list", ProjectID: testID(231)}); err != nil || len(intake.Sources) != 1 || intake.Sources[0].ProjectID != testID(211) {
		t.Fatalf("specialist other-project intake list = %+v, %v", intake, err)
	}
	waitDispatch(t, done)
}

// An overseer lists intake and requests one fresh exact-head review in its
// own project (#1725), and reaches no other operator view or intake write.
func TestDaemonOverseerListsIntakeAndRequestsAReview(t *testing.T) {
	fixture := newDispatchFixture(t)
	ctx := context.Background()
	active := prepareActiveAttempt(t, fixture, 221)
	reader, err := api.NewAttemptReaderFromEnvironment(fixture.socket)
	if err != nil {
		t.Fatal(err)
	}
	review := func(project string) func() error {
		return func() error {
			_, err := reader.Intake(ctx, api.IntakeInput{Action: "review_pr", ProjectID: project, ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), BaseRef: "main", Provider: "codex"}})
			return err
		}
	}
	for name, invoke := range map[string]func() error{
		"intake list": func() error { _, err := reader.Intake(ctx, api.IntakeInput{Action: "list"}); return err },
		"review":      review(active.run.ProjectID.String()),
	} {
		done := fixture.serve(t)
		if err := invoke(); err != nil {
			t.Fatalf("overseer %s = %v", name, err)
		}
		waitDispatch(t, done)
	}
	for name, invoke := range map[string]func() error{
		"status":                 func() error { _, err := reader.Snapshot(ctx); return err },
		"review another project": review(testID(222)),
		"intake write":           func() error { _, err := reader.Intake(ctx, api.IntakeInput{Action: "linear_disconnect"}); return err },
	} {
		done := fixture.serve(t)
		var remote *api.RemoteError
		if err := invoke(); !errors.As(err, &remote) || remote.Code() != api.RemoteForbidden {
			t.Fatalf("overseer %s = %v, want forbidden", name, err)
		}
		waitDispatch(t, done)
	}
}
