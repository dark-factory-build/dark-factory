import assert from "node:assert/strict";
import test from "node:test";
import { createElement, StrictMode } from "react";
import { act, create } from "react-test-renderer";
import { FactoryApp } from "../dist/src/index.js";

test("StrictMode mounts the public FactoryApp and closes each owned BrowserClient once", async () => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const previousWebSocket = Object.getOwnPropertyDescriptor(globalThis, "WebSocket");
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  const historyState = { route: "factory" };
  const sockets = [];
  class FakeWebSocket {
    constructor(url) {
      this.url = url;
      this.closeCount = 0;
      sockets.push(this);
    }

    close() {
      this.closeCount += 1;
    }
  }
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      location: { origin: "https://app.darkfactory.build", hash: "", pathname: "/factory", search: "" },
      history: { state: historyState, replaceState: () => {} },
    },
  });
  Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: FakeWebSocket });
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;

  let renderer;
  try {
    await act(async () => {
      renderer = create(createElement(StrictMode, null, createElement(FactoryApp)));
    });
    assert.equal(sockets.length, 2);
    assert.deepEqual(sockets.map((socket) => ({ url: socket.url, closeCount: socket.closeCount })), [
      { url: "ws://127.0.0.1:43123/browser/v1", closeCount: 1 },
      { url: "ws://127.0.0.1:43123/browser/v1", closeCount: 0 },
    ]);

    await act(async () => { renderer.unmount(); });
    assert.deepEqual(sockets.map((socket) => socket.closeCount), [1, 1]);
  } finally {
    if (renderer !== undefined) renderer.unmount();
    if (previousWindow === undefined) delete globalThis.window;
    else Object.defineProperty(globalThis, "window", previousWindow);
    if (previousWebSocket === undefined) delete globalThis.WebSocket;
    else Object.defineProperty(globalThis, "WebSocket", previousWebSocket);
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});
