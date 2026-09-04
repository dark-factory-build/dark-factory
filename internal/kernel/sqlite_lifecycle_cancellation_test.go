package kernel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A caller's cancellation must never consume a connection from the retained
// finite set. That set is sealed at activation and cannot mint a replacement,
// and the writer set holds exactly one physical connection, so a single
// cancellation that interrupted a lifecycle statement used to destroy it and
// brick every later write with "retained sqlite connection set is exhausted"
// — the signature observed during daemon shutdown, where the owned context is
// cancelled while a write is in flight.
//
// The cancellation must land while BEGIN IMMEDIATE is blocked on the write
// lock, because an already-cancelled context is refused earlier, at writer
// admission. A second store holds that lock to make the window deterministic.
func TestCallerCancellationDuringBeginKeepsTheRetainedWriterSet(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatalf("open operational store: %v", err)
	}
	defer store.Close()
	plan := installFaultWriter(t, store, path)
	writerID := faultWriterConnectionID(t, store)

	if open := store.writer.Stats().OpenConnections; open != 1 {
		t.Fatalf("retained writer set = %d physical connections, want 1", open)
	}

	blocker, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open blocking store: %v", err)
	}
	defer blocker.Close()
	held, err := blocker.beginUncheckedWrite(context.Background())
	if err != nil {
		t.Fatalf("hold the write lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = held.Rollback(nil)
			held.Close()
		}
	}()

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	beginEntered := plan.watchBegin()
	beginResult := make(chan error, 1)
	go func() {
		tx, err := store.beginUncheckedWrite(callerCtx)
		if tx != nil {
			_ = tx.Rollback(nil)
			tx.Close()
		}
		beginResult <- err
	}()

	select {
	case <-beginEntered:
	case <-time.After(time.Second):
		t.Fatal("BEGIN IMMEDIATE did not enter the real driver")
	}
	select {
	case beginErr := <-beginResult:
		t.Fatalf("BEGIN IMMEDIATE returned before cancellation: %v", beginErr)
	default:
	}
	cancelCaller()
	select {
	case beginErr := <-beginResult:
		if beginErr == nil || !errors.Is(beginErr, context.Canceled) {
			t.Fatalf("cancelled BEGIN error = %v, want context.Canceled", beginErr)
		}
		var unknown *OutcomeUnknownError
		if !errors.As(beginErr, &unknown) {
			t.Fatalf("cancelled BEGIN error = %v, want OutcomeUnknownError", beginErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled BEGIN did not return while the write lock was held")
	}
	if err := held.Rollback(nil); err != nil {
		t.Fatalf("release the blocking write lock: %v", err)
	}
	held.Close()
	released = true

	if open := store.writer.Stats().OpenConnections; open != 1 {
		t.Fatalf("retained writer set = %d physical connections after a cancelled BEGIN, want 1", open)
	}
	if currentID := faultWriterConnectionID(t, store); currentID != writerID || plan.wasClosed(currentID) {
		t.Fatalf("cancelled BEGIN did not retain clean writer connection: id=%d closed=%v", currentID, plan.wasClosed(currentID))
	}

	// The Store must still serve writes: this is what the shutdown signature broke.
	after, err := store.beginUncheckedWrite(context.Background())
	if err != nil {
		t.Fatalf("write after a cancelled BEGIN: %v", err)
	}
	if err := after.Rollback(nil); err != nil {
		t.Fatalf("rollback after a cancelled BEGIN: %v", err)
	}
	after.Close()
}
