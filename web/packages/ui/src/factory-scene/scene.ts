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
  /** The unit a quarantined or shared machine belongs to, for its label. */
  owner?: string;
}>;

/** A deployment unit: one hall. Repository never decides its walls. */
export type SceneHall = Readonly<{
  id: string;
  label: string;
  runtime?: GraphNode["runtime"];
  reading: SceneReading;
  /** Placement band: clients, public, internal, tools. Static evidence alone decides it. */
  band: 0 | 1 | 2 | 3;
  machines: readonly SceneMachine[];
}>;

export type SceneFlow = Readonly<{ from: string; to: string; kind: string; reading: SceneReading }>;

/** The world model: operational graph and runtime reading, grouped for one floor. */
export type SceneGraph = Readonly<{
  /** Changes only when static structure or the detail level changes. */
  digest: string;
  halls: readonly SceneHall[];
  /** Stores and queues no single unit owns. */
  shared: readonly SceneMachine[];
  /** External parties at the fence. */
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
  operations: readonly Readonly<{ entityId?: string; roomId?: string; path: string; previousPath?: string; kind: "addition" | "modification" | "removal" | "move" }>[];
}>;

/** The changes touching one machine or hall. */
export function proposalsForEntity(proposals: readonly SceneProposal[], id?: string): readonly SceneProposal[] {
  if (id === undefined) return [];
  return proposals.filter((proposal) => proposal.operations.some((operation) => operation.entityId === id || operation.roomId === id));
}

export type SceneWorker = Readonly<{
  id: string;
  name: string;
  role: "orchestrator" | "worker";
  provider?: "claude_code" | "codex" | "shell";
  activity: "busy" | "waiting" | "needs-you" | "idle";
  paused?: boolean;
  appearance?: SpriteAppearance;
  /** Live work is placed in a hall; retained samples annotate the resting area. */
  location?: "working" | "last-observed" | "unobserved" | "resting";
  locationLabel?: string;
  /** The hall the worker is in. */
  nodeId?: string;
  /** The machine within that hall. */
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

type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;
export type SceneRoomLayout = SceneRect & Readonly<{
  id: string;
  kind: "hall" | "yard" | "quarantine";
  door: ScenePoint;
  contents: readonly RoomContent[];
}>;

type SceneHeading = Readonly<{ label: string; x: number; y: number }>;

/** A gate in the perimeter fence for one external party. */
export type SceneGate = SceneRect & Readonly<{ machine: SceneMachine }>;

export type SceneLayout = Readonly<{
  width: number;
  height: number;
  rooms: readonly SceneRoomLayout[];
  headings: readonly SceneHeading[];
  corridors: readonly SceneRect[];
  gates: readonly SceneGate[];
  /** The fence line's x; gates hang on it. */
  fence: number;
  restingTop: number;
  /** The outbound line along the top, from beside the desks to the fence. Fixed: it is there, empty, with no work. */
  line: readonly (SceneRect & Readonly<{ label: string }>)[];
}>;

export type SceneWorkerPlacement = Readonly<{
  id: string;
  area: "room" | "resting" | "staging" | "outside" | "overflow";
  roomId?: string;
  /** A resting worker who has got up for a while stands here instead of sitting. */
  errand?: BreakRoomErrand;
  errandKey?: string;
  x: number;
  y: number;
}>;

/** Coffee is ambient; the rest are implements, used when a recorded event says so (shelf also ambiently). */
export type BreakRoomErrand = "shelf" | "coffee" | "board" | "missions" | "tasks";

/** How a machine is pictured. A shape is chosen from the node kind, never its name. */
export type StationShape = "line" | "dock" | "manifold" | "clock" | "cell" | "silo" | "conveyor" | "crate";

export type RoomContent = SceneRect & Readonly<{
  key: string;
  entityId: string;
  representedIds?: readonly string[];
  shape: StationShape;
  machine: SceneMachine;
  workSurface: true;
}>;

// Halls share walls; dimensions never depend on live work.
const CORRIDOR = 32;
export const COMMON_WIDTH = 192;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + COMMON_WIDTH + 16 + CORRIDOR;
// The outbound line runs along the top beside the desks; halls start below it, a constant away.
const LINE_TOP = 32;
const FLOOR_TOP = LINE_TOP + 40 + 16;
export const WORKER_GAP = 40;
export const WORKER_SIZE = 20;
const BAY = 176;
const COLUMNS = 4;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

const shapeOf = (machine: SceneMachine): StationShape => machine.represented !== undefined ? "manifold"
  : machine.kind === "ingress" ? machine.trigger === "timer" ? "clock" : "dock"
  : machine.kind === "job" ? "cell" : machine.kind === "store" ? "silo" : machine.kind === "queue" ? "conveyor"
  : machine.kind === "processor" ? "line" : "crate";

const size: Record<StationShape, { width: number; height: number }> = {
  line: { width: 96, height: 44 }, dock: { width: 40, height: 18 }, manifold: { width: 44, height: 40 }, clock: { width: 22, height: 22 },
  cell: { width: 34, height: 30 }, silo: { width: 28, height: 40 }, conveyor: { width: 64, height: 16 }, crate: { width: 22, height: 22 },
};

type Spot = Readonly<{ machine: SceneMachine; x: number; y: number }>;
// A label is set in an 8px monospace face, about this wide a character.
const CHAR = 5, LANE = 22, GAP = 8;
/** How wide a machine stands with its label: docks label above from their left edge, the rest centred below. */
const footprint = (machine: SceneMachine) => {
  const shape = shapeOf(machine), width = size[shape].width, chars = Array.from(machine.label).length;
  return Math.max(width, CHAR * (shape === "dock" || shape === "manifold" ? Math.min(chars, 22) : Math.min(chars, Math.max(6, Math.floor((width + 10) / 5)))));
};

/** Right-wall stock: columns of `rows`, each as wide as its widest footprint, items bottom-aligned on rows as tall as the main line, with a walkway under each. */
function stock(machines: readonly SceneMachine[], rows: number, right: number, top: number) {
  const spots: Spot[] = [];
  let used = 0;
  for (let start = 0; start < machines.length; start += rows) {
    const column = machines.slice(start, start + rows), width = Math.max(...column.map(footprint)), left = right - used - width;
    column.forEach((machine, row) => { const { width: w, height: h } = size[shapeOf(machine)]; spots.push({ machine, x: left + (width - w) / 2, y: top + 52 + row * 66 - h }); });
    used += width + GAP;
  }
  return { spots, width: used };
}

/**
 * A hall's stations in fixed zones by kind: intake docks one per line along
 * the left wall, timers by the nameplate, job cells over the main line in the
 * middle, and stock and unrecognised crates to the right, each spaced by its
 * drawn width and label. Static machines only, so live data never moves one.
 * Undefined when the zones cannot fit the width.
 */
function planHall(hall: SceneHall, width: number) {
  const docks = hall.machines.filter((machine) => machine.kind === "ingress" && machine.trigger !== "timer");
  const clocks = hall.machines.filter((machine) => machine.kind === "ingress" && machine.trigger === "timer");
  const right = hall.machines.filter((machine) => machine.kind === "store" || machine.kind === "queue" || machine.kind === "unknown");
  const cells = hall.machines.filter((machine) => machine.kind === "job");
  // Timers fill the header right to left, clear of the nameplate and the changeover mark, wrapping into more header rows.
  const perClockRow = Math.max(1, Math.floor((width - 66 - 140) / 36) + 1), clockRows = Math.ceil(clocks.length / perClockRow);
  const top = clockRows === 0 ? 44 : 52 + (clockRows - 1) * 44;
  const spots: Spot[] = clocks.map((machine, index) => ({ machine, x: width - 66 - (index % perClockRow) * 36, y: 14 + Math.floor(index / perClockRow) * 44 }));
  docks.forEach((machine, index) => spots.push({ machine, x: LANE, y: top + 8 + index * 40 }));
  const from = docks.length === 0 ? LANE : LANE + Math.max(...docks.map(footprint)) + GAP;
  // ponytail: tries every column height from three; a hall holds tens of machines, not thousands.
  for (let rows = 3; rows <= Math.max(3, right.length); rows++) {
    const east = stock(right, rows, width - LANE, top), to = width - LANE - east.width, span = to - from;
    if (span < size.line.width) continue;
    const perRow = Math.floor((span - size.cell.width) / 42) + 1, cellRows = Math.ceil(cells.length / perRow);
    // Cells and the line stand on the stock's rows, so the walk under any row is clear from the right-hand lane.
    const lineY = top + 8 + cellRows * 66;
    const cellsLeft = from + Math.floor((span - Math.min(cells.length, perRow) * 42 + 8) / 2);
    const all = [...spots, ...east.spots, ...cells.map((machine, index) => ({ machine, x: cellsLeft + (index % perRow) * 42, y: top + 22 + Math.floor(index / perRow) * 66 })),
      { machine: { id: hall.id, kind: "processor" as const, label: hall.label, reading: hall.reading }, x: from + Math.floor((span - size.line.width) / 2), y: lineY }];
    const height = Math.max(hall.machines.length === 0 ? 132 : 176, lineY + size.line.height + 56, ...all.map(({ machine, y }) => y + size[shapeOf(machine)].height + 34));
    return { height, spots: all };
  }
  return undefined;
}

/** Shared and quarantined stock: left to right at their own widths, wrapping at the wall. */
function flow(machines: readonly SceneMachine[], width: number) {
  let x = LANE, row = 0;
  const spots = machines.map((machine): Spot => {
    if (x > LANE && x + footprint(machine) > width - LANE) { row++; x = LANE; }
    x += footprint(machine) + GAP;
    return { machine, x: x - footprint(machine) - GAP + (footprint(machine) - size[shapeOf(machine)].width) / 2, y: 80 + row * 66 - size[shapeOf(machine)].height };
  });
  return { height: 56 + (row + 1) * 66, spots };
}

function placeSpots(spots: readonly Spot[], room: SceneRect): readonly RoomContent[] {
  return spots.map(({ machine, x, y }) => {
    const shape = shapeOf(machine);
    return { key: machine.id, entityId: machine.id, ...(machine.represented === undefined ? {} : { representedIds: machine.represented }), shape, machine, workSurface: true,
      x: room.x + x, y: room.y + y, ...size[shape] };
  });
}

/** The narrowest hall, from a span its machine count suggests, whose zones fit. */
function fitHall(hall: SceneHall) {
  const count = hall.machines.length;
  for (let span = count <= 4 ? 1 : count <= 10 ? 2 : 3; ; span++) {
    const plan = planHall(hall, span * BAY);
    if (plan !== undefined || span >= COLUMNS) return { span, plan: plan ?? { height: 176, spots: [] } };
  }
}

/**
 * Halls are packed in band order (clients, public, internal, tools), then ID
 * order, so a new edge or a busy hour never moves a hall. Shared
 * stores stand in the yard after the halls; runtime-only activity waits in a
 * quarantine bay last of all, so it can grow without shifting anything.
 * External parties are gates in the fence along the right edge.
 */
export function layoutScene(graph: SceneGraph, columns = COLUMNS): SceneLayout {
  const halls = [...graph.halls].sort((left, right) => left.band - right.band || compareText(left.id, right.id));
  const width = ROOM_LEFT + columns * BAY + PADDING;
  const fence = width + 8;
  const rooms: SceneRoomLayout[] = [], headings: SceneHeading[] = [], corridors: SceneRect[] = [];
  let top = FLOOR_TOP;
  type Member = { id: string; kind: SceneRoomLayout["kind"]; span: number; height: number; compose: (room: SceneRect) => readonly RoomContent[] };
  const placeRow = (members: readonly Member[]) => {
    for (let start = 0; start < members.length;) {
      const row: Member[] = [];
      let used = 0;
      while (start < members.length) {
        const member = members[start]!, span = Math.min(columns, member.span);
        if (used + span > columns) break;
        row.push({ ...member, span }); used += span; start++;
      }
      const height = Math.max(...row.map((member) => member.height));
      let x = ROOM_LEFT;
      for (const member of row) {
        const rectangle = { x, y: top, width: member.span * BAY, height };
        rooms.push({ id: member.id, kind: member.kind, ...rectangle, contents: member.compose(rectangle), door: { x: x + rectangle.width / 2, y: top + height } });
        x += rectangle.width;
      }
      corridors.push({ x: ROOM_LEFT - CORRIDOR, y: top + height, width: x - ROOM_LEFT + CORRIDOR, height: CORRIDOR });
      top += height + CORRIDOR;
    }
  };
  // One continuous packing: band then id, so same-runtime halls stay adjacent without a row break per band.
  placeRow(halls.map((hall) => {
    const { span, plan } = fitHall(hall);
    return { id: hall.id, kind: "hall" as const, span, height: plan.height, compose: (room: SceneRect) => placeSpots(plan.spots, room) };
  }));
  const yardRoom = (id: string, kind: SceneRoomLayout["kind"], label: string, machines: readonly SceneMachine[]) => {
    if (machines.length === 0) return;
    headings.push({ label, x: ROOM_LEFT, y: top });
    top += 16;
    const plan = flow(machines, columns * BAY);
    placeRow([{ id, kind, span: columns, height: plan.height, compose: (room) => placeSpots(plan.spots, room) }]);
  };
  yardRoom("yard", "yard", "Shared yard", [...graph.shared].sort((left, right) => compareText(left.id, right.id)));
  yardRoom("quarantine", "quarantine", "Quarantine · runtime activity the code does not explain", graph.quarantine);
  // The entrance stays beside the halls, including on large plants.
  corridors.push({ x: ROOM_LEFT - CORRIDOR, y: FLOOR_TOP, width: CORRIDOR, height: Math.max(160, top - FLOOR_TOP) });
  const parties = [...graph.parties].sort((left, right) => compareText(left.id, right.id));
  const gates = parties.map((machine, index): SceneGate => ({ machine, x: fence - 6, y: FLOOR_TOP + 16 + index * 44, width: 12, height: 28 }));
  const height = Math.max(224, top, ...gates.map((gate) => gate.y + gate.height + 24));
  const line = LINE_STATIONS.map((label, index) => ({ label, x: ROOM_LEFT + index * BAY, y: LINE_TOP, width: BAY, height: 40 }));
  return { width: fence + 140, height, rooms, headings, corridors, gates, fence, restingTop: 168, line };
}

/**
 * How many bays a row holds so the whole floor shows largest in a pane of this
 * size: from four (the outbound line's width) up to twelve. Static structure
 * and the pane alone decide it.
 */
export function fitColumns(graph: SceneGraph, width: number, height: number) {
  let best = COLUMNS, scale = 0;
  for (let columns = COLUMNS; columns <= 12; columns++) {
    const layout = layoutScene(graph, columns), fit = Math.min(width / layout.width, height / (layout.height + 2 * PADDING));
    if (fit > scale) { best = columns; scale = fit; }
  }
  return best;
}

/** Newest first within a station; past the eighth, crates wait unseen behind the "+N". */
export function placeCrates(layout: SceneLayout, crates: readonly SceneCrate[]) {
  const counts = [0, 0, 0, 0] as [number, number, number, number];
  return [...crates].sort((left, right) => right.number - left.number || compareText(left.id, right.id)).map((crate) => {
    const station = layout.line[crate.station]!, slot = counts[crate.station]++;
    return { crate, shown: slot < LINE_SLOTS, x: station.x + 16 + Math.min(slot, LINE_SLOTS) * 18, y: station.y + 28 };
  });
}

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[], social: "nearby" | "commons" = "commons"): readonly SceneWorkerPlacement[] {
  const rooms = new Map(layout.rooms.map((room) => [room.id, room]));
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
    const room = worker.nodeId === undefined ? undefined : rooms.get(worker.nodeId);
    if (room === undefined) { areas.outside.push(worker); continue; }
    const position = workPositions(room, worker.observedBayId).find((point) => !occupied(point));
    if (position === undefined) { areas.overflow.push(worker); continue; }
    placed.push({ id: worker.id, area: "room", roomId: room.id, ...position });
  }
  const nearby = social === "nearby" ? layout.rooms.filter((room) => room.kind === "hall") : [];
  const localCounts = new Map<string, number>();
  const commons: SceneWorker[] = [];
  // Known locations keep their seats before unlocated idle workers join a nearby pair.
  const resting = areas.resting.sort((left, right) => Number(nearby.some((room) => room.id === right.nodeId)) - Number(nearby.some((room) => room.id === left.nodeId)) || compareText(left.id, right.id));
  for (const worker of resting) {
    const known = nearby.find((room) => room.id === worker.nodeId);
    const candidates = known ? [known] : [...nearby.filter((room) => localCounts.get(room.id) === 1), ...nearby.slice(0, 3)];
    const seat = candidates.filter((room) => (localCounts.get(room.id) ?? 0) < 2).flatMap((room) => [-24, 24].map((offset) => ({ roomId: room.id, x: room.door.x + offset, y: room.door.y - 24 }))).find((point) => !occupied(point));
    if (seat === undefined) { commons.push(worker); continue; }
    placed.push({ id: worker.id, area: "resting", ...seat });
    localCounts.set(seat.roomId, (localCounts.get(seat.roomId) ?? 0) + 1);
  }
  const planning = (["staging", "outside", "overflow"] as const).flatMap((area) => areas[area].map((worker) => ({ worker, area })));
  const seats = commonSeating(layout, commons.length, planning.length);
  commons.forEach((worker, slot) => placed.push({ id: worker.id, area: "resting", ...seats.resting[slot]! }));
  planning.forEach(({ worker, area }, slot) => placed.push({ id: worker.id, area, ...seats.planning[slot]! }));
  return placed.sort((left, right) => compareText(left.id, right.id));
}

/** The side commons has four seats per row and no empty duplicate seats for local rest. */
export function commonSeating(layout: SceneLayout, restingCount: number, planningCount: number) {
  const seats = (count: number, top: number) => Array.from({ length: count }, (_, slot) => ({
    x: PADDING + 24 + (slot % 4) * WORKER_GAP,
    y: top + Math.floor(slot / 4) * WORKER_GAP,
  }));
  return { resting: seats(restingCount, layout.restingTop), planning: seats(planningCount,
    layout.restingTop + (restingCount === 0 ? 0 : (Math.ceil(restingCount / 4) - 1) * WORKER_GAP + 64)) };
}

// Left to right along the break room's back wall.
const NOOK_ORDER: readonly BreakRoomErrand[] = ["board", "missions", "tasks", "shelf", "coffee"];

/**
 * Stable furniture in the side commons, never moved by data. Halls hold machinery, not break-room furniture.
 * Ambient turns come first in the list, so their timing never depends on the implements beside them.
 */
export function breakRoomNook(_layout: SceneLayout, _restingCount: number, _planningCount: number, _nearby = false) {
  return { width: COMMON_WIDTH, furniture: (["shelf", "coffee", "board", "missions", "tasks"] as const).map((errand) => ({
    errand, key: String(errand), roomId: undefined as string | undefined,
    // High enough that a cue over a visitor's head clears the implement's sign.
    x: PADDING + 6 + NOOK_ORDER.indexOf(errand) * 38, y: 70,
    stand: { x: PADDING + 12 + NOOK_ORDER.indexOf(errand) * 38, y: 136 },
  })) };
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
    const suited = placements.filter((placement) => placement.area === "resting" && placement.roomId === piece.roomId && habitOf(placement.id) === piece.errand);
    // Whoever has the piece keeps it for as long as they rest and the turn runs, whoever else
    // sits down or leaves meanwhile. (Every turn starts free, so nobody is carried into the next.)
    const holder = before.find((placement) => placement.errandKey === piece.key || placement.errandKey === undefined && placement.errand === piece.errand)?.id;
    const visitor = clock < 0 || clock - turn * TURN < FREE ? undefined : suited.find((placement) => placement.id === holder) ?? suited[(Math.imul(turn + 1, 2654435761) >>> 0) % Math.max(suited.length, 4)];
    if (visitor !== undefined) visiting.set(visitor.id, piece);
  }
  return placements.map((placement) => { const piece = visiting.get(placement.id); return piece === undefined ? placement : { ...placement, errand: piece.errand, errandKey: piece.key, ...piece.stand }; });
}

/** A worker stands in front of the machine they are working on; no parallel workstation map. */
export function workPositions(room: SceneRoomLayout, entityId?: string): readonly ScenePoint[] {
  const surface = room.contents.find((item) => item.entityId === entityId || item.representedIds?.includes(entityId ?? ""))
    ?? room.contents.find((item) => item.shape === "line") ?? room.contents[0];
  if (surface === undefined) return [{ x: room.door.x, y: room.door.y - 32 }];
  const offsets = surface.width >= 88 ? [0, -24, 24] : [0, -24];
  return offsets.map((offset) => ({ x: surface.x + surface.width / 2 + offset, y: surface.y + surface.height + WORKER_SIZE / 2 + 2 }));
}
