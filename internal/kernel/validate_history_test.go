package kernel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

func historyBytes(size int, kind byte, n int) []byte {
	raw := make([]byte, size)
	raw[0] = kind
	binary.BigEndian.PutUint32(raw[size-4:], uint32(n))
	return raw
}

func historyKeys(t *testing.T, n int) AdmissionKeys {
	t.Helper()
	id := func(kind byte) ResourceID {
		value, err := ResourceIDFromBytes(historyBytes(IDBytes, kind, n))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	run, err := RunIDFromBytes(historyBytes(IDBytes, 0x10, n))
	if err != nil {
		t.Fatal(err)
	}
	session, err := TerminalSessionIDFromBytes(historyBytes(IDBytes, 0x11, n))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := AttemptDigestFromBytes(historyBytes(DigestBytes, 0x12, n))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := ResultProofDigestFromBytes(historyBytes(DigestBytes, 0x13, n))
	if err != nil {
		t.Fatal(err)
	}
	change, err := ChangeIDFromBytes(historyBytes(IDBytes, 0x14, n))
	if err != nil {
		t.Fatal(err)
	}
	return AdmissionKeys{
		RunID: run, TerminalSessionID: session, AttemptDigest: attempt, ResultProofDigest: proof, CandidateChangeID: change,
		RuntimeRoot: fmt.Sprintf("/runtime/%d", n),
		Resources:   AdmissionResourceIDs{RuntimeRoot: id(0x15), RunnerProcess: id(0x16), ProviderProcess: id(0x17), ProviderGroup: id(0x18)},
	}
}

// retainedHistoryStore builds tasks×runs of settled worker history through
// the real lifecycle: every run after a task's first is a send-back retry
// that reopens the retained Change, the shape a live factory accumulates.
func retainedHistoryStore(t *testing.T, tasks, runs int) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := createTestStore(ctx, filepath.Join(t.TempDir(), "kernel.db"), FactoryConfig{DispatchEnabled: true, Capacity: 1}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 1), Name: "p", Root: "/project"}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, NewAgent{ID: agentID(t, 2), ProjectID: project.ID, Name: "w", Role: RoleWorker, Provider: ProviderCodex, ToolBudgetLimit: 5}, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	selection := testChangeSelection(t)
	at := int64(100)
	next := 0
	for index := range tasks {
		taskID, err := TaskIDFromBytes(historyBytes(IDBytes, 0x20, index))
		if err != nil {
			t.Fatal(err)
		}
		incarnation, err := IncarnationIDFromBytes(historyBytes(IDBytes, 0x21, index))
		if err != nil {
			t.Fatal(err)
		}
		task, err := store.EnqueueTask(ctx, NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnation, Title: "t"}, mustTime(t, at))
		if err != nil {
			t.Fatal(err)
		}
		for attempt := range runs {
			next++
			at += 100
			keys := historyKeys(t, next)
			admission, err := store.AdmitNext(ctx, keys, mustTime(t, at))
			if err != nil || !admission.Admitted() {
				t.Fatalf("admission %d = %+v, %v", next, admission, err)
			}
			run := *admission.Run
			if attempt == 0 {
				change, found, err := store.Change(ctx, *run.ChangeID)
				if err != nil || !found {
					t.Fatalf("change = %+v, %v", change, err)
				}
				prepared, err := store.RecordChangePrepared(ctx, change.ID, change.Revision, selection, mustTime(t, at+1))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkChangeAvailable(ctx, change.ID, prepared.Revision, selection.commit, mustTime(t, at+2)); err != nil {
					t.Fatal(err)
				}
			}
			activated := activateAllResourcesUnique(t, store, run, at+3, int64(next)*20)
			session := terminalSessionForRunTest(t, store, run.ID)
			running, err := store.ActivateRun(ctx, run.ID, session.ID, activated.Revision, session.Revision, mustTime(t, at+10))
			if err != nil {
				t.Fatal(err)
			}
			proposal, _ := NewSuccessProposal("done")
			if _, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, mustTime(t, at+11)); err != nil {
				t.Fatal(err)
			}
			observeMissingProcessExits(t, store, running.ID, at+12)
			releaseAllRunResources(t, store, running.ID, at+15)
			finalizing := closeTerminalSessionAtCurrent(t, store, running.ID, at+20)
			if _, err := finalizeTestRun(t, store, finalizing, at+21); err != nil {
				t.Fatal(err)
			}
			if attempt+1 < runs {
				at += 100
				current, found, err := store.Task(ctx, task.ID)
				if err != nil || !found {
					t.Fatalf("task = %+v, %v", current, err)
				}
				if _, err := store.SendBackTask(ctx, task.ID, current.Revision, "again", mustTime(t, at)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return store
}

// validationStatements counts the SQL statements one durable-controls
// validation runs against the writer connection.
func validationStatements(t *testing.T, store *Store) (int, time.Duration) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	count := 0
	trace := func(mask sqlite3.TraceEvent, callback func(sqlite3.TraceEvent, any, any) error) {
		if err := tx.connection.Raw(func(driverConnection any) error {
			return driverConnection.(sqliteDriver.Conn).Raw().Trace(mask, callback)
		}); err != nil {
			t.Fatal(err)
		}
	}
	trace(sqlite3.TRACE_STMT, func(sqlite3.TraceEvent, any, any) error {
		count++
		return nil
	})
	started := time.Now()
	validationErr := validateDurableControls(ctx, tx.connection)
	elapsed := time.Since(started)
	trace(0, nil)
	if validationErr != nil {
		t.Fatal(validationErr)
	}
	if err := tx.Rollback(nil); err != nil {
		t.Fatal(err)
	}
	return count, elapsed
}

// Every validated read and write walks the whole durable graph, so its cost
// must grow linearly with retained history: each retry a task accumulates
// adds a fixed number of statements, never one more walk of the task's
// history per run already recorded.
func TestDurableValidationCostIsLinearInRetainedHistory(t *testing.T) {
	const tasks, maxRuns = 2, 4
	var counts []int
	var last *Store
	for runs := 1; runs <= maxRuns; runs++ {
		store := retainedHistoryStore(t, tasks, runs)
		count, elapsed := validationStatements(t, store)
		t.Logf("tasks=%d runs/task=%d statements=%d elapsed=%s", tasks, runs, count, elapsed)
		counts = append(counts, count)
		if runs < maxRuns {
			store.Close()
		} else {
			last = store
		}
	}
	defer last.Close()
	for index := 2; index < len(counts); index++ {
		if step, previous := counts[index]-counts[index-1], counts[index-1]-counts[index-2]; step != previous {
			t.Fatalf("validation statements grow superlinearly with retry depth: %v", counts)
		}
	}

	// The cheaper pass still refuses corrupt history and colliding identities.
	for _, test := range []struct{ name, corrupt string }{
		{"noncontiguous task revision", `UPDATE tasks SET work_revision = work_revision + 1`},
		{"terminal session closes after run", `UPDATE terminal_sessions SET closed_at_ms = closed_at_ms + 2, updated_at_ms = updated_at_ms + 2`},
		{"runtime path collision", `UPDATE resources SET path_dev = 10, path_inode = 10001 WHERE kind = 'runtime_root'`},
		{"process identity collision", `UPDATE resources SET pid = 1001, pgid = 2001, birth_digest = zeroblob(32) WHERE kind = 'runner_process'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := last.beginUncheckedWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close()
			if _, err := tx.connection.ExecContext(ctx, test.corrupt); err != nil {
				t.Fatal(err)
			}
			if err := validateDurableControls(ctx, tx.connection); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("corrupt retained history accepted: %v", err)
			}
			if err := tx.Rollback(nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	tx, err := last.beginValidatedWrite(context.Background())
	if err != nil {
		t.Fatalf("rolled-back corruption left the history refused: %v", err)
	}
	tx.Close()
}
