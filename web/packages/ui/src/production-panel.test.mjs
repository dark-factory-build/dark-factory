import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { renderToStaticMarkup } from "react-dom/server";
import { ProductionPanel } from "../dist/src/production-panel.js";

const item = {
  visualId: "change:1", projectId: "project", repository: "owner/repo", tasks: ["task-1"], missions: ["mission-1"],
  pullRequest: { number: 7, title: "Machine", head: "a".repeat(40), state: "merged", merge: "b".repeat(40), url: "https://github.com/owner/repo/pull/7" },
  review: { head: "a".repeat(40), state: "allow", current: true, allowed: true, sourceFresh: true, findings: "", url: "https://github.com/owner/repo/pull/7#pullrequestreview-1" },
  reviewers: [{ id: "review-1", repository: "owner/repo", number: 7, head: "a".repeat(40), name: "Independent", provider: "codex", state: "allow" }],
  checks: [{ id: "check-1", repository: "owner/repo", name: "CI", revision: "a".repeat(40), scope: "head", state: "completed", conclusion: "success", pull_requests: [7], jobs: [{ id: "job-1", name: "Gate", state: "completed", conclusion: "success" }], overflow: 0, applicable: true }],
  deliveries: [{ id: "delivery-1", repository: "owner/repo", kind: "site", destination: "production", revision: "b".repeat(40), state: "verified", verified_at: 20, pull_requests: [7], verified: true }],
  completed: true, status: "delivered", nextAction: "Delivery verified at all recorded destinations.",
};

test("production panel exposes evidence and external links without authority controls", () => {
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [item], onSelect() {}, selected: "project:change:1", connected: true }));
  assert.match(markup, /Machine/);
  assert.match(markup, /Open pull request/);
  assert.match(markup, /Independent/);
  assert.match(markup, /Gate/);
  assert.match(markup, /production · verified/);
  assert.doesNotMatch(markup, />Merge</);
  assert.doesNotMatch(markup, />Run CI</);
});

test("production panel uses a native bounded selector and does not mount every task detail", () => {
  const withTasks = { ...item, tasks: ["task-1", "task-2"] };
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [withTasks], onSelect() {}, selected: "project:change:1", connected: true }));
  assert.match(markup, /<select/);
  assert.match(markup, /task-1/);
  assert.doesNotMatch(markup, /Work details/);
});

test("review prose removes only stored operation markers", () => {
  const findings = "Useful finding.\n<!-- dark-factory-operation:deadbeef -->\nKeep this conclusion.";
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [{ ...item, review: { ...item.review, findings } }], onSelect() {}, selected: "project:change:1", connected: true }));
  assert.match(markup, /Useful finding\./);
  assert.match(markup, /Keep this conclusion\./);
  assert.doesNotMatch(markup, /dark-factory-operation|deadbeef/);
});


test("switching from a pending task to a known task fences the old failure", async () => {
  let reject;
  const pending = new Promise((_, fail) => { reject = fail; });
  let tree;
  const known = { id: "task-2", project_id: "project", assigned_agent_id: "agent", title: "Known task", status: "queued", priority: 0, revision: 1n };
  await act(async () => { tree = create(createElement(ProductionPanel, {
    items: [{ ...item, tasks: ["task-1", "task-2"] }], selected: "project:change:1", onSelect() {},
    state: { tasks: new Map([[known.id, known]]), projects: new Map(), agents: new Map() }, call: () => pending,
  })); });
  const button = (needle) => tree.root.findAllByType("button").find((node) => node.children.join("").includes(needle));
  await act(async () => { button("task-1").props.onClick(); });
  assert.match(JSON.stringify(tree.toJSON()), /Loading task/);
  await act(async () => { button("Known task").props.onClick(); });
  await act(async () => { reject(new Error("old request")); await pending.catch(() => {}); });
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /Task detail is unavailable|Loading task/);
  await act(async () => tree.unmount());
});
