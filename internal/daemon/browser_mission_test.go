//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserMissionRoundTripAndProjectAuthority(t *testing.T) {
	f := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail|kernel.BrowserCapabilityHumanActions)
	conn := f.pair(t)
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x51}, 16))
	otherID, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x52}, 16))
	for index, pair := range []struct {
		id   kernel.ProjectID
		name string
	}{
		{projectID, "mission-project"},
		{otherID, "other-project"},
	} {
		root := contentRepositoryFixture(t)
		if _, err := f.store.CreateProject(ctx, kernel.NewProject{ID: pair.id, Name: pair.name, Root: root}, adapterTime(t, int64(10+index))); err != nil {
			t.Fatal(err)
		}
	}
	ownerID, _ := kernel.AgentIDFromBytes(bytes.Repeat([]byte{0x53}, 16))
	owner, err := f.store.CreateAgent(ctx, kernel.NewAgent{ID: ownerID, ProjectID: projectID, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	missionID, _ := kernel.OutcomeIDFromBytes(bytes.Repeat([]byte{0x54}, 16))
	seq := 0
	send := func(operation string, input map[string]any) browserprotocol.ControlFrame {
		t.Helper()
		seq++
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := browserprotocol.EncodeProjectContent("mission-"+string(rune('a'+seq)), browserprotocol.ProjectContent{Operation: operation, Input: raw})
		if err != nil {
			t.Fatal(err)
		}
		adapterWrite(t, conn, wire)
		return adapterRead(t, conn)
	}
	frame := send("mission_create", map[string]any{"project_id": projectID.String(), "id": missionID.String(), "owner_agent_id": ownerID.String(), "expected_agent_revision": owner.Revision.Int64(), "objective": "Coordinate the production floor", "criteria": "The overseer records acceptance evidence"})
	if frame.Type != browserprotocol.TypeProjectContentResult {
		t.Fatalf("mission create = %+v", frame)
	}
	var created struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &created); err != nil || created.ID != missionID.String() || created.Kind != "mission" || created.State != "open" {
		t.Fatalf("created mission = %+v, %v", created, err)
	}
	frame = send("outcome_list", map[string]any{"project_id": projectID.String(), "kind": "mission", "limit": 1})
	if frame.Type != browserprotocol.TypeProjectContentResult {
		t.Fatalf("mission list = %+v", frame)
	}
	var listed struct {
		Items []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Objective string `json:"objective"`
			State     string `json:"state"`
			Stale     bool   `json:"stale"`
		} `json:"items"`
	}
	if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &listed); err != nil || len(listed.Items) != 1 || listed.Items[0].ID != missionID.String() || listed.Items[0].Kind != "mission" || listed.Items[0].Objective == "" || listed.Items[0].State != "open" || listed.Items[0].Stale {
		t.Fatalf("mission list output = %+v, %v", listed, err)
	}
	frame = send("mission_tasks", map[string]any{"project_id": projectID.String(), "id": missionID.String(), "limit": 8})
	if frame.Type != browserprotocol.TypeProjectContentResult {
		t.Fatalf("mission tasks = %+v", frame)
	}
	var tasks struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &tasks); err != nil || len(tasks.Tasks) != 1 {
		t.Fatalf("mission tasks output = %+v, %v", tasks, err)
	}
	for _, key := range []string{"task_id", "project_id", "title", "status", "assigned_agent_id", "revision", "work_revision", "priority", "blocked_reason"} {
		if _, ok := tasks.Tasks[0][key]; !ok {
			t.Fatalf("mission task missing %q: %+v", key, tasks.Tasks[0])
		}
	}
	if _, ok := tasks.Tasks[0]["revision"].(string); !ok {
		t.Fatalf("revision is not decimal string: %#v", tasks.Tasks[0]["revision"])
	}
	if _, ok := tasks.Tasks[0]["priority"].(string); !ok {
		t.Fatalf("priority is not decimal string: %#v", tasks.Tasks[0]["priority"])
	}
	if _, err := browserprotocol.EncodeProjectContentResult("mission-wire", browserprotocol.ProjectContentResult{Operation: "mission_tasks", Output: frame.Body.(browserprotocol.ProjectContentResult).Output}); err != nil {
		t.Fatalf("mission tasks wire encoding = %v", err)
	}
	frame = send("outcome_read", map[string]any{"project_id": otherID.String(), "id": missionID.String()})
	if frame.Type != browserprotocol.TypeError {
		t.Fatalf("cross-project mission read = %+v", frame)
	}
	if _, err := f.backend.ProjectContent(ctx, rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "mission_tasks", Input: []byte(`{"project_id":"` + projectID.String() + `","id":"` + missionID.String() + `","limit":9}`)}); !errors.Is(err, browser.ErrInvalidRequest) {
		t.Fatalf("mission task limit = %v", err)
	}
}
