import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { ProjectOutcomes } from "../dist/src/project-outcomes.js";
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const words = (node) => typeof node === "string" ? node : (node.children ?? []).map(words).join("");
const click = async (tree, label) => act(async () => tree.root.findAllByType("button").find(node => words(node).includes(label)).props.onClick());
const mount = async (t, call) => { let tree; await act(async () => { tree = create(createElement(ProjectOutcomes, { project: "project", call })); }); t.after(() => act(async () => tree.unmount())); return tree; };
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

test("all outcome pages load automatically while earlier rows stay usable; detail is fetched only on selection", async t => {
  const page = deferred(), calls = [];
  const tree = await mount(t, async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "outcome_list") return input.offset === 0 ? { items: [{ id: "a", objective: "First outcome", state: "accepted" }], next_offset: 1 } : page.promise;
    return { id: input.id, revision: 4, author: "operator", authority: "operator", missing_references: [], document: { objective: "First outcome", state: "accepted", criteria: "Pass checks", conclusion: "Delivered\nVerified", source_issue: "https://example.com/issue", judgment: "Evidence checked" } };
  });
  assert.deepEqual(calls.map(({ operation, input }) => [operation, input.offset, input.limit]), [["outcome_list", 0, 1], ["outcome_list", 1, 1]]);
  assert.equal(tree.root.findByType("fieldset").props.disabled, false);
  await click(tree, "First outcome");
  assert.equal(calls.at(-1).operation, "outcome_read");
  assert.equal(calls.at(-1).input.revision, 0);
  assert.deepEqual(tree.root.findAllByType("h4").map(words), ["Criteria", "Conclusion", "Judgment", "References"]);
  assert.doesNotMatch(words(tree.root), /Missing references|Successful work is not acceptance/);
  await act(async () => page.resolve({ items: [{ id: "a", objective: "First outcome", state: "accepted" }, { id: "b", objective: "Second outcome", state: "open" }], next_offset: 0 }));
  await click(tree, "All outcomes");
  assert.equal(tree.root.findAllByType("li").length, 2);
  assert.doesNotMatch(words(tree.root), /Browse outcomes|Next outcomes|Refresh/);
});

test("project switch discards old outcome pages and stops their pagination", async t => {
  const old = deferred(), calls = [];
  const call = async (operation, input) => { calls.push(input); return input.project_id === "project" ? old.promise : { items: [{ id: "new", objective: "New project" }], next_offset: 0 }; };
  const tree = await mount(t, call);
  await act(async () => tree.update(createElement(ProjectOutcomes, { project: "other", call })));
  await act(async () => old.resolve({ items: [{ id: "old", objective: "Old project" }], next_offset: 1 }));
  assert.equal(calls.length, 2);
  assert.match(words(tree.root), /New project/); assert.doesNotMatch(words(tree.root), /Old project/);
});

test("stalled pagination keeps rows and exposes a recovery action", async t => {
  let calls = 0;
  const tree = await mount(t, async () => ({ items: [{ id: "a", objective: "Available outcome" }], next_offset: ++calls > 2 ? 0 : 1 }));
  assert.equal(calls, 2); assert.equal(tree.root.findAllByType("li").length, 1);
  assert.match(words(tree.root.findByProps({ role: "alert" })), /Could not load remaining outcomes/);
  await click(tree, "Retry");
  assert.equal(calls, 3); assert.equal(tree.root.findAllByProps({ role: "alert" }).length, 0);
});

test("editing replaces the reader, preserves comparison fields and expected revision, then reloads the list", async t => {
  const calls = [], document = { kind: "comparison", objective: "Compare", criteria: "Same source", state: "proposed", baseline: { task_id: "base", task_work_revision: 2 }, candidates: [{ task_id: "candidate", task_work_revision: 3 }], links: [{ task_id: "linked", task_work_revision: 1 }], question: "Which?" };
  const tree = await mount(t, async (operation, input) => { calls.push({ operation, input }); return operation === "outcome_list" ? { items: [{ id: "a", objective: "Compare" }] } : { id: "a", revision: operation === "outcome_write" ? 6 : 5, document: operation === "outcome_write" ? input.document : document }; });
  await click(tree, "Compare"); await click(tree, "Edit outcome");
  assert.equal(tree.root.findAllByType("article").length, 0);
  const values = { objective: "Compare", criteria: "Same source", state: "accepted", question: "Which?", "baseline.task_id": "base", "baseline.task_work_revision": "2", "candidate0.task_id": "candidate", "candidate0.task_work_revision": "3", "candidate1.task_id": "other", "candidate1.task_work_revision": "1" };
  const original = globalThis.FormData; globalThis.FormData = class { get(key) { return values[key] ?? ""; } };
  try { await act(async () => tree.root.findByType("form").props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = original; }
  const write = calls.find(({ operation }) => operation === "outcome_write").input;
  assert.equal(write.expected_revision, 5); assert.equal(write.project_id, "project");
  assert.deepEqual(write.document.baseline, document.baseline); assert.deepEqual(write.document.links, document.links);
  assert.equal(calls.at(-1).operation, "outcome_list"); assert.equal(tree.root.findAllByType("article").length, 1);
});


test("equivalent callback replacement preserves an open editor without restarting pagination", async t => {
  const calls = [];
  const call = async (operation) => { calls.push(operation); return { items: [] }; };
  const tree = await mount(t, call);
  await click(tree, "New outcome");
  await act(async () => tree.update(createElement(ProjectOutcomes, { project: "project", call: (...args) => call(...args) })));
  assert.equal(tree.root.findAllByType("form").length, 1);
  assert.deepEqual(calls, ["outcome_list"]);
});

test("leaving during a write preserves the write but never restarts the closed list", async () => {
  const write = deferred(), calls = [];
  let tree;
  await act(async () => { tree = create(createElement(ProjectOutcomes, { project: "project", call: async (operation) => { calls.push(operation); return operation === "outcome_write" ? write.promise : { items: [] }; } })); });
  await click(tree, "New outcome");
  const original = globalThis.FormData; globalThis.FormData = class { get() { return ""; } };
  try { await act(async () => tree.root.findByType("form").props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = original; }
  await act(async () => tree.unmount());
  await act(async () => write.resolve({ id: "new", revision: 1, document: {} }));
  assert.deepEqual(calls, ["outcome_list", "outcome_write"]);
});
