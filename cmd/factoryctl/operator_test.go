//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestParseExactOperatorCommands(t *testing.T) {
	id := strings.Repeat("ab", 16)
	valid := []struct {
		name string
		args []string
	}{
		{name: "select model", args: []string{"agent", "select-model", "--agent", id, "--revision", "7", "--model", "gpt-5.6-luna"}},
		{name: "project create", args: []string{"project", "create", "--name", "North Workshop", "--root", "/private/tmp/repo"}},
		{name: "project limits", args: []string{"project", "limits", "--project", id, "--revision", "7", "--run-budget", "20", "--max-run-seconds", "900"}},
		{name: "agent create shell default role", args: []string{"agent", "create", "--project", id, "--name", "Builder One", "--provider", "shell", "--tool-budget", "100"}},
		{name: "agent create codex controls", args: []string{"agent", "create", "--project", id, "--name", "Foreman", "--provider", "codex", "--model", "gpt-5.6-luna", "--reasoning-effort", "medium", "--tool-budget", "100", "--role", "orchestrator"}},
		{name: "agent idle policy", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "standing_instruction", "--after-seconds", "60", "--instruction", "review retained changes"}},
		{name: "agent idle wait", args: []string{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "wait"}},
		{name: "account discover", args: []string{"account", "discover"}},
		{name: "account list", args: []string{"account", "list"}},
		{name: "account link", args: []string{"account", "link", "--provider", "codex", "--home", "/Users/operator/.codex-dogfood", "--label", "dogfood"}},
		{name: "agent select account", args: []string{"agent", "select-account", "--agent", id, "--revision", "7", "--account", strings.Repeat("cd", 16)}},
		{name: "agent select model", args: []string{"agent", "select-model", "--agent", id, "--revision", "7", "--model", "gpt-5.6-luna", "--reasoning-effort", "medium"}},
		{name: "task add minimal", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "Tighten the queue ordering"}},
		{name: "task add full", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "t", "--body", "b", "--priority", "-5"}},
		{name: "task add supplied identities", args: []string{"task", "add", "--project", id, "--agent", id, "--title", "t", "--task-id", id, "--incarnation-id", strings.Repeat("cd", 16)}},
		{name: "task add any eligible worker", args: []string{"task", "add", "--project", id, "--agent", "any", "--title", "t"}},
		{name: "status", args: []string{"status"}},
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
	invalid := [][]string{
		{"project", "create", "--name", "n"},
		{"project", "create", "--root", "/private/tmp/repo"},
		{"project", "create", "--name", "n", "--root", "relative"},
		{"project", "create", "--name", "n", "--root", "/r", "--name", "n"},
		{"project", "create", "--name", "n", "--root", "/r", "--project", id},
		{"project", "limits", "--project", id, "--revision", "0", "--run-budget", "1", "--max-run-seconds", "1"},
		{"project", "limits", "--project", id, "--revision", "1", "--run-budget", "1", "--max-run-seconds", "86401"},
		{"agent", "create", "--project", id, "--name", "n"},
		{"agent", "create", "--project", id, "--name", "n", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "unknown", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--model", "model", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--reasoning-effort", "medium", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "codex", "--reasoning-effort", "extreme", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "claude_code", "--reasoning-effort", "ultra", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "codex", "--model", strings.Repeat("m", 129), "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--tool-budget", "0"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--tool-budget", "01"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--tool-budget", "x"},
		{"agent", "create", "--project", "short", "--name", "n", "--provider", "shell", "--tool-budget", "1"},
		{"agent", "create", "--project", id, "--name", "n", "--provider", "shell", "--tool-budget", "1", "--role", "manager"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7", "--after-seconds", "60", "--instruction", "x", "--run-budget", "1"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "standing_instruction", "--after-seconds", "0", "--instruction", "x", "--run-budget", "1"},
		{"agent", "idle-policy", "--agent", id, "--revision", "7", "--policy", "wait", "--run-budget", "1"},
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
		{"dispatch", "toggle"},
		{"dispatch", "on", "--revision", "0"},
		{"dispatch", "on", "--revision", "01"},
		{"dispatch"},
		{"capacity", "--workers", "0", "--revision", "1"},
		{"capacity", "--workers", "1025", "--revision", "1"},
		{"capacity", "--workers", "2"},
		{"capacity", "--workers", "2", "--revision", "0"},
		{"project", "make", "--name", "n", "--root", "/r"},
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
		"--model", "gpt-5.6-luna", "--reasoning-effort", "medium", "--tool-budget", "100", "--role", "orchestrator",
	}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("agent create = exit %d stderr %q", exit, stderr.String())
	}
	if received.ProjectID != projectID || received.Name != "Builder" || received.Role != "orchestrator" || received.Provider != "codex" || received.Model != "gpt-5.6-luna" || received.ReasoningEffort != "medium" || received.ToolBudgetLimit != 100 || !validHumanRequestKey(received.ID) {
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
	exit := run(context.Background(), []string{"task", "add", "--project", projectID, "--agent", agentID, "--title", "Probe the flaky gate", "--body", "private body", "--priority", "-3"}, webEnvironment(fixture), &stdout, &stderr)
	awaitServer(t, done)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("task add = exit %d stderr %q", exit, stderr.String())
	}
	if received.ProjectID != projectID || received.AssignedAgentID != agentID || received.Title != "Probe the flaky gate" || received.Body != "private body" || received.Priority != -3 {
		t.Fatalf("daemon received %+v", received)
	}
	if !validHumanRequestKey(received.ID) || !validHumanRequestKey(received.IncarnationID) || received.ID == received.IncarnationID {
		t.Fatalf("minted identities = %q, %q", received.ID, received.IncarnationID)
	}
	if !strings.Contains(stdout.String(), received.ID) || !strings.Contains(stdout.String(), received.IncarnationID) {
		t.Fatalf("printed %q", stdout.String())
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
	exit := run(context.Background(), []string{"agent", "create", "--project", strings.Repeat("11", 16), "--name", "Builder", "--provider", "shell", "--tool-budget", "10"}, webEnvironment(fixture), &stdout, &stderr)
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

func TestTaskRecoveryUsesOperatorClient(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	taskID, incarnationID := strings.Repeat("11", 16), strings.Repeat("22", 16)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.TaskRecoveryInput()
		if !ok || input.TaskID != taskID || input.IncarnationID != incarnationID {
			t.Errorf("unexpected recovery call: %+v", call)
		}
		reply, err := api.NewTaskRecoveryReply(api.TaskRecovery{State: "found", TaskID: taskID, IncarnationID: incarnationID, ProjectID: taskID, AssignedAgentID: taskID, WorkRevision: 1, Revision: 1, Status: "blocked", BlockedReason: "tool unavailable", ArtifactPaths: []string{}, Disposition: "none", OverseerNotification: "none"})
		if err != nil {
			t.Error(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"task", "recovery", "--task", taskID, "--incarnation", incarnationID}, webEnvironment(fixture), &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("task recovery = exit %d stderr %q", exit, stderr.String())
	}
	awaitServer(t, done)
	var result api.TaskRecovery
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.BlockedReason != "tool unavailable" {
		t.Fatalf("recovery response %q: %v", stdout.String(), err)
	}
}

func TestHumanCommandsUseOperatorClient(t *testing.T) {
	id := strings.Repeat("11", 16)
	operation := strings.Repeat("22", 16)
	for _, reply := range []bool{false, true} {
		t.Run(fmt.Sprint(reply), func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			args := []string{"human", "list"}
			if reply {
				args = []string{"human", "reply", "--operation-id", operation, "--request", id, "--revision", "3", "--reply", "Proceed"}
			}
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				if !reply {
					if call.Kind() != api.CallHumanRequests {
						t.Errorf("wrong list call %v", call.Kind())
					}
					return api.NewHumanRequestListReply(api.HumanRequestList{Requests: []api.HumanRequest{}})
				}
				input, ok := call.HumanReplyInput()
				if !ok || input.RequestID != id || input.OperationID != operation || input.ExpectedRevision != 3 || input.Reply != "Proceed" {
					t.Errorf("wrong reply input %+v", input)
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
