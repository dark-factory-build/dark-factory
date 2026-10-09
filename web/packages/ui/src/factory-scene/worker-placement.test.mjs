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
    const source = workers.find((person) => person.id === at.id), station = layout.stations.find((item) => item.entityId === at.stationId);
    assert.ok(station.entityId === source.observedBayId || station.representedIds?.includes(source.observedBayId), `${at.id} works at its own machine`);
    assert.ok(Math.hypot(at.x - station.anchor.x, at.y - station.anchor.y) <= 36, "beside it");
    assert.ok(standable(layout, at), `${at.id} stands clear of every solid`);
  }
  assert.deepEqual(placed.find((placement) => placement.id === "worker-00"), { id: "worker-00", area: "work", stationId: "unit-0-0", ...layout.stations.find((item) => item.entityId === "unit-0-0").anchor });
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

test("the commons seats every agent at rest and half at the work tables; anyone more sits on benches below the floor", () => {
  const sized = (agents) => layoutScene(units(2), undefined, { agents, implements: ["board", "missions", "tasks", "shelf", "coffee"] });
  const layout = sized(6), inside = (seat) => seat.x - WORKER_SIZE / 2 >= layout.commons.x && seat.x + WORKER_SIZE / 2 <= layout.commons.x + layout.commons.width && seat.y >= layout.commons.y && seat.y <= layout.commons.y + layout.commons.height;
  assert.deepEqual(commonSeating(layout, 0, 0), { resting: [], planning: [] });
  for (const [rest, plan] of [[0, 1], [1, 0], [1, 1], [5, 3], [6, 6], [30, 30]]) {
    const seating = commonSeating(layout, rest, plan);
    assert.equal(seating.resting.length, rest);
    assert.equal(seating.planning.length, plan);
    assert.ok(seating.resting.slice(0, 6).every(inside) && seating.planning.slice(0, 3).every(inside), "six agents: six resting seats, three at work tables");
    for (const seat of [...seating.resting.slice(8), ...seating.planning.slice(4)]) assert.ok(seat.y > layout.height, "past the commons, below everything");
    separated([...seating.resting, ...seating.planning].map((seat, index) => ({ id: index, ...seat })));
  }
  // The commons grows with the agents and nothing else: two agents get a small one, twelve a larger one.
  const two = sized(2).commons, twelve = sized(12).commons;
  assert.ok(two.width * two.height < layout.commons.width * layout.commons.height && layout.commons.width * layout.commons.height < twelve.width * twelve.height);
  assert.ok(layoutScene(units(2), undefined, { agents: 2, implements: [] }).commons.height < two.height, "implements take room only when present");
  const crowd = Array.from({ length: 40 }, (_, index) => worker(`rest-${String(index).padStart(2, "0")}`));
  assert.ok(placeWorkers(layout, crowd).every((seat) => inside(seat) || seat.y > layout.height));
});

test("a change in the number of agents resizes the commons in place; no machine moves", () => {
  const before = layoutScene(units(6), undefined, { agents: 3, implements: ["board", "coffee"] });
  for (const agents of [1, 8, 20]) {
    const after = layoutScene(units(6), before, { agents, implements: ["board", "coffee"] });
    assert.deepEqual(after.stations.map(({ entityId: key, x, y }) => [key, x - after.stations[0].x, y - after.stations[0].y]), before.stations.map(({ entityId: key, x, y }) => [key, x - before.stations[0].x, y - before.stations[0].y]), `${agents} agents`);
    for (const station of after.stations) assert.ok(!(station.footprint.x < after.facilities.x + after.facilities.width && after.facilities.x < station.footprint.x + station.footprint.width && station.footprint.y < after.facilities.y + after.facilities.height && after.facilities.y < station.footprint.y + station.footprint.height), "clear of every machine");
  }
});

test("the commons furniture stays put whoever rests or works", () => {
  const layout = layoutScene(units(3)), nook = breakRoomNook(layout), { x, y } = layout.commons;
  assert.deepEqual(nook.furniture.map(({ errand, x: px, y: py, stand }) => ({ errand, x: px - x, y: py - y, stand: { x: stand.x - x, y: stand.y - y } })), [
    { errand: "shelf", x: 120, y: 18, stand: { x: 126, y: 84 } }, { errand: "coffee", x: 158, y: 18, stand: { x: 164, y: 84 } },
    { errand: "board", x: 6, y: 18, stand: { x: 12, y: 84 } }, { errand: "missions", x: 44, y: 18, stand: { x: 50, y: 84 } }, { errand: "tasks", x: 82, y: 18, stand: { x: 88, y: 84 } },
  ]);
  for (const piece of nook.furniture) assert.ok(standable(layout, piece.stand), `${piece.errand} is used from free floor`);
});
