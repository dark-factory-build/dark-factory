package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestActivateRunRejectsCausallyEarlyResources(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	activateResourcesAt(t, store, run, 20)
	before := captureWriteFootprint(t, store)
	if _, err := store.ActivateRun(context.Background(), run.ID, run.Revision, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early activation = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early activation footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.ActivateRun(context.Background(), run.ID, run.Revision, mustTime(t, 20)); err != nil {
		t.Fatalf("activation boundary = %v", err)
	}
}

func TestFinalizingRejectsCausallyEarlyResourceUpdate(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	if _, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 699), mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	failure, _ := NewFailureProposal(FailureActivation, "cleanup")
	before := captureWriteFootprint(t, store)
	if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalizing = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early finalizing footprint before=%+v after=%+v", before, after)
	}
}

func TestExitDrivenFinalizingRejectsCausallyEarlyResourceUpdate(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	active, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 701), mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(701, 1701)
	if _, err := store.ActivateResource(context.Background(), run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	exit, _ := NewProcessExitCode(1, 1, mustTime(t, 10))
	before := captureWriteFootprint(t, store)
	if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, active.Identity, exit, mustTime(t, 11)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early exit-driven finalizing = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early exit-driven footprint before=%+v after=%+v", before, after)
	}
}

func TestReleaseProviderRejectsBeforeExit(t *testing.T) {
	proposal, _ := NewSuccessProposal("done")
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	finalizing, err := store.ProposeAttemptOutcome(context.Background(), keys.AttemptDigest, proposal, mustTime(t, 40))
	if err != nil {
		t.Fatal(err)
	}
	provider := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.MarkResourceUnresolved(context.Background(), run.ID, runtime.ID, runtime.Revision, runtime.Identity, "later cleanup", mustTime(t, 50)); err != nil {
		t.Fatalf("advance independent resource cleanup = %v", err)
	}
	exit, _ := NewProcessExitCode(1, 0, mustTime(t, 42))
	observed, err := store.ObserveProviderExit(context.Background(), run.ID, finalizing.Revision, provider.Identity, exit, mustTime(t, 43))
	if err != nil {
		t.Fatal(err)
	}
	provider = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
	before := captureWriteFootprint(t, store)
	if _, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 41)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("release before exit = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("release-before-exit footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 42)); err != nil {
		t.Fatalf("release at exit = %v", err)
	}
	replay, err := store.ReleaseResource(context.Background(), run.ID, provider.ID, provider.Revision, provider.Identity, mustTime(t, 40))
	if err != nil || replay.State != ResourceReleased {
		t.Fatalf("older release replay = %+v, %v", replay, err)
	}
	_ = observed
}

func TestFinalizeRunRejectsBeforeResourceCleanupTime(t *testing.T) {
	failure, _ := NewFailureProposal(FailureInternal, "cleanup")
	store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
	defer store.Close()
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 45)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalization = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("early finalization footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 53)); err != nil {
		t.Fatalf("finalization boundary = %v", err)
	}
}

func TestFinalizeRunRejectsBeforeLateWorkerChangeCheckpoint(t *testing.T) {
	success, _ := NewSuccessProposal("done")
	store, run := finalizingReleasedRun(t, RoleWorker, VerificationNone, success)
	defer store.Close()
	corruptSQL(t, store, `UPDATE changes SET available_at_ms = 60, updated_at_ms = 60 WHERE id = ?`, run.ChangeID.Bytes())
	before := captureWriteFootprint(t, store)
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 55)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("early finalization before late Change = %v", err)
	}
	if after := captureWriteFootprint(t, store); after != before {
		t.Fatalf("late Change footprint before=%+v after=%+v", before, after)
	}
	if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 60)); err != nil {
		t.Fatalf("late Change boundary finalization = %v", err)
	}
}

func TestChronologyScannersRejectImpossibleRows(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*Store, func() error)
	}{
		{name: "admitted update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := admittedOrchestratorRun(t)
			corruptSQL(t, store, `UPDATE runs SET updated_at_ms = admitted_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "declared resource update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := admittedOrchestratorRun(t)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = declared_at_ms + 1 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "active resource update changed", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = activated_at_ms + 1 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "running after updated", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			corruptSQL(t, store, `UPDATE runs SET running_at_ms = updated_at_ms + 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "finalizing credential mismatch", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			corruptSQL(t, store, `UPDATE runs SET credential_revoked_at_ms = finalizing_at_ms - 1 WHERE id = ?`, run.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "terminal before finalizing", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE runs SET terminal_at_ms = finalizing_at_ms - 1 WHERE id = ?`, terminal.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), terminal.ID); return err }
		}},
		{name: "resource activation after running", setup: func(t *testing.T) (*Store, func() error) {
			store, run, _ := runningOrchestratorRun(t)
			runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET activated_at_ms = 31, updated_at_ms = 31 WHERE id = ?`, runner.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "finalizing resource before finalizing", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
			corruptSQL(t, store, `UPDATE resources SET updated_at_ms = 39 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "released provider before exit", setup: func(t *testing.T) (*Store, func() error) {
			failure, _ := NewFailureProposal(FailureInternal, "cleanup")
			store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
			corruptSQL(t, store, `UPDATE resources SET released_at_ms = 40, updated_at_ms = 40 WHERE id = ?`, resource.ID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), run.ID); return err }
		}},
		{name: "terminal before Change", setup: func(t *testing.T) (*Store, func() error) {
			success, _ := NewSuccessProposal("done")
			store, run := finalizingReleasedRun(t, RoleWorker, VerificationNone, success)
			terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
			if err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE changes SET available_at_ms = 90, updated_at_ms = 90 WHERE id = ?`, terminal.ChangeID.Bytes())
			return store, func() error { _, _, err := store.Run(context.Background(), terminal.ID); return err }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, read := test.setup(t)
			defer store.Close()
			if !errors.Is(read(), ErrCorruptState) {
				t.Fatalf("read accepted corruption: %v", read())
			}
		})
	}

	t.Run("change checkpoint", func(t *testing.T) {
		store, run, _ := runningWorkerRun(t)
		defer store.Close()
		corruptSQL(t, store, `UPDATE changes SET updated_at_ms = available_at_ms - 1 WHERE id = ?`, run.ChangeID.Bytes())
		if _, _, err := store.Change(context.Background(), *run.ChangeID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Change accepted corruption: %v", err)
		}
	})
	t.Run("reserved Change update changed", func(t *testing.T) {
		store, run, _ := runningWorkerRun(t)
		defer store.Close()
		corruptSQL(t, store, `UPDATE changes SET phase = 'reserved', object_format = NULL, selected_commit = NULL, repository_root = NULL, repository_dev = NULL, repository_inode = NULL, selected_at_ms = NULL, stage_dev = NULL, stage_inode = NULL, prepared_at_ms = NULL, tree_digest = NULL, entry_count = NULL, total_bytes = NULL, source_dev = NULL, source_inode = NULL, available_at_ms = NULL, updated_at_ms = created_at_ms + 1 WHERE id = ?`, run.ChangeID.Bytes())
		if _, _, err := store.Change(context.Background(), *run.ChangeID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("reserved Change accepted corruption: %v", err)
		}
	})
	t.Run("task completion checkpoint", func(t *testing.T) {
		failure, _ := NewFailureProposal(FailureInternal, "cleanup")
		store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, failure)
		defer store.Close()
		terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE tasks SET completed_at_ms = updated_at_ms - 1 WHERE id = ?`, terminal.TaskID.Bytes())
		if _, _, err := store.Task(context.Background(), terminal.TaskID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("task completion corruption accepted: %v", err)
		}
	})
	t.Run("blocked task terminal checkpoint", func(t *testing.T) {
		blocked, _ := NewBlockedProposal("blocked")
		store, run := finalizingReleasedRun(t, RoleOrchestrator, VerificationNone, blocked)
		defer store.Close()
		terminal, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 80))
		if err != nil {
			t.Fatal(err)
		}
		corruptSQL(t, store, `UPDATE tasks SET updated_at_ms = 79 WHERE id = ?`, terminal.TaskID.Bytes())
		if _, _, err := store.Run(context.Background(), terminal.ID); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("blocked task/run mismatch accepted: %v", err)
		}
	})
}

func activateResourcesAt(t *testing.T, store *Store, run Run, at int64) {
	t.Helper()
	resources := resourcesForRunTest(t, store, run.ID)
	provider := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	if _, _, err := store.ActivateProviderResources(context.Background(), run.ID, provider.ID, provider.Revision, group.ID, group.Revision, processIdentity(t, 700), mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.Kind == ResourceProviderProcess || resource.Kind == ResourceProviderGroup {
			continue
		}
		var identity ResourceIdentity
		if resource.Kind == ResourceRuntimeRoot {
			identity, _ = NewPathResourceIdentity(700, 1700)
		} else {
			identity = processIdentity(t, 702)
		}
		if _, err := store.ActivateResource(context.Background(), run.ID, resource.ID, resource.Revision, identity, mustTime(t, at)); err != nil {
			t.Fatal(err)
		}
	}
}
