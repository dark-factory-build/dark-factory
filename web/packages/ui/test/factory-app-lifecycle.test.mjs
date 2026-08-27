import assert from "node:assert/strict";
import test from "node:test";
import { createElement, StrictMode } from "react";
import { act, create } from "react-test-renderer";
import { FactoryAppController } from "../dist/src/factory-app-controller.js";
import { FactoryAppLifecycle } from "../dist/src/factory-app.js";
import { fixtureState } from "../../../fixtures/state.mjs";

test("StrictMode gives each mounted effect one fenced BrowserClient lifecycle", async () => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT;
  const historyState = { route: "factory" };
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      location: { origin: "https://app.darkfactory.build", hash: "", pathname: "/factory", search: "" },
      history: { state: historyState, replaceState: () => {} },
    },
  });
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;

  const records = [];
  const controllerFactory = (options) => {
    const record = { connects: 0, closes: 0, publications: 0 };
    const controller = new FactoryAppController({
      ...options,
      onChange: (snapshot) => {
        record.publications += 1;
        options.onChange(snapshot);
      },
      clientFactory: (clientOptions) => {
        record.callbacks = clientOptions;
        return {
          session: {},
          connect: () => {
            record.connects += 1;
            clientOptions.onState(fixtureState);
            clientOptions.onStatus("ready");
            return Promise.resolve();
          },
          close: () => {
            record.closes += 1;
            clientOptions.onStatus("connecting");
            clientOptions.onState(fixtureState);
          },
        };
      },
    });
    records.push(record);
    return controller;
  };

  let renderer;
  try {
    await act(async () => {
      renderer = create(createElement(StrictMode, null, createElement(FactoryAppLifecycle, { controllerFactory })));
    });
    assert.equal(records.length, 2);
    assert.deepEqual(records.map(({ connects, closes }) => ({ connects, closes })), [
      { connects: 1, closes: 1 },
      { connects: 1, closes: 0 },
    ]);

    const beforeUnmount = records.map((record) => record.publications);
    await act(async () => { renderer.unmount(); });
    assert.deepEqual(records.map(({ connects, closes }) => ({ connects, closes })), [
      { connects: 1, closes: 1 },
      { connects: 1, closes: 1 },
    ]);
    for (const record of records) {
      record.callbacks.onStatus("closed");
      record.callbacks.onState(fixtureState);
    }
    assert.deepEqual(records.map((record) => record.publications), beforeUnmount);
  } finally {
    if (renderer !== undefined) renderer.unmount();
    if (previousWindow === undefined) delete globalThis.window;
    else Object.defineProperty(globalThis, "window", previousWindow);
    globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct;
  }
});
