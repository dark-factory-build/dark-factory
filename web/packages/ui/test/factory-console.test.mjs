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
import { ProjectLibrary } from "../dist/src/project-library.js";
import { AgentPanel, TaskDetail, WorkPanel } from "../dist/src/console-sidebar.js";
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
  onSaveAgentConfig: () => {},
});

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
  for (const label of ["Factory floor", "Selected detail", "Work", "Right panel"]) {
    assert.match(markup, new RegExp(`aria-label="${label}"`));
  }
  assert.doesNotMatch(markup, /ACTIVE RUNS|OPERATOR VIEW/);
  assert.equal(markup.includes("<dt>QUEUED</dt>"), false);
  assert.equal(markup.includes("<dt>NEEDS YOU</dt>"), false);
  assert.match(markup, /Needs you <span class="dfBadge">1<\/span>/);
  assert.match(markup, /Work <span class="dfBadge">1<\/span>/);
  assert.match(markup, /Builder One asks/);
  assert.match(markup, /Review the state projection/);
  assert.match(markup, /Review the state projection[\s\S]*North Workshop · Builder One asks/);
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

test("the sidebar roster groups agents while the floor stays visible", () => {
  const floor = render();
  assert.match(floor, /aria-label="Dark Factory operational floor"/);

  const agents = render({ detail: "agent" }).split('class="dfAgentList"')[1];
  assert.match(agents, /aria-label="Overseer"/);
  // Rank is the served role, oversight first, and nothing invents a new field.
  const overseer = agents.indexOf('aria-label="Overseer"');
  const worker = agents.indexOf('aria-label="Worker"');
  assert.ok(overseer > -1 && worker > overseer);
  assert.ok(agents.indexOf("Dispatch Lead") < agents.indexOf("Builder One"));
  assert.match(agents, /Builder One[\s\S]*?claude_code[\s\S]*?needs you/);
  assert.match(agents, /Builder Two[\s\S]*?1 queued/);
  assert.equal(agents.includes("rank"), false);
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
    ["agent", render({ detail: "agent", selectedAgent: agentSelection(), onSaveAgentConfig: () => {} })],
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
  assert.match(render({ detail: "agent" }), />Overseer</);
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
  assert.match(render({ state: emptyState, detail: "agent" }), /no agents/);

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
  assert.equal((render({ state: bounded, detail: "agent" }).match(/dfAgentList__row/g) ?? []).length, 0, "no handler, no button");
  assert.equal((render({ state: bounded, detail: "agent", onSelectAgent: () => {} }).match(/dfAgentList__row/g) ?? []).length, 9);
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

test("the Needs-you filter shows an open request plus a blocked task, badge 2", async () => {
  const blocked = { ...fixtureState.tasks.get(ids.task), id: "b3".repeat(16), title: "Stuck", status: "blocked", blocked_reason: "why" };
  const state = baseState({ tasks: new Map([...fixtureState.tasks, [blocked.id, blocked]]) });
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state, detail: "work", onDetail() {} })); });
  const chip = tree.root.findAllByType("button").find((button) => textOf(button) === "Needs you");
  assert.ok(chip.props.children.some((child) => child?.props?.n === 2));
  await act(async () => chip.props.onClick());
  const titles = tree.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem__taskTitle dfWorkRow__title" }).map((node) => node.children.join(""));
  assert.ok(titles.includes("Review the state projection"));
  assert.ok(tree.root.findByProps({ "aria-label": "Work" }).findAllByType("button").some((button) => button.children.join("") === "Stuck"));
  assert.equal(titles.includes("Tighten the queue ordering"), false, "queued work is filtered out");
  await act(async () => tree.unmount());
});

test("an overseer pass is hidden until asked for, but its request row is not", () => {
  const overseerTask = { ...fixtureState.tasks.get(ids.task), assigned_agent_id: ids.orchestrator };
  const request = fixtureState.humanRequests.get(ids.request);
  const hide = render({ detail: "work", state: baseState({ tasks: new Map([[overseerTask.id, overseerTask]]), humanRequests: new Map() }) });
  assert.doesNotMatch(hide, /Review the state projection/);
  assert.match(render({ detail: "work", state: baseState({ tasks: new Map([[overseerTask.id, overseerTask]]), humanRequests: new Map([[request.id, request]]) }) }), /Review the state projection/);
});

test("a succeeded task linked to an open PR is in review with its PR cell", () => {
  const done = { ...fixtureState.tasks.get(ids.task), status: "succeeded" };
  const state = baseState({ tasks: new Map([[done.id, done]]), humanRequests: new Map() });
  const pr = { visualId: "change:1", projectId: done.project_id, repository: "o/r", tasks: [done.id], missions: [], pullRequest: { number: 7, state: "open", title: "Ship", head: "a".repeat(40) }, review: { state: "pending" }, checks: [], deliveries: [], reviewers: [], completed: false, completedAt: 0, status: "open", nextAction: "" };
  const markup = renderToStaticMarkup(createElement(WorkPanel, { state, rows: workRows(state, [pr]), filter: "all", onFilter() {}, byMission: false, onByMission() {}, missions: null, ready: true, onSelectProduction() {}, onMission() {} }));
  assert.match(markup, /data-stage="in-review">in review/);
  assert.match(markup, /PR #7/);
  assert.match(markup, /Review the state projection/);
});

test("a selected PR never traps the Work tab: Open mission and a project switch both return", async () => {
  const done = { ...fixtureState.tasks.get(ids.task), status: "succeeded" };
  const state = baseState({ tasks: new Map([[done.id, done]]), humanRequests: new Map() });
  const head = "a".repeat(40);
  const records = [
    { repository: "o/r", kind: "repository", id: "o/r", visual_id: "", observed_at: Date.now(), document: {}, tasks: [], missions: [] },
    { repository: "o/r", kind: "pull_request", id: "7", visual_id: "change:7", observed_at: Date.now(), document: { number: 7, title: "Ship", head, state: "open", review: { head, state: "allow" } }, tasks: [done.id], missions: ["m1"] },
  ];
  const priorDocument = globalThis.document, priorAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.document = { visibilityState: "visible", hidden: false, addEventListener() {}, removeEventListener() {} };
  let tree;
  try {
    const call = async (op, input) => op === "production" ? { records: input.project_id === done.project_id ? records : [], next_offset: 0, total: 2 } : { items: [], next_offset: 0 };
    function Harness() { const [detail, setDetail] = useState("work"); return createElement(FactoryConsole, { status: "ready", state, detail, onDetail: setDetail, onProjectContent: call }); }
    await act(async () => { tree = create(createElement(Harness)); });
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
    const shown = () => tree.root.findAllByProps({ "aria-label": "Work" }).length === 1 && tree.root.findByProps({ "aria-label": "Work" }).parent.parent.props.hidden !== true;
    const prButton = () => tree.root.findAllByType("button").find((button) => textOf(button).startsWith("PR #"));
    await act(async () => prButton().props.onClick());
    assert.equal(shown(), false, "the PR detail replaces the list");
    await act(async () => tree.root.findAllByType("button").find((button) => textOf(button).startsWith("Open mission")).props.onClick());
    assert.equal(shown(), true, "Open mission leaves the PR detail");
    assert.equal(tree.root.findByProps({ "aria-label": "Work filter" }).findAllByType("button").find((button) => textOf(button) === "By mission").props["aria-pressed"], true);
    await act(async () => tree.root.findByProps({ "aria-label": "Work filter" }).findAllByType("button")[0].props.onClick());
    await act(async () => prButton().props.onClick());
    assert.equal(shown(), false);
    await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.secondProject } }));
    assert.equal(shown(), true, "a stale selection falls back to the list");
  } finally { if (tree) await act(async () => tree.unmount()); globalThis.document = priorDocument; globalThis.IS_REACT_ACT_ENVIRONMENT = priorAct; }
});

test("a floor question opens its answer form even from By mission or a PR detail", async () => {
  const request = fixtureState.humanRequests.get(ids.request);
  let tree;
  function Harness() {
    const [chosen, setChosen] = useState();
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, detail: "work", onDetail() {}, selectedHumanRequest: chosen, onSelectHumanRequest: (item) => setChosen(selectedRequest({ request: item })), onCloseHumanRequest() {}, onReplyHumanRequest() {} });
  }
  await act(async () => { tree = create(createElement(Harness)); });
  const filter = () => tree.root.findByProps({ "aria-label": "Work filter" }).findAllByType("button");
  await act(async () => filter().find((button) => textOf(button) === "By mission").props.onClick());
  await act(async () => tree.root.findByType(FactoryFloor).props.onSelectHumanRequest(request));
  assert.equal(filter().find((button) => textOf(button) === "By mission").props["aria-pressed"], false);
  assert.equal(tree.root.findAllByProps({ "aria-label": "Selected question" }).length > 0, true);
  await act(async () => tree.unmount());
});

test("the MISSIONS desk opens Work in By-mission view", async () => {
  let tree;
  function Harness() { const [detail, setDetail] = useState("floor"); return createElement(FactoryConsole, { status: "ready", state: fixtureState, detail, onDetail: setDetail }); }
  await act(async () => { tree = create(createElement(Harness)); });
  await act(async () => tree.root.findByType(FactoryFloor).props.onOpenMissions(ids.project));
  const toggle = tree.root.findByProps({ "aria-label": "Work filter" }).findAllByType("button").find((button) => button.children.join("") === "By mission");
  assert.equal(toggle.props["aria-pressed"], true);
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "detail");
  await act(async () => tree.unmount());
});

test("Work lists blocked work, hides overseer passes until asked, flags dispatch off and queues new work", async () => {
  const running = fixtureState.tasks.get(ids.task);
  const supervisor = fixtureState.agents.get(ids.orchestrator);
  const state = baseState({
    factory: { ...fixtureState.factory, dispatch_enabled: false },
    tasks: new Map([
      ...fixtureState.tasks,
      ["b1".repeat(16), { ...running, id: "b1".repeat(16), title: "Stuck on a prerequisite", status: "blocked", blocked_reason: "Needs a human to resolve a merge conflict <script>" }],
      ["b2".repeat(16), { ...running, id: "b2".repeat(16), title: "Standing instruction", status: "running", assigned_agent_id: supervisor.id }],
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
  assert.ok(!titled("Standing instruction"), "an overseer pass is hidden by default");
  await act(async () => { panel.findByProps({ "aria-label": "Show overseer and specialist passes" }).props.onClick(); });
  assert.ok(titled("Standing instruction"), "and shown once asked for");
  assert.ok(panel.findAllByProps({ role: "status" }).some((node) => node.children.join("").includes("New work is paused")));
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
  const markup = render({ state, detail: "agent" });
  assert.match(markup, /aria-label="Builder One: ready · North Workshop"/);
  assert.equal(markup.includes('aria-label="Builder One: busy"'), false);
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
  assert.match(render({ state: undefined, status: "syncing", detail: "agent" }), /waiting for the factory/);
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
  for (const rule of [/\.dfConsoleDialog \*/, /\.dfConsoleDialog button,/, /\.dfConsoleDialog button:disabled,[\s\S]*?\{/, /\.dfConsoleShell\s*\{[^}]*font-family: var\(--df-font-mono\)/, /\.dfConsoleDialog\s*\{[^}]*color: var\(--df-console-text\)/]) {
    assert.match(css, rule);
  }
  // The panel scrolls, never the <dialog>: a scrollbar click on the dialog
  // itself has event.target === the dialog and would close SETTINGS.
  assert.match(css, /\.dfConsoleDialog \.dfConsoleSidebar__panel \{[^}]*max-height:[^}]*overflow: auto;/);
  assert.equal(/\.dfConsoleDialog\s*\{[^}]*overflow: auto/.test(css), false);
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
    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "Settings" && typeof button.props.onClick === "function").props.onClick(); });
    await act(async () => { renderer.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.secondProject } }); });
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Terminal" }).length, 0);
    await act(async () => { renderer.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: "" } }); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => textOf(button).startsWith("Needs you")).props.onClick(); });
    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "Open terminal").props.onClick(); });
    assert.equal(renderer.root.findByProps({ "aria-label": "Project" }).props.value, ids.project, "opening a global question switches to its agent’s project");
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
    detail: "agent",
    selectedAgent: agentSelection(),
    onSelectAgent: () => {},
    onSaveAgentConfig: () => {},
    onEditTask: () => {},
  });
  assert.match(markup, /aria-label="Agent Builder One"/);
  assert.match(markup, /Builder One[\s\S]*?claude_code · claude-opus-5/);
  assert.match(markup, /aria-label="Agent controls"/);
  assert.match(markup, />Terminal</);
  assert.match(markup, />Settings</);
  assert.match(markup, /aria-label="Agent configuration"/);
  assert.match(markup, /value="claude-opus-5"/);
  assert.match(markup, /value="high"/);
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

test("a queued row uses the shared Work row and opens TaskDetail", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const detailReads = [];
    const selections = [];
    const queued = { ...fixtureState.tasks.get([...fixtureState.tasks.keys()][1]), title: "Queued task", assigned_agent_id: ids.agent };
    const props = {
      status: "ready",
      state: baseState({ tasks: new Map([[queued.id, queued]]) }),
      detail: "work",
      selectedAgent: agentSelection(),
      onSelectTask: (taskId) => selections.push(taskId),
      onLoadTaskDetail: async (task, peerOffset, expectedHead) => {
        detailReads.push(task.id);
        return { taskId: task.id, revision: task.revision, head: expectedHead ?? 7n, instruction: "Original brief", feedback: "", peerQuestions: [] };
      },
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
    assert.deepEqual(detailReads, []);
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

test("a rejected edit says plainly that the durable value did not change", () => {
  const markup = render({
    selectedAgent: agentSelection(),
    onSaveAgentConfig: () => {},
    edit: { target: ids.agent, pending: false, error: new SessionError("stale") },
  });
  assert.match(markup, /Someone else changed this\. Reopen it and try again\./);
  assert.match(markup, /role="alert"/);
  assert.equal((markup.match(/role="alert"/g) ?? []).length, 1, "a config refusal has one shared alert");
  const unknown = render({ selectedAgent: agentSelection(), onSaveAgentConfig: () => {}, edit: { target: ids.agent, pending: false, error: { code: "internal" } } });
  assert.match(unknown, /The edit did not complete\./);
  assert.match(render({ selectedAgent: agentSelection(), onSaveAgentConfig: () => {}, edit: { pending: true } }), />Saving</);
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


test("the floor stays on the left and the agent roster lives in the sidebar", () => {
  const floor = render({ detail: "agent" });
  assert.doesNotMatch(floor, /aria-label="Left view"/);
  assert.ok(floor.indexOf('aria-label="Agents"') > floor.indexOf('aria-label="Selected detail"'));
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
  // Oversight is listed first, so the first row is the orchestrator's.
  const agent = fixtureState.agents.get(ids.orchestrator);
  const calls = [];
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, {
    status: "ready", state: baseState(), detail: "agent", selectedHumanRequest: selectedRequest(),
    onSelectAgent: (value) => calls.push(["agent", value]),
    onOpenTerminalForHumanRequest: (value) => calls.push(["request", value]),
  })); });
  const row = renderer.root.findAllByType("button").find((element) => typeof element.props.className === "string" && element.props.className.includes("dfAgentList__row"));
  await act(async () => { row.props.onClick(); });
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
  // An agent that names no model still runs with one. The row and the panel
  // header say which, so a blank is never read as "no model".
  const rows = render({ detail: "agent" });
  assert.match(rows, /codex · gpt-6-astra/);
  assert.match(rows, /claude_code · claude-opus-5/);
  assert.match(withAgent(inheritingAgent()), /Builder Two[\s\S]*?codex · gpt-6-astra/);
  // The provider with no model says nothing extra rather than a dangling dot.
  assert.match(withAgent(shellAgent), /Shell Hand[\s\S]*?shell/);
});

test("archived workers stay selectable from the archived view and expose only restore", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const worker = { ...fixtureState.agents.get(ids.agent), archived: true, paused: true };
  const state = baseState({ agents: new Map([[worker.id, worker]]) });
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state, detail: "agent", onSelectAgent() {} })); });
    assert.equal(renderer.root.findAllByType("button").some((button) => String(button.props.className).includes("dfAgentList__row")), false);
    const archived = renderer.root.findByProps({ type: "checkbox" });
    await act(async () => { archived.props.onChange({ currentTarget: { checked: true } }); });
    assert.equal(renderer.root.findAllByType("button").some((button) => String(button.props.className).includes("dfAgentList__row")), true);
    await act(async () => { renderer.update(createElement(FactoryConsole, { status: "ready", state, detail: "agent", selectedAgent: { id: worker.id, name: worker.name, revision: worker.revision }, onSaveAgentConfig() {} })); });
    const text = JSON.stringify(renderer.toJSON());
    assert.match(text, /archived/);
    assert.match(text, /Restore paused/);
    assert.equal(text.includes("Archive worker"), false);
    assert.equal(text.includes("TERMINAL"), false);
    assert.equal(text.includes("Queue paused"), false);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("archive confirmation follows connection readiness and submits only the lifecycle change", async () => {
  const worker = { ...fixtureState.agents.get(ids.agent), archived: false };
  const state = baseState({ agents: new Map([[worker.id, worker]]) });
  const saves = [];
  const props = { state, selectedAgent: { id: worker.id, name: worker.name, revision: worker.revision }, agentPanel: "config", onSaveAgentConfig: (edit) => saves.push(edit) };
  let renderer;
  const button = (label) => renderer.root.findAllByType("button").find((entry) => entry.props.children === label);
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { ...props, status: "closed" })); });
    assert.equal(button("Archive worker").props.disabled, true);
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, status: "ready" })); });
    assert.equal(button("Archive worker").props.disabled, false);
    await act(async () => { button("Archive worker").props.onClick(); });
    assert.deepEqual(saves, []);
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, status: "closed" })); });
    assert.equal(button("Confirm archive").props.disabled, true);
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, status: "ready" })); });
    assert.equal(button("Confirm archive").props.disabled, false);
    await act(async () => { button("Confirm archive").props.onClick(); });
    assert.deepEqual(saves, [{ archived: true }]);
  } finally {
    if (renderer) await act(async () => { renderer.unmount(); });
  }
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

test("agent config drafts survive an acknowledged live revision", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const original = fixtureState.agents.get(ids.agent);
  const state = baseState({ agents: new Map([[original.id, original]]) });
  const selectedAgent = { id: original.id, name: original.name, revision: original.revision };
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state, selectedAgent, agentPanel: "config", onSaveAgentConfig() {} })); });
    const model = () => renderer.root.findByProps({ id: `df-model-${original.id}` });
    await act(async () => model().props.onChange({ currentTarget: { value: "operator draft" } }));
    const changed = { ...original, revision: original.revision + 1n, paused: !original.paused };
    await act(async () => renderer.update(createElement(FactoryConsole, { status: "ready", state: baseState({ agents: new Map([[changed.id, changed]]) }), selectedAgent: { ...selectedAgent, revision: changed.revision }, agentPanel: "config", onSaveAgentConfig() {} })));
    assert.equal(model().props.value, "operator draft");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("the shell provider has no model inputs but keeps PAUSED", () => {
  const markup = withAgent(shellAgent);
  assert.match(markup, />shell has no model</);
  assert.equal(markup.includes("df-model-"), false);
  assert.equal(markup.includes("df-effort-"), false);
  // The daemon rejects a model for shell; pausing it is still an edit.
  assert.match(markup, /id="df-paused-[0-9a-f]+"/);
  assert.match(markup, />Paused</);
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
  await act(async () => { form().props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(edits.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 2, idleInstruction: "Look for follow-up work." });
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
  assert.deepEqual(edits.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 1, idleInstruction: "Look for follow-up work." });
  // Editing a standing rule preserves its recorded wake history.
  const spent = { ...fixtureState.agents.get(ids.agent), idle_policy: "standing_instruction", idle_after_seconds: 600, idle_instruction: "Look for follow-up work.", idle_run_budget: 3, idle_runs_used: 3 };
  const spentState = baseState({ agents: new Map([...fixtureState.agents, [spent.id, spent]]) });
  const again = [];
  let spentRenderer;
  await act(async () => { spentRenderer = create(createElement(FactoryConsole, { status: "ready", state: spentState, selectedAgent: agentSelection(), onSaveAgentConfig: (config) => again.push(config) })); });
  const spentField = (id) => spentRenderer.root.findAll((node) => node.props.id === id)[0];
  assert.match(renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", state: spentState, selectedAgent: agentSelection(), onSaveAgentConfig: () => {} })), /3 idle runs/);
  await act(async () => { spentField(`df-idle-instruction-${ids.agent}`).props.onChange({ currentTarget: { value: "Look for follow-up work, then tidy." } }); });
  await act(async () => { spentRenderer.root.findAllByType("form")[0].props.onSubmit({ preventDefault() {} }); });
  assert.deepEqual(again.at(-1), { idlePolicy: "standing_instruction", idleAfterSeconds: 600, idleInstruction: "Look for follow-up work, then tidy." });
});

test("overseer supervision names worker events and a seconds cooldown", () => {
  const agent = { ...fixtureState.agents.get(ids.agent), role: "orchestrator", idle_policy: "standing_instruction", idle_after_seconds: 10, idle_instruction: "Inspect worker activity.", idle_run_budget: 1 };
  const agents = new Map(fixtureState.agents);
  agents.set(agent.id, agent);
  const markup = render({ state: baseState({ agents }), selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision }, onSaveAgentConfig: () => {} });
  for (const text of ["Supervision", "When work changes", "supervise worker activity", "Cooldown seconds", "initial inspection, then worker events"]) assert.match(markup, new RegExp(text));
  assert.match(markup, new RegExp(`id="df-idle-after-${agent.id}"[^>]*value="10"`));
});

test("task selection and floor questions open the exact existing task detail and question route", async () => {
  const loaded = [];
  const questions = [];
  const queueSelections = [];
  const props = {
    status: "ready", state: fixtureState, graphs: fixtureGraphs,
    runPaths: new Map([[ids.agent, runSample(ids.agent, ["internal/kernel/state.go"])]]),
    onLoadTaskDetail: async (task) => {
      loaded.push(task);
      return { taskId: task.id, revision: task.revision, head: fixtureState.head, instruction: "Observed task detail", feedback: "", peerQuestions: [] };
    },
  };
  function Harness({ state = fixtureState, editable = false }) {
    const [selectedTaskId, setSelectedTaskId] = useState();
    const [detail, setDetail] = useState("work");
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
      onSelectHumanRequest: (request) => { questions.push(request); setSelectedHumanRequest(selectedRequest({ request, question: "Should the migration also cover the users table? The plan only names accounts." })); setDetail("work"); },
    });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  const task = fixtureState.tasks.get(ids.task);
  // A running task with an open question is listed as that question; selecting the task is the console's own route.
  const openRunning = () => act(async () => { tree.root.findByType(FactoryConsole).props.onSelectTask(task.id); });
  await openRunning();
  assert.equal(loaded.length, 1);
  assert.equal(loaded[0], task);
  assert.deepEqual(queueSelections, [task.id]);
  assert.equal(tree.root.findByProps({ "aria-label": "Task details" }).findByType("h3").children.join(""), task.title);
  await act(async () => { tree.root.findByProps({ "aria-label": "Task details" }).props.onClose(); });
  const queuedTitle = tree.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem__taskTitle dfWorkRow__title" }).find((node) => node.children.join("") === "Tighten the queue ordering");
  await act(async () => { queuedTitle.props.onClick(); });
  assert.deepEqual(queueSelections, [task.id, undefined, "32".repeat(16)]);
  assert.equal(tree.root.findByProps({ "aria-label": "Task details" }).findByType("h3").children.join(""), fixtureState.tasks.get("32".repeat(16)).title);
  await act(async () => { tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: "" } }); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Work" }).length, 1, "queue remains one canonical panel");
  assert.equal(tree.root.findAllByProps({ "data-floor-inbox": 1 }).length, 1, "the floor shows the pile; the panel is where it is read");
  const queuedTask = fixtureState.tasks.get("32".repeat(16));
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  await openRunning();
  // The selected running task is retried: queued work uses the same detail route.
  const requeued = new Map(fixtureState.tasks).set(task.id, { ...task, status: "queued" });
  await act(async () => { tree.update(createElement(Harness, { editable: true, state: { ...fixtureState, tasks: requeued } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 1, "queued work opens the shared task detail");
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  await act(async () => { tree.root.findByProps({ "aria-label": "Task details" }).props.onClose(); });
  // A queued row uses the same task-selection route as every other Work row.
  const queuedRow = tree.root.findAllByProps({ className: "dfConsoleItem dfWorkRow" }).find((row) => rowTitle(row) === queuedTask.title);
  await act(async () => { rowButton(queuedRow).props.onClick(); });
  const started = new Map(fixtureState.tasks).set(queuedTask.id, { ...queuedTask, status: "running" });
  await act(async () => { tree.update(createElement(Harness, { editable: true, state: { ...fixtureState, tasks: started } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 1, "a queued task remains on the shared detail route");
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  await act(async () => { tree.root.findByProps({ "data-human-request-id": ids.request }).props.onClick(); });
  assert.equal(questions[0], fixtureState.humanRequests.get(ids.request));
  assert.equal(tree.root.findByProps({ "aria-label": "Selected question" }).findByProps({ className: "dfFactoryConsole__question" }).children.join(""), "Should the migration also cover the users table? The plan only names accounts.");
  await openRunning();
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 1);
  const tasks = new Map(fixtureState.tasks);
  tasks.delete(task.id);
  await act(async () => { tree.update(createElement(Harness, { state: { ...fixtureState, tasks } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "removed work is not retained as invented history");
  await act(async () => tree.unmount());
});

test("removed history controls leave inspected task detail intact", () => {
  const agentMarkup = renderToStaticMarkup(createElement(AgentPanel, { agent: fixtureState.agents.get(ids.agent), state: fixtureState, ready: true }));
  const workMarkup = renderToStaticMarkup(createElement(WorkPanel, { state: fixtureState, rows: workRows(fixtureState, []), filter: "all", onFilter() {}, byMission: false, onByMission() {}, missions: null, ready: true, onSelectProduction() {}, onMission() {} }));
  const detailMarkup = renderToStaticMarkup(createElement(TaskDetail, { task: fixtureState.tasks.get(ids.task), onLoadTaskDetail: async () => ({ taskId: ids.task, revision: 1n, head: 1n, instruction: "kept", feedback: "", peerQuestions: [] }) }));
  assert.doesNotMatch(agentMarkup, /Recent work|aria-label="History"/);
  assert.doesNotMatch(workMarkup, /Recent work|aria-label="History"/);
  assert.match(detailMarkup, /aria-label="Work details"/);
});

test("floor omits the global evidence essay", () => {
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, view: "floor" }));
  assert.doesNotMatch(markup, /Floor evidence|Rooms describe a repository snapshot/);
  assert.doesNotMatch(markup, /Scroll the floor to explore/);
  assert.doesNotMatch(markup, /Floor detail|Topology detail|Social furniture|one connected floor/);
  assert.match(markup, /Find machine/);
});





test("mobile navigation switches presentation without mutating work", () => {
  const calls = [];
  const elements = consoleElements({ status: "ready", state: fixtureState, detail: "floor", onDetail: (value) => calls.push(value) });
  const nav = elements.find((element) => element.props["aria-label"] === "Console views");
  const buttons = nav.props.children.filter((element) => element.type === "button" || element.type.name === "IconButton");
  assert.equal(buttons[0].props["aria-pressed"], true);
  assert.deepEqual(buttons.map(textOf), ["Floor", "Agents", "Work", "Board", "Library"]);
  buttons.find((button) => textOf(button) === "Work").props.onClick();
  assert.deepEqual(calls, ["work"]);
  assert.equal(elements.find((element) => element.type === "main").props["data-mobile-view"], "floor");
});


test("mobile Agents opens the sidebar roster and selection keeps the floor mounted", async () => {
  let tree;
  function Harness() {
    const [detail, setDetail] = useState("work");
    const [selectedAgent, setAgent] = useState();
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, detail, onDetail: setDetail, selectedAgent,
      onSelectAgent: (agent) => { setAgent(agent); setDetail("agent"); } });
  }
  await act(async () => { tree = create(createElement(Harness)); });
  const floor = tree.root.findByProps({ className: "dfFactoryFloor__map" });
  const nav = tree.root.findByProps({ "aria-label": "Console views" });
  await act(async () => nav.findAllByType("button")[1].props.onClick());
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "detail");
  assert.equal(nav.findAllByType("button")[1].props["aria-pressed"], true);
  const roster = tree.root.findByProps({ className: "dfAgentList" });
  await act(async () => roster.findAllByType("button")[0].props.onClick());
  assert.ok(tree.root.findByType(FactoryConsole).props.selectedAgent);
  assert.equal(tree.root.findAllByProps({ className: "dfAgentList" }).length, 0);
  await act(async () => tree.root.findAllByType("button").find((button) => button.props.children === "All agents").props.onClick());
  assert.equal(tree.root.findAllByProps({ className: "dfAgentList" }).length, 1);
  await act(async () => nav.findAllByType("button")[0].props.onClick());
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
  await act(async () => { renderer.root.findAllByProps({ "aria-label": "Inspect kernel" })[0].props.onClick(); });
  assert.equal(reads, preparedReads, "entity selection reuses the projection");
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

test("floor project changes clear task selection without restoring an old dialog", async () => {
  const task = [...fixtureState.tasks.values()].find((task) => task.project_id === ids.secondProject);
  function Harness() {
    const [selectedTaskId, onSelectTask] = useState(task.id);
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, selectedTaskId, onSelectTask });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 1);
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.project } }));
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0);
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: "" } }));
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0);
  await act(async () => tree.unmount());
});

test("one machine inspector loads its evidence on open, investigates once and discusses its own node", async (t) => {
  const warnings = [];
  t.mock.method(console, "error", (...args) => warnings.push(args.join(" ")));
  const store = fixtureGraph.nodes.find((node) => node.label === "state.db"), loads = [], opens = [], tasks = [];
  const props = { state: fixtureState, graphs: fixtureGraphs, requestedEntity: { id: store.id },
    onLoadNode: async (project, node) => { loads.push([project, node]); return { project_id: project, node_id: node, selectors: { "go.package": "internal/kernel/store" }, evidence: [{ origin: "static", source: "go-ast", confidence: "high" }], sources: [], modules: [], observers: [] }; },
    onOpenBoard: (...args) => opens.push(args), onAddTask: async (...args) => { tasks.push(args); return true; } };
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryFloor, props)); });
  const inspector = () => renderer.root.findAllByProps({ "aria-label": "Machine inspector" });
  const click = async (label) => { await act(async () => renderer.root.findAllByType("button").find((button) => button.children.join("") === label).props.onClick()); };
  assert.equal(inspector().length, 1);
  assert.deepEqual(loads, [[ids.project, store.id]], "evidence is fetched when the inspector opens, by its project and node");
  assert.ok(inspector()[0].findAllByType("dt").some((term) => term.children.join("") === "go.package"), "the loaded selectors are shown");
  await click("Discuss this machine");
  assert.deepEqual(opens, [[ids.project, `${ids.project}:${store.id}`]]);
  await click("Investigate");
  assert.equal(tasks.length, 1);
  assert.equal(tasks[0][0].project_id, ids.project);
  assert.equal(tasks[0][2], "any");
  assert.match(tasks[0][1], /Investigate the store "state\.db" in kernel/);
  assert.match(tasks[0][1], /Nothing observes this component/);
  assert.ok(renderer.root.findAllByType("button").some((button) => button.props.disabled && button.children.join("") === "Investigation queued"), "asking twice is not possible");
  const other = fixtureGraph.nodes.find((node) => node.label === "web");
  await act(async () => renderer.update(createElement(FactoryFloor, { ...props, requestedEntity: { id: other.id } })));
  assert.equal(inspector().length, 1);
  assert.deepEqual(loads.at(-1), [ids.project, other.id]);
  assert.ok(renderer.root.findAllByType("button").some((button) => button.children.join("") === "Investigate"), "the old investigation does not follow the new machine");
  assert.ok(!warnings.some((message) => message.includes("same key")), "inspectors must have distinct reconciliation identities");
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

test("Use in a new task opens New task with removable Library chips that are submitted with the task", async () => {
  const attempts = [];
  let tree;
  function Harness() { const [detail, setDetail] = useState("floor"); return createElement(FactoryConsole, { status: "ready", state: fixtureState, detail, onDetail: setDetail, onProjectContent: async () => ({ items: [], next_offset: 0 }), onAddTask: async (...args) => { attempts.push(args); return true; } }); }
  await act(async () => { tree = create(createElement(Harness)); });
  const guide = { content_id: "ab".repeat(16), revision: 3n, title: "Deployment guide", project_id: ids.project };
  const notes = { content_id: "cd".repeat(16), revision: 1n, title: "Release notes", project_id: ids.project };
  await act(async () => tree.root.findByType(FactoryFloor).props.onOpenLibrary(ids.project));
  await act(async () => tree.root.findByType(ProjectLibrary).props.onUseInTask(guide));
  assert.equal(tree.root.findAllByType(ProjectLibrary).length, 0, "the Library closes into the existing form");
  await act(async () => tree.root.findByType(FactoryFloor).props.onOpenLibrary(ids.project));
  await act(async () => tree.root.findByType(ProjectLibrary).props.onUseInTask(notes));
  const form = () => tree.root.findByProps({ "aria-label": "New task" });
  assert.equal(form().parent.props.open, true, "New task opens with the chosen context");
  const chips = () => tree.root.findAllByProps({ "aria-label": "Library context" }).flatMap((list) => list.findAllByType("li").map((item) => textOf(item.findByType("span"))));
  assert.deepEqual(chips(), ["Deployment guide · Revision 3", "Release notes · Revision 1"]);
  const targets = form().findByType("select").findAllByType("option").map((option) => option.props.value);
  assert.ok(targets.length > 0 && targets.every((value) => value === `any:${ids.project}` || fixtureState.agents.get(value)?.project_id === ids.project), "targets stay in the content's project");
  await act(async () => tree.root.findByProps({ "aria-label": "Remove Release notes" }).props.onClick());
  assert.deepEqual(chips(), ["Deployment guide · Revision 3"]);
  const nativeFormData = globalThis.FormData;
  globalThis.FormData = class { get(name) { return name === "target" ? `any:${ids.project}` : "Follow the guide"; } };
  try { await act(async () => form().props.onSubmit({ preventDefault() {}, currentTarget: { reset() {} } })); } finally { globalThis.FormData = nativeFormData; }
  assert.deepEqual(attempts[0].slice(1, 3), ["Follow the guide", "any"]);
  assert.deepEqual(attempts[0][4], [guide], "the chip is submitted with the task");
  assert.deepEqual(chips(), [], "an added task clears its context");
  await act(async () => tree.unmount());
});

test("new-work admission reports acknowledgement and preserves active work", async () => {
  let acknowledge;
  const calls = [];
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, {
    status: "ready", state: fixtureState,
    onSetDispatch: (revision, enabled) => { calls.push({ revision, enabled }); return new Promise((resolve) => { acknowledge = resolve; }); },
  })); });
  const label = fixtureState.factory.dispatch_enabled ? "Pause new work" : "Resume new work";
  const control = tree.root.findAllByType("button").find((button) => textOf(button) === label);
  await act(async () => control.props.onClick());
  assert.deepEqual(calls, [{ revision: fixtureState.factory.revision, enabled: !fixtureState.factory.dispatch_enabled }]);
  assert.match(JSON.stringify(tree.toJSON()), /Waiting…/);
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /New work resumed\.|New work paused\. Active/);
  await act(async () => acknowledge({ revision: fixtureState.factory.revision + 1n, enabled: !fixtureState.factory.dispatch_enabled }));
  assert.match(JSON.stringify(tree.toJSON()), fixtureState.factory.dispatch_enabled ? /New work paused\. Active processes continue\./ : /New work resumed\./);
  await act(async () => tree.unmount());
});

test("Missions and Production share the task dialog and preserve their origin on close", async () => {
  const { MissionsPanel } = await import("../dist/src/missions-panel.js");
  const { ProductionPanel } = await import("../dist/src/production-panel.js");
  const historical = { ...fixtureState.tasks.get(ids.task), id: "fe".repeat(16), status: "succeeded", title: "Delivered historical task" };
  for (const [origin, Panel] of [["missions", MissionsPanel], ["production", ProductionPanel]]) {
    const navigations = [], selections = []; let tree;
    await act(async () => { tree = create(createElement(FactoryConsole, {status: "ready", state: fixtureState, detail: origin, onDetail: value => navigations.push(value), onSelectTask: id => selections.push(id), onLoadTaskDetail: async task => ({taskId: task.id, revision: task.revision, instruction: "Retained instruction", feedback: "", peerQuestions: []})})); });
    await act(async () => tree.root.findByType(Panel).props.onOpenTask(historical));
    assert.equal(tree.root.findAllByProps({"aria-label": "Task details"}).length, 1);
    assert.equal(tree.root.findByProps({"aria-label": "Work details"}).findByType("h3").children.join(""), historical.title);
    await act(async () => tree.root.findByProps({"aria-label": "Task details"}).props.onClose());
    assert.equal(tree.root.findAllByProps({"aria-label": "Task details"}).length, 0);
    assert.deepEqual(navigations, []);
    assert.deepEqual(selections, []);
    await act(async () => tree.unmount());
  }
});

test("opening a project shelf scopes Library without changing the floor filter", async () => {
  const calls = [];
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs,
    onProjectContent: async (operation, input) => { calls.push({ operation, input }); return { items: [], next_offset: 0 }; },
  })); });
  await act(async () => tree.root.findByType(FactoryFloor).props.onOpenLibrary(ids.secondProject));
  assert.equal(tree.root.findByType(ProjectLibrary).props.initialProjectId, ids.secondProject);
  assert.equal(tree.root.findByType(FactoryFloor).props.projectId, undefined);
  assert.equal(tree.root.findByType(FactoryFloor).props.state.projects.size, fixtureState.projects.size);
  assert.ok(calls.some(({ operation, input }) => operation === "search" && input.project_id === ids.secondProject));
  await act(async () => tree.unmount());
});


test("Library source navigation selects the machine on the floor and keeps one inspector", async () => {
  const [first, destination] = [fixtureGraph.nodes.find((node) => node.label === "state.db"), fixtureGraph.nodes.find((node) => node.label === "web")];
  let tree;
  await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, onProjectContent: async () => ({ items: [] }) })); });
  const assertSelection = (id) => {
    const inspectors = tree.root.findAllByProps({ "aria-label": "Machine inspector" });
    assert.equal(inspectors.length, id ? 1 : 0);
    if (id) {
      assert.equal(inspectors[0].findAllByType("code")[0].children.join(""), id);
      assert.equal(inspectors[0].findByType("details").props.open, undefined, "evidence starts collapsed");
    }
  };
  for (let visit = 0; visit < 2; visit++) {
    await act(async () => tree.root.findByProps({ type: "search" }).props.onChange({ target: { value: first.label } }));
    await act(async () => tree.root.findByProps({ className: "dfFactoryEntityTools__results" }).findAllByType("button").find((button) => button.children.join("") === `${first.label} · ${first.kind}`).props.onClick({ currentTarget: { closest: () => null } }));
    assertSelection(first.id);
    await act(async () => tree.root.findByType(FactoryFloor).props.onOpenLibrary(ids.project));
    await act(async () => tree.root.findByType(ProjectLibrary).props.onSource(`${ids.project}:${destination.id}`));
    assertSelection(destination.id);
    await act(async () => tree.root.findAllByType("button").find((button) => button.children.join("") === "Focus on floor").props.onClick());
    assertSelection(destination.id);
  }
  await act(async () => tree.root.findAllByType("button").find((button) => button.children.join("") === "Close").props.onClick());
  assertSelection("");
  await act(async () => tree.unmount());
});

test("one project is selected automatically, with no header picker, and a missions prompt when several exist", () => {
  const one = baseState({ projects: new Map([...fixtureState.projects].slice(0, 1)) });
  const single = render({ state: one, detail: "work" });
  assert.equal(single.includes('aria-label="Project"'), false);
  assert.equal(single.includes("Mission project"), false);
  assert.match(single, /Missions/);
  const many = render({ detail: "work" });
  assert.match(many, /aria-label="Project"/);
  assert.match(many, /Choose a project in the header/);
  assert.equal(many.includes("Mission project"), false);
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
  const tabs = tree.root.findAllByType("button").filter((button) => textOf(button).startsWith("Needs you"));
  assert.ok(tabs.length > 0);
  assert.ok(tabs.every((button) => !/\d/.test(textOf(button))));
  assert.doesNotMatch(text, /Review the state projection/);
  await act(async () => tree.unmount());
});

test("opening a question from the Library switches the header to its project so the reply form renders", async () => {
  const other = fixtureState.humanRequests.get(ids.request).project_id === ids.project ? ids.secondProject : ids.project;
  function Harness() {
    const [chosen, setChosen] = useState();
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, onDetail() {}, detail: "work", selectedHumanRequest: chosen, onSelectHumanRequest: (request) => setChosen(selectedRequest({ request })), onCloseHumanRequest() {}, onReplyHumanRequest() {} });
  }
  let tree;
  await act(async () => { tree = create(createElement(Harness)); });
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: other } }));
  await act(async () => tree.root.findByType(FactoryFloor).props.onOpenLibrary(other));
  await act(async () => tree.root.findByType(ProjectLibrary).props.onRecord("human_request", ids.request, fixtureState.humanRequests.get(ids.request).project_id));
  assert.equal(tree.root.findByProps({ "aria-label": "Project" }).props.value, fixtureState.humanRequests.get(ids.request).project_id);
  assert.equal(tree.root.findAllByProps({ "aria-label": "Selected question" }).length > 0, true);
  await act(async () => tree.unmount());
});

test("a specialist is grouped, states what it is doing and owes, and Stop archives once idle", async () => {
  const worker = fixtureState.agents.get(ids.agent);
  const specialist = { ...worker, id: "5a".repeat(16), name: "Doc Watcher", archived: false, idle_policy: "standing_instruction", idle_after_seconds: 600, idle_instruction: "Keep the docs true to the code.\nSecond line stays out.", idle_run_budget: 4, idle_runs_used: 1, idle_wake_on: "failures",
    specialist: { next_review_at_ms: 1750000000000, next_reason: "events", waiting: "capacity", quiet_reviews: 0, open_proposals: 1, open_proposal_limit: 3, last_review_task_id: "" } };
  const state = baseState({ agents: new Map([...fixtureState.agents, [specialist.id, specialist]]), humanRequests: new Map() });
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "agent", state, selectedAgent: { id: specialist.id, name: specialist.name, revision: specialist.revision }, onSaveAgentConfig() {} }));
  assert.match(renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "agent", state })), /<section aria-label="Specialist">/, "its own group");
  assert.match(markup, /waiting: waiting for a free background slot/);
  assert.match(markup, /aria-label="Remit">Keep the docs true to the code\.</);
  assert.match(markup, /Next review .*\(events\) or sooner on failures/);
  assert.match(markup, /Stop specialist/);
  assert.doesNotMatch(markup, /Archive worker/);
  // Stop asks first, then archives; a running review is ended by the same archive.
  const edits = [];
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", detail: "agent", state, selectedAgent: { id: specialist.id, name: specialist.name, revision: specialist.revision }, onSaveAgentConfig: (c) => edits.push(c) })); });
  const button = (label) => renderer.root.findAllByType("button").find((b) => b.children.join("") === label);
  await act(async () => { button("Stop specialist").props.onClick(); });
  assert.deepEqual(edits, []);
  await act(async () => { button("Confirm stop").props.onClick(); });
  assert.deepEqual(edits, [{ archived: true }]);
  const running = baseState({ agents: state.agents, humanRequests: new Map(), tasks: new Map([...fixtureState.tasks, ["c1".repeat(16), { ...fixtureState.tasks.get(ids.task), id: "c1".repeat(16), title: "Standing instruction", status: "running", assigned_agent_id: specialist.id }]]) });
  const live = renderToStaticMarkup(createElement(FactoryConsole, { status: "ready", detail: "agent", state: running, selectedAgent: { id: specialist.id, name: specialist.name, revision: specialist.revision }, onSaveAgentConfig() {} }));
  assert.match(live, /reviewing: Keep the docs true to the code\./, "a running review is its remit at work");
  assert.match(live, /Stop specialist/);
});
