import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { TerminalPanel } from "../dist/src/factory-app.js";

function terminalView(overrides = {}) {
  return {
    agentId: "21".repeat(16),
    agentName: "Builder One",
    agentRevision: 10n,
    phase: "ready",
    writable: true,
    resets: 0,
    surfaceVersion: 0,
    ...overrides,
  };
}

function panel(terminal = terminalView(), onClose = () => {}) {
  return createElement(
    TerminalPanel,
    { terminal, onClose },
    createElement("div", { className: "terminal-surface" }, "live terminal surface"),
  );
}

test("the terminal is quiet inline console content", () => {
  const markup = renderToStaticMarkup(panel(terminalView({ taskTitle: "Repair finalization" })));
  assert.match(markup, /dfFactoryConsole__terminalPanel/);
  assert.match(markup, /Builder One · Repair finalization/);
  assert.match(markup, /live terminal surface/);
  assert.match(markup, />CLOSE TERMINAL<\/button>/);
  for (const noise of ["CURRENT RUN TERMINAL", "READY", "you have control", "watching", "take control", "hand back", "Steer"]) {
    assert.equal(markup.includes(noise), false, noise);
  }
  assert.equal((markup.match(/<button/g) ?? []).length, 1, "close is the only terminal control");
});

test("close remains available through setup and invokes the owner once", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  try {
    for (const phase of ["idle", "resolving", "attaching", "acquiring", "ready", "closing", "closed"]) {
      let closes = 0;
      let renderer;
      await act(async () => {
        renderer = create(panel(terminalView({ phase }), () => { closes += 1; }));
      });
      const button = renderer.root.findByType("button");
      assert.equal(button.props.disabled, phase === "closing" || phase === "closed", phase);
      if (!button.props.disabled) {
        await act(async () => { button.props.onClick(); });
        assert.equal(closes, 1, phase);
      }
      await act(async () => { renderer.unmount(); });
    }
  } finally {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("exceptional input ownership and replay loss are concise", () => {
  const occupied = renderToStaticMarkup(panel(terminalView({ writable: false, error: { code: "invalid_request" } })));
  assert.match(occupied, /TERMINAL OPEN ELSEWHERE/);
  const unavailable = renderToStaticMarkup(panel(terminalView({ writable: false, error: { code: "connection" } })));
  assert.match(unavailable, /TERMINAL UNAVAILABLE/);
  assert.equal(unavailable.includes("TERMINAL OPEN ELSEWHERE"), false);
  const reset = renderToStaticMarkup(panel(terminalView({ resets: 1 })));
  assert.match(reset, /Earlier output is no longer retained/);
  const quiet = renderToStaticMarkup(panel());
  assert.equal(quiet.includes("TERMINAL OPEN ELSEWHERE"), false);
  assert.equal(quiet.includes("Earlier output"), false);
});
