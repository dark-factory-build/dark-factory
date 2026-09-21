import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { MissionsPanel } from "../dist/src/missions-panel.js";
import { fixtureState } from "../../../fixtures/state.mjs";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const project = [...fixtureState.projects.keys()][1];
const overseer = [...fixtureState.agents.values()].find((agent) => agent.project_id === project && agent.role === "orchestrator");
const button = (tree, label) => tree.root.findAllByType("button").find((node) => node.children.join("") === label);

test("mission drafts survive state updates and creation waits for acknowledgement", async () => {
  const calls = [];
  let acknowledge;
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "outcome_list") return { items: [] };
    if (operation === "mission_tasks") return { tasks: [] };
    if (operation === "mission_create") return new Promise((resolve) => { acknowledge = resolve; });
    throw new Error("unexpected request");
  };
  let tree;
  const props = { state: fixtureState, projectId: project, active: true, call, onProject() {} };
  await act(async () => { tree = create(createElement(MissionsPanel, props)); });
  assert.equal(calls[0].input.kind, "mission");
  await act(async () => button(tree, "New mission").props.onClick());
  await act(async () => tree.root.findAllByType("textarea")[0].props.onChange({ target: { value: "Ship the operator interface" } }));
  await act(async () => tree.root.findAllByType("textarea")[1].props.onChange({ target: { value: "Verified on desktop and phone" } }));
  await act(async () => tree.update(createElement(MissionsPanel, { ...props, state: { ...fixtureState, head: fixtureState.head + 1n } })));
  assert.equal(tree.root.findAllByType("textarea")[0].props.value, "Ship the operator interface");
  await act(async () => tree.root.findByType("form").props.onSubmit({ preventDefault() {} }));
  const request = calls.find((entry) => entry.operation === "mission_create");
  assert.equal(request.input.owner_agent_id, overseer.id);
  assert.equal(request.input.objective, "Ship the operator interface");
  assert.equal(button(tree, "Create mission").props.disabled, true);
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /Mission saved and queued/);
  await act(async () => acknowledge({ id: request.input.id, owner_agent_id: overseer.id, document: { kind: "mission", objective: request.input.objective, criteria: request.input.criteria, state: "open" } }));
  assert.equal(tree.root.findAllByType("form").length, 0);
  assert.match(JSON.stringify(tree.toJSON()), /Mission saved and queued/);
  assert.match(JSON.stringify(tree.toJSON()), /Awaiting the overseer/);
  await act(async () => tree.unmount());
});

test("switching project fences an old mission read", async () => {
  const other = [...fixtureState.projects.keys()][0];
  let late;
  const call = async (operation, input) => {
    if (operation === "outcome_list" && input.project_id === project) return new Promise((resolve) => { late = resolve; });
    return { items: [] };
  };
  let tree;
  const props = { state: fixtureState, active: true, call, onProject() {} };
  await act(async () => { tree = create(createElement(MissionsPanel, { ...props, projectId: project })); });
  await act(async () => tree.update(createElement(MissionsPanel, { ...props, projectId: other })));
  await act(async () => late({ items: [{ id: "old", objective: "Wrong project mission", kind: "mission", state: "accepted" }] }));
  assert.doesNotMatch(JSON.stringify(tree.toJSON()), /Wrong project mission/);
  await act(async () => tree.unmount());
});

test("an open mission reconciles after reconnect without discarding a draft", async () => {
  let reads = 0;
  const mission = { id: "objective", objective: "Deliver the floor", state: "open" };
  const call = async (operation) => {
    if (operation === "outcome_list") return { items: [mission] };
    if (operation === "mission_tasks") return { tasks: [] };
    if (operation === "outcome_read") { reads++; return { ...mission, document: { ...mission, criteria: "Real evidence", remaining_work: reads === 1 ? "Review" : "Deliver" } }; }
    throw new Error("unexpected request");
  };
  const props = { state: fixtureState, projectId: project, active: true, call, onProject() {} };
  let tree;
  await act(async () => { tree = create(createElement(MissionsPanel, props)); });
  await act(async () => tree.root.findAllByType("button").find((node) => node.children[0] === mission.objective).props.onClick());
  await act(async () => button(tree, "New mission").props.onClick());
  await act(async () => tree.root.findAllByType("textarea")[0].props.onChange({ target: { value: "Next objective draft" } }));
  await act(async () => tree.update(createElement(MissionsPanel, { ...props, call: undefined })));
  assert.match(JSON.stringify(tree.toJSON()), /last observed mission state/);
  await act(async () => tree.update(createElement(MissionsPanel, props)));
  assert.equal(reads, 2);
  assert.equal(tree.root.findAllByType("textarea")[0].props.value, "Next objective draft");
  assert.match(JSON.stringify(tree.toJSON()), /Deliver/);
  await act(async () => tree.unmount());
});

test("mission details lead with action and open related tasks without a nested inspector", async () => {
  const opened = [], mission = { id: "mission", objective: "Deliver changes", state: "open" };
  const task = { task_id: "task", project_id: project, title: "Related task", status: "succeeded", revision: "1", priority: 0 };
  let tree;
  const call = async operation => operation === "outcome_list" ? {items: [mission]} : operation === "mission_tasks" ? {tasks: [task]} : {...mission, document: {...mission, criteria: "Acceptance evidence", remaining_work: "Deploy it"}};
  await act(async () => { tree = create(createElement(MissionsPanel, {state: fixtureState, projectId: project, active: true, call, onProject() {}, onOpenTask: task => opened.push(task)})); });
  await act(async () => tree.root.findAllByType("button").find(node => node.children[0] === mission.objective).props.onClick());
  const details = tree.root.findAllByType("details").find(node => node.findByType("summary").children.join("") === "Acceptance criteria");
  assert.ok(!details.props.open);
  const output = JSON.stringify(tree.toJSON());
  assert.ok(output.indexOf("Deploy it") < output.indexOf("Acceptance evidence"));
  await act(async () => tree.root.findAllByType("button").find(node => node.children.join("").includes("Related task")).props.onClick());
  assert.equal(opened[0].id, "task");
  assert.equal(tree.root.findAllByProps({"aria-label": "Work details"}).length, 0);
  await act(async () => button(tree, "← Back to Missions").props.onClick());
  assert.ok(tree.root.findAllByType("button").some(node => node.children[0] === mission.objective));
  await act(async () => tree.unmount());
});
