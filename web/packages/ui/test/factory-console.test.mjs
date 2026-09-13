import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { createElement, isValidElement, useEffect, useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { MAX_TASK_PRIORITY, ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryApp, FactoryConsole, floorScene } from "../dist/src/index.js";
import { layoutScene } from "../dist/src/factory-scene/scene.js";
import { TerminalPanel } from "../dist/src/factory-app.js";
import { fixtureState, fixtureTopologies, fixtureTopology } from "../../../fixtures/state.mjs";

const ids = {
  project: [...fixtureState.projects.keys()][0],
  secondProject: [...fixtureState.projects.keys()][1],
  agent: [...fixtureState.agents.keys()][0],
  orchestrator: [...fixtureState.agents.keys()][1],
  idleAgent: [...fixtureState.agents.keys()][2],
  task: [...fixtureState.tasks.keys()][0],
  request: [...fixtureState.humanRequests.keys()][0],
};

const baseState = (overrides = {}) => ({ ...fixtureState, ...overrides });

const render = (props = {}) => renderToStaticMarkup(createElement(FactoryConsole, {
  status: "ready",
  state: baseState(),
  ...props,
}));

const VIEWS = ["floor", "agents"];

const agentSelection = (id = ids.agent) => {
  const agent = fixtureState.agents.get(id);
  return { id: agent.id, name: agent.name, revision: agent.revision };
};

const selectedRequest = (overrides = {}) => ({
  request: fixtureState.humanRequests.get(ids.request),
  phase: "ready",
  question: "Proceed with the migration?",
  options: [],
  canReply: true,
  canCancel: true,
  replyMaxBytes: 8192,
  reply: "",
  ...overrides,
});

const runSample = (agentId, paths, taskId = ids.task, taskRevision = fixtureState.tasks.get(taskId)?.revision ?? 1n) => ({
  taskId,
  taskRevision,
  runId: "71".repeat(16),
  projectId: fixtureState.agents.get(agentId).project_id,
  paths,
});

test("error banner keeps its centered layout after the paragraph reset", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /\.dfFactoryConsole :where\(h1, h2, p, dl, ul\),[\s\S]*?\.dfConsoleSidebar :where\(h1, h2, h3, p, dl, ul\)\s*\{\s*margin: 0;\s*\}/);
  assert.match(css, /\.dfFactoryConsole__error\s*\{[\s\S]*?margin: 0 auto 1\.25rem;/);
  assert.match(css, /\.dfFactoryFloor__scene \{ overflow-x: auto; \}/);
  assert.equal(css.includes("@keyframes dfFactoryScene"), false);
});

test("one screen keeps Factory and the operator panels together", () => {
  const markup = render();
  assert.match(markup, /<main class="dfFactoryConsole" aria-label="Factory operator console">/);
  for (const label of ["Factory counters", "Factory floor", "Selected detail", "NEEDS YOU", "Left view", "Right panel"]) {
    assert.match(markup, new RegExp(`aria-label="${label}"`));
  }
  // Counters read the served factory, not a second count of it.
  assert.match(markup, /<dt>ACTIVE RUNS<\/dt><dd>2<\/dd>/);
  assert.equal(markup.includes("<dt>QUEUED</dt>"), false);
  assert.equal(markup.includes("<dt>NEEDS YOU</dt>"), false);
  assert.match(markup, /NEEDS YOU <span>1<\/span>/);
  assert.match(markup, /QUEUE <span>1<\/span>/);
  assert.match(markup, /Builder One asks/);
  assert.match(markup, /Review the state projection/);
  assert.match(markup, /North Workshop · Review the state projection/);
  assert.equal(markup.includes("DECISION NEEDED"), false, "an unopened request stays brief");
  assert.match(render({ detail: "queue" }), /aria-label="Queue"/);
  // No screen union survives: there is no navigation away from this screen.
  assert.equal(markup.includes("dfFactoryConsole__homeLink"), false);
  assert.equal(markup.includes("BUILDING STATE UNAVAILABLE"), false);
});

test("a selected decision names the action and keeps one collapse control", () => {
  const markup = render({ selectedHumanRequest: selectedRequest(), onCloseHumanRequest: () => {}, onReplyHumanRequest: () => {}, onCancelHumanRequest: () => {} });
  assert.match(markup, /<h3>DECISION NEEDED<\/h3>/);
  assert.match(markup, />STOP TASK<\/button>/);
  assert.equal(markup.includes(">CLOSE</button>"), false);
});

test("suggested answers fill the reply without sending it", () => {
  const calls = [];
  const elements = expand(FactoryConsole({ status: "ready", state: baseState(), selectedHumanRequest: selectedRequest({ options: ["Continue", "Stop"] }), onHumanReplyChange: (value) => calls.push(value) }));
  elements.find((element) => element.type === "button" && Array.isArray(element.props.children) && element.props.children[0] === "Continue").props.onClick();
  assert.deepEqual(calls, ["Continue"]);
  const markup = render({ selectedHumanRequest: selectedRequest({ options: ["Continue", "Stop"] }) });
  assert.match(markup, />Continue · RECOMMENDED<\/button>/);
  assert.match(markup, />Stop<\/button>/);
});

test("read-only decisions retain disabled suggestions and explain their status", () => {
  const open = render({ selectedHumanRequest: selectedRequest({ options: ["Keep accounts", "Include users"], canReply: false }) });
  assert.match(open, /aria-label="Suggested answers"/);
  assert.match(open, />Keep accounts · RECOMMENDED<\/button>/);
  assert.match(open, />Include users<\/button>/);
  assert.match(open, /<button type="button" disabled="">Keep accounts/);
  assert.match(open, /THIS OPEN DECISION IS READ-ONLY IN THIS VIEW\./);
  assert.equal(open.includes("YOUR ANSWER"), false);

  const deliveryUnknown = render({ selectedHumanRequest: selectedRequest({ request: { ...fixtureState.humanRequests.get(ids.request), status: "delivery_unknown" }, canReply: false }) });
  assert.match(deliveryUnknown, /THIS DECISION IS DELIVERY UNKNOWN\./);
});

test("the roster stays visible while the optional floor opens and closes", () => {
  const floor = render();
  assert.match(floor, /aria-label="Dark Factory codebase floor"/);

  const agents = render({ view: "agents" });
  assert.match(agents, /aria-label="Agents"/);
  assert.match(agents, /aria-label="OVERSEER"/);
  // Rank is the served role, oversight first, and nothing invents a new field.
  const overseer = agents.indexOf('aria-label="OVERSEER"');
  const worker = agents.indexOf('aria-label="WORKER"');
  assert.ok(overseer > -1 && worker > overseer);
  assert.ok(agents.indexOf("Dispatch Lead") < agents.indexOf("Builder One"));
  assert.match(agents, /Builder One[\s\S]*?claude_code[\s\S]*?needs you/);
  assert.match(agents, /Builder Two[\s\S]*?1 queued/);
  assert.equal(agents.includes("rank"), false);
});

test("viewed geometry is independent of worker activity and population", () => {
  const root = floorScene(fixtureState, fixtureTopologies).topology.nodes[0].id;
  const baseline = floorScene(fixtureState, fixtureTopologies, undefined, undefined, root);
  const extra = { ...fixtureState.agents.get(ids.idleAgent), id: "ab".repeat(16), name: "Extra resting worker" };
  const changed = { ...fixtureState, agents: new Map([...fixtureState.agents].reverse()).set(extra.id, extra) };
  const overlay = floorScene(changed, fixtureTopologies, new Map([[ids.agent, runSample(ids.agent, ["web"])]]), undefined, root);
  assert.deepEqual(overlay.topology, baseline.topology);
  assert.deepEqual(layoutScene(overlay.topology).rooms, layoutScene(baseline.topology).rooms);
  assert.deepEqual(new Set(overlay.workers.map((worker) => worker.id)), new Set(changed.agents.keys()));
  assert.equal(baseline.workers.find((worker) => worker.id === ids.agent).location, "unobserved");
  const web = baseline.topology.nodes.find((node) => node.path === "web").id;
  assert.equal(overlay.workers.find((worker) => worker.id === ids.agent).nodeId, web);
  const observed = floorScene(fixtureState, fixtureTopologies, undefined,
    new Map([[ids.idleAgent, runSample(ids.idleAgent, ["web"], "72".repeat(16))]]))
    .workers.find((worker) => worker.id === ids.idleAgent);
  assert.deepEqual([observed.location, observed.nodeId, observed.locationLabel], ["last-observed", undefined, "web"]);
  const fallback = floorScene(fixtureState, undefined);
  assert.equal(fallback.topology.nodes.length, fixtureState.projects.size);
  assert.ok(fallback.topology.nodes.every((node) => node.sizeBucket === undefined));
  assert.ok(fallback.workers.every((worker) => worker.nodeId === undefined));
  const empty = floorScene(undefined, undefined);
  assert.deepEqual([empty.topology.nodes, empty.workers, empty.tasks], [[], [], []]);
  assert.equal(empty.omittedLocations, 0);
});

test("floor tasks retain every served task and its exact observed footprint", () => {
  const repository = floorScene(fixtureState, fixtureTopologies).topology.nodes.find((node) => node.project?.id === ids.project);
  const scene = floorScene(fixtureState, fixtureTopologies, new Map([[ids.agent, runSample(ids.agent, ["web", "internal/kernel/store"])] ]), undefined, repository.id);
  const room = (path) => scene.topology.nodes.find((node) => node.path === path).id;
  const store = `${ids.project}:${fixtureTopology.nodes.find((node) => node.path === "internal/kernel/store").id}`;
  const web = room("web");
  assert.deepEqual(scene.tasks, [
    {
      id: ids.task,
      agentId: ids.agent,
      projectId: ids.project,
      title: "Review the state projection",
      status: "running",
      roomIds: [store, web],
      representativeRoomId: store,
      humanRequestIds: [ids.request],
    },
    {
      id: "32".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.secondProject,
      title: "Tighten the queue ordering",
      status: "queued",
      roomIds: [],
      humanRequestIds: [],
    },
    {
      id: "33".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.project,
      title: "Close the resize race",
      status: "succeeded",
      roomIds: [],
      humanRequestIds: [],
    },
    {
      id: "34".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.secondProject,
      title: "Probe the flaky gate",
      status: "failed",
      roomIds: [],
      humanRequestIds: [],
    },
  ]);
  assert.deepEqual(scene.workers.find((worker) => worker.id === ids.agent).nodeId, store);
});

test("hierarchy navigation is bounded, identity-led, and leaves outside work discoverable", () => {
  const landing = floorScene(fixtureState, fixtureTopologies);
  assert.deepEqual(landing.topology.nodes.map((node) => node.label), ["North Workshop", "South Workshop"]);
  assert.deepEqual(landing.topology.nodes.map((node) => node.sizeBucket), ["large", undefined], "a served root is the project landing; only unavailable structure uses the fallback");
  assert.deepEqual(landing.navigation.breadcrumbs, [{ label: "All projects" }]);
  const project = landing.topology.nodes.find((node) => node.id === ids.project);
  assert.equal(project, undefined, "a valid served root does not gain a synthetic project room");
  const repository = landing.topology.nodes.find((node) => node.project?.id === ids.project);
  const repositoryView = floorScene(fixtureState, fixtureTopologies, undefined, undefined, repository.id);
  const kernel = repositoryView.topology.nodes.find((node) => node.label === "kernel");
  assert.deepEqual(repositoryView.topology.nodes.map((node) => node.label), ["North Workshop", "kernel", "web"]);
  assert.ok(repositoryView.navigation.enterableIds.includes(kernel.id));
  assert.ok(!repositoryView.navigation.enterableIds.includes(repository.id), "the current scope has no no-op Enter action");
  const nested = floorScene(fixtureState, fixtureTopologies, undefined, undefined, kernel.id);
  assert.deepEqual(nested.topology.nodes.map((node) => node.label), ["kernel", "store"]);
  assert.ok(!nested.navigation.enterableIds.includes(kernel.id), "nested scopes also exclude their current room");
  assert.deepEqual(nested.navigation.breadcrumbs.map((crumb) => crumb.label), ["All projects", "North Workshop", "kernel"]);
  assert.equal(nested.navigation.backScopeId, repository.id, "Back is one level; breadcrumbs remain direct ancestor links");

  const misleading = served({ ...fixtureTopology, nodes: fixtureTopology.nodes.map((node) => ({ ...node, path: "unrelated", label: "same" })) });
  const misleadingRepository = floorScene(fixtureState, misleading, undefined, undefined,
    floorScene(fixtureState, misleading).topology.nodes.find((node) => node.project?.id === ids.project).id);
  assert.equal(misleadingRepository.navigation.enterableIds.length, 1, "served parent ids, not labels or paths, make the room navigable");

  const root = fixtureTopology.nodes[0];
  const topology = served({ ...fixtureTopology, nodes: [root, ...Array.from({ length: 30 }, (_, index) => ({ id: `${index}`.padStart(64, "0"), parent_id: root.id, kind: "directory", path: `path-${index}`, label: `Child ${index}`, language: "", size_bucket: "tiny" }))] });
  const scope = floorScene(fixtureState, topology).topology.nodes.find((node) => node.project?.id === ids.project);
  const task = { ...fixtureState.tasks.get(ids.task), revision: 99n };
  const scene = floorScene({ ...fixtureState, tasks: new Map([[task.id, task]]) }, topology,
    new Map([[ids.agent, runSample(ids.agent, ["path-29"], task.id, task.revision)]]), undefined, scope.id);
  assert.equal(scene.topology.nodes.length, 24);
  assert.deepEqual([scene.navigation.omittedChildren, scene.navigation.outsideScopeActivity, scene.omittedLocations], [7, 1, 1]);
  assert.equal(scene.workers.find((worker) => worker.id === ids.agent).location, "working");

  const before = layoutScene(repositoryView.topology);
  const after = layoutScene({ ...repositoryView.topology, nodes: [...repositoryView.topology.nodes, { id: "z", parentId: repository.id, path: "z", label: "Z", kind: "directory", project: repository.project }] });
  for (const room of before.rooms) assert.deepEqual(after.rooms.find((candidate) => candidate.id === room.id), room);

});

test("malformed served containment degrades without cycles or invented parent links", () => {
  const root = fixtureTopology.nodes[0];
  const loop = { ...root, id: "e5".repeat(32), parent_id: "f6".repeat(32), path: "loop", label: "Loop" };
  const back = { ...root, id: "f6".repeat(32), parent_id: loop.id, path: "back", label: "Back" };
  const malformed = served({ ...fixtureTopology, nodes: [loop, back] });
  const first = floorScene(fixtureState, malformed);
  const second = floorScene(fixtureState, malformed);
  assert.deepEqual(first, second, "invalid topology has a bounded deterministic projection");
  assert.deepEqual(first.topology.nodes.filter((node) => node.project?.id === ids.project).map((node) => [node.id, node.parentId, node.sizeBucket]), [[ids.project, undefined, undefined]]);
  assert.equal(first.navigation.enterableIds.length, 0);
});

test("task footprints reject stale, retry, cancelled, terminal, and cross-project samples", () => {
  const original = fixtureState.tasks.get(ids.task);
  const sample = runSample(ids.agent, ["web"]);
  const footprint = (task, candidate = sample) => floorScene(
    baseState({ tasks: new Map([[task.id, task]]) }), fixtureTopologies, new Map([[ids.agent, candidate]]),
  ).tasks[0];
  for (const status of ["queued", "blocked", "succeeded", "failed", "cancelled"]) {
    const order = footprint({ ...original, status });
    assert.deepEqual([order.roomIds, order.representativeRoomId], [[], undefined], status);
  }
  const retry = { ...original, revision: original.revision + 1n };
  assert.deepEqual([footprint(retry).roomIds, footprint(retry).representativeRoomId], [[], undefined]);
  const crossProject = { ...original, project_id: ids.secondProject };
  assert.deepEqual([footprint(crossProject, { ...sample, projectId: ids.secondProject }).roomIds, footprint(crossProject, { ...sample, projectId: ids.secondProject }).representativeRoomId], [[], undefined]);
});

test("tasks join requests only by exact task, agent, and project identity", () => {
  const southTask = {
    ...fixtureState.tasks.get(ids.task),
    id: "30".repeat(16),
    project_id: ids.secondProject,
    assigned_agent_id: ids.orchestrator,
    title: "Coordinate the south floor",
    revision: 99n,
  };
  const request = fixtureState.humanRequests.get(ids.request);
  const southRequest = { ...request, id: "40".repeat(16), task_id: southTask.id, agent_id: ids.orchestrator, project_id: ids.secondProject };
  const requests = new Map([
    [request.id, request],
    [southRequest.id, southRequest],
    ["42".repeat(16), { ...request, id: "42".repeat(16), task_id: southTask.id, agent_id: ids.agent, project_id: ids.secondProject }],
    ["43".repeat(16), { ...request, id: "43".repeat(16), task_id: southTask.id, agent_id: ids.orchestrator, project_id: ids.project }],
    ["44".repeat(16), { ...request, id: "44".repeat(16), task_id: ids.task, agent_id: ids.orchestrator, project_id: ids.project }],
  ]);
  const scene = floorScene(
    baseState({ tasks: new Map([[ids.task, fixtureState.tasks.get(ids.task)], [southTask.id, southTask]]), humanRequests: requests }),
    served(fixtureTopology, topologyFor(ids.secondProject, [["repository", ".", "large"]])),
    new Map([
      [ids.agent, runSample(ids.agent, ["web"])],
      [ids.orchestrator, { taskId: southTask.id, taskRevision: southTask.revision, runId: "73".repeat(16), projectId: ids.secondProject, paths: ["."] }],
    ]),
  );
  assert.deepEqual(scene.tasks.map((order) => [order.id, order.projectId, order.humanRequestIds]), [
    [southTask.id, ids.secondProject, [southRequest.id]],
    [ids.task, ids.project, [request.id]],
  ]);
  assert.equal(scene.tasks[0].roomIds.length, 1);
  assert.equal(scene.tasks[0].roomIds[0].startsWith(`${ids.secondProject}:`), true);
});

/**
 * A served structure for any tree at all: each entry is [kind, path, size] with
 * its parent given by index, so a repository, a module and a package can all
 * sit at "." exactly as the daemon serves them.
 */
function topologyFor(projectId, entries) {
  return {
    projectId,
    digest: `${projectId}`.slice(0, 64),
    sourceRevision: "",
    nodes: entries.map(([kind, path, sizeBucket, parent], index) => ({
      id: `${index}`.padStart(64, "0"),
      parent_id: parent === undefined ? "" : `${parent}`.padStart(64, "0"),
      kind,
      path,
      label: path === "." ? "root" : path.slice(path.lastIndexOf("/") + 1),
      language: "",
      size_bucket: sizeBucket,
    })),
  };
}

const served = (...views) => new Map(views.map((view) => [view.projectId, view]));

const PROJECT_NAME = "Any Project";

const soloState = (projectId) => ({
  ...fixtureState,
  projects: new Map([[projectId, { id: projectId, name: PROJECT_NAME, revision: 1n }]]),
  agents: new Map(),
  tasks: new Map(),
  humanRequests: new Map(),
});

test("an active overseer without a path remains unknown", () => {
  const overseer = { ...fixtureState.agents.get(ids.orchestrator), project_id: ids.project };
  const agents = new Map(fixtureState.agents);
  agents.set(overseer.id, overseer);
  const task = { ...fixtureState.tasks.get(ids.task), id: "35".repeat(16), project_id: ids.project, assigned_agent_id: overseer.id, title: "Supervise", status: "running" };
  const state = baseState({ agents, tasks: new Map([[task.id, task]]) });
  const worker = floorScene(state, fixtureTopologies).workers.find((item) => item.id === ids.orchestrator);
  assert.deepEqual([worker.location, worker.nodeId, worker.locationLabel], ["unobserved", undefined, undefined]);
  const observed = floorScene(state, fixtureTopologies, new Map([[ids.orchestrator, {
    taskId: task.id, taskRevision: task.revision, runId: "71".repeat(16), projectId: ids.project, paths: ["."],
  }]]))
    .workers.find((item) => item.id === ids.orchestrator);
  assert.equal(observed.location, "working", "an observed overseer keeps the observed room");
});

test("served hierarchy remains navigable across different repository shapes", () => {
  for (const shape of [
    [["repository", ".", "large"], ["module", ".", "large", 0], ["package", "gamma", "medium", 1], ["directory", "delta", "small", 1]],
    [["repository", ".", "large"], ["package", ".", "large", 0], ["directory", "one", "medium", 1], ["directory", "two", "small", 1]],
    [["repository", ".", "large"], ["directory", "docs", "small", 0], ["directory", "src", "medium", 0]],
  ]) {
    const topology = topologyFor(ids.project, shape);
    const topologies = served(topology);
    const state = soloState(ids.project);
    const landing = floorScene(state, topologies);
    assert.deepEqual(landing.topology.nodes.map((node) => node.label), [PROJECT_NAME]);
    for (const parent of topology.nodes) {
      const children = topology.nodes.filter((node) => node.parent_id === parent.id);
      const view = floorScene(state, topologies, undefined, undefined, `${ids.project}:${parent.id}`);
      assert.deepEqual(new Set(view.topology.nodes.map((node) => node.id)),
        new Set([parent, ...children].map((node) => `${ids.project}:${node.id}`)));
      assert.equal(view.navigation.omittedChildren, 0);
    }
  }
});

test("project scopes preserve worker identity and explicit project overflow", () => {
  // Two projects holding the same path are served the same node id; two rooms
  // on one floor may not share one, and a worker may not walk into the other
  // project's code. A paused worker stays in resting rather than occupying its
  // project's latest room.
  const shape = [["repository", ".", "large"], ["directory", "src", "medium", 0]];
  const topologies = served(topologyFor(ids.project, shape), topologyFor(ids.secondProject, shape));
  const scene = floorScene(fixtureState, topologies, new Map([[ids.agent, runSample(ids.agent, ["src"])], [ids.orchestrator, runSample(ids.orchestrator, ["src"])]]));
  assert.equal(scene.topology.nodes.length, 2);
  const scopes = scene.topology.nodes.map((root) => floorScene(fixtureState, topologies, undefined, undefined, root.id));
  const scopedRooms = scopes.flatMap((scope) => scope.topology.nodes);
  assert.equal(new Set(scopedRooms.map((node) => node.id)).size, 4);
  assert.deepEqual(new Set(scene.workers.map((worker) => worker.id)), new Set(fixtureState.agents.keys()));
  const nodeOf = (agentId) => scene.workers.find((worker) => worker.id === agentId).nodeId;
  assert.notEqual(nodeOf(ids.agent), undefined);
  assert.equal(nodeOf(ids.orchestrator), undefined);
  assert.ok(nodeOf(ids.agent).startsWith(`${ids.project}:`));
  assert.equal(scopes.find((scope) => scope.topology.nodes[0].project.id === ids.secondProject).topology.nodes.some((node) => node.id === nodeOf(ids.agent)), false);
  // Two structures have arrived and neither root room reads as the served
  // label: on a floor of many projects only the project name tells them apart.
  assert.deepEqual(scene.topology.nodes.filter((node) => node.path === ".").map((node) => node.label),
    [fixtureState.projects.get(ids.project).name, fixtureState.projects.get(ids.secondProject).name]);

  // More projects than the floor can detail: every one keeps its own room
  // before any one keeps a second, and none of them is served first.
  const projects = Array.from({ length: 8 }, (_, index) => ({ id: `${index}`.padStart(32, "b"), name: `Project ${index}`, revision: 1n }));
  const crowd = floorScene(
    { ...fixtureState, projects: new Map(projects.map((project) => [project.id, project])), agents: new Map(), tasks: new Map() },
    served(...projects.map((project) => topologyFor(project.id, [
      ["repository", ".", "large"],
      ...Array.from({ length: 10 }, (_, index) => ["directory", `dir-${index}`, "medium", 0]),
    ]))),
  );
  assert.equal(crowd.topology.nodes.length, projects.length);
  assert.equal(crowd.topology.nodes.filter((node) => node.path === ".").length, projects.length);

  // Past one room per project the floor cannot detail them all. The projects
  // the cap reaches keep their own room; an agent whose project it never
  // reached stands off the floor rather than in another project's room.
  const overflow = Array.from({ length: 30 }, (_, index) => ({ id: `${index}`.padStart(32, "d"), name: `Project ${index}`, revision: 1n }));
  const posted = [overflow[0], overflow.at(-1)].map((project, index) => ({
    id: `${index}`.padStart(32, "e"), project_id: project.id, name: project.name,
    role: "worker", provider: "shell", paused: false, model: "", reasoning_effort: "", revision: 1n,
  }));
  const stranded = floorScene(
    {
      ...fixtureState,
      projects: new Map(overflow.map((project) => [project.id, project])),
      agents: new Map(posted.map((agent) => [agent.id, agent])),
      tasks: new Map(),
      humanRequests: new Map(),
    },
    served(...overflow.map((project) => topologyFor(project.id, [["repository", ".", "large"], ["directory", "src", "medium", 0]]))),
  );
  assert.deepEqual(stranded.topology.nodes.map((node) => node.path), Array.from({ length: 24 }, () => "."));
  assert.equal(stranded.navigation.omittedChildren, overflow.length - 24);
  assert.deepEqual(new Set(stranded.workers.map((worker) => worker.id)), new Set(posted.map((worker) => worker.id)));
  assert.equal(stranded.workers[0].location, "resting");
  assert.equal(stranded.workers[0].nodeId, undefined);
  assert.equal(stranded.workers.at(-1).nodeId, undefined);
});

test("hostile names and titles are escaped as text and private detail is absent", () => {
  const hostile = "<img src=x onerror=alert(1)>";
  const hostileState = baseState({
    agents: new Map([[ids.agent, { id: ids.agent, project_id: ids.project, name: hostile, role: "worker", provider: "claude_code", paused: false, model: hostile, reasoning_effort: "", effective_model: hostile, effective_reasoning_effort: "", model_source: "agent", revision: 10n }]]),
    tasks: new Map([[ids.task, { id: ids.task, project_id: ids.project, assigned_agent_id: ids.agent, title: hostile, status: "running", priority: 10, revision: 12n }]]),
  });
  for (const view of VIEWS) {
    // The second pass renders the config form, so the hostile model reaches
    // an input value rather than being dropped with the whole section.
    for (const props of [{ view }, { view, selectedAgent: agentSelection(), onSaveAgentConfig: () => {} }]) {
      const markup = render({ state: hostileState, ...props });
      assert.equal(markup.includes("<img"), false, view);
      assert.equal(markup.includes("question"), false, view);
    }
  }
  assert.match(render({ state: hostileState }), /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(render({ state: hostileState, selectedAgent: agentSelection(), onSaveAgentConfig: () => {} }), /<input id="df-model-[0-9a-f]+"[^>]*value="&lt;img src=x onerror=alert\(1\)&gt;"/);
});

test("the console never shows a kernel-grammar or retired vocabulary word", () => {
  const withDetail = {
    selectedHumanRequest: selectedRequest(),
    onHumanReplyChange: () => {},
    onReplyHumanRequest: () => {},
    onCancelHumanRequest: () => {},
    onCloseHumanRequest: () => {},
    onSelectAgent: () => {},
  };
  const terminalView = (overrides = {}) => createElement(TerminalPanel, {
    terminal: { agentId: "21".repeat(16), agentName: "Builder One", agentRevision: 10n, phase: "ready", writable: true, resets: 0, surfaceVersion: 0, ...overrides.terminal },
    onClose: () => {},
  }, createElement("div"));
  const surfaces = [
    ...VIEWS.map((view) => [view, render({ ...withDetail, view })]),
    ["agent", render({ view: "agents", selectedAgent: agentSelection(), onSaveAgentConfig: () => {} })],
    ["settings", render({ settingsOpen: true, onToggleSettings: () => {} })],
    ["terminal", renderToStaticMarkup(terminalView())],
    ["terminal-reset", renderToStaticMarkup(terminalView({ terminal: { resets: 1 } }))],
  ];
  // "overseer" left this list by owner decision: it is the console's word for
  // the orchestrator rank. Everything else is still kernel grammar. The lease
  // ban is anchored at a word start so it still catches lease/leased/leases
  // without catching the floor's "release-ready".
  for (const [name, markup] of surfaces) {
    for (const forbidden of [/attempt/i, /converge/i, /admission/i, /finalize/i, /unresolved/i, /proposal/i, /verdict/i, /\bALLOW\b/, /\bBLOCK\b/, /\blease/i, /intake/i, /quarantine/i, /work item/i, /cancel run/i]) {
      assert.equal(forbidden.test(markup), false, `${name}: ${forbidden}`);
    }
  }
  assert.match(render({ view: "agents" }), />OVERSEER</);
});

test("transitional session statuses have stable live labels and offer no factory action", () => {
  for (const status of ["idle", "connecting", "authenticating", "syncing", "closed"]) {
    const markup = render({ status, onSelectAgent: () => {}, onSelectHumanRequest: () => {}, onView: () => {}, onToggleSettings: () => {} });
    assert.match(markup, new RegExp(`>${status.toUpperCase()}<`));
    // SETTINGS is the only button before the factory is ready; the floor is a
    // native disclosure, not a factory action.
    const live = (markup.match(/<button(?![^>]*disabled)/g) ?? []).length;
    assert.equal(live, 1, status);
    assert.match(markup, /<button type="button" aria-pressed="false" disabled=""/);
  }
  const ready = render({ status: "ready" });
  assert.match(ready, /class="dfFactoryConsole__connection dfFactoryConsole__visuallyHidden"/);
  assert.match(ready, /role="status" aria-live="polite" aria-atomic="true"/);
});

test("closed and pairing-uncertain errors have no ineffective action", () => {
  for (const error of [new SessionError("connection", true), new SessionError("pairing_uncertain"), new ProtocolError("malformed")]) {
    const markup = render({ status: "closed", error, onSelectAgent: () => {}, onSelectHumanRequest: () => {}, onView: () => {}, onToggleSettings: () => {} });
    assert.match(markup, /role="alert"/);
    // The banner offers nothing to press, and the only live buttons on a
    // closed console is SETTINGS, which changes nothing in the factory.
    assert.equal((markup.match(/<button(?![^>]*disabled)/g) ?? []).length, 1);
    assert.doesNotMatch(markup, /role="alert"[^>]*>[^<]*<button/);
    assert.equal(markup.includes("RETRY CONNECTION"), false);
    assert.equal(markup.includes("Error:"), false);
    assert.equal(markup.includes("secret"), false);
  }
});

test("protocol errors remain finite and never expose their message", () => {
  const markup = render({ status: "closed", error: new ProtocolError("malformed") });
  assert.match(markup, /The server sent an invalid frame\./);
  assert.equal(markup.includes("malformed"), false);
});

test("unknown and inherited error codes use a finite fallback", () => {
  for (const code of ["__proto__", "constructor", "toString", "hasOwnProperty", ""]) {
    const markup = render({ status: "closed", error: { code } });
    assert.match(markup, /The connection could not continue\./);
    if (code !== "") assert.equal(markup.includes(code), false);
  }
});

test("Needs You and Queue keep every served item reachable", () => {
  const emptyState = baseState({ projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map() });
  assert.match(render({ state: emptyState }), /all quiet — nothing needs you/);
  assert.match(render({ state: emptyState, detail: "queue" }), /THE QUEUE IS EMPTY/);
  assert.match(render({ state: emptyState, view: "agents" }), /no agents/);

  const agents = new Map();
  const tasks = new Map();
  const requests = new Map();
  for (let index = 0; index < 9; index += 1) {
    const suffix = String(index).padStart(2, "0");
    const agentID = `${suffix}${"21".repeat(15)}`;
    const taskID = `${suffix}${"31".repeat(15)}`;
    const requestID = `${suffix}${"41".repeat(15)}`;
    agents.set(agentID, { id: agentID, project_id: ids.project, name: `Agent ${index}`, role: "worker", provider: "codex", paused: false, model: "", reasoning_effort: "", revision: BigInt(index + 1) });
    tasks.set(taskID, { id: taskID, project_id: ids.project, assigned_agent_id: agentID, title: `Task ${index}`, status: "queued", priority: index, revision: BigInt(index + 1) });
    requests.set(requestID, { id: requestID, project_id: ids.project, agent_id: agentID, task_id: taskID, created_at: 1n, updated_at: 1n, revision: BigInt(index + 1), kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true });
  }
  const bounded = baseState({ agents, tasks, humanRequests: requests });
  const markup = render({ state: bounded });
  assert.equal((markup.match(/<details class="dfConsoleItem"/g) ?? []).length, 9);
  const queue = render({ state: bounded, detail: "queue" });
  assert.equal((queue.match(/<li class="dfConsoleItem"/g) ?? []).length, 9);
  assert.equal((markup.match(/\+1 more/g) ?? []).length, 0);
  assert.match(markup, />9 ITEMS</);
  assert.match(queue, /Task 8/);
  assert.equal((render({ state: bounded, view: "agents" }).match(/dfAgentList__row/g) ?? []).length, 0, "no handler, no button");
  assert.equal((render({ state: bounded, view: "agents", onSelectAgent: () => {} }).match(/dfAgentList__row/g) ?? []).length, 9);
});

test("Queue keeps running tasks visible without a second queue", () => {
  const markup = render({ detail: "queue" });
  assert.match(markup, /aria-label="Running tasks"/);
  assert.match(markup, /Review the state projection/);
  assert.equal((markup.match(/aria-label="Queue"/g) ?? []).length, 1, "one queue panel");
});

test("a terminal blocked task is neither building nor current agent work", () => {
  const state = baseState({
    tasks: new Map([[ids.task, { ...fixtureState.tasks.get(ids.task), status: "blocked" }]]),
    humanRequests: new Map(),
  });
  const markup = render({ state, view: "agents" });
  assert.match(markup, /aria-label="Builder One: ready"/);
  assert.equal(markup.includes('aria-label="Builder One: busy"'), false);
});

test("the production console exposes no speculative or unsupported surface", () => {
  for (const view of VIEWS) {
    const markup = render({ view }).toLowerCase();
    for (const text of ["not yet served", "awaiting deploy", "suggestions", "add work", "accept", "dismiss", "task record"]) {
      assert.equal(markup.includes(text), false, `${view}: ${text}`);
    }
  }
});

test("an unavailable snapshot is explicit and does not invent runtime state", () => {
  const markup = render({ state: undefined, status: "syncing" });
  assert.match(markup, /WAITING FOR SNAPSHOT/);
  assert.match(markup, /WAITING FOR SNAPSHOT/);
  assert.match(markup, /<dt>ACTIVE RUNS<\/dt><dd>—<\/dd>/);
  assert.match(markup, /QUEUE <span>—<\/span>/);
  assert.equal(markup.includes("THE QUEUE IS EMPTY"), false);
  assert.equal(markup.includes("all quiet"), false);
  assert.match(render({ state: undefined, status: "syncing", view: "agents" }), /waiting for the factory/);
});

test("HumanRequest delivery states remain visibly distinct", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  for (const [status, label] of [
    ["open", "OPEN"],
    ["delivering", "DELIVERING"],
    ["delivery_unknown", "DELIVERY UNKNOWN"],
  ]) {
    const markup = render({ state: baseState({ humanRequests: new Map([[request.id, { ...request, status }]]) }) });
    assert.match(markup, new RegExp(`>${label} ·`));
  }
});

test("the two-column console keeps one mounted terminal slot", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /\.dfConsoleRow__agent\s*\{[^}]*min-width: 0;[^}]*overflow-wrap: anywhere;/);
  assert.match(css, /\.dfFactoryConsole__terminalPanel :where\(p\)\s*\{\s*margin: 0;/);
  assert.match(css, /\.dfConsoleRow\s*\{[^}]*flex-wrap: wrap;/);
  assert.match(css, /\.dfConsoleLayout\s*\{[^}]*grid-template-columns: minmax\(0, 2fr\) minmax\(0, 1fr\);/);
  assert.match(css, /\.dfFactoryConsole__instructionActions\s*\{[^}]*display: flex;[^}]*flex-wrap: wrap;/);
  for (const rule of [/\.dfConsoleDialog \*/, /\.dfConsoleDialog button,/, /\.dfConsoleDialog button:disabled,[\s\S]*?\{/, /\.dfConsoleDialog\s*\{[^}]*font-family: ui-monospace/, /\.dfConsoleDialog\s*\{[^}]*color: var\(--df-console-text\)/]) {
    assert.match(css, rule);
  }
  // The panel scrolls, never the <dialog>: a scrollbar click on the dialog
  // itself has event.target === the dialog and would close SETTINGS.
  assert.match(css, /\.dfConsoleDialog \.dfConsoleSidebar__panel \{[^}]*max-height:[^}]*overflow: auto;/);
  assert.equal(/\.dfConsoleDialog\s*\{[^}]*overflow: auto/.test(css), false);
  assert.match(css, /:focus-visible\s*\{\s*outline: 2px solid var\(--df-console-accent\);/);

  const withTerminal = render({ selectedAgent: agentSelection(), terminalContent: createElement("section", { "aria-label": "Agent terminal" }) });
  assert.match(withTerminal, /<h1>DARK FACTORY<\/h1>/);
  assert.match(withTerminal, /dfConsoleLayout__left[\s\S]*?dfConsoleSidebar/);
  assert.match(withTerminal, /dfConsoleSidebar__terminalSlot" aria-label="Terminal"><section aria-label="Agent terminal"><\/section>/);
  assert.equal(withTerminal.includes("dfConsoleLayout__right"), false);
});

test("switching right panels keeps the selected terminal mounted", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let mounted = 0;
  let unmounted = 0;
  function TerminalProbe() {
    useEffect(() => { mounted += 1; return () => { unmounted += 1; }; }, []);
    return createElement("section", { "aria-label": "Agent terminal" }, "terminal");
  }
  try {
    const props = { status: "ready", state: baseState(), selectedAgent: agentSelection(), terminalContent: createElement(TerminalProbe) };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, { ...props, detail: "agent" })); });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, detail: "queue" })); });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, detail: "needs-you" })); });
    assert.equal(mounted, 1);
    assert.equal(unmounted, 0);
    await act(async () => { renderer.unmount(); });
    assert.equal(unmounted, 1);
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("opening a selected question restores that agent's terminal panel", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  function ConsoleHarness() {
    const [detail, setDetail] = useState("agent");
    const [panel, setPanel] = useState("terminal");
    return createElement(FactoryConsole, {
      status: "ready",
      state: baseState(),
      selectedAgent: agentSelection(),
      selectedHumanRequest: selectedRequest(),
      detail,
      onDetail: setDetail,
      agentPanel: panel,
      onAgentPanel: setPanel,
      onOpenTerminalForHumanRequest: () => { setDetail("agent"); setPanel("terminal"); },
      terminalContent: createElement("p", null, "terminal"),
    });
  }
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(ConsoleHarness)); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "CONFIG").props.onClick(); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => Array.isArray(button.props.children) && button.props.children[0] === "NEEDS YOU ").props.onClick(); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "OPEN TERMINAL").props.onClick(); });
    const terminal = renderer.root.findByProps({ "aria-label": "Terminal" });
    const config = renderer.root.findByProps({ "aria-label": "Agent configuration" });
    assert.equal(terminal.props.hidden, false);
    assert.equal(config.props.hidden, true);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("selecting an agent exposes terminal and configuration controls", () => {
  const markup = render({
    view: "agents",
    selectedAgent: agentSelection(),
    onSelectAgent: () => {},
    onSaveAgentConfig: () => {},
    onEditTask: () => {},
  });
  assert.match(markup, /aria-label="Agent Builder One"/);
  assert.match(markup, /Builder One[\s\S]*?claude_code · claude-opus-5/);
  assert.match(markup, /aria-label="Agent controls"/);
  assert.match(markup, />TERMINAL</);
  assert.match(markup, />CONFIG</);
  assert.match(markup, /aria-label="Agent configuration"/);
  assert.match(markup, /value="claude-opus-5"/);
  assert.match(markup, /value="high"/);
  assert.equal(markup.includes("aria-label=\"Agent queue\""), false);
  assert.match(markup, /dfConsoleLayout__left/);
  assert.equal(markup.includes("dfConsoleLayout__right"), false);

  // Without handlers the sidebar is a readout, never a dead form.
  const readOnly = render({ selectedAgent: agentSelection() });
  assert.equal(readOnly.includes("<input"), false);
  assert.equal(readOnly.includes("OPEN TERMINAL"), false);
});

test("a paused agent with queued work says the queue is paused", () => {
  const task = [...fixtureState.tasks.values()].find((item) => item.status === "queued");
  const selected = fixtureState.agents.get(task.assigned_agent_id);
  const agent = { ...selected, paused: true };
  const agents = new Map(fixtureState.agents);
  agents.set(agent.id, agent);
  const markup = render({
    state: baseState({ agents }),
    selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision },
  });
  assert.match(markup, />QUEUE PAUSED</);
  assert.equal(markup.includes("QUEUED · WAITING FOR CAPACITY"), false);
});

test("the queued task row keeps served order and changes its exact priority", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const edits = [];
    const detailReads = [];
    const queued = { ...fixtureState.tasks.get([...fixtureState.tasks.keys()][1]), title: "Served first", priority: 2 };
    const other = { ...queued, id: "39".repeat(16), title: "Higher but served second", priority: 9, revision: 20n };
    const state = baseState({
      tasks: new Map([[queued.id, { ...queued, assigned_agent_id: ids.agent }], [other.id, { ...other, assigned_agent_id: ids.agent }]]),
    });
    const props = {
      status: "ready",
      state,
      detail: "queue",
      selectedAgent: agentSelection(),
      onSaveAgentConfig: (config) => edits.push(["config", config]),
      onEditTask: async (task, change) => { edits.push([task.id, change]); return true; },
      onLoadTaskDetail: async (task, peerOffset, expectedHead) => {
        detailReads.push(task.id);
        if (peerOffset !== undefined) throw new SessionError("stale");
        return { taskId: task.id, revision: task.revision, head: expectedHead ?? 7n, instruction: "Original brief", feedback: "Review this carefully", peerQuestions: [{ id: `${peerOffset ?? 0n}`.padStart(32, "0"), source_task_id: queued.id, target_task_id: other.id, question: "What changed?", recipient_delivery_state: "delivered", answer_delivery_state: "pending", revision: 1n, created_at_ms: 1n, updated_at_ms: 1n }], nextPeerOffset: 1n };
      },
    };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    const buttons = renderer.root.findAllByType("button");
    const byLabel = (label) => buttons.find((button) => button.props["aria-label"] === label);
    const queueRows = renderer.root.findByProps({ "aria-label": "Queue" }).findAllByType("details").filter((row) => row.props.className === "dfConsoleItem");
    assert.deepEqual(queueRows.map((row) => row.findByType("strong").props.children), [queued.title, other.title], "the queue keeps the server's per-agent order, rather than re-sorting priority");
    assert.ok(renderer.root.findAllByType("p").some((paragraph) => paragraph.props.children === "Grouped by agent · no global start order"));
    assert.ok(renderer.root.findAllByType("span").some((span) => (Array.isArray(span.props.children) ? span.props.children.join("") : String(span.props.children)).includes("QUEUED · PRIORITY 2")));
    await act(async () => { byLabel(`Increase priority for ${queued.title}`).props.onClick(); });
    assert.deepEqual(edits.at(-1), [queued.id, { priority: queued.priority + 1 }]);
    await act(async () => { byLabel(`Decrease priority for ${other.title}`).props.onClick(); });
    assert.deepEqual(edits.at(-1), [other.id, { priority: other.priority - 1 }]);

    const highest = { ...queued, title: "Highest", priority: MAX_TASK_PRIORITY };
    const lowest = { ...other, title: "Lowest", priority: -MAX_TASK_PRIORITY };
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, state: baseState({ tasks: new Map([[highest.id, highest], [lowest.id, lowest]]) }) })); });
    const bounded = renderer.root.findAllByType("button");
    assert.equal(bounded.find((button) => button.props["aria-label"] === `Increase priority for ${highest.title}`).props.disabled, true);
    assert.equal(bounded.find((button) => button.props["aria-label"] === `Decrease priority for ${lowest.title}`).props.disabled, true);

    await act(async () => { renderer.update(createElement(FactoryConsole, props)); });

    const editBrief = () => renderer.root.findAllByType("button").find((button) => button.props.children === "EDIT BRIEF");
    const firstRow = renderer.root.findByProps({ "aria-label": "Queue" }).findAllByType("details").find((row) => row.props.className === "dfConsoleItem");
    assert.equal(firstRow.props.open, undefined, "queue rows start collapsed");
    assert.deepEqual(detailReads, [], "collapsed queued rows do not read private briefs");
    await act(async () => { firstRow.props.onToggle({ currentTarget: { open: true } }); });
    assert.deepEqual(detailReads, [queued.id]);
    const title = renderer.root.findAllByType("input").find((input) => input.props.value === queued.title);
    await act(async () => { title.props.onChange({ currentTarget: { value: "Renamed" } }); });
    const instruction = renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`);
    await act(async () => { instruction.props.onChange({ currentTarget: { value: "Replacement brief" } }); });
		assert.ok(renderer.root.findAllByType("pre").some((item) => item.props.children === "Review this carefully"));
    await act(async () => { await renderer.root.findAllByType("button").find((button) => button.props.children === "SAVE BRIEF").props.onClick(); });
    assert.deepEqual(edits.at(-1), [queued.id, { title: "Renamed", body: "Replacement brief" }]);
    await act(async () => { firstRow.props.onToggle({ currentTarget: { open: false } }); firstRow.props.onToggle({ currentTarget: { open: true } }); });
    assert.deepEqual(detailReads, [queued.id], "reopening a cached row does not overwrite the brief");

    const assign = renderer.root.findAllByType("select").find((select) => select.props.id === `df-assign-${queued.id}`);
    // Reassignment offers only agents in the same project.
    assert.deepEqual(assign.props.children.map((option) => option.props.children), ["Builder One", "Builder Two"]);
    await act(async () => { assign.props.onChange({ currentTarget: { value: "23".repeat(16) } }); });
    assert.deepEqual(edits.at(-1), [queued.id, { assignedAgentId: "23".repeat(16) }]);

    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "CANCEL").props.onClick(); });
    assert.deepEqual(edits.at(-1), [queued.id, { cancel: true }]);

    // A refused brief edit preserves the operator's drafts at the unchanged
    // revision, rather than silently replacing the intended instruction.
    await act(async () => { await editBrief().props.onClick(); });
    const titleValue = () => renderer.root.findAllByType("input").find((input) => input.props.id === `df-title-${queued.id}`).props.value;
    const instructionValue = () => renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`).props.value;
    await act(async () => { renderer.root.findAllByType("input").find((input) => input.props.id === `df-title-${queued.id}`).props.onChange({ currentTarget: { value: "Keep this draft" } }); });
    await act(async () => { renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`).props.onChange({ currentTarget: { value: "Keep this instruction" } }); });
    assert.equal(titleValue(), "Keep this draft");
    await act(async () => { await renderer.root.findAllByType("button").find((button) => button.props.children === "OLDER CONVERSATION").props.onClick(); });
    assert.equal(titleValue(), "Keep this draft");
    assert.equal(instructionValue(), "Keep this instruction");
    assert.ok(renderer.root.findAllByProps({ role: "alert" }).some((item) => String(item.props.children).includes("SAVE OR DISCARD YOUR DRAFT")));
    const revised = baseState({ tasks: new Map([[queued.id, { ...queued, assigned_agent_id: ids.agent, revision: queued.revision + 1n }], [other.id, { ...other, assigned_agent_id: ids.agent }]]) });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, state: revised })); });
    assert.equal(titleValue(), "Keep this draft");
    assert.equal(instructionValue(), "Keep this instruction");
    assert.equal(renderer.root.findAllByProps({ role: "alert" }).some((item) => String(item.props.children).includes("TASK CHANGED")), true);
    assert.equal(renderer.root.findAllByType("button").find((button) => button.props.children === "SAVE BRIEF").props.disabled, true);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("a config refusal resets only its form and config saves remain partial", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const edits = [];
    const queued = [...fixtureState.tasks.values()].find((task) => task.status === "queued");
    const agent = fixtureState.agents.get(ids.agent);
    const props = {
      status: "ready",
      state: baseState(),
      detail: "agent",
      agentPanel: "config",
      selectedAgent: agentSelection(),
      onSaveAgentConfig: (config) => edits.push(config),
    };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    const model = () => renderer.root.findAllByType("input").find((input) => input.props.id === `df-model-${agent.id}`);
    const config = () => renderer.root.findAllByType("form").find((form) => form.props["aria-label"] === "Agent configuration");
    const paused = () => renderer.root.findAllByType("input").find((input) => input.props.type === "checkbox");

    await act(async () => { model().props.onChange({ currentTarget: { value: "half-typed" } }); });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, edit: { target: queued.id, pending: false, error: new SessionError("stale") } })); });
    assert.equal(model().props.value, "half-typed", "a task refusal leaves the config draft intact");
    assert.equal(renderer.root.findAllByProps({ role: "alert" }).length, 1, "the shared alert remains visible");

    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, edit: { target: agent.id, pending: false, error: new SessionError("stale") } })); });
    assert.equal(model().props.value, agent.model, "a config refusal remounts its own form");
    assert.equal(renderer.root.findAllByProps({ role: "alert" }).length, 1, "a config refusal has one shared alert");
    await act(async () => { renderer.update(createElement(FactoryConsole, props)); });

    await act(async () => { paused().props.onChange({ currentTarget: { checked: true } }); });
    await act(async () => { config().props.onSubmit({ preventDefault() {} }); });
    assert.deepEqual(edits.at(-1), { paused: true });

    await act(async () => { model().props.onChange({ currentTarget: { value: "claude-sonnet-5" } }); });
    await act(async () => { config().props.onSubmit({ preventDefault() {} }); });
    assert.deepEqual(edits.at(-1), { model: "claude-sonnet-5", paused: true });

    await act(async () => { model().props.onChange({ currentTarget: { value: agent.model } }); });
    await act(async () => { paused().props.onChange({ currentTarget: { checked: false } }); });
    await act(async () => { config().props.onSubmit({ preventDefault() {} }); });
    assert.deepEqual(edits.at(-1), {}, "an untouched config submits no changes");
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("recent work remains collapsed, bounded, and private until opened", async () => {
  const tasks = new Map(Array.from({ length: 12 }, (_, index) => {
    const task = {
      ...fixtureState.tasks.get(ids.task),
      id: String(index + 1).padStart(32, "0"),
      assigned_agent_id: ids.agent,
      title: "Direct instruction",
      status: index % 3 === 0 ? "blocked" : index % 3 === 1 ? "succeeded" : "failed",
      revision: BigInt(index + 1),
      updated_at_ms: BigInt(1_700_000_000_000 + index),
    };
    return [task.id, task];
  }));
  const detailCalls = [];
  const historyCalls = [];
  const listCalls = [];
  const props = {
    status: "ready", state: baseState({ tasks }), selectedAgent: agentSelection(), onSaveAgentConfig: () => {}, onEditTask: async () => true,
    onLoadTaskDetail: async (task, peerOffset, expectedHead) => {
      detailCalls.push([task.id, peerOffset, expectedHead]);
      return {
        taskId: task.id, revision: task.revision, head: expectedHead ?? 9n,
        instruction: `Instruction ${task.id}`,
        feedback: "https://github.com/example-owner/example-repo/pull/42 https://example.test/not-a-pr",
        outcome: `Outcome ${task.id}`,
        peerQuestions: [],
      };
    },
    onLoadTaskHistory: async (task) => {
      historyCalls.push(task.id);
      return { taskId: task.id, entries: [{ operationId: "91".repeat(16), kind: "message", actor: "operator", body: "reviewed", status: "delivered", createdAtMs: 1_700_000_000_012n }] };
    },
    onLoadTaskList: async (_agentId, cursor) => {
      listCalls.push(cursor);
      const recent = [...tasks.values()].sort((left, right) => left.updated_at_ms === right.updated_at_ms ? right.id.localeCompare(left.id) : left.updated_at_ms > right.updated_at_ms ? -1 : 1);
      const start = cursor === undefined ? 0 : recent.findIndex((task) => task.id === cursor.beforeTaskId) + 1;
      const page = recent.slice(start, start + 10);
      return { agentId: ids.agent, head: 9n, total: BigInt(recent.length), tasks: page, hasMore: start + page.length < recent.length };
    },
  };
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
  const recent = renderer.root.findByProps({ className: "dfConsoleRecentWork dfConsoleSidebar__section" });
  assert.equal(renderer.root.findAllByType("dialog").length, 0);
  assert.deepEqual(detailCalls, []);
  assert.deepEqual(listCalls, []);
  assert.deepEqual(historyCalls, []);
  await act(async () => { recent.findByType("button").props.onClick(); });
  assert.equal(detailCalls.length, 1, "only the selected task loads private detail");
  assert.equal(listCalls.length, 1);
  assert.deepEqual(historyCalls, [detailCalls[0][0]]);
  const items = () => renderer.root.findAllByProps({ className: "dfRecentWorkRow" });
  assert.equal(items().length, 10);
  assert.equal(items()[0].props["aria-pressed"], true);
  assert.equal(detailCalls[0][0], "00000000000000000000000000000012");
  assert.equal(JSON.stringify(items()[0].children.map((child) => typeof child === "string" ? child : child.props.children)).includes("Instruction"), false, "list titles do not repeat full private instructions");
  const detail = renderer.root.findByProps({ "aria-label": "Work details" });
  assert.ok(detail.findAllByType("p").some((item) => item.props.children === `Outcome ${detailCalls[0][0]}`));
  assert.deepEqual(detail.findAllByType("a").map((link) => link.props.href), ["https://github.com/example-owner/example-repo/pull/42"]);
  await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "SHOW MORE").props.onClick(); });
  assert.equal(detailCalls.length, 1, "pagination does not fetch private details for unselected work");
  assert.deepEqual(listCalls[1], { beforeUpdatedAtMs: 1_700_000_000_002n, beforeTaskId: "00000000000000000000000000000003" });
  assert.equal(items().length, 12);
  await act(async () => { items()[11].props.onClick(); });
  assert.equal(detailCalls.length, 2);
  assert.equal(historyCalls.length, 2);
  assert.equal(items()[11].props["aria-pressed"], true);
  await act(async () => { renderer.root.findByType("dialog").props.onClose(); });
  assert.equal(renderer.root.findAllByType("dialog").length, 0);
  await act(async () => { renderer.unmount(); });
});

test("late recent-work detail never crosses an agent remount", async () => {
  const first = fixtureState.agents.get(ids.agent);
  const second = fixtureState.agents.get(ids.idleAgent);
  const firstTask = { ...fixtureState.tasks.get(ids.task), assigned_agent_id: first.id, status: "succeeded", revision: 21n, updated_at_ms: 21n };
  const secondTask = { ...firstTask, id: "52".repeat(16), assigned_agent_id: second.id, status: "failed", revision: 22n, updated_at_ms: 22n };
  const pending = new Map();
  const props = {
    status: "ready", state: baseState({ tasks: new Map([[firstTask.id, firstTask], [secondTask.id, secondTask]]) }), selectedAgent: agentSelection(first.id), onSaveAgentConfig: () => {},
    onLoadTaskDetail: (task) => new Promise((resolve) => pending.set(task.id, resolve)),
    onLoadTaskList: async (agentId) => ({ agentId, head: 9n, total: 1n, tasks: [agentId === first.id ? firstTask : secondTask], hasMore: false }),
  };
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
  const openRecent = async () => {
    const recent = renderer.root.findByProps({ className: "dfConsoleRecentWork dfConsoleSidebar__section" });
    await act(async () => { recent.findByType("button").props.onClick(); });
  };
  await openRecent();
  assert.equal(pending.has(firstTask.id), true);
  await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, selectedAgent: agentSelection(second.id) })); });
  await openRecent();
  pending.get(secondTask.id)({ taskId: secondTask.id, revision: secondTask.revision, head: 9n, instruction: "SECOND DETAIL", feedback: "", outcome: "SECOND OUTCOME", peerQuestions: [] });
  await act(async () => {});
  pending.get(firstTask.id)({ taskId: firstTask.id, revision: firstTask.revision, head: 9n, instruction: "FIRST DETAIL", feedback: "", outcome: "FIRST OUTCOME", peerQuestions: [] });
  await act(async () => {});
  const text = renderer.toJSON();
  const markup = JSON.stringify(text);
  assert.match(markup, /SECOND DETAIL/);
  assert.equal(markup.includes("FIRST DETAIL"), false, "the late prior-agent detail has no new panel to update");
  await act(async () => { renderer.unmount(); });
});

test("a rejected edit says plainly that the durable value did not change", () => {
  const markup = render({
    selectedAgent: agentSelection(),
    onSaveAgentConfig: () => {},
    edit: { target: ids.agent, pending: false, error: new SessionError("stale") },
  });
  assert.match(markup, /SOMEONE ELSE CHANGED THIS — REOPEN IT AND TRY AGAIN/);
  assert.match(markup, /role="alert"/);
  assert.equal((markup.match(/role="alert"/g) ?? []).length, 1, "a config refusal has one shared alert");
  const unknown = render({ selectedAgent: agentSelection(), onSaveAgentConfig: () => {}, edit: { target: ids.agent, pending: false, error: { code: "internal" } } });
  assert.match(unknown, /THE EDIT DID NOT COMPLETE/);
  assert.match(render({ selectedAgent: agentSelection(), onSaveAgentConfig: () => {}, edit: { pending: true } }), />SAVING</);
});

test("a queued edit refusal remains visible after its task leaves the queue", () => {
  const rejected = { target: ids.task, pending: false, error: new SessionError("stale") };
  const task = fixtureState.tasks.get(ids.task);
  const states = [
    baseState({ tasks: new Map([[task.id, { ...task, status: "running" }]]) }),
    baseState({ tasks: new Map([[task.id, { ...task, status: "cancelled" }]]) }),
    baseState({ tasks: new Map() }),
  ];
  for (const state of states) {
    const markup = render({ state, detail: "needs-you", edit: rejected });
    assert.match(markup, /SOMEONE ELSE CHANGED THIS — REOPEN IT AND TRY AGAIN/);
    assert.equal((markup.match(/role="alert"/g) ?? []).length, 1);
  }
});

test("the settings modal carries the factory readout and a pairing mount point", () => {
  const markup = render({ settingsOpen: true, onToggleSettings: () => {} });
  assert.match(markup, /<dialog class="dfConsoleDialog" aria-label="Settings">/);
  assert.match(markup, /aria-label="BUILDING"/);
  assert.match(markup, /<dt>DISPATCH<\/dt><dd>ENABLED<\/dd>/);
  assert.match(markup, /<dt>RUN ALLOWANCE<\/dt><dd>North Workshop: 7 LEFT \(5 USED\) · South Workshop: NOT LIMITED \(3 USED\)<\/dd>/);
  assert.match(markup, /<dt>PER-RUN LIMIT<\/dt><dd>North Workshop: 900 SECONDS · South Workshop: NOT LIMITED<\/dd>/);
  assert.match(markup, /<dt>REVISION<\/dt><dd>42<\/dd>/);
  assert.match(markup, /127\.0\.0\.1:43123/);
  assert.match(markup, /aria-label="PAIRING"/);
  assert.match(markup, /phone pairing arrives here/);
  // The peer PR drops its own component into the same slot.
  const paired = render({ settingsOpen: true, onToggleSettings: () => {}, pairing: createElement("p", null, "PAIR A PHONE") });
  assert.match(paired, /PAIR A PHONE/);
  assert.equal(paired.includes("phone pairing arrives here"), false);
  // The modal is over the console, so it neither closes nor replaces a sidebar.
  const both = render({ settingsOpen: true, onToggleSettings: () => {}, selectedAgent: agentSelection() });
  assert.match(both, /aria-label="Settings"/);
  assert.match(both, /aria-label="Agent Builder One"/);
});

test("settings edits project limits as future runs with an explicit unlimited choice", () => {
  const markup = render({ settingsOpen: true, onToggleSettings: () => {}, onSaveProjectLimits: () => {} });
  assert.match(markup, /aria-label="PROJECT LIMITS"/);
  assert.match(markup, /value="7"/);
  assert.match(markup, /REMAINING RUN ALLOWANCE/);
  assert.match(markup, /UNLIMITED RUNS/);
  assert.match(markup, /value="0"/);
  assert.match(markup, /MAX SECONDS PER RUN \(0 = UNLIMITED\)/);
  assert.match(markup, /AUTONOMOUS GITHUB ISSUE WORK REQUIRES BOTH LIMITS/);
});

test("settings rejects a blank per-run duration before saving", async () => {
  const calls = [];
  let renderer;
  await act(async () => {
    renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onSaveProjectLimits: (...args) => calls.push(args) }));
  });
  const form = renderer.root.findByProps({ "aria-label": `Limits for ${fixtureState.projects.get(ids.project).name}` });
  const inputs = form.findAllByType("input");
  await act(async () => { inputs[2].props.onChange({ currentTarget: { value: "" } }); });
  await act(async () => { form.props.onSubmit({ preventDefault: () => {} }); });
  assert.equal(calls.length, 0);
  assert.match(JSON.stringify(renderer.toJSON()), /DURATION MUST BE 0–86400 SECONDS/);
  await act(async () => { renderer.unmount(); });
});

test("SETTINGS opens and closes as a native modal, over whatever sidebar is open", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const calls = [];
    const node = { showModal: () => calls.push("showModal"), close: () => calls.push("close") };
    let renderer;
    await act(async () => {
      renderer = create(createElement(FactoryConsole, {
        status: "ready",
        state: baseState(),
        settingsOpen: true,
        selectedAgent: agentSelection(),
        onToggleSettings: () => calls.push("toggle"),
      }), { createNodeMock: () => node });
    });
    // The browser opens it, so ESC, the backdrop and the focus trap are its.
    assert.deepEqual(calls, ["showModal"]);
    const dialog = renderer.root.findByType("dialog");
    // The agent sidebar is still mounted underneath.
    assert.equal(renderer.root.findAll((instance) => instance.props["aria-label"] === "Agent Builder One").length, 1);

    // Every exit goes through close(), so focus always returns to SETTINGS,
    // and the close event is what tells the console the modal is gone.
    renderer.root.findAllByType("button").find((button) => button.props.children === "CLOSE").props.onClick();
    dialog.props.onClick({ target: node });
    dialog.props.onClick({ target: {} });
    assert.deepEqual(calls, ["showModal", "close", "close"]);
    await act(async () => { dialog.props.onClose(); });
    assert.deepEqual(calls.at(-1), "toggle");
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("only the selected agent portrait offers appearance editing in either view", () => {
  for (const view of VIEWS) {
    const props = { view, onSelectAgent: () => {}, onEditAppearance: () => {}, topologies: fixtureTopologies };
    assert.equal((render(props).match(/aria-label="Edit appearance for /g) ?? []).length, 0);
    const markup = render({ ...props, selectedAgent: agentSelection() });
    assert.equal((markup.match(/aria-label="Edit appearance for /g) ?? []).length, 1);
    assert.match(markup, /class="dfAgentSpriteEdit" aria-label="Edit appearance for Builder One"/);
  }
});

test("one sprite editor previews categories and saves one atomic appearance", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const saved = [];
    const node = { showModal: () => {}, close: () => {} };
    let renderer;
    await act(async () => {
      renderer = create(createElement(FactoryConsole, {
        status: "ready",
        state: baseState(),
        appearanceAgentId: ids.agent,
        onSaveAgentAppearance: (agentId, appearance) => { saved.push([agentId, appearance]); return Promise.resolve(true); },
        onCloseAppearance: () => {},
      }), { createNodeMock: () => node });
    });
    const dialog = renderer.root.findByProps({ "aria-label": "Edit appearance for Builder One" });
    assert.deepEqual(dialog.findAllByType("label").map((label) => label.findByType("span").children.join("")), ["SKIN TONE", "HAIR STYLE", "HAIR COLOUR", "FACE DETAIL", "CLOTHING STYLE", "CLOTHING COLOUR", "SHOES", "TOOL", "HEADWEAR"]);
    assert.equal(dialog.findAllByProps({ className: "dfAgentSprite" }).length, 1);
    await act(async () => { dialog.findAllByType("select")[0].props.onChange({ target: { value: "3" } }); });
    await act(async () => { dialog.findByType("form").props.onSubmit({ preventDefault() {} }); });
    assert.equal(saved.length, 1);
    assert.equal(saved[0][0], ids.agent);
    assert.equal(saved[0][1].automatic, false);
    assert.equal(saved[0][1].skin, 3);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("a refused appearance edit keeps the editor and its draft", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    let closes = 0;
    const node = { showModal: () => {}, close: () => { closes += 1; } };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, {
      status: "ready", state: baseState(), appearanceAgentId: ids.agent,
      edit: { target: ids.agent, pending: false, error: { code: "stale" } },
      onSaveAgentAppearance: () => Promise.resolve(false), onCloseAppearance: () => {},
    }), { createNodeMock: () => node }); });
    const dialog = renderer.root.findByProps({ "aria-label": "Edit appearance for Builder One" });
    await act(async () => { dialog.findAllByType("select")[0].props.onChange({ target: { value: "3" } }); });
    await act(async () => { await dialog.findByType("form").props.onSubmit({ preventDefault() {} }); });
    assert.equal(closes, 0);
    assert.equal(dialog.findByProps({ role: "alert" }).children.join(""), "SOMEONE ELSE CHANGED THIS — REOPEN IT AND TRY AGAIN");
    assert.equal(dialog.findAllByType("select")[0].props.value, 3);
    await act(async () => { renderer.unmount(); });
  } finally { globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct; }
});

test("Factory and Agents are explicit left-side alternatives", () => {
  const floor = render();
  assert.match(floor, /aria-label="Left view"/);
  assert.match(floor, /aria-pressed="true" disabled="">FACTORY/);
  assert.match(render({ view: "agents" }), /aria-label="Agents"/);
  // The top bar keeps the wordmark, the counters, and SETTINGS.
  const actions = floor.split('class="dfConsoleBar__actions"')[1];
  assert.match(actions.slice(0, actions.indexOf("</div>")), />SETTINGS</);
  assert.match(floor, /<h2>FACTORY FLOOR<\/h2>/);
});

test("FactoryApp server-renders without reading browser globals", () => {
  const markup = renderToStaticMarkup(createElement(FactoryApp));
  assert.match(markup, /Factory operator console/);
  assert.match(markup, />IDLE</);
  assert.match(markup, /WAITING FOR SNAPSHOT/);
});

test("selected hostile private detail is escaped and actions remain semantic", () => {
  const hostile = "<script>steal(authority)</script>";
  const markup = render({
    selectedHumanRequest: selectedRequest({ question: hostile, reply: "<reply>" }),
    onHumanReplyChange: () => {},
    onReplyHumanRequest: () => {},
    onCancelHumanRequest: () => {},
    onCloseHumanRequest: () => {},
  });
  assert.match(markup, /aria-label="Selected question"/);
  assert.match(markup, /aria-label="Answer this question"/);
  // Selected detail expands in its original list row, with one heading.
  assert.match(markup, /<details class="dfConsoleItem" open=""><summary[^>]*><strong>Builder One asks<\/strong>/);
  assert.match(markup, /<article class="dfFactoryConsole__humanRequest"/);
  assert.match(markup, /OPEN · North Workshop · Review the state projection/);
  assert.match(markup, /&lt;script&gt;steal\(authority\)&lt;\/script&gt;/);
  assert.equal(markup.includes("<script>"), false);
  assert.match(markup, /<textarea[^>]*>&lt;reply&gt;<\/textarea>/);
  assert.match(markup, />ANSWER</);
  assert.match(markup, />STOP TASK</);
  assert.equal(markup.includes("expectedRunRevision"), false);
});

test("request, reply, cancel, and summary collapse forward only presentation intent", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const calls = [];
  const baseProps = {
    status: "ready",
    state: baseState(),
    onSelectHumanRequest: (value) => calls.push(["select", value]),
    onHumanReplyChange: (value) => calls.push(["change", value]),
    onReplyHumanRequest: () => calls.push(["reply"]),
    onCancelHumanRequest: () => calls.push(["cancel"]),
    onCloseHumanRequest: () => calls.push(["close"]),
  };

  const requestElements = expand(FactoryConsole(baseProps));
  requestElements.find((element) => element.type === "summary" && element.props.className === "dfConsoleItem__summary").props.onClick({ preventDefault() {} });
  assert.equal(calls[0][0], "select");
  assert.equal(calls[0][1], request);
  const busyElements = expand(FactoryConsole({ ...baseProps, selectedHumanRequest: selectedRequest({ phase: "replying" }) }));
  const busySummary = busyElements.find((element) => element.type === "summary" && element.props.className === "dfConsoleItem__summary");
  assert.equal(busySummary.props["aria-disabled"], true);
  busySummary.props.onClick({ preventDefault() {} });
  assert.equal(calls.length, 1, "an in-flight answer cannot be collapsed or switched");

  const selectedElements = expand(FactoryConsole({ ...baseProps, selectedHumanRequest: selectedRequest() }));
  selectedElements.find((element) => element.type === "textarea").props.onChange({ currentTarget: { value: "Proceed." } });
  let prevented = false;
  selectedElements.find((element) => element.type === "form").props.onSubmit({ preventDefault: () => { prevented = true; } });
  selectedElements.find((element) => element.type === "button" && element.props.children === "STOP TASK").props.onClick();
  selectedElements.find((element) => element.type === "summary" && element.props.className === "dfConsoleItem__summary").props.onClick({ preventDefault() {} });
  assert.equal(prevented, true);
  assert.deepEqual(calls.slice(1), [["change", "Proceed."], ["reply"], ["cancel"], ["close"]]);
});

test("agent and question terminal actions expose only current public intent", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  // Oversight is listed first, so the first row is the orchestrator's.
  const agent = fixtureState.agents.get(ids.orchestrator);
  const calls = [];
  const elements = expand(FactoryConsole({
    status: "ready",
    state: baseState(),
    view: "agents",
    selectedHumanRequest: selectedRequest(),
    onSelectAgent: (value) => calls.push(["agent", value]),
    onOpenTerminalForHumanRequest: (value) => calls.push(["request", value]),
  }));
  const row = elements.find((element) => element.type === "button" && typeof element.props.className === "string" && element.props.className.includes("dfAgentList__row"));
  row.props.onClick();
  elements.filter((element) => element.type === "button" && element.props.children === "OPEN TERMINAL").at(-1).props.onClick();
  assert.equal(calls[0][0], "agent");
  assert.equal(calls[0][1].id, agent.id);
  assert.equal(calls[0][1].revision, agent.revision);
  assert.deepEqual(calls[1], ["request", request]);

  const markup = render({ selectedAgent: agentSelection(), terminalContent: createElement("div", null, "<raw-output>") });
  assert.match(markup, /&lt;raw-output&gt;/);
  assert.equal(markup.includes("runId"), false);
  assert.equal(markup.includes("sessionId"), false);
});

test("the view toggle and settings forward exactly one intent each", () => {
  const calls = [];
  const elements = expand(FactoryConsole({
    status: "ready",
    state: baseState(),
    onView: (value) => calls.push(["view", value]),
    onToggleSettings: () => calls.push(["settings"]),
  }));
  const chrome = elements.filter((element) => element.type === "button" && element.props.disabled !== true);
  assert.deepEqual(chrome.map((element) => element.props.children), ["SETTINGS", "FACTORY", "AGENTS"]);
  chrome[0].props.onClick();
  chrome.find((element) => element.props.children === "AGENTS").props.onClick();
  assert.deepEqual(calls, [["settings"], ["view", "agents"]]);
});

function expand(node, result = []) {
  if (Array.isArray(node)) {
    for (const child of node) expand(child, result);
    return result;
  }
  if (!isValidElement(node)) return result;
  if (typeof node.type === "function") {
    // Stateful surfaces have their own renderer checks; this walk tests sibling intent callbacks.
    if (["QueuePanel", "FactoryFloor"].includes(node.type.name)) return result;
    expand(node.type(node.props), result);
    return result;
  }
  result.push(node);
  expand(node.props.children, result);
  return result;
}

test("PAIR A PHONE appears in settings only with authority, and shows the minted code", () => {
  const svg = '<svg viewBox="0 0 1 1"/>';
  const link = "https://app.darkfactory.build/remote#df_remote&node=n0&expires=1767225600";
  const settings = { settingsOpen: true, onToggleSettings: () => {} };
  // Pairing lives in the settings sidebar, which is the only place it shows.
  assert.equal(render({ ...settings }).includes("PAIR A PHONE"), false);
  assert.match(render({ ...settings, remoteInviteAllowed: true }), /PAIR A PHONE/);
  assert.equal(render({ remoteInviteAllowed: true }).includes("PAIR A PHONE"), false, "not without settings");
  assert.match(render({ ...settings, remoteInviteAllowed: true, selectedAgent: agentSelection() }), /PAIR A PHONE/, "the modal is over the sidebar, not behind it");
  // Its slot still takes an explicit override.
  assert.match(render({ ...settings, remoteInviteAllowed: true, pairing: createElement("p", null, "OTHER") }), /OTHER/);

  const shown = render({ ...settings, remoteInviteAllowed: true, remoteInvite: { link, svg, expiresAtMs: 1767225600000n } });
  assert.ok(shown.includes(`src="data:image/svg+xml;utf8,${encodeURIComponent(svg)}"`), shown);
  assert.ok(shown.includes(link.replaceAll("&", "&amp;")));
  assert.match(shown, /DISMISS/);
  // The minted code is never handed to the browser as markup. The floor draws
  // its own SVG, so the check names the invite's exact bytes.
  assert.equal(shown.includes(svg), false);

  const failed = render({ ...settings, remoteInviteAllowed: true, remoteInviteError: "not_found" });
  assert.match(failed, /NO PAIRING CODE — NOT FOUND/);
  assert.match(failed, /DISMISS/);
  assert.equal(render({ ...settings, remoteInviteAllowed: true }).includes("DISMISS"), false);
});

const shellAgent = { id: ids.agent, project_id: ids.project, name: "Shell Hand", role: "worker", provider: "shell", paused: true, model: "", reasoning_effort: "", effective_model: "", effective_reasoning_effort: "", model_source: "", revision: 10n };

const withAgent = (agent) => render({
  view: "agents",
  state: baseState({ agents: new Map([[agent.id, agent]]) }),
  selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision },
  onSaveAgentConfig: () => {},
});

const inheritingAgent = () => fixtureState.agents.get("23".repeat(16));

test("the console shows the model an agent will actually run with", () => {
  // An agent that names no model still runs with one. The row and the panel
  // header say which, so a blank is never read as "no model".
  const rows = render({ view: "agents" });
  assert.match(rows, /codex · gpt-6-astra/);
  assert.match(rows, /claude_code · claude-opus-5/);
  assert.match(withAgent(inheritingAgent()), /Builder Two[\s\S]*?codex · gpt-6-astra/);
  // The provider with no model says nothing extra rather than a dangling dot.
  assert.match(withAgent(shellAgent), /Shell Hand[\s\S]*?shell/);
});

test("the config inputs stay the agent's own override and caption where it came from", () => {
  // Own: the served value is in the box and the caption names the agent.
  const own = withAgent(fixtureState.agents.get(ids.agent));
  assert.match(own, /<input id="df-model-[0-9a-f]+"[^>]*placeholder="claude-opus-5"/);
  assert.match(own, /<input id="df-model-[0-9a-f]+"[^>]*value="claude-opus-5"/);
  assert.match(own, />set on this agent</);

  // Inherited: the box is empty because the override is, and the placeholder
  // plus the caption say what the run gets and which file decided it.
  const inherited = withAgent(inheritingAgent());
  assert.match(inherited, /<input id="df-model-[0-9a-f]+"[^>]*placeholder="gpt-6-astra"/);
  assert.match(inherited, /<input id="df-model-[0-9a-f]+"[^>]*value=""/);
  assert.match(inherited, /<input id="df-effort-[0-9a-f]+"[^>]*placeholder="high"/);
  assert.match(inherited, /inherited from \/Users\/operator\/\.codex\/config\.toml/);

  // Unknown: an older daemon, or a CLI whose configuration the factory cannot
  // read. The console says so rather than implying the model is unset.
  const unknown = withAgent({ ...inheritingAgent(), effective_model: "", effective_reasoning_effort: "", model_source: "" });
  assert.match(unknown, /CLI default \(not visible to the factory\)/);
  assert.equal(unknown.includes("inherited from"), false);
});

test("the shell provider has no model inputs but keeps PAUSED", () => {
  const markup = withAgent(shellAgent);
  assert.match(markup, />shell has no model</);
  assert.equal(markup.includes("df-model-"), false);
  assert.equal(markup.includes("df-effort-"), false);
  // The daemon rejects a model for shell; pausing it is still an edit.
  assert.match(markup, /id="df-paused-[0-9a-f]+"/);
  assert.match(markup, />PAUSED</);
});

test("the agent config offers its provider's linked accounts and the provider default", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const edits = [];
    const state = baseState();
    const linked = [...state.accounts.values()][0];
    const props = {
      status: "ready",
      state,
      selectedAgent: agentSelection(),
      onSaveAgentConfig: (config) => edits.push(config),
    };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    const account = renderer.root.findAllByType("select").find((select) => select.props.id === `df-account-${ids.agent}`);
    // The selected agent is claude_code, so only claude_code logins are offered.
    assert.deepEqual(account.props.children[0].props.children, "provider default");
    assert.deepEqual(account.props.children[1].map((option) => option.props.children), [linked.label]);
    assert.equal(account.props.value, linked.id);

    // Only what the operator changed is sent, and "" clears the selection.
    await act(async () => { account.props.onChange({ currentTarget: { value: "" } }); });
    const form = renderer.root.findAllByProps({ "aria-label": "Agent configuration" })[0].findByType("form");
    await act(async () => { form.props.onSubmit({ preventDefault() {} }); });
    assert.deepEqual(edits.at(-1), { accountId: "" });

    // A shell agent has no logins at all, so it has no account control.
    const shell = { ...state.agents.get(ids.agent), provider: "shell", model: "", reasoning_effort: "", account_id: "" };
    await act(async () => {
      renderer.update(createElement(FactoryConsole, { ...props, state: baseState({ agents: new Map([[shell.id, shell]]) }) }));
    });
    assert.equal(renderer.root.findAllByType("select").some((select) => select.props.id === `df-account-${ids.agent}`), false);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("settings asks the daemon for logins on open and links the one the operator names", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const login = {
      provider: "codex",
      home: "/Users/operator/.codex-dogfood",
      label: ".codex-dogfood",
      email: "operator@example.com",
      organization: "",
      default_model: "gpt-6-astra",
      default_reasoning_effort: "high",
      linked_id: "",
    };
    const asked = [];
    const linkings = [];
    const updates = [];
    const props = {
      status: "ready",
      state: baseState(),
      settingsOpen: true,
      onToggleSettings: () => {},
      onLoadAccounts: () => asked.push("asked"),
      onLinkAccount: (candidate, label) => linkings.push([candidate.home, label]),
      onUpdateAccount: (account, change) => updates.push([account.id, account.revision, change]),
      accounts: [login],
    };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    // Discovery is an observation, so opening SETTINGS is what asks for it.
    assert.deepEqual(asked, ["asked"]);
    const section = renderer.root.findAllByProps({ "aria-label": "ACCOUNTS" })[0];
    assert.ok(section !== undefined);
    const label = renderer.root.findAllByType("input").find((input) => input.props.id === `df-account-label-${login.home}`);
    await act(async () => { label.props.onChange({ currentTarget: { value: "dogfood" } }); });
    const link = renderer.root.findAllByType("button").find((button) => button.props.children === "LINK");
    await act(async () => { link.props.onClick(); });
    assert.deepEqual(linkings, [[login.home, "dogfood"]]);
    const account = [...props.state.accounts.values()][0];
    const linkedLabel = renderer.root.findAllByType("input").find((input) => input.props.id === `df-linked-account-${account.id}`);
    await act(async () => { linkedLabel.props.onChange({ currentTarget: { value: "personal" } }); });
    const button = (name) => renderer.root.findAllByType("button").find((item) => item.props.children === name);
    await act(async () => { button("SAVE LABEL").props.onClick(); });
    assert.deepEqual(updates, [[account.id, account.revision, { label: "personal" }]]);
    assert.equal(button("UNLINK").props.disabled, true, "an agent still references this account");
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, state: { ...props.state, agents: new Map() } })); });
    assert.equal(button("UNLINK").props.disabled, false);
    await act(async () => { button("UNLINK").props.onClick(); });
    assert.deepEqual(updates.at(-1), [account.id, account.revision, { remove: true }]);
    await act(async () => { button("REFRESH ACCOUNTS").props.onClick(); });
    assert.equal(asked.length, 2);


    // The daemon's refusal is shown plainly rather than retried.
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, accountsError: "not_found" })); });
    assert.ok(renderer.root.findAllByProps({ role: "alert" }).length > 0);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("the RULES block saves an idle rule and sends only what changed", async () => {
  const edits = [];
  const props = { status: "ready", state: baseState(), selectedAgent: agentSelection(), onSaveAgentConfig: (config) => edits.push(config) };
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
  const field = (id) => renderer.root.findAll((node) => node.props.id === id)[0];
  const form = () => renderer.root.findAllByType("form")[0];
  // Waiting agents show no instruction controls; choosing the rule reveals them.
  assert.equal(field(`df-idle-instruction-${ids.agent}`), undefined);
  await act(async () => { field(`df-idle-${ids.agent}`).props.onChange({ currentTarget: { value: "standing_instruction" } }); });
  await act(async () => { field(`df-idle-after-${ids.agent}`).props.onChange({ currentTarget: { value: "2" } }); });
  await act(async () => { field(`df-idle-instruction-${ids.agent}`).props.onChange({ currentTarget: { value: "Look for follow-up work." } }); });
  await act(async () => { field(`df-idle-budget-${ids.agent}`).props.onChange({ currentTarget: { value: "3" } }); });
  await act(async () => { form().props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(edits.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 2, idleInstruction: "Look for follow-up work.", idleRunBudget: 3 });
  // A rule the daemon would refuse never leaves the form: no wait means no
  // save, and the form says why.
  await act(async () => { field(`df-idle-after-${ids.agent}`).props.onChange({ currentTarget: { value: "0" } }); });
  const saveButton = () => renderer.root.findAllByType("button").find((button) => button.props.type === "submit");
  assert.equal(saveButton().props.disabled, true);
  const before = edits.length;
  await act(async () => { form().props.onSubmit({ preventDefault() {} }); });
  assert.equal(edits.length, before);
  assert.ok(renderer.root.findAllByType("p").some((paragraph) => String(paragraph.props.children).includes("needs at least a second")));
  await act(async () => { field(`df-idle-after-${ids.agent}`).props.onChange({ currentTarget: { value: "1" } }); });
  assert.equal(saveButton().props.disabled, false);
  await act(async () => { form().props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(edits.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 1, idleInstruction: "Look for follow-up work.", idleRunBudget: 3 });
  // On a spent rule, editing the text leaves the budget out, so the count
  // stands; retyping the same budget sends it, which restarts the count.
  const spent = { ...fixtureState.agents.get(ids.agent), idle_policy: "standing_instruction", idle_after_seconds: 600, idle_instruction: "Look for follow-up work.", idle_run_budget: 3, idle_runs_used: 3 };
  const spentState = baseState({ agents: new Map([...fixtureState.agents, [spent.id, spent]]) });
  const again = [];
  let spentRenderer;
  await act(async () => { spentRenderer = create(createElement(FactoryConsole, { status: "ready", state: spentState, selectedAgent: agentSelection(), onSaveAgentConfig: (config) => again.push(config) })); });
  const spentField = (id) => spentRenderer.root.findAll((node) => node.props.id === id)[0];
  assert.match(renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", state: spentState, selectedAgent: agentSelection(), onSaveAgentConfig: () => {} })), /3 of 3 idle runs used/);
  await act(async () => { spentField(`df-idle-instruction-${ids.agent}`).props.onChange({ currentTarget: { value: "Look for follow-up work, then tidy." } }); });
  await act(async () => { spentRenderer.root.findAllByType("form")[0].props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(again.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 600, idleInstruction: "Look for follow-up work, then tidy." });
  await act(async () => { spentField(`df-idle-budget-${ids.agent}`).props.onChange({ currentTarget: { value: "3" } }); });
  await act(async () => { spentRenderer.root.findAllByType("form")[0].props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(again.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 600, idleInstruction: "Look for follow-up work, then tidy.", idleRunBudget: 3 });
});

test("overseer supervision names worker events and a seconds cooldown", () => {
  const agent = { ...fixtureState.agents.get(ids.agent), role: "orchestrator", idle_policy: "standing_instruction", idle_after_seconds: 10, idle_instruction: "Inspect worker activity.", idle_run_budget: 1 };
  const agents = new Map(fixtureState.agents);
  agents.set(agent.id, agent);
  const markup = render({ state: baseState({ agents }), selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision }, onSaveAgentConfig: () => {} });
  for (const text of ["SUPERVISION", "WHEN WORK CHANGES", "supervise worker activity", "COOLDOWN SECONDS", "initial inspection, then worker events"]) assert.match(markup, new RegExp(text));
  assert.match(markup, new RegExp(`id="df-idle-after-${agent.id}"[^>]*value="10"`));
});

test("PAIRED DEVICES lists what the factory granted and revokes any device but this one", async () => {
  const own = "60".repeat(16);
  const phone = "70".repeat(16);
  const asked = [];
  const revoked = [];
  const props = {
    status: "ready",
    state: baseState(),
    settingsOpen: true,
    onToggleSettings: () => {},
    remoteInviteAllowed: true,
    ownClientId: own,
    onLoadDevices: () => asked.push("asked"),
    onRevokeDevice: (device) => revoked.push(device),
    devices: { clients: [
      { clientId: own, capabilities: 31, revision: 1n, createdAtMs: 1767225600000n },
      { clientId: phone, capabilities: 7, revision: 3n, createdAtMs: 1767139200000n },
    ], more: false },
  };
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
  // The list is an observation, so opening the panel is what asks for it.
  assert.deepEqual(asked, ["asked"]);
  const rows = renderer.root.findAllByProps({ className: "dfFactoryConsole__device" });
  assert.equal(rows.length, 2);
  const label = (row) => [].concat(row.findAllByType("span")[0].props.children).join("");
  assert.equal(label(rows[0]), "BROWSER · THIS BROWSER");
  assert.equal(label(rows[1]), "PHONE");
  const buttons = () => renderer.root.findAllByType("button").filter((button) => ["REVOKE", "CONFIRM REVOKE", "KEEP"].includes(button.props.children));
  assert.equal(buttons().length, 1, "only the phone can be revoked, never this console");
  await act(async () => { buttons()[0].props.onClick(); });
  assert.deepEqual(buttons().map((button) => button.props.children), ["CONFIRM REVOKE", "KEEP"]);
  await act(async () => { buttons().find((button) => button.props.children === "KEEP").props.onClick(); });
  assert.deepEqual(revoked, [], "KEEP revokes nothing");
  await act(async () => { buttons()[0].props.onClick(); });
  await act(async () => { buttons().find((button) => button.props.children === "CONFIRM REVOKE").props.onClick(); });
  assert.deepEqual(revoked, [{ clientId: phone, expectedRevision: 3n }]);

  const empty = render({ settingsOpen: true, onToggleSettings: () => {}, remoteInviteAllowed: true, devices: { clients: [], more: false } });
  assert.match(empty, /PAIRED DEVICES/);
  assert.match(empty, /nothing paired/);
  assert.match(render({ settingsOpen: true, onToggleSettings: () => {}, remoteInviteAllowed: true, devicesError: "unauthorized" }), /DEVICES — UNAUTHORIZED/);
});

test("floor objects select the exact existing task detail and question route", async () => {
  const loaded = [];
  const questions = [];
  const queueSelections = [];
  const props = {
    status: "ready", state: fixtureState, topologies: fixtureTopologies,
    runPaths: new Map([[ids.agent, runSample(ids.agent, ["internal/kernel/state.go"])]]),
    onLoadTaskDetail: async (task) => {
      loaded.push(task);
      return { taskId: task.id, revision: task.revision, head: fixtureState.head, instruction: "Observed task detail", feedback: "", peerQuestions: [] };
    },
  };
  function Harness({ state = fixtureState, editable = false }) {
    const [selectedTaskId, setSelectedTaskId] = useState();
    const [detail, setDetail] = useState("needs-you");
    const [selectedHumanRequest, setSelectedHumanRequest] = useState();
    return createElement(FactoryConsole, {
      ...props,
      state,
      detail,
      onDetail: setDetail,
      selectedTaskId,
      onSelectTask: (taskId) => { queueSelections.push(taskId); setSelectedTaskId(taskId); },
      ...(editable ? { onEditTask: async () => true } : {}),
      selectedHumanRequest,
      onSelectHumanRequest: (request) => { questions.push(request); setSelectedHumanRequest(selectedRequest({ request, question: "Should the migration also cover the users table? The plan only names accounts." })); setDetail("needs-you"); },
      onOpenQueue: () => setDetail("queue"),
    });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  const task = fixtureState.tasks.get(ids.task);
  const rootID = `${ids.project}:${fixtureTopology.nodes[0].id}`;
  await act(async () => { tree.root.findByProps({ "data-enter-room-id": rootID }).props.onClick(); });
  const kernelID = `${ids.project}:${fixtureTopology.nodes[1].id}`;
  await act(async () => { tree.root.findByProps({ "data-enter-room-id": kernelID }).props.onClick(); });
  await act(async () => { tree.root.findByProps({ "data-workbench-task-id": task.id }).props.onKeyDown({ key: "Enter", preventDefault() {} }); });
  assert.equal(loaded.length, 1);
  assert.equal(loaded[0], task);
  assert.deepEqual(queueSelections, [task.id]);
  assert.equal(tree.root.findByProps({ "aria-label": "Task details" }).findByType("h2").children.join(""), `TASK · ${task.id.slice(0, 8)}`);
  await act(async () => { tree.root.findAllByType("button").find((button) => button.children.join("") === "BACK").props.onClick(); });
  assert.equal(tree.root.findByProps({ "aria-label": "Task details" }).findByType("h2").children.join(""), `TASK · ${task.id.slice(0, 8)}`, "navigation retains the existing detail selection");
  await act(async () => { tree.root.findByProps({ "data-enter-room-id": kernelID }).props.onClick(); });
  const running = tree.root.findByProps({ "aria-label": "Running tasks" }).findByType("button");
  await act(async () => { running.props.onClick(); });
  assert.deepEqual(queueSelections, [task.id, task.id]);
  assert.equal(tree.root.findByProps({ "aria-label": "Task details" }).findByType("h2").children.join(""), `TASK · ${task.id.slice(0, 8)}`);
  await act(async () => { tree.root.findByProps({ "data-floor-queue": "" }).props.onKeyDown({ key: "Enter", preventDefault() {} }); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Queue" }).length, 1, "queue remains one canonical panel");
  assert.equal(tree.root.findAllByProps({ "data-floor-queue": "" }).length, 1);
  const queuedTask = fixtureState.tasks.get("32".repeat(16));
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  const queuedRow = tree.root.findAllByProps({ className: "dfConsoleItem__summary" }).find((row) => row.findAllByType("strong").some((strong) => strong.children.join("") === queuedTask.title));
  await act(async () => { queuedRow.props.onClick({ preventDefault() {} }); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "editable queued work stays in its inline editor");
  await act(async () => { tree.root.findByProps({ "data-human-request-id": ids.request }).props.onClick(); });
  assert.equal(questions[0], fixtureState.humanRequests.get(ids.request));
  assert.equal(tree.root.findByProps({ "aria-label": "Selected question" }).findByProps({ className: "dfFactoryConsole__question" }).children.join(""), "Should the migration also cover the users table? The plan only names accounts.");
  const tasks = new Map(fixtureState.tasks);
  tasks.delete(queuedTask.id);
  await act(async () => { tree.update(createElement(Harness, { state: { ...fixtureState, tasks } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "removed work is not retained as invented history");
  await act(async () => tree.unmount());
});

test("dependency projection keeps served identity, hidden endpoints and project boundaries", () => {
  const mirrored = new Map(fixtureTopologies).set(ids.secondProject, { ...fixtureTopology, projectId: ids.secondProject });
  const rootID = `${ids.project}:${fixtureTopology.nodes[0].id}`;
  const view = floorScene(fixtureState, mirrored, undefined, undefined, rootID);
  const kernel = view.topology.nodes.find((node) => node.path === "internal/kernel");
  assert.equal(kernel.language, "go");
  assert.equal(kernel.childCount, 1);
  assert.equal(kernel.dependencies.omitted, 1);
  const store = kernel.dependencies.links.find((link) => link.direction === "to");
  assert.equal(store.nodeId, `${ids.project}:${fixtureTopology.nodes[3].id}`);
  assert.equal(view.topology.nodes.some((node) => node.id === store.nodeId), false, "hidden endpoints retain their exact identity");
  assert.ok(kernel.dependencies.links.every((link) => link.nodeId.startsWith(`${ids.project}:`)), "matching node hashes in another project never cross-link");
  const legacy = new Map([[ids.project, { ...fixtureTopology, dependencies: undefined }]]);
  assert.equal(floorScene(fixtureState, legacy, undefined, undefined, rootID).topology.nodes[0].dependencies, undefined);
  const hostile = new Map([[ids.project, { ...fixtureTopology, dependencies: { ...fixtureTopology.dependencies,
    edges: [{ from: fixtureTopology.nodes[1].id, to: "foreign", weight: 1 }] } }]]);
  assert.deepEqual(floorScene(fixtureState, hostile, undefined, undefined, rootID).topology.nodes.find((node) => node.id === kernel.id).dependencies.links, []);
});
