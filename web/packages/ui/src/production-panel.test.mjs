import assert from "node:assert/strict";
import test from "node:test";
import { createElement, useState } from "react";
import { act, create } from "react-test-renderer";
import { renderToStaticMarkup } from "react-dom/server";
import { productionKey } from "../dist/src/production-view.js";
import { ProductionPanel } from "../dist/src/production-panel.js";

const active = { source: { kind: "unavailable", base: "", head: "", observation: "", observedAt: 0, paths: [], relationships: [], relationshipsOmitted: 0, relationshipsUnavailable: "unavailable", omitted: 0, reason: "unavailable", stale: true }, visualId: "change:1", projectId: "project", repository: "owner/repo", tasks: ["task-1", "task-2"], missions: ["mission-1"], pullRequest: { number: 7, title: "Machine", head: "a".repeat(40), state: "open", url: "https://github.com/owner/repo/pull/7" }, review: { head: "a".repeat(40), state: "allow", current: true, allowed: true, sourceFresh: true, findings: "", url: "" }, reviewers: [], checks: [{ id: "check-1", repository: "owner/repo", name: "CI", revision: "a".repeat(40), scope: "head", state: "running", conclusion: "", pull_requests: [7], jobs: [], overflow: 0, applicable: true }], deliveries: [], completed: false, status: "open", nextAction: "Current-head checks are running." };
const completed = { ...active, visualId: "change:2", pullRequest: { ...active.pullRequest, title: "Delivered", state: "merged" }, completed: true, status: "delivered" };

test("production queue excludes completed work and shows simultaneous review and CI stages", () => {
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [active, completed], onSelect() {}, connected: true }));
  assert.match(markup, /Machine/);
  assert.match(markup, /Review/);
  assert.match(markup, /CI/);
  assert.match(markup, /Change history[\s\S]*Delivered/);
  assert.doesNotMatch(markup, /<select/);
});

test("a pull request's delivery receipt is never read as current confirmation while disconnected", () => {
  const delivered = { ...completed, deliveries: [{ repository: "owner/repo", id: "release", kind: "release", destination: "runtime:host-1", revision: "b".repeat(40), state: "verified", pull_requests: [7], verified_at: 1000 }] };
  const markup = (props) => renderToStaticMarkup(createElement(ProductionPanel, { items: [delivered], selected: productionKey(completed), onSelect() {}, ...props }));
  assert.match(markup({ connected: true }), /runtime:host-1 · verified/);
  assert.match(markup({ connected: false }), /last recorded verified; not current confirmation/);
  assert.match(renderToStaticMarkup(createElement(ProductionPanel, { items: [active], selected: productionKey(active), onSelect() {}, connected: true })), /No delivery evidence recorded/);
});

test("a selected completed item remains inspectable", () => {
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [active, completed], selected: productionKey(completed), onSelect() {}, connected: true }));
  assert.match(markup, /Delivered/);
  assert.match(markup, /Back to Changes/);
});

test("a queue row selects controlled detail and review prose hides its receipt marker", async () => {
  function Controlled() { const [selected, setSelected] = useState(); return createElement(ProductionPanel, { items: [active], selected, onSelect: setSelected, connected: true }); }
  let tree;
  await act(async () => { tree = create(createElement(Controlled)); });
  await act(async () => { tree.root.findAllByType("button").find((node) => node.findAllByType("strong").some((title) => title.children.join("") === "Machine")).props.onClick(); });
  assert.match(JSON.stringify(tree.toJSON()), /Back to Changes/);
  const marker = `<!-- dark-factory-operation:12345678-1234-1234-1234-123456789abc:${"e".repeat(64)} -->`;
  const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [{ ...active, review: { ...active.review, findings: `Useful finding.\n${marker}` } }], selected: productionKey(active), onSelect() {}, connected: true }));
  assert.match(markup, /Useful finding/);
  assert.doesNotMatch(markup, /dark-factory-operation/);
  await act(async () => tree.unmount());
});

test("switching tasks fences an old request and keeps the selected known task", async () => {
  const opened = []; let reject; const pending = new Promise((_, fail) => { reject = fail; }); let tree;
  const known = { id: "task-2", project_id: "project", assigned_agent_id: "agent", title: "Known task", status: "queued", priority: 0, revision: 1n };
  await act(async () => { tree = create(createElement(ProductionPanel, { items: [active], selected: productionKey(active), onSelect() {}, state: { tasks: new Map([[known.id, known]]), projects: new Map(), agents: new Map() }, call: () => pending, onOpenTask: (task) => opened.push(task) })); });
  const button = (needle) => tree.root.findAllByType("button").find((node) => node.children.join("").includes(needle));
  await act(async () => { button("task-1").props.onClick(); });
  await act(async () => { button("Known task").props.onClick(); });
  await act(async () => { reject(new Error("old request")); await pending.catch(() => {}); });
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /Task details are unavailable|Loading task/);
  assert.deepEqual(opened, [known]);
  await act(async () => tree.unmount());
});

test("review prose hides the canonical terminal receipt, preserving human examples", () => {
  const marker = `<!-- dark-factory-operation:12345678-1234-1234-1234-123456789abc:${"e".repeat(64)} -->`;
  const render = (findings) => renderToStaticMarkup(createElement(ProductionPanel, { items: [{ ...active, review: { ...active.review, findings } }], onSelect() {}, selected: productionKey(active), connected: true }));
  const markup = render(`Useful finding.\nKeep this conclusion.\n\n${marker}\n`);
  assert.match(markup, /Useful finding\./);
  assert.match(markup, /Keep this conclusion\./);
  assert.doesNotMatch(markup, /dark-factory-operation|12345678/);
  assert.match(render("Human example: <!-- dark-factory-operation:deadbeef -->"), /deadbeef/);
  assert.match(render(`${marker}\nHuman explanation after the example.`), /12345678/);
  assert.match(render(`Inline example ${marker}`), /12345678/);
});

test("leaving Production prevents a late task read opening a dialog", async () => {
  let resolve, tree; const opened = [];
  const props = { items: [active], selected: productionKey(active), onSelect() {}, onOpenTask: task => opened.push(task), call: () => new Promise(done => { resolve = done; }) };
  await act(async () => { tree = create(createElement(ProductionPanel, props)); });
  await act(async () => { tree.root.findAllByType("button").find(node => node.children.join("").includes("task-1")).props.onClick(); });
  await act(async () => { tree.update(createElement(ProductionPanel, { ...props, active: false })); });
  await act(async () => { resolve({ task_id: "task-1", title: "Completed task", revision: "1", status: "succeeded" }); });
  assert.deepEqual(opened, []);
  await act(async () => tree.unmount());
});

test("one Show more control expands loaded work before requesting another page", async () => {
  let requested = 0, tree;
  const items = Array.from({length: 13}, (_, i) => ({ ...active, visualId: `change:${i}` }));
  await act(async () => { tree = create(createElement(ProductionPanel, { items, onSelect() {}, overflow: 20, loadMore: () => requested++ })); });
  const more = () => tree.root.findAllByType("button").filter(node => node.children.join("") === "Show more");
  assert.equal(more().length, 1);
  await act(async () => more()[0].props.onClick());
  assert.equal(requested, 0);
  assert.equal(tree.root.findAllByProps({className: "dfProduction__row"}).length, 13);
  assert.equal(more().length, 1);
  await act(async () => more()[0].props.onClick());
  assert.equal(requested, 1);
  await act(async () => tree.unmount());
});


test("source disclosure distinguishes observed empty files from unavailable and omitted edits", () => {
  const complete = { ...active.source, kind: "committed", base: "b".repeat(40), head: active.pullRequest.head, target: "refs/heads/main", stale: false, reason: "", relationshipsUnavailable: "" };
  for (const [source, expected, status] of [
    [complete, "No observed file edits.", ""],
    [{ ...complete, kind: "unavailable", reason: "Snapshot missing" }, "File edits unavailable.", "Source details unavailable"],
    [{ ...complete, omitted: 3 }, "File edits are outside this observation.", "Source details incomplete"],
  ]) {
    const item = { ...active, source };
    const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [item], selected: productionKey(item), onSelect() {} }));
    assert.match(markup, /<details aria-label="Before and proposed source"><summary>Source changes<\/summary>/);
    assert.ok(markup.includes(expected));
    assert.match(markup, /Target <code>refs\/heads\/main<\/code>/);
    if (status) {
      assert.ok(markup.includes(status));
      assert.doesNotMatch(markup, /No observed file edits|0 observed/);
    } else assert.doesNotMatch(markup, /Source details unavailable|Source details incomplete/);
  }
});

test("disconnected source and revision evidence remains readable without enabled work actions", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const item = { ...active, source: { ...active.source, kind: "working-tree", base: "b".repeat(40), head: active.pullRequest.head, observation: "observed-dirty-tree", paths: [{ status: "renamed", old_path: "before.go", path: "after.go" }], reason: "", relationshipsUnavailable: "" } };
  let tree;
  try {
    await act(async () => { tree = create(createElement(ProductionPanel, { items: [item], selected: productionKey(item), onSelect() {}, connected: false, onOpenTask() {}, onMission() {} })); });
    const source = tree.root.findByProps({ "aria-label": "Before and proposed source" });
    assert.equal(source.props.open, undefined);
    const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [item], selected: productionKey(item), onSelect() {}, connected: false }));
    for (const evidence of ["Last observed state", "Disconnected. This is the last observed state.", "Source details out of date", "before.go", "after.go", "observed-dirty-tree", "Commit checks and approval do not cover these edits.", active.pullRequest.head]) assert.ok(markup.includes(evidence));
    assert.ok(tree.root.findAllByType("button").filter((button) => button.children.join("").startsWith("Open ")).every((button) => button.props.disabled));
    assert.equal(tree.root.findAllByType("button").find((button) => button.props.children.some?.((child) => child === "Back to Changes")).props.disabled, undefined);
  } finally { if (tree) await act(async () => tree.unmount()); }
});


test("missing or omitted relationship evidence never asserts no dependency changes", () => {
  for (const [kind, omitted, unavailable, expected] of [
    ["unavailable", 0, "", "Static dependency changes unavailable."],
    ["committed", 3, "", "Dependency changes are outside this observation."],
    ["committed", 0, "Analysis refused", "Analysis refused"],
    ["committed", 0, "", "No static dependency changes in this observation."],
  ]) {
    const item = { ...active, source: { ...active.source, kind, relationships: [], relationshipsOmitted: omitted, relationshipsUnavailable: unavailable } };
    const markup = renderToStaticMarkup(createElement(ProductionPanel, { items: [item], selected: productionKey(item), onSelect() {} }));
    assert.ok(markup.includes(expected));
    if (kind === "unavailable" || omitted || unavailable) assert.doesNotMatch(markup, /No static dependency changes/);
    if (omitted) assert.match(markup, /3 relationship changes omitted/);
  }
});
