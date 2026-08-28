import assert from "node:assert/strict";
import { createElement, StrictMode } from "react";
import { act, create } from "react-test-renderer";
import { FactoryApp } from "../../dist/src/index.js";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const listeners = new Set();
globalThis.window = {
  location: { origin: "https://strict.test", hash: "", pathname: "/", search: "" },
  history: { state: null, replaceState() {} },
  addEventListener: (_type, listener) => listeners.add(listener),
  removeEventListener: (_type, listener) => listeners.delete(listener),
};

async function flush() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 10));
}

let renderer;
try {
  await act(async () => {
    renderer = create(createElement(StrictMode, null, createElement(FactoryApp)), {
      createNodeMock: (element) => element.type === "div" ? { isConnected: true } : null,
    });
    await flush();
  });
  const open = renderer.root.findAllByType("button").find((button) => typeof button.props.className === "string" && button.props.className.includes("dfConsoleStrip__agent"));
  assert.ok(open, "public FactoryApp must expose the agent strip terminal action");
  await act(async () => {
    open.props.onClick();
    await flush();
  });
  await act(async () => { await flush(); });

  const counters = globalThis.__darkFactoryStrictProbe;
  assert.equal(counters.clients, 2, "StrictMode must create two public app sessions");
  assert.equal(counters.resolves, 1, "selected agent must resolve one exact target");
  assert.equal(counters.opens, 1);
  assert.equal(counters.attaches, 1);
  assert.equal(counters.acquires, 1);
  assert.ok(counters.terminals >= 1, "selected public terminal must construct xterm");
  assert.equal(counters.terminals - counters.disposes, 1, "one selected terminal must remain live before unmount");

  await act(async () => { renderer.unmount(); });
  assert.equal(counters.terminals, counters.disposes, "unmount must dispose the selected xterm");
  assert.equal(counters.sessionCloses, 2, "each public app session must close exactly once");
  console.log(JSON.stringify(counters));
} finally {
  if (renderer !== undefined) renderer.unmount();
}
