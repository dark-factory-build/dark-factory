// Six operational graphs that exercise the floor's layout, and the measures
// taken of any floor drawn from them. The measures read a neutral view (world
// extent, station bodies and interaction positions, belts, a route function),
// so the same numbers can be taken of any renderer.
import { node, unit, graphWith } from "./graph.mjs";
import { fixtureGraph } from "./state.mjs";

const id = (n) => n.toString(16).padStart(4, "0").repeat(8);
const at = (n, kind, label, extra = {}) => node(0, kind, label, { id: id(n), ...extra });
const hub = (n, label, runtime, extra = {}) => unit(0, label, [`src/${label}`], { id: id(n), runtime, ...extra });
const edge = (from, to, kind = "calls") => ({ from: id(from), to: id(to), kind, evidence: "static", observation: "unobserved", state: "unknown" });

/** One unit with many operations: folded routes, timers, jobs, its own stores and queues. */
function monolith() {
  const nodes = [hub(1, "monolith", "server")], edges = [];
  for (let i = 0; i < 14; i++) { nodes.push(at(10 + i, "ingress", `/api/v1/resource-${i}`, { unit: id(1), trigger: "request" })); edges.push(edge(10 + i, 1, "handles")); }
  for (let i = 0; i < 4; i++) nodes.push(at(30 + i, "ingress", `nightly-${i}`, { unit: id(1), trigger: "timer" }));
  for (let i = 0; i < 9; i++) { nodes.push(at(40 + i, "job", `job-${i}`, { unit: id(1) })); edges.push(edge(40 + i, 60 + i % 2, "consumes")); if (i < 4) edges.push(edge(30 + i, 40 + i, "runs")); }
  for (let i = 0; i < 3; i++) { nodes.push(at(50 + i, "store", `monolith-db-${i}`, { unit: id(1) })); edges.push(edge(1, 50 + i, "uses")); }
  for (let i = 0; i < 2; i++) { nodes.push(at(60 + i, "queue", `monolith-queue-${i}`, { unit: id(1) })); edges.push(edge(1, 60 + i, "publishes")); }
  nodes.push(at(70, "external", "payments.example")); edges.push(edge(1, 70));
  return graphWith(nodes, edges);
}

/** Six small services that share two stores and two queues and call each other. */
function services() {
  const nodes = [], edges = [];
  for (let s = 0; s < 6; s++) {
    const u = 1 + s;
    nodes.push(hub(u, `service-${s}`, s % 2 ? "worker" : "server"));
    for (let d = 0; d < 1 + s % 2; d++) { nodes.push(at(20 + s * 4 + d, "ingress", `/s${s}/op-${d}`, { unit: id(u), trigger: "request" })); edges.push(edge(20 + s * 4 + d, u, "handles")); }
    edges.push(edge(u, 100 + s % 2, "uses"));
    edges.push(edge(u, 110 + s % 2, s < 3 ? "publishes" : "consumes"));
    if (s > 0) edges.push(edge(u - 1, 20 + s * 4, "calls"));
  }
  nodes.push(at(100, "store", "accounts-db"), at(101, "store", "cache"), at(110, "queue", "events"), at(111, "queue", "mail"));
  return graphWith(nodes, edges);
}

/** Units from three repositories with dependencies across them. */
function repositories() {
  const nodes = [hub(1, "app/web", "browser"), hub(2, "app/api", "server"), hub(3, "billing/service", "server"), hub(4, "notify/worker", "worker"), hub(5, "tools/cli", "cli")];
  const edges = [];
  nodes.push(at(10, "ingress", "/orders", { unit: id(2), trigger: "request" }), at(11, "ingress", "/users", { unit: id(2), trigger: "request" }));
  nodes.push(at(12, "ingress", "/charge", { unit: id(3), trigger: "request" }), at(13, "store", "ledger", { unit: id(3) }));
  nodes.push(at(14, "ingress", "on-event", { unit: id(4), trigger: "message" }), at(15, "job", "digest", { unit: id(4) }));
  nodes.push(at(20, "queue", "order-events"), at(21, "external", "smtp.example"), at(22, "store", "orders-db", { unit: id(2) }));
  edges.push(edge(1, 10), edge(1, 11), edge(10, 2, "handles"), edge(11, 2, "handles"), edge(2, 12), edge(12, 3, "handles"), edge(3, 13, "uses"),
    edge(2, 20, "publishes"), edge(20, 14, "consumes"), edge(14, 4, "handles"), edge(4, 21), edge(2, 22, "uses"), edge(5, 11));
  return { ...graphWith(nodes, edges), sources: ["app", "billing", "notify"].map((name, index) => ({ repository_id: id(200 + index), name, kind: "integrated", target_ref: "main", revision: "c3".repeat(20), observed_at: 0 })) };
}

/** Components with nothing between them, external parties, and activity the code does not explain. */
function disconnected() {
  const nodes = [hub(1, "lonely-a", "server"), hub(2, "lonely-b", "process"), hub(3, "caller", "server"), at(10, "ingress", "/a", { unit: id(1), trigger: "request" })];
  nodes.push(at(11, "unknown", "unrecognised", { unit: id(2) }), at(20, "external", "api.partner"), at(21, "external", "never-called"), at(22, "external", "also-uncalled"));
  nodes.push(at(30, "unknown", "POST /mystery", { evidence: "runtime", observation: "observed", state: "active", rate_per_hour: 3 }));
  nodes.push(at(31, "unknown", "GET /owned-mystery", { unit: id(3), evidence: "runtime", observation: "observed", state: "active" }));
  nodes.push(at(32, "store", "orphan-store"));
  return graphWith(nodes, [edge(3, 20), edge(10, 1, "handles")]);
}

/** Twenty units of nine routes each: folding keeps every unit readable. */
function large() {
  const nodes = [], edges = [];
  for (let u = 0; u < 20; u++) {
    const base = 100 + u * 16, hubId = base;
    nodes.push(hub(hubId, `unit-${String(u).padStart(2, "0")}`, ["server", "worker", "process", "browser"][u % 4]));
    for (let d = 0; d < 9; d++) { nodes.push(at(base + 1 + d, "ingress", `/u${u}/route-${d}`, { unit: id(hubId), trigger: "request" })); edges.push(edge(base + 1 + d, hubId, "handles")); }
    nodes.push(at(base + 10, "job", `u${u}-job-a`, { unit: id(hubId) }), at(base + 11, "job", `u${u}-job-b`, { unit: id(hubId) }), at(base + 12, "store", `u${u}-db`, { unit: id(hubId) }));
    edges.push(edge(hubId, base + 12, "uses"), edge(hubId, 10 + u % 4, "uses"));
    if (u > 0) edges.push(edge(hubId - 16, base + 1, "calls"));
  }
  for (let s = 0; s < 4; s++) nodes.push(at(10 + s, "store", `shared-${s}`));
  return graphWith(nodes, edges);
}

/** The six graphs, by name, in the order results are reported. */
export const FLOOR_FIXTURES = {
  "small-mixed": () => ({ ...fixtureGraph, edges: [...fixtureGraph.edges] }),
  monolith,
  "shared-services": services,
  "multi-repo": repositories,
  disconnected,
  large,
};

/** A few new components: a route on the first unit, a store two units share, and a new unit calling the first. */
export function withAdditions(graph) {
  const units = graph.nodes.filter((item) => item.kind === "processor").sort((a, b) => a.id < b.id ? -1 : 1);
  const [first, second = first] = units;
  const extra = [at(4000, "ingress", "/added-route", { unit: first.id, trigger: "request" }), at(4001, "store", "added-shared"), hub(4002, "added-unit", "server"), at(4003, "ingress", "/added-unit", { unit: id(4002), trigger: "request" })];
  const links = [{ ...edge(4000, 0, "handles"), to: first.id }, { ...edge(0, 4001, "uses"), from: first.id }, { ...edge(0, 4001, "uses"), from: second.id }, edge(4003, 4002, "handles"), { ...edge(4002, 0), to: first.id }];
  return { ...graph, digest: `${graph.digest}+`, nodes: [...graph.nodes, ...extra], edges: [...graph.edges, ...links] };
}

/** A few components gone: the first unit's last member and the last unowned store or queue. */
export function withRemovals(graph) {
  const first = graph.nodes.filter((item) => item.kind === "processor").sort((a, b) => a.id < b.id ? -1 : 1)[0];
  const member = graph.nodes.filter((item) => item.unit === first?.id).at(-1);
  const shared = graph.nodes.filter((item) => item.unit === undefined && (item.kind === "store" || item.kind === "queue")).at(-1);
  const gone = new Set([member?.id, shared?.id].filter(Boolean));
  return { ...graph, digest: `${graph.digest}-`, nodes: graph.nodes.filter((item) => !gone.has(item.id)), edges: graph.edges.filter((item) => !gone.has(item.from) && !gone.has(item.to)) };
}

/** A reordering of the same structure: arrays reversed and interleaved. */
export function shuffled(graph) {
  const mix = (items) => [...items].reverse().map((item, index, all) => all[(index * 7) % all.length]).filter((item, index, all) => all.indexOf(item) === index);
  const nodes = mix(graph.nodes), edges = mix(graph.edges);
  return { ...graph, nodes: nodes.length === graph.nodes.length ? nodes : [...graph.nodes].reverse(), edges: edges.length === graph.edges.length ? edges : [...graph.edges].reverse() };
}

const RADIUS = 9; // a worker's half-width, less a pixel of grace for grazing

/** Whether segment a-b passes through the rectangle (Liang–Barsky). */
export function segmentHits(a, b, rect) {
  let t0 = 0, t1 = 1;
  const dx = b.x - a.x, dy = b.y - a.y;
  for (const [p, q] of [[-dx, a.x - rect.x], [dx, rect.x + rect.width - a.x], [-dy, a.y - rect.y], [dy, rect.y + rect.height - a.y]]) {
    if (p === 0) { if (q <= 0) return false; continue; }
    const t = q / p;
    if (p < 0) { if (t > t1) return false; if (t > t0) t0 = t; } else { if (t < t0) return false; if (t < t1) t1 = t; }
  }
  return t0 < t1;
}

const grow = (rect, by) => ({ x: rect.x - by, y: rect.y - by, width: rect.width + 2 * by, height: rect.height + 2 * by });
const overlaps = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
const length = (points) => points.slice(1).reduce((sum, point, index) => sum + Math.hypot(point.x - points[index].x, point.y - points[index].y), 0);
const mean = (values) => values.length === 0 ? 0 : values.reduce((sum, value) => sum + value, 0) / values.length;
const round = (value) => Math.round(value * 10) / 10, ratio = (value) => Math.round(value * 100) / 100;

/** The belts a rendered floor draws: the first path of every element marked data-belt. */
export function beltsFromMarkup(html) {
  return [...html.matchAll(/<(?:g|path)[^>]*data-belt="[^"]*"[^>]*>/g)].map((match) => {
    const d = (match[0].match(/ d="([^"]+)"/) ?? html.slice(match.index).match(/ d="([^"]+)"/))[1];
    return [...d.matchAll(/(-?[\d.]+)[ ,](-?[\d.]+)/g)].map(([, x, y]) => ({ x: Number(x), y: Number(y) }));
  });
}

/** The world a rendered floor shows: its SVG viewBox. */
export function extentFromMarkup(html) {
  const [, , width, height] = html.match(/<svg[^>]*data-graph-digest[^>]*>/)[0].match(/viewBox="([^"]+)"/)[1].split(" ").map(Number);
  return { width, height };
}

/**
 * Measures one floor.
 * view: { extent, stations: [{ id, body, anchor? }], belts: [points], rest: point,
 *         flows: [{ from, to }], route(from, to, fromId?, toId?) -> points[] | undefined }
 * A route counts as clear only if no segment passes within a worker's half-width of a machine body
 * other than the two it joins (whose own fronts it must approach).
 */
export function measureFloor(view) {
  const bodies = view.stations.map((station) => station.body);
  let overlap = 0;
  for (let i = 0; i < bodies.length; i++) for (let j = i + 1; j < bodies.length; j++) if (overlaps(bodies[i], bodies[j])) overlap++;
  const clear = (points, ...ends) => points.every((point, index) => index === 0 || view.stations.every((station) => ends.includes(station.id) && segmentHits(points[index - 1], point, station.body) === false || !segmentHits(points[index - 1], point, grow(station.body, RADIUS))));
  const anchored = view.stations.filter((station) => station.anchor !== undefined);
  const fromRest = anchored.map((station) => ({ station, points: view.route(view.rest, station.anchor, undefined, station.id) }));
  const reachable = fromRest.filter(({ points, station }) => points !== undefined && clear([view.rest, ...points], station.id));
  const byId = new Map(anchored.map((station) => [station.id, station]));
  const pairs = view.flows.flatMap(({ from, to }) => {
    const a = byId.get(from), b = byId.get(to);
    if (a === undefined || b === undefined || a === b) return [];
    const straight = Math.hypot(b.anchor.x - a.anchor.x, b.anchor.y - a.anchor.y), points = view.route(a.anchor, b.anchor, a.id, b.id);
    return straight < 1 || points === undefined ? [] : [{ straight, walked: length([a.anchor, ...points]), clear: clear([a.anchor, ...points], a.id, b.id) }];
  });
  const belts = view.belts.map(length);
  return {
    extent: { width: round(view.extent.width), height: round(view.extent.height), area: Math.round(view.extent.width * view.extent.height) },
    stations: view.stations.length,
    bodyOverlaps: overlap,
    interaction: { positions: anchored.length, reachableClear: reachable.length, meanWalkFromRest: round(mean(fromRest.filter(({ points }) => points).map(({ points }) => length([view.rest, ...points])))) },
    connections: { belts: belts.length, total: round(belts.reduce((sum, value) => sum + value, 0)), mean: round(mean(belts)), max: round(Math.max(0, ...belts)) },
    routes: { pairs: pairs.length, meanWalk: round(mean(pairs.map((pair) => pair.walked))), meanStraight: round(mean(pairs.map((pair) => pair.straight))),
      meanDetour: ratio(mean(pairs.map((pair) => pair.walked / pair.straight))), maxDetour: ratio(Math.max(0, ...pairs.map((pair) => pair.walked / pair.straight))),
      clipped: pairs.filter((pair) => !pair.clear).length },
  };
}

/** How far retained stations moved, after removing the shift the whole floor made (its median). */
export function displacement(before, after) {
  const old = new Map(before.map((station) => [station.id, station.body]));
  const moves = after.flatMap((station) => { const was = old.get(station.id); return was === undefined ? [] : [{ dx: station.body.x - was.x, dy: station.body.y - was.y }]; });
  const median = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;
  const sx = median(moves.map((move) => move.dx)), sy = median(moves.map((move) => move.dy));
  const distances = moves.map((move) => Math.hypot(move.dx - sx, move.dy - sy));
  return { retained: moves.length, moved: distances.filter((value) => value > 0.5).length, mean: round(mean(distances)), max: round(Math.max(0, ...distances)) };
}
