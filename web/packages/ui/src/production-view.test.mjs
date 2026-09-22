import assert from "node:assert/strict";
import test from "node:test";
import { deriveProductionView, inProgressProduction, productionStages } from "./production-view.ts";

const record = (kind, id, visual_id, document, extra = {}) => ({ repository: "owner/repo", project_id: "project", kind, id, visual_id, observed_at: 10, document, tasks: [], missions: [], ...extra });
const head = "a".repeat(40);
const key = "project\0owner/repo\0change:1";

test("production view scopes same PR numbers and refuses stale review or merge-group checks", () => {
  const view = deriveProductionView([
    record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, state: "open", review: { head: "b".repeat(40), state: "allow" } }),
    record("check", "ci-head", "", { name: "CI", revision: head, scope: "head", state: "completed", conclusion: "success", pull_requests: [7], jobs: [{ id: "job", name: "gate", state: "completed", conclusion: "success", url: "" }], overflow: 2 }),
    record("check", "ci-queue", "", { name: "Queue", revision: "c".repeat(40), scope: "merge_group", state: "completed", conclusion: "success", pull_requests: [7] }),
    record("check", "ci-other-repo", "", { name: "Other", revision: head, scope: "head", state: "completed", conclusion: "success", pull_requests: [7] }, { repository: "other/repo" }),
  ]);
  const machine = view.contraptions[key];
  assert.equal(machine.review.allowed, false);
  assert.equal(machine.review.current, false);
  assert.equal(machine.checks.find((check) => check.id === "ci-head")?.applicable, true);
  assert.equal(machine.checks.find((check) => check.id === "ci-queue")?.applicable, false);
  assert.equal(machine.checks.find((check) => check.id === "ci-head")?.overflow, 2);
  assert.equal(machine.checks.some((check) => check.id === "ci-other-repo"), false);
});

test("merged is incomplete until every known destination is verified", () => {
  const base = { number: 7, title: "Machine", head, merge: "d".repeat(40), state: "merged", review: { head, state: "allow" } };
  const incomplete = deriveProductionView([record("pull_request", "7", "change:1", base), record("delivery", "deploy-site", "", { kind: "site", destination: "production", revision: base.merge, state: "verified", verified_at: 20, pull_requests: [7] }), record("delivery", "deploy-runtime", "", { kind: "runtime", destination: "mac", revision: base.merge, state: "running", pull_requests: [7] })]).contraptions[key];
  assert.equal(incomplete.completed, false);
  assert.equal(incomplete.deliveries.length, 2);
  const complete = deriveProductionView([record("pull_request", "7", "change:1", base), record("delivery", "deploy-site", "", { kind: "site", destination: "production", revision: base.merge, state: "verified", verified_at: 20, pull_requests: [7] }), record("delivery", "deploy-runtime", "", { kind: "runtime", destination: "mac", revision: base.merge, state: "verified", verified_at: 21, pull_requests: [7] })]).contraptions[key];
  assert.equal(complete.completed, true);
});

test("stale repository health blocks actionability but preserves historical review", () => {
  const view = deriveProductionView([record("repository", "owner/repo", "", { unavailable: "" }, { observed_at: 1 }), record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, state: "open", review: { head, state: "allow" } })], 200_000);
  const machine = view.contraptions[key];
  assert.equal(machine.review.current, true);
  assert.equal(machine.review.allowed, false);
  assert.equal(machine.review.sourceFresh, false);
  assert.match(machine.nextAction, /stale or unavailable/);
});

test("construction and PR with one visual identity remain one contraption", () => {
  const machine = deriveProductionView([record("construction", "c1", "change:1", { title: "Build station", phase: "blocked", status: "blocked", head, task_id: "task-1", blocked_reason: "Needs review" }, { tasks: ["task-1"], missions: ["mission-1"] }), record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, state: "open", review: { head, state: "unknown" } })]).contraptions[key];
  assert.equal(machine.pullRequest.number, 7);
  assert.equal(machine.construction.blocked_reason, "Needs review");
  assert.deepEqual(machine.tasks, ["task-1"]);
});


test("fresh repository reads cannot keep old active processes running", () => {
  const view = deriveProductionView([
    record("repository", "owner/repo", "", {}, { observed_at: 200_000 }),
    record("pull_request", "7", "change:1", { number: 7, head, state: "open", review: { head, state: "allow" } }, { observed_at: 200_000 }),
    record("check", "old", "", { revision: head, scope: "head", state: "in_progress", pull_requests: [7] }),
    record("reviewer", "old", "", { number: 7, head, state: "running" }),
  ], 200_000);
  const machine = view.contraptions[key];
  assert.equal(machine.checks[0].state, "stale");
  assert.equal(machine.reviewers[0].state, "stale");
  assert.match(machine.nextAction, /unavailable or incomplete/);
});


test("a later verified attempt supersedes failure at the same destination", () => {
  const pr = { number: 7, title: "Machine", head, merge: "d".repeat(40), state: "merged" };
  const failed = record("delivery", "old", "", { destination: "site", state: "failed", updated_at: 15, pull_requests: [7, 8] });
  const verified = record("delivery", "new", "", { destination: "site", state: "verified", updated_at: 25, verified_at: 25, pull_requests: [7, 8] });
  const view = deriveProductionView([record("pull_request", "7", "change:1", pr), failed, verified, { ...verified, project_id: "second-project" }]);
  assert.equal(view.contraptions[key].completed, true);
  assert.equal(view.contraptions[key].deliveries.length, 2);
  assert.deepEqual(view.contraptions[key].deliveries.map((delivery) => delivery.pull_requests), [[7, 8], [7, 8]]);
  const pending = deriveProductionView([record("pull_request", "7", "change:1", pr), verified, { ...failed, document: { ...failed.document, updated_at: 30 } }]);
  assert.equal(pending.contraptions[key].completed, false);
});


test("tied delivery timestamps never let success hide an unresolved attempt", () => {
  const pr = record("pull_request", "7", "change:1", { number: 7, head, state: "merged", merge: head });
  const success = record("delivery", "older", "", { destination: "site", revision: head, state: "verified", verified_at: 1000, updated_at: 2000, pull_requests: [7] });
  for (const state of ["blocked", "running", "unknown"]) {
    const unresolved = record("delivery", "newer", "", { destination: "site", revision: "b".repeat(40), state, updated_at: 2000, pull_requests: [7] });
    for (const order of [[success, unresolved], [unresolved, success]]) {
      const view = deriveProductionView([pr, ...order]);
      assert.equal(view.contraptions[key].completed, false);
      assert.match(view.contraptions[key].nextAction, /pending/);
      assert.equal(view.deliveries["project\0owner/repo\0newer"].state, state);
    }
  }
});


test("re-reading an old running release receipt does not prove continuing installation activity", () => {
  const view = deriveProductionView([record("delivery", "attempt", "", { destination: "host", state: "running", updated_at: 1, pull_requests: [] }, { observed_at: 200_000 })], 200_000);
  assert.equal(view.deliveries["project\0owner/repo\0attempt"].state, "stale");
});


test("in-progress membership excludes delivered and proven unchanged finished work", () => {
  const make = (document) => Object.values(deriveProductionView([record("construction", "c", "change:c", document)]).contraptions)[0];
  const finished = make({ status: "succeeded", phase: "retained", has_changes: false });
  assert.equal(inProgressProduction(finished), false);
  assert.match(finished.nextAction, /finished without a source change/);
  const unpublished = make({ status: "succeeded", phase: "retained", has_changes: true });
  assert.equal(inProgressProduction(unpublished), true);
  assert.deepEqual(productionStages(unpublished), ["Publication unrecorded"]);
  assert.doesNotMatch(unpublished.nextAction, /in progress/);
  assert.equal(inProgressProduction(make({ status: "blocked", has_changes: false })), true);
  assert.equal(inProgressProduction(make({ status: "failed", has_changes: false })), true);
  assert.equal(inProgressProduction(make({ status: "cancelled", has_changes: true })), false);
  assert.equal(inProgressProduction({ ...unpublished, completed: true }), false);
});

test("stage labels retain parallel review and CI and distinguish unverified delivery", () => {
  const records = [record("repository", "owner/repo", "", {}), record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, state: "open", review: { head, state: "running" } }), record("check", "ci", "", { revision: head, scope: "head", state: "running", pull_requests: [7] })];
  const machine = deriveProductionView(records).contraptions[key];
  assert.deepEqual(productionStages(machine), ["Review running", "CI running"]);
  assert.deepEqual(productionStages({ ...machine, review: { ...machine.review, state: "unknown" }, reviewers: [{ state: "running" }] }), ["Review running", "CI running"]);
  assert.deepEqual(productionStages({ ...machine, pullRequest: { ...machine.pullRequest, state: "merged" } }), ["Merged", "Delivery unverified"]);
  assert.deepEqual(productionStages({ ...machine, review: { ...machine.review, sourceFresh: false } }), ["Review stale", "CI stale"]);
});
