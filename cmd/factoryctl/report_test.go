package main

import (
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestReportWhyAndAgentSetParse(t *testing.T) {
	id := strings.Repeat("ab", 16)
	command, _, ok := parse([]string{"report", "--since", "7d", "--by", "day"})
	if !ok || command.kind != commandReport || command.report != (api.ReportInput{SinceSeconds: 7 * 86400, BucketSeconds: 86400}) {
		t.Fatalf("report = %+v, %v", command.report, ok)
	}
	command, _, ok = parse([]string{"attempt", "why", "--pull", "1651"})
	if !ok || !command.attemptRead || command.report != (api.ReportInput{Pull: 1651}) {
		t.Fatalf("attempt why = %+v, %v", command.report, ok)
	}
	command, _, ok = parse([]string{"agent", "set", "--agent", id, "--cadence", "30m", "--wake-on", "none", "--pause"})
	if !ok || command.kind != commandAgentSet || *command.agentSet.IdleAfterSeconds != 1800 || *command.agentSet.IdleWakeOn != "" || !*command.agentSet.Paused || command.agentSet.Model != nil {
		t.Fatalf("agent set = %+v, %v", command.agentSet, ok)
	}
	for _, args := range [][]string{
		{"why"},
		{"why", "--pull", "1", "--task", id},
		{"report", "--by", "30m"},
		{"report", "--since", "1h", "--by", "day"},
		{"agent", "set", "--agent", id},
		{"agent", "set", "--agent", id, "--pause", "--resume"},
		{"agent", "set", "--agent", id, "--wake-on", "everything"},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("%q parsed", args)
		}
	}
}
