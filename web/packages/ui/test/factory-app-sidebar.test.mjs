import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { TerminalContent, TerminalPanel } from "../dist/src/factory-app.js";
import { TaskConversation } from "../dist/src/console-sidebar.js";
const textOf = (node) => [].concat(node.props.children).flat(Infinity).filter((child) => typeof child === "string").join("").trim();

function terminalView(overrides = {}) {
  return {
    agentId: "21".repeat(16),
    agentName: "Builder One",
    agentRevision: 10n,
    phase: "ready",
    writable: true,
    paused: false,
    queued: false,
    hasOutputSurface: false,
    resets: 0,
    finishing: false,
    surfaceVersion: 0,
    ...overrides,
  };
}

function panel(terminal = terminalView()) {
  return createElement(
    TerminalPanel,
    { terminal },
    createElement("div", { className: "terminal-surface" }, "live terminal surface"),
  );
}

test("answered task conversation keeps the question and answer text without delivery details", () => {
  const markup = renderToStaticMarkup(createElement(TaskConversation, {
    brief: {
      taskId: "task-1",
      revision: 1n,
      head: 2n,
      instruction: "",
      feedback: "",
      peerQuestions: [{
        id: "question-1",
        source_task_id: "source-task",
        target_task_id: "target-task",
        question: "What changed?",
        answer: "The sidebar is simpler.",
        recipient_delivery_state: "delivered",
        answer_delivery_state: "delivered",
        revision: 1n,
        created_at_ms: 1n,
        updated_at_ms: 1n,
      }],
    },
  }));
  assert.match(markup, /What changed\?/);
  assert.match(markup, /The sidebar is simpler\./);
  assert.equal(markup.includes("Recipient delivery"), false);
  assert.equal(markup.includes("Answer delivery"), false);
});

// Agent pane render coverage above deliberately excludes task-entry controls.
test("the terminal is a quiet sidebar", () => {
  const markup = renderToStaticMarkup(panel(terminalView({ taskTitle: "Repair finalization" })));
  assert.match(markup, /dfFactoryConsole__terminalPanel/);
  assert.match(markup, />Repair finalization<\/p>/);
  assert.match(markup, /live terminal surface/);
  assert.equal(markup.includes("Close"), false);
  for (const noise of ["CURRENT RUN TERMINAL", "Ready", "you have control", "watching", "take control", "hand back", "Steer"]) {
    assert.equal(markup.includes(noise), false, noise);
  }
  assert.equal((markup.match(/<button/g) ?? []).length, 0, "the terminal has no duplicate controls");
});
test("finalizing work shows no blank terminal or idle input", () => {
  const markup = renderToStaticMarkup(createElement(TerminalContent, {
    terminal: terminalView({ taskTitle: "Repair finalization", finishing: true }),
    controller: {},
  }));
  assert.match(markup, />Finishing<\/p>/);
  assert.equal(markup.includes("textarea"), false);
  assert.equal(markup.includes('role="application"'), false);
});
test("paused or capacity-queued idle agents show truthful state without task entry", () => {
  for (const overrides of [{ paused: true }, { queued: true }]) {
    const markup = renderToStaticMarkup(createElement(TerminalContent, {
      terminal: terminalView({ phase: "idle", writable: false, ...overrides }),
      controller: {},
    }));
    assert.match(markup, overrides.paused ? />Paused<\/p>/ : /Queued · waiting for capacity/);
    assert.equal(markup.includes("textarea"), false);
    assert.equal(markup.includes("Add to queue"), false);
  }
});

test("a definite steering refusal is visible", () => {
  const markup = renderToStaticMarkup(createElement(TerminalContent, {
    terminal: terminalView({ taskTitle: "Standing inspection", controlReady: true, controlError: { code: "stale" } }),
    controller: {},
  }));
  assert.match(markup, />Control not sent</);
});

test("the terminal has controls but no duplicated task history", () => {
  const markup = renderToStaticMarkup(createElement(TerminalContent, {
    terminal: terminalView({ taskTitle: "Standing inspection", controlReady: true }),
    controller: {},
  }));
  assert.equal(markup.includes("History"), false);
  for (const removed of ["textarea", "Any worker", "Start", "Add follow-up work", "Add to queue"]) assert.equal(markup.includes(removed), false, removed);
  assert.match(markup, />Interrupt<\/button>/);
  assert.match(markup, />Stop current<\/button>/);
});


test("exceptional input ownership and replay loss are concise", () => {
  const occupied = renderToStaticMarkup(panel(terminalView({ writable: false, error: { code: "stale" } })));
  assert.match(occupied, /Terminal open elsewhere/);
  assert.match(renderToStaticMarkup(panel(terminalView({ writable: false, error: { code: "stale" }, errorSource: "input" }))), /Terminal open elsewhere/);
  const unavailable = renderToStaticMarkup(panel(terminalView({ writable: false, error: { code: "connection" } })));
  assert.match(unavailable, /Terminal attach unavailable/);
  assert.equal(unavailable.includes("Terminal open elsewhere"), false);
  const inputRejected = renderToStaticMarkup(panel(terminalView({ writable: true, hasOutputSurface: true, error: { code: "invalid_request" }, errorSource: "input" })));
  assert.match(inputRejected, /Input rejected/);
  assert.equal(inputRejected.includes("Terminal attach unavailable"), false);
  const inputLeaseUnavailable = renderToStaticMarkup(panel(terminalView({ writable: false, hasOutputSurface: true, error: { code: "connection" }, errorSource: "input" })));
  assert.match(inputLeaseUnavailable, /Input unavailable/);
  assert.equal(inputLeaseUnavailable.includes("Terminal attach unavailable"), false);
  const inputConnectionLost = renderToStaticMarkup(panel(terminalView({ phase: "closed", writable: false, error: { code: "connection" }, errorSource: "input" })));
  assert.match(inputConnectionLost, /Terminal input connection lost/);
  assert.equal(inputConnectionLost.includes("ATTACHMENT REMAINS LIVE"), false);
  const displayProblem = renderToStaticMarkup(panel(terminalView({ hasOutputSurface: true, error: { code: "internal" }, errorSource: "display" })));
  assert.match(displayProblem, /Terminal display error/);
  const reset = renderToStaticMarkup(panel(terminalView({ resets: 1 })));
  assert.match(reset, /Earlier output is no longer retained/);
  const quiet = renderToStaticMarkup(panel());
  assert.equal(quiet.includes("Terminal open elsewhere"), false);
  assert.equal(quiet.includes("Earlier output"), false);
});

// Agent pane render coverage deliberately excludes task-entry controls.
