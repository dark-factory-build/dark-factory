import assert from "node:assert/strict";
import test from "node:test";
import { WORKER_SIZE, breakRoomNook, commonSeating, layoutScene, placeWorkers, standable } from "../../dist/src/factory-scene/scene.js";
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
    const source = workers.find((person) => person.id === at.id), station = layout.stations.find((item) => item.key === at.stationId);
    assert.ok(station.key === source.observedBayId || station.representedIds?.includes(source.observedBayId), `${at.id} works at its own machine`);
    assert.ok(Math.hypot(at.x - station.anchor.x, at.y - station.anchor.y) <= 36, "beside it");
    assert.ok(standable(layout, at), `${at.id} stands clear of every solid`);
  }
  assert.deepEqual(placed.find((placement) => placement.id === "worker-00"), { id: "worker-00", area: "work", stationId: "unit-0-0", ...layout.stations.find((item) => item.key === "unit-0-0").anchor });
});

test("nearby rest forms pairs beside a unit's main machine without duplicating seats in the commons", () => {
  const layout = layoutScene(units(20));
  const known = Array.from({ length: 5 }, (_, index) => worker(`known-${index}`, { nodeId: "unit-7", location: "last-observed", paused: index === 0 }));
  const unknown = Array.from({ length: 8 }, (_, index) => worker(`unknown-${index}`));
  const source = [...known, ...unknown, worker("working", { nodeId: "unit-0", observedBayId: "unit-0-5", activity: "waiting", location: "working" }), worker("unobserved", { activity: "waiting", location: "unobserved" })];
  const before = structuredClone(source), placed = placeWorkers(layout, source, "nearby");
  assert.deepEqual(source, before, "ambient placement does not mutate authoritative state");
  assert.deepEqual(placeWorkers(layout, [...source].reverse(), "nearby"), placed);
  const local = placed.filter((person) => person.area === "resting" && person.stationId !== undefined), common = placed.filter((person) => person.area === "resting" && person.stationId === undefined);
  assert.equal(local.filter((person) => person.stationId === "unit-7").length, 2);
  assert.equal(common.filter((person) => person.id.startsWith("known-")).length, 3, "a known full destination does not become unrelated work elsewhere");
  const clusters = new Map();
  for (const person of local) clusters.set(person.stationId, (clusters.get(person.stationId) ?? 0) + 1);
  assert.ok(clusters.size <= 4, "unlocated idle workers use three shared destinations, not every unit");
  assert.ok([...clusters.values()].every((count) => count === 2));
  for (const person of local) assert.ok(standable(layout, person), `${person.id} rests on free floor`);
  assert.equal(placed.find((person) => person.id === "unobserved").area, "staging");
  assert.equal(placed.find((person) => person.id === "working").area, "work");
  separated(placed);
  assert.deepEqual(common.map(({ x, y }) => ({ x, y })).sort((a, b) => a.y - b.y || a.x - b.x), commonSeating(layout, common.length, 1).resting);
});

test("the commons seats a fixed number; a crowd sits on benches below the floor and never moves a machine", () => {
  const layout = layoutScene(units(2)), inside = (seat) => seat.x - WORKER_SIZE / 2 >= layout.commons.x && seat.x + WORKER_SIZE / 2 <= layout.commons.x + layout.commons.width && seat.y >= layout.commons.y && seat.y <= layout.commons.y + layout.commons.height;
  assert.deepEqual(commonSeating(layout, 0, 0), { resting: [], planning: [] });
  for (const [rest, plan] of [[0, 1], [1, 0], [1, 1], [5, 3], [8, 4], [100, 100]]) {
    const seating = commonSeating(layout, rest, plan);
    assert.equal(seating.resting.length, rest);
    assert.equal(seating.planning.length, plan);
    assert.ok(seating.resting.slice(0, 8).every(inside) && seating.planning.slice(0, 4).every(inside));
    for (const seat of [...seating.resting.slice(8), ...seating.planning.slice(4)]) assert.ok(seat.y > layout.height, "past the commons, below everything");
    separated([...seating.resting, ...seating.planning].map((seat, index) => ({ id: index, ...seat })));
  }
  const crowd = Array.from({ length: 40 }, (_, index) => worker(`rest-${String(index).padStart(2, "0")}`));
  assert.deepEqual(layoutScene(units(2)), layout, "workers never enter layout");
  assert.ok(placeWorkers(layout, crowd).every((seat) => inside(seat) || seat.y > layout.height));
});

test("the commons furniture stays put whoever rests or works", () => {
  const layout = layoutScene(units(3)), nook = breakRoomNook(layout), { x, y } = layout.commons;
  assert.deepEqual(nook.furniture.map(({ errand, x: px, y: py, stand }) => ({ errand, x: px - x, y: py - y, stand: { x: stand.x - x, y: stand.y - y } })), [
    { errand: "shelf", x: 120, y: 54, stand: { x: 126, y: 120 } }, { errand: "coffee", x: 158, y: 54, stand: { x: 164, y: 120 } },
    { errand: "board", x: 6, y: 54, stand: { x: 12, y: 120 } }, { errand: "missions", x: 44, y: 54, stand: { x: 50, y: 120 } }, { errand: "tasks", x: 82, y: 54, stand: { x: 88, y: 120 } },
  ]);
  for (const piece of nook.furniture) assert.ok(standable(layout, piece.stand), `${piece.errand} is used from free floor`);
});
