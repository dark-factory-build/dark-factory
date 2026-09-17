//go:build darwin

package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProposeOutcomeRefusesBeforeDurableProposalForLiveWorker(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 0x36, testID(0x36), "worker")
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), active.run.ID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v found=%v err=%v", session, found, err)
	}
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, nil)
	digest := sha256.Sum256(active.bearer)
	live.attemptDigest, _ = kernel.AttemptDigestFromBytes(digest[:])
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.daemon.unregisterLiveAttempt(active.run.ID, live) })

	done := fixture.serve(t)
	if _, err := active.client.Succeed(context.Background(), "dirty implementation"); err == nil {
		t.Fatal("dirty-source success unexpectedly accepted")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteConflict {
			t.Fatalf("dirty-source refusal = %v", err)
		}
	}
	waitDispatch(t, done)
	observed, found, err := fixture.store.Run(context.Background(), active.run.ID)
	if err != nil || !found || observed.Phase != kernel.RunRunning || observed.Proposal != nil {
		t.Fatalf("refusal changed durable run: %+v found=%v err=%v", observed, found, err)
	}
}
