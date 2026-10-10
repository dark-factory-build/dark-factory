import assert from "node:assert/strict";
import test from "node:test";
import { breakRoomNook, layoutScene, placeWorkers, standable } from "../../dist/src/factory-scene/scene.js";
import { machine, sceneGraph, unit } from "../../../../fixtures/scene.mjs";

// Units of six machines each, one of them a fold standing for aliases.
const units = (count) => sceneGraph(Array.from({ length: count }, (_, index) => unit(`unit-${index}`, { machines: Array.from({ length: 6 }, (_, slot) =>
  machine(`unit-${index}-${slot}`, slot % 2 ? "store" : "job", { represented: slot === 5 ? [`unit-${index}-alias-5`] : undefined })) })));
const worker = (id, extra = {}) => ({ id, name: id, role: "worker", activity: "idle", location: "resting", ...extra });
const separated = (placements) => {
  for (let i = 0; i < placements.length; i++) for (let j = i + 1; j < placements.length; j++) {
    assert.ok(Math.abs(placements[i].x - placements[j].x) >= 24 || Math.abs(placements[i].y - placements[j].y) >= 24, `${placements[i].id} overlaps ${placements[j].id}`);
  }
};

test("work slots belong to the observed machine, including a fold's members, and stand on free floor", () => {
  const layout = layoutScene(units(1));
  const workers = Array.from({ length: 12 }, (_, index) => worker(`worker-${String(index).padStart(2, "0")}`, { activity: "busy", location: "working", nodeId: "unit-0", observedBayId: index === 11 ? "unit-0-alias-5" : `unit-0-${Math.floor(index / 2)}` }));
  const placed = placeWorkers(layout, workers);
  separated(placed);
  assert.deepEqual(placeWorkers(layout, [...workers].reverse()), placed);
  for (const at of placed.filter((placement) => placement.area === "work")) {
    const source = workers.find((person) => person.id === at.id), station = layout.stations.find((item) => item.entityId === at.stationId);
    assert.ok(station.entityId === source.observedBayId || station.representedIds?.includes(source.observedBayId), `${at.id} works at its own machine`);
    assert.ok(Math.hypot(at.x - station.anchor.x, at.y - station.anchor.y) <= 36, "beside it");
    assert.ok(standable(layout, at), `${at.id} stands clear of every solid`);
  }
  assert.deepEqual(placed.find((placement) => placement.id === "worker-00"), { id: "worker-00", area: "work", stationId: "unit-0-0", ...layout.stations.find((item) => item.entityId === "unit-0-0").anchor });
});

test("anyone not at work stands beside a machine: the one last worked at, else beside someone alone, with no cap", () => {
  const layout = layoutScene(units(20));
  const known = Array.from({ length: 9 }, (_, index) => worker(`known-${index}`, { nodeId: "unit-7", location: "last-observed", paused: index === 0 }));
  const unknown = Array.from({ length: 8 }, (_, index) => worker(`unknown-${index}`));
  const source = [...known, ...unknown, worker("working", { nodeId: "unit-0", observedBayId: "unit-0-5", activity: "waiting", location: "working" }), worker("unobserved", { activity: "waiting", location: "unobserved" })];
  const before = structuredClone(source), placed = placeWorkers(layout, source);
  assert.deepEqual(source, before, "ambient placement does not mutate authoritative state");
  assert.deepEqual(placeWorkers(layout, [...source].reverse()), placed);
  const main = layout.stations.find((station) => station.entityId === "unit-7");
  const beside = placed.filter((person) => person.id.startsWith("known-"));
  assert.ok(beside.every((person) => person.area === "resting" && person.stationId === "unit-7"), "all nine rest at the machine they last worked at");
  assert.ok(beside.every((person) => Math.abs(person.x - main.anchor.x) <= 24 + 48 * 2 && person.y > main.anchor.y), "beside it and below its standing spot");
  const others = placed.filter((person) => person.id.startsWith("unknown-") || person.id === "unobserved");
  const sharing = new Map();
  for (const person of others) sharing.set(person.stationId, (sharing.get(person.stationId) ?? 0) + 1);
  assert.ok([...sharing.values()].every((count) => count <= 2) && sharing.size === Math.ceil(others.length / 2), "the rest pair up beside each other, each pair at a machine of its own");
  assert.equal(placed.find((person) => person.id === "unobserved").area, "staging");
  assert.equal(placed.find((person) => person.id === "working").area, "work");
  for (const person of placed) assert.ok(standable(layout, person), `${person.id} stands on free floor`);
  separated(placed);
  const crowd = placeWorkers(layout, Array.from({ length: 300 }, (_, index) => worker(`rest-${String(index).padStart(3, "0")}`, { nodeId: "unit-3" })));
  assert.equal(crowd.length, 300, "nobody is turned away");
  separated(crowd);
  assert.equal(placeWorkers(layoutScene(sceneGraph([])), [worker("alone")]).length, 1, "an empty floor still has room");
});

test("the fixtures stand in free floor between the machines, and only the machines place them", () => {
  const layout = layoutScene(units(3)), whole = (piece) => ({ x: piece.x - 8, y: piece.y - 8, width: 36, height: 88 });
  assert.deepEqual(layout.fixtures.map((piece) => piece.errand), ["board", "missions", "tasks", "shelf", "coffee"]);
  const rects = layout.fixtures.map(whole), overlap = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
  for (const [index, rect] of rects.entries()) {
    assert.ok(layout.stations.every((station) => !overlap(rect, station.footprint)), `${layout.fixtures[index].errand} is clear of every machine's footprint`);
    assert.ok(rects.every((other, at) => at === index || !overlap(rect, other)), "and of each other");
    assert.ok(rect.x >= 0 && rect.x + rect.width <= layout.width && rect.y + rect.height <= layout.height, "on the floor");
    assert.ok(standable(layout, layout.fixtures[index].stand), `${layout.fixtures[index].errand} is used from free floor`);
  }
  // Who is offered or at rest never moves one, nor a machine; a floor with no gap puts them below.
  const fewer = layoutScene(units(3), undefined, ["coffee"]);
  assert.deepEqual(fewer.fixtures, layout.fixtures);
  assert.deepEqual(breakRoomNook(fewer).furniture.map((piece) => piece.errand), ["coffee"]);
  assert.deepEqual(fewer.stations, layout.stations);
  const tight = layoutScene(units(1));
  assert.ok(tight.fixtures.every((piece) => piece.y + 88 <= tight.height), "the floor grows to hold what no gap could");
  for (const piece of tight.fixtures) assert.ok(standable(tight, piece.stand));
});

test("a worker at a line stands at its control panel; at anything else, in the middle of its front", () => {
  const layout = layoutScene(sceneGraph([unit("api", { machines: [machine("api-db", "store")] })]));
  const line = layout.stations.find((station) => station.shape === "line"), store = layout.stations.find((station) => station.shape === "silo");
  assert.equal(line.anchor.x, line.x + line.width - 13, "under the panel at the line's right end");
  assert.equal(store.anchor.x, store.x + store.width / 2);
  assert.ok(standable(layout, line.anchor));
  const [placed] = placeWorkers(layout, [worker("operator", { activity: "busy", location: "working", nodeId: "api" })]);
  assert.deepEqual(placed, { id: "operator", area: "work", stationId: "api", ...line.anchor });
});
