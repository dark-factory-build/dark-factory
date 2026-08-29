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

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	released := make(chan struct{})
	go func() {
		// Cancel the caller while BEGIN IMMEDIATE is blocked on the lock, then
		// release the lock so a lifecycle-bounded BEGIN can still converge.
		time.Sleep(50 * time.Millisecond)
		cancelCaller()
		time.Sleep(50 * time.Millisecond)
		_ = held.Rollback(nil)
		held.Close()
		close(released)
	}()

	tx, beginErr := store.beginUncheckedWrite(callerCtx)
	if beginErr == nil {
		_ = tx.Rollback(nil)
		tx.Close()
	} else if errors.Is(beginErr, errConnectionSetExhausted) {
		t.Fatalf("cancelled BEGIN exhausted the retained writer set: %v", beginErr)
	}
	<-released

	if open := store.writer.Stats().OpenConnections; open != 1 {
		t.Fatalf("retained writer set = %d physical connections after a cancelled BEGIN, want 1", open)
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
