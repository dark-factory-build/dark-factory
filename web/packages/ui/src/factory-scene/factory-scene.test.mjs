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
import { PADDING, layoutScene, placeWorkers } from "../../dist/src/factory-scene/scene.js";
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

/** Verify the rendered 16px person stays on the floor and crosses walls only at doors. */
function assertRouteGeometry(layout, start, route, message) {
  const clearance = spriteAtlas.frame / 2;
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
  assert.equal(layout.rooms[0].furnishings.length, 0, "unknown composition does not invent room contents");

  for (const room of layout.rooms) {
    assert.ok(room.door.y === room.y + room.height && room.door.x > room.x && room.door.x < room.x + room.width);
    assert.ok(room.workstation.x >= room.x && room.workstation.x < room.x + room.width);
    assert.ok(room.standing.x >= room.x && room.standing.x < room.x + room.width);
    assert.ok(room.standing.y > room.workstation.y + 16, `standing slot clears workstation in ${room.id}`);
    assert.ok(layout.corridors.some((corridor) => room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width && room.door.y >= corridor.y && room.door.y <= corridor.y + corridor.height), `door ${room.id} reaches a corridor`);
  }
  for (const count of [1, 4, 24]) {
    const many = { digest: `${count}`, nodes: Array.from({ length: count }, (_, index) => ({ id: `room-${index}`, parentId: "", path: `room-${index}`, label: `Room ${index}`, kind: "directory", sizeBucket: ["empty", "tiny", "small", "medium", "large"][index % 5] })) };
    const connected = layoutScene(many);
    assert.equal(corridorReachability(connected), true, `${count} rooms remain reachable from the spine`);
    for (const room of connected.rooms) {
      const furniture = { x: room.workstation.x, y: room.workstation.y, width: 16, height: 16 };
      const person = { x: room.standing.x - 8, y: room.standing.y - 8, width: 16, height: 16 };
      assert.equal(overlaps(furniture, person), false, `standing slot clears furniture in ${room.id}`);
      const walkway = { x: room.standing.x - 8, y: room.standing.y - 8, width: 16, height: room.door.y - room.standing.y + 8 };
      assert.ok(walkway.x >= room.x && walkway.x + walkway.width <= room.x + room.width && walkway.y >= room.y && walkway.y + walkway.height <= room.y + room.height, `standing path stays inside ${room.id}`);
      assert.equal(overlaps(furniture, walkway), false, `standing path clears furniture in ${room.id}`);
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
  assert.equal(areas[0], areas[1], "empty and tiny rooms stay compact");
  assert.ok(areas[1] < areas[2] && areas[2] < areas[3] && areas[3] < areas[4]);
  assert.ok(areas[4] <= areas[0] * 3, "one large component cannot dominate the map");
  assert.equal(corridorReachability(sized), true, "mixed footprints share reachable doorway edges");

  const placements = placeWorkers(layout, workers);
  assert.deepEqual(placements, placeWorkers(layout, [...workers].reverse()));
  assert.equal(workerFrames(workers[0]).length, 8);
  assert.match(workerFrames(workers[0]).at(-1), /person\.system\.worker\.codex\.busy/);
  assert.match(workerFrames({ ...workers[0], provider: "made_up" }).at(-1), /person\.system\.worker\.shell\.busy/);

  const first = render();
  const reordered = render({
    topology: { ...topology, nodes: [...topology.nodes].reverse() },
    workers: [...workers].reverse(),
  });
  assert.equal(first, reordered);
  assert.match(first, /data-topology-digest="fixture-1"/);
  assert.equal(first.includes(">STAGED</text>"), false);
  assert.match(first, /data-room-id="src"/);
  // The room subtitle carries the served size bucket, and nothing when the
  // room stands for a project whose structure is unavailable.
  assert.match(first, />PACKAGE · MEDIUM</);
  assert.match(renderToStaticMarkup(createElement(FactoryScene, {
    topology: { digest: "d", nodes: [{ id: "p", parentId: "", path: "Project", label: "Project", kind: "repository" }] },
    workers: []
  })), />STRUCTURE UNAVAILABLE<\/text>/);
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
    assert.deepEqual(frame, worker.location === "working" ? workerFrames(worker, { action: "interacting", frame: 0 }) : workerFrames(worker));
    for (const name of frame) assert.ok(name in spriteAtlas.frames, name);
  }
  assert.equal(first.includes("dfFactoryScene__alternate"), false);
  assert.match(first, /data-corridor/);
  assert.equal(first.includes("<animate"), false);
  const unobserved = render({ workers: [{ ...workers[0], location: "unobserved", nodeId: undefined }] });
  assert.match(unobserved, /UNKNOWN LOCATION · 1/);
  assert.match(unobserved, /working; location not yet observed/);
  assert.match(first, /RESTING AREA · 1/);
  const restingY = Number(first.match(/data-worker-id="worker-a"[^>]*transform="translate\([^ ]+ ([0-9.]+)\)"/)[1]);
  const roomYs = [...first.matchAll(/data-room-id="[^"]+"[^>]*>[\s\S]*?<rect x="[^"]+" y="([0-9.]+)"/g)].map((match) => Number(match[1]));
  const roomBottoms = [...first.matchAll(/data-room-id="[^"]+"[^>]*>\s*<title>[\s\S]*?<rect x="[^"]+" y="([0-9.]+)" width="[^"]+" height="([0-9.]+)"/g)].map((match) => Number(match[1]) + Number(match[2]));
  assert.ok(restingY >= Math.max(...roomBottoms) + 24, "resting area stays below every room with clearance");
  const capped = render({ workers: [{ ...workers[0], location: "working", locationLabel: "Source", nodeId: undefined }], omittedLocations: 1 });
  assert.match(capped, /OUTSIDE DISPLAYED ROOMS · 1/);
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
  assert.deepEqual(densePlacements[0], { id: "worker-0", area: "room", roomId: "src", x: srcRoom.standing.x, y: srcRoom.standing.y });
  for (const placement of densePlacements.filter(({ y }) => y < layout.height)) {
    assert.ok(placement.x - 8 >= srcRoom.x && placement.x + 8 <= srcRoom.x + srcRoom.width);
    assert.ok(placement.y - 8 >= srcRoom.y && placement.y + 8 <= srcRoom.y + srcRoom.height);
    assert.ok(placement.y - 8 >= srcRoom.y + 40, "room workers stay below the title and kind");
  }
  const denseSvg = render({ workers: denseWorkers });
  assert.match(denseSvg, /WORKER AREA AT CAPACITY · 95/);
  const denseHeight = Number(denseSvg.match(/viewBox="0 0 [^ ]+ ([^"]+)"/)[1]);
  assert.ok(denseHeight > Math.max(...densePlacements.map(({ y }) => y + 8)));

  const mixedPlacements = placeWorkers(layout, [
    ...denseWorkers,
    { ...workers[1], location: "resting" },
  ]);
  const restingBottom = Math.max(...mixedPlacements.filter((placement) => placement.area === "resting").map((placement) => placement.y + 8));
  const overflowTop = Math.min(...mixedPlacements.filter((placement) => placement.area === "overflow").map((placement) => placement.y));
  assert.ok(overflowTop - restingBottom >= 24, "resting and overflow areas have separate rows");
  const directOutside = placeWorkers(layout, [
    { ...workers[1], location: "resting" },
    { ...workers[0], location: "unobserved", nodeId: undefined },
  ]);
  const directRestingBottom = Math.max(...directOutside.filter((placement) => placement.area === "resting").map((placement) => placement.y + 8));
  const directStagingTop = Math.min(...directOutside.filter((placement) => placement.area === "staging").map((placement) => placement.y));
  assert.ok(directStagingTop - directRestingBottom >= 24, "direct placement keeps resting and unobserved workers apart");

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
  assert.match(emptySvg, /aria-label="RESTING AREA · 20"/);
  const emptyArea = emptySvg.match(/aria-label="RESTING AREA · 20"><rect x="[^"]+" y="([0-9.]+)" width="[^"]+" height="([0-9.]+)"/);
  const emptyLabel = emptySvg.match(/<text x="[^"]+" y="([0-9.]+)"[^>]*>EMPTY FLOOR<\/text>/);
  const emptyHeight = Number(emptySvg.match(/viewBox="0 0 [^ ]+ ([0-9.]+)"/)[1]);
  assert.ok(emptyArea !== null && emptyLabel !== null);
  assert.ok(Number(emptyArea[1]) > Number(emptyLabel[1]) + 10, "resting area clears the empty-floor label");
  assert.ok(emptyHeight > Number(emptyLabel[1]), "empty-floor label remains inside the scene");
  const emptyWithStaging = render({
    topology: { digest: "empty", nodes: [] },
    workers: [...emptyWorkers, { ...workers[0], location: "unobserved", nodeId: undefined }],
  });
  const stagingArea = emptyWithStaging.match(/aria-label="UNKNOWN LOCATION · 1"><rect x="[^"]+" y="([0-9.]+)"/);
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
  for (const role of ["worker", "orchestrator"]) {
    for (const provider of ["claude_code", "codex", "shell"]) {
      for (const activity of ["busy", "waiting", "needs-you", "idle"]) {
        const base = { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0 };
        const add = (appearance) => { for (const frame of workerFrames({ id: "agent", name: "Agent", role, provider, activity, appearance })) reached.add(frame); };
        for (const field of ["skin", "face", "shoes", "tool", "headwear"]) for (let index = 0; index < spriteOptions[field].length; index++) add({ ...base, [field]: index });
        for (let hair = 0; hair < spriteOptions.hair.length; hair++) for (let hair_colour = 0; hair_colour < spriteOptions.hair_colour.length; hair_colour++) add({ ...base, hair, hair_colour });
        for (let outfit = 0; outfit < spriteOptions.outfit.length; outfit++) for (let clothes_colour = 0; clothes_colour < spriteOptions.clothes_colour.length; clothes_colour++) add({ ...base, outfit, clothes_colour });
      }
    }
  }
  const fallback = { id: idForIdentity(2), name: "Fallback", role: "worker", provider: "unknown", activity: "debugging" };
  assert.match(workerFrames(fallback).at(-1), /person\.system\.worker\.shell\.idle/);
  for (const frame of workerFrames(fallback)) reached.add(frame);
  for (const direction of ["north", "south", "east", "west"]) for (const frame of [0, 1]) {
    for (const name of workerFrames(fallback, { action: "walking", direction, frame })) reached.add(name);
  }
  for (const frame of [0, 1]) for (const name of workerFrames(fallback, { action: "interacting", frame })) reached.add(name);
  const personFrames = Object.keys(spriteAtlas.frames).filter((name) => name.startsWith("person."));
  assert.deepEqual([...reached].sort(), personFrames.sort());
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
    const crowded = { ...source, x: sourceRoom.standing.x + offset };
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
  assert.equal(routeBetween(layout, source, { ...destination, area: "outside" }), undefined, "omitted rooms never gain an invented route");
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

    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers: moved, connected: false })); });
    const destination = placeWorkers(layoutScene(topology), moved).find((placement) => placement.id === workers[0].id);
    assert.ok(destination);
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform, `translate(${destination.x} ${destination.y})`);
    assert.ok(cancelled.length > 0, "disconnect cleans the pending animation frame");

    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: false })); });
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, connected: true })); });
    assert.equal(requested.length, cancelled.length, "reconnect snaps to its current snapshot instead of replaying the missed route");
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
  const noObservation = render({ tasks: [{ ...tasks[0], roomIds: [], humanRequestIds: [] }] });
  assert.doesNotMatch(noObservation, /data-work-footprint=|data-human-request-id=/);
});


test("queue selection picks the exact task sharing a representative workstation", () => {
  const tasks = ["first", "second"].map((id) => ({ id, agentId: "worker-b", projectId: "project", title: id, status: "running", roomIds: ["src"], representativeRoomId: "src", humanRequestIds: [] }));
  const markup = render({ tasks, selectedTaskId: "second", onSelectTask() {} });
  assert.match(markup, /data-workbench-task-id="second"/);
  assert.doesNotMatch(markup, /data-workbench-task-id="first"/);
});


test("evidenced furnishings have clear interaction positions in every footprint", () => {
  const rich = { digest: "contents", nodes: ["empty", "tiny", "small", "medium", "large"].map((sizeBucket) => ({
    id: sizeBucket, parentId: "", path: sizeBucket, label: sizeBucket, kind: "package", sizeBucket,
    language: "go", childCount: 2, dependencies: { omitted: 0, links: [{ nodeId: "other", label: "Other", path: "other", direction: "to", weight: 1 }] },
  })) };
  const layout = layoutScene(rich);
  assert.equal(corridorReachability(layout), true);
  for (const room of layout.rooms) {
    assert.deepEqual(room.furnishings.map((item) => item.kind), ["board", "connections"]);
    const objects = [room.workstation, ...room.furnishings].map((item) => ({ ...item, width: 16, height: 16 }));
    for (const item of room.furnishings) {
      const passage = { x: Math.min(item.standing.x, room.standing.x) - 8, y: room.standing.y - 8,
        width: Math.abs(item.standing.x - room.standing.x) + 16, height: 16 };
      assert.equal(item.standing.y, room.standing.y);
      assert.ok(passage.x >= room.x + 4 && passage.x + passage.width <= room.x + room.width - 4);
      assert.ok(objects.every((object) => !overlaps(object, passage)), "every interaction joins the doorway route without crossing furniture");
    }
  }
});

test("room inspection distinguishes bounded static evidence, hidden endpoints and unavailable support", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const links = Array.from({ length: 10 }, (_, index) => ({ nodeId: `hidden-${index}`, label: `Hidden ${index}`, path: `area-${index}`, direction: "to", weight: 1 }));
  const evidenced = { ...topology, nodes: topology.nodes.map((node, index) => index === 0 ? { ...node, dependencies: { omitted: 3, links } } : node) };
  const entered = [];
  let renderer;
  await act(async () => { renderer = create(createElement(FactoryScene, { topology: evidenced, workers: [], onEnterRoom: (id) => entered.push(id) })); });
  const select = () => renderer.root.findByProps({ "aria-label": "Inspect room" });
  await act(async () => { select().props.onChange({ target: { value: "repo" } }); });
  const details = () => renderer.root.findByProps({ "aria-label": "Room details" });
  assert.ok(details().findAllByType("p").some((p) => typeof p.props.children === "string" && p.props.children.startsWith("Partial static evidence")));
  const buttons = details().findAllByType("button");
  assert.equal(buttons.length, 8, "the selected neighbourhood remains bounded");
  await act(async () => { buttons[0].props.onClick(); });
  assert.deepEqual(entered, ["hidden-0"], "hidden endpoint navigation uses its served identity");
  await act(async () => { select().props.onChange({ target: { value: "lib" } }); });
  assert.ok(details().findAllByType("p").some((p) => p.props.children === "Dependencies unavailable from this daemon."));
  await act(async () => { renderer.unmount(); });
});
