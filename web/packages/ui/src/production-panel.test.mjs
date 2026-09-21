import assert from "node:assert/strict";
import test from "node:test";
import { createElement, useState } from "react";
import { act, create } from "react-test-renderer";
import { renderToStaticMarkup } from "react-dom/server";
import { ProductionPanel } from "../dist/src/production-panel.js";

const active = { visualId: "change:1", projectId: "project", repository: "owner/repo", tasks: ["task-1", "task-2"], missions: ["mission-1"], pullRequest: { number: 7, title: "Machine", head: "a".repeat(40), state: "open", url: "https://github.com/owner/repo/pull/7" }, review: { head: "a".repeat(40), state: "allow", current: true, allowed: true, sourceFresh: true, findings: "", url: "" }, reviewers: [], checks: [{ id: "check-1", repository: "owner/repo", name: "CI", revision: "a".repeat(40), scope: "head", state: "running", conclusion: "", pull_requests: [7], jobs: [], overflow: 0, applicable: true }], deliveries: [], completed: false, status: "open", nextAction: "Current-head checks are running." };
const completed = { ...active, visualId: "change:2", pullRequest: { ...active.pullRequest, title: "Delivered", state: "merged" }, completed: true, status: "delivered" };

test("production queue excludes completed work and shows simultaneous review and CI stages", () => {
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [active, completed], onSelect() {}, connected: true }));
  assert.match(markup, /Machine/);
  assert.match(markup, /Review/);
  assert.match(markup, /CI/);
  assert.doesNotMatch(markup, /Delivered/);
  assert.doesNotMatch(markup, /<select/);
});

test("a selected completed item remains inspectable", () => {
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [active, completed], selected: "project:change:2", onSelect() {}, connected: true }));
  assert.match(markup, /Delivered/);
  assert.match(markup, /Work queue/);
});

test("a queue row selects controlled detail and review prose hides its receipt marker", async () => {
  function Controlled() { const [selected, setSelected] = useState(); return createElement(ProductionPanel, { items: [active], selected, onSelect: setSelected, connected: true }); }
  let tree;
  await act(async () => { tree = create(createElement(Controlled)); });
  await act(async () => { tree.root.findAllByType("button").find((node) => node.children.join("").includes("Machine")).props.onClick(); });
  assert.match(JSON.stringify(tree.toJSON()), /Work queue/);
  const marker = `<!-- dark-factory-operation:12345678-1234-1234-1234-123456789abc:${"e".repeat(64)} -->`;
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [{ ...active, review: { ...active.review, findings: `Useful finding.\n${marker}` } }], selected: "project:change:1", onSelect() {}, connected: true }));
  assert.match(markup, /Useful finding/);
  assert.doesNotMatch(markup, /dark-factory-operation/);
  await act(async () => tree.unmount());
});

test("switching tasks fences an old request and keeps the selected known task", async () => {
  let reject; const pending = new Promise((_, fail) => { reject = fail; }); let tree;
  const known = { id: "task-2", project_id: "project", assigned_agent_id: "agent", title: "Known task", status: "queued", priority: 0, revision: 1n };
  await act(async () => { tree = create(createElement(ProductionPanel, { items: [active], selected: "project:change:1", onSelect() {}, state: { tasks: new Map([[known.id, known]]), projects: new Map(), agents: new Map() }, call: () => pending })); });
  const button = (needle) => tree.root.findAllByType("button").find((node) => node.children.join("").includes(needle));
  await act(async () => { button("task-1").props.onClick(); });
  await act(async () => { button("Known task").props.onClick(); });
  await act(async () => { reject(new Error("old request")); await pending.catch(() => {}); });
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /Task details are unavailable|Loading task/);
  await act(async () => tree.unmount());
});
