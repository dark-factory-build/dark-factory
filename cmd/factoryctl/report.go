package main

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// parseReport reads report [--project ID] [--since DURATION] [--by hour|half-day|day|DURATION]
// and why --pull N | --task ID [--project ID].
func parseReport(args []string) (attemptCommand, bool, bool) {
	c := attemptCommand{kind: commandReport}
	why := args[0] == "why"
	if !why {
		c.report.SinceSeconds, c.report.BucketSeconds = 86400, 43200
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) || seen[args[i]] {
			return attemptCommand{}, false, false
		}
		seen[args[i]] = true
		name, value := args[i], args[i+1]
		seconds, isSpan := reportSpan(value)
		count, isCount := parseCount(value, false)
		switch {
		case name == "--project" && validHumanRequestKey(value):
			c.report.ProjectID = value
		case !why && name == "--since" && isSpan:
			c.report.SinceSeconds = seconds
		case !why && name == "--by" && isSpan:
			c.report.BucketSeconds = seconds
		case why && name == "--pull" && isCount:
			c.report.Pull = count
		case why && name == "--task" && validHumanRequestKey(value):
			c.report.TaskID = value
		default:
			return attemptCommand{}, false, false
		}
	}
	if why && (c.report.Pull == 0) == (c.report.TaskID == "") {
		return attemptCommand{}, false, false
	}
	return c, false, why || c.report.BucketSeconds >= 3600 && c.report.BucketSeconds <= c.report.SinceSeconds && c.report.SinceSeconds <= 366*86400
}

// reportSpan is a whole number of seconds: hour, half-day, day, a Go
// duration, or N days as Nd.
func reportSpan(value string) (uint64, bool) {
	switch value {
	case "hour":
		return 3600, true
	case "half-day":
		return 43200, true
	case "day":
		return 86400, true
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.ParseUint(days, 10, 16)
		return n * 86400, err == nil && n > 0
	}
	span, err := time.ParseDuration(value)
	return uint64(span / time.Second), err == nil && span >= time.Second
}

func runReport(ctx context.Context, client *api.OperatorClient, command attemptCommand, stdout, stderr io.Writer) int {
	report, err := client.Report(ctx, command.report)
	if err != nil {
		return writeWebFailure(stderr, "report", err)
	}
	for _, line := range report.Lines {
		_, _ = io.WriteString(stdout, line+"\n")
	}
	return 0
}

// parseAgentSet reads agent set --agent ID [--cadence DURATION] [--wake-on
// failures|merges|failures,merges|none] [--model TEXT [--reasoning-effort E]]
// [--pause|--resume].
func parseAgentSet(args []string) (attemptCommand, bool, bool) {
	c := attemptCommand{kind: commandAgentSet}
	var update api.OverseerAgentUpdateInput
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if seen[name] || (name == "--pause" || name == "--resume") && update.Paused != nil {
			return attemptCommand{}, false, false
		}
		seen[name] = true
		if name == "--pause" || name == "--resume" {
			paused := name == "--pause"
			update.Paused = &paused
			continue
		}
		if i+1 >= len(args) {
			return attemptCommand{}, false, false
		}
		i++
		value := args[i]
		switch name {
		case "--agent":
			c.agent = value
		case "--cadence":
			seconds, ok := reportSpan(value)
			if !ok || seconds > 604800 {
				return attemptCommand{}, false, false
			}
			cadence := uint32(seconds)
			update.IdleAfterSeconds = &cadence
		case "--wake-on":
			if value == "none" {
				value = ""
			} else if value != "failures" && value != "merges" && value != "failures,merges" {
				return attemptCommand{}, false, false
			}
			update.IdleWakeOn = &value
		case "--model":
			update.Model = &value
		case "--reasoning-effort":
			update.ReasoningEffort = &value
		default:
			return attemptCommand{}, false, false
		}
	}
	update.AgentID = c.agent
	c.agentSet = update
	return c, false, validHumanRequestKey(c.agent) && (update.Paused != nil || update.IdleAfterSeconds != nil || update.IdleWakeOn != nil || update.Model != nil || update.ReasoningEffort != nil)
}

// runAgentSet writes the changed fields at the agent's current revision. A
// transport failure or a concurrent edit rereads and writes again, so the
// operator never retries by hand.
func runAgentSet(ctx context.Context, client *api.OperatorClient, command attemptCommand, stdout, stderr io.Writer) int {
	var err error
	for range 3 {
		var snapshot api.DashboardSnapshot
		if snapshot, err = client.Snapshot(ctx); err != nil {
			if retryableAgentSet(err) {
				continue
			}
			break
		}
		err = errNoAgent
		for _, agent := range snapshot.Agents {
			if agent.ID == command.agent {
				update := command.agentSet
				update.ExpectedRevision = agent.Revision
				var result api.MutationResult
				if result, err = client.UpdateAgent(ctx, update); err == nil {
					return writeJSON(stdout, result)
				}
			}
		}
		if !retryableAgentSet(err) {
			break
		}
	}
	return writeWebFailure(stderr, "agent set", err)
}

var errNoAgent = errors.New("factoryctl: no such agent")

func retryableAgentSet(err error) bool {
	var remote *api.RemoteError
	if errors.As(err, &remote) {
		return remote.Code() == api.RemoteUnavailable || remote.Code() == api.RemoteRevisionConflict
	}
	return err != errNoAgent
}
