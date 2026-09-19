import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { inflateSync } from "node:zlib";
import { join } from "node:path";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { AgentSprite, FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { PADDING, WORKER_SIZE, commonSeating, layoutScene, placeWorkers } from "../../dist/src/factory-scene/scene.js";
import { resolvedAppearance, spriteOptions, workerFrames } from "../../dist/src/factory-scene/appearance.js";
import { pointOnRoute, routeBetween, routeFromCurrent, routeFromSpine } from "../../dist/src/factory-scene/movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "../../dist/src/factory-scene/sprites/sprites.generated.js";

const topology = {
  digest: "fixture-1",
  nodes: [
    { id: "repo", parentId: "", path: ".", label: "Repository", kind: "repository", sizeBucket: "large" },
    { id: "lib", parentId: "repo", path: "packages/lib", label: "<Shared & Library> 📦📦", kind: "package", sizeBucket: "medium" },
    { id: "src", parentId: "lib", path: "packages/lib/src", label: "Source", kind: "directory", sizeBucket: "small" },
  ],
};

const fileCounts = { source: 200, tests: 140, documentation: 36, configuration: 6, assets: 98, unclassified: 4 };
const inventoryTopology = { ...topology, nodes: topology.nodes.map((node) => ({ ...node,
  inventory: { direct: fileCounts, total: fileCounts, samples: [], samples_omitted: 484 },
})) };

const workers = [
  { id: "worker-b", name: "Builder", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "src" },
  { id: "worker-a", name: "Planner", role: "orchestrator", activity: "needs-you", nodeId: "missing" },
];

// Pinned outputs keep these tests independent of the hash implementation.
const pinnedIdentities = Object.freeze({
  "identity-2": 0,
  "identity-1": 1,
  "identity-0": 2,
  "identity-3": 3,
  "worker-a": 1,
  "worker-b": 0,
  "stable-worker": 3,
  "worker-😀": 3,
});

function identityFor(id) {
  const identity = pinnedIdentities[id];
  assert.notEqual(identity, undefined, `missing pinned identity for ${id}`);
  return identity;
}

function idForIdentity(identity) {
  const id = ["identity-2", "identity-1", "identity-0", "identity-3"][identity];
  assert.ok(id, `missing pinned id for identity ${identity}`);
  return id;
}

function render(props = {}) {
  return renderToStaticMarkup(createElement(FactoryScene, { topology, workers, ...props }));
}

test("single-room project headings omit duplicate copy without moving the building", () => {
  const node = { ...topology.nodes[0], project: { id: "project-a", name: "Repository" } };
  const landing = layoutScene({ digest: "landing", nodes: [node] });
  const distinct = layoutScene({ digest: "landing", nodes: [{ ...node, label: "Subsystem" }] });
  assert.deepEqual(landing.headings, []);
  assert.equal(distinct.headings[0].label, "Repository");
  assert.deepEqual(landing.rooms, distinct.rooms);
  assert.deepEqual(landing.corridors, distinct.corridors);
  const multiple = layoutScene({ digest: "children", nodes: [node, { ...node, id: "child", path: "child" }] });
  assert.equal(multiple.headings[0].label, "Repository");
});

function overlaps(left, right) {
  return left.x <= right.x + right.width && right.x <= left.x + left.width
    && left.y <= right.y + right.height && right.y <= left.y + left.height;
}

function corridorReachability(layout) {
  const all = layout.corridors;
  const seen = new Set([0]);
  const queue = [0];
  while (queue.length > 0) {
    const current = queue.shift();
    all.forEach((candidate, index) => {
      if (!seen.has(index) && overlaps(all[current], candidate)) { seen.add(index); queue.push(index); }
    });
  }
  return layout.rooms.every((room) => all.some((corridor, index) => seen.has(index)
    && room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width
    && room.door.y >= corridor.y && room.door.y <= corridor.y + corridor.height));
}

/** Verify the rendered 20px person stays on the floor and crosses walls only at doors. */
function assertRouteGeometry(layout, start, route, message) {
  const clearance = WORKER_SIZE / 2;
  const spine = layout.corridors.at(-1);
  assert.ok(spine, `${message}: missing spine`);
  const clearRect = (point, rect) => point.x - clearance >= rect.x && point.x + clearance <= rect.x + rect.width
    && point.y - clearance >= rect.y && point.y + clearance <= rect.y + rect.height;
  // The rendered resting/staging floor starts where the connected spine reaches
  // it and continues to the fixed right edge of the scene.
  const clearCommon = (point) => point.x - clearance >= PADDING && point.x + clearance <= layout.width - PADDING
    && point.y - clearance >= layout.restingTop - 32;
  const points = [start, ...route.points];
  for (let index = 1; index < points.length; index++) {
    const from = points[index - 1];
    const to = points[index];
    assert.ok(from.x === to.x || from.y === to.y, `${message}: diagonal segment ${index}`);
    if (from.y === to.y) {
      const wall = layout.rooms.find((room) => from.y === room.door.y
        && Math.max(Math.min(from.x, to.x), room.x) <= Math.min(Math.max(from.x, to.x), room.x + room.width));
      assert.equal(wall, undefined, `${message}: horizontal segment ${index} follows a room wall`);
      const clearHorizontal = (rect) => from.y - clearance >= rect.y && from.y + clearance <= rect.y + rect.height
        && Math.min(from.x, to.x) - clearance >= rect.x && Math.max(from.x, to.x) + clearance <= rect.x + rect.width;
      const clearCommonHorizontal = () => Math.min(from.x, to.x) - clearance >= PADDING && Math.max(from.x, to.x) + clearance <= layout.width - PADDING
        && from.y - clearance >= layout.restingTop - 32;
      assert.ok(layout.rooms.some(clearHorizontal) || layout.corridors.some(clearHorizontal) || clearCommonHorizontal(), `${message}: horizontal segment ${index} leaves clear floor`);
      continue;
    }
    const low = Math.min(from.y, to.y);
    const high = Math.max(from.y, to.y);
    const rowFor = (room) => layout.corridors.find((corridor) => corridor !== spine && corridor.y === room.door.y
      && room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width);
    const throughDoor = (room) => {
      const row = rowFor(room);
      return row !== undefined && Math.abs(from.x - room.door.x) + clearance <= row.height / 2
        && ((low >= room.y + clearance && high <= room.door.y) || (low >= row.y && high <= row.y + row.height - clearance));
    };
    const boundaryRecovery = layout.corridors.some((corridor) => corridor !== spine && low === corridor.y && high <= corridor.y + corridor.height / 2
      && from.x - clearance >= corridor.x && from.x + clearance <= corridor.x + corridor.width);
    for (const room of layout.rooms) {
      const touchesBottom = low <= room.door.y && high >= room.door.y;
      if (touchesBottom && from.x >= room.x && from.x <= room.x + room.width) {
        assert.ok(throughDoor(room) || boundaryRecovery, `${message}: vertical segment ${index} crosses ${room.id} outside its doorway`);
      }
    }
    const clearVertical = (rect) => from.x - clearance >= rect.x && from.x + clearance <= rect.x + rect.width
      && low >= rect.y + clearance && high <= rect.y + rect.height - clearance;
    const spineOrCommon = from.x - clearance >= spine.x && from.x + clearance <= spine.x + spine.width && low >= spine.y + clearance;
    assert.ok(layout.rooms.some(clearVertical) || layout.corridors.some(clearVertical) || layout.rooms.some(throughDoor) || boundaryRecovery || spineOrCommon || clearCommon({ x: from.x, y: low }), `${message}: vertical segment ${index} leaves clear floor`);
  }
}

test("the pure scene model feeds a deterministic SVG renderer", () => {
  const layout = layoutScene(topology);
  assert.deepEqual(layout, layoutScene({ ...topology, nodes: [...topology.nodes].reverse() }));
  assert.equal(layout.rooms[0].contents.length, 0, "unknown composition does not invent room contents");

  for (const room of layout.rooms) {
    assert.ok(room.door.y === room.y + room.height && room.door.x > room.x && room.door.x < room.x + room.width);
    assert.ok(layout.corridors.some((corridor) => room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width && room.door.y >= corridor.y && room.door.y <= corridor.y + corridor.height), `door ${room.id} reaches a corridor`);
  }
  for (const count of [1, 2, 3, 4, 5, 11, 24]) {
    const many = { digest: `${count}`, nodes: Array.from({ length: count }, (_, index) => ({ id: `room-${index}`, parentId: "", path: `room-${index}`, label: `Room ${index}`, kind: "directory", inventory: inventoryTopology.nodes[0].inventory, sizeBucket: ["empty", "tiny", "small", "medium", "large"][index % 5] })) };
    const connected = layoutScene(many);
    assert.equal(corridorReachability(connected), true, `${count} rooms remain reachable from the spine`);
    for (const room of connected.rooms) {
      const placement = placeWorkers(connected, [{ ...workers[0], nodeId: room.id }])[0];
      assert.equal(room.contents.length, 1);
      const route = routeFromSpine(connected, { x: connected.corridors.at(-1).x + connected.corridors.at(-1).width / 2, y: connected.restingTop }, placement);
      assert.ok(route);
      assertRouteGeometry(connected, { x: connected.corridors.at(-1).x + connected.corridors.at(-1).width / 2, y: connected.restingTop }, route, room.id);
    }
  }
  const multi = layoutScene({ digest: "multi", nodes: Array.from({ length: 4 }, (_, index) => ({
    id: `project-${index}`, parentId: "", path: ".", label: `Project ${index}`, kind: "repository", sizeBucket: "large",
    project: { id: `p-${index % 2}`, name: `Project ${index % 2}` },
  })) });
  assert.equal(corridorReachability(multi), true, "multi-project rooms remain reachable from the spine");

  const buckets = ["empty", "tiny", "small", "medium", "large"];
  const sized = layoutScene({ digest: "sizes", nodes: buckets.map((sizeBucket) => ({ id: sizeBucket, path: sizeBucket, label: sizeBucket, kind: "directory", sizeBucket })) });
  const areas = buckets.map((bucket) => { const room = sized.rooms.find((room) => room.id === bucket); return room.width * room.height; });
  assert.ok(new Set(areas).size > 1, "workshops have varied proportions");
  const rescanned = layoutScene({ ...sized, digest: "rescanned", nodes: buckets.map((sizeBucket) => ({ id: sizeBucket, path: sizeBucket, label: sizeBucket, kind: "directory", sizeBucket: "large" })) });
  assert.deepEqual(sized.rooms, rescanned.rooms, "file volume changes do not move walls");
  const rows = [...new Set(sized.rooms.map((room) => room.y))];
  assert.notEqual(sized.rooms.find((room) => room.y === rows[0]).width, sized.rooms.find((room) => room.y === rows[1]).width, "row seams stagger");
  for (const room of sized.rooms) {
    const next = sized.rooms.find((other) => other.y === room.y && other.x > room.x);
    if (next) assert.equal(room.x + room.width, next.x, "neighbouring rooms share walls");
  }
  assert.ok(areas[4] <= areas[0] * 3, "one large component cannot dominate the map");
  assert.equal(corridorReachability(sized), true, "mixed footprints share reachable doorway edges");

  const placements = placeWorkers(layout, workers);
  assert.deepEqual(placements, placeWorkers(layout, [...workers].reverse()));
  assert.equal(workerFrames(workers[0]).length, 9);
  assert.equal(workerFrames(workers[0]).at(-1), "person.system.worker.codex");
  assert.equal(workerFrames({ ...workers[0], provider: "made_up" }).at(-1), "person.system.worker.shell");
  assert.equal(workerFrames(workers[1]).at(-1), "person.alert", "the alert rides above whoever needs you");

  const first = render();
  const reordered = render({
    topology: { ...topology, nodes: [...topology.nodes].reverse() },
    workers: [...workers].reverse(),
  });
  assert.equal(first, reordered);
  assert.match(first, /data-topology-digest="fixture-1"/);
  assert.equal(first.includes(">STAGED</text>"), false);
  assert.match(first, /data-room-id="src"/);
  assert.match(first, /package · subtree/);
  assert.doesNotMatch(first, /dfRoomDetails|Inspect room|PACKAGE · SUBTREE/);
  const compact = renderToStaticMarkup(createElement(FactoryScene, {
    topology: { digest: "compact", nodes: [{ id: "p", path: ".", label: "Project", kind: "repository", sizeBucket: "large" }] },
    workers: [], omittedLocations: 1,
  }));
  const compactWidth = Number(compact.match(/viewBox="0 0 (\d+)/)[1]);
  assert.ok(Number(compact.match(/max-width:(\d+)px/)[1]) <= compactWidth * 2, "a one-room scope caps sprite magnification");
  assert.match(compact, /current locations not shown in this view/);
  assert.equal(compact.includes("omitted by the room cap"), false);
  assert.match(first, /&lt;Shared &amp; Library/);
  assert.equal(first.includes("�"), false);
  assert.equal((first.match(/data-worker-id=/g) ?? []).length, workers.length);
  assert.equal((first.match(/data-worker-location=/g) ?? []).length, workers.length);
  // The sheet is one <defs> entry, not one copy per worker, and every frame a
  // worker stands on is a real window on it.
  assert.equal(first.split(spriteSheet).length - 1, 1);
  assert.equal((first.match(/<symbol /g) ?? []).length, Object.keys(spriteAtlas.frames).length);
  for (const worker of workers) {
    const rendered = first.slice(first.indexOf(`data-worker-id="${worker.id}"`));
    const frame = rendered.slice(0, rendered.indexOf("</g>")).match(/href="#df-frame-([^"]+)"/g)
      .map((match) => match.slice('href="#df-frame-'.length, -1));
    // Off the floor of a room a worker sits: resting with a cup on the table, or planning.
    const seat = worker.location === "working" ? undefined : worker.location === "unobserved" ? "planning" : "resting";
    assert.deepEqual(frame, [...workerFrames(worker, undefined, seat), ...(seat === "resting" ? ["person.held.cup"] : [])]);
    if (seat !== undefined) assert.equal(frame.some((name) => name.startsWith("person.tool.")), false, "tools are put down at a table");
    for (const name of frame) assert.ok(name in spriteAtlas.frames, name);
  }
  assert.equal(first.includes("dfFactoryScene__alternate"), false);
  assert.match(first, /data-corridor/);
  assert.equal(first.includes("<animate"), false);
  const unobserved = render({ workers: [{ ...workers[0], location: "unobserved", nodeId: undefined }] });
  assert.match(unobserved, /aria-label="Planning"/);
  assert.match(unobserved, /working; location not yet observed/);
  assert.doesNotMatch(unobserved, /data-worker-task-id/);
  assert.match(first, /aria-label="Break room"/);
  const restingY = Number(first.match(/data-worker-id="worker-a"[^>]*transform="translate\([^ ]+ ([0-9.]+)\)"/)[1]);
  const roomYs = [...first.matchAll(/data-room-id="[^"]+"[^>]*>[\s\S]*?<rect x="[^"]+" y="([0-9.]+)"/g)].map((match) => Number(match[1]));
  const roomBottoms = [...first.matchAll(/data-room-id="[^"]+"[^>]*>\s*<rect x="[^"]+" y="([0-9.]+)" width="[^"]+" height="([0-9.]+)"/g)].map((match) => Number(match[1]) + Number(match[2]));
  assert.ok(restingY >= Math.max(...roomBottoms) + 24, "resting area stays below every room with clearance");
  const capped = render({ workers: [{ ...workers[0], location: "working", locationLabel: "Source", nodeId: undefined }], omittedLocations: 1 });
  assert.match(capped, /aria-label="Planning"/);
  assert.match(capped, /representative location near observed changes in Source; outside displayed rooms/);
  const observed = render({ workers: [{ ...workers[1], location: "last-observed", locationLabel: "Source" }] });
  assert.match(observed, /last observed near changes in Source/);

  // The sheet the renderer reads is every frame it can draw, on a 16px grid,
  // inside the size the generator wrote next to it.
  assert.ok(Object.keys(spriteAtlas.frames).length > 0);
  assert.match(first, new RegExp(`width="${spriteSheetSize.width}" height="${spriteSheetSize.height}"`));
  for (const [name, cell] of Object.entries(spriteAtlas.frames)) {
    assert.equal(cell.x % spriteAtlas.frame, 0, name);
    assert.equal(cell.y % spriteAtlas.frame, 0, name);
    assert.ok(cell.x >= 0 && cell.y >= 0 && cell.x + spriteAtlas.frame <= spriteSheetSize.width && cell.y + spriteAtlas.frame <= spriteSheetSize.height, name);
  }

  const denseWorkers = Array.from({ length: 100 }, (_, index) => ({
    id: `worker-${index}`,
    name: `Worker ${index}`,
    role: "worker",
    activity: "busy",
    location: "working",
    nodeId: "src",
  }));
  const densePlacements = placeWorkers(layout, denseWorkers);
  assert.equal(new Set(densePlacements.map(({ x, y }) => `${x},${y}`)).size, denseWorkers.length);
  const srcRoom = layout.rooms.find((room) => room.id === "src");
  assert.deepEqual(densePlacements[0], { id: "worker-0", area: "room", roomId: "src", x: srcRoom.door.x, y: srcRoom.door.y - 32 });
  for (const placement of densePlacements.filter(({ area }) => area === "room")) {
    assert.ok(placement.x - WORKER_SIZE / 2 >= srcRoom.x && placement.x + WORKER_SIZE / 2 <= srcRoom.x + srcRoom.width);
    assert.ok(placement.y - WORKER_SIZE / 2 >= srcRoom.y && placement.y + WORKER_SIZE / 2 <= srcRoom.y + srcRoom.height);
    assert.ok(placement.y - WORKER_SIZE / 2 >= srcRoom.y + 40, "room workers stay below the title and kind");
  }
  const denseSvg = render({ workers: denseWorkers });
  assert.match(denseSvg, /aria-label="Planning"/);
  const denseHeight = Number(denseSvg.match(/viewBox="0 0 [^ ]+ ([^"]+)"/)[1]);
  assert.ok(denseHeight > Math.max(...densePlacements.map(({ y }) => y + 8)));

  const mixedPlacements = placeWorkers(layout, [
    ...denseWorkers,
    { ...workers[1], location: "resting" },
  ]);
  const overflowTop = Math.min(...mixedPlacements.filter((placement) => placement.area === "overflow").map((placement) => placement.y));
  assert.equal(overflowTop, layout.restingTop, "wide floors put planning beside breaks");
  assert.ok(mixedPlacements.filter(({ area }) => area === "overflow").every(({ x }) => x > mixedPlacements.find(({ area }) => area === "resting").x + WORKER_SIZE));
  const directOutside = placeWorkers(layout, [
    { ...workers[1], location: "resting" },
    { ...workers[0], location: "unobserved", nodeId: undefined },
  ]);
  const directStagingTop = Math.min(...directOutside.filter((placement) => placement.area === "staging").map((placement) => placement.y));
  assert.equal(directStagingTop, layout.restingTop, "unobserved work shares the planning bay");
  assert.ok(directOutside[0].x !== directOutside[1].x, "break and planning seats remain separate");

  const changed = layoutScene({
    digest: "fixture-2",
    nodes: [...topology.nodes, { id: "docs", parentId: "repo", path: "docs", label: "Docs", kind: "directory", sizeBucket: "tiny" }],
  });
  assert.equal(changed.rooms.some((room) => room.id === "docs"), true);
  assert.notDeepEqual(changed, layout);

  const emptyLayout = layoutScene({ digest: "empty", nodes: [] });
  const emptyWorkers = denseWorkers.slice(0, 20).map(({ nodeId: _nodeId, location: _location, ...worker }) => ({ ...worker, location: "resting" }));
  const emptyPlacements = placeWorkers(emptyLayout, emptyWorkers);
  assert.equal(new Set(emptyPlacements.map(({ x, y }) => `${x},${y}`)).size, emptyWorkers.length);
  const emptySvg = render({ topology: { digest: "empty", nodes: [] }, workers: emptyWorkers });
  assert.match(emptySvg, /EMPTY FLOOR/);
  // An empty floor in a wide column stays a panel, not a poster.
  assert.match(emptySvg, new RegExp(`min-width:${emptyLayout.width}px`));
  assert.match(emptySvg, /aria-label="Break room"/);
  const emptyArea = emptySvg.match(/aria-label="Common room"><rect x="[^"]+" y="([0-9.]+)" width="[^"]+" height="([0-9.]+)"/);
  const emptyLabel = emptySvg.match(/<text x="[^"]+" y="([0-9.]+)"[^>]*>EMPTY FLOOR<\/text>/);
  const emptyHeight = Number(emptySvg.match(/viewBox="0 0 [^ ]+ ([0-9.]+)"/)[1]);
  assert.ok(emptyArea !== null && emptyLabel !== null);
  assert.ok(Number(emptyArea[1]) > Number(emptyLabel[1]) + 10, "resting area clears the empty-floor label");
  assert.ok(emptyHeight > Number(emptyLabel[1]), "empty-floor label remains inside the scene");
  const emptyWithStaging = render({
    topology: { digest: "empty", nodes: [] },
    workers: [...emptyWorkers, { ...workers[0], location: "unobserved", nodeId: undefined }],
  });
  const stagingArea = emptyWithStaging.match(/aria-label="Planning"><text x="[^"]+" y="([0-9.]+)"/);
  const stagingLabel = emptyWithStaging.match(/<text x="[^"]+" y="([0-9.]+)"[^>]*>EMPTY FLOOR<\/text>/);
  assert.ok(stagingArea !== null && stagingLabel !== null);
  assert.ok(Number(stagingLabel[1]) + PADDING <= Number(stagingArea[1]), "empty-floor label clears the staging area");
});

test("rooms group under their project's heading and stay on the tile grid", () => {
  const grouped = {
    digest: "fixture-2",
    nodes: [
      { id: "b-root", parentId: "", path: ".", label: "Beta", kind: "repository", project: { id: "p-b", name: "Beta Works" } },
      { id: "a-web", parentId: "a-root", path: "web", label: "web", kind: "package", project: { id: "p-a", name: "Alpha Works" } },
      { id: "b-src", parentId: "b-root", path: "src", label: "src", kind: "directory", project: { id: "p-b", name: "Beta Works" } },
      { id: "a-root", parentId: "", path: ".", label: "Alpha", kind: "repository", project: { id: "p-a", name: "Alpha Works" } },
      { id: "a-cmd", parentId: "a-root", path: "cmd", label: "cmd", kind: "directory", project: { id: "p-a", name: "Alpha Works" } },
    ],
  };
  const layout = layoutScene(grouped);
  assert.deepEqual(layout, layoutScene({ ...grouped, nodes: [...grouped.nodes].reverse() }));
  assert.deepEqual(layout.rooms.map((room) => room.id), ["a-root", "a-cmd", "a-web", "b-root", "b-src"]);
  assert.deepEqual(layout.headings.map((heading) => heading.label), ["Alpha Works", "Beta Works"]);
  const [alpha, beta] = layout.headings;
  for (const room of layout.rooms.slice(0, 3)) assert.ok(room.y > alpha.y && room.y < beta.y, `${room.id} outside Alpha`);
  for (const room of layout.rooms.slice(3)) assert.ok(room.y > beta.y, `${room.id} outside Beta`);
  for (const room of layout.rooms) {
    assert.ok(room.door.y === room.y + room.height);
  }
  // Two projects with one name are still two blocks under two headings.
  const twins = layoutScene({ ...grouped, nodes: grouped.nodes.map((node) => ({ ...node, project: { id: node.project.id, name: "Twin" } })) });
  assert.deepEqual(twins.headings.map((heading) => heading.label), ["Twin", "Twin"]);
  assert.deepEqual(twins.rooms.map((room) => room.id), layout.rooms.map((room) => room.id));
  // The heading is its own element at the row the layout gave it, and every
  // door sits on the tile grid like the room it opens.
  const markup = renderToStaticMarkup(createElement(FactoryScene, { topology: grouped, workers: [] }));
  for (const heading of layout.headings) {
    assert.match(markup, new RegExp(`<text data-floor-heading="${heading.label}" x="${heading.x}" y="${heading.y + 11}"[^>]*>${heading.label}</text>`));
  }
  assert.equal((markup.match(/data-room-walls=/g) ?? []).length, layout.rooms.length);
  // Rooms without a project stand under no heading, as before.
  assert.deepEqual(layoutScene(topology).headings, []);
});

test("worker identity is stable while operational state changes", () => {
  const base = { id: "stable-worker", name: "Builder", role: "worker", provider: "codex", activity: "busy", nodeId: "src" };
  const stableIdentity = 3;
  const variants = [
    { ...base, name: "Renamed", nodeId: "repo" },
    { ...base, activity: "waiting" },
    { ...base, provider: "claude_code" },
    { ...base, role: "orchestrator" },
    { ...base, provider: "made_up", activity: "unknown" },
  ];
  for (const worker of variants) assert.equal(resolvedAppearance(worker).hair, stableIdentity);
  assert.equal(resolvedAppearance({ ...base, id: "worker-😀" }).hair, 3);
});

test("the standalone agent sprite crops one existing stable frame", () => {
  const agent = { id: "worker-b", name: "Builder", role: "worker", provider: "codex" };
  const frames = workerFrames({ ...agent, activity: "busy" });
  const markup = renderToStaticMarkup(createElement(AgentSprite, { agent, activity: "busy" }));
  assert.match(markup, /class="dfAgentSprite"/);
  assert.match(markup, /aria-label="Builder, worker, busy"/);
  assert.equal((markup.match(/<image /g) ?? []).length, frames.length);
  for (const frame of frames) { const cell = spriteAtlas.frames[frame]; assert.match(markup, new RegExp(`x="${cell.x === 0 ? 0 : -cell.x}" y="${cell.y === 0 ? 0 : -cell.y}"`)); }
  assert.equal(markup.includes("df-frame-"), false, "standalone sprite creates no document symbol id");
});

test("the selected scene worker has a ring without changing its sprite", () => {
  const markup = render({ selectedWorkerId: "worker-b" });
  const selected = markup.slice(markup.indexOf('data-worker-id="worker-b"'));
  assert.match(selected.slice(0, selected.indexOf("</g>")), /dfFactoryScene__worker--selected/);
  assert.match(selected.slice(0, selected.indexOf("</g>")), /class="dfFactoryScene__selection"/);
  for (const frame of workerFrames(workers[0])) assert.match(selected.slice(0, selected.indexOf("</g>")), new RegExp(`href="#df-frame-${frame}"`));
});

test("every generated person layer is reachable, including fallbacks", () => {
  const reached = new Set();
  // Still, frozen, and every beat of a worker's own clock; on the floor and at both tables.
  const moments = [undefined, ...[0, 100, 350, 500, 1000, 1500].flatMap((at) => ["still", "interacting", "walking"].flatMap((action) => [0, 1].map((frame) => ({ action, frame, at, direction: "east" }))))];
  for (const role of ["worker", "orchestrator"]) {
    for (const provider of ["claude_code", "codex", "shell"]) {
      for (const activity of ["busy", "waiting", "needs-you", "idle"]) {
        const base = { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0 };
        const add = (appearance) => { for (const motion of moments) for (const seat of [undefined, "resting", "planning"]) for (const frame of workerFrames({ id: "agent", name: "Agent", role, provider, activity, appearance }, motion, seat)) reached.add(frame); };
        for (const field of ["skin", "face", "shoes", "tool", "headwear"]) for (let index = 0; index < spriteOptions[field].length; index++) add({ ...base, [field]: index });
        for (let hair = 0; hair < spriteOptions.hair.length; hair++) for (let hair_colour = 0; hair_colour < spriteOptions.hair_colour.length; hair_colour++) add({ ...base, hair, hair_colour });
        for (let outfit = 0; outfit < spriteOptions.outfit.length; outfit++) for (let clothes_colour = 0; clothes_colour < spriteOptions.clothes_colour.length; clothes_colour++) add({ ...base, outfit, clothes_colour });
      }
    }
  }
  const fallback = { id: idForIdentity(2), name: "Fallback", role: "worker", provider: "unknown", activity: "debugging" };
  assert.match(workerFrames(fallback).at(-1), /^person\.system\.worker\.shell$/);
  assert.ok(workerFrames(fallback).includes("person.skin.2.idle") || workerFrames(fallback).some((name) => /^person\.skin\.\d\.idle$/.test(name)), "an unknown activity stands idle");
  const personFrames = Object.keys(spriteAtlas.frames).filter((name) => name.startsWith("person."));
  assert.deepEqual([...reached].sort(), personFrames.sort());
  // One cup, one pose: never a tool and a cup, never two held things.
  for (const motion of moments) for (const seat of [undefined, "resting", "planning"]) for (const activity of ["busy", "waiting", "needs-you", "idle"]) {
    const frames = workerFrames({ ...fallback, activity }, motion, seat);
    assert.ok(frames.filter((name) => name.startsWith("person.held.") || name.startsWith("person.tool.")).length <= 1, `${activity}/${seat}: ${frames}`);
    assert.equal(new Set(frames).size, frames.length);
  }
  // At the bench a carried tool is worked with; empty hands and mugs type.
  const bench = (tool, frame) => workerFrames({ ...fallback, activity: "busy", appearance: { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool, headwear: 0 } }, { action: "interacting", frame });
  const toolNames = spriteOptions.tool.map(({ name }) => name);
  for (const name of ["clipboard", "wrench", "tablet"]) {
    const tool = toolNames.indexOf(name);
    assert.deepEqual([0, 1].map((frame) => bench(tool, frame).filter((layer) => /skin|legs|tool|held/.test(layer)).map((layer) => layer.replace("person.", ""))),
      [["skin.0.idle", "legs.plain.stand", `tool.${tool}.low`], ["skin.0.walk.1", "legs.plain.stand", `tool.${tool}.high`]], name);
  }
  for (const name of ["none", "mug"]) assert.ok(bench(toolNames.indexOf(name), 0).includes("person.held.keyboard"), name);
  // Glasses blink with their lenses; bare eyes with the face's own shadow.
  const eyes = (face) => workerFrames({ ...fallback, appearance: { automatic: false, skin: 2, hair: 0, hair_colour: 0, face, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0 } }, { action: "still", frame: 0, at: 0 }).filter((layer) => layer.includes("blink"));
  assert.deepEqual([eyes(0), eyes(spriteOptions.face.findIndex(({ name }) => name === "glasses"))], [["person.blink.2"], ["person.blink.glasses"]]);
  // No clock value, however wrong, may name a frame the sheet does not hold.
  for (const at of [-1, -0.5, -4000, NaN, Infinity, 1e15]) for (const seat of [undefined, "resting", "planning"]) for (const activity of ["busy", "needs-you", "idle"]) {
    for (const name of workerFrames({ ...fallback, activity }, { action: "still", frame: 0, at }, seat)) assert.ok(name in spriteAtlas.frames, `${name} at ${at}`);
  }
  // A stilled clock rests every pose on its first frame, with open eyes.
  assert.deepEqual(workerFrames({ ...fallback, activity: "needs-you" }).filter((name) => /wave|blink/.test(name)).map((name) => name.split(".").slice(-2).join(".")), ["wave.0", "wave.0"]);
});

test("movement uses clear corridor lanes, doors and standing points", () => {
  const layout = layoutScene(topology);
  const placements = placeWorkers(layout, [
    { ...workers[0], id: "source", nodeId: "src" },
    { ...workers[0], id: "destination", nodeId: "lib" },
  ]);
  const sourcePlacement = placements.find((placement) => placement.id === "source");
  const destination = placements.find((placement) => placement.id === "destination");
  assert.ok(sourcePlacement && destination);
  const source = { ...sourcePlacement, x: sourcePlacement.x - 24 };
  const route = routeBetween(layout, source, destination);
  assert.ok(route !== undefined && route.length > 0);
  assert.deepEqual(pointOnRoute({ x: source.x, y: source.y }, route, route.length), { x: destination.x, y: destination.y });
  const sourceRoom = layout.rooms.find((room) => room.id === source.roomId);
  const destinationRoom = layout.rooms.find((room) => room.id === destination.roomId);
  const spine = layout.corridors.at(-1);
  assert.ok(sourceRoom && destinationRoom && spine);
  const sourceCorridor = layout.corridors.find((corridor) => corridor !== spine && corridor.y === sourceRoom.door.y && sourceRoom.door.x >= corridor.x && sourceRoom.door.x <= corridor.x + corridor.width);
  const destinationCorridor = layout.corridors.find((corridor) => corridor !== spine && corridor.y === destinationRoom.door.y && destinationRoom.door.x >= corridor.x && destinationRoom.door.x <= corridor.x + corridor.width);
  assert.ok(sourceCorridor && destinationCorridor);
  const center = spine.x + spine.width / 2;
  assert.deepEqual(route.points.slice(0, 7), [
    { x: sourceRoom.door.x, y: source.y }, sourceRoom.door,
    { x: sourceRoom.door.x, y: sourceCorridor.y + sourceCorridor.height / 2 },
    { x: center, y: sourceCorridor.y + sourceCorridor.height / 2 },
    { x: center, y: destinationCorridor.y + destinationCorridor.height / 2 },
    { x: destinationRoom.door.x, y: destinationCorridor.y + destinationCorridor.height / 2 }, destinationRoom.door,
  ]);
  assert.deepEqual(route.points.at(-1), { x: destination.x, y: destination.y });
  assertRouteGeometry(layout, source, route, "room to room");
  // Every crowded standing slot uses the actual opening, never its own offset
  // x-coordinate through a bottom wall.
  for (const offset of [0, -24, 24, -48, 48]) {
    const crowded = { ...source, x: sourcePlacement.x + offset };
    const crowdedRoute = routeBetween(layout, crowded, destination);
    assert.ok(crowdedRoute);
    assert.ok(crowdedRoute.points.some((point) => point.x === sourceRoom.door.x && point.y === sourceRoom.door.y));
    if (offset !== 0) assert.deepEqual(crowdedRoute.points.slice(0, 2), [{ x: sourceRoom.door.x, y: crowded.y }, sourceRoom.door]);
    assert.deepEqual(crowdedRoute.points.slice(-2), [destinationRoom.door, { x: destinationRoom.door.x, y: destination.y }]);
    assertRouteGeometry(layout, crowded, crowdedRoute, `crowded source ${offset}`);
  }
  const [resting, staging] = placeWorkers(layout, [
    { ...workers[0], id: "resting", location: "resting", nodeId: undefined },
    { ...workers[0], id: "staging", location: "unobserved", nodeId: undefined },
  ]);
  for (const common of [resting, staging]) {
    const outbound = routeBetween(layout, common, destination);
    const returned = routeBetween(layout, destination, common);
    assert.ok(outbound && returned, `${common.area} is connected by the displayed common-space spine`);
    assert.deepEqual(pointOnRoute(common, outbound, outbound.length), { x: destination.x, y: destination.y });
    assert.deepEqual(pointOnRoute(destination, returned, returned.length), { x: common.x, y: common.y });
    assertRouteGeometry(layout, destination, returned, `${common.area} return`);
  }
  const planner = placeWorkers(layout, [{ ...workers[0], nodeId: "omitted" }])[0];
  const planningRoute = routeBetween(layout, source, planner);
  assert.ok(planningRoute, "work outside displayed rooms goes to the planning table");
  assertRouteGeometry(layout, source, planningRoute, "planning table");
  assert.deepEqual(pointOnRoute(source, planningRoute, planningRoute.length), { x: planner.x, y: planner.y });
});

test("rapid retargeting uses the room containing the rendered worker", () => {
  const layout = layoutScene({
    digest: "rapid-retarget",
    nodes: ["A", "B", "C", "D"].map((id) => ({ id, parentId: "", path: id, label: id, kind: "directory", sizeBucket: "tiny" })),
  });
  const placements = new Map(placeWorkers(layout, ["A", "B", "C", "D"].map((id) => ({ id, name: id, role: "worker", activity: "busy", location: "working", nodeId: id }))).map((placement) => [placement.id, placement]));
  const a = placements.get("A");
  const b = placements.get("B");
  const c = placements.get("C");
  const d = placements.get("D");
  assert.ok(a && b && c && d);
  const enteredB = pointOnRoute(a, routeBetween(layout, a, b), routeBetween(layout, a, b).length - 8);
  const bRoom = layout.rooms.find((room) => room.id === "B");
  assert.ok(bRoom && enteredB.y < bRoom.door.y, "sample is inside B after its door");

  // B → C is replaced immediately by B → D. Both routes begin by leaving B,
  // rather than reusing A's row from the route that originally reached B.
  for (const destination of [c, d]) {
    const route = routeFromCurrent(layout, enteredB, destination);
    assert.ok(route);
    assert.deepEqual(route.points[0], { x: enteredB.x, y: bRoom.door.y });
    assertRouteGeometry(layout, enteredB, route, `rapid retarget to ${destination.id}`);
  }
});

test("retargets leave the current room or corridor through a clear lane", () => {
  const layout = layoutScene({
    digest: "retarget-lanes",
    nodes: ["A", "B", "C", "D"].map((id) => ({ id, parentId: "", path: id, label: id, kind: "directory", sizeBucket: "tiny" })),
  });
  const placements = new Map(placeWorkers(layout, ["A", "B", "C", "D"].map((id) => ({ id, name: id, role: "worker", activity: "busy", location: "working", nodeId: id }))).map((placement) => [placement.id, placement]));
  const source = placements.get("A");
  const destination = placements.get("D");
  assert.ok(source && destination);
  const room = layout.rooms.find((candidate) => candidate.id === source.roomId);
  const spine = layout.corridors.at(-1);
  assert.ok(room && spine);
  const row = layout.corridors.find((corridor) => corridor !== spine && corridor.y === room.door.y && room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width);
  assert.ok(row);
  const laneY = row.y + row.height / 2;
  const center = spine.x + spine.width / 2;
  const cases = [
    ["room", source],
    ["doorway", room.door],
    // A render from the superseded wall-line route still escapes perpendicularly
    // into this real corridor before it travels horizontally.
    ["wall line", { x: room.door.x - row.height, y: room.door.y }],
    ["row corridor", { x: room.door.x - row.height, y: laneY }],
    ["spine", { x: center, y: row.y - row.height / 2 }],
  ];
  for (const [name, current] of cases) {
    const route = routeFromCurrent(layout, current, destination);
    assert.ok(route, `${name} has a current-state route`);
    assertRouteGeometry(layout, current, route, `retarget from ${name}`);
  }
  const wallLine = routeFromCurrent(layout, cases[2][1], destination);
  assert.ok(wallLine);
  assert.deepEqual(wallLine.points.slice(0, 2), [
    { x: room.door.x - row.height, y: laneY },
    { x: center, y: laneY },
  ]);
  const fromSpine = routeFromSpine(layout, { x: center, y: row.y - row.height / 2 }, destination);
  assert.ok(fromSpine);
  assertRouteGeometry(layout, { x: center, y: row.y - row.height / 2 }, fromSpine, "direct spine route");
});

test("the production scene stops motion on disconnect and unmount", async () => {
  const topology = inventoryTopology;
  const requested = [];
  const cancelled = [];
  const requestAnimationFrame = globalThis.requestAnimationFrame;
  const cancelAnimationFrame = globalThis.cancelAnimationFrame;
  globalThis.requestAnimationFrame = (callback) => { requested.push(callback); return requested.length; };
  globalThis.cancelAnimationFrame = (id) => { cancelled.push(id); };
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryScene, { topology, workers, connected: true })); });
    const moved = [{ ...workers[0], nodeId: "lib" }, workers[1]];
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: moved, connected: true })); });
    assert.ok(requested.length > 0, "one scene clock schedules the route");
    const staticRoomProps = renderer.root.findByProps({ "data-room-id": "lib" }).props;
    const atlasProps = renderer.root.findByType("defs").props;
    const layoutBeforeTick = renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout;
    const workerBeforeTick = renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform;
    await act(async () => { requested.at(-1)(performance.now() + 50); });
    assert.equal(renderer.root.findByProps({ "data-room-id": "lib" }).props, staticRoomProps, "RAF does not recreate static room elements");
    assert.equal(renderer.root.findByType("defs").props, atlasProps, "RAF does not recreate the sprite atlas");
    assert.equal(renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout, layoutBeforeTick);
    assert.notEqual(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform, workerBeforeTick);
    await act(async () => { renderer.update(createElement(FactoryScene, { topology: { ...topology, digest: "metadata-only", nodes: topology.nodes.map((node) => ({ ...node, dependencies: { omitted: 1, links: [] } })) }, workers: moved, connected: true })); });
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "walking", "a metadata-only digest change preserves the route");

    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: moved, connected: false })); });
    const destination = placeWorkers(layoutScene(topology), moved).find((placement) => placement.id === workers[0].id);
    assert.ok(destination);
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform, `translate(${destination.x} ${destination.y})`);
    assert.ok(cancelled.length > 0, "disconnect cleans the pending animation frame");

    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: false })); });
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: true })); });
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "interacting", "reconnect snaps to current observed work instead of replaying the missed route");
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: moved, connected: true })); });
    assert.ok(requested.length > cancelled.length, "a later observed change starts a fresh route");
    await act(async () => { renderer.unmount(); });
    assert.equal(cancelled.length, requested.length, "unmount cleans every scheduled scene frame");
  } finally {
    globalThis.requestAnimationFrame = requestAnimationFrame;
    globalThis.cancelAnimationFrame = cancelAnimationFrame;
  }
});

// Nothing else runs the generator, so the shipped module could drift from it.
// A PNG's pixels: its IHDR and its inflated scanlines. The deflate bytes
// themselves depend on the zlib a Node was built with, so two builds of the
// same image may not share a byte; they must share every pixel.
function pngPixels(bytes) {
  assert.deepEqual([...bytes.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);
  const idats = [];
  let header;
  for (let offset = 8; offset < bytes.length;) {
    const length = bytes.readUInt32BE(offset);
    const type = bytes.toString("latin1", offset + 4, offset + 8);
    const data = bytes.subarray(offset + 8, offset + 8 + length);
    if (type === "IHDR") header = Buffer.from(data);
    if (type === "IDAT") idats.push(data);
    offset += 12 + length;
  }
  return Buffer.concat([header, inflateSync(Buffer.concat(idats))]);
}

// Text that embeds the sheet as a data URL compares by its text with every
// embedded PNG replaced, plus those PNGs' pixels in order.
function withPixels(text) {
  const pixels = [];
  const stripped = text.replaceAll(/data:image\/png;base64,([A-Za-z0-9+/=]+)/g, (_, base64) => {
    pixels.push(pngPixels(Buffer.from(base64, "base64")));
    return "data:image/png;base64,<pixels>";
  });
  return { stripped, pixels };
}

test("the committed sprite module is exactly what the generator writes", () => {
  const sprites = new URL("./sprites/", import.meta.url);
  const scratch = mkdtempSync(join(tmpdir(), "df-sprites-"));
  try {
    copyFileSync(new URL("gen-sprites.mjs", sprites), join(scratch, "gen-sprites.mjs"));
    // A failing generator reports its own assertion, not just a bad exit.
    execFileSync(process.execPath, ["gen-sprites.mjs"], { cwd: scratch, stdio: "pipe" });
    assert.deepEqual(pngPixels(readFileSync(join(scratch, "sprites.png"))), pngPixels(readFileSync(new URL("sprites.png", sprites))), "sprites.png");
    for (const name of ["sprites.generated.ts", "preview.html"]) {
      const fresh = withPixels(readFileSync(join(scratch, name), "utf8"));
      const committed = withPixels(readFileSync(new URL(name, sprites), "utf8"));
      assert.equal(fresh.stripped, committed.stripped, name);
      assert.deepEqual(fresh.pixels, committed.pixels, `${name} pixels`);
      assert.ok(fresh.pixels.length >= 1, `${name} embeds the sheet`);
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
});

test("stationary tasks expose affected areas and link the existing queue and questions", () => {
  const tasks = Array.from({ length: 11 }, (_, index) => ({
    id: `task-${index}`, agentId: "worker-b", projectId: "project",
    title: index === 0 ? "<script>unsafe & title</script>" : `Task ${index}`,
    status: index === 0 ? "running" : "queued",
    roomIds: index === 0 ? ["lib", "src", "outside"] : [],
    ...(index === 0 ? { representativeRoomId: "src" } : {}),
    humanRequestIds: index === 0 ? ["question-1"] : [],
  }));
  const markup = render({ tasks, onSelectTask() {}, onOpenQueue() {}, onSelectHumanRequest() {} });
  assert.equal((markup.match(/data-floor-queue=/g) ?? []).length, 1);
  assert.equal((markup.match(/data-work-footprint=/g) ?? []).length, 2);
  assert.match(markup, /Open queue, 10 tasks/);
  assert.match(markup, /data-workbench-task-id="task-0"/);
  assert.match(markup, /data-human-request-id="question-1"/);
  assert.match(markup, /aria-label="Question from Builder"/);
  assert.doesNotMatch(markup, /Work order|data-work-order-id/);
  assert.match(markup, /role="button" tabindex="0"/);
  assert.match(markup, /&lt;script&gt;unsafe &amp; title&lt;\/script&gt;/);
  assert.doesNotMatch(markup, /<script>/);
  assert.doesNotMatch(markup, /stroke-dasharray|CHANGES WITHIN|CHANGED|>task-0</);
  assert.equal((markup.match(/data-room-operating="true"/g) ?? []).length, 2);
  const disconnected = render({ tasks, connected: false });
  assert.doesNotMatch(disconnected, /data-room-operating="true"|aria-label="Working:/);
  assert.match(disconnected, /aria-label="Disconnected · last observed work:/);
  assert.doesNotMatch(render({ tasks: tasks.map((task) => ({ ...task, status: "succeeded" })) }), /data-room-operating="true"/);
  const noObservation = render({ tasks: [{ ...tasks[0], roomIds: [], humanRequestIds: [] }] });
  assert.doesNotMatch(noObservation, /data-work-footprint=|data-human-request-id=/);
});


test("queue selection picks the exact task sharing a representative workstation", () => {
  const tasks = ["first", "second"].map((id) => ({ id, agentId: "worker-b", projectId: "project", title: id, status: "running", roomIds: ["src"], representativeRoomId: "src", humanRequestIds: [] }));
  const markup = render({ tasks, selectedTaskId: "second", onSelectTask() {} });
  assert.match(markup, /data-workbench-task-id="second"/);
  assert.doesNotMatch(markup, /data-workbench-task-id="first"/);
});


test("larger workers fit compact common seating in narrow, wide and crowded floors", () => {
  for (const roomCount of [1, 3, 11]) {
    const nodes = Array.from({ length: roomCount }, (_, index) => ({ ...inventoryTopology.nodes[0], id: `room-${index}`, path: `room-${index}` }));
    const layout = layoutScene({ digest: "proportions", nodes });
    for (const count of [0, 1, 12, 100]) {
      const seating = commonSeating(layout, count, count);
      const seats = [...seating.resting, ...seating.planning];
      assert.equal(new Set(seats.map(({ x, y }) => `${x},${y}`)).size, seats.length);
      for (const seat of seats) {
        assert.ok(seat.x - WORKER_SIZE / 2 >= PADDING && seat.x + WORKER_SIZE / 2 <= layout.width - PADDING);
        for (const other of seats) if (seat !== other) assert.ok(Math.abs(seat.x - other.x) >= WORKER_SIZE || Math.abs(seat.y - other.y) >= WORKER_SIZE);
      }
      if (count > 0) assert.equal(seating.resting[0].y, seating.planning[0].y);
      const previewWorkers = seats.map((seat, index) => ({ ...workers[0], id: `seat-${index}`, location: index < seating.resting.length ? "resting" : "unobserved" }));
      const markup = render({ topology: { digest: "proportions", nodes }, workers: previewWorkers });
      assert.ok(markup.includes('transform="scale(1.25)"'));
      assert.equal((markup.match(/aria-label="Planning"/g) ?? []).length, count > 0 ? 1 : 0);
      const height = Number(markup.match(/viewBox="0 0 [^ ]+ ([^"]+)"/)[1]);
      assert.ok(height > Math.max(...seats.map(({ y }) => y + WORKER_SIZE / 2)));
    }
  }
});

test("narrow bays truncate full-width titles while retaining their accessible name", () => {
  for (const glyph of ["界", "😀", "👨‍👩‍👧‍👦", "🇯🇵", "é"]) {
    const label = glyph.repeat(15);
    const nodes = ["a", "b"].map((id) => ({ ...inventoryTopology.nodes[0], id, path: ".", label }));
    const markup = render({ topology: { digest: "wide-titles", nodes }, workers: [] });
    assert.match(markup, new RegExp(`>${glyph.repeat(6)}…</text>`));
    assert.match(markup, new RegExp(`aria-label="${label}`), "full title remains accessible");
    assert.doesNotMatch(markup, new RegExp(`>${label}</text>`));
  }
});

test("pictured contents and occupied surface slots leave door routes clear in every footprint", () => {
  for (const count of [1, 4, 11]) {
    const nodes = Array.from({ length: count }, (_, index) => ({ ...inventoryTopology.nodes[0], id: `room-${index}`, path: `room-${index}` }));
    const layout = layoutScene({ digest: "contents", nodes });
    for (const room of layout.rooms) {
      assert.equal(corridorReachability(layout), true);
      const surface = room.contents.find((item) => item.workSurface);
      assert.ok(surface);
      const placements = placeWorkers(layout, Array.from({ length: 12 }, (_, index) => ({ ...workers[0], id: `person-${index}`, nodeId: room.id })));
      const occupied = placements.filter((item) => item.area === "room");
      assert.ok(occupied.length > 0 && occupied.length < placements.length);
      assert.equal(occupied[0].x, surface.x + surface.width / 2);
      assert.equal(occupied[0].y, surface.y + surface.height + WORKER_SIZE / 2);
      for (const person of occupied) {
        assert.ok(person.x - WORKER_SIZE / 2 >= surface.x && person.x + WORKER_SIZE / 2 <= surface.x + surface.width);
        const lane = { x: Math.min(person.x, room.door.x) - WORKER_SIZE / 2, y: person.y - WORKER_SIZE / 2, width: Math.abs(person.x - room.door.x) + WORKER_SIZE, height: WORKER_SIZE };
        for (const object of room.contents) assert.ok(object.y + object.height <= lane.y || !overlaps(object, lane), "route never crosses pictured content interiors");
        const route = routeFromSpine(layout, { x: layout.corridors.at(-1).x + layout.corridors.at(-1).width / 2, y: layout.restingTop }, person);
        assert.ok(route);
        assertRouteGeometry(layout, { x: layout.corridors.at(-1).x + layout.corridors.at(-1).width / 2, y: layout.restingTop }, route, room.id);
      }
      for (let index = 0; index < room.contents.length; index++) for (const other of room.contents.slice(index + 1)) assert.equal(overlaps(room.contents[index], other), false);
    }
  }
});

test("compact tooltips open on hover, focus and tap and dismiss without a detail panel", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const priorWindow = globalThis.window;
  const priorDocument = globalThis.document;
  globalThis.window = { innerWidth: 390, innerHeight: 844 };
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { topology: inventoryTopology, workers: [] })); });
    const map = renderer.root.findByProps({ className: "dfFactoryFloor__map" });
    const target = renderer.root.findAll((node) => node.props["data-tooltip"])[0];
    const event = { target: { closest: (selector) => selector === '[role="tooltip"]' ? null : ({ querySelector: () => null, getBoundingClientRect: () => ({ left: 380, bottom: 830 }), getAttribute: () => target.props["data-tooltip"] }) } };
    for (const handler of ["onPointerOver", "onFocus", "onClick"]) {
      await act(async () => map.props[handler](event));
      const tip = renderer.root.findByProps({ role: "tooltip" });
      assert.match(tip.props.children, /200 source/);
      assert.ok(tip.props.style.left <= 118 && tip.props.style.top <= 732);
      await act(async () => map.props.onKeyDown({ key: "Escape" }));
      assert.equal(renderer.root.findAllByProps({ role: "tooltip" }).length, 0);
    }
    await act(async () => map.props.onFocus(event));
    await act(async () => map.props.onScroll());
    assert.equal(renderer.root.findAllByProps({ role: "tooltip" }).length, 0);
    globalThis.document = { activeElement: { ...event.target.closest("[data-tooltip]"), matches: (selector) => selector === "[data-tooltip]" } };
    await act(async () => map.props.onFocus(event));
    await act(async () => map.props.onScroll());
    assert.equal(renderer.root.findAllByProps({ role: "tooltip" }).length, 1, "autoscroll retains focus even after pointer interaction");
    await act(async () => map.props.onPointerLeave());
    assert.equal(renderer.root.findAllByProps({ role: "tooltip" }).length, 1, "pointer departure retains focused tooltip");
    assert.equal(renderer.root.findAllByType("select").length, 0);
    assert.equal(renderer.root.findAllByType("table").length, 0);
  } finally {
    await act(async () => renderer?.unmount());
    globalThis.window = priorWindow;
    globalThis.document = priorDocument;
  }
});

test("inventory equipment is bounded, counted once and clears standing lanes in every room size", () => {
  const counts = { source: 200, tests: 140, documentation: 36, configuration: 6, assets: 98, unclassified: 4 };
  for (const sizeBucket of ["tiny", "small", "medium", "large"]) {
    const node = { ...topology.nodes[0], sizeBucket, inventory: { direct: counts, total: counts, samples: ["main.go"], samples_omitted: 483 }, components: [{ id: "a", label: "A" }, { id: "b", label: "B" }, { id: "c", label: "C" }] };
    const room = layoutScene({ digest: "inventory", nodes: [node] }).rooms[0];
    const contents = room.contents;
    assert.ok(contents.length > 0 && contents.length <= 6);
    assert.equal(new Set(contents.map((item) => item.key)).size, contents.length);
    for (let index = 0; index < contents.length; index++) for (const other of contents.slice(index + 1)) assert.equal(overlaps(contents[index], other), false, "plans and equipment have distinct occupied rectangles");
    assert.deepEqual(contents, layoutScene({ digest: "inventory", nodes: [node] }).rooms[0].contents);
    const increased = { ...node, inventory: { ...node.inventory, direct: { ...counts, source: 201 }, total: { ...counts, source: 201 } } };
    const incremented = layoutScene({ digest: "increment", nodes: [increased] }).rooms[0].contents;
    assert.deepEqual(incremented.map(({ count, ...item }) => item), contents.map(({ count, ...item }) => item), "one more file updates a count, not repeated arithmetic equipment");
    assert.equal(incremented.find((item) => item.kind === "source").count, 201);
    for (const item of contents) {
      assert.ok(item.width >= 32 && item.height >= 24, "equipment is multi-tile, not tiny prop labels");
      assert.ok(item.x >= room.x + 8 && item.x + item.width <= room.x + room.width - 8);
      assert.ok(item.y >= room.y + 40 && item.y + item.height <= room.door.y - 32, "all crowded standing slots and routes stay clear");
    }
    for (const kind of Object.keys(counts)) {
      const group = contents.filter((item) => item.kind === kind);
      if (group.length) assert.equal(group.reduce((sum, item) => sum + item.count, 0), counts[kind], "multiple equipment groups partition represented counts");
    }
    const plain = { ...node, components: [], inventory: undefined };
    assert.deepEqual(layoutScene({ digest: "plain", nodes: [plain] }).rooms[0].contents, [], "unavailable does not invent equipment");
    assert.deepEqual(layoutScene({ digest: "zero", nodes: [{ ...plain, inventory: { ...node.inventory, total: Object.fromEntries(Object.keys(counts).map((key) => [key, 0])) } }] }).rooms[0].contents, [], "explicit zero inventory stays empty");
  }
});

test("stationary active workers animate while idle, reduced, hidden and disconnected clocks stop", async () => {
  const topology = inventoryTopology;
  const saved = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document };
  const frames = new Map();
  const timers = new Map();
  let clock = 0;
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = (callback, delay) => { assert.equal(delay, 200); timers.set(++next, callback); return next; };
  globalThis.clearTimeout = (id) => timers.delete(id);
  let next = 0, visibilityListener, mediaListener;
  const media = { matches: false, addEventListener: (_event, listener) => { mediaListener = listener; }, removeEventListener() {} };
  globalThis.window = { matchMedia: () => media };
  globalThis.document = { visibilityState: "visible", addEventListener: (_event, listener) => { visibilityListener = listener; }, removeEventListener() {} };
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { topology, workers, connected: true })); });
    assert.equal(timers.size, 1);
    const staticRoom = renderer.root.findByProps({ "data-room-id": "src" }).props;
    const frameNames = () => renderer.root.findByProps({ "data-worker-id": workers[0].id }).findAllByType("use").map((node) => node.props.href);
    assert.equal(frames.size, 0, "stationary work never schedules RAF");
    const seen = new Set();
    for (clock = 200; clock <= 2000; clock += 200) { await act(async () => { timers.values().next().value(); }); seen.add(frameNames().join()); }
    assert.ok(seen.size >= 2, "stationary interaction frame advances");
    assert.equal(renderer.root.findByProps({ "data-room-id": "src" }).props, staticRoom);
    await act(async () => { clock += 1; globalThis.document.visibilityState = "hidden"; visibilityListener(); });
    assert.equal(timers.size, 0);
    await act(async () => { clock += 1; globalThis.document.visibilityState = "visible"; visibilityListener(); });
    assert.equal(timers.size, 1);
    await act(async () => { media.matches = true; mediaListener(); });
    assert.equal(timers.size, 0);
    await act(async () => { media.matches = false; mediaListener(); });
    assert.equal(timers.size, 1);
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: false })); });
    assert.equal(timers.size, 0);
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: true })); });
    assert.equal(timers.size, 1);
    const planningWorkers = [...workers, { ...workers[0], id: "planning", location: "unobserved", nodeId: undefined }];
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: planningWorkers, connected: true, appearance: { scenery: "subtle", animation: "off" } })); });
    assert.equal(timers.size, 0, "animation-off stops the clock");
    assert.equal(frames.size, 0, "animation-off stops movement");
    assert.equal(renderer.root.findByProps({ "data-worker-id": "planning" }).findAllByProps({ "data-planning-light": "" }).length, 1, "connected planning lamp stays lit with animation off");
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: planningWorkers, connected: false, appearance: { scenery: "subtle", animation: "off" } })); });
    assert.equal(renderer.root.findAllByProps({ "data-planning-light": "" }).length, 0, "disconnect extinguishes planning lamps");
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: workers.map((worker) => ({ ...worker, activity: "waiting", location: "resting" })), connected: false })); });
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: [], connected: true })); });
    assert.equal(timers.size, 0, "idle floor leaves no continuous animation clock");
    await act(async () => { renderer.update(createElement(FactoryScene, { topology: { ...topology, nodes: topology.nodes.map(({ inventory, ...node }) => node) }, workers, connected: true })); });
    assert.equal(timers.size, 1, "people on the floor keep its slow pulse");
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "still");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});


test("direct rooms picture only direct counts while subtree rooms retain descendants", () => {
  const direct = { source: 1, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 };
  const node = { ...inventoryTopology.nodes[0], inventory: { ...inventoryTopology.nodes[0].inventory, direct } };
  const displayed = (inventoryScope) => layoutScene({ digest: "scope", nodes: [{ ...node, inventoryScope }] }).rooms[0].contents;
  assert.deepEqual(displayed("direct").map(({ kind, count }) => ({ kind, count })), [{ kind: "source", count: 1 }]);
  assert.equal(displayed("subtree").find((item) => item.kind === "source").count, fileCounts.source);
});

test("dependencies are cabled between shown rooms, routes light on inspection, and the floor carries no native tooltip", async () => {
  const link = (nodeId) => ({ omitted: 0, links: [{ nodeId, label: nodeId, path: nodeId, direction: "to", weight: 1 }] });
  const linked = { ...topology, nodes: topology.nodes.map((node) => ({ ...node, dependencies: node.id === "lib" ? link("src") : node.id === "src" ? link("lib") : link("hidden") })) };
  const markup = renderToStaticMarkup(createElement(FactoryScene, { topology: linked, workers: [] }));
  assert.ok([...markup.matchAll(/data-wire-trunk="1" d="([^"]+)"/g)].every((match) => !/NaN|undefined/.test(match[1])));
  assert.match(markup, /data-wire-trunk/);
  assert.doesNotMatch(markup, /data-wire=|<title>/);
  assert.doesNotMatch(renderToStaticMarkup(createElement(FactoryScene, { topology, workers: [] })), /data-wire/);

  const priorWindow = globalThis.window;
  globalThis.window = { innerWidth: 390, innerHeight: 844 };
  try {
    let renderer;
    await act(async () => { renderer = create(createElement(FactoryScene, { topology: linked, workers: [] })); });
    const map = renderer.root.findByProps({ className: "dfFactoryFloor__map" });
    const over = (roomId) => ({ target: { closest: (selector) => selector === "[data-room-id]" ? { getAttribute: () => roomId } : null } });
    const lit = () => renderer.root.findAll((node) => node.props["data-wire"] !== undefined).map((node) => node.props["data-wire"]);
    // Both directions of one edge are a single wire; hidden endpoints draw nothing.
    await act(async () => map.props.onPointerOver(over("src")));
    assert.deepEqual(lit(), ["lib src"]);
    await act(async () => map.props.onPointerOver(over("repo")));
    assert.deepEqual(lit(), []);
    for (const leave of [() => map.props.onBlur(), () => map.props.onKeyDown({ key: "Escape" }), () => map.props.onPointerLeave()]) {
      await act(async () => map.props.onFocus(over("lib")));
      assert.deepEqual(lit(), ["lib src"]);
      await act(async () => leave());
      assert.deepEqual(lit(), [], "a lit route never outlives the inspection that lit it");
    }
    assert.match(renderer.root.findAll((node) => node.props["data-tooltip"]?.includes("Wired to"))[0].props["data-tooltip"], /Wired to (lib|src|hidden)$/);
  } finally { globalThis.window = priorWindow; }
});
