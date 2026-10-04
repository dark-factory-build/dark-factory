import { productionKey, proposedProduction, type ProductionContraption } from "./production-view.js";
import { MAX_SNAPSHOT_ENTITIES, type AgentItem, type StateView, type TaskItem, type TopologyView } from "@dark-factory/client";
import { compareText, inventoryLabels, type InventoryKind, type SceneNode, type SceneTopology, type SceneWorker, type SceneProposal } from "./factory-scene/scene.js";

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

/** Flat grouping is presentation only; canonical source IDs never depend on detail. */
export type FloorDetail = "coarse" | "auto" | "fine";
export const MAX_FLOOR_ROOMS = 96;
export type FloorScene = Readonly<{
  topology: SceneTopology;
  workers: readonly SceneWorker[];
  tasks: readonly SceneTask[];
  omittedLocations: number;
  aggregatedLocations: number;
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
  for (const members of children.values()) members.sort((left, right) => compareText(left.path, right.path) || compareText(left.label, right.label) || compareText(left.id, right.id));
  for (const room of roomByID.values()) {
    const members = children.get(room.id) ?? [];
    roomByID.set(room.id, { ...room, childCount: members.length, components: members.map((node) => ({ id: node.id, label: node.label, feature: node.inventory === undefined ? "unavailable" : (Object.keys(inventoryLabels) as InventoryKind[])
      .filter((kind) => node.inventory!.total[kind] > 0)
      .sort((left, right) => node.inventory!.total[right] - node.inventory!.total[left] || compareText(left, right))[0] ?? "empty" })) });
  }
  const digest = projects.map((project) => topologies?.get(project.id)?.digest).filter((value) => value !== undefined).join(" ");
  return { hierarchies, blocksByProject, roomByID, children, digest };
}

const kindOrder = { repository: 0, directory: 1, module: 2, package: 3 };
const countFiles = (node: SceneNode) => Object.values(node.inventory?.direct ?? {}).reduce((sum, count) => sum + count, 0);
const emptyCounts = () => ({ source: 0, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 });

/** Every physical path has exactly one owner; same-path wrappers remain aliases. */
export function selectFloor(prepared: ReturnType<typeof prepareFloor>, detail: FloorDetail = "auto") {
  const { hierarchies, blocksByProject, roomByID } = prepared;
  const owners = new Map<string, SceneNode>();
  for (const node of roomByID.values()) {
    const key = `${node.project?.id}:${node.path}`, old = owners.get(key);
    if (old === undefined || kindOrder[node.kind] > kindOrder[old.kind]
      || kindOrder[node.kind] === kindOrder[old.kind] && node.id < old.id) owners.set(key, node);
  }
  const canonical = [...owners.values()].sort((a, b) => compareText(a.project?.id ?? "", b.project?.id ?? "") || compareText(a.path, b.path) || compareText(a.id, b.id));
  const canonicalOf = (node: SceneNode) => owners.get(`${node.project?.id}:${node.path}`)!;
  const parentOf = (node: SceneNode): SceneNode | undefined => {
    let parent = node.parentId === undefined ? undefined : roomByID.get(node.parentId);
    while (parent !== undefined && parent.path === node.path) parent = parent.parentId === undefined ? undefined : roomByID.get(parent.parentId);
    return parent === undefined ? undefined : canonicalOf(parent);
  };
  const children = new Map<string, SceneNode[]>();
  for (const node of canonical) {
    const parent = parentOf(node);
    if (parent !== undefined) children.set(parent.id, [...children.get(parent.id) ?? [], node]);
  }
  const detailByID = new Map(canonical.map((node) => {
    const links = new Map<string, NonNullable<SceneNode["dependencies"]>["links"][number]>();
    for (const alias of roomByID.values()) {
      if (canonicalOf(alias).id !== node.id) continue;
      for (const link of alias.dependencies?.links ?? []) {
        const target = roomByID.get(link.nodeId);
        if (!target || canonicalOf(target).id === node.id) continue;
        const owner = canonicalOf(target), key = `${link.direction}:${owner.id}`;
        links.set(key, { ...link, nodeId: owner.id, label: owner.label, weight: (links.get(key)?.weight ?? 0) + link.weight });
      }
    }
    const members = children.get(node.id) ?? [];
    return [node.id, { ...node, childCount: members.length, components: members.map((member) => ({ id: member.id, label: member.label })),
      ...(node.dependencies === undefined ? {} : { dependencies: { ...node.dependencies, links: [...links.values()] } }),
    }] as const;
  }));
  const rootIds = new Set(hierarchies.map((hierarchy) => canonicalOf(hierarchy.projectRoom).id));
  // Unwrap short namespace chains, not broad areas such as Go's internal/.
  // A wide namespace remains a room containing its packages as assemblies.
  const wrapper = (node: SceneNode) => ["internal", "src", "lib", "packages", "apps", "web"].includes(node.path.split("/").at(-1)!)
    && (children.get(node.id)?.length ?? 0) > 0 && children.get(node.id)!.length <= 4;
  const candidates = canonical.filter((node) => {
    if (rootIds.has(node.id)) return true;
    if (detail === "fine") return true;
    const parent = parentOf(node);
    if (detail === "coarse") return parent !== undefined && rootIds.has(parent.id);
    if (wrapper(node)) return false;
    if (Object.values(node.inventory?.total ?? {}).reduce((sum, count) => sum + count, 0) <= 4 && node.inventory !== undefined && !(children.get(node.id)?.length)) return false;
    // Large direct packages warrant their own bay; this uses integrated source,
    // never proposal counts or live worker activity.
    if (node.kind === "package" && parent?.path.split("/").at(-1) === "internal" && countFiles(node) > 80) return true;
    let ancestor = parent;
    while (ancestor !== undefined && !rootIds.has(ancestor.id)) {
      if (!wrapper(ancestor)) return false;
      ancestor = parentOf(ancestor);
    }
    return true;
  });
  // Keep project roots, then stable path order. Overflow still belongs to a
  // visible ancestor and stays searchable; activity never chooses room order.
  const kept = new Set([...new Set([...rootIds, ...candidates.map((node) => node.id)])].slice(0, Math.max(MAX_FLOOR_ROOMS, rootIds.size)));
  const visibleAncestor = (id: string | undefined): string | undefined => {
    const source = id === undefined ? undefined : roomByID.get(id);
    let node = source === undefined ? undefined : canonicalOf(source);
    while (node !== undefined && !kept.has(node.id)) node = parentOf(node);
    return node?.id;
  };
  const members = new Map<string, SceneNode[]>();
  for (const node of canonical) {
    const room = visibleAncestor(node.id);
    if (room !== undefined) members.set(room, [...members.get(room) ?? [], node]);
  }
  const references = new Map<string, SceneNode[]>();
  for (const node of roomByID.values()) {
    const room = visibleAncestor(node.id);
    if (room !== undefined) references.set(room, [...references.get(room) ?? [], node]);
  }
  const rooms: SceneNode[] = canonical.filter((node) => kept.has(node.id)).map((node) => {
    const owned = members.get(node.id) ?? [];
    const total = emptyCounts();
    for (const member of owned) for (const kind of Object.keys(total) as InventoryKind[]) total[kind] += member.inventory?.direct[kind] ?? 0;
    const links = new Map<string, NonNullable<SceneNode["dependencies"]>["links"][number]>();
    for (const member of references.get(node.id) ?? []) for (const link of member.dependencies?.links ?? []) {
      const targetID = visibleAncestor(link.nodeId);
      if (targetID === undefined || targetID === node.id) continue;
      const key = `${link.direction}:${targetID}`, target = roomByID.get(targetID)!;
      links.set(key, { ...link, nodeId: targetID, label: target.label, path: target.path, weight: (links.get(key)?.weight ?? 0) + link.weight });
    }
    // A package includes its tests, manifests and manuals. Keep nested functional
    // packages and substantial source directories as named assemblies, instead of
    // giving every resource folder a separate workstation.
    const assemblyOwners = new Set(owned.filter((member) => member.id === node.id || member.kind === "package"
      || !["src", "lib", "scripts"].includes(member.path.split("/").at(-1)!) && (member.inventory?.direct.source ?? 0) > 4).map((member) => member.id));
    const ownedIds = new Set(owned.map((member) => member.id));
    const assemblyGroups = new Map<string, SceneNode[]>();
    for (const member of owned) {
      let owner = member;
      while (!assemblyOwners.has(owner.id)) {
        const parent = parentOf(owner);
        if (!parent || !ownedIds.has(parent.id)) { owner = node; break; }
        owner = parent;
      }
      assemblyGroups.set(owner.id, [...assemblyGroups.get(owner.id) ?? [], member]);
    }
    return {
      ...node, label: node.path === "." ? node.project?.name ?? node.label : node.path, inventoryScope: "subtree",
      inventory: owned.every((member) => member.inventory !== undefined) ? { ...node.inventory!, total } : undefined,
      assemblies: [...assemblyGroups].flatMap(([id, group]) => {
        const member = detailByID.get(id)!;
        const total = emptyCounts();
        for (const source of group) for (const kind of Object.keys(total) as InventoryKind[]) total[kind] += source.inventory?.direct[kind] ?? 0;
        if (!Object.values(total).some(Boolean) && group.every((source) => source.inventory !== undefined)) return [];
        const samples = group.flatMap((source) => (source.inventory?.samples ?? []).map((sample) => source.path === member.path ? sample : `${source.path.slice(member.path === "." ? 0 : member.path.length + 1)}/${sample}`)).slice(0, 32);
        return [{ id, path: member.path, label: member.path === "." ? "Repository files" : member.path === node.path ? member.path.split("/").at(-1)! : member.path.slice(node.path === "." ? 0 : node.path.length + 1),
          inventoryScope: "subtree" as const,
          inventory: group.some((source) => source.inventory === undefined) ? undefined : { direct: member.inventory!.direct, total, samples, samples_omitted: Math.max(0, Object.values(total).reduce((sum, count) => sum + count, 0) - samples.length) },
          sizeBucket: member.sizeBucket, representedIds: group.map((source) => source.id), dependencies: member.dependencies,
        }];
      }),
      ...(node.dependencies === undefined ? {} : { dependencies: { ...node.dependencies, links: [...links.values()] } }),
    };
  });
  return { blocksByProject, roomByID, detailByID, kept, visibleAncestor, canonical,
    topology: { digest: `${prepared.digest}:${detail}`, nodes: rooms },
    aggregatedLocations: Math.max(0, candidates.length - kept.size),
  };
}

/** Live task and observed-path projection reuses the selected static hierarchy. */
export function projectFloor(state: StateView | undefined, selected: ReturnType<typeof selectFloor>, runPaths?: ReadonlyMap<string, RunPathSample>, lastRunPaths?: ReadonlyMap<string, RunPathSample>): FloorScene {
  const { blocksByProject, roomByID, kept, visibleAncestor } = selected;
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
    if (display !== undefined && displayedObservation !== undefined) observedBayId = displayedObservation;
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
      ...(location === "last-observed" && last !== undefined ? { nodeId: visibleAncestor(last) } : {}),
      ...(location === "working" && live !== undefined ? {
        nodeId: display ?? live,
        locationWithin: display !== undefined && display !== displayedObservation,
        ...(observedBayId === undefined ? {} : { observedBayId }),
      } : {}),
    };
  });
  return {
    topology: selected.topology, workers, tasks,
    omittedLocations: [...liveRooms].filter((id) => !kept.has(id)).length,
    aggregatedLocations: selected.aggregatedLocations,
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
  const fallback: SceneNode = { id: project.id, path: ".", label: project.name, kind: "repository", inventoryScope: "subtree", project: { id: project.id, name: project.name } };
  if (!valid) return { project, projectRoom: fallback, nodes: [fallback] };
  const roots = served.filter((node) => node.parent_id === "");
  // The daemon serves one repository root. If that root is unavailable or a
  // malformed graph offers several roots, keep the honest unavailable room
  // instead of manufacturing a containment edge from project text.
  if (roots.length !== 1) return { project, projectRoom: fallback, nodes: [fallback] };
  const root = roots[0]!;
  const componentLabel = (node: typeof root) => node.kind === "package" && node.label === "main" && node.path !== "." ? node.path.split("/").at(-1)! : node.label;
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
    ...(node.inventory === undefined ? {} : { inventory: node.inventory }),
    ...(topology?.dependencies === undefined ? {} : { dependencies: { omitted: topology.dependencies.omitted, links: links.get(node.id) ?? [] } }),
    project: { id: project.id, name: project.name },
  }));
  return { project, projectRoom: nodes.find((node) => node.id === `${project.id}:${root.id}`)!, nodes };
}

/** Project each proposal independently; never manufacture a combined future tree. */
export function projectProposals(selected: ReturnType<typeof selectFloor>, items: readonly ProductionContraption[]) {
  const provisional = new Map<string, SceneNode>();
  const roomForPath = (project: string, path: string) => selected.canonical
    .filter((node) => node.project?.id === project && (node.path === "." || path === node.path || path.startsWith(`${node.path}/`)))
    .sort((a, b) => b.path.length - a.path.length || compareText(a.id, b.id))[0];
  const proposals: SceneProposal[] = items.filter(proposedProduction).map((item) => {
    const source = item.source;
    const operations = source.paths.map((path) => {
      const before = roomForPath(item.projectId, path.old_path ?? path.path);
      const destination = roomForPath(item.projectId, path.path);
      const directory = path.path.includes("/") ? path.path.slice(0, path.path.lastIndexOf("/")) : ".";
      let roomId = selected.visibleAncestor(destination?.id);
      let entityId = before?.id;
      if (["added", "renamed"].includes(path.status) && destination !== undefined && directory !== destination.path) {
        roomId = `${item.projectId}:proposed:${directory}`;
        const id = `${roomId}@${productionKey(item)}`, existing = provisional.get(roomId);
        if (!existing?.assemblies?.some((assembly) => assembly.id === id)) {
          const paths = source.paths.filter((entry) => ["added", "renamed"].includes(entry.status) && entry.path.slice(0, entry.path.lastIndexOf("/")) === directory);
          const counts = emptyCounts();
          for (const entry of paths) counts[entry.resource ?? "unclassified"]++;
          provisional.set(roomId, { id: roomId, path: directory, label: directory, kind: "directory", project: destination.project, inventoryScope: "direct", proposed: true, assemblies: [...existing?.assemblies ?? [], { id, proposalId: productionKey(item), path: directory, label: directory.split("/").at(-1)!, inventoryScope: "direct", representedIds: [roomId], sourceIncomplete: true, sourcePaths: paths.map((entry) => entry.path), inventory: { direct: counts, total: counts, samples: paths.map((entry) => entry.path.split("/").at(-1)!), samples_omitted: source.omitted } }] });
        }
        if (path.status === "added") entityId = roomId;
      }
      return { entityId, roomId, path: path.path, previousPath: path.old_path,
        kind: ({ added: "addition", modified: "modification", deleted: "removal", renamed: "move" } as const)[path.status] };
    });
    return { id: productionKey(item), title: item.pullRequest?.title || item.construction?.title || "Proposed change",
      state: source.kind === "unavailable" ? "unavailable" : source.stale ? "stale" : "active", base: source.base, head: source.head, operations,
      relationships: source.relationships.map((edge) => ({ status: edge.status, fromPath: edge.from_path, toPath: edge.to_path, weight: edge.weight,
        fromEntityId: provisional.get(`${item.projectId}:proposed:${edge.from_path}`)?.id ?? roomForPath(item.projectId, edge.from_path)?.id,
        toEntityId: provisional.get(`${item.projectId}:proposed:${edge.to_path}`)?.id ?? roomForPath(item.projectId, edge.to_path)?.id,
        fromId: provisional.get(`${item.projectId}:proposed:${edge.from_path}`)?.id ?? selected.visibleAncestor(roomForPath(item.projectId, edge.from_path)?.id),
        toId: provisional.get(`${item.projectId}:proposed:${edge.to_path}`)?.id ?? selected.visibleAncestor(roomForPath(item.projectId, edge.to_path)?.id),
      })),
    };
  });
  const reviewers = new Map<string, SceneWorker>();
  for (const item of items.filter(proposedProduction)) {
    const proposal = proposals.find((candidate) => candidate.id === productionKey(item))!;
    const operation = proposal.operations.find((candidate) => candidate.roomId !== undefined);
    for (const actor of item.reviewers) {
      const id = `review:${item.projectId}:${item.repository}:${actor.id}`;
      if (reviewers.has(id)) continue;
      const working = actor.state === "running" && proposal.state === "active" && actor.head === proposal.head && operation !== undefined;
      reviewers.set(id, { id, name: actor.name || actor.id, role: "worker", activity: working ? "busy" : actor.state === "waiting" ? "waiting" : "idle",
        location: working ? "working" : "resting", nodeId: working ? operation.roomId : undefined, observedBayId: working ? operation.entityId : undefined,
        review: { proposalId: proposal.id, scope: `Review assignment at ${actor.head || "unknown head"}; ${proposal.operations.length} observed paths. Individual file inspection is not observed.` } });
    }
  }
  const addedRooms = [...provisional.values()].sort((a, b) => compareText(a.id, b.id)).slice(0, 16);
  const addedIDs = new Set(addedRooms.map((room) => room.id));
  const roomFallback = new Map([...provisional.values()].filter((room) => !addedIDs.has(room.id)).map((room) => [room.id, selected.visibleAncestor(roomForPath(room.project!.id, room.path)?.id)]));
  for (const proposal of proposals) for (const operation of proposal.operations) {
    if (operation.roomId !== undefined && roomFallback.has(operation.roomId)) Object.assign(operation, { roomId: roomFallback.get(operation.roomId) });
  }
  for (const proposal of proposals) for (const edge of proposal.relationships ?? []) {
    if (edge.fromId && roomFallback.has(edge.fromId)) Object.assign(edge, { fromId: roomFallback.get(edge.fromId) });
    if (edge.toId && roomFallback.has(edge.toId)) Object.assign(edge, { toId: roomFallback.get(edge.toId) });
  }
  const actors = [...reviewers.values()].map((actor) => actor.nodeId !== undefined && roomFallback.has(actor.nodeId) ? { ...actor, nodeId: roomFallback.get(actor.nodeId) } : actor);
  return { topology: { ...selected.topology, nodes: [...selected.topology.nodes, ...addedRooms] }, proposals, reviewers: actors, aggregatedProposals: provisional.size - addedRooms.length };
}
