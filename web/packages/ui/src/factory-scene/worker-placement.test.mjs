import assert from "node:assert/strict";
import test from "node:test";
import { COMMON_WIDTH, PADDING, breakRoomNook, commonSeating, placeWorkers } from "../../dist/src/factory-scene/scene.js";

const room = (id, index = 0) => {
  const x = 256 + index % 2 * 392, y = 64 + Math.floor(index / 2) * 416, width = 392, height = 384;
  return { id, kind: "hall", x, y, width, height, door: { x: x + width / 2, y: y + height }, contents: Array.from({ length: 6 }, (_, slot) => ({
    key: `${id}-${slot}`, entityId: `${id}-${slot}`, representedIds: [`${id}-alias-${slot}`], kind: "source", label: `${slot}`, count: 1, workSurface: true,
    x: x + 24 + slot % 2 * 172, y: y + 36 + Math.floor(slot / 2) * 96, width: 144, height: 48,
  })) };
};
const floor = (count = 1) => ({ width: 1056, height: 128 + count * 416, rooms: Array.from({ length: count }, (_, index) => room(`room-${index}`, index)), headings: [], corridors: [], restingTop: 128 });
const worker = (id, extra = {}) => ({ id, name: id, role: "worker", activity: "idle", location: "resting", ...extra });
const separated = (placements) => {
  for (let i = 0; i < placements.length; i++) for (let j = i + 1; j < placements.length; j++) {
    assert.ok(Math.abs(placements[i].x - placements[j].x) >= 24 || Math.abs(placements[i].y - placements[j].y) >= 24, `${placements[i].id} overlaps ${placements[j].id}`);
  }
};

test("work slots belong to the observed assembly surface, including aliases", () => {
  const layout = floor();
  const workers = Array.from({ length: 12 }, (_, index) => worker(`worker-${String(index).padStart(2, "0")}`, { activity: "busy", location: "working", nodeId: "room-0", observedBayId: `room-0-${index % 2 ? "alias-" : ""}${Math.floor(index / 2)}` }));
  const placed = placeWorkers(layout, workers);
  assert.equal(placed.filter((placement) => placement.area === "room").length, workers.length);
  separated(placed);
  assert.deepEqual(placeWorkers(layout, [...workers].reverse()), placed);
  for (const at of placed) {
    const source = workers.find((person) => person.id === at.id), item = layout.rooms[0].contents[Math.floor(workers.indexOf(source) / 2)];
    assert.equal(at.y, item.y + item.height + 12);
    assert.ok(at.x >= item.x && at.x <= item.x + item.width);
  }
});

test("nearby rest forms pairs without duplicate seats or duplicating them in commons", () => {
  const layout = floor(20);
  const known = Array.from({ length: 5 }, (_, index) => worker(`known-${index}`, { nodeId: "room-7", location: "last-observed", paused: index === 0 }));
  const unknown = Array.from({ length: 8 }, (_, index) => worker(`unknown-${index}`));
  const source = [...known, ...unknown, worker("working", { nodeId: "room-0", observedBayId: "room-0-5", activity: "waiting", location: "working" }), worker("unobserved", { activity: "waiting", location: "unobserved" })];
  const before = structuredClone(source), placed = placeWorkers(layout, source, "nearby");
  assert.deepEqual(source, before, "ambient placement does not mutate authoritative state");
  assert.deepEqual(placeWorkers(layout, [...source].reverse(), "nearby"), placed);
  const local = placed.filter((person) => person.area === "resting" && person.roomId !== undefined), common = placed.filter((person) => person.area === "resting" && person.roomId === undefined);
  assert.equal(local.filter((person) => person.roomId === "room-7").length, 2);
  assert.equal(common.filter((person) => person.id.startsWith("known-")).length, 3, "a known full destination does not become unrelated work elsewhere");
  const clusters = new Map();
  for (const person of local) clusters.set(person.roomId, (clusters.get(person.roomId) ?? 0) + 1);
  assert.ok(clusters.size <= 4, "unlocated idle workers use three shared destinations, not every room");
  assert.ok([...clusters.values()].every((count) => count === 2));
  assert.equal(placed.find((person) => person.id === "unobserved").area, "staging");
  assert.equal(placed.find((person) => person.id === "working").area, "room");
  separated(placed);
  assert.deepEqual(common.map(({ x, y }) => ({ x, y })).sort((a, b) => a.y - b.y || a.x - b.x), commonSeating(layout, common.length, 1).resting);
});

test("side commons has exact occupied seats, bounded columns, and separate planning rows", () => {
  const layout = floor();
  assert.deepEqual(commonSeating(layout, 0, 0), { resting: [], planning: [] });
  for (const [rest, plan] of [[0, 1], [1, 0], [1, 1], [5, 7], [100, 100]]) {
    const seating = commonSeating(layout, rest, plan);
    assert.equal(seating.resting.length, rest);
    assert.equal(seating.planning.length, plan);
    for (const seat of [...seating.resting, ...seating.planning]) assert.ok(seat.x - 12 >= PADDING && seat.x + 12 <= PADDING + COMMON_WIDTH);
    if (rest && plan) assert.equal(seating.planning[0].y - seating.resting.at(-1).y, 64);
    separated([...seating.resting, ...seating.planning].map((seat, index) => ({ id: index, ...seat })));
  }
});

test("the commons furniture stays put whoever rests nearby or works", () => {
  const layout = floor(3), nook = breakRoomNook(layout, 0, 0, true);
  assert.deepEqual(nook, breakRoomNook(layout, 100, 100, true));
  assert.deepEqual(nook.furniture.map(({ errand, roomId, x, y, stand }) => ({ errand, roomId, x, y, stand })), [
    { errand: "shelf", roomId: undefined, x: 136, y: 90, stand: { x: 146, y: 136 } }, { errand: "coffee", roomId: undefined, x: 174, y: 90, stand: { x: 184, y: 136 } },
  ]);
});
