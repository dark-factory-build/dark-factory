import assert from "node:assert/strict";
import test from "node:test";
import { createElement, useEffect } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { TerminalSidebar } from "../dist/src/factory-app.js";

const counters = { mounts: 0, unmounts: 0 };

function Probe() {
  useEffect(() => {
    counters.mounts += 1;
    return () => { counters.unmounts += 1; };
  }, []);
  return createElement("div", null, "live terminal surface");
}

function terminalView(overrides = {}) {
  return {
    agentId: "21".repeat(16),
    agentName: "Builder One",
    agentRevision: 10n,
    phase: "ready",
    writable: false,
    leaseOperation: "none",
    surfaceVersion: 0,
    ...overrides,
  };
}

function sidebarProps(overrides = {}, calls = { close: 0, take: 0, handBack: 0, stop: 0, toggle: 0 }) {
  return {
    calls,
    props: {
      terminal: terminalView(overrides.terminal),
      snapshot: { status: "ready", ...overrides.snapshot },
      collapsed: overrides.collapsed ?? false,
      onToggleCollapsed: () => { calls.toggle += 1; },
      onClose: () => { calls.close += 1; },
      onTakeControl: () => { calls.take += 1; },
      onHandBack: () => { calls.handBack += 1; },
      onStop: () => { calls.stop += 1; },
      stopUnavailable: "daemon per-run stop authority outside NEEDS YOU",
    },
  };
}

test("collapse clips the sidebar without unmounting the terminal or touching the session", async () => {
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  counters.mounts = 0;
  counters.unmounts = 0;
  const { props, calls } = sidebarProps();
  let renderer;
  try {
    await act(async () => {
      renderer = create(createElement(TerminalSidebar, props, createElement(Probe)));
    });
    assert.equal(counters.mounts, 1);
    assert.equal(counters.unmounts, 0);

    await act(async () => {
      renderer.update(createElement(TerminalSidebar, { ...props, collapsed: true }, createElement(Probe)));
    });
    assert.equal(counters.unmounts, 0, "collapse must not unmount the terminal surface");
    assert.equal(calls.close, 0, "collapse must not close the session");
    const tab = renderer.root.findAllByType("button").find((button) => button.props.className === "dfConsoleSidebar__tab");
    assert.ok(tab, "the collapsed edge tab renders");
    assert.equal(tab.props.children, "Builder One");
    const body = renderer.root.findAllByType("div").find((element) => element.props.className === "dfConsoleSidebar__body");
    assert.equal(body.props["aria-hidden"], "true");

    await act(async () => {
      renderer.update(createElement(TerminalSidebar, { ...props, collapsed: false }, createElement(Probe)));
    });
    assert.equal(counters.mounts, 1, "expand reuses the same live surface, no re-open");
    assert.equal(counters.unmounts, 0);
    assert.equal(calls.close, 0);

    await act(async () => { renderer.unmount(); });
    assert.equal(counters.unmounts, 1, "only a real unmount tears the surface down");
  } finally {
    if (renderer !== undefined) renderer.unmount();
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});

test("the collapsed sidebar server-renders the tab and keeps the terminal body", () => {
  const { props } = sidebarProps({ collapsed: true });
  const markup = renderToStaticMarkup(createElement(TerminalSidebar, props, createElement("div", null, "live terminal surface")));
  assert.match(markup, /Terminal \(collapsed\)/);
  assert.match(markup, /dfConsoleSidebar__tab/);
  assert.match(markup, /Builder One/);
  assert.match(markup, /live terminal surface/, "the body stays rendered while clipped");
  assert.match(markup, /aria-hidden="true"/);
});

test("steer is never a dead-looking live control and stop routes only real authority", () => {
  const readOnly = sidebarProps();
  const readOnlyMarkup = renderToStaticMarkup(createElement(TerminalSidebar, readOnly.props, createElement("div")));
  assert.match(readOnlyMarkup, /<button type="button" title="takes control so you can type">Steer<\/button>/, "read-only ready steer is enabled");

  const writable = sidebarProps({ terminal: { writable: true } });
  const writableMarkup = renderToStaticMarkup(createElement(TerminalSidebar, writable.props, createElement("div")));
  assert.match(writableMarkup, /<button[^>]*disabled=""[^>]*title="you have control — type in the terminal"[^>]*>Steer<\/button>/);
  assert.match(writableMarkup, /you have control/);

  const request = { id: "41".repeat(16), agent_id: "21".repeat(16), revision: 13n };
  const withStop = sidebarProps({
    terminal: { writable: true },
    snapshot: { selectedHumanRequest: { request, phase: "ready", canReply: true, canCancel: true, replyMaxBytes: 8192, reply: "" } },
  });
  const stopMarkup = renderToStaticMarkup(createElement(TerminalSidebar, withStop.props, createElement("div")));
  assert.match(stopMarkup, /<button type="button" title="stop this run">Stop<\/button>/);

  const withoutStop = sidebarProps();
  const noStopMarkup = renderToStaticMarkup(createElement(TerminalSidebar, withoutStop.props, createElement("div")));
  assert.match(noStopMarkup, /<button[^>]*disabled=""[^>]*title="needs: daemon per-run stop authority outside NEEDS YOU"[^>]*>Stop<\/button>/);
});

test("the replay-reset banner appears only after a server reset", async () => {
  const { renderToStaticMarkup } = await import("react-dom/server");
  const { createElement } = await import("react");
  const props = (resets) => ({
    terminal: { agentId: "21".repeat(16), agentName: "Builder One", agentRevision: 10n, phase: "ready", writable: false, leaseOperation: "none", resets, surfaceVersion: 0 },
    snapshot: { status: "ready" },
    collapsed: false,
    onToggleCollapsed: () => {},
    onClose: () => {},
    onTakeControl: () => {},
    onHandBack: () => {},
    onStop: () => {},
    stopUnavailable: "daemon per-run stop authority outside NEEDS YOU",
  });
  const quiet = renderToStaticMarkup(createElement(TerminalSidebar, props(0), createElement("div")));
  assert.equal(/Replay reset/.test(quiet), false);
  const reset = renderToStaticMarkup(createElement(TerminalSidebar, props(1), createElement("div")));
  assert.match(reset, /role="status"/);
  assert.match(reset, /Replay reset — earlier output is no longer retained/);
});
