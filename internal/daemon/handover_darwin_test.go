//go:build darwin

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

// handoverResultWire mirrors the runner package's private attempt-result.json
// shape closely enough to forge one real, authenticatable "inner_converged"
// spool for the adoption tests below: a real result on disk, not a stub.
type handoverResultWire struct {
	Version   int                     `json:"version"`
	AttemptID string                  `json:"attempt_id"`
	Kind      string                  `json:"kind"`
	Proof     string                  `json:"proof"`
	Process   *runner.Identity        `json:"process,omitempty"`
	Exit      *handoverResultExitWire `json:"exit,omitempty"`
}

type handoverResultExitWire struct {
	Code *int `json:"code,omitempty"`
}

// runProviderIdentity finishes bringing a recoveryFixture run to exactly the
// RunRunning/ResourceActive shape the boot-handover adoption path requires: a
// running attempt with an active runner and an active provider pair.
func (fixture *recoveryFixture) runProviderIdentity(t *testing.T, pid int) kernel.ResourceIdentity {
	t.Helper()
	ctx := context.Background()
	providerIdentity, err := processResourceIdentity(runner.Identity{PID: pid, PGID: pid, Birth: runner.Birth{Seconds: 1700, Microseconds: 3}})
	if err != nil {
		t.Fatal(err)
	}
	states := fixture.resourceStates(t)
	process, group := states[kernel.ResourceProviderProcess], states[kernel.ResourceProviderGroup]
	if _, _, err := fixture.store.ActivateProviderResources(ctx, fixture.run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustKernelTime(t, 240)); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.store.TerminalSessionForRun(ctx, fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}
	run, err := fixture.store.ActivateRun(ctx, fixture.run.ID, session.ID, fixture.currentRun(t).Revision, session.Revision, mustKernelTime(t, 250))
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = run
	return providerIdentity
}

// holdLifetimeLease locks the runtime's lifetime file exactly like a live
// runner would, so OpenRecoveredRuntime observes it busy: the precondition
// every handover adoption test in this file needs.
func (fixture *recoveryFixture) holdLifetimeLease(t *testing.T) *os.File {
	t.Helper()
	lease, err := os.OpenFile(filepath.Join(fixture.parentPath, fixture.run.ID.String(), runner.RuntimeLifetimeLeaseName), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return lease
}

func (fixture *recoveryFixture) writeTakeoverGrant(t *testing.T, token string) {
	t.Helper()
	body, err := json.Marshal(takeoverGrant{RunID: fixture.run.ID.String(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.parentPath, fixture.run.ID.String(), runner.TakeoverGrantName), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *recoveryFixture) listenTakeover(t *testing.T) net.Listener {
	t.Helper()
	path := filepath.Join(fixture.parentPath, fixture.run.ID.String(), runner.TakeoverSocketName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// The runner's own endpoint is private; runtime removal knows it by that
	// exact shape, so the fixture must bind it the same way.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// writeForgedInnerConvergedResult writes a real, canonically shaped
// attempt-result.json spool for the given proof and provider identity, and
// returns its exact on-disk file identity and content digest, the same
// values AuthenticateAttemptResult recomputes and compares against.
func (fixture *recoveryFixture) writeForgedInnerConvergedResult(t *testing.T, proof [32]byte, provider runner.Identity, code int) (runner.FileIdentity, string) {
	t.Helper()
	codeValue := code
	wire := handoverResultWire{
		Version: 1, AttemptID: fixture.run.ID.String(), Kind: "inner_converged",
		Proof: hex.EncodeToString(proof[:]), Process: &provider, Exit: &handoverResultExitWire{Code: &codeValue},
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	// A converged inner attempt leaves both activation markers behind; the
	// authenticator refuses a spool without them.
	fixture.writeMarker(t, runner.OuterActivationMarkerName)
	fixture.writeMarker(t, runner.InnerActivationMarkerName)
	fixture.writeArtifact(t, body)
	path := filepath.Join(fixture.parentPath, fixture.run.ID.String(), runner.AttemptResultSpoolName)
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, hex.EncodeToString(sum[:])
}

// takeoverHandshake is one fake runner endpoint's outcome: the accepted,
// still-open control connection the daemon adopted, or why it never was.
type takeoverHandshake struct {
	conn net.Conn
	err  error
}

// serveTakeoverHandshake plays the runner side of one exact takeover dial:
// it accepts the single connection, reads and checks the client's grant
// presentation line, replies accepted, and sends handover-attached. It then
// hands the still-open connection to the caller, exactly as a live runner
// keeps its control capability open: the daemon commits the adopted
// descriptor (getpeername included) after the handshake, and a peer that
// vanished first would make that commitment fail. The caller owns the
// connection from there and writes the attempt result on it. Failures are
// reported on the returned channel rather than through t, because this runs
// on its own goroutine and must never call t.Fatal.
func serveTakeoverHandshake(listener net.Listener, runID, token string) <-chan takeoverHandshake {
	handshakes := make(chan takeoverHandshake, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			handshakes <- takeoverHandshake{err: err}
			return
		}
		if err := func() error {
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			// One newline-terminated request line; the daemon keeps its write
			// half open for the control frames that follow.
			line, err := readTakeoverReplyLine(conn, maxTakeoverGrantBytes)
			if err != nil {
				return err
			}
			var request takeoverGrant
			if err := json.Unmarshal(line, &request); err != nil {
				return fmt.Errorf("decode takeover request: %w", err)
			}
			if request.RunID != runID || request.Token != token {
				return fmt.Errorf("unexpected takeover request %+v", request)
			}
			if _, err := conn.Write([]byte("{\"accepted\":true}\n")); err != nil {
				return err
			}
			if err := writeHandoverTestFrame(conn, terminalEffectWireFrame{Version: 2, Kind: "handover-attached"}); err != nil {
				return fmt.Errorf("write handover-attached: %w", err)
			}
			return conn.SetDeadline(time.Time{})
		}(); err != nil {
			_ = conn.Close()
			handshakes <- takeoverHandshake{err: err}
			return
		}
		handshakes <- takeoverHandshake{conn: conn}
	}()
	return handshakes
}

// writeHandoverTestFrame writes one length-prefixed JSON frame using the
// exact wire shape internal/runner's attempt control protocol reads,
// mirrored here as terminalEffectWireFrame. It never calls t.Fatal so it is
// safe from a background goroutine.
func writeHandoverTestFrame(w io.Writer, frame terminalEffectWireFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	_, err = w.Write(append(header[:], body...))
	return err
}

// adoptedRun is one run this daemon adopted from a live protocol-2 runner,
// with the single lever a test needs to make that runner converge.
type adoptedRun struct {
	provider  kernel.ResourceIdentity
	handshake takeoverHandshake
	// converge plays the runner's own convergence: it exits, releasing the
	// runtime lifetime lease it has held since before this daemon booted,
	// and publishes its result on the connection the takeover handed over.
	// The lease goes first so the tail cannot observe a runtime a converged
	// runner still holds.
	converge func()
}

// adoptRunningHandover stages a running attempt behind a real takeover
// socket, runs one recovery sweep, and asserts the sweep adopted it without
// touching the durable run.
func (fixture *recoveryFixture) adoptRunningHandover(t *testing.T, pid int) *adoptedRun {
	t.Helper()
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	provider := fixture.runProviderIdentity(t, pid)
	lease := fixture.holdLifetimeLease(t)
	token := "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab1"
	fixture.writeTakeoverGrant(t, token)
	listener := fixture.listenTakeover(t)
	spoolIdentity, spoolDigest := fixture.writeForgedInnerConvergedResult(t, fixture.proof, runner.Identity{PID: pid, PGID: pid, Birth: runner.Birth{Seconds: 1700, Microseconds: 3}}, 0)
	endpoint := serveTakeoverHandshake(listener, fixture.run.ID.String(), token)

	before := fixture.currentRun(t)
	disposition := fixture.sweep(t)
	if disposition.Action != RecoveredAdopted || disposition.Err != nil {
		t.Fatalf("adoption disposition = %+v", disposition)
	}
	var handshake takeoverHandshake
	select {
	case handshake = <-endpoint:
		if handshake.err != nil {
			t.Fatalf("fake takeover endpoint: %v", handshake.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fake takeover endpoint did not finish")
	}
	t.Cleanup(func() { _ = handshake.conn.Close() })
	// The adopted attempt is a normal registered live owner immediately, and
	// the run is untouched: still running, with its provider still declared.
	fixture.daemon.attemptMu.Lock()
	_, registered := fixture.daemon.attempts[fixture.run.ID]
	fixture.daemon.attemptMu.Unlock()
	if !registered {
		t.Fatal("adopted run was not registered as a live attempt")
	}
	afterAdoption := fixture.currentRun(t)
	if afterAdoption.Phase != kernel.RunRunning || afterAdoption.Revision != before.Revision {
		t.Fatalf("adoption mutated the run: %+v -> %+v", before, afterAdoption)
	}
	if states := fixture.resourceStates(t); states[kernel.ResourceProviderProcess].Identity != provider {
		t.Fatalf("adopted provider identity = %+v, want %+v", states[kernel.ResourceProviderProcess].Identity, provider)
	}
	return &adoptedRun{provider: provider, handshake: handshake, converge: func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
			return
		}
		identity := spoolIdentity
		if err := writeHandoverTestFrame(handshake.conn, terminalEffectWireFrame{
			Version: 1, Kind: "attempt-result-ready", FileIdentity: &identity, Digest: spoolDigest,
		}); err != nil {
			t.Error(err)
		}
	}}
}

// awaitSettled waits for the adopted run's background tail to reach terminal
// and reports what it was still holding if it never did.
func (fixture *recoveryFixture) awaitSettled(t *testing.T, patience time.Duration) kernel.Run {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		run := fixture.currentRun(t)
		if run.Phase == kernel.RunTerminal {
			return run
		}
		if time.Now().After(deadline) {
			detail := "<no proposal>"
			if run.Proposal != nil {
				detail = fmt.Sprintf("kind=%s code=%s detail=%q", run.Proposal.Kind().String(), run.Proposal.Code().String(), run.Proposal.Detail())
			}
			t.Fatalf("adopted run did not settle: phase=%s proposal=[%s]", run.Phase.String(), detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRecoverySweepAdoptsRunningHandoverAndSettlesInBackground(t *testing.T) {
	fixture := newRecoveryFixture(t, 0x80)
	adopted := fixture.adoptRunningHandover(t, 99990)
	adopted.converge()

	// The background tail authenticates, consumes, and settles the run
	// exactly like runNext's own tail; wait for it to converge.
	final := fixture.awaitSettled(t, 6*time.Second)
	if final.Proposal == nil || final.Terminal == nil {
		t.Fatalf("settled adopted run = %+v", final)
	}
	states := fixture.resourceStates(t)
	for _, kind := range []kernel.ResourceKind{kernel.ResourceRunnerProcess, kernel.ResourceProviderProcess, kernel.ResourceProviderGroup, kernel.ResourceRuntimeRoot} {
		if states[kind].State != kernel.ResourceReleased {
			t.Fatalf("settled resource %s = %+v", kind, states[kind])
		}
	}
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), fixture.run.ID)
	if err != nil || !found || session.State != kernel.TerminalSessionClosed {
		t.Fatalf("settled session = %+v found=%v err=%v", session, found, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.parentPath, fixture.run.ID.String())); !os.IsNotExist(err) {
		t.Fatalf("settled runtime directory persists: %v", err)
	}
	fixture.daemon.attemptMu.Lock()
	_, stillRegistered := fixture.daemon.attempts[fixture.run.ID]
	fixture.daemon.attemptMu.Unlock()
	if stillRegistered {
		t.Fatal("settled adopted run is still a registered live attempt")
	}
}

// TestAdoptedTailContinuesAfterBlockedRuntimeRemoval is the adopted twin of
// TestContinueUnsettledRunOutlivesRuntimeWriter: the adopted tail is the only
// owner this run has, so a tail that stops short on a runtime it cannot yet
// remove must continue itself rather than leave the run finalizing with a
// proposal and nobody waiting on it.
func TestAdoptedTailContinuesAfterBlockedRuntimeRemoval(t *testing.T) {
	fixture := newRecoveryFixture(t, 0xb0)
	adopted := fixture.adoptRunningHandover(t, 99993)
	work := filepath.Join(fixture.parentPath, fixture.run.ID.String(), runtimeTempName, "go-build")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// A `go build` look-alike churning under tmp for a bounded window, the
	// #726 shape: runtime removal refuses a tree that changes under it.
	writerCtx, stopWriter := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer stopWriter()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for writerCtx.Err() == nil {
			for index := 0; index < 64; index++ {
				action := filepath.Join(work, fmt.Sprintf("b%03d", index))
				_ = os.Mkdir(action, 0o755)
				_ = os.WriteFile(filepath.Join(action, "_pkg_.a"), nil, 0o644)
			}
			for index := 0; index < 64; index += 2 {
				_ = os.RemoveAll(filepath.Join(work, fmt.Sprintf("b%03d", index)))
			}
		}
	}()
	adopted.converge()
	final := fixture.awaitSettled(t, 30*time.Second)
	<-writerDone
	if final.Proposal == nil || final.Terminal == nil {
		t.Fatalf("settled adopted run = %+v", final)
	}
	for kind, resource := range fixture.resourceStates(t) {
		if resource.State != kernel.ResourceReleased {
			t.Fatalf("%s retained: %+v", kind, resource)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.parentPath, fixture.run.ID.String())); !os.IsNotExist(err) {
		t.Fatalf("settled runtime directory persists: %v", err)
	}
}

// TestLiveAttemptShutdownHandsOverReleasedControllerWithoutFailingRun proves
// the daemon-side half of the protocol-2 shutdown handover: a context
// cancellation reaching a released, terminal-ready controller sends
// handover-quiesce, and once the runner replies handover-quiesced the
// attempt converges with no error and, critically, without ever touching the
// durable run or its resources — exactly the fact runNext's own early return
// depends on. This drives the controller with a manually simulated runner
// peer, the same pattern readyTerminalEffectController already uses,
// because the real runner process built by this worktree's own TestMain
// does not yet wire a live takeover listener into RunAttemptRunner (that is
// the parallel runner-side agent's work) and would otherwise always answer
// handover-quiesce with a graceful handover-rejected fallback.
func TestLiveAttemptShutdownHandsOverReleasedControllerWithoutFailingRun(t *testing.T) {
	fixture := newRecoveryFixture(t, 0xa0)
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.runProviderIdentity(t, 99992)
	before := fixture.currentRun(t)
	beforeResources := fixture.resourceStates(t)
	for kind, resource := range beforeResources {
		if resource.State != kernel.ResourceActive {
			t.Fatalf("precondition: resource %s = %+v, want active", kind, resource)
		}
	}
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}

	controller, peer := readyTerminalEffectController(t)
	attempt := newLiveAttempt(fixture.daemon, fixture.run.ID, session.ID, controller)
	attempt.releaseSent = true
	attempt.readySeen = true
	if err := fixture.daemon.registerLiveAttempt(attempt); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	startLiveAttempt(attempt, runCtx)
	cancel()

	frame := readTerminalEffectWire(t, peer)
	if frame.Version != 2 || frame.Kind != "handover-quiesce" {
		t.Fatalf("shutdown frame = %+v", frame)
	}
	writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 2, Kind: "handover-quiesced", Floor: 5, Head: 42})

	result := attempt.waitResult()
	if !result.handedOver || result.err != nil || result.notice != nil {
		t.Fatalf("shutdown handover result = %+v", result)
	}
	select {
	case <-attempt.done:
	case <-time.After(3 * time.Second):
		t.Fatal("attempt did not converge after handover")
	}
	if attempt.finalErr != nil {
		t.Fatalf("handed-over attempt final error = %v", attempt.finalErr)
	}
	fixture.daemon.attemptMu.Lock()
	_, stillRegistered := fixture.daemon.attempts[fixture.run.ID]
	fixture.daemon.attemptMu.Unlock()
	if stillRegistered {
		t.Fatal("handed-over attempt is still registered")
	}

	after := fixture.currentRun(t)
	if after.Phase != before.Phase || after.Revision != before.Revision || after.Phase != kernel.RunRunning {
		t.Fatalf("shutdown handover mutated the run: %+v -> %+v", before, after)
	}
	afterResources := fixture.resourceStates(t)
	for kind, resource := range beforeResources {
		if afterResources[kind].State != resource.State || afterResources[kind].Revision != resource.Revision {
			t.Fatalf("shutdown handover mutated resource %s: %+v -> %+v", kind, resource, afterResources[kind])
		}
	}
}

func TestRecoverySweepStaysLiveHolderForBusyRuntimeWithoutTakeoverSocket(t *testing.T) {
	fixture := newRecoveryFixture(t, 0x90)
	fixture.stageRuntime(t)
	fixture.beginRunnerStart(t)
	fixture.activateRunner(t)
	fixture.runProviderIdentity(t, 99991)
	fixture.holdLifetimeLease(t)
	// Deliberately no takeover.sock or takeover.json: a busy runtime with no
	// grant must fall back to today's live-holder conclusion unchanged.
	before := fixture.currentRun(t)
	disposition := fixture.sweep(t)
	if disposition.Action != RecoveredLiveHolder || disposition.Err != nil {
		t.Fatalf("disposition = %+v", disposition)
	}
	after := fixture.currentRun(t)
	if after.Phase != before.Phase || after.Revision != before.Revision {
		t.Fatalf("busy runtime without a grant mutated the run: %+v -> %+v", before, after)
	}
	fixture.daemon.attemptMu.Lock()
	_, registered := fixture.daemon.attempts[fixture.run.ID]
	fixture.daemon.attemptMu.Unlock()
	if registered {
		t.Fatal("live-holder disposition registered a live attempt")
	}
}
