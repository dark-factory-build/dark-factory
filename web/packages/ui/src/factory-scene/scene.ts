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
const YARD = 120;
const BAND_LABELS = ["Clients", "Public", "Internal", "Tools"] as const;

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

/**
 * Lay out a hall's stations in fixed zones by kind: intake docks along the
 * left wall, timers by the door, the main line in the middle, cells across
 * the top and silos to the right. Static machines only, so live data never
 * moves a machine.
 */
function composeHall(hall: SceneHall, room: SceneRect): readonly RoomContent[] {
  const place = (machine: SceneMachine, x: number, y: number): RoomContent => {
    const shape = shapeOf(machine);
    return { key: machine.id, entityId: machine.id, ...(machine.represented === undefined ? {} : { representedIds: machine.represented }), shape, machine, workSurface: true, x, y, ...size[shape] };
  };
  const docks = hall.machines.filter((machine) => machine.kind === "ingress" && machine.trigger !== "timer");
  const clocks = hall.machines.filter((machine) => machine.kind === "ingress" && machine.trigger === "timer");
  const cells = hall.machines.filter((machine) => machine.kind === "job");
  const silos = hall.machines.filter((machine) => machine.kind === "store" || machine.kind === "queue");
  const top = room.y + 44, contents: RoomContent[] = [];
  docks.forEach((machine, index) => contents.push(place(machine, room.x + 22, top + 8 + index * 40)));
  clocks.forEach((machine, index) => contents.push(place(machine, room.x + room.width - 30 - index * 26, room.y + 14)));
  const lineX = room.x + Math.max(72, Math.floor((room.width - size.line.width) / 2));
  const lineY = top + 34 + (cells.length === 0 ? 0 : 30);
  contents.push(place({ id: hall.id, kind: "processor", label: hall.label, reading: hall.reading }, lineX, lineY));
  const right = room.x + room.width - 50;
  const perRow = Math.max(1, Math.floor((right - lineX) / 42));
  cells.forEach((machine, index) => contents.push(place(machine, lineX + (index % perRow) * 42, top + 6 + Math.floor(index / perRow) * 34)));
  silos.forEach((machine, index) => contents.push(place(machine, right - Math.floor(index / 3) * 36, top + 8 + (index % 3) * 62)));
  // What the code shows but nothing recognised waits by the door as a crate.
  hall.machines.filter((machine) => machine.kind === "unknown").forEach((machine, index) => contents.push(place(machine, room.x + 14 + index * 28, room.y + room.height - 40)));
  return contents;
}

function hallUnits(hall: SceneHall) {
  const count = hall.machines.length;
  // Silos stand right of the main line, so a hall holding any is never a single bay.
  return Math.max(count <= 4 ? 1 : count <= 10 ? 2 : 3, hall.machines.some((machine) => machine.kind === "store" || machine.kind === "queue") ? 2 : 1);
}

function hallHeight(hall: SceneHall) {
  const docks = hall.machines.filter((machine) => machine.kind === "ingress" && machine.trigger !== "timer").length;
  const silos = hall.machines.filter((machine) => machine.kind === "store" || machine.kind === "queue").length;
  const cells = hall.machines.some((machine) => machine.kind === "job") ? 30 : 0;
  return Math.max(hall.machines.length === 0 ? 132 : 176, 44 + 16 + docks * 40, 44 + 16 + Math.min(3, silos) * 62, 44 + 34 + cells + size.line.height + 56);
}

/**
 * Halls sit in fixed bands (clients, public, internal, tools) and in ID order
 * within a band, so a new edge or a busy hour never moves a hall. Shared
 * stores stand in the yard after the halls; runtime-only activity waits in a
 * quarantine bay last of all, so it can grow without shifting anything.
 * External parties are gates in the fence along the right edge.
 */
export function layoutScene(graph: SceneGraph): SceneLayout {
  const halls = [...graph.halls].sort((left, right) => left.band - right.band || compareText(left.id, right.id));
  const width = ROOM_LEFT + COLUMNS * BAY + PADDING;
  const fence = width + 8;
  const rooms: SceneRoomLayout[] = [], headings: SceneHeading[] = [], corridors: SceneRect[] = [];
  let top = FLOOR_TOP;
  type Member = { id: string; kind: SceneRoomLayout["kind"]; span: number; height: number; compose: (room: SceneRect) => readonly RoomContent[] };
  const placeRow = (members: readonly Member[]) => {
    for (let start = 0; start < members.length;) {
      const row: Member[] = [];
      let used = 0;
      while (start < members.length) {
        const member = members[start]!, span = Math.min(COLUMNS, member.span);
        if (used + span > COLUMNS) break;
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
  for (const band of [0, 1, 2, 3] as const) {
    const members = halls.filter((hall) => hall.band === band);
    if (members.length === 0) continue;
    headings.push({ label: BAND_LABELS[band], x: ROOM_LEFT, y: top });
    top += 16;
    placeRow(members.map((hall) => ({ id: hall.id, kind: "hall" as const, span: hallUnits(hall), height: hallHeight(hall), compose: (room: SceneRect) => composeHall(hall, room) })));
  }
  const yardRoom = (id: string, kind: SceneRoomLayout["kind"], label: string, machines: readonly SceneMachine[]) => {
    if (machines.length === 0) return;
    headings.push({ label, x: ROOM_LEFT, y: top });
    top += 16;
    const perRow = COLUMNS * 4, rowsNeeded = Math.ceil(machines.length / perRow);
    placeRow([{ id, kind, span: COLUMNS, height: Math.max(YARD, 56 + rowsNeeded * 66), compose: (room) => machines.map((machine, index): RoomContent => {
      const shape = shapeOf(machine);
      return { key: machine.id, entityId: machine.id, shape, machine, workSurface: true, x: room.x + 24 + (index % perRow) * (BAY / 4), y: room.y + 40 + Math.floor(index / perRow) * 66, ...size[shape] };
    }) }]);
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
