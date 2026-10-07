import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { FactoryFloor } from "../dist/src/console-screens.js";
import { projectGraph, projectProposals, projectFloor } from "../dist/src/console-view.js";
import { deriveProductionView, productionKey } from "../dist/src/production-view.js";
import { layoutScene } from "../dist/src/factory-scene/scene.js";
import { hex, node as graphNode, unit, graphWith, observed, quiet } from "../../../fixtures/graph.mjs";

const project = { id: "project", name: "Factory" }, projects = new Map([[project.id, project]]);
const prepare = (graph, detail) => projectGraph(new Map([[project.id, graph]]), [project.id], detail);
// alpha (a server) with routes and a store, web (a browser), a shared queue, an external and runtime-only activity.
const route = (n, extra = {}) => graphNode(n, "ingress", `/r${n}`, { unit: hex(1), trigger: "request", paths: [`internal/alpha/r${n}.go`], ...extra });
const base = (docks = 3) => [
  unit(1, "alpha", ["internal/alpha"]), unit(2, "web", ["web"], { runtime: "browser" }),
  ...Array.from({ length: docks }, (_, index) => route(10 + index)),
  graphNode(30, "store", "state.db", { unit: hex(1), paths: ["internal/alpha/store"] }),
  graphNode(40, "queue", "work queue"), graphNode(50, "external", "github"),
  graphNode(60, "unknown", "POST /mystery", { evidence: "runtime", ...observed }),
];
const ids = (list) => list.map((item) => item.id);
const record = (id, paths, overrides = {}) => ({ project_id: "project", repository: "owner/factory", kind: "pull_request", id, visual_id: id, observed_at: 1000, tasks: [], missions: [], document: { number: Number(id), title: `Change ${id}`, head: "b".repeat(40), state: "open", review: { head: "b".repeat(40), state: "allow" }, source: { kind: "committed", base: "a".repeat(40), head: "b".repeat(40), observed_at: 1000, paths, omitted: 0 }, ...overrides } });
const items = (records, now = 1000) => Object.values(deriveProductionView([{ project_id: "project", repository: "owner/factory", kind: "repository", id: "repo", visual_id: "", observed_at: 1000, document: {}, tasks: [], missions: [] }, ...records], now).contraptions);

test("request docks fold into one manifold in auto, stay whole in fine and vanish in coarse", () => {
  const few = prepare(graphWith(base(3))).graph.halls.find((hall) => hall.id === hex(1));
  assert.equal(few.machines.filter((machine) => machine.kind === "ingress").length, 3, "up to six docks are all shown");
  const nodes = base(8);
  const hallOf = (detail) => prepare(graphWith(nodes), detail).graph.halls.find((hall) => hall.id === hex(1));
  const auto = hallOf("auto").machines.filter((machine) => machine.kind === "ingress");
  assert.equal(auto.length, 1);
  assert.equal(auto[0].represented.length, 8);
  assert.equal(hallOf("fine").machines.filter((machine) => machine.kind === "ingress").length, 8);
  assert.equal(hallOf("fine").machines.some((machine) => machine.represented !== undefined), false);
  assert.equal(hallOf("coarse").machines.length, 0, "the plant view keeps halls only");
  // Every folded route still resolves to the manifold, so a path touching one marks it.
  const folded = prepare(graphWith(nodes));
  assert.equal(folded.where.get(hex(13)).machine, auto[0].id);
  assert.equal(folded.locate(project.id, "internal/alpha/r13.go").machine, auto[0].id);
});

test("runtime-only and unknown nodes are quarantined, externals become gates and shared stores go to the yard", () => {
  const { graph, where } = prepare(graphWith(base(2)));
  assert.deepEqual(ids(graph.quarantine), [hex(60)]);
  assert.equal(graph.halls.some((hall) => hall.machines.some((machine) => machine.id === hex(60))), false, "never inside a hall");
  assert.deepEqual(ids(graph.parties), [hex(50)]);
  assert.deepEqual(ids(graph.shared), [hex(40)]);
  assert.equal(where.get(hex(40)).hall, "yard");
  assert.equal(where.get(hex(60)).hall, "quarantine");
  assert.equal(where.get(hex(50)).hall, undefined);
  const layout = layoutScene(graph);
  assert.equal(layout.gates.length, 1);
  assert.deepEqual(layout.rooms.filter((room) => room.kind !== "hall").map((room) => [room.id, room.kind]), [["yard", "yard"], ["quarantine", "quarantine"]]);
  // A runtime-only node with a unit is still the runtime's claim, not the code's.
  const claimed = prepare(graphWith([...base(2), graphNode(61, "ingress", "/seen", { unit: hex(1), evidence: "runtime", ...observed })])).graph;
  assert.deepEqual(ids(claimed.quarantine), [hex(60), hex(61)].sort());
});

test("a folded manifold never claims idle unless every member is quiet", () => {
  const docks = (readings) => base(8).map((item, index) => index >= 2 && index < 10 ? { ...item, ...readings[index - 2] } : item);
  const reading = (name) => ({ quiet: quiet, observed: observed, unobserved: {} }[name]);
  const manifold = (names) => prepare(graphWith(docks(names.map(reading)))).graph.halls[0].machines.find((machine) => machine.represented !== undefined).reading;
  assert.equal(manifold(Array(8).fill("quiet")).state, "idle");
  assert.equal(manifold(Array(8).fill("quiet")).observation, "quiet");
  for (const names of [[..."quiet,quiet,quiet,quiet,quiet,quiet,quiet,unobserved".split(",")], [..."quiet,quiet,quiet,quiet,quiet,quiet,quiet,observed".split(",")]]) {
    const folded = manifold(names);
    assert.notEqual(folded.state, "idle", names.join());
    assert.equal(folded.observation, "partial");
  }
  assert.equal(manifold(Array(8).fill("observed")).state, "active");
  assert.equal(manifold(Array(8).fill("unobserved")).state, "unknown");
});

test("a worker stands at the machine whose path matches the run path, else at the unit holding it, else is unobserved", () => {
  const prepared = prepare(graphWith(base(2)));
  const state = { projects, agents: new Map([["actor", { id: "actor", project_id: "project", name: "Worker", role: "worker" }]]), tasks: new Map([["task", { id: "task", project_id: "project", assigned_agent_id: "actor", status: "running", revision: 1n, title: "Edit" }]]), humanRequests: new Map() };
  const at = (path) => projectFloor(state, prepared, new Map([["actor", { taskId: "task", taskRevision: 1n, runId: "run", projectId: "project", paths }]])).workers[0];
  const paths = [];
  const place = (...changed) => { paths.length = 0; paths.push(...changed); return at(); };
  const exact = place("internal/alpha/store/state.go");
  assert.deepEqual([exact.location, exact.nodeId, exact.observedBayId], ["working", hex(1), hex(30)]);
  const route = place("internal/alpha/r10.go");
  assert.equal(route.observedBayId, hex(10));
  const fallback = place("internal/alpha/other/thing.go");
  assert.deepEqual([fallback.location, fallback.nodeId, fallback.observedBayId], ["working", hex(1), hex(1)], "the unit whose module path contains it");
  const elsewhere = place("docs/readme.md");
  assert.equal(elsewhere.location, "unobserved");
  assert.equal(elsewhere.nodeId, undefined);
  assert.equal(place("web/src/app.ts").nodeId, hex(2));
  // A stale sample (another revision) is no evidence at all.
  assert.equal(projectFloor(state, prepared, new Map([["actor", { taskId: "task", taskRevision: 2n, runId: "run", projectId: "project", paths: ["web/x.ts"] }]])).workers[0].location, "unobserved");
});

test("flows join the machines that picture their endpoints, keep the busiest of parallel edges and skip a machine's own", () => {
  const nodes = base(8);
  const edge = (from, to, kind, extra = {}) => ({ from: hex(from), to: hex(to), kind, evidence: "static", observation: "unobserved", state: "unknown", ...extra });
  const { graph } = prepare(graphWith(nodes, [
    edge(2, 10, "calls"), edge(2, 11, "calls", observed), edge(10, 1, "handles"), edge(1, 30, "uses"), edge(1, 50, "calls"), edge(10, 60, "calls"), edge(10, 11, "calls"),
  ]));
  const folded = graph.halls[0].machines.find((machine) => machine.represented !== undefined).id;
  const flow = (from, to) => graph.flows.find((item) => item.from === from && item.to === to);
  assert.equal(flow(hex(2), folded).reading.ratePerHour, 60, "two routes behind one manifold are one belt, as busy as the busiest");
  assert.ok(flow(folded, hex(1)), "a folded route still hands its work to the unit");
  assert.ok(flow(hex(1), hex(30)) && flow(hex(1), hex(50)) && flow(folded, hex(60)), "stores, gates and quarantined crates are endpoints too");
  assert.equal(graph.flows.length, 5, "an edge between two routes of one manifold is inside one machine");
});

test("overlap stays separate; changes mark the machines they touch and never move a hall", () => {
  const prepared = prepare(graphWith(base(2)));
  const changes = items([record("1", [{ status: "modified", path: "internal/alpha/store/state.go" }, { status: "added", path: "new/area/dispatcher.go" }, { status: "deleted", path: "web/old.ts" }, { status: "renamed", old_path: "web/old-name.ts", path: "new/area/routes.ts" }]), record("2", [{ status: "modified", path: "internal/alpha/store/state.go" }])]);
  const projected = projectProposals(prepared, changes);
  assert.equal(projected.proposals.length, 2);
  assert.equal(projected.proposals[0].operations[0].entityId, hex(30));
  assert.equal(projected.proposals[0].operations[0].entityId, projected.proposals[1].operations[0].entityId);
  assert.notEqual(projected.proposals[0].id, projected.proposals[1].id);
  assert.deepEqual(projected.proposals[0].operations.map((op) => op.kind), ["modification", "addition", "removal", "move"]);
  assert.equal(projected.proposals[0].operations[1].entityId, undefined, "a path no machine owns is unplaced, never invented");
  assert.equal(projected.proposals[0].operations[2].roomId, hex(2));
  assert.equal(projected.proposals[0].operations[3].previousPath, "web/old-name.ts");
  for (const state of ["closed", "merged"]) assert.equal(projectProposals(prepared, items([record("1", [], { state })])).proposals.length, 0);
});

test("dirty and mismatched heads cannot inherit approval or checks", () => {
  const check = { project_id: "project", repository: "owner/factory", kind: "check", id: "check", visual_id: "", observed_at: 1000, tasks: [], missions: [], document: { revision: "b".repeat(40), scope: "head", state: "completed", conclusion: "success", pull_requests: [1] } };
  for (const source of [{ kind: "working-tree", head: "b".repeat(40), observation: "dirty" }, { kind: "committed", head: "c".repeat(40) }]) {
    const [item] = items([record("1", [], { source: { ...source, observed_at: 1000, paths: [{ status: "modified", path: "internal/alpha/movement.go" }] } }), check]);
    assert.equal(item.review.current, false); assert.equal(item.review.allowed, false); assert.equal(item.checks[0].applicable, false);
  }
  assert.equal(items([record("1", [])], 200000)[0].source.stale, true);
});

test("unsafe and unavailable source stays explicit without invented paths", () => {
  const [item] = items([record("1", [{ status: "added", path: "../escape" }, { status: "renamed", path: "new/path", old_path: "/private/source" }, { status: "modified", path: "safe.go" }])]);
  assert.equal(item.source.paths.length, 1); assert.equal(item.source.omitted, 2);
  const unknown = items([record("2", [], { source: undefined })]);
  assert.equal(projectProposals(prepare(graphWith(base(2))), unknown).proposals[0].state, "unavailable");
  assert.equal(unknown[0].source.paths.length, 0);
});

test("one reviewer run does not multiply per affected path", () => {
  const reviewer = { project_id: "project", repository: "owner/factory", kind: "reviewer", id: "run1", visual_id: "", observed_at: 1000, tasks: [], missions: [], document: { number: 1, head: "b".repeat(40), name: "Ada", state: "running", provider: "codex" } };
  const changes = items([record("1", [{ status: "modified", path: "internal/alpha/a.go" }, { status: "modified", path: "web/b.ts" }]), reviewer]), actors = projectProposals(prepare(graphWith(base(2))), changes).reviewers;
  assert.equal(actors.length, 1); assert.match(actors[0].review.scope, /Individual file inspection is not observed/);
  assert.equal(actors[0].review.proposalId, productionKey(changes[0]));
});


test("thirty proposals draw no floor Changes button or Help", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const records = Array.from({ length: 30 }, (_, i) => record(String(i + 1), [{ status: "modified", path: "internal/alpha/movement.go" }]));
  const changes = items(records), selected = [];
  const props = { state: { projects, agents: new Map(), tasks: new Map(), humanRequests: new Map() }, graphs: new Map([[project.id, graphWith(base(2))]]), changes, floorAppearance: { detail: "auto", social: "nearby" }, onSelectChange: (id) => selected.push(id) };
  const text = (value) => typeof value === "string" ? value : (value.children ?? []).map(text).join("");
  let tree;
  try {
    await act(async () => { tree = create(createElement(FactoryFloor, props)); });
    const header = () => tree.root.findByProps({ className: "dfFactoryEntityTools" });
    assert.equal(header().findAllByProps({ type: "search" }).length, 1);
    assert.deepEqual(header().findAllByType("button").map(text), []);
    assert.equal(header().findAllByType("details").length, 0);
    await act(async () => tree.update(createElement(FactoryFloor, { ...props, selectedChange: productionKey(changes[29]) })));
    assert.match(text(header()), /Viewing: Change 30/);
    assert.deepEqual(header().findAllByType("button").map(text), ["Clear selection"]);
    await act(async () => header().findAllByType("button").find((button) => text(button) === "Clear selection").props.onClick());
    assert.deepEqual(selected, [""]);
  } finally { if (tree) await act(async () => tree.unmount()); }
});
