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
  proposalId?: string;
  purpose?: string; sourcePaths?: readonly string[]; sourceIncomplete?: boolean;
  representedIds?: readonly string[];
  dependencies?: Readonly<{ omitted: number; links: readonly Readonly<{ nodeId: string; label: string; path: string; direction: "to" | "from"; weight: number }>[] }>;
}>;

export type SceneProposal = Readonly<{
  id: string; title: string; state: "active" | "stale" | "unavailable"; base?: string; head?: string;
  /** Cable endpoints use displayed rooms; entity endpoints retain canonical source ownership. */
  relationships?: readonly Readonly<{ status: "added" | "removed"; fromId?: string; toId?: string; fromEntityId?: string; toEntityId?: string; fromPath: string; toPath: string; weight: number }>[];
  operations: readonly Readonly<{ entityId?: string; roomId?: string; path: string; previousPath?: string; kind: "addition" | "modification" | "removal" | "move"; label?: string }>[];
}>;

export type SceneNode = Readonly<{
  assemblies?: readonly SceneAssembly[];
  proposed?: boolean;
  sourceIncomplete?: boolean; sourcePaths?: readonly string[];
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

/** Inspect the displayed group's proposals without replacing its canonical identity. */
export function proposalsForEntity(topology: SceneTopology, proposals: readonly SceneProposal[], id?: string): readonly SceneProposal[] {
  if (id === undefined) return [];
  const members = new Set([id]);
  for (const room of topology.nodes) for (const assembly of room.assemblies ?? []) if (room.id === id || assembly.id === id) {
    members.add(assembly.id);
    for (const member of assembly.representedIds ?? []) members.add(member);
  }
  return proposals.filter((proposal) => proposal.operations.some((operation) => members.has(operation.entityId ?? "") || operation.roomId === id)
    || proposal.relationships?.some((edge) => members.has(edge.fromEntityId ?? edge.fromId ?? "") || members.has(edge.toEntityId ?? edge.toId ?? "")));
}

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

// Workshop bays share walls; dimensions never depend on live work.
const CORRIDOR = 32;
export const COMMON_WIDTH = 192;
export const PADDING = 16;
export const ROOM_LEFT = PADDING + COMMON_WIDTH + 16 + CORRIDOR;
const FLOOR_TOP = 48;
export const WORKER_GAP = 40;
export const WORKER_SIZE = 20;

/** Ordering for the floor: byte order over served fields, never a locale. */
export function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0;
}

/** Topology alone fixes buildings. Corridors express access, never imports. */
export function layoutScene(topology: SceneTopology, selectedProposalId?: string): SceneLayout {
  const nodes = [...topology.nodes].sort((left, right) =>
    Number(Boolean(left.proposed)) - Number(Boolean(right.proposed)) || compareText(left.project?.name ?? "", right.project?.name ?? "") || compareText(left.project?.id ?? "", right.project?.id ?? "")
    || Number(left.path === ".") - Number(right.path === ".") || compareText(left.path, right.path) || compareText(left.label, right.label) || compareText(left.id, right.id));
  const groups = new Map<string, SceneNode[]>();
  for (const node of nodes) groups.set(`${node.proposed ? "proposed:" : ""}${node.project?.id ?? ""}`, [...(groups.get(`${node.proposed ? "proposed:" : ""}${node.project?.id ?? ""}`) ?? []), node]);
  const assembled = nodes.some((node) => node.assemblies !== undefined);
  const units = (node: SceneNode) => !assembled ? 1 : (node.assemblies?.length ?? 0) > 6 ? 4
    : (node.assemblies?.length ?? 0) > 1 || node.sizeBucket === "large" ? 2 : 1;
  const columns = Math.min(4, Math.max(1, ...[...groups.values()].filter((group) => !group[0]?.proposed).map((group) => group.reduce((sum, node) => sum + units(node), 0))));
  const bay = 160;
  const width = ROOM_LEFT + columns * bay + PADDING;
  const rooms: SceneRoomLayout[] = [], headings: SceneHeading[] = [], corridors: SceneRect[] = [];
  let top = FLOOR_TOP;
  for (const members of groups.values()) {
    const project = members[0]!.project;
    if (project !== undefined) {
      if (members.length !== 1 || members[0]!.label !== project.name) headings.push({ label: project.name, x: ROOM_LEFT, y: top });
      top += 16;
    }
    for (let start = 0; start < members.length;) {
      const row: { node: SceneNode; span: number }[] = [];
      let used = 0;
      while (start < members.length) {
        const node = members[start]!, span = Math.min(columns, units(node));
        if (used + span > columns) break;
        row.push({ node, span }); used += span; start++;
      }
      const height = assembled ? Math.max(...row.map(({ node, span }) => {
        const count = Math.min(12, node.assemblies?.length ?? 0);
        const across = Math.min(span, Math.max(1, count));
        return count <= 1 ? (node.sizeBucket === "large" ? 274 : 208) : 146 + (Math.ceil(count / across) - 1) * 110 + (count > across ? 68 : 128);
      })) : 144;
      let x = ROOM_LEFT;
      for (const { node, span } of row) {
        const rectangle = { x, y: top, width: span * bay, height };
        rooms.push({ id: node.id, ...rectangle, contents: composeRoom(node, rectangle, selectedProposalId),
          door: { x: x + rectangle.width / 2, y: top + height } });
        x += rectangle.width;
      }
      corridors.push({ x: ROOM_LEFT - CORRIDOR, y: top + height, width: x - ROOM_LEFT + CORRIDOR, height: CORRIDOR });
      top += height + CORRIDOR;
    }
  }
  // The entrance stays beside the source rooms, including on large repositories.
  // Live actors can extend its seating downwards without moving any source area.
  corridors.push({ x: ROOM_LEFT - CORRIDOR, y: FLOOR_TOP, width: CORRIDOR, height: Math.max(160, top - FLOOR_TOP) });
  return { width, height: Math.max(224, top), rooms, headings, corridors, restingTop: 168 };
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
  const nearby = social === "nearby" ? layout.rooms.filter((room) => room.contents.some((item) => item.entityId !== undefined)) : [];
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


/** Stable furniture in the side commons and above each local resting pair. */
export function breakRoomNook(layout: SceneLayout, _restingCount: number, _planningCount: number, nearby = false) {
  return { width: COMMON_WIDTH, furniture: (["shelf", "coffee"] as const).map((errand, index) => ({
    errand, key: String(errand), roomId: undefined as string | undefined,
    x: PADDING + 120 + index * 38, y: 90,
    stand: { x: PADDING + 130 + index * 38, y: 136 },
  })).concat(!nearby ? [] : layout.rooms.filter((room) => room.contents.some((item) => item.entityId !== undefined)).map((room, index) => ({
    errand: index % 2 === 0 ? "shelf" as const : "coffee" as const, key: room.id, roomId: room.id,
    x: room.x + room.width - 42, y: room.door.y - 79,
    stand: { x: room.x + room.width - 30, y: room.door.y - 48 },
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
export type RoomContent = SceneRect & Readonly<{ key: string; kind: ContentKind; label: string; labelWidth?: number; count: number; entityId?: string; selectionId?: string; representedIds?: readonly string[]; resourceCounts?: Readonly<Record<InventoryKind, number>>; proposalId?: string; sourceIncomplete?: boolean; responsibility?: Responsibility; scale?: number; parts?: readonly Readonly<{ label: string; motif: Responsibility }>[]; workSurface?: boolean; furnishing?: "console" | "bench" | "drafting" }>;

/** File-backed equipment fills a bounded work zone; the access lanes stay clear. */
function composeRoom(node: SceneNode, room: SceneRect, selectedProposalId?: string): readonly RoomContent[] {
  if (node.assemblies !== undefined) {
    const assemblies = node.assemblies.filter((assembly) => !assembly.proposalId || !selectedProposalId || assembly.proposalId === selectedProposalId);
    // Proposed versions are alternatives, never inputs to an aggregate inventory.
    const rest = assemblies.slice(11);
    const sum = { source: 0, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 };
    for (const assembly of rest) for (const kind of Object.keys(sum) as InventoryKind[]) sum[kind] += assembly.inventory?.[assembly.inventoryScope === "direct" ? "direct" : "total"][kind] ?? 0;
    const shown: readonly SceneAssembly[] = assemblies.some((assembly) => assembly.proposalId) ? assemblies.slice(0, 12) : assemblies.length <= 12 ? assemblies : [...assemblies.slice(0, 11), {
      id: `${node.id}:aggregate`, path: node.path, label: `${rest.length} more assemblies`, inventoryScope: "direct",
      representedIds: rest.flatMap((assembly) => [assembly.id, ...assembly.representedIds ?? []]),
      inventory: rest.some((assembly) => assembly.inventory === undefined) ? undefined : { direct: sum, total: sum, samples: [], samples_omitted: Object.values(sum).reduce((a, b) => a + b, 0) },
    }];
    const columns = Math.min(Math.max(1, Math.floor(room.width / 160)), shown.length), rows = Math.ceil(shown.length / Math.max(1, columns));
    const cellWidth = (room.width - 48) / Math.max(1, columns), cellHeight = rows > 1 ? 110 : room.height - 146;
    return shown.map((assembly, index) => {
      const counts = assembly.inventory?.[assembly.inventoryScope === "direct" ? "direct" : "total"];
      const kinds = (Object.keys(inventoryLabels) as InventoryKind[]).filter((kind) => (counts?.[kind] ?? 0) > 0);
      const kind = kinds.includes("source") ? "source" : kinds.sort((a, b) => counts![b] - counts![a] || compareText(a, b))[0] ?? "unclassified";
      const total = Object.values(counts ?? {}).reduce((sum, count) => sum + count, 0);
      const scale = equipmentScale(total);
      const width = Math.min(cellWidth - (columns > 1 ? 16 : 0), [76, 128, 260, 344][scale]!);
      // Leave a standing lane for 24px actor targets before the next row's label.
      const height = Math.min(cellHeight - (rows > 1 ? 42 : 0), [62, 90, 150, 164][scale]!);
      const sampleFamilies = (assembly.inventory?.samples ?? []).map((path) => path.split("/").at(-1)!.replace(/(?:[._-](?:test|tests|spec))?\.[^.]+$/, "").split(/[._-]/)[0]!).filter(Boolean);
      const families = [...new Set(sampleFamilies)];
      const parts = scale < 2 || kind !== "source" ? [] : families.sort((a, b) => Number(responsibility(b) !== "generic") - Number(responsibility(a) !== "generic") || sampleFamilies.filter((family) => family === b).length - sampleFamilies.filter((family) => family === a).length || compareText(a, b)).slice(0, width >= 240 && height >= 120 ? 4 : 2).map((label) => ({ label, motif: responsibility(label) }));
      return { key: assembly.id, entityId: assembly.id, selectionId: assembly.id === `${node.id}:aggregate` ? node.id : undefined, representedIds: assembly.representedIds, kind, label: assembly.label, count: counts?.[kind] ?? 0, resourceCounts: counts, proposalId: assembly.proposalId, sourceIncomplete: assembly.sourceIncomplete, parts, responsibility: responsibility(assembly.path), scale, workSurface: true,
        x: room.x + 24 + (index % Math.max(1, columns)) * cellWidth, y: room.y + 62 + Math.floor(index / Math.max(1, columns)) * cellHeight, width, height, labelWidth: cellWidth - (columns > 1 ? 16 : 0) };
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
