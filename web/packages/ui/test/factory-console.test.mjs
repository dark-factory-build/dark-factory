import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { createElement, isValidElement, useEffect, useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { MAX_SNAPSHOT_ENTITIES, ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryApp, FactoryConsole } from "../dist/src/index.js";
import { layoutScene } from "../dist/src/factory-scene/scene.js";
import { projectGraph, projectFloor, workRows } from "../dist/src/console-view.js";
import { FactoryFloor, StageMeter } from "../dist/src/console-screens.js";
import { AgentPanel, WorkPanel } from "../dist/src/console-sidebar.js";
import { FactoryScene } from "../dist/src/factory-scene/factory-scene.js";
import { TerminalPanel } from "../dist/src/factory-app.js";
import { fixtureState, fixtureGraphs, fixtureGraph } from "../../../fixtures/state.mjs";
import { graphWith, hex, unit } from "../../../fixtures/graph.mjs";
const rowButton = (row) => row.findByProps({ className: "dfConsoleItem__taskTitle dfWorkRow__title" });
const rowTitle = (row) => rowButton(row).children.join("");
const textOf = (node) => [].concat(node.props.children).flat(Infinity).filter((child) => typeof child === "string").join("").trim();

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
const oneProjectState = () => baseState({ projects: new Map([[ids.project, fixtureState.projects.get(ids.project)]]) });

const render = (props = {}) => renderToStaticMarkup(createElement(FactoryConsole, {
  status: "ready",
  state: baseState(),
  ...props,
}));

function consoleElements(props) {
  let tree;
  act(() => { tree = create(createElement(FactoryConsole, props)); });
  return tree.root.findAll((node) => typeof node.type === "string").map((node) => ({ type: node.type, props: node.props }));
}

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

function floorScene(state, graphs, runPaths, lastRunPaths) {
  return projectFloor(state, projectGraph(graphs, [...state?.projects.keys() ?? []].sort()), runPaths, lastRunPaths);
}

const runSample = (agentId, paths, taskId = ids.task, taskRevision = fixtureState.tasks.get(taskId)?.revision ?? 1n) => ({
  taskId,
  taskRevision,
  runId: "71".repeat(16),
  projectId: fixtureState.agents.get(agentId).project_id,
  paths,
});

const shellAgent = { id: ids.agent, project_id: ids.project, name: "Shell Hand", role: "worker", provider: "shell", paused: true, model: "", reasoning_effort: "", effective_model: "", effective_reasoning_effort: "", model_source: "", revision: 10n };

const withAgent = (agent) => render({
  detail: "agent",
  state: baseState({ agents: new Map([[agent.id, agent]]) }),
  selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision },
});

// The selectable sprite of a non-archived agent on the floor.
const workerSprite = (tree, id) => tree.root.findByProps({ "data-worker-id": id }).find((node) => node.type === "g" && node.props.role === "button");

const inheritingAgent = () => fixtureState.agents.get("23".repeat(16));

test("error banner keeps its centered layout after the paragraph reset", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /\.dfFactoryConsole :where\(h1, h2, p, dl, ul\),[\s\S]*?\.dfConsoleSidebar :where\(h1, h2, h3, p, dl, ul\)\s*\{\s*margin: 0;\s*\}/);
  assert.match(css, /\.dfFactoryConsole__error\s*\{[\s\S]*?margin: 0 auto 1\.25rem;/);
  assert.match(css, /\.dfFactoryFloor__map \{ position: absolute; inset: 0; overflow: auto; height: 100%;/);
  assert.match(css, /\.dfFactoryTooltip \{[^}]*-webkit-line-clamp: 8;[^}]*pointer-events: none;/, "a tooltip stays short enough to need no scrolling, and never takes the click meant for what it covers");
  assert.match(render(), /class="dfFactoryFloor__map" role="region" aria-label="Scrollable factory floor" tabindex="0"/);
  assert.equal(css.includes("@keyframes dfFactoryScene"), false);
});

test("one screen keeps Factory and the operator panels together", () => {
  const markup = render();
  assert.match(markup, /<main class="dfFactoryConsole" aria-label="Factory operator console">/);
  for (const label of ["Factory floor", "Selected detail", "Work", "Console views"]) {
    assert.match(markup, new RegExp(`aria-label="${label}"`));
  }
  assert.doesNotMatch(markup, /aria-label="Right panel"/);
  assert.equal((markup.match(/aria-label="Console views"/g) ?? []).length, 1);
  assert.doesNotMatch(markup, /ACTIVE RUNS|OPERATOR VIEW/);
  assert.equal(markup.includes("<dt>QUEUED</dt>"), false);
  assert.equal(markup.includes("<dt>NEEDS YOU</dt>"), false);
  assert.match(markup, /Work <span class="dfBadge">1<\/span>/);
  assert.doesNotMatch(markup, /aria-label="Work filter"|Show overseer and specialist passes|Needs you <span/);
  assert.match(markup, /Builder One asks/);
  assert.match(markup, /Review the state projection/);
  assert.match(markup, /Review the state projection[\s\S]*North Workshop · Builder One asks/);
  assert.equal(markup.includes("By mission"), false);
  assert.equal(markup.includes("Decision needed"), false, "an unopened request stays brief");
  assert.match(render({ detail: "work" }), /aria-label="Work"/);
  // No screen union survives: there is no navigation away from this screen.
  assert.equal(markup.includes("dfFactoryConsole__homeLink"), false);
  assert.equal(markup.includes("BUILDING STATE UNAVAILABLE"), false);
});

test("a selected decision names the action and keeps one collapse control", () => {
  const markup = render({ selectedHumanRequest: selectedRequest(), onCloseHumanRequest: () => {}, onReplyHumanRequest: () => {}, onCancelHumanRequest: () => {} });
  assert.match(markup, /<h3>Decision needed<\/h3>/);
  assert.match(markup, />Stop task<\/button>/);
  assert.equal(markup.includes(">Close</button>"), false);
});

test("a suggested answer sends on the one tap it looks like", () => {
  const calls = [];
  const elements = consoleElements({ status: "ready", state: baseState(), selectedHumanRequest: selectedRequest({ options: ["Continue", "Stop"] }), onHumanReplyChange: (value) => calls.push(value), onReplyHumanRequest: () => calls.push("sent") });
  elements.find((element) => element.type === "button" && Array.isArray(element.props.children) && element.props.children[0] === "Continue").props.onClick();
  assert.deepEqual(calls, ["Continue", "sent"], "the floor sends too: one shared control, one behaviour");
  const markup = render({ selectedHumanRequest: selectedRequest({ options: ["Continue", "Stop"] }) });
  assert.match(markup, />Continue · Recommended<\/button>/);
  assert.match(markup, />Stop<\/button>/);
});

test("read-only decisions retain disabled suggestions and explain their status", () => {
  const open = render({ selectedHumanRequest: selectedRequest({ options: ["Keep accounts", "Include users"], canReply: false }) });
  assert.match(open, /aria-label="Suggested answers"/);
  assert.match(open, />Keep accounts · Recommended<\/button>/);
  assert.match(open, />Include users<\/button>/);
  assert.match(open, /<button type="button" disabled="">Keep accounts/);
  assert.match(open, /This open decision is read-only in this view\./);
  assert.equal(open.includes("Your answer"), false);

  const deliveryUnknown = render({ selectedHumanRequest: selectedRequest({ request: { ...fixtureState.humanRequests.get(ids.request), status: "delivery_unknown" }, canReply: false }) });
  assert.match(deliveryUnknown, /This decision is delivery unknown\./);
});

test("viewed geometry is independent of worker activity and population", () => {
  const baseline = floorScene(fixtureState, fixtureGraphs);
  const extra = { ...fixtureState.agents.get(ids.idleAgent), id: "ab".repeat(16), name: "Extra resting worker" };
  const changed = { ...fixtureState, agents: new Map([...fixtureState.agents].reverse()).set(extra.id, extra) };
  const overlay = floorScene(changed, fixtureGraphs, new Map([[ids.agent, runSample(ids.agent, ["web"])]]));
  assert.deepEqual(overlay.graph, baseline.graph);
  assert.deepEqual(layoutScene(overlay.graph).stations, layoutScene(baseline.graph).stations);
  assert.deepEqual(new Set(overlay.workers.map((worker) => worker.id)), new Set(changed.agents.keys()));
  assert.equal(baseline.workers.find((worker) => worker.id === ids.agent).location, "unobserved");
  const web = baseline.graph.units.find((item) => item.label === "web").id;
  assert.equal(overlay.workers.find((worker) => worker.id === ids.agent).nodeId, web);
  const observed = floorScene(fixtureState, fixtureGraphs, undefined,
    new Map([[ids.idleAgent, runSample(ids.idleAgent, ["web"], "72".repeat(16))]]))
    .workers.find((worker) => worker.id === ids.idleAgent);
  assert.deepEqual([observed.location, observed.nodeId, observed.locationLabel], ["last-observed", web, "web"]);
  const fallback = floorScene(fixtureState, undefined);
  assert.deepEqual(fallback.graph.units, [], "no graph served, no units invented");
  assert.ok(fallback.workers.every((worker) => worker.nodeId === undefined));
  const empty = floorScene(undefined, undefined);
  assert.deepEqual([empty.graph.units, empty.workers, empty.tasks], [[], [], []]);
});

test("floor tasks retain every served task and its exact observed run", () => {
  const scene = floorScene(fixtureState, fixtureGraphs, new Map([[ids.agent, runSample(ids.agent, ["web", "internal/kernel/store"])] ]));
  const unitId = (label) => scene.graph.units.find((item) => item.label === label).id;
  assert.deepEqual(scene.tasks, [
    {
      id: ids.task,
      agentId: ids.agent,
      projectId: ids.project,
      title: "Review the state projection",
      status: "running",
      observation: { taskRevision: fixtureState.tasks.get(ids.task).revision, runId: "71".repeat(16) },
      humanRequestIds: [ids.request],
    },
    {
      id: "32".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.secondProject,
      title: "Tighten the queue ordering",
      status: "queued",
      humanRequestIds: [],
    },
    {
      id: "33".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.project,
      title: "Close the resize race",
      status: "succeeded",
      humanRequestIds: [],
    },
    {
      id: "34".repeat(16),
      agentId: ids.idleAgent,
      projectId: ids.secondProject,
      title: "Probe the flaky gate",
      status: "failed",
      humanRequestIds: [],
    },
  ]);
  const worker = scene.workers.find((item) => item.id === ids.agent);
  assert.deepEqual([worker.nodeId, worker.observedBayId], [unitId("kernel"), "a4".repeat(16)], "at the store the run changed, in the unit holding it");
});

test("task footprints reject stale, retry, cancelled, terminal, and cross-project samples", () => {
  const original = fixtureState.tasks.get(ids.task);
  const sample = runSample(ids.agent, ["web"]);
  const footprint = (task, candidate = sample) => floorScene(
    baseState({ tasks: new Map([[task.id, task]]) }), fixtureGraphs, new Map([[ids.agent, candidate]]),
  ).tasks[0];
  for (const status of ["queued", "blocked", "succeeded", "failed", "cancelled"]) {
    const order = footprint({ ...original, status });
    assert.equal(order.observation, undefined, status);
  }
  const retry = { ...original, revision: original.revision + 1n };
  assert.equal(footprint(retry).observation, undefined);
  const crossProject = { ...original, project_id: ids.secondProject };
  assert.equal(footprint(crossProject, { ...sample, projectId: ids.secondProject }).observation, undefined);
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
    new Map(fixtureGraphs).set(ids.secondProject, graphWith([unit(0x71, "south", ["."])])),
    new Map([
      [ids.agent, runSample(ids.agent, ["web"])],
      [ids.orchestrator, { taskId: southTask.id, taskRevision: southTask.revision, runId: "73".repeat(16), projectId: ids.secondProject, paths: ["."] }],
    ]),
  );
  assert.deepEqual(scene.tasks.map((order) => [order.id, order.projectId, order.humanRequestIds]), [
    [southTask.id, ids.secondProject, [southRequest.id]],
    [ids.task, ids.project, [request.id]],
  ]);
  assert.equal(scene.tasks[0].observation.runId, "73".repeat(16));
});

const served = (...views) => new Map(views.map((view) => [view.project_id, view]));

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
  const worker = floorScene(state, fixtureGraphs).workers.find((item) => item.id === ids.orchestrator);
  assert.deepEqual([worker.location, worker.nodeId, worker.locationLabel], ["unobserved", undefined, undefined]);
  const observed = floorScene(state, fixtureGraphs, new Map([[ids.orchestrator, {
    taskId: task.id, taskRevision: task.revision, runId: "71".repeat(16), projectId: ids.project, paths: ["internal/kernel"],
  }]]))
    .workers.find((item) => item.id === ids.orchestrator);
  assert.equal(observed.location, "working", "an observed overseer keeps the observed unit");
});





test("hostile names and titles are escaped as text and private detail is absent", () => {
  const hostile = "<img src=x onerror=alert(1)>";
  const hostileState = baseState({
    agents: new Map([[ids.agent, { id: ids.agent, project_id: ids.project, name: hostile, role: "worker", provider: "claude_code", paused: false, model: hostile, reasoning_effort: "", effective_model: hostile, effective_reasoning_effort: "", model_source: "agent", revision: 10n }]]),
    tasks: new Map([[ids.task, { id: ids.task, project_id: ids.project, assigned_agent_id: ids.agent, title: hostile, status: "running", priority: 10, revision: 12n }]]),
  });
  for (const view of VIEWS) {
    // The second pass renders the selected agent, so the hostile model reaches
    // its heading rather than being dropped with the whole panel.
    for (const props of [{ view }, { view, selectedAgent: agentSelection() }]) {
      const markup = render({ state: hostileState, ...props });
      assert.equal(markup.includes("<img"), false, view);
      assert.equal(markup.includes("question"), false, view);
    }
  }
  assert.match(render({ state: hostileState }), /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(render({ state: hostileState, detail: "agent", selectedAgent: agentSelection() }), /claude_code · &lt;img src=x onerror=alert\(1\)&gt;/);
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
    ["agent", render({ detail: "agent", selectedAgent: agentSelection() })],
    ["settings", render({ settingsOpen: true, onToggleSettings: () => {} })],
    ["terminal", renderToStaticMarkup(terminalView())],
    ["terminal-reset", renderToStaticMarkup(terminalView({ terminal: { resets: 1 } }))],
  ];
  // "overseer" and private issue review left this list by owner decision: they
  // are console controls. Everything else is still kernel grammar. The lease
  // ban is anchored at a word start so it still catches lease/leased/leases
  // without catching the floor's "release-ready". The floor's quarantine bay is
  // the operational plant's own word for unexplained runtime activity.
  for (const [name, markup] of surfaces) {
    for (const forbidden of [/attempt/i, /converge/i, /finalize/i, /unresolved/i, /verdict/i, /\bALLOW\b/, /\bBLOCK\b/, /\blease/i, /work item/i, /cancel run/i]) {
      assert.equal(forbidden.test(markup), false, `${name}: ${forbidden}`);
    }
  }
  assert.match(render({ detail: "agent", selectedAgent: agentSelection(ids.orchestrator) }), />Overseer · /);
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

test("Work keeps every served item reachable", () => {
  const emptyState = baseState({ projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map() });
  assert.match(render({ state: emptyState }), /No open work/);
  assert.match(render({ state: emptyState, detail: "agent" }), /No open work/, "no agent to show leaves Work, never an empty sidebar");

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
  assert.equal((markup.match(/<li class="dfConsoleItem dfWorkRow"/g) ?? []).length, 9);
  assert.equal((markup.match(/\+1 more/g) ?? []).length, 0);
  assert.doesNotMatch(markup, /9 items/);
  assert.match(markup, /Task 8/);
});

test("Work keeps running tasks visible beside queued ones", () => {
  const markup = render({ detail: "work", state: baseState({ humanRequests: new Map() }) });
  assert.match(markup, /data-stage="running"/);
  assert.doesNotMatch(markup, /Briefs are editable while queued/);
  const queuedOnly = baseState({ tasks: new Map([...fixtureState.tasks].filter(([, task]) => task.status === "queued")) });
  assert.doesNotMatch(render({ detail: "work", state: queuedOnly }), /Briefs are editable while queued/);
  assert.match(markup, /dfConsoleItem__taskTitle/);
  assert.match(markup, /Review the state projection/);
  assert.equal((markup.match(/aria-label="Work"/g) ?? []).length, 1, "one work panel");
});

test("Work ranks an open request and a blocked task above queued work, Work badge 2", async () => {
  const blocked = { ...fixtureState.tasks.get(ids.task), id: "b3".repeat(16), title: "Stuck", status: "blocked", blocked_reason: "why" };
  const state = baseState({ tasks: new Map([...fixtureState.tasks, [blocked.id, blocked]]) });
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state, detail: "work", onDetail() {} })); });
  const work = tree.root.findByProps({ "aria-label": "Console views" }).findAllByType("button").find((button) => textOf(button).startsWith("Work"));
  assert.equal(work.findByProps({ className: "dfBadge" }).children.join(""), "2");
  const titles = tree.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem__taskTitle dfWorkRow__title" }).map((node) => node.children.join(""));
  const queued = titles.indexOf("Tighten the queue ordering");
  assert.ok(queued > titles.indexOf("Review the state projection") && queued > titles.indexOf("Stuck") && titles.includes("Stuck"), "needs-you rows lead, queued work follows");
  await act(async () => tree.unmount());
});

test("an overseer pass is shown in Work", () => {
  const overseerTask = { ...fixtureState.tasks.get(ids.task), assigned_agent_id: ids.orchestrator };
  assert.match(render({ detail: "work", state: baseState({ tasks: new Map([[overseerTask.id, overseerTask]]), humanRequests: new Map() }) }), /Review the state projection/);
});

test("a succeeded task linked to an open PR is in review with its PR cell", () => {
  const done = { ...fixtureState.tasks.get(ids.task), status: "succeeded" };
  const state = baseState({ tasks: new Map([[done.id, done]]), humanRequests: new Map() });
  const pr = { visualId: "change:1", projectId: done.project_id, repository: "o/r", tasks: [done.id], missions: [], pullRequest: { number: 7, state: "open", title: "Ship", url: "https://github.com/o/r/pull/7", head: "a".repeat(40) }, review: { state: "pending" }, checks: [], deliveries: [], reviewers: [], completed: false, completedAt: 0, status: "open", nextAction: "" };
  const markup = renderToStaticMarkup(createElement(WorkPanel, { state, rows: workRows(state, [pr]), ready: true }));
  assert.match(markup, /data-stage="in-review">in review/);
  assert.match(markup, /<a href="https:\/\/github.com\/o\/r\/pull\/7" target="_blank" rel="noreferrer">PR #7<\/a>/);
  assert.doesNotMatch(markup, /aria-label="Production inspection"/);
  assert.match(markup, /Review the state projection/);
});

test("a task-less production row opens its pull request and keeps its stages", () => {
  const pr = { visualId: "change:taskless", projectId: ids.project, repository: "o/r", tasks: [], missions: [], pullRequest: { number: 8, state: "open", title: "Taskless", url: "https://github.com/o/r/pull/8", head: "b".repeat(40) }, review: { state: "pending" }, checks: [], deliveries: [], reviewers: [], completed: false, completedAt: 0, status: "open", nextAction: "" };
  const markup = renderToStaticMarkup(createElement(WorkPanel, { state: fixtureState, rows: workRows(fixtureState, [pr]), ready: true }));
  assert.match(markup, /<a class="dfConsoleItem__taskTitle dfWorkRow__title" href="https:\/\/github.com\/o\/r\/pull\/8" target="_blank" rel="noreferrer">Taskless<\/a>/);
  assert.match(markup, /data-stage="in-review">in review/);
});

test("Work is one ranked list: blocked work first, every origin shown, flags dispatch off and queues new work", async () => {
  const running = fixtureState.tasks.get(ids.task);
  const supervisor = fixtureState.agents.get(ids.orchestrator);
  const state = baseState({
    factory: { ...fixtureState.factory, dispatch_enabled: false },
    tasks: new Map([
      ...fixtureState.tasks,
      ["b1".repeat(16), { ...running, id: "b1".repeat(16), title: "Stuck on a prerequisite", status: "blocked", blocked_reason: "Needs a human to resolve a merge conflict <script>" }],
      ["b2".repeat(16), { ...running, id: "b2".repeat(16), title: "Standing instruction", status: "running", assigned_agent_id: supervisor.id }],
      ["b3".repeat(16), { ...running, id: "b3".repeat(16), title: "Overseer stuck", status: "blocked", assigned_agent_id: supervisor.id }],
    ]),
  });
  const added = [];
  const edits = [];
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", detail: "work", state, onDetail() {}, onEditTask: async (task, change) => { edits.push([task.id, change]); return true; }, onAddTask: async (agent, instruction, mode) => { added.push([agent.id, instruction, mode]); return true; } })); });
  const panel = renderer.root.findByProps({ "aria-label": "Work" });
  assert.ok(panel.findAllByType("button").some((button) => button.children.join("") === "Stuck on a prerequisite"));
  // The block reason is agent-written free text: it renders as plain text
  // (React's default escaping), never through dangerouslySetInnerHTML.
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "work", state, onDetail() {} }));
  assert.match(markup, /Needs a human to resolve a merge conflict &lt;script&gt;/);
  await act(async () => { panel.findByProps({ "aria-label": "Cancel Stuck on a prerequisite" }).props.onClick(); });
  assert.deepEqual(edits, [["b1".repeat(16), { cancel: true }]], "a blocked task is cleared in one step");
  await act(async () => { panel.findByProps({ "aria-label": "Retry Stuck on a prerequisite" }).props.onClick(); });
  assert.deepEqual(edits.at(-1), ["b1".repeat(16), { retry: true }], "or sent round again in one step");
  const titled = (title) => panel.findAllByType("button").some((button) => button.children.join("") === title);
  assert.ok(titled("Standing instruction") && titled("Overseer stuck"), "overseer passes are never hidden by origin");
  assert.equal(panel.findAllByProps({ "aria-label": "Work filter" }).length + panel.findAllByProps({ "aria-label": "Show overseer and specialist passes" }).length, 0, "no filter or reveal control");
  const order = panel.findAllByProps({ className: "dfConsoleItem__taskTitle dfWorkRow__title" }).map((row) => row.children.join(""));
  assert.ok(order.indexOf("Overseer stuck") < order.indexOf("Tighten the queue ordering") && order.indexOf("Stuck on a prerequisite") < order.indexOf("Tighten the queue ordering"), "needs-you rows rank above queued work");
  assert.ok(order.indexOf("Overseer stuck") < order.indexOf("Standing instruction"), "needs-you rows rank above running work");
  assert.ok(panel.findAllByProps({ role: "status" }).some((node) => node.children.join("").includes("New work is paused")));
  // Admission is CLI-only: the paused status names factoryctl and no header button offers to change it.
  assert.match(markup, /<p role="status">New work is paused\. Queued tasks wait; active processes continue\. Resume with <code>factoryctl dispatch on<\/code>\.<\/p>/);
  assert.doesNotMatch(markup, /Pause new work|Resume new work|dfConsoleBar__actions/);
  const form = panel.findByProps({ "aria-label": "New task" });
  const values = { target: `any:${ids.project}`, instruction: " Ship it " };
  const nativeFormData = globalThis.FormData;
  globalThis.FormData = class { get(name) { return values[name]; } };
  try { await act(async () => { form.props.onSubmit({ preventDefault() {}, currentTarget: { reset() {} } }); }); }
  finally { globalThis.FormData = nativeFormData; }
  assert.equal(added.length, 1);
  assert.deepEqual(added[0].slice(1), [" Ship it ", "any"]);
  assert.equal(fixtureState.agents.get(added[0][0]).role, "worker", "shared work is queued through a worker of that project");
});

test("a terminal blocked task is neither building nor current agent work", () => {
  const state = baseState({
    tasks: new Map([[ids.task, { ...fixtureState.tasks.get(ids.task), status: "blocked" }]]),
    humanRequests: new Map(),
  });
  const markup = render({ state, graphs: fixtureGraphs });
  assert.match(markup, /aria-label="Builder One, worker, waiting, ready in resting area"/);
  assert.equal(markup.includes('aria-label="Builder One, worker, busy'), false);
});

test("the production console exposes no speculative or unsupported surface", () => {
  for (const view of VIEWS) {
    const markup = render({ view, graphs: new Map(fixtureGraphs).set(ids.secondProject, graphWith([])) }).toLowerCase();
    for (const text of ["not yet served", "awaiting deploy", "suggestions", "add work", ">accept</button>", "dismiss", "task record"]) {
      assert.equal(markup.includes(text), false, `${view}: ${text}`);
    }
  }
});

test("an unavailable snapshot is explicit and does not invent runtime state", () => {
  const markup = render({ state: undefined, status: "syncing" });
  assert.match(markup, /Waiting for the latest state…/);
  assert.match(markup, /Waiting for the latest state…/);
  assert.match(markup, /Connection status: Syncing/);
  assert.match(markup, /Work <\/button>/);
  assert.equal(markup.includes("NO QUEUED TASKS"), false);
  assert.equal(markup.includes("all quiet"), false);
});

test("HumanRequest delivery states remain visibly distinct", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  for (const [status, stage] of [["open", "needs-you"], ["delivering", "delivering"], ["delivery_unknown", "delivery_unknown"]]) {
    const markup = render({ state: baseState({ humanRequests: new Map([[request.id, { ...request, status }]]) }) });
    assert.match(markup, new RegExp(`data-stage="${stage}">${stage.replace("_", " ").replace("-", " ")}</span>`));
  }
});

test("the two-column console keeps one mounted terminal slot", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /\.dfConsoleRow__agent\s*\{[^}]*min-width: 0;[^}]*overflow-wrap: anywhere;/);
  assert.match(css, /\.dfFactoryConsole__terminalPanel :where\(p\)\s*\{\s*margin: 0;/);
  assert.match(css, /\.dfConsoleRow\s*\{[^}]*flex-wrap: wrap;/);
  assert.match(css, /\.dfConsoleLayout\s*\{[^}]*grid-template-columns: minmax\(0, 2fr\) minmax\(0, 1fr\);/);
  assert.match(css, /\.dfFactoryConsole__instructionActions\s*\{[^}]*display: flex;[^}]*flex-wrap: wrap;/);
  assert.match(css, /\.dfConsoleShell\s*\{[^}]*font-family: var\(--df-font-mono\)/);
  assert.match(css, /:focus-visible\s*\{\s*outline: 2px solid var\(--df-console-accent\);/);

  const withTerminal = render({ selectedAgent: agentSelection(), terminalContent: createElement("section", { "aria-label": "Agent terminal" }) });
  assert.match(withTerminal, /<h1>Dark Factory<\/h1>/);
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
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, detail: "work" })); });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, detail: "work" })); });
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
    return createElement(FactoryConsole, {
      status: "ready",
      state: baseState(),
      selectedAgent: agentSelection(),
      selectedHumanRequest: selectedRequest(),
      detail,
      onDetail: setDetail,
      onOpenTerminalForHumanRequest: () => setDetail("agent"),
      terminalContent: createElement("p", null, "terminal"),
    });
  }
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(ConsoleHarness)); });
    await act(async () => { renderer.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.secondProject } }); });
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Terminal" }).length, 0);
    await act(async () => { renderer.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: "" } }); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => textOf(button).startsWith("Work")).props.onClick(); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "Open terminal").props.onClick(); });
    assert.equal(renderer.root.findByProps({ "aria-label": "Project" }).props.value, ids.project, "opening a global question switches to its agent’s project");
    assert.ok(renderer.root.findByProps({ "aria-label": "Terminal" }).findByProps({ children: "terminal" }));
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("selecting an agent shows its status and terminal with no configuration controls", () => {
  const markup = render({
    detail: "agent",
    selectedAgent: agentSelection(),
    onSelectAgent: () => {},
    onEditTask: () => {},
  });
  assert.match(markup, /aria-label="Agent Builder One"/);
  assert.match(markup, /Builder One[\s\S]*?claude_code · claude-opus-5/);
  assert.match(markup, /class="dfConsoleSidebar__status"/);
  assert.match(markup, /<section class="dfConsoleSidebar__section dfConsoleSidebar__terminalSlot" aria-label="Terminal">/);
  for (const absent of ['aria-label="Agent controls"', 'aria-label="Agent configuration"', ">Settings<", ">Pause<", ">Resume<"]) assert.equal(markup.includes(absent), false, absent);
  assert.equal(markup.includes("aria-label=\"Agent queue\""), false);
  assert.match(markup, /dfConsoleLayout__left/);
  assert.equal(markup.includes("dfConsoleLayout__right"), false);

  // Without handlers the sidebar is a readout, never a dead form.
  const readOnly = render({ selectedAgent: agentSelection() });
  assert.equal(readOnly.includes('name="instruction"'), false);
  assert.equal(readOnly.includes("Open terminal"), false);
});

test("the agent panel shows recorded effort only for the current run that recorded it", () => {
  const telemetry = { tokens_in: 12000, tokens_out: 400, cost_micro_usd: 310000, tool_calls: 18, api_requests: 9, quiet_seconds: 4 };
  assert.equal(fixtureState.tasks.get(ids.task).assigned_agent_id, ids.agent);
  assert.equal(fixtureState.tasks.get(ids.task).status, "running");
  const withTelemetry = render({ selectedAgent: agentSelection(), runPaths: new Map([[ids.agent, { ...runSample(ids.agent, []), telemetry }]]) });
  assert.match(withTelemetry, /aria-label="Agent Builder One"[\s\S]*>12\.4k tokens · \$0\.31 · 18 tool calls</);
  for (const runPaths of [undefined, new Map([[ids.agent, runSample(ids.agent, [])]]), new Map([[ids.agent, { ...runSample(ids.agent, [], ids.task, 99n), telemetry }]])]) {
    assert.doesNotMatch(render({ selectedAgent: agentSelection(), runPaths }), /tokens|tool call/);
  }
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
  assert.match(markup, />Queue paused</);
  assert.equal(markup.includes("Queued · waiting for capacity"), false);
});

test("unclaimed shared work waits under its project until an eligible worker claims it", () => {
  const template = [...fixtureState.tasks.values()].find((task) => task.status === "queued");
  const shared = { ...template, id: "3a".repeat(16), title: "Anyone free", assigned_agent_id: "" };
  const markup = render({
    state: baseState({ tasks: new Map([[shared.id, shared]]) }),
    detail: "work",
    selectedAgent: agentSelection(),
    onEditTask: () => {},
  });
  assert.match(markup, /Anyone free/);
  assert.match(markup, /Any eligible worker/);
  assert.doesNotMatch(markup, /Priority/);
  assert.doesNotMatch(markup, /df-assign-/);
});

test("a queued row uses the shared Work row and only selects it", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const selections = [];
    const queued = { ...fixtureState.tasks.get([...fixtureState.tasks.keys()][1]), title: "Queued task", assigned_agent_id: ids.agent };
    const props = {
      status: "ready",
      state: baseState({ tasks: new Map([[queued.id, queued]]) }),
      detail: "work",
      selectedAgent: agentSelection(),
      onSelectTask: (taskId) => selections.push(taskId),
    };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    const work = renderer.root.findByProps({ "aria-label": "Work" });
    const row = work.findAllByProps({ className: "dfConsoleItem dfWorkRow" }).find((item) => rowTitle(item) === queued.title);
    assert.equal(row.findAllByType("button").some((button) => button.props.children === "Edit brief"), false);
    assert.equal(row.findAllByType("button").some((button) => button.props["aria-label"] === `Increase priority for ${queued.title}`), false);
    assert.equal(row.findAllByType("select").some((select) => select.props.id === `df-assign-${queued.id}`), false);
    await act(async () => { rowButton(row).props.onClick(); });
    assert.deepEqual(selections, [queued.id]);
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Task details" }).length, 0);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("a rejected edit says plainly that the durable value did not change", () => {
  const markup = render({
    selectedAgent: agentSelection(),
    edit: { target: ids.agent, pending: false, error: new SessionError("stale") },
  });
  assert.match(markup, /Someone else changed this\. Reopen it and try again\./);
  assert.match(markup, /role="alert"/);
  assert.equal((markup.match(/role="alert"/g) ?? []).length, 1, "an agent refusal has one shared alert");
  const unknown = render({ selectedAgent: agentSelection(), edit: { target: ids.agent, pending: false, error: { code: "internal" } } });
  assert.match(unknown, /The edit did not complete\./);
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
    const markup = render({ state, detail: "work", edit: rejected });
    assert.match(markup, /Someone else changed this\. Reopen it and try again\./);
    assert.equal((markup.match(/role="alert"/g) ?? []).length, 1);
  }
});

test("agent panels render plain portraits without appearance editing", () => {
  for (const view of VIEWS) {
    const markup = render({ view, onSelectAgent: () => {}, selectedAgent: agentSelection(), graphs: fixtureGraphs });
    assert.doesNotMatch(markup, /Edit appearance for|dfSpriteEditor/);
  }
});

test("the floor is the only agent picker: no Agents view, roster or All agents control", () => {
  const floor = render({ detail: "agent", onDetail() {}, onSelectAgent() {} });
  assert.doesNotMatch(floor, /aria-label="Left view"/);
  assert.doesNotMatch(floor, /aria-label="Agents"|dfAgentList|All agents|>Agents</);
  assert.match(floor, /aria-label="Factory floor"/);
  assert.match(floor, /aria-label="Pull request progress"/);
});

test("FactoryApp server-renders without reading browser globals", () => {
  const markup = renderToStaticMarkup(createElement(FactoryApp));
  assert.match(markup, /Factory operator console/);
  assert.match(markup, />Idle</);
  assert.match(markup, /Waiting for the latest state…/);
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
  assert.match(markup, /aria-expanded="true"[^>]*>Review the state projection<\/button>/);
  assert.match(markup, /<article class="dfFactoryConsole__humanRequest"/);
  assert.match(markup, /North Workshop · Builder One asks/);
  assert.match(markup, /&lt;script&gt;steal\(authority\)&lt;\/script&gt;/);
  assert.equal(markup.includes("<script>"), false);
  assert.match(markup, /<textarea[^>]*>&lt;reply&gt;<\/textarea>/);
  assert.match(markup, />Answer</);
  assert.match(markup, />Stop task</);
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

  const requestElements = consoleElements(baseProps);
  requestElements.find((element) => element.type === "button" && element.props.className === "dfConsoleItem__taskTitle dfWorkRow__title").props.onClick();
  assert.equal(calls[0][0], "select");
  assert.equal(calls[0][1], request);
  const busyElements = consoleElements({ ...baseProps, selectedHumanRequest: selectedRequest({ phase: "replying" }) });
  const busySummary = busyElements.find((element) => element.type === "button" && element.props.className === "dfConsoleItem__taskTitle dfWorkRow__title");
  assert.equal(busySummary.props.disabled, true);
  assert.equal(calls.length, 1, "an in-flight answer cannot be collapsed or switched");

  const selectedElements = consoleElements({ ...baseProps, selectedHumanRequest: selectedRequest() });
  selectedElements.find((element) => element.type === "textarea").props.onChange({ currentTarget: { value: "Proceed." } });
  let prevented = false;
  selectedElements.find((element) => element.type === "form").props.onSubmit({ preventDefault: () => { prevented = true; } });
  selectedElements.find((element) => element.type === "button" && element.props.children === "Stop task").props.onClick();
  selectedElements.find((element) => element.type === "button" && element.props.className === "dfConsoleItem__taskTitle dfWorkRow__title").props.onClick();
  assert.equal(prevented, true);
  assert.deepEqual(calls.slice(1), [["change", "Proceed."], ["reply"], ["cancel"], ["close"]]);
});

test("agent and question terminal actions expose only current public intent", async () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const agent = fixtureState.agents.get(ids.orchestrator);
  const calls = [];
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, {
    status: "ready", state: baseState(), graphs: fixtureGraphs, detail: "agent", selectedHumanRequest: selectedRequest(),
    onSelectAgent: (value) => calls.push(["agent", value]),
    onOpenTerminalForHumanRequest: (value) => calls.push(["request", value]),
  })); });
  await act(async () => { workerSprite(renderer, agent.id).props.onClick(); });
  await act(async () => { renderer.root.findAllByType("button").filter((element) => element.props.children === "Open terminal").at(-1).props.onClick(); });
  assert.equal(calls[0][0], "agent");
  assert.equal(calls[0][1].id, agent.id);
  assert.equal(calls[0][1].revision, agent.revision);
  assert.deepEqual(calls[1], ["request", request]);
  await act(async () => { renderer.unmount(); });

  const markup = render({ selectedAgent: agentSelection(), terminalContent: createElement("div", null, "<raw-output>") });
  assert.match(markup, /&lt;raw-output&gt;/);
  assert.equal(markup.includes("runId"), false);
  assert.equal(markup.includes("sessionId"), false);
});

test("the console shows the model an agent will actually run with", () => {
  // An agent that names no model still runs with one. The panel header says
  // which, so a blank is never read as "no model".
  assert.match(withAgent(inheritingAgent()), /Builder Two[\s\S]*?codex · gpt-6-astra/);
  // The provider with no model says nothing extra rather than a dangling dot.
  assert.match(withAgent(shellAgent), /Shell Hand[\s\S]*?shell/);
});

test("archived workers stay off the floor and stale selection has no panel", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const worker = { ...fixtureState.agents.get(ids.agent), archived: true, paused: true };
  const state = baseState({ agents: new Map([[worker.id, worker]]) });
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state, graphs: fixtureGraphs, detail: "agent", onSelectAgent() {} })); });
    assert.equal(renderer.root.findAllByProps({ "data-worker-id": worker.id }).length, 0);
    await act(async () => { renderer.update(createElement(FactoryConsole, { status: "ready", state, detail: "agent", selectedAgent: { id: worker.id, name: worker.name, revision: worker.revision } })); });
    const text = JSON.stringify(renderer.toJSON());
    assert.equal(text.includes("Restore paused"), false);
    assert.equal(renderer.root.findAllByProps({ "aria-label": `Agent ${worker.name}` }).length, 0);
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Terminal" }).length, 0);
    assert.equal(text.includes("Queue paused"), false);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("task selection highlights the Work row and floor questions open the existing question route", async () => {
  const questions = [];
  const queueSelections = [];
  function Harness({ state = fixtureState }) {
    const [selectedTaskId, setSelectedTaskId] = useState();
    const [detail, setDetail] = useState("work");
    const [selectedHumanRequest, setSelectedHumanRequest] = useState();
    return createElement(FactoryConsole, {
      status: "ready", graphs: fixtureGraphs,
      runPaths: new Map([[ids.agent, runSample(ids.agent, ["internal/kernel/state.go"])]]),
      state,
      detail,
      onDetail: setDetail,
      selectedTaskId,
      onSelectTask: (taskId) => { queueSelections.push(taskId); setSelectedTaskId(taskId); },
      selectedHumanRequest,
      onSelectHumanRequest: (request) => { questions.push(request); setSelectedHumanRequest(selectedRequest({ request, question: "Should the migration also cover the users table? The plan only names accounts." })); setDetail("work"); },
    });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  const queuedTask = fixtureState.tasks.get("32".repeat(16));
  const queuedRow = () => tree.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem dfWorkRow" }).find((row) => rowTitle(row) === queuedTask.title);
  assert.equal(rowButton(queuedRow()).props["aria-pressed"], false);
  await act(async () => { rowButton(queuedRow()).props.onClick(); });
  assert.deepEqual(queueSelections, [queuedTask.id]);
  assert.equal(rowButton(queuedRow()).props["aria-pressed"], true);
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "selecting work opens no dialog");
  await act(async () => { tree.root.findByProps({ "data-human-request-id": ids.request }).props.onClick(); });
  assert.equal(questions[0], fixtureState.humanRequests.get(ids.request));
  assert.equal(tree.root.findByProps({ "aria-label": "Selected question" }).findByProps({ className: "dfFactoryConsole__question" }).children.join(""), "Should the migration also cover the users table? The plan only names accounts.");
  await act(async () => tree.unmount());
});

test("Work and agent panels carry no history disclosure", () => {
  const agentMarkup = renderToStaticMarkup(createElement(AgentPanel, { agent: fixtureState.agents.get(ids.agent), state: fixtureState, ready: true }));
  const workMarkup = renderToStaticMarkup(createElement(WorkPanel, { state: fixtureState, rows: workRows(fixtureState, []), ready: true }));
  assert.doesNotMatch(agentMarkup, /Recent work|aria-label="History"/);
  assert.doesNotMatch(workMarkup, /Recent work|aria-label="History"/);
});

test("floor omits the global evidence essay", () => {
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, view: "floor" }));
  assert.doesNotMatch(markup, /Floor evidence|Rooms describe a repository snapshot/);
  assert.doesNotMatch(markup, /Scroll the floor to explore/);
  assert.doesNotMatch(markup, /Floor detail|Topology detail|Social furniture|one connected floor/);
  assert.doesNotMatch(markup, /Find machine/);
});





test("mobile navigation switches presentation without mutating work", () => {
  const calls = [];
  const elements = consoleElements({ status: "ready", state: fixtureState, detail: "floor", onDetail: (value) => calls.push(value) });
  const nav = elements.find((element) => element.props["aria-label"] === "Console views");
  const buttons = nav.props.children.filter((element) => element.type === "button" || element.type.name === "IconButton");
  assert.equal(buttons[0].props["aria-pressed"], true);
  assert.deepEqual(buttons.map(textOf), ["Floor", "Work"]);
  buttons.find((button) => textOf(button) === "Work").props.onClick();
  assert.deepEqual(calls, ["work"]);
  assert.equal(elements.find((element) => element.type === "main").props["data-mobile-view"], "floor");
});


test("a floor sprite is keyboard-selectable and opens its Agent panel while the floor stays mounted", async () => {
  let tree;
  function Harness() {
    const [detail, setDetail] = useState("floor");
    const [selectedAgent, setAgent] = useState();
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, detail, onDetail: setDetail, selectedAgent,
      onSelectAgent: (agent) => { setAgent(agent); setDetail("agent"); } });
  }
  await act(async () => { tree = create(createElement(Harness)); });
  const floor = tree.root.findByProps({ className: "dfFactoryFloor__map" });
  const agent = fixtureState.agents.get(ids.agent);
  const sprite = workerSprite(tree, agent.id);
  assert.equal(sprite.props.tabIndex, 0);
  let prevented = false;
  await act(async () => sprite.props.onKeyDown({ key: "Enter", preventDefault() { prevented = true; } }));
  assert.equal(prevented, true);
  assert.equal(tree.root.findByType(FactoryConsole).props.selectedAgent.id, agent.id);
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "detail");
  assert.equal(tree.root.findAllByProps({ "aria-label": `Agent ${agent.name}` }).length, 1);
  await act(async () => tree.root.findByProps({ "aria-label": "Console views" }).findAllByType("button")[0].props.onClick());
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "floor");
  assert.equal(tree.root.findByProps({ className: "dfFactoryFloor__map" }), floor);
  await act(async () => tree.unmount());
});

test("floor preparation survives draft and live snapshot updates and relayouts only when structure changes", async () => {
  let reads = 0;
  const graph = { ...fixtureGraph, get nodes() { reads += 1; return fixtureGraph.nodes; } };
  let graphs = new Map(fixtureGraphs).set(ids.project, graph);
  function DraftFloor({ state, connected = true }) {
    const [draft, setDraft] = useState("");
    return createElement("div", null,
      createElement("input", { value: draft, onChange: (event) => setDraft(event.target.value) }),
      createElement(FactoryFloor, { state, graphs, connected }));
  }
  let renderer;
  await act(async () => { renderer = create(createElement(DraftFloor, { state: fixtureState })); });
  const scene = () => renderer.root.findByType(FactoryScene);
  const layout = () => renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout;
  const preparedReads = reads;
  const originalGraph = scene().props.graph;
  const originalLayout = layout();
  assert.ok(preparedReads > 0);
  await act(async () => { renderer.root.findAllByType("input").find((input) => input.props.type !== "search").props.onChange({ target: { value: "draft typing" } }); });
  assert.equal(reads, preparedReads);
  assert.equal(scene().props.graph, originalGraph);
  assert.equal(layout(), originalLayout);
  const fresh = { ...fixtureState, head: fixtureState.head + 1n, projects: new Map([...fixtureState.projects].map(([id, project]) => [id, { ...project }])), tasks: new Map(fixtureState.tasks), humanRequests: new Map() };
  fresh.tasks.set(ids.task, { ...fresh.tasks.get(ids.task), title: "Updated live task" });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh })); });
  assert.equal(reads, preparedReads, "fresh snapshot project objects do not rebuild the graph projection");
  assert.equal(scene().props.tasks.find((task) => task.id === ids.task).title, "Updated live task");
  assert.equal(layout(), originalLayout);
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh, connected: false })); });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh, connected: true })); });
  assert.equal(layout(), originalLayout);
  // A new answer with moved readings and the same digest replaces the graph but never relayouts.
  const hot = { ...fixtureGraph, nodes: fixtureGraph.nodes.map((node) => node.kind === "ingress" && node.observation === "observed" ? { ...node, rate_per_hour: 999 } : node) };
  graphs = new Map(graphs).set(ids.project, hot);
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh, connected: false })); });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh })); });
  assert.notEqual(scene().props.graph, originalGraph);
  assert.equal(scene().props.graph.units.flatMap((item) => item.machines).find((machine) => machine.label === "/browser").reading.ratePerHour, 999);
  assert.equal(layout(), originalLayout, "readings never move a machine");
  // A new digest is a new structure.
  graphs = new Map(graphs).set(ids.project, { ...fixtureGraph, digest: "cd".repeat(32), nodes: fixtureGraph.nodes.filter((node) => node.label !== "web"), edges: [] });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh, connected: false })); });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh })); });
  assert.deepEqual(scene().props.graph.units.map((item) => item.label), ["kernel"]);
  assert.notEqual(layout(), originalLayout);
  await act(async () => { renderer.unmount(); });
});

test("task meter displays every canonical status without conflating cancellation", () => {
  for (const [stage, filled, marker] of [["queued", 1, ""], ["running", 2, ""], ["succeeded", 2, "✓"], ["failed", 0, "×"], ["blocked", 0, "!"], ["cancelled", 0, "−"]]) {
    const markup = renderToStaticMarkup(createElement(StageMeter, { stage }));
    assert.ok(markup.includes(`stage: ${stage}`));
    assert.equal((markup.match(/segment--filled/g) ?? []).length, filled);
    assert.ok(markup.includes(`aria-hidden="true">${marker}</span>`));
    assert.equal(markup.includes("terminal--failed"), stage === "failed");
  }
});






test("open Work re-reads the local intake list so issues polled later reach the inbox", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const loads = [];
  const actions = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: true, revision: 3n };
  const props = (sources) => ({ status: "ready", state: oneProjectState(), intake: new Map([[ids.project, { state: "ok", sources }]]), onLoadIntake: (id) => loads.push(id), onIntakeAction: (...args) => actions.push(args) });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props([source]))); });
    assert.deepEqual(loads, [ids.project]);
    assert.equal(renderer.root.findAll((node) => node.props["aria-label"] === "Accept Fix parser").length, 0);
    await act(async () => t.mock.timers.tick(30_000));
    assert.deepEqual(loads, [ids.project, ids.project], "Work refreshes the local list without a project change");
    const waiting = { number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Fix parser", body: "", author: "reporter", labels: [], content_hash: "ab".repeat(32), reason: "needs_manual_acceptance" };
    await act(async () => renderer.update(createElement(FactoryConsole, props([{ ...source, sync: { last_attempt_at: 2n, last_success_at: 2n, imported_tasks: 0, state: "ok", error: "", waiting: [waiting] } }]))));
    assert.equal(renderer.root.findAll((node) => node.props["aria-label"] === "Accept Fix parser").length, 1);
    await act(async () => renderer.root.findByProps({ "aria-label": "Accept Fix parser" }).props.onClick());
    assert.deepEqual(actions, [[ids.project, { action: "accept", source_id: source.id, expected_revision: source.revision, issue_number: waiting.number, content_hash: waiting.content_hash }]]);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("floor project changes clear task selection", async () => {
  const task = [...fixtureState.tasks.values()].find((task) => task.project_id === ids.secondProject);
  function Harness() {
    const [selectedTaskId, onSelectTask] = useState(task.id);
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, selectedTaskId, onSelectTask });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  assert.equal(tree.root.findByType(FactoryConsole).props.selectedTaskId, task.id);
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.project } }));
  assert.equal(tree.root.findByType(FactoryConsole).props.selectedTaskId, undefined);
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0);
  await act(async () => tree.unmount());
});

test("the floor reads a machine from its tooltip and offers no inspector or Investigate control", async () => {
  const store = fixtureGraph.nodes.find((node) => node.label === "state.db");
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryFloor, { state: fixtureState, graphs: fixtureGraphs })); });
  const station = renderer.root.findByProps({ "aria-label": store.label });
  assert.equal(station.props.tabIndex, 0, "the station is reachable by keyboard");
  assert.match(station.props["data-tooltip"], /^state\.db\nstore in kernel\nNo telemetry: activity unknown/, "the tooltip is the machine's truthful reading");
  assert.equal(station.props.onClick, undefined, "a station opens nothing");
  assert.equal(renderer.root.findAllByProps({ "aria-label": "Machine inspector" }).length, 0);
  assert.equal(renderer.root.findAllByType("button").filter((button) => /Investigate|Close/.test(button.children.join(""))).length, 0);
  await act(async () => renderer.unmount());
});

test("new task retains pasted files on failure, supports removal, and clears on success", async () => {
  const state = baseState();
  const attempts = [];
  let succeed = false, renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", detail: "work", state, onDetail() {}, onAddTask: async (...args) => { attempts.push(args); return succeed; } })); });
  const form = () => renderer.root.findByProps({ "aria-label": "New task" });
  const file = new File(["attachment"], "notes.txt", { type: "text/plain" });
  let prevented = false;
  await act(async () => { form().props.onPaste({ clipboardData: { files: [file] }, preventDefault() { prevented = true; } }); });
  assert.equal(prevented, true);
  const nativeFormData = globalThis.FormData;
  globalThis.FormData = class { get(name) { return name === "target" ? `any:${ids.project}` : "Inspect this"; } };
  let resets = 0;
  const submit = () => form().props.onSubmit({ preventDefault() {}, currentTarget: { reset() { resets++; } } });
  try {
    await act(async () => { submit(); });
    assert.deepEqual(attempts[0][3], [file]);
    assert.equal(resets, 0);
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Remove notes.txt" }).length, 1);
    await act(async () => { renderer.root.findByProps({ "aria-label": "Remove notes.txt" }).props.onClick(); });
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Remove notes.txt" }).length, 0);
    await act(async () => { form().props.onDrop({ dataTransfer: { files: [file] }, preventDefault() {} }); });
    succeed = true;
    await act(async () => { submit(); });
    assert.equal(resets, 1);
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Task attachments" }).length, 0);
  } finally { globalThis.FormData = nativeFormData; await act(async () => renderer.unmount()); }
});






test("one project is selected automatically, with no header picker", () => {
  const one = baseState({ projects: new Map([...fixtureState.projects].slice(0, 1)) });
  const single = render({ state: one, detail: "work" });
  assert.equal(single.includes('aria-label="Project"'), false);
  assert.equal(single.includes("Mission project"), false);
  const many = render({ detail: "work" });
  assert.match(many, /aria-label="Project"/);
});

test("Needs you and Changes counts hide at zero, and Help is gone", () => {
  const markup = render({ state: baseState({ humanRequests: new Map() }), onDetail() {} });
  assert.equal(/Needs you (<!-- -->)?\d/.test(markup), false);
  assert.equal(/Changes (<!-- -->)?\d/.test(markup), false);
  assert.equal(markup.includes("Help"), false);
  assert.equal(markup.includes(" items"), false);
});

test("Work lists and counts only the chosen project's requests", async () => {
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state: fixtureState, onDetail() {} })); });
  const request = [...fixtureState.humanRequests.values()][0];
  const other = request.project_id === ids.project ? ids.secondProject : ids.project;
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: other } }));
  const text = JSON.stringify(tree.toJSON());
  assert.equal(text.includes(" asks"), false);
  const work = tree.root.findByProps({ "aria-label": "Console views" }).findAllByType("button").find((button) => textOf(button).startsWith("Work"));
  assert.equal(work.findAllByProps({ className: "dfBadge" }).length, 0);
  assert.doesNotMatch(text, /Review the state projection/);
  await act(async () => tree.unmount());
});


test("a specialist states what it is doing and owes, with no browser lifecycle controls", () => {
  const worker = fixtureState.agents.get(ids.agent);
  const specialist = { ...worker, id: "5a".repeat(16), name: "Doc Watcher", archived: false, idle_policy: "standing_instruction", idle_after_seconds: 600, idle_instruction: "Keep the docs true to the code.\nSecond line stays out.", idle_run_budget: 4, idle_runs_used: 1, idle_wake_on: "failures",
    specialist: { next_review_at_ms: 1750000000000, next_reason: "events", waiting: "capacity", quiet_reviews: 0, open_proposals: 1, open_proposal_limit: 3, last_review_task_id: "" } };
  const state = baseState({ agents: new Map([...fixtureState.agents, [specialist.id, specialist]]), humanRequests: new Map() });
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "agent", state, selectedAgent: { id: specialist.id, name: specialist.name, revision: specialist.revision } }));
  assert.match(markup, /waiting: waiting for a free background slot/);
  assert.match(markup, /aria-label="Remit">Keep the docs true to the code\.</);
  assert.match(markup, /Next review .*\(events\) or sooner on failures/);
  assert.doesNotMatch(markup, /aria-label="Contributions"/);
  assert.match(markup, /aria-label="Terminal"/);
  for (const absent of [">Pause<", ">Resume<", "Stop specialist", "Archive worker", 'aria-label="Agent configuration"']) assert.equal(markup.includes(absent), false, absent);
  const running = baseState({ agents: state.agents, humanRequests: new Map(), tasks: new Map([...fixtureState.tasks, ["c1".repeat(16), { ...fixtureState.tasks.get(ids.task), id: "c1".repeat(16), title: "Standing instruction", status: "running", assigned_agent_id: specialist.id }]]) });
  const live = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "agent", state: running, selectedAgent: { id: specialist.id, name: specialist.name, revision: specialist.revision } }));
  assert.match(live, /reviewing: Keep the docs true to the code\./, "a running review is its remit at work");
});
