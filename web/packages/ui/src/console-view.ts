import { MAX_SNAPSHOT_ENTITIES, type AgentItem, type StateView, type TaskItem, type TopologyView } from "@dark-factory/client";
import { compareText, inventoryLabels, type InventoryKind, type SceneNode, type SceneTopology, type SceneWorker } from "./factory-scene/scene.js";

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

export type FactoryCounters = Readonly<{
  queued: number | undefined;
  needsYou: number | undefined;
}>;

export function factoryCounters(state: StateView | undefined): FactoryCounters {
  if (state === undefined) return { queued: undefined, needsYou: undefined };
  let queued = 0;
  for (const task of state.tasks.values()) {
    if (task.status === "queued") queued += 1;
  }
  return { queued, needsYou: state.humanRequests.size };
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

/** One viewed hierarchy scope holds this many rooms. */
export const MAX_SCOPE_ROOMS = 24;

export type FloorScene = Readonly<{
  topology: SceneTopology;
  workers: readonly SceneWorker[];
  tasks: readonly SceneTask[];
  omittedLocations: number;
  navigation: FloorNavigation;
}>;

export type FloorNavigation = Readonly<{
  /** Undefined is the all-projects landing scope. */
  scopeId?: string;
  /** The immediate ancestor for Back; breadcrumbs may jump further. */
  backScopeId?: string;
  breadcrumbs: readonly Readonly<{ id?: string; label: string }>[];
  enterableIds: readonly string[];
  omittedChildren: number;
  outsideScopeActivity: number;
  hiddenScopeActivity: number;
  page: number;
  pageCount: number;
}>;

/** A served task and its observed footprint, without inferring any execution detail. */
export type SceneTask = Readonly<{
  id: string;
  agentId: string;
  projectId: string;
  title: string;
  status: TaskItem["status"];
  roomIds: readonly string[];
  representativeRoomId?: string;
  /** Derived display only; exact observed rooms above remain unchanged. */
  displayRoomIds?: readonly string[];
  displayRoomId?: string;
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
}>;

/** Disposable hierarchy and dependency indexes, rebuilt only for new source facts. */
export function prepareFloor(projectMap: StateView["projects"] | undefined, topologies: ReadonlyMap<string, TopologyView> | undefined) {
  const projects = projectMap === undefined ? [] : [...projectMap.values()].sort((left, right) => compareText(left.name, right.name) || compareText(left.id, right.id));
  const hierarchies = projects.map((project) => projectHierarchy(project, topologies?.get(project.id)));
  const blocksByProject = new Map(hierarchies.map((hierarchy) => [hierarchy.project.id, hierarchy.nodes]));
  const roomByID = new Map(hierarchies.flatMap((hierarchy) => hierarchy.nodes).map((room) => [room.id, room]));
  const children = new Map<string, SceneNode[]>();
  for (const room of roomByID.values()) {
    if (room.parentId !== undefined) children.set(room.parentId, [...(children.get(room.parentId) ?? []), room]);
  }
  for (const members of children.values()) members.sort((left, right) => compareText(left.id, right.id));
  const digest = projects.map((project) => topologies?.get(project.id)?.digest).filter((value) => value !== undefined).join(" ");
  return { hierarchies, blocksByProject, roomByID, children, digest };
}

/** Scope and page select a stable room map independently of live activity. */
export function selectFloor(prepared: ReturnType<typeof prepareFloor>, scopeId?: string, requestedPage = 0) {
  const { hierarchies, blocksByProject, roomByID, children } = prepared;
  const validScope = scopeId !== undefined && roomByID.has(scopeId) ? scopeId : undefined;
  const scope = validScope === undefined ? undefined : roomByID.get(validScope)!;
  const scopeChildren = scope === undefined ? hierarchies.map((hierarchy) => hierarchy.projectRoom) : children.get(scope.id) ?? [];
  const showScope = scope !== undefined && !scopeChildren.some((child) => child.path === scope.path);
  const roomLimit = MAX_SCOPE_ROOMS - (showScope ? 1 : 0);
  const pageCount = Math.max(1, Math.ceil(scopeChildren.length / roomLimit));
  const page = scopeId !== validScope || !Number.isFinite(requestedPage) ? 0 : Math.max(0, Math.min(pageCount - 1, Math.floor(requestedPage)));
  const pageChildren = scopeChildren.slice(page * roomLimit, (page + 1) * roomLimit);
  // Counts belong to physical paths. A same-path child represents a hidden
  // wrapper's direct files when its sibling subtrees are displayed separately.
  const splitContents = scope !== undefined && scopeChildren.some((child) => child.path !== scope.path);
  const rooms: SceneNode[] = (showScope ? [scope, ...pageChildren] : pageChildren).map((room) => ({
    ...room,
    inventoryScope: splitContents && room.path === scope?.path ? "direct" : "subtree",
  }));
  const kept = new Set(rooms.map((room) => room.id));
  const visibleAncestor = (id: string | undefined): string | undefined => {
    let room = id === undefined ? undefined : roomByID.get(id);
    while (room !== undefined && !kept.has(room.id)) room = room.parentId === undefined ? undefined : roomByID.get(room.parentId);
    return room?.id;
  };
  const inScope = (id: string): boolean => {
    if (validScope === undefined) return true;
    let room = roomByID.get(id);
    while (room !== undefined && room.id !== validScope) room = room.parentId === undefined ? undefined : roomByID.get(room.parentId);
    return room !== undefined;
  };
  const crumbs: Array<{ id?: string; label: string }> = [{ label: "All projects" }];
  if (scope !== undefined) {
    const chain: SceneNode[] = [];
    const seen = new Set<string>();
    let current: SceneNode | undefined = scope;
    // Topology frames validate node fields but containment still arrives from
    // outside this projection. A malformed parent cycle degrades to the
    // project fallback below; keep this bound as the final UI-side guard.
    while (current !== undefined && !seen.has(current.id)) {
      seen.add(current.id);
      chain.unshift(current);
      current = current.parentId === undefined ? undefined : roomByID.get(current.parentId);
    }
    crumbs.push(...chain.map((node) => ({ id: node.id, label: node.label })));
  }
  const digest = `${prepared.digest}:${validScope ?? "root"}:${page}`;
  return {
    blocksByProject, roomByID, kept, visibleAncestor, inScope,
    topology: { digest, nodes: rooms },
    navigation: {
      ...(validScope === undefined ? {} : { scopeId: validScope }),
      ...(scope === undefined ? {} : { backScopeId: scope.parentId }),
      breadcrumbs: crumbs,
      enterableIds: rooms.filter((room) => room.id !== validScope && (children.get(room.id)?.length ?? 0) > 0).map((room) => room.id),
      omittedChildren: Math.max(0, scopeChildren.length - pageChildren.length),
      page, pageCount,
    },
  };
}

/** Live task and observed-path projection reuses the selected static hierarchy. */
export function projectFloor(state: StateView | undefined, selected: ReturnType<typeof selectFloor>, runPaths?: ReadonlyMap<string, RunPathSample>, lastRunPaths?: ReadonlyMap<string, RunPathSample>): FloorScene {
  const { blocksByProject, roomByID, kept, visibleAncestor, inScope } = selected;
  const liveRooms = new Set<string>();
  const tasks = state === undefined ? [] : [...state.tasks.values()]
		.filter((task) => !state.agents.get(task.assigned_agent_id)?.archived)
    .sort((left, right) => compareText(left.id, right.id))
    .map((task) => {
      const sample = matchingRunSample(state, task, runPaths);
      const footprint = runFootprint(blocksByProject.get(task.project_id) ?? [], sample);
      const displayRoomIds = [...new Set(footprint.roomIds.map(visibleAncestor).filter((id): id is string => id !== undefined))];
      const displayRoomId = visibleAncestor(footprint.representativeRoomId) ?? displayRoomIds[0];
      return {
        id: task.id,
        agentId: task.assigned_agent_id,
        projectId: task.project_id,
        title: task.title,
        status: task.status,
        roomIds: footprint.roomIds,
        ...(footprint.representativeRoomId === undefined ? {} : { representativeRoomId: footprint.representativeRoomId }),
        ...(sample === undefined ? {} : {
          observation: { taskRevision: sample.taskRevision, runId: sample.runId },
          displayRoomIds,
          ...(displayRoomId === undefined ? {} : { displayRoomId }),
        }),
        humanRequestIds: [...state.humanRequests.values()]
          .filter((request) => request.task_id === task.id && request.agent_id === task.assigned_agent_id && request.project_id === task.project_id)
          .sort((left, right) => compareText(left.id, right.id))
          .map((request) => request.id),
      };
    });
  const workByTask = new Map(tasks.map((order) => [order.id, order]));
  const workers = state === undefined ? [] : [...state.agents.values()].filter((agent) => !agent.archived).map((agent) => {
    const task = agentCurrentTask(agent, state);
    const block = blocksByProject.get(agent.project_id) ?? [];
    const live = task === undefined ? undefined : workByTask.get(task.id)?.representativeRoomId;
    const previous = lastRunPaths?.get(agent.id);
    const last = previous?.projectId === agent.project_id && previous.paths.length > 0 ? runFootprint(block, previous).representativeRoomId : undefined;
    const work = task === undefined ? undefined : workByTask.get(task.id);
    const display = work?.displayRoomId;
    const displayedObservation = display === undefined || visibleAncestor(live) === display ? live
      : work?.roomIds.find((id) => visibleAncestor(id) === display);
    let observedBayId: string | undefined;
    if (selected.navigation.scopeId !== undefined && display !== undefined && displayedObservation !== undefined) {
      const pictured = roomByID.get(displayedObservation);
      if (pictured?.parentId === display) observedBayId = pictured.id;
    }
    if (live !== undefined) liveRooms.add(display ?? live);
    const location: SceneWorker["location"] = task === undefined ? last === undefined ? "resting" : "last-observed" : live !== undefined ? "working" : "unobserved";
    const room = location === "working" ? roomByID.get(displayedObservation!) : location === "last-observed" ? roomByID.get(last!) : undefined;
    return {
      id: agent.id,
      name: agent.name,
      role: agent.role,
      provider: agent.provider,
      ...(agent.appearance === undefined ? {} : { appearance: agent.appearance }),
      activity: agentActivity(agent, state),
      paused: agent.paused,
      location,
      ...(room === undefined ? {} : { locationLabel: room.label }),
      ...(location === "working" && live !== undefined ? {
        nodeId: display ?? live,
        locationWithin: display !== undefined && display !== displayedObservation,
        // An ancestor summary is not evidence that an arbitrary descendant is
        // pictured. Only its exact direct child may receive a named bay.
        // The all-projects overview is a subtree preview, not a second child
        // hierarchy. Named bays begin only after entering a served scope.
        ...(observedBayId === undefined ? {} : { observedBayId }),
      } : {}),
    };
  });
  const observedTasks = tasks.filter((task) => task.status === "running" && task.roomIds.length > 0);
  const outsideScopeActivity = observedTasks.filter((task) => !task.roomIds.some(inScope)).length;
  const hiddenScopeActivity = observedTasks.filter((task) => task.roomIds.some(inScope) && task.displayRoomIds?.length === 0).length;
  return {
    topology: selected.topology, workers, tasks,
    omittedLocations: [...liveRooms].filter((id) => !kept.has(id)).length,
    navigation: { ...selected.navigation, outsideScopeActivity, hiddenScopeActivity },
  };
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

/**
 * Each changed path picks the deepest eligible room whose own path prefixes it
 * (the root's "." prefixes everything). Same-path kinds follow daemon NodeForPath
 * precedence; stable ids settle equivalent rooms. Every affected room is retained, while
 * the room holding the most paths is the representative; ties use room order.
 */
function runFootprint(rooms: readonly SceneNode[], sample: RunPathSample | undefined): Readonly<{ roomIds: readonly string[]; representativeRoomId?: string }> {
  const kindOrder = { repository: 0, directory: 1, module: 2, package: 3 };
  const counts = new Map<SceneNode, number>();
  for (const path of sample?.paths ?? []) {
    const room = rooms
      .filter((candidate) => candidate.path === "." || path === candidate.path || path.startsWith(`${candidate.path}/`))
      .sort((left, right) => right.path.length - left.path.length || kindOrder[right.kind] - kindOrder[left.kind] || compareText(left.id, right.id))[0];
    if (room !== undefined) counts.set(room, (counts.get(room) ?? 0) + 1);
  }
  const representativeRoomId = [...counts]
    .sort(([left, leftCount], [right, rightCount]) => rightCount - leftCount || compareText(left.path, right.path) || compareText(left.id, right.id))[0]?.[0].id;
  return {
    roomIds: [...counts.keys()].sort((left, right) => compareText(left.path, right.path) || compareText(left.id, right.id)).map((room) => room.id),
    ...(representativeRoomId === undefined ? {} : { representativeRoomId }),
  };
}

/** All containment comes from served parent ids; paths and labels are display/activity data only. */
function projectHierarchy(project: { id: string; name: string }, topology: TopologyView | undefined) {
  const served = topology?.nodes ?? [];
  const servedByID = new Map<string, typeof served[number]>();
  const unique = served.length <= MAX_SNAPSHOT_ENTITIES && served.every((node) => !servedByID.has(node.id) && (servedByID.set(node.id, node), true));
  // ponytail: this walks at most the protocol's 4,096 served nodes per node;
  // a future larger graph should validate containment once at decode time.
  const valid = served.length > 0 && unique
    && served.every((node) => node.parent_id === "" || servedByID.has(node.parent_id))
    && served.every((node) => {
      const seen = new Set<string>();
      let current = node;
      while (current.parent_id !== "") {
        if (seen.has(current.id)) return false;
        seen.add(current.id);
        const parent = servedByID.get(current.parent_id);
        if (parent === undefined) return false;
        current = parent;
      }
      return true;
    });
  const fallback: SceneNode = { id: project.id, path: project.name, label: project.name, kind: "repository", inventoryScope: "subtree", project: { id: project.id, name: project.name } };
  if (!valid) return { project, projectRoom: fallback, nodes: [fallback] };
  const roots = served.filter((node) => node.parent_id === "");
  // The daemon serves one repository root. If that root is unavailable or a
  // malformed graph offers several roots, keep the honest unavailable room
  // instead of manufacturing a containment edge from project text.
  if (roots.length !== 1) return { project, projectRoom: fallback, nodes: [fallback] };
  const root = roots[0]!;
  const componentLabel = (node: typeof root) => node.kind === "package" && node.label === "main" && node.path !== "." ? node.path.split("/").at(-1)! : node.label;
  const components = new Map<string, NonNullable<SceneNode["components"]>[number][]>();
  for (const node of served) if (node.parent_id !== "") {
    const feature = node.inventory === undefined ? "unavailable" : (Object.keys(inventoryLabels) as InventoryKind[])
      .filter((kind) => node.inventory!.total[kind] > 0)
      .sort((left, right) => node.inventory!.total[right] - node.inventory!.total[left] || compareText(left, right))[0] ?? "empty";
    components.set(node.parent_id, [...(components.get(node.parent_id) ?? []), { id: `${project.id}:${node.id}`, label: componentLabel(node), feature }]);
  }
  for (const children of components.values()) children.sort((a, b) => compareText(a.id, b.id));
  const links = new Map<string, NonNullable<SceneNode["dependencies"]>["links"][number][]>();
  for (const edge of topology?.dependencies?.edges ?? []) {
    // Decoder owns endpoint validation; retain project scoping for direct projections too.
    const from = servedByID.get(edge.from), to = servedByID.get(edge.to);
    if (from === undefined || to === undefined) continue;
    for (const [owner, target, direction] of [[from, to, "to"], [to, from, "from"]] as const) {
      const entry = { nodeId: `${project.id}:${target.id}`, label: target.id === root.id ? project.name : target.label, path: target.path, direction, weight: edge.weight };
      links.set(owner.id, [...(links.get(owner.id) ?? []), entry]);
    }
  }
  const nodes = served.map((node) => ({
    id: `${project.id}:${node.id}`,
    ...(node.parent_id === "" ? {} : { parentId: `${project.id}:${node.parent_id}` }),
    path: node.path,
    label: node.id === root.id ? project.name : componentLabel(node),
    kind: node.kind,
    sizeBucket: node.size_bucket,
    language: node.language,
    inventoryScope: "subtree" as const,
    childCount: components.get(node.id)?.length ?? 0,
    components: components.get(node.id) ?? [],
    ...(node.inventory === undefined ? {} : { inventory: node.inventory }),
    ...(topology?.dependencies === undefined ? {} : { dependencies: { omitted: topology.dependencies.omitted, links: links.get(node.id) ?? [] } }),
    project: { id: project.id, name: project.name },
  }));
  return { project, projectRoom: nodes.find((node) => node.id === `${project.id}:${root.id}`)!, nodes };
}
