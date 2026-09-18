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
