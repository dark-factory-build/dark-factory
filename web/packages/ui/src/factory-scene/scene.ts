import type { SpriteAppearance, TopologyView } from "@dark-factory/client";

export type SceneTopology = Readonly<{
  digest: string;
  nodes: readonly SceneNode[];
}>;

export type SceneAssembly = Readonly<{
  id: string; path: string; label: string;
  inventoryScope: "direct" | "subtree";
  inventory?: TopologyView["nodes"][number]["inventory"];
  sizeBucket?: "empty" | "tiny" | "small" | "medium" | "large";
  purpose?: string; sourcePaths?: readonly string[]; sourceIncomplete?: boolean;
  representedIds?: readonly string[];
  dependencies?: Readonly<{ omitted: number; links: readonly Readonly<{ nodeId: string; label: string; path: string; direction: "to" | "from"; weight: number }>[] }>;
}>;

export type SceneProposal = Readonly<{
  id: string; title: string; state: "active" | "stale" | "unavailable"; base?: string; head?: string;
  operations: readonly Readonly<{ entityId?: string; roomId?: string; path: string; previousPath?: string; kind: "addition" | "modification" | "removal" | "move"; label?: string }>[];
}>;

export type SceneNode = Readonly<{
  assemblies?: readonly SceneAssembly[];
  proposed?: boolean;
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
  review?: Readonly<{ proposalId: string; scope: string }>;
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
  /** A resting worker who has got up for a while stands here instead of sitting. */
  errand?: BreakRoomErrand;
  errandKey?: string;
  x: number;
  y: number;
}>;

export type BreakRoomErrand = "shelf" | "coffee";

// Staggered workshop bays share walls; dimensions never depend on live work.
const CORRIDOR = 32;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + CORRIDOR;
const FLOOR_TOP = 48;
export const WORKER_GAP = 40;
export const WORKER_SIZE = 20;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

/** Topology alone fixes buildings. Corridors express access, never imports. */
export function layoutScene(topology: SceneTopology): SceneLayout {
  const nodes = [...topology.nodes].sort((left, right) =>
    Number(Boolean(left.proposed)) - Number(Boolean(right.proposed)) || compareText(left.project?.name ?? "", right.project?.name ?? "") || compareText(left.project?.id ?? "", right.project?.id ?? "")
    || compareText(left.path, right.path) || compareText(left.label, right.label) || compareText(left.id, right.id));
  const groups = new Map<string, SceneNode[]>();
  for (const node of nodes) groups.set(`${node.proposed ? "proposed:" : ""}${node.project?.id ?? ""}`, [...(groups.get(`${node.proposed ? "proposed:" : ""}${node.project?.id ?? ""}`) ?? []), node]);
  const widestGroup = Math.max(1, ...[...groups.values()].filter((group) => !group[0]?.proposed).map((group) => group.length));
  const assembled = nodes.some((node) => node.assemblies !== undefined);
  const columns = assembled ? Math.min(3, Math.max(1, widestGroup)) : widestGroup <= 4 ? Math.max(1, Math.min(2, widestGroup)) : Math.min(4, Math.ceil(Math.sqrt(widestGroup)));
  const bays = assembled ? Array.from({ length: columns }, () => 280) : columns === 4 ? [160, 208, 144, 192] : columns === 3 ? [160, 224, 160] : columns === 2 ? [144, 208] : [224];
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
      const height = assembled ? Math.max(...members.slice(start, start + columns).map((node) => 76 + Math.ceil(Math.max(1, Math.min(6, node.assemblies?.length ?? 1)) / 2) * 96)) : row % 2 === 0 ? 112 : 128;
      let x = ROOM_LEFT;
      for (const [index, node] of members.slice(start, start + columns).entries()) {
        const width = widths[index]!;
        const ownHeight = assembled ? 76 + Math.ceil(Math.max(1, Math.min(6, node.assemblies?.length ?? 1)) / 2) * 96 : height;
        const rectangle = { x, y: top + height - ownHeight, width, height: ownHeight };
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

export function placeWorkers(layout: SceneLayout, workers: readonly SceneWorker[], social: "nearby" | "commons" = "commons"): readonly SceneWorkerPlacement[] {
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
    const positions = workPositions(room, worker.observedBayId);
    if (slot >= positions.length) { areas.overflow.push(worker); continue; }
    roomCounts.set(room.id, slot + 1);
    placed.push({ id: worker.id, area: "room", roomId: room.id, ...positions[slot]! });
  }
  const planning = (["staging", "outside", "overflow"] as const).flatMap((area) => areas[area].map((worker) => ({ worker, area })));
  const seats = commonSeating(layout, areas.resting.length, planning.length);
  const nearby = social === "nearby" ? layout.rooms.filter((room) => room.contents.some((item) => item.entityId !== undefined)) : [];
  areas.resting.forEach((worker, slot) => {
    const room = nearby.find((room) => room.id === worker.nodeId) ?? nearby[slot % Math.max(1, nearby.length)];
    const localSlot = Math.floor(slot / Math.max(1, nearby.length));
    placed.push(room === undefined || localSlot > 1 ? { id: worker.id, area: "resting", ...seats.resting[slot]! }
      : { id: worker.id, area: "resting", roomId: room.id, x: room.x + 28 + localSlot * WORKER_GAP, y: room.door.y - 24 });
  });
  planning.forEach(({ worker, area }, slot) => placed.push({ id: worker.id, area, ...seats.planning[slot]! }));
  return placed.sort((left, right) => compareText(left.id, right.id));
}

/** Two compact seating sections share one bay, growing down only when crowded. */
export function commonSeating(layout: SceneLayout, restingCount: number, planningCount: number) {
  const available = layout.width - ROOM_LEFT - PADDING;
  // The break-room furniture keeps whatever two seats a section can spare it.
  const capacity = (available - 16 - nookWidth(layout)) / 2;
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


// Each piece of break-room furniture wants this much of the back wall.
const PIECE = 30;
const nookWidth = (layout: SceneLayout) => Math.max(0, Math.min(2 * PIECE + 4, layout.width - ROOM_LEFT - PADDING - (2 * 88 + 16)));

/**
 * Furniture along the break room's back wall, right of the last seats, with one
 * standing place in front of each piece: a bookshelf where there is room for
 * one thing, a coffee station beside it where there is room for two. On a floor
 * with room for neither, workers simply stay seated.
 */
export function breakRoomNook(layout: SceneLayout, restingCount: number, planningCount: number, nearby = false) {
  const width = nookWidth(layout), compact = width < 2 * PIECE + 4, pieces = compact ? 2 : Math.floor(width / PIECE);
  if (pieces === 0) return undefined;
  const seating = commonSeating(layout, restingCount, planningCount);
  const left = Math.max(...[...seating.resting, ...seating.planning].map((seat) => seat.x)) + 24;
  const compactX = layout.width - PADDING - WORKER_SIZE;
  return { width, furniture: (["shelf", "coffee"] as const).slice(0, pieces).map((errand, index) => ({
    errand, key: String(errand), roomId: undefined as string | undefined,
    x: compact ? compactX : left + 5 + index * PIECE,
    y: layout.restingTop - 38 + (compact ? index * 24 : 0),
    stand: { x: compact ? compactX + 10 : left + 15 + index * PIECE, y: layout.restingTop - 8 + (compact ? index * 24 : 0) },
  })).concat(!nearby ? [] : layout.rooms.filter((room) => room.contents.some((item) => item.entityId !== undefined)).map((room, index) => ({
    errand: index % 2 === 0 ? "shelf" as const : "coffee" as const, key: room.id, roomId: room.id,
    x: room.x + room.width - 42, y: room.door.y - 55,
    stand: { x: room.x + room.width - 30, y: room.door.y - 24 },
  }))) };
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

/** Pictured surface slots also determine standing destinations; no parallel workstation map. */
export function workPositions(room: SceneRoomLayout, entityId?: string): readonly ScenePoint[] {
  const surface = room.contents.find((item) => item.workSurface && (item.entityId === entityId || item.representedIds?.includes(entityId ?? ""))) ?? room.contents.find((item) => item.workSurface);
  if (surface === undefined) return [{ x: room.door.x, y: room.door.y - 32 }];
  const offsets = surface.width >= 120 ? [0, -24, 24, -48, 48] : surface.width >= 88 ? [0, -24, 24] : [0, -24];
  return offsets.map((offset) => ({ x: surface.x + surface.width / 2 + offset, y: surface.y + surface.height + WORKER_SIZE / 2 }));
}

export type InventoryKind = keyof typeof inventoryLabels;
export const inventoryLabels = { source: "Source", tests: "Tests", documentation: "Docs", configuration: "Config", assets: "Assets", unclassified: "Unclassified" } as const;
type ContentKind = keyof typeof inventoryLabels;
export type Responsibility = "movement" | "messaging" | "selection" | "admission" | "storage" | "interface" | "generic";
/** Name-based visual hints, never claims of analysis. */
export function responsibility(path: string): Responsibility {
  const name = path.toLowerCase();
  return /movement|routing|transport/.test(name) ? "movement" : /message|event|protocol|relay/.test(name) ? "messaging" : /topology|source|selection/.test(name) ? "selection" : /admission|queue|dispatch|task/.test(name) ? "admission" : /store|persist|database|sqlite/.test(name) ? "storage" : /browser|console|interface/.test(name) ? "interface" : "generic";
}
/** Eligible file counts, clamped to four visual buckets; generated/vendor files are excluded upstream. */
export function equipmentScale(count: number): number { return count <= 4 ? 0 : count <= 20 ? 1 : count <= 80 ? 2 : 3; }
export type RoomContent = SceneRect & Readonly<{ key: string; kind: ContentKind; label: string; count: number; entityId?: string; selectionId?: string; representedIds?: readonly string[]; resourceCounts?: Readonly<Record<InventoryKind, number>>; responsibility?: Responsibility; scale?: number; parts?: readonly Readonly<{ label: string; motif: Responsibility }>[]; workSurface?: boolean; furnishing?: "console" | "bench" | "drafting" }>;

/** Background fittings stay sparse; inventory detail belongs in the tooltip. */
function composeRoom(node: SceneNode, room: SceneRect): readonly RoomContent[] {
  if (node.assemblies !== undefined) {
    const rest = node.assemblies.slice(5);
    const sum = { source: 0, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 };
    for (const assembly of rest) for (const kind of Object.keys(sum) as InventoryKind[]) sum[kind] += assembly.inventory?.[assembly.inventoryScope === "direct" ? "direct" : "total"][kind] ?? 0;
    const shown: readonly SceneAssembly[] = node.assemblies.length <= 6 ? node.assemblies : [...node.assemblies.slice(0, 5), {
      id: `${node.id}:aggregate`, path: node.path, label: `${rest.length} more assemblies`, inventoryScope: "direct",
      representedIds: rest.flatMap((assembly) => [assembly.id, ...assembly.representedIds ?? []]),
      inventory: rest.some((assembly) => assembly.inventory === undefined) ? undefined : { direct: sum, total: sum, samples: [], samples_omitted: Object.values(sum).reduce((a, b) => a + b, 0) },
    }];
    return shown.flatMap((assembly, index) => {
    const counts = assembly.inventory?.[assembly.inventoryScope === "direct" ? "direct" : "total"];
    const kinds = (Object.keys(inventoryLabels) as InventoryKind[]).filter((kind) => (counts?.[kind] ?? 0) > 0);
    const kind = kinds.includes("source") ? "source" : kinds.sort((a, b) => counts![b] - counts![a] || compareText(a, b))[0] ?? "unclassified";
    const total = Object.values(counts ?? {}).reduce((sum, count) => sum + count, 0);
    const scale = equipmentScale(total);
    const files = assembly.inventory?.samples ?? [];
    const sampleFamilies = files.map((path) => path.split("/").at(-1)!.replace(/(?:[._-](?:test|tests|spec))?\.[^.]+$/, "").split(/[._-]/)[0]!).filter(Boolean);
    const families = [...new Set(sampleFamilies)];
    const parts = scale < 2 || kind !== "source" ? [] : families.sort((a, b) => Number(responsibility(b) !== "generic") - Number(responsibility(a) !== "generic") || sampleFamilies.filter((family) => family === b).length - sampleFamilies.filter((family) => family === a).length || compareText(a, b)).slice(0, 2).map((label) => ({ label, motif: responsibility(label) }));
    return [{ key: assembly.id, entityId: assembly.id, selectionId: assembly.id === `${node.id}:aggregate` ? node.id : undefined, representedIds: assembly.representedIds, kind, label: assembly.label, count: counts?.[kind] ?? 0, resourceCounts: counts, parts, responsibility: responsibility(assembly.path), scale, workSurface: true,
      x: room.x + 24 + (index % 2) * 132, y: room.y + 56 + Math.floor(index / 2) * 96, width: 104, height: 52 }];
    });
  }
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
