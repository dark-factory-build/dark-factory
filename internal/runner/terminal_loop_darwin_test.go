//go:build darwin

package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func readOwnerFrame(t *testing.T, peer *os.File) TerminalFrame {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw attemptFrame
	if err := readFrame(peer, &raw, maxFrameBytes); err != nil {
		t.Fatal(err)
	}
	frame, err := terminalEventFromFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func fillOwnerSocket(t *testing.T, file *os.File) {
	t.Helper()
	if err := unix.SetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, unix.SO_SNDBUF, 1024); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, 4096)
	for {
		_, err := unix.Write(int(file.Fd()), filler)
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminalOwnerReplayUsesCorrelationWithoutMovingLiveCursor(t *testing.T) {
	daemon, peer, err := newControlPair("terminal-owner", "terminal-peer")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	defer daemon.Close()
	owner := &terminalOwner{daemon: daemon, daemonOpen: true, credit: 1024, ring: &terminalByteRing{}}
	if err := owner.ring.Append([]byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := owner.attach(TerminalCommand{Kind: TerminalAttach, Correlation: 11, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalAttached || got.Correlation != 11 {
		t.Fatalf("first attach = %+v", got)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalOutput || got.Correlation != 11 || got.Start != 0 || got.End != 3 || string(got.Payload) != "old" {
		t.Fatalf("first replay = %+v", got)
	}
	if owner.sent != 3 {
		t.Fatalf("live cursor after first attach = %d, want 3", owner.sent)
	}
	if err := owner.ring.Append([]byte("live")); err != nil {
		t.Fatal(err)
	}
	if err := owner.flush(); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalOutput || got.Correlation != 0 || got.Start != 3 || got.End != 7 || string(got.Payload) != "live" {
		t.Fatalf("live output = %+v", got)
	}
	if err := owner.attach(TerminalCommand{Kind: TerminalAttach, Correlation: 22, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalAttached || got.Correlation != 22 {
		t.Fatalf("second attach = %+v", got)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalOutput || got.Correlation != 22 || got.Start != 0 || got.End != 7 || string(got.Payload) != "oldlive" {
		t.Fatalf("second replay = %+v", got)
	}
	if owner.sent != 7 {
		t.Fatalf("live cursor after second attach = %d, want 7", owner.sent)
	}
}

func TestTerminalOwnerReplaysRetainedOutputAfterPTYEOF(t *testing.T) {
	daemon, peer, err := newControlPair("terminal-owner-eof", "terminal-peer-eof")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	defer daemon.Close()
	owner := &terminalOwner{daemon: daemon, daemonOpen: true, credit: 0, ptyDrained: true, ring: &terminalByteRing{}}
	if err := owner.ring.Append([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	if err := owner.emitPTYEOF(); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalPTYEOF {
		t.Fatalf("EOF frame = %+v", got)
	}
	if err := owner.attach(TerminalCommand{Kind: TerminalAttach, Correlation: 17, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalAttached || got.Correlation != 17 || got.Head != 8 {
		t.Fatalf("post-EOF attach = %+v", got)
	}
	if err := owner.command(terminalCommandFrame(TerminalCommand{Kind: TerminalCredit, Credit: 64})); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalOutput || got.Correlation != 17 || got.Start != 0 || got.End != 8 || string(got.Payload) != "retained" {
		t.Fatalf("post-EOF replay = %+v", got)
	}
	if len(owner.replay) != 0 {
		t.Fatalf("replay request remained after head: %+v", owner.replay)
	}
	// Once replay is complete, EOF must not produce a new uncorrelated stream.
	if err := owner.command(terminalCommandFrame(TerminalCommand{Kind: TerminalCredit, Credit: 64})); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var raw attemptFrame
	if err := readFrame(peer, &raw, maxFrameBytes); err == nil {
		t.Fatal("post-EOF flush emitted unsolicited output")
	}
}

func TestTerminalOwnerPostEOFStaleAttachResets(t *testing.T) {
	daemon, peer, err := newControlPair("terminal-owner-stale-eof", "terminal-peer-stale-eof")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	defer daemon.Close()
	owner := &terminalOwner{daemon: daemon, daemonOpen: true, ptyDrained: true, ring: &terminalByteRing{}}
	if err := owner.ring.Append(bytes.Repeat([]byte{'x'}, terminalReplayCapacity+1)); err != nil {
		t.Fatal(err)
	}
	if err := owner.emitPTYEOF(); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalPTYEOF {
		t.Fatalf("EOF frame = %+v", got)
	}
	if err := owner.attach(TerminalCommand{Kind: TerminalAttach, Correlation: 23, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	got := readOwnerFrame(t, peer)
	if got.Kind != TerminalReset || got.Correlation != 23 || got.Floor != 1 || got.Head != terminalReplayCapacity+1 {
		t.Fatalf("post-EOF stale reset = %+v", got)
	}
}

func TestTerminalOwnerHumanReplyIsRejectedAfterEOFWithoutPTYEffect(t *testing.T) {
	daemon, peer, err := newControlPair("terminal-owner-reply-eof", "terminal-peer-reply-eof")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	defer daemon.Close()
	owner := &terminalOwner{daemon: daemon, daemonOpen: true, ptyDrained: true, ring: &terminalByteRing{}}
	if err := owner.emitPTYEOF(); err != nil {
		t.Fatal(err)
	}
	if got := readOwnerFrame(t, peer); got.Kind != TerminalPTYEOF {
		t.Fatalf("EOF frame = %+v", got)
	}
	command := TerminalCommand{Kind: TerminalHumanReply, Correlation: 31, Payload: []byte("reply")}
	if err := owner.command(terminalCommandFrame(command)); err != nil {
		t.Fatal(err)
	}
	got := readOwnerFrame(t, peer)
	if got.Kind != TerminalHumanReplyResult || got.Correlation != command.Correlation || got.Status != TerminalResultRejected || got.Count != 0 || got.Generation != 0 || got.Sequence != 0 {
		t.Fatalf("post-EOF reply result = %+v", got)
	}
}

func TestTerminalPayloadResultPreservesExactWriteOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		written int
		err     error
		status  TerminalResultStatus
	}{
		{name: "complete", written: 5, status: TerminalResultOK},
		{name: "partial", written: 2, err: io.ErrUnexpectedEOF, status: TerminalResultPartial},
		{name: "uncertain", written: 0, err: os.ErrDeadlineExceeded, status: TerminalResultUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, status := terminalPayloadResult(test.written, 5, test.err)
			if count != uint32(test.written) || status != test.status {
				t.Fatalf("result=(%d,%q), want=(%d,%q)", count, status, test.written, test.status)
			}
		})
	}
}

func TestTerminalOwnerWriteFailurePoisonsDaemonCapability(t *testing.T) {
	daemon, peer, err := newControlPair("terminal-owner", "terminal-peer")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	owner := &terminalOwner{daemon: daemon, daemonOpen: true, ring: &terminalByteRing{}}
	fillOwnerSocket(t, daemon)
	err = owner.send(TerminalFrame{Kind: TerminalReady})
	if err == nil {
		t.Fatal("saturated owner write unexpectedly succeeded")
	}
	if owner.daemonOpen || owner.daemon != nil {
		t.Fatalf("failed owner writer remained open: open=%t daemon=%v", owner.daemonOpen, owner.daemon)
	}
	if err := owner.send(TerminalFrame{Kind: TerminalReady}); !errors.Is(err, io.EOF) {
		t.Fatalf("send after poison = %v", err)
	}
}

func TestRetireReadableFilterReturnsVisibleUnresolvedWithinBound(t *testing.T) {
	reads := &attemptReadSet{
		daemonRegistered: true,
		testUnregister:   func(int) error { return syscall.EPERM },
	}
	started := time.Now()
	err := retireReadableFilter(reads.removeDaemon)
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("permanent filter error = %v, want ErrUnresolved", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("filter retirement exceeded bound: %s", elapsed)
	}
	if !reads.daemonRegistered {
		t.Fatal("failed retirement falsely cleared durable filter ownership")
	}
}

func TestTerminalOwnerCannotEmitEOFBeforePTYDrain(t *testing.T) {
	owner := &terminalOwner{daemonOpen: false, ring: &terminalByteRing{}}
	if err := owner.emitPTYEOF(); !errors.Is(err, ErrState) {
		t.Fatalf("EOF before PTY drain = %v, want ErrState", err)
	}
	if owner.ptyEOF {
		t.Fatal("rejected EOF transition changed owner state")
	}
}
