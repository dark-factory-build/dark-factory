package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDirectInvocationRequiresExactPrivateMode(t *testing.T) {
	for _, args := range [][]string{nil, {"--version"}, {"--exec-gate", "extra"}, {"--attempt-runner", "extra"}, {"--change-worker", "extra"}} {
		if err := run(args); !errors.Is(err, errPrivateCapability) {
			t.Fatalf("args=%q err=%v", args, err)
		}
	}
}

func TestChangeWorkerModeRequiresInheritedCapabilitiesBeforeEffect(t *testing.T) {
	if os.Getenv("FACTORY_RUNNER_DIRECT_WORKER") == "1" {
		if err := run([]string{"--change-worker"}); err == nil || !strings.Contains(err.Error(), "Change worker failed") {
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
