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
  components?: readonly Readonly<{ id: string; label: string }>[];
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
  /** Exact observed direct child, retained separately from its displayed parent. */
  observedBayId?: string;
}>;

export type ScenePoint = Readonly<{ x: number; y: number }>;

type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;
export type SceneRoomLayout = SceneRect & Readonly<{
  id: string;
  arrangement: "hall" | "parent" | "bench";
  omittedBayCount: number;
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
  /** A worker only occupies a named bay when its exact observed child is pictured. */
  bayId?: string;
  x: number;
  y: number;
}>;

// Bounded size buckets leave a fixed grid cell and a common doorway edge.
const ROOM_WIDTH = 240;
const ROOM_HEIGHT = 224;
const FOOTPRINTS = {
  empty: [128, 152], tiny: [128, 152], small: [160, 184],
  medium: [192, 216], large: [224, 224],
} as const;
const CORRIDOR = 32;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + CORRIDOR;
const FLOOR_TOP = 48;
const WORKER_GAP = 24;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

/** Topology alone fixes buildings. Corridors express access, never imports. */
export function layoutScene(topology: SceneTopology): SceneLayout {
  const nodes = [...topology.nodes].sort((left, right) =>
    compareText(left.project?.name ?? "", right.project?.name ?? "") || compareText(left.project?.id ?? "", right.project?.id ?? "")
    || compareText(left.path, right.path) || compareText(left.id, right.id));
  // Keep the established one-to-three-room geometry when one local child is
  // added; larger scopes retain the existing bounded deterministic grid.
  const columns = nodes.length <= 4 ? Math.max(1, Math.min(2, nodes.length)) : Math.min(4, Math.ceil(Math.sqrt(nodes.length)));
  const width = ROOM_LEFT + columns * ROOM_WIDTH + PADDING;
  const groups = new Map<string, SceneNode[]>();
  for (const node of nodes) groups.set(node.project?.id ?? "", [...(groups.get(node.project?.id ?? "") ?? []), node]);
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
      const [width, height] = FOOTPRINTS[node.sizeBucket ?? "small"];
      const bottom = top + Math.floor(index / columns) * (ROOM_HEIGHT + CORRIDOR) + ROOM_HEIGHT;
      const y = bottom - height;
      const center = x + width / 2;
      const rectangle = { x, y, width, height };
      const arrangement = node.parentId === undefined || node.parentId === "" ? "hall" : (node.components?.length ?? 0) > 0 ? "parent" : "bench";
      const contents = composeRoom(node, rectangle, arrangement);
      rooms.push({ id: node.id, ...rectangle, arrangement, omittedBayCount: Math.max(0, (node.components?.length ?? 0) - contents.filter((item) => item.kind === "component").length), contents,
        door: { x: center, y: bottom } });
      if (index % columns === 0) corridors.push({ x: PADDING, y: bottom, width: CORRIDOR + Math.min(columns, members.length - index) * ROOM_WIDTH, height: CORRIDOR });
    });
    top += Math.ceil(members.length / columns) * (ROOM_HEIGHT + CORRIDOR) + 16;
  }
  // A spine joins each project's corridor and the expandable common area below.
  corridors.push({ x: PADDING, y: FLOOR_TOP, width: CORRIDOR, height: top - FLOOR_TOP + 32 });
  return { width, height: top + 80, rooms, headings, corridors, restingTop: top + 32 };
}

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[]): readonly SceneWorkerPlacement[] {
  const rooms = new Map(layout.rooms.map((room) => [room.id, room]));
  const roomCounts = new Map<string, number>();
  const outsideColumns = Math.max(1, Math.floor((layout.width - ROOM_LEFT - PADDING - 32) / WORKER_GAP) + 1);
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
    const bay = worker.observedBayId !== undefined && room.contents.some((item) => item.targetId === worker.observedBayId)
      ? worker.observedBayId : undefined;
    const occupancy = JSON.stringify([room.id, bay ?? null]);
    const slot = roomCounts.get(occupancy) ?? 0;
    const positions = workPositions(room, bay);
    if (slot >= positions.length) { areas.overflow.push(worker); continue; }
    roomCounts.set(occupancy, slot + 1);
    placed.push({ id: worker.id, area: "room", roomId: room.id, ...(bay === undefined ? {} : { bayId: bay }), ...positions[slot]! });
  }
  let top = layout.restingTop;
  for (const area of ["resting", "staging", "outside", "overflow"] as const) {
    areas[area].forEach((worker, slot) => placed.push({ id: worker.id, area,
      x: ROOM_LEFT + 16 + (slot % outsideColumns) * WORKER_GAP,
      y: top + Math.floor(slot / outsideColumns) * WORKER_GAP }));
    // Keep a connected resting bay even when empty; populations only grow below buildings.
    if (area === "resting" || areas[area].length > 0) top += Math.max(1, Math.ceil(areas[area].length / outsideColumns)) * WORKER_GAP + 40;
  }
  return placed.sort((left, right) => compareText(left.id, right.id));
}


/** Pictured surface slots also determine standing destinations; no parallel workstation map. */
export function workPositions(room: SceneRoomLayout, bayId?: string): readonly ScenePoint[] {
  const surface = bayId === undefined ? room.contents.find((item) => item.workSurface) : room.contents.find((item) => item.targetId === bayId);
  if (surface === undefined) return [{ x: room.door.x, y: room.door.y - 32 }];
  // Component bays share the composition's reserved right-hand approach. This
  // is the same pictured rectangle used by rendering and hit testing, rather
  // than a second map of invented workstations.
  if (bayId !== undefined) return [{ x: room.x + room.width - 12, y: surface.y + surface.height / 2 }];
  const offsets = surface.width >= 120 ? [0, -24, 24, -48, 48] : surface.width >= 88 ? [0, -24, 24] : [0, -24];
  return offsets.map((offset) => ({ x: surface.x + surface.width / 2 + offset, y: surface.y + surface.height + 8 }));
}

export const inventoryLabels = { source: "Source", tests: "Tests", documentation: "Docs", configuration: "Config", assets: "Assets", unclassified: "Unclassified" } as const;
type ContentKind = keyof typeof inventoryLabels | "component";
export type RoomContent = SceneRect & Readonly<{ key: string; kind: ContentKind; label: string; count: number; targetId?: string; workSurface?: boolean }>;

/** Three bounded arrangements: a root hall, parent edge bays, or a leaf bench. */
function composeRoom(node: SceneNode, room: SceneRect, arrangement: SceneRoomLayout["arrangement"]): readonly RoomContent[] {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  const kinds = (Object.keys(inventoryLabels) as Array<keyof typeof inventoryLabels>)
    .filter((kind) => (counts?.[kind] ?? 0) > 0)
    .sort((left, right) => counts![right] - counts![left] || compareText(left, right));
  const primary = kinds[0];
  const contents: RoomContent[] = [];
  const children = [...(node.components ?? [])].sort((a, b) => compareText(a.label, b.label) || compareText(a.id, b.id));
  // All pictured rectangles stop before this 20px edge strip. Its centre is
  // the 8px-clear side approach used for both the bench and direct-child bays.
  const contentLeft = room.x + 20;
  const contentWidth = room.width - 40;
  // Root halls can show four direct bays; compact parents deliberately show
  // fewer. Six remains the hard scene-wide pictured-bay bound.
  const bayLimit = arrangement === "bench" ? 0 : arrangement === "parent" ? (room.width >= 192 ? 2 : 1) : room.width >= 216 ? 4 : room.width >= 192 ? 3 : 1;
  const bays = children.slice(0, Math.min(6, bayLimit));
  const columns = arrangement === "parent" ? 1 : bays.length > 3 ? 2 : bays.length;
  const bayWidth = columns === 0 ? 0 : (contentWidth - Math.max(0, columns - 1) * 8) / columns;
  const bayHeight = arrangement === "parent" ? 34 : 38;
  bays.forEach((child, index) => {
    const column = index % columns, row = Math.floor(index / columns);
    contents.push({ key: child.id, kind: "component", label: child.label, count: 1, targetId: child.id,
      x: contentLeft + column * (bayWidth + 8), y: room.y + 48 + row * (bayHeight + 8), width: bayWidth, height: bayHeight,
    });
  });
  if (primary === undefined) return contents;
  const width = Math.min(contentWidth, Math.max(room.width >= 160 ? 112 : 80, room.width - ({ source: 40, tests: 56, documentation: 72, assets: 64, configuration: 80, unclassified: 80 }[primary])));
  const height = room.height >= 184 ? 64 : 32;
  contents.push({ key: primary, kind: primary, label: inventoryLabels[primary], count: counts![primary], workSurface: true,
    // Keep the doorway and its horizontal approach clear by a full sprite.
    x: contentLeft, y: room.y + room.height - height - 32, width, height });
  // Tests retain a supporting place even beside a much larger source installation.
  const secondary = kinds.filter((kind) => kind !== primary).sort((a, b) => Number(b === "tests") - Number(a === "tests") || compareText(a, b));
  const slots = arrangement === "bench" ? 2 : bays.length >= 3 ? 1 : 2;
  const bayBottom = bays.length === 0 ? room.y + 38 : room.y + 48 + Math.ceil(bays.length / columns) * (bayHeight + 8) - 8;
  const supportY = bayBottom + 8;
  const primaryTop = contents.find((item) => item.workSurface)!.y;
  const supportCount = supportY + 24 <= primaryTop - 8 ? Math.min(slots, Math.floor(contentWidth / 56)) : 0;
  secondary.slice(0, supportCount).forEach((kind, index) => contents.push({
    key: kind, kind, label: inventoryLabels[kind], count: counts![kind],
    x: contentLeft + index * 56, y: supportY, width: 48, height: 24,
  }));
  return contents;
}
