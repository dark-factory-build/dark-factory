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
export function walkable(solids: readonly SceneRect[], a: ScenePoint, b: ScenePoint, radius = RADIUS) {
  const step = distance(a, b) <= STEP;
  return solids.every((rect) => step && (within(a, rect, radius) || within(b, rect, radius)) ? !crosses(a, b, rect) : !crosses(a, b, rect, radius));
}

type Grid = Readonly<{ columns: number; rows: number; blocked: Uint8Array; solids: readonly SceneRect[]; radius: number }>;
const grids = new WeakMap<object, Map<string, Grid>>();

/** A grid over the floor (and, for walking, as far down as `height` reaches: the benches) for things of this radius among these solids. */
function gridFor(solids: readonly SceneRect[], radius: number, width: number, height: number): Grid {
  const cached = grids.get(solids) ?? new Map<string, Grid>();
  grids.set(solids, cached);
  const hit = cached.get(`${radius} ${width} ${height}`);
  if (hit !== undefined) return hit;
  const columns = Math.ceil(width / CELL), rows = Math.ceil(height / CELL), blocked = new Uint8Array(columns * rows);
  // A cell is blocked when any part of it is within the radius of a solid, so every step between free cells is clear.
  for (const rect of solids) {
    const left = Math.max(0, Math.floor((rect.x - radius) / CELL)), right = Math.min(columns - 1, Math.ceil((rect.x + rect.width + radius) / CELL) - 1);
    const top = Math.max(0, Math.floor((rect.y - radius) / CELL)), bottom = Math.min(rows - 1, Math.ceil((rect.y + rect.height + radius) / CELL) - 1);
    for (let row = top; row <= bottom; row++) for (let column = left; column <= right; column++) blocked[row * columns + column] = 1;
  }
  const grid = { columns, rows, blocked, solids, radius };
  cached.set(`${radius} ${width} ${height}`, grid);
  return grid;
}

const cellCentre = (grid: Grid, cell: number) => ({ x: (cell % grid.columns) * CELL + CELL / 2, y: Math.floor(cell / grid.columns) * CELL + CELL / 2 });

/** The nearest free cell a point can step to in a straight line, or the point's own cell when it is free. */
function entry(grid: Grid, point: ScenePoint) {
  const column = Math.floor(point.x / CELL), row = Math.floor(point.y / CELL), reach = Math.ceil(STEP / CELL);
  let best: { cell: number; distance: number } | undefined;
  for (let dy = -reach; dy <= reach; dy++) for (let dx = -reach; dx <= reach; dx++) {
    const c = column + dx, r = row + dy;
    if (c < 0 || r < 0 || c >= grid.columns || r >= grid.rows || grid.blocked[r * grid.columns + c] === 1) continue;
    const cell = r * grid.columns + c, at = cellCentre(grid, cell), far = distance(point, at);
    if (far > STEP || best !== undefined && far >= best.distance || !walkable(grid.solids, point, at, grid.radius)) continue;
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
function smooth(solids: readonly SceneRect[], points: readonly ScenePoint[], radius = RADIUS) {
  const out = [points[0]!];
  for (let at = 0; at < points.length - 1;) {
    let next = at + 1;
    for (let ahead = at + 2; ahead < points.length; ahead++) { if (!walkable(solids, points[at]!, points[ahead]!, radius)) break; next = ahead; }
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
  return navigate(layout.solids, RADIUS, layout.width, Math.ceil(Math.max(layout.height, from.y + STEP + CELL, to.y + STEP + CELL) / 64) * 64, from, to);
}

function navigate(solids: readonly SceneRect[], radius: number, width: number, height: number, from: ScenePoint, to: ScenePoint): Route | undefined {
  if (samePoint(from, to)) return { points: [], length: 0 };
  if (walkable(solids, from, to, radius)) return route([from, to]);
  const grid = gridFor(solids, radius, width, height);
  const start = entry(grid, from), goal = entry(grid, to);
  if (start === undefined || goal === undefined) return undefined;
  const cells = search(grid, start, goal);
  if (cells === undefined) return undefined;
  return route(smooth(solids, [from, ...cells.map((cell) => cellCentre(grid, cell)), to], radius));
}

// Belts run on their own coarse grid, square: so every crossing is a right angle, and two belts never share a run.
const BELT_CELL = 8, BEND = 3, CROSSING = 14, SEARCH_LIMIT = 30000;
// Nothing crosses this close to a label, a port, a junction or a machine.
export const CROSSING_CLEARANCE = 10;
const H = 1, V = 2;

export type BeltCrossing = Readonly<{ x: number; y: number; over: "h" | "v" }>;

/** What a belt keeps off: machine bodies, every label, where workers stand, and the development neighbourhood. */
export function beltObstacles(layout: SceneLayout): readonly SceneRect[] {
  const reach = WORKER_SIZE / 2, block = layout.facilities, inset = 12;
  return [...layout.stations.flatMap((station) => [station as SceneRect, station.label, { x: station.anchor.x - reach, y: station.anchor.y - reach, width: 2 * reach, height: 2 * reach }]),
    ...layout.regions.map((region) => regionLabelBox(region)), { x: block.x + inset, y: block.y + inset, width: block.width - 2 * inset, height: block.height - 2 * inset }];
}

/** Where a region's one name is drawn. */
export function regionLabelBox(region: SceneLayout["regions"][number]): SceneRect {
  return { x: region.label.x, y: region.label.y - 7, width: region.label.width, height: 9 };
}

/**
 * Every belt at once, on a square grid: shortest first, each from the pair of
 * ports (a machine's sides, its top when no label hangs there; never its
 * front) whose run is cheapest in length, bends and crossings. A run never
 * goes along another belt, so parallel belts lie side by side a cell apart, a
 * bundle, not a braid. It may cross one only straight through, at a right
 * angle, away from labels, ports, junctions and machines, and never where the
 * other turns. Undefined runs are drawn straight between their nearest sides.
 */
export function beltRoutes(layout: SceneLayout, pairs: readonly Readonly<{ key: string; from: SceneStation; to: SceneStation }>[]) {
  const obstacles = beltObstacles(layout), columns = Math.ceil(layout.width / BELT_CELL), rows = Math.ceil(layout.height / BELT_CELL), size = columns * rows;
  const square = (cell: number, by: number) => ({ x: (cell % columns) * BELT_CELL - by, y: Math.floor(cell / columns) * BELT_CELL - by, width: BELT_CELL + 2 * by, height: BELT_CELL + 2 * by });
  const blocked = new Uint8Array(size), near = new Uint8Array(size), occupied = new Uint8Array(size);
  const hitsBlocked = occupiedBy(obstacles), hitsNear = occupiedBy(obstacles, CROSSING_CLEARANCE - 2);
  for (let cell = 0; cell < size; cell++) { blocked[cell] = hitsBlocked(square(cell, 2)) ? 1 : 0; near[cell] = hitsNear(square(cell, 0)) ? 1 : 0; }
  const centre = (cell: number) => ({ x: (cell % columns) * BELT_CELL + BELT_CELL / 2, y: Math.floor(cell / columns) * BELT_CELL + BELT_CELL / 2 });
  const step = [1, -1, columns, -columns], DIRECTIONS = [0, 1, 2, 3];
  const valid = (cell: number, direction: number) => direction === 0 ? cell % columns !== columns - 1 : direction === 1 ? cell % columns !== 0 : direction === 2 ? cell + columns < size : cell - columns >= 0;
  const used = new Set<string>(), bridged = new Uint8Array(size);
  const labels = [...layout.stations.map((station) => station.label), ...layout.regions.map(regionLabelBox)];
  /** A machine's ports on one side: where a belt leaves its edge, the free cell it starts from, and the way out. */
  const portsOf = (station: SceneStation, side: 0 | 1 | 3) => {
    const outward = side, span = side === 3 ? [station.x + 3, station.x + station.width - 3] : [station.y + 3, station.y + station.height - 3];
    const across = (side === 3 ? station.x + station.width / 2 : station.y + station.height / 2), slots: { edge: ScenePoint; cell: number; out: number; key: string }[] = [];
    for (let line = Math.floor(span[0]! / BELT_CELL); line <= Math.floor(span[1]! / BELT_CELL); line++) {
      const at = Math.min(span[1]!, Math.max(span[0]!, line * BELT_CELL + BELT_CELL / 2));
      const edge = side === 0 ? { x: station.x + station.width, y: at } : side === 1 ? { x: station.x, y: at } : { x: at, y: station.y };
      let cell = side === 3 ? Math.floor((station.y - 1) / BELT_CELL) * columns + line : line * columns + Math.floor((side === 0 ? station.x + station.width + 1 : station.x - 1) / BELT_CELL);
      for (let tries = 0; tries < 5 && cell >= 0 && cell < size && blocked[cell] === 1; tries++) cell = valid(cell, outward) ? cell + step[outward]! : -1;
      // A port is clear of every label on its way out, and of any crossing near it.
      if (cell >= 0 && cell < size && blocked[cell] === 0 && bridged[cell] === 0 && labels.every((rect) => !crosses(edge, centre(cell), rect))) slots.push({ edge, cell, out: outward, key: `${station.key} ${side} ${line}` });
    }
    return slots.sort((a, b) => Math.abs((side === 3 ? a.edge.x : a.edge.y) - across) - Math.abs((side === 3 ? b.edge.x : b.edge.y) - across));
  };
  const sidesOf = (station: SceneStation, toward: ScenePoint): (0 | 1 | 3)[] => {
    const middle = { x: station.x + station.width / 2, y: station.y + station.height / 2 };
    const facing: 0 | 1 = toward.x >= middle.x ? 0 : 1, other: 0 | 1 = facing === 0 ? 1 : 0;
    const top = station.shape !== "dock" && station.shape !== "manifold";
    const order: (0 | 1 | 3)[] = toward.y < middle.y - station.height && top ? [3, facing, other] : [facing, ...(top ? [3 as const] : []), other];
    return order.filter((side) => !(side === 0 && station.shape === "gate"));
  };
  // One set of search arrays for every run; a generation stamp says which entries belong to this one.
  const states = size * 4, cost = new Float64Array(states), from = new Int32Array(states), stamp = new Uint32Array(states), done = new Uint32Array(states);
  let generation = 0;
  const search = (start: number, out: number, goal: number, arrive: number, shareRuns = false, anywhere = false, joins?: ReadonlyMap<number, number>) => {
    generation++;
    // Joining, a run crosses nothing within a junction's reach of where it might end.
    const joinNear = new Uint8Array(joins === undefined ? 0 : size);
    for (const cell of joins?.keys() ?? []) for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) { const column = cell % columns + dx, row = Math.floor(cell / columns) + dy; if (column >= 0 && row >= 0 && column < columns && row < rows) joinNear[row * columns + column] = 1; }
    const costOf = (state: number) => stamp[state] === generation ? cost[state]! : Infinity;
    const gx = goal % columns, gy = Math.floor(goal / columns), guess = (cell: number) => goal < 0 ? 0 : 1.5 * (Math.abs(cell % columns - gx) + Math.abs(Math.floor(cell / columns) - gy));
    const heap: [number, number][] = [];
    const less = (a: [number, number], b: [number, number]) => a[0] < b[0] || a[0] === b[0] && a[1] < b[1];
    const push = (item: [number, number]) => { heap.push(item); for (let i = heap.length - 1; i > 0;) { const parent = (i - 1) >> 1; if (!less(heap[i]!, heap[parent]!)) break; [heap[i], heap[parent]] = [heap[parent]!, heap[i]!]; i = parent; } };
    const pop = () => { const top = heap[0]!, last = heap.pop()!; if (heap.length > 0) { heap[0] = last; for (let i = 0; ;) { const l = 2 * i + 1, r = l + 1; let m = i; if (l < heap.length && less(heap[l]!, heap[m]!)) m = l; if (r < heap.length && less(heap[r]!, heap[m]!)) m = r; if (m === i) break; [heap[i], heap[m]] = [heap[m]!, heap[i]!]; i = m; } } return top; };
    const first = start * 4 + out;
    stamp[first] = generation; cost[first] = 0; from[first] = -1; push([guess(start), first]);
    for (let expanded = 0; heap.length > 0 && expanded < SEARCH_LIMIT; expanded++) {
      const [, state] = pop();
      if (done[state] === generation) continue;
      done[state] = generation;
      const cell = state >> 2, direction = state & 3;
      // A join ends square-on on another belt's straight run: a junction.
      const joined = joins?.has(cell) === true && cell !== start && (joins.get(cell)! & (direction < 2 ? H : V)) === 0;
      if (cell === goal && direction === arrive || joined) {
        const path = [state];
        while (from[path[0]!]! !== -1) path.unshift(from[path[0]!]!);
        return { cost: cost[state]!, cells: path.map((item) => item >> 2) };
      }
      // Straight through a crossing: no turning on another belt.
      const crossingHere = !anywhere && cell !== start && (occupied[cell]! & (direction < 2 ? V : H)) !== 0;
      for (const next of DIRECTIONS) {
        if (next === (direction ^ 1) || crossingHere && next !== direction || !valid(cell, next)) continue;
        const target = cell + step[next]!, along = next < 2 ? H : V, across = next < 2 ? V : H;
        if (blocked[target] === 1) continue;
        const ending = target === goal || joins?.has(target) === true && near[target] === 0 && bridged[target] === 0 && occupied[target] === joins.get(target) && (joins.get(target)! & along) === 0;
        const sharing = !ending && !anywhere && (occupied[target]! & along) !== 0;
        if (sharing && (!shareRuns || (occupied[target]! & across) !== 0)) continue;
        const crossing = !ending && !anywhere && !sharing && (occupied[target]! & across) !== 0;
        if (crossing && (near[target] === 1 || joinNear[target] === 1)) continue;
        const value = cost[state]! + 1 + (next === direction ? 0 : BEND) + (crossing ? CROSSING : 0) + (sharing ? 4 : 0), key = target * 4 + next;
        if (value < costOf(key)) { stamp[key] = generation; cost[key] = value; from[key] = state; push([value + guess(target), key]); }
      }
    }
    return undefined;
  };
  const routes = new Map<string, readonly ScenePoint[]>(), crossings: BeltCrossing[] = [], junctions: ScenePoint[] = [];
  // Each machine's laid belts, as the straight cells another belt may join square-on, with their direction.
  const runsAt = new Map<string, Map<number, number>>();
  const middle = (station: SceneStation) => ({ x: station.x + station.width / 2, y: station.y + station.height / 2 });
  const span = ({ from, to }: (typeof pairs)[number]) => Math.hypot(middle(from).x - middle(to).x, middle(from).y - middle(to).y);
  for (const pair of [...pairs].sort((left, right) => span(left) - span(right) || (left.key < right.key ? -1 : 1))) {
    let best: { cost: number; cells: number[]; start: { edge: ScenePoint; key: string }; end: { edge: ScenePoint; key: string } } | undefined;
    // The two sides facing each other first; every side only when those find no run.
    for (const reach of [2, 3]) for (const fromSide of best === undefined ? sidesOf(pair.from, middle(pair.to)).slice(0, reach) : []) for (const toSide of sidesOf(pair.to, middle(pair.from)).slice(0, reach)) {
      if (reach === 3 && sidesOf(pair.from, middle(pair.to)).indexOf(fromSide) < 2 && sidesOf(pair.to, middle(pair.from)).indexOf(toSide) < 2) continue;
      const start = portsOf(pair.from, fromSide).find((slot) => !used.has(slot.key)) ?? portsOf(pair.from, fromSide)[0];
      const end = portsOf(pair.to, toSide).find((slot) => !used.has(slot.key)) ?? portsOf(pair.to, toSide)[0];
      if (start === undefined || end === undefined || start.cell === end.cell) continue;
      // The facing sides are tried first; the first run found is kept.
      const found = best === undefined ? search(start.cell, start.out, end.cell, end.out ^ 1) : undefined;
      if (found !== undefined) best = { ...found, start, end };
    }
    // With every port taken, a run joins square-on a belt already serving the machine it is going to (or coming from): a junction.
    let reversed = false;
    for (const [here, there, flip] of [[pair.from, pair.to, false], [pair.to, pair.from, true]] as const) for (const side of best === undefined ? sidesOf(here, middle(there)) : []) {
      const start = portsOf(here, side).find((slot) => !used.has(slot.key)) ?? portsOf(here, side)[0], joins = runsAt.get(there.key);
      if (start === undefined || joins === undefined) continue;
      const found = search(start.cell, start.out, -1, -1, false, false, joins);
      if (found !== undefined) { best = { ...found, start, end: { edge: centre(found.cells.at(-1)!), key: "" } }; reversed = flip; }
    }
    // Hemmed in further, a run may join a laid belt's straight run as a trunk; failing even that, it is laid round machines
    // and labels alone, over whatever belts are there.
    for (const anywhere of [false, true]) for (const fromSide of best === undefined ? sidesOf(pair.from, middle(pair.to)) : []) for (const toSide of sidesOf(pair.to, middle(pair.from))) {
      const start = portsOf(pair.from, fromSide)[0], end = portsOf(pair.to, toSide)[0];
      if (start === undefined || end === undefined || start.cell === end.cell) continue;
      const found = search(start.cell, start.out, end.cell, end.out ^ 1, true, anywhere);
      if (found !== undefined && (best === undefined || found.cost < best.cost)) best = { ...found, start, end };
    }
    if (best === undefined) {
      const a = middle(pair.from), b = middle(pair.to);
      routes.set(pair.key, [{ x: b.x >= a.x ? pair.from.x + pair.from.width : pair.from.x, y: a.y }, { x: a.x > b.x ? pair.to.x + pair.to.width : pair.to.x, y: b.y }]);
      continue;
    }
    used.add(best.start.key); if (best.end.key !== "") used.add(best.end.key); else junctions.push(best.end.edge);
    const cells = best.cells;
    cells.forEach((cell, index) => {
      const before = cells[index - 1], after = cells[index + 1];
      const marks = (before === undefined ? 0 : Math.abs(cell - before) === 1 ? H : V) | (after === undefined ? 0 : Math.abs(after - cell) === 1 ? H : V);
      const crossed = index > 0 && index < cells.length - 1 && marks !== (H | V) && (occupied[cell]! & (marks === H ? V : H)) !== 0;
      if (crossed) {
        crossings.push({ ...centre(cell), over: marks === H ? "h" : "v" });
        for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) { const column = cell % columns + dx, row = Math.floor(cell / columns) + dy; if (column >= 0 && row >= 0 && column < columns && row < rows) bridged[row * columns + column] = 1; }
      }
      occupied[cell] = occupied[cell]! | (index === 0 || index === cells.length - 1 ? H | V : marks);
    });
    // Nothing crosses near a port: its cells join the floor kept clear of crossings.
    for (const end of [cells[0]!, cells.at(-1)!]) for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) {
      const column = end % columns + dx, row = Math.floor(end / columns) + dy;
      if (column >= 0 && row >= 0 && column < columns && row < rows) near[row * columns + column] = 1;
    }
    for (const station of [pair.from, pair.to]) {
      const runs = runsAt.get(station.key) ?? new Map<number, number>();
      cells.forEach((cell, index) => { if (index > 1 && index < cells.length - 2 && (occupied[cell] === H || occupied[cell] === V)) runs.set(cell, occupied[cell]!); });
      runsAt.set(station.key, runs);
    }
    // Only the corners are kept: a straight run is one leg.
    const corners = cells.filter((cell, index) => index === 0 || index === cells.length - 1 || (cell - cells[index - 1]!) !== (cells[index + 1]! - cell)).map(centre);
    const points = [best.start.edge, ...corners, ...(best.end.key === "" ? [] : [best.end.edge])];
    // A run laid from the far end is drawn the way material moves.
    routes.set(pair.key, reversed ? points.reverse() : points);
  }
  return { routes, crossings, junctions };
}

/** Whether a rectangle overlaps any of these, grown by `pad`. */
function occupiedBy(rects: readonly SceneRect[], pad = 0) {
  return (rect: SceneRect) => rects.some((other) => rect.x < other.x + other.width + pad && other.x - pad < rect.x + rect.width && rect.y < other.y + other.height + pad && other.y - pad < rect.y + rect.height);
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
