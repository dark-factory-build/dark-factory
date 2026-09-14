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
}>;

export type ScenePoint = Readonly<{ x: number; y: number }>;

export type SceneRect = Readonly<{ x: number; y: number; width: number; height: number }>;
export type SceneRoomLayout = SceneRect & Readonly<{
  id: string;
  door: ScenePoint;
  workstation: ScenePoint;
  standing: ScenePoint;
  furnishings: readonly (ScenePoint & Readonly<{ kind: "board" | "cabinet" | "connections"; label: string; standing: ScenePoint }>)[];
}>;

export type SceneHeading = Readonly<{ label: string; x: number; y: number }>;

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

// Bounded size buckets leave a fixed grid cell and a common doorway edge.
const ROOM_WIDTH = 224;
const ROOM_HEIGHT = 160;
const FOOTPRINTS = {
  empty: [128, 112], tiny: [128, 112], small: [160, 128],
  medium: [192, 144], large: [224, 160],
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
      const furnishings: SceneRoomLayout["furnishings"][number][] = [];
      if ((node.childCount ?? 0) > 0 || node.language) furnishings.push({ kind: "board", x: x + 16, y: bottom - 64,
        label: (node.childCount ?? 0) > 0 ? `${node.childCount} served subcomponents` : `${node.language} composition`, standing: { x: x + 24, y: bottom - 32 } });
      if ((node.dependencies?.links.length ?? 0) > 0 || node.kind === "module" || node.kind === "package") furnishings.push({
        kind: (node.dependencies?.links.length ?? 0) > 0 ? "connections" : "cabinet", x: x + width - 32, y: bottom - 64,
        label: (node.dependencies?.links.length ?? 0) > 0 ? "Observed dependency endpoints" : `Served ${node.kind} boundary`, standing: { x: x + width - 24, y: bottom - 32 } });
      rooms.push({ id: node.id, x, y, width, height, furnishings,
        door: { x: center, y: bottom },
        workstation: { x: center - 8, y: bottom - 64 }, standing: { x: center, y: bottom - 32 } });
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
    const slot = roomCounts.get(room.id) ?? 0;
    // Five unobstructed standing slots; extra people stay explicitly at capacity.
    if (slot >= 5) { areas.overflow.push(worker); continue; }
    roomCounts.set(room.id, slot + 1);
    placed.push({ id: worker.id, area: "room", roomId: room.id,
      x: room.standing.x + [0, -24, 24, -48, 48][slot]!, y: room.standing.y });
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
