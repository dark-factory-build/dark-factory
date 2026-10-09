// The floor is one connected space: these hold for every fixture graph, not one picture.
import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { layoutScene, placeWorkers, standable } from "../../dist/src/factory-scene/scene.js";
import { CROSSING_CLEARANCE, beltRoutes, crosses, findRoute, regionLabelBox, walkable } from "../../dist/src/factory-scene/movement.js";
import { projectGraph } from "../../dist/src/console-view.js";
import { publicFloor } from "../../dist/src/public-floor.js";
import { FLOOR_FIXTURES, beltsFromMarkup, chainScene, crossings, denseScene, pathological, shuffled, withAdditions, withRemovals } from "../../../../fixtures/floor.mjs";

const project = (wire) => projectGraph(new Map([["p", wire]]), ["p"]).graph;
const floors = Object.entries(FLOOR_FIXTURES).map(([name, build]) => ({ name, wire: build(), graph: project(build()) }));
const strictly = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
const where = (layout) => new Map(layout.stations.map((station) => [station.entityId, { x: station.x, y: station.y, width: station.width, height: station.height }]));
const relative = (layout, keys) => { const first = layout.stations.find((station) => keys.has(station.entityId)); return [...where(layout)].filter(([key]) => keys.has(key)).map(([key, rect]) => [key, rect.x - first.x, rect.y - first.y]); };

function assertWalk(layout, from, route, to, message) {
  assert.ok(route, `${message}: no walk`);
  const points = [from, ...route.points];
  for (let index = 1; index < points.length; index++) {
    assert.ok(walkable(layout.solids, points[index - 1], points[index]), `${message}: leg ${index} comes within a worker's radius of a solid or cuts its corner`);
    for (const station of layout.stations) assert.equal(crosses(points[index - 1], points[index], station), false, `${message}: through ${station.entityId}`);
  }
  assert.deepEqual([points.at(-1).x, points.at(-1).y], [to.x, to.y], `${message}: arrives`);
}

test("no machine, label or standing spot overlaps another, and every standing spot is reachable from where someone rests through free floor", () => {
  for (const { name, graph } of floors) {
    const layout = layoutScene(graph);
    // Footprints hold body, label and standing spot with an aisle around them; none overlaps another, so neither do any of those.
    for (let i = 0; i < layout.stations.length; i++) for (let j = i + 1; j < layout.stations.length; j++) {
      assert.equal(strictly(layout.stations[i].footprint, layout.stations[j].footprint), false, `${name}: ${layout.stations[i].entityId} and ${layout.stations[j].entityId}`);
    }
    for (const station of layout.stations) {
      const { footprint: f } = station;
      assert.ok(station.x >= f.x && station.y >= f.y && station.x + station.width <= f.x + f.width && station.y + station.height <= f.y + f.height, `${name}: ${station.entityId} inside its footprint`);
      assert.ok(f.x >= 0 && f.y >= 0 && f.x + f.width <= layout.width && f.y + f.height <= layout.height, `${name}: ${station.entityId} inside the world`);
      assert.equal(strictly(f, layout.facilities), false, `${name}: ${station.entityId} clear of the line`);
      assert.ok(standable(layout, station.anchor), `${name}: ${station.entityId}'s standing spot is free floor`);
    }
    assert.ok(layout.facilities.x + layout.facilities.width <= layout.width && layout.facilities.y + layout.facilities.height <= layout.height, `${name}: fit includes the facilities`);
    const seat = placeWorkers(layout, [{ id: "idler", name: "idler", role: "worker", activity: "idle", location: "resting" }])[0];
    for (const station of layout.stations) assertWalk(layout, seat, findRoute(layout, seat, station.anchor), station.anchor, `${name}: rest to ${station.entityId}`);
  }
});

test("cold layout is deterministic and the order the graph arrives in never changes it", () => {
  for (const { name, wire, graph } of floors) {
    const layout = layoutScene(graph);
    assert.deepEqual(layoutScene(project(wire)), layout, `${name}: same input, same floor`);
    assert.deepEqual(layoutScene(project(shuffled(wire))), layout, `${name}: shuffled input, same floor`);
    assert.deepEqual(layoutScene({ ...graph, units: [...graph.units].reverse().map((unit) => ({ ...unit, machines: [...unit.machines].reverse() })), flows: [...graph.flows].reverse(), shared: [...graph.shared].reverse() }), layout, `${name}: reordered scene graph`);
  }
});

test("traffic, faults and state, however dramatic, move nothing: machines, furniture and walls of nothing stay put", () => {
  for (const { name, graph } of floors) {
    const loud = { ...graph, units: graph.units.map((unit) => ({ ...unit, reading: { ...unit.reading, state: "failing", ratePerHour: 1e7, errorPermille: 999, latencyMs: 60000 } })),
      flows: graph.flows.map((flow) => ({ ...flow, reading: { ...flow.reading, observation: "observed", state: "active", ratePerHour: 1e7 } })) };
    const quiet = layoutScene(graph), busy = layoutScene(loud);
    assert.deepEqual([busy.stations.map(({ entityId: key, x, y }) => [key, x, y]), busy.facilities, busy.solids], [quiet.stations.map(({ entityId: key, x, y }) => [key, x, y]), quiet.facilities, quiet.solids], name);
  }
});

test("a small structural change keeps everything else where it stood; a new machine stands near what it is linked to", () => {
  for (const { name, wire, graph } of floors) {
    const before = layoutScene(graph);
    for (const [change, next] of [["added", project(withAdditions(wire))], ["removed", project(withRemovals(wire))]]) {
      const after = layoutScene(next, before);
      const kept = new Set(after.stations.map((station) => station.entityId).filter((key) => before.stations.some((station) => station.entityId === key)));
      // Re-placing a station that grew (a fold's longer count) is allowed; nothing else moves relative to anything else.
      const grown = new Set([...kept].filter((key) => after.stations.find((s) => s.entityId === key).footprint.width !== before.stations.find((s) => s.entityId === key).footprint.width));
      const still = new Set([...kept].filter((key) => !grown.has(key)));
      assert.deepEqual(relative(after, still), relative(before, still), `${name}: ${change} moved what it did not touch`);
      assert.ok(grown.size <= 1, `${name}: ${change} re-placed ${grown.size}`);
    }
    const after = layoutScene(project(withAdditions(wire)), before);
    const shared = after.stations.find((station) => station.machine.label === "added-shared"), unit = after.stations.find((station) => station.machine.label === "added-unit");
    // withAdditions links the new store to the first two units by id, and the new unit to the first.
    const firstTwo = new Set(wire.nodes.filter((node) => node.kind === "processor").map((node) => node.id).sort().slice(0, 2));
    const users = after.stations.filter((station) => firstTwo.has(station.entityId)).sort((a, b) => a.entityId < b.entityId ? -1 : 1);
    const gap = (a, b) => Math.max(0, a.footprint.x - b.footprint.x - b.footprint.width, b.footprint.x - a.footprint.x - a.footprint.width) + Math.max(0, a.footprint.y - b.footprint.y - b.footprint.height, b.footprint.y - a.footprint.y - a.footprint.height);
    // Beside them where there is room. Where they are surrounded (a monolith's hub, the middle of a dense floor) nothing is
    // pushed aside: it takes the nearest free floor, which is never farther than the edge of the floor nearest them.
    const near = (user) => Math.max(2.5 * Math.max(user.footprint.width, user.footprint.height), Math.min(user.footprint.x, user.footprint.y, after.width - user.footprint.x - user.footprint.width, after.height - user.footprint.y - user.footprint.height));
    assert.ok(users.some((user) => gap(shared, user) <= near(user)), `${name}: a new shared store stands near its users`);
    assert.ok(gap(unit, users[0]) <= near(users[0]), `${name}: a new unit stands near the unit it calls`);
  }
});

test("a shared dependency is one machine on neutral floor near its users, never a copy or a corner of one owner's area", () => {
  for (const { name, wire } of floors) {
    const graph = project(withAdditions(wire)), layout = layoutScene(graph);
    const shared = layout.stations.filter((station) => station.machine.label === "added-shared");
    assert.equal(shared.length, 1, `${name}: one canonical machine`);
    assert.equal(shared[0].unit, undefined, `${name}: owned by nobody`);
    for (const region of layout.regions) for (const rect of region.rects) assert.equal(strictly(rect, shared[0]), false, `${name}: inside ${region.unit}'s area`);
    assert.equal(graph.flows.filter((flow) => flow.to === shared[0].entityId).length, Math.min(2, graph.units.length - 1), `${name}: every link kept`);
  }
});

test("areas are paint: hidden, the floor is the same, still connected and walkable; never across another unit's machine", () => {
  for (const { name, graph } of floors) {
    const layout = layoutScene(graph), unpainted = { ...layout, regions: [] };
    const markup = renderToStaticMarkup(createElement(FactoryScene, { graph, workers: [] }));
    const hidden = markup.replace(/<g data-regions=""[^>]*>[\s\S]*?<\/g><\/g>|<g data-regions=""[^>]*><\/g>/, "");
    assert.ok(markup.includes("data-regions"), name);
    assert.doesNotMatch(hidden, /data-region=/, `${name}: the paint is gone`);
    for (const station of layout.stations) assert.ok(hidden.includes(`data-entity-id="${station.entityId}"`), `${name}: ${station.entityId} still drawn`);
    assert.equal((hidden.match(/data-belt=/g) ?? []).length, (markup.match(/data-belt=/g) ?? []).length, `${name}: every belt still drawn`);
    const seat = placeWorkers(layout, [{ id: "idler", name: "idler", role: "worker", activity: "idle", location: "resting" }])[0];
    for (const station of layout.stations) assert.deepEqual(findRoute(unpainted, seat, station.anchor), findRoute(layout, seat, station.anchor), `${name}: walks never read areas`);
    for (const region of layout.regions) for (const rect of region.rects) for (const station of layout.stations) {
      if (station.unit !== region.unit) assert.equal(strictly(rect, station), false, `${name}: ${region.unit}'s area covers ${station.entityId}`);
    }
  }
});

test("crossing from one area into another costs nothing: no hidden door, and walks cut diagonally where the floor allows", () => {
  let diagonal = 0;
  for (const { name, graph } of floors) {
    const layout = layoutScene(graph);
    const mains = layout.stations.filter((station) => station.unit === station.entityId);
    for (const a of mains) for (const b of mains) {
      if (a === b) continue;
      const route = findRoute(layout, a.anchor, b.anchor), straight = Math.hypot(b.anchor.x - a.anchor.x, b.anchor.y - a.anchor.y);
      assertWalk(layout, a.anchor, route, b.anchor, `${name}: ${a.entityId} to ${b.entityId}`);
      // Only machines are in the way: the walk is never much longer than going round them.
      assert.ok(route.length <= 1.6 * straight + 120, `${name}: ${a.entityId} to ${b.entityId} detours ${route.length} for ${straight}`);
      let previous = a.anchor;
      for (const point of route.points) { if (Math.abs(point.x - previous.x) > 1 && Math.abs(point.y - previous.y) > 1) diagonal++; previous = point; }
    }
  }
  assert.ok(diagonal > 0, "walks are not Manhattan-only");
});

test("disconnected, external and unexplained components stay visibly apart and never gain an owner", () => {
  const { graph } = floors.find((floor) => floor.name === "disconnected"), layout = layoutScene(graph);
  const byLabel = (label) => layout.stations.find((station) => station.machine.label === label);
  for (const label of ["never-called", "also-uncalled", "api.partner"]) assert.equal(byLabel(label).shape, "gate", label);
  for (const label of ["POST /mystery", "GET /owned-mystery", "orphan-store", "never-called"]) assert.equal(byLabel(label).unit, undefined, label);
  // Unlinked groups keep more than an aisle between them.
  const lonelyA = byLabel("lonely-a"), lonelyB = byLabel("lonely-b"), apart = (a, b) => !strictly({ ...a.footprint, x: a.footprint.x - 12, y: a.footprint.y - 12, width: a.footprint.width + 24, height: a.footprint.height + 24 }, b.footprint);
  assert.ok(apart(lonelyA, lonelyB), "two unrelated units do not touch");
  const claimed = byLabel("GET /owned-mystery"), caller = byLabel("caller");
  assert.ok(Math.hypot(claimed.x - caller.x, claimed.y - caller.y) < 200, "runtime activity stands near the unit it claims");
});

test("operator and public floors use one layout from what each may receive, and the public one carries no private detail", () => {
  const { wire } = floors.find((floor) => floor.name === "multi-repo");
  const world = { generated_at: 1, summary: wire.summary, workers: [], crates: [],
    nodes: wire.nodes.map((node) => ({ id: node.id, kind: node.kind, label: node.label, ...(node.unit === undefined ? {} : { unit: node.unit }), ...(node.trigger === undefined ? {} : { trigger: node.trigger }), evidence: node.evidence, observation: node.observation, state: node.state, activity: "none" })),
    edges: wire.edges.map((edge) => ({ from: edge.from, to: edge.to, kind: edge.kind, evidence: edge.evidence, observation: edge.observation, state: edge.state, activity: "none" })) };
  const operator = layoutScene(project(wire)), shown = publicFloor(world);
  assert.deepEqual(layoutScene(shown.graph).stations.map(({ entityId: key, x, y }) => [key, x, y]), operator.stations.map(({ entityId: key, x, y }) => [key, x, y]), "the same structure lays out the same");
  assert.deepEqual(shown.graph.sources, []);
  const markup = renderToStaticMarkup(createElement(FactoryScene, { graph: shown.graph, workers: shown.workers, crates: shown.crates }));
  for (const secret of wire.nodes.flatMap((node) => node.paths)) assert.equal(markup.includes(secret), false, `no source path: ${secret}`);
  assert.doesNotMatch(markup, /Evidence|Loading evidence/, "no detail loader on a public floor");
});

/** Each fixture as the scene draws it: its layout (as rendered here) and its belts. */
const drawn = floors.map(({ name, graph }) => {
  const layout = layoutScene(graph), markup = renderToStaticMarkup(createElement(FactoryScene, { graph, workers: [] }));
  return { name, layout, markup, belts: beltsFromMarkup(markup), everything: beltsFromMarkup(markup, "all"), labels: [...layout.stations.map((station) => ({ key: station.entityId, rect: station.label })), ...layout.regions.map((region) => ({ key: `area ${region.unit}`, rect: regionLabelBox(region) }))] };
});

test("belts cross only square-on, and every crossing is drawn as a bridge", () => {
  for (const { name, belts, markup } of drawn) {
    const bridges = [...markup.matchAll(/data-crossing="[hv]" transform="translate\(([-\d.]+) ([-\d.]+)\)/g)].map(([, x, y]) => ({ x: Number(x), y: Number(y) }));
    for (const crossing of crossings(belts)) {
      assert.ok(crossing.angle >= 60, `${name}: a crossing at ${Math.round(crossing.x)},${Math.round(crossing.y)} is ${Math.round(crossing.angle)}°`);
      assert.ok(bridges.some((bridge) => Math.hypot(bridge.x - crossing.x, bridge.y - crossing.y) < 2), `${name}: the crossing at ${Math.round(crossing.x)},${Math.round(crossing.y)} has a bridge`);
    }
  }
});

test("no crossing lands on or near a label, a port, a junction or a machine", () => {
  for (const { name, layout, belts, labels } of drawn) {
    const ends = belts.flatMap((belt) => [belt[0], belt.at(-1)]);
    const away = (point, rect) => point.x < rect.x - CROSSING_CLEARANCE || point.x > rect.x + rect.width + CROSSING_CLEARANCE || point.y < rect.y - CROSSING_CLEARANCE || point.y > rect.y + rect.height + CROSSING_CLEARANCE;
    for (const crossing of crossings(belts)) {
      for (const { key, rect } of [...labels, ...layout.stations.map((station) => ({ key: station.entityId, rect: station }))]) assert.ok(away(crossing, rect), `${name}: a crossing at ${Math.round(crossing.x)},${Math.round(crossing.y)} is on ${key}`);
      for (const end of ends) assert.ok(Math.hypot(end.x - crossing.x, end.y - crossing.y) >= CROSSING_CLEARANCE, `${name}: a crossing beside a port or junction at ${end.x},${end.y}`);
    }
  }
});

test("no belt or overhead link runs through a label or a machine, labels never overlap each other, and no two belts run along each other", () => {
  for (const { name, layout, belts, everything, labels } of drawn) {
    for (const [index, belt] of everything.entries()) for (let leg = 1; leg < belt.length; leg++) {
      for (const { key, rect } of labels) assert.equal(crosses(belt[leg - 1], belt[leg], rect), false, `${name}: belt or link ${index} runs through the label of ${key}`);
      for (const station of layout.stations) assert.equal(crosses(belt[leg - 1], belt[leg], { x: station.x + 1, y: station.y + 1, width: station.width - 2, height: station.height - 2 }), false, `${name}: belt or link ${index} runs through ${station.entityId}`);
    }
    for (let i = 0; i < labels.length; i++) for (let j = i + 1; j < labels.length; j++) assert.equal(strictly(labels[i].rect, labels[j].rect), false, `${name}: ${labels[i].key}'s label over ${labels[j].key}'s`);
    // Legs of two belts on one line, overlapping: one belt drawn along the other, which a reader cannot tell apart.
    const legs = belts.flatMap((belt, index) => belt.slice(1).map((point, leg) => ({ index, a: belt[leg], b: point })));
    for (const one of legs) for (const two of legs) {
      if (one.index >= two.index) continue;
      const flat = (leg) => Math.abs(leg.a.y - leg.b.y) < 0.5, upright = (leg) => Math.abs(leg.a.x - leg.b.x) < 0.5;
      const overlap = (p0, p1, q0, q1) => Math.min(Math.max(p0, p1), Math.max(q0, q1)) - Math.max(Math.min(p0, p1), Math.min(q0, q1));
      if (flat(one) && flat(two) && Math.abs(one.a.y - two.a.y) < 2) assert.ok(overlap(one.a.x, one.b.x, two.a.x, two.b.x) <= 2, `${name}: belts ${one.index} and ${two.index} run along each other at y=${one.a.y}`);
      if (upright(one) && upright(two) && Math.abs(one.a.x - two.a.x) < 2) assert.ok(overlap(one.a.y, one.b.y, two.a.y, two.b.y) <= 2, `${name}: belts ${one.index} and ${two.index} run along each other at x=${one.a.x}`);
    }
  }
});

test("a unit's neighbouring machines are one region, named once; only members far apart make another lobe", () => {
  const monolith = drawn.find((floor) => floor.name === "monolith");
  assert.equal(monolith.layout.regions.length, 1, "twenty machines standing together are one area, not twenty tiles");
  assert.equal((monolith.markup.match(/data-region-label=/g) ?? []).length, 1, "and it is named once");
  for (const { name, layout } of drawn) {
    const units = new Set(layout.stations.flatMap((station) => station.unit ?? []));
    assert.ok(layout.regions.length <= layout.stations.filter((station) => station.unit !== undefined).length / 2 + units.size, `${name}: regions merge, they are not one per machine`);
    for (const region of layout.regions) assert.ok(region.rects.some((rect) => region.label.x >= rect.x && region.label.x <= rect.x + rect.width && region.label.y - 7 >= rect.y - 7 && region.label.y <= rect.y + rect.height + 7), `${name}: ${region.unit}'s name sits on its own region`);
  }
});

test("on dense floors no belt or overhead link passes through a machine or a label, and every belt crossing is square and bridged", () => {
  for (const config of [{ seed: 2, units: 10, per: 6, shared: 5, flows: 2 }, { seed: 51, units: 60, per: 4, shared: 8, parties: 4, flows: 0.5 }]) {
    const graph = denseScene(config), layout = layoutScene(graph), at = new Map(layout.stations.map((station) => [station.entityId, station]));
    const pairs = graph.flows.map((flow) => ({ key: `${flow.from} ${flow.to}`, from: at.get(flow.from), to: at.get(flow.to) }));
    const { routes, crossings: bridges, links } = beltRoutes(layout, pairs);
    const { stubs } = beltRoutes(layout, pairs);
    assert.equal(routes.size + stubs.size, pairs.length, `seed ${config.seed}: every connection is drawn, as a belt, a link or a pair of stubs`);
    const labels = [...layout.stations.map((station) => station.label), ...layout.regions.map(regionLabelBox)];
    for (const [key, points] of routes) for (let leg = 1; leg < points.length; leg++) {
      assert.ok(points[leg - 1].x === points[leg].x || points[leg - 1].y === points[leg].y, `seed ${config.seed}: ${key} is square`);
      for (const station of layout.stations) assert.equal(crosses(points[leg - 1], points[leg], { x: station.x + 1, y: station.y + 1, width: station.width - 2, height: station.height - 2 }), false, `seed ${config.seed}: ${key} through ${station.entityId}`);
      for (const label of labels) assert.equal(crosses(points[leg - 1], points[leg], label), false, `seed ${config.seed}: ${key} through a label`);
    }
    const laid = [...routes].filter(([key]) => !links.has(key)).map(([, points]) => points);
    for (const crossing of crossings(laid)) {
      assert.ok(crossing.angle >= 89, `seed ${config.seed}: a ${Math.round(crossing.angle)}° crossing`);
      assert.ok(bridges.some((bridge) => Math.hypot(bridge.x - crossing.x, bridge.y - crossing.y) < 2), `seed ${config.seed}: an unbridged crossing at ${crossing.x},${crossing.y}`);
    }
  }
});

test("unrelated groups pack toward 16:10, not into a strip", () => {
  for (const [name, graph] of [["12 chains", chainScene(12)], ["48 chains", chainScene(48)], ["large", floors.find((floor) => floor.name === "large").graph],
    ["a chain of 20 shared machines", pathological("chain", 20)], ["a chain of 4,000 shared machines", pathological("chain", 4000)]]) {
    const layout = layoutScene(graph), aspect = layout.width / layout.height;
    assert.ok(aspect >= 1 && aspect <= 2.5, `${name}: ${layout.width}×${layout.height} is ${aspect.toFixed(2)}:1`);
  }
});

test("every connection is drawn, as a belt, an overhead link or a pair of named stubs, even where the work runs out", () => {
  for (const [which, size] of [["spokes", 1500], ["hub", 300], ["clique", 40]]) {
    const graph = pathological(which, size), started = performance.now(), markup = renderToStaticMarkup(createElement(FactoryScene, { graph, workers: [] }));
    assert.equal((markup.match(/data-belt=/g) ?? []).length, graph.flows.length, `${which}: ${graph.flows.length} flows, all drawn`);
    assert.ok(performance.now() - started < 5000, `${which}: bounded work`);
  }
});

test("a store shared by forty services is laid out near them, not in fallback rows", () => {
  const reading = { evidence: "static", observation: "unobserved", state: "unknown", ratePerHour: 0, errorPermille: 0, latencyMs: 0 }, units = [], flows = [];
  for (let u = 0; u < 40; u++) {
    const id = `u${String(u).padStart(3, "0")}`;
    units.push({ id, label: `svc-${u}`, reading, machines: ["ingress", "store", "job"].map((kind, k) => ({ id: `${id}m${k}`, kind, label: `${kind}-${u}`, reading })) });
    flows.push({ from: id, to: "s0", kind: "uses", reading });
  }
  const layout = layoutScene({ digest: "shared", units, shared: [{ id: "s0", kind: "store", label: "shared-0", reading }], parties: [], quarantine: [], flows });
  const store = layout.stations.find((station) => station.entityId === "s0"), mains = layout.stations.filter((station) => station.unit === station.entityId);
  const mean = mains.reduce((sum, station) => sum + Math.hypot(station.x - store.x, station.y - store.y), 0) / mains.length;
  // Fallback rows put the store in a corner, about 940 px from the average service; placed around it, about 500.
  assert.ok(mean < 650, `mean service-to-store distance ${Math.round(mean)}`);
  assert.ok(new Set(layout.stations.map((station) => station.footprint.y)).size > layout.stations.length / 4, "not laid in a few equal rows");
});

test("stubs each have their own port, and their names touch no machine, label, belt, link or other name", () => {
  let stubs = 0, named = 0;
  for (const graph of [denseScene({ seed: 2, units: 10, per: 6, shared: 5, flows: 2 }), denseScene({ seed: 9, units: 30, per: 6, shared: 10, parties: 8, flows: 1.5 }), pathological("hub", 300), pathological("spokes", 1200)]) {
    const layout = layoutScene(graph), at = new Map(layout.stations.map((station) => [station.entityId, station]));
    const { routes, stubs: ends } = beltRoutes(layout, graph.flows.map((flow) => ({ key: `${flow.from} ${flow.to}`, from: at.get(flow.from), to: at.get(flow.to) })));
    const solids = [...layout.stations, ...layout.stations.map((station) => station.label), ...layout.regions.map(regionLabelBox)], ports = new Set(), texts = [];
    const legs = [...routes.values()].flatMap((points) => points.slice(1).map((point, index) => [points[index], point]));
    for (const end of [...ends.values()].flat()) {
      if (end === undefined) continue;
      stubs++;
      const port = `${end.points[0].x},${end.points[0].y}`;
      assert.equal(ports.has(port), false, `two stubs share the port at ${port}`);
      ports.add(port);
      for (const rect of solids) assert.equal(crosses(end.points[0], end.points[1], { x: rect.x + 1, y: rect.y + 1, width: rect.width - 2, height: rect.height - 2 }), false, "a stub through a machine or label");
      if (end.text === undefined) continue;
      named++;
      const box = end.text;
      for (const rect of [...solids, ...texts]) assert.equal(strictly(box, rect), false, `${end.text.value} over something`);
      for (const [a, b] of legs) assert.equal(crosses(a, b, box), false, `${end.text.value} over a belt or link`);
      texts.push(box);
    }
  }
  assert.ok(stubs > 0 && named > 0, `the fixtures exercise stubs (${stubs}, ${named} named)`);
});
