import assert from "node:assert/strict";
import test from "node:test";
import { prepareFloor, selectFloor, projectProposals, projectFloor, MAX_FLOOR_ROOMS } from "../dist/src/console-view.js";
import { deriveProductionView, productionKey } from "../dist/src/production-view.js";
import { layoutScene } from "../dist/src/factory-scene/scene.js";
const counts = (source, tests = 0) => ({ source, tests, documentation: 0, configuration: 0, assets: 0, unclassified: 0 });
const inv = (source, tests = 0) => ({ direct: counts(source, tests), total: counts(source, tests), samples: ["movement.go"], samples_omitted: Math.max(0, source + tests - 1) });
const node = (id, path, parent_id, inventory = inv(1), kind = "directory") => ({ id, path, parent_id, inventory, kind, label: path, language: "", size_bucket: "small" });
const root = node("root", ".", "", inv(2), "repository");
const nodes = [root, node("module", ".", "root", inv(2), "module"), node("internal", "internal", "module", inv(0)), node("kernel", "internal/kernel", "internal", inv(8, 4), "package"), node("store", "internal/kernel/store", "kernel", inv(3)), node("web", "web", "module", inv(0)), node("ui", "web/ui", "web", inv(5, 2), "module"), node("scene", "web/ui/scene", "ui", inv(6, 3))];
const project = { id: "project", name: "Factory" }, projects = new Map([[project.id, project]]);
const topology = { projectId: project.id, digest: "source", sourceRevision: "a".repeat(40), nodes };
const prepare = (data = topology) => prepareFloor(projects, new Map([[project.id, data]]));
const record = (id, paths, overrides = {}) => ({ project_id: "project", repository: "owner/factory", kind: "pull_request", id, visual_id: id, observed_at: 1000, tasks: [], missions: [], document: { number: Number(id), title: `Change ${id}`, head: "b".repeat(40), state: "open", review: { head: "b".repeat(40), state: "allow" }, source: { kind: "committed", base: "a".repeat(40), head: "b".repeat(40), observed_at: 1000, paths, omitted: 0 }, ...overrides } });
const items = (records, now = 1000) => Object.values(deriveProductionView([{ project_id: "project", repository: "owner/factory", kind: "repository", id: "repo", visual_id: "", observed_at: 1000, document: {}, tasks: [], missions: [] }, ...records], now).contraptions);

test("flat detail preserves every canonical source once across same-path wrappers", () => {
  const prepared = prepare(), identity = [...prepared.roomByID.keys()];
  for (const detail of ["coarse", "auto", "fine"]) {
    const floor = selectFloor(prepared, detail), assemblies = floor.topology.nodes.flatMap((room) => room.assemblies);
    assert.equal(new Set(assemblies.map((assembly) => assembly.id)).size, assemblies.length);
    assert.equal(assemblies.reduce((sum, assembly) => sum + assembly.inventory.total.source, 0), 24);
    const represented = assemblies.flatMap((assembly) => assembly.representedIds);
    assert.equal(new Set(represented).size, represented.length, "each canonical area has one physical owner");
    for (const source of floor.canonical.filter((source) => Object.values(source.inventory?.direct ?? {}).some(Boolean))) assert.ok(represented.includes(source.id));
    assert.equal(floor.topology.nodes.reduce((sum, room) => sum + room.inventory.total.source, 0), 24);
    assert.equal(floor.visibleAncestor("project:root"), floor.visibleAncestor("project:module"));
    assert.deepEqual([...prepared.roomByID.keys()], identity);
    for (const id of identity) assert.ok(floor.visibleAncestor(id));
  }
  assert.ok(selectFloor(prepared, "auto").topology.nodes.some((room) => room.path === "internal/kernel"));
  assert.ok(selectFloor(prepared, "auto").topology.nodes.some((room) => room.path === "web/ui"));
  assert.ok(selectFloor(prepared, "coarse").topology.nodes.length < selectFloor(prepared, "fine").topology.nodes.length);
});

test("bounded aggregation retains ownership and active observations beyond room cap", () => {
  const many = Array.from({ length: 160 }, (_, i) => node(`child${i}`, `component-${String(i).padStart(3, "0")}`, root.id));
  const prepared = prepare({ ...topology, nodes: [root, ...many] }), floor = selectFloor(prepared, "fine");
  assert.equal(floor.topology.nodes.length, MAX_FLOOR_ROOMS);
  assert.equal(floor.aggregatedLocations, 65);
  for (const source of prepared.roomByID.values()) assert.ok(floor.visibleAncestor(source.id));
  const state = { projects, agents: new Map([["actor", { id: "actor", project_id: "project", name: "Worker", role: "worker" }]]), tasks: new Map([["task", { id: "task", project_id: "project", assigned_agent_id: "actor", status: "running", revision: 1n, title: "Edit hidden area" }]]), humanRequests: new Map() };
  const scene = projectFloor(state, floor, new Map([["actor", { taskId: "task", taskRevision: 1n, runId: "run", projectId: "project", paths: ["component-159/fix.go"] }]]));
  assert.equal(scene.tasks[0].roomIds[0], "project:child159");
  assert.ok(scene.tasks[0].displayRoomId);
  assert.equal(scene.omittedLocations, 0);
});

test("automatic detail assembles command entry points in one stable source area", () => {
  const commandNodes = [node("cmd", "cmd", "module", inv(0)), node("ctl", "cmd/factoryctl", "cmd", inv(8, 4), "package"), node("daemon", "cmd/factoryd", "cmd", inv(2), "package")];
  const prepared = prepare({ ...topology, nodes: [...nodes, ...commandNodes] });
  const floor = selectFloor(prepared), commandRoom = floor.topology.nodes.find((room) => room.path === "cmd");
  assert.deepEqual(commandRoom.assemblies.map((assembly) => assembly.id), ["project:ctl", "project:daemon"]);
  assert.equal(commandRoom.inventory.total.source, 10);
  assert.equal(floor.visibleAncestor("project:ctl"), "project:cmd");
  assert.equal(selectFloor(prepared, "fine").visibleAncestor("project:ctl"), "project:ctl");
  const changed = selectFloor(prepare({ ...topology, nodes: [...nodes, ...commandNodes.map((item) => item.id === "ctl" ? { ...item, inventory: inv(90, 40) } : item)] }));
  const geometry = (value) => layoutScene(value.topology).rooms.map(({ id, x, y, width, height }) => ({ id, x, y, width, height }));
  assert.deepEqual(geometry(changed), geometry(floor), "ordinary file count changes do not rearrange rooms");
});

test("overlap stays separate; additions, abandonment and merge labels never move finished rooms", () => {
  const floor = selectFloor(prepare()), changes = items([record("1", [{ status: "modified", path: "internal/kernel/movement.go" }, { status: "added", path: "new/area/dispatcher.go" }, { status: "deleted", path: "web/ui/old.ts" }, { status: "renamed", old_path: "web/ui/scene/movement.ts", path: "new/area/routes.ts" }]), record("2", [{ status: "modified", path: "internal/kernel/movement.go" }])]);
  const projected = projectProposals(floor, changes);
  assert.equal(projected.proposals.length, 2);
  assert.equal(projected.proposals[0].operations[0].entityId, projected.proposals[1].operations[0].entityId);
  assert.notEqual(projected.proposals[0].id, projected.proposals[1].id);
  assert.deepEqual(projected.proposals[0].operations.map((op) => op.kind), ["modification", "addition", "removal", "move"]);
  assert.equal(projected.proposals[0].operations[3].previousPath, "web/ui/scene/movement.ts");
  const before = layoutScene(floor.topology), after = layoutScene(projected.topology);
  for (const room of before.rooms) assert.deepEqual(after.rooms.find((other) => other.id === room.id), room);
  assert.equal(projected.topology.nodes.filter((room) => room.proposed).length, 1);
  for (const state of ["closed", "merged"]) assert.equal(projectProposals(floor, items([record("1", [], { state })])).proposals.length, 0);
  assert.deepEqual(floor.topology, selectFloor(prepare()).topology);
});

test("dirty and mismatched heads cannot inherit approval or checks", () => {
  const check = { project_id: "project", repository: "owner/factory", kind: "check", id: "check", visual_id: "", observed_at: 1000, tasks: [], missions: [], document: { revision: "b".repeat(40), scope: "head", state: "completed", conclusion: "success", pull_requests: [1] } };
  for (const source of [{ kind: "working-tree", head: "b".repeat(40), observation: "dirty" }, { kind: "committed", head: "c".repeat(40) }]) {
    const [item] = items([record("1", [], { source: { ...source, observed_at: 1000, paths: [{ status: "modified", path: "internal/kernel/movement.go" }] } }), check]);
    assert.equal(item.review.current, false); assert.equal(item.review.allowed, false); assert.equal(item.checks[0].applicable, false);
  }
  assert.equal(items([record("1", [])], 200000)[0].source.stale, true);
});

test("unsafe and unavailable source stays explicit without invented paths", () => {
  const [item] = items([record("1", [{ status: "added", path: "../escape" }, { status: "renamed", path: "new/path", old_path: "/private/source" }, { status: "modified", path: "safe.go" }])]);
  assert.equal(item.source.paths.length, 1); assert.equal(item.source.omitted, 2);
  const unknown = items([record("2", [], { source: undefined })]);
  assert.equal(projectProposals(selectFloor(prepare()), unknown).proposals[0].state, "unavailable");
  assert.equal(unknown[0].source.paths.length, 0);
});

test("one reviewer run does not multiply per affected path", () => {
  const reviewer = { project_id: "project", repository: "owner/factory", kind: "reviewer", id: "run1", visual_id: "", observed_at: 1000, tasks: [], missions: [], document: { number: 1, head: "b".repeat(40), name: "Ada", state: "running", provider: "codex" } };
  const changes = items([record("1", [{ status: "modified", path: "internal/kernel/a.go" }, { status: "modified", path: "web/ui/b.ts" }]), reviewer]), actors = projectProposals(selectFloor(prepare()), changes).reviewers;
  assert.equal(actors.length, 1); assert.match(actors[0].review.scope, /Individual file inspection is not observed/);
  assert.equal(actors[0].review.proposalId, productionKey(changes[0]));
});


test("source inspectors normalize aliases, containment and relationship targets to their canonical owners", () => {
  const prepared = prepare({ ...topology, dependencies: { omitted: 0, edges: [{ from: "root", to: "kernel", weight: 1 }] } });
  const { detailByID } = selectFloor(prepared);
  assert.equal(detailByID.has("project:root"), false, "same-path wrapper is not a duplicate source inspector");
  assert.equal(detailByID.get("project:module").inventory.direct.source, 2);
  assert.deepEqual(detailByID.get("project:module").components.map((node) => node.id), ["project:internal", "project:web"]);
  assert.equal(detailByID.get("project:module").dependencies.links[0].nodeId, "project:kernel");
  assert.equal(detailByID.get("project:kernel").dependencies.links[0].nodeId, "project:module");
});


test("new-area versions retain observed resource classes and source-backed dependency changes", () => {
  const change = record("1", [{ status: "added", path: "new/area/code.go", resource: "source" }, { status: "added", path: "new/area/code_test.go", resource: "tests" }, { status: "added", path: "new/area/README.md", resource: "documentation" }, { status: "added", path: "new/area/rules.json", resource: "configuration" }]);
  change.document.source.relationships = [{ status: "added", from_path: "new/area", to_path: "internal/kernel", weight: 2 }, { status: "removed", from_path: "web/ui", to_path: "internal/kernel", weight: 1 }, { status: "added", from_path: "../unsafe", to_path: "web/ui", weight: 1 }];
  const changed = items([change]), projected = projectProposals(selectFloor(prepare()), changed);
  const newArea = projected.topology.nodes.find((node) => node.proposed);
  assert.deepEqual(newArea.assemblies[0].inventory.direct, { source: 1, tests: 1, documentation: 1, configuration: 1, assets: 0, unclassified: 0 });
  assert.equal(newArea.assemblies[0].sourceIncomplete, true, "bounded observed paths do not assert final scale");
  assert.deepEqual(projected.proposals[0].relationships.map((edge) => edge.status), ["added", "removed"]);
  assert.equal(projected.proposals[0].relationships[0].fromId, newArea.id);
  assert.equal(projected.proposals[0].relationships[0].toId, "project:kernel");
  assert.equal(changed[0].source.relationshipsOmitted, 1);
});


test("automatic areas keep wide namespaces and package resources together without losing exact owners", () => {
  const packages = Array.from({ length: 8 }, (_, index) => node(`p${index}`, `internal/p${index}`, "internal", inv(index === 0 ? 100 : 5, 2), "package"));
  const inventory = { ...inv(0), direct: { ...counts(0), documentation: 4 }, total: { ...counts(0), documentation: 4 } };
  const source = [root, node("internal", "internal", "root", inv(0)), ...packages,
    node("tests", "internal/p1/test", "p1", inv(0, 8)), node("manuals", "internal/p1/docs", "p1", inventory)];
  const prepared = prepare({ ...topology, nodes: source });
  const auto = selectFloor(prepared), fine = selectFloor(prepared, "fine");
  assert.ok(auto.topology.nodes.length < fine.topology.nodes.length / 2);
  assert.ok(auto.topology.nodes.some((room) => room.path === "internal"));
  assert.ok(auto.topology.nodes.some((room) => room.path === "internal/p0"));
  const assembly = auto.topology.nodes.flatMap((room) => room.assemblies).find((assembly) => assembly.id === "project:p1");
  assert.deepEqual(assembly.inventory.total, { ...counts(5, 10), documentation: 4 });
  assert.deepEqual(assembly.inventory.direct, counts(5, 2), "aggregate scale never overwrites canonical direct contents");
  assert.deepEqual(assembly.representedIds, ["project:p1", "project:manuals", "project:tests"]);
  for (const id of assembly.representedIds) assert.ok(auto.detailByID.has(id) && fine.detailByID.has(id));
});
