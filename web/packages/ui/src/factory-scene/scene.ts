import type { SpriteAppearance, TopologyView } from "@dark-factory/client";

export type SceneTopology = Readonly<{
  digest: string;
  nodes: readonly SceneNode[];
}>;

export type SceneNode = Readonly<{
  id: string;
  /** Served parent identity; this is navigation data, never derived from text. */
  parentId?: string;
  path: string;
  label: string;
  kind: "repository" | "module" | "package" | "directory";
  /** Absent when the room stands for a project rather than a served node. */
  sizeBucket?: "empty" | "tiny" | "small" | "medium" | "large";
  language?: string;
  childCount?: number;
  components?: readonly Readonly<{ id: string; label: string; feature?: InventoryKind | "empty" | "unavailable" }>[];
  inventoryScope: "direct" | "subtree";
  inventory?: TopologyView["nodes"][number]["inventory"];
  dependencies?: Readonly<{
    omitted: number;
    links: readonly Readonly<{ nodeId: string; label: string; path: string; direction: "to" | "from"; weight: number }>[];
  }>;
  /** The project this room belongs to: rooms sharing an id are laid out together under its name. */
  project?: Readonly<{ id: string; name: string }>;
}>;

export type SceneWorker = Readonly<{
  id: string;
  name: string;
  role: "orchestrator" | "worker";
  provider?: "claude_code" | "codex" | "shell";
  activity: "busy" | "waiting" | "needs-you" | "idle";
  paused?: boolean;
  appearance?: SpriteAppearance;
  /** Live work is placed in a room; retained samples annotate the resting area. */
  location?: "working" | "last-observed" | "unobserved" | "resting";
  locationLabel?: string;
  /** The displayed room contains the more specific observed area. */
  locationWithin?: boolean;
  nodeId?: string;
  observedBayId?: string;
}>;

export type ScenePoint = Readonly<{ x: number; y: number }>;

type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;
export type SceneRoomLayout = SceneRect & Readonly<{
  id: string;
  door: ScenePoint;
  contents: readonly RoomContent[];
}>;

type SceneHeading = Readonly<{ label: string; x: number; y: number }>;

export type SceneLayout = Readonly<{
  width: number;
  height: number;
  rooms: readonly SceneRoomLayout[];
  headings: readonly SceneHeading[];
  corridors: readonly SceneRect[];
  restingTop: number;
}>;

export type SceneWorkerPlacement = Readonly<{
  id: string;
  area: "room" | "resting" | "staging" | "outside" | "overflow";
  roomId?: string;
  x: number;
  y: number;
}>;

// Staggered workshop bays share walls; dimensions never depend on live work.
const CORRIDOR = 32;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + CORRIDOR;
const FLOOR_TOP = 48;
const WORKER_GAP = 40;
export const WORKER_SIZE = 20;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

/** Topology alone fixes buildings. Corridors express access, never imports. */
export function layoutScene(topology: SceneTopology): SceneLayout {
  const nodes = [...topology.nodes].sort((left, right) =>
    compareText(left.project?.name ?? "", right.project?.name ?? "") || compareText(left.project?.id ?? "", right.project?.id ?? "")
    || compareText(left.path, right.path) || compareText(left.id, right.id));
  const groups = new Map<string, SceneNode[]>();
  for (const node of nodes) groups.set(node.project?.id ?? "", [...(groups.get(node.project?.id ?? "") ?? []), node]);
  const widestGroup = Math.max(1, ...[...groups.values()].map((group) => group.length));
  const columns = widestGroup <= 4 ? Math.max(1, Math.min(2, widestGroup)) : Math.min(4, Math.ceil(Math.sqrt(widestGroup)));
  const bays = columns === 4 ? [160, 208, 144, 192] : columns === 3 ? [160, 224, 160] : columns === 2 ? [144, 208] : [224];
  const width = ROOM_LEFT + bays.reduce((sum, bay) => sum + bay, 0) + PADDING;
  const rooms: SceneRoomLayout[] = [];
  const headings: SceneHeading[] = [];
  const corridors: SceneRect[] = [];
  let top = FLOOR_TOP;
  for (const members of groups.values()) {
    const project = members[0]!.project;
    if (project !== undefined) {
      if (members.length !== 1 || members[0]!.label !== project.name) headings.push({ label: project.name, x: ROOM_LEFT, y: top });
      top += 16;
    }
    for (let start = 0; start < members.length; start += columns) {
      const row = Math.floor(start / columns);
      const widths = row % 2 === 0 ? bays : [...bays.slice(1), bays[0]!];
      const height = row % 2 === 0 ? 112 : 128;
      let x = ROOM_LEFT;
      for (const [index, node] of members.slice(start, start + columns).entries()) {
        const width = widths[index]!;
        const rectangle = { x, y: top, width, height };
        rooms.push({ id: node.id, ...rectangle, contents: composeRoom(node, rectangle),
          door: { x: x + width / 2, y: top + height } });
        x += width;
      }
      corridors.push({ x: PADDING, y: top + height, width: x - PADDING, height: CORRIDOR });
      top += height + CORRIDOR;
    }
  }
  // A spine joins each project's corridor and the expandable common area below.
  corridors.push({ x: PADDING, y: FLOOR_TOP, width: CORRIDOR, height: top - FLOOR_TOP + 32 });
  return { width, height: top + 32, rooms, headings, corridors, restingTop: top + 32 };
}

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[]): readonly SceneWorkerPlacement[] {
  const rooms = new Map(layout.rooms.map((room) => [room.id, room]));
  const roomCounts = new Map<string, number>();
  const sorted = [...workers].sort((left, right) => compareText(left.id, right.id));
  const placed: SceneWorkerPlacement[] = [];
  const areas: Record<"resting" | "staging" | "outside" | "overflow", SceneWorker[]> = { resting: [], staging: [], outside: [], overflow: [] };
  for (const worker of sorted) {
    if (worker.location !== "working") {
      areas[worker.location === "unobserved" ? "staging" : "resting"].push(worker);
      continue;
    }
    const room = worker.nodeId === undefined ? undefined : rooms.get(worker.nodeId);
    if (room === undefined) { areas.outside.push(worker); continue; }
    const slot = roomCounts.get(room.id) ?? 0;
    const positions = workPositions(room);
    if (slot >= positions.length) { areas.overflow.push(worker); continue; }
    roomCounts.set(room.id, slot + 1);
    placed.push({ id: worker.id, area: "room", roomId: room.id, ...positions[slot]! });
  }
  const planning = (["staging", "outside", "overflow"] as const).flatMap((area) => areas[area].map((worker) => ({ worker, area })));
  const seats = commonSeating(layout, areas.resting.length, planning.length);
  areas.resting.forEach((worker, slot) => placed.push({ id: worker.id, area: "resting", ...seats.resting[slot]! }));
  planning.forEach(({ worker, area }, slot) => placed.push({ id: worker.id, area, ...seats.planning[slot]! }));
  return placed.sort((left, right) => compareText(left.id, right.id));
}

/** Two compact seating sections share one bay, growing down only when crowded. */
export function commonSeating(layout: SceneLayout, restingCount: number, planningCount: number) {
  const available = layout.width - ROOM_LEFT - PADDING;
  const capacity = (available - 16) / 2;
  const columns = Math.max(2, Math.min(Math.floor((capacity - 48) / WORKER_GAP) + 1, Math.max(restingCount, planningCount)));
  const width = (columns - 1) * WORKER_GAP + 48;
  const seats = (count: number, left: number, top: number) => Array.from({ length: Math.max(2, count) }, (_, slot) => ({
    x: left + 24 + (slot % columns) * WORKER_GAP,
    y: top + Math.floor(slot / columns) * WORKER_GAP,
  }));
  const resting = seats(restingCount, ROOM_LEFT, layout.restingTop);
  const planning = planningCount === 0 ? [] : seats(planningCount,
    ROOM_LEFT + width + 16, layout.restingTop);
  return { resting, planning };
}


/** Pictured surface slots also determine standing destinations; no parallel workstation map. */
export function workPositions(room: SceneRoomLayout): readonly ScenePoint[] {
  const surface = room.contents.find((item) => item.workSurface);
  if (surface === undefined) return [{ x: room.door.x, y: room.door.y - 32 }];
  const offsets = surface.width >= 120 ? [0, -24, 24, -48, 48] : surface.width >= 88 ? [0, -24, 24] : [0, -24];
  return offsets.map((offset) => ({ x: surface.x + surface.width / 2 + offset, y: surface.y + surface.height + WORKER_SIZE / 2 }));
}

export type InventoryKind = keyof typeof inventoryLabels;
export const inventoryLabels = { source: "Source", tests: "Tests", documentation: "Docs", configuration: "Config", assets: "Assets", unclassified: "Unclassified" } as const;
type ContentKind = keyof typeof inventoryLabels;
export type RoomContent = SceneRect & Readonly<{ key: string; kind: ContentKind; label: string; count: number; workSurface?: boolean; furnishing?: "console" | "bench" | "drafting" }>;

/** Background fittings stay sparse; inventory detail belongs in the tooltip. */
function composeRoom(node: SceneNode, room: SceneRect): readonly RoomContent[] {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  if (counts === undefined) return [];
  const primary = (Object.keys(inventoryLabels) as Array<keyof typeof inventoryLabels>)
    .filter((kind) => counts[kind] > 0)
    .sort((a, b) => counts[b] - counts[a] || compareText(a, b))[0];
  if (primary === undefined) return [];
  const furnishing = room.width < 160 ? "console" : room.width < 192 ? "bench" : "drafting";
  const width = furnishing === "console" ? 72 : furnishing === "bench" ? 88 : 104;
  const x = furnishing === "console" ? room.x + 24 : furnishing === "bench" ? room.x + room.width - width - 24 : room.x + (room.width - width) / 2;
  return [{ key: primary, kind: primary, label: inventoryLabels[primary], count: counts[primary], workSurface: true, furnishing,
    x, y: room.y + 48, width, height: 32 }];
}
