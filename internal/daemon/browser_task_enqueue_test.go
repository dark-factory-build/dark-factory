//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserTaskEnqueueCreatesPrivateDurableTaskAndWakesScheduler(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityHumanActions)
	projectID, err := kernel.ProjectIDFromBytes(adapterID(t, 60))
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := kernel.AgentIDFromBytes(adapterID(t, 61))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := kernel.TaskIDFromBytes(adapterID(t, 62))
	if err != nil {
		t.Fatal(err)
	}
	incarnationID, err := kernel.IncarnationIDFromBytes(adapterID(t, 63))
	if err != nil {
		t.Fatal(err)
	}
	project, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "browser enqueue", Root: "/private/browser-enqueue"}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "idle shell", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 2}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	connection := fixture.pair(t)
	select {
	case <-fixture.daemon.schedulerWake:
		t.Fatal("setup unexpectedly woke scheduler")
	default:
	}
	instruction := "PRIVATE_BROWSER_INSTRUCTION_SENTINEL"
	request := browserprotocol.TaskEnqueue{
		TaskID:                taskID.String(),
		IncarnationID:         incarnationID.String(),
		AgentID:               agent.ID.String(),
		ExpectedAgentRevision: browserprotocol.Decimal(agent.Revision.Int64()),
		Instruction:           instruction,
	}
	payload, err := browserprotocol.EncodeTaskEnqueue("enqueue", request)
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, payload)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeTaskEnqueueResult || frame.ID != "enqueue" {
		t.Fatalf("enqueue response = %+v", frame)
	}
	result := frame.Body.(browserprotocol.TaskEnqueueResult)
	if result.TaskID != taskID.String() || result.Revision != 1 || result.AgentRevision != request.ExpectedAgentRevision {
		t.Fatalf("enqueue result = %+v", result)
	}
	select {
	case <-fixture.daemon.schedulerWake:
	default:
		t.Fatal("successful browser enqueue did not wake scheduler")
	}
	stored, found, err := fixture.store.Task(context.Background(), taskID)
	if err != nil || !found {
		t.Fatalf("durable task = %+v, found=%v, err=%v", stored, found, err)
	}
	if stored.ProjectID != project.ID || stored.AssignedAgentID != agent.ID || stored.IncarnationID != incarnationID || stored.Title != "Direct instruction" || stored.Body != instruction || stored.Status != kernel.TaskQueued || stored.Priority != 0 {
		t.Fatalf("durable task = %+v", stored)
	}
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), instruction) {
		t.Fatalf("public snapshot leaked instruction: %s", encoded)
	}
}

func TestBrowserTaskEnqueueRejectsMissingCapabilityAndStaleAgent(t *testing.T) {
	for index, test := range []struct {
		name         string
		capabilities kernel.BrowserCapabilityMask
		stale        bool
		want         browserprotocol.ErrorCode
	}{
		{name: "missing capability", capabilities: kernel.BrowserCapabilityObserve, want: browserprotocol.ErrorUnauthorized},
		{name: "stale agent", capabilities: kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityHumanActions, stale: true, want: browserprotocol.ErrorStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdapterFixture(t, test.capabilities)
			projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, byte(70+index*4)))
			agentID, _ := kernel.AgentIDFromBytes(adapterID(t, byte(71+index*4)))
			taskID, _ := kernel.TaskIDFromBytes(adapterID(t, byte(72+index*4)))
			incarnationID, _ := kernel.IncarnationIDFromBytes(adapterID(t, byte(73+index*4)))
			project, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "browser reject", Root: "/private/browser-reject"}, adapterTime(t, 10))
			if err != nil {
				t.Fatal(err)
			}
			agent, err := fixture.store.CreateAgent(context.Background(), kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "idle shell", Role: kernel.RoleWorker, Provider: kernel.ProviderShell, ToolBudgetLimit: 2}, adapterTime(t, 11))
			if err != nil {
				t.Fatal(err)
			}
			connection := fixture.pair(t)
			revision := browserprotocol.Decimal(agent.Revision.Int64())
			if test.stale {
				revision++
			}
			request := browserprotocol.TaskEnqueue{TaskID: hex.EncodeToString(taskID.Bytes()), IncarnationID: hex.EncodeToString(incarnationID.Bytes()), AgentID: hex.EncodeToString(agent.ID.Bytes()), ExpectedAgentRevision: revision, Instruction: "must not persist"}
			payload, err := browserprotocol.EncodeTaskEnqueue("enqueue", request)
			if err != nil {
				t.Fatal(err)
			}
			adapterWrite(t, connection, payload)
			frame := adapterRead(t, connection)
			if frame.Type != browserprotocol.TypeError || frame.ID != "enqueue" || frame.Body.(browserprotocol.Error).Code != test.want {
				t.Fatalf("rejection = %+v, want %s", frame, test.want)
			}
			if _, found, err := fixture.store.Task(context.Background(), taskID); err != nil || found {
				t.Fatalf("rejected task found=%v err=%v", found, err)
			}
			select {
			case <-fixture.daemon.schedulerWake:
				t.Fatal("rejected enqueue woke scheduler")
			default:
			}
		})
	}
}
