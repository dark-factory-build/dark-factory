import type { GraphNode, GraphReading, GraphSummary, GraphSource, RunTelemetry, SpriteAppearance } from "@dark-factory/client";

/** A node's runtime reading, as the floor shows it. Static evidence never sets state. */
export type SceneReading = Readonly<{
  evidence: GraphReading["evidence"];
  observation: GraphReading["observation"];
  state: GraphReading["state"];
  ratePerHour: number;
  errorPermille: number;
  latencyMs: number;
  lastSeen?: number;
  /** The unit's last observed deploy: a changeover, not traffic. */
  deployedAt?: number;
}>;

/** One machine on the floor: one operational node, or a fold of several. */
export type SceneMachine = Readonly<{
  id: string;
  kind: GraphNode["kind"];
  label: string;
  trigger?: GraphNode["trigger"];
  reading: SceneReading;
  /** Nodes this machine stands for when it folds several (a manifold of routes). */
  represented?: readonly string[];
  /** The folded routes' labels, listed inside the manifold when zoomed in. */
  routes?: readonly string[];
  /** The name of the unit a runtime-only machine claims, for its label. */
  owner?: string;
  /** That unit's id: the machine stands near it, never inside it. */
  claims?: string;
}>;

/** A deployment unit: its main machine and the machines it owns. Logical only; it has no walls or plot. */
export type SceneUnit = Readonly<{
  id: string;
  label: string;
  runtime?: GraphNode["runtime"];
  reading: SceneReading;
  machines: readonly SceneMachine[];
}>;

export type SceneFlow = Readonly<{ from: string; to: string; kind: string; reading: SceneReading }>;

/** The world model: operational graph and runtime reading, for one floor. */
export type SceneGraph = Readonly<{
  /** Changes only when static structure or the detail level changes. */
  digest: string;
  units: readonly SceneUnit[];
  /** Stores and queues no single unit owns. */
  shared: readonly SceneMachine[];
  /** External parties. */
  parties: readonly SceneMachine[];
  /** Runtime activity no static node explains. */
  quarantine: readonly SceneMachine[];
  flows: readonly SceneFlow[];
  summary?: GraphSummary;
  sources?: readonly GraphSource[];
  /** When the daemon read the runtime state shown. */
  observedAt?: number;
}>;

export type SceneProposal = Readonly<{
  id: string; title: string; state: "active" | "stale" | "unavailable"; base?: string; head?: string;
  operations: readonly Readonly<{ entityId?: string; unitId?: string; path: string; previousPath?: string; kind: "addition" | "modification" | "removal" | "move" }>[];
}>;

/** The changes touching one machine or unit. */
export function proposalsForEntity(proposals: readonly SceneProposal[], id?: string): readonly SceneProposal[] {
  if (id === undefined) return [];
  return proposals.filter((proposal) => proposal.operations.some((operation) => operation.entityId === id || operation.unitId === id));
}

export type SceneWorker = Readonly<{
  id: string;
  name: string;
  role: "orchestrator" | "worker";
  provider?: "claude_code" | "codex" | "shell";
  /** A worker with a standing instruction: its specialty and what it is doing, as the factory serves them. */
  specialist?: Readonly<{ title: string; text: string; next: string }>;
  activity: "busy" | "waiting" | "needs-you" | "idle";
  paused?: boolean;
  appearance?: SpriteAppearance;
  /** Live work is placed at a machine; retained samples annotate the resting area. */
  location?: "working" | "last-observed" | "unobserved" | "resting";
  locationLabel?: string;
  /** The unit the worker is at, or the unowned machine. */
  nodeId?: string;
  /** The machine it works at. */
  observedBayId?: string;
  review?: Readonly<{ proposalId: string; scope: string }>;
  /** What the live run's agent CLI recorded; absent when it recorded nothing. */
  telemetry?: Readonly<RunTelemetry>;
}>;

/** A busy worker whose agent recorded no tool call or API request for this long is shown waiting. */
export const QUIET_SECONDS = 120;

/** Recorded effort and spend as one line, e.g. "12.4k tokens · $0.31 · 18 tool calls"; only what was recorded. */
export function telemetryLine(telemetry: RunTelemetry): string {
  const tokens = telemetry.tokens_in + telemetry.tokens_out;
  const cost = telemetry.cost_micro_usd / 1e6;
  return [
    tokens === 0 ? "" : `${tokens < 1000 ? tokens : tokens < 1e6 ? `${(tokens / 1e3).toFixed(1)}k` : `${(tokens / 1e6).toFixed(1)}M`} tokens`,
    cost === 0 ? "" : cost < 0.01 ? "<$0.01" : `$${cost.toFixed(2)}`,
    telemetry.tool_calls === 0 ? "" : `${telemetry.tool_calls} tool call${telemetry.tool_calls === 1 ? "" : "s"}`,
  ].filter((part) => part !== "").join(" · ");
}

/** Tooltip lines for a worker's recorded effort, and its recorded silence once that reads as waiting. */
export function recordedEffort(telemetry: RunTelemetry): string {
  const line = telemetryLine(telemetry);
  const quiet = telemetry.quiet_seconds ?? 0;
  return `${line === "" ? "" : `\n${line}`}${quiet >= QUIET_SECONDS ? `\nNo tool call or API request recorded for ${Math.floor(quiet / 60)}m` : ""}`;
}

export type ScenePoint = Readonly<{ x: number; y: number }>;

/** One change request on the outbound line, at the station its records put it. */
export type SceneCrate = Readonly<{
  /** The production key, which is also its proposal's id. */
  id: string;
  number: number;
  title: string;
  station: 0 | 1 | 2 | 3;
  /** The recorded stages, as the Work row names them. */
  stage: string;
  fault: boolean;
  taskIds: readonly string[];
}>;

export const LINE_STATIONS = ["Review", "Checks", "Merge queue", "Shipped"] as const;

export type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;

/** How a machine is pictured. A shape is chosen from the node kind, never its name. */
export type StationShape = "line" | "dock" | "manifold" | "clock" | "cell" | "silo" | "conveyor" | "crate" | "gate";

/** One machine where it stands: its body is solid, its label and the spot in front of it are floor. */
export type SceneStation = SceneRect & Readonly<{
  entityId: string;
  representedIds?: readonly string[];
  shape: StationShape;
  machine: SceneMachine;
  /** The unit it belongs to: its own id for a unit's main machine. */
  unit?: string;
  /** Where a worker stands to use it. */
  anchor: ScenePoint;
  /** Where its label is drawn: floor nobody builds on and no belt crosses. */
  label: SceneRect;
  /** Body, label and standing spot with an aisle's half-width around them. Footprints never overlap. */
  footprint: SceneRect;
}>;

export type SceneLayout = Readonly<{
  width: number;
  height: number;
  stations: readonly SceneStation[];
  /** Each unit's area: the footprints of its stations, in lobes where they are apart. Painted only; layout never reads it. */
  regions: readonly SceneRegion[];
  /** The five fixtures, each in free floor between machines. They depend on the machines alone, never on who is offered or who is here. */
  fixtures: readonly SceneFixture[];
  /** The fixtures this floor shows. */
  implements: readonly BreakRoomErrand[];
  /** What nobody walks through: machine bodies and fixtures. */
  solids: readonly SceneRect[];
}>;

/**
 * One neighbourhood of a unit: members standing next to each other, merged
 * into one irregular patch of floor. `rects` tile it without overlapping;
 * `outline` traces its edge; `label` is where its one name goes.
 */
export type SceneRegion = Readonly<{ unit: string; lobe: number; hue: number; rects: readonly SceneRect[]; outline: string; label: ScenePoint & Readonly<{ width: number }> }>;

export type SceneWorkerPlacement = Readonly<{
  id: string;
  area: "work" | "resting" | "staging" | "outside" | "overflow";
  /** The machine worked at, or the unit's main machine a worker rests beside. */
  stationId?: string;
  /** A resting worker who has got up for a while stands here instead of sitting. */
  errand?: BreakRoomErrand;
  errandKey?: string;
  x: number;
  y: number;
}>;

/** Coffee is ambient; the rest are implements, used when a recorded event says so (shelf also ambiently). */
export type BreakRoomErrand = "shelf" | "coffee" | "board" | "missions" | "tasks";

/** A piece of furniture and the spot a worker stands to use it. */
export type SceneFixture = Readonly<{ errand: BreakRoomErrand; x: number; y: number; stand: ScenePoint }>;

export const PADDING = 16;
export const WORKER_SIZE = 20;
/** Half an aisle around every footprint: two neighbours leave a worker room to pass with the walking grid's slack. */
export const MARGIN = 18;
// Unrelated groups stand this much further apart than neighbours, so a disconnected group reads as one.
const GROUP_GAP = 24;
const ASPECT = 1.6;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

const shapeOf = (machine: SceneMachine): StationShape => machine.represented !== undefined ? "manifold"
  : machine.kind === "ingress" ? machine.trigger === "timer" ? "clock" : "dock"
  : machine.kind === "job" ? "cell" : machine.kind === "store" ? "silo" : machine.kind === "queue" ? "conveyor"
  : machine.kind === "processor" ? "line" : machine.kind === "external" ? "gate" : "crate";

const size: Record<StationShape, { width: number; height: number }> = {
  line: { width: 96, height: 44 }, dock: { width: 40, height: 18 }, manifold: { width: 44, height: 40 }, clock: { width: 22, height: 22 },
  cell: { width: 34, height: 30 }, silo: { width: 28, height: 40 }, conveyor: { width: 64, height: 16 }, crate: { width: 22, height: 22 }, gate: { width: 12, height: 28 },
};

// A label is set in an 8px monospace face, about this wide a character.
const CHAR = 5;
/** How many characters of a label a station shows: the renderer truncates to exactly this. */
export function labelChars(shape: StationShape, width: number) {
  return shape === "dock" || shape === "manifold" || shape === "line" ? 22 : shape === "gate" ? 20 : Math.max(6, Math.floor((width + 10) / 5));
}

/** Where a station's label, and the worker using it, go, relative to its body's corner. */
function sides(machine: SceneMachine) {
  const shape = shapeOf(machine), { width, height } = size[shape], chars = Math.min(Array.from(machine.label).length, labelChars(shape, width)) * CHAR;
  // Docks and manifolds are labelled above from their left edge; a gate to its right over two lines; the rest centred below.
  const label = shape === "dock" || shape === "manifold" ? { x: 0, y: -11, width: chars, height: 10 }
    : shape === "gate" ? { x: width + 6, y: 2, width: 124, height: 23 }
    : { x: (width - chars) / 2, y: height + 1, width: chars, height: 10 };
  // A line is worked at its control panel, the column at its right end, close up where its label leaves room;
  // everything else from the middle of its front.
  // Its footprint keeps the usual standing room either way, so where a worker stands never moves a machine.
  const panel = shape === "line" && label.x + label.width <= width - 13 - WORKER_SIZE / 2, front = Math.max(height + 16, label.y + label.height + 13);
  const anchor = { x: shape === "line" ? width - 13 : width / 2, y: panel ? height + WORKER_SIZE / 2 + 2 : front };
  const left = Math.min(0, label.x, anchor.x - WORKER_SIZE / 2) - MARGIN, top = Math.min(0, label.y) - MARGIN;
  const right = Math.max(width, label.x + label.width, anchor.x + WORKER_SIZE / 2) + MARGIN, bottom = front + WORKER_SIZE / 2 + MARGIN;
  return { shape, width, height, anchor, label, footprint: { x: Math.floor(left), y: Math.floor(top), width: Math.ceil(right - Math.floor(left)), height: Math.ceil(bottom - Math.floor(top)) } };
}

type Rect = { x: number; y: number; width: number; height: number };
const overlaps = (a: SceneRect, b: SceneRect) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
const centre = (rect: SceneRect) => ({ x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 });
const union = (rects: readonly SceneRect[]) => {
  const x = Math.min(...rects.map((rect) => rect.x)), y = Math.min(...rects.map((rect) => rect.y));
  return { x, y, width: Math.max(...rects.map((rect) => rect.x + rect.width)) - x, height: Math.max(...rects.map((rect) => rect.y + rect.height)) - y };
};
type Links = ReadonlyMap<string, ReadonlyMap<string, number>>;

/** Rectangles in buckets, so finding an overlap costs the same however large the floor. */
function bucketed(pad = 0) {
  const SIZE = 160, buckets = new Map<number, SceneRect[]>();
  const grow = (rect: SceneRect) => pad === 0 ? rect : { x: rect.x - pad, y: rect.y - pad, width: rect.width + 2 * pad, height: rect.height + 2 * pad };
  const each = (rect: SceneRect, visit: (bucket: SceneRect[]) => void, make = false) => {
    for (let x = Math.floor(rect.x / SIZE); x <= Math.floor((rect.x + rect.width) / SIZE); x++) for (let y = Math.floor(rect.y / SIZE); y <= Math.floor((rect.y + rect.height) / SIZE); y++) {
      const key = x * 65536 + y, bucket = buckets.get(key) ?? (make ? [] : undefined);
      if (bucket === undefined) continue;
      if (make) buckets.set(key, bucket);
      visit(bucket);
    }
  };
  return {
    add: (rect: SceneRect) => each(grow(rect), (bucket) => bucket.push(rect), true),
    remove: (rect: SceneRect) => each(grow(rect), (bucket) => { const index = bucket.indexOf(rect); if (index >= 0) bucket.splice(index, 1); }),
    hits: (rect: SceneRect, counts: (other: SceneRect) => boolean = () => true) => { let hit = false; each(grow(rect), (bucket) => { hit ||= bucket.some((other) => overlaps(rect, grow(other)) && counts(other)); }); return hit; },
  };
}

function occupied(rects: readonly SceneRect[], pad = 0) {
  const index = bucketed(pad);
  for (const rect of rects) index.add(rect);
  return index.hits;
}

/** Every free spot touching a placed rectangle: each side at three alignments, and each corner. */
function around(rect: SceneRect, width: number, height: number, gap = 0) {
  const { x, y } = rect, r = x + rect.width + gap, b = y + rect.height + gap, l = x - width - gap, t = y - height - gap;
  const across = [x, x + (rect.width - width) / 2, x + rect.width - width], down = [y, y + (rect.height - height) / 2, y + rect.height - height];
  return [...down.flatMap((at) => [{ x: r, y: at }, { x: l, y: at }]), ...across.flatMap((at) => [{ x: at, y: b }, { x: at, y: t }]),
    { x: r, y: b }, { x: l, y: b }, { x: r, y: t }, { x: l, y: t }].map((point) => ({ x: Math.round(point.x), y: Math.round(point.y) }));
}

/** Footprints placed so far, with an overlap index and their bounds kept as they grow. */
function floorOf() {
  const placed = new Map<string, Rect>(), index = bucketed();
  let bounds: Rect | undefined;
  return {
    placed, index, bounds: () => bounds, work: 0,
    put(key: string, rect: Rect) { placed.set(key, rect); index.add(rect); bounds = bounds === undefined ? rect : union([bounds, rect]); },
    take(key: string) { const rect = placed.get(key)!; placed.delete(key); index.remove(rect); return rect; },
  };
}

/**
 * The free spot for one footprint: beside a placed neighbour, where the
 * weighted distance to its placed neighbours plus the growth of the bounds is
 * least. A spiral around them is the fallback, so placement always ends. A
 * current spot, when given, is kept unless another is clearly better.
 */
function placeOne(key: string, width: number, height: number, floor: ReturnType<typeof floorOf>, links: Links, current?: Rect): Rect {
  const bounds = floor.bounds();
  if (bounds === undefined) return current ?? { x: 0, y: 0, width, height };
  const near = [...(links.get(key) ?? [])].filter(([other]) => floor.placed.has(other)).map(([other, weight]) => ({ rect: floor.placed.get(other)!, weight }));
  const score = (spot: Rect) => {
    const at = centre(spot), grown = union([bounds, spot]);
    return near.reduce((sum, { rect, weight }) => { const other = centre(rect); return sum + weight * Math.hypot(at.x - other.x, at.y - other.y); }, 0)
      + 0.5 * (grown.width + grown.height - bounds.width - bounds.height);
  };
  let best = current === undefined ? undefined : { spot: current, score: score(current) - 1 };
  const consider = (spot: Rect) => {
    floor.work++;
    if (floor.index.hits(spot)) return;
    const value = score(spot);
    if (best === undefined || value < best.score - 1e-9 || Math.abs(value - best.score) <= 1e-9 && (spot.y < best.spot.y || spot.y === best.spot.y && spot.x < best.spot.x)) best = { spot, score: value };
  };
  for (const { rect } of near) for (const point of around(rect, width, height)) consider({ ...point, width, height });
  const from = near.length === 0 ? centre(bounds) : centre(union(near.map(({ rect }) => rect)));
  // Rings as coarse as the footprint is small: a crowded neighbourhood is searched in a few dozen rings, not hundreds.
  const pitch = Math.max(16, Math.round(Math.min(width, height) / 2));
  for (let ring = 1; best === undefined; ring++) {
    const reach = ring * pitch;
    for (let step = -reach; step <= reach; step += pitch) for (const [dx, dy] of [[step, -reach], [step, reach], [-reach, step], [reach, step]]) {
      consider({ x: Math.round(from.x - width / 2 + dx!), y: Math.round(from.y - height / 2 + dy!), width, height });
    }
  }
  return best.spot;
}

/** The order footprints are placed in: the most linked first, then whichever is most linked to those placed (ties by total links, then id). */
function placementOrder(keys: readonly string[], links: Links, placed: ReadonlySet<string> = new Set()) {
  const total = (key: string) => [...(links.get(key) ?? [])].reduce((sum, [, value]) => sum + value, 0);
  const left = [...keys].sort(compareText), toPlaced = new Map(left.map((key) => [key, [...(links.get(key) ?? [])].reduce((sum, [other, value]) => placed.has(other) ? sum + value : sum, 0)]));
  const totals = new Map(left.map((key) => [key, total(key)])), order: string[] = [];
  while (left.length > 0) {
    let pick = 0;
    for (let index = 1; index < left.length; index++) {
      const a = left[index]!, b = left[pick]!, byPlaced = toPlaced.get(a)! - toPlaced.get(b)!;
      if (byPlaced > 0 || byPlaced === 0 && totals.get(a)! > totals.get(b)!) pick = index;
    }
    const [key] = left.splice(pick, 1);
    order.push(key!);
    for (const [other, value] of links.get(key!) ?? []) if (toPlaced.has(other)) toPlaced.set(other, toPlaced.get(other)! + value);
  }
  return order;
}

/** A connected group laid out from nothing: placed in order, then two bounded passes that move a footprint only to a better free spot. */
function layoutGroup(keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  // Placement has a work cap in proportion to the links it has to honour (ordinary groups use a fifth of it); only a
  // runaway group, such as thousands of machines all linked to one hub, passes it. That group, or one that comes out a
  // strip (a chain), is laid in rows in placement order instead, so neighbours stay side by side and the shape stays
  // bounded. Refinement has its own cap of the same size and simply stops there.
  const floor = floorOf(), order = placementOrder(keys, links), cap = 4_000 + 200 * keys.reduce((sum, key) => sum + (links.get(key)?.size ?? 0), 0);
  for (const key of order) { const { width, height } = sizes.get(key)!; floor.put(key, placeOne(key, width, height, floor, links)); if (floor.work > cap) return rows(order, sizes); }
  floor.work = 0;
  for (let pass = 0; pass < 2 && order.length > 1 && floor.work <= cap; pass++) for (const key of order) {
    if (floor.work > cap) break;
    const current = floor.take(key);
    floor.put(key, placeOne(key, current.width, current.height, floor, links, current));
  }
  const bounds = floor.bounds()!;
  return bounds.width > 2.2 * bounds.height || bounds.height > 2.2 * bounds.width ? rows(order, sizes) : floor.placed;
}

/** Footprints in rows near 16:10, in order, each row running back the way the last came, so consecutive ones stay neighbours. */
function rows(order: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>) {
  const area = order.reduce((sum, key) => sum + sizes.get(key)!.width * sizes.get(key)!.height, 0), shelf = Math.ceil(Math.max(...order.map((key) => sizes.get(key)!.width), Math.sqrt(area * ASPECT)));
  const placed = new Map<string, Rect>();
  let row: string[] = [], y = 0, forward = true;
  const flush = () => {
    let x = forward ? 0 : shelf;
    for (const key of row) { const { width, height } = sizes.get(key)!; if (!forward) x -= width; placed.set(key, { x, y, width, height }); if (forward) x += width; }
    y += Math.max(...row.map((key) => sizes.get(key)!.height)); row = []; forward = !forward;
  };
  for (const key of order) { if (row.length > 0 && row.reduce((sum, item) => sum + sizes.get(item)!.width, 0) + sizes.get(key)!.width > shelf) flush(); row.push(key); }
  if (row.length > 0) flush();
  return placed;
}

/**
 * Groups on shelves, tallest first. The shelf width is the
 * one, among the widths the groups' running sums offer, that keeps the floor
 * smallest and nearest 16:10, whatever the pane.
 */
function packGroups(groups: readonly ReadonlyMap<string, Rect>[]) {
  if (groups.length === 0) return new Map<string, Rect>();
  const boxes = groups.map((group) => ({ group, bounds: union([...group.values()]) })).sort((a, b) => b.bounds.height - a.bounds.height || compareText([...a.group.keys()][0]!, [...b.group.keys()][0]!));
  const shelve = (shelf: number) => {
    const out = new Map<string, Rect>();
    let x = 0, y = 0, tallest = 0, wide = 0;
    for (const { group, bounds } of boxes) {
      if (x > 0 && x + bounds.width > shelf) { y += tallest + GROUP_GAP; x = 0; tallest = 0; }
      for (const [key, rect] of group) out.set(key, { ...rect, x: rect.x - bounds.x + x, y: rect.y - bounds.y + y });
      x += bounds.width + GROUP_GAP; tallest = Math.max(tallest, bounds.height); wide = Math.max(wide, x - GROUP_GAP);
    }
    const high = y + tallest;
    return { out, cost: wide * high * (1 + 4 * Math.abs(Math.log(wide / high / ASPECT))) };
  };
  let sum = 0;
  const widths = [...new Set([Math.max(...boxes.map(({ bounds }) => bounds.width)), ...boxes.map(({ bounds }) => sum += bounds.width + GROUP_GAP)])];
  // ponytail: one packing per candidate width, O(groups²); a binary search on width if floors reach thousands of groups.
  return widths.map(shelve).reduce((best, next) => next.cost < best.cost ? next : best).out;
}

type Piece = { key: string; machine: SceneMachine; unit?: string };

/** Ownership weighs most, then flows; a runtime-only machine leans only on the unit it claims. Traffic never enters. */
function linksOf(graph: SceneGraph, keys: ReadonlySet<string>) {
  const links = new Map<string, Map<string, number>>();
  const link = (a: string, b: string, weight: number) => {
    if (a === b || !keys.has(a) || !keys.has(b)) return;
    for (const [from, to] of [[a, b], [b, a]] as const) { const row = links.get(from) ?? new Map<string, number>(); row.set(to, (row.get(to) ?? 0) + weight); links.set(from, row); }
  };
  for (const unit of graph.units) for (const machine of unit.machines) link(machine.id, unit.id, 3);
  for (const machine of graph.quarantine) if (machine.claims !== undefined) link(machine.id, machine.claims, 1);
  for (const flow of graph.flows) link(flow.from, flow.to, 1);
  // Neighbours in id order, so sums and ties never depend on the order the graph arrived in.
  return new Map([...links].sort(([left], [right]) => compareText(left, right)).map(([key, row]) => [key, new Map([...row].sort(([left], [right]) => compareText(left, right)))]));
}

/** Connected groups by link, each named by its least key, largest first. */
function groupsOf(keys: readonly string[], links: Links) {
  const seen = new Set<string>(), groups: string[][] = [];
  for (const key of [...keys].sort(compareText)) {
    if (seen.has(key)) continue;
    const group: string[] = [], queue = [key];
    seen.add(key);
    while (queue.length > 0) { const next = queue.pop()!; group.push(next); for (const other of (links.get(next) ?? new Map()).keys()) if (!seen.has(other)) { seen.add(other); queue.push(other); } }
    groups.push(group.sort(compareText));
  }
  return groups.sort((left, right) => right.length - left.length || compareText(left[0]!, right[0]!));
}

/** The machinery's floor: the machines and the fixtures, which depend on the machines alone. */
export type SceneMachinery = Pick<SceneLayout, "width" | "height" | "stations" | "regions" | "fixtures">;

// A fixture is 20 by 30; its zone adds room to stand in front of it and a walkway round it.
const ZONE_W = 36, ZONE_H = 88;

/**
 * The floor, from structure alone: every machine at its drawn size, related
 * machines beside each other, unrelated groups apart on a floor near 16:10,
 * shared stores beside their users. Ids, kinds, labels, ownership and flows
 * decide it; traffic, state, workers, the agent count and the pane never do.
 * Given the previous floor, a small structural change keeps every machine
 * that still exists where it stood.
 */
export function placeMachinery(graph: SceneGraph, previous?: SceneMachinery): SceneMachinery {
  const pieces: Piece[] = [];
  for (const unit of graph.units) {
    pieces.push({ key: unit.id, machine: { id: unit.id, kind: "processor", label: unit.label, reading: unit.reading }, unit: unit.id });
    for (const machine of unit.machines) pieces.push({ key: machine.id, machine, unit: unit.id });
  }
  for (const machine of [...graph.shared, ...graph.parties, ...graph.quarantine]) pieces.push({ key: machine.id, machine });
  // Input order never matters: everything below works in id order.
  pieces.sort((left, right) => compareText(left.key, right.key));
  const geometry = new Map(pieces.map((piece) => [piece.key, sides(piece.machine)]));
  const sizes = new Map(pieces.map((piece) => [piece.key, geometry.get(piece.key)!.footprint] as const));
  const keys = [...sizes.keys()], links = linksOf(graph, new Set(keys));
  const placed = (previous === undefined ? undefined : keep(previous, keys, sizes, links)) ?? cold(keys, sizes, links);
  // Shift to the floor's corner; a shift moves everything together.
  const all = [...placed.values()], left = (all.length === 0 ? 0 : Math.min(...all.map((rect) => rect.x))) - PADDING, top = (all.length === 0 ? 0 : Math.min(...all.map((rect) => rect.y))) - PADDING;
  const at = (key: string) => { const rect = placed.get(key)!; return { ...rect, x: rect.x - left, y: rect.y - top }; };
  const stations = pieces.map((piece): SceneStation => {
    const { shape, width, height, anchor, label, footprint } = geometry.get(piece.key)!, spot = at(piece.key);
    const x = spot.x - footprint.x, y = spot.y - footprint.y, machine = piece.machine;
    return { entityId: piece.key, ...(machine.represented === undefined ? {} : { representedIds: machine.represented }), shape, machine,
      ...(piece.unit === undefined ? {} : { unit: piece.unit }), x, y, width, height, anchor: { x: x + anchor.x, y: y + anchor.y }, label: { ...label, x: x + label.x, y: y + label.y }, footprint: spot };
  });
  const width = Math.max(240, ...all.map((rect) => rect.x + rect.width)) - left + PADDING, height = Math.max(160, ...all.map((rect) => rect.y + rect.height)) - top + PADDING;
  const fixtures = placeFixtures(stations.map((station) => station.footprint), width, height);
  return { width, height: Math.max(height, ...fixtures.map((piece) => piece.y + ZONE_H + PADDING)), stations, regions: regionsOf(stations), fixtures };
}

/**
 * Each fixture in the free floor nearest the middle of the machinery, one after
 * another in a fixed order: between machines when there is a gap, below them when
 * there is none. Only the machines decide it, so nothing live ever moves one.
 */
function placeFixtures(blocks: readonly SceneRect[], width: number, height: number): readonly SceneFixture[] {
  const hits = occupied(blocks), zones: SceneRect[] = [], spots: ScenePoint[] = [];
  for (let y = PADDING; y <= height + NOOK_ORDER.length * ZONE_H; y += 12) for (let x = PADDING; x + ZONE_W <= width - PADDING; x += 12) spots.push({ x, y });
  const far = (spot: ScenePoint) => Math.hypot(spot.x + ZONE_W / 2 - width / 2, spot.y + ZONE_H / 2 - height / 2);
  spots.sort((a, b) => far(a) - far(b) || a.y - b.y || a.x - b.x);
  return NOOK_ORDER.map((errand) => {
    const spot = spots.find((at) => { const zone = { ...at, width: ZONE_W, height: ZONE_H }; return !hits(zone) && !zones.some((other) => overlaps(zone, other)); })!;
    zones.push({ ...spot, width: ZONE_W, height: ZONE_H });
    return { errand, x: spot.x + 8, y: spot.y + 8, stand: { x: spot.x + 18, y: spot.y + 74 } };
  });
}

/** The floor with the fixtures this floor shows; which they are never moves a machine or a fixture. */
export function furnish(machinery: SceneMachinery, offered: readonly BreakRoomErrand[]): SceneLayout {
  const present = NOOK_ORDER.filter((errand) => offered.includes(errand));
  const furniture = machinery.fixtures.filter((piece) => present.includes(piece.errand)).map((piece) => ({ x: piece.x, y: piece.y, width: 20, height: 30 }));
  return { ...machinery, implements: present, solids: [...machinery.stations, ...furniture].map(({ x, y, width, height }) => ({ x, y, width, height })) };
}

export function layoutScene(graph: SceneGraph, previous?: SceneMachinery, offered: readonly BreakRoomErrand[] = NOOK_ORDER): SceneLayout {
  return furnish(placeMachinery(graph, previous), offered);
}

function cold(keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  return packGroups(groupsOf(keys, links).map((group) => layoutGroup(group, sizes, links)));
}

/**
 * Local stability: footprints that still exist stay where they were and new
 * ones are placed beside their placed neighbours, or in the nearest free
 * floor when they have none. Undefined when the change is too large for that to stay readable, so
 * the floor is laid out cold.
 */
function keep(previous: SceneMachinery, keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  const before = new Map(previous.stations.map((station) => [station.entityId, station.footprint] as const));
  const floor = floorOf();
  for (const key of [...keys].sort(compareText)) {
    const was = before.get(key);
    if (was === undefined) continue;
    const { width, height } = sizes.get(key)!, spot = { x: was.x, y: was.y, width, height };
    // One that has grown into a neighbour is placed afresh beside its neighbours, like a new one.
    if (!floor.index.hits(spot)) floor.put(key, spot);
  }
  const fresh = keys.filter((key) => !floor.placed.has(key));
  if (fresh.length > Math.max(4, keys.length / 4)) return undefined;
  for (const key of placementOrder(fresh, links, new Set(floor.placed.keys()))) { const { width, height } = sizes.get(key)!; floor.put(key, placeOne(key, width, height, floor, links)); }
  return floor.placed;
}

// Members of a unit closer than this (two aisles) share one region; farther apart they are separate lobes.
const BRIDGE = 4 * MARGIN + 8, REGION_INSET = 0, REGION_CELL = 6;
// Area hues, far apart on the wheel; neighbouring areas never share one when the palette allows.
const HUES = [210, 35, 140, 290, 0, 180, 80, 330];

/**
 * Areas are painted after layout from where machines stand: a unit's members
 * whose footprints are next to each other merge into one irregular region,
 * the gaps between them bridged only where no other machine is in the way.
 * Members apart make separate lobes. No region covers another machine's
 * footprint or any floor that is not between its own members.
 */
function regionsOf(stations: readonly SceneStation[]): readonly SceneRegion[] {
  const inset = (station: SceneStation) => ({ x: station.footprint.x + REGION_INSET, y: station.footprint.y + REGION_INSET, width: station.footprint.width - 2 * REGION_INSET, height: station.footprint.height - 2 * REGION_INSET, unit: station.unit });
  const units = [...new Set(stations.flatMap((station) => station.unit ?? []))].sort(compareText), everyone = bucketed();
  for (const station of stations) everyone.add(inset(station));
  const byUnit = new Map(units.map((unit) => [unit, stations.filter((station) => station.unit === unit)]));
  const regions = units.flatMap((unit) => {
    const rects = byUnit.get(unit)!.map(inset), blocked = (rect: SceneRect) => everyone.hits(rect, (other) => (other as { unit?: string }).unit !== unit);
    const parent = rects.map((_, index) => index), find = (index: number): number => parent[index] === index ? index : parent[index] = find(parent[index]!);
    const bridges: { at: number; rect: SceneRect }[] = [];
    // Only members within bridging reach of each other are paired, found through buckets.
    const reach = bucketed(BRIDGE);
    rects.forEach((rect, index) => reach.add({ ...rect, index } as SceneRect));
    const neighbours = (i: number) => { const found: number[] = []; reach.hits(rects[i]!, (other) => { const j = (other as unknown as { index: number }).index; if (j > i) found.push(j); return false; }); return found; };
    for (let i = 0; i < rects.length; i++) for (const j of neighbours(i)) {
      const a = rects[i]!, b = rects[j]!;
      const x0 = Math.max(a.x, b.x), x1 = Math.min(a.x + a.width, b.x + b.width), y0 = Math.max(a.y, b.y), y1 = Math.min(a.y + a.height, b.y + b.height);
      // Face to face across a gap: the bridge is the span they share, as wide as the gap. Corner to corner: the band
      // between them, as wide as both, which meets each along an edge.
      // A bridge narrower than half an aisle would be a stray strip; corner bands only span a single aisle.
      const bridge = x1 - x0 >= MARGIN && y1 <= y0 && y0 - y1 <= BRIDGE ? { x: x0, y: y1, width: x1 - x0, height: y0 - y1 }
        : y1 - y0 >= MARGIN && x1 <= x0 && x0 - x1 <= BRIDGE ? { x: x1, y: y0, width: x0 - x1, height: y1 - y0 }
        : x1 <= x0 && y1 <= y0 && x0 - x1 <= 2 * MARGIN && y0 - y1 <= 2 * MARGIN ? { x: Math.min(a.x, b.x), y: y1, width: Math.max(a.x + a.width, b.x + b.width) - Math.min(a.x, b.x), height: y0 - y1 } : undefined;
      if (bridge === undefined || blocked(bridge)) continue;
      parent[find(i)] = find(j);
      bridges.push({ at: i, rect: bridge });
    }
    const lobes = new Map<number, SceneRect[]>();
    rects.forEach((rect, index) => lobes.set(find(index), [...lobes.get(find(index)) ?? [], rect]));
    for (const { at, rect } of bridges) lobes.get(find(at))!.push(rect);
    return [...lobes.values()].map((parts, lobe) => ({ unit, lobe, hue: 0, bounds: union(parts), ...trace(parts) }));
  });
  // Greedy colouring in unit order: each unit takes the first hue no neighbouring area (within an aisle) has.
  const hue = new Map<string, number>(), nearby = bucketed(24);
  for (const region of regions) nearby.add({ ...region.bounds, unit: region.unit } as SceneRect);
  for (const unit of units) {
    const taken = new Set<number>();
    for (const region of regions) if (region.unit === unit) nearby.hits(region.bounds, (other) => { const owner = (other as { unit?: string }).unit!; if (owner !== unit && hue.has(owner)) taken.add(hue.get(owner)!); return false; });
    hue.set(unit, HUES.find((value) => !taken.has(value)) ?? HUES[hue.size % HUES.length]!);
  }
  return regions.map(({ bounds: _, ...region }) => ({ ...region, hue: hue.get(region.unit)! }));
}

/** A patch of floor made of rectangles, as non-overlapping row runs, its outline, and the top-left corner for its name. */
function trace(parts: readonly SceneRect[]) {
  const bounds = union(parts), columns = Math.ceil(bounds.width / REGION_CELL), rows = Math.ceil(bounds.height / REGION_CELL);
  // A cell is in the patch when its centre is inside a part: each part marks its own cells.
  const mask = Array.from({ length: rows }, () => new Array<boolean>(columns).fill(false));
  for (const rect of parts) for (let row = Math.max(0, Math.floor((rect.y - bounds.y) / REGION_CELL - .5) + 1); row < rows && bounds.y + (row + .5) * REGION_CELL < rect.y + rect.height; row++)
    for (let column = Math.max(0, Math.floor((rect.x - bounds.x) / REGION_CELL - .5) + 1); column < columns && bounds.x + (column + .5) * REGION_CELL < rect.x + rect.width; column++) mask[row]![column] = true;
  const at = (column: number, row: number) => mask[row]?.[column] === true;
  const rects: SceneRect[] = [], edges: string[] = [];
  for (let row = 0; row < rows; row++) for (let column = 0; column < columns; column++) {
    if (!at(column, row)) continue;
    const x = bounds.x + column * REGION_CELL, y = bounds.y + row * REGION_CELL;
    if (!at(column - 1, row)) { let end = column; while (at(end + 1, row)) end++; rects.push({ x, y, width: (end - column + 1) * REGION_CELL, height: REGION_CELL }); }
    if (!at(column, row - 1)) edges.push(`M${x} ${y}h${REGION_CELL}`);
    if (!at(column, row + 1)) edges.push(`M${x} ${y + REGION_CELL}h${REGION_CELL}`);
    if (!at(column - 1, row)) edges.push(`M${x} ${y}v${REGION_CELL}`);
    if (!at(column + 1, row)) edges.push(`M${x + REGION_CELL} ${y}v${REGION_CELL}`);
  }
  const top = rects[0]!;
  return { rects, outline: edges.join(""), label: { x: top.x + 4, y: top.y + 10, width: top.width - 8 } };
}

/** Whether a worker standing here clears every solid. */
export function standable(layout: SceneLayout, point: ScenePoint) {
  const reach = WORKER_SIZE / 2;
  return layout.solids.every((rect) => point.x <= rect.x - reach || point.x >= rect.x + rect.width + reach || point.y <= rect.y - reach || point.y >= rect.y + rect.height + reach);
}

/** The station picturing a node: its own, or the fold that holds it. */
export function stationOf(layout: SceneLayout, id: string | undefined) {
  return id === undefined ? undefined : layout.stations.find((station) => station.entityId === id) ?? layout.stations.find((station) => station.representedIds?.includes(id));
}

/** A worker stands in front of the machine it works on; more stand beside them where the floor is clear. */
export function workPositions(layout: SceneLayout, station: SceneStation): readonly ScenePoint[] {
  const { x, y } = station.anchor;
  return [[0, 0], [-24, 0], [24, 0], [0, 24], [-24, 24], [24, 24]].map(([dx, dy]) => ({ x: x + dx!, y: y + dy! })).filter((point, index) => index === 0 || standable(layout, point));
}

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[]): readonly SceneWorkerPlacement[] {
  const sorted = [...workers].sort((left, right) => compareText(left.id, right.id));
  const placed: SceneWorkerPlacement[] = [];
  // ponytail: a bounded actor list uses linear collision checks; use spatial buckets if thousands are displayed.
  // Nobody stands where a visitor to a fixture would.
  const occupied = (point: ScenePoint) => [...placed, ...layout.fixtures.map((piece) => piece.stand)].some((other) => Math.abs(point.x - other.x) < 24 && Math.abs(point.y - other.y) < 24);
  const resting: { worker: SceneWorker; area: "resting" | "staging" | "outside" | "overflow" }[] = [];
  const homeOf = (worker: SceneWorker) => stationOf(layout, worker.observedBayId) ?? stationOf(layout, worker.nodeId);
  for (const worker of sorted) {
    if (worker.location !== "working") { resting.push({ worker, area: worker.location === "unobserved" ? "staging" : "resting" }); continue; }
    const station = homeOf(worker);
    if (station === undefined) { resting.push({ worker, area: "outside" }); continue; }
    const position = workPositions(layout, station).find((point) => !occupied(point));
    if (position === undefined) { resting.push({ worker, area: "overflow" }); continue; }
    placed.push({ id: worker.id, area: "work", stationId: station.entityId, ...position });
  }
  // Anyone not at work stands beside a machine: the one they last worked at, else beside someone who is alone, else
  // the first free one. Rows fan out beside and then below it for as long as there are people; nobody is turned away.
  const homes = layout.stations.length > 0 ? layout.stations.map((station) => ({ id: station.entityId as string | undefined, ...station.anchor })) : [{ id: undefined, x: 2 * PADDING + 8, y: PADDING }];
  const crowd = new Map<string | undefined, number>();
  const beside = (home: ScenePoint) => {
    for (let row = 1; ; row++) for (let step = 0; step < 3; step++) for (const side of [-1, 1]) {
      const point = { x: Math.min(layout.width - PADDING, Math.max(PADDING, home.x + side * (24 + 48 * step))), y: home.y + 24 * row };
      if (!occupied(point) && standable(layout, point)) return point;
    }
  };
  // Those with a machine take their places first.
  resting.sort((left, right) => Number(homeOf(right.worker) !== undefined) - Number(homeOf(left.worker) !== undefined) || compareText(left.worker.id, right.worker.id));
  for (const { worker, area } of resting) {
    const station = homeOf(worker), home = station === undefined ? (homes.find((next) => crowd.get(next.id) === 1) ?? homes.find((next) => !crowd.has(next.id)) ?? homes[0]!) : { id: station.entityId as string | undefined, ...station.anchor };
    crowd.set(home.id, (crowd.get(home.id) ?? 0) + 1);
    placed.push({ id: worker.id, area, ...(home.id === undefined ? {} : { stationId: home.id }), ...beside(home)! });
  }
  return placed.sort((left, right) => compareText(left.id, right.id));
}

// Left to right along the board wall: the order the five are placed in.
const NOOK_ORDER: readonly BreakRoomErrand[] = ["board", "missions", "tasks", "shelf", "coffee"];

/**
 * Stable furniture, never moved by data.
 * Ambient turns come first in the list, so their timing never depends on the implements beside them.
 */
export function breakRoomNook(layout: SceneLayout) {
  return { furniture: (["shelf", "coffee", "board", "missions", "tasks"] as const).flatMap((errand) => { const piece = layout.fixtures.find((item) => item.errand === errand); return piece === undefined || !layout.implements.includes(errand) ? [] : [{ ...piece, key: String(errand) }]; }) };
}

// Each piece of furniture is visited in turns: free for the first part of a turn, then one visitor.
const TURN = 24000, FREE = 8000;

/**
 * Turns at the furniture are fair, and a visit ends only when its turn does or its visitor
 * stops resting — given the placements it last returned as `before`: for each piece, each
 * turn belongs to one of the seated workers whose habit it suits, drawn afresh
 * every turn, and to nobody when there are few of them, so a lone reader is not
 * forever on their feet. The pieces are out of step, and every turn starts free,
 * so a floor that has just loaded stays seated for a while.
 */
export function placeErrands(placements: readonly SceneWorkerPlacement[], nook: ReturnType<typeof breakRoomNook>, habitOf: (id: string) => BreakRoomErrand | undefined, at: number, before: readonly SceneWorkerPlacement[] = []): readonly SceneWorkerPlacement[] {
  if (nook === undefined) return placements;
  const visiting = new Map<string, (typeof nook.furniture)[number]>();
  for (const [index, piece] of nook.furniture.entries()) {
    // Later pieces run behind the first, so every piece's first turn begins free.
    const clock = at - index * TURN / 2, turn = Math.floor(clock / TURN);
    const suited = placements.filter((placement) => placement.area === "resting" && habitOf(placement.id) === piece.errand);
    // Whoever has the piece keeps it for as long as they rest and the turn runs, whoever else
    // sits down or leaves meanwhile. (Every turn starts free, so nobody is carried into the next.)
    const holder = before.find((placement) => placement.errandKey === piece.key || placement.errandKey === undefined && placement.errand === piece.errand)?.id;
    const visitor = clock < 0 || clock - turn * TURN < FREE ? undefined : suited.find((placement) => placement.id === holder) ?? suited[(Math.imul(turn + 1, 2654435761) >>> 0) % Math.max(suited.length, 4)];
    if (visitor !== undefined) visiting.set(visitor.id, piece);
  }
  return placements.map((placement) => { const piece = visiting.get(placement.id); return piece === undefined ? placement : { ...placement, errand: piece.errand, errandKey: piece.key, ...piece.stand }; });
}
