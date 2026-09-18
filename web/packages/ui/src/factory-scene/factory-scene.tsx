import { useLayoutEffect, useEffect, useMemo, useRef, useState, type MouseEvent, type FocusEvent, type PointerEvent, type KeyboardEvent } from "react";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  commonSeats,
  placeWorkers,
  inventoryLabels,
  type RoomContent,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { workerFrames } from "./appearance.js";
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
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && placements.some((placement) => placement.id === worker.id && placement.area === "room" && layout.rooms.find((room) => room.id === placement.roomId)?.contents.some((item) => item.workSurface))).map((worker) => worker.id)), [workers, placements, layout]);
  const positions = useSceneMotion(layout, placements, geometryKey, connected, active);
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
        const seated = placement.area !== "room" && position.motion.action !== "walking";
        const frames = workerFrames(worker, position.motion).filter((frame) => !seated || !frame.startsWith("person.tool."));
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-worker-location={worker.location ?? "resting"}
            data-worker-action={position.motion.action}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g role="img" className="dfFactoryScene__target" data-tooltip={`${worker.name} · ${worker.activity}\n${placement.area === "room" ? `Working near ${worker.locationLabel ?? room?.label ?? "observed changes"}` : placement.area === "resting" ? worker.paused ? "Paused · taking a break" : "Taking a break" : worker.location === "unobserved" ? "Planning · location not yet observed" : "Planning · work outside this room"}`} aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...sceneAction(onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
                <rect className="dfFactoryScene__focus" x={-12} y={-12} width="24" height="24" rx="3" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-seated={seated ? placement.area === "resting" ? "coffee" : "planning" : undefined} data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}
              {!seated ? null : <g aria-hidden="true">
                <path d="M-5 4h4v3h-5 M2 4h4v3H2" stroke="#838574" strokeWidth="2" fill="#4f5d59" />
                {placement.area === "resting" || !connected ? null : <g data-planning-light="" ><circle cx="12" cy="-14" r="12" fill="url(#df-lamplight)" /><path d="M12 -18v-5h-5" fill="none" stroke="#788379" strokeWidth="2" /><path d="M4 -20h6" stroke="#dfc38f" strokeWidth="3" /></g>}
        {placement.area === "resting" ? <g><rect x="3" y="-1" width="5" height="4" fill="#d1c8a9" /><path d="M8 0h2v2H8" fill="none" stroke="#d1c8a9" /></g> : <path d="M2 0l5 -4" stroke="#d1c8a9" strokeWidth="2" />}
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
export function FactoryScene({ topology, workers, appearance = DEFAULT_FLOOR_APPEARANCE, omittedLocations = 0, enterableRoomIds = [], onEnterRoom, selectedWorkerId, onSelectWorker, tasks = [], selectedTaskId, onSelectTask, onOpenQueue, onSelectHumanRequest, connected = true }: FactorySceneProps) {
  const [tooltip, setTooltip] = useState<{ text: string; x: number; y: number }>();
  const tooltipElement = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!tooltip || !tooltipElement.current) return;
    const height = tooltipElement.current.getBoundingClientRect().height;
    const y = Math.max(8, Math.min(tooltip.y, window.innerHeight - height - 8));
    if (y !== tooltip.y) setTooltip({ ...tooltip, y });
  }, [tooltip]);
  const showTooltip = (target: Element | null) => {
    if (!target) { setTooltip(undefined); return; }
    const box = (target.querySelector("rect") ?? target).getBoundingClientRect();
    setTooltip({ text: target.getAttribute("data-tooltip")!, x: Math.max(8, Math.min(box.left, window.innerWidth - 272)), y: Math.max(8, Math.min(box.bottom, window.innerHeight - 112)) });
  };
  const inspect = (event: PointerEvent<HTMLDivElement> | FocusEvent<HTMLDivElement> | MouseEvent<HTMLDivElement>) => {
    const element = event.target as Element;
    if (!element.closest('[role="tooltip"]')) showTooltip(element.closest("[data-tooltip]"));
  };
  const layout = useMemo(() => layoutScene(topology), [topology]);
  const placements = useMemo(() => placeWorkers(layout, workers), [layout, workers]);
  const nodes = new Map(topology.nodes.map((node) => [node.id, node]));
  const resting = placements.filter((placement) => placement.area === "resting");
  const staging = placements.filter((placement) => placement.area === "staging");
  const overflow = placements.filter((placement) => placement.area === "overflow");
  const outside = placements.filter((placement) => placement.area === "outside");
  const boardTop = Math.max(layout.height, ...placements.map((placement) => placement.y + 24)) + PADDING;
  const sceneHeight = boardTop + (tasks.some((task) => task.status === "queued") ? 48 : 0) + PADDING;
  const affected = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => order.status === "running" && (order.displayRoomIds ?? order.roomIds).includes(room.id))]));
  const enterable = new Set(enterableRoomIds);
  const queued = tasks.filter((order) => order.status === "queued").length;
  // A compact scope still needs room for readable labels, not poster-sized
  // sprites; larger scopes retain their existing scrollable viewport.
  const maxWidth = Math.min(640, layout.width * 2);

  return (
    <>
    <div className="dfFactoryFloor__map" onClick={inspect} onPointerOver={inspect} onFocus={inspect} onPointerLeave={() => setTooltip(undefined)} onBlur={() => setTooltip(undefined)} onScroll={() => showTooltip(typeof document !== "undefined" && document.activeElement?.matches("[data-tooltip]:focus-visible") ? document.activeElement : null)} onKeyDown={(event) => { if (event.key === "Escape") setTooltip(undefined); }} role="region" aria-label="Scrollable codebase floor" tabIndex={0}>
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
        <pattern id="df-floor" patternUnits="userSpaceOnUse" width="32" height="24">
          <rect width="32" height="24" fill="#202c30" />
          <path d="M1 2h13v9H1z M17 1h13v10H17z M-6 14H7v8H-6z M10 14h13v9H10z M26 14h12v8H26z" fill="#263237" stroke="#202b30" strokeWidth="1" />
          <path d="M3 3h9 M19 2h8 M12 15h8" stroke="#2b383c" strokeWidth="1" />
        </pattern>
        <pattern id="df-wall" patternUnits="userSpaceOnUse" width="24" height="12">
          <rect width="24" height="12" fill="#41494a" />
          <path d="M0 0h24 M0 6h24 M12 0v6 M0 6v6 M24 6v6" stroke="#20292d" strokeWidth="2" />
        </pattern>
        <radialGradient id="df-lamplight" cx="85%" cy="10%" r="95%">
          <stop offset="0" stopColor="#f2dab0" stopOpacity=".18" />
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
            {contents.map((item) => <g key={item.key} data-room-content={item.kind} aria-hidden="true"><Equipment item={item} operating={operating} scenery={appearance.scenery} /></g>)}
            <g tabIndex={0} className="dfFactoryScene__target" data-tooltip={roomInfo(node)} aria-label={roomInfo(node)}>
              <rect className="dfFactoryScene__focus" x={room.x + 8} y={room.y + 12} width={room.width - 44} height="24" rx="2" fill="#182429" />
              <text x={room.x + 14} y={room.y + 28} fill="#d7ddcf" fontFamily="ui-monospace, monospace" fontSize="10" fontWeight="700">{shortLabel(node.label, 19)}</text>
            </g>
            <g data-work-footprint={task === undefined ? undefined : room.id} data-room-operating={operating}
              data-workbench-task-id={task?.id}
              data-tooltip={task === undefined ? "No current work" : `${connected ? "Working" : "Disconnected · last observed work"} · ${task.title}${footprint.length > 1 ? `\n${footprint.length - 1} more tasks in this area` : ""}`}
              aria-label={task === undefined ? "No current work" : `Working: ${task.title}`}
              {...sceneAction(task === undefined || onSelectTask === undefined ? undefined : () => onSelectTask(task.id))}>
              <rect x={room.x + room.width - 32} y={room.y + 12} width="24" height="24" fill="transparent" />
              <path d={`M${room.x + room.width - 26} ${room.y + 18}h14v10h-14Z`} fill="#303e40" stroke="#53605b" />
              <rect x={room.x + room.width - 24} y={room.y + 20} width="10" height="5" fill={operating ? "#e5c58b" : "#626c64"} />
            </g>
            {!canEnter ? null : <g data-enter-room-id={room.id} {...sceneAction(() => onEnterRoom(room.id))} aria-label={`Enter ${node.label}`}>
              <rect x={room.x + room.width - 32} y={room.y + room.height - 24} width="24" height="16" fill="#172b38" stroke="#80ddff" />
              <text x={room.x + room.width - 20} y={room.y + room.height - 13} textAnchor="middle" fill="#80ddff" fontFamily="ui-monospace, monospace" fontSize="7">↳</text>
            </g>}
          </g>
        );
      })}



      {[
        { label: "Break room", people: resting, top: layout.restingTop, planning: false },
        ...[staging, outside, overflow].filter((people) => people.length > 0).map((people) => ({ label: "Planning", people, top: people[0]!.y, planning: true })),
      ].map(({ label, people, top, planning }) => {
        const seats = commonSeats(layout, people.length, top);
        const columns = seats.filter((seat) => seat.y === top).length;
        const width = Math.max(...seats.map((seat) => seat.x)) - ROOM_LEFT + 24;
        return <g key={top}>
          <Area label={label} width={width} top={top - 40} bottom={seats.at(-1)!.y + 24} />
          {seats.map((seat, index) => <g key={index} aria-hidden="true" data-common-seat={planning ? "planning" : "resting"} transform={`translate(${seat.x} ${seat.y})`}>
            <rect x="-7" y="3" width="14" height="6" rx="2" fill="#655948" stroke="#897c61" />
            <path d="M-5 9v3 M5 9v3" stroke="#3f4540" strokeWidth="3" />
            {index > 0 && seats[index - 1]!.y === seat.y && index % columns % 2 === 1 ? null : <>
              <rect x="-18" y="-19" width={seats[index + 1]?.y === seat.y ? 76 : 36} height="10" fill={planning ? "#455c5e" : "#655d4c"} stroke="#8c8871" />
              {planning ? <g><rect x="-12" y="-17" width="24" height="6" fill="#9fae9e" /><path d="M-9 -15h12v3H-3v-3 M5 -14h4" fill="none" stroke="#536e70" />
                <path d="M12 -18v-5h-5" fill="none" stroke="#788379" strokeWidth="2" /><path data-planning-light={connected} d="M4 -20h6" stroke={connected ? "#dfc38f" : "#626c64"} strokeWidth="3" />
              </g> : <g><rect x="5" y="-17" width="4" height="4" rx="1" fill="#cbc4a6" /><path d="M9 -16h2v2H9" fill="none" stroke="#cbc4a6" /></g>}
            </>}
          </g>)}
        </g>;
      })}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="24" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      <SceneWorkers layout={layout} placements={placements} nodes={nodes} workers={workers} tasks={tasks} connected={connected && appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectHumanRequest={onSelectHumanRequest} />

      {queued === 0 ? null : <g data-floor-queue="" {...sceneAction(onOpenQueue)} aria-label={`Open queue, ${queued} tasks`}>
        {[...Array(Math.min(queued, 3))].map((_, index) => <rect key={index} x={ROOM_LEFT + index * 3} y={boardTop + index * 3} width="24" height="28" fill="#d9d2b5" stroke={tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? "#80ddff" : "#a6a087"} />)}
        <text x={ROOM_LEFT + 40} y={boardTop + 18} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="10">QUEUE · {queued}</text>
      </g>}

    </svg>
    {tooltip === undefined ? null : <div ref={tooltipElement} className="dfFactoryTooltip" role="tooltip" style={{ left: tooltip.x, top: tooltip.y }}>{tooltip.text}</div>}
    </div>
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


/** Furniture is subdued scenery, never a second set of file-category controls. */
function Equipment({ item, operating, scenery }: { item: RoomContent; operating: boolean; scenery: FloorAppearance["scenery"] }) {
  return <g transform={`translate(${item.x} ${item.y})`} opacity={operating ? .9 : .55}>
    <rect x="4" y="27" width={item.width - 8} height="6" fill="#131e22" />
    <rect y="12" width={item.width} height="16" rx="2" fill="#665f4e" stroke="#8a8067" />
    <path d={`M4 29v5 M${item.width - 4} 29v5`} stroke="#424a46" strokeWidth="3" />
    <g transform={`translate(${item.width / 2 - 8} 0)`}>
      <rect width="16" height="12" fill="#3b4948" stroke="#65726b" />
      <rect x="2" y="2" width="12" height="8" fill={operating ? "#779c95" : "#263e40"} />
      {operating ? <path d="M4 4h8 M4 7h5" stroke="#bdd0b2" strokeWidth="1" /> : null}
      <path d="M3 14h10" stroke="#9b9c86" strokeWidth="2" />
    </g>
    {scenery === "off" ? null : <g opacity={scenery === "subtle" ? .6 : 1}>
    <rect x={item.width - 32} y="15" width="18" height="8" fill="#969480" transform={`rotate(-6 ${item.width - 23} 19)`} />
    <rect x="0" y="-25" width="44" height="10" fill="#424b46" stroke="#677165" />
    <path d="M7 -23v6 M13 -23v6 M20 -23v6 M28 -23v6" stroke="#88866d" strokeWidth="3" />
    </g>}
  </g>;
}

function roomInfo(node: SceneTopology["nodes"][number]) {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  const inventory = counts === undefined ? "Inventory unavailable" : Object.entries(inventoryLabels).filter(([kind]) => counts[kind as keyof typeof counts] > 0).map(([kind, label]) => `${counts[kind as keyof typeof counts]} ${label.toLowerCase()}`).join(" · ") || "No scanned files";
  return `${node.path === "." ? node.label : node.path}\n${node.language ? `${node.language} · ` : ""}${node.kind} · ${node.inventoryScope === "direct" ? "direct files" : "subtree"}\n${inventory}`;
}
