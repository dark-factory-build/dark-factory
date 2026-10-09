package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestReleaseWaitsAcrossTheRestartAndMapsTheOutcome(t *testing.T) {
	releasePoll = time.Millisecond
	running := kernel.ProductionDelivery{State: "running", Phase: "build"}
	for _, test := range []struct {
		name  string
		wait  bool
		reads []kernel.ProductionDelivery
		want  int
	}{
		{"started", false, nil, 0},
		{"verified", true, []kernel.ProductionDelivery{running, {}, {State: "verified"}}, 0},
		{"rolled back", true, []kernel.ProductionDelivery{{State: "failed", Reason: "crashed"}}, exitFailure},
		{"stage failed", true, []kernel.ProductionDelivery{{State: "failed", Phase: "stage"}}, exitRefused},
		{"trial failed", true, []kernel.ProductionDelivery{{State: "failed", Phase: "trial"}}, exitFailure},
		{"drain timeout", true, []kernel.ProductionDelivery{{State: "failed", Phase: "drain"}}, exitRefused},
		{"build failed", true, []kernel.ProductionDelivery{{State: "failed", Phase: "build"}}, exitRefused},
	} {
		reads := test.reads
		call := func(start bool) (kernel.ProductionDelivery, error) {
			if start {
				return running, nil
			}
			next := reads[0]
			reads = reads[1:]
			if next.State == "" {
				// The daemon is restarting.
				return next, errors.New("unavailable")
			}
			return next, nil
		}
		if got := release(context.Background(), true, test.wait, call, io.Discard, io.Discard); got != test.want {
			t.Errorf("%s: exit %d, want %d", test.name, got, test.want)
		}
	}
	refused := func(bool) (kernel.ProductionDelivery, error) {
		return kernel.ProductionDelivery{}, errors.New("conflict")
	}
	if got := release(context.Background(), true, true, refused, io.Discard, io.Discard); got != exitRefused {
		t.Errorf("refused start: exit %d", got)
	}
	// #1390: reading a release must never start one.
	read := func(start bool) (kernel.ProductionDelivery, error) {
		if start {
			t.Fatal("reading a release started it")
		}
		return kernel.ProductionDelivery{State: "verified"}, nil
	}
	if got := release(context.Background(), false, false, read, io.Discard, io.Discard); got != 0 {
		t.Errorf("read: exit %d", got)
	}
}

func TestReleaseFlagsParseInEitherOrder(t *testing.T) {
	for _, flags := range [][]string{{"--start", "--wait"}, {"--wait", "--start"}} {
		var stderr bytes.Buffer
		runRelease(context.Background(), append([]string{"sha"}, flags...), func(string) string { return "" }, io.Discard, &stderr)
		if strings.Contains(stderr.String(), "invalid arguments") {
			t.Errorf("%v: %s", flags, stderr.String())
		}
	}
}
