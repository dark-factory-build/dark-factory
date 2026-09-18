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

// Shared walls and one human-scale workshop footprint keep the floor connected.
const ROOM_WIDTH = 176;
const ROOM_HEIGHT = 160;
const CORRIDOR = 32;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + CORRIDOR;
const FLOOR_TOP = 48;
const WORKER_GAP = 40;

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
  const width = ROOM_LEFT + columns * ROOM_WIDTH + PADDING;
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
    members.forEach((node, index) => {
      const x = ROOM_LEFT + (index % columns) * ROOM_WIDTH;
      const width = ROOM_WIDTH, height = ROOM_HEIGHT;
      const bottom = top + Math.floor(index / columns) * (ROOM_HEIGHT + CORRIDOR) + ROOM_HEIGHT;
      const y = bottom - height;
      const center = x + width / 2;
      const rectangle = { x, y, width, height };
      rooms.push({ id: node.id, ...rectangle, contents: composeRoom(node, rectangle),
        door: { x: center, y: bottom } });
      if (index % columns === 0) corridors.push({ x: PADDING, y: bottom, width: CORRIDOR + Math.min(columns, members.length - index) * ROOM_WIDTH, height: CORRIDOR });
    });
    top += Math.ceil(members.length / columns) * (ROOM_HEIGHT + CORRIDOR);
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
  let top = layout.restingTop;
  for (const area of ["resting", "staging", "outside", "overflow"] as const) {
    const seats = commonSeats(layout, areas[area].length, top);
    areas[area].forEach((worker, slot) => placed.push({ id: worker.id, area, ...seats[slot]! }));
    // Keep a furnished break room even when empty; extra workers grow below it.
    if (area === "resting" || areas[area].length > 0) top = seats.at(-1)!.y + WORKER_GAP + 48;
  }
  return placed.sort((left, right) => compareText(left.id, right.id));
}


/** Furniture and seated workers use the same positions and person-sized spacing. */
export function commonSeats(layout: SceneLayout, count: number, top: number): readonly ScenePoint[] {
  const columns = Math.max(1, Math.floor((layout.width - ROOM_LEFT - PADDING - 48) / WORKER_GAP) + 1);
  return Array.from({ length: Math.max(Math.min(4, columns), count) }, (_, slot) => ({
    x: ROOM_LEFT + 24 + (slot % columns) * WORKER_GAP,
    y: top + Math.floor(slot / columns) * WORKER_GAP,
  }));
}

/** Pictured surface slots also determine standing destinations; no parallel workstation map. */
export function workPositions(room: SceneRoomLayout): readonly ScenePoint[] {
  const surface = room.contents.find((item) => item.workSurface);
  if (surface === undefined) return [{ x: room.door.x, y: room.door.y - 32 }];
  const offsets = surface.width >= 120 ? [0, -24, 24, -48, 48] : surface.width >= 88 ? [0, -24, 24] : [0, -24];
  return offsets.map((offset) => ({ x: surface.x + surface.width / 2 + offset, y: surface.y + surface.height + 8 }));
}

export type InventoryKind = keyof typeof inventoryLabels;
export const inventoryLabels = { source: "Source", tests: "Tests", documentation: "Docs", configuration: "Config", assets: "Assets", unclassified: "Unclassified" } as const;
type ContentKind = keyof typeof inventoryLabels;
export type RoomContent = SceneRect & Readonly<{ key: string; kind: ContentKind; label: string; count: number; workSurface?: boolean }>;

/** Background fittings stay sparse; inventory detail belongs in the tooltip. */
function composeRoom(node: SceneNode, room: SceneRect): readonly RoomContent[] {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  if (counts === undefined) return [];
  const primary = (Object.keys(inventoryLabels) as Array<keyof typeof inventoryLabels>)
    .filter((kind) => counts[kind] > 0)
    .sort((a, b) => counts[b] - counts[a] || compareText(a, b))[0];
  if (primary === undefined) return [];
  return [{ key: primary, kind: primary, label: inventoryLabels[primary], count: counts[primary], workSurface: true,
    x: room.x + 24, y: room.y + 72, width: room.width - 48, height: 32 }];
}
