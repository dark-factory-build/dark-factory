//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestParseExactOperatorCommands(t *testing.T) {
	id := strings.Repeat("ab", 16)
	valid := []struct {
		name string
		args []string
	}{
		{name: "debug profile", args: []string{"debug", "profile", "--seconds", "30"}},
		{name: "select model", args: []string{"agent", "select-model", "--agent", id, "--revision", "7", "--model", "gpt-5.6-luna"}},
		{name: "project create", args: []string{"project", "create", "--name", "North Workshop", "--root", "/private/tmp/repo"}},
		{name: "agent create shell default role", args: []string{"agent", "create", "--project", id, "--name", "Builder One", "--provider", "shell"}},
		{name: "agent create codex controls", args: []string{"agent", "create", "--project", id, "--name", "Foreman", "--provider", "codex", "--model", "gpt-5.6-luna", "--reasoning-effort", "medium", "--role", "orchestrator"}},
		{name: "agent idle policy", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "standing_instruction", "--after-seconds", "60", "--instruction", "review retained changes"}},
		{name: "agent idle policy legacy zero budget", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "standing_instruction", "--after-seconds", "60", "--instruction", "legacy scope"}},
		{name: "agent idle wait", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "wait"}},
		{name: "specialist wake-on", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "standing_instruction", "--after-seconds", "60", "--instruction", "review", "--wake-on", "failures,merges"}},
		{name: "account discover", args: []string{"account", "discover"}},
		{name: "account list", args: []string{"account", "list"}},
		{name: "account link", args: []string{"account", "link", "--provider", "codex", "--home", "/Users/operator/.codex-dogfood", "--label", "dogfood"}},
		{name: "account update", args: []string{"account", "update", "--account", id, "--revision", "7", "--label", "renamed"}},
		{name: "attachment retention", args: []string{"attachment", "retention", "--enabled", "false"}},
		{name: "telemetry ingest", args: []string{"telemetry", "ingest", "--action", "status"}},
		{name: "agent select account", args: []string{"agent", "select-account", "--agent", id, "--revision", "7", "--account", strings.Repeat("cd", 16)}},
		{name: "agent select model", args: []string{"agent", "select-model", "--agent", id, "--revision", "7", "--model", "gpt-5.6-luna", "--reasoning-effort", "medium"}},
		{name: "task add minimal", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "Tighten the queue ordering"}},
		{name: "task add full", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "t", "--body", "b", "--priority", "-5"}},
		{name: "task add supplied identities", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "t", "--task-id", id, "--incarnation-id", strings.Repeat("cd", 16)}},
		{name: "task add any eligible worker", args: []string{"task", "add", "--project", id, "--agent", "any", "--title", "t"}},
		{name: "task add with files", args: []string{"task", "add", "--project", id, "--agent", "any", "--title", "t", "--file", "notes.txt,/private/shot.png"}},
		{name: "status", args: []string{"status"}},
		{name: "project oversight", args: []string{"status", "--project", id}},
		{name: "task send back", args: []string{"task", "send-back", "--task", id, "--note", "five findings"}},
		{name: "dispatch on", args: []string{"dispatch", "on"}},
		{name: "dispatch off", args: []string{"dispatch", "off"}},
		{name: "dispatch guarded", args: []string{"dispatch", "on", "--revision", "7"}},
		{name: "worker capacity", args: []string{"capacity", "--workers", "2", "--revision", "7"}},
		{name: "worker stop", args: []string{"worker", "stop", "--operation-id", id, "--task", id, "--task-revision", "2", "--run", id, "--run-revision", "3"}},
		{name: "worker replace", args: []string{"worker", "replace", "--operation-id", id, "--task", id, "--task-revision", "2", "--run", id, "--run-revision", "3", "--successor-task", strings.Repeat("cd", 16), "--successor-incarnation", strings.Repeat("ef", 16), "--instruction", "continue"}},
		{name: "worker message", args: []string{"worker", "message", "--operation-id", id, "--task", id, "--task-revision", "2", "--run", id, "--run-revision", "3", "--message", "continue"}},
		{name: "worker interrupt", args: []string{"worker", "interrupt", "--operation-id", id, "--task", id, "--task-revision", "2", "--run", id, "--run-revision", "3"}},
		{name: "worker operation", args: []string{"worker", "operation", "--operation-id", id}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			command, help, ok := parse(test.args)
			if help || !ok {
				t.Fatalf("valid operator command rejected: %+v help=%v ok=%v", command, help, ok)
			}
		})
	}
	if command, _, _ := parse(valid[1].args); command.role != "worker" {
		t.Fatalf("default role = %q", command.role)
	}
	// `--agent any` is the CLI spelling of the wire's empty assigned agent.
	if command, _, _ := parse([]string{"task", "add", "--project", id, "--agent", "any", "--title", "t"}); command.agent != "any" || anyWorkerAgent(command.agent) != "" || anyWorkerAgent(id) != id {
		t.Fatalf("any-worker task add = %+v", command)
	}
	if _, _, ok := parse([]string{"agent", "select-model", "--agent", "any", "--revision", "7", "--model", "m"}); ok {
		t.Fatal("`any` accepted where one agent is required")
	}
	if command, _, ok := parse([]string{"attachment", "retention"}); !ok || command.retentionEnabled != nil {
		t.Fatalf("retention inspection command = %+v, ok=%v", command, ok)
	}
	invalid := [][]string{
		{"project", "create", "--name", "n"},
		{"project", "create", "--root", "/private/tmp/repo"},
		{"project", "create", "--name", "n", "--root", "relative"},
		{"project", "create", "--name", "n", "--root", "/r", "--name", "n"},
		{"project", "create", "--name", "n", "--root", "/r", "--project", id},
		{"agent", "create", "--project", id, "--name", "n"},
		{"agent", "create", "--project", id, "--name", "n"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "unknown"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--model", "model"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--reasoning-effort", "medium"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "codex", "--reasoning-effort", "extreme"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "claude_code", "--reasoning-effort", "ultra"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "codex", "--model", strings.Repeat("m", 129)},
		{"agent", "create", "--project", "short", "--name", "n", "--provider", "shell"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--role", "manager"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "wait", "--wake-on", "failures"},
		{"account", "link", "--provider", "shell", "--home", "/Users/operator/.shell", "--label", "shell"},
		{"account", "link", "--provider", "codex", "--home", "relative", "--label", "codex"},
		{"agent", "select-account", "--agent", id, "--revision", "0", "--account", id},
		{"agent", "select-model", "--agent", id, "--revision", "0", "--model", "gpt-5.6-luna"},
		{"agent", "select-model", "--agent", id, "--revision", "1", "--model", ""},
		{"agent", "select-model", "--agent", id, "--revision", "1", "--model", "gpt-5.6-luna", "--reasoning-effort", "extreme"},
		{"task", "add", "--project", id, "--title", "t"},
		{"task", "add", "--project", id, "--agent", id},
		{"task", "add", "--project", id, "--agent", id, "--title", ""},
		{"task", "add", "--project", id, "--agent", id, "--title", "t", "--priority", "1000001"},
		{"task", "add", "--project", id, "--agent", id, "--title", "t", "--priority", "+1"},
		{"task", "add", "--project", id, "--agent", id, "--title", "t", "--task-id", id},
		{"task", "send-back", "--task", id},
		{"task", "send-back", "--note", "n"},
		{"task", "send-back", "--task", id, "--note", ""},
		{"task", "send-back", "--task", "short", "--note", "n"},
		{"task", "send-back", "--task", id, "--note", strings.Repeat("n", 8193)},
		{"status", "--project"},
		{"status", "--project", "short"},
		{"dispatch", "toggle"},
		{"dispatch", "on", "--revision", "0"},
		{"dispatch", "on", "--revision", "01"},
		{"dispatch"},
		{"capacity", "--workers", "0", "--revision", "1"},
		{"capacity", "--workers", "1025", "--revision", "1"},
		{"capacity", "--workers", "2"},
		{"capacity", "--workers", "2", "--revision", "0"},
		{"project", "make", "--name", "n", "--root", "/r"},
		{"debug", "profile", "--seconds", "4294967296"},
	}
	for _, args := range invalid {
		t.Run("invalid "+strings.Join(args, " "), func(t *testing.T) {
			if command, help, ok := parse(args); help || ok {
				t.Fatalf("invalid operator command accepted: %+v", command)
			}
		})
	}
	for _, args := range [][]string{{"project", "--help"}, {"project", "create", "--help"}, {"task", "add", "-h"}, {"dispatch", "--help"}} {
		if _, help, ok := parse(args); !help || !ok {
			t.Fatalf("help form rejected: %v", args)
		}
	}
}

func TestAttachmentRetentionCLIInspectionPreservesOmittedEnabled(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		enabled, present, ok := call.AttachmentRetention()
		if !ok || present || enabled {
			t.Errorf("inspection request enabled=%v present=%v ok=%v", enabled, present, ok)
		}
		return api.NewContentReply(api.AttachmentRetentionResult{Enabled: false})
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"attachment", "retention"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"enabled":false`) {
		t.Fatalf("retention inspection = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestDebugProfileCLIUsesOperatorAPIAndExtendedTimeout(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.ProfileInput()
		if !ok || input.Seconds != 300 {
			t.Errorf("profile call = kind %v input %+v ok=%v", call.Kind(), input, ok)
		}
		return api.NewContentReply(api.ProfileResult{CPUPath: "/home/cpu", HeapPath: "/home/heap", GoroutinePath: "/home/goroutine", Seconds: input.Seconds})
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"debug", "profile", "--seconds", "300"}, webEnvironment(fixture), &stdout, &stderr)
	result := awaitServer(t, done)
	if result.err != nil || exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"seconds":300`) {
		t.Fatalf("debug profile = exit %d stdout %q stderr %q server=%v", exit, stdout.String(), stderr.String(), result.err)
	}
	command, help, ok := parse([]string{"debug", "profile", "--seconds", "300"})
	if !ok || help || operatorRequestTimeout(command) != 330*time.Second {
		t.Fatalf("profile timeout = %+v help=%t ok=%t timeout=%s", command, help, ok, operatorRequestTimeout(command))
	}
}

func TestWorkerStopUsesOperatorClient(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := strings.Repeat("ab", 16)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.OverseerRunStopInput()
		if !ok || call.Kind() != api.CallOperatorStopRun || input.OperationID != id || input.ExpectedTaskRevision != 2 || input.ExpectedRunRevision != 3 {
			t.Errorf("operator stop call = kind %v input %+v ok=%v", call.Kind(), input, ok)
		}
		reply, err := api.NewMutationReply(api.MutationResult{Head: 4, Revision: 5, Intervention: &api.OverseerInterventionResult{OperationID: id, State: "delivered"}})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"worker", "stop", "--operation-id", id, "--task", id, "--task-revision", "2", "--run", id, "--run-revision", "3"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"state":"delivered"`) {
		t.Fatalf("worker stop = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestWorkerMessageRejectsMalformedOperationIDWithoutClientEffect(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookups := 0
	exit := run(context.Background(), []string{"worker", "message", "--operation-id", "bad", "--task", strings.Repeat("ab", 16), "--task-revision", "2", "--run", strings.Repeat("cd", 16), "--run-revision", "3", "--message", "continue"}, func(string) string {
		lookups++
		return "/private/should-not-be-read"
	}, &stdout, &stderr)
	if exit != exitUsage || lookups != 0 || stdout.Len() != 0 || stderr.String() != "factoryctl worker message: invalid arguments\nusage: factoryctl worker message --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION --message TEXT\n" {
		t.Fatalf("worker message validation = exit %d lookups %d stdout %q stderr %q", exit, lookups, stdout.String(), stderr.String())
	}
}

func TestWorkerOperationUsesReadOnlyOperatorCall(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := strings.Repeat("ab", 16)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.WorkerOperationInput()
		if !ok || call.Kind() != api.CallOperatorWorkerOperation || input.OperationID != id {
			t.Errorf("operator receipt call = kind %v input %+v ok=%v", call.Kind(), input, ok)
		}
		reply, err := api.NewWorkerOperationReply(api.WorkerOperation{OperationID: id, TaskID: strings.Repeat("cd", 16), RunID: strings.Repeat("ef", 16), State: "unknown", Detail: "delivery uncertain"})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"worker", "operation", "--operation-id", id}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"state":"unknown"`) || !strings.Contains(stdout.String(), `"detail":"delivery uncertain"`) {
		t.Fatalf("worker operation = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestParseOperatorTaskAndAgentControls(t *testing.T) {
	id := strings.Repeat("1", 32)
	task, help, ok := parse([]string{"task", "update", "--task", id, "--revision", "7", "--retry"})
	if !ok || help || !task.operatorControl || task.kind != commandOverseerTaskUpdate || !task.retry {
		t.Fatalf("task control = %+v, help=%t ok=%t", task, help, ok)
	}
	agent, help, ok := parse([]string{"agent", "pause", "--agent", id, "--revision", "8"})
	if !ok || help || !agent.operatorControl || agent.kind != commandOverseerAgentUpdate || !agent.paused {
		t.Fatalf("agent control = %+v, help=%t ok=%t", agent, help, ok)
	}
}

func TestParseOperatorTaskRead(t *testing.T) {
	id := strings.Repeat("2", 32)
	command, help, ok := parse([]string{"task", "read", "--task", id, "--revision", "7", "--offset", "2048"})
	if !ok || help || command.kind != commandTaskRead || command.id != id || command.expectedRevision != 7 || command.offset != 2048 {
		t.Fatalf("task read = %+v, help=%t ok=%t", command, help, ok)
	}
}

func TestOperatorObservationCommandsUseOperatorAuthority(t *testing.T) {
	id := strings.Repeat("1", 32)
	for _, args := range [][]string{
		{"agent", "paths", "--agent", id},
		{"terminal", "observe", "--project", id, "--task", id, "--run", id},
	} {
		var stdout, stderr bytes.Buffer
		exit := run(context.Background(), args, func(string) string { return "" }, &stdout, &stderr)
		if exit != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "operator client configuration is invalid") {
			t.Fatalf("%v routed incorrectly: exit %d stdout %q stderr %q", args, exit, stdout.String(), stderr.String())
		}
	}
}

func TestOperatorCommandsRequireExactEnvironmentBeforeDialing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"dispatch", "on"}, func(string) string { return "" }, &stdout, &stderr)
	if exit != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "operator client configuration is invalid") {
		t.Fatalf("missing environment = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

// An absent socket under its home is a daemon that is down or restarting,
// not a misconfigured shell (#1830).
func TestOperatorCommandReportsAbsentSocketAsDaemonDown(t *testing.T) {
	directory, err := os.MkdirTemp(runner.ShortTempDir(), "dark-factory-factoryctl-down-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	home := filepath.Join(directory, "home")
	if _, err := install.Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(home, "operator.token")
	writeToken(t, token, bytes.Repeat([]byte{'O'}, 32))
	environment := map[string]string{"DARK_FACTORY_SOCKET": install.LocalAPISocketPath(home), "DARK_FACTORY_OPERATOR_TOKEN_FILE": token}
	for _, args := range [][]string{{"dispatch", "on"}, {"release", strings.Repeat("a", 40)}} {
		var stdout, stderr bytes.Buffer
		exit := run(context.Background(), args, func(name string) string { return environment[name] }, &stdout, &stderr)
		if exit != exitFailure || stdout.Len() != 0 || stderr.String() != "factoryctl: factoryd is not running or is restarting\n" {
			t.Fatalf("%v with absent socket = exit %d stdout %q stderr %q", args, exit, stdout.String(), stderr.String())
		}
	}
}

func TestProjectCreateMintsIdentityAndReportsResult(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	var received api.CreateProjectInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.CreateProjectInput()
		if !ok {
			t.Errorf("call kind = %v", call.Kind())
		}
		received = input
		reply, err := api.NewMutationReply(api.MutationResult{Head: 7, Revision: 3})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"project", "create", "--name", "North Workshop", "--root", "/private/tmp/repo"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("project create = exit %d stderr %q", exit, stderr.String())
	}
	if received.Name != "North Workshop" || received.Root != "/private/tmp/repo" || !validHumanRequestKey(received.ID) {
		t.Fatalf("daemon received %+v", received)
	}
	var printed struct {
		ID       string `json:"id"`
		Head     uint64 `json:"head"`
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil || printed.ID != received.ID || printed.Head != 7 || printed.Revision != 3 {
		t.Fatalf("printed %q parsed %+v err %v", stdout.String(), printed, err)
	}
}

func TestAgentCreateCarriesProviderControls(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	projectID := strings.Repeat("11", 16)
	var received api.CreateAgentInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.CreateAgentInput()
		if !ok {
			t.Errorf("call kind = %v", call.Kind())
		}
		received = input
		reply, err := api.NewMutationReply(api.MutationResult{Head: 8, Revision: 1})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{
		"agent", "create", "--project", projectID, "--name", "Builder", "--provider", "codex",
		"--model", "gpt-5.6-luna", "--reasoning-effort", "medium", "--role", "orchestrator",
	}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("agent create = exit %d stderr %q", exit, stderr.String())
	}
	if received.ProjectID != projectID || received.Name != "Builder" || received.Role != "orchestrator" || received.Provider != "codex" || received.Model != "gpt-5.6-luna" || received.ReasoningEffort != "medium" || !validHumanRequestKey(received.ID) {
		t.Fatalf("daemon received %+v", received)
	}
	if !strings.Contains(stdout.String(), received.ID) || !strings.Contains(stdout.String(), `"head":8`) || !strings.Contains(stdout.String(), `"revision":1`) {
		t.Fatalf("printed %q", stdout.String())
	}
}

func TestAgentIdlePolicyUsesTheOperatorAPI(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	agentID := strings.Repeat("22", 16)
	var received api.AgentIdlePolicyInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		var ok bool
		received, ok = call.AgentIdlePolicyInput()
		if !ok || call.Kind() != api.CallAgentIdlePolicy {
			t.Errorf("call = %v, input=%+v, ok=%v", call.Kind(), received, ok)
		}
		reply, err := api.NewMutationReply(api.MutationResult{Head: 8, Revision: 3})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"agent", "idle-policy", "--agent", agentID, "--revision", "2", "--policy", "standing_instruction", "--after-seconds", "60", "--instruction", "review retained changes"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || received != (api.AgentIdlePolicyInput{AgentID: agentID, ExpectedRevision: 2, Policy: "standing_instruction", AfterSeconds: 60, Instruction: "review retained changes"}) || !strings.Contains(stdout.String(), `"revision":3`) {
		t.Fatalf("idle policy = exit %d input=%+v stdout=%q stderr=%q", exit, received, stdout.String(), stderr.String())
	}
}

func TestTaskAddMintsDistinctTaskAndIncarnationIdentities(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	projectID := strings.Repeat("11", 16)
	agentID := strings.Repeat("22", 16)
	var received api.EnqueueTaskInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.EnqueueTaskInput()
		if !ok {
			t.Errorf("call kind = %v", call.Kind())
		}
		received = input
		reply, err := api.NewMutationReply(api.MutationResult{Head: 9, Revision: 1})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"task", "add", "--project", projectID, "--agent", agentID, "--title", "Probe the flaky gate", "--body", "private body", "--priority", "-3", "--content", strings.Repeat("44", 16) + ":2," + strings.Repeat("55", 16) + ":1"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("task add = exit %d stderr %q", exit, stderr.String())
	}
	if received.ProjectID != projectID || received.AssignedAgentID != agentID || received.Title != "Probe the flaky gate" || received.Body != "private body" || received.Priority != -3 || len(received.Content) != 2 || received.Content[0] != (api.ContentPinInput{ContentID: strings.Repeat("44", 16), ContentRevision: 2}) || received.Content[1].ContentRevision != 1 {
		t.Fatalf("daemon received %+v", received)
	}
	if !validHumanRequestKey(received.ID) || !validHumanRequestKey(received.IncarnationID) || received.ID == received.IncarnationID {
		t.Fatalf("minted identities = %q, %q", received.ID, received.IncarnationID)
	}
	if !strings.Contains(stdout.String(), received.ID) || !strings.Contains(stdout.String(), received.IncarnationID) {
		t.Fatalf("printed %q", stdout.String())
	}
}

// --file is the operator's one attachment path: every file is read whole and
// sent with the enqueue; an unreadable or out-of-bounds file refuses the whole
// command before any request.
func TestTaskAddAttachesFilesWithinBounds(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := strings.Repeat("11", 16)
	dir := t.TempDir()
	notes, shot := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "shot.png")
	if err := os.WriteFile(notes, []byte("private notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shot, []byte{0, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	var received api.EnqueueTaskInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		received, _ = call.EnqueueTaskInput()
		reply, err := api.NewMutationReply(api.MutationResult{Head: 9, Revision: 1})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"task", "add", "--project", id, "--agent", "any", "--title", "Inspect", "--file", notes + "," + shot}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || len(received.Attachments) != 2 || received.Attachments[0].Name != "notes.txt" || string(received.Attachments[0].Data) != "private notes" || received.Attachments[1].Name != "shot.png" || string(received.Attachments[1].Data) != "\x00\xff" {
		t.Fatalf("task add = exit %d stderr %q attachments %+v", exit, stderr.String(), received.Attachments)
	}
	stderr.Reset()
	if exit := run(context.Background(), []string{"task", "add", "--project", id, "--agent", "any", "--title", "Inspect", "--file", filepath.Join(dir, "missing.txt")}, webEnvironment(fixture), &stdout, &stderr); exit == 0 || !strings.Contains(stderr.String(), "missing.txt: no such file") {
		t.Fatalf("missing file: exit %d stderr %q", exit, stderr.String())
	}
}

// The file bounds are the kernel's: an unreadable, empty or oversized file
// names itself, and more than eight files never parse.
func TestTaskAttachmentsReadFilesWithinBounds(t *testing.T) {
	dir := t.TempDir()
	notes, empty, large := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "empty.txt"), filepath.Join(dir, "large.bin")
	for path, data := range map[string][]byte{notes: []byte("private notes"), empty: nil, large: make([]byte, kernel.MaxTaskAttachmentBytes+1)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := taskAttachments([]string{notes})
	if err != nil || len(got) != 1 || got[0].Name != "notes.txt" || string(got[0].Data) != "private notes" {
		t.Fatalf("read = %+v, %v", got, err)
	}
	if got, err := taskAttachments(nil); err != nil || got != nil {
		t.Fatalf("no files = %+v, %v", got, err)
	}
	for _, refused := range []struct{ path, want string }{
		{filepath.Join(dir, "missing.txt"), "missing.txt: no such file"},
		{empty, "empty.txt: attachments must be nonempty"},
		{large, "large.bin: attachments must be nonempty files totalling at most 8 MiB"},
	} {
		if _, err := taskAttachments([]string{notes, refused.path}); err == nil || !strings.Contains(err.Error(), refused.want) {
			t.Fatalf("%s: %v", refused.path, err)
		}
	}
	if _, _, ok := parse([]string{"task", "add", "--project", strings.Repeat("11", 16), "--agent", "any", "--title", "t", "--file", strings.Repeat(notes+",", kernel.MaxTaskAttachments) + notes}); ok {
		t.Fatal("nine files parsed")
	}
}

func TestTaskSendBackCarriesTaskAndNote(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	taskID := strings.Repeat("33", 16)
	var received api.SendBackInput
	var kind api.CallKind
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		kind = call.Kind()
		received, _ = call.SendBackInput()
		reply, err := api.NewMutationReply(api.MutationResult{Head: 12, Revision: 4})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"task", "send-back", "--task", taskID, "--note", "private note sentinel"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("task send-back = exit %d stderr %q", exit, stderr.String())
	}
	if kind != api.CallSendBackTask || received != (api.SendBackInput{TaskID: taskID, Note: "private note sentinel"}) {
		t.Fatalf("daemon received kind %v, %+v", kind, received)
	}
	var printed struct {
		ID       string `json:"id"`
		Head     uint64 `json:"head"`
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil || printed.ID != taskID || printed.Head != 12 || printed.Revision != 4 {
		t.Fatalf("printed %q, %v", stdout.String(), err)
	}
	if strings.Contains(stdout.String(), "private note sentinel") {
		t.Fatal("output leaked the note")
	}
	for _, args := range [][]string{
		{"task", "send-back", "--task", taskID, "--note", "n", "--name", "x"},
		{"task", "send-back", "--task", taskID, "--note", "n", "--project", taskID},
	} {
		if _, help, ok := parse(args); help || ok {
			t.Fatalf("parsed %q", strings.Join(args, " "))
		}
	}
}

func TestDispatchReadsExactFactoryRevisionThenSets(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	var setRevision uint64
	var setEnabled bool
	done := serveMany(fixture.listener,
		func(call api.Call) api.Reply {
			if call.Kind() != api.CallSnapshot {
				t.Errorf("first call kind = %v", call.Kind())
			}
			reply, err := api.NewSnapshotReply(api.DashboardSnapshot{Head: 4, Factory: api.FactorySummary{DispatchEnabled: false, Capacity: 8, ActiveRuns: 0, Revision: 21}, Projects: []api.ProjectSummary{}, Agents: []api.AgentSummary{}, Tasks: []api.TaskSummary{}})
			if err != nil {
				t.Fatal(err)
			}
			return reply
		},
		func(call api.Call) api.Reply {
			revision, enabled, ok := call.Dispatch()
			if !ok || call.Kind() != api.CallSetDispatch {
				t.Errorf("second call = %v", call.Kind())
			}
			setRevision, setEnabled = revision, enabled
			reply, err := api.NewMutationReply(api.MutationResult{Head: 5, Revision: 22})
			if err != nil {
				t.Fatal(err)
			}
			return reply
		})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"dispatch", "on"}, webEnvironment(fixture), &stdout, &stderr)
	awaitMany(t, done, 2)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("dispatch = exit %d stderr %q", exit, stderr.String())
	}
	if setRevision != 21 || !setEnabled {
		t.Fatalf("set_dispatch received revision %d enabled %v", setRevision, setEnabled)
	}
	if !strings.Contains(stdout.String(), `"enabled":true`) || !strings.Contains(stdout.String(), `"revision":22`) {
		t.Fatalf("printed %q", stdout.String())
	}
}

func TestDispatchExplicitRevisionDoesNotRefreshOrOverrideTheGuard(t *testing.T) {
	for _, test := range []struct {
		code    api.RemoteErrorCode
		message string
	}{
		{api.RemoteConflict, "local API request conflicts with durable state"},
		{api.RemoteRevisionConflict, "local API revision is stale"},
		{api.RemoteInternal, "local API failed internally"},
		{api.RemoteUnauthorized, "local API credential is unauthorized"},
		{api.RemoteForbidden, "local API request is forbidden"},
		{api.RemoteUnavailable, "local API is unavailable"},
	} {
		t.Run(string(test.code), func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				revision, enabled, ok := call.Dispatch()
				if !ok || call.Kind() != api.CallSetDispatch || revision != 19 || !enabled {
					t.Errorf("guarded dispatch = kind=%v revision=%d enabled=%v ok=%v", call.Kind(), revision, enabled, ok)
				}
				reply, err := api.NewErrorReply(test.code)
				if err != nil {
					t.Fatal(err)
				}
				return reply
			})
			var stdout, stderr bytes.Buffer
			exit := run(context.Background(), []string{"dispatch", "on", "--revision", "19"}, webEnvironment(fixture), &stdout, &stderr)
			if result := awaitServer(t, done); result.err != nil {
				t.Fatal(result.err)
			}
			if exit != exitFailure || stdout.Len() != 0 || stderr.String() != "factoryctl: dispatch: "+test.message+"\n" {
				t.Fatalf("guarded stale dispatch = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestCapacityUsesExplicitRevisionAndNamesTheOverseerLane(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		revision, capacity, ok := call.Capacity()
		if !ok || call.Kind() != api.CallSetCapacity || revision != 19 || capacity != 2 {
			t.Errorf("capacity = kind=%v revision=%d workers=%d ok=%v", call.Kind(), revision, capacity, ok)
		}
		reply, err := api.NewMutationReply(api.MutationResult{Head: 5, Revision: 20})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"capacity", "--workers", "2", "--revision", "19"}, webEnvironment(fixture), &stdout, &stderr)
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"workers":2`) || !strings.Contains(stdout.String(), `"overseer_lane":1`) {
		t.Fatalf("capacity = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestOperatorRemoteRejectionIsReportedWithoutFabricatedSuccess(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		reply, err := api.NewErrorReply(api.RemoteConflict)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"agent", "create", "--project", strings.Repeat("11", 16), "--name", "Builder", "--provider", "shell"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "agent create: local API request conflicts with durable state") {
		t.Fatalf("rejection = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestAgentSelectModelCarriesRevisionCheckedControls(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	agentID := strings.Repeat("22", 16)
	var received api.AgentModelSelectInput
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		var ok bool
		received, ok = call.AgentModelSelectInput()
		if !ok || call.Kind() != api.CallAgentSelectModel {
			t.Errorf("call = %v, input = %+v, ok = %v", call.Kind(), received, ok)
		}
		reply, err := api.NewMutationReply(api.MutationResult{Head: 10, Revision: 8})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"agent", "select-model", "--agent", agentID, "--revision", "7", "--model", "gpt-5.6-luna", "--reasoning-effort", "medium"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 || received != (api.AgentModelSelectInput{AgentID: agentID, ExpectedRevision: 7, Model: "gpt-5.6-luna", ReasoningEffort: "medium"}) {
		t.Fatalf("select model = exit %d stderr %q received %+v", exit, stderr.String(), received)
	}
}

func TestAgentAppearanceUsesOperatorAgentUpdate(t *testing.T) {
	agentID := strings.Repeat("22", 16)
	for _, args := range [][]string{{"--appearance", "1/2/3"}, {"--appearance", ""}, {"--appearance", "auto"}} {
		_, _, ok := parse(append([]string{"agent", "appearance", "--agent", agentID}, args...))
		if ok {
			t.Fatalf("parse %q accepted", args)
		}
	}
	for look, want := range map[string]string{"auto": "", "1/2/3/4/5/6/7/8/9": "1/2/3/4/5/6/7/8/9"} {
		fixture := newAPIFixture(t)
		var received api.OverseerAgentUpdateInput
		done := serveOne(fixture.listener, func(call api.Call) api.Reply {
			var ok bool
			received, ok = call.OverseerAgentUpdateInput()
			if !ok || call.Kind() != api.CallOperatorUpdateAgent {
				t.Errorf("call = %v, ok = %v", call.Kind(), ok)
			}
			reply, err := api.NewMutationReply(api.MutationResult{Head: 10, Revision: 8})
			if err != nil {
				t.Fatal(err)
			}
			return reply
		})
		var stdout, stderr bytes.Buffer
		exit := run(context.Background(), []string{"agent", "appearance", "--agent", agentID, "--revision", "7", "--appearance", look}, webEnvironment(fixture), &stdout, &stderr)
		awaitServer(t, done)
		fixture.close(t)
		if exit != 0 || stderr.Len() != 0 || received.AgentID != agentID || received.ExpectedRevision != 7 || received.Paused != nil || received.Archived != nil || received.Appearance == nil || *received.Appearance != want {
			t.Fatalf("appearance %q = exit %d stderr %q received %+v", look, exit, stderr.String(), received)
		}
	}
}

func TestAccountDiscoverCLICollectsPagesAndRejectsRepeatedCursor(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "repeated cursor"}[repeated], func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			served := make(chan struct{})
			go func() {
				defer close(served)
				for page := 0; page < 2; page++ {
					done := serveOne(fixture.listener, func(call api.Call) api.Reply {
						offset, ok := call.AccountsOffset()
						if !ok || offset != uint32(page*4096) {
							t.Errorf("page %d offset=%d valid=%t", page, offset, ok)
						}
						count := 4096
						if page == 1 {
							count = 1
						}
						accounts := api.Accounts{Accounts: make([]api.DiscoveredAccount, count)}
						for index := range accounts.Accounts {
							accounts.Accounts[index] = api.DiscoveredAccount{Provider: "codex", Home: "/private/account", Label: "linked", LinkedID: strings.Repeat("ab", 16), UnavailableReason: "login is no longer discoverable"}
						}
						if page == 0 || repeated {
							next := uint32(4096)
							accounts.NextOffset = &next
						}
						reply, err := api.NewAccountsReply(accounts)
						if err != nil {
							t.Errorf("page reply: %v", err)
						}
						return reply
					})
					if result := <-done; result.err != nil {
						t.Errorf("page server: %v", result.err)
					}
				}
			}()
			var stdout, stderr bytes.Buffer
			exit := run(context.Background(), []string{"account", "discover"}, webEnvironment(fixture), &stdout, &stderr)
			<-served
			if repeated {
				if exit == 0 {
					t.Fatal("repeated cursor accepted")
				}
				return
			}
			var result api.Accounts
			if exit != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || len(result.Accounts) != 4097 || result.NextOffset != nil || result.Accounts[4096].UnavailableReason == "" {
				t.Fatalf("CLI discovery exit=%d count=%d stderr=%q", exit, len(result.Accounts), stderr.String())
			}
		})
	}
}

func TestHumanCommandsUseOperatorClient(t *testing.T) {
	id := strings.Repeat("11", 16)
	operation := strings.Repeat("22", 16)
	for _, verb := range []string{"list", "reply", "cancel"} {
		t.Run(verb, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			args := map[string][]string{
				"list":   {"human", "list"},
				"reply":  {"human", "reply", "--operation-id", operation, "--request", id, "--revision", "3", "--reply", "Proceed"},
				"cancel": {"human", "cancel", "--request", id, "--revision", "3", "--run-revision", "5"},
			}[verb]
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				switch verb {
				case "list":
					if call.Kind() != api.CallHumanRequests {
						t.Errorf("wrong list call %v", call.Kind())
					}
					return api.NewHumanRequestListReply(api.HumanRequestList{Requests: []api.HumanRequest{}})
				case "reply":
					input, ok := call.HumanReplyInput()
					if !ok || input.RequestID != id || input.OperationID != operation || input.ExpectedRevision != 3 || input.Reply != "Proceed" {
						t.Errorf("wrong reply input %+v", input)
					}
				default:
					input, ok := call.HumanCancelInput()
					if !ok || input.RequestID != id || input.ExpectedRevision != 3 || input.ExpectedRunRevision != 5 {
						t.Errorf("wrong cancel input %+v", input)
					}
				}
				result, err := api.NewMutationReply(api.MutationResult{Head: 7, Revision: 4})
				if err != nil {
					t.Error(err)
				}
				return result
			})
			var stdout, stderr bytes.Buffer
			if exit := run(context.Background(), args, webEnvironment(fixture), &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
				t.Fatalf("command failed: exit=%d stderr=%q", exit, stderr.String())
			}
			awaitServer(t, done)
			if !json.Valid(stdout.Bytes()) {
				t.Fatalf("invalid output %q", stdout.String())
			}
		})
	}
}

func TestRepositoryEnableDisableUseEnabledOperatorAction(t *testing.T) {
	for _, action := range []string{"enable", "disable"} {
		t.Run(action, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			id := strings.Repeat("ab", 16)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				input, ok := call.ProjectRepositoryInput()
				if !ok || input.Action != "enabled" || input.ID != id || input.ExpectedRevision != 3 || input.Enabled == nil || *input.Enabled != (action == "enable") {
					t.Errorf("repository mutation: %+v", input)
				}
				return api.NewContentReply(api.ProjectRepository{ID: id, Revision: 4})
			})
			var stdout, stderr bytes.Buffer
			exit := run(context.Background(), []string{"project", "repository", action, "--id", id, "--revision", "3"}, webEnvironment(fixture), &stdout, &stderr)
			result := awaitServer(t, done)
			if result.err != nil {
				t.Errorf("server: %v", result.err)
			}
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, stderr.String())
			}
		})
	}
}

func TestRepositoryAddMintsIDWhenOmitted(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	project := strings.Repeat("ab", 16)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.ProjectRepositoryInput()
		if !ok || input.Action != "add" || input.ID == "" || input.ProjectID != project || input.Name != "Service" || input.Root != "/private/tmp/service" || input.BaseRef != "release" {
			t.Errorf("repository add = %+v", input)
		}
		return api.NewContentReply(api.ProjectRepository{ID: input.ID, Revision: 1})
	})
	var stdout, stderr bytes.Buffer
	args := []string{"project", "repository", "add", "--project", project, "--name", "Service", "--root", "/private/tmp/service", "--base", "release"}
	if exit := run(context.Background(), args, webEnvironment(fixture), &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
		t.Fatalf("repository add exit=%d stderr=%q", exit, stderr.String())
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestParseAttachmentCleanupAndCompaction(t *testing.T) {
	command, help, ok := parse([]string{"task", "update", "--task", strings.Repeat("01", 16), "--revision", "2", "--remove-attachments"})
	if !ok || help || !command.operatorControl || !command.removeAttachments {
		t.Fatalf("cleanup parse: %+v %v %v", command, help, ok)
	}
	for _, extra := range [][]string{{"--retry"}, {"--cancel"}, {"--body", "changed"}, {"--remove-attachments"}} {
		args := append([]string{"task", "update", "--task", strings.Repeat("01", 16), "--revision", "2", "--remove-attachments"}, extra...)
		if _, _, ok := parse(args); ok {
			t.Fatalf("mixed cleanup accepted: %v", args)
		}
	}
	command, help, ok = parse([]string{"storage", "compact"})
	if !ok || help || command.kind != commandCompactStorage {
		t.Fatalf("compact parse: %+v %v %v", command, help, ok)
	}
}
