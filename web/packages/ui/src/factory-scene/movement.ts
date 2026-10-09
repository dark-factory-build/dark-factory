import { WORKER_SIZE, type SceneLayout, type ScenePoint, type SceneRect, type SceneStation } from "./scene.js";

type WalkingDirection = "north" | "south" | "east" | "west";

export type WorkerMotion = Readonly<{
  action: "still" | "walking" | "interacting";
  direction?: WalkingDirection;
  frame: 0 | 1;
  /** The worker's own running clock in milliseconds; absent while motion is stilled. */
  at?: number;
}>;

export type Route = Readonly<{
  points: readonly ScenePoint[];
  length: number;
}>;

const EPSILON = 0.01;
/** The walking grid's cell; a cell is free when a worker anywhere in it clears every solid. */
export const CELL = 6;
const RADIUS = WORKER_SIZE / 2;
// A seat at a table, or a spot inside a machine's clearance, is stepped into from a free cell at most this far away.
const STEP = 30;

export function samePoint(left: ScenePoint, right: ScenePoint) {
  return Math.abs(left.x - right.x) < EPSILON && Math.abs(left.y - right.y) < EPSILON;
}

function distance(left: ScenePoint, right: ScenePoint) {
  return Math.hypot(right.x - left.x, right.y - left.y);
}

/** Whether segment a-b passes through the rectangle grown by `by` (Liang–Barsky). */
export function crosses(a: ScenePoint, b: ScenePoint, rect: SceneRect, by = 0) {
  let t0 = 0, t1 = 1;
  const dx = b.x - a.x, dy = b.y - a.y, x = rect.x - by, y = rect.y - by, right = rect.x + rect.width + by, bottom = rect.y + rect.height + by;
  for (const [p, q] of [[-dx, a.x - x], [dx, right - a.x], [-dy, a.y - y], [dy, bottom - a.y]] as const) {
    if (p === 0) { if (q <= 0) return false; continue; }
    const t = q / p;
    if (p < 0) { if (t > t1) return false; if (t > t0) t0 = t; } else { if (t < t0) return false; if (t < t1) t1 = t; }
  }
  return t0 < t1;
}

const within = (point: ScenePoint, rect: SceneRect, by: number) => point.x > rect.x - by && point.x < rect.x + rect.width + by && point.y > rect.y - by && point.y < rect.y + rect.height + by;

/**
 * Whether a worker can walk straight from a to b: clear of every solid by its
 * radius. Only a short step into or out of a spot beside a solid (a seat at
 * its table) may come closer, and never through it; nobody walks the length of
 * a table past the people sitting at it.
 */
export function walkable(solids: readonly SceneRect[], a: ScenePoint, b: ScenePoint) {
  const step = distance(a, b) <= STEP;
  return solids.every((rect) => step && (within(a, rect, RADIUS) || within(b, rect, RADIUS)) ? !crosses(a, b, rect) : !crosses(a, b, rect, RADIUS));
}

type Grid = Readonly<{ columns: number; rows: number; blocked: Uint8Array }>;
const grids = new WeakMap<SceneLayout, Map<number, Grid>>();

/** The walking grid over the floor and, below it, as far down as `height` reaches (the benches). */
function gridFor(layout: SceneLayout, height: number): Grid {
  const cached = grids.get(layout) ?? new Map<number, Grid>();
  grids.set(layout, cached);
  const hit = cached.get(height);
  if (hit !== undefined) return hit;
  const columns = Math.ceil(layout.width / CELL), rows = Math.ceil(height / CELL), blocked = new Uint8Array(columns * rows);
  // A cell is blocked when any part of it is within a worker's radius of a solid, so every step between free cells is clear.
  for (const rect of layout.solids) {
    const left = Math.max(0, Math.floor((rect.x - RADIUS) / CELL)), right = Math.min(columns - 1, Math.ceil((rect.x + rect.width + RADIUS) / CELL) - 1);
    const top = Math.max(0, Math.floor((rect.y - RADIUS) / CELL)), bottom = Math.min(rows - 1, Math.ceil((rect.y + rect.height + RADIUS) / CELL) - 1);
    for (let row = top; row <= bottom; row++) for (let column = left; column <= right; column++) blocked[row * columns + column] = 1;
  }
  const grid = { columns, rows, blocked };
  cached.set(height, grid);
  return grid;
}

const cellCentre = (grid: Grid, cell: number) => ({ x: (cell % grid.columns) * CELL + CELL / 2, y: Math.floor(cell / grid.columns) * CELL + CELL / 2 });

/** The nearest free cell a point can step to in a straight line, or the point's own cell when it is free. */
function entry(grid: Grid, solids: readonly SceneRect[], point: ScenePoint) {
  const column = Math.floor(point.x / CELL), row = Math.floor(point.y / CELL), reach = Math.ceil(STEP / CELL);
  let best: { cell: number; distance: number } | undefined;
  for (let dy = -reach; dy <= reach; dy++) for (let dx = -reach; dx <= reach; dx++) {
    const c = column + dx, r = row + dy;
    if (c < 0 || r < 0 || c >= grid.columns || r >= grid.rows || grid.blocked[r * grid.columns + c] === 1) continue;
    const cell = r * grid.columns + c, at = cellCentre(grid, cell), far = distance(point, at);
    if (far > STEP || best !== undefined && far >= best.distance || !walkable(solids, point, at)) continue;
    best = { cell, distance: far };
  }
  return best?.cell;
}

/** A* over the grid with diagonal steps; a diagonal never cuts a corner. Cells in order, or undefined when unreachable. */
function search(grid: Grid, start: number, goal: number) {
  const { columns, rows, blocked } = grid, size = columns * rows;
  const cost = new Float64Array(size).fill(Infinity), from = new Int32Array(size).fill(-1), closed = new Uint8Array(size);
  const gx = goal % columns, gy = Math.floor(goal / columns);
  const guess = (cell: number) => { const dx = Math.abs(cell % columns - gx), dy = Math.abs(Math.floor(cell / columns) - gy); return Math.max(dx, dy) + (Math.SQRT2 - 1) * Math.min(dx, dy); };
  // A binary heap of [estimate, cell]; ties go to the lower cell, so a route never depends on timing.
  const heap: [number, number][] = [];
  const less = (a: [number, number], b: [number, number]) => a[0] < b[0] - 1e-9 || Math.abs(a[0] - b[0]) <= 1e-9 && a[1] < b[1];
  const push = (item: [number, number]) => { heap.push(item); for (let i = heap.length - 1; i > 0;) { const parent = (i - 1) >> 1; if (!less(heap[i]!, heap[parent]!)) break; [heap[i], heap[parent]] = [heap[parent]!, heap[i]!]; i = parent; } };
  const pop = () => { const top = heap[0]!, last = heap.pop()!; if (heap.length > 0) { heap[0] = last; for (let i = 0; ;) { const l = 2 * i + 1, r = l + 1; let m = i; if (l < heap.length && less(heap[l]!, heap[m]!)) m = l; if (r < heap.length && less(heap[r]!, heap[m]!)) m = r; if (m === i) break; [heap[i], heap[m]] = [heap[m]!, heap[i]!]; i = m; } } return top; };
  cost[start] = 0;
  push([guess(start), start]);
  while (heap.length > 0) {
    const [, cell] = pop();
    if (closed[cell] === 1) continue;
    if (cell === goal) {
      const path = [cell];
      while (from[path[0]!]! !== -1) path.unshift(from[path[0]!]!);
      return path;
    }
    closed[cell] = 1;
    const x = cell % columns, y = Math.floor(cell / columns);
    for (const [dx, dy] of [[1, 0], [-1, 0], [0, 1], [0, -1], [1, 1], [1, -1], [-1, 1], [-1, -1]] as const) {
      const nx = x + dx, ny = y + dy;
      if (nx < 0 || ny < 0 || nx >= columns || ny >= rows) continue;
      const next = ny * columns + nx;
      if (blocked[next] === 1 || closed[next] === 1) continue;
      if (dx !== 0 && dy !== 0 && (blocked[y * columns + nx] === 1 || blocked[ny * columns + x] === 1)) continue;
      const step = cost[cell]! + (dx !== 0 && dy !== 0 ? Math.SQRT2 : 1);
      if (step < cost[next]!) { cost[next] = step; from[next] = cell; push([step + guess(next), next]); }
    }
  }
  return undefined;
}

/** Pull a path straight wherever the exact segment is walkable. */
function smooth(solids: readonly SceneRect[], points: readonly ScenePoint[]) {
  const out = [points[0]!];
  for (let at = 0; at < points.length - 1;) {
    let next = at + 1;
    for (let ahead = at + 2; ahead < points.length; ahead++) { if (!walkable(solids, points[at]!, points[ahead]!)) break; next = ahead; }
    out.push(points[next]!);
    at = next;
  }
  return out;
}

/**
 * The walk from where a worker is drawn to where it is going, through free
 * floor: straight when nothing is in the way, otherwise around machines and
 * furniture by the grid. Undefined when no walk exists; never a line through
 * a solid.
 */
export function findRoute(layout: SceneLayout, from: ScenePoint, to: ScenePoint): Route | undefined {
  if (samePoint(from, to)) return { points: [], length: 0 };
  if (walkable(layout.solids, from, to)) return route([from, to]);
  const grid = gridFor(layout, Math.ceil(Math.max(layout.height, from.y + STEP + CELL, to.y + STEP + CELL) / 64) * 64);
  const start = entry(grid, layout.solids, from), goal = entry(grid, layout.solids, to);
  if (start === undefined || goal === undefined) return undefined;
  const cells = search(grid, start, goal);
  if (cells === undefined) return undefined;
  return route(smooth(layout.solids, [from, ...cells.map((cell) => cellCentre(grid, cell)), to]));
}

/**
 * A belt between two machines: from the side facing the other, straight where it clears every other machine, else
 * along the aisles. The front, where a worker stands and most labels hang, is never a port, nor a label's side.
 */
export function beltBetween(layout: SceneLayout, from: SceneStation, to: SceneStation): readonly ScenePoint[] {
  const a = { x: from.x + from.width / 2, y: from.y + from.height / 2 }, b = { x: to.x + to.width / 2, y: to.y + to.height / 2 };
  const port = (station: SceneStation, toward: ScenePoint, centre: ScenePoint) => Math.abs(toward.x - centre.x) * station.height >= Math.abs(toward.y - centre.y) * station.width
    || toward.y > centre.y || station.shape === "dock" || station.shape === "manifold"
    ? { x: toward.x >= centre.x ? station.x + station.width + 2 : station.x - 2, y: centre.y }
    : { x: centre.x, y: station.y - 2 };
  const start = port(from, b, a), end = port(to, a, b);
  const others = layout.stations.filter((station) => station !== from && station !== to);
  if (others.every((station) => !crosses(start, end, station, 3)) && !crosses(start, end, from) && !crosses(start, end, to)) return [start, end];
  const walk = findRoute(layout, start, end);
  // A belt is overhead: with no clear run it is still drawn, straight, rather than dropped.
  return walk === undefined ? [start, end] : [start, ...walk.points];
}

function route(points: readonly ScenePoint[]): Route {
  const compact = points.filter((point, index) => index === 0 || !samePoint(points[index - 1]!, point));
  return { points: compact.slice(1).map(({ x, y }) => ({ x, y })), length: compact.slice(1).reduce((total, point, index) => total + distance(compact[index]!, point), 0) };
}

export function pointOnRoute(start: ScenePoint, route: Route, travelled: number): ScenePoint {
  let previous = start;
  let remaining = travelled;
  for (const point of route.points) {
    const length = distance(previous, point);
    // Within a hair of a corner is the corner: a diagonal leg's rounding never leaves a worker beside where it stops.
    if (remaining >= length - EPSILON) { remaining -= length; previous = point; continue; }
    const ratio = remaining / length;
    return { x: previous.x + (point.x - previous.x) * ratio, y: previous.y + (point.y - previous.y) * ratio };
  }
  return previous;
}

export function directionBetween(from: ScenePoint, to: ScenePoint): WalkingDirection {
  const horizontal = Math.abs(to.x - from.x) >= Math.abs(to.y - from.y);
  return horizontal ? to.x >= from.x ? "east" : "west" : to.y >= from.y ? "south" : "north";
}
