//go:build darwin

package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// This uses an actual long-running PTY child. The endpoint's grant check is
// tested separately; only its authenticated descriptor handoff is simulated.
func TestTerminalOwnerKeepsSameProviderAndPTYAcrossControlReattach(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(f.root, "provider.ready")
	continued := filepath.Join(f.root, "provider.continue")
	after := filepath.Join(f.root, "provider.after")
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("printf 'before-handover\\n'; printf x > %q; while test ! -f %q; do sleep 0.01; done; printf 'after-handover\\n'; printf x > %q; exec /bin/sleep 30", ready, continued, after)
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	installOwnedChildSafetyCleanup(t, child)
	identity := child.Identity()
	master := child.ptyMaster
	oldRunner, oldDaemon, err := newControlPair("old-runner", "old-daemon")
	if err != nil {
		t.Fatal(err)
	}
	defer oldRunner.Close()
	reads := &attemptReadSet{kq: child.kq, daemonFD: int(oldRunner.Fd()), workerFD: -1, ptyFD: int(master.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerPTY(); err != nil {
		t.Fatal(err)
	}
	defer reads.processOnly()
	replacements := make(chan *os.File, 1)
	admissionStopped := false
	transport := &HandoverTransport{Replacements: replacements, Current: oldRunner, Stop: func() { admissionStopped = true }}
	owner := &terminalOwner{child: child, daemon: oldRunner, reads: reads, daemonOpen: true, ptyOpen: true, ring: &terminalByteRing{}, handover: transport}
	done := make(chan error, 1)
	go func() { _, err := owner.serve(); done <- err }()
	old := &AttemptController{file: oldDaemon, state: controllerProviderReleased, terminalReady: true}
	defer old.Close()
	if err := old.SendHandoverQuiesce(); err != nil {
		t.Fatal(err)
	}
	quiesced, err := old.Next(4 * time.Second)
	if err != nil || quiesced.Kind != AttemptHandoverQuiesced || quiesced.Floor > quiesced.Head {
		t.Fatalf("quiesced=%+v err=%v", quiesced, err)
	}
	if err := old.Terminate(); !errors.Is(err, ErrState) {
		t.Fatalf("old owner retained control: %v", err)
	}
	if got, err := readIdentity(identity.PID); err != nil || got != identity || child.ptyMaster != master {
		t.Fatalf("provider or PTY changed before reattach: identity=%+v err=%v", got, err)
	}
	newRunner, newDaemon, err := newControlPair("new-runner", "new-daemon")
	if err != nil {
		t.Fatal(err)
	}
	replacements <- newRunner // endpoint delivers this only after grant/fence proof
	readTakeoverAccept(t, newDaemon)
	newOwner, err := AdoptHandoverControl(newDaemon)
	if err != nil {
		t.Fatal(err)
	}
	defer newOwner.Close()
	attached, err := newOwner.Next(4 * time.Second)
	if err != nil || attached.Kind != AttemptHandoverAttached || attached.Floor > attached.Head || attached.Head < quiesced.Head {
		t.Fatalf("attached=%+v err=%v", attached, err)
	}
	if got, err := readIdentity(identity.PID); err != nil || got != identity || child.ptyMaster != master {
		t.Fatalf("provider or PTY changed across reattach: identity=%+v err=%v", got, err)
	}
	// A replacement daemon dying after attachment leaves the same provider
	// recoverable by another fenced connection; it does not replay input.
	if err := newOwner.Close(); err != nil {
		t.Fatal(err)
	}
	finalRunner, finalDaemon, err := newControlPair("final-runner", "final-daemon")
	if err != nil {
		t.Fatal(err)
	}
	replacements <- finalRunner
	readTakeoverAccept(t, finalDaemon)
	finalOwner, err := AdoptHandoverControl(finalDaemon)
	if err != nil {
		t.Fatal(err)
	}
	defer finalOwner.Close()
	if event, err := finalOwner.Next(4 * time.Second); err != nil || event.Kind != AttemptHandoverAttached {
		t.Fatalf("crash reattach=%+v err=%v", event, err)
	}
	if got, err := readIdentity(identity.PID); err != nil || got != identity || child.ptyMaster != master {
		t.Fatalf("provider or PTY changed after daemon crash: identity=%+v err=%v", got, err)
	}
	if err := os.WriteFile(continued, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFile(t, after)
	// Queue a replacement before the provider exits. The owner must consume
	// it from the handover channel on the child-exit path; there is no PTY
	// read or idle tick left to trigger the usual handover step.
	exitRunner, exitDaemon, err := newControlPair("exit-runner", "exit-daemon")
	if err != nil {
		t.Fatal(err)
	}
	exitOwner, err := AdoptHandoverControl(exitDaemon)
	if err != nil {
		t.Fatal(err)
	}
	defer exitOwner.Close()
	replacements <- exitRunner
	if err := unix.Kill(-identity.PGID, unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("terminal owner: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("reattached owner did not converge")
	}
	readTakeoverAccept(t, exitDaemon)
	if event, err := exitOwner.Next(4 * time.Second); err != nil || event.Kind != AttemptHandoverAttached {
		t.Fatalf("exit-path reattach=%+v err=%v", event, err)
	}
	if transport.Current != exitRunner {
		t.Fatal("final result authority did not follow the replacement connection")
	}
	if !admissionStopped {
		t.Fatal("handover admission was not stopped before exit-path adoption")
	}
	// Finalization must notify the adopted daemon. The original daemon socket
	// was fenced during adoption, so using it here would publish a durable
	// spool without delivering the result that settles the live run.
	cfg := attemptConfig{AttemptID: "attempt-handover-settlement", ResultName: AttemptResultSpoolName, ResultProof: testResultProofHex()}
	if err := finishAttemptWithExit(child, f.dir, cfg, reads, transport.Current, true, nil); err != nil {
		t.Fatalf("finish adopted attempt: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		result, err := exitOwner.Next(time.Until(deadline))
		if err != nil {
			t.Fatalf("adopted result notification err=%v", err)
		}
		if result.Kind == AttemptResultReady {
			if result.Result == nil {
				t.Fatal("adopted result notification had no notice")
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("adopted result notification=%+v", result)
		}
	}
	output, _, err := owner.ring.Read(owner.ring.Floor())
	if err != nil || string(output) != "before-handover\r\nafter-handover\r\n" {
		t.Fatalf("ordered PTY output=%q err=%v", output, err)
	}
	if err := transport.Current.Close(); err != nil {
		t.Fatal(err)
	}
}

// withShortHandoverGrace overrides handoverDetachedGrace for the duration of
// one test. Tests in this package never run in parallel, so the shared
// package var is safe to mutate and restore.
func withShortHandoverGrace(t *testing.T, d time.Duration) {
	t.Helper()
	previous := testHandoverDetachedGrace
	testHandoverDetachedGrace = d
	t.Cleanup(func() { testHandoverDetachedGrace = previous })
}

// shortRuntimeRoot returns a fixture root short enough to keep
// takeover.sock's absolute path under macOS's 104-byte sun_path bound; a
// long, test-name-derived t.TempDir() path can exceed it.
func shortRuntimeRoot(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/private/tmp", "df-runner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

type handoverFixture struct {
	f         *fixture
	identity  Identity
	owner     *terminalOwner
	transport *HandoverTransport
	old       *AttemptController
	done      chan error
	continued string
	after     string
}

// startHandoverFixture launches a long-running PTY provider behind a real
// takeover.sock/takeover.json endpoint with its original control connection
// still attached: the state a replacement daemon finds when the daemon it
// displaces has not quiesced or died.
func startHandoverFixture(t *testing.T, attemptID string) *handoverFixture {
	t.Helper()
	f := newFixtureAt(t, shortRuntimeRoot(t))
	ready := filepath.Join(f.root, "provider.ready")
	continued := filepath.Join(f.root, "provider.continue")
	after := filepath.Join(f.root, "provider.after")
	script := fmt.Sprintf("printf 'before-handover\\n'; printf x > %q; while test ! -f %q; do sleep 0.01; done; printf 'after-handover\\n'; printf x > %q; exec /bin/sleep 30", ready, continued, after)
	hf := startHandoverProvider(t, f, attemptID, script, ready)
	hf.continued, hf.after = continued, after
	return hf
}

// startBusyHandoverFixture is the same endpoint behind a provider that never
// stops writing: a numbered line as fast as the shell can print it, the way
// a TUI redraw keeps the PTY readable, so the owner loop's idle tick never
// fires while it runs.
func startBusyHandoverFixture(t *testing.T, attemptID string) *handoverFixture {
	t.Helper()
	f := newFixtureAt(t, shortRuntimeRoot(t))
	ready := filepath.Join(f.root, "provider.ready")
	script := fmt.Sprintf("printf x > %q; i=0; while :; do i=$((i+1)); printf 'line %%d\\n' $i; done", ready)
	return startHandoverProvider(t, f, attemptID, script, ready)
}

func startHandoverProvider(t *testing.T, f *fixture, attemptID, script, ready string) *handoverFixture {
	t.Helper()
	gate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: f.cwd})
	if err != nil {
		t.Fatal(err)
	}
	child, err := StartBlockedPTY(f.lease, gate, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	f.child = child
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	installOwnedChildSafetyCleanup(t, child)
	identity := child.Identity()
	oldRunner, oldDaemon, err := newControlPair("old-runner", "old-daemon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldRunner.Close() })
	reads := &attemptReadSet{kq: child.kq, daemonFD: int(oldRunner.Fd()), workerFD: -1, ptyFD: int(child.ptyMaster.Fd())}
	if err := reads.registerDaemon(); err != nil {
		t.Fatal(err)
	}
	if err := reads.registerPTY(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reads.processOnly() })
	transport, closeEndpoint := startTakeoverEndpoint(f.dir, attemptID)
	if transport == nil {
		t.Fatal("startTakeoverEndpoint degraded to no transport in a short-root fixture")
	}
	t.Cleanup(closeEndpoint)
	owner := &terminalOwner{child: child, daemon: oldRunner, reads: reads, daemonOpen: true, ptyOpen: true, ring: &terminalByteRing{}, handover: transport}
	done := make(chan error, 1)
	go func() { _, err := owner.serve(); done <- err }()
	old := &AttemptController{file: oldDaemon, state: controllerProviderReleased, terminalReady: true}
	t.Cleanup(func() { _ = old.Close() })
	return &handoverFixture{f: f, identity: identity, owner: owner, transport: transport, old: old, done: done}
}

// startQuiescedHandoverFixture is the same fixture after the original daemon
// has sent handover-quiesce and been fenced: the detached state a replacement
// finds when the daemon it displaces left cleanly (or died).
func startQuiescedHandoverFixture(t *testing.T, attemptID string) *handoverFixture {
	t.Helper()
	hf := startHandoverFixture(t, attemptID)
	if err := hf.old.SendHandoverQuiesce(); err != nil {
		t.Fatal(err)
	}
	quiesced, err := hf.old.Next(4 * time.Second)
	if err != nil || quiesced.Kind != AttemptHandoverQuiesced || quiesced.Floor > quiesced.Head {
		t.Fatalf("quiesced=%+v err=%v", quiesced, err)
	}
	return hf
}

// readTakeoverAccept consumes the one accepted reply the owner loop writes on
// a replacement connection, after fencing the owner it displaces and before
// its first frame.
func readTakeoverAccept(t *testing.T, file *os.File) {
	t.Helper()
	line, err := readTakeoverLine(file, maxTakeoverBody)
	if err != nil {
		t.Fatal(err)
	}
	var response takeoverResponse
	if err := json.Unmarshal(line, &response); err != nil || !response.Accepted {
		t.Fatalf("replacement reply %q = %+v: %v", line, response, err)
	}
}

func readTakeoverToken(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, TakeoverGrantName))
	if err != nil {
		t.Fatal(err)
	}
	var grant takeoverGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatal(err)
	}
	return grant.Token
}

// dialTakeover sends one newline-terminated takeover request and reads the
// one newline-terminated reply, leaving both halves of the connection open
// and positioned exactly after that reply, so a caller keeps reading framed
// events (e.g. handover-attached) from the same connection.
func dialTakeover(t *testing.T, root, runID, token string) (net.Conn, takeoverResponse) {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(root, TakeoverSocketName))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(takeoverGrant{RunID: runID, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := readTakeoverLine(conn, maxTakeoverBody)
	if err != nil {
		t.Fatal(err)
	}
	var response takeoverResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return conn, response
}

func TestTakeoverShutdownRejectsQueuedCandidate(t *testing.T) {
	f := newFixtureAt(t, shortRuntimeRoot(t))
	const attemptID = "attempt-takeover-shutdown"
	transport, closeEndpoint := startTakeoverEndpoint(f.dir, attemptID)
	if transport == nil {
		t.Fatal("startTakeoverEndpoint degraded to no transport in a short-root fixture")
	}
	token := readTakeoverToken(t, f.root)
	conn, err := net.Dial("unix", filepath.Join(f.root, TakeoverSocketName))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body, err := json.Marshal(takeoverGrant{RunID: attemptID, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for readTakeoverToken(t, f.root) == token {
		if !time.Now().Before(deadline) {
			t.Fatal("takeover request was not authenticated before shutdown")
		}
		time.Sleep(time.Millisecond)
	}
	// Shutdown joins the accept loop, then rejects the authenticated descriptor
	// that was queued after the owner stopped consuming replacements.
	closeEndpoint()
	if err := conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := readTakeoverLine(conn, maxTakeoverBody)
	if err != nil {
		t.Fatal(err)
	}
	var response takeoverResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.Accepted || response.Error != "runner-exiting" {
		t.Fatalf("shutdown response=%+v, want runner-exiting refusal", response)
	}
	for _, name := range []string{TakeoverSocketName, TakeoverGrantName} {
		if _, err := os.Stat(filepath.Join(f.root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdown left %s: %v", name, err)
		}
	}
}

func awaitHandoverConverge(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("terminal owner: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("owner loop did not converge")
	}
}

// TestTakeoverEndpointAttachesOnDiskToken drives the real takeover.sock
// endpoint end to end: a client that only knows the on-disk token is
// accepted, sees handover-attached, and the provider's PID/PTY never change.
// Closing that connection afterward lets the short grace override converge
// the attempt, proving the result path still runs to completion.
func TestTakeoverEndpointAttachesOnDiskToken(t *testing.T) {
	withShortHandoverGrace(t, 200*time.Millisecond)
	const attemptID = "attempt-takeover-ok"
	hf := startQuiescedHandoverFixture(t, attemptID)
	token := readTakeoverToken(t, hf.f.root)
	conn, response := dialTakeover(t, hf.f.root, attemptID, token)
	if !response.Accepted {
		t.Fatalf("takeover rejected: %+v", response)
	}
	var attached attemptFrame
	if err := readFrame(conn, &attached, maxConfigBytes); err != nil || attached.Kind != "handover-attached" || attached.Floor > attached.Head {
		t.Fatalf("attached frame=%+v err=%v", attached, err)
	}
	if got, err := readIdentity(hf.identity.PID); err != nil || got != hf.identity {
		t.Fatalf("provider changed across takeover: identity=%+v err=%v", got, err)
	}
	if err := os.WriteFile(hf.continued, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFile(t, hf.after)
	// A replacement daemon that later disappears (crash, restart) leaves the
	// runner detached again; the short grace override converges the attempt
	// instead of waiting on the real ten-minute ceiling.
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	awaitHandoverConverge(t, hf.done)
	output, _, err := hf.owner.ring.Read(hf.owner.ring.Floor())
	if err != nil || string(output) != "before-handover\r\nafter-handover\r\n" {
		t.Fatalf("ordered PTY output=%q err=%v", output, err)
	}
}

// TestTakeoverStaleTokenRefusedRunContinues proves a wrong-token probe is
// refused without rotating the real bearer or disturbing the detached run,
// and that the legitimate takeover afterward still succeeds normally.
func TestTakeoverStaleTokenRefusedRunContinues(t *testing.T) {
	withShortHandoverGrace(t, 200*time.Millisecond)
	const attemptID = "attempt-takeover-stale"
	hf := startQuiescedHandoverFixture(t, attemptID)
	real := readTakeoverToken(t, hf.f.root)
	stale, err := newTakeoverToken()
	if err != nil || stale == real {
		t.Fatalf("stale probe collided with the real token: %v", err)
	}
	staleConn, staleResponse := dialTakeover(t, hf.f.root, attemptID, stale)
	if staleResponse.Accepted || staleResponse.Error != "stale" {
		t.Fatalf("stale takeover response=%+v", staleResponse)
	}
	_ = staleConn.Close()
	if got := readTakeoverToken(t, hf.f.root); got != real {
		t.Fatal("a rejected probe rotated the bearer")
	}
	conn, response := dialTakeover(t, hf.f.root, attemptID, real)
	if !response.Accepted {
		t.Fatalf("legitimate takeover after stale probe rejected: %+v", response)
	}
	var attached attemptFrame
	if err := readFrame(conn, &attached, maxConfigBytes); err != nil || attached.Kind != "handover-attached" {
		t.Fatalf("attached frame=%+v err=%v", attached, err)
	}
	if got, err := readIdentity(hf.identity.PID); err != nil || got != hf.identity {
		t.Fatalf("provider changed after stale probe: identity=%+v err=%v", got, err)
	}
	_ = conn.Close()
	awaitHandoverConverge(t, hf.done)
}

// TestTakeoverDetachedGraceTerminatesProvider proves that a quiesced run no
// replacement ever claims still converges: past the (overridden, short)
// grace ceiling the runner terminates the provider itself and returns
// without ErrUnresolved, so the caller's normal result-publishing path runs.
func TestTakeoverDetachedGraceTerminatesProvider(t *testing.T) {
	withShortHandoverGrace(t, 150*time.Millisecond)
	hf := startQuiescedHandoverFixture(t, "attempt-takeover-grace")
	awaitHandoverConverge(t, hf.done)
	if _, err := readIdentity(hf.identity.PID); err == nil {
		t.Fatal("provider still running after the detached grace ceiling")
	}
	if hf.transport.Current != nil {
		t.Fatal("handover transport still references a control stream after grace termination")
	}
}

// TestTakeoverFencesAttachedOwnerBeforeAccepting is the double-owner window:
// the endpoint consumes the grant while the previous daemon is still
// attached, so the run must be fenced before the replacement is ever told it
// owns it. The accepted reply is written by the owner loop itself, after that
// fence, which makes reading the reply proof the old stream is already over —
// and a command the old daemon writes afterwards can never be executed.
func TestTakeoverFencesAttachedOwnerBeforeAccepting(t *testing.T) {
	withShortHandoverGrace(t, 2*time.Second)
	const attemptID = "attempt-takeover-attached"
	hf := startHandoverFixture(t, attemptID)
	token := readTakeoverToken(t, hf.f.root)

	conn, response := dialTakeover(t, hf.f.root, attemptID, token)
	if !response.Accepted {
		t.Fatalf("takeover of an attached owner rejected: %+v", response)
	}
	// The reply was written after the fence, so the old owner's end of the
	// story is already on its socket and needs no waiting.
	fenced, err := hf.old.Next(time.Second)
	if err != nil {
		t.Fatalf("old owner was still attached when the replacement was accepted: %v", err)
	}
	if fenced.Kind != AttemptHandoverQuiesced {
		t.Fatalf("old owner fence = %+v, want handover-quiesced", fenced)
	}
	var attached attemptFrame
	if err := readFrame(conn, &attached, maxConfigBytes); err != nil || attached.Kind != "handover-attached" || attached.Floor > attached.Head {
		t.Fatalf("attached frame=%+v err=%v", attached, err)
	}

	// A terminate on the old stream after the reply is not a command any
	// more: the runner closed that capability, and the provider it named
	// keeps running through the rest of its script.
	_ = hf.old.Terminate()
	if got, err := readIdentity(hf.identity.PID); err != nil || got != hf.identity {
		t.Fatalf("fenced owner still terminated the provider: identity=%+v err=%v", got, err)
	}
	if err := os.WriteFile(hf.continued, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFile(t, hf.after)
	if got, err := readIdentity(hf.identity.PID); err != nil || got != hf.identity {
		t.Fatalf("provider changed under the replacement: identity=%+v err=%v", got, err)
	}
	_ = conn.Close()
	awaitHandoverConverge(t, hf.done)
	output, _, err := hf.owner.ring.Read(hf.owner.ring.Floor())
	if err != nil || string(output) != "before-handover\r\nafter-handover\r\n" {
		t.Fatalf("ordered PTY output=%q err=%v", output, err)
	}
}

// TestTakeoverAdoptsUnderContinuousProviderOutput is the production failure
// of 17 Sep 2026: every worker's provider was a TUI that never stopped
// drawing, the replacement daemon's dial rotated the grant and then timed
// out unanswered, and the sweep concluded live-holder while the only idle
// provider, the overseer's, was adopted. The owner loop took replacements
// only on a kevent-timeout tick, which a continuously readable PTY never
// produces. A replacement must be answered within the daemon's 3s handshake
// budget under exactly that output, the provider and its output must carry
// across unchanged, and the detached grace must still converge the provider
// when no replacement stays.
func TestTakeoverAdoptsUnderContinuousProviderOutput(t *testing.T) {
	withShortHandoverGrace(t, 300*time.Millisecond)
	const attemptID = "attempt-takeover-busy"
	hf := startBusyHandoverFixture(t, attemptID)
	if err := hf.old.SendHandoverQuiesce(); err != nil {
		t.Fatal(err)
	}
	if quiesced, err := hf.old.Next(4 * time.Second); err != nil || quiesced.Kind != AttemptHandoverQuiesced {
		t.Fatalf("quiesced=%+v err=%v", quiesced, err)
	}
	token := readTakeoverToken(t, hf.f.root)
	started := time.Now()
	conn, response := dialTakeover(t, hf.f.root, attemptID, token)
	if !response.Accepted {
		t.Fatalf("takeover under continuous output rejected: %+v", response)
	}
	// The daemon's takeoverDialTimeout is 3s; a reply that needs the
	// provider to pause is a reply the daemon never sees.
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("takeover answered after %v, past the daemon's handshake budget", elapsed)
	}
	var attached attemptFrame
	if err := readFrame(conn, &attached, maxConfigBytes); err != nil || attached.Kind != "handover-attached" || attached.Floor > attached.Head {
		t.Fatalf("attached frame=%+v err=%v", attached, err)
	}
	if got, err := readIdentity(hf.identity.PID); err != nil || got != hf.identity {
		t.Fatalf("provider changed under the replacement: identity=%+v err=%v", got, err)
	}
	// Losing the replacement with nothing behind it leaves the run to the
	// detached grace, which must converge the provider under this output too.
	_ = conn.Close()
	awaitHandoverConverge(t, hf.done)
	if _, err := readIdentity(hf.identity.PID); err == nil {
		t.Fatal("busy provider still running after the detached grace ceiling")
	}
	// The ring kept filling past the adoption, in order, with no restart:
	// the numbered lines after the floor are consecutive.
	output, _, err := hf.owner.ring.Read(hf.owner.ring.Floor())
	if err != nil || hf.owner.ring.Head() <= attached.Head {
		t.Fatalf("ring after adoption: head %d (attached at %d) err=%v", hf.owner.ring.Head(), attached.Head, err)
	}
	lines := strings.Split(string(output), "\r\n")
	if len(lines) < 3 {
		t.Fatalf("retained output too short: %q", output)
	}
	expected := 0
	for _, line := range lines[1 : len(lines)-1] { // the first and last lines may be partial
		number, convErr := strconv.Atoi(strings.TrimPrefix(line, "line "))
		if convErr != nil || (expected != 0 && number != expected) {
			t.Fatalf("retained output broke at %q (want line %d): %v", line, expected, convErr)
		}
		expected = number + 1
	}
}
