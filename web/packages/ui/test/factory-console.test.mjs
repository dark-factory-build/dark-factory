import assert from "node:assert/strict";
import test from "node:test";
import { createElement, isValidElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryApp, FactoryConsole } from "../dist/src/index.js";
import { TerminalSidebar } from "../dist/src/factory-app.js";
import { fixtureState } from "../../../fixtures/state.mjs";
import { fixtureConsoleExtras } from "../../../fixtures/console.mjs";

const ids = {
  project: [...fixtureState.projects.keys()][0],
  secondProject: [...fixtureState.projects.keys()][1],
  agent: [...fixtureState.agents.keys()][0],
  secondAgent: [...fixtureState.agents.keys()][1],
  task: [...fixtureState.tasks.keys()][0],
  request: [...fixtureState.humanRequests.keys()][0],
};

const baseState = (overrides = {}) => ({ ...fixtureState, ...overrides });

const render = (props = {}) => renderToStaticMarkup(createElement(FactoryConsole, {
  status: "ready",
  state: baseState(),
  ...props,
}));

const SCREENS = [
  { kind: "home" },
  { kind: "queue" },
  { kind: "needs-you" },
  { kind: "task", taskId: ids.task },
];

test("projects, agents, tasks, and requests retain their canonical relationships", () => {
  const request = [...fixtureState.humanRequests.values()][0];
  const task = fixtureState.tasks.get(request.task_id);
  const agent = fixtureState.agents.get(request.agent_id);
  assert.ok(task);
  assert.ok(agent);
  assert.equal(request.project_id, task.project_id);
  assert.equal(request.project_id, agent.project_id);
  assert.equal(request.agent_id, task.assigned_agent_id);

  const home = render();
  assert.match(home, /Builder One/);
  assert.match(home, /Review the state projection/);
  const needsYou = render({ screen: { kind: "needs-you" } });
  assert.match(needsYou, /Builder One asks/);
  assert.match(needsYou, /North Workshop · TASK 31313131/);
  const record = render({ screen: { kind: "task", taskId: ids.task } });
  assert.match(record, /Review the state projection/);
  assert.match(record, /Builder One · North Workshop/);
});

test("hostile names and titles are escaped as text and private detail is absent", () => {
  const hostile = "<img src=x onerror=alert(1)>";
  const hostileState = baseState({
    agents: new Map([[ids.agent, { id: ids.agent, project_id: ids.project, name: hostile, role: "worker", provider: "claude_code", paused: false, revision: 10n }]]),
    tasks: new Map([[ids.task, { id: ids.task, project_id: ids.project, assigned_agent_id: ids.agent, title: hostile, status: "running", priority: 10, revision: 12n }]]),
  });
  for (const screen of SCREENS) {
    const markup = render({ state: hostileState, screen });
    assert.equal(markup.includes("<img"), false, screen.kind);
    assert.equal(markup.includes("question"), false, screen.kind);
    assert.equal(markup.includes("provider"), false, screen.kind);
  }
  const markup = render({ state: hostileState });
  assert.match(markup, /&lt;img src=x onerror=alert\(1\)&gt;/);
});

test("hostile extras render as text on every surface that shows them", () => {
  const hostile = "<script>alert(1)</script>";
  const extras = {
    ticker: [{ at: "14:00", text: hostile }],
    suggestions: [{ id: "51".repeat(16), title: hostile, origin: "github", detail: hostile }],
    rowTicks: new Map([[ids.task, hostile]]),
    records: new Map([[ids.task, { asked: hostile, happened: hostile, runNumber: 1, review: { outcome: "blocked", findings: [hostile] }, checks: [hostile], files: [{ path: hostile, additions: 1, deletions: 1, patch: hostile }], transcript: hostile }]]),
  };
  for (const screen of SCREENS) {
    const markup = render({ extras, screen });
    assert.equal(markup.includes("<script"), false, screen.kind);
  }
  assert.match(render({ extras }), /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
});

test("the console never shows a kernel-grammar or retired vocabulary word", () => {
  const withDetail = {
    selectedHumanRequest: {
      request: fixtureState.humanRequests.get(ids.request),
      phase: "ready",
      question: "Proceed with the migration?",
      canReply: true,
      canCancel: true,
      replyMaxBytes: 8192,
      reply: "",
    },
    onHumanReplyChange: () => {},
    onReplyHumanRequest: () => {},
    onCancelHumanRequest: () => {},
    onCloseHumanRequest: () => {},
    onNavigate: () => {},
    onSelectAgent: () => {},
  };
  const sidebarView = (overrides = {}) => createElement(TerminalSidebar, {
    terminal: { agentId: "21".repeat(16), agentName: "Builder One", agentRevision: 10n, phase: "ready", writable: false, leaseOperation: "none", surfaceVersion: 0, ...overrides.terminal },
    snapshot: { status: "ready" },
    collapsed: overrides.collapsed ?? false,
    onToggleCollapsed: () => {},
    onClose: () => {},
    onTakeControl: () => {},
    onHandBack: () => {},
    onStop: () => {},
    stopUnavailable: "daemon per-run stop authority outside NEEDS YOU",
  }, createElement("div"));
  const surfaces = [
    ...SCREENS.map((screen) => [screen.kind, render({ ...withDetail, extras: fixtureConsoleExtras, screen })]),
    ["sidebar", renderToStaticMarkup(sidebarView())],
    ["sidebar-writable", renderToStaticMarkup(sidebarView({ terminal: { writable: true } }))],
    ["sidebar-collapsed", renderToStaticMarkup(sidebarView({ collapsed: true }))],
  ];
  for (const [name, markup] of surfaces) {
    for (const forbidden of [/attempt/i, /converge/i, /admission/i, /finalize/i, /unresolved/i, /proposal/i, /verdict/i, /\bALLOW\b/, /\bBLOCK\b/, /lease/i, /intake/i, /quarantine/i, /overseer/i, /work item/i, /cancel run/i]) {
      assert.equal(forbidden.test(markup), false, `${name}: ${forbidden}`);
    }
  }
});

test("all session statuses have stable live labels and no unsupported ready controls", () => {
  for (const status of ["idle", "connecting", "authenticating", "syncing", "ready", "closed"]) {
    const markup = render({ status });
    assert.match(markup, new RegExp(`>${status.toUpperCase()}<`));
    assert.equal(markup.includes("<button"), false);
  }
});

test("closed and pairing-uncertain errors have no ineffective action", () => {
  for (const error of [new SessionError("connection", true), new SessionError("pairing_uncertain"), new ProtocolError("malformed")]) {
    const markup = render({ status: "closed", error });
    assert.match(markup, /role="alert"/);
    assert.equal(markup.includes("<button"), false);
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

test("empty and maximum bounded collections have explicit, semantic output", () => {
  const emptyState = baseState({ factory: [], projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map() });
  const emptyHome = render({ state: emptyState });
  assert.match(emptyHome, /BUILDING STATE UNAVAILABLE/);
  assert.match(emptyHome, /no agents/);
  assert.match(emptyHome, /no tasks yet/);
  assert.match(render({ state: emptyState, screen: { kind: "needs-you" } }), /all quiet — nothing needs you/);
  assert.match(render({ state: emptyState, screen: { kind: "queue" } }), /the queue is empty/);

  const agents = new Map();
  const tasks = new Map();
  const requests = new Map();
  for (let index = 0; index < 8; index += 1) {
    const suffix = String(index).padStart(2, "0");
    const agentID = `${suffix}${"21".repeat(15)}`;
    const taskID = `${suffix}${"31".repeat(15)}`;
    const requestID = `${suffix}${"41".repeat(15)}`;
    agents.set(agentID, { id: agentID, project_id: ids.project, name: `Agent ${index}`, role: "worker", provider: "codex", paused: false, revision: BigInt(index + 1) });
    tasks.set(taskID, { id: taskID, project_id: ids.project, assigned_agent_id: agentID, title: `Task ${index}`, status: "queued", priority: index, revision: BigInt(index + 1) });
    requests.set(requestID, { id: requestID, project_id: ids.project, agent_id: agentID, task_id: taskID, created_at: 1n, updated_at: 1n, revision: BigInt(index + 1), kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true });
  }
  const bounded = baseState({ agents, tasks, humanRequests: requests });
  const home = render({ state: bounded });
  assert.equal((home.match(/class="dfConsoleRow"/g) ?? []).length, 8);
  assert.equal((home.match(/dfConsoleStrip__agent /g) ?? []).length, 8);
  assert.match(home, /8 queued</);
  assert.match(home, />Task 7</);
  const needsYou = render({ state: bounded, screen: { kind: "needs-you" } });
  assert.equal((needsYou.match(/class="dfFactoryConsole__card"/g) ?? []).length, 8);
  assert.match(needsYou, />8 ITEMS</);
  assert.match(needsYou, /Agent 7 asks/);
});

test("semantic structure remains keyboard-safe and contains no unsupported action fields", () => {
  const markup = render();
  assert.match(markup, /<main class="dfFactoryConsole" aria-label="Factory operator console">/);
  for (const label of ["Agents and factory counters", "BUILDING", "Tasks", "Latest factory event"]) {
    assert.match(markup, new RegExp(`aria-label="${label}"`));
  }
  assert.match(render({ screen: { kind: "needs-you" } }), /aria-label="NEEDS YOU"/);
  assert.match(render({ screen: { kind: "queue" } }), /aria-label="Queue"/);
  assert.match(render({ screen: { kind: "queue" } }), /aria-label="Suggestions"/);
  assert.match(render({ screen: { kind: "task", taskId: ids.task } }), /aria-label="Task record"/);
  assert.match(markup, /role="status" aria-live="polite"/);
  assert.match(markup, /<ul class="dfConsoleRows">/);
  assert.equal(/<(input|textarea|select|form)\b/.test(markup), false);
});

test("stage meters fill only on store-backed stage and the strip counts honestly", () => {
  const markup = render({ extras: fixtureConsoleExtras });
  assert.match(markup, /aria-label="stage: building"/);
  assert.match(markup, /aria-label="stage: queued"/);
  assert.match(markup, /aria-label="stage: done"/);
  assert.match(markup, /aria-label="stage: failed"/);
  const doneMeter = markup.split('aria-label="stage: done"')[1].split("</span></span>")[0];
  assert.equal((doneMeter.match(/dfStageMeter__segment--filled/g) ?? []).length, 4);
  const failedMeter = markup.split('aria-label="stage: failed"')[1].split("</span></span>")[0];
  assert.equal((failedMeter.match(/dfStageMeter__segment--filled/g) ?? []).length, 0);
  assert.match(markup, /1 queued</);
  assert.match(markup, /1 NEEDS YOU</);
  assert.match(markup, /2 awaiting deploy</);
  assert.match(markup, /review: approved/);
  assert.match(markup, /\+42 −18/);
});

test("data the daemon does not serve renders as explicitly not served, never invented", () => {
  const home = render();
  assert.match(home, /event feed not yet served by the daemon/);
  assert.match(home, /— awaiting deploy</);
  const queue = render({ screen: { kind: "queue" } });
  assert.match(queue, /suggestions are not yet served by the daemon/);
  const record = render({ screen: { kind: "task", taskId: ids.task } });
  assert.equal((record.match(/<p class="dfFactoryConsole__empty">not yet served by the daemon<\/p>/g) ?? []).length, 5);
  const gone = render({ screen: { kind: "task", taskId: "99".repeat(16) } });
  assert.match(gone, /no longer in the factory state/);
});

test("queue mutations and suggestion admission are disabled with their daemon gap named", () => {
  const queue = render({ screen: { kind: "queue" }, extras: fixtureConsoleExtras, onNavigate: () => {} });
  for (const needs of ["task-creation", "task-edit", "task-comment", "task-delete", "suggestion-accept"]) {
    assert.match(queue, new RegExp(`needs: daemon ${needs}`));
  }
  assert.equal((queue.match(/title="needs:/g) ?? []).length, (queue.match(/disabled="" title="needs:/g) ?? []).length);
  assert.ok((queue.match(/title="needs:/g) ?? []).length >= 8);
  assert.match(queue, /proposed work waits here until you accept it/);
  assert.match(queue, /accept/);
  assert.match(queue, /dismiss/);
});

test("an unavailable snapshot is explicit and does not invent runtime state", () => {
  const markup = render({ state: undefined, status: "syncing" });
  assert.match(markup, /NO SNAPSHOT/);
  assert.match(markup, /waiting for the factory/);
  assert.equal(markup.includes("ACTIVE RUNS"), false);
});

test("FactoryApp server-renders without reading browser globals", () => {
  const markup = renderToStaticMarkup(createElement(FactoryApp));
  assert.match(markup, /Factory operator console/);
  assert.match(markup, />IDLE</);
  assert.match(markup, /NO SNAPSHOT/);
  const withExtras = renderToStaticMarkup(createElement(FactoryApp, { extras: fixtureConsoleExtras }));
  assert.match(withExtras, /Factory operator console/);
});

test("selected hostile private detail is escaped and actions remain semantic", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const hostile = "<script>steal(authority)</script>";
  const markup = render({
    screen: { kind: "needs-you" },
    selectedHumanRequest: {
      request,
      phase: "ready",
      question: hostile,
      canReply: true,
      canCancel: true,
      replyMaxBytes: 8192,
      reply: "<reply>",
    },
    onHumanReplyChange: () => {},
    onReplyHumanRequest: () => {},
    onCancelHumanRequest: () => {},
    onCloseHumanRequest: () => {},
  });
  assert.match(markup, /aria-label="Selected question"/);
  assert.match(markup, /aria-label="Answer this question"/);
  assert.match(markup, /&lt;script&gt;steal\(authority\)&lt;\/script&gt;/);
  assert.equal(markup.includes("<script>"), false);
  assert.match(markup, /<textarea[^>]*>&lt;reply&gt;<\/textarea>/);
  assert.match(markup, />ANSWER</);
  assert.match(markup, />Stop</);
  assert.equal(markup.includes("expectedRunRevision"), false);
});

test("request, reply, cancel, and close controls forward only presentation intent", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const calls = [];
  const baseProps = {
    status: "ready",
    state: baseState(),
    screen: { kind: "needs-you" },
    onSelectHumanRequest: (value) => calls.push(["select", value]),
    onHumanReplyChange: (value) => calls.push(["change", value]),
    onReplyHumanRequest: () => calls.push(["reply"]),
    onCancelHumanRequest: () => calls.push(["cancel"]),
    onCloseHumanRequest: () => calls.push(["close"]),
  };

  const requestElements = expand(FactoryConsole(baseProps));
  requestElements.find((element) => element.type === "button" && element.props.children === "VIEW").props.onClick();
  assert.equal(calls[0][0], "select");
  assert.equal(calls[0][1], request);

  const selectedElements = expand(FactoryConsole({
    ...baseProps,
    selectedHumanRequest: { request, phase: "ready", question: "Proceed?", canReply: true, canCancel: true, replyMaxBytes: 8192, reply: "" },
  }));
  selectedElements.find((element) => element.type === "textarea").props.onChange({ currentTarget: { value: "Proceed." } });
  let prevented = false;
  selectedElements.find((element) => element.type === "form").props.onSubmit({ preventDefault: () => { prevented = true; } });
  selectedElements.find((element) => element.type === "button" && element.props.children === "Stop").props.onClick();
  selectedElements.find((element) => element.type === "button" && element.props.children === "CLOSE").props.onClick();
  assert.equal(prevented, true);
  assert.deepEqual(calls.slice(1), [["change", "Proceed."], ["reply"], ["cancel"], ["close"]]);
});

test("agent and question terminal actions expose only current public intent", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const agent = fixtureState.agents.get(ids.agent);
  const calls = [];
  const elements = expand(FactoryConsole({
    status: "ready",
    state: baseState(),
    screen: { kind: "needs-you" },
    selectedAgent: { id: agent.id, name: agent.name, revision: agent.revision },
    selectedHumanRequest: { request, phase: "ready", question: "Proceed?", canReply: true, canCancel: true, replyMaxBytes: 8192, reply: "" },
    onSelectAgent: (value) => calls.push(["agent", value]),
    onOpenTerminalForHumanRequest: (value) => calls.push(["request", value]),
    terminalContent: createElement("div", null, "terminal output is not React state"),
  }));
  const stripAgent = elements.find((element) => element.type === "button" && typeof element.props.className === "string" && element.props.className.includes("dfConsoleStrip__agent"));
  assert.equal(stripAgent.props["aria-pressed"], true);
  stripAgent.props.onClick();
  elements.filter((element) => element.type === "button" && element.props.children === "OPEN TERMINAL").at(-1).props.onClick();
  assert.equal(calls[0][0], "agent");
  assert.equal(calls[0][1].id, agent.id);
  assert.equal(calls[0][1].revision, agent.revision);
  assert.deepEqual(calls[1], ["request", request]);
  const markup = render({ terminalContent: createElement("div", null, "<raw-output>") });
  assert.match(markup, /&lt;raw-output&gt;/);
  assert.equal(markup.includes("runId"), false);
  assert.equal(markup.includes("sessionId"), false);
});

test("counter navigation targets the queue and NEEDS YOU screens exactly", () => {
  const navigations = [];
  const elements = expand(FactoryConsole({
    status: "ready",
    state: baseState(),
    onNavigate: (screen) => navigations.push(screen),
  }));
  const counters = elements.filter((element) => element.type === "button" && typeof element.props.className === "string" && element.props.className.includes("dfConsoleStrip__counter"));
  assert.equal(counters.length, 2);
  for (const counter of counters) counter.props.onClick();
  assert.deepEqual(navigations, [{ kind: "queue" }, { kind: "needs-you" }]);
});

function expand(node, result = []) {
  if (Array.isArray(node)) {
    for (const child of node) expand(child, result);
    return result;
  }
  if (!isValidElement(node)) return result;
  if (typeof node.type === "function") {
    expand(node.type(node.props), result);
    return result;
  }
  result.push(node);
  expand(node.props.children, result);
  return result;
}
