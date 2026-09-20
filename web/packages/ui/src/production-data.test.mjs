import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { useProduction } from "../dist/src/production-data.js";

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
