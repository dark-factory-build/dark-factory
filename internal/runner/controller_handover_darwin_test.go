//go:build darwin

package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControllerHandoverRecipient(t *testing.T) {
	raw := os.Getenv("RUNNER_HANDOVER_TEST_STATE")
	if raw == "" {
		return
	}
	var state AttemptControllerHandover
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(3, "transferred-controller")
	controller, err := AdoptAttemptController(file, state)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	// The old process sent this command but never read its reply. Adoption
	// consumes the original reply, without repeating the effectful command.
	event, err := controller.Next(4 * time.Second)
	if err != nil || event.Frame == nil || event.Frame.Kind != TerminalGenerationResult || event.Frame.Correlation != 1 {
		t.Fatalf("pending reply: %+v %v", event, err)
	}
	if err := controller.SendTerminalCommand(TerminalCommand{Kind: TerminalAttach, Correlation: 2, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	event, err = controller.Next(4 * time.Second)
	if err != nil || event.Frame == nil || event.Frame.Kind != TerminalAttached {
		t.Fatalf("attach: %+v %v", event, err)
	}
	if err := controller.SendTerminalCommand(TerminalCommand{Kind: TerminalCredit, Credit: 4096}); err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("RUNNER_HANDOVER_TEST_ROOT")
	if err := os.WriteFile(filepath.Join(root, "continue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	output := ""
	for !strings.Contains(output, "post-output") {
		event, err = controller.Next(4 * time.Second)
		if err != nil || event.Frame == nil {
			t.Fatalf("output: %+v %v", event, err)
		}
		if event.Frame.Kind == TerminalOutput {
			output += string(event.Frame.Payload)
		}
	}
	if !strings.Contains(output, "pre-output") || strings.Index(output, "pre-output") > strings.Index(output, "post-output") {
		t.Fatalf("lost or reordered output: %q", output)
	}
	if err := os.WriteFile(filepath.Join(root, "finish"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		event, err = controller.Next(6 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == AttemptResultReady {
			break
		}
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	// This helper intentionally consumes inherited fd3; the parent package FD
	// inventory assumes tests preserve their initial descriptor set.
	os.Exit(0)
}

func TestAttemptControllerHandoverPreservesProviderAcrossProcesses(t *testing.T) {
	f := newAttemptFixture(t, "shell", "")
	f.activateOuter()
	f.advanceToProvider()
	if err := f.controller.Release(StageProvider); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(f.root, "provider.pid"))
	originalPID, err := os.ReadFile(filepath.Join(f.root, "provider.pid"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := f.controller.Next(4 * time.Second)
	if err != nil || event.Frame == nil || event.Frame.Kind != TerminalReady {
		t.Fatalf("ready: %+v %v", event, err)
	}
	if err := f.controller.SendTerminalCommand(TerminalCommand{Kind: TerminalGenerationInstall, Correlation: 1, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	old := f.controller
	file, state, err := old.DetachForHandover()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := old.Terminate(); !errors.Is(err, ErrState) {
		t.Fatalf("stale control: %v", err)
	}
	if _, _, err := old.DetachForHandover(); !errors.Is(err, ErrState) {
		t.Fatalf("duplicate transfer: %v", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerHandoverRecipient$")
	cmd.Env = append(os.Environ(), "RUNNER_HANDOVER_TEST_STATE="+string(raw), "RUNNER_HANDOVER_TEST_ROOT="+f.root)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.ExtraFiles = []*os.File{file}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Only the replacement process now owns the transferred endpoint.
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("recipient: %v: %s", err, output.String())
	}
	pid, err := os.ReadFile(filepath.Join(f.root, "provider.pid"))
	if err != nil || string(pid) != string(originalPID) {
		t.Fatalf("provider changed: %q %q %v", originalPID, pid, err)
	}
	effect, err := os.ReadFile(filepath.Join(f.root, "provider.effect"))
	if err != nil || string(effect) != "x" {
		t.Fatalf("effect replayed: %q %v", effect, err)
	}
	if _, err := f.outer.FinishAfterExit(6 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptControllerHandoverRefusesBusyOrPartialRead(t *testing.T) {
	c, peer, err := NewAttemptController()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer peer.Close()
	c.state = controllerProviderReleased
	c.terminalReady = true
	// Hold Next inside a real partial-frame read, then race transfer against it.
	if _, err := peer.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Next(200 * time.Millisecond); done <- err }()
	deadline := time.Now().Add(time.Second)
	for c.operation.TryLock() {
		c.operation.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("read did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := c.DetachForHandover(); !errors.Is(err, ErrState) {
		t.Fatalf("busy transfer: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("expected partial-frame timeout")
	}

	if _, _, err := c.DetachForHandover(); !errors.Is(err, ErrState) {
		t.Fatalf("partial transfer: %v", err)
	}
}
