package daemon

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func daemonTestID(t *testing.T, value byte) (kernel.TaskID, kernel.ChangeID) {
	t.Helper()
	raw := bytes.Repeat([]byte{value}, 16)
	task, err := kernel.TaskIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	change, err := kernel.ChangeIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return task, change
}

func mustRevisionForDaemonTest(t *testing.T, value int64) kernel.Revision {
	t.Helper()
	revision, err := kernel.NewRevision(value)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestRetainedReviewHandoffMismatchNamesTheFirstIdentityMismatch(t *testing.T) {
	taskID, _ := daemonTestID(t, 1)
	_, changeID := daemonTestID(t, 2)
	base := kernel.RetainedChangeHandoff{
		TaskID:           taskID,
		ChangeID:         changeID,
		BaseCommit:       "a8728c9586803ffdc6cad1c006aac17f95f74ada",
		TaskWorkRevision: mustRevisionForDaemonTest(t, 2),
		ChangeRevision:   mustRevisionForDaemonTest(t, 6),
	}
	for name, mutate := range map[string]func(*kernel.RetainedChangeHandoff){
		"task":   func(value *kernel.RetainedChangeHandoff) { value.TaskID, _ = daemonTestID(t, 3) },
		"Change": func(value *kernel.RetainedChangeHandoff) { _, value.ChangeID = daemonTestID(t, 3) },
		"base": func(value *kernel.RetainedChangeHandoff) {
			value.BaseCommit = "ddec95ebff8847a687c480ee0ab6f193b0411c35"
		},
		"work revision":   func(value *kernel.RetainedChangeHandoff) { value.TaskWorkRevision = mustRevisionForDaemonTest(t, 4) },
		"Change revision": func(value *kernel.RetainedChangeHandoff) { value.ChangeRevision = mustRevisionForDaemonTest(t, 10) },
	} {
		t.Run(name, func(t *testing.T) {
			actual := base
			mutate(&actual)
			err := retainedReviewHandoffMismatch(base, actual)
			if err == nil || !errors.Is(err, kernel.ErrConflict) || !strings.Contains(err.Error(), name) {
				t.Fatalf("mismatch = %v", err)
			}
		})
	}
	if err := retainedReviewHandoffMismatch(base, base); err != nil {
		t.Fatalf("equal handoff mismatch = %v", err)
	}
}
