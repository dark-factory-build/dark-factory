package daemon

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestProjectOverseerSnapshotUsesOnlyPrivateSourceSnapshot(t *testing.T) {
	handoff, projectID, head := projectionHandoff(t)
	runtime := t.TempDir()
	source := filepath.Join(runtime, "retained-source", handoff.ChangeID.String())
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	projected, err := projectOverseerSnapshot(kernel.OverseerSnapshot{ProjectID: projectID, Head: head, Handoffs: []kernel.RetainedChangeHandoff{handoff}}, map[kernel.RetainedChangeHandoff]string{handoff: source})
	if err != nil || len(projected.Handoffs) != 1 || projected.Handoffs[0].SourcePath != source {
		t.Fatalf("projected handoff = %+v, %v", projected, err)
	}
}

func TestProjectOverseerTargetedSnapshotKeepsTaskStateWithoutSourceGrant(t *testing.T) {
	handoff, projectID, head := projectionHandoff(t)
	snapshot := kernel.OverseerSnapshot{ProjectID: projectID, Head: head, Tasks: []kernel.OverseerTask{{ID: handoff.TaskID, ProjectID: projectID, Title: "state"}}, Handoffs: []kernel.RetainedChangeHandoff{handoff}}
	projected, err := projectOverseerSnapshot(snapshot, nil)
	if err != nil || len(projected.Tasks) != 1 || len(projected.Handoffs) != 0 {
		t.Fatalf("ungranted task state = %+v, %v", projected, err)
	}
}

func TestProjectRetainedChangeHandoffsRejectsMissingAndEscapingSnapshots(t *testing.T) {
	handoff, _, _ := projectionHandoff(t)
	if _, err := projectRetainedChangeHandoffs(map[kernel.RetainedChangeHandoff]string{handoff: "/private/factory/changes/" + handoff.ChangeID.String()}); err == nil {
		t.Fatal("shared Changes path accepted")
	}
	source := filepath.Join(t.TempDir(), "retained-source", handoff.ChangeID.String())
	if _, err := projectRetainedChangeHandoffs(map[kernel.RetainedChangeHandoff]string{handoff: source}); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	projected, err := projectRetainedChangeHandoffs(map[kernel.RetainedChangeHandoff]string{handoff: source})
	if err != nil || len(projected) != 1 || projected[0].SourcePath != source {
		t.Fatalf("projected snapshot = %+v, %v", projected, err)
	}
}
