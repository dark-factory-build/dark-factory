import assert from "node:assert/strict";
import test from "node:test";
import { createElement, isValidElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ProtocolError, SessionError } from "@dark-factory/client";
import { FactoryApp, FactoryConsole } from "../dist/src/index.js";
import { fixtureState } from "../../../fixtures/state.mjs";

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

test("projects, agents, tasks, and requests retain their canonical relationships", () => {
  const request = [...fixtureState.humanRequests.values()][0];
  const task = fixtureState.tasks.get(request.task_id);
  const agent = fixtureState.agents.get(request.agent_id);
  assert.ok(task);
  assert.ok(agent);
  assert.equal(request.project_id, task.project_id);
  assert.equal(request.project_id, agent.project_id);
  assert.equal(request.agent_id, task.assigned_agent_id);

  const markup = render();
  assert.match(markup, /North Workshop/);
  assert.match(markup, /South Workshop/);
  assert.match(markup, /WORKER · North Workshop/);
  assert.match(markup, /PRIORITY 10 · North Workshop/);
  assert.match(markup, /North Workshop · TASK 31313131/);
  assert.match(markup, /AGENT 21212121/);
});

test("hostile names and titles are escaped as text and private detail is absent", () => {
  const hostile = "<img src=x onerror=alert(1)>";
  const markup = render({
    state: baseState({
      agents: new Map([[ids.agent, { id: ids.agent, project_id: ids.project, name: hostile, role: "worker", paused: false, revision: 10n }]]),
      tasks: new Map([[ids.task, { id: ids.task, project_id: ids.project, assigned_agent_id: ids.agent, title: hostile, status: "running", priority: 10, revision: 12n }]]),
    }),
  });
  assert.match(markup, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.equal(markup.includes("<img"), false);
  assert.equal(markup.includes("question"), false);
  assert.equal(markup.includes("provider"), false);
});

test("all session statuses have stable live labels and no unsupported ready controls", () => {
  for (const status of ["idle", "connecting", "authenticating", "syncing", "ready", "closed"]) {
    const markup = render({ status });
    assert.match(markup, new RegExp(`>${status.toUpperCase()}<`));
  }
  assert.equal(render().includes("<button"), false);
  assert.equal(render({ status: "connecting", onRetry: () => {} }).includes("<button"), false);
});

test("closed connection exposes only the retry callback and a finite safe error", () => {
  let retries = 0;
  const markup = render({ status: "closed", error: new SessionError("connection", true), onRetry: () => { retries += 1; } });
  assert.match(markup, /Connection unavailable\./);
  assert.match(markup, /role="alert"/);
  assert.match(markup, /RETRY CONNECTION/);
  assert.equal(markup.includes("Error:"), false);
  assert.equal(markup.includes("secret"), false);
  assert.equal(retries, 0);
});

test("protocol errors remain finite and never expose their message", () => {
  const markup = render({ status: "closed", error: new ProtocolError("malformed"), onRetry: () => {} });
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
  const empty = render({ state: baseState({ factory: [], projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map() }) });
  assert.match(empty, /BUILDING STATE UNAVAILABLE/);
  assert.match(empty, /NO AGENTS/);
  assert.match(empty, /NO TASKS/);
  assert.match(empty, /NO OPEN REQUESTS/);

  const agents = new Map();
  const tasks = new Map();
  const requests = new Map();
  for (let index = 0; index < 8; index += 1) {
    const suffix = String(index).padStart(2, "0");
    const agentID = `${suffix}${"21".repeat(15)}`;
    const taskID = `${suffix}${"31".repeat(15)}`;
    const requestID = `${suffix}${"41".repeat(15)}`;
    agents.set(agentID, { id: agentID, project_id: ids.project, name: `Agent ${index}`, role: "worker", paused: false, revision: BigInt(index + 1) });
    tasks.set(taskID, { id: taskID, project_id: ids.project, assigned_agent_id: agentID, title: `Task ${index}`, status: "queued", priority: index, revision: BigInt(index + 1) });
    requests.set(requestID, { id: requestID, project_id: ids.project, agent_id: agentID, task_id: taskID, created_at: 1n, updated_at: 1n, revision: BigInt(index + 1), kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true });
  }
  const markup = render({ state: baseState({ agents, tasks, humanRequests: requests }) });
  assert.equal((markup.match(/class="dfFactoryConsole__card"/g) ?? []).length, 24);
  assert.match(markup, />8 ITEMS</);
  assert.match(markup, />Agent 7</);
  assert.match(markup, />Task 7</);
  assert.match(markup, /REQUEST 07414141/);
});

test("semantic structure remains keyboard-safe and contains no unsupported action fields", () => {
  const markup = render();
  assert.match(markup, /<main class="dfFactoryConsole" aria-label="Factory operator console">/);
  for (const label of ["BUILDING", "AGENTS", "TASK QUEUE", "NEEDS YOU"]) assert.match(markup, new RegExp(`aria-label="${label}"`));
  assert.match(markup, /role="status" aria-live="polite"/);
  assert.match(markup, /<ul class="dfFactoryConsole__list">/);
  assert.equal(/<(input|textarea|select|form)\b/.test(markup), false);
});

test("an unavailable snapshot is explicit and does not invent runtime state", () => {
  const markup = render({ state: undefined, status: "syncing" });
  assert.match(markup, /NO SNAPSHOT/);
  assert.match(markup, /WAITING FOR SNAPSHOT/);
  assert.equal(markup.includes("ACTIVE RUNS"), false);
});

test("FactoryApp server-renders without reading browser globals", () => {
  const markup = renderToStaticMarkup(createElement(FactoryApp));
  assert.match(markup, /Factory operator console/);
  assert.match(markup, />IDLE</);
  assert.match(markup, /NO SNAPSHOT/);
});

test("selected hostile private detail is escaped and actions remain semantic", () => {
  const request = fixtureState.humanRequests.get(ids.request);
  const hostile = "<script>steal(authority)</script>";
  const markup = render({
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
  assert.match(markup, /aria-label="Selected human request"/);
  assert.match(markup, /aria-label="Reply to human request"/);
  assert.match(markup, /&lt;script&gt;steal\(authority\)&lt;\/script&gt;/);
  assert.equal(markup.includes("<script>"), false);
  assert.match(markup, /<textarea[^>]*>&lt;reply&gt;<\/textarea>/);
  assert.match(markup, /SEND REPLY/);
  assert.match(markup, /CANCEL RUN/);
  assert.equal(markup.includes("expectedRunRevision"), false);
});

test("request, reply, cancel, and close controls forward only presentation intent", () => {
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
  requestElements.find((element) => element.type === "button" && element.props.children === "VIEW REQUEST").props.onClick();
  assert.equal(calls[0][0], "select");
  assert.equal(calls[0][1], request);

  const selectedElements = expand(FactoryConsole({
    ...baseProps,
    selectedHumanRequest: { request, phase: "ready", question: "Proceed?", canReply: true, canCancel: true, replyMaxBytes: 8192, reply: "" },
  }));
  selectedElements.find((element) => element.type === "textarea").props.onChange({ currentTarget: { value: "Proceed." } });
  let prevented = false;
  selectedElements.find((element) => element.type === "form").props.onSubmit({ preventDefault: () => { prevented = true; } });
  selectedElements.find((element) => element.type === "button" && element.props.children === "CANCEL RUN").props.onClick();
  selectedElements.find((element) => element.type === "button" && element.props.children === "CLOSE").props.onClick();
  assert.equal(prevented, true);
  assert.deepEqual(calls.slice(1), [["change", "Proceed."], ["reply"], ["cancel"], ["close"]]);
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
