import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  placeWorkers,
  roomContents,
  inventoryLabels,
  type RoomContent,
  type SceneRoomLayout,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { workerFrames } from "./appearance.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";

export type {
  SceneHeading,
  SceneLayout,
  SceneNode,
  SceneRoomLayout,
  SceneTopology,
  SceneWorker,
  SceneWorkerPlacement,
} from "./scene.js";

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

function retargetRoute(layout: ReturnType<typeof layoutScene>, motion: MotionState, point: { x: number; y: number }, destination: ReturnType<typeof placeWorkers>[number], at: number) {
  return routeFromCurrent(layout, point, destination);
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
      const route = retargetRoute(layout, old, current, placement, at);
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
    if (active.size === 0 && ![...motions.current.values()].some((motion) => motionPoint(motion, at).walking)) return;
    let frame = requestAnimationFrame((time) => setClock(time));
    return () => cancelAnimationFrame(frame);
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
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.restingTop, layout.corridors, layout.rooms.map(({ id, x, y, width, height, door, standing }) => [id, x, y, width, height, door, standing])]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && placements.some((placement) => placement.id === worker.id && placement.area === "room")).map((worker) => worker.id)), [workers, placements]);
  const positions = useSceneMotion(layout, placements, geometryKey, connected, active);
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  return <>{placements.map((placement) => {
        const worker = workerById.get(placement.id);
        if (worker === undefined) return null;
        const position = positions.get(placement.id) ?? { ...placement, motion: { action: "still", frame: 0 } as WorkerMotion };
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const location = worker.location === "working"
          ? `representative location${worker.locationWithin ? " within this component; more specific observed area" : " near observed changes"}${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? "at workstation" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
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
            <g role="img" aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...(onSelectWorker === undefined ? {} : { onClick: () => onSelectWorker(worker.id), style: { cursor: "pointer" } })}>
              <title>{`${worker.name} · ${location}`}</title>
              <rect x={-12} y={-12} width="24" height="24" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined} transform={position.motion.action === "interacting" && position.motion.frame === 1 ? "translate(1 -1)" : undefined}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}</g>
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
        const contents = roomContents(node, room);
        const work = footprint.filter((order) => (order.displayRoomId ?? order.representativeRoomId) === room.id);
        const task = work.find((order) => order.id === selectedTaskId) ?? work[0];
        const canEnter = onEnterRoom !== undefined && enterable.has(room.id);
        return (
          <g key={room.id} data-room-id={room.id}>
            <title>{node.path}</title>
            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="url(#df-floor)" />
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
              <Equipment item={item} />
            </g>)}
            {node.inventory === undefined ? <text x={room.x + 12} y={room.y + room.height - 52} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">INVENTORY UNAVAILABLE</text> : contents.length === 0 ? <text x={room.x + 12} y={room.y + 62} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">NO SCANNED FILES</text> : null}
            {(node.dependencies?.links.length ?? 0) === 0 ? null : <g {...sceneAction(() => setSelectedRoomId(room.id))} aria-label={`Inspect static dependencies of ${node.label}`}>
              <rect x={room.x + room.width - 24} y={room.y + 20} width="16" height="10" fill="#493d68" stroke="#beacff" /><text x={room.x + room.width - 16} y={room.y + 28} textAnchor="middle" fill="#e4dafa" fontSize="9">↔</text>
            </g>}
            <g {...sceneAction(() => setSelectedRoomId(room.id))} aria-label={`Inspect ${node.label}`}>

            <text x={room.x + 8} y={room.y + 18} fill="#f2f6f8" fontFamily="ui-monospace, monospace" fontSize="11" fontWeight="700">
              {shortLabel(node.label, Math.floor((room.width - 16) / 7))}
            </text>
            </g>
            <text x={room.x + 8} y={room.y + 34} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">
              {node.sizeBucket === undefined ? "STRUCTURE UNAVAILABLE" : `${node.kind.toUpperCase()} · ${node.sizeBucket.toUpperCase()}`}
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


/** A few multi-tile silhouettes in the existing SVG, rather than one object per file. */
function Equipment({ item }: { item: RoomContent }) {
  const storage = item.kind === "documentation" || item.kind === "component";
  return <g transform={`translate(${item.x} ${item.y})`}>
    <svg width={item.width} height={item.height - 8} viewBox="0 0 48 32" preserveAspectRatio="none" aria-hidden="true">
      <rect x="2" y="4" width="46" height="28" rx="2" fill="#02090e" fillOpacity=".7" />
      {item.kind === "source" ? <>
        <rect x="1" y="1" width="20" height="29" fill="#314959" stroke="#7894a5" /><rect x="25" y="1" width="20" height="29" fill="#263c4c" stroke="#7894a5" />
        {[7, 14, 21].map((y) => <g key={y}><path d={`M4 ${y}H18 M28 ${y}H42`} stroke="#9ac9d5" strokeWidth="3" /><path d={`M5 ${y}h2 M29 ${y}h2`} stroke="#182733" strokeWidth="3" /></g>)}
      </> : item.kind === "tests" ? <>
        <path d="M2 18H45V23H2Z M5 23V31 M41 23V31" fill="#a98768" stroke="#cbb391" strokeWidth="2" />
        <rect x="5" y="2" width="23" height="15" fill="#344252" stroke="#9babb7" /><path d="M8 11h4l3-6 4 9 3-5h3" fill="none" stroke="#e6c575" strokeWidth="2" /><path d="M34 4v11h8V4 M32 4h12" fill="#796d97" stroke="#c4b3df" strokeWidth="2" />
      </> : storage ? <>
        <rect x="3" y="1" width="40" height="29" fill={item.kind === "component" ? "#445967" : "#8b694a"} stroke="#b9a48a" />
        {[4, 13, 22].map((y) => <g key={y}><rect x="6" y={y} width="34" height="6" fill={item.kind === "component" ? "#8194a0" : "#dfcfab"} /><path d={`M20 ${y + 3}h7`} stroke="#3c4346" strokeWidth="2" /></g>)}
      </> : item.kind === "configuration" ? <>
        <path d="M4 7L10 1H41L45 24H4Z" fill="#8a614c" stroke="#d3a77d" /><path d="M8 11H40" stroke="#e9c39e" />
        {[12, 24, 36].map((x) => <g key={x}><circle cx={x} cy="17" r="4" fill="#222f38" stroke="#dcc2a1" /><path d={`M${x} 17v-3`} stroke="#a9c8d2" /></g>)}<path d="M9 25v6 M39 25v6" stroke="#717d82" strokeWidth="3" />
      </> : item.kind === "assets" ? <>
        <rect x="2" y="1" width="43" height="24" rx="2" fill="#384058" stroke="#aaa7d1" /><rect x="5" y="4" width="37" height="18" fill="#64668b" /><circle cx="34" cy="8" r="3" fill="#dec79c" /><path d="M6 21L17 8l8 10 6-6 10 9" fill="#b5a6cf" /><path d="M23 26v4 M15 31h17" stroke="#97a5b4" strokeWidth="2" />
      </> : <>
        <rect x="3" y="6" width="40" height="24" fill="#746958" stroke="#c3b39b" /><path d="M3 6l8-5h25l7 5 M8 9l30 18 M38 9L8 27" fill="none" stroke="#b9a482" strokeWidth="2" /><text x="23" y="23" textAnchor="middle" fill="#ede2c9" fontSize="16">?</text>
      </>}
    </svg>
    <text x={item.width / 2} y={item.height} textAnchor="middle" fill="#d0dae0" fontFamily="ui-monospace, monospace" fontSize="7">{shortLabel(item.kind === "component" ? item.label : `${item.label} ${item.count}`, Math.floor(item.width / 4))}</text>
  </g>;
}

function InventoryInfo({ node, room }: { node: SceneTopology["nodes"][number]; room: SceneRoomLayout }) {
  const inventory = node.inventory;
  const groups = roomContents(node, room);
  const omittedComponents = (node.components?.length ?? 0) - groups.filter((group) => group.kind === "component").length;
  const omittedFiles = inventory === undefined ? 0 : Object.values(inventory.total).reduce((sum, count) => sum + count, 0) - groups.filter((group) => group.kind !== "component").reduce((sum, group) => sum + group.count, 0);
  return <>
    {inventory === undefined ? <p>Scanned inventory unavailable.</p> : <>
    <table><caption>Scanned files · subtree includes direct contents</caption><thead><tr><th>Kind</th><th>Direct</th><th>Subtree</th></tr></thead><tbody>
      {Object.entries(inventoryLabels).map(([kind, label]) => <tr key={kind}><th>{label}</th><td>{inventory.direct[kind as keyof typeof inventoryLabels]}</td><td>{inventory.total[kind as keyof typeof inventoryLabels]}</td></tr>)}
    </tbody></table>
    <p>Equipment groups represent subtree inventory. Shared-path components overlap; do not add their totals. Test files do not show test success or coverage.</p>
    <p>{inventory.samples.length === 0 ? "No direct filenames in this sample." : `Direct filename sample: ${inventory.samples.join(", ")}.`}{inventory.samples_omitted === 0 ? "" : ` ${inventory.samples_omitted} direct filenames omitted.`}</p>
    {omittedFiles === 0 ? null : <p>{omittedFiles} scanned files in other categories are not pictured; all served counts are in the table.</p>}
    </>}
    {omittedComponents === 0 ? null : <p>{omittedComponents} subcomponents without pictured cabinets; enter this room and use its pages to reach them.</p>}
  </>;
}
