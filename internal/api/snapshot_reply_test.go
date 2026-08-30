//go:build darwin || linux

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotReplyKeepsEmptyCollectionsAndCopiesBacking(t *testing.T) {
	empty := DashboardSnapshot{
		Head:     0,
		Factory:  FactorySummary{Capacity: 1, Revision: 1},
		Projects: []ProjectSummary{},
		Agents:   []AgentSummary{},
		Tasks:    []TaskSummary{},
	}
	emptyReply, err := NewSnapshotReply(empty)
	if err != nil {
		t.Fatal(err)
	}
	if emptyReply.snapshot.Projects == nil || emptyReply.snapshot.Agents == nil || emptyReply.snapshot.Tasks == nil {
		t.Fatalf("reply collections became nil: %+v", emptyReply.snapshot)
	}
	encoded, err := json.Marshal(emptyReply.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"head":0,"factory":{"dispatch_enabled":false,"capacity":1,"active_runs":0,"revision":1},"projects":[],"agents":[],"tasks":[]}` {
		t.Fatalf("empty collection encoding = %s", encoded)
	}

	input := DashboardSnapshot{
		Head:     0,
		Factory:  FactorySummary{Capacity: 1, Revision: 1},
		Projects: []ProjectSummary{{ID: strings.Repeat("1", 32), Name: "project", Revision: 1}},
		Agents:   []AgentSummary{{ID: strings.Repeat("2", 32), ProjectID: strings.Repeat("1", 32), Name: "agent", Role: "worker", Revision: 1}},
		Tasks:    []TaskSummary{{ID: strings.Repeat("3", 32), ProjectID: strings.Repeat("1", 32), AssignedAgentID: strings.Repeat("2", 32), Title: "task", Status: "queued", Revision: 1}},
	}
	reply, err := NewSnapshotReply(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Projects[0].Name = "changed project"
	input.Agents[0].Name = "changed agent"
	input.Tasks[0].Title = "changed task"
	if reply.snapshot.Projects[0].Name != "project" || reply.snapshot.Agents[0].Name != "agent" || reply.snapshot.Tasks[0].Title != "task" {
		t.Fatal("reply retained caller backing array")
	}
}
