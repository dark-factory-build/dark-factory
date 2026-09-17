package kernel

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

func optionalContentID(t testing.TB, seed byte) ContentID {
	t.Helper()
	id, err := ContentIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedUnusedContent(t testing.TB, store *Store, project ProjectID, count int) {
	t.Helper()
	ctx := context.Background()
	for index := 0; index < count; index++ {
		if _, err := store.CreateContent(ctx, NewContent{
			ID: optionalContentID(t, byte(index+40)), ProjectID: project,
			Kind: ContentKind("procedure"), Title: "unused procedure",
			Description: "metadata only", Body: "never loaded by admission",
			Author: "operator", SourceReferences: "issue746",
		}, mustTimeTB(t, int64(index+10))); err != nil {
			t.Fatal(err)
		}
	}
}

// traceAdmission counts statements on the sole writer connection. The pool
// is intentionally fixed at one connection, so the traced idle connection is
// the one AdmitNext reuses. If that existing test seam stops observing the
// call, fail rather than claim an operation-count proof we did not obtain.
func traceAdmission(t *testing.T, store *Store, keys AdmissionKeys, at UnixMillis) (AdmissionResult, int) {
	t.Helper()
	ctx := context.Background()
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := connection.Raw(func(driverConnection any) error {
		return driverConnection.(sqliteDriver.Conn).Raw().Trace(sqlite3.TRACE_STMT, func(sqlite3.TraceEvent, any, any) error {
			count++
			return nil
		})
	}); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := store.AdmitNext(ctx, keys, at)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("SQLite trace seam observed no ordinary admission statements")
	}
	return result, count
}

func TestUnusedProjectContentDoesNotEnterOrdinaryAdmissionPath(t *testing.T) {
	ctx := context.Background()
	type observed struct {
		result    AdmissionResult
		count     int
		snapshot  DashboardSnapshot
		task      Task
		factory   FactoryState
		footprint admissionCounts
	}
	observations := make([]observed, 0, 2)
	for _, populated := range []bool{false, true} {
		store, _, project, agent := newAdmissionStore(t, RoleWorker, 2)
		task, err := store.EnqueueTask(ctx, NewTask{
			ID: taskID(t, 20), ProjectID: project.ID, AssignedAgentID: agent.ID,
			IncarnationID: incarnationID(t, 21), Title: "ordinary task",
		}, mustTime(t, 4))
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		beforeFactory, err := store.Factory(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		beforeSnapshot, err := store.Snapshot(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		beforeFootprint := admissionFootprint(t, store)
		if populated {
			seedUnusedContent(t, store, project.ID, 32)
			afterFactory, err := store.Factory(ctx)
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			afterSnapshot, err := store.Snapshot(ctx)
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeFactory, afterFactory) || !reflect.DeepEqual(beforeSnapshot, afterSnapshot) || !reflect.DeepEqual(beforeFootprint, admissionFootprint(t, store)) {
				store.Close()
				t.Fatalf("unused content changed ordinary state: factory %+v -> %+v, snapshot %+v -> %+v", beforeFactory, afterFactory, beforeSnapshot, afterSnapshot)
			}
		}
		beforeTask, found, err := store.Task(ctx, task.ID)
		if err != nil || !found || beforeTask.Status != TaskQueued {
			store.Close()
			t.Fatalf("queued task before admission = %+v, found=%v, err=%v", beforeTask, found, err)
		}
		result, count := traceAdmission(t, store, admissionKeys(t, 30, nil), mustTime(t, 5))
		afterTask, found, err := store.Task(ctx, task.ID)
		if err != nil || !found {
			store.Close()
			t.Fatalf("task after admission = %+v, found=%v, err=%v", afterTask, found, err)
		}
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		afterFactory, err := store.Factory(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		// Each temporary home has an unrelated random daemon identity.
		afterFactory.DaemonID = DaemonID{}
		afterFootprint := admissionFootprint(t, store)
		observations = append(observations, observed{result: result, count: count, snapshot: snapshot, task: afterTask, factory: afterFactory, footprint: afterFootprint})
		if beforeFootprint.invalidations == 0 {
			t.Fatal("ordinary fixture did not expose an invalidation footprint")
		}
		store.Close()
	}
	if !reflect.DeepEqual(observations[0].result, observations[1].result) || !reflect.DeepEqual(observations[0].task, observations[1].task) || !reflect.DeepEqual(observations[0].snapshot, observations[1].snapshot) || !reflect.DeepEqual(observations[0].factory, observations[1].factory) || !reflect.DeepEqual(observations[0].footprint, observations[1].footprint) {
		t.Fatalf("ordinary admission changed with unused content: empty=%+v populated=%+v", observations[0], observations[1])
	}
	if observations[0].count != observations[1].count {
		t.Fatalf("ordinary admission statement count changed with unused content: empty=%d populated=%d", observations[0].count, observations[1].count)
	}
}

func benchmarkAdmissionID(b *testing.B, seed byte) []byte {
	b.Helper()
	return bytes.Repeat([]byte{seed}, IDBytes)
}

func benchmarkAdmissionKeys(b *testing.B, seed byte) AdmissionKeys {
	b.Helper()
	digest, err := AttemptDigestFromBytes(bytes.Repeat([]byte{seed}, DigestBytes))
	if err != nil {
		b.Fatal(err)
	}
	proof, err := ResultProofDigestFromBytes(bytes.Repeat([]byte{seed + 1}, DigestBytes))
	if err != nil {
		b.Fatal(err)
	}
	run, err := RunIDFromBytes(benchmarkAdmissionID(b, seed))
	if err != nil {
		b.Fatal(err)
	}
	session, err := TerminalSessionIDFromBytes(benchmarkAdmissionID(b, seed+20))
	if err != nil {
		b.Fatal(err)
	}
	change, err := ChangeIDFromBytes(benchmarkAdmissionID(b, seed+5))
	if err != nil {
		b.Fatal(err)
	}
	resource := func(value byte) ResourceID {
		id, err := ResourceIDFromBytes(benchmarkAdmissionID(b, value))
		if err != nil {
			b.Fatal(err)
		}
		return id
	}
	return AdmissionKeys{
		RunID: run, TerminalSessionID: session, AttemptDigest: digest, ResultProofDigest: proof, CandidateChangeID: change,
		RuntimeRoot: "/runtime/benchmark",
		Resources:   AdmissionResourceIDs{RuntimeRoot: resource(seed + 1), RunnerProcess: resource(seed + 2), ProviderProcess: resource(seed + 3), ProviderGroup: resource(seed + 4)},
	}
}

func benchmarkOrdinaryAdmission(b *testing.B, populated bool) {
	b.Helper()
	path := filepath.Join(b.TempDir(), "kernel.db")
	canonical, err := canonicalTestDatabasePath(path)
	if err != nil {
		b.Fatal(err)
	}
	store, err := createTestStore(context.Background(), canonical, FactoryConfig{DispatchEnabled: true, Capacity: 2}, mustTimeTB(b, 1))
	if err != nil {
		b.Fatal(err)
	}
	projectID, err := ProjectIDFromBytes(benchmarkAdmissionID(b, 1))
	if err != nil {
		store.Close()
		b.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), NewProject{ID: projectID, Name: "p", Root: "/benchmark"}, mustTimeTB(b, 2))
	if err != nil {
		store.Close()
		b.Fatal(err)
	}
	agentID, err := AgentIDFromBytes(benchmarkAdmissionID(b, 2))
	if err != nil {
		store.Close()
		b.Fatal(err)
	}
	if _, err := store.CreateAgent(context.Background(), NewAgent{ID: agentID, ProjectID: project.ID, Name: "a", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTimeTB(b, 3)); err != nil {
		store.Close()
		b.Fatal(err)
	}
	defer store.Close()
	if populated {
		seedUnusedContent(b, store, project.ID, 64)
	}
	keys := benchmarkAdmissionKeys(b, 30)
	ctx := context.Background()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := store.AdmitNext(ctx, keys, mustTimeTB(b, int64(index+10))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOrdinaryAdmissionIgnoresUnusedContent(b *testing.B) {
	b.Run("empty library", func(b *testing.B) { benchmarkOrdinaryAdmission(b, false) })
	b.Run("populated library", func(b *testing.B) { benchmarkOrdinaryAdmission(b, true) })
}
