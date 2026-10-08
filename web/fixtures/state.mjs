const projectID = "11".repeat(16);
const secondProjectID = "12".repeat(16);
const agentID = "21".repeat(16);
const secondAgentID = "22".repeat(16);
const thirdAgentID = "23".repeat(16);
const taskID = "31".repeat(16);
const queuedTaskID = "32".repeat(16);
const doneTaskID = "33".repeat(16);
const failedTaskID = "34".repeat(16);
const secondRunningTaskID = "35".repeat(16);
const requestID = "41".repeat(16);
const accountID = "51".repeat(16);

export const fixtureState = {
  head: 42n,
  factory: { dispatch_enabled: true, capacity: 8, active_runs: 2, revision: 42n },
  projects: new Map([
    [projectID, { id: projectID, name: "North Workshop", run_budget_limit: 12n, runs_used: 5n, max_run_seconds: 900, revision: 4n }],
    [secondProjectID, { id: secondProjectID, name: "South Workshop", run_budget_limit: 0n, runs_used: 3n, max_run_seconds: 0, revision: 5n }],
  ]),
  agents: new Map([
    [agentID, { id: agentID, project_id: projectID, name: "Builder One", role: "worker", provider: "claude_code", paused: false, model: "claude-opus-5", reasoning_effort: "high", effective_model: "claude-opus-5", effective_reasoning_effort: "high", model_source: "agent", revision: 10n, account_id: accountID, idle_policy: "wait", idle_after_seconds: 0, idle_instruction: "", idle_run_budget: 0, idle_runs_used: 0 }],
    [secondAgentID, { id: secondAgentID, project_id: secondProjectID, name: "Dispatch Lead", role: "orchestrator", provider: "claude_code", paused: true, model: "claude-opus-5", reasoning_effort: "", effective_model: "claude-opus-5", effective_reasoning_effort: "", model_source: "agent", revision: 11n, account_id: "", idle_policy: "wait", idle_after_seconds: 0, idle_instruction: "", idle_run_budget: 0, idle_runs_used: 0 }],
    [thirdAgentID, { id: thirdAgentID, project_id: projectID, name: "Builder Two", role: "worker", provider: "codex", paused: false, model: "", reasoning_effort: "", effective_model: "gpt-6-astra", effective_reasoning_effort: "high", model_source: "/Users/operator/.codex/config.toml", revision: 12n, account_id: "", idle_policy: "wait", idle_after_seconds: 0, idle_instruction: "", idle_run_budget: 0, idle_runs_used: 0 }],
  ]),
  tasks: new Map([
    [taskID, { id: taskID, project_id: projectID, assigned_agent_id: agentID, title: "Review the state projection", status: "running", priority: 10, revision: 12n }],
    [queuedTaskID, { id: queuedTaskID, project_id: secondProjectID, assigned_agent_id: thirdAgentID, title: "Tighten the queue ordering", status: "queued", priority: 6, revision: 13n }],
    [doneTaskID, { id: doneTaskID, project_id: projectID, assigned_agent_id: thirdAgentID, title: "Close the resize race", status: "succeeded", priority: 8, revision: 14n }],
    [failedTaskID, { id: failedTaskID, project_id: secondProjectID, assigned_agent_id: thirdAgentID, title: "Probe the flaky gate", status: "failed", priority: 4, revision: 15n }],
  ]),
  humanRequests: new Map([
    [requestID, { id: requestID, project_id: projectID, agent_id: agentID, task_id: taskID, created_at: 40n, updated_at: 42n, revision: 13n, kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true }],
  ]),
  accounts: new Map([
    [accountID, { id: accountID, provider: "claude_code", home: "/Users/operator/.claude", label: "work", revision: 1n }],
  ]),
};

export const fixtureFloorState = {
  ...fixtureState,
  agents: new Map(fixtureState.agents).set(secondAgentID, { ...fixtureState.agents.get(secondAgentID), paused: false }),
  tasks: new Map(fixtureState.tasks).set(secondRunningTaskID, { id: secondRunningTaskID, project_id: secondProjectID, assigned_agent_id: secondAgentID, title: "Coordinate the release train", status: "running", priority: 9, revision: 16n }),
};

const nodeID = (prefix) => prefix.repeat(16);
const reading = (evidence, observation, state, rate) => ({ evidence, observation, state, ...(rate === undefined ? {} : { rate_per_hour: rate }) });
const summary = { components: 7, inferred: 6, observed: 3, quiet: 1, partial: 1, stale: 0, unobserved: 1, opaque: 1, runtime_only: 1, contradicted: 0 };

/** The first project's operational graph: a daemon with routes, a store, a shared queue, an external and runtime-only activity. */
export const fixtureGraph = {
  project_id: projectID,
  digest: "ab".repeat(32),
  observed_at: 1760000000000,
  sources: [{ repository_id: "03".repeat(16), name: "north-workshop", kind: "integrated", target_ref: "main", revision: "c3".repeat(20), observed_at: 1760000000000 }],
  nodes: [
    { id: nodeID("a1"), kind: "processor", label: "kernel", runtime: "process", paths: ["internal/kernel"], ...reading("both", "partial", "active", 120) },
    { id: nodeID("a2"), kind: "ingress", label: "/browser", unit: nodeID("a1"), trigger: "request", paths: ["internal/kernel/server.go"], ...reading("both", "observed", "active", 120), latency_p95_ms: 40 },
    { id: nodeID("a3"), kind: "ingress", label: "/v1/traces", unit: nodeID("a1"), trigger: "request", paths: ["internal/kernel/traces.go"], ...reading("static", "quiet", "idle") },
    { id: nodeID("a4"), kind: "store", label: "state.db", unit: nodeID("a1"), paths: ["internal/kernel/store"], ...reading("static", "unobserved", "unknown") },
    { id: nodeID("b1"), kind: "processor", label: "web", runtime: "browser", paths: ["web"], ...reading("static", "unobserved", "unknown") },
    { id: nodeID("c1"), kind: "queue", label: "work queue", paths: [], ...reading("static", "opaque", "unknown") },
    { id: nodeID("d1"), kind: "external", label: "api.github.com", paths: [], ...reading("static", "opaque", "unknown") },
    { id: nodeID("e1"), kind: "unknown", label: "POST /mystery", paths: [], ...reading("runtime", "observed", "active", 3) },
  ],
  edges: [
    { from: nodeID("b1"), to: nodeID("a2"), kind: "calls", ...reading("both", "observed", "active", 120) },
    { from: nodeID("a1"), to: nodeID("d1"), kind: "calls", ...reading("static", "unobserved", "unknown") },
  ],
  summary,
  omitted: 0,
};

/** The floor takes one graph per project; the second one is unserved. */
export const fixtureGraphs = new Map([[projectID, fixtureGraph]]);

/** One observed live run; the other running agent deliberately stays unknown. */
export const fixtureRunPaths = new Map([[agentID, {
  taskId: taskID,
  taskRevision: fixtureState.tasks.get(taskID).revision,
  runId: "71".repeat(16),
  projectId: projectID,
  paths: ["internal/kernel/store", "internal/kernel/store/state.go"],
}]]);

const crowded = Array.from({ length: 18 }, (_, index) => {
  const id = (96 + index).toString(16).padStart(2, "0").repeat(16);
  const taskId = (128 + index).toString(16).padStart(2, "0").repeat(16);
  return { id, taskId, index };
});

/** Optional same-room overflow fixture: ?fixture=crowded. */
export const fixtureCrowdedState = {
  ...fixtureFloorState,
  factory: { ...fixtureFloorState.factory, active_runs: 12 },
  agents: new Map([
    ...fixtureFloorState.agents,
    ...crowded.map(({ id, index }) => [id, { ...fixtureState.agents.get(thirdAgentID), id, name: `Overflow ${index + 1}`, revision: BigInt(50 + index) }]),
  ]),
  tasks: new Map([
    ...fixtureFloorState.tasks,
    ...crowded.slice(0, 10).map(({ id, taskId, index }) => [taskId, { id: taskId, project_id: projectID, assigned_agent_id: id, title: `Crowded build ${index + 1}`, status: "running", priority: 3, revision: BigInt(60 + index) }]),
  ]),
};

export const fixtureCrowdedRunPaths = new Map([
  ...fixtureRunPaths,
  ...crowded.slice(0, 10).map(({ id, taskId, index }) => [id, { taskId, taskRevision: BigInt(60 + index), runId: (160 + index).toString(16).padStart(2, "0").repeat(16), projectId: projectID, paths: ["internal/kernel/store"] }]),
]);
