package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestResourceActivationTimeIsAtomicAndDurable(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	path := storePath(t, store)
	resources := resourcesForRunTest(t, store, run.ID)
	provider := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	providerAt := mustTime(t, 20)
	activeProvider, activeGroup, err := store.ActivateProviderResources(context.Background(), run.ID, provider.ID, provider.Revision, group.ID, group.Revision, processIdentity(t, 960), providerAt)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if activeProvider.ActivatedAt == nil || activeGroup.ActivatedAt == nil || activeProvider.ActivatedAt.Int64() != providerAt.Int64() || activeGroup.ActivatedAt.Int64() != providerAt.Int64() {
		store.Close()
		t.Fatalf("provider activation times = %+v, %+v", activeProvider, activeGroup)
	}
	runner := resourceOfKind(t, resources, ResourceRunnerProcess)
	runnerAt := mustTime(t, 21)
	activeRunner, err := store.ActivateResource(context.Background(), run.ID, runner.ID, runner.Revision, processIdentity(t, 961), runnerAt)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if activeRunner.ActivatedAt == nil || activeRunner.ActivatedAt.Int64() != runnerAt.Int64() {
		store.Close()
		t.Fatalf("runner activation time = %+v", activeRunner)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh := resourcesForRunTest(t, reopened, run.ID)
	freshProvider := resourceOfKind(t, fresh, ResourceProviderProcess)
	freshGroup := resourceOfKind(t, fresh, ResourceProviderGroup)
	freshRunner := resourceOfKind(t, fresh, ResourceRunnerProcess)
	if freshProvider.ActivatedAt == nil || freshGroup.ActivatedAt == nil || freshRunner.ActivatedAt == nil || freshProvider.ActivatedAt.Int64() != providerAt.Int64() || freshGroup.ActivatedAt.Int64() != providerAt.Int64() || freshRunner.ActivatedAt.Int64() != runnerAt.Int64() {
		t.Fatalf("reopened activation times = %+v, %+v, %+v", freshProvider, freshGroup, freshRunner)
	}
}

func TestProcessExitBeforeResourceActivationIsRejectedWithoutFootprint(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    ResourceKind
		observe func(*Store, Run, ResourceIdentity, ProcessExit) error
	}{
		{name: "provider", kind: ResourceProviderProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) error {
			_, err := store.ObserveProviderExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
			return err
		}},
		{name: "runner", kind: ResourceRunnerProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) error {
			_, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), test.kind)
			if resource.ActivatedAt == nil {
				t.Fatalf("resource has no activation time = %+v", resource)
			}
			exit, err := NewProcessExitRecoveredAbsence(1, mustTime(t, resource.ActivatedAt.Int64()-1))
			if err != nil {
				t.Fatal(err)
			}
			before := captureWriteFootprint(t, store)
			if err := test.observe(store, run, resource.Identity, exit); !errors.Is(err, ErrConflict) {
				t.Fatalf("pre-activation %s exit = %v", test.name, err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				t.Fatalf("rejected exit footprint before=%+v after=%+v", before, after)
			}
			fresh, found, err := store.Run(context.Background(), run.ID)
			if err != nil || !found || fresh.Revision != run.Revision || fresh.ProviderExit != nil || fresh.RunnerExit != nil || fresh.Phase != RunRunning {
				t.Fatalf("rejected exit changed run = %+v, found=%v, err=%v", fresh, found, err)
			}
		})
	}
}

func TestProcessExitAtResourceActivationIsAccepted(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    ResourceKind
		observe func(*Store, Run, ResourceIdentity, ProcessExit) (Run, error)
	}{
		{name: "provider", kind: ResourceProviderProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) (Run, error) {
			return store.ObserveProviderExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
		}},
		{name: "runner", kind: ResourceRunnerProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) (Run, error) {
			return store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			defer store.Close()
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), test.kind)
			if resource.ActivatedAt == nil {
				t.Fatalf("resource has no activation time = %+v", resource)
			}
			exit, err := NewProcessExitCode(1, 0, *resource.ActivatedAt)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := test.observe(store, run, resource.Identity, exit)
			if err != nil || observed.Phase != RunFinalizing {
				t.Fatalf("boundary exit = %+v, %v", observed, err)
			}
		})
	}
}

func TestPersistedProcessExitBeforeResourceActivationFailsEveryBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		column  string
		kind    ResourceKind
		observe func(*Store, Run, ResourceIdentity, ProcessExit) (Run, error)
	}{
		{name: "provider", column: "provider_exit_at_ms", kind: ResourceProviderProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) (Run, error) {
			return store.ObserveProviderExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
		}},
		{name: "runner", column: "runner_exit_at_ms", kind: ResourceRunnerProcess, observe: func(store *Store, run Run, identity ResourceIdentity, exit ProcessExit) (Run, error) {
			return store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, identity, exit, mustTime(t, 40))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Run("Run", func(t *testing.T) {
				store, run := corruptPersistedExitBeforeActivation(t, test.column, test.kind, test.observe)
				defer store.Close()
				if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
					t.Fatalf("corrupt persisted %s exit read = %v", test.name, err)
				}
			})
			t.Run("RecoverableRuns", func(t *testing.T) {
				store, _ := corruptPersistedExitBeforeActivation(t, test.column, test.kind, test.observe)
				defer store.Close()
				if _, err := store.RecoverableRuns(context.Background()); !errors.Is(err, ErrCorruptState) {
					t.Fatalf("corrupt persisted %s exit recovery = %v", test.name, err)
				}
			})
			t.Run("Open", func(t *testing.T) {
				store, run := corruptPersistedExitBeforeActivation(t, test.column, test.kind, test.observe)
				path := storePath(t, store)
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
					if reopened != nil {
						reopened.Close()
					}
					t.Fatalf("corrupt persisted %s exit open = %v", test.name, err)
				}
				_ = run
			})
			t.Run("FinalizeRun", func(t *testing.T) {
				store, run := corruptPersistedExitBeforeActivation(t, test.column, test.kind, test.observe)
				defer store.Close()
				before := captureWriteFootprint(t, store)
				if _, err := store.FinalizeRun(context.Background(), run.ID, run.Revision, mustTime(t, 60)); !errors.Is(err, ErrCorruptState) {
					t.Fatalf("corrupt persisted %s exit finalization = %v", test.name, err)
				}
				if after := captureWriteFootprint(t, store); after != before {
					t.Fatalf("corrupt persisted %s exit finalization footprint before=%+v after=%+v", test.name, before, after)
				}
			})
		})
	}
}

func corruptPersistedExitBeforeActivation(t *testing.T, column string, kind ResourceKind, observe func(*Store, Run, ResourceIdentity, ProcessExit) (Run, error)) (*Store, Run) {
	t.Helper()
	store, run, _ := runningOrchestratorRun(t)
	resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), kind)
	exit, err := NewProcessExitCode(1, 0, mustTime(t, 40))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	observed, err := observe(store, run, resource.Identity, exit)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	corruptSQL(t, store, `UPDATE runs SET `+column+` = ? WHERE id = ?`, resource.ActivatedAt.Int64()-1, run.ID.Bytes())
	return store, observed
}

func TestDirectReleaseRetainsNilActivationTime(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	failure, _ := NewFailureProposal(FailureActivation, "pre-exec cleanup")
	if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	for _, resource := range resourcesForRunTest(t, store, run.ID) {
		released, err := store.ReleaseResource(context.Background(), run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 21))
		if err != nil {
			t.Fatalf("release %s = %v", resource.Kind, err)
		}
		if released.ActivatedAt != nil {
			t.Fatalf("directly released resource acquired activation time = %+v", released)
		}
	}
}

func TestResourceActivationTimeScannerRejectsImpossibleRows(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, Run)
	}{
		{name: "active missing activation", mutate: func(t *testing.T, store *Store, run Run) {
			corruptSQL(t, store, `UPDATE resources SET activated_at_ms = NULL WHERE run_id = ? AND kind = 'runner_process'`, run.ID.Bytes())
		}},
		{name: "releasing empty identity with activation", mutate: func(t *testing.T, store *Store, run Run) {
			failure, _ := NewFailureProposal(FailureActivation, "pre-exec cleanup")
			if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40)); err != nil {
				t.Fatal(err)
			}
			corruptSQL(t, store, `UPDATE resources SET state = 'releasing', pid = NULL, pgid = NULL, birth_digest = NULL, activated_at_ms = 40 WHERE run_id = ? AND kind = 'runner_process'`, run.ID.Bytes())
		}},
		{name: "activation after update", mutate: func(t *testing.T, store *Store, run Run) {
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET activated_at_ms = updated_at_ms + 1 WHERE id = ?`, resource.ID.Bytes())
		}},
		{name: "release before activation", mutate: func(t *testing.T, store *Store, run Run) {
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
			corruptSQL(t, store, `UPDATE resources SET state = 'released', released_at_ms = activated_at_ms - 1, updated_at_ms = activated_at_ms WHERE id = ?`, resource.ID.Bytes())
		}},
		{name: "release before update", mutate: func(t *testing.T, store *Store, run Run) {
			failure, _ := NewFailureProposal(FailureActivation, "pre-exec cleanup")
			if _, err := store.FailRun(context.Background(), run.ID, run.Revision, failure, mustTime(t, 40)); err != nil {
				t.Fatal(err)
			}
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
			statement := `UPDATE resources SET state = 'released', released_at_ms = updated_at_ms, updated_at_ms = updated_at_ms + 1 WHERE id = ?`
			if _, err := store.writer.Exec(statement, resource.ID.Bytes()); err == nil {
				t.Fatal("schema accepted released timestamp before updated timestamp")
			}
			corruptSQL(t, store, statement, resource.ID.Bytes())
		}},
		{name: "provider activation times differ", mutate: func(t *testing.T, store *Store, run Run) {
			corruptSQL(t, store, `UPDATE resources SET activated_at_ms = activated_at_ms + 1 WHERE run_id = ? AND kind = 'provider_group'`, run.ID.Bytes())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := runningOrchestratorRun(t)
			path := storePath(t, store)
			test.mutate(t, store, run)
			if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("corrupt resource read = %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("open corrupt resource = %v", err)
			}
		})
	}
}
