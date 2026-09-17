//go:build darwin

package daemon

import (
	"testing"
	"time"
)

func TestSuccessfulRuntimeCleanupDrainsRetainedSourceBeforeDroppingLive(t *testing.T) {
	runID, sessionID := liveTestIDs(t, 13001)
	attempt := newLiveAttempt(nil, runID, sessionID, nil)
	if !attempt.beginSourceOperation() {
		t.Fatal("initial source operation refused")
	}
	close(attempt.done)
	if err := attempt.join(); err != nil {
		t.Fatalf("joined successful owner = %v", err)
	}
	drained := make(chan struct{})
	go func() {
		attempt.closeSourceOperations()
		close(drained)
	}()
	<-attempt.sourceCloseStarted
	select {
	case <-drained:
		t.Fatal("runtime cleanup completed before source materialization ended")
	default:
	}
	attempt.endSourceOperation()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("runtime cleanup did not drain source materialization")
	}
}
