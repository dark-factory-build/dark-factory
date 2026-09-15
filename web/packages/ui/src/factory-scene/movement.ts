import type { SceneLayout, ScenePoint, SceneWorkerPlacement } from "./scene.js";

type WalkingDirection = "north" | "south" | "east" | "west";

export type WorkerMotion = Readonly<{
  action: "still" | "walking" | "interacting";
  direction?: WalkingDirection;
  frame: 0 | 1;
}>;

export type Route = Readonly<{
  points: readonly ScenePoint[];
  length: number;
}>;

const EPSILON = 0.01;

export function samePoint(left: ScenePoint, right: ScenePoint) {
  return Math.abs(left.x - right.x) < EPSILON && Math.abs(left.y - right.y) < EPSILON;
}

function distance(left: ScenePoint, right: ScenePoint) {
  return Math.hypot(right.x - left.x, right.y - left.y);
}

function spine(layout: SceneLayout) {
  return layout.corridors.at(-1);
}

/** The row rectangle is the source of both the doorway gap and its clear lane. */
function rowCorridor(layout: SceneLayout, room: SceneLayout["rooms"][number]) {
  const mainSpine = spine(layout);
  return layout.corridors.find((corridor) => corridor !== mainSpine
    && corridor.y === room.door.y
    && room.door.x >= corridor.x && room.door.x <= corridor.x + corridor.width);
}

function corridorAt(layout: SceneLayout, point: ScenePoint) {
  const mainSpine = spine(layout);
  return layout.corridors.find((corridor) => corridor !== mainSpine
    && point.x >= corridor.x && point.x <= corridor.x + corridor.width
    && point.y >= corridor.y && point.y <= corridor.y + corridor.height);
}

function laneY(corridor: SceneLayout["corridors"][number]) {
  return corridor.y + corridor.height / 2;
}

function leaveRoom(layout: SceneLayout, room: SceneLayout["rooms"][number], from: ScenePoint, center: number) {
  const corridor = rowCorridor(layout, room);
  if (corridor === undefined) return undefined;
  const clear = laneY(corridor);
  return [
    { x: room.door.x, y: from.y }, room.door,
    { x: room.door.x, y: clear },
    { x: center, y: clear },
  ];
}

function enterRoom(layout: SceneLayout, room: SceneLayout["rooms"][number], to: ScenePoint, center: number) {
  const corridor = rowCorridor(layout, room);
  if (corridor === undefined) return undefined;
  const clear = laneY(corridor);
  return [
    { x: center, y: clear },
    { x: room.door.x, y: clear },
    room.door,
    { x: room.door.x, y: to.y }, to,
  ];
}

/**
 * The only moving route: leave a known room through its existing door, use the
 * corridor spine, then enter the next room beside its pictured work surface.
 * The connected resting/staging common space is also reachable by that spine;
 * omitted rooms never gain an invented route.
 */
export function routeBetween(
  layout: SceneLayout,
  from: SceneWorkerPlacement,
  to: SceneWorkerPlacement,
): Route | undefined {
  const common = (area: SceneWorkerPlacement["area"]) => area === "resting" || area === "staging";
  if ((from.area !== "room" && !common(from.area)) || (to.area !== "room" && !common(to.area))) return undefined;
  const source = from.roomId === undefined ? undefined : layout.rooms.find((room) => room.id === from.roomId);
  const destination = to.roomId === undefined ? undefined : layout.rooms.find((room) => room.id === to.roomId);
  if (from.area === "room" && source === undefined || to.area === "room" && destination === undefined) return undefined;
  if (source !== undefined && source.id === destination?.id) return route([from, to]);
  const mainSpine = spine(layout);
  if (mainSpine === undefined) return undefined;
  const center = mainSpine.x + mainSpine.width / 2;
  const sourceRoute = source === undefined ? [{ x: center, y: from.y }] : leaveRoom(layout, source, from, center);
  const destinationRoute = destination === undefined ? [{ x: center, y: to.y }, to] : enterRoom(layout, destination, to, center);
  if (sourceRoute === undefined || destinationRoute === undefined) return undefined;
  const points = [
    ...sourceRoute,
    ...destinationRoute,
  ];
  return route([from, ...points]);
}

function route(points: readonly ScenePoint[]): Route {
  const compact = points.filter((point, index) => index === 0 || !samePoint(points[index - 1]!, point));
  return { points: compact.slice(1), length: compact.slice(1).reduce((total, point, index) => total + distance(compact[index]!, point), 0) };
}

/** Continue from a point already in a corridor or the spine to a known room or common space. */
export function routeFromSpine(layout: SceneLayout, from: ScenePoint, to: SceneWorkerPlacement): Route | undefined {
  if (to.area !== "room" && to.area !== "resting" && to.area !== "staging") return undefined;
  const destination = to.roomId === undefined ? undefined : layout.rooms.find((room) => room.id === to.roomId);
  const mainSpine = spine(layout);
  if (to.area === "room" && destination === undefined || mainSpine === undefined) return undefined;
  const center = mainSpine.x + mainSpine.width / 2;
  const currentCorridor = corridorAt(layout, from);
  const clear = currentCorridor === undefined ? undefined : laneY(currentCorridor);
  const destinationRoute = destination === undefined ? [{ x: center, y: to.y }, to] : enterRoom(layout, destination, to, center);
  if (destinationRoute === undefined) return undefined;
  return route([from,
    ...(clear === undefined ? [{ x: center, y: from.y }] : [{ x: from.x, y: clear }, { x: center, y: clear }]),
    ...destinationRoute,
  ]);
}

/** Retarget from the rendered point, never a previously intended room. */
export function routeFromCurrent(layout: SceneLayout, from: ScenePoint, to: SceneWorkerPlacement): Route | undefined {
  // A door belongs to its corridor: retaining it as a room edge would let a
  // later route use a room that the worker has already left.
  const room = layout.rooms.find((candidate) =>
    from.x >= candidate.x && from.x <= candidate.x + candidate.width
    && from.y >= candidate.y && from.y < candidate.y + candidate.height);
  return room === undefined
    ? routeFromSpine(layout, from, to)
    : routeBetween(layout, { id: to.id, area: "room", roomId: room.id, ...from }, to);
}

export function pointOnRoute(start: ScenePoint, route: Route, travelled: number): ScenePoint {
  let previous = start;
  let remaining = travelled;
  for (const point of route.points) {
    const length = distance(previous, point);
    if (remaining <= length || length === 0) {
      const ratio = length === 0 ? 1 : remaining / length;
      return { x: previous.x + (point.x - previous.x) * ratio, y: previous.y + (point.y - previous.y) * ratio };
    }
    remaining -= length;
    previous = point;
  }
  return previous;
}

export function directionBetween(from: ScenePoint, to: ScenePoint): WalkingDirection {
  const horizontal = Math.abs(to.x - from.x) >= Math.abs(to.y - from.y);
  return horizontal ? to.x >= from.x ? "east" : "west" : to.y >= from.y ? "south" : "north";
}
