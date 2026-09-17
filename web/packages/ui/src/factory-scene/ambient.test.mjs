import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { ambientPlacements, ambientSeats } from "../../dist/src/factory-scene/ambient.js";
import { FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { DEFAULT_FLOOR_APPEARANCE } from "../../dist/src/floor-appearance.js";
import { layoutScene, placeWorkers } from "../../dist/src/factory-scene/scene.js";

test("idle workers receive deterministic shared-area destinations", () => {
  const layout = layoutScene({ digest: "ambient", nodes: [{ id: "root", path: ".", label: "Root", kind: "repository", sizeBucket: "large" }] });
  const workers = [
    { id: "b", name: "B", role: "worker", activity: "idle", location: "resting", nodeId: undefined },
    { id: "a", name: "A", role: "worker", activity: "waiting", location: "resting", nodeId: undefined },
    { id: "working", name: "Working", role: "worker", activity: "busy", location: "working", nodeId: "root" },
  ];
  const placements = placeWorkers(layout, workers);
  const first = ambientPlacements(layout, placements, workers);
  const second = ambientPlacements(layout, placements, [...workers].reverse());
  assert.deepEqual(first, second);
  assert.deepEqual(first.filter(({ id }) => id === "working"), placements.filter(({ id }) => id === "working"));
  assert.ok(first.filter(({ id }) => id === "a" || id === "b").every(({ y }) => y >= layout.restingTop));
  assert.deepEqual(ambientPlacements(layout, placements, workers, "off"), placements);
  assert.deepEqual([...new Set(ambientSeats(layout).map(({ destination }) => destination))], ["notice-board", "break-counter", "shared-table"]);
  const phased = ambientPlacements(layout, placements, workers, "lively", 1);
  assert.ok(phased.some((placement, index) => placement.x !== first[index]?.x || placement.y !== first[index]?.y));
  const needs = ambientPlacements(layout, placements, [{ ...workers[0], activity: "needs-you" }], "lively");
  assert.deepEqual(needs, placements);
  assert.ok(first.filter(({ id }) => id === "a" || id === "b").every(({ x, y }) => x >= 48 && x <= layout.width - 16 && y >= layout.restingTop));
  const crowdedWorkers = Array.from({ length: 8 }, (_, index) => ({ id: `idle-${index}`, name: `Idle ${index}`, role: "worker", activity: "idle", location: "resting" }));
  const crowded = ambientPlacements(layout, placeWorkers(layout, crowdedWorkers), crowdedWorkers, "lively");
  const occupied = crowded.filter(({ id }) => id.startsWith("idle-")).map(({ x, y }) => `${x},${y}`);
  const seats = new Set(ambientSeats(layout).map(({ point }) => `${point.x},${point.y}`));
  assert.equal(occupied.filter((point) => seats.has(point)).length, 6, "six seats are unique and overflow remains in the existing resting line");
  assert.equal(occupied.length, 8);
  assert.equal(new Set(occupied).size, 8, "crowded workers never share one standing point");
  assert.equal(crowded.filter(({ ambientSocial }) => ambientSocial).length, 2, "only the occupied table pair receives a social pose");
  for (const phase of [0, 1, 2, 3]) {
    const reassigned = crowdedWorkers.map((worker) => worker.id === crowded[0].id ? { ...worker, location: "working", nodeId: "root", activity: "busy" } : worker);
    const next = ambientPlacements(layout, placeWorkers(layout, reassigned), reassigned, "lively", phase);
    const active = next.find(({ id }) => id === crowded[0].id);
    assert.equal(active.area, "room", "real work preempts every ambient phase without a delay");
    assert.equal(active.ambient, undefined);
  }
});

test("ambient scheduling stops for hidden, reduced-motion, disconnected and ineligible scenes", () => {
  const previousWindow = globalThis.window, previousDocument = globalThis.document;
  const intervals = new Map(), listeners = new Map();
  const media = { matches: false, addEventListener: (_, callback) => listeners.set("media", callback), removeEventListener: () => listeners.delete("media") };
  let nextId = 0;
  globalThis.window = { setInterval: (callback) => { intervals.set(++nextId, callback); return nextId; }, clearInterval: (id) => intervals.delete(id), matchMedia: () => media };
  globalThis.document = { visibilityState: "visible", addEventListener: (name, callback) => listeners.set(name, callback), removeEventListener: (name) => listeners.delete(name) };
  const topology = { digest: "ambient-clock", nodes: [{ id: "room", path: ".", label: "Room", kind: "repository", sizeBucket: "large" }] };
  const worker = { id: "idle", name: "Idle", role: "worker", activity: "idle", location: "resting" };
  let scene;
  try {
    act(() => { scene = create(createElement(FactoryScene, { topology, workers: [worker] })); });
    assert.equal(intervals.size, 1);
    act(() => { globalThis.document.visibilityState = "hidden"; listeners.get("visibilitychange")(); });
    assert.equal(intervals.size, 0);
    act(() => { globalThis.document.visibilityState = "visible"; listeners.get("visibilitychange")(); });
    assert.equal(intervals.size, 1);
    act(() => { media.matches = true; listeners.get("media")(); });
    assert.equal(intervals.size, 0);
    act(() => { media.matches = false; listeners.get("media")(); });
    assert.equal(intervals.size, 1);
    act(() => { scene.update(createElement(FactoryScene, { topology, workers: [worker], appearance: { ...DEFAULT_FLOOR_APPEARANCE, ambientLife: "off" } })); });
    assert.equal(intervals.size, 0);
    act(() => { scene.update(createElement(FactoryScene, { topology, workers: [worker], connected: false })); });
    assert.equal(intervals.size, 0);
    act(() => { scene.update(createElement(FactoryScene, { topology, workers: [{ ...worker, location: "working", nodeId: "room", activity: "busy" }] })); });
    assert.equal(intervals.size, 0);
  } finally {
    act(() => { scene?.unmount(); });
    if (previousWindow === undefined) delete globalThis.window; else globalThis.window = previousWindow;
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
});
