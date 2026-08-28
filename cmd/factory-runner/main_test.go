package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDirectInvocationRequiresExactPrivateMode(t *testing.T) {
	for _, args := range [][]string{nil, {"--exec-gate", "extra"}, {"--attempt-runner", "extra"}, {"--change-worker-shell", "extra"}} {
		if err := run(args, &bytes.Buffer{}); !errors.Is(err, errPrivateCapability) {
			t.Fatalf("args=%q err=%v", args, err)
		}
	}
}

func TestVersionIsTheOnlyPublicRunnerInvocation(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--version"}, &output); err != nil || output.String() != "factory-runner development\n" {
		t.Fatalf("version = %q, %v", output.String(), err)
	}
}

func TestChangeWorkerModeRequiresInheritedCapabilitiesBeforeEffect(t *testing.T) {
	if os.Getenv("FACTORY_RUNNER_DIRECT_WORKER") == "1" {
		if err := run([]string{"--change-worker-shell"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Change worker failed") {
			t.Fatalf("direct worker error = %v", err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestChangeWorkerModeRequiresInheritedCapabilitiesBeforeEffect$")
	command.Env = append(os.Environ(), "FACTORY_RUNNER_DIRECT_WORKER=1")
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helper=%v output=%q", err, body)
	}
	if strings.Contains(string(body), "private") {
		t.Fatalf("private capability detail leaked: %q", body)
	}
}
