import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { createElement, isValidElement, useEffect, useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { MAX_SNAPSHOT_ENTITIES, MAX_TASK_PRIORITY, ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryApp, FactoryConsole } from "../dist/src/index.js";
import { layoutScene } from "../dist/src/factory-scene/scene.js";
import { projectGraph, projectFloor, workRows } from "../dist/src/console-view.js";
import { FactoryFloor, StageMeter } from "../dist/src/console-screens.js";
import { ProjectLibrary } from "../dist/src/project-library.js";
import { SettingsDialog, WorkPanel } from "../dist/src/console-sidebar.js";
import { FactoryScene } from "../dist/src/factory-scene/factory-scene.js";
import { TerminalPanel } from "../dist/src/factory-app.js";
import { DEFAULT_FLOOR_APPEARANCE, readFloorAppearance } from "../dist/src/floor-appearance.js";
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

test("community help works disconnected without private report fields", () => {
  const markup = render({ status: "closed", state: undefined, settingsOpen: true });
  for (const href of ["https://darkfactory.build/feedback?kind=bug", "https://darkfactory.build/feedback?kind=feature", "https://darkfactory.build/backlog"]) {
    assert.ok(markup.includes(`href="${href}" target="_blank" rel="noopener noreferrer"`));
  }
  assert.match(markup, /Report a problem/);
  assert.doesNotMatch(markup, /Reporting and voting/);
});

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

// Compose the runtime's independently memoized projections for fixture assertions.
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

test("error banner keeps its centered layout after the paragraph reset", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /\.dfFactoryConsole :where\(h1, h2, p, dl, ul\),[\s\S]*?\.dfConsoleSidebar :where\(h1, h2, h3, p, dl, ul\)\s*\{\s*margin: 0;\s*\}/);
  assert.match(css, /\.dfFactoryConsole__error\s*\{[\s\S]*?margin: 0 auto 1\.25rem;/);
  assert.match(css, /\.dfFactoryFloor__map \{ overflow: auto; max-height: 70vh;/);
  assert.match(css, /\.dfFactoryTooltip \{[^}]*-webkit-line-clamp: 8;[^}]*pointer-events: none;/, "a tooltip stays short enough to need no scrolling, and never takes the click meant for what it covers");
  assert.match(render(), /class="dfFactoryFloor__map" role="region" aria-label="Scrollable factory floor" tabindex="0"/);
  assert.equal(css.includes("@keyframes dfFactoryScene"), false);
});

test("floor appearance is local, field-validated, and available before a connection", () => {
  assert.deepEqual(readFloorAppearance('{"scenery":"off","dependencyLinks":"bad","labels":"names-and-counts","taskProps":false,"animation":"off"}'), {
    ...DEFAULT_FLOOR_APPEARANCE, scenery: "off", animation: "off",
  });
  assert.deepEqual(readFloorAppearance('{"scenery":"subtle"}'), { ...DEFAULT_FLOOR_APPEARANCE, scenery: "subtle" });
  assert.deepEqual(readFloorAppearance("not json"), DEFAULT_FLOOR_APPEARANCE);
  const markup = render({ status: "closed", settingsOpen: true });
  for (const text of ["Scenery", "Animation", "Reset floor appearance", "Saved in this browser."]) assert.match(markup, new RegExp(text));
  assert.doesNotMatch(markup, /Dependency links|Task props|Ambient life/);
});

test("repository roots stay inside private settings", () => {
  const project = fixtureState.projects.get(ids.project);
  const root = "/private/operator/checkout";
  const repositories = new Map([[project.id, [{ id: "ad".repeat(16), project_id: project.id, name: "Checkout", root, base_ref: "main", enabled: true, default: true, revision: 1n, fetch_state: "setup_required", publication_state: "ready", github_repository_id: 123456n, readiness_message: "Git checkout needs operator setup." }]]]);
  assert.equal(render({ repositories }).includes(root), false);
  const settings = render({ settingsOpen: true, repositories });
  assert.match(settings, /Repositories/);
  assert.match(settings, new RegExp(root));
  assert.match(settings, /Fetch: setup required/);
  assert.match(settings, /Publication: Identity verified/);
  assert.match(settings, /base branch and Git login/);
  assert.match(settings, /Check fetch readiness/);
  assert.match(settings, /Refresh GitHub binding/);
  assert.match(settings, /Git checkout needs operator setup\./);
  assert.match(settings, /Add repository/);
});

test("floor appearance waits for storage, changes while disconnected, and resets only itself", () => {
  const priorWindow = globalThis.window;
  const entries = new Map([
    ["dark-factory.floor-appearance", '{"scenery":"subtle","dependencyLinks":"overview","labels":"bad","taskProps":"bad","animation":"off"}'],
    ["dark-factory.pairing", "keep"],
  ]);
  const writes = [];
  globalThis.window = { localStorage: {
    getItem: (key) => entries.get(key) ?? null,
    setItem: (key, value) => { writes.push([key, value]); entries.set(key, value); },
    removeItem: (key) => { writes.push([key]); entries.delete(key); },
  } };
  try {
    let tree;
    act(() => { tree = create(createElement(FactoryConsole, { status: "closed", state: baseState(), settingsOpen: true })); });
    assert.deepEqual(writes, [], "loading never writes defaults");
    const selects = tree.root.findAllByType("select").filter((node) => ["subtle", "off"].includes(node.props.value));
    assert.equal(selects[0].props.value, "subtle");
    assert.equal(selects[1].props.value, "off");
    act(() => { selects[0].props.onChange({ currentTarget: { value: "off" } }); });
    assert.deepEqual(JSON.parse(entries.get("dark-factory.floor-appearance")), { ...DEFAULT_FLOOR_APPEARANCE, scenery: "off", animation: "off" });
    act(() => { tree.root.findAllByType("button").find((button) => button.props.children === "Reset floor appearance").props.onClick(); });
    assert.equal(entries.has("dark-factory.floor-appearance"), false);
    assert.equal(entries.get("dark-factory.pairing"), "keep");
  } finally {
    if (priorWindow === undefined) delete globalThis.window;
    else globalThis.window = priorWindow;
  }
});

test("blocked floor-appearance storage leaves the console renderable", () => {
  const priorWindow = globalThis.window;
  globalThis.window = { localStorage: { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); }, removeItem: () => { throw new Error("blocked"); } } };
  try {
    assert.doesNotThrow(() => act(() => { create(createElement(FactoryConsole, { status: "closed", state: baseState(), settingsOpen: true })); }));
  } finally {
    if (priorWindow === undefined) delete globalThis.window;
    else globalThis.window = priorWindow;
  }
});

test("one screen keeps Factory and the operator panels together", () => {
  const markup = render();
  assert.match(markup, /<main class="dfFactoryConsole" aria-label="Factory operator console">/);
  for (const label of ["Factory floor", "Selected detail", "Work", "Left view", "Right panel"]) {
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

test("the roster stays visible while the optional floor opens and closes", () => {
  const floor = render();
  assert.match(floor, /aria-label="Dark Factory operational floor"/);

  const agents = render({ view: "agents" });
  assert.match(agents, /aria-label="Agents"/);
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
  assert.deepEqual(layoutScene(overlay.graph).rooms, layoutScene(baseline.graph).rooms);
  assert.deepEqual(new Set(overlay.workers.map((worker) => worker.id)), new Set(changed.agents.keys()));
  assert.equal(baseline.workers.find((worker) => worker.id === ids.agent).location, "unobserved");
  const web = baseline.graph.halls.find((hall) => hall.label === "web").id;
  assert.equal(overlay.workers.find((worker) => worker.id === ids.agent).nodeId, web);
  const observed = floorScene(fixtureState, fixtureGraphs, undefined,
    new Map([[ids.idleAgent, runSample(ids.idleAgent, ["web"], "72".repeat(16))]]))
    .workers.find((worker) => worker.id === ids.idleAgent);
  assert.deepEqual([observed.location, observed.nodeId, observed.locationLabel], ["last-observed", web, "web"]);
  const fallback = floorScene(fixtureState, undefined);
  assert.deepEqual(fallback.graph.halls, [], "no graph served, no halls invented");
  assert.ok(fallback.workers.every((worker) => worker.nodeId === undefined));
  const empty = floorScene(undefined, undefined);
  assert.deepEqual([empty.graph.halls, empty.workers, empty.tasks], [[], [], []]);
});

test("floor tasks retain every served task and its exact observed footprint", () => {
  const scene = floorScene(fixtureState, fixtureGraphs, new Map([[ids.agent, runSample(ids.agent, ["web", "internal/kernel/store"])] ]));
  const hall = (label) => scene.graph.halls.find((item) => item.label === label).id;
  assert.deepEqual(scene.tasks, [
    {
      id: ids.task,
      agentId: ids.agent,
      projectId: ids.project,
      title: "Review the state projection",
      status: "running",
      roomIds: [hall("kernel"), hall("web")],
      representativeRoomId: hall("kernel"),
      observation: { taskRevision: fixtureState.tasks.get(ids.task).revision, runId: "71".repeat(16) },
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
  const worker = scene.workers.find((item) => item.id === ids.agent);
  assert.deepEqual([worker.nodeId, worker.observedBayId], [hall("kernel"), "a4".repeat(16)], "at the store the run changed, in the unit holding it");
});

test("task footprints reject stale, retry, cancelled, terminal, and cross-project samples", () => {
  const original = fixtureState.tasks.get(ids.task);
  const sample = runSample(ids.agent, ["web"]);
  const footprint = (task, candidate = sample) => floorScene(
    baseState({ tasks: new Map([[task.id, task]]) }), fixtureGraphs, new Map([[ids.agent, candidate]]),
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
  assert.deepEqual(scene.tasks[0].roomIds, [hex(0x71)]);
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
  assert.equal(observed.location, "working", "an observed overseer keeps the observed hall");
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
  assert.match(render({ view: "agents" }), />Overseer</);
});

test("transitional session statuses have stable live labels and offer no factory action", () => {
  for (const status of ["idle", "connecting", "authenticating", "syncing", "closed"]) {
    const markup = render({ status, onSelectAgent: () => {}, onSelectHumanRequest: () => {}, onView: () => {}, onToggleSettings: () => {} });
    assert.match(markup, new RegExp(`>${status[0].toUpperCase()}${status.slice(1)}<`));
    assert.match(markup, /class="dfFactoryConsole__connection" aria-label="Connection status:/);
    // SETTINGS is the only button before the factory is ready; the floor is a
    // native disclosure, not a factory action.
    const live = (markup.match(/<button(?![^>]*disabled)/g) ?? []).length;
    assert.equal(live, 1, status);
    assert.match(markup, /<button type="button" aria-pressed="false" disabled=""/);
  }
  const ready = render({ status: "ready" });
  assert.match(ready, /class="dfFactoryConsole__visuallyHidden"/);
  assert.match(ready, /role="status" aria-live="polite" aria-atomic="true"/);
  assert.match(render({ status: "closed", error: new SessionError("connection") }), /class="dfFactoryConsole__visuallyHidden" aria-label="Connection status:/);
});

test("closed and pairing-uncertain errors have no ineffective action", () => {
  for (const error of [new SessionError("connection", true), new SessionError("pairing_uncertain"), new ProtocolError("malformed")]) {
    const markup = render({ status: "closed", error, onSelectAgent: () => {}, onSelectHumanRequest: () => {}, onView: () => {}, onToggleSettings: () => {} });
    assert.match(markup, /role="alert"/);
    // The banner offers nothing to press, and the only live buttons on a
    // closed console is SETTINGS, which changes nothing in the factory.
    assert.equal((markup.match(/<button(?![^>]*disabled)/g) ?? []).length, 1);
    assert.doesNotMatch(markup, /role="alert"[^>]*>[^<]*<button/);
    assert.equal(markup.includes("Retry connection"), false);
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

test("Work keeps every served item reachable", () => {
  const emptyState = baseState({ projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map() });
  assert.match(render({ state: emptyState }), /No open work/);
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
  assert.equal((markup.match(/<li class="dfConsoleItem dfWorkRow"/g) ?? []).length, 9);
  assert.equal((markup.match(/\+1 more/g) ?? []).length, 0);
  assert.doesNotMatch(markup, /9 items/);
  assert.match(markup, /Task 8/);
  assert.equal((render({ state: bounded, view: "agents" }).match(/dfAgentList__row/g) ?? []).length, 0, "no handler, no button");
  assert.equal((render({ state: bounded, view: "agents", onSelectAgent: () => {} }).match(/dfAgentList__row/g) ?? []).length, 9);
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

test("History reads the project's tasks and is disabled across all projects", async () => {
  const scopes = [];
  let tree;
  const props = { status: "ready", state: fixtureState, detail: "work", onDetail() {}, onLoadTaskList: async (scope) => { scopes.push(scope); return { agentId: ids.project, head: 1n, total: 0n, tasks: [], hasMore: false }; } };
  await act(async () => { tree = create(createElement(FactoryConsole, props)); });
  const history = () => tree.root.findByProps({ "aria-label": "Work" }).findAllByType("button").find((button) => button.props["aria-label"] === "History");
  assert.equal(history().props.disabled, true);
  await act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value: ids.project } }));
  await act(async () => history().props.onClick());
  assert.deepEqual(scopes, [{ project_id: ids.project }]);
  await act(async () => tree.unmount());
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
  await act(async () => { panel.findByProps({ "aria-label": "Show overseer passes" }).props.onClick(); });
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
  const markup = render({ state, view: "agents" });
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
  assert.match(render({ state: undefined, status: "syncing", view: "agents" }), /waiting for the factory/);
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
    view: "agents",
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
  assert.match(markup, /Any eligible worker · Priority/);
  assert.match(markup, /<option value="" disabled=""[^>]*>Any eligible worker<\/option>/);
});

test("the queued task row keeps served order and changes its exact priority", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const edits = [];
    const detailReads = [];
    const queued = { ...fixtureState.tasks.get([...fixtureState.tasks.keys()][1]), title: "Served first", priority: 2 };
    const other = { ...queued, id: "39".repeat(16), title: "Higher but served second", priority: 9, revision: 20n };
    const archivedPeer = { ...fixtureState.agents.get("23".repeat(16)), id: "24".repeat(16), name: "Retired Builder", archived: true, paused: true };
    const state = baseState({
      agents: new Map([...fixtureState.agents, [archivedPeer.id, archivedPeer]]),
      tasks: new Map([[queued.id, { ...queued, assigned_agent_id: ids.agent }], [other.id, { ...other, assigned_agent_id: ids.agent }]]),
    });
    const props = {
      status: "ready",
      state,
      detail: "work",
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
    const queueRows = renderer.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem dfWorkRow" }).filter((row) => [queued.title, other.title].includes(rowTitle(row)));
    assert.deepEqual(queueRows.map(rowTitle), [queued.title, other.title], "the queue keeps the server's per-agent order, rather than re-sorting priority");
    assert.ok(!renderer.root.findAllByType("p").some((paragraph) => paragraph.props.children === "Grouped by agent · no global start order"));
    assert.ok(renderer.root.findAllByType("span").some((span) => (Array.isArray(span.props.children) ? span.props.children.join("") : String(span.props.children)).includes("Priority 2")));
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

    const editBrief = () => renderer.root.findAllByType("button").find((button) => button.props.children === "Edit brief");
    const firstRow = renderer.root.findByProps({ "aria-label": "Work" }).findAllByProps({ className: "dfConsoleItem dfWorkRow" }).find((row) => rowTitle(row) === queued.title);
    assert.equal(firstRow.findByProps({ className: "dfConsoleItem__detail" }).props.hidden, true, "queue rows start collapsed");
    assert.deepEqual(detailReads, [], "collapsed queued rows do not read private briefs");
    await act(async () => { rowButton(firstRow).props.onClick(); });
    assert.deepEqual(detailReads, [queued.id]);
    const title = renderer.root.findAllByType("input").find((input) => input.props.value === queued.title);
    await act(async () => { title.props.onChange({ currentTarget: { value: "Renamed" } }); });
    const instruction = renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`);
    await act(async () => { instruction.props.onChange({ currentTarget: { value: "Replacement brief" } }); });
		assert.ok(renderer.root.findAllByType("pre").some((item) => item.props.children === "Review this carefully"));
    await act(async () => { await renderer.root.findAllByType("button").find((button) => button.props.children === "Save brief").props.onClick(); });
    assert.deepEqual(edits.at(-1), [queued.id, { title: "Renamed", body: "Replacement brief" }]);
    await act(async () => { rowButton(firstRow).props.onClick(); rowButton(firstRow).props.onClick(); });
    assert.deepEqual(detailReads, [queued.id], "reopening a cached row does not overwrite the brief");

    const assign = renderer.root.findAllByType("select").find((select) => select.props.id === `df-assign-${queued.id}`);
    // Reassignment offers only active agents in the same project. Retired
    // names remain on historical rows, but cannot receive new queued work.
    assert.deepEqual(assign.props.children.map((option) => option.props.children), ["Builder One", "Builder Two"]);
    await act(async () => { assign.props.onChange({ currentTarget: { value: "23".repeat(16) } }); });
    assert.deepEqual(edits.at(-1), [queued.id, { assignedAgentId: "23".repeat(16) }]);

    await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "Cancel").props.onClick(); });
    assert.deepEqual(edits.at(-1), [queued.id, { cancel: true }]);

    // A refused brief edit preserves the operator's drafts at the unchanged
    // revision, rather than silently replacing the intended instruction.
    await act(async () => { await editBrief().props.onClick(); });
    const titleValue = () => renderer.root.findAllByType("input").find((input) => input.props.id === `df-title-${queued.id}`).props.value;
    const instructionValue = () => renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`).props.value;
    await act(async () => { renderer.root.findAllByType("input").find((input) => input.props.id === `df-title-${queued.id}`).props.onChange({ currentTarget: { value: "Keep this draft" } }); });
    await act(async () => { renderer.root.findAllByType("textarea").find((input) => input.props.id === `df-instruction-${queued.id}`).props.onChange({ currentTarget: { value: "Keep this instruction" } }); });
    assert.equal(titleValue(), "Keep this draft");
    await act(async () => { await renderer.root.findAllByType("button").find((button) => button.props.children === "Older conversation").props.onClick(); });
    assert.equal(titleValue(), "Keep this draft");
    assert.equal(instructionValue(), "Keep this instruction");
    assert.ok(renderer.root.findAllByProps({ role: "alert" }).some((item) => String(item.props.children).includes("Save or discard your draft")));
    const revised = baseState({ tasks: new Map([[queued.id, { ...queued, assigned_agent_id: ids.agent, revision: queued.revision + 1n }], [other.id, { ...other, assigned_agent_id: ids.agent }]]) });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, state: revised })); });
    assert.equal(titleValue(), "Keep this draft");
    assert.equal(instructionValue(), "Keep this instruction");
    assert.equal(renderer.root.findAllByProps({ role: "alert" }).some((item) => String(item.props.children).includes("Task changed")), true);
    assert.equal(renderer.root.findAllByType("button").find((button) => button.props.children === "Save brief").props.disabled, true);
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
  await act(async () => { renderer.root.findAllByType("button").find((button) => button.props.children === "Show more").props.onClick(); });
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
    onLoadTaskList: async ({ agent_id }) => ({ agentId: agent_id, head: 9n, total: 1n, tasks: [agent_id === first.id ? firstTask : secondTask], hasMore: false }),
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

test("the settings modal keeps actionable settings compact", () => {
  const markup = render({ settingsOpen: true, onToggleSettings: () => {} });
  assert.match(markup, /<dialog class="dfConsoleDialog" aria-label="Settings">/);
  assert.doesNotMatch(markup, /aria-label="BUILDING"/);
  assert.doesNotMatch(markup, /<dt>DISPATCH<\/dt>/);
  assert.doesNotMatch(markup, /aria-label="This factory"/);
  assert.match(markup, /aria-label="Run limits"/);
  assert.doesNotMatch(markup, /<dt>(RUN ALLOWANCE|PER-RUN LIMIT)<\/dt>/);
  assert.equal((markup.match(/aria-label="Limits for North Workshop"/g) ?? []).length, 1);
  assert.equal((markup.match(/aria-label="Limits for South Workshop"/g) ?? []).length, 1);
  assert.match(markup, /5 runs used · 7 future runs left/);
  assert.match(markup, /3 runs used · unlimited/);
  assert.match(markup, /value="900"/);
  assert.match(markup, /aria-label="Pairing"/);
  assert.match(markup, /Pairing unavailable/);
  // The peer PR drops its own component into the same slot.
  const paired = render({ settingsOpen: true, onToggleSettings: () => {}, pairing: createElement("p", null, "Pair a phone") });
  assert.match(paired, /Pair a phone/);
  assert.equal(paired.includes("Pairing unavailable"), false);
  // The modal is over the console, so it neither closes nor replaces a sidebar.
  const both = render({ settingsOpen: true, onToggleSettings: () => {}, selectedAgent: agentSelection() });
  assert.match(both, /aria-label="Settings"/);
  assert.match(both, /aria-label="Agent Builder One"/);
});

test("private GitHub settings stays behind the paired admin surface", async () => {
  const calls = [];
  const settings = { settingsOpen: true, onToggleSettings: () => {}, onGitHub: (request) => calls.push(request) };
  const disconnected = render(settings);
  assert.match(disconnected, /aria-label="GitHub settings"/);
  assert.match(disconnected, />disconnected</);
  assert.match(disconnected, />Connect GitHub<\/button>/);
  let renderer;
  act(() => { renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState(), ...settings })); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Connect GitHub").props.onClick(); });
  assert.ok(calls.some((request) => request.action === "connect"));
  renderer.unmount();
  const denied = render({ ...settings, github: { pending: false, result: { state: "denied" } } });
  assert.match(denied, /Access expired or was denied/);
  act(() => { renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState(), ...settings, github: { pending: false, result: { state: "denied" } } })); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Reset GitHub access").props.onClick(); });
  assert.equal(calls.at(-1).action, "disconnect");
  renderer.unmount();
  const unavailable = render({ ...settings, github: { pending: false, result: { state: "unavailable" } } });
  assert.match(unavailable, /GitHub is unavailable/);
  const disconnectPending = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "", state: "disconnect_pending", repositories: [] } } } });
  assert.match(disconnectPending, /Retry disconnect/);
  const awaitingConfirmation = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "awaiting_confirmation", repositories: [] } } } });
  assert.match(awaitingConfirmation, /Reset GitHub access/);
  const connecting = render({ ...settings, github: { pending: false, result: { state: "ok", authorization: { connection_id: "c", authorization_url: "https://github.com/login/oauth/authorize", expires_at: 123n } } } });
  assert.match(connecting, /Reset GitHub access/);
  const expired = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "disconnected", repositories: [] } } } });
  assert.match(expired, /Reset GitHub access/);
  act(() => { renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState(), ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "disconnected", repositories: [] } } } })); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Reset GitHub access").props.onClick(); });
  assert.equal(calls.at(-1).action, "disconnect");
  renderer.unmount();
  act(() => { renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState(), ...settings, github: { pending: false, result: { state: "unavailable" } } })); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Retry GitHub access").props.onClick(); });
  assert.equal(calls.at(-1).action, "refresh");
  renderer.unmount();
  const connected = render({ ...settings, github: { pending: false, result: {
    state: "ok",
    status: { connection_id: "c", state: "connected", repositories: [] },
    installations: { installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, html_url: "https://github.com/settings/installations/7", eligibility: "available" }], next_page: 2 },
  } } });
  assert.match(connected, /Refresh access/);
  assert.match(connected, /Disconnect/);
  assert.match(connected, /factory-org · available/);
  assert.match(connected, /href="https:\/\/github.com\/settings\/installations\/7"/);
  assert.match(connected, /More installations/);
  assert.equal(connected.includes("javascript:"), false);
  assert.equal(connected.includes("evil.example"), false);
  assert.match(disconnected, /GitHub/);

  const noInstallation = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "connected", repositories: [] }, installations: { installations: [], installation_url: "https://github.com/apps/factory-maintainer/installations/new" } } } });
  assert.match(noInstallation, /Install or request GitHub app access/);
  assert.match(noInstallation, /href="https:\/\/github.com\/apps\/factory-maintainer\/installations\/new"/);
  const pendingInstallationPages = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "connected", repositories: [] }, installations: { installations: [], next_page: 2, installation_url: "https://github.com/apps/factory-maintainer/installations/new" } } } });
  assert.doesNotMatch(pendingInstallationPages, /Install or request GitHub app access/);
  const unavailableInstall = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "connected", repositories: [] }, installations: { installations: [], installation_url: "https://evil.example\/apps\/factory\/installations\/new" } } } });
  assert.doesNotMatch(unavailableInstall, /Install or request GitHub app access/);
  const visibleInstall = render({ ...settings, github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "connected", repositories: [] }, installations: { installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, eligibility: "available" }], installation_url: "https://github.com/apps/factory-maintainer/installations/new" } } } });
  assert.doesNotMatch(visibleInstall, /Install or request GitHub app access/);

  const pagerCalls = [];
  const pageProps = (installations) => ({ status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onGitHub: (request) => pagerCalls.push(request), github: { pending: false, result: { state: "ok", status: { connection_id: "pager", state: "connected", repositories: [] }, installations } } });
  act(() => { renderer = create(createElement(FactoryConsole, pageProps({ installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, html_url: "https://github.com/settings/installations/7", eligibility: "available" }], next_page: 2 }))); });
  pagerCalls.length = 0;
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "More installations").props.onClick(); });
  act(() => { renderer.update(createElement(FactoryConsole, pageProps({ installations: [{ id: 8, account: { id: 9, login: "another-org" }, suspended_at: null, html_url: "https://github.com/settings/installations/8", eligibility: "available" }], next_page: 3 }))); });
  assert.equal(renderer.root.findAllByType("button").some((button) => textOf(button) === "Previous installations"), true);
  act(() => { renderer.root.findAllByType("button").find((button) => textOf(button) === "Refresh access").props.onClick(); });
  assert.deepEqual(pagerCalls, [{ action: "installations", page: 2 }, { action: "refresh" }]);
  assert.equal(renderer.root.findAllByType("button").some((button) => textOf(button) === "Previous installations"), false);
  renderer.unmount();

  const retainedInstallations = (installations) => ({ status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onGitHub: () => {}, github: { pending: false, result: { state: "ok", status: { connection_id: "retained", state: "connected", repositories: [] }, installations } } });
  act(() => { renderer = create(createElement(FactoryConsole, retainedInstallations({ installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, eligibility: "available" }], next_page: 2, installation_url: "https://github.com/apps/factory-maintainer/installations/new" }))); });
  act(() => { renderer.update(createElement(FactoryConsole, retainedInstallations({ installations: [], installation_url: "https://github.com/apps/factory-maintainer/installations/new" }))); });
  assert.equal(renderer.root.findAllByType("a").some((anchor) => String(anchor.props.children).includes("Install or request GitHub app access")), false);
  renderer.unmount();

  const refreshCalls = [];
  const refreshProps = (installations) => ({ status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onGitHub: (request) => refreshCalls.push(request), github: { pending: false, result: { state: "ok", status: { connection_id: "refresh-link", state: "connected", repositories: [] }, installations } } });
  act(() => { renderer = create(createElement(FactoryConsole, refreshProps({ installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, eligibility: "available" }] }))); });
  act(() => { renderer.root.findAllByType("button").find((button) => textOf(button) === "Refresh access").props.onClick(); });
  act(() => { renderer.update(createElement(FactoryConsole, refreshProps({ installations: [], installation_url: "https://github.com/apps/factory-maintainer/installations/new" }))); });
  assert.equal(refreshCalls.at(-1).action, "refresh");
  assert.equal(renderer.root.findAllByType("a").some((anchor) => String(anchor.props.children).includes("Install or request GitHub app access")), true);
  renderer.unmount();

  const repositoryCalls = [];
  const statusRepositories = [];
  const repositoryProps = (repositories, delegated = statusRepositories, installationID = 7) => ({ status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onGitHub: (request) => repositoryCalls.push(request), github: { pending: false, result: { state: "ok", status: { connection_id: "c", state: "connected", repositories: delegated }, installations: { installations: [{ id: installationID, account: { id: 8, login: "factory-org" }, suspended_at: null, eligibility: "available" }], }, repositories } } });
  act(() => { renderer = create(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }], next_page: 2 }))); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Choose repositories").props.onClick(); });
  act(() => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }], next_page: 2 }))); });
  act(() => { renderer.root.findByProps({"aria-label":"GitHub settings"}).findAllByType("input").find((input) => input.props.type === "checkbox").props.onChange({ currentTarget: { checked: true } }); });
  await act(async () => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 102, full_name: "factory-org/two", permissions: { pull: true, push: true, maintain: true, admin: true } }] }))); });
  act(() => { renderer.root.findAllByType("button").find((button) => String(button.props.children).startsWith("Save access")).props.onClick(); });
  assert.deepEqual(repositoryCalls.find((request) => request.action === "delegate"), { action: "delegate", repositories: [{ installation_id: 7, repository_id: 101, repository: "factory-org/one" }] });
  renderer.unmount();

  const delegated = [{ installation_id: 7, repository_id: 101, repository: "factory-org/one" }];
  act(() => { renderer = create(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }] }, delegated))); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Choose repositories").props.onClick(); });
  act(() => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }] }, delegated))); });
  assert.equal(renderer.root.findByProps({"aria-label":"GitHub settings"}).findAllByType("input").find((input) => input.props.type === "checkbox").props.checked, true);
  act(() => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }] }, []))); });
  assert.equal(renderer.root.findByProps({"aria-label":"GitHub settings"}).findAllByType("input").find((input) => input.props.type === "checkbox").props.checked, false);
  act(() => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 101, full_name: "factory-org/one", permissions: { pull: true, push: true, maintain: true, admin: true } }] }, [], 8))); });
  act(() => { renderer.root.findAllByType("button").find((button) => button.props.children === "Choose repositories").props.onClick(); });
  assert.equal(renderer.root.findAllByProps({ className: "dfConsoleSidebar__list" }).length, 0);
  act(() => { renderer.update(createElement(FactoryConsole, repositoryProps({ repositories: [{ id: 202, full_name: "factory-org/two", permissions: { pull: true, push: true, maintain: true, admin: true } }] }, [], 8))); });
  assert.equal(renderer.root.findAllByProps({ className: "dfConsoleSidebar__list" }).length, 1);
  renderer.unmount();
});

test("settings edits project limits as future runs with an explicit unlimited choice", () => {
  const markup = render({ settingsOpen: true, onToggleSettings: () => {}, onSaveProjectLimits: () => {} });
  assert.match(markup, /aria-label="Project limits"/);
  assert.match(markup, /value="7"/);
  assert.match(markup, /Remaining run allowance/);
  assert.match(markup, /Unlimited runs/);
  assert.match(markup, /value="0"/);
  assert.match(markup, /Max seconds per run \(0 = unlimited\)/);
  assert.doesNotMatch(markup, /AUTONOMOUS GITHUB ISSUE WORK REQUIRES BOTH LIMITS/);
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
  assert.match(JSON.stringify(renderer.toJSON()), /Duration must be 0–86400 seconds/);
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
    renderer.root.findAllByType("button").find((button) => button.props["aria-label"] === "Close").props.onClick();
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
    const props = { view, onSelectAgent: () => {}, onEditAppearance: () => {}, graphs: fixtureGraphs };
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
    assert.deepEqual(dialog.findAllByType("label").map((label) => label.findByType("span").children.join("")), ["Skin tone", "Hair style", "Hair colour", "Face detail", "Clothing style", "Clothing colour", "Shoes", "Tool", "Headwear"]);
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
    assert.equal(dialog.findByProps({ role: "alert" }).children.join(""), "Someone else changed this. Reopen it and try again.");
    assert.equal(dialog.findAllByType("select")[0].props.value, 3);
    await act(async () => { renderer.unmount(); });
  } finally { globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct; }
});

test("Factory and Agents are explicit left-side alternatives", () => {
  const floor = render();
  assert.match(floor, /aria-label="Left view"/);
  assert.match(floor, /aria-pressed="true" disabled="">Floor/);
  assert.match(render({ view: "agents" }), /aria-label="Agents"/);
  // The top bar keeps the wordmark, the counters, and SETTINGS.
  const actions = floor.split('class="dfConsoleBar__actions"')[1];
  assert.match(actions.slice(0, actions.indexOf("</div>")), /aria-label="Settings"/);
  assert.match(floor, /<h2[^>]*>Factory floor<\/h2>/);
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
    status: "ready", state: baseState(), view: "agents", selectedHumanRequest: selectedRequest(),
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

test("the view toggle and settings forward exactly one intent each", () => {
  const calls = [];
  const elements = consoleElements({
    status: "ready",
    state: baseState(),
    onView: (value) => calls.push(["view", value]),
    onToggleSettings: () => calls.push(["settings"]),
  });
  const chrome = elements.filter((element) => element.type === "button" && ["Settings", "Floor", "Agents"].includes(element.props["aria-label"] ?? element.props.children));
  assert.deepEqual(chrome.map((element) => element.props["aria-label"] ?? element.props.children), ["Settings", "Floor", "Agents"]);
  chrome[0].props.onClick();
  chrome.find((element) => element.props.children === "Agents").props.onClick();
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
    if (["WorkPanel", "FactoryFloor", "ProjectLibrary"].includes(node.type.name)) return result;
    expand(node.type(node.props), result);
    return result;
  }
  result.push(node);
  expand(node.props.children, result);
  return result;
}

test("Pair a phone appears in settings only with authority, and shows the minted code", () => {
  const svg = '<svg viewBox="0 0 1 1"/>';
  const link = "https://app.darkfactory.build/remote#df_remote&node=n0&expires=1767225600";
  const settings = { settingsOpen: true, onToggleSettings: () => {} };
  // Pairing lives in the settings sidebar, which is the only place it shows.
  assert.equal(render({ ...settings }).includes("Pair a phone"), false);
  assert.match(render({ ...settings, remoteInviteAllowed: true }), /Pair a phone/);
  assert.equal(render({ remoteInviteAllowed: true }).includes("Pair a phone"), false, "not without settings");
  assert.match(render({ ...settings, remoteInviteAllowed: true, selectedAgent: agentSelection() }), /Pair a phone/, "the modal is over the sidebar, not behind it");
  // Its slot still takes an explicit override.
  assert.match(render({ ...settings, remoteInviteAllowed: true, pairing: createElement("p", null, "OTHER") }), /OTHER/);

  const shown = render({ ...settings, remoteInviteAllowed: true, remoteInvite: { link, svg, expiresAtMs: 1767225600000n } });
  assert.ok(shown.includes(`src="data:image/svg+xml;utf8,${encodeURIComponent(svg)}"`), shown);
  assert.ok(shown.includes(link.replaceAll("&", "&amp;")));
  assert.match(shown, /Dismiss/);
  // The minted code is never handed to the browser as markup. The floor draws
  // its own SVG, so the check names the invite's exact bytes.
  assert.equal(shown.includes(svg), false);

  const failed = render({ ...settings, remoteInviteAllowed: true, remoteInviteError: "not_found" });
  assert.match(failed, /No pairing code — not found/);
  assert.match(failed, /Dismiss/);
  assert.equal(render({ ...settings, remoteInviteAllowed: true }).includes("Dismiss"), false);
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

test("archived workers stay selectable from the archived view and expose only restore", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const worker = { ...fixtureState.agents.get(ids.agent), archived: true, paused: true };
  const state = baseState({ agents: new Map([[worker.id, worker]]) });
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state, view: "agents", onSelectAgent() {} })); });
    assert.equal(renderer.root.findAllByType("button").some((button) => String(button.props.className).includes("dfAgentList__row")), false);
    const archived = renderer.root.findByType("input");
    await act(async () => { archived.props.onChange({ currentTarget: { checked: true } }); });
    assert.equal(renderer.root.findAllByType("button").some((button) => String(button.props.className).includes("dfAgentList__row")), true);
    await act(async () => { renderer.update(createElement(FactoryConsole, { status: "ready", state, view: "agents", selectedAgent: { id: worker.id, name: worker.name, revision: worker.revision }, onSaveAgentConfig() {} })); });
    const text = JSON.stringify(renderer.toJSON());
    assert.match(text, /archived/);
    assert.match(text, /Restore paused/);
    assert.equal(text.includes("Archive worker"), false);
    assert.equal(text.includes("TERMINAL"), false);
    assert.equal(text.includes("Queue paused"), false);
    assert.equal(renderer.root.findByProps({ className: "dfAgentSpriteEdit" }).props.disabled, true);
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
      unavailable_reason: "",
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
    const section = renderer.root.findAllByProps({ "aria-label": "Accounts" })[0];
    assert.ok(section !== undefined);
    assert.equal(JSON.stringify(renderer.toJSON()).includes(login.home), false, "account paths stay out of the common flow");
    assert.equal(JSON.stringify(renderer.toJSON()).includes(login.default_model), false, "model details stay out of account linking");
    const label = renderer.root.findAllByType("input").find((input) => input.props.id === `df-account-label-${login.provider}-0`);
    await act(async () => { label.props.onChange({ currentTarget: { value: "dogfood" } }); });
    const link = renderer.root.findAllByType("button").find((button) => button.props.children === "Link");
    await act(async () => { link.props.onClick(); });
    assert.deepEqual(linkings, [[login.home, "dogfood"]]);
    const account = [...props.state.accounts.values()][0];
    const linkedLabel = renderer.root.findAllByType("input").find((input) => input.props.id === `df-linked-account-${account.id}`);
    await act(async () => { linkedLabel.props.onChange({ currentTarget: { value: "personal" } }); });
    const button = (name) => renderer.root.findAllByType("button").find((item) => textOf(item) === name);
    await act(async () => { button("Save label").props.onClick(); });
    assert.deepEqual(updates, [[account.id, account.revision, { label: "personal" }]]);
    assert.equal(button("Unlink").props.disabled, true, "an agent still references this account");
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, state: { ...props.state, agents: new Map() } })); });
    assert.equal(button("Unlink").props.disabled, false);
    await act(async () => { button("Unlink").props.onClick(); });
    assert.notDeepEqual(updates.at(-1)?.[2], { remove: true }, "unlinking asks for confirmation first");
    await act(async () => { button("Confirm unlink").props.onClick(); });
    assert.deepEqual(updates.at(-1), [account.id, account.revision, { remove: true }]);
    await act(async () => { button("Refresh accounts").props.onClick(); });
    assert.equal(asked.length, 2);


    // The daemon's refusal is shown plainly rather than retried.
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, accountsError: "not_found" })); });
    assert.ok(renderer.root.findAllByProps({ role: "alert" }).length > 0);
    await act(async () => { renderer.unmount(); });
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("settings rereads private GitHub state when the browser reconnects", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    const calls = [];
    const props = { status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onGitHub: (request) => calls.push(request) };
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
    assert.deepEqual(calls, [{ action: "status" }]);
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, status: "connecting" })); });
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, status: "ready" })); });
    assert.deepEqual(calls, [{ action: "status" }, { action: "status" }]);
    renderer.unmount();
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("settings keeps an unavailable linked account visible with recovery guidance", () => {
  const account = [...baseState().accounts.values()][0];
  const markup = render({ settingsOpen: true, accounts: [{
    provider: account.provider,
    home: account.home,
    label: account.label,
    email: "",
    organization: "",
    default_model: "",
    default_reasoning_effort: "",
    linked_id: account.id,
    unavailable_reason: "login is no longer discoverable",
  }] });
  assert.match(markup, /Account unavailable · login is no longer discoverable\./);
  assert.ok(markup.includes(`Sign in again using <code>${account.home}</code>, then refresh.`));
});

test("settings explains the next step when discovery finds no provider logins", () => {
  const state = { ...baseState(), accounts: new Map() };
  const guidance = "Sign in with your provider CLI on this Mac, then refresh.";
  assert.ok(render({ settingsOpen: true, state, accounts: [] }).includes(guidance));
  assert.equal(render({ settingsOpen: true, state }).includes(guidance), false, "no discovery result is not an empty result");
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
  assert.equal(field(`df-idle-budget-${ids.agent}`), undefined);
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
  assert.equal(spentField(`df-idle-budget-${ids.agent}`), undefined);
});

test("overseer supervision names worker events and a seconds cooldown", () => {
  const agent = { ...fixtureState.agents.get(ids.agent), role: "orchestrator", idle_policy: "standing_instruction", idle_after_seconds: 10, idle_instruction: "Inspect worker activity.", idle_run_budget: 1 };
  const agents = new Map(fixtureState.agents);
  agents.set(agent.id, agent);
  const markup = render({ state: baseState({ agents }), selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision }, onSaveAgentConfig: () => {} });
  for (const text of ["Supervision", "When work changes", "supervise worker activity", "Cooldown seconds", "initial inspection, then worker events"]) assert.match(markup, new RegExp(text));
  assert.match(markup, new RegExp(`id="df-idle-after-${agent.id}"[^>]*value="10"`));
});

test("Paired devices lists what the factory granted and revokes any device but this one", async () => {
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
  assert.equal(label(rows[0]), "Browser · This browser");
  assert.equal(label(rows[1]), "Phone");
  const buttons = () => renderer.root.findAllByType("button").filter((button) => ["Revoke", "Confirm revoke", "Keep"].includes(button.props.children));
  assert.equal(buttons().length, 1, "only the phone can be revoked, never this console");
  await act(async () => { buttons()[0].props.onClick(); });
  assert.deepEqual(buttons().map((button) => button.props.children), ["Confirm revoke", "Keep"]);
  await act(async () => { buttons().find((button) => button.props.children === "Keep").props.onClick(); });
  assert.deepEqual(revoked, [], "Keep revokes nothing");
  await act(async () => { buttons()[0].props.onClick(); });
  await act(async () => { buttons().find((button) => button.props.children === "Confirm revoke").props.onClick(); });
  assert.deepEqual(revoked, [{ clientId: phone, expectedRevision: 3n }]);

  const empty = render({ settingsOpen: true, onToggleSettings: () => {}, remoteInviteAllowed: true, devices: { clients: [], more: false } });
  assert.match(empty, /Paired devices/);
  assert.match(empty, /nothing paired/);
  assert.match(render({ settingsOpen: true, onToggleSettings: () => {}, remoteInviteAllowed: true, devicesError: "unauthorized" }), /Devices — unauthorized/);
});

test("floor objects select the exact existing task detail and question route", async () => {
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
  await act(async () => { tree.root.findByProps({ "data-workbench-task-id": task.id }).props.onKeyDown({ key: "Enter", preventDefault() {} }); });
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
  await act(async () => { tree.root.findByProps({ "data-workbench-task-id": task.id }).props.onKeyDown({ key: "Enter", preventDefault() {} }); });
  // The selected running task is retried: a queued, editable task has its inline editor, not a dialog.
  const requeued = new Map(fixtureState.tasks).set(task.id, { ...task, status: "queued" });
  await act(async () => { tree.update(createElement(Harness, { editable: true, state: { ...fixtureState, tasks: requeued } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "editable queued work stays in its inline editor");
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  await act(async () => { tree.root.findByProps({ "aria-label": "Task details" }).props.onClose(); });
  // Expanding a queued row selects nothing, so its start cannot open a dialog unasked.
  const queuedRow = tree.root.findAllByProps({ className: "dfConsoleItem dfWorkRow" }).find((row) => rowTitle(row) === queuedTask.title);
  await act(async () => { rowButton(queuedRow).props.onClick(); });
  const started = new Map(fixtureState.tasks).set(queuedTask.id, { ...queuedTask, status: "running" });
  await act(async () => { tree.update(createElement(Harness, { editable: true, state: { ...fixtureState, tasks: started } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "a queued task that starts running opens no dialog");
  await act(async () => { tree.update(createElement(Harness, { editable: true })); });
  await act(async () => { tree.root.findByProps({ "data-human-request-id": ids.request }).props.onClick(); });
  assert.equal(questions[0], fixtureState.humanRequests.get(ids.request));
  assert.equal(tree.root.findByProps({ "aria-label": "Selected question" }).findByProps({ className: "dfFactoryConsole__question" }).children.join(""), "Should the migration also cover the users table? The plan only names accounts.");
  await act(async () => { tree.root.findByProps({ "data-workbench-task-id": task.id }).props.onKeyDown({ key: "Enter", preventDefault() {} }); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 1);
  const tasks = new Map(fixtureState.tasks);
  tasks.delete(task.id);
  await act(async () => { tree.update(createElement(Harness, { state: { ...fixtureState, tasks } })); });
  assert.equal(tree.root.findAllByProps({ "aria-label": "Task details" }).length, 0, "removed work is not retained as invented history");
  await act(async () => tree.unmount());
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


test("mobile Floor and Agents tabs track both directions and restore after Work", async () => {
  let tree;
  function Harness() {
    const [view, setView] = useState("agents");
    const [detail, setDetail] = useState("work");
    return createElement(FactoryConsole, { status: "ready", state: fixtureState, view, onView: setView, detail, onDetail: setDetail });
  }
  await act(async () => { tree = create(createElement(Harness)); });
  assert.equal(tree.root.findByType(FactoryConsole).props.view, "agents");
  const nav = tree.root.findByProps({ "aria-label": "Console views" });
  await act(async () => { nav.findAllByType("button")[0].props.onClick(); });
  assert.equal(tree.root.findByType(FactoryConsole).props.view, "floor");
  assert.equal(tree.root.findByType(FactoryConsole).props.detail, "floor");
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "floor");
  await act(async () => { nav.findAllByType("button")[1].props.onClick(); });
  assert.equal(tree.root.findByType(FactoryConsole).props.view, "agents");
  assert.equal(nav.findAllByType("button")[0].props["aria-pressed"], false);
  assert.equal(nav.findAllByType("button")[1].props["aria-pressed"], true);
  await act(async () => { nav.findAllByType("button")[2].props.onClick(); });
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "detail");
  await act(async () => { nav.findAllByType("button")[1].props.onClick(); });
  assert.equal(tree.root.findByType("main").props["data-mobile-view"], "floor");
  assert.equal(nav.findAllByType("button")[1].props["aria-pressed"], true);
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
  assert.equal(scene().props.graph.halls.flatMap((hall) => hall.machines).find((machine) => machine.label === "/browser").reading.ratePerHour, 999);
  assert.equal(layout(), originalLayout, "readings never move a machine");
  // A new digest is a new structure.
  graphs = new Map(graphs).set(ids.project, { ...fixtureGraph, digest: "cd".repeat(32), nodes: fixtureGraph.nodes.filter((node) => node.label !== "web"), edges: [] });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh, connected: false })); });
  await act(async () => { renderer.update(createElement(DraftFloor, { state: fresh })); });
  assert.deepEqual(scene().props.graph.halls.map((hall) => hall.label), ["kernel"]);
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






test("Repositories reads only the selected project when opened", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const props = () => ({ status: "ready", state: baseState(), settingsOpen: true,
    onToggleSettings: () => {}, onLoadRepositories: (id) => calls.push(id) });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props())); });
    assert.deepEqual(calls, [], "collapsed settings must not read private project data");
    const section = renderer.root.findByProps({ "aria-label": "Repositories" });
    const element = { open: true };
    await act(async () => section.props.onToggle({ target: element, currentTarget: element }));
    assert.deepEqual(calls, [ids.project]);
    await act(async () => renderer.update(createElement(FactoryConsole, props())));
    assert.deepEqual(calls, [ids.project], "response renders must not trigger another read");
    const select = section.findAllByType("select")[0];
    const secondProject = [...fixtureState.projects.keys()].find((id) => id !== ids.project);
    await act(async () => select.props.onChange({ currentTarget: { value: secondProject } }));
    assert.deepEqual(calls, [ids.project, secondProject]);
    element.open = false;
    await act(async () => section.props.onToggle({ target: element, currentTarget: element }));
    await act(async () => select.props.onChange({ currentTarget: { value: ids.project } }));
    assert.deepEqual(calls, [ids.project, secondProject], "a closed section must not load");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("Sources follow the header project and read only on the Connections tab", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const props = (extra = {}) => ({ status: "ready", state: baseState(), settingsOpen: true, onToggleSettings: () => {}, onLoadIntake: (id) => calls.push(id), ...extra });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props())); });
    assert.equal(renderer.root.findByProps({ "aria-label": "Sources" }).findAllByType("select").length, 0, "no project picker of its own");
    assert.match(JSON.stringify(renderer.toJSON()), /Choose a project in the header/);
    assert.deepEqual(calls.filter((id) => id !== ids.project), [], "nothing else is read");
    await act(async () => renderer.root.findAll((node) => node.type === "button" && node.props.role === "tab" && node.props.children === "Connections")[0].props.onClick());
    await act(async () => renderer.update(createElement(FactoryConsole, props({ state: oneProjectState() }))));
    assert.ok(calls.includes(ids.project), "the header project is read once Connections is shown");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("the Tasks panel names its sources and Manage opens Settings at Connections", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  let toggled = 0;
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "factory:ready", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: true, revision: 1n };
  const props = (extra = {}) => ({ status: "ready", state: oneProjectState(), onToggleSettings: () => { toggled++; }, intake: new Map([[ids.project, { state: "ok", sources: [source] }]]), ...extra });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props())); });
    const line = () => renderer.root.findByProps({ "aria-label": "Task sources" });
    assert.match(line().children.join(""), /Sources: example\/widgets · label factory:ready/);
    await act(async () => line().findByType("button").props.onClick());
    assert.equal(toggled, 1);
    await act(async () => renderer.update(createElement(FactoryConsole, props({ settingsOpen: true }))));
    assert.equal(renderer.root.findAll((node) => node.type === "button" && node.props.role === "tab" && node.props["aria-selected"] === true)[0].props.children, "Connections");
    await act(async () => renderer.update(createElement(FactoryConsole, props({ intake: new Map([[ids.project, { state: "ok", sources: [] }]]) }))));
    assert.match(line().children.join(""), /No sources/);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("Work shows issues waiting for approval as Needs you inbox rows with Accept", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const waiting = { number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Fix parser", body: "", author: "reporter", labels: ["bug"], content_hash: "ab".repeat(32), reason: "untrusted_author" };
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "bug", policy: "trusted_authors", trusted_authors: ["owner"], poll_seconds: 60, admission_limit: 25, enabled: true, revision: 3n, sync: { last_attempt_at: 1n, last_success_at: 1n, imported_tasks: 0, state: "ok", error: "", waiting: [waiting] } };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: oneProjectState(), intake: new Map([[ids.project, { state: "ok", sources: [source] }]]), onIntakeAction: (projectId, request) => calls.push({ projectId, request }) })); });
    const work = renderer.root.findByProps({ "aria-label": "Work" });
    await act(async () => work.findAllByType("button").find((button) => textOf(button).startsWith("Needs you")).props.onClick());
    const text = (node) => typeof node === "string" ? node : node.children.map(text).join(" ");
    const row = work.findAll((node) => node.type === "li" && text(node).includes("Fix parser"))[0];
    assert.match(text(row), /inbox/);
    assert.match(text(row), /#17/);
    await act(async () => row.findByProps({ "aria-label": "Accept Fix parser" }).props.onClick());
    assert.deepEqual(calls, [{ projectId: ids.project, request: { action: "accept", source_id: source.id, expected_revision: 3n, issue_number: 17n, content_hash: "ab".repeat(32) } }]);
    let toggled = 0;
    const props = renderer.root.findByType(FactoryConsole).props;
    await act(async () => renderer.update(createElement(FactoryConsole, { ...props, onToggleSettings: () => { toggled++; }, intake: new Map([[ids.project, { state: "content_changed", sources: [source] }]]) })));
    const alert = renderer.root.findByProps({ role: "alert" });
    assert.match(text(alert), /Could not accept:\s+content changed/);
    await act(async () => alert.findByType("button").props.onClick());
    assert.equal(toggled, 1, "a stale issue routes to Sources for fresh content");
    await act(async () => renderer.update(createElement(FactoryConsole, { ...props, intakePending: new Set([ids.project]) })));
    assert.equal(renderer.root.findByProps({ "aria-label": "Accept Fix parser" }).props.disabled, true, "a pending accept cannot be resubmitted");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("open Work re-reads the local intake list so issues polled later reach the inbox", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const loads = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: true, revision: 3n };
  const props = (sources) => ({ status: "ready", state: oneProjectState(), intake: new Map([[ids.project, { state: "ok", sources }]]), onLoadIntake: (id) => loads.push(id), onIntakeAction: () => {} });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props([source]))); });
    assert.deepEqual(loads, [ids.project]);
    assert.equal(renderer.root.findAll((node) => node.props["aria-label"] === "Accept Fix parser").length, 0);
    await act(async () => t.mock.timers.tick(30_000));
    assert.deepEqual(loads, [ids.project, ids.project], "Work refreshes the local list without a project change");
    const waiting = { number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Fix parser", body: "", author: "reporter", labels: [], content_hash: "ab".repeat(32), reason: "needs_manual_acceptance" };
    await act(async () => renderer.update(createElement(FactoryConsole, props([{ ...source, sync: { last_attempt_at: 2n, last_success_at: 2n, imported_tasks: 0, state: "ok", error: "", waiting: [waiting] } }]))));
    assert.equal(renderer.root.findAll((node) => node.props["aria-label"] === "Accept Fix parser").length, 1);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("issue review refuses a stale preview before acceptance", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: ids.orchestrator, label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: false, revision: 3n };
  const review = { source_id: source.id, state: "ok", sources: [source], candidates: [{ number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Fix parser", body: "Keep this exact reviewed body.", author: "reporter", labels: ["bug"], content_hash: "ab".repeat(32), reason: "needs_manual_acceptance" }], reviewed_revision: 2n, next_page: 2 };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, intake: new Map([[ids.project, review]]), onIntakeAction: (projectId, request) => calls.push({ projectId, request }) })); });
    const preview = renderer.root.findByProps({ "aria-label": "Sources" }).findAll((node) => node.type === "button" && textOf(node) === "Refresh")[0];
    await act(async () => preview.props.onClick());
    assert.deepEqual(calls, [{ projectId: ids.project, request: { action: "preview", source_id: source.id, page: 1 } }]);
    assert.equal(renderer.root.findAll((node) => node.type === "button" && node.props.children === "Accept").length, 0);
    assert.equal(renderer.root.findAll((node) => node.type === "button" && node.props.children === "More issues").length, 0);
    const sameRevisionWrongSource = {...review,reviewed_revision:source.revision,source_id:"99".repeat(16)};
    await act(async()=>renderer.update(createElement(FactoryConsole,{...renderer.root.findByType(FactoryConsole).props,intake:new Map([[ids.project,sameRevisionWrongSource]])})));
    assert.equal(renderer.root.findAll((node)=>node.type==="button" && node.props.children==="Accept").length,0,"another source with the same revision is not this review");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});


test("private issue review shows concise state and linked work", () => {
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: false, revision: 3n };
  const review = { source_id: source.id, reviewed_revision: source.revision, state: "withdrawal_pending", task_id: ids.task, imported_tasks: ["87".repeat(16)], sources: [source], candidates: [{ number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Fix parser", body: "Keep this exact reviewed body.", author: "reporter", labels: ["bug"], content_hash: "ab".repeat(32), reason: "withdrawal_pending", acceptance_id: "86".repeat(16), task_id: ids.task }] };
  const markup = render({ state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, intake: new Map([[ids.project, review]]) });
  assert.match(markup, /withdrawal pending/);
  assert.match(markup, /View task/);
  assert.match(markup, /Details/);
  assert.match(markup, /Add source/);
  assert.doesNotMatch(markup, /Backlog|Issue inbox/);
  assert.doesNotMatch(markup, />Withdraw</);
  assert.doesNotMatch(markup, /87878787878787878787878787878787/);
  assert.doesNotMatch(markup, /private\/tmp|\/Users\//);
});

test("issue source configuration uses private checkout and overseer names", () => {
  const checkout = { id: "89".repeat(16), project_id: ids.project, name: "Primary checkout", root: "/private/source", base_ref: "HEAD", enabled: true, default: true, revision: 1n };
  const markup = render({ state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, repositories: new Map([[ids.project, [checkout]]]) });
  assert.match(markup, /Code repository/);
  assert.match(markup, /Primary checkout/);
  assert.match(markup, /Allow trusted authors/);
  assert.doesNotMatch(markup, /Destination repository ID/);
});


test("editing an intake filter preserves migrated priority rules", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: ids.orchestrator, label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: false, revision: 3n, priority_default: 2, priority_by_label: { urgent: 10 } };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, intake: new Map([[ids.project, { state: "ok", sources: [source] }]]), onIntakeAction: (projectId, request) => calls.push(request) })); });
    const form = renderer.root.findByProps({ "aria-label": "Sources" }).findAllByType("form").find((form) => form.findAllByType("button").some((button) => button.props.children === "Save"));
    await act(async () => form.findAllByType("input").find((input) => input.props.value === "bug").props.onChange({ currentTarget: { value: "enhancement" } }));
    await act(async () => form.props.onSubmit({ preventDefault() {} }));
    assert.equal(calls.length, 1);
    assert.equal(calls[0].configuration.label, "enhancement");
    assert.equal(calls[0].configuration.priority_default, 2);
    assert.deepEqual(calls[0].configuration.priority_by_label, { urgent: 10 });
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});


test("revised issue content can be accepted without discarding the prior receipt", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: ids.orchestrator, label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: false, revision: 3n };
  const candidate = { number: 17n, url: "https://github.com/example/widgets/issues/17", title: "Revised parser instructions", body: "The operator must review this new content.", author: "reporter", labels: ["bug"], content_hash: "cd".repeat(32), reason: "content_changed", acceptance_id: "86".repeat(16), task_id: ids.task };
  const review = { source_id: source.id, state: "ok", sources: [source], candidates: [candidate], reviewed_revision: 3n };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, intake: new Map([[ids.project, review]]), onIntakeAction: (projectId, request) => calls.push(request) })); });
    await act(async () => renderer.root.findByProps({ "aria-label": "Sources" }).findAll((node) => node.type === "button" && textOf(node) === "Refresh")[0].props.onClick());
    const content = renderer.root.findAllByType("details").find((node) => node.findAllByType("summary").some((summary) => summary.props.children === "Details") && node.findAllByType("details").length === 1);
    assert.equal(content.props.open, undefined, "issue bodies are collapsed until selected");
    assert.equal(content.findAll((node) => node.type === "button" && node.props.children === "Accept").length, 1, "acceptance stays inside content review");
    await act(async () => content.findAll((node) => node.type === "button" && node.props.children === "Accept")[0].props.onClick());
    assert.deepEqual(calls.at(-1), { action: "accept", source_id: source.id, expected_revision: 3n, issue_number: 17n, content_hash: candidate.content_hash });
    await act(async () => renderer.root.findAll((node) => node.type === "button" && node.props.children === "Withdraw")[0].props.onClick());
    assert.deepEqual(calls.at(-1), { action: "withdraw", acceptance_id: candidate.acceptance_id });
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});


test("a rejected create keeps the add form and its typed values", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: "", label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: true, revision: 1n };
  const checkout = { id: "89".repeat(16), project_id: ids.project, name: "Widgets", root: "/private/source", base_ref: "main", enabled: true, default: true, revision: 1n };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, repositories: new Map([[ids.project, [checkout]]]), github: { result: { state: "ok", status: { repositories: [{ repository: "example/issues", repository_id: 42n }] } } }, intake: new Map([[ids.project, { state: "ok", sources: [source] }]]), onIntakeAction: (projectId, request) => calls.push(request) })); });
    const sources = () => renderer.root.findByProps({ "aria-label": "Sources" });
    await act(async () => sources().findAll((node) => node.type === "button" && textOf(node) === "Add source")[0].props.onClick());
    const form = () => sources().findAllByType("form")[0];
    await act(async () => form().findAllByType("input")[0].props.onChange({ currentTarget: { value: "typed-label" } }));
    await act(async () => form().props.onSubmit({ preventDefault() {} }));
    assert.equal(calls.at(-1).action, "create");
    assert.equal(form().findAllByType("input")[0].props.value, "typed-label");
    assert.ok(form().findAll((node) => node.type === "button" && textOf(node) === "Add source").length === 1, "still the add form, not the edit form");
    assert.doesNotMatch(JSON.stringify(sources().findAll((node) => node.type === "p").map((node) => node.children)), /No sources yet/);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});


test("refreshing a changed source replaces stale configuration drafts", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const source = { id: "88".repeat(16), project_id: ids.project, github_repository_id: 42n, repository: "example/widgets", target_repository_id: "89".repeat(16), overseer_agent_id: ids.orchestrator, label: "bug", policy: "manual", trusted_authors: [], poll_seconds: 60, admission_limit: 25, enabled: false, revision: 3n };
  const props = (item) => ({ status: "ready", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, intake: new Map([[ids.project, { state: "ok", sources: [item] }]]), onIntakeAction: (projectId, request) => calls.push(request) });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props(source))); });
    const changed = { ...source, revision: 4n, target_repository_id: "90".repeat(16), label: "enhancement", policy: "trusted_authors", trusted_authors: ["reviewer"] };
    await act(async () => renderer.update(createElement(FactoryConsole, props(changed))));
    const form = renderer.root.findByProps({ "aria-label": "Sources" }).findAllByType("form").find((form) => form.findAllByType("button").some((button) => button.props.children === "Save"));
    await act(async () => form.props.onSubmit({ preventDefault() {} }));
    assert.equal(calls.length, 1);
    assert.equal(calls[0].expected_revision, 4n);
    assert.equal(calls[0].configuration.target_repository_id, changed.target_repository_id);
    assert.equal(calls[0].configuration.label, "enhancement");
    assert.equal(calls[0].configuration.policy, "trusted_authors");
    assert.deepEqual(calls[0].configuration.trusted_authors, ["reviewer"]);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});


test("new issue sources select the sole overseer and default checkout without operator IDs", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const calls = [];
  const checkout = { id: "89".repeat(16), project_id: ids.project, name: "Widgets", root: "/private/source", base_ref: "release", enabled: true, default: true, revision: 1n };
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, { status: "ready", state: baseState({ projects: oneProjectState().projects, agents: new Map([[ids.orchestrator, { ...fixtureState.agents.get(ids.orchestrator), project_id: ids.project }]]) }), settingsOpen: true, onToggleSettings: () => {}, repositories: new Map([[ids.project, [checkout]]]), github: {result:{state:"ok",status:{repositories:[{repository:"example/issues",repository_id:42n}]}}}, onIntakeAction: (projectId, request) => calls.push(request) })); });
    const form = renderer.root.findAllByType("form").find((form) => form.findAllByType("button").some((button) => textOf(button) === "Add source"));
    const policy = form.findAllByType("select").find((select) => select.props.value === "manual");
    await act(async () => policy.props.onChange({ currentTarget: { value: "trusted_authors" } }));
    await act(async () => form.props.onSubmit({ preventDefault() {} }));
    assert.equal(calls[0].configuration.overseer_agent_id, ids.orchestrator);
    assert.equal(calls[0].configuration.target_repository_id, checkout.id);
    assert.deepEqual(calls[0].configuration.trusted_authors, ["@me"]);
    assert.equal(calls[0].configuration.poll_seconds, 60);
    assert.equal(calls[0].configuration.admission_limit, 25);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("open Sources reload destinations as well as sources after reconnect", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  const sources = [], repositories = [];
  const props = (ready) => ({ status: ready ? "ready" : "disconnected", state: oneProjectState(), settingsOpen: true, onToggleSettings: () => {}, repositories: new Map(), intake: new Map(), onLoadIntake: ready ? (id) => sources.push(id) : undefined, onLoadRepositories: ready ? (id) => repositories.push(id) : undefined });
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, props(true))); });
    assert.deepEqual(repositories, [], "Sources stay unread while another tab is showing");
    await act(async () => renderer.root.findAll((node) => node.type === "button" && node.props.role === "tab" && node.props.children === "Connections")[0].props.onClick());
    assert.deepEqual(repositories, [ids.project]);
    const before = sources.length;
    await act(async () => renderer.update(createElement(FactoryConsole, props(false))));
    await act(async () => renderer.update(createElement(FactoryConsole, props(true))));
    assert.deepEqual(repositories, [ids.project, ids.project]);
    assert.ok(sources.length > before);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous;
  }
});

test("one project selector scopes floor, agents, tasks and project limits across view changes", async () => {
  let tree;
  const graphs = new Map(fixtureGraphs).set(ids.secondProject, graphWith([unit(0x71, "south", ["south"])]));
  const props = { status: "ready", state: fixtureState, graphs, selectedAgent: agentSelection(), onSelectAgent() {}, onSelectTask() {} };
  await act(async () => { tree = create(createElement(FactoryConsole, props)); });
  const choose = async (value) => act(async () => tree.root.findByProps({ "aria-label": "Project" }).props.onChange({ currentTarget: { value } }));
  const floor = () => tree.root.findByType(FactoryFloor);
  await choose(ids.project);
  assert.equal(floor().props.projectId, ids.project);
  assert.equal(floor().props.state.projects.size, 1);
  assert.ok([...floor().props.state.agents.values()].every((agent) => agent.project_id === ids.project));
  assert.ok([...floor().props.state.tasks.values()].every((task) => task.project_id === ids.project));
  const halls = () => floor().findByType(FactoryScene).props.graph.halls.map((hall) => hall.label).sort();
  assert.deepEqual(halls(), ["kernel", "web"], "only the selected project's halls");
  await act(async () => { tree.update(createElement(FactoryConsole, { ...props, view: "agents", settingsOpen: true })); });
  const rows = tree.root.findAllByProps({ className: "dfConsoleRow dfAgentList__row" });
  assert.equal(rows.length, [...fixtureState.agents.values()].filter((agent) => agent.project_id === ids.project && !agent.archived).length);
  assert.ok(rows.every((row) => row.props["aria-label"].includes(fixtureState.projects.get(ids.project).name)));
  const limits = tree.root.findByProps({ "aria-label": "Project limits" });
  assert.ok(JSON.stringify(limits.findAllByType("h4").map((heading) => heading.children)).includes(fixtureState.projects.get(ids.project).name));
  assert.ok(!JSON.stringify(limits.findAllByType("h4").map((heading) => heading.children)).includes(fixtureState.projects.get(ids.secondProject).name));
  await act(async () => { tree.update(createElement(FactoryConsole, props)); });
  assert.equal(floor().props.projectId, ids.project, "switching views retains project scope");
  await choose("");
  assert.equal(floor().props.state.agents.size, fixtureState.agents.size);
  assert.deepEqual(halls(), ["kernel", "south", "web"]);
  await choose(ids.project);
  await choose(ids.secondProject);
  assert.equal(floor().props.projectId, ids.secondProject);
  assert.equal(tree.root.findAllByProps({ "aria-label": `Agent ${fixtureState.agents.get(ids.agent).name}` }).length, 0, "a foreign selected agent cannot retain controls");
  assert.deepEqual(halls(), ["south"]);
  assert.ok([...floor().props.state.tasks.values()].every((task) => task.project_id === ids.secondProject));
  await act(async () => tree.unmount());
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

test("settings reports the running version and the update command without any production record", () => {
  const settings = (props) => renderToStaticMarkup(createElement(SettingsDialog, {
    floorAppearance: DEFAULT_FLOOR_APPEARANCE, onFloorAppearanceChange() {}, onResetFloorAppearance() {},
    state: undefined, ready: false, ...props,
  }));
  const markup = settings({ runtime: { version: "v0.4.2", source: "a".repeat(40), target: "darwin/arm64", build_id: "build", release: true } });
  assert.match(markup, /Running version <code>v0\.4\.2<\/code>/);
  assert.match(markup, /Latest release not observed/);
  assert.match(markup, /factoryctl service uninstall --home/);
  assert.match(markup, /factoryctl service install --home/);
  assert.doesNotMatch(markup, /deploy-runtime\.py|up to date/i);
  const published = settings({ release: { version: "v0.4.3", url: "https://github.com/dark-factory-build/dark-factory/releases/tag/v0.4.3" } });
  assert.match(published, /Running version <code>not observed<\/code>/);
  assert.match(published, /releases\/tag\/v0\.4\.3"[^>]*><code>v0\.4\.3<\/code>/);
});

test("settings tabs hide other sections, support keyboard navigation and retain drafts", async () => {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryConsole, {status:"ready",state:baseState(),settingsOpen:true,onToggleSettings(){},onCreateProject(){}})); });
    const tabs = () => renderer.root.findAllByProps({role:"tab"});
    const panels = () => renderer.root.findAllByProps({role:"tabpanel"});
    assert.deepEqual(tabs().map(tab=>tab.props.children),["Projects","Connections","Devices","Appearance","Updates"]);
    const selected = (index) => {
      assert.deepEqual(tabs().map(tab=>tab.props["aria-selected"]),tabs().map((_,i)=>i===index));
      assert.equal(panels().filter(panel=>!panel.props.hidden).length,1);
      assert.equal(panels()[index].props.hidden,false);
      for (let i=0;i<tabs().length;i++) {
        assert.equal(tabs()[i].props["aria-controls"],panels()[i].props.id);
        assert.equal(panels()[i].props["aria-labelledby"],tabs()[i].props.id);
        assert.equal(tabs()[i].props.tabIndex,i===index?0:-1);
      }
    };
    selected(0);
    const form=renderer.root.findAllByType("form").find(form=>form.findAllByType("button").some(button=>textOf(button)==="Create project"));
    await act(async()=>form.findAllByType("input")[0].props.onChange({currentTarget:{value:"Unfinished project"}}));
    await act(async()=>tabs()[1].props.onClick());
    selected(1);
    let focused=-1;
    const parentElement={querySelectorAll(){return tabs().map((_,i)=>({focus(){focused=i;}}));}};
    for (const [key,want] of [["End",4],["ArrowRight",0],["ArrowLeft",4],["Home",0]]) {
      let prevented=false;
      await act(async()=>tabs().find(tab=>tab.props["aria-selected"]).props.onKeyDown({key,preventDefault(){prevented=true;},currentTarget:{parentElement}}));
      assert.equal(prevented,true);assert.equal(focused,want);selected(want);
    }
    assert.equal(form.findAllByType("input")[0].props.value,"Unfinished project");
  } finally {
    if (renderer) await act(async()=>renderer.unmount());
    globalThis.IS_REACT_ACT_ENVIRONMENT=previous;
  }
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

test("Settings persists automatic attachment cleanup and keeps saved value on failure", async () => {
  let saved = false, fail = false, renderer;
  const calls = [];
  const props = { status: "ready", state: fixtureState, settingsOpen: true, onToggleSettings() {}, onAttachmentRetention: async (enabled) => { calls.push(enabled); if (fail) throw new Error("offline"); if (enabled !== undefined) saved = enabled; return saved; } };
  await act(async () => { renderer = create(createElement(FactoryConsole, props)); });
  const checkbox = () => renderer.root.findByProps({ "aria-label": "Attachment storage" }).findByType("input");
  try {
    assert.equal(checkbox().props.checked, false);
    await act(async () => { checkbox().props.onChange({ currentTarget: { checked: true } }); });
    assert.equal(saved, true);
    assert.equal(checkbox().props.checked, true);
    fail = true;
    await act(async () => { checkbox().props.onChange({ currentTarget: { checked: false } }); });
    assert.equal(checkbox().props.checked, true);
    assert.ok(renderer.root.findAllByProps({ role: "alert" }).some((node) => JSON.stringify(node.children).includes("Could not save attachment")));
    fail = false;
    await act(async () => { renderer.update(createElement(FactoryConsole, { ...props, settingsOpen: false })); });
    await act(async () => { renderer.update(createElement(FactoryConsole, props)); });
    assert.equal(checkbox().props.checked, true);
    assert.deepEqual(calls, [undefined, true, false, undefined]);
  } finally { await act(async () => renderer.unmount()); }
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

test("one machine inspector loads its evidence on open, investigates once and discusses its own node", async (t) => {
  const warnings = [];
  t.mock.method(console, "error", (...args) => warnings.push(args.join(" ")));
  const store = fixtureGraph.nodes.find((node) => node.label === "state.db"), loads = [], opens = [], tasks = [];
  const props = { state: fixtureState, graphs: fixtureGraphs, requestedEntity: { id: store.id }, floorAppearance: { ...DEFAULT_FLOOR_APPEARANCE, detail: "fine" },
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
  // No telemetry is worth a look: one task for the project's worker, queued behind nothing else.
  await click("Investigate");
  assert.equal(tasks.length, 1);
  assert.equal(tasks[0][0].project_id, ids.project);
  assert.equal(tasks[0][2], "any");
  assert.match(tasks[0][1], /Investigate the store "state\.db" in kernel/);
  assert.match(tasks[0][1], /Nothing observes this component/);
  assert.ok(renderer.root.findAllByType("button").some((button) => button.props.disabled && button.children.join("") === "Investigation queued"), "asking twice is not possible");
  // Another machine resets the inspector and fetches its own evidence.
  const other = fixtureGraph.nodes.find((node) => node.label === "web");
  await act(async () => renderer.update(createElement(FactoryFloor, { ...props, requestedEntity: { id: other.id } })));
  assert.equal(inspector().length, 1);
  assert.deepEqual(loads.at(-1), [ids.project, other.id]);
  assert.ok(renderer.root.findAllByType("button").some((button) => button.children.join("") === "Investigate"), "the old investigation does not follow the new machine");
  assert.ok(!warnings.some((message) => message.includes("same key")), "inspectors must have distinct reconciliation identities");
  await act(async () => renderer.unmount());
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
    await act(async () => tree.root.findByProps({ className: "dfFactoryEntityTools__results" }).findAllByType("button").find((button) => button.children.join("") === `${first.label} · ${first.kind}`).props.onClick());
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
