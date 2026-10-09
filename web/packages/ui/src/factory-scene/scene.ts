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
export const LINE_SLOTS = 8;

export type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;

/** How a machine is pictured. A shape is chosen from the node kind, never its name. */
export type StationShape = "line" | "dock" | "manifold" | "clock" | "cell" | "silo" | "conveyor" | "crate" | "gate";

/** One machine where it stands: its body is solid, its label and the spot in front of it are floor. */
export type SceneStation = SceneRect & Readonly<{
  key: string;
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
  /** The development neighbourhood: the commons and the outbound line, placed as one block. */
  facilities: SceneRect;
  commons: SceneRect;
  restingTop: number;
  /** Seats per table row and rows of each kind, from the number of agents; and the implements actually there. */
  seats: Readonly<{ agents: number; perRow: number; rows: number; planningRows: number }>;
  implements: readonly BreakRoomErrand[];
  /** The outbound line, one row per station. Fixed: it is there, empty, with no work. */
  line: readonly (SceneRect & Readonly<{ label: string }>)[];
  /** What nobody walks through: machine bodies, commons tables and implements. */
  solids: readonly SceneRect[];
}>;

/**
 * One neighbourhood of a unit: members standing next to each other, merged
 * into one irregular patch of floor. `rects` tile it without overlapping;
 * `outline` traces its edge; `label` is where its one name goes.
 */
export type SceneRegion = Readonly<{ unit: string; lobe: number; rects: readonly SceneRect[]; outline: string; label: ScenePoint & Readonly<{ width: number }> }>;

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

export const PADDING = 16;
/** What the commons holds: a seat for every agent at rest and at the work tables, and the implements the floor offers. */
export type CommonsNeeds = Readonly<{ agents: number; implements: readonly BreakRoomErrand[] }>;
export const WORKER_GAP = 40;
export const WORKER_SIZE = 20;
/** Half an aisle around every footprint: two neighbours leave a worker room to pass with the walking grid's slack. */
export const MARGIN = 18;
// The commons, sized to what is in it: implements along the top, two rows of four seats, a planning row; the
// outbound line right below it. Rows of seats are further apart than seats in a row, so the aisle behind a table is always walkable.
const ROW_PITCH = 56, LINE_ROW = 28, LINE_GAP = 8, LINE_WIDTH = 176;
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
  const anchor = { x: width / 2, y: Math.max(height + 16, label.y + label.height + 13) };
  const left = Math.min(0, label.x, anchor.x - WORKER_SIZE / 2) - MARGIN, top = Math.min(0, label.y) - MARGIN;
  const right = Math.max(width, label.x + label.width, anchor.x + WORKER_SIZE / 2) + MARGIN, bottom = anchor.y + WORKER_SIZE / 2 + MARGIN;
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

/** Whether a rectangle overlaps any of these (grown by `pad`): bucketed, so a check costs the same however large the floor. */
function occupied(rects: readonly SceneRect[], pad = 0) {
  const SIZE = 160, buckets = new Map<number, SceneRect[]>();
  const each = (rect: SceneRect, visit: (key: number) => void) => {
    for (let x = Math.floor(rect.x / SIZE); x <= Math.floor((rect.x + rect.width) / SIZE); x++) for (let y = Math.floor(rect.y / SIZE); y <= Math.floor((rect.y + rect.height) / SIZE); y++) visit(x * 65536 + y);
  };
  for (const rect of rects) { const grown = { x: rect.x - pad, y: rect.y - pad, width: rect.width + 2 * pad, height: rect.height + 2 * pad }; each(grown, (key) => { const bucket = buckets.get(key); if (bucket === undefined) buckets.set(key, [grown]); else bucket.push(grown); }); }
  return (rect: SceneRect) => { let hit = false; each(rect, (key) => { hit ||= (buckets.get(key) ?? []).some((other) => overlaps(rect, other)); }); return hit; };
}

/** Every free spot touching a placed rectangle: each side at three alignments, and each corner. */
function around(rect: SceneRect, width: number, height: number, gap = 0) {
  const { x, y } = rect, r = x + rect.width + gap, b = y + rect.height + gap, l = x - width - gap, t = y - height - gap;
  const across = [x, x + (rect.width - width) / 2, x + rect.width - width], down = [y, y + (rect.height - height) / 2, y + rect.height - height];
  return [...down.flatMap((at) => [{ x: r, y: at }, { x: l, y: at }]), ...across.flatMap((at) => [{ x: at, y: b }, { x: at, y: t }]),
    { x: r, y: b }, { x: l, y: b }, { x: r, y: t }, { x: l, y: t }].map((point) => ({ x: Math.round(point.x), y: Math.round(point.y) }));
}

/**
 * The free spot for one footprint: beside a placed neighbour, where the
 * weighted distance to its placed neighbours plus the growth of the bounds is
 * least. A spiral around them is the fallback, so placement always ends. A
 * current spot, when given, is kept unless another is clearly better.
 */
function placeOne(key: string, width: number, height: number, placed: ReadonlyMap<string, Rect>, links: Links, current?: Rect): Rect {
  if (placed.size === 0) return current ?? { x: 0, y: 0, width, height };
  const near = [...(links.get(key) ?? [])].filter(([other]) => placed.has(other)).map(([other, weight]) => ({ rect: placed.get(other)!, weight }));
  const others = [...placed.values()], bounds = union(others), taken = occupied(others);
  const score = (spot: Rect) => {
    const at = centre(spot), grown = union([bounds, spot]);
    return near.reduce((sum, { rect, weight }) => { const other = centre(rect); return sum + weight * Math.hypot(at.x - other.x, at.y - other.y); }, 0)
      + 0.5 * (grown.width + grown.height - bounds.width - bounds.height);
  };
  let best = current === undefined ? undefined : { spot: current, score: score(current) - 1 };
  const consider = (spot: Rect) => {
    if (taken(spot)) return;
    const value = score(spot);
    if (best === undefined || value < best.score - 1e-9 || Math.abs(value - best.score) <= 1e-9 && (spot.y < best.spot.y || spot.y === best.spot.y && spot.x < best.spot.x)) best = { spot, score: value };
  };
  for (const { rect } of near) for (const point of around(rect, width, height)) consider({ ...point, width, height });
  const from = near.length === 0 ? centre(bounds) : centre(union(near.map(({ rect }) => rect)));
  for (let ring = 1; best === undefined; ring++) {
    const reach = ring * 16;
    for (let step = -reach; step <= reach; step += 16) for (const [dx, dy] of [[step, -reach], [step, reach], [-reach, step], [reach, step]]) {
      consider({ x: Math.round(from.x - width / 2 + dx!), y: Math.round(from.y - height / 2 + dy!), width, height });
    }
  }
  return best.spot;
}

/**
 * The order a connected group is placed in: its most linked footprint, then whichever is most linked to those placed.
 * ponytail: rescans what is left each step, O(n² × links); keep running weights if floors reach thousands of stations.
 */
function placementOrder(keys: readonly string[], links: Links, placed: ReadonlySet<string> = new Set()) {
  const weight = (key: string, among: ReadonlySet<string> | undefined) => [...(links.get(key) ?? [])].reduce((sum, [other, value]) => among === undefined || among.has(other) ? sum + value : sum, 0);
  const done = new Set(placed), order: string[] = [], left = [...keys].sort(compareText);
  while (left.length > 0) {
    let pick = 0;
    for (let index = 1; index < left.length; index++) {
      const a = left[index]!, b = left[pick]!, byPlaced = weight(a, done) - weight(b, done);
      if (byPlaced > 0 || byPlaced === 0 && weight(a, undefined) > weight(b, undefined)) pick = index;
    }
    const [key] = left.splice(pick, 1);
    order.push(key!);
    done.add(key!);
  }
  return order;
}

/** A connected group laid out from nothing: placed in order, then two bounded passes that move a footprint only to a better free spot. */
function layoutGroup(keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  const placed = new Map<string, Rect>(), order = placementOrder(keys, links);
  for (const key of order) { const { width, height } = sizes.get(key)!; placed.set(key, placeOne(key, width, height, placed, links)); }
  for (let pass = 0; pass < 2 && order.length > 1; pass++) for (const key of order) {
    const current = placed.get(key)!;
    placed.delete(key);
    placed.set(key, placeOne(key, current.width, current.height, placed, links, current));
  }
  return placed;
}

/**
 * Where a group goes beside those placed: touching a placed footprint, a gap
 * clear of every other group's footprints (so it can tuck into a corner
 * another group leaves), where the floor stays smallest and nearest its usual
 * shape.
 */
function besideGroups(placed: readonly Rect[], group: readonly Rect[]) {
  const bounds = union(group);
  if (placed.length === 0) return { x: bounds.x, y: bounds.y };
  const taken = occupied(placed, GROUP_GAP - 1), floor = union(placed);
  let best: { x: number; y: number; cost: number } | undefined;
  // ponytail: every placed footprint anchors every footprint of the group: O(placed × group) per group, fine to a few thousand stations.
  for (const anchor of placed) for (const piece of group) for (const spot of around(anchor, piece.width, piece.height, GROUP_GAP)) {
    const dx = spot.x - piece.x, dy = spot.y - piece.y;
    if (group.some((rect) => taken({ ...rect, x: rect.x + dx, y: rect.y + dy }))) continue;
    const all = union([floor, { ...bounds, x: bounds.x + dx, y: bounds.y + dy }]), cost = all.width * all.height * (1 + 0.5 * Math.abs(Math.log(all.width / all.height / ASPECT)));
    const x = bounds.x + dx, y = bounds.y + dy;
    if (best === undefined || cost < best.cost - 1e-6 || Math.abs(cost - best.cost) <= 1e-6 && (y < best.y || y === best.y && x < best.x)) best = { x, y, cost };
  }
  return best!;
}

/** Groups packed in order, each beside the placed ones. */
function packGroups(groups: readonly ReadonlyMap<string, Rect>[]) {
  const out = new Map<string, Rect>();
  for (const group of groups) {
    const rects = [...group.values()], bounds = union(rects), spot = besideGroups([...out.values()], rects);
    for (const [key, rect] of group) out.set(key, { ...rect, x: rect.x + spot.x - bounds.x, y: rect.y + spot.y - bounds.y });
  }
  return out;
}

type Piece = { key: string; machine: SceneMachine; unit?: string };
const FACILITIES = "facilities";

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
  return links;
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

/**
 * The floor, from structure alone: every machine at its drawn size, related
 * machines beside each other, unrelated groups apart, shared stores beside
 * their users. Ids, kinds, labels, ownership and flows decide it; traffic,
 * state, workers and the pane never do. Given the previous layout, a small
 * structural change keeps every machine that still exists where it stood.
 */
export function layoutScene(graph: SceneGraph, previous?: SceneLayout, needs: CommonsNeeds = { agents: 8, implements: NOOK_ORDER }): SceneLayout {
  const pieces: Piece[] = [];
  for (const unit of graph.units) {
    pieces.push({ key: unit.id, machine: { id: unit.id, kind: "processor", label: unit.label, reading: unit.reading }, unit: unit.id });
    for (const machine of unit.machines) pieces.push({ key: machine.id, machine, unit: unit.id });
  }
  for (const machine of [...graph.shared, ...graph.parties, ...graph.quarantine]) pieces.push({ key: machine.id, machine });
  // Input order never matters: everything below works in id order.
  pieces.sort((left, right) => compareText(left.key, right.key));
  // The commons is as big as what is in it: tables for the agents, the implements present. A different agent count is a
  // structural change for the commons alone; with the previous layout it resizes in place and nothing else moves.
  const present = NOOK_ORDER.filter((errand) => needs.implements.includes(errand)), perRow = Math.min(4, Math.max(1, needs.agents)), rows = Math.max(1, Math.ceil(needs.agents / perRow));
  // Every agent can rest at once; at most half are ever planning away from a machine at once.
  const planningRows = Math.max(1, Math.ceil(Math.ceil(needs.agents / 2) / perRow));
  const restingAt = present.length > 0 ? 116 : 40, commonHeight = restingAt + (rows + planningRows - 1) * ROW_PITCH + 30;
  const commonWidth = Math.max(present.length * 38 + 8, (perRow - 1) * WORKER_GAP + 48, 96);
  const facility = { width: Math.max(commonWidth, LINE_WIDTH + 8) + 2 * MARGIN, height: commonHeight + LINE_GAP + LINE_ROW * LINE_STATIONS.length + 2 * MARGIN };
  const geometry = new Map(pieces.map((piece) => [piece.key, sides(piece.machine)]));
  const sizes = new Map([...pieces.map((piece) => [piece.key, geometry.get(piece.key)!.footprint] as const), [FACILITIES, facility] as const]);
  const keys = [...sizes.keys()];
  const links = linksOf(graph, new Set(keys));
  // A floor laid out before any agent was known (the console's first render) is no history to keep.
  const placed = (previous === undefined || previous.seats.agents === 0 ? undefined : keep(previous, keys, sizes, links)) ?? cold(keys, sizes, links);
  // Shift to the floor's corner; a shift moves everything together.
  const all = [...placed.values()], left = Math.min(...all.map((rect) => rect.x)) - PADDING, top = Math.min(...all.map((rect) => rect.y)) - PADDING;
  const at = (key: string) => { const rect = placed.get(key)!; return { ...rect, x: rect.x - left, y: rect.y - top }; };
  const stations = pieces.map((piece): SceneStation => {
    const { shape, width, height, anchor, label, footprint } = geometry.get(piece.key)!, spot = at(piece.key);
    const x = spot.x - footprint.x, y = spot.y - footprint.y, machine = piece.machine;
    return { key: piece.key, entityId: piece.key, ...(machine.represented === undefined ? {} : { representedIds: machine.represented }), shape, machine,
      ...(piece.unit === undefined ? {} : { unit: piece.unit }), x, y, width, height, anchor: { x: x + anchor.x, y: y + anchor.y }, label: { ...label, x: x + label.x, y: y + label.y }, footprint: spot };
  });
  const block = at(FACILITIES), commons = { x: block.x + MARGIN, y: block.y + MARGIN, width: commonWidth, height: commonHeight };
  const line = LINE_STATIONS.map((label, index) => ({ label, x: commons.x + 8, y: commons.y + commonHeight + LINE_GAP + index * LINE_ROW, width: LINE_WIDTH, height: LINE_ROW - 4 }));
  const restingTop = commons.y + restingAt, seats = { agents: needs.agents, perRow, rows, planningRows };
  const regions = regionsOf(stations);
  const width = Math.max(...[...placed.keys()].map((key) => at(key).x + at(key).width)) + PADDING;
  const height = Math.max(...[...placed.keys()].map((key) => at(key).y + at(key).height)) + PADDING;
  const furniture = [...tables({ commons, restingTop, seats }), ...nookPieces(commons, present).map((piece) => ({ x: piece.x, y: piece.y, width: 20, height: 30 }))];
  return { width, height, stations, regions, facilities: block, commons, restingTop, seats, implements: present, line, solids: [...stations, ...furniture].map(({ x, y, width, height }) => ({ x, y, width, height })) };
}

function cold(keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  const groups = groupsOf(keys, links).map((group) => layoutGroup(group, sizes, links));
  // The facilities go second, beside the largest group, which is where most work happens.
  const facilities = groups.findIndex((group) => group.has(FACILITIES));
  const [first, ...rest] = groups.filter((_, index) => index !== facilities);
  return packGroups([...(first === undefined ? [] : [first]), groups[facilities]!, ...rest]);
}

/**
 * Local stability: footprints that still exist stay where they were and new
 * ones are placed beside their placed neighbours, or beside the floor when
 * they have none. Undefined when the change is too large for that to stay
 * readable, so the floor is laid out cold.
 */
function keep(previous: SceneLayout, keys: readonly string[], sizes: ReadonlyMap<string, { width: number; height: number }>, links: Links) {
  const before = new Map<string, SceneRect>([...previous.stations.map((station) => [station.key, station.footprint] as const), [FACILITIES, previous.facilities]]);
  const placed = new Map<string, Rect>();
  for (const key of [...keys].sort(compareText)) {
    const was = before.get(key);
    if (was === undefined) continue;
    const { width, height } = sizes.get(key)!, spot = { x: was.x, y: was.y, width, height };
    // One that has grown into a neighbour is placed afresh beside its neighbours, like a new one.
    if (![...placed.values()].some((rect) => overlaps(rect, spot))) placed.set(key, spot);
  }
  const fresh = keys.filter((key) => !placed.has(key));
  if (fresh.length > Math.max(4, keys.length / 4)) return undefined;
  for (const key of placementOrder(fresh, links, new Set(placed.keys()))) {
    const { width, height } = sizes.get(key)!;
    const linked = [...(links.get(key) ?? [])].some(([other]) => placed.has(other));
    // A new group of its own starts beside the floor, apart from it.
    placed.set(key, linked ? placeOne(key, width, height, placed, links) : { ...besideGroups([...placed.values()], [{ x: 0, y: 0, width, height }]), width, height });
  }
  return placed;
}

// Members of a unit closer than this (about one aisle and a bit) share one region; farther apart they are separate lobes.
const BRIDGE = 2 * MARGIN + 16, REGION_INSET = 4, REGION_CELL = 6;

/**
 * Areas are painted after layout from where machines stand: a unit's members
 * whose footprints are next to each other merge into one irregular region,
 * the gaps between them bridged only where no other machine is in the way.
 * Members apart make separate lobes. No region covers another machine's
 * footprint or any floor that is not between its own members.
 */
function regionsOf(stations: readonly SceneStation[]): readonly SceneRegion[] {
  const inset = (rect: SceneRect) => ({ x: rect.x + REGION_INSET, y: rect.y + REGION_INSET, width: rect.width - 2 * REGION_INSET, height: rect.height - 2 * REGION_INSET });
  const units = [...new Set(stations.flatMap((station) => station.unit ?? []))].sort(compareText);
  return units.flatMap((unit) => {
    const members = stations.filter((station) => station.unit === unit), rects = members.map((station) => inset(station.footprint));
    const blocked = occupied(stations.filter((station) => station.unit !== unit).map((station) => inset(station.footprint)));
    const parent = rects.map((_, index) => index), find = (index: number): number => parent[index] === index ? index : parent[index] = find(parent[index]!);
    const bridges: { at: number; rect: SceneRect }[] = [];
    for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) {
      const a = rects[i]!, b = rects[j]!;
      const x0 = Math.max(a.x, b.x), x1 = Math.min(a.x + a.width, b.x + b.width), y0 = Math.max(a.y, b.y), y1 = Math.min(a.y + a.height, b.y + b.height);
      // Face to face across a gap: the bridge is the span they share, as wide as the gap.
      const bridge = x1 - x0 >= 12 && y1 < y0 && y0 - y1 <= BRIDGE ? { x: x0, y: y1, width: x1 - x0, height: y0 - y1 }
        : y1 - y0 >= 12 && x1 < x0 && x0 - x1 <= BRIDGE ? { x: x1, y: y0, width: x0 - x1, height: y1 - y0 } : undefined;
      if (bridge === undefined || blocked(bridge)) continue;
      parent[find(i)] = find(j);
      bridges.push({ at: i, rect: bridge });
    }
    const lobes = new Map<number, SceneRect[]>();
    rects.forEach((rect, index) => lobes.set(find(index), [...lobes.get(find(index)) ?? [], rect]));
    for (const { at, rect } of bridges) lobes.get(find(at))!.push(rect);
    return [...lobes.values()].map((parts, lobe) => ({ unit, lobe, ...trace(parts) }));
  });
}

/** A patch of floor made of rectangles, as non-overlapping row runs, its outline, and the top-left corner for its name. */
function trace(parts: readonly SceneRect[]) {
  const bounds = union(parts), columns = Math.ceil(bounds.width / REGION_CELL), rows = Math.ceil(bounds.height / REGION_CELL);
  const filled = (column: number, row: number) => column >= 0 && row >= 0 && column < columns && row < rows
    && parts.some((rect) => { const x = bounds.x + (column + .5) * REGION_CELL, y = bounds.y + (row + .5) * REGION_CELL; return x > rect.x && x < rect.x + rect.width && y > rect.y && y < rect.y + rect.height; });
  const mask = Array.from({ length: rows }, (_, row) => Array.from({ length: columns }, (_, column) => filled(column, row)));
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

/** The furniture in the commons: left to right along its back wall. */
function nookPieces(commons: SceneRect, present: readonly BreakRoomErrand[]) {
  return present.map((errand, index) => ({ errand, x: commons.x + 6 + index * 38, y: commons.y + 18, stand: { x: commons.x + 12 + index * 38, y: commons.y + 84 } }));
}

/** The commons tables, each with the bench its seats are on: one per row of seats, always there, and solid. Nobody walks over a sitter. */
function tables(layout: Pick<SceneLayout, "commons" | "restingTop" | "seats">) {
  const { rows, perRow, planningRows } = layout.seats, top = planningTop(layout);
  return [...Array.from({ length: rows }, (_, row) => layout.restingTop + row * ROW_PITCH), ...Array.from({ length: planningRows }, (_, row) => top + row * ROW_PITCH)]
    .map((y) => ({ x: layout.commons.x + 6, y: y + 3, width: (perRow - 1) * WORKER_GAP + 36, height: 15 }));
}

const planningTop = (layout: Pick<SceneLayout, "restingTop" | "seats">) => layout.restingTop + layout.seats.rows * ROW_PITCH;

/** The tables drawn in the commons, as rows of seat positions. */
export function commonTables(layout: SceneLayout) {
  return tables(layout).map((table, index) => ({ ...table, planning: index >= layout.seats.rows }));
}

/** Newest first within a station; past the eighth, crates wait unseen behind the "+N". */
export function placeCrates(layout: SceneLayout, crates: readonly SceneCrate[]) {
  const counts = [0, 0, 0, 0] as [number, number, number, number];
  return [...crates].sort((left, right) => right.number - left.number || compareText(left.id, right.id)).map((crate) => {
    const station = layout.line[crate.station]!, slot = counts[crate.station]++;
    return { crate, shown: slot < LINE_SLOTS, x: station.x + 16 + Math.min(slot, LINE_SLOTS) * 18, y: station.y + 18 };
  });
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

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[], social: "nearby" | "commons" = "commons"): readonly SceneWorkerPlacement[] {
  const sorted = [...workers].sort((left, right) => compareText(left.id, right.id));
  const placed: SceneWorkerPlacement[] = [];
  // ponytail: a bounded actor list uses linear collision checks; use spatial buckets if thousands are displayed.
  const occupied = (point: ScenePoint) => placed.some((other) => Math.abs(point.x - other.x) < 24 && Math.abs(point.y - other.y) < 24);
  const areas: Record<"resting" | "staging" | "outside" | "overflow", SceneWorker[]> = { resting: [], staging: [], outside: [], overflow: [] };
  for (const worker of sorted) {
    if (worker.location !== "working") {
      areas[worker.location === "unobserved" ? "staging" : "resting"].push(worker);
      continue;
    }
    const station = stationOf(layout, worker.observedBayId) ?? stationOf(layout, worker.nodeId);
    if (station === undefined) { areas.outside.push(worker); continue; }
    const position = workPositions(layout, station).find((point) => !occupied(point));
    if (position === undefined) { areas.overflow.push(worker); continue; }
    placed.push({ id: worker.id, area: "work", stationId: station.key, ...position });
  }
  // Resting nearby is beside a unit's main machine.
  const nearby = social === "nearby" ? layout.stations.filter((station) => station.unit === station.key) : [];
  const localCounts = new Map<string, number>();
  const commons: SceneWorker[] = [];
  // Known locations keep their seats before unlocated idle workers join a nearby pair.
  const resting = areas.resting.sort((left, right) => Number(nearby.some((station) => station.key === right.nodeId)) - Number(nearby.some((station) => station.key === left.nodeId)) || compareText(left.id, right.id));
  for (const worker of resting) {
    const known = nearby.find((station) => station.key === worker.nodeId);
    const candidates = known ? [known] : [...nearby.filter((station) => localCounts.get(station.key) === 1), ...nearby.slice(0, 3)];
    const seat = candidates.filter((station) => (localCounts.get(station.key) ?? 0) < 2)
      .flatMap((station) => [-24, 24].map((offset) => ({ stationId: station.key, x: station.anchor.x + offset, y: station.anchor.y + 24 })))
      .find((point) => !occupied(point) && standable(layout, point));
    if (seat === undefined) { commons.push(worker); continue; }
    placed.push({ id: worker.id, area: "resting", ...seat });
    localCounts.set(seat.stationId, (localCounts.get(seat.stationId) ?? 0) + 1);
  }
  const planning = (["staging", "outside", "overflow"] as const).flatMap((area) => areas[area].map((worker) => ({ worker, area })));
  const seats = commonSeating(layout, commons.length, planning.length);
  commons.forEach((worker, slot) => placed.push({ id: worker.id, area: "resting", ...seats.resting[slot]! }));
  planning.forEach(({ worker, area }, slot) => placed.push({ id: worker.id, area, ...seats.planning[slot]! }));
  return placed.sort((left, right) => compareText(left.id, right.id));
}

/**
 * The commons seats every agent at rest and half of them at the work tables, up to four to a table.
 * Anyone more (a reviewer passing through) sits on a bench below the floor, so they never move a machine.
 */
export function commonSeating(layout: SceneLayout, restingCount: number, planningCount: number) {
  const { perRow, rows, planningRows } = layout.seats, row = (slot: number, top: number) => ({ x: layout.commons.x + 24 + (slot % perRow) * WORKER_GAP, y: top + Math.floor(slot / perRow) * ROW_PITCH });
  let bench = 0;
  const benches = () => { const slot = bench++; return { x: PADDING + 24 + (slot % 4) * WORKER_GAP, y: layout.height + 24 + Math.floor(slot / 4) * ROW_PITCH }; };
  const resting = Array.from({ length: restingCount }, (_, slot) => slot < perRow * rows ? row(slot, layout.restingTop) : benches());
  const planning = Array.from({ length: planningCount }, (_, slot) => slot < perRow * planningRows ? row(slot, planningTop(layout)) : benches());
  return { resting, planning };
}

// Left to right along the break room's back wall.
const NOOK_ORDER: readonly BreakRoomErrand[] = ["board", "missions", "tasks", "shelf", "coffee"];

/**
 * Stable furniture in the commons, never moved by data.
 * Ambient turns come first in the list, so their timing never depends on the implements beside them.
 */
export function breakRoomNook(layout: SceneLayout) {
  const pieces = nookPieces(layout.commons, layout.implements);
  return { width: layout.commons.width, furniture: (["shelf", "coffee", "board", "missions", "tasks"] as const).flatMap((errand) => { const piece = pieces.find((item) => item.errand === errand); return piece === undefined ? [] : [{ ...piece, key: String(errand) }]; }) };
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
    const suited = placements.filter((placement) => placement.area === "resting" && placement.stationId === undefined && habitOf(placement.id) === piece.errand);
    // Whoever has the piece keeps it for as long as they rest and the turn runs, whoever else
    // sits down or leaves meanwhile. (Every turn starts free, so nobody is carried into the next.)
    const holder = before.find((placement) => placement.errandKey === piece.key || placement.errandKey === undefined && placement.errand === piece.errand)?.id;
    const visitor = clock < 0 || clock - turn * TURN < FREE ? undefined : suited.find((placement) => placement.id === holder) ?? suited[(Math.imul(turn + 1, 2654435761) >>> 0) % Math.max(suited.length, 4)];
    if (visitor !== undefined) visiting.set(visitor.id, piece);
  }
  return placements.map((placement) => { const piece = visiting.get(placement.id); return piece === undefined ? placement : { ...placement, errand: piece.errand, errandKey: piece.key, ...piece.stand }; });
}
