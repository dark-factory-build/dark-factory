//go:build darwin

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// The installed model is deliberately replaced by the existing Codex protocol
// fixture. This proves actual provider-process delivery, not model learning.
func TestSupervisorKnowledgeDeliveredToFreshProviderProcess(t *testing.T) {
	fixture := newSupervisorFixture(t, "unused shell program")
	ctx := context.Background()
	task, found, err := fixture.store.Task(ctx, fixture.taskID)
	if err != nil || !found {
		t.Fatal(err)
	}
	lesson := seedContextKnowledge(t, &dispatchFixture{daemon: fixture.daemon, store: fixture.store}, task.ProjectID, 233, kernel.ContentLesson, kernel.KnowledgeMetadata{Status: "current", SourceRevision: fixture.base, Evidence: []string{"review: source guard at " + fixture.base}}, "Preserve the exact source receipt before modifying the guard.")
	if err := fixture.store.AttachContentToTask(ctx, task.ID, task.ProjectID, lesson.ID, lesson.Revision, supervisorTime()); err != nil {
		t.Fatal(err)
	}
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET title=?,body=? WHERE id=?`, "fresh knowledge task", "Inspect the guard using its retained lesson", task.ID.Bytes())
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, executable, filepath.Join(tools, "codex"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath
	run, err := fixture.daemon.RunNext(ctx, fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertTerminal(t, run, kernel.OutcomeSucceeded)
	result := run.Proposal.Result()
	if !strings.Contains(result, lesson.ID.String()) || !strings.Contains(result, `"revision":1`) || !strings.Contains(result, "Preserve the exact source receipt") || !strings.Contains(result, "PTY=120x40") {
		t.Fatalf("real provider fixture did not receive frozen lesson: %s", result)
	}
	accesses, err := contentAccesses(ctx, fixture.store, run.ProjectID, kernel.ContentAccess{ContentID: lesson.ID, ContentRevision: lesson.Revision})
	if err != nil {
		t.Fatal(err)
	}
	supplied := false
	for _, access := range accesses {
		if access.Kind == "supplied" && access.ContentID == lesson.ID && access.ContentRevision == lesson.Revision && access.ByteLength > 0 {
			supplied = true
		}
	}
	if !supplied {
		t.Fatalf("missing provider served receipt: %+v", accesses)
	}
	fixture.assertReleased(t, run)
}
