package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestDeclaredResourcesRejectHiddenIdentityAndRecoveredAbsenceAuthority(t *testing.T) {
	for _, kind := range []ResourceKind{ResourceRunnerProcess, ResourceProviderProcess, ResourceProviderGroup, ResourceRuntimeRoot} {
		t.Run(kind.String(), func(t *testing.T) {
			store, run, _ := admittedOrchestratorRun(t)
			path := storePath(t, store)
			resource := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), kind)
			factory, err := store.Factory(context.Background())
			if err != nil {
				store.Close()
				t.Fatal(err)
			}

			statement, args := declaredIdentityCorruption(t, resource)
			if _, err := store.writer.Exec(statement, args...); err == nil {
				store.Close()
				t.Fatal("schema accepted identity on declared resource")
			}
			fresh, found, err := store.Resource(context.Background(), resource.ID)
			if err != nil || !found || fresh.State != ResourceDeclared || !fresh.Identity.Empty() {
				store.Close()
				t.Fatalf("failed schema update changed resource = %+v, found=%v, err=%v", fresh, found, err)
			}

			corruptSQL(t, store, statement, args...)
			if _, _, err := store.Resource(context.Background(), resource.ID); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("direct corrupt resource read = %v", err)
			}
			if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("run with corrupt declared resource = %v", err)
			}
			before := captureWriteFootprint(t, store)
			exit, _ := NewProcessExitRecoveredAbsence(1, mustTime(t, 20))
			if _, err := store.ObserveRunnerExit(context.Background(), run.ID, run.Revision, processIdentity(t, 91), exit, mustTime(t, 21)); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("recovered absence over corrupt declared resource = %v", err)
			}
			if _, err := store.SetDispatch(context.Background(), factory.Revision, false, mustTime(t, 22)); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("validated mutation over corrupt declared resource = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				store.Close()
				t.Fatalf("corrupt resource refusal footprint: before=%+v after=%+v", before, after)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			beforeOpen := captureDatabaseEvidence(t, path)
			if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("open with corrupt declared resource = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, beforeOpen)
		})
	}
}

func declaredIdentityCorruption(t *testing.T, resource Resource) (string, []any) {
	t.Helper()
	if resource.Kind == ResourceRuntimeRoot {
		identity, err := NewFileIdentity(81, 82)
		if err != nil {
			t.Fatal(err)
		}
		return `UPDATE resources SET path_dev = ?, path_inode = ? WHERE id = ?`, []any{identity.device, identity.inode, resource.ID.Bytes()}
	}
	identity := processIdentity(t, 83)
	pid, pgid, birth, ok := identity.Process()
	if !ok {
		t.Fatal("test process identity is not process identity")
	}
	return `UPDATE resources SET pid = ?, pgid = ?, birth_digest = ? WHERE id = ?`, []any{pid, pgid, birth.Bytes(), resource.ID.Bytes()}
}
