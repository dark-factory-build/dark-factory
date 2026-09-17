//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	transport := &HandoverTransport{Replacements: replacements, Current: oldRunner}
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
	if err := finalOwner.Terminate(); err != nil {
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
	if transport.Current != finalRunner {
		t.Fatal("final result authority did not follow the replacement connection")
	}
	output, _, err := owner.ring.Read(owner.ring.Floor())
	if err != nil || string(output) != "before-handover\r\nafter-handover\r\n" {
		t.Fatalf("ordered PTY output=%q err=%v", output, err)
	}
	if err := transport.Current.Close(); err != nil {
		t.Fatal(err)
	}
}
