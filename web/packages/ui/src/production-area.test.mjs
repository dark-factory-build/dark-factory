import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { ProductionArea, productionConnector, sharedChecks, productionHeight } from "../dist/src/production-area.js";

test("shared CI is one execution across PRs but repository identities remain distinct", () => {
  const check = { id: "ci:1", repository: "owner/one", scope: "merge_group" };
  const items = [{ checks: [check] }, { checks: [check, { ...check, repository: "owner/two" }] }];
  assert.equal(sharedChecks(items).length, 2);
  assert.equal(sharedChecks([{ checks: [check], pullRequest: { state: "merged" } }]).length, 0);
  assert.ok(productionHeight(320, 0) < productionHeight(320, 1));
  assert.ok(productionHeight(320, 20) > productionHeight(320, 2));
});

test("production connector is rendered from the two room rectangles", () => {
  const upper = { x: 48, y: 120, width: 200, height: 80 };
  const lower = { x: 8, y: 240, width: 304, height: 120 };
  assert.deepEqual(productionConnector(upper, lower), { x: 48, y: 200, width: 32, height: 40 });
  const markup = renderToStaticMarkup(createElement(ProductionArea, {
    items: [], width: 320, top: lower.y - 8, upperRoom: upper, onSelect() {},
  }));
  assert.match(markup, /<rect x="48" y="-32" width="32" height="40" fill="url\(#df-floor\)"><\/rect>/);
  assert.match(markup, /M8 8H48 M80 8H312/);
});

test("production boxes expose result stickers and only show a live reviewer", () => {
  const item = {
    projectId: "project", visualId: "change:1", repository: "owner/repo", tasks: [], missions: [], linksOverflow: false,
    pullRequest: { title: "A simple change", state: "open", head: "a" }, construction: undefined,
    review: { head: "a", state: "running", current: true, allowed: false, sourceFresh: true, findings: "", url: "" },
    checks: [{ id: "ci", repository: "owner/repo", name: "CI", revision: "a", scope: "head", state: "completed", conclusion: "success", pull_requests: [1], jobs: [], overflow: 0, applicable: true }],
    deliveries: [], reviewers: [{ id: "reviewer", number: 1, head: "a", name: "Rae", provider: "codex", state: "running" }],
    completed: false, completedAt: 0, status: "open", nextAction: "", blockedReason: "",
  };
  const render = (value) => renderToStaticMarkup(createElement(ProductionArea, { items: [value], width: 320, top: 0, upperRoom: { x: 8, y: -80, width: 304, height: 40 }, pulse: 1000, onSelect: () => {} }));
  const live = render(item);
  assert.match(live, /Review running/);
  assert.match(live, /CI passed/);
  assert.match(live, /Rae, reviewer, working/);
  assert.doesNotMatch(live, /data-contraption-variant/);
  const blocked = render({ ...item, blockedReason: "Needs your answer" });
  assert.match(blocked, /Blocked: Needs your answer/);
  const longReason = "Independent review is required before publication can proceed.";
  const boundedBlocked = render({ ...item, blockedReason: longReason });
  assert.doesNotMatch(boundedBlocked, new RegExp(`>Blocked: ${longReason}</text>`));
  assert.match(boundedBlocked, new RegExp(`Blocked: ${longReason}`));
  const finished = render({ ...item, completed: true, status: "delivered", review: { ...item.review, sourceFresh: false }, reviewers: [{ ...item.reviewers[0], state: "allow" }] });
  assert.doesNotMatch(finished, /Rae, reviewer, working/);
});

test("production boxes render every result sticker set", () => {
  const base = {
    projectId: "project", visualId: "change:stickers", repository: "owner/repo", tasks: [], missions: [], linksOverflow: false,
    pullRequest: { title: "Sticker change", state: "open", head: "a" }, construction: undefined,
    review: { head: "a", state: "allow", current: true, allowed: true, sourceFresh: true, findings: "", url: "" }, checks: [], deliveries: [], deliveryDestinations: [], reviewers: [],
    completed: false, completedAt: 0, status: "open", nextAction: "", blockedReason: "",
  };
  const render = (value) => renderToStaticMarkup(createElement(ProductionArea, { items: [value], width: 320, top: 0, upperRoom: { x: 8, y: -80, width: 304, height: 40 }, onSelect: () => {} }));
  assert.match(render(base), /Review passed/);
  assert.match(render({ ...base, review: { ...base.review, allowed: false, state: "pending" } }), /Review pending/);
  assert.match(render({ ...base, review: { ...base.review, current: false, head: "b" } }), /Review stale/);
  assert.match(render({ ...base, review: { ...base.review, state: "running", allowed: false }, checks: [{ id: "ci", applicable: true, state: "running", conclusion: "" }] }), /CI running/);
  assert.match(render({ ...base, checks: [{ id: "ci", applicable: true, state: "completed", conclusion: "failure" }] }), /CI failed/);
  assert.match(render({ ...base, pullRequest: { ...base.pullRequest, merge_queue: "main" } }), /Merge queued/);
  assert.match(render({ ...base, pullRequest: { ...base.pullRequest, state: "merged" }, completed: true, status: "merged" }), /Merge merged/);
  assert.match(render({ ...base, deliveryDestinations: ["production"] }), /Delivery pending/);
  assert.match(render({ ...base, deliveryDestinations: ["production"], deliveries: [{ destination: "production", verified: true }] }), /Delivery passed/);
});

test("a running reviewer stays at the box after the walk-in window", async () => {
  const item = {
    projectId: "project", visualId: "change:long-review", repository: "owner/repo", tasks: [], missions: [], linksOverflow: false,
    pullRequest: { title: "Long review", state: "open", head: "a" }, construction: undefined,
    review: { head: "a", state: "running", current: true, allowed: false, sourceFresh: true, findings: "", url: "" },
    checks: [], deliveries: [], reviewers: [{ id: "reviewer", number: 1, head: "a", name: "Rae", provider: "codex", state: "running" }],
    completed: false, completedAt: 0, status: "open", nextAction: "", blockedReason: "",
  };
  const props = { items: [item], width: 320, top: 0, upperRoom: { x: 8, y: -80, width: 304, height: 40 }, pulse: 0, onSelect: () => {} };
  let renderer;
  await act(async () => { renderer = create(createElement(ProductionArea, props)); });
  await act(async () => { renderer.update(createElement(ProductionArea, { ...props, pulse: 2_000 })); });
  assert.match(JSON.stringify(renderer.toJSON()), /Rae, reviewer, working/);
  await act(async () => renderer.unmount());
});

test("a finished reviewer remains in leaving motion until its exit window ends", async () => {
  const item = {
    projectId: "project", visualId: "change:departure", repository: "owner/repo", tasks: [], missions: [], linksOverflow: false,
    pullRequest: { title: "Finished review", state: "open", head: "a" }, construction: undefined,
    review: { head: "a", state: "running", current: true, allowed: false, sourceFresh: true, findings: "", url: "" },
    checks: [], deliveries: [], reviewers: [{ id: "reviewer", number: 1, head: "a", name: "Rae", provider: "codex", state: "running" }],
    completed: false, completedAt: 0, status: "open", nextAction: "", blockedReason: "",
  };
  const props = { items: [item], width: 320, top: 0, upperRoom: { x: 8, y: -80, width: 304, height: 40 }, pulse: 0, onSelect: () => {} };
  let renderer;
  await act(async () => { renderer = create(createElement(ProductionArea, props)); });
  const finished = { ...item, review: { ...item.review, state: "allow", allowed: true }, reviewers: [] };
  await act(async () => { renderer.update(createElement(ProductionArea, { ...props, items: [finished], pulse: 1_000 })); });
  assert.match(JSON.stringify(renderer.toJSON()), /Rae, reviewer, leaving/);
  await act(async () => { renderer.update(createElement(ProductionArea, { ...props, items: [finished], pulse: 1_400 })); });
  assert.match(JSON.stringify(renderer.toJSON()), /Rae, reviewer, leaving/);
  await act(async () => { renderer.update(createElement(ProductionArea, { ...props, items: [finished], pulse: 2_000 })); });
  assert.doesNotMatch(JSON.stringify(renderer.toJSON()), /Rae, reviewer, leaving/);
  await act(async () => renderer.unmount());
});
