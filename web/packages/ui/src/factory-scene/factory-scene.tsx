import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  placeWorkers,
  inventoryLabels,
  type RoomContent,
  type SceneRoomLayout,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { workerFrames } from "./appearance.js";
import { ambientPlacements } from "./ambient.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
  appearance?: FloorAppearance;
  topology: SceneTopology;
  workers: readonly SceneWorker[];
  tasks?: readonly SceneTask[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  onOpenQueue?: () => void;
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

function shortLabel(label: string, limit = 18) {
  const glyphs = [...label];
  return glyphs.length > limit ? `${glyphs.slice(0, limit - 1).join("")}…` : label;
}

/** One 16px frame of the sheet, sized and placed in scene coordinates. */
function Frame({ name, x, y, className, scale = 1 }: { name: string; x: number; y: number; className?: string; scale?: 1 | 2 }) {
  return <use href={`#df-frame-${name}`} x={x} y={y} width={FRAME * scale} height={FRAME * scale} className={className} />;
}

function FrameRun({ name, x, y, length, vertical = false }: { name: string; x: number; y: number; length: number; vertical?: boolean }) {
  return <>{Array.from({ length: Math.floor(length / FRAME) }, (_, index) => <Frame key={index} name={name} x={x + (vertical ? 0 : index * FRAME)} y={y + (vertical ? index * FRAME : 0)} />)}</>;
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

/** One browser clock; source state only ever supplies the next local destination. */
function useSceneMotion(layout: ReturnType<typeof layoutScene>, placements: ReturnType<typeof placeWorkers>, topologyDigest: string, connected: boolean, active: ReadonlySet<string>) {
  const motions = useRef(new Map<string, MotionState>());
  const priorTopology = useRef<string | undefined>(undefined);
  const priorConnected = useRef<boolean | undefined>(undefined);
  const [clock, setClock] = useState(0);
  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

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
    if (active.size === 0) return;
    const timer = setTimeout(() => setClock(now()), 450);
    return () => clearTimeout(timer);
  }, [clock, connected, reduced, active]);

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
    output.set(placement.id, {
      ...current.point,
      motion: current.walking
        ? { action: "walking", direction: current.direction!, frame: Math.floor(clock / 150) % 2 as 0 | 1 }
        : { action: active.has(placement.id) ? "interacting" : "still", frame: connected && !reduced && (typeof document === "undefined" || document.visibilityState === "visible") ? Math.floor(clock / 450) % 2 as 0 | 1 : 0 },
    });
  }
  return output;
}

/** The animation clock updates worker elements without rerendering the floor or atlas. */
function SceneWorkers({ layout, placements, nodes, workers, tasks, connected, selectedWorkerId, onSelectWorker, onSelectHumanRequest, animationEnabled }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectHumanRequest"> & {
  layout: ReturnType<typeof layoutScene>;
  placements: ReturnType<typeof placeWorkers>;
  nodes: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  tasks: readonly SceneTask[];
  connected: boolean;
  animationEnabled: boolean;
}) {
  // Inventory/dependency metadata may change without changing a route's geometry.
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.restingTop, layout.corridors, layout.rooms.map(({ id, x, y, width, height, door }) => [id, x, y, width, height, door])]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && placements.some((placement) => placement.id === worker.id && placement.area === "room" && (placement.bayId !== undefined || layout.rooms.find((room) => room.id === placement.roomId)?.contents.some((item) => item.workSurface)))).map((worker) => worker.id)), [workers, placements, layout]);
  const positions = useSceneMotion(layout, placements, geometryKey, connected && animationEnabled, active);
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  return <>{placements.map((placement) => {
        const worker = workerById.get(placement.id);
        if (worker === undefined) return null;
        const position = positions.get(placement.id) ?? { ...placement, motion: { action: "still", frame: 0 } as WorkerMotion };
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const picturedSurface = layout.rooms.find((candidate) => candidate.id === placement.roomId)?.contents.some((item) => item.workSurface);
        const location = worker.location === "working"
          ? `${placement.bayId === undefined ? "representative location" : "exact observed direct child"}${worker.locationWithin ? " within this component; more specific observed area" : " near observed changes"}${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? placement.bayId === undefined ? picturedSurface ? "at the shared pictured workbench" : "at a general work position; inventory unavailable or empty" : "at the pictured child bay" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
          : worker.location === "unobserved" ? "working; location not yet observed"
          : worker.location === "last-observed" && worker.locationLabel !== undefined ? `last observed near changes in ${worker.locationLabel}; resting area`
          : worker.paused ? "paused in resting area" : placement.ambient === "notice-board" ? "reading at the shared notice board; ambient presentation only"
          : placement.ambient === "break-counter" ? "at the break counter; ambient presentation only"
          : placement.ambientSocial ? "with another worker at the shared table; ambient pose, no factory message"
          : placement.ambient === "shared-table" ? "waiting at the shared table; ambient presentation only" : "ready in resting area";

        const attention = tasks.flatMap((order) => order.agentId === worker.id ? order.humanRequestIds : []);
        const frames = workerFrames(worker, position.motion);
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-worker-location={worker.location ?? "resting"}
            data-worker-action={position.motion.action}
            data-ambient-pose={placement.ambientSocial ? "chat" : placement.ambient}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g role="img" aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...sceneAction(onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
              <title>{`${worker.name} · ${location}`}</title>
              <rect x={-12} y={-12} width="24" height="24" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}</g>
            </g>
            {position.motion.action === "walking" || placement.ambient === undefined ? null : <g aria-hidden="true">
              {placement.ambientSocial ? <Frame name="indicator.chat" x={-8} y={-26} />
                : placement.ambient === "break-counter" ? <Frame name="prop.cup" x={4} y={-12} />
                : placement.ambient === "notice-board" ? <Frame name="prop.book" x={4} y={-10} /> : null}
            </g>}
            {attention.length === 0 ? null : <g {...sceneAction(onSelectHumanRequest === undefined ? undefined : () => onSelectHumanRequest(attention[0]!))} aria-label={`Question from ${worker.name}`} data-human-request-id={attention[0]}>
              <rect x="8" y="-22" width="24" height="24" fill="transparent" /><Frame name="indicator.attention" x={12} y={-18} />
            </g>}
          </g>
        );
      })}</>;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ topology, workers, appearance = DEFAULT_FLOOR_APPEARANCE, omittedLocations = 0, enterableRoomIds = [], onEnterRoom, selectedWorkerId, onSelectWorker, tasks = [], selectedTaskId, onSelectTask, onOpenQueue, onSelectHumanRequest, connected = true }: FactorySceneProps) {
  const [selectedRoomId, setSelectedRoomId] = useState<string>();
  const [ambientPhase, setAmbientPhase] = useState(0);
  const ambientEligible = workers.some((worker) => worker.location !== "working" && worker.location !== "unobserved" && !worker.paused && worker.activity !== "needs-you"
    && (appearance.ambientLife === "lively" || worker.activity === "idle" || worker.activity === "waiting"));
  useEffect(() => {
    if (!connected || !ambientEligible || appearance.animation === "off" || appearance.ambientLife === "off" || typeof window === "undefined" || typeof document === "undefined" || typeof window.setInterval !== "function") return;
    const media = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    const interval = appearance.ambientLife === "lively" ? 12000 : 18000;
    let timer: ReturnType<typeof window.setInterval> | undefined;
    const stop = () => { if (timer !== undefined) { window.clearInterval(timer); timer = undefined; } };
    const start = () => {
      stop();
      if (document.visibilityState !== "visible" || media?.matches) return;
      timer = window.setInterval(() => setAmbientPhase((phase) => phase + 1), interval);
    };
    const resume = () => { if (document.visibilityState === "visible") setAmbientPhase((phase) => phase + 1); start(); };
    start();
    media?.addEventListener("change", start);
    document.addEventListener("visibilitychange", resume);
    return () => { stop(); media?.removeEventListener("change", start); document.removeEventListener("visibilitychange", resume); };
  }, [appearance.ambientLife, appearance.animation, ambientEligible, connected]);
  const layout = useMemo(() => layoutScene(topology), [topology]);
  const placements = useMemo(() => ambientPlacements(layout, placeWorkers(layout, workers), workers, appearance.ambientLife, ambientPhase), [layout, workers, appearance.ambientLife, ambientPhase]);
  const nodes = new Map(topology.nodes.map((node) => [node.id, node]));
  const resting = placements.filter((placement) => placement.area === "resting");
  const staging = placements.filter((placement) => placement.area === "staging");
  const overflow = placements.filter((placement) => placement.area === "overflow");
  const outside = placements.filter((placement) => placement.area === "outside");
  const boardTop = Math.max(layout.height, ...placements.map((placement) => placement.y + 24)) + PADDING;
  const sceneHeight = boardTop + (tasks.some((task) => task.status === "queued") ? 48 : 0) + PADDING;
  const affected = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => (order.displayRoomIds ?? order.roomIds).includes(room.id))]));
  const enterable = new Set(enterableRoomIds);
  const queued = tasks.filter((order) => order.status === "queued").length;
  const selectedRoom = nodes.get(selectedRoomId ?? "");
  const selectedGeometry = layout.rooms.find((room) => room.id === selectedRoom?.id);
  const links = selectedRoom?.dependencies?.links ?? [];
  const shownLinks = links.slice(0, 8);
  const dependencySegments = (() => {
    if (appearance.dependencyLinks === "off") return [];
    const segments: Array<{ source: SceneRoomLayout; target: SceneRoomLayout; key: string }> = [];
    const seen = new Set<string>();
    for (const room of layout.rooms) {
      if (appearance.dependencyLinks === "selected-room" && selectedGeometry?.id !== room.id) continue;
      for (const link of nodes.get(room.id)?.dependencies?.links ?? []) {
        const other = layout.rooms.find((candidate) => candidate.id === link.nodeId);
        if (other === undefined) continue;
        const source = link.direction === "to" ? room : other;
        const target = link.direction === "to" ? other : room;
        const key = `${source.id}->${target.id}`;
        if (seen.has(key)) continue;
        seen.add(key);
        segments.push({ source, target, key });
      }
    }
    return segments;
  })();
  const dependencyLimit = appearance.dependencyLinks === "overview" ? 16 : 8;
  const visibleDependencySegments = dependencySegments.slice(0, dependencyLimit);
  const floorFill = appearance.scenery === "rich" ? "url(#df-floor)" : appearance.scenery === "subtle" ? "#14212a" : "#0d171e";
  // A compact scope still needs room for readable labels, not poster-sized
  // sprites; larger scopes retain their existing scrollable viewport.
  const maxWidth = Math.min(640, layout.width * 2);

  return (
    <>
    <p className="dfFactoryFloor__mapHint">Scroll the floor to explore · choose a room below for details</p>
    <div className="dfFactoryFloor__map" role="region" aria-label="Scrollable codebase floor" tabIndex={0}>
    <svg
      viewBox={`0 0 ${layout.width} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory codebase floor"
      data-topology-digest={topology.digest}
      style={{ display: "block", width: "100%", minWidth: layout.width, maxWidth, height: "auto", margin: "0 auto", background: "#08131d", shapeRendering: "crispEdges" }}
    >
      <title>Dark Factory codebase floor</title>
      <desc>{`${layout.rooms.length} topology spaces, ${workers.length} workers${omittedLocations === 0 ? "" : `, ${omittedLocations} current locations not shown in this view`}`}</desc>
      <defs>
        {/* The sheet enters the document once; every frame is a window on it. */}
        <image id="df-sheet" href={spriteSheet} width={spriteSheetSize.width} height={spriteSheetSize.height} style={{ imageRendering: "pixelated" }} />
        {Object.entries(spriteAtlas.frames).map(([name, cell]) => (
          <symbol key={name} id={`df-frame-${name}`} viewBox={`${cell.x} ${cell.y} ${FRAME} ${FRAME}`}>
            <use href="#df-sheet" />
          </symbol>
        ))}
        <pattern id="df-floor" patternUnits="userSpaceOnUse" width={FRAME * 2} height={FRAME * 2}>
          <rect width={FRAME * 2} height={FRAME * 2} fill="#17242c" />
          <rect x="2" y="2" width="2" height="2" fill="#31444c" />
          <rect x="18" y="18" width="2" height="2" fill="#31444c" />
        </pattern>
      </defs>
      <rect width={layout.width} height={sceneHeight} fill="#08131d" />
      <text x={PADDING} y="20" fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="10" fontWeight="700">
        {layout.rooms.length} SPACES
      </text>

      {layout.corridors.map((corridor, index) => <rect key={index} data-corridor="" {...corridor} fill={floorFill} />)}
      <rect x={PADDING} y={layout.restingTop - 32} width={ROOM_LEFT - PADDING} height={boardTop - layout.restingTop + 32} fill={floorFill} />
      {layout.headings.map((heading) => (
        <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="9" fontWeight="700">
          {shortLabel(heading.label)}
        </text>
      ))}

      {visibleDependencySegments.map(({ source, target, key }) => <path key={key} data-static-dependency="" d={`M${source.x + source.width / 2},${source.y - 6} H${target.x + target.width / 2} V${target.y - 6}`} fill="none" stroke="#beacff" strokeWidth="1.5" strokeDasharray="4 4" pointerEvents="none"><title>{`Static dependency: ${nodes.get(source.id)?.label} → ${nodes.get(target.id)?.label}`}</title></path>)}
      {appearance.dependencyLinks === "overview" && dependencySegments.length > visibleDependencySegments.length ? <g data-dependency-summary aria-label={`${dependencySegments.length - visibleDependencySegments.length} additional sampled dependency links omitted from overview`}><title>{`${dependencySegments.length - visibleDependencySegments.length} additional sampled dependency links omitted from overview`}</title></g> : null}

      {layout.rooms.map((room) => {
        const node = nodes.get(room.id);
        if (node === undefined) return null;
        const footprint = affected.get(room.id) ?? [];
        const contents = room.contents;
        const work = footprint.filter((order) => (order.displayRoomId ?? order.representativeRoomId) === room.id);
        const task = work.find((order) => order.id === selectedTaskId) ?? work[0];
        const canEnter = onEnterRoom !== undefined && enterable.has(room.id);
        return (
          <g key={room.id} data-room-id={room.id} data-room-arrangement={room.arrangement}>
            <title>{node.path}</title>
            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill={room.arrangement === "hall" ? "#23383a" : room.arrangement === "parent" ? "#22343b" : "#1e3038"} />
            {appearance.scenery === "rich" ? <g aria-hidden="true"><Frame name="tile.floor.2" x={room.x + 16} y={room.y + room.height - 48} /><Frame name="tile.floor.2" x={room.x + room.width - 32} y={room.y + room.height - 48} /></g> : null}
            <g data-room-walls="">
            <FrameRun name="tile.wall" x={room.x} y={room.y} length={room.width} />
            <FrameRun name="tile.wall" x={room.x} y={room.y + FRAME} length={room.height - FRAME} vertical />
            <rect x={room.x + room.width - 4} y={room.y + FRAME} width="4" height={room.height - FRAME} fill="#718694" />
            <Frame name="tile.wall.corner" x={room.x} y={room.y} />
            <Frame name="tile.wall.corner" x={room.x + room.width - FRAME} y={room.y} />
            <Frame name="tile.wall.door" x={room.door.x - FRAME / 2} y={room.door.y - FRAME} />
            </g>
            {appearance.scenery === "rich" ? <g aria-hidden="true"><Frame name="prop.lamp.on" x={room.x + 16} y={room.y + 16} /><Frame name="prop.cable.horizontal" x={room.x + 32} y={room.y + 16} /></g> : null}
            {!appearance.taskProps || footprint.length === 0 ? null : <rect data-work-footprint={room.id} x={room.x + 5} y={room.y + 39} width={room.width - 10} height={room.height - 46} fill="#d8a94c" fillOpacity="0.10" stroke={footprint.some((order) => order.id === selectedTaskId) ? "#80ddff" : "#d8a94c"} strokeDasharray="3 3"><title>{`Observed changes ${footprint.some((order) => !order.roomIds.includes(room.id)) ? "within this component" : "in this area"} for ${footprint.length} running task(s): ${footprint.map((order) => order.id.slice(0, 8)).join(", ")}`}</title></rect>}
            {!appearance.taskProps || footprint.length === 0 ? null : <g aria-label={footprint.some((order) => !order.roomIds.includes(room.id)) ? "Observed changes within this component" : "Observed changes in this area"}><Frame name="indicator.attention" x={room.x + room.width - 20} y={room.y + 18} /></g>}
            {!appearance.taskProps || work.length === 0 ? null : <g
              data-workbench-task-id={task!.id}
              {...sceneAction(onSelectTask === undefined ? undefined : () => onSelectTask(task!.id))}
              aria-label={`Task ${task!.id.slice(0, 8)}: ${task!.title}; representative area${work.length > 1 ? `; ${work.length - 1} more running tasks in the queue panel` : ""}`}
            >
              <title>{task!.title}</title>
              <Frame name="prop.book" x={room.x + 8} y={room.door.y - 22} />
              <text x={room.x + 26} y={room.door.y - 10} fill="#f0c777" fontSize="7" fontFamily="ui-monospace, monospace">{task!.id.slice(0, 8)}</text>
            </g>}
            {contents.map((item) => <g key={item.key} data-room-content={item.kind} {...sceneAction(item.targetId !== undefined && !nodes.has(item.targetId) && onEnterRoom === undefined ? undefined : () => { setSelectedRoomId(item.targetId ?? room.id); if (item.targetId !== undefined) onEnterRoom?.(item.targetId); })}
              aria-label={item.kind === "component" ? `Open component ${item.label}` : `Inspect ${item.count} ${item.label.toLowerCase()} files in ${node.label}`}>
              <title>{item.kind === "component" ? item.label : `${item.count} scanned ${item.label.toLowerCase()} files represented by this group`}</title>
              <Equipment item={item} showCount={appearance.labels === "names-and-counts"} />
            </g>)}
            {room.omittedBayCount === 0 ? null : <text data-omitted-bays={room.omittedBayCount} x={room.x + room.width - 8} y={room.y + 44} textAnchor="end" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">+{room.omittedBayCount} BAYS</text>}
            {node.inventory === undefined ? <text x={room.x + 12} y={room.y + room.height - 52} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">INVENTORY UNAVAILABLE</text> : contents.length === 0 ? <text x={room.x + 12} y={room.y + 62} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">NO SCANNED FILES</text> : null}
            {(node.dependencies?.links.length ?? 0) === 0 ? null : <g {...sceneAction(() => setSelectedRoomId(room.id))} aria-label={`Inspect static dependencies of ${node.label}`}>
              <rect x={room.x + room.width - 28} y={room.y + 18} width="24" height="24" fill="transparent" />
              <rect x={room.x + room.width - 24} y={room.y + 20} width="16" height="10" fill="#493d68" stroke="#beacff" /><text x={room.x + room.width - 16} y={room.y + 28} textAnchor="middle" fill="#e4dafa" fontSize="9">↔</text>
            </g>}
            <g {...sceneAction(() => setSelectedRoomId(room.id))} aria-label={`Inspect ${node.label}`}>
            <rect x={room.x + 4} y={room.y} width={room.width - 8} height="24" fill="transparent" />
            <text x={room.x + 8} y={room.y + 18} fill="#f2f6f8" fontFamily="ui-monospace, monospace" fontSize="11" fontWeight="700">
              {shortLabel(node.label, Math.floor((room.width - 16) / 7))}
            </text>
            </g>
            <text x={room.x + 8} y={room.y + 34} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">
              {node.sizeBucket === undefined ? "STRUCTURE UNAVAILABLE" : `${node.kind.toUpperCase()} · ${(node.inventoryScope === "direct" ? "DIRECT CONTENTS" : "SUBTREE")}`}
            </text>
            {!canEnter ? null : <g data-enter-room-id={room.id} {...sceneAction(() => onEnterRoom(room.id))} aria-label={`Enter ${node.label}`}>
              <rect x={room.x + room.width - 48} y={room.y + room.height - 24} width="40" height="16" fill="#172b38" stroke="#80ddff" />
              <text x={room.x + room.width - 28} y={room.y + room.height - 13} textAnchor="middle" fill="#80ddff" fontFamily="ui-monospace, monospace" fontSize="7">ENTER</text>
            </g>}
          </g>
        );
      })}



      <Area label={`RESTING AREA · ${resting.length}`} width={layout.width - ROOM_LEFT - PADDING} top={layout.restingTop - 28} bottom={Math.max(layout.restingTop + 24, ...resting.map((placement) => placement.y + 24))} fill={floorFill} />
      {staging.length === 0 ? null : <Area label={`STAGING · ${staging.length}`} width={layout.width - ROOM_LEFT - PADDING} top={staging[0]!.y - 28} bottom={Math.max(...staging.map((placement) => placement.y + 24))} fill={floorFill} />}
      {outside.length === 0 ? null : <Area label={`OUTSIDE DISPLAYED ROOMS · ${outside.length}`} width={layout.width - ROOM_LEFT - PADDING} top={outside[0]!.y - 28} bottom={Math.max(...outside.map((placement) => placement.y + 24))} fill={floorFill} />}
      {overflow.length === 0 ? null : <Area label={`WORKER AREA AT CAPACITY · ${overflow.length}`} width={layout.width - ROOM_LEFT - PADDING} top={overflow[0]!.y - 28} bottom={Math.max(...overflow.map((placement) => placement.y + 24))} fill={floorFill} />}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="38" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      {appearance.scenery === "off" ? null : <AmbientFixtures x={ROOM_LEFT + 8} y={layout.restingTop - 4} />}

      <SceneWorkers layout={layout} placements={placements} nodes={nodes} workers={workers} tasks={tasks} connected={connected} animationEnabled={appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectHumanRequest={onSelectHumanRequest} />

      {queued === 0 || !appearance.taskProps ? null : <g data-floor-queue="" {...sceneAction(onOpenQueue)} aria-label={`Open queue, ${queued} tasks`}>
        {[...Array(Math.min(queued, 3))].map((_, index) => <Frame key={index} name="prop.book" x={ROOM_LEFT + index * 4} y={boardTop + index * 4} scale={2} />)}
        {tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? <rect x={ROOM_LEFT - 2} y={boardTop - 2} width="42" height="42" fill="none" stroke="#80ddff" strokeWidth="2" /> : null}
        <text x={ROOM_LEFT + 40} y={boardTop + 18} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="10">QUEUE · {queued}</text>
      </g>}

    </svg>
    </div>
    <section className="dfRoomDetails" aria-label="Room details">
      <label>Room <select aria-label="Inspect room" value={selectedRoom?.id ?? ""} onChange={(event) => setSelectedRoomId(event.target.value || undefined)}>
        <option value="">Select a room</option>
        {layout.rooms.map((room) => <option key={room.id} value={room.id}>{nodes.get(room.id)?.label}</option>)}
      </select></label>
      {selectedRoom === undefined ? null : <details key={selectedRoom.id} open>
        <summary>Room info</summary>
        {selectedRoom.path === selectedRoom.label ? null : <p>{selectedRoom.path}</p>}
        <p>{selectedRoom.kind} · {selectedRoom.sizeBucket ?? "size unavailable"}{selectedRoom.language ? ` · ${selectedRoom.language}` : ""}{selectedRoom.childCount === undefined ? "" : ` · ${selectedRoom.childCount} subcomponents`}</p>
        <InventoryInfo node={selectedRoom} room={selectedGeometry!} />
        <p>{selectedRoom.dependencies === undefined ? "Dependencies unavailable." : "Dashed lines: sampled static imports and manifest dependencies."}</p>
        {selectedRoom.dependencies === undefined ? null : <>
          {links.length === 0 ? <p>No relationships in this sample.</p> : <ul>{shownLinks.map((link) => <li key={`${link.direction}:${link.nodeId}`}>
            {link.direction === "to" ? "Depends on " : "Used by "}
            <button type="button" disabled={onEnterRoom === undefined && !nodes.has(link.nodeId)} onClick={() => { setSelectedRoomId(link.nodeId); if (!nodes.has(link.nodeId)) onEnterRoom?.(link.nodeId); }}>{link.label}</button>
            {nodes.has(link.nodeId) ? "" : " · outside view"} · {link.path}
          </li>)}</ul>}
          {links.length <= shownLinks.length ? null : <p>{links.length - shownLinks.length} more relationships in this room's supplied sample.</p>}
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

function Area({ label, width, top, bottom, fill }: { label: string; width: number; top: number; bottom: number; fill: string }) {
  return (
    <g role="group" aria-label={label}>
      <rect x={ROOM_LEFT} y={top} width={width} height={bottom - top} fill={fill} />
      <text x={ROOM_LEFT + 6} y={top + 15} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">{label}</text>
    </g>
  );
}

function AmbientFixtures({ x, y }: { x: number; y: number }) {
  return <g data-ambient-fixtures="" aria-label="Shared break and planning fixtures" transform={`translate(${x} ${y})`}>
    <title>Shared break and planning fixtures</title>
    <Frame name="prop.board" x={0} y={0} scale={2} />
    <Frame name="prop.coffee" x={64} y={0} scale={2} />
    <Frame name="prop.bench" x={128} y={32} scale={2} />
    <text x="112" y="13" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="7">COMMON AREA</text>
  </g>;
}


/** Physical equipment is assembled from native atlas frames; more space adds modules. */
function Equipment({ item, showCount }: { item: RoomContent; showCount: boolean }) {
  const { width, height, kind, workSurface } = item;
  const feature = kind === "component" ? item.feature : kind;
  const prop = feature === "source" ? "prop.rack" : feature === "tests" || feature === "configuration" ? "prop.terminal"
    : feature === "documentation" || feature === "assets" ? "prop.cabinet" : "prop.bench";
  const modules = workSurface ? Math.min(3, Math.max(2, Math.floor((width - 8) / 32))) : 1;
  const propY = workSurface && height >= 64 ? 4 : 1;
  const benchY = workSurface ? height - 21 : height - 15;
  if (kind === "component") return <g data-component-feature={feature ?? "unavailable"} transform={`translate(${item.x} ${item.y})`}>
    <Frame name={feature === "empty" || feature === "unavailable" ? "prop.bench" : prop} x={4} y={2} />
    <rect x="24" y="17" width={width - 28} height="4" fill="#78969a" />
    <Frame name="prop.workbench" x={width - 16} y={4} />
    <text x="25" y="13" fill="#f2edda" fontFamily="ui-monospace, monospace" fontSize="9">{shortLabel(item.label.split("/").at(-1) || item.label, Math.floor((width - 30) / 5.5))}</text>
  </g>;
  return <g transform={`translate(${item.x} ${item.y})`}>
    {workSurface ? <FrameRun name="prop.workbench" x={0} y={benchY} length={width} />
      : <Frame name="prop.bench" x={0} y={benchY} />}
    {Array.from({ length: modules }, (_, index) => <Frame key={index} name={prop} x={8 + index * (height >= 64 ? 48 : 32)} y={propY} scale={height >= 64 ? 2 : 1} />)}
    <text x="4" y={height - 1} fill="#f2edda" fontFamily="ui-monospace, monospace" fontSize="9">
      {item.label}{showCount ? ` ${item.count}` : ""}
    </text>
  </g>;
}

function InventoryInfo({ node, room }: { node: SceneTopology["nodes"][number]; room: SceneRoomLayout }) {
  const inventory = node.inventory;
  const groups = room.contents;
  const omittedComponents = (node.components?.length ?? 0) - groups.filter((group) => group.kind === "component").length;
  const omittedFiles = inventory === undefined ? 0 : Object.values(inventory[node.inventoryScope === "direct" ? "direct" : "total"]).reduce((sum, count) => sum + count, 0) - groups.filter((group) => group.kind !== "component").reduce((sum, group) => sum + group.count, 0);
  return <>
    {inventory === undefined ? <p>Scanned inventory unavailable.</p> : <>
    <table><caption>Scanned files · subtree includes direct contents</caption><thead><tr><th>Kind</th><th>Direct</th><th>Subtree</th></tr></thead><tbody>
      {Object.entries(inventoryLabels).map(([kind, label]) => <tr key={kind}><th>{label}</th><td>{inventory.direct[kind as keyof typeof inventoryLabels]}</td><td>{inventory.total[kind as keyof typeof inventoryLabels]}</td></tr>)}
    </tbody></table>
    <p>Equipment represents {node.inventoryScope === "direct" ? "direct files in this component" : "this component’s subtree"}. Test inventory does not indicate test results.</p>
    <p>{inventory.samples.length === 0 ? "No direct filenames in this sample." : `Direct filename sample: ${inventory.samples.join(", ")}.`}{inventory.samples_omitted === 0 ? "" : ` ${inventory.samples_omitted} direct filenames omitted.`}</p>
    {omittedFiles === 0 ? null : <p>{omittedFiles} scanned files in other categories are not pictured; all served counts are in the table.</p>}
    </>}
    {omittedComponents === 0 ? null : <p>{omittedComponents} subcomponents without pictured plans; enter this room and use its pages to reach them.</p>}
  </>;
}
