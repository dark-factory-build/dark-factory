package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const exitRefused = 75

var (
	// releaseWaitLimit covers the build, the 10-minute drain, the restart
	// and the 5-minute trial; releasePoll is a package-test seam.
	releaseWaitLimit = 45 * time.Minute
	releasePoll      = 2 * time.Second
)

// runRelease asks factoryd to release a merged commit into its own service.
// The daemon restarts during the release, so --wait reads the durable record
// until it settles and treats an unreachable daemon as not settled yet.
func runRelease(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || len(args) > 2 || len(args) == 2 && args[1] != "--wait" {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}
	client, err := api.NewOperatorClient(getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE"))
	if err != nil {
		_, _ = io.WriteString(stderr, "factoryctl: operator client configuration is invalid\n")
		return exitFailure
	}
	return release(ctx, len(args) == 2, func(start bool) (kernel.ProductionDelivery, error) {
		callContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return client.Release(callContext, api.ReleaseInput{SHA: args[0], Start: start})
	}, stdout, stderr)
}

func release(ctx context.Context, wait bool, call func(start bool) (kernel.ProductionDelivery, error), stdout, stderr io.Writer) int {
	delivery, err := call(true)
	if err != nil {
		writeWebFailure(stderr, "release", err)
		return exitRefused
	}
	for deadline := time.Now().Add(releaseWaitLimit); wait && delivery.State == "running"; {
		if time.Now().After(deadline) {
			_, _ = io.WriteString(stderr, "factoryctl: release did not settle in time\n")
			break
		}
		select {
		case <-ctx.Done():
			return exitFailure
		case <-time.After(releasePoll):
		}
		if next, err := call(false); err == nil {
			delivery = next
		}
	}
	if err := json.NewEncoder(stdout).Encode(delivery); err != nil {
		return exitFailure
	}
	switch {
	case delivery.State == "verified" || !wait && delivery.State == "running":
		return 0
	case delivery.State == "failed" && (delivery.Phase == "build" || delivery.Phase == "drain"):
		// Nothing was swapped: the factory still runs the old build.
		return exitRefused
	default:
		return exitFailure
	}
}
