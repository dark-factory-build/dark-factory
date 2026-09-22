import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { useProduction } from "../dist/src/production-data.js";

test("a factory holding no project still observes the running build and published release", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  const requests = [];
  const runtime = { version: "v0.4.2", source: "a".repeat(40), target: "darwin/arm64", build_id: "build", release: true };
  const release = { version: "v0.4.3", url: "https://github.com/dark-factory-build/dark-factory/releases/tag/v0.4.3" };
  let state, tree;
  function Probe() { state = useProduction([], async (operation, input) => { requests.push([operation, input.project_id]); return { runtime, release, records: [], total: 0, next_offset: 0 }; }); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe)); });
    assert.deepEqual(requests, [["production", ""]], "one projectless read, not none and not one per project");
    assert.deepEqual(state.runtime, runtime);
    assert.deepEqual(state.release, release);
    assert.deepEqual(state.records, []);
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});

test("reconnect requires a new serving-runtime observation before confirming its identity", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  const build = (source) => ({ version: source, source, target: "darwin/arm64", build_id: source, release: true });
  const page = (runtime) => ({ runtime, records: [], total: 0, next_offset: 0 });
  let state, tree, resolve;
  function Probe({ call }) { state = useProduction(["project"], call); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe, { call: async () => page(build("old")) })); });
    assert.equal(state.runtime.source, "old");
    await act(async () => { tree.update(createElement(Probe, {})); });
    assert.equal(state.runtime, undefined);
    const pending = new Promise((done) => { resolve = done; });
    await act(async () => { tree.update(createElement(Probe, { call: () => pending })); });
    assert.equal(state.runtime, undefined);
    await act(async () => { resolve(page(build("new"))); await pending; });
    assert.equal(state.runtime.source, "new");
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});
