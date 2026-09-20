import assert from "node:assert/strict";
import test from "node:test";
import { deriveProductionView } from "./production-view.ts";

const record = (kind, id, visual_id, document, extra = {}) => ({ repository: "owner/repo", kind, id, visual_id, observed_at: 10, document, tasks: [], missions: [], ...extra });
const head = "a".repeat(40);

test("production view keeps one contraption identity and refuses stale review or merge-group checks", () => {
  const view = deriveProductionView([
    record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, state: "open", review: { head: "b".repeat(40), state: "allow" } }),
    record("check", "ci-head", "", { id: "ci-head", name: "CI", revision: head, scope: "head", state: "completed", conclusion: "success", pull_requests: [7] }),
    record("check", "ci-queue", "", { id: "ci-queue", name: "Queue", revision: "c".repeat(40), scope: "merge_group", state: "completed", conclusion: "success", pull_requests: [7] }),
  ]);
  const machine = view.contraptions["change:1"];
  assert.equal(machine.review.allowed, false);
  assert.equal(machine.review.current, false);
  assert.equal(machine.checks.find((check) => check.id === "ci-head")?.applicable, true);
  assert.equal(machine.checks.find((check) => check.id === "ci-queue")?.applicable, false);
});

test("merged is not delivered, while verified delivery completes the rack", () => {
  const merged = deriveProductionView([record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, merge: "d".repeat(40), state: "merged", review: { head, state: "allow" } })]).contraptions["change:1"];
  assert.equal(merged.completed, false);
  assert.match(merged.nextAction, /delivery verification/);
  const delivered = deriveProductionView([
    record("pull_request", "7", "change:1", { number: 7, title: "Machine", head, merge: "d".repeat(40), state: "merged", review: { head, state: "allow" } }),
    record("delivery", "deploy-1", "", { id: "deploy-1", kind: "site", destination: "production", revision: "d".repeat(40), state: "verified", verified_at: 20, pull_requests: [7] }),
  ]).contraptions["change:1"];
  assert.equal(delivered.completed, true);
  assert.equal(delivered.delivery.verified, true);
});

test("construction preserves operator document and stays on the active rack", () => {
  const machine = deriveProductionView([record("construction", "c1", "change:c1", { title: "Build station", phase: "blocked", status: "blocked", head, task_id: "task-1", blocked_reason: "Needs review" }, { tasks: ["task-1"], missions: ["mission-1"] })]).contraptions["change:c1"];
  assert.equal(machine.completed, false);
  assert.equal(machine.construction.blocked_reason, "Needs review");
  assert.deepEqual(machine.tasks, ["task-1"]);
});
