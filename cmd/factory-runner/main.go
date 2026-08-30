package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

var errPrivateCapability = errors.New("factory-runner: exact private mode and inherited capabilities required")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errPrivateCapability) {
			os.Exit(64)
		}
		os.Exit(70)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errPrivateCapability
	}
	switch args[0] {
	case "--version":
		_, err := fmt.Fprintf(stdout, "factory-runner %s\n", buildinfo.Current().Version())
		return err
	case "--build-identity":
		return buildinfo.Current().WriteJSON(stdout)
	case "--exec-gate":
		if err := runner.RunExecGate(); err != nil {
			return fmt.Errorf("factory-runner: exec gate failed: %w", err)
		}
	case "--attempt-runner":
		if err := runner.RunAttemptRunner(); err != nil {
			return fmt.Errorf("factory-runner: attempt runner failed: %w", err)
		}
	case "--change-worker":
		if err := changeworker.Run(context.Background()); err != nil {
			return fmt.Errorf("factory-runner: Change worker failed: %w", err)
		}
	default:
		return errPrivateCapability
	}
	return nil
}
