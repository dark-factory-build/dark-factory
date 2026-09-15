package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProjectOverseerSnapshotPublishesOnlyTheDaemonDerivedExactTree(t *testing.T) {
	parent := t.TempDir()
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
	if err := os.Mkdir(filepath.Join(parent, changeID.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	handoff := kernel.RetainedChangeHandoff{ChangeID: changeID, BaseCommit: "0123456789abcdef0123456789abcdef01234567", TaskID: taskID, TaskWorkRevision: work, ChangeRevision: work}
	projected, err := projectOverseerSnapshot(kernel.OverseerSnapshot{ProjectID: projectID, Head: head, Handoffs: []kernel.RetainedChangeHandoff{handoff}}, parent, []kernel.RetainedChangeHandoff{handoff})
	if err != nil || len(projected.Handoffs) != 1 || projected.Handoffs[0].SourcePath != filepath.Join(parent, changeID.String()) {
		t.Fatalf("projected handoff = %+v, %v", projected.Handoffs, err)
	}
	if err := os.Remove(filepath.Join(parent, changeID.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := projectOverseerSnapshot(kernel.OverseerSnapshot{Handoffs: []kernel.RetainedChangeHandoff{{ChangeID: changeID}}}, parent, []kernel.RetainedChangeHandoff{{ChangeID: changeID}}); err == nil {
		t.Fatal("missing retained tree was projected")
	}
	if projected, err := projectOverseerSnapshot(kernel.OverseerSnapshot{Handoffs: []kernel.RetainedChangeHandoff{handoff}}, parent, nil); err != nil || len(projected.Handoffs) != 0 {
		t.Fatalf("ungranted handoff = %+v, %v", projected.Handoffs, err)
	}
	stale := handoff
	stale.ChangeRevision, err = kernel.NewRevision(2)
	if err != nil {
		t.Fatal(err)
	}
	if projected, err := projectOverseerSnapshot(kernel.OverseerSnapshot{Handoffs: []kernel.RetainedChangeHandoff{stale}}, parent, []kernel.RetainedChangeHandoff{handoff}); err != nil || len(projected.Handoffs) != 0 {
		t.Fatalf("stale handoff = %+v, %v", projected.Handoffs, err)
	}
}
