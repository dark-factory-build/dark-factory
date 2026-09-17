package daemon

import (
	"bytes"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func projectionHandoff(t *testing.T) (kernel.RetainedChangeHandoff, kernel.ProjectID, kernel.EventSequence) {
	t.Helper()
	changeID, err := kernel.ChangeIDFromBytes(bytes.Repeat([]byte{2}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := kernel.TaskIDFromBytes(bytes.Repeat([]byte{3}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{1}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	work, err := kernel.NewRevision(1)
	if err != nil {
		t.Fatal(err)
	}
	head, err := kernel.NewEventSequence(1)
	if err != nil {
		t.Fatal(err)
	}
	return kernel.RetainedChangeHandoff{ChangeID: changeID, BaseCommit: "0123456789abcdef0123456789abcdef01234567", TaskID: taskID, TaskWorkRevision: work, ChangeRevision: work}, projectID, head
}

// Status carries each settled Change's identities and nothing about where
// it is; an explicit attempt source request answers that.
func TestProjectOverseerSnapshotCarriesHandoffIdentitiesWithoutLocations(t *testing.T) {
	handoff, projectID, head := projectionHandoff(t)
	handoff.HeadCommit = "89abcdef0123456789abcdef0123456789abcdef"
	snapshot := kernel.OverseerSnapshot{ProjectID: projectID, Head: head, Tasks: []kernel.OverseerTask{{ID: handoff.TaskID, ProjectID: projectID, Title: "state"}}, Handoffs: []kernel.RetainedChangeHandoff{handoff}}
	projected, err := projectOverseerSnapshot(snapshot)
	if err != nil || len(projected.Tasks) != 1 || len(projected.Handoffs) != 1 {
		t.Fatalf("projected = %+v, %v", projected, err)
	}
	got := projected.Handoffs[0]
	if got.ChangeID != handoff.ChangeID.String() || got.BaseCommit != handoff.BaseCommit || got.HeadCommit != handoff.HeadCommit || got.TaskID != handoff.TaskID.String() || got.TaskWorkRevision != uint64(handoff.TaskWorkRevision.Int64()) || got.ChangeRevision != uint64(handoff.ChangeRevision.Int64()) || got.SourcePath != "" || got.GitDirectory != "" || got.Branch != "" || got.Dirty {
		t.Fatalf("projected handoff = %+v", got)
	}
	handoff.HeadCommit = ""
	projected, err = projectOverseerSnapshot(kernel.OverseerSnapshot{ProjectID: projectID, Head: head, Handoffs: []kernel.RetainedChangeHandoff{handoff}})
	if err != nil || len(projected.Handoffs) != 1 || projected.Handoffs[0].HeadCommit != "" {
		t.Fatalf("Git-free handoff projected = %+v, %v", projected, err)
	}
}
