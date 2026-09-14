import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  placeWorkers,
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
function useSceneMotion(layout: ReturnType<typeof layoutScene>, placements: ReturnType<typeof placeWorkers>, topologyDigest: string, connected: boolean) {
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
    if (![...motions.current.values()].some((motion) => motionPoint(motion, at).walking)) return;
    let frame = requestAnimationFrame((time) => setClock(time));
    return () => cancelAnimationFrame(frame);
  }, [clock, connected, reduced]);

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
        : { action: placement.area === "room" ? "interacting" : "still", frame: 0 },
    });
  }
  return output;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ topology, workers, omittedLocations = 0, enterableRoomIds = [], onEnterRoom, selectedWorkerId, onSelectWorker, tasks = [], selectedTaskId, onSelectTask, onOpenQueue, onSelectHumanRequest, connected = true }: FactorySceneProps) {
  const [selectedRoomId, setSelectedRoomId] = useState<string>();
  const layout = useMemo(() => layoutScene(topology), [topology]);
  const placements = useMemo(() => placeWorkers(layout, workers), [layout, workers]);
  const positions = useSceneMotion(layout, placements, topology.digest, connected);
  const nodes = new Map(topology.nodes.map((node) => [node.id, node]));
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  const resting = placements.filter((placement) => placement.area === "resting");
  const staging = placements.filter((placement) => placement.area === "staging");
  const overflow = placements.filter((placement) => placement.area === "overflow");
  const outside = placements.filter((placement) => placement.area === "outside");
  const boardTop = Math.max(layout.height, ...placements.map((placement) => placement.y + 24)) + PADDING;
  const sceneHeight = boardTop + (tasks.some((task) => task.status === "queued") ? 48 : 0) + PADDING;
  const affected = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => order.roomIds.includes(room.id))]));
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
        const work = footprint.filter((order) => order.representativeRoomId === room.id);
        const task = work.find((order) => order.id === selectedTaskId) ?? work[0];
        const canEnter = onEnterRoom !== undefined && enterable.has(room.id);
        return (
          <g key={room.id} data-room-id={room.id}>
            <title>{node.path}</title>
            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="url(#df-floor)" />
            <rect x={room.x} y={room.y} width={room.width} height={FRAME} fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} H${room.x + room.width} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#638095" strokeWidth="4" />
            {footprint.length === 0 ? null : <rect data-work-footprint={room.id} x={room.x + 5} y={room.y + 39} width={room.width - 10} height={room.height - 46} fill="#d8a94c" fillOpacity="0.10" stroke={footprint.some((order) => order.id === selectedTaskId) ? "#80ddff" : "#d8a94c"} strokeDasharray="3 3"><title>{`Observed changed area for ${footprint.length} running task(s): ${footprint.map((order) => order.id.slice(0, 8)).join(", ")}`}</title></rect>}
            {footprint.length === 0 ? null : <text x={room.x + room.width - 8} y={room.y + 44} textAnchor="end" fill="#f0c777" fontFamily="ui-monospace, monospace" fontSize="8">CHANGED</text>}
            {work.length === 0 ? null : <g
              data-workbench-task-id={task!.id}
              {...sceneAction(onSelectTask === undefined ? undefined : () => onSelectTask(task!.id))}
              aria-label={`Task ${task!.id.slice(0, 8)}: ${task!.title}; representative area${work.length > 1 ? `; ${work.length - 1} more running tasks in the queue panel` : ""}`}
            >
              <title>{task!.title}</title>
              <rect x={room.x + 8} y={room.door.y - 20} width="40" height="14" fill="#d9d2b5" stroke="#a6a087" />
              <text x={room.x + 11} y={room.door.y - 10} fill="#253441" fontSize="6" fontFamily="ui-monospace, monospace">{task!.id.slice(0, 8)}</text>
            </g>}
            <Frame name="tile.workstation" x={room.workstation.x} y={room.workstation.y} />
            {room.furnishings.map((item) => <g key={item.kind} data-room-content={item.kind}><title>{item.label}</title>
              <Frame name={item.kind === "board" ? "tile.board" : "tile.cabinet"} x={item.x} y={item.y} />
              {item.kind !== "connections" ? null : <text x={item.x + 8} y={item.y + 8} textAnchor="middle" fontSize="8" fill="#beacff">↔</text>}
            </g>)}
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

      {placements.map((placement) => {
        const worker = workerById.get(placement.id);
        if (worker === undefined) return null;
        const position = positions.get(placement.id) ?? { ...placement, motion: { action: "still", frame: 0 } as WorkerMotion };
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const location = worker.location === "working"
          ? `representative location near observed changes${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? "at workstation" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
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
              {frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}
            </g>
            {attention.length === 0 ? null : <g {...sceneAction(onSelectHumanRequest === undefined ? undefined : () => onSelectHumanRequest(attention[0]!))} aria-label={`Question from ${worker.name}`} data-human-request-id={attention[0]}>
              <rect x="10" y="-20" width="22" height="22" rx="3" fill="#f0c777" /><text x="21" y="-5" textAnchor="middle" fill="#172330" fontSize="16" fontWeight="700">!</text>
            </g>}
          </g>
        );
      })}

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
      {selectedRoom === undefined ? null : <details key={selectedRoom.id}>
        <summary>Room info</summary>
        {selectedRoom.path === selectedRoom.label ? null : <p>{selectedRoom.path}</p>}
        <p>{selectedRoom.kind} · {selectedRoom.sizeBucket ?? "size unavailable"}{selectedRoom.language ? ` · ${selectedRoom.language}` : ""}{selectedRoom.childCount === undefined ? "" : ` · ${selectedRoom.childCount} subcomponents`}</p>
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
