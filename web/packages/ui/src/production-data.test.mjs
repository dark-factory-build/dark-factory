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
    assert.equal(state.read, true);
    await act(async () => { tree.update(createElement(Probe, {})); });
    assert.equal(state.runtime, undefined);
    assert.equal(state.read, false);
    const pending = new Promise((done) => { resolve = done; });
    await act(async () => { tree.update(createElement(Probe, { call: () => pending })); });
    assert.equal(state.runtime, undefined);
    assert.equal(state.read, false, "records kept from before the drop are not read on this connection");
    await act(async () => { resolve(page(build("new"))); await pending; });
    assert.equal(state.runtime.source, "new");
    assert.equal(state.read, true);
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});

test("switching projects is a first read too: nothing read for the new list counts until it arrives", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  const page = { records: [], total: 0, next_offset: 0 };
  const seen = [];
  let tree, resolve;
  function Probe({ projects, call }) { const state = useProduction(projects, call); seen.push([projects.join(), state.read]); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe, { projects: ["a"], call: async () => page })); });
    assert.deepEqual(seen.at(-1), ["a", true]);
    const pending = new Promise((done) => { resolve = done; });
    seen.length = 0;
    await act(async () => { tree.update(createElement(Probe, { projects: ["b"], call: () => pending })); });
    assert.ok(seen.length > 0 && seen.every(([projects, read]) => projects === "b" && read === false), `not read for b, from its first render: ${JSON.stringify(seen)}`);
    await act(async () => { resolve(page); await pending; });
    assert.deepEqual(seen.at(-1), ["b", true]);
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});

test("every page of the relevant set is read until next_offset is 0, with no record ceiling", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  const total = 300;
  const offsets = [];
  let state, tree;
  const call = async (_operation, { offset, limit }) => {
    offsets.push(offset);
    const records = Array.from({ length: Math.min(limit, total - offset) }, (_, index) => ({ repository: "owner/repo", kind: "pull_request", id: String(offset + index), visual_id: "", observed_at: 1, document: {}, tasks: [], missions: [] }));
    return { records, total, next_offset: offset + records.length < total ? offset + records.length : 0 };
  };
  function Probe() { state = useProduction(["project"], call); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe)); });
    assert.equal(state.records.length, total);
    assert.equal(offsets.length, Math.ceil(total / 8));
    assert.equal("overflow" in state, false);
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});
