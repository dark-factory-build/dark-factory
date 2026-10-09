import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { inflateSync } from "node:zlib";
import { join } from "node:path";
import test from "node:test";
import { Profiler, createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { AgentSprite, FactoryScene, commonsNeeds } from "../../dist/src/factory-scene/factory-scene.js";
import { WORKER_SIZE, breakRoomNook, commonSeating, commonTables, layoutScene, placeCrates, placeErrands, placeWorkers, standable } from "../../dist/src/factory-scene/scene.js";
import { deriveProductionView, projectCrates } from "../../dist/src/production-view.js";
import { publicFloor } from "../../dist/src/public-floor.js";
import { breakRoomHabit, resolvedAppearance, restingItem, spriteOptions, workerFrames, workerPhase } from "../../dist/src/factory-scene/appearance.js";
import { crosses, findRoute, pointOnRoute, walkable } from "../../dist/src/factory-scene/movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "../../dist/src/factory-scene/sprites/sprites.generated.js";
import { busy, machine, sceneGraph, unit, unitsOf, unread } from "../../../../fixtures/scene.mjs";

const graph = sceneGraph([
  unit("repo"),
  unit("lib", { label: "<Shared & Library> 📦📦", machines: [machine("lib-in", "ingress", { label: "/in", trigger: "request", reading: busy }), machine("lib-db", "store", { label: "db" })] }),
  unit("src", { machines: [machine("src-job", "job")] }),
]);

const commons = { social: "commons", scenery: "subtle", animation: "follow-device" };

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

/** The floor a scene with these props lays out: its commons sized for its agents and implements. */
const floorFor = (floorGraph, props = {}) => layoutScene(floorGraph, undefined, commonsNeeds({ workers, appearance: commons, ...props }));

function render(props = {}) {
  return renderToStaticMarkup(createElement(FactoryScene, { graph, appearance: commons, workers, appearance: commons, ...props }));
}

/**
 * A walk a 20px person can take: every leg clear of every solid by their
 * radius (a seat may be stepped into beside its own table, never through it),
 * never through a machine, ending exactly where it was sent.
 */
function assertWalk(layout, start, route, to, message) {
  assert.ok(route, `${message}: no route`);
  const points = [start, ...route.points];
  for (let index = 1; index < points.length; index++) {
    assert.ok(walkable(layout.solids, points[index - 1], points[index]), `${message}: leg ${index} passes too close to a solid`);
    for (const station of layout.stations) assert.equal(crosses(points[index - 1], points[index], station), false, `${message}: leg ${index} goes through ${station.entityId}`);
  }
  assert.deepEqual({ x: points.at(-1).x, y: points.at(-1).y }, { x: to.x, y: to.y }, `${message}: ends where it was sent`);
}

test("the pure scene model feeds a deterministic SVG renderer", () => {
  const layout = layoutScene(graph);
  assert.deepEqual(layout, layoutScene({ ...graph, units: [...graph.units].reverse() }));
  assert.deepEqual(layout.stations.filter((station) => station.unit === "src").map((station) => station.entityId).sort(), ["src", "src-job"], "a unit is its main machine and its machines");
  for (const count of [1, 2, 3, 4, 5, 11, 24]) {
    const connected = layoutScene(unitsOf(Array.from({ length: count }, (_, index) => `unit-${index}`)));
    const rest = commonSeating(connected, 1, 0).resting[0];
    for (const station of connected.stations) {
      const placement = placeWorkers(connected, [{ ...workers[0], nodeId: station.entityId }])[0];
      assert.equal(placement.stationId, station.entityId);
      assertWalk(connected, rest, findRoute(connected, rest, placement), placement, `${count}: ${station.entityId}`);
    }
  }

  const placements = placeWorkers(layout, workers);
  assert.deepEqual(placements, placeWorkers(layout, [...workers].reverse()));
  assert.equal(workerFrames(workers[0]).length, 9);
  assert.deepEqual(workerFrames(workers[0]).slice(-2), ["person.system.worker.codex", "person.held.keyboard"], "what is held is drawn over the badge");
  assert.ok(workerFrames({ ...workers[0], provider: "made_up" }).includes("person.system.worker.shell"));
  assert.equal(workerFrames(workers[1]).at(-1), "person.alert", "the alert rides above whoever needs you");

  const first = render();
  const reordered = render({ graph: { ...graph, units: [...graph.units].reverse() }, workers: [...workers].reverse() });
  assert.equal(first, reordered);
  assert.match(first, /data-graph-digest="repo,lib,src"/);
  assert.equal(first.includes(">STAGED</text>"), false);
  assert.match(first, /data-entity-id="src"/);
  assert.doesNotMatch(first, /data-room-id|data-corridor|data-fence|data-room-walls/, "no rooms, corridors, walls or fence");
  assert.match(first, /Find machine/);
  assert.doesNotMatch(first, /Inspect room|<select/);
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
    // Away from a machine a worker sits, resting or planning. A first render
    // starts every clock at zero, so each worker stands at their own phase.
    const seat = worker.location === "working" ? undefined : worker.location === "unobserved" ? "planning" : "resting";
    const action = rendered.match(/data-worker-action="([^"]+)"/)[1];
    assert.deepEqual(frame, workerFrames(worker, { action, frame: 0, at: workerPhase(worker.id) }, seat));
    if (seat !== undefined) assert.equal(frame.some((name) => name.startsWith("person.tool.")), false, "tools are put down at a table");
    for (const name of frame) assert.ok(name in spriteAtlas.frames, name);
  }
  assert.equal(first.includes("dfFactoryScene__alternate"), false);
  assert.equal(first.includes("<animate"), false);
  const unobserved = render({ workers: [{ ...workers[0], location: "unobserved", nodeId: undefined }] });
  assert.match(unobserved, /aria-label="Work tables"/);
  assert.match(unobserved, /working; location not yet observed/);
  assert.doesNotMatch(unobserved, /data-worker-task-id/);
  assert.match(first, /aria-label="Break room · ambient"/);
  const restPosition = first.match(/data-worker-id="worker-a"[^>]*transform="translate\(([^ ]+) ([0-9.]+)\)"/);
  assert.ok(Number(restPosition[1]) >= layout.commons.x + 12 && Number(restPosition[1]) <= layout.commons.x + layout.commons.width - 12);
  assert.equal(Number(restPosition[2]), layout.restingTop, "resting is at the commons");
  const capped = render({ workers: [{ ...workers[0], location: "working", locationLabel: "Source", nodeId: undefined }] });
  assert.match(capped, /aria-label="Work tables"/);
  assert.match(capped, /at the machine its observed changes touch in Source; outside the machines shown/);
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
  const line = layout.stations.find((station) => station.entityId === "src");
  assert.deepEqual(densePlacements[0], { id: "worker-0", area: "work", stationId: "src", ...line.anchor });
  for (const placement of densePlacements.filter(({ area }) => area === "work")) assert.ok(standable(layout, placement), `${placement.id} stands on free floor`);
  const denseSvg = render({ workers: denseWorkers });
  assert.match(denseSvg, /aria-label="Work tables"/);
  const denseHeight = Number(denseSvg.match(/viewBox="0 0 [^ ]+ ([^"]+)"/)[1]);
  assert.ok(denseHeight > Math.max(...densePlacements.map(({ y }) => y + 8)));

  const directOutside = placeWorkers(layout, [
    { ...workers[1], location: "resting" },
    { ...workers[0], location: "unobserved", nodeId: undefined },
  ]);
  assert.ok(Math.abs(directOutside[0].y - directOutside[1].y) >= 24, "break and planning seats remain separate");

  const changed = layoutScene({ ...graph, digest: "fixture-2", units: [...graph.units, unit("docs")] });
  assert.equal(changed.stations.some((station) => station.entityId === "docs"), true);
  assert.notDeepEqual(changed, layout);

  const emptyLayout = floorFor(sceneGraph([]), { workers: denseWorkers.slice(0, 20) });
  const emptyWorkers = denseWorkers.slice(0, 20).map(({ nodeId: _nodeId, location: _location, ...worker }) => ({ ...worker, location: "resting" }));
  const emptyPlacements = placeWorkers(emptyLayout, emptyWorkers);
  assert.equal(new Set(emptyPlacements.map(({ x, y }) => `${x},${y}`)).size, emptyWorkers.length);
  const emptySvg = render({ graph: sceneGraph([]), workers: emptyWorkers });
  assert.match(emptySvg, /NO OPERATIONAL STRUCTURE INFERRED YET/);
  // Before the plant has been read, an empty floor says it is reading, not that nothing was found.
  const readingSvg = render({ graph: sceneGraph([]), workers: emptyWorkers, reading: true });
  assert.match(readingSvg, /READING THE PLANT…/);
  assert.doesNotMatch(readingSvg, /NO OPERATIONAL STRUCTURE/);
  // An empty floor in a wide column stays a panel, not a poster.
  assert.match(emptySvg, new RegExp(`max-width:${emptyLayout.width}px`));
  assert.match(emptySvg, /aria-label="Break room · ambient"/);
  const emptyLabel = emptySvg.match(/<text x="([^"]+)" y="([0-9.]+)"[^>]*>NO OPERATIONAL STRUCTURE INFERRED YET<\/text>/);
  assert.ok(emptyLabel !== null && Number(emptyLabel[2]) < emptyLayout.commons.y, "the empty-floor label stands clear of the commons");
});

test("nothing live, runtime-only or worker-made moves a machine", () => {
  const owned = (id, extra) => unit(id, { machines: [machine(`${id}-in`, "ingress", { trigger: "request" }), machine(`${id}-db`, "store")], ...extra });
  const base = sceneGraph([owned("zeta"), owned("beta"), owned("alpha"), owned("front")], { shared: [machine("shared-queue", "queue")], parties: [machine("github", "external")],
    flows: [{ from: "alpha", to: "shared-queue", kind: "publishes", reading: unread }, { from: "beta", to: "shared-queue", kind: "consumes", reading: unread }, { from: "front", to: "github", kind: "calls", reading: unread }] });
  const layout = layoutScene(base);
  const stable = (floor) => floor.stations.map(({ entityId: key, x, y, width, height }) => ({ key, x, y, width, height }));
  const live = { ...base, units: base.units.map((item) => ({ ...item, reading: { ...busy, errorPermille: 500, latencyMs: 9000 }, machines: item.machines.map((m) => ({ ...m, reading: { ...busy, ratePerHour: 1e6, state: "failing" } })) })),
    flows: base.flows.map((flow) => ({ ...flow, reading: { ...busy, ratePerHour: 1e6 } })) };
  assert.deepEqual(stable(layoutScene(live)), stable(layout), "traffic, faults and state move nothing");
  const workersEverywhere = Array.from({ length: 60 }, (_, index) => ({ id: `w${index}`, name: "w", role: "worker", activity: "busy", location: "working", nodeId: "alpha" }));
  placeWorkers(layout, workersEverywhere);
  assert.deepEqual(stable(layoutScene(base)), stable(layout), "nor do workers");
  assert.equal(layout.stations.find((station) => station.entityId === "github").shape, "gate", "an external party is a gate, visibly outside");
  // Runtime-only activity is new structure, but it arrives beside what stands: nothing already there moves relative to anything else.
  const arrived = layoutScene({ ...base, digest: "arrived", quarantine: [machine("mystery", "unknown", { reading: { ...busy, evidence: "runtime" }, claims: "alpha" })] }, layout);
  const relative = (floor) => { const [first] = floor.stations; return stable(floor).filter((station) => station.key !== "mystery").map((station) => ({ ...station, x: station.x - first.x, y: station.y - first.y })); };
  assert.deepEqual(relative(arrived), relative(layout));
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
  const moments = [undefined, ...[0, 100, 350, 500, 1000, 1500].flatMap((at) => ["still", "interacting", "walking"].flatMap((action) => [0, 1].flatMap((frame) => (action === "walking" ? ["north", "south", "east", "west", undefined] : [undefined]).map((direction) => ({ action, frame, at, direction })))))];
  for (const role of ["worker", "orchestrator"]) {
    for (const provider of ["claude_code", "codex", "shell"]) {
      for (const activity of ["busy", "waiting", "needs-you", "idle"]) {
        const base = { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0 };
        const add = (appearance) => { for (const motion of moments) for (const [seat, stroking] of [[], ["resting"], ["resting", 0], ["resting", 400], ["planning"]]) for (const frame of workerFrames({ id: "agent", name: "Agent", role, provider, activity, appearance }, motion, seat, undefined, stroking)) reached.add(frame); };
        for (const field of ["skin", "face", "shoes", "tool", "headwear"]) for (let index = 0; index < spriteOptions[field].length; index++) add({ ...base, [field]: index });
        for (let hair = 0; hair < spriteOptions.hair.length; hair++) for (let hair_colour = 0; hair_colour < spriteOptions.hair_colour.length; hair_colour++) add({ ...base, hair, hair_colour });
        for (let outfit = 0; outfit < spriteOptions.outfit.length; outfit++) for (let clothes_colour = 0; clothes_colour < spriteOptions.clothes_colour.length; clothes_colour++) add({ ...base, outfit, clothes_colour });
      }
    }
  }
  const fallback = { id: idForIdentity(2), name: "Fallback", role: "worker", provider: "unknown", activity: "debugging" };
  assert.ok(workerFrames(fallback).includes("person.system.worker.shell"));
  // Workers with nothing suitable in hand each have their own habit at the table.
  const habits = new Set();
  for (let index = 0; index < 40; index++) for (const at of [0, 500]) {
    const worker = { id: `habit-${index}`, name: "Habit", role: "worker", provider: "codex", activity: "idle" };
    habits.add(restingItem(worker).item);
    for (const name of workerFrames(worker, { action: "still", frame: 0, at }, "resting")) reached.add(name);
  }
  assert.deepEqual([...habits].sort(), ["book", "cup", "snack"]);
  assert.ok(workerFrames(fallback).includes("person.skin.2.idle") || workerFrames(fallback).some((name) => /^person\.skin\.\d\.idle$/.test(name)), "an unknown activity stands idle");
  const personFrames = Object.keys(spriteAtlas.frames).filter((name) => name.startsWith("person."));
  assert.deepEqual([...reached].sort(), personFrames.sort());
  // One cup, one pose: never a tool and a cup, never two held things.
  for (const motion of moments) for (const seat of [undefined, "resting", "planning"]) for (const activity of ["busy", "waiting", "needs-you", "idle"]) {
    const frames = workerFrames({ ...fallback, activity }, motion, seat);
    assert.ok(frames.filter((name) => name.startsWith("person.held.") || name.startsWith("person.tool.")).length <= 1, `${activity}/${seat}: ${frames}`);
    assert.equal(new Set(frames).size, frames.length);
  }
  // Walking away shows a back with no face, badge or blink; walking across shows
  // a profile; towards the viewer, or with no direction known, the front.
  const walk = (direction, frame = 0) => workerFrames({ ...fallback, activity: "needs-you" }, { action: "walking", frame, at: 0, direction });
  const layers = (names) => names.map((name) => name.split(".")[1]);
  assert.deepEqual(layers(walk("north")), ["skin", "legs", "outfit", "hair", "shoes", "headwear", "tool", "alert"]);
  assert.ok(walk("north").filter((name) => !/legs|shoes|alert/.test(name)).every((name) => name.includes(".back")));
  assert.deepEqual(layers(walk("east")), ["skin", "legs", "outfit", "hair", "face", "shoes", "headwear", "tool", "system", "alert"]);
  assert.deepEqual(walk("west"), walk("east"), "west is east, mirrored by the scene");
  assert.ok(walk("east").filter((name) => !/system|alert/.test(name)).every((name) => name.includes(".side")));
  assert.deepEqual(walk("south"), walk(undefined));
  assert.ok(walk("south").every((name) => !/\.(back|side)/.test(name)));
  for (const direction of ["north", "east"]) assert.notDeepEqual(walk(direction, 0), walk(direction, 1));
  // Standing still never turns away, whatever direction was last walked.
  assert.ok(workerFrames(fallback, { action: "still", frame: 0, at: 0, direction: "north" }).every((name) => !/\.(back|side)/.test(name)));
  // What is carried decides what is rested with, where it suits a table.
  const carrying = (name) => ({ ...fallback, appearance: { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: spriteOptions.tool.findIndex((tool) => tool.name === name), headwear: 0 } });
  assert.deepEqual(["mug", "tablet", "clipboard"].map((name) => restingItem(carrying(name)).item), ["cup", "tablet", "clipboard"]);
  // Nothing jumps from the table to the mouth: sampled at the floor's own pulse,
  // a thing only ever moves one stage at a time, and everything is used and put back.
  const stages = ["table", "chest", "mouth"];
  for (const worker of [carrying("mug"), carrying("tablet"), carrying("clipboard"), ...Array.from({ length: 12 }, (_, index) => ({ ...fallback, id: `habit-${index}` }))]) {
    const visited = new Set();
    let previous = restingItem(worker, 0).where;
    for (let at = 0; at <= 30000; at += 200) {
      const { item, where } = restingItem(worker, at);
      assert.ok(Math.abs(stages.indexOf(where) - stages.indexOf(previous)) <= 1, `${item} jumped from ${previous} to ${where} at ${at}`);
      const frames = workerFrames(worker, { action: "still", frame: 0, at }, "resting");
      assert.deepEqual(frames.filter((name) => name.startsWith("person.held.")), where === "table" ? [] : [`person.held.${item}.${where}`]);
      assert.ok(frames.some((name) => name.endsWith(where === "mouth" ? ".sip" : where === "chest" ? ".hold" : ".idle")), `${where}: ${frames}`);
      visited.add(where); previous = where;
    }
    assert.ok(visited.has("table") && visited.has("chest"), `${restingItem(worker).item}: ${[...visited]}`);
    assert.equal(visited.has("mouth"), ["cup", "snack"].includes(restingItem(worker).item));
  }
  assert.equal(restingItem(fallback, undefined).where, "table", "a stilled clock leaves everything on the table");
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

test("a walk crosses the shared floor directly, around machines, never through one or by an imaginary door", () => {
  const layout = layoutScene(graph);
  const placements = placeWorkers(layout, [
    { ...workers[0], id: "source", nodeId: "src" },
    { ...workers[0], id: "destination", nodeId: "lib", observedBayId: "lib-db" },
  ]);
  const source = placements.find((placement) => placement.id === "source"), destination = placements.find((placement) => placement.id === "destination");
  const route = findRoute(layout, source, destination);
  assertWalk(layout, source, route, destination, "machine to machine");
  assert.deepEqual(pointOnRoute(source, route, route.length), { x: destination.x, y: destination.y });
  // No detour through anything: a walk is no longer than the shortest way round, which the straight line bounds from below.
  assert.ok(route.length < 2 * Math.hypot(destination.x - source.x, destination.y - source.y) + 40, `direct enough: ${route.length}`);
  const [resting, staging] = placeWorkers(layout, [
    { ...workers[0], id: "resting", location: "resting", nodeId: undefined },
    { ...workers[0], id: "staging", location: "unobserved", nodeId: undefined },
  ]);
  for (const common of [resting, staging]) {
    assertWalk(layout, common, findRoute(layout, common, destination), destination, `${common.area} out`);
    assertWalk(layout, destination, findRoute(layout, destination, common), common, `${common.area} back`);
  }
  // A walk past a machine's corner never cuts it: every leg keeps a worker's radius from it.
  const machines = sceneGraph([unit("a", { machines: [machine("a-1", "store"), machine("a-2", "job"), machine("a-3", "queue")] }), unit("b", { machines: [machine("b-1", "store")] })],
    { flows: [{ from: "a", to: "b", kind: "calls", reading: unread }] });
  const floor = layoutScene(machines);
  for (const from of floor.stations) for (const to of floor.stations) if (from !== to) assertWalk(floor, from.anchor, findRoute(floor, from.anchor, to.anchor), to.anchor, `${from.entityId} to ${to.entityId}`);
});

test("a retargeted walk starts where the worker is drawn, and a walk that cannot be made is said, not faked", () => {
  const layout = layoutScene(unitsOf(["A", "B", "C", "D"]));
  const at = new Map(placeWorkers(layout, ["A", "B", "C", "D"].map((id) => ({ id, name: id, role: "worker", activity: "busy", location: "working", nodeId: id }))).map((placement) => [placement.id, placement]));
  const first = findRoute(layout, at.get("A"), at.get("B"));
  // Rendered most of the way to B, the worker is sent to C, then at once to D: each walk starts from the drawn point.
  const drawn = pointOnRoute(at.get("A"), first, first.length * 0.7);
  for (const destination of [at.get("C"), at.get("D")]) {
    const route = findRoute(layout, drawn, destination);
    assertWalk(layout, drawn, route, destination, `retarget to ${destination.id}`);
  }
  // A destination walled in by machines has no walk; nothing draws a line through them.
  const boxed = { ...layout, solids: [...layout.solids, ...[[-40, -40, 80, 20], [-40, 20, 80, 20], [-40, -20, 20, 40], [20, -20, 20, 40]].map(([x, y, width, height]) => ({ x: at.get("D").x + x, y: at.get("D").y + y, width, height }))] };
  assert.equal(findRoute(boxed, at.get("A"), at.get("D")), undefined);
});

test("tables stand in front of whoever sits at them, with each resting worker's one thing on top", () => {
  const seatedWorkers = [
    ...Array.from({ length: 9 }, (_, index) => ({ ...workers[0], id: `habit-${index}`, activity: "idle", location: "resting", nodeId: undefined })),
    { ...workers[0], id: "asking", activity: "needs-you", location: "resting", nodeId: undefined },
    { ...workers[0], id: "planner", location: "unobserved", nodeId: undefined },
  ];
  const markup = render({ workers: seatedWorkers });
  const lastWorker = markup.lastIndexOf("data-worker-id="), firstTable = markup.indexOf("data-common-table="), firstItem = markup.indexOf("data-table-item=");
  assert.ok(lastWorker < firstTable && firstTable < firstItem, "workers, then the tables over their laps, then what lies on the tables");
  assert.equal((markup.match(/data-common-table="resting"/g) ?? []).length, floorFor(graph, { workers: seatedWorkers }).seats.rows, "a table for every four agents, always there: tables are solid, so they never appear or vanish");
  assert.equal(floorFor(graph, { workers: seatedWorkers }).seats.rows, 3, "eleven agents, three rows of four");
  // A first render puts each clock at the worker's own phase: a thing is on the table exactly when it is not in hand.
  for (const worker of seatedWorkers.filter((candidate) => candidate.location === "resting")) {
    const rest = restingItem(worker, worker.activity === "needs-you" ? undefined : workerPhase(worker.id));
    const own = markup.slice(markup.indexOf(`data-worker-id="${worker.id}"`)), held = own.slice(0, own.indexOf("</g>")).includes("person.held.");
    assert.equal(held, rest.where !== "table", `${worker.id}: ${JSON.stringify(rest)}`);
  }
  const onTables = seatedWorkers.filter((worker) => worker.location === "resting" && restingItem(worker, worker.activity === "needs-you" ? undefined : workerPhase(worker.id)).where === "table").length;
  assert.equal((markup.match(/data-table-item=/g) ?? []).length, onTables);
  assert.ok(onTables > 0 && onTables < 10, "the sample has things both in hand and on the table");
  assert.match(markup, /data-worker-id="asking"[\s\S]*?wave\./, "someone asking for you waves; their thing waits on the table");
  assert.equal((markup.match(/data-planning-light=""/g) ?? []).length, 1);
});

test("resting workers take fair, uninterrupted turns at the break-room furniture and walk there around the tables", () => {
  const person = (id, extra) => ({ id, name: id, role: "worker", provider: "codex", activity: "idle", location: "resting", ...extra });
  const carrying = (id, name) => person(id, { appearance: { automatic: false, skin: 0, hair: 0, hair_colour: 0, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: spriteOptions.tool.findIndex((tool) => tool.name === name), headwear: 0 } });
  const crowd = Array.from({ length: 12 }, (_, index) => person(`habit-${String(index).padStart(2, "0")}`));
  // A reader is drawn to the shelf, a drinker or snacker to the coffee; someone asking for you, or with their reading in hand, to neither.
  for (const worker of crowd) assert.equal(breakRoomHabit(worker), restingItem(worker).item === "book" ? "shelf" : "coffee");
  assert.deepEqual([person("asking", { activity: "needs-you" }), carrying("reader", "tablet"), carrying("checker", "clipboard")].map(breakRoomHabit), [undefined, undefined, undefined]);
  assert.deepEqual([...new Set(crowd.map(breakRoomHabit))].sort(), ["coffee", "shelf"]);

  // The furniture stands inside the commons, clear of every seat, on a wide floor and on the narrowest.
  const wide = layoutScene(unitsOf(Array.from({ length: 16 }, (_, index) => `room-${index}`)));
  const nook = breakRoomNook(wide);
  assert.deepEqual(nook.furniture.map((piece) => piece.errand), ["shelf", "coffee", "board", "missions", "tasks"]);
  const narrow = layoutScene(unitsOf(["room-0"])), narrowNook = breakRoomNook(narrow);
  assert.deepEqual(narrowNook.furniture.map((piece) => piece.errand), ["shelf", "coffee", "board", "missions", "tasks"]);
  for (const [layout, pieces, counts] of [[wide, nook.furniture, [12, 1]], [narrow, narrowNook.furniture, [40, 40]]]) for (const piece of pieces) {
    const { commons } = layout;
    assert.ok(piece.x >= commons.x && piece.x + WORKER_SIZE <= commons.x + commons.width && piece.stand.x + 12 <= commons.x + commons.width);
    assert.ok(piece.y >= commons.y && piece.y + WORKER_SIZE < layout.restingTop, "break furniture stays above common seating");
    const seating = commonSeating(layout, ...counts);
    for (const seat of [...seating.resting, ...seating.planning]) assert.ok(Math.abs(seat.x - piece.stand.x) >= 24 || Math.abs(seat.y - piece.stand.y) >= 24, "furniture visitors do not overlap a seated target");
  }

  // Half an hour of turns: one visitor a piece at most, nobody but a suited, seated worker,
  // every visit runs its whole turn, every turn starts free, and everyone gets a fair share.
  const placements = placeWorkers(wide, [...crowd, person("planner", { location: "unobserved" }), person("asking", { activity: "needs-you" })]);
  const habits = new Map([...crowd, person("asking", { activity: "needs-you" })].map((worker) => [worker.id, breakRoomHabit(worker)]));
  const visits = new Map(), running = new Map();
  let cutShort = 0;
  let before = [];
  for (let at = 0; at < 1800000; at += 2000) {
    before = placeErrands(placements, nook, (id) => habits.get(id), at, before);
    const away = before.filter((placement) => placement.errand !== undefined);
    assert.equal(new Set(away.map((placement) => placement.errand)).size, away.length, "one visitor a piece");
    for (const placement of away) { assert.equal(habits.get(placement.id), placement.errand); assert.notEqual(placement.id, "planner"); }
    for (const piece of nook.furniture) {
      const now = away.find((placement) => placement.errand === piece.errand)?.id, before = running.get(piece.errand);
      if (before?.id !== undefined && before.id !== now && at - before.since < 15000) cutShort++;
      if (before?.id !== now) { running.set(piece.errand, { id: now, since: at }); if (now !== undefined) visits.set(now, (visits.get(now) ?? 0) + 1); }
    }
  }
  assert.equal(cutShort, 0, "no visit is cut short");
  assert.equal(placeErrands(placements, nook, (id) => habits.get(id), 0).some((placement) => placement.errand !== undefined), false, "a floor that has just loaded stays seated");
  for (const piece of nook.furniture) {
    const shares = crowd.filter((worker) => habits.get(worker.id) === piece.errand).map((worker) => visits.get(worker.id) ?? 0);
    assert.ok(Math.min(...shares) > 0 && Math.max(...shares) <= 4 * Math.min(...shares), `${piece.errand} turns are shared: ${shares}`);
  }
  assert.equal(visits.has("asking"), false);
  // A visit outlasts comings and goings among the others: someone suited sitting down ahead of
  // the visitor in the order, or another leaving, mid-turn changes nothing; it ends with the turn,
  // or when the visitor themselves stops resting.
  let mid = 0, held = [];
  for (let at = 0, last = []; at < 1800000 && mid === 0; at += 2000) { last = placeErrands(placements, nook, (id) => habits.get(id), at, last); if (last.filter((placement) => placement.errand !== undefined).length === 2) { mid = at; held = last; } }
  const holders = held.filter((placement) => placement.errand !== undefined);
  assert.equal(holders.length, 2, "a moment with the shelf and the coffee both occupied: the implements have no ambient turns");
  const newcomer = person("aaa-first-in-order"), joined = placeWorkers(wide, [...crowd, newcomer, person("planner", { location: "unobserved" }), person("asking", { activity: "needs-you" })]);
  for (const suits of ["shelf", "coffee"]) {
    const habit = (id) => id === newcomer.id ? suits : habits.get(id);
    assert.deepEqual(placeErrands(joined, nook, habit, mid + 2000, held).filter((placement) => placement.errand !== undefined).map(({ id, errand }) => [id, errand]), holders.map(({ id, errand }) => [id, errand]), `a newcomer suited to the ${suits} takes nobody's place`);
    assert.notDeepEqual(placeErrands(joined, nook, habit, mid + 2000).filter((placement) => placement.errand !== undefined).map(({ id }) => id), holders.map(({ id }) => id), "the sample does change who would be drawn afresh");
  }
  const leaving = new Set(placements.filter((placement) => placement.area === "resting" && !holders.some((holder) => holder.id === placement.id)).slice(0, 3).map((placement) => placement.id));
  const departed = placements.filter((placement) => !leaving.has(placement.id));
  assert.deepEqual(placeErrands(departed, nook, (id) => habits.get(id), mid + 2000, held).filter((placement) => placement.errand !== undefined).map(({ id }) => id), holders.map(({ id }) => id), "others leaving takes nobody's place");
  const withoutVisitor = placements.filter((placement) => placement.id !== holders[0].id);
  assert.equal(placeErrands(withoutVisitor, nook, (id) => habits.get(id), mid + 2000, held).some((placement) => placement.id === holders[0].id), false);
  assert.equal(placeErrands(placements, undefined, (id) => habits.get(id), 60000), placements);
  // A lone reader is not forever on their feet.
  const lone = placeWorkers(wide, [crowd.find((worker) => breakRoomHabit(worker) === "shelf")]);
  let loneAway = 0; for (let at = 0; at < 1800000; at += 2000) if (placeErrands(lone, nook, () => "shelf", at)[0].errand !== undefined) loneAway++;
  assert.ok(loneAway > 0 && loneAway / 900 < .25, `away ${loneAway}/900 beats`);

  // Every walk in the commons goes round the tables, never over one, and never through anybody's seat but its own two ends;
  // arrivals from a machine and from mid-walk points between the rows count too.
  const seating = commonSeating(wide, 8, 4), seats = [...seating.resting, ...seating.planning];
  const stands = nook.furniture.map((piece) => piece.stand);
  const machineSpot = placeWorkers(wide, [{ ...workers[0], nodeId: "room-5" }])[0];
  const between = [wide.restingTop - 16, wide.restingTop + 32].map((y) => ({ x: wide.commons.x + 4, y }));
  const walks = [...seats.flatMap((from) => [...seats.filter((to) => to !== from), ...stands].map((to) => [from, to])), ...stands.flatMap((from) => [...seats, machineSpot].map((to) => [from, to])), ...seats.map((from) => [from, machineSpot]),
    ...[machineSpot, ...between].flatMap((from) => [...seats, ...stands].map((to) => [from, to]))];
  assert.ok(seating.resting.some((seat) => seat.y !== seating.resting[0].y), "the sample has more than one row");
  for (const [from, to] of walks) {
    const path = findRoute(wide, from, to);
    assertWalk(wide, from, path, to, `common route ${JSON.stringify(from)} to ${JSON.stringify(to)}`);
    let previous = from;
    for (const point of path.points) {
      for (const seat of seats) {
        if (seat.x === from.x && seat.y === from.y || seat.x === to.x && seat.y === to.y) continue;
        assert.equal(crosses(previous, point, { x: seat.x - 6, y: seat.y - 4, width: 12, height: 8 }), false, `${JSON.stringify(from)} to ${JSON.stringify(to)} walks through the seat at ${seat.x},${seat.y}`);
      }
      previous = point;
    }
  }

  // Standing at the furniture is its own pose: on their feet, the thing in hand, no tool.
  const atShelf = workerFrames(crowd[0], { action: "still", frame: 0, at: 0 }, undefined, "shelf");
  assert.ok(atShelf.some((name) => name.endsWith(".hold")) && atShelf.includes("person.held.book.chest") && atShelf.some((name) => name.includes("legs.") && name.endsWith(".stand")));
  assert.ok(workerFrames(crowd[0], { action: "still", frame: 0, at: 0 }, undefined, "coffee").includes("person.held.cup.chest"));
  assert.ok(atShelf.every((name) => !name.startsWith("person.tool.")));
  for (const implement of ["board", "missions", "tasks"]) assert.ok(workerFrames(crowd[0], { action: "still", frame: 0, at: 0 }, undefined, implement).includes("person.held.clipboard.chest"), implement);
  // The floor draws exactly the furniture the nook has; with the scenery off, only the implements, never the coffee.
  assert.equal((render().match(/data-break-room=/g) ?? []).length, breakRoomNook(floorFor(graph)).furniture.length);
  assert.equal(breakRoomNook(floorFor(graph)).furniture.length, 1, "with no panels to open, only the coffee stands there");
  assert.deepEqual(render({ appearance: { scenery: "off", animation: "on" }, onOpenBoard() {}, onOpenMissions() {}, onOpenTasks() {}, onOpenLibrary() {} }).match(/data-break-room="\w+"/g), ['data-break-room="shelf"', 'data-break-room="board"', 'data-break-room="missions"', 'data-break-room="tasks"']);
});

test("a floor mounted late still starts seated, then someone gets up, stands at the furniture, and comes back", async () => {
  const saved = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document };
  let visibility;
  globalThis.window = { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) };
  globalThis.document = { visibilityState: "visible", addEventListener: (_event, listener) => { visibility = listener; }, removeEventListener() {} };
  // The page has been open a while: well past any first free turn counted from zero.
  let clock = 47000, next = 0;
  const timers = new Map(), frames = new Map();
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = (callback) => { timers.set(++next, callback); return next; };
  globalThis.clearTimeout = (id) => timers.delete(id);
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  const resting = Array.from({ length: 10 }, (_, index) => ({ ...workers[0], id: `habit-${String(index).padStart(2, "0")}`, activity: "idle", location: "resting", nodeId: undefined }));
  // The floor offers its library, so readers have a shelf to visit.
  const libraryOpen = { onOpenLibrary() {} };
  let renderer;
  try {
    // Every commit is looked at, not only where things settle: a worker drawn at the
    // furniture for a single frame after the floor is stilled is still drawn there.
    const commits = [];
    // In every commit, pose and place are of one moment: whoever is drawn standing at the
    // furniture is drawn at the furniture, never for a frame at their seat.
    const stands = new Set(breakRoomNook(floorFor(graph, { workers: resting, ...libraryOpen })).furniture.map((piece) => `translate(${piece.stand.x} ${piece.stand.y})`));
    const misplaced = () => renderer === undefined ? 0 : renderer.root.findAll((node) => typeof node.props["data-tooltip"] === "string" && /at the (bookshelf|coffee station)/.test(node.props["data-tooltip"]))
      .filter((node) => node.parent.props["data-worker-action"] !== "walking" && !stands.has(node.parent.props.transform)).length;
    const scene = (props) => createElement(Profiler, { id: "floor", onRender: () => commits.push(misplaced()) }, createElement(FactoryScene, { graph, appearance: commons, workers: resting, ...libraryOpen, connected: true, ...props }));
    await act(async () => { renderer = create(scene({})); });
    // Where each sprite is actually drawn, against the seat it belongs in.
    const outOfSeat = (floor) => {
      const seatOf = new Map(placeWorkers(renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout, resting).map((placement) => [placement.id, `translate(${placement.x} ${placement.y})`]));
      return renderer.root.findAll((node) => node.props["data-worker-id"] !== undefined).filter((node) => node.props.transform !== seatOf.get(node.props["data-worker-id"])).length;
    };
    const pieces = breakRoomNook(floorFor(graph, { workers: resting, ...libraryOpen })).furniture.length;
    const away = () => renderer.root.findAll((node) => typeof node.props["data-tooltip"] === "string" && /at the (bookshelf|coffee station)/.test(node.props["data-tooltip"]));
    const standingWithIt = () => away().some((node) => node.parent.props["data-worker-action"] === "still" && node.parent.findAllByType("use").some((use) => /held\.(book|cup)\.chest/.test(use.props.href)));
    const tick = async () => {
      clock += 100;
      const due = [...frames.values(), ...timers.values()]; frames.clear(); timers.clear();
      await act(async () => { for (const callback of due) callback(clock); });
    };
    // A second of movement, most of a minute hidden, and the floor is still in its first free turn:
    // time it was not moving does not count.
    for (let step = 0; step < 10; step++) await tick();
    await act(async () => { globalThis.document.visibilityState = "hidden"; visibility(); });
    clock += 45000;
    await act(async () => { globalThis.document.visibilityState = "visible"; visibility(); });
    for (let step = 0; step < 60; step++) { await tick(); assert.equal(away().length, 0, `someone got up ${step / 10}s after the tab came back`); }
    const mounted = clock - 7000;
    let firstAway, stood = false, cameBack = false;
    for (let step = 0; step < 1500 && !cameBack; step++) {
      await tick();
      if (away().length > 0) { firstAway ??= clock - mounted; stood ||= standingWithIt(); }
      else if (stood) cameBack = true;
      assert.ok(away().length <= pieces, "never more visitors than furniture");
    }
    assert.ok(firstAway >= 8000, `nobody gets up during the floor's first free turn, however long the page has been open: ${firstAway}`);
    assert.ok(stood, "the visitor arrives and stands with the thing in hand");
    assert.ok(cameBack, "and sits down again");
    assert.ok(commits.length > 100 && commits.every((count) => count === 0), `someone was drawn standing at the furniture while somewhere else: ${commits.filter((count) => count > 0).length} of ${commits.length} commits`);
    // Stilled motion seats everyone in the very render that stills it, by whichever means.
    const seatedNow = (when) => {
      assert.equal(away().length, 0, `someone is at the furniture ${when}`);
      for (const worker of renderer.root.findAll((node) => node.props["data-worker-id"] !== undefined)) assert.ok(worker.props["data-worker-action"] !== "walking" && worker.findAll((node) => node.props["data-seated"] === "coffee").length === 1, `${worker.props["data-worker-id"]} is not in their seat ${when}`);
    };
    const untilSomeoneStands = async () => { for (let step = 0; step < 3000 && !standingWithIt(); step++) await tick(); assert.ok(standingWithIt()); };
    for (const [how, still, resume] of [
      ["with animation turned off", { appearance: { ...commons, animation: "off" } }, {}],
      ["on disconnect", { connected: false }, {}],
    ]) {
      await untilSomeoneStands();
      const seen = [];
      await act(async () => { renderer.update(createElement(Profiler, { id: "floor", onRender: () => seen.push(away().length + outOfSeat(graph)) }, createElement(FactoryScene, { graph, appearance: commons, workers: resting, ...libraryOpen, connected: true, ...still }))); });
      assert.ok(seen.length > 0 && seen.every((count) => count === 0), `${how}: someone was drawn at or placed by the furniture in a commit after the floor was stilled: ${seen}`);
      seatedNow(how);
      await act(async () => { renderer.update(scene(resume)); });
    }
    // A visit stays with whoever holds it, whoever else of the same habit leaves or comes back meanwhile.
    await untilSomeoneStands();
    const holders = () => away().map((node) => node.parent.props["data-worker-id"]).join();
    assert.equal(away().length, 1, "one visitor on this one-piece floor");
    const holding = holders(), habit = breakRoomHabit(resting.find((worker) => worker.id === holding));
    const alike = resting.filter((worker) => worker.id !== holding && breakRoomHabit(worker) === habit);
    assert.ok(alike.length > 0, "the fixture has someone else who could have had the turn");
    for (const leaver of alike) {
      // What is drawn follows the motion state, a render behind: a tick lets it catch up with each change.
      await act(async () => { renderer.update(scene({ workers: resting.filter((worker) => worker !== leaver) })); });
      await tick();
      assert.equal(renderer.root.findAll((node) => node.props["data-worker-id"] === leaver.id).length, 0, `${leaver.id} has left the drawn floor`);
      assert.equal(holders(), holding, `the visit changed hands when ${leaver.id} left`);
      await act(async () => { renderer.update(scene({})); });
      await tick();
      assert.equal(holders(), holding, `the visit changed hands when ${leaver.id} came back`);
    }
    await untilSomeoneStands();
    await act(async () => { clock += 1; globalThis.document.visibilityState = "hidden"; visibility(); });
    seatedNow("in a hidden tab");
    await act(async () => { clock += 1; globalThis.document.visibilityState = "visible"; visibility(); });
    // Another floor starts again from seated, however long this one had been going.
    for (let step = 0; step < 3000 && away().length === 0; step++) await tick();
    assert.ok(away().length > 0, "someone is up when the floor changes");
    const another = { ...graph, digest: "another-floor", units: [...graph.units, unit("extra-1"), unit("extra-2")] };
    const floorCommits = [];
    await act(async () => { renderer.update(createElement(Profiler, { id: "floor", onRender: () => floorCommits.push(away().length + outOfSeat(another)) }, createElement(FactoryScene, { graph: another, appearance: commons, workers: resting, ...libraryOpen, connected: true }))); });
    assert.ok(floorCommits.length > 0 && floorCommits.every((count) => count === 0), `someone was drawn out of their seat in a commit of the new floor: ${floorCommits}`);
    // Judged by what is drawn, from the very first render of the new floor: nobody at the
    // furniture, nobody walking, nobody on their feet with a book or a cup.
    const seatedAsDrawn = (when) => {
      assert.equal(away().length, 0, `someone is at the furniture ${when}`);
      for (const worker of renderer.root.findAll((node) => node.props["data-worker-id"] !== undefined)) {
        assert.notEqual(worker.props["data-worker-action"], "walking", `${worker.props["data-worker-id"]} is walking ${when}`);
        assert.ok(worker.findAll((node) => node.props["data-seated"] === "coffee").length === 1, `${worker.props["data-worker-id"]} is not in their seat ${when}`);
      }
    };
    seatedAsDrawn("as the new floor appears");
    for (let step = 0; step < 70; step++) { await tick(); seatedAsDrawn(`${step / 10}s into the new floor`); }
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});

test("a walking worker is drawn facing where they go, and only a westward walk is mirrored", async () => {
  const saved = { requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, performance: globalThis.performance };
  let clock = 0, pending;
  globalThis.performance = { now: () => clock };
  globalThis.requestAnimationFrame = (callback) => { pending = callback; return 1; };
  globalThis.cancelAnimationFrame = () => { pending = undefined; };
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    const drawn = () => {
      const worker = renderer.root.findByProps({ "data-worker-id": workers[0].id });
      const sprite = worker.findAll((node) => node.type === "g" && typeof node.props.transform === "string" && node.props.transform.startsWith("scale("))[0];
      return { action: worker.props["data-worker-action"], facing: worker.props["data-worker-facing"], transform: sprite.props.transform, frames: sprite.findAllByType("use").map((node) => node.props.href) };
    };
    const seen = new Set();
    const check = () => {
      const { action, facing, transform, frames } = drawn();
      assert.equal(transform.includes("scale(-1 1)"), action === "walking" && facing === "west", `${action}/${facing}: ${transform}`);
      assert.equal(facing !== undefined, action === "walking");
      assert.equal(frames.some((name) => name.includes(".back")), facing === "north", `${facing}: ${frames}`);
      assert.equal(frames.some((name) => name.includes(".side")), facing === "east" || facing === "west", `${facing}: ${frames}`);
      seen.add(facing);
    };
    check();
    // There and back again covers both horizontal directions of the same route.
    // Across the floor and back, then down to the commons to rest.
    for (const where of [{ nodeId: "lib" }, { nodeId: "src" }, { location: "resting", activity: "idle", nodeId: undefined }]) {
      await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: [{ ...workers[0], ...where }, workers[1]], connected: true })); });
      for (let step = 0; step < 400 && pending !== undefined; step++) { const tick = pending; pending = undefined; clock += 40; await act(async () => { tick(clock); }); check(); }
      assert.notEqual(drawn().action, "walking", "the route ends");
    }
    assert.ok(seen.has("west") && seen.has("east"), `both profiles were walked: ${[...seen]}`);
    assert.ok(seen.has("north") || seen.has("south"), `a vertical leg was walked: ${[...seen]}`);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
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
    await act(async () => { renderer = create(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    const moved = [{ ...workers[0], nodeId: "lib" }, workers[1]];
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: moved, connected: true })); });
    assert.ok(requested.length > 0, "one scene clock schedules the route");
    const staticRoomProps = renderer.root.findByProps({ "data-entity-id": "lib" }).props;
    const atlasProps = renderer.root.findByType("defs").props;
    const layoutBeforeTick = renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout;
    const workerBeforeTick = renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform;
    await act(async () => { requested.at(-1)(performance.now() + 50); });
    assert.equal(renderer.root.findByProps({ "data-entity-id": "lib" }).props, staticRoomProps, "RAF does not recreate static machine elements");
    assert.equal(renderer.root.findByType("defs").props, atlasProps, "RAF does not recreate the sprite atlas");
    assert.equal(renderer.root.find((node) => node.type.name === "SceneWorkers").props.layout, layoutBeforeTick);
    assert.notEqual(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform, workerBeforeTick);
    await act(async () => { renderer.update(createElement(FactoryScene, { graph: { ...graph, units: graph.units.map((item) => ({ ...item, reading: busy })) }, appearance: commons, workers: moved, connected: true })); });
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "walking", "a reading-only change preserves the route");

    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: moved, connected: false })); });
    const destination = placeWorkers(floorFor(graph, { workers: moved }), moved).find((placement) => placement.id === workers[0].id);
    assert.ok(destination);
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props.transform, `translate(${destination.x} ${destination.y})`);
    assert.ok(cancelled.length > 0, "disconnect cleans the pending animation frame");

    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers, connected: false })); });
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "interacting", "reconnect snaps to current observed work instead of replaying the missed route");
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: moved, connected: true })); });
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

test("stationary tasks link the existing queue and questions", () => {
  const tasks = Array.from({ length: 11 }, (_, index) => ({
    id: `task-${index}`, agentId: "worker-b", projectId: "project",
    title: index === 0 ? "<script>unsafe & title</script>" : `Task ${index}`,
    status: index === 0 ? "running" : "queued",
    humanRequestIds: index === 0 ? ["question-1"] : [],
  }));
  const markup = render({ tasks, onSelectTask() {}, onSelectHumanRequest() {}, onOpenTasks() {} });
  assert.equal((markup.match(/data-floor-inbox="10"/g) ?? []).length, 1);
  // The tray is a distinct, keyboard reachable route into the existing Tasks panel.
  assert.match(markup, /data-floor-inbox="10"[^>]*aria-label="Open Tasks"[^>]*role="button"[^>]*tabindex="0"/);
  // Selecting queued work in the panel still lights its pile on the floor.
  assert.match(render({ tasks, selectedTaskId: "task-1", onSelectTask() {}, onOpenTasks() {} }), /data-floor-inbox="10"[\s\S]*?stroke="#80ddff"/);
  const pile = (html) => html.match(/data-floor-inbox="\d+"[\s\S]*?<\/g><\/g>/)[0].match(/fill="#e4dcc0"/g)?.length ?? 0;
  assert.deepEqual([pile(markup), pile(render({ tasks: tasks.slice(0, 3), onOpenTasks() {} })), pile(render({ tasks: [tasks[0]], onOpenTasks() {} }))], [3, 2, 0], "the pile follows the queue, up to three papers");
  // The tray is there wherever the floor can open Tasks: it stays when nothing waits, and says so.
  const idle = render({ tasks: [tasks[0]], onSelectTask() {}, onOpenTasks() {} });
  assert.match(idle, /data-floor-inbox="0"/);
  assert.doesNotMatch(idle, />QUEUE/, "the tray says nothing; MERGE QUEUE is the outbound line's station");
  assert.match(markup, /data-human-request-id="question-1"/);
  assert.match(markup, /aria-label="Question from Builder"/);
  assert.doesNotMatch(markup, /Work order|data-work-order-id/);
  assert.match(markup, /role="button" tabindex="0"/);
  assert.doesNotMatch(markup, /<script>/);
  assert.doesNotMatch(markup, /stroke-dasharray|CHANGES WITHIN|CHANGED|>task-0</);
  const noObservation = render({ tasks: [{ ...tasks[0], humanRequestIds: [] }] });
  assert.doesNotMatch(noObservation, /data-human-request-id=/);
});

test("the separate desks are gone: the break room's implements open their panels", () => {
  const markup = render({ projectId: "project-a", onOpenTasks() {}, onOpenMissions() {}, onOpenBoard() {}, onOpenLibrary() {} });
  assert.doesNotMatch(markup, /data-floor-tray-desk|data-common-table="planning"/);
  assert.equal((markup.match(/>(BOARD|MISSIONS|TASKS|LIBRARY)</g) ?? []).length, 4, "each implement signed once");
  assert.match(markup, /data-break-room="tasks" data-floor-inbox="0"[^>]*aria-label="Open Tasks"[^>]*role="button"[^>]*tabindex="0"/);
  assert.match(markup, /data-break-room="missions"[^>]*data-tooltip="Missions · inspect objectives"[^>]*aria-label="Open Missions"[^>]*role="button"[^>]*tabindex="0"/);
  assert.match(markup, /data-break-room="board"[^>]*aria-label="Open discussion board"[^>]*role="button"[^>]*tabindex="0"/);
  assert.match(markup, /data-break-room="shelf"[^>]*aria-label="Open project library"[^>]*role="button"[^>]*tabindex="0"/);
  // Without a panel to open, an implement is scenery, not a dead button.
  assert.doesNotMatch(render({ projectId: "project-a" }), /data-break-room="missions"[^>]*role="button"/);
});

test("planning table detail stays within the tabletop, and every seat has its table", () => {
  const planningWorkers = [0, 1].map((index) => ({ ...workers[0], id: `planner-${index}`, location: "unobserved", nodeId: undefined }));
  const markup = render({ workers: planningWorkers });
  const layout = floorFor(graph, { workers: planningWorkers }), table = commonTables(layout).find((item) => item.planning);
  assert.match(markup, new RegExp(`M${table.width - 4} 4h-4`), "detail line ends inside the tabletop edge");
  for (const seat of [...commonSeating(layout, 2, 2).resting, ...commonSeating(layout, 2, 2).planning]) {
    assert.ok(commonTables(layout).some((item) => seat.x > item.x && seat.x < item.x + item.width && item.y - seat.y === 3), `the seat at ${seat.x},${seat.y} has a table in front`);
  }
});

test("floor action hit areas invoke existing task and mission routes", async () => {
  let tasksOpened;
  let missionsOpened;
  let conversationTask;
  const task = { id: "task-peer", agentId: "worker-b", projectId: "project", title: "Peer work", status: "running", humanRequestIds: [] };
  let renderer;
  await act(async () => {
    renderer = create(createElement(FactoryScene, {
      graph,
      appearance: commons,
      projectId: "project",
      workers,
      tasks: [task],
      peerQuestions: [{ id: "peer-1", source_task_id: "task-peer", target_task_id: "task-other", answered: false, revision: 1n }],
      onOpenTasks: (projectId) => { tasksOpened = projectId; },
      onOpenMissions: (projectId) => { missionsOpened = projectId; },
      onSelectTask: (taskId) => { conversationTask = taskId; },
      connected: false,
    }));
  });
  const tray = renderer.root.findByProps({ "data-floor-inbox": 0 });
  const planning = renderer.root.findByProps({ "data-break-room": "missions" });
  for (const target of [tray, planning]) assert.equal(target.findAll((node) => node.props.className === "dfFactoryScene__focus").length, 1, "one focus ring per implement");
  await act(async () => tray.props.onClick());
  await act(async () => planning.props.onKeyDown({ key: "Enter", preventDefault() {} }));
  assert.equal(tasksOpened, "project");
  assert.equal(missionsOpened, "project");
  await act(async () => renderer.root.findByProps({ "data-peer-question-count": 1 }).props.onClick());
  assert.equal(conversationTask, "task-peer");
  await act(async () => renderer.unmount());
});


test("larger workers fit compact common seating in narrow, wide and crowded floors", () => {
  for (const roomCount of [1, 3, 11]) {
    const floor = unitsOf(Array.from({ length: roomCount }, (_, index) => `room-${index}`));
    const layout = layoutScene(floor);
    for (const count of [0, 1, 12, 100]) {
      const seating = commonSeating(layout, count, count);
      const seats = [...seating.resting, ...seating.planning];
      assert.equal(new Set(seats.map(({ x, y }) => `${x},${y}`)).size, seats.length);
      for (const seat of seats) {
        assert.ok(seat.x - 12 >= layout.commons.x && seat.x + 12 <= layout.commons.x + layout.commons.width || seat.y > layout.height, "in the commons, or on a bench below the floor");
        for (const other of seats) if (seat !== other) assert.ok(Math.abs(seat.x - other.x) >= WORKER_SIZE || Math.abs(seat.y - other.y) >= WORKER_SIZE);
      }
      assert.equal(seating.resting.length, count);
      assert.equal(seating.planning.length, count);
      const previewWorkers = seats.map((seat, index) => ({ ...workers[0], id: `seat-${index}`, location: index < seating.resting.length ? "resting" : "unobserved" }));
      const markup = render({ graph: floor, workers: previewWorkers });
      if (count > 0) assert.ok(markup.includes('transform="scale(1.25)"'));
      assert.equal((markup.match(/aria-label="Work tables"/g) ?? []).length, count > 0 ? 1 : 0);
      const height = Number(markup.match(/viewBox="0 0 [^ ]+ ([^"]+)"/)[1]);
      assert.ok(height > Math.max(...seats.map(({ y }) => y + WORKER_SIZE / 2)));
    }
  }
});

test("a unit's main machine truncates a full-width name while keeping its accessible name, in the space layout reserved", () => {
  for (const glyph of ["界", "😀", "👨‍👩‍👧‍👦", "🇯🇵", "é"]) {
    const label = glyph.repeat(30);
    const markup = render({ graph: sceneGraph(["a", "b"].map((id) => unit(id, { label }))), workers: [] });
    const shown = markup.match(/data-shape="line"[\s\S]*?class="dfPlant__label" font-size="8">([^<]+)<\/text>/)?.[1];
    assert.ok(shown?.endsWith("…"), "the visible name fits under its machine");
    assert.ok([...new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(shown)].length < 30);
    assert.match(markup, new RegExp(`aria-label="Inspect ${label}`), "full name remains accessible");
    assert.doesNotMatch(markup, new RegExp(`>${label}</text>`));
  }
});

test("the public layout key covers every placement input and nothing live", () => {
  const node = (id, extra = {}) => ({ id, kind: "processor", label: id, runtime: "process", evidence: "static", observation: "unobserved", state: "unknown", activity: "none", ...extra });
  const edge = (from, to, extra = {}) => ({ from, to, kind: "calls", evidence: "static", observation: "unobserved", state: "unknown", activity: "none", ...extra });
  // Many ids, so a change far past the first few still has to change the key.
  const many = Array.from({ length: 40 }, (_, index) => node(`unit-${String(index).padStart(2, "0")}`));
  const world = { generated_at: 0, summary: graph.summary, nodes: [node("a"), node("b"), node("c"), node("a-db", { kind: "store", unit: "a" }), ...many], edges: [edge("a", "b")], workers: [] };
  const key = (changed) => publicFloor({ ...world, ...changed }).graph.digest;
  for (const [what, nodes] of [["a late unit", [...world.nodes, node("z")]], ["a runtime", [...world.nodes.slice(0, 2), node("c", { runtime: "cli" }), ...world.nodes.slice(3)]],
    ["an owner", [...world.nodes.slice(0, 3), node("a-db", { kind: "store", unit: "b" }), ...many]], ["a kind", [...world.nodes.slice(0, 3), node("a-db", { kind: "queue", unit: "a" }), ...many]],
    ["a label far down the list (its size)", [...world.nodes.slice(0, -1), node("unit-39", { label: "a much longer public name" })]]]) {
    assert.notEqual(key({ nodes }), key(), `${what} relays the floor`);
  }
  for (const [what, edges] of [["a new connection", [...world.edges, edge("b", "c")]], ["a connection gone", []], ["a connection's kind", [edge("a", "b", { kind: "uses" })]]]) {
    assert.notEqual(key({ edges }), key(), `${what} relays the floor`);
  }
  assert.equal(key({ edges: [edge("a", "b", { activity: "high", state: "active", observation: "observed" })] }), key(), "a connection's traffic does not");
  assert.equal(key({ generated_at: 9, nodes: world.nodes.map((item) => ({ ...item, activity: "high", state: "active", observation: "observed" })), workers: [{ activity: "busy", unit: "a" }] }), key(), "traffic, state and workers never do");
});

test("compact tooltips open on hover, focus and tap without opening component details", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const priorWindow = globalThis.window;
  const priorDocument = globalThis.document;
  globalThis.window = { innerWidth: 390, innerHeight: 844 };
  let renderer;
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { graph, appearance: commons, workers: [] }), { createNodeMock: () => ({ getBoundingClientRect: () => ({ height: window.innerHeight === 320 ? 160 : 60 }) }) }); });
    const map = renderer.root.findByProps({ className: "dfFactoryFloor__map" });
    const target = renderer.root.findAll((node) => node.props["data-tooltip"])[0];
    const event = { target: { closest: () => ({ querySelector: () => null, getBoundingClientRect: () => ({ left: 380, top: 806, bottom: 830 }), getAttribute: () => target.props["data-tooltip"] }) } };
    for (const handler of ["onPointerOver", "onFocus", "onClick"]) {
      await act(async () => map.props[handler](event));
      const tip = renderer.root.findByProps({ role: "tooltip" });
      assert.match(tip.props.children, /No telemetry/);
      assert.ok(tip.props.style.left <= 118 && tip.props.style.top + 60 <= 806, "no room below: the tooltip sits above its target, not over it");
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
    // Neither side fits a 160px card on a 320px screen: it takes the roomier side and shrinks to it.
    globalThis.window = { innerWidth: 390, innerHeight: 320 };
    await act(async () => map.props.onPointerOver({ target: { closest: () => ({ querySelector: () => null, getBoundingClientRect: () => ({ left: 10, top: 145, bottom: 169 }), getAttribute: () => target.props["data-tooltip"] }) } }));
    const tight = renderer.root.findByProps({ role: "tooltip" }).props.style;
    assert.ok(tight.top >= 169 && tight.top + tight.maxHeight <= 312, `crammed tooltip must clear its target: ${JSON.stringify(tight)}`);
    assert.equal(renderer.root.findAllByType("select").length, 0);
    assert.equal(renderer.root.findAllByProps({ "aria-label": "Source inspector" }).length, 0, "hover and focus do not open the inspector");
    assert.equal(renderer.root.findAllByType("table").length, 0);
  } finally {
    await act(async () => renderer?.unmount());
    globalThis.window = priorWindow;
    globalThis.document = priorDocument;
  }
});

test("stationary active workers animate while idle, reduced, hidden and disconnected clocks stop", async () => {
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
    await act(async () => { renderer = create(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    assert.equal(timers.size, 1);
    const staticRoom = renderer.root.findByProps({ "data-entity-id": "src" }).props;
    const frameNames = () => renderer.root.findByProps({ "data-worker-id": workers[0].id }).findAllByType("use").map((node) => node.props.href);
    assert.equal(frames.size, 0, "stationary work never schedules RAF");
    const seen = new Set();
    for (clock = 200; clock <= 2000; clock += 200) { await act(async () => { timers.values().next().value(); }); seen.add(frameNames().join()); }
    assert.ok(seen.size >= 2, "stationary interaction frame advances");
    assert.equal(renderer.root.findByProps({ "data-entity-id": "src" }).props, staticRoom);
    await act(async () => { clock += 1; globalThis.document.visibilityState = "hidden"; visibilityListener(); });
    assert.equal(timers.size, 0);
    await act(async () => { clock += 1; globalThis.document.visibilityState = "visible"; visibilityListener(); });
    assert.equal(timers.size, 1);
    await act(async () => { media.matches = true; mediaListener(); });
    assert.equal(timers.size, 0);
    await act(async () => { media.matches = false; mediaListener(); });
    assert.equal(timers.size, 1);
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers, connected: false })); });
    assert.equal(timers.size, 0);
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    assert.equal(timers.size, 1);
    const planningWorkers = [...workers, { ...workers[0], id: "planning", location: "unobserved", nodeId: undefined }];
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: planningWorkers, connected: true, appearance: { ...commons, animation: "off" } })); });
    assert.equal(timers.size, 0, "animation-off stops the clock");
    assert.equal(frames.size, 0, "animation-off stops movement");
    assert.equal(renderer.root.findAllByProps({ "data-planning-light": "" }).length, 1, "connected planning lamp stays lit with animation off");
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: planningWorkers, connected: false, appearance: { ...commons, animation: "off" } })); });
    assert.equal(renderer.root.findAllByProps({ "data-planning-light": "" }).length, 0, "disconnect extinguishes planning lamps");
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: workers.map((worker) => ({ ...worker, activity: "waiting", location: "resting" })), connected: false })); });
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers: [], connected: true })); });
    assert.equal(timers.size, 0, "idle floor leaves no continuous animation clock");
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, appearance: commons, workers, connected: true })); });
    assert.equal(timers.size, 1, "people on the floor keep its slow pulse");
    assert.equal(renderer.root.findByProps({ "data-worker-id": workers[0].id }).props["data-worker-action"], "interacting");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});


test("a reviewer of a proposal uses the same actor system", async () => {
  const operations = ["addition", "modification", "removal", "move"].map((kind) => ({ entityId: "lib-in", unitId: "lib", path: `internal/lib/${kind}.go`, kind }));
  const items = [{ id: "first", title: "First change", state: "active", operations }, { id: "second", title: "Second change", state: "stale", operations: [operations[1]] }];
  const inspected = [], actors = [{ ...workers[0], id: "review-run", nodeId: "lib", observedBayId: "lib-in", review: { proposalId: "first", scope: "Assigned changed paths; file inspection unavailable" } }];
  let tree;
  await act(async () => { tree = create(createElement(FactoryScene, { graph, appearance: commons, workers: actors, proposals: { items, selected: "second", onSelect: (id) => inspected.push(id) } })); });
  const reviewer = tree.root.findByProps({ "data-reviewer-id": "review-run" });
  assert.equal(tree.root.findAllByProps({ "data-worker-id": "review-run" }).length, 1);
  await act(async () => reviewer.findAll((node) => node.props.role === "button")[0].props.onKeyDown({ key: "Enter", preventDefault() {} }));
  assert.deepEqual(inspected, ["first"]);
  await act(async () => tree.unmount());
});

test("resting nearby keeps deterministic walks to every machine of the unit", () => {
  const layout = layoutScene(sceneGraph([unit("lib", { machines: [machine("a", "ingress", { trigger: "request" }), machine("e", "ingress", { trigger: "request" }), machine("b", "store")] })]));
  const idle = ["reader", "friend"].map((id) => ({ ...workers[0], id, activity: "idle", location: "resting", nodeId: "lib" }));
  const resting = placeWorkers(layout, idle, "nearby");
  assert.ok(resting.every((placement) => placement.area === "resting" && placement.stationId === "lib"));
  for (const station of layout.stations) {
    const atWork = placeWorkers(layout, [{ ...workers[0], nodeId: "lib", observedBayId: station.entityId }])[0];
    assertWalk(layout, resting[0], findRoute(layout, resting[0], atWork), atWork, station.entityId);
  }
});

test("nearby resting actors do not leave ghost tables or seats in the side commons", () => {
  const floor = sceneGraph([unit("lib", { machines: [machine("lib-db", "store")] })]);
  const people = ["one", "two"].map((id) => ({ ...workers[0], id, activity: "idle", paused: id === "one", location: "last-observed", nodeId: "lib" }));
  const appearance = { social: "nearby", scenery: "off", animation: "off" };
  const markup = render({ graph: floor, workers: people, appearance });
  assert.equal((markup.match(/data-nearby-rest=/g) ?? []).length, 2);
  assert.equal((markup.match(/data-worker-id=/g) ?? []).length, 2);
  assert.doesNotMatch(markup, /data-common-seat=/, "no stool in the commons for anyone resting nearby");
  assert.match(markup, /Paused · taking a break/);
  const waiting = render({ graph: floor, workers: [...people, { ...workers[0], id: "unlocated", location: "unobserved" }], appearance });
  assert.equal((waiting.match(/data-common-seat="planning"/g) ?? []).length, 1);
  assert.doesNotMatch(waiting, /data-common-seat="resting"/);
  assert.match(waiting, /working; location not yet observed/);
});

test("the commons shelf opens the floor's project library", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const opened = [];
  let tree;
  await act(async () => { tree = create(createElement(FactoryScene, { graph, appearance: commons, workers: [], projectId: "floor-project", appearance: { social: "nearby", scenery: "off", animation: "off" }, onOpenLibrary: (id) => opened.push(id) })); });
  assert.deepEqual(opened, [], "rendering ambient furniture does not retrieve library content");
  const shelves = tree.root.findAllByProps({ "aria-label": "Open project library" });
  assert.equal(shelves.length, 1);
  assert.equal(shelves[0].props["data-tooltip"], "Library · documents");
  await act(async () => shelves[0].props.onKeyDown({ key: "Enter", preventDefault() {} }));
  assert.deepEqual(opened, ["floor-project"]);
  await act(async () => tree.unmount());
});

test("an outside gate says not connected or no calls instead of claiming it cannot be seen", () => {
  const caption = (observation) => render({ graph: sceneGraph([unit("api", { machines: [machine("api-in", "ingress")] })], { parties: [machine("linear", "external", { reading: { ...unread, observation } })], flows: [] }), workers: [] });
  assert.match(caption("unobserved"), /not connected/);
  assert.match(caption("quiet"), /no calls in 15 min/);
  assert.match(caption("opaque"), /can&#x27;t see inside|can't see inside/);
});

test("an unobserved machine is greyed with no material belt; an observed edge with a rate carries material", () => {
  const floor = sceneGraph([unit("web", { machines: [machine("web-ui", "job")] }), unit("api", { machines: [machine("api-in", "ingress", { trigger: "request", reading: busy }), machine("api-db", "store")] })], {
    parties: [machine("github", "external", { reading: { ...unread, observation: "opaque" } })],
    flows: [{ from: "web-ui", to: "api-in", kind: "calls", reading: busy }, { from: "api", to: "api-db", kind: "uses", reading: unread }, { from: "api", to: "github", kind: "calls", reading: unread }],
  });
  const markup = render({ graph: floor, workers: [] });
  assert.match(markup, /data-entity-id="api-db" data-observation="unobserved" data-state="unknown" data-shape="silo"/);
  assert.match(markup, /data-entity-id="api-in" data-observation="observed" data-state="active" data-shape="dock"/);
  assert.match(markup, /data-entity-id="github" data-observation="opaque"/);
  // the observed edge draws material, over its casing
  assert.match(markup, /<g data-belt="calls" data-observation="observed" data-state="active"><path[^>]*class="dfPlant__casing"><\/path><path[^>]*class="b-base"><\/path><path[^>]*class="dfPlant__material"/);
  assert.equal((markup.match(/data-belt="uses" data-observation="unobserved"/g) ?? []).length, 1, "an unobserved edge is a faint dashed run");
  assert.equal(markup.includes('data-belt="uses" data-observation="unobserved" data-state'), false, "and carries no material");
  assert.equal((markup.match(/dfPlant__material/g) ?? []).length, 1, "only the observed belt moves material");
});

test("the coverage header shows the summary numbers", () => {
  const summary = { components: 7, inferred: 6, observed: 3, quiet: 1, partial: 1, stale: 0, unobserved: 1, opaque: 1, runtime_only: 1, contradicted: 0 };
  const markup = render({ graph: { ...graph, summary }, workers: [] });
  const header = markup.match(/aria-label="Observation coverage"[^>]*>(.*?)<\/p>/)[1].replace(/<[^>]+>/g, "");
  assert.equal(header, "3 observed · 1 observed, quiet · 1 partly observed · 0 stale · 1 no telemetry · 1 external · 1 runtime-only · grey machines have no telemetry: unknown, not idle", "observation counts add up to the components");
  assert.doesNotMatch(render({ workers: [] }), /Observation coverage/, "no summary, no claim");
});

test("a deploy is a changeover dated by the graph, not by the viewer's clock", () => {
  const at = 1_760_000_000_000;
  const render = (deployedAt) => renderToStaticMarkup(createElement(FactoryScene, { graph: sceneGraph([unit("unit", { reading: { ...busy, deployedAt } })], { observedAt: at }), workers: [] }));
  assert.match(render(at - 60_000), /data-changeover="[^"]*"[^>]*>deployed</, "a quiet deployed tag, not a warning");
  assert.doesNotMatch(render(at - 60_000), /dfPlant__scaffold|dfPlant__changed/);
  assert.match(render(at - 3 * 60 * 60_000), /data-changeover=/);
  assert.doesNotMatch(render(at - 2 * 24 * 60 * 60_000), /data-changeover=/);
  assert.doesNotMatch(render(undefined), /data-changeover=/);
});

test("fit shows the whole floor in the pane without relaying it out, the overview only when zoomed past it, and every zoom path returns to fit", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const priorWindow = globalThis.window, priorObserver = globalThis.ResizeObserver, priorStorage = globalThis.localStorage;
  const stored = new Map();
  globalThis.localStorage = { getItem: (key) => stored.get(key) ?? null, setItem: (key, value) => stored.set(key, value) };
  globalThis.window = { innerWidth: 1200, innerHeight: 800 };
  let resized;
  globalThis.ResizeObserver = class { constructor(callback) { resized = callback; } observe() {} disconnect() {} };
  // A 600×400 pane over the floor; the floor's box follows its rendered width and the pane's scroll.
  const listeners = {};
  const pane = { scrollLeft: 0, scrollTop: 0, clientWidth: 600, clientHeight: 400, getBoundingClientRect: () => ({ left: 0, top: 0, width: pane.clientWidth, height: pane.clientHeight }), addEventListener: (name, listener) => { listeners[name] = listener; }, removeEventListener() {} };
  const floor = { getBoundingClientRect: () => ({ left: -pane.scrollLeft, top: -pane.scrollTop, width: style().width }) };
  const view = { attributes: {}, setAttribute(name, value) { this.attributes[name] = Number(value); } };
  let renderer;
  const svg = () => renderer.root.findAll((node) => node.props["data-graph-digest"] === graph.digest)[0];
  const style = () => svg()?.props.style ?? {};
  const height = () => Number(svg().props.viewBox.split(" ")[3]), sceneWidth = () => Number(svg().props.viewBox.split(" ")[2]);
  const fitted = () => { const width = sceneWidth(), scale = Math.min(1, pane.clientWidth / width, pane.clientHeight / height()); return [Math.floor(width * scale), Math.floor(height() * scale)]; };
  const wheel = (deltaY) => act(async () => listeners.wheel({ ctrlKey: true, deltaY, clientX: 300, clientY: 200, preventDefault() {} }));
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { graph, appearance: commons, workers }), { createNodeMock: (element) => element.type === "svg" && element.props.viewBox?.startsWith("0 0 ") && element.props["data-graph-digest"] ? floor : element.type === "div" && element.props.className === "dfFactoryFloor__map" ? pane : element.type === "rect" && element.props["data-overview-view"] !== undefined ? view : {} }); });
    assert.equal(renderer.root.findAll((node) => node.props.className === "dfPlantMap").length, 0, "fitted, the whole floor is in view: no overview");
    const layout = floorFor(graph), viewBox = svg().props.viewBox;
    assert.equal(sceneWidth(), layout.width, "the floor is laid out from structure, not the pane");
    const button = (label) => renderer.root.findAll((node) => node.type === "button" && node.props["aria-label"] === label)[0];
    assert.equal(button("Zoom out").props.disabled, true, "fitted: nothing further out to show");
    assert.deepEqual([style().width, style().height], fitted(), "fit is the whole floor, bounded by the pane's height as well as its width");
    assert.ok(style().width <= 600 && style().height <= 400);
    // A tall pane (a phone held upright) fits by width; the fit follows the pane, never the floor's own size.
    pane.clientWidth = 300; pane.clientHeight = 900;
    await act(async () => resized());
    assert.deepEqual([style().width, style().height], fitted());
    assert.ok(style().width >= 299 && style().width <= 300, "the whole width of the upright pane");
    assert.equal(svg().props.viewBox, viewBox, "a resize scales the floor; it never moves a machine");
    pane.clientWidth = 600; pane.clientHeight = 400;
    await act(async () => resized());
    const [fitWidth] = fitted();
    const width = sceneWidth();
    const centre = () => ({ x: (pane.scrollLeft + 300) / (style().width / width), y: (pane.scrollTop + 200) / (style().width / width) });
    const before = centre();
    await act(async () => button("Zoom in").props.onClick());
    assert.ok(Math.abs(style().width - Math.floor(fitWidth * 1.5)) <= 1, "one and a half times the fit, to the pixel");
    const overview = renderer.root.findByProps({ className: "dfPlantMap" });
    assert.match(overview.props["aria-label"], /^Plant overview: 3 units, 0 external, 2 workers$/);
    assert.equal(overview.findAll((node) => node.props["data-overview-station"] !== undefined).length, layout.stations.length);
    assert.equal(overview.props.style.width, "min(12rem, " + (10 * width / height()).toFixed(2) + "rem)", "a wide pane keeps the usual overview");
    // The overview folds to a button, and the browser remembers it; past fit it comes back as it was left.
    await act(async () => button("Hide overview").props.onClick());
    assert.equal(renderer.root.findAll((node) => node.props.className === "dfPlantMap").length, 0);
    assert.equal(stored.get("dfFloorOverview"), "hidden");
    await act(async () => button("Show overview").props.onClick());
    assert.equal(stored.get("dfFloorOverview"), "shown");
    await act(async () => button("Zoom in").props.onClick());
    assert.equal(view.attributes.width, Math.min(width - view.attributes.x, 600 / (style().width / width)), "the overview window is the pane, in floor units");
    for (const axis of ["x", "y"]) assert.ok(Math.abs(centre()[axis] - before[axis]) < 2, `zoom keeps the floor's ${axis} under the pane's centre`);
    // A pinch out after button zooms returns to fit: the wheel sees the current zoom, not the one at mount.
    await wheel(1000);
    assert.equal(style().width, fitWidth, "pinching back to fit lands on fit");
    assert.equal(button("Zoom out").props.disabled, true);
    await wheel(-100);
    assert.ok(style().width > fitWidth, "a pinch in zooms from the fit scale");
    // At the limit a further zoom changes nothing, and leaves nothing for the next change to replay.
    for (let index = 0; index < 8; index += 1) await act(async () => button("Zoom in").props.onClick());
    assert.equal(style().width, width * 4);
    pane.scrollLeft = pane.scrollTop = 0;
    await act(async () => button("Zoom in").props.onClick());
    await act(async () => button("Fit floor").props.onClick());
    assert.equal(style().width, fitWidth);
    assert.deepEqual([pane.scrollLeft, pane.scrollTop], [0, 0], "fit applies no stale zoom anchor");
    // On a phone-width pane the overview is a quarter of it.
    pane.clientWidth = 390; pane.clientHeight = 590;
    await act(async () => resized());
    await act(async () => button("Zoom in").props.onClick());
    assert.match(renderer.root.findByProps({ className: "dfPlantMap" }).props.style.width, /^\d+px$/);
    assert.ok(Number.parseInt(renderer.root.findByProps({ className: "dfPlantMap" }).props.style.width, 10) <= 390 / 4 + 1);
    assert.equal(svg().props.viewBox, viewBox, "zooming and resizing never relay the floor out");
    // Storage that refuses leaves the toggle working for the visit.
    globalThis.localStorage = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
    await act(async () => button("Hide overview").props.onClick());
    assert.equal(renderer.root.findAll((node) => node.props.className === "dfPlantMap").length, 0);
    // An empty plant has no overview and nothing to zoom.
    await act(async () => renderer.update(createElement(FactoryScene, { graph: sceneGraph([]), appearance: commons, workers: [] })));
    assert.equal(renderer.root.findAll((node) => node.props.className === "dfPlantOverview").length, 0);
  } finally {
    await act(async () => renderer?.unmount());
    globalThis.window = priorWindow;
    globalThis.ResizeObserver = priorObserver;
    if (priorStorage === undefined) delete globalThis.localStorage; else globalThis.localStorage = priorStorage;
  }
});

test("a fold's inspector lists its routes and shows only the chosen route's evidence", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const folded = sceneGraph([unit("api", { machines: [machine("api:docks", "ingress", { label: "3 routes", represented: ["n0", "n1", "n2"], routes: ["/a", "/b", "/c"] })] })]);
  const pending = new Map(), loads = [];
  const onLoadNode = (id) => { loads.push(id); return new Promise((resolve, reject) => pending.set(id, { resolve, reject })); };
  const detail = (id) => ({ selectors: { node: id }, evidence: [], sources: [], modules: [], observers: [] });
  let renderer;
  const inspector = () => renderer.root.findByProps({ "aria-label": "Machine inspector" });
  const text = () => JSON.stringify(renderer.toJSON());
  const click = (label) => act(async () => inspector().findAllByType("button").find((button) => button.children.join("") === label).props.onClick());
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { graph: folded, appearance: commons, workers: [], onLoadNode, requestedEntity: { id: "api:docks" } })); });
    assert.deepEqual(inspector().findByProps({ "aria-label": "Folded routes" }).findAllByType("button").map((button) => button.children.join("")), ["/a", "/b", "/c"]);
    assert.deepEqual(loads, [], "a fold loads nothing until a route is chosen");
    await click("/a");
    assert.deepEqual(loads, ["n0"]);
    assert.match(text(), /Loading evidence…/);
    // Back and on to another route before the first answers: the late answer is never shown.
    await click("Back to folded routes");
    await click("/b");
    await act(async () => pending.get("n0").resolve(detail("n0")));
    assert.match(text(), /Loading evidence…/);
    assert.doesNotMatch(text(), /"n0"/);
    await act(async () => pending.get("n1").resolve(detail("n1")));
    assert.deepEqual(inspector().findAllByType("dd").map((term) => term.findByType("code").children.join("")), ["n1"]);
    await click("Back to folded routes");
    await click("/c");
    await act(async () => pending.get("n2").reject(new Error("gone")));
    assert.match(text(), /Evidence unavailable\./);
  } finally {
    await act(async () => renderer?.unmount());
  }
  // A request for a member (the Library naming its source) opens the fold with that member chosen, and loads it.
  loads.length = 0;
  let member;
  try {
    await act(async () => { member = create(createElement(FactoryScene, { graph: folded, appearance: commons, workers: [], onLoadNode, requestedEntity: { id: "n1" } })); });
    assert.deepEqual(loads, ["n1"]);
    assert.match(JSON.stringify(member.toJSON()), /Back to folded routes/);
    assert.match(JSON.stringify(member.root.findByProps({ "aria-label": "Machine inspector" }).findByType("h3").children), /3 routes/);
  } finally {
    await act(async () => member?.unmount());
  }
  // A public floor names the routes without loading anything.
  let publicRenderer;
  await act(async () => { publicRenderer = create(createElement(FactoryScene, { graph: folded, appearance: commons, workers: [], requestedEntity: { id: "api:docks" } })); });
  const list = publicRenderer.root.findByProps({ "aria-label": "Folded routes" });
  assert.deepEqual(list.findAllByType("li").map((item) => item.children.join("")), ["/a", "/b", "/c"]);
  assert.equal(list.findAllByType("button").length, 0, "public floors have no evidence to load");
  await act(async () => publicRenderer.unmount());
});

test("zooming to twice the fitted scale lists a manifold's routes inside its unmoved footprint", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const priorWindow = globalThis.window;
  globalThis.window = { innerWidth: 1200, innerHeight: 800 };
  const routes = Array.from({ length: 9 }, (_, index) => `/route-${index}`);
  const folded = sceneGraph([unit("api", { machines: [machine("api:docks", "ingress", { label: "9 routes", represented: routes, routes })] })]);
  const pane = { scrollLeft: 0, scrollTop: 0, clientWidth: 600, clientHeight: 400, getBoundingClientRect: () => ({ left: 0, top: 0, width: 600, height: 400 }) };
  let renderer;
  const style = () => renderer.root.findAll((node) => node.props["data-graph-digest"] === folded.digest)[0].props.style;
  const floor = { getBoundingClientRect: () => ({ left: 0, top: 0, width: typeof style().width === "number" ? style().width : layoutScene(folded).width }) };
  const manifold = () => renderer.root.findAll((node) => node.props["data-shape"] === "manifold")[0];
  const box = () => manifold().findAllByType("rect")[0].props;
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { graph: folded, appearance: commons, workers: [] }), { createNodeMock: (element) => element.props["data-graph-digest"] ? floor : element.props.className === "dfFactoryFloor__map" ? pane : {} }); });
    const zoomIn = () => act(async () => renderer.root.findAll((node) => node.type === "button" && node.props["aria-label"] === "Zoom in")[0].props.onClick());
    const fitted = box();
    assert.equal(manifold().findAllByProps({ "data-manifold-routes": "" }).length, 0, "fitted, a manifold shows its dock bars");
    await zoomIn();
    assert.equal(manifold().findAllByProps({ "data-manifold-routes": "" }).length, 0, "1.5x is not yet close enough to read");
    await zoomIn();
    const lines = manifold().findByProps({ "data-manifold-routes": "" }).findAllByType("text").map((node) => node.children.join(""));
    assert.deepEqual(lines, [...routes.slice(0, 6), "+3 more"]);
    assert.deepEqual(box(), fitted, "detail never moves or resizes the manifold");
  } finally {
    await act(async () => renderer?.unmount());
    globalThis.window = priorWindow;
  }
});

test("a unit known busy only as a whole moves its intake while its machines stay partial", () => {
  const partialBusy = { ...busy, observation: "partial" };
  const markup = renderToStaticMarkup(createElement(FactoryScene, { appearance: commons, workers: [], graph: sceneGraph([
    unit("edge", { reading: partialBusy, machines: [machine("edge-in", "ingress", { label: "/in", trigger: "request", reading: { ...unread, observation: "partial" } })] }),
    unit("dark"),
  ]) }));
  const intakes = [...markup.matchAll(/<g data-belt="intake" data-observation="([a-z]+)" data-state="([a-z]+)"/g)].map((match) => match.slice(1));
  assert.deepEqual(intakes, [["partial", "active"]], "only the unit with a known state has an intake, and it carries material");
  assert.match(markup, /data-belt="intake"[^>]*>(?:(?!<\/g>).)*dfPlant__material/s);
  assert.match(markup, /data-entity-id="edge-in" data-observation="partial"/, "the machine itself is still only partly observed");
});

test("a unit's runtime is said in words, each belt runs port to port between the machines it joins, and one dock feeds the intake", () => {
  const graph = sceneGraph([unit("edge", { runtime: "worker", reading: busy, machines: [machine("edge-in", "ingress", { trigger: "request" }), machine("edge-job", "job")] })],
    { flows: [{ from: "edge", to: "edge-job", kind: "runs", reading: unread }] });
  const markup = renderToStaticMarkup(createElement(FactoryScene, { appearance: commons, workers: [], graph }));
  assert.match(markup, /data-entity-id="edge"[^>]*data-tooltip="edge\nedge unit\n/, "the runtime is said where the unit's main machine is described");
  assert.doesNotMatch(markup, /data-runtime=|dfPlant__plate/, "no nameplate, no runtime badge");
  const items = Object.fromEntries(floorFor(graph, { workers: [] }).stations.map((item) => [item.entityId, item]));
  const belt = (kind) => markup.match(new RegExp(`<g data-belt="${kind}"[^>]*><path d="([^"]+)"`))[1].match(/-?[\d.]+ -?[\d.]+/g).map((pair) => pair.split(" ").map(Number));
  const touches = ([x, y], item) => x >= item.x - 8 && x <= item.x + item.width + 8 && y >= item.y - 8 && y <= item.y + item.height + 8;
  const [line, cell, dock] = [items.edge, items["edge-job"], items["edge-in"]];
  assert.ok(touches(belt("runs")[0], line) && touches(belt("runs").at(-1), cell), "from a port on the main machine to a port on the cell");
  assert.ok(touches(belt("intake")[0], dock) && touches(belt("intake").at(-1), line), "arrivals come in at the dock");
  const world = { generated_at: 0, summary: graph.summary, nodes: [{ id: "u", kind: "processor", label: "Edge function 1", runtime: "worker", evidence: "static", observation: "unobserved", state: "unknown", activity: "none" }], edges: [], workers: [] };
  assert.equal(publicFloor(world).graph.units[0].runtime, undefined, "a public name already says the runtime");
});

const pr = (number, document = {}, extra = {}) => ({ repository: "owner/repo", project_id: "project", kind: "pull_request", id: String(number), visual_id: `change:${number}`, observed_at: 10, tasks: [], missions: [], ...extra,
  document: { number, title: `Change ${number}`, head: "a".repeat(40), state: "open", ...document } });
const ciCheck = (number, state, conclusion) => ({ repository: "owner/repo", project_id: "project", kind: "check", id: `ci-${number}`, visual_id: "", observed_at: 10, tasks: [], missions: [],
  document: { name: "CI", revision: "a".repeat(40), scope: "head", state, conclusion, pull_requests: [number] } });
const allow = { review: { head: "a".repeat(40), state: "allow" } };
const cratesOf = (records) => projectCrates(Object.values(deriveProductionView(records).contraptions));
const atStation = (crates, number) => crates.find((crate) => crate.number === number);

test("the outbound line puts each change request at the station its records name, and nowhere without one", () => {
  const crates = cratesOf([
    pr(1), pr(2, { review: { head: "a".repeat(40), state: "block" } }),
    pr(3, allow), ciCheck(3, "in_progress", ""), pr(4, allow), ciCheck(4, "completed", "failure"),
    pr(5, { ...allow, merge_queue: "queued" }), ciCheck(5, "completed", "success"),
    pr(6, { ...allow, state: "merged", merge: "d".repeat(40) }), pr(7, { state: "closed" }),
  ]);
  // Shipped holds a day of merges by the records' own clock; older ones have left the line.
  const day = 24 * 60 * 60_000, merged = (number, at) => pr(number, { ...allow, state: "merged", merge: "d".repeat(40), merged_at: new Date(at).toISOString() }, { observed_at: day * 3 });
  assert.deepEqual(cratesOf([merged(8, day * 2 + 1), merged(9, day * 2 - 1)]).map((crate) => crate.number), [8]);
  assert.deepEqual(crates.map((crate) => [crate.number, crate.station, crate.fault]).sort((a, b) => a[0] - b[0]),
    [[1, 0, false], [2, 0, true], [3, 1, false], [4, 1, true], [5, 2, false], [6, 3, false]], "closed unmerged is gone");
  assert.match(atStation(crates, 2).stage, /Correction/);

  const markup = render({ crates, connected: true });
  assert.deepEqual([...markup.matchAll(/data-crate-station="(\d)"( data-fault="")?[^>]*aria-label="PR #(\d+)/g)].map((match) => [Number(match[3]), Number(match[1]), match[2] !== undefined]).sort((a, b) => a[0] - b[0]),
    crates.map((crate) => [crate.number, crate.station, crate.fault]).sort((a, b) => a[0] - b[0]), "drawn where the records put them");
  assert.match(markup, /PR #2 · Change 2\nReview · Correction/, "the tooltip says the number, title and stage");
  assert.equal((markup.match(/class="redtag"/g) ?? []).length, 2, "a blocked review and a failed check show the fault");
  const empty = render({ connected: true });
  assert.match(empty, /data-work-line/, "the line is always there, under the work tables");
  assert.doesNotMatch(empty, /data-crate=/, "with no change requests, nothing on it");
  assert.doesNotMatch(render({ crates: [], connected: true }), /data-crate/);

  // Machines never move for it: the line stands with the commons, apart from every machine, there with or without work, and no wider than the commons.
  const layout = layoutScene(graph);
  for (const station of layout.line) {
    assert.ok(station.x >= layout.facilities.x && station.x + station.width <= layout.facilities.x + layout.facilities.width && station.y + station.height <= layout.facilities.y + layout.facilities.height);
    for (const machine of layout.stations) assert.ok(station.x >= machine.footprint.x + machine.footprint.width || machine.footprint.x >= station.x + station.width || station.y >= machine.footprint.y + machine.footprint.height || machine.footprint.y >= station.y + station.height, `${station.label} clear of ${machine.entityId}`);
  }
  assert.deepEqual(layoutScene(graph), layout);

  // Deterministic, newest first, eight to a station and then a count.
  const many = Array.from({ length: 11 }, (_, index) => ({ id: `c${index}`, number: index + 1, title: "x", station: 0, stage: "Review pending", fault: false, taskIds: [] }));
  const once = placeCrates(layout, many), again = placeCrates(layout, [...many].reverse());
  assert.deepEqual(once, again);
  assert.deepEqual(once.filter((spot) => spot.shown).map((spot) => spot.crate.number), [11, 10, 9, 8, 7, 6, 5, 4]);
  assert.match(render({ crates: many }), /data-crate-overflow="3"[^>]*>\+3</);
});

test("the console and factoryd put the shared fixture's records at the same stations", () => {
  // factoryd's publicCrates (internal/daemon/public_work_test.go) reads the same file.
  const fixture = JSON.parse(readFileSync(new URL("../../../../fixtures/production-crates.json", import.meta.url), "utf8"));
  const crates = projectCrates(Object.values(deriveProductionView(fixture.records, fixture.now).contraptions));
  assert.deepEqual(crates.map((crate) => [crate.number, crate.station, crate.fault]).sort((a, b) => a[0] - b[0]), fixture.crates);
});

test("the public floor puts published crates on the line by station alone, never a number or title", () => {
  const world = { generated_at: 0, summary: { components: 0, inferred: 0, observed: 0, quiet: 0, partial: 0, stale: 0, unobserved: 0, opaque: 0, runtime_only: 0, contradicted: 0 }, nodes: [], edges: [], workers: [],
    crates: [{ id: "f".repeat(32), station: 2 }, { id: "e".repeat(32), station: 0, fault: true }] };
  const { crates } = publicFloor(world);
  assert.deepEqual(crates.map((crate) => [crate.id, crate.station, crate.fault, crate.number, crate.title]), [["f".repeat(32), 2, false, 0, ""], ["e".repeat(32), 0, true, 0, ""]]);
  assert.deepEqual(publicFloor({ ...world, crates: undefined }).crates, [], "an older factory publishes no line");
  const markup = render({ crates, connected: true });
  assert.match(markup, /data-crate="e{32}" data-crate-station="0" data-fault=""[^>]*data-tooltip="A change request\nReview · needs correction"/);
  assert.match(markup, /aria-label="A change request: Merge queue"/);
  assert.doesNotMatch(markup, /PR #/);
});

test("a crate moves once when its recorded stage changes, never on first sight or reconnect, and lights what it changes", async () => {
  const graph = sceneGraph([unit("a", { machines: [machine("a-in", "ingress", { trigger: "request" })] })]);
  const workers = [{ id: "ada", name: "Ada", role: "worker", activity: "busy", location: "working", nodeId: "a" }];
  const tasks = [{ id: "t1", agentId: "ada", projectId: "p", title: "t", status: "running", humanRequestIds: [] }];
  const crate = (station, extra = {}) => ({ id: "k1", number: 1, title: "One", station, stage: "s", fault: false, taskIds: ["t1"], ...extra });
  const proposals = { items: [{ id: "k1", title: "One", state: "active", operations: [{ entityId: "a-in", unitId: "a", path: "x", kind: "modification" }] }], onSelect: (id) => selected.push(id) };
  const selected = [];
  const layout = floorFor(graph, { workers, appearance: { scenery: "off" } });
  const slot = (crates, id = "k1") => { const spot = placeCrates(layout, crates).find((item) => item.crate.id === id); return [spot.x, spot.y]; };
  const saved = { performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document, setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout };
  let clock = 1000, next = 0, renderer;
  const frames = new Map();
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = () => ++next;
  globalThis.clearTimeout = () => {};
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  globalThis.window = { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) };
  globalThis.document = { visibilityState: "visible", addEventListener() {}, removeEventListener() {} };
  const tick = async (ms) => { await act(async () => { clock += ms; for (const [id, callback] of [...frames]) { frames.delete(id); callback(clock); } }); };
  const at = (id = "k1") => renderer.root.findByProps({ "data-crate": id }).props.transform.match(/translate\(([-\d.]+) ([-\d.]+)\)/).slice(1).map(Number);
  const scene = (crates, connected = true) => createElement(FactoryScene, { graph, workers, tasks, proposals, crates, connected, appearance: { scenery: "off", animation: "follow-device" } });
  try {
    // The floor connects before its production records are read; they arrive a render later.
    await act(async () => { renderer = create(scene(undefined, false)); });
    await act(async () => { renderer.update(scene(undefined)); });
    await tick(16);
    await act(async () => { renderer.update(scene([crate(1), crate(0, { id: "k0", number: 3 })])); });
    await tick(16);
    assert.equal(frames.size, 0, "the first read is history: nothing slides or flies in from its author");
    assert.deepEqual(at(), slot([crate(1)]), "what was already so when the floor opened stays put");
    await act(async () => { renderer.update(scene([crate(1)])); });

    await act(async () => { renderer.update(scene([crate(2)])); });
    await tick(300);
    const midway = at(), [from, to] = [slot([crate(1)]), slot([crate(2)])];
    assert.ok(midway[1] > from[1] && midway[1] < to[1], `slides down the line from one station's row to the next: ${midway}`);
    assert.ok(Math.abs((midway[0] - from[0]) * (to[1] - from[1]) - (midway[1] - from[1]) * (to[0] - from[0])) < 1e-6, "along the belt between them");
    for (let step = 0; step < 80 && frames.size > 0; step += 1) await tick(16);
    assert.deepEqual(at(), slot([crate(2)]));
    assert.equal(frames.size, 0, "and then nothing moves");
    await act(async () => { renderer.update(scene([crate(2, { stage: "other" })])); });
    await tick(16);
    assert.equal(frames.size, 0, "a change that is not a stage change moves nothing");

    // Reconnecting is a first look: what changed meanwhile is history.
    await act(async () => { renderer.update(scene([crate(2)], false)); });
    await act(async () => { renderer.update(scene([crate(3)])); });
    await tick(16);
    assert.deepEqual(at(), slot([crate(3)]));
    // Records kept from before a drop are unread on the new connection until refreshed; the refresh is a first look.
    await act(async () => { renderer.update(scene([crate(3)], false)); });
    await act(async () => { renderer.update(scene(undefined)); });
    await act(async () => { renderer.update(scene([crate(1)])); });
    await tick(16);
    assert.equal(frames.size, 0, "what changed offline is not replayed");
    assert.deepEqual(at(), slot([crate(1)]));
    await act(async () => { renderer.update(scene([crate(3)])); });
    await tick(16);
    for (let step = 0; step < 80 && frames.size > 0; step += 1) await tick(16);

    // A newly opened change comes from the agent whose task opened it.
    const opened = [crate(3), crate(0, { id: "k2", number: 2 })];
    await act(async () => { renderer.update(scene(opened)); });
    await tick(16);
    const ada = placeWorkers(layout, workers)[0];
    assert.ok(Math.hypot(at("k2")[0] - ada.x, at("k2")[1] - ada.y) < Math.hypot(slot(opened, "k2")[0] - ada.x, slot(opened, "k2")[1] - ada.y) - 20, `leaves Ada: ${at("k2")}`);
    for (let step = 0; step < 80 && frames.size > 0; step += 1) await tick(16);
    assert.deepEqual(at("k2"), slot(opened, "k2"));

    const target = renderer.root.findByProps({ "data-crate": "k1" });
    assert.equal(renderer.root.findAll((node) => node.props["data-lit"] !== undefined).length, 0);
    await act(async () => target.props.onPointerEnter());
    assert.deepEqual(renderer.root.findAll((node) => node.props["data-lit"] !== undefined && node.type === "rect").length, 1, "the machine its change touches is lit");
    await act(async () => target.props.onPointerLeave());
    assert.equal(renderer.root.findAll((node) => node.props["data-lit"] !== undefined).length, 0);
    await act(async () => target.props.onClick());
    assert.deepEqual(selected, ["k1"], "and it opens its Work row");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});
