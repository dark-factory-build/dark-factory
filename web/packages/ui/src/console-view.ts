import { productionKey, proposedProduction, type ProductionContraption } from "./production-view.js";
import { GRAPH_RUNTIMES, GRAPH_STATES, type AgentItem, type GraphNode, type GraphReading, type HumanRequestItem, type IntakeCandidate, type IntakeSource, type OperationalGraphView, type RunTelemetry, type StateView, type TaskItem } from "@dark-factory/client";
import { compareText, QUIET_SECONDS, type SceneFlow, type SceneGraph, type SceneUnit, type SceneMachine, type SceneProposal, type SceneReading, type SceneWorker } from "./factory-scene/scene.js";

export type AgentActivity = "busy" | "waiting" | "needs-you" | "idle";
/** The operator-facing state has one name for each actionable condition. */
export type AgentStatus = "working" | "ready" | "needs-you" | "paused";

/** Tasks an agent is on right now (durable assignment, live statuses). */
export function agentCurrentTask(agent: AgentItem, state: StateView): TaskItem | undefined {
	if (agent.archived) return undefined;
  for (const task of state.tasks.values()) {
    if (
      task.assigned_agent_id === agent.id &&
      task.status === "running"
    ) {
      return task;
    }
  }
  return undefined;
}

/** The operator-facing state has one name for each actionable condition. */
export function agentStatus(agent: AgentItem, state: StateView): AgentStatus {
  for (const request of state.humanRequests.values()) {
    if (request.agent_id === agent.id) return "needs-you";
  }
  if (agentCurrentTask(agent, state) !== undefined) return "working";
  return agent.paused ? "paused" : "ready";
}

/** The sprite vocabulary derives from the operator-facing status once. */
export function agentActivity(agent: AgentItem, state: StateView): AgentActivity {
  switch (agentStatus(agent, state)) {
    case "needs-you": return "needs-you";
    case "working": return "busy";
    case "paused": return "idle";
    case "ready": return "waiting";
  }
}

/** The overseer is the console's entry point; a worker is a usable fallback. */
export function primaryAgent(state: StateView): AgentItem | undefined {
  return [...state.agents.values()]
		.filter((agent) => !agent.archived)
    .sort((left, right) =>
      (left.role === right.role ? 0 : left.role === "orchestrator" ? -1 : 1)
      || (left.role === "orchestrator" && left.paused !== right.paused ? left.paused ? 1 : -1 : 0)
      || compareText(left.name, right.name)
      || compareText(left.id, right.id))[0];
}

export type WorkState = "needs-you" | "inbox" | "in-review" | "running" | "queued";
export type WorkRow = Readonly<{ key: string; state: WorkState; title: string; task?: TaskItem; request?: HumanRequestItem; pr?: ProductionContraption; origin?: string; intake?: Readonly<{ source: IntakeSource; candidate: IntakeCandidate }> }>;
/** An issue waiting at the door needs the operator as much as a question does. */
export const needsYou = (row: WorkRow) => row.state === "needs-you" || row.state === "inbox";

/** The one Work list: issues the intake poll found waiting for acceptance, open tasks, and finished tasks whose PR is still in flight, attention first. */
export function workRows(state: StateView | undefined, inProgress: readonly ProductionContraption[], sources: readonly IntakeSource[] = []): readonly WorkRow[] {
  if (state === undefined) return [];
  const requests = new Map([...state.humanRequests.values()].map((request) => [request.task_id, request]));
  const rows = new Map<string, { task?: TaskItem; request?: HumanRequestItem; pr?: ProductionContraption; key: string }>();
  for (const task of state.tasks.values()) if (task.status === "queued" || task.status === "running" || task.status === "blocked" || requests.has(task.id)) rows.set(task.id, { key: task.id, task, request: requests.get(task.id) });
  for (const request of requests.values()) if (!state.tasks.has(request.task_id)) rows.set(request.task_id, { key: request.task_id, request });
  for (const pr of inProgress) {
    if (pr.pullRequest === undefined) continue;
    const linked = pr.tasks.flatMap((id) => state.tasks.get(id) ?? []);
    const open = linked.filter((task) => rows.has(task.id));
    for (const task of open.length > 0 ? open : linked.slice(0, 1)) rows.set(task.id, { ...rows.get(task.id), key: task.id, task, pr });
    if (linked.length === 0) rows.set(productionKey(pr), { key: productionKey(pr), pr });
  }
  const rank: Record<WorkState, number> = { "needs-you": 0, inbox: 1, "in-review": 2, running: 3, queued: 4 };
  const inbox = sources.flatMap((source) => (source.sync?.waiting ?? []).map((candidate): WorkRow => ({ key: `${source.id}:${candidate.number}`, state: "inbox", title: candidate.title, origin: `#${candidate.number}`, intake: { source, candidate } })));
  return [...inbox, ...[...rows.values()].map(({ task, request, pr, key }): WorkRow => {
    const terminal = task === undefined || task.status === "succeeded" || task.status === "failed" || task.status === "cancelled";
    const origin = task === undefined ? undefined : task.issue_number !== undefined ? `#${task.issue_number}` : task.mission_id !== undefined ? "mission" : state.agents.get(task.assigned_agent_id)?.role === "orchestrator" ? "overseer" : undefined;
    return { key, task, request, pr, origin, title: task?.title ?? pr?.pullRequest?.title ?? `Question from ${state.agents.get(request?.agent_id ?? "")?.name ?? "Agent"}`,
      state: request !== undefined || task?.status === "blocked" ? "needs-you" : terminal ? "in-review" : task.status === "running" ? "running" : "queued" };
  })].sort((a, b) => rank[a.state] - rank[b.state]);
}

/** Active work first, then queued, then finished; priority breaks ties. */
export function orderTasksForHome(state: StateView): readonly TaskItem[] {
  const rank: Record<TaskItem["status"], number> = {
    running: 0,
    queued: 1,
    blocked: 2,
    succeeded: 3,
    failed: 4,
    cancelled: 4,
  };
  return [...state.tasks.values()].sort((left, right) => {
    const byStage = rank[left.status] - rank[right.status];
    return byStage !== 0 ? byStage : right.priority - left.priority;
  });
}

/** Routes fold into a manifold past six per unit; zooming in lists them inside it. */
const MAX_DOCKS = 6;

export type FloorScene = Readonly<{
  graph: SceneGraph;
  workers: readonly SceneWorker[];
  tasks: readonly SceneTask[];
}>;

/** A served task and the run sample observed for it, without inferring any execution detail. */
export type SceneTask = Readonly<{
  id: string;
  agentId: string;
  projectId: string;
  title: string;
  status: TaskItem["status"];
  observation?: Readonly<{ taskRevision: bigint; runId: string }>;
  humanRequestIds: readonly string[];
}>;

/** One changed-path sample is tied to the task and provider run that produced it. */
export type RunPathSample = Readonly<{
  taskId: string;
  taskRevision: bigint;
  runId: string;
  projectId: string;
  paths: readonly string[];
  telemetry?: Readonly<RunTelemetry>;
}>;

const reading = (value: GraphReading & { error_permille?: number; latency_p95_ms?: number; last_seen?: number; deployed_at?: number }): SceneReading => ({
  evidence: value.evidence, observation: value.observation, state: value.state, ratePerHour: value.rate_per_hour ?? 0,
  errorPermille: value.error_permille ?? 0, latencyMs: value.latency_p95_ms ?? 0, ...(value.last_seen === undefined ? {} : { lastSeen: value.last_seen }),
  ...(value.deployed_at === undefined ? {} : { deployedAt: value.deployed_at }),
});

/** A fold of several machines claims only what all of them support. */
function combine(readings: readonly SceneReading[]): SceneReading {
  const observations = new Set(readings.map((item) => item.observation));
  const observation = observations.size === 1 ? readings[0]!.observation : "partial";
  const measured = readings.filter((item) => item.state !== "unknown");
  const state = observation === "quiet" ? "idle" : measured.length === 0 ? "unknown"
    : GRAPH_STATES.find((candidate) => candidate !== "idle" && measured.some((item) => item.state === candidate)) ?? "unknown";
  const evidence = new Set(readings.map((item) => item.evidence));
  return { evidence: evidence.size === 1 ? readings[0]!.evidence : "both", observation, state,
    ratePerHour: readings.reduce((sum, item) => sum + item.ratePerHour, 0), errorPermille: Math.max(0, ...readings.map((item) => item.errorPermille)),
    latencyMs: Math.max(0, ...readings.map((item) => item.latencyMs)) };
}

/**
 * The world projection: operational nodes become units and their machines,
 * shared stock, external parties and quarantined activity. Only static
 * evidence gives a machine its owner; runtime-only nodes stay unexplained.
 */
export function projectGraph(graphs: ReadonlyMap<string, OperationalGraphView> | undefined, projects: readonly string[]) {
  const units: SceneUnit[] = [], shared: SceneMachine[] = [], parties: SceneMachine[] = [], quarantine: SceneMachine[] = [], flows = new Map<string, SceneFlow>();
  /** Node id to the machine that pictures it, and the unit that owns it. */
  const where = new Map<string, { unit?: string; machine: string }>();
  const nodes: (GraphNode & { projectId: string })[] = [];
  const digests: string[] = [];
  for (const projectId of projects) {
    const graph = graphs?.get(projectId);
    if (graph === undefined) continue;
    digests.push(graph.digest);
    const byId = new Map(graph.nodes.map((node) => [node.id, node]));
    for (const node of graph.nodes) nodes.push({ ...node, projectId });
    // Only runtime-only nodes are quarantined: a static "no recognised entry
    // points" marker belongs to its unit.
    const staticNode = (node: GraphNode) => node.evidence !== "runtime";
    for (const unit of graph.nodes.filter((node) => node.kind === "processor" && staticNode(node))) {
      const own = graph.nodes.filter((node) => node.unit === unit.id && staticNode(node)).sort((left, right) => compareText(left.label, right.label) || compareText(left.id, right.id));
      const machines: SceneMachine[] = [];
      const docks = own.filter((node) => node.kind === "ingress" && node.trigger !== "timer");
      const foldDocks = docks.length > MAX_DOCKS;
      if (foldDocks) {
        const id = `${unit.id}:docks`;
        machines.push({ id, kind: "ingress", label: `${docks.length} routes`, represented: docks.map((node) => node.id), routes: docks.map((node) => node.label), reading: combine(docks.map(reading)) });
        for (const node of docks) where.set(node.id, { unit: unit.id, machine: id });
      }
      for (const node of own) {
        if (foldDocks && docks.includes(node)) continue;
        machines.push({ id: node.id, kind: node.kind, label: node.label, ...(node.trigger === undefined ? {} : { trigger: node.trigger }), reading: reading(node) });
        where.set(node.id, { unit: unit.id, machine: node.id });
      }
      units.push({ id: unit.id, label: unit.label, ...(unit.runtime === undefined ? {} : { runtime: unit.runtime }), reading: reading(unit), machines });
      where.set(unit.id, { unit: unit.id, machine: unit.id });
    }
    for (const node of graph.nodes) {
      if (where.has(node.id)) continue;
      const machine: SceneMachine = { id: node.id, kind: node.kind, label: node.label, reading: reading(node),
        ...(node.unit === undefined ? {} : { owner: byId.get(node.unit)?.label ?? "", ...(where.get(node.unit)?.unit === node.unit ? { claims: node.unit } : {}) }) };
      where.set(node.id, { machine: node.id });
      if (node.kind === "external") parties.push(machine);
      else if (staticNode(node) && node.unit === undefined) shared.push(machine);
      else quarantine.push(machine);
    }
    for (const edge of graph.edges) {
      const from = where.get(edge.from)?.machine, to = where.get(edge.to)?.machine;
      if (from === undefined || to === undefined || from === to) continue;
      // Folded flows claim only what all of them support, like folded machines.
      const key = `${from} ${to}`, held = flows.get(key);
      flows.set(key, { from, to, kind: held?.kind ?? edge.kind, reading: held === undefined ? reading(edge) : combine([held.reading, reading(edge)]) });
    }
  }
  quarantine.sort((left, right) => compareText(left.owner ?? "", right.owner ?? "") || compareText(left.label, right.label) || compareText(left.id, right.id));
  const graph: SceneGraph = {
    digest: `${digests.join(" ")}:${quarantine.map((machine) => machine.id).join(",")}`,
    units, shared, parties, quarantine, flows: [...flows.values()],
    observedAt: Math.max(0, ...projects.map((id) => graphs?.get(id)?.observed_at ?? 0)),
    ...(projects.length === 1 && graphs?.get(projects[0]!) !== undefined ? { summary: graphs.get(projects[0]!)!.summary, sources: graphs.get(projects[0]!)!.sources } : {}),
  };
  /**
   * Which machine a changed path belongs to: the machine built from that
   * file or directory, else the unit whose code area holds it (the most
   * specific area wins; a running unit is preferred to a command-line tool).
   */
  const locate = (projectId: string, path: string): { unit?: string; machine: string } | undefined => {
    const within = (area: string) => area === "." || path === area || path.startsWith(`${area}/`);
    const candidates = nodes.filter((node) => node.projectId === projectId && node.paths.some(within) && node.kind !== "external" && where.has(node.id));
    const score = (node: GraphNode) => Math.max(...node.paths.filter(within).map((area) => area === "." ? 0 : area.length));
    const leaf = candidates.filter((node) => node.kind !== "processor").sort((left, right) => score(right) - score(left) || compareText(left.id, right.id))[0];
    const unit = candidates.filter((node) => node.kind === "processor").sort((left, right) => score(right) - score(left)
      || GRAPH_RUNTIMES.indexOf(left.runtime ?? "cli") - GRAPH_RUNTIMES.indexOf(right.runtime ?? "cli") || compareText(left.id, right.id))[0];
    const best = leaf !== undefined && (unit === undefined || score(leaf) >= score(unit)) ? leaf : unit;
    return best === undefined ? undefined : where.get(best.id);
  };
  return { graph, locate, where };
}

/** Live task and observed-path projection onto the world. */
export function projectFloor(state: StateView | undefined, prepared: ReturnType<typeof projectGraph>, runPaths?: ReadonlyMap<string, RunPathSample>, lastRunPaths?: ReadonlyMap<string, RunPathSample>): FloorScene {
  const footprint = (sample: RunPathSample | undefined) => {
    const counts = new Map<string, { unit?: string; machine: string; count: number }>();
    for (const path of sample?.paths ?? []) {
      const found = prepared.locate(sample!.projectId, path);
      if (found === undefined) continue;
      counts.set(found.machine, { ...found, count: (counts.get(found.machine)?.count ?? 0) + 1 });
    }
    return [...counts.values()].sort((left, right) => right.count - left.count || compareText(left.machine, right.machine));
  };
  const tasks = state === undefined ? [] : [...state.tasks.values()]
    .filter((task) => !state.agents.get(task.assigned_agent_id)?.archived)
    .sort((left, right) => compareText(left.id, right.id))
    .map((task) => {
      const sample = matchingRunSample(state, task, runPaths);
      return {
        id: task.id, agentId: task.assigned_agent_id, projectId: task.project_id, title: task.title, status: task.status,
        ...(sample === undefined ? {} : { observation: { taskRevision: sample.taskRevision, runId: sample.runId } }),
        humanRequestIds: [...state.humanRequests.values()]
          .filter((request) => request.task_id === task.id && request.agent_id === task.assigned_agent_id && request.project_id === task.project_id)
          .sort((left, right) => compareText(left.id, right.id))
          .map((request) => request.id),
      };
    });
  const labels = new Map([...prepared.graph.units.map((unit) => [unit.id, unit.label] as const), ...[...prepared.graph.shared, ...prepared.graph.quarantine].map((machine) => [machine.id, machine.label] as const)]);
  const workers = state === undefined ? [] : [...state.agents.values()].filter((agent) => !agent.archived).map((agent): SceneWorker => {
    const task = agentCurrentTask(agent, state);
    const sample = task === undefined ? undefined : matchingRunSample(state, task, runPaths);
    const live = footprint(sample)[0];
    const telemetry = sample?.telemetry;
    // Recorded silence, not a guess: a busy agent that has made no tool call
    // or API request for a while is waiting on something.
    const activity = agentActivity(agent, state);
    const previous = lastRunPaths?.get(agent.id);
    const last = previous?.projectId === agent.project_id && previous.paths.length > 0 ? footprint(previous)[0] : undefined;
    const location: SceneWorker["location"] = task === undefined ? last === undefined ? "resting" : "last-observed" : live !== undefined ? "working" : "unobserved";
    const at = location === "working" ? live : location === "last-observed" ? last : undefined;
    return {
      id: agent.id, name: agent.name, role: agent.role, provider: agent.provider,
      ...(agent.appearance === undefined ? {} : { appearance: agent.appearance }),
      activity: activity === "busy" && (telemetry?.quiet_seconds ?? 0) >= QUIET_SECONDS ? "waiting" : activity, paused: agent.paused, location,
      ...(telemetry === undefined ? {} : { telemetry }),
      ...(at === undefined ? {} : { locationLabel: labels.get(at.unit ?? at.machine) ?? "", nodeId: at.unit ?? at.machine }),
      ...(location === "working" && live !== undefined ? { observedBayId: live.machine } : {}),
    };
  });
  return { graph: prepared.graph, workers, tasks };
}

/** The current run's recorded telemetry, only while its sample is for the agent's exact running task. */
export function agentTelemetry(agent: AgentItem, state: StateView | undefined, runPaths: ReadonlyMap<string, RunPathSample> | undefined): RunTelemetry | undefined {
  const task = state === undefined ? undefined : agentCurrentTask(agent, state);
  return state === undefined || task === undefined ? undefined : matchingRunSample(state, task, runPaths)?.telemetry;
}

/** A live sample is evidence only for its exact running task and assigned agent. */
function matchingRunSample(
  state: StateView,
  task: TaskItem,
  runPaths: ReadonlyMap<string, RunPathSample> | undefined,
): RunPathSample | undefined {
  if (task.status !== "running") return undefined;
  const project = state.projects.get(task.project_id);
  if (project?.id !== task.project_id) return undefined;
  const agent = state.agents.get(task.assigned_agent_id);
  if (agent?.id !== task.assigned_agent_id || agent.project_id !== task.project_id) return undefined;
  const sample = runPaths?.get(agent.id);
  return sample?.taskId === task.id && sample.taskRevision === task.revision && sample.projectId === task.project_id && sample.runId !== ""
    ? sample
    : undefined;
}

/** Project each proposal onto the machines it changes; never manufacture a combined future plant. */
export function projectProposals(prepared: ReturnType<typeof projectGraph>, items: readonly ProductionContraption[]) {
  const proposals: SceneProposal[] = items.filter(proposedProduction).map((item) => ({
    id: productionKey(item), title: item.pullRequest?.title || item.construction?.title || "Proposed change",
    state: item.source.kind === "unavailable" ? "unavailable" : item.source.stale ? "stale" : "active", base: item.source.base, head: item.source.head,
    operations: item.source.paths.map((path) => {
      const found = prepared.locate(item.projectId, path.path) ?? (path.old_path === undefined ? undefined : prepared.locate(item.projectId, path.old_path));
      return { ...(found === undefined ? {} : { entityId: found.machine, ...(found.unit === undefined ? {} : { unitId: found.unit }) }), path: path.path, ...(path.old_path === undefined ? {} : { previousPath: path.old_path }),
        kind: ({ added: "addition", modified: "modification", deleted: "removal", renamed: "move" } as const)[path.status] };
    }),
  }));
  const reviewers = new Map<string, SceneWorker>();
  for (const item of items.filter(proposedProduction)) {
    const proposal = proposals.find((candidate) => candidate.id === productionKey(item))!;
    const operation = proposal.operations.find((candidate) => candidate.entityId !== undefined);
    for (const actor of item.reviewers) {
      const id = `review:${item.projectId}:${item.repository}:${actor.id}`;
      if (reviewers.has(id)) continue;
      const working = actor.state === "running" && proposal.state === "active" && actor.head === proposal.head && operation !== undefined;
      reviewers.set(id, { id, name: actor.name || actor.id, role: "worker", activity: working ? "busy" : actor.state === "waiting" ? "waiting" : "idle",
        location: working ? "working" : "resting", ...(working ? { nodeId: operation.unitId ?? operation.entityId, observedBayId: operation.entityId } : {}),
        review: { proposalId: proposal.id, scope: `Review assignment at ${actor.head || "unknown head"}; ${proposal.operations.length} observed paths. Individual file inspection is not observed.` } });
    }
  }
  return { proposals, reviewers: [...reviewers.values()] };
}
