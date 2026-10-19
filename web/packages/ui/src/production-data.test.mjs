import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { useProduction } from "../dist/src/production-data.js";

test("a factory holding no project reads nothing", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  const requests = [];
  let records, tree;
  function Probe() { records = useProduction([], async (operation, input) => { requests.push([operation, input.project_id]); return { records: [], total: 0, next_offset: 0 }; }); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe)); });
    assert.deepEqual(requests, []);
    assert.deepEqual(records, []);
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
  let records, tree;
  const call = async (_operation, { offset, limit }) => {
    offsets.push(offset);
    const page = Array.from({ length: Math.min(limit, total - offset) }, (_, index) => ({ repository: "owner/repo", kind: "pull_request", id: String(offset + index), visual_id: "", observed_at: 1, document: {}, tasks: [], missions: [] }));
    return { records: page, total, next_offset: offset + page.length < total ? offset + page.length : 0 };
  };
  function Probe() { records = useProduction(["project"], call); return null; }
  try {
    await act(async () => { tree = create(createElement(Probe)); });
    assert.equal(records.length, total);
    assert.equal(offsets.length, Math.ceil(total / 8));
  } finally {
    if (tree) await act(async () => tree.unmount());
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});
