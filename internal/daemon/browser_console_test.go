package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// consoleFixture pairs one browser client and gives it a project, an agent and
// a queued task to edit.
type consoleFixture struct {
	*adapterFixture
	project kernel.Project
	agent   kernel.Agent
	task    kernel.Task
}

func newConsoleFixture(t *testing.T, capabilities kernel.BrowserCapabilityMask, root string) *consoleFixture {
	t.Helper()
	fixture := newAdapterFixture(t, capabilities)
	fixture.pair(t)
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 0x21))
	agentID, _ := kernel.AgentIDFromBytes(adapterID(t, 0x22))
	taskID, _ := kernel.TaskIDFromBytes(adapterID(t, 0x23))
	incarnationID, _ := kernel.IncarnationIDFromBytes(adapterID(t, 0x24))
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "console", Root: root}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "console agent", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 4}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: "console task", Priority: 1}, adapterTime(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	return &consoleFixture{adapterFixture: fixture, project: project, agent: agent, task: task}
}

func consoleRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTopologyFixture(t, root, "go.mod", "module example.com/console\n")
	writeTopologyFixture(t, root, "one/one.go", "package one\n")
	return root
}

// The adapter is the authority on both the edit and the revision the client
// gets back: the result must name the same entity at the next revision.
func TestBrowserConsoleUpdatesAdvanceTheExactRevision(t *testing.T) {
	fixture := newConsoleFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityHumanActions, consoleRoot(t))
	ctx := context.Background()
	client := rawBrowserClient(fixture.client.ID)
	model, effort, paused := "gpt-5-codex", "high", browserprotocol.Bool(true)
	agentResult, err := fixture.backend.UpdateAgent(ctx, client, browserprotocol.AgentUpdate{
		AgentID: fixture.agent.ID.String(), ExpectedRevision: decimalRevision(fixture.agent.Revision),
		Model: &model, ReasoningEffort: &effort, Paused: &paused,
	})
	if err != nil || agentResult.AgentID != fixture.agent.ID.String() || agentResult.Revision != decimalRevision(fixture.agent.Revision)+1 {
		t.Fatalf("agent update = %+v, %v", agentResult, err)
	}
	stored, found, err := fixture.store.Agent(ctx, fixture.agent.ID)
	if err != nil || !found || stored.Model != model || stored.ReasoningEffort != effort || !stored.Paused {
		t.Fatalf("stored agent = %+v, found=%v, err=%v", stored, found, err)
	}
	// The same observation cannot be spent twice.
	if _, err := fixture.backend.UpdateAgent(ctx, client, browserprotocol.AgentUpdate{
		AgentID: fixture.agent.ID.String(), ExpectedRevision: decimalRevision(fixture.agent.Revision), Paused: &paused,
	}); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("replayed agent update = %v", err)
	}

	title, priority, status := "edited", int64(5), "cancelled"
	taskResult, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: decimalRevision(fixture.task.Revision),
		Title: &title, Priority: &priority,
	})
	if err != nil || taskResult.TaskID != fixture.task.ID.String() || taskResult.Revision != decimalRevision(fixture.task.Revision)+1 {
		t.Fatalf("task update = %+v, %v", taskResult, err)
	}
	cancelled, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: taskResult.Revision, Status: &status,
	})
	if err != nil || cancelled.Revision != taskResult.Revision+1 {
		t.Fatalf("task cancel = %+v, %v", cancelled, err)
	}
	storedTask, found, err := fixture.store.Task(ctx, fixture.task.ID)
	if err != nil || !found || storedTask.Title != title || storedTask.Priority != priority || storedTask.Status != kernel.TaskCancelled {
		t.Fatalf("stored task = %+v, found=%v, err=%v", storedTask, found, err)
	}
	// A task that has left the queue is a conflict, not a fresh edit.
	if _, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: cancelled.Revision, Title: &title,
	}); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("edit after cancel = %v", err)
	}
}

// Topology is an observation; the two updates are operator mutations. An
// observe-only pairing may do the first and none of the second.
func TestBrowserConsoleGatesUpdatesOnHumanActionsButNotTopology(t *testing.T) {
	root := consoleRoot(t)
	fixture := newConsoleFixture(t, kernel.BrowserCapabilityObserve, root)
	ctx := context.Background()
	client := rawBrowserClient(fixture.client.ID)
	paused, status := browserprotocol.Bool(true), "cancelled"
	if _, err := fixture.backend.UpdateAgent(ctx, client, browserprotocol.AgentUpdate{
		AgentID: fixture.agent.ID.String(), ExpectedRevision: decimalRevision(fixture.agent.Revision), Paused: &paused,
	}); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("observe-only agent update = %v", err)
	}
	if _, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: decimalRevision(fixture.task.Revision), Status: &status,
	}); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("observe-only task update = %v", err)
	}
	// A refused mutation left nothing behind.
	stored, found, err := fixture.store.Agent(ctx, fixture.agent.ID)
	if err != nil || !found || stored.Revision != fixture.agent.Revision || stored.Paused {
		t.Fatalf("agent after refused update = %+v, found=%v, err=%v", stored, found, err)
	}
	result, err := fixture.backend.Topology(ctx, client, browserprotocol.TopologyGet{ProjectID: fixture.project.ID.String()})
	if err != nil || result.ProjectID != fixture.project.ID.String() || len(result.Digest) != 64 || len(result.Nodes) == 0 {
		t.Fatalf("observe-only topology = %+v, %v", result, err)
	}
	for _, node := range result.Nodes {
		if node.Kind == "" || node.Path == "" || node.SizeBucket == "" {
			t.Fatalf("topology node is not projected: %+v", node)
		}
	}
	// An unknown project is not found; a caller that gave up gets a retryable
	// answer rather than a fault.
	unknown, _ := kernel.ProjectIDFromBytes(adapterID(t, 0x25))
	if _, err := fixture.backend.Topology(ctx, client, browserprotocol.TopologyGet{ProjectID: unknown.String()}); !errors.Is(err, browser.ErrNotFound) {
		t.Fatalf("unknown project topology = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.backend.Topology(cancelled, client, browserprotocol.TopologyGet{ProjectID: fixture.project.ID.String()}); !errors.Is(err, browser.ErrRateLimited) {
		t.Fatalf("abandoned topology request = %v", err)
	}
}

func TestBrowserConsoleRejectsUnusableIdentitiesAndRevisions(t *testing.T) {
	fixture := newConsoleFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityHumanActions, consoleRoot(t))
	ctx := context.Background()
	client := rawBrowserClient(fixture.client.ID)
	unknownAgent, _ := kernel.AgentIDFromBytes(adapterID(t, 0x31))
	if _, err := fixture.backend.UpdateAgent(ctx, client, browserprotocol.AgentUpdate{
		AgentID: unknownAgent.String(), ExpectedRevision: 1,
	}); !errors.Is(err, browser.ErrNotFound) {
		t.Fatalf("unknown agent = %v", err)
	}
	if _, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: decimalRevision(fixture.task.Revision) + 5,
	}); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("stale task revision = %v", err)
	}
	// The durable key is (agent, project) together, so a foreign agent is a
	// refused edit rather than a corrupt row.
	foreignProject, _ := kernel.ProjectIDFromBytes(adapterID(t, 0x32))
	foreignAgent, _ := kernel.AgentIDFromBytes(adapterID(t, 0x33))
	other, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: foreignProject, Name: "other", Root: filepath.Join(t.TempDir(), "other")}, adapterTime(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: foreignAgent, ProjectID: other.ID, Name: "stranger", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ToolBudgetLimit: 1}, adapterTime(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	assigned := stranger.ID.String()
	if _, err := fixture.backend.UpdateTask(ctx, client, browserprotocol.TaskUpdate{
		TaskID: fixture.task.ID.String(), ExpectedRevision: decimalRevision(fixture.task.Revision), AssignedAgentID: &assigned,
	}); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("cross-project reassignment = %v", err)
	}
}
