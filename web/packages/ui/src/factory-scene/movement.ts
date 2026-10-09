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
  const queue = heap();
  cost[start] = 0;
  queue.push(guess(start), start);
  while (queue.size() > 0) {
    const cell = queue.pop();
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
      if (step < cost[next]!) { cost[next] = step; from[next] = cell; queue.push(step + guess(next), next); }
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

// Belts run on their own grid, square: every crossing is a right angle, and two belts never share a run.
const BELT_CELL = 8, BEND = 3, CROSSING = 14, SLACK = 12, PER_RUN = 200;
// Nothing crosses this close to a label, a port, a junction or a machine.
export const CROSSING_CLEARANCE = 10;
const H = 1, V = 2;

export type BeltCrossing = Readonly<{ x: number; y: number; over: "h" | "v" }>;
/** One end of a connection drawn as stubs: out of its own port, naming the other end; the name is drawn only where it touches nothing. */
export type BeltStub = Readonly<{ points: readonly ScenePoint[]; name: string; text?: SceneRect & Readonly<{ value: string }> }>;
// A stub's name is set in a 6px monospace face, about this wide a character.
const STUB_CHAR = 3.7;
type Floor = Pick<SceneLayout, "width" | "height" | "stations" | "regions" | "facilities" | "fixtures">;

/** What a belt keeps off: machine bodies, every label, where workers stand, the fixtures and the outbound line. */
export function beltObstacles(layout: Floor): readonly SceneRect[] {
  const reach = WORKER_SIZE / 2, block = layout.facilities, inset = 12;
  return [...layout.stations.flatMap((station) => [station as SceneRect, station.label, { x: station.anchor.x - reach, y: station.anchor.y - reach, width: 2 * reach, height: 2 * reach }]),
    ...layout.fixtures.flatMap((piece) => [{ x: piece.x, y: piece.y, width: 20, height: 30 }, { x: piece.stand.x - reach, y: piece.stand.y - reach, width: 2 * reach, height: 2 * reach }]),
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
 * bundle, not a braid. It crosses one only straight through, at a right
 * angle, away from labels, ports, junctions and machines, and never where the
 * other turns. With every port of a machine taken it joins a belt serving it,
 * square-on, at a junction. Each run searches only round its two ends, and
 * all of them share a budget that grows with their number; a run that finds
 * no belt route within it is an overhead link instead: laid round machines
 * and labels alone, drawn faint and dashed, and kept out of the belt rules.
 * Links have a budget of their own; past it a connection is a pair of
 * labelled stubs, never dropped.
 */
export function beltRoutes(layout: Floor, pairs: readonly Readonly<{ key: string; from: SceneStation; to: SceneStation }>[]) {
  const obstacles = beltObstacles(layout), columns = Math.ceil(layout.width / BELT_CELL), rows = Math.ceil(layout.height / BELT_CELL), size = columns * rows;
  const raster = (rects: readonly SceneRect[], pad: number) => {
    const cells = new Uint8Array(size);
    for (const rect of rects) for (let row = Math.max(0, Math.floor((rect.y - pad) / BELT_CELL)); row < Math.min(rows, Math.ceil((rect.y + rect.height + pad) / BELT_CELL)); row++)
      for (let column = Math.max(0, Math.floor((rect.x - pad) / BELT_CELL)); column < Math.min(columns, Math.ceil((rect.x + rect.width + pad) / BELT_CELL)); column++) cells[row * columns + column] = 1;
    return cells;
  };
  const labels = [...layout.stations.map((station) => station.label), ...layout.regions.map(regionLabelBox)];
  const blocked = raster(obstacles, 2), near = raster(obstacles, CROSSING_CLEARANCE - 2), labelled = raster(labels, 0), occupied = new Uint8Array(size), bridged = new Uint8Array(size);
  const centre = (cell: number) => ({ x: (cell % columns) * BELT_CELL + BELT_CELL / 2, y: Math.floor(cell / columns) * BELT_CELL + BELT_CELL / 2 });
  const mark = (cells: Uint8Array, cell: number) => { for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) { const column = cell % columns + dx, row = Math.floor(cell / columns) + dy; if (column >= 0 && row >= 0 && column < columns && row < rows) cells[row * columns + column] = 1; } };
  const used = new Set<string>();
  /** A machine's ports on one side: where a belt leaves its edge, square to it, the free cell it starts from, and the way out. */
  const portsOf = (station: SceneStation, side: 0 | 1 | 3, belt = true) => {
    const span = side === 3 ? [station.x + 3, station.x + station.width - 3] : [station.y + 3, station.y + station.height - 3];
    const across = side === 3 ? station.x + station.width / 2 : station.y + station.height / 2, slots: { edge: ScenePoint; cell: number; out: number; key: string }[] = [];
    for (let line = Math.ceil((span[0]! - BELT_CELL / 2) / BELT_CELL); line * BELT_CELL + BELT_CELL / 2 <= span[1]!; line++) {
      const at = line * BELT_CELL + BELT_CELL / 2, edge = side === 0 ? { x: station.x + station.width, y: at } : side === 1 ? { x: station.x, y: at } : { x: at, y: station.y };
      const step = side === 0 ? 1 : side === 1 ? -1 : -columns;
      let cell = side === 3 ? Math.floor((station.y - 1) / BELT_CELL) * columns + line : line * columns + Math.floor((side === 0 ? station.x + station.width + 1 : station.x - 1) / BELT_CELL), clear = true;
      for (let tries = 0; tries < 5 && cell >= 0 && cell < size && blocked[cell] === 1; tries++) { clear &&= labelled[cell] === 0; cell += step; }
      // A port is clear of every label on its way out, and a belt's of any crossing near it.
      if (clear && cell >= 0 && cell < size && blocked[cell] === 0 && (!belt || bridged[cell] === 0) && labelled[cell] === 0) slots.push({ edge, cell, out: side, key: `${station.entityId} ${side} ${line}` });
    }
    return slots.sort((a, b) => Math.abs((side === 3 ? a.edge.x : a.edge.y) - across) - Math.abs((side === 3 ? b.edge.x : b.edge.y) - across));
  };
  const sidesOf = (station: SceneStation, toward: ScenePoint): (0 | 1 | 3)[] => {
    const middle = { x: station.x + station.width / 2, y: station.y + station.height / 2 };
    const facing: 0 | 1 = toward.x >= middle.x ? 0 : 1, other: 0 | 1 = facing === 0 ? 1 : 0, top = station.shape !== "dock" && station.shape !== "manifold";
    const order: (0 | 1 | 3)[] = toward.y < middle.y - station.height && top ? [3, facing, other] : [facing, ...(top ? [3 as const] : []), other];
    return order.filter((side) => !(side === 0 && station.shape === "gate"));
  };
  let spent = 0, linked = 0, generation = 0, cost = new Float64Array(0), from = new Int32Array(0), stamp = new Uint32Array(0), done = new Uint32Array(0);
  const budget = 150_000 + PER_RUN * pairs.length;
  /**
   * One run, searched only in a window round its ends. Modes: 0 keeps every belt rule; 1 may also run along a laid belt's
   * straight run as a trunk; 2 is an overhead link, which keeps off machines and labels and nothing else.
   */
  const search = (start: number, out: number, goal: number, arrive: number, mode: 0 | 1 | 2, joins?: ReadonlyMap<number, number>, slack = SLACK) => {
    if (mode < 2 ? spent > budget : linked > budget) return undefined;
    const ends = [start, ...(goal >= 0 ? [goal] : [...(joins?.keys() ?? [])])];
    const x0 = Math.max(0, Math.min(...ends.map((cell) => cell % columns)) - slack), x1 = Math.min(columns - 1, Math.max(...ends.map((cell) => cell % columns)) + slack);
    const y0 = Math.max(0, Math.min(...ends.map((cell) => Math.floor(cell / columns))) - slack), y1 = Math.min(rows - 1, Math.max(...ends.map((cell) => Math.floor(cell / columns))) + slack);
    const width = x1 - x0 + 1, local = (cell: number) => (Math.floor(cell / columns) - y0) * width + cell % columns - x0, states = width * (y1 - y0 + 1) * 4;
    // One set of search arrays for every run, grown as needed; a generation stamp says which entries are this run's.
    if (cost.length < states) { cost = new Float64Array(states); from = new Int32Array(states); stamp = new Uint32Array(states); done = new Uint32Array(states); }
    generation++;
    const costOf = (key: number) => stamp[key] === generation ? cost[key]! : Infinity;
    const nearJoin = (cell: number) => { for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) if (joins?.has(cell + dy * columns + dx)) return true; return false; };
    const gx = goal % columns, gy = Math.floor(goal / columns), guess = (cell: number) => goal < 0 ? 0 : 1.5 * (Math.abs(cell % columns - gx) + Math.abs(Math.floor(cell / columns) - gy));
    const queue = heap(), first = start * 4 + out;
    stamp[local(start) * 4 + out] = generation; cost[local(start) * 4 + out] = 0; from[local(start) * 4 + out] = -1; queue.push(guess(start), first);
    // A run that has not arrived within a few times its own length is not going to: each run's work is bounded by its length.
    const cap = Math.min(states, 80 * (width + y1 - y0 + 1));
    for (let expanded = 0; queue.size() > 0 && expanded < cap; expanded++) {
      if (mode < 2 ? spent++ > budget : linked++ > budget) return undefined;
      const state = queue.pop(), cell = state >> 2, direction = state & 3, here = local(cell) * 4 + direction;
      if (done[here] === generation) continue;
      done[here] = generation;
      // A join ends square-on on another belt's straight run: a junction.
      const joined = joins?.has(cell) === true && cell !== start && (joins.get(cell)! & (direction < 2 ? H : V)) === 0;
      if (cell === goal && direction === arrive || joined) {
        const path = [state];
        for (let back = from[here]!; back !== -1; back = from[local(back >> 2) * 4 + (back & 3)]!) path.unshift(back);
        return { cost: cost[here]!, cells: path.map((item) => item >> 2) };
      }
      // Straight through a crossing: no turning on another belt.
      const crossingHere = mode < 2 && cell !== start && (occupied[cell]! & (direction < 2 ? V : H)) !== 0;
      for (const next of [0, 1, 2, 3]) {
        const column = cell % columns + (next === 0 ? 1 : next === 1 ? -1 : 0), row = Math.floor(cell / columns) + (next === 2 ? 1 : next === 3 ? -1 : 0);
        if (next === (direction ^ 1) || crossingHere && next !== direction || column < x0 || column > x1 || row < y0 || row > y1) continue;
        const target = row * columns + column, along = next < 2 ? H : V, across = next < 2 ? V : H;
        if (blocked[target] === 1) continue;
        const ending = target === goal || joins?.has(target) === true && near[target] === 0 && bridged[target] === 0 && occupied[target] === joins.get(target) && (joins.get(target)! & along) === 0;
        const sharing = !ending && mode < 2 && (occupied[target]! & along) !== 0;
        if (sharing && (mode === 0 || (occupied[target]! & across) !== 0)) continue;
        const crossing = !ending && mode < 2 && !sharing && (occupied[target]! & across) !== 0;
        if (crossing && (near[target] === 1 || joins !== undefined && nearJoin(target))) continue;
        const value = cost[here]! + 1 + (next === direction ? 0 : BEND) + (crossing ? CROSSING : 0) + (sharing ? 4 : 0), key = local(target) * 4 + next;
        if (value < costOf(key)) { stamp[key] = generation; cost[key] = value; from[key] = state; queue.push(value + guess(target), target * 4 + next); }
      }
    }
    return undefined;
  };
  const routes = new Map<string, readonly ScenePoint[]>(), crossings: BeltCrossing[] = [], junctions: ScenePoint[] = [], links = new Set<string>(), stubs = new Map<string, readonly (BeltStub | undefined)[]>();
  const trail = new Uint8Array(size), texted = new Uint8Array(size), stubbed: { key: string; ends: ({ points: ScenePoint[]; out: number; name: string } | undefined)[] }[] = [];
  // Each machine's laid belts, as the straight cells another belt may join square-on, with their direction.
  const runsAt = new Map<string, Map<number, number>>();
  const middle = (station: SceneStation) => ({ x: station.x + station.width / 2, y: station.y + station.height / 2 });
  const span = ({ from, to }: (typeof pairs)[number]) => Math.hypot(middle(from).x - middle(to).x, middle(from).y - middle(to).y);
  for (const pair of [...pairs].sort((left, right) => span(left) - span(right) || (left.key < right.key ? -1 : 1))) {
    type Found = { cost: number; cells: number[]; start: { edge: ScenePoint; key: string }; end: { edge: ScenePoint; key: string } };
    let best: Found | undefined, reversed = false, link = false;
    const slot = (station: SceneStation, side: 0 | 1 | 3) => portsOf(station, side).find((item) => !used.has(item.key));
    // The two sides facing each other first; every side only when those find no run.
    for (const reach of [2, 3]) for (const fromSide of best === undefined ? sidesOf(pair.from, middle(pair.to)).slice(0, reach) : []) for (const toSide of sidesOf(pair.to, middle(pair.from)).slice(0, reach)) {
      if (best !== undefined || reach === 3 && sidesOf(pair.from, middle(pair.to)).indexOf(fromSide) < 2 && sidesOf(pair.to, middle(pair.from)).indexOf(toSide) < 2) continue;
      const start = slot(pair.from, fromSide), end = slot(pair.to, toSide);
      if (start === undefined || end === undefined || start.cell === end.cell) continue;
      const found = search(start.cell, start.out, end.cell, end.out ^ 1, 0);
      if (found !== undefined) best = { ...found, start, end };
    }
    // With every port taken, a run joins square-on a belt already serving the machine it is going to (or coming from).
    for (const [here, there, flip] of [[pair.from, pair.to, false], [pair.to, pair.from, true]] as const) for (const side of best === undefined ? sidesOf(here, middle(there)).slice(0, 2) : []) {
      const start = slot(here, side);
      const joins = runsAt.get(there.entityId) ?? new Map<number, number>();
      if (best !== undefined || start === undefined || joins.size === 0) continue;
      const found = search(start.cell, start.out, -1, -1, 0, joins);
      if (found !== undefined) { best = { ...found, start, end: { edge: centre(found.cells.at(-1)!), key: "" } }; reversed = flip; }
    }
    // Past that, or past the budget, it is an overhead link.
    for (const reach of [1, 3]) for (const fromSide of best === undefined ? sidesOf(pair.from, middle(pair.to)).slice(0, reach) : []) for (const toSide of sidesOf(pair.to, middle(pair.from)).slice(0, reach)) {
      const start = portsOf(pair.from, fromSide, false)[0], end = portsOf(pair.to, toSide, false)[0];
      if (best !== undefined || start === undefined || end === undefined || start.cell === end.cell) continue;
      const found = search(start.cell, start.out, end.cell, end.out ^ 1, 2, undefined, reach === 1 ? SLACK : 4 * SLACK);
      if (found !== undefined) best = { ...found, start, end };
      link = true;
    }
    // Past both budgets a connection is still drawn: a short stub out of a free port of each machine, its own, each to be
    // named after the other end once everything else is laid.
    if (best === undefined) {
      // A machine with every port taken shows no stub; the other end's stub still names it. Only when neither has a port
      // left does one share a port, so the connection is never dropped.
      const ends = ([[pair.from, pair.to], [pair.to, pair.from]] as const).map(([station, toward], index, both) => {
        const ports = sidesOf(station, middle(toward)).flatMap((side) => portsOf(station, side, false)), free = (item: (typeof ports)[number]) => !used.has(item.key) && occupied[item.cell] === 0;
        const port = ports.find(free) ?? (index === 0 && !sidesOf(both[1]![0], middle(both[1]![1])).flatMap((side) => portsOf(both[1]![0], side, false)).some(free) ? ports[0] : undefined);
        if (port === undefined) return undefined;
        used.add(port.key); occupied[port.cell] = H | V; mark(near, port.cell);
        return { points: [port.edge, centre(port.cell)], out: port.out, name: toward.machine.label };
      });
      stubbed.push({ key: pair.key, ends });
      continue;
    }
    const cells = best.cells;
    // Only the corners are kept: a straight run is one leg.
    const corners = cells.filter((cell, index) => index === 0 || index === cells.length - 1 || (cell - cells[index - 1]!) !== (cells[index + 1]! - cell)).map(centre);
    const points = [best.start.edge, ...corners, ...(best.end.key === "" ? [] : [best.end.edge])];
    // A run laid from the far end is drawn the way material moves.
    routes.set(pair.key, reversed ? points.reverse() : points);
    if (link) { links.add(pair.key); for (const cell of cells) trail[cell] = 1; continue; }
    used.add(best.start.key); if (best.end.key !== "") used.add(best.end.key); else junctions.push(best.end.edge);
    cells.forEach((cell, index) => {
      const before = cells[index - 1], after = cells[index + 1];
      const marks = (before === undefined ? 0 : Math.abs(cell - before) === 1 ? H : V) | (after === undefined ? 0 : Math.abs(after - cell) === 1 ? H : V);
      if (index > 0 && index < cells.length - 1 && marks !== (H | V) && (occupied[cell]! & (marks === H ? V : H)) !== 0) {
        crossings.push({ ...centre(cell), over: marks === H ? "h" : "v" });
        mark(bridged, cell);
      }
      occupied[cell] = occupied[cell]! | (index === 0 || index === cells.length - 1 ? H | V : marks);
    });
    // Nothing crosses near a port or a junction.
    for (const end of [cells[0]!, cells.at(-1)!]) mark(near, end);
    // Only a belt's cells near the machine are kept for joining, a bounded few: a junction is where belts meet it, not across the floor.
    for (const station of [pair.from, pair.to]) {
      const runs = runsAt.get(station.entityId) ?? new Map<number, number>(), at = middle(station), reach = 2 * SLACK * BELT_CELL;
      cells.forEach((cell, index) => { if (runs.size < 64 && index > 1 && index < cells.length - 2 && (occupied[cell] === H || occupied[cell] === V) && Math.abs(centre(cell).x - at.x) + Math.abs(centre(cell).y - at.y) <= reach) runs.set(cell, occupied[cell]!); });
      runsAt.set(station.entityId, runs);
    }
  }
  // A stub's name goes where it touches nothing: no machine, label, belt, link or other name. A few spots by its end are
  // tried, then a shorter name; failing all, the name is left to the stub's title.
  const free = (box: SceneRect) => {
    if (box.x < 0 || box.y < 0 || box.x + box.width > layout.width || box.y + box.height > layout.height) return false;
    for (let row = Math.floor(box.y / BELT_CELL); row <= Math.floor((box.y + box.height) / BELT_CELL); row++) for (let column = Math.floor(box.x / BELT_CELL); column <= Math.floor((box.x + box.width) / BELT_CELL); column++) {
      const cell = row * columns + column;
      if (blocked[cell] === 1 || occupied[cell] !== 0 || trail[cell] === 1 || texted[cell] === 1) return false;
    }
    return true;
  };
  // Ends stay in order, from then to; a machine with no port left has none.
  for (const { key, ends } of stubbed) stubs.set(key, ends.map((end) => {
    if (end === undefined) return undefined;
    const at = end.points[1]!, full = `→ ${end.name}`;
    for (const value of [full.length > 16 ? `${full.slice(0, 15)}…` : full, full.length > 8 ? `${full.slice(0, 7)}…` : undefined]) {
      if (value === undefined) continue;
      const width = value.length * STUB_CHAR, height = 7;
      const spots = end.out === 0 ? [[at.x + 6, at.y - 4], [at.x + 6, at.y - 12], [at.x + 6, at.y + 4]] : end.out === 1 ? [[at.x - 6 - width, at.y - 4], [at.x - 6 - width, at.y - 12], [at.x - 6 - width, at.y + 4]]
        : [[at.x - width / 2, at.y - 14], [at.x + 6, at.y - 10], [at.x - 6 - width, at.y - 10]];
      const spot = spots.map(([x, y]) => ({ x: Math.round(x!), y: Math.round(y!), width, height })).find(free);
      if (spot === undefined) continue;
      for (let row = Math.floor(spot.y / BELT_CELL); row <= Math.floor((spot.y + spot.height) / BELT_CELL); row++) for (let column = Math.floor(spot.x / BELT_CELL); column <= Math.floor((spot.x + spot.width) / BELT_CELL); column++) texted[row * columns + column] = 1;
      return { points: end.points, name: end.name, text: { ...spot, value } };
    }
    return { points: end.points, name: end.name };
  }));
  return { routes, crossings, junctions, links, stubs };
}

/** A binary heap of [estimate, id] in two flat arrays; ties go to the lower id, so a route never depends on timing. Estimates are halves, so exact comparison is safe. */
function heap() {
  const keys: number[] = [], ids: number[] = [];
  const less = (a: number, b: number) => keys[a]! < keys[b]! || keys[a] === keys[b] && ids[a]! < ids[b]!;
  const swap = (a: number, b: number) => { [keys[a], keys[b]] = [keys[b]!, keys[a]!]; [ids[a], ids[b]] = [ids[b]!, ids[a]!]; };
  return {
    size: () => keys.length,
    push(key: number, id: number) { keys.push(key); ids.push(id); for (let i = keys.length - 1; i > 0;) { const parent = (i - 1) >> 1; if (!less(i, parent)) break; swap(i, parent); i = parent; } },
    /** The id of the least entry, taken off. */
    pop(): number {
      const top = ids[0]!, key = keys.pop()!, id = ids.pop()!;
      if (keys.length > 0) { keys[0] = key; ids[0] = id; for (let i = 0; ;) { const l = 2 * i + 1, r = l + 1; let m = i; if (l < keys.length && less(l, m)) m = l; if (r < keys.length && less(r, m)) m = r; if (m === i) break; swap(i, m); i = m; } }
      return top;
    },
  };
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
