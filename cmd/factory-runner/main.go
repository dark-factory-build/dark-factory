package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

var errPrivateCapability = errors.New("factory-runner: private --exec-gate capability required")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errPrivateCapability) {
			os.Exit(64)
		}
		os.Exit(70)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "--exec-gate" {
		return errPrivateCapability
	}
	if err := runner.RunExecGate(); err != nil {
		return fmt.Errorf("factory-runner: exec gate failed: %w", err)
	}
	return nil
}
