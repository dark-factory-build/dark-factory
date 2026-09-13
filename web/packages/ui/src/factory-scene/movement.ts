import type { SceneLayout, ScenePoint, SceneWorkerPlacement } from "./scene.js";

export type MotionAction = "still" | "walking" | "interacting";
export type WalkingDirection = "north" | "south" | "east" | "west";

export type WorkerMotion = Readonly<{
  action: MotionAction;
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

/**
 * The only moving route: leave a known room through its existing door, use the
 * corridor spine, then enter the next room beside its existing workstation.
 * Staging, resting and omitted-room locations deliberately have no invented
 * route through code rooms.
 */
export function routeBetween(
  layout: SceneLayout,
  from: SceneWorkerPlacement,
  to: SceneWorkerPlacement,
): Route | undefined {
  if (from.area !== "room" || to.area !== "room" || from.roomId === undefined || to.roomId === undefined) return undefined;
  const source = layout.rooms.find((room) => room.id === from.roomId);
  const destination = layout.rooms.find((room) => room.id === to.roomId);
  if (source === undefined || destination === undefined) return undefined;
  const points = from.roomId === to.roomId
    ? [{ x: to.x, y: to.y }]
    : [
      { x: from.x, y: source.door.y },
      { x: layout.corridors.at(-1)!.x + layout.corridors.at(-1)!.width / 2, y: source.door.y },
      { x: layout.corridors.at(-1)!.x + layout.corridors.at(-1)!.width / 2, y: destination.door.y },
      { x: to.x, y: destination.door.y },
      { x: to.x, y: to.y },
    ];
  const compact = points.filter((point, index) => index === 0 || !samePoint(points[index - 1]!, point));
  const all = [{ x: from.x, y: from.y }, ...compact];
  return { points: compact, length: all.slice(1).reduce((total, point, index) => total + distance(all[index]!, point), 0) };
}

/** Continue from a point already in a corridor or the spine to a known room. */
export function routeFromSpine(layout: SceneLayout, from: ScenePoint, to: SceneWorkerPlacement): Route | undefined {
  if (to.area !== "room" || to.roomId === undefined) return undefined;
  const destination = layout.rooms.find((room) => room.id === to.roomId);
  const spine = layout.corridors.at(-1);
  if (destination === undefined || spine === undefined) return undefined;
  const center = spine.x + spine.width / 2;
  const points = [
    { x: center, y: from.y },
    { x: center, y: destination.door.y },
    { x: to.x, y: destination.door.y },
    { x: to.x, y: to.y },
  ].filter((point, index, all) => index === 0 || !samePoint(all[index - 1]!, point));
  const all = [from, ...points];
  return { points, length: all.slice(1).reduce((total, point, index) => total + distance(all[index]!, point), 0) };
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
