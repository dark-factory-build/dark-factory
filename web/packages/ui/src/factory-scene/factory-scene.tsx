import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  placeWorkers,
  workPositions,
  inventoryLabels,
  type RoomContent,
  type SceneRoomLayout,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { workerFrames } from "./appearance.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
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
function SceneWorkers({ layout, placements, nodes, workers, tasks, connected, selectedWorkerId, onSelectWorker, onSelectHumanRequest }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectHumanRequest"> & {
  layout: ReturnType<typeof layoutScene>;
  placements: ReturnType<typeof placeWorkers>;
  nodes: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  tasks: readonly SceneTask[];
  connected: boolean;
}) {
  // Inventory/dependency metadata may change without changing a route's geometry.
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.restingTop, layout.corridors, layout.rooms.map(({ id, x, y, width, height, door }) => [id, x, y, width, height, door])]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && placements.some((placement) => placement.id === worker.id && placement.area === "room" && (placement.bayId !== undefined || layout.rooms.find((room) => room.id === placement.roomId)?.contents.some((item) => item.workSurface)))).map((worker) => worker.id)), [workers, placements, layout]);
  const positions = useSceneMotion(layout, placements, geometryKey, connected, active);
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
          : worker.paused ? "paused in resting area" : "ready in resting area";

        const attention = tasks.flatMap((order) => order.agentId === worker.id ? order.humanRequestIds : []);
        const frames = workerFrames(worker, position.motion);
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-worker-location={worker.location ?? "resting"}
            data-worker-action={position.motion.action}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g role="img" aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...sceneAction(onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
              <title>{`${worker.name} · ${location}`}</title>
              <rect x={-12} y={-12} width="24" height="24" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}
              {position.motion.action !== "interacting" ? null : <g aria-hidden="true" fill="none" stroke="#8b9d9d" strokeWidth="2">
                <path d={`M-5 -3L-8 -9L-5 ${position.motion.frame === 0 ? -16 : -14} M5 -3L8 -9L5 ${position.motion.frame === 0 ? -14 : -16}`} />
              </g>}</g>
            </g>
            {attention.length === 0 ? null : <g {...sceneAction(onSelectHumanRequest === undefined ? undefined : () => onSelectHumanRequest(attention[0]!))} aria-label={`Question from ${worker.name}`} data-human-request-id={attention[0]}>
              <rect x="10" y="-20" width="22" height="22" rx="3" fill="#f0c777" /><text x="21" y="-5" textAnchor="middle" fill="#172330" fontSize="16" fontWeight="700">!</text>
            </g>}
          </g>
        );
      })}</>;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ topology, workers, omittedLocations = 0, enterableRoomIds = [], onEnterRoom, selectedWorkerId, onSelectWorker, tasks = [], selectedTaskId, onSelectTask, onOpenQueue, onSelectHumanRequest, connected = true }: FactorySceneProps) {
  const [selectedRoomId, setSelectedRoomId] = useState<string>();
  const layout = useMemo(() => layoutScene(topology), [topology]);
  const placements = useMemo(() => placeWorkers(layout, workers), [layout, workers]);
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
      style={{ display: "block", width: "100%", minWidth: layout.width, maxWidth, height: "auto", margin: "0 auto", background: "#08131d" }}
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
          <Frame name="tile.floor.0" x={0} y={0} />
          <Frame name="tile.floor.1" x={FRAME} y={0} />
          <Frame name="tile.floor.1" x={0} y={FRAME} />
          <Frame name="tile.floor.0" x={FRAME} y={FRAME} />
        </pattern>
        <pattern id="df-wall" patternUnits="userSpaceOnUse" width={FRAME} height={FRAME}>
          <Frame name="tile.wall" x={0} y={0} />
        </pattern>
      </defs>
      <rect width={layout.width} height={sceneHeight} fill="#08131d" />
      <text x={PADDING} y="20" fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="10" fontWeight="700">
        {layout.rooms.length} SPACES
      </text>

      {layout.corridors.map((corridor, index) => <rect key={index} data-corridor="" {...corridor} fill="url(#df-floor)" />)}
      <rect x={PADDING} y={layout.restingTop - 32} width={ROOM_LEFT - PADDING} height={boardTop - layout.restingTop + 32} fill="url(#df-floor)" />
      {layout.headings.map((heading) => (
        <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="9" fontWeight="700">
          {shortLabel(heading.label)}
        </text>
      ))}

      {selectedGeometry === undefined ? null : shownLinks.map((link) => {
        const other = layout.rooms.find((room) => room.id === link.nodeId);
        if (other === undefined) return null;
        const source = link.direction === "to" ? selectedGeometry : other;
        const target = link.direction === "to" ? other : selectedGeometry;
        return <path key={`${link.direction}:${link.nodeId}`} data-static-dependency="" d={`M${source.x + source.width / 2},${source.y - 6} H${target.x + target.width / 2} V${target.y - 6}`} fill="none" stroke="#beacff" strokeWidth="1.5" strokeDasharray="4 4" pointerEvents="none"><title>{`Static dependency: ${nodes.get(source.id)?.label} → ${nodes.get(target.id)?.label}`}</title></path>;
      })}

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
            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="#17252f" />
            <path d={`M${room.x + 4} ${room.y + 40}H${room.x + room.width - 4}`} stroke="#31434d" strokeWidth="2" />
            <rect x={room.x} y={room.y} width={room.width} height={FRAME} fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} H${room.x + room.width} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#638095" strokeWidth="4" />
            {footprint.length === 0 ? null : <rect data-work-footprint={room.id} x={room.x + 5} y={room.y + 39} width={room.width - 10} height={room.height - 46} fill="#d8a94c" fillOpacity="0.10" stroke={footprint.some((order) => order.id === selectedTaskId) ? "#80ddff" : "#d8a94c"} strokeDasharray="3 3"><title>{`Observed changes ${footprint.some((order) => !order.roomIds.includes(room.id)) ? "within this component" : "in this area"} for ${footprint.length} running task(s): ${footprint.map((order) => order.id.slice(0, 8)).join(", ")}`}</title></rect>}
            {footprint.length === 0 ? null : <text x={room.x + room.width - 8} y={room.y + 44} textAnchor="end" fill="#f0c777" fontFamily="ui-monospace, monospace" fontSize="8">{footprint.some((order) => !order.roomIds.includes(room.id)) ? "CHANGES WITHIN" : "CHANGED"}</text>}
            {work.length === 0 ? null : <g
              data-workbench-task-id={task!.id}
              {...sceneAction(onSelectTask === undefined ? undefined : () => onSelectTask(task!.id))}
              aria-label={`Task ${task!.id.slice(0, 8)}: ${task!.title}; representative area${work.length > 1 ? `; ${work.length - 1} more running tasks in the queue panel` : ""}`}
            >
              <title>{task!.title}</title>
              <rect x={room.x + 8} y={room.door.y - 20} width="40" height="14" fill="#d9d2b5" stroke="#a6a087" />
              <text x={room.x + 11} y={room.door.y - 10} fill="#253441" fontSize="6" fontFamily="ui-monospace, monospace">{task!.id.slice(0, 8)}</text>
            </g>}
            {contents.map((item) => <g key={item.key} data-room-content={item.kind} {...sceneAction(item.targetId !== undefined && !nodes.has(item.targetId) && onEnterRoom === undefined ? undefined : () => { setSelectedRoomId(item.targetId ?? room.id); if (item.targetId !== undefined) onEnterRoom?.(item.targetId); })}
              aria-label={item.kind === "component" ? `Open component ${item.label}` : `Inspect ${item.count} ${item.label.toLowerCase()} files in ${node.label}`}>
              <title>{item.kind === "component" ? item.label : `${item.count} scanned ${item.label.toLowerCase()} files represented by this group`}</title>
              <Equipment item={item} positions={item.workSurface ? workPositions(room) : []} />
            </g>)}
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



      <Area label={`RESTING AREA · ${resting.length}`} width={layout.width - ROOM_LEFT - PADDING} top={layout.restingTop - 28} bottom={Math.max(layout.restingTop + 24, ...resting.map((placement) => placement.y + 24))} />
      {staging.length === 0 ? null : <Area label={`STAGING · ${staging.length}`} width={layout.width - ROOM_LEFT - PADDING} top={staging[0]!.y - 28} bottom={Math.max(...staging.map((placement) => placement.y + 24))} />}
      {outside.length === 0 ? null : <Area label={`OUTSIDE DISPLAYED ROOMS · ${outside.length}`} width={layout.width - ROOM_LEFT - PADDING} top={outside[0]!.y - 28} bottom={Math.max(...outside.map((placement) => placement.y + 24))} />}
      {overflow.length === 0 ? null : <Area label={`WORKER AREA AT CAPACITY · ${overflow.length}`} width={layout.width - ROOM_LEFT - PADDING} top={overflow[0]!.y - 28} bottom={Math.max(...overflow.map((placement) => placement.y + 24))} />}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="38" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      <SceneWorkers layout={layout} placements={placements} nodes={nodes} workers={workers} tasks={tasks} connected={connected} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectHumanRequest={onSelectHumanRequest} />

      {queued === 0 ? null : <g data-floor-queue="" {...sceneAction(onOpenQueue)} aria-label={`Open queue, ${queued} tasks`}>
        {[...Array(Math.min(queued, 3))].map((_, index) => <rect key={index} x={ROOM_LEFT + index * 3} y={boardTop + index * 3} width="24" height="28" fill="#d9d2b5" stroke={tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? "#80ddff" : "#a6a087"} />)}
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

function Area({ label, width, top, bottom }: { label: string; width: number; top: number; bottom: number }) {
  return (
    <g role="group" aria-label={label}>
      <rect x={ROOM_LEFT} y={top} width={width} height={bottom - top} fill="url(#df-floor)" />
      <text x={ROOM_LEFT + 6} y={top + 15} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">{label}</text>
    </g>
  );
}


/** Native SVG dimensions keep equipment on the workers’ pixel scale. */
function Equipment({ item, positions }: { item: RoomContent; positions: readonly { x: number; y: number }[] }) {
  const { width, height, kind, workSurface } = item;
  const ink = "#101b24";
  if (kind === "component") return <g transform={`translate(${item.x} ${item.y})`}>
    <rect x="2" y="3" width={width} height={height} fill={ink} opacity=".6" />
    <rect width={width} height={height} fill="#384e5a" stroke="#8197a0" strokeWidth="2" />
    <path d={`M4 4h${width - 8}v${height - 8}H4Z`} fill="#263b47" stroke="#5f7681" />
    <path d="M8 8h12v10H8Z M20 13h8" fill="none" stroke="#9cb9bf" strokeWidth="2" />
    <text x="6" y={height - 5} fill="#e0e4d7" fontSize="9" fontFamily="ui-monospace, monospace">{shortLabel(item.label.split("/").at(-1) || item.label, Math.floor((width - 10) / 5.4))}</text>
  </g>;
  const tint = kind === "tests" ? "#c5ae78" : kind === "documentation" ? "#c5ac88" : kind === "assets" ? "#b7a4ca" : kind === "source" ? "#8db2bd" : "#acaa9c";
  const objectX = workSurface ? width - 36 : 4;
  const rich = workSurface && height === 64;
  const front = height - 12;
  return <g transform={`translate(${item.x} ${item.y})`}>
    <rect x="3" y="5" width={width} height={height - 3} fill={ink} opacity=".6" />
    {workSurface ? <>
      <path d={`M0 ${front}H${width}v7H0Z`} fill={kind === "source" || kind === "tests" ? "#526674" : "#896f57"} stroke={kind === "source" || kind === "tests" ? "#99adb3" : "#c1a68a"} strokeWidth="2" />
      <path d={`M4 ${front + 8}v4 M${width - 6} ${front + 8}v4`} stroke="#455866" strokeWidth="4" />
      <path d={`M6 ${front + 4}H${width - 6}`} stroke="#5a493d" strokeWidth="2" />

    </> : <path d={`M0 19H${width}v4H0Z`} fill="#455967" stroke="#71838a" strokeWidth="2" />}
    {!rich ? null : <g stroke={ink} strokeWidth="2">
      {kind === "source" ? <>
        <rect x="4" y="2" width="88" height="42" fill="#263c48" stroke="#6a8894" />
        {[8, 36, 64].map((x) => <g key={x}><rect x={x} y="6" width="24" height="34" fill="#3d5a68" />
          {[12, 22, 32].map((y) => <path key={y} d={`M${x + 3} ${y}h18`} stroke="#8babb1" strokeWidth="4" />)}
        </g>)}
        <path d="M8 46h80" stroke="#4a626d" strokeWidth="4" />
      </> : kind === "tests" ? <>
        <path d="M4 28h90v18H4Z" fill="#52616a" stroke="#87979a" />
        {[10, 48].map((x) => <g key={x}><rect x={x} y="4" width="32" height="24" fill="#243740" stroke="#97a6a3" /><path d={`M${x + 5} 10h8 M${x + 5} 16h20 M${x + 5} 22h14`} stroke="#c4b786" /></g>)}
        <path d="M10 34h24 M50 34h24 M10 40h24 M50 40h24" stroke="#b8b29a" />
      </> : kind === "documentation" ? <>
        <rect x="4" y="2" width="64" height="44" fill="#594b3e" stroke="#b09a7b" />
        {[6, 26].map((y) => <g key={y}><path d={`M8 ${y + 16}h56`} stroke="#c2a27e" />{[8, 18, 28, 42, 52].map((x) => <rect key={x} x={x} y={y} width="6" height="14" fill={x % 3 === 0 ? "#a58b6b" : "#d0c2a0"} stroke="none" />)}</g>)}
        <path d="M76 28l12-4 12 4v18l-12-4-12 4Z M88 24v18" fill="#d7ceb0" stroke="#968c73" />
      </> : kind === "assets" ? <>
        <path d="M10 4h64v40H10Z" fill="#675d78" stroke="#b6a2c6" />
        <path d="M16 34l14-18 12 9 10-15 16 24Z" fill="#b3a4c4" stroke="none" />
        <rect x="17" y="10" width="8" height="6" fill="#dcc697" stroke="none" />
        <path d="M8 46h70 M18 44v6 M66 44v6" stroke="#8c7f86" strokeWidth="3" />
        <path d="M86 8h8v38h-8Z M98 18h6v28h-6Z" fill="#c5b9a3" stroke="#867d77" />
      </> : kind === "configuration" ? <>
        <rect x="4" y="2" width="76" height="44" fill="#77604b" stroke="#c1a283" />
        {[16, 36, 56].map((x) => <g key={x}><path d={`M${x} 10v28`} stroke="#2d3d43" strokeWidth="4" /><path d={`M${x - 5} ${x / 2 + 6}h10`} stroke="#d2c3a3" strokeWidth="4" /></g>)}
      </> : <>
        <path d="M4 12l10-8h50l8 8v34H4Z" fill="#80745e" stroke="#b8a98b" />
        <path d="M4 12h68 M12 18l52 22 M64 18L12 40" stroke="#b8a98b" />
      </>}
    </g>}
    <g transform={`translate(${objectX} ${rich ? 32 : 0})`} stroke={ink} strokeWidth="2">
      {kind === "source" ? <>
        <path d="M0 0h30v18H0Z" fill="#405d6c" stroke="#87a6af" />
        <path d="M4 5h22 M4 10h22 M4 15h22" stroke="#9ab9bd" />
        <path d="M8 3v14 M20 3v14" stroke="#263d4a" />
      </> : kind === "tests" ? <>
        <rect width="22" height="16" fill="#43535c" stroke="#9eafb4" />
        <path d="M5 5h4 M5 10h4 M13 5h4 M13 10h4" stroke="#c7b984" />
        <path d="M27 4h4v12h-4Z" fill="#b6a578" /><path d="M26 2h6" stroke="#c9bc97" />
      </> : kind === "documentation" ? <>
        <path d="M0 2h6v17H0Z M8 0h7v19H8Z M18 5h12v14H18Z" fill="#b49a74" stroke="#d6c1a0" />
        <path d="M2 7h2 M10 5h3 M21 9h6 M21 13h6" stroke="#5a554a" />
      </> : kind === "assets" ? <>
        <rect width="30" height="18" fill="#615b78" stroke="#ab9dbf" />
        <path d="M4 14l6-7 5 4 5-6 6 9Z" fill="#b3a5c7" stroke="none" />
        <rect x="4" y="3" width="4" height="3" fill="#d6c29c" stroke="none" />
      </> : kind === "configuration" ? <>
        <rect width="30" height="18" fill="#7c6853" stroke="#c2a484" />
        <path d="M5 4v10 M15 4v10 M25 4v10" stroke="#343c3f" />
        <path d="M2 8h6 M12 5h6 M22 11h6" stroke="#d3c2a1" />
      </> : <>
        <path d="M0 4h30v14H0Z" fill="#817968" stroke="#c1b79e" />
        <path d="M2 6l26 10 M28 6L2 16" stroke="#aa9e84" />
      </>}
    </g>
    {positions.map((point) => <g key={point.x} transform={`translate(${point.x - item.x} ${front - 2})`} aria-hidden="true">
      <path d="M-8 0H8v6H-8Z" fill="#30424c" stroke="#a9b8b6" strokeWidth="1" />
      <path d="M-5 2h10 M-5 4h10" stroke="#829696" strokeWidth="1" />
    </g>)}
    {!workSurface ? null : <g>
      <rect x={rich ? width - 80 : 4} y={rich ? 2 : front + 2} width={rich ? 76 : width - 8} height="10" fill="#d4cbb0" stroke="#746e5f" />
      <text x={rich ? width - 76 : 8} y={rich ? 10 : front + 10} fill="#27353a" fontSize="8" fontFamily="ui-monospace, monospace">{kind === "unclassified" ? "Other" : item.label} {item.count}</text>
    </g>}
    {workSurface ? null : <text x="0" y={height} fill={tint} fontSize="8" fontFamily="ui-monospace, monospace">{item.label} {item.count}</text>}
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
