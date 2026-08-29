package kernel

import (
	"context"
	"errors"
	"testing"
)

func TestProviderResourcesActivateOnlyAsExactPair(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	identity := processIdentity(t, 960)
	for _, resource := range []Resource{process, group} {
		if _, err := store.ActivateResource(context.Background(), run.ID, resource.ID, resource.Revision, identity, mustTime(t, 20)); !errors.Is(err, ErrConflict) {
			t.Fatalf("individual %s activation = %v", resource.Kind.String(), err)
		}
	}
	activeProcess, activeGroup, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, identity, mustTime(t, 21))
	if err != nil || activeProcess.State != ResourceActive || activeGroup.State != ResourceActive || activeProcess.Identity != identity || activeGroup.Identity != identity {
		t.Fatalf("atomic provider activation process=%+v group=%+v err=%v", activeProcess, activeGroup, err)
	}
	replayProcess, replayGroup, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, identity, mustTime(t, 99))
	if err != nil || replayProcess.Revision != activeProcess.Revision || replayGroup.Revision != activeGroup.Revision {
		t.Fatalf("atomic provider replay process=%+v group=%+v err=%v", replayProcess, replayGroup, err)
	}
}

func TestProviderResourceActivationSuppressedRowRollsBackPair(t *testing.T) {
	for _, kind := range []ResourceKind{ResourceProviderProcess, ResourceProviderGroup} {
		t.Run(kind.String(), func(t *testing.T) {
			store, run, _ := admittedOrchestratorRun(t)
			defer store.Close()
			resources := resourcesForRunTest(t, store, run.ID)
			process := resourceOfKind(t, resources, ResourceProviderProcess)
			group := resourceOfKind(t, resources, ResourceProviderGroup)
			if _, err := store.writer.Exec(`CREATE TRIGGER suppress_provider_activation BEFORE UPDATE OF state ON resources WHEN OLD.kind = '` + kind.String() + `' AND NEW.state = 'active' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			_, _, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, processIdentity(t, 961), mustTime(t, 20))
			if !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("suppressed %s activation = %v", kind.String(), err)
			}
			for _, resource := range resourcesForRunTest(t, store, run.ID) {
				if resource.Kind == ResourceProviderProcess || resource.Kind == ResourceProviderGroup {
					if resource.State != ResourceDeclared || !resource.Identity.Empty() || resource.Revision.Int64() != 1 {
						t.Fatalf("partial provider footprint after %s suppression: %+v", kind.String(), resource)
					}
				}
			}
		})
	}
}

func TestProviderResourceCommitAmbiguityIsBothOrNeither(t *testing.T) {
	for _, test := range []struct {
		name         string
		fault        storeFaultKind
		wantActive   bool
		wantRetained bool
	}{
		{name: "before apply", fault: storeFaultCommitBefore},
		{name: "after apply", fault: storeFaultCommitAfter, wantActive: true, wantRetained: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := admittedOrchestratorRun(t)
			defer store.Close()
			path := storePath(t, store)
			resources := resourcesForRunTest(t, store, run.ID)
			process := resourceOfKind(t, resources, ResourceProviderProcess)
			group := resourceOfKind(t, resources, ResourceProviderGroup)
			plan := installFaultWriter(t, store, path)
			plan.arm(test.fault)
			_, _, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, processIdentity(t, 962), mustTime(t, 20))
			requireStoreOutcomeUnknown(t, err)
			assertFaultWriterDisposition(t, store, plan, test.wantRetained)
			fresh := resourcesForRunTest(t, store, run.ID)
			freshProcess := resourceOfKind(t, fresh, ResourceProviderProcess)
			freshGroup := resourceOfKind(t, fresh, ResourceProviderGroup)
			if (freshProcess.State == ResourceActive) != test.wantActive || (freshGroup.State == ResourceActive) != test.wantActive || freshProcess.Identity.Empty() != !test.wantActive || freshGroup.Identity.Empty() != !test.wantActive || freshProcess.Identity != freshGroup.Identity {
				t.Fatalf("ambiguous provider footprint process=%+v group=%+v", freshProcess, freshGroup)
			}
		})
	}
}

func TestProviderIdentityPairCorruptionFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name       string
		corruptSQL string
		args       func(*testing.T, Resource, Resource) []any
	}{
		{name: "one sided", corruptSQL: `UPDATE resources SET state = 'active', pid = ?, pgid = ?, birth_digest = ?, revision = revision + 1, updated_at_ms = 20 WHERE id = ?`, args: func(t *testing.T, process, _ Resource) []any {
			identity := processIdentity(t, 963)
			pid, pgid, birth, _ := identity.Process()
			return []any{pid, pgid, birth.Bytes(), process.ID.Bytes()}
		}},
		{name: "mismatched", corruptSQL: `UPDATE resources SET state = 'active', pid = CASE kind WHEN 'provider_process' THEN ? ELSE ? END, pgid = CASE kind WHEN 'provider_process' THEN ? ELSE ? END, birth_digest = CASE kind WHEN 'provider_process' THEN ? ELSE ? END, revision = revision + 1, updated_at_ms = 20 WHERE id IN (?, ?)`, args: func(t *testing.T, process, group Resource) []any {
			left := processIdentity(t, 964)
			right := processIdentity(t, 965)
			leftPID, leftPGID, leftBirth, _ := left.Process()
			rightPID, rightPGID, rightBirth, _ := right.Process()
			return []any{leftPID, rightPID, leftPGID, rightPGID, leftBirth.Bytes(), rightBirth.Bytes(), process.ID.Bytes(), group.ID.Bytes()}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := admittedOrchestratorRun(t)
			path := storePath(t, store)
			resources := resourcesForRunTest(t, store, run.ID)
			process := resourceOfKind(t, resources, ResourceProviderProcess)
			group := resourceOfKind(t, resources, ResourceProviderGroup)
			corruptSQL(t, store, test.corruptSQL, test.args(t, process, group)...)
			if _, _, err := store.Run(context.Background(), run.ID); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("corrupt provider pair read = %v", err)
			}
			before := captureWriteFootprint(t, store)
			factory, _ := store.Factory(context.Background())
			if _, err := store.SetDispatch(context.Background(), factory.Revision, true, mustTime(t, 30)); !errors.Is(err, ErrCorruptState) {
				store.Close()
				t.Fatalf("validated write over corrupt provider pair = %v", err)
			}
			if after := captureWriteFootprint(t, store); after != before {
				store.Close()
				t.Fatalf("corrupt provider refusal footprint before=%+v after=%+v", before, after)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			beforeOpen := captureDatabaseEvidence(t, path)
			if reopened, err := Open(context.Background(), path); !errors.Is(err, ErrCorruptState) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("open corrupt provider pair = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, beforeOpen)
		})
	}
}
