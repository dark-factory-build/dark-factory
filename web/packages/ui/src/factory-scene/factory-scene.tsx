import { useLayoutEffect, useEffect, useMemo, useRef, useState, type MouseEvent, type FocusEvent, type PointerEvent, type KeyboardEvent, type ReactNode } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  compareText,
  ROOM_LEFT,
  layoutScene,
  commonSeating,
  WORKER_SIZE,
  placeWorkers,
  placeErrands,
  breakRoomNook,
  inventoryLabels,
  type RoomContent,
  type SceneRoomLayout,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { breakRoomHabit, restingItem, workerFrames, workerPhase } from "./appearance.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
  appearance?: FloorAppearance;
  topology: SceneTopology;
  detailNodes?: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  workers: readonly SceneWorker[];
  tasks?: readonly SceneTask[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onSelectHumanRequest?: (requestId: string) => void;
  /** A dropped session reconciles to its latest snapshot instead of replaying local motion. */
  connected?: boolean;
  /** Current changed locations omitted by the bounded room map. */
  omittedLocations?: number;
  /** Served stable identities with children in the current hierarchy scope. */
  enterableRoomIds?: readonly string[];
  /** Presentation-only hierarchy navigation; it has no factory authority. */
  onEnterRoom?: (roomId: string) => void;
  /** The selected agent is highlighted without changing its deterministic placement. */
  selectedWorkerId?: string;
  /** Pointer convenience only; the AGENTS list is the keyboard path. */
  onSelectWorker?: (workerId: string) => void;
}>;

export type AgentSpriteProps = Readonly<{
  agent: Pick<SceneWorker, "id" | "name" | "role" | "provider" | "appearance">;
  activity: SceneWorker["activity"];
}>;

const FRAME = spriteAtlas.frame;
// Tables stand this far below a seat's centre: over the lap, under the hands.
const TABLE_DROP = 29;
// What rests on the table is the chest-height drawing, set down this many sprite pixels.
const SET_DOWN = 6;
const LABEL_SEGMENTS = new Intl.Segmenter("en", { granularity: "grapheme" });

function shortLabel(label: string, limit = 18) {
  const glyphs = Array.from(LABEL_SEGMENTS.segment(label), ({ segment }) => segment);
  // Reserve two columns for non-Latin glyphs and the ellipsis; font fallback
  // can render them full-width even inside a monospace label.
  const columns = (glyph: string) => /[^\u0000-\u00ff]/u.test(glyph) ? 2 : 1;
  if (glyphs.reduce((width, glyph) => width + columns(glyph), 0) <= limit) return label;
  let width = 0;
  return `${glyphs.filter((glyph) => (width += columns(glyph)) <= limit - 2).join("")}…`;
}

/** One 16px frame of the sheet, sized and placed in scene coordinates. */
function Frame({ name, x, y, className }: { name: string; x: number; y: number; className?: string }) {
  return <use href={`#df-frame-${name}`} x={x} y={y} width={FRAME} height={FRAME} className={className} />;
}

/** A standalone crop of the shared sheet for lists and detail panels. */
export function AgentSprite({ agent, activity }: AgentSpriteProps) {
  const frames = workerFrames({ ...agent, activity });
  return <svg viewBox={`0 0 ${FRAME} ${FRAME}`} role="img" aria-label={`${agent.name}, ${agent.role}, ${activity}`} className="dfAgentSprite">
    {frames.map((frame) => { const cell = spriteAtlas.frames[frame as keyof typeof spriteAtlas.frames]; return <image key={frame} href={spriteSheet} x={-cell.x} y={-cell.y} width={spriteSheetSize.width} height={spriteSheetSize.height} style={{ imageRendering: "pixelated" }} />; })}
  </svg>;
}

type MotionState = Readonly<{
  placement: ReturnType<typeof placeWorkers>[number];
  point: { x: number; y: number };
  route?: ReturnType<typeof routeBetween>;
  startedAt?: number;
}>;

const WALK_SPEED = 96;

function now() { return typeof performance === "undefined" ? 0 : performance.now(); }

function motionPoint(motion: MotionState, at: number) {
  if (motion.route === undefined || motion.startedAt === undefined) return { point: motion.point, walking: false };
  const travelled = Math.max(0, (at - motion.startedAt) / 1000 * WALK_SPEED);
  if (travelled >= motion.route.length) return { point: pointOnRoute(motion.point, motion.route, motion.route.length), walking: false };
  let previous = motion.point;
  let remaining = travelled;
  for (const point of motion.route.points) {
    const length = Math.hypot(point.x - previous.x, point.y - previous.y);
    if (remaining <= length) return { point: pointOnRoute(motion.point, motion.route, travelled), walking: true, direction: directionBetween(previous, point) };
    remaining -= length;
    previous = point;
  }
  return { point: motion.placement, walking: false };
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  return reduced;
}

/** One browser clock; source state only ever supplies the next local destination. */
function useSceneMotion(layout: ReturnType<typeof layoutScene>, placements: ReturnType<typeof placeWorkers>, topologyDigest: string, connected: boolean, reduced: boolean, active: ReadonlySet<string>) {
  const motions = useRef(new Map<string, MotionState>());
  const priorTopology = useRef<string | undefined>(undefined);
  const priorConnected = useRef<boolean | undefined>(undefined);
  const [clock, setClock] = useState(0);

  useEffect(() => {
    const at = now();
    const hidden = typeof document !== "undefined" && document.visibilityState !== "visible";
    const topologyChanged = priorTopology.current !== undefined && priorTopology.current !== topologyDigest;
    const reconnected = priorConnected.current === false && connected;
    priorTopology.current = topologyDigest;
    priorConnected.current = connected;
    const previous = motions.current;
    const next = new Map<string, MotionState>();
    for (const placement of placements) {
      const old = previous.get(placement.id);
      if (old === undefined || reduced || hidden || topologyChanged || reconnected || !connected) {
        next.set(placement.id, { placement, point: placement });
        continue;
      }
      const current = motionPoint(old, at).point;
      const unchanged = old.placement.area === placement.area && old.placement.roomId === placement.roomId && samePoint(old.placement, placement);
      if (unchanged) {
        next.set(placement.id, motionPoint(old, at).walking ? { ...old, point: old.point } : { placement, point: placement });
        continue;
      }
      const route = routeFromCurrent(layout, current, placement);
      next.set(placement.id, route === undefined || route.length === 0
        ? { placement, point: placement }
        : { placement, point: current, route, startedAt: at });
    }
    motions.current = next;
    setClock(at);
  }, [connected, layout, placements, reduced, topologyDigest]);

  useEffect(() => {
    if (!connected || reduced || typeof document !== "undefined" && document.visibilityState !== "visible" || typeof requestAnimationFrame !== "function") return;
    const at = now();
    if ([...motions.current.values()].some((motion) => motionPoint(motion, at).walking)) {
      const frame = requestAnimationFrame((time) => setClock(time));
      return () => cancelAnimationFrame(frame);
    }
    // Blinks, sips and waves are short, so the floor keeps a slow pulse while anyone is on it.
    if (placements.length === 0) return;
    const timer = setTimeout(() => setClock(now()), 200);
    return () => clearTimeout(timer);
  }, [clock, connected, reduced, placements]);

  useEffect(() => {
    if (typeof document === "undefined") return;
    const reconcile = () => {
      motions.current = new Map([...motions.current].map(([id, motion]) => [id, { placement: motion.placement, point: motion.placement }]));
      setClock(now());
    };
    document.addEventListener("visibilitychange", reconcile);
    return () => document.removeEventListener("visibilitychange", reconcile);
  }, []);

  const output = new Map<string, Readonly<{ x: number; y: number; motion: WorkerMotion }>>();
  for (const placement of placements) {
    const state = motions.current.get(placement.id);
    const current = state === undefined ? { point: placement, walking: false } : motionPoint(state, clock);
    const at = connected && !reduced && (typeof document === "undefined" || document.visibilityState === "visible") ? clock + workerPhase(placement.id) : undefined;
    output.set(placement.id, {
      ...current.point,
      motion: current.walking
        ? { action: "walking", direction: current.direction!, frame: Math.floor((at ?? clock) / 150) % 2 as 0 | 1, at }
        : { action: active.has(placement.id) ? "interacting" : "still", frame: at === undefined ? 0 : Math.floor(at / (380 + workerPhase(placement.id) % 140)) % 2 as 0 | 1, at },
    });
  }
  return { positions: output, pulse: connected && !reduced && (typeof document === "undefined" || document.visibilityState === "visible") ? clock : undefined };
}

/** The animation clock updates worker elements without rerendering the floor or atlas. */
function SceneWorkers({ errands, furniture, layout, placements, nodes, workers, tasks, connected, animate, selectedWorkerId, onSelectWorker, onSelectHumanRequest }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectHumanRequest"> & {
  layout: ReturnType<typeof layoutScene>;
  placements: ReturnType<typeof placeWorkers>;
  nodes: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  tasks: readonly SceneTask[];
  connected: boolean;
  animate: boolean;
  /** Drawn over the workers: tables stand in front of whoever sits at them. */
  furniture: ReactNode;
  /** Whether the break-room furniture is there to be visited. */
  errands: boolean;
}) {
  // Inventory/dependency metadata may change without changing a route's geometry.
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.restingTop, layout.corridors, layout.rooms.map(({ id, x, y, width, height, door }) => [id, x, y, width, height, door])]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && placements.some((placement) => placement.id === worker.id && placement.area === "room" && layout.rooms.find((room) => room.id === placement.roomId)?.contents.some((item) => item.workSurface))).map((worker) => worker.id)), [workers, placements, layout]);
  // The break room keeps no clock of its own: every couple of seconds of the
  // floor's pulse it asks who has got up. Where motion is stilled the pulse is
  // absent, and everyone stays seated.
  // The count belongs to the floor it was taken on: one left over from another floor is ignored at once.
  const [errandBeat, setErrandBeat] = useState<Readonly<{ floor: string; clock: number }>>();
  // It counts only while this floor is moving, judged in the render that uses it: the moment
  // motion is stilled by any means, or another floor is shown, everyone is back in their seat.
  const reduced = useReducedMotion();
  const moving = connected && animate && !reduced && (typeof document === "undefined" || document.visibilityState === "visible");
  const errandClock = moving && errandBeat?.floor === geometryKey ? errandBeat.clock : undefined;
  const seatedPlacements = placements;
  placements = useMemo(() => {
    const nook = breakRoomNook(layout, seatedPlacements.filter((placement) => placement.area === "resting").length, seatedPlacements.filter((placement) => placement.area !== "room" && placement.area !== "resting").length);
    // With the scenery off there is no furniture to walk to.
    if (errandClock === undefined || !errands) return seatedPlacements;
    const byId = new Map(workers.map((worker) => [worker.id, worker]));
    return placeErrands(seatedPlacements, nook, (id) => { const worker = byId.get(id); return worker === undefined ? undefined : breakRoomHabit(worker); }, errandClock);
  }, [seatedPlacements, errandClock, errands, layout, workers]);
  const { positions, pulse } = useSceneMotion(layout, placements, geometryKey, connected && animate, reduced, active);
  // Turns run on the time this floor has actually been moving: a hidden tab, stilled
  // motion or a long gap adds nothing, and a different floor starts again from seated.
  const movingTime = useRef({ floor: geometryKey, total: 0, last: undefined as number | undefined });
  useEffect(() => {
    const time = movingTime.current;
    if (time.floor !== geometryKey) Object.assign(time, { floor: geometryKey, total: 0, last: undefined });
    if (pulse !== undefined && time.last !== undefined && pulse - time.last < 1000) time.total += pulse - time.last;
    time.last = pulse;
    const clock = Math.floor(time.total / 2000) * 2000;
    setErrandBeat((beat) => pulse === undefined ? undefined : beat?.floor === geometryKey && beat.clock === clock ? beat : { floor: geometryKey, clock });
  }, [pulse, geometryKey]);
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  return <>{placements.map((placement) => {
        const worker = workerById.get(placement.id);
        if (worker === undefined) return null;
        const position = positions.get(placement.id) ?? { ...placement, motion: { action: "still", frame: 0 } as WorkerMotion };
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const picturedSurface = layout.rooms.find((candidate) => candidate.id === placement.roomId)?.contents.some((item) => item.workSurface);
        const location = worker.location === "working"
          ? `representative location${worker.locationWithin ? " within this component; more specific observed area" : " near observed changes"}${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? picturedSurface ? "at the pictured work surface" : "at a general work position; inventory unavailable or empty" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
          : worker.location === "unobserved" ? "working; location not yet observed"
          : worker.location === "last-observed" && worker.locationLabel !== undefined ? `last observed near changes in ${worker.locationLabel}; resting area`
          : worker.paused ? "paused in resting area" : "ready in resting area";

        const attention = tasks.flatMap((order) => order.agentId === worker.id ? order.humanRequestIds : []);
        const seated = placement.area !== "room" && placement.errand === undefined && position.motion.action !== "walking";
        const frames = workerFrames(worker, position.motion, placement.area === "room" || placement.errand !== undefined ? undefined : placement.area === "resting" ? "resting" : "planning", placement.errand);
        // A step lifts the whole body a pixel.
        const bob = position.motion.action === "walking" && position.motion.frame === 1 ? -1 : 0;
        // Only a profile facing east is drawn; walking west is its mirror image.
        const facingWest = position.motion.action === "walking" && position.motion.direction === "west";
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-worker-location={worker.location ?? "resting"}
            data-worker-action={position.motion.action}
            data-worker-facing={position.motion.action === "walking" ? position.motion.direction : undefined}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g role="img" className="dfFactoryScene__target" data-tooltip={`${worker.name} · ${worker.activity}\n${placement.area === "room" ? `Working near ${worker.locationLabel ?? room?.label ?? "observed changes"}` : placement.errand === "shelf" ? "Taking a break · at the bookshelf" : placement.errand === "coffee" ? "Taking a break · at the coffee station" : placement.area === "resting" ? worker.paused ? "Paused · taking a break" : "Taking a break" : worker.location === "unobserved" ? "Planning · location not yet observed" : "Planning · work outside this room"}`} aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...sceneAction(onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
                <rect className="dfFactoryScene__focus" x={-12} y={-12} width="24" height="24" rx="3" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-seated={seated ? placement.area === "resting" ? "coffee" : "planning" : undefined} data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}><g transform={`scale(${WORKER_SIZE / FRAME})${bob === 0 ? "" : ` translate(0 ${bob})`}${facingWest ? " scale(-1 1)" : ""}`}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}</g>
              </g>
            </g>
            {attention.length === 0 ? null : <g {...sceneAction(onSelectHumanRequest === undefined ? undefined : () => onSelectHumanRequest(attention[0]!))} aria-label={`Question from ${worker.name}`} data-human-request-id={attention[0]}>
              <rect x="10" y="-20" width="22" height="22" rx="3" fill="#f0c777" /><text x="21" y="-5" textAnchor="middle" fill="#172330" fontSize="16" fontWeight="700">!</text>
            </g>}
          </g>
        );
      })}
      {furniture}
      {/* On the table in front of each of them: a planner's lit lamp, or the one thing a resting worker has until it is in their hand. */}
      {placements.map((placement) => {
        const worker = workerById.get(placement.id), position = positions.get(placement.id);
        if (worker === undefined || placement.area === "room" || placement.errand !== undefined || position?.motion.action === "walking") return null;
        if (placement.area !== "resting") return !connected ? null : <g key={placement.id} data-planning-light="" aria-hidden="true" pointerEvents="none" transform={`translate(${position?.x ?? placement.x} ${(position?.y ?? placement.y) + TABLE_DROP})`}><circle cx="7" cy="-16" r="14" fill="url(#df-lamplight)" /><path d="M12 -18v-5h-5" fill="none" stroke="#788379" strokeWidth="2" /><path d="M4 -20h6" stroke="#dfc38f" strokeWidth="3" /></g>;
        const rest = restingItem(worker, worker.activity === "needs-you" ? undefined : position?.motion.at);
        return rest.where !== "table" ? null : <g key={placement.id} aria-hidden="true" pointerEvents="none" data-table-item={rest.item} transform={`translate(${position?.x ?? placement.x} ${position?.y ?? placement.y}) scale(${WORKER_SIZE / FRAME})`}>
          <Frame name={`person.held.${rest.item}.chest`} x={-8} y={-8 + SET_DOWN} />
        </g>;
      })}</>;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ topology, detailNodes, workers, appearance = DEFAULT_FLOOR_APPEARANCE, omittedLocations = 0, enterableRoomIds = [], onEnterRoom, selectedWorkerId, onSelectWorker, tasks = [], selectedTaskId, onSelectTask, onSelectHumanRequest, connected = true }: FactorySceneProps) {
  const [selectedRoomId, setSelectedRoomId] = useState<string>();
  const [tooltip, setTooltip] = useState<{ text: string; x: number; y: number; top: number; bottom: number; room: number }>();
  const tooltipElement = useRef<HTMLDivElement>(null);
  const [linkedFrom, setLinkedFrom] = useState<string>();
  useLayoutEffect(() => {
    if (!tooltip || !tooltipElement.current) return;
    const height = tooltipElement.current.getBoundingClientRect().height;
    const below = window.innerHeight - 8 - tooltip.bottom, above = tooltip.top - 8; // take the side with room and shrink to it, so a tooltip never covers the target it describes
    const [y, room] = height <= below || below >= above ? [tooltip.bottom, below] : [Math.max(8, tooltip.top - height), above];
    if (y !== tooltip.y || room !== tooltip.room) setTooltip({ ...tooltip, y, room });
  }, [tooltip]);
  const showTooltip = (target: Element | null) => {
    if (!target) { setTooltip(undefined); return; }
    const box = (target.querySelector("rect") ?? target).getBoundingClientRect();
    setTooltip({ text: target.getAttribute("data-tooltip")!, x: Math.max(8, Math.min(box.left, window.innerWidth - 272)), y: Math.max(8, box.bottom), top: box.top, bottom: box.bottom, room: window.innerHeight });
  };
  const retainFocusedTooltip = () => showTooltip(typeof document !== "undefined" && document.activeElement?.matches("[data-tooltip]") ? document.activeElement : null);
  const inspect = (event: PointerEvent<HTMLDivElement> | FocusEvent<HTMLDivElement> | MouseEvent<HTMLDivElement>) => {
    const element = event.target as Element;
    showTooltip(element.closest("[data-tooltip]"));
    setLinkedFrom(element.closest("[data-room-id]")?.getAttribute("data-room-id") ?? undefined);
  };
  const layout = useMemo(() => layoutScene(topology), [topology]);
  const placements = useMemo(() => placeWorkers(layout, workers), [layout, workers]);
  const nodes = new Map(topology.nodes.map((node) => [node.id, node]));
  const availableNodes = detailNodes ?? nodes;
  const selectedRoom = availableNodes.get(selectedRoomId ?? "");
  const links = selectedRoom?.dependencies?.links ?? [];
  const resting = placements.filter((placement) => placement.area === "resting");
  const planning = placements.filter((placement) => placement.area !== "room" && placement.area !== "resting");
  const seating = commonSeating(layout, resting.length, planning.length);
  const seats = [...seating.resting, ...seating.planning];
  const commonBottom = Math.max(...seats.map(({ y }) => y)) + 24;
  const commonWidth = Math.max(...seats.map(({ x }) => x)) + 24 - ROOM_LEFT;
  const nook = breakRoomNook(layout, resting.length, planning.length);
  const boardTop = Math.max(layout.height, commonBottom, ...placements.map((placement) => placement.y + 24)) + PADDING;
  const sceneHeight = boardTop + PADDING;
  const affected = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => order.status === "running" && (order.displayRoomIds ?? order.roomIds).includes(room.id))]));
  const enterable = new Set(enterableRoomIds);
  const cabling = useMemo(() => wires(layout, topology), [layout, topology]);
  const queued = tasks.filter((order) => order.status === "queued").length;
  // A compact scope still needs room for readable labels, not poster-sized
  // sprites; larger scopes retain their existing scrollable viewport.
  const maxWidth = Math.min(640, layout.width * 2);
  // One table per row, as long as the row, standing between the viewer and the
  // people at it: it covers their laps, and what they rest with sits on it.
  const tables = [{ seats: seating.resting, planning: false }, { seats: seating.planning, planning: true }].flatMap(({ seats, planning }) =>
    [...new Set(seats.map((seat) => seat.y))].map((y) => { const row = seats.filter((seat) => seat.y === y), first = row[0]!; return <g key={`${planning} ${y}`} aria-hidden="true" pointerEvents="none" data-common-table={planning ? "planning" : "resting"} transform={`translate(${first.x} ${y + TABLE_DROP})`}>
      <rect x="-18" y="-21" width={row.at(-1)!.x - first.x + 36} height="10" fill={planning ? "#455c5e" : "#655d4c"} stroke="#8c8871" />
      {!planning ? null : <g><rect x="-12" y="-19" width="24" height="6" fill="#9fae9e" /><path d="M-9 -17h12v3H-3v-3 M5 -16h4" fill="none" stroke="#536e70" /></g>}
    </g>; }));

  return (
    <>
    <div className="dfFactoryFloor__map" onClick={inspect} onPointerOver={inspect} onFocus={inspect} onPointerLeave={() => { retainFocusedTooltip(); setLinkedFrom(undefined); }} onBlur={() => { setTooltip(undefined); setLinkedFrom(undefined); }} onScroll={retainFocusedTooltip} onKeyDown={(event) => { if (event.key === "Escape") { setTooltip(undefined); setLinkedFrom(undefined); } }} role="region" aria-label="Scrollable codebase floor" tabIndex={0}>
    <svg
      viewBox={`0 0 ${layout.width} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory codebase floor"
      data-topology-digest={topology.digest}
      style={{ display: "block", width: "100%", minWidth: layout.width, maxWidth, height: "auto", margin: "0 auto", background: "#08131d" }}
    >
      <desc>{`${layout.rooms.length} topology spaces, ${workers.length} workers${omittedLocations === 0 ? "" : `, ${omittedLocations} current locations not shown in this view`}`}</desc>
      <defs>
        {/* The sheet enters the document once; every frame is a window on it. */}
        <image id="df-sheet" href={spriteSheet} width={spriteSheetSize.width} height={spriteSheetSize.height} style={{ imageRendering: "pixelated" }} />
        {Object.entries(spriteAtlas.frames).map(([name, cell]) => (
          <symbol key={name} id={`df-frame-${name}`} viewBox={`${cell.x} ${cell.y} ${FRAME} ${FRAME}`}>
            <use href="#df-sheet" />
          </symbol>
        ))}
        <pattern id="df-floor" patternUnits="userSpaceOnUse" width="32" height="24">
          <rect width="32" height="24" fill="#222c2f" />
          <path d="M1 2h13v9H1z M17 1h13v10H17z M-6 14H7v8H-6z M10 14h13v9H10z M26 14h12v8H26z" fill="#273134" stroke="#242e31" strokeWidth="1" />
          <path d="M3 3h9 M19 2h8 M12 15h8" stroke="#2a3437" strokeWidth="1" />
        </pattern>
        <pattern id="df-wall" patternUnits="userSpaceOnUse" width="24" height="12">
          <rect width="24" height="12" fill="#41494a" />
          <path d="M0 0h24 M0 6h24 M12 0v6 M0 6v6 M24 6v6" stroke="#20292d" strokeWidth="2" />
        </pattern>
        <radialGradient id="df-lamplight" cx="50%" cy="45%" r="60%">
          <stop offset="0" stopColor="#f2dab0" stopOpacity=".24" />
          <stop offset="1" stopColor="#f2dab0" stopOpacity="0" />
        </radialGradient>
      </defs>
      <rect width={layout.width} height={sceneHeight} fill="#08131d" />
      {layout.corridors.map((corridor, index) => <rect key={index} data-corridor="" {...corridor} fill="url(#df-floor)" />)}
      <rect x={PADDING} y={layout.restingTop - 32} width={ROOM_LEFT - PADDING} height={boardTop - layout.restingTop + 32} fill="url(#df-floor)" />
      {layout.headings.map((heading) => (
        <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="9" fontWeight="700">
          {shortLabel(heading.label)}
        </text>
      ))}

      {layout.rooms.map((room) => {
        const node = nodes.get(room.id);
        if (node === undefined) return null;
        const footprint = affected.get(room.id) ?? [];
        const contents = room.contents;
        const task = footprint.find((order) => order.id === selectedTaskId) ?? footprint[0];
        const operating = connected && task !== undefined;
        const canEnter = onEnterRoom !== undefined && enterable.has(room.id);
        return (
          <g key={room.id} data-room-id={room.id}>

            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="url(#df-floor)" />
            <rect x={room.x} y={room.y} width={room.width} height="10" fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} M${room.x + room.width},${room.y} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#465355" strokeWidth="4" />
            <path d={`M${room.x + 4} ${room.y + 10}v${room.height - 14} M${room.x + room.width - 4} ${room.y + 10}v${room.height - 14}`} stroke="#141f23" strokeWidth="2" />
            <rect x={room.x + 4} y={room.y + 10} width={room.width - 8} height={room.height - 12} fill={operating ? "url(#df-lamplight)" : "#08131d"} opacity={operating ? 1 : .18} pointerEvents="none" />
            {appearance.scenery === "off" ? null : <g aria-hidden="true" opacity={appearance.scenery === "subtle" ? .35 : .6}>
              <path d={`M${room.door.x - 12} ${room.door.y - 8}h24 M${room.door.x - 7} ${room.door.y - 14}h14`} stroke="#53615c" strokeWidth="2" />
            </g>}
            {contents.map((item) => <g key={item.key} data-room-content={item.kind} aria-hidden="true"><Equipment item={item} operating={operating} scenery={appearance.scenery} /></g>)}
            <g {...sceneAction(() => setSelectedRoomId(room.id))} className="dfFactoryScene__target" data-tooltip={roomInfo(node)} aria-label={`Inspect ${node.label}`}>
              <rect className="dfFactoryScene__focus" x={room.x + 8} y={room.y + 12} width={room.width - 44} height="24" rx="2" fill="#182429" />
              <text x={room.x + 14} y={room.y + 28} fill="#d7ddcf" fontFamily="ui-monospace, monospace" fontSize="10" fontWeight="700">{shortLabel(node.label, Math.floor((room.width - 54) / 6))}</text>
            </g>
            <g data-work-footprint={task === undefined ? undefined : room.id} data-room-operating={operating}
              data-workbench-task-id={task?.id}
              data-tooltip={task === undefined ? "No current work" : `${connected ? "Working" : "Disconnected · last observed work"} · ${task.title}${footprint.length > 1 ? `\n${footprint.length - 1} more tasks in this area` : ""}`}
              aria-label={task === undefined ? "No current work" : `${connected ? "Working" : "Disconnected · last observed work"}: ${task.title}`}
              {...sceneAction(task === undefined || onSelectTask === undefined ? undefined : () => onSelectTask(task.id))}>
              <rect x={room.x + room.width - 32} y={room.y + 12} width="24" height="24" fill="transparent" />
              <path d={`M${room.x + room.width - 26} ${room.y + 18}h14v10h-14Z`} fill="#303e40" stroke="#53605b" />
              <rect x={room.x + room.width - 24} y={room.y + 20} width="10" height="5" fill={operating ? "#e5c58b" : "#626c64"} />
            </g>
            {!canEnter ? null : <g data-enter-room-id={room.id} {...sceneAction(() => onEnterRoom(room.id))} aria-label={`Open contents of ${node.label}`}>
              <rect x={room.x + room.width - 92} y={room.y + room.height - 24} width="84" height="16" fill="#172b38" stroke="#80ddff" />
              <text x={room.x + room.width - 50} y={room.y + room.height - 13} textAnchor="middle" fill="#80ddff" fontFamily="ui-monospace, monospace" fontSize="7">Open contents</text>
            </g>}
          </g>
        );
      })}



      {appearance.scenery === "off" ? null : <g aria-hidden="true" fill="none" strokeLinecap="round" pointerEvents="none">
        <g opacity={appearance.scenery === "subtle" ? .3 : .5}>{cabling.trunks.map((trunk, index) => <path key={index} data-wire-trunk={trunk.count} d={trunk.d} stroke={trunk.wall ? "#9aa69c" : "#728078"} strokeWidth={Math.min(trunk.wall ? 4 : 8, 1.5 * Math.sqrt(trunk.count))} />)}</g>
        {cabling.routes.filter((wire) => linkedFrom === wire.from || linkedFrom === wire.to).map((wire) =>
          <path key={`${wire.from} ${wire.to}`} data-wire={`${wire.from} ${wire.to}`} d={wire.d} stroke="#e5c58b" strokeWidth="1.5" opacity=".9" />)}
      </g>}
      <Area width={commonWidth + (nook?.width ?? 0)} top={layout.restingTop - 40} bottom={commonBottom} />
      {/* Somewhere to go other than the table: against the back wall, muted like the rest of the furniture. */}
      {appearance.scenery === "off" ? null : nook?.furniture.map((piece) => <g key={piece.errand} aria-hidden="true" data-break-room={piece.errand} opacity=".8" transform={`translate(${piece.x} ${piece.y}) scale(${WORKER_SIZE / FRAME})`}>
        <Frame name={piece.errand === "shelf" ? "prop.bookshelf" : "prop.coffeestation"} x={0} y={0} />
      </g>)}
      {[
        { label: "Break room", seats: seating.resting, planning: false, occupied: resting.length },
        { label: "Planning", seats: seating.planning, planning: true, occupied: planning.length },
      ].filter(({ seats }) => seats.length > 0).map(({ label, seats, planning, occupied }) => {
        const top = seats[0]!.y;
        return <g key={label} role="group" aria-label={label}>
          <text x={seats[0]!.x - 18} y={top - 16} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">{label}</text>
          {/* A stool only where someone sits; the tables stand in front of them, drawn after the workers. */}
          {seats.slice(0, occupied).map((seat, index) => <g key={index} aria-hidden="true" data-common-seat={planning ? "planning" : "resting"} transform={`translate(${seat.x} ${seat.y})`}>
            <rect x="-7" y="3" width="14" height="6" rx="2" fill="#655948" stroke="#897c61" />
            <path d="M-5 9v3 M5 9v3" stroke="#3f4540" strokeWidth="3" />
          </g>)}
        </g>;
      })}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="24" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      <SceneWorkers errands={appearance.scenery !== "off"} furniture={tables} layout={layout} placements={placements} nodes={nodes} workers={workers} tasks={tasks} connected={connected} animate={appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectHumanRequest={onSelectHumanRequest} />
      {/* Waiting work, as the tray it would be on a real desk. Scenery, like
          everything else standing on these tables: the pile says how the queue
          is doing, the Tasks panel is where it is read and changed. */}
      <g data-floor-inbox={queued} aria-hidden="true" pointerEvents="none" transform={`translate(${ROOM_LEFT + commonWidth - 30} ${layout.restingTop + TABLE_DROP - 21})`}>
        {[...Array(Math.min(queued, 3))].map((_, index) => <rect key={index} x="1" y={-2 - index * 3} width="18" height="3" fill="#e4dcc0" stroke={tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? "#80ddff" : "#a6a087"} />)}
        <path d="M-2 -4v6h24v-6 M-2 2h24" fill="none" stroke="#c2b184" strokeWidth="2" />
      </g>


    </svg>
    {tooltip === undefined ? null : <div ref={tooltipElement} className="dfFactoryTooltip" role="tooltip" style={{ left: tooltip.x, top: tooltip.y, maxHeight: tooltip.room }}>{tooltip.text}</div>}
    </div>
    <section className="dfRoomDetails" aria-label="Room details">
      <label>Room <select aria-label="Inspect room" value={selectedRoom?.id ?? ""} onChange={(event) => setSelectedRoomId(event.target.value || undefined)}>
        <option value="">Select a room</option>
        {[...availableNodes.values()].sort((a, b) => compareText(a.project?.name ?? "", b.project?.name ?? "") || compareText(a.path, b.path) || compareText(a.label, b.label) || compareText(a.id, b.id)).map((node) => <option key={node.id} value={node.id}>{node.project?.name} · {node.label} · {node.path}</option>)}
      </select></label>
      {selectedRoom === undefined ? null : <details key={selectedRoom.id} open>
        <summary>{selectedRoom.label} · Room info</summary>
        {onEnterRoom === undefined || (selectedRoom.components?.length ?? 0) === 0 && !enterable.has(selectedRoom.id) ? null : <button type="button" onClick={() => onEnterRoom(selectedRoom.id)}>Open contents</button>}
        {(selectedRoom.components?.length ?? 0) === 0 ? null : <ul>{selectedRoom.components!.map((component) => <li key={component.id}><button type="button" disabled={!availableNodes.has(component.id)} onClick={() => setSelectedRoomId(component.id)}>{component.label}</button></li>)}</ul>}
        {selectedRoom.path === selectedRoom.label ? null : <p>{selectedRoom.path}</p>}
        <p>{selectedRoom.kind} · {selectedRoom.sizeBucket ?? "size unavailable"}{selectedRoom.language ? ` · ${selectedRoom.language}` : ""}{selectedRoom.childCount === undefined ? "" : ` · ${selectedRoom.childCount} subcomponents`}</p>
        <p>{roomInfo(selectedRoom)}</p>
        {selectedRoom.inventory === undefined ? null : <table><caption>Scanned files</caption><thead><tr><th>Kind</th><th>Direct</th><th>Subtree</th></tr></thead><tbody>{Object.entries(inventoryLabels).map(([kind, label]) => <tr key={kind}><th>{label}</th><td>{selectedRoom.inventory!.direct[kind as keyof typeof inventoryLabels]}</td><td>{selectedRoom.inventory!.total[kind as keyof typeof inventoryLabels]}</td></tr>)}</tbody></table>}
        <p>{selectedRoom.dependencies === undefined ? "Dependencies unavailable." : "Supplied static imports and manifest dependencies. Floor cables group their visible endpoints."}</p>
        {selectedRoom.dependencies === undefined ? null : <>
          {links.length === 0 ? <p>No relationships in this sample.</p> : <ul>{links.map((link) => <li key={`${link.direction}:${link.nodeId}`}>
            {link.direction === "to" ? "Depends on " : "Used by "}
            <button type="button" disabled={!availableNodes.has(link.nodeId)} onClick={() => setSelectedRoomId(link.nodeId)}>{link.label}</button>
            {nodes.has(link.nodeId) ? "" : " · outside view"} · {link.path}
          </li>)}</ul>}
          {selectedRoom.dependencies.omitted === 0 ? null : <p>{selectedRoom.dependencies.omitted} project relationships omitted from the supplied topology.</p>}
        </>}
      </details>}
    </section>
    </>
  );
}

function sceneAction(select: (() => void) | undefined) {
  return select === undefined ? {} : { role: "button", tabIndex: 0, onClick: select, style: { cursor: "pointer" }, onKeyDown: (event: KeyboardEvent<SVGGElement>) => {
    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); }
  } };
}

function Area({ width, top, bottom }: { width: number; top: number; bottom: number }) {
  return (
    <g role="group" aria-label="Common room">
      <rect x={ROOM_LEFT} y={top} width={width} height={bottom - top} fill="url(#df-floor)" />
      <path d={`M${ROOM_LEFT} ${top + 24}v-24h${width}v${bottom - top}H${ROOM_LEFT}v-8`} fill="none" stroke="#465355" strokeWidth="3" />
    </g>
  );
}


/**
 * Static dependencies as floor cabling. Every link is routed once: out under
 * the desk and through the door, along corridors, down the nearest wall of any
 * row in between, and into a lower room down its side wall. At rest the shared
 * stretches are drawn once, thicker the more links they carry; a link's own
 * end-to-end route is only drawn when one of its rooms is inspected.
 */
function wires(layout: ReturnType<typeof layoutScene>, topology: SceneTopology) {
  type Point = { x: number; y: number };
  // Corridor cable sways by position alone, so a lit route lies exactly on its
  // trunk; stretches pinned inside a wall or a doorway run straight.
  const on = (across: boolean, fixed: number, along: number): Point =>
    across ? { x: along, y: fixed + 2 * Math.sin(along / 28 + fixed) } : { x: fixed, y: along };
  const spot = ({ x, y }: Point) => `${+x.toFixed(1)} ${+y.toFixed(1)}`;
  const stretch = (across: boolean, fixed: number, from: number, to: number) => {
    let d = "";
    for (let along = from + (from < to ? 8 : -8); from < to ? along < to : along > to; along += from < to ? 8 : -8) d += `L${spot(on(across, fixed, along))}`;
    return `${d}L${spot(on(across, fixed, to))}`;
  };
  const rooms = new Map(layout.rooms.map((room) => [room.id, room]));
  const rowOf = (room: SceneRoomLayout) => room.door.y;
  const rows = [...new Set(layout.rooms.map(rowOf))].sort((a, b) => a - b);
  const pairs = new Set<string>();
  for (const node of topology.nodes) for (const link of node.dependencies?.links ?? []) {
    if (link.nodeId !== node.id && rooms.has(node.id) && rooms.has(link.nodeId)) pairs.add([node.id, link.nodeId].sort().join("\n"));
  }
  // Leads and corner bends, drawn once however many links share them.
  const leads = new Map<string, { d: string; count: number }>();
  const carry = (key: string, d: string) => leads.set(key, { d, count: (leads.get(key)?.count ?? 0) + 1 });
  const lead = (room: SceneRoomLayout, wallX?: number) => {
    const desk = room.contents.find((item) => item.workSurface);
    const key = `${room.id} ${wallX ?? "door"}`;
    let d: string;
    if (wallX === undefined) {
      const plug = desk === undefined ? { x: room.x + room.width / 2, y: room.y + 60 } : { x: desk.x + desk.width / 2, y: desk.y + 33 };
      d = `M${plug.x} ${plug.y}C${plug.x + 14} ${plug.y + 22} ${room.door.x - 10} ${room.door.y - 18} ${room.door.x} ${room.door.y}`;
    } else {
      const left = wallX < room.x + room.width / 2;
      const plug = desk === undefined ? { x: room.x + room.width / 2, y: room.y + 60 } : { x: left ? desk.x : desk.x + desk.width, y: desk.y + 22 };
      d = `M${wallX} ${plug.y - 12}C${wallX} ${plug.y + 12} ${(wallX + plug.x) / 2} ${plug.y + 16} ${plug.x} ${plug.y}`;
    }
    carry(key, d);
    return d;
  };
  // Axis-aligned stretches by channel, for counting what each one carries.
  const channels = new Map<string, [number, number][]>();
  const routes = [...pairs].sort().map((pair, index) => {
    const [from, to] = (pair.split("\n").map((id) => rooms.get(id)!) as [SceneRoomLayout, SceneRoomLayout]).sort((a, b) => rowOf(a) - rowOf(b));
    const points: Point[] = [{ x: from.door.x, y: from.door.y }, { x: from.door.x, y: from.door.y + 16 }];
    let tail: string;
    if (rowOf(from) === rowOf(to)) {
      points.push({ x: to.door.x, y: to.door.y + 16 }, { x: to.door.x, y: to.door.y });
      tail = lead(to);
    } else {
      const nearest = (row: number, x: number) => layout.rooms.filter((room) => rowOf(room) === row).flatMap((room) => [room.x, room.x + room.width])
        .reduce((best, wall) => Math.abs(wall - x) < Math.abs(best - x) ? wall : best);
      for (const row of rows.filter((row) => row > rowOf(from) && row < rowOf(to))) {
        const wall = nearest(row, to.x + to.width / 2);
        points.push({ x: wall, y: points.at(-1)!.y }, { x: wall, y: row + 16 });
      }
      const wall = Math.abs(to.x - points.at(-1)!.x) <= Math.abs(to.x + to.width - points.at(-1)!.x) ? to.x : to.x + to.width;
      const desk = to.contents.find((item) => item.workSurface);
      points.push({ x: wall, y: points.at(-1)!.y }, { x: wall, y: (desk === undefined ? to.y + 60 : desk.y + 22) - 12 });
      tail = lead(to, wall);
    }
    let run = "", bent = "";
    for (let at = 1; at < points.length; at += 1) {
      const a = points[at - 1]!, b = points[at]!, before = points[at - 2], next = points[at + 1];
      const across = a.y === b.y, fixed = across ? a.y : a.x, [start, end] = across ? [a.x, b.x] : [a.y, b.y];
      // A vertical stretch that is not a door stub runs inside a wall.
      const key = `${across ? "h" : at === 1 || (next === undefined && rowOf(from) === rowOf(to)) ? "v" : "w"}${fixed}`;
      const corner = Math.min(8, Math.abs(end - start) / 2), way = Math.sign(end - start);
      const entry = start + (before === undefined ? 0 : way * corner), exit = end - (next === undefined ? 0 : way * corner);
      // A stretch stops short of each bend, so a turning cable curves into the bundle it joins.
      channels.set(key, [...(channels.get(key) ?? []), [Math.min(entry, exit), Math.max(entry, exit)]]);
      if (before !== undefined) { const bend = `${bent}Q${a.x} ${a.y} ${spot(on(across, fixed, entry))}`; carry(bend, `M${bend}`); }
      run += `${before === undefined ? "M" : `Q${a.x} ${a.y} `}${spot(on(across, fixed, entry))}${stretch(across, fixed, entry, exit)}`;
      bent = spot(on(across, fixed, exit));
    }
    return { from: from.id, to: to.id, d: `${lead(from)} ${run} ${tail}` };
  });
  const trunks: { d: string; count: number; wall?: boolean }[] = [...leads.values()];
  for (const [key, spans] of channels) {
    const fixed = Number(key.slice(1)), cuts = [...new Set(spans.flat())].sort((a, b) => a - b);
    for (let at = 1; at < cuts.length; at += 1) {
      const lo = cuts[at - 1]!, hi = cuts[at]!, count = spans.filter(([from, to]) => from <= lo && to >= hi).length;
      if (count === 0) continue;
      trunks.push({ count, wall: key[0] === "w", d: `M${spot(on(key[0] === "h", fixed, lo))}${stretch(key[0] === "h", fixed, lo, hi)}` });
    }
  }
  return { routes, trunks };
}

/** Furniture is subdued scenery, never a second set of file-category controls. */
function Equipment({ item, operating, scenery }: { item: RoomContent; operating: boolean; scenery: FloorAppearance["scenery"] }) {
  return <g transform={`translate(${item.x} ${item.y})`} opacity={operating ? .9 : .55}>
    <rect x="4" y="27" width={item.width - 8} height="6" fill="#131e22" />
    <rect y="12" width={item.width} height="16" rx="2" fill={item.furnishing === "console" ? "#455653" : "#665f4e"} stroke="#8a8067" />
    <path d={`M4 29v5 M${item.width - 4} 29v5`} stroke="#424a46" strokeWidth="3" />
    <g transform={`translate(${item.width / 2 - 8} 0)`}>
      <rect width="16" height="12" fill="#3b4948" stroke="#65726b" />
      <rect x="2" y="2" width="12" height="8" fill={operating ? "#779c95" : "#263e40"} />
      {operating ? <path d="M4 4h8 M4 7h5" stroke="#bdd0b2" strokeWidth="1" /> : null}
      <path d="M3 14h10" stroke="#9b9c86" strokeWidth="2" />
    </g>
    {scenery === "off" ? null : <g opacity={scenery === "subtle" ? .6 : 1}>
    {item.furnishing === "drafting" ? <g>
      <rect x="10" y="15" width="30" height="9" fill="#969480" />
      <path d="M14 17h14v4H18v-4 M31 18h5" fill="none" stroke="#526b6b" />
    </g> : item.furnishing === "console" ? <path d="M8 19h12 M8 22h7" stroke="#727e72" strokeWidth="2" /> : <rect x={item.width - 24} y="15" width="16" height="8" fill="#969480" />}

    </g>}
  </g>;
}

function roomInfo(node: SceneTopology["nodes"][number]) {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  // The cabling is scenery; its content is stated here for every reader and appearance.
  const links = [...new Set((node.dependencies?.links ?? []).map((link) => link.label))];
  const inventory = counts === undefined ? "Inventory unavailable" : Object.entries(inventoryLabels).filter(([kind]) => counts[kind as keyof typeof counts] > 0).map(([kind, label]) => `${counts[kind as keyof typeof counts]} ${label.toLowerCase()}`).join(" · ") || "No scanned files";
  return `${node.path === "." ? node.label : node.path}\n${node.language ? `${node.language} · ` : ""}${node.kind} · ${node.inventoryScope === "direct" ? "direct files" : "subtree"}\n${inventory}${links.length === 0 ? "" : `\nWired to ${links.slice(0, 6).join(", ")}${links.length > 6 ? ` +${links.length - 6}` : ""}`}`;
}
