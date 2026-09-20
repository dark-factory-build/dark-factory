import assert from "node:assert/strict";
import test from "node:test";
import { deriveProductionView } from "./production-view.ts";

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
