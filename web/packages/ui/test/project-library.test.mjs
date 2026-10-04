import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { ProjectLibrary } from "../dist/src/project-library.js";
import { fixtureState } from "../../../fixtures/state.mjs";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
test("library is lazy and reads immutable body pages only on demand", async () => {
  const calls = [];
  const metadata = { id: "ab".repeat(16), revision: 2, latest_revision: 3, kind: "procedure", title: "Optional guide", description: "", author: "worker", source_references: "" };
  const call = async (operation, input) => { calls.push({ operation, input }); if (operation === "list") return { items: [metadata] }; if (operation === "read") return metadata; if (operation === "body") return { body: "read me", complete: true }; throw new Error("unexpected operation"); };
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, call })); });
  assert.equal(calls.length, 0);
  const click = async (label) => { const button = renderer.root.findAllByType("button").find((button) => button.children.join("").includes(label)); assert.ok(button, label); await act(async () => { await button.props.onClick(); }); };
  await click("Browse library");
  assert.deepEqual(calls.map((call) => call.operation), ["list"]);
  await click("Optional guide");
  assert.deepEqual(calls.map((call) => call.operation), ["list", "read"]);
  assert.equal(renderer.root.findByType("pre").children.length, 0);
  await click("Read body");
  assert.equal(calls[2].input.revision, 2);
  assert.equal(calls[2].input.limit, 8192);
  assert.deepEqual(renderer.root.findByType("pre").children, ["read me"]);
  const revise = renderer.root.findAllByType("button").find((button) => button.children.join("") === "Revise document");
  assert.equal(revise.props.disabled, true, "superseded revision cannot be edited as current");
  await act(async () => renderer.unmount());
});

test("board shares immutable threads, resolves by revision, and retains linked conclusions", async () => {
  const calls = [], source = `${[...fixtureState.projects.keys()][0]}:node-stable`;
  const metadata = { id: "ab".repeat(16), revision: 2, latest_revision: 2, repository_id: "cd".repeat(16), kind: "discussion", title: "Root cause", description: "finding", author: "reviewer", source_references: JSON.stringify({ status: "tentative", entities: [source], evidence: ["test:regression"], branch: "topic", environment: "staging", pinned: false, resolved: false }) };
  let current = metadata;
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "search") return { items: input.kind === "discussion_reply" ? [] : [current], next_offset: 0 };
    if (operation === "read") return current;
    if (operation === "body") return { body: "Concrete evidence, not a permission grant.", complete: true };
    if (operation === "revise") { current = { ...current, ...input, revision: current.revision + 1, latest_revision: current.revision + 1 }; return current; }
    if (operation === "create") return { ...input, revision: 1, latest_revision: 1, author: "operator" };
    if (operation === "accesses") return { items: [{ content_revision: input.revision, kind: "read", run_id: "run", offset: 0, byte_length: 12 }] };
    throw new Error(`unexpected ${operation}`);
  };
  const opened = [];
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, call, board: true, entity: source, repository: metadata.repository_id, onSource: (ref) => opened.push(ref) })); });
  const click = async (label) => { const button = renderer.root.findAllByType("button").find((button) => button.children.join("").includes(label)); assert.ok(button, label); await act(async () => { button.props.onClick(); }); };
  assert.equal(calls.length, 0);
  await click("New discussion");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "repository_id").props.defaultValue, metadata.repository_id, "entity entry point seeds the editor repository");
  await click("Cancel");
  await click("Browse discussions");
  assert.equal(calls[0].input.entity, source);
  assert.equal(calls[0].input.limit, 4);
  await click("Root cause");
  assert.equal(renderer.root.findAllByType("button").find((button) => button.children.join("") === "Resolve discussion").props.disabled, true);
  await click("Read body");
  await click("Read replies");
  assert.deepEqual(calls.at(-1).input, { project_id: [...fixtureState.projects.keys()][0], repository_id: metadata.repository_id, branch: "topic", environment: "staging", thread_id: metadata.id, kind: "discussion_reply", offset: 0, limit: 4 });
  await click("Task access");
  assert.ok(renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")));
  await click("Resolve discussion");
  assert.ok(!renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")), "new root revision clears previous revision receipts");
  const revised = calls.find((value) => value.operation === "revise").input;
  assert.equal(revised.expected_revision, 2);
  assert.equal(revised.body, "Concrete evidence, not a permission grant.");
  assert.equal(JSON.parse(revised.source_references).resolved, true);
  assert.equal(current.revision, 3);
  await click("Source:"); assert.deepEqual(opened, [source]);
  await click("Task access"); assert.equal(calls.at(-1).input.revision, 3);
  await click("Reply");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "kind").props.defaultValue, "discussion_reply");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "thread_id").props.defaultValue, metadata.id);
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "repository_id").props.defaultValue, metadata.repository_id);
  await click("Cancel");
  assert.deepEqual(renderer.root.findByType("pre").children, ["Concrete evidence, not a permission grant."], "cancelling a reply preserves the loaded root body");
  await click("Retain conclusion");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "kind").props.defaultValue, "lesson");
  assert.match(renderer.root.findAllByType("textarea").find((input) => input.props.name === "evidence").props.defaultValue, /content:abab.*@3/);
  const originalFormData = globalThis.FormData;
  const values = { kind: "lesson", repository_id: metadata.repository_id, title: "Retained conclusion", body: "Use exact authority", status: "tentative", evidence: `content:${metadata.id}@3`, entities: source, thread_id: metadata.id, branch: "topic", environment: "staging" };
  globalThis.FormData = class { get(name) { return values[name] ?? null; } };
  try { await act(async () => renderer.root.findByProps({ "aria-label": "Knowledge editor" }).props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = originalFormData; }
  assert.ok(!renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")), "new conclusion clears another document's receipts");
  assert.equal(calls.at(-1).operation, "create");
  assert.equal(calls.at(-1).input.repository_id, metadata.repository_id);
  assert.ok(!calls.some((value) => /task|reply|deliver/.test(value.operation)), "discussion actions never invoke task delivery");
  await act(async () => renderer.unmount());
});

test("ID-only links resolve latest through explicit metadata revisions; historical selections stay pinned", async () => {
  const rootID = "ab".repeat(16), lessonID = "cd".repeat(16), priorID = "ef".repeat(16), calls = [];
  const documents = new Map([[rootID, { title: "Discussion", kind: "discussion", latest_revision: 3, source_references: JSON.stringify({ status: "current" }) }], [lessonID, { title: "Lesson", kind: "lesson", latest_revision: 2, source_references: JSON.stringify({ status: "current", thread_id: rootID, supersedes: priorID }) }], [priorID, { title: "Earlier lesson", kind: "lesson", latest_revision: 4, source_references: JSON.stringify({ status: "superseded" }) }]]);
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "read") { assert.ok(Number.isSafeInteger(input.revision) && input.revision > 0, "real store rejects revision zero"); const doc = documents.get(input.id); assert.ok(doc); return { ...doc, id: input.id, revision: input.revision }; }
    if (operation === "list") return { items: [{ ...documents.get(lessonID), id: lessonID, revision: 1 }], next_offset: 0 };
    throw new Error(`unexpected ${operation}`);
  };
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, call, initialID: lessonID })); });
  const click = async (label) => { await act(async () => renderer.root.findAllByType("button").find((button) => button.children.join("").includes(label)).props.onClick()); };
  const reads = () => calls.filter((call) => call.operation === "read").map(({ input }) => [input.id, input.revision]);
  assert.deepEqual(reads(), [[lessonID, 1], [lessonID, 2]], "initial ID opens the current immutable revision");
  await click("Original discussion:");
  assert.deepEqual(reads().slice(-2), [[rootID, 1], [rootID, 3]]);
  await click("Browse library"); await click("Lesson · lesson · r1");
  assert.deepEqual(reads().at(-1), [lessonID, 1], "explicit historical selection never advances to latest");
  await click("Supersedes:");
  assert.deepEqual(reads().slice(-2), [[priorID, 1], [priorID, 4]]);
  await act(async () => renderer.unmount());
});
