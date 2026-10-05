import { useLayoutEffect, useEffect, useMemo, useRef, useState, type MouseEvent, type FocusEvent, type PointerEvent, type KeyboardEvent, type ReactNode } from "react";
import type { PeerQuestionItem } from "@dark-factory/client";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  commonSeating,
  WORKER_SIZE,
  placeWorkers,
  placeErrands,
  breakRoomNook,
  inventoryLabels,
  responsibility,
  proposalsForEntity,
  type RoomContent,
  type SceneRoomLayout,
  type SceneProposal,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { breakRoomHabit, restingItem, workerFrames, workerPhase } from "./appearance.js";
import { catAt, catBed, chats, gossip, type Seat } from "./idle-life.js";
import { endsAt, messageAt, observe, send, type FloorMessage, type Seen } from "./messages.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
  proposals?: { items: readonly SceneProposal[]; selected?: string; onSelect: (id: string) => void };
  tools?: ReactNode;
  sourceDetails?: ReactNode;
  onDiscussSource?: () => void;
  requestedEntity?: { id: string };
  onSelectEntity?: (entityId: string | undefined) => void;
  onOpenLibrary?: (projectId?: string) => void;
  onOpenBoard?: (projectId?: string) => void;
  appearance?: FloorAppearance;
  topology: SceneTopology;
  /** Current project scope, when the floor has one. */
  projectId?: string;
  detailNodes?: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  workers: readonly SceneWorker[];
  tasks?: readonly SceneTask[];
  /** Questions between live tasks: shown as they are asked and answered, and stated while they wait. */
  peerQuestions?: readonly PeerQuestionItem[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  /** Open the existing project task queue from the floor inbox. */
  onOpenTasks?: (projectId?: string) => void;
  /** Open the operator's objective view from the planning table. */
  onOpenMissions?: (projectId?: string) => void;
  onSelectHumanRequest?: (requestId: string) => void;
  /** A dropped session reconciles to its latest snapshot instead of replaying local motion. */
  connected?: boolean;
  /** Current changed locations omitted by the bounded room map. */
  omittedLocations?: number;
  /** The selected agent is highlighted without changing its deterministic placement. */
  selectedWorkerId?: string;
  /** Pointer convenience only; the AGENTS list is the keyboard path. */
  onSelectWorker?: (workerId: string) => void;
}>;

export type AgentSpriteProps = Readonly<{
  agent: Pick<SceneWorker, "id" | "name" | "role" | "provider" | "appearance">;
  activity: SceneWorker["activity"];
  size?: number;
}>;

const FRAME = spriteAtlas.frame;
const NO_QUESTIONS: readonly PeerQuestionItem[] = [];
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
export function AgentSprite({ agent, activity, size }: AgentSpriteProps) {
  const frames = workerFrames({ ...agent, activity });
  return <svg viewBox={`0 0 ${FRAME} ${FRAME}`} overflow="hidden" width={size} height={size} style={size === undefined ? undefined : { width: size, height: size }} role="img" aria-label={`${agent.name}, ${agent.role}, ${activity}`} className="dfAgentSprite">
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
function useSceneMotion(layout: ReturnType<typeof layoutScene>, seated: ReturnType<typeof placeWorkers>, topologyDigest: string, connected: boolean, reduced: boolean, active: ReadonlySet<string>, workers: FactorySceneProps["workers"], errands: boolean, nearby: boolean, restless: (at: number) => boolean) {
  const motions = useRef(new Map<string, MotionState>());
  const priorTopology = useRef<string | undefined>(undefined);
  const priorConnected = useRef<boolean | undefined>(undefined);
  const motionsFloor = useRef(topologyDigest);
  const [clock, setClockState] = useState(0);
  // Where nothing may move, or the floor has been replaced, remembered motion and turns are ignored at once.
  const moving = connected && !reduced && (typeof document === "undefined" || document.visibilityState === "visible");
  // Break-room turns run on the time this floor has actually been moving: a hidden tab,
  // stilled motion or a long gap adds nothing, and a different floor starts again from seated.
  const movingTime = useRef({ total: 0, last: undefined as number | undefined });
  const setClock = (at: number) => {
    const time = movingTime.current;
    if (moving && time.last !== undefined && at - time.last < 1000) time.total += at - time.last;
    time.last = moving ? at : undefined;
    setClockState(at);
  };
  // The break room keeps no clock of its own: every couple of seconds of moving time it asks who has got up.
  const errandClock = moving && errands && motionsFloor.current === topologyDigest ? Math.floor(movingTime.current.total / 2000) * 2000 : undefined;
  // Who holds which piece, so that a visit outlasts changes among the others resting.
  const errandsBefore = useRef<ReturnType<typeof placeWorkers>>([]);
  const placements = useMemo(() => {
    // With the scenery off there is no furniture to walk to.
    if (errandClock === undefined) return errandsBefore.current = seated;
    const nook = breakRoomNook(layout, seated.filter((placement) => placement.area === "resting" && placement.roomId === undefined).length, seated.filter((placement) => placement.area !== "room" && placement.area !== "resting").length, nearby);
    const byId = new Map(workers.map((worker) => [worker.id, worker]));
    return errandsBefore.current = placeErrands(seated, nook, (id) => { const worker = byId.get(id); return worker === undefined ? undefined : breakRoomHabit(worker); }, errandClock, errandsBefore.current);
  }, [seated, errandClock, layout, workers, nearby]);

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
    if (motionsFloor.current !== topologyDigest) movingTime.current = { total: 0, last: undefined };
    motionsFloor.current = topologyDigest;
    setClock(at);
  }, [connected, layout, placements, reduced, topologyDigest]);

  useEffect(() => {
    if (!connected || reduced || typeof document !== "undefined" && document.visibilityState !== "visible" || typeof requestAnimationFrame !== "function") return;
    const at = now();
    // On an empty floor the clock is stopped for what lives there too, so nothing there asks for frames.
    if (placements.length > 0 && restless(at) || [...motions.current.values()].some((motion) => motionPoint(motion, at).walking)) {
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

  // Remembered motion lags a render behind what it is told. Where nothing may move, or the
  // floor it was worked out on has been replaced, everyone is simply drawn where they belong.
  const output = new Map<string, Readonly<{ x: number; y: number; motion: WorkerMotion; placement: ReturnType<typeof placeWorkers>[number] }>>();
  for (const placement of placements) {
    const state = moving && motionsFloor.current === topologyDigest ? motions.current.get(placement.id) : undefined;
    const current = state === undefined ? { point: placement, walking: false } : motionPoint(state, clock);
    const at = moving ? clock + workerPhase(placement.id) : undefined;
    output.set(placement.id, {
      // The placement this motion is actually on: pose and position are always of one moment.
      placement: state?.placement ?? placement,
      ...current.point,
      motion: current.walking
        ? { action: "walking", direction: current.direction!, frame: Math.floor((at ?? clock) / 150) % 2 as 0 | 1, at }
        : { action: active.has(placement.id) ? "interacting" : "still", frame: at === undefined ? 0 : Math.floor(at / (380 + workerPhase(placement.id) % 140)) % 2 as 0 | 1, at },
    });
  }
  return { placements, positions: output, pulse: moving ? clock : undefined };
}

/** The animation clock updates worker elements without rerendering the floor or atlas. */
function SceneWorkers({ nearby, errands, furniture, restingSeats, tray, peerQuestions, layout, placements: seated, nodes, workers, tasks, connected, animate, selectedWorkerId, onSelectWorker, onSelectTask, onSelectHumanRequest, onSelectProposal }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectTask" | "onSelectHumanRequest"> & {
  onSelectProposal?: (id: string) => void;
  layout: ReturnType<typeof layoutScene>;
  placements: ReturnType<typeof placeWorkers>;
  nodes: ReadonlyMap<string, SceneTopology["nodes"][number]>;
  tasks: readonly SceneTask[];
  connected: boolean;
  animate: boolean;
  /** Drawn over the workers: tables stand in front of whoever sits at them. */
  furniture: ReactNode;
  /** Whether the break-room furniture is there to be visited, and the cat to be found. */
  errands: boolean;
  nearby: boolean;
  /** The break room's seats, where its idle life happens. */
  restingSeats: readonly Seat[];
  /** Where waiting work is kept, and so where handed-over work comes from. */
  tray: { x: number; y: number };
  peerQuestions: readonly PeerQuestionItem[];
}) {
  // Inventory/dependency metadata may change without changing a route's geometry.
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.restingTop, layout.corridors, layout.rooms.map(({ id, x, y, width, height, door }) => [id, x, y, width, height, door])]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && seated.some((placement) => placement.id === worker.id && placement.area === "room" && layout.rooms.find((room) => room.id === placement.roomId)?.contents.some((item) => item.workSurface))).map((worker) => worker.id)), [workers, seated, layout]);
  const reduced = useReducedMotion();
  const mail = useRef<readonly FloorMessage[]>([]);
  const { placements, positions, pulse } = useSceneMotion(layout, seated, geometryKey, connected && animate, reduced, active, workers, errands, nearby, (at) => mail.current.some((message) => endsAt(message) > at) || errands && catAt(restingSeats.filter((seat) => seat.y === restingSeats[0]!.y), at)?.moving === true);
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  // Nobody on the floor, no pulse: what lives there rests as it does under any stopped clock.
  const at = placements.length === 0 ? undefined : pulse;
  // What changed since the last look is sent across the floor once; with the clock stopped it is only noted.
  const seen = useRef<Seen>(undefined);
  const live = useRef(false);
  live.current = at !== undefined;
  useEffect(() => {
    // The floor is mounted before there is any state, and may lose it again: a look only counts while
    // connected, so the first snapshot after connecting or reconnecting is history, never a burst of old news.
    if (!connected) { seen.current = undefined; return; }
    const { seen: next, events } = observe(seen.current, tasks, peerQuestions);
    seen.current = next;
    const started = now();
    mail.current = [...mail.current.filter((message) => endsAt(message) > started), ...(!live.current ? [] : events).flatMap((event) => {
      const to = seated.find((placement) => placement.id === event.to), from = seated.find((placement) => placement.id === event.from);
      if (to === undefined || event.from !== undefined && from === undefined || mail.current.some((message) => message.key === event.key)) return [];
      const origin = from ?? tray;
      return [send(event, started, origin, from === undefined ? undefined : routeBetween(layout, from, to), Math.hypot(to.x - origin.x, to.y - origin.y))];
    })].slice(-16);
  }, [tasks, peerQuestions, connected]);
  const flights = at === undefined ? [] : mail.current.flatMap((message) => { const to = positions.get(message.to); return to === undefined ? [] : [{ message, ...messageAt(message, to, at) }]; });
  // The sender raises a hand and says what it is as it leaves; the receiver raises one as it lands.
  const hailing = new Map<string, 0 | 1>(), calling = new Map<string, "ask" | "tell" | "hum">();
  for (const { message, hailing: who, sending, landed } of flights) {
    if (who !== undefined) hailing.set(who === "from" ? message.from! : message.to, who === "from" ? 1 : 0);
    if (sending) calling.set(message.from!, message.kind === "ask" ? "ask" : "tell");
    else if (landed && message.kind !== "assign") calling.set(message.to, "hum");
  }
  const agentOf = new Map(tasks.map((task) => [task.id, task.agentId]));
  const waiting = peerQuestions.filter((question) => !question.answered).map((question) => ({ asker: agentOf.get(question.source_task_id), asked: agentOf.get(question.target_task_id) }));
  const peerQuestionsByAgent = new Map<string, number>();
  for (const { asker, asked } of waiting) {
    if (asker !== undefined) peerQuestionsByAgent.set(asker, (peerQuestionsByAgent.get(asker) ?? 0) + 1);
    if (asked !== undefined) peerQuestionsByAgent.set(asked, (peerQuestionsByAgent.get(asked) ?? 0) + 1);
  }
  // Idle life belongs to people sitting still in their seats with nothing to ask of anyone.
  const seats = restingSeats.map((seat): Seat => {
    const id = placements.find((placement) => placement.area === "resting" && samePoint(placement, seat))?.id;
    return { ...seat, id, free: id !== undefined && positions.get(id)?.motion.action === "still" && workerById.get(id)?.activity !== "needs-you" };
  });
  const rows = [...new Set(seats.map((seat) => seat.y))].map((y) => seats.filter((seat) => seat.y === y));
  // The cat has the first table; every table has its talk.
  const bed = errands ? catBed(rows[0] ?? []) ?? { x: PADDING + 72, y: layout.restingTop - 30 } : undefined;
  const cat = errands ? catAt(rows[0] ?? [], at) ?? { ...bed!, frame: "sleep.0" as const, west: false, moving: false } : undefined;
  const news = useMemo(() => gossip(workers, tasks), [workers, tasks]);
  const talk = rows.flatMap((row) => chats(row, news, at));
  const catDoing = cat === undefined ? "" : cat.pettedBy !== undefined ? `being fussed over by ${workerById.get(cat.pettedBy)?.name}` : cat.frame.startsWith("sleep") ? "asleep" : cat.moving ? "on the prowl" : "supervising";
  // On the table the cat is in front of everyone; on the floor behind it, behind them.
  const puss = cat === undefined ? null : <g data-cat={cat.frame} role="img" aria-label={`The cat, ${catDoing}`} className="dfFactoryScene__target" data-tooltip={`The cat\n${catDoing.charAt(0).toUpperCase()}${catDoing.slice(1)}`} transform={`translate(${cat.x} ${cat.y}) scale(${WORKER_SIZE / FRAME})`}>
        <rect x="4" y="2" width="12" height="10" fill="transparent" />
        <g aria-hidden="true" transform={cat.west ? "translate(22 0) scale(-1 1)" : undefined}><Frame name={`cat.${cat.frame}`} x={3} y={-4} /></g>
      </g>;
  return <>
      {bed === undefined ? null : <g aria-hidden="true" pointerEvents="none" data-cat-bed="" transform={`translate(${bed.x} ${bed.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name="cat.bed" x={3} y={-4} /></g>}
      {cat !== undefined && cat.y !== rows[0]?.[0]?.y ? puss : null}
      {/* A question and its answer run along the corridors people walk, under their feet. */}
      <g aria-hidden="true" pointerEvents="none">{flights.map(({ message, point, trail }) => point === undefined || message.kind === "assign" ? null : <g key={message.key} data-pulse={message.kind} fill={message.kind === "ask" ? "#80ddff" : "#9fe7b0"}>
        {trail.map((spot, index) => <circle key={index} cx={spot.x} cy={spot.y} r="1.5" opacity={.5 - index * .15} />)}
        <circle cx={point.x} cy={point.y} r="6" opacity=".22" /><circle cx={point.x} cy={point.y} r="2.5" />
      </g>)}</g>
      {placements.map((told) => {
        const worker = workerById.get(told.id);
        if (worker === undefined) return null;
        const position = positions.get(told.id) ?? { ...told, placement: told, motion: { action: "still", frame: 0 } as WorkerMotion };
        // Drawn as where their motion has them, which is a render behind where they have just been told to go.
        const placement = position.placement;
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const picturedSurface = layout.rooms.find((candidate) => candidate.id === placement.roomId)?.contents.some((item) => item.workSurface);
        const location = worker.location === "working"
          ? `representative location${worker.locationWithin ? " within this component; more specific observed area" : " near observed changes"}${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? picturedSurface ? "at the pictured work surface" : "at a general work position; inventory unavailable or empty" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
          : worker.location === "unobserved" ? "working; location not yet observed"
          : worker.location === "last-observed" && worker.locationLabel !== undefined ? `last observed near changes in ${worker.locationLabel}; resting area`
          : worker.paused ? "paused in resting area" : "ready in resting area";

        const attention = tasks.flatMap((order) => order.agentId === worker.id ? order.humanRequestIds : []);
        const sitting = placement.area !== "room" && placement.errand === undefined && position.motion.action !== "walking";
        const stroking = cat?.pettedBy === worker.id ? cat.pettedFor : undefined;
        const chat = talk.find(({ between }) => between.includes(worker.id));
        const said = chat === undefined ? "" : `\n${workerById.get(chat.between[0])!.name}: ${chat.remark.line}${chat.replied ? `\n${workerById.get(chat.between[1])!.name}: ${chat.remark.reply}` : ""}`;
        const frames = workerFrames(worker, position.motion, placement.area === "room" || placement.errand !== undefined ? undefined : placement.area === "resting" ? "resting" : "planning", placement.errand, stroking, hailing.get(worker.id));
        const asking = waiting.flatMap(({ asker, asked }) => asker === worker.id && workerById.has(asked ?? "") ? [`\nAsked ${workerById.get(asked!)!.name} · waiting for an answer`] : asked === worker.id && workerById.has(asker ?? "") ? [`\nHas a question from ${workerById.get(asker!)!.name}`] : []);
        const peerQuestionCount = peerQuestionsByAgent.get(worker.id) ?? 0;
        const peerTaskID = peerQuestions.find((question) => !question.answered && (agentOf.get(question.source_task_id) === worker.id || agentOf.get(question.target_task_id) === worker.id))?.source_task_id;
        // A step lifts the whole body a pixel.
        const bob = position.motion.action === "walking" && position.motion.frame === 1 ? -1 : 0;
        // Only a profile facing east is drawn; walking west is its mirror image.
        const facingWest = position.motion.action === "walking" && position.motion.direction === "west";
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-reviewer-id={worker.review ? worker.id : undefined}
            data-worker-location={worker.location ?? "resting"}
            data-worker-action={position.motion.action}
            data-worker-facing={position.motion.action === "walking" ? position.motion.direction : undefined}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g aria-hidden="true" pointerEvents="none">
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-seated={sitting ? placement.area === "resting" ? "coffee" : "planning" : undefined} data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}><g transform={`scale(${WORKER_SIZE / FRAME})${bob === 0 ? "" : ` translate(0 ${bob})`}${facingWest ? " scale(-1 1)" : ""}`}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}</g>
              </g>
            </g>
            <g role="img" className="dfFactoryScene__target" data-tooltip={`${worker.name} · ${worker.activity}${worker.review ? `\nReview assignment: ${worker.review.scope}; representative visit, not exact file inspection` : ""}\n${placement.area === "room" ? `Working near ${worker.locationLabel ?? room?.label ?? "observed changes"}` : placement.errand === "shelf" ? "Taking a break · at the bookshelf" : placement.errand === "coffee" ? "Taking a break · at the coffee station" : placement.area === "resting" ? `${worker.paused ? "Paused · taking a break" : "Taking a break"}${stroking === undefined ? "" : " · fussing the cat"}${said}` : worker.location === "unobserved" ? "Planning · location not yet observed" : "Planning · work outside this room"}${[...new Set(asking)].join("")}`} aria-label={`${worker.name}, ${worker.review ? "reviewer" : worker.role}, ${worker.activity}, ${worker.review?.scope ?? location}`} {...sceneAction(worker.review && onSelectProposal ? () => onSelectProposal(worker.review!.proposalId) : onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
                <rect className="dfFactoryScene__focus" x={-12} y={-12} width="24" height="24" rx="3" fill="transparent" />
            </g>
            {worker.review === undefined ? null : <g aria-hidden="true"><rect x="7" y="1" width="10" height="13" fill="#e1d1aa" stroke="#5c787b" /><text x="12" y="10" textAnchor="middle" fill="#203d46" fontSize="8">R</text></g>}
            {attention.length === 0 ? null : <g {...sceneAction(onSelectHumanRequest === undefined ? undefined : () => onSelectHumanRequest(attention[0]!))} aria-label={`Question from ${worker.name}`} data-human-request-id={attention[0]}>
              <rect x="10" y="-20" width="22" height="22" rx="3" fill="#f0c777" /><text x="21" y="-5" textAnchor="middle" fill="#172330" fontSize="16" fontWeight="700">!</text>
            </g>}
            {peerQuestionCount === 0 ? null : <g {...sceneAction(peerTaskID !== undefined && onSelectTask !== undefined ? () => onSelectTask(peerTaskID) : onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))} aria-label={`${peerQuestionCount} peer question${peerQuestionCount === 1 ? "" : "s"}`} data-peer-question-count={peerQuestionCount}>
              <rect x="-32" y="-20" width="22" height="22" rx="3" fill="#80ddff" /><text x="-21" y="-5" textAnchor="middle" fill="#172330" fontSize="11" fontWeight="700">{peerQuestionCount}</text>
            </g>}
          </g>
        );
      })}
      {furniture}
      {/* On the table in front of each of them: a planner's lit lamp, or the one thing a resting worker has until it is in their hand. */}
      {placements.map((told) => {
        const worker = workerById.get(told.id), position = positions.get(told.id), placement = position?.placement ?? told;
        if (worker === undefined || placement.area === "room" || placement.errand !== undefined || position?.motion.action === "walking") return null;
        if (placement.area !== "resting") return !connected ? null : <g key={placement.id} data-planning-light="" aria-hidden="true" pointerEvents="none" transform={`translate(${position?.x ?? placement.x} ${(position?.y ?? placement.y) + TABLE_DROP})`}><circle cx="7" cy="-16" r="14" fill="url(#df-lamplight)" /><path d="M12 -18v-5h-5" fill="none" stroke="#788379" strokeWidth="2" /><path d="M4 -20h6" stroke="#dfc38f" strokeWidth="3" /></g>;
        const rest = restingItem(worker, worker.activity === "needs-you" || cat?.pettedBy === worker.id || hailing.has(worker.id) ? undefined : position?.motion.at);
        return rest.where !== "table" ? null : <g key={placement.id} aria-hidden="true" pointerEvents="none" data-table-item={rest.item} transform={`translate(${position?.x ?? placement.x} ${position?.y ?? placement.y}) scale(${WORKER_SIZE / FRAME})`}>
          <Frame name={`person.held.${rest.item}.chest`} x={-8} y={-8 + SET_DOWN} />
        </g>;
      })}
      {cat !== undefined && cat.y === rows[0]?.[0]?.y ? puss : null}
      {/* Said and felt, over everyone's heads: never words on the floor, those are in the tooltip. */}
      <g aria-hidden="true" pointerEvents="none">
        {[...calling].map(([id, glyph]) => { const position = positions.get(id); return position === undefined ? null : <g key={`call ${id}`} data-bubble={glyph} data-call={id} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${glyph}`} x={1} y={-21} /></g>; })}
        {flights.map(({ message, point }) => point === undefined || message.kind !== "assign" ? null : <g key={message.key} data-paper={message.to} transform={`translate(${point.x} ${point.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`paper.${Math.floor(at! / 120) % 2}`} x={-8} y={-8} /></g>)}
        {talk.map(({ speaking }) => { const position = speaking && positions.get(speaking.id); return !position ? null : <g key={speaking.id} data-bubble={speaking.glyph} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${speaking.glyph}`} x={1} y={-21} /></g>; })}
        {cat?.pettedBy === undefined || cat.pettedFor! < 600 ? null : <g data-heart="" opacity={cat.pettedFor! > 2800 ? .5 : 1} transform={`translate(${positions.get(cat.pettedBy)!.x} ${positions.get(cat.pettedBy)!.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name="heart" x={-4} y={-24 - Math.floor((cat.pettedFor! - 600) / 450)} /></g>}
      </g></>;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ proposals, tools, sourceDetails, onDiscussSource, requestedEntity, onSelectEntity, onOpenLibrary, onOpenBoard, topology, detailNodes, workers, appearance = DEFAULT_FLOOR_APPEARANCE, omittedLocations = 0, selectedWorkerId, onSelectWorker, tasks = [], peerQuestions = NO_QUESTIONS, selectedTaskId, onSelectTask, onOpenTasks, onOpenMissions, onSelectHumanRequest, projectId, connected = true }: FactorySceneProps) {
  const [selectedRoomId, setSelectedRoomId] = useState<string>();
  const [search, setSearch] = useState("");
  const mapElement = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState<{ top: number; bottom: number }>();
  const updateViewport = () => {
    const map = mapElement.current, svg = map?.querySelector?.("svg");
    if (!map || !svg || !map.clientHeight) return;
    const scale = svg.getBoundingClientRect().width / svg.viewBox.baseVal.width;
    if (scale > 0) { const top = (map.getBoundingClientRect().top - svg.getBoundingClientRect().top) / scale; setViewport({ top: top - 180, bottom: top + map.clientHeight / scale + 180 }); }
  };
  useEffect(() => {
    updateViewport();
    const map = mapElement.current;
    if (!map || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(updateViewport); observer.observe(map);
    return () => observer.disconnect();
  }, [topology.digest]);
  const selectEntity = (id: string | undefined) => { setSelectedRoomId(id); onSelectEntity?.(id); };
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
  const layout = useMemo(() => layoutScene(topology, proposals?.selected), [topology, proposals?.selected]);
  const placements = useMemo(() => placeWorkers(layout, workers, appearance.social), [layout, workers, appearance.social]);
  const visibleProposals = proposals?.items.filter((proposal) => !proposals.selected || proposal.id === proposals.selected) ?? [];
  const nodes = new Map(topology.nodes.filter((node) => !node.proposed || visibleProposals.some((proposal) => proposal.operations.some((operation) => operation.roomId === node.id))).map((node) => [node.id, node]));
  const availableNodes = new Map(detailNodes ?? nodes);
  for (const node of nodes.values()) if (node.proposed) availableNodes.set(node.id, { ...node, sourceIncomplete: true, components: (node.assemblies ?? []).filter((assembly) => !assembly.proposalId || visibleProposals.some((proposal) => proposal.id === assembly.proposalId)).map((assembly) => ({ id: assembly.id, label: assembly.label })) });
  for (const node of nodes.values()) for (const assembly of node.assemblies ?? []) if ((!assembly.proposalId || visibleProposals.some((proposal) => proposal.id === assembly.proposalId)) && !availableNodes.has(assembly.id)) availableNodes.set(assembly.id, { ...assembly, kind: "directory", project: node.project });
  const roomFor = (id: string) => layout.rooms.find((room) => room.id === id || nodes.get(room.id)?.assemblies?.some((assembly) => assembly.id === id || assembly.representedIds?.includes(id)));
  const focusEntity = (id: string) => { selectEntity(id); const room = roomFor(id); if (room) Array.from(mapElement.current?.querySelectorAll("[data-room-id]") ?? []).find((element) => element.getAttribute("data-room-id") === room.id)?.scrollIntoView({ block: "center", inline: "center", behavior: "instant" }); };
  useEffect(() => { if (requestedEntity) { setSearch(""); focusEntity(requestedEntity.id); } }, [requestedEntity]);
  const operationMatches = (operation: SceneProposal["operations"][number], roomId: string, item?: RoomContent) => item === undefined
    ? operation.roomId === roomId || operation.entityId === roomId
    : operation.entityId === item.entityId || item.representedIds?.includes(operation.entityId ?? "") || operation.roomId === roomId && !operation.entityId;
  const operationsFor = (roomId: string, item?: RoomContent) => visibleProposals.filter((proposal) => !item?.proposalId || item.proposalId === proposal.id).flatMap((proposal) => proposal.operations.filter((operation) => operationMatches(operation, roomId, item)).map((operation) => ({ proposal, operation })));
  const selectedRoom = availableNodes.get(selectedRoomId ?? "");
  const selectedAssembly = topology.nodes.flatMap((node) => node.assemblies ?? []).find((assembly) => assembly.id === selectedRoomId);
  const contents = (selectedAssembly?.representedIds?.length ?? 0) > 1
    ? selectedAssembly!.representedIds!.filter((id) => id !== selectedRoomId).flatMap((id) => { const node = availableNodes.get(id); return node ? [{ id, label: node.path }] : []; })
    : selectedRoom?.components ?? [];
  const links = selectedRoom?.dependencies?.links ?? [];
  const resting = placements.filter((placement) => placement.area === "resting");
  const planning = placements.filter((placement) => placement.area !== "room" && placement.area !== "resting");
  const commonResting = resting.filter((placement) => placement.roomId === undefined);
  const seating = commonSeating(layout, commonResting.length, planning.length);
  const seats = [...seating.resting, ...seating.planning];
  const commonBottom = Math.max(layout.restingTop, ...seats.map(({ y }) => y)) + 32;
  const nook = breakRoomNook(layout, commonResting.length, planning.length, appearance.social === "nearby");
  const station = { missions: { x: PADDING + 40, y: 48 }, tasks: { x: PADDING + 120, y: 48 } };
  const boardTop = Math.max(layout.height, commonBottom, 112, ...placements.map((placement) => placement.y + 24)) + PADDING;
  const sceneWidth = layout.width;
  const sceneHeight = boardTop + PADDING;
  const affected = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => order.status === "running" && (order.displayRoomIds ?? order.roomIds).includes(room.id))]));
  const cabling = useMemo(() => wires(layout, topology), [layout, topology]);
  const proposedCables = visibleProposals.flatMap((proposal) => (proposal.relationships ?? []).flatMap((edge) => {
    const from = nodes.get(edge.fromId ?? ""), to = nodes.get(edge.toId ?? "");
    if (!from || !to) return [];
    const routes = wires(layout, { digest: topology.digest, nodes: [{ ...from, dependencies: { omitted: 0, links: [{ nodeId: to.id, label: to.label, path: to.path, direction: "to", weight: edge.weight }] } }] }).routes;
    return routes.map((route) => ({ ...route, edge, proposal }));
  }));
  const queued = tasks.filter((order) => order.status === "queued").length;
  // The hand-off origin is the visible task tray, below rather than beside the seats.
  const tray = station.tasks;
  // A compact scope still needs room for readable labels, not poster-sized
  // sprites; larger scopes retain their existing scrollable viewport.
  const maxWidth = sceneWidth;
  // One table per row, as long as the row, standing between the viewer and the
  // people at it: it covers their laps, and what they rest with sits on it.
  const tables = [{ seats: seating.resting, planning: false }, { seats: seating.planning, planning: true }].flatMap(({ seats, planning }) =>
    [...new Set(seats.map((seat) => seat.y))].map((y) => { const row = seats.filter((seat) => seat.y === y), first = row[0]!;
      const tableWidth = row.at(-1)!.x - first.x + 36;
      return <g key={`${planning} ${y}`} data-common-table={planning ? "planning-workers" : "resting"} aria-hidden="true" pointerEvents="none" transform={`translate(${first.x} ${y + TABLE_DROP})`}>
        <rect x="-18" y="-21" width={tableWidth} height="10" fill={planning ? "#455c5e" : "#655d4c"} stroke="#8c8871" />
        {!planning ? null : <g><rect x="-17" y="-20" width={tableWidth - 2} height="8" fill="#9fae9e" /><path d={`M-14 -18h${Math.max(12, tableWidth - 18)}v3h-7v-3 M${tableWidth - 22} -17h4`} fill="none" stroke="#536e70" /></g>}
      </g>; }));

  return (
    <>
    <div className="dfFactoryEntityTools">
      <label>Find source <input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Package, assembly or path" /></label>
      {search === "" ? null : <div className="dfFactoryEntityTools__results">{[...availableNodes.values()].filter((node) => !node.proposed && `${node.label} ${node.path}`.toLowerCase().includes(search.toLowerCase())).slice(0, 30).map((node) => <button type="button" key={node.id} onClick={() => focusEntity(node.id)}>{node.path === "." ? node.label : node.path}</button>)}</div>}
      {tools}
      {proposals?.selected && proposals.items.some((item) => item.id === proposals.selected) ? <p className="dfFactoryEntityTools__notice"><span className="dfFactoryEntityTools__selection">Viewing: {proposals.items.find((item) => item.id === proposals.selected)?.title}</span><button type="button" onClick={() => proposals.onSelect("")}>Clear selection</button></p> : null}
    </div>
    <div ref={mapElement} className="dfFactoryFloor__map" onClick={inspect} onPointerOver={inspect} onFocus={inspect} onPointerLeave={() => { retainFocusedTooltip(); setLinkedFrom(undefined); }} onBlur={() => { setTooltip(undefined); setLinkedFrom(undefined); }} onScroll={() => { retainFocusedTooltip(); updateViewport(); }} onKeyDown={(event) => { if (event.key === "Escape") { setTooltip(undefined); setLinkedFrom(undefined); } }} role="region" aria-label="Scrollable codebase floor" tabIndex={0}>
    <svg
      viewBox={`0 0 ${sceneWidth} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory codebase floor"
      data-topology-digest={topology.digest}
      style={{ display: "block", width: "100%", minWidth: Math.min(sceneWidth, 864), maxWidth, height: "auto", margin: "0 auto", background: "#08131d" }}
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
      <rect x={PADDING} y={16} width={ROOM_LEFT - PADDING} height={commonBottom - 16} fill="url(#df-floor)" />
      {layout.headings.map((heading) => (
        <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="9" fontWeight="700">
          {shortLabel(heading.label)}
        </text>
      ))}

      {layout.rooms.map((room) => {
        const node = nodes.get(room.id);
        if (node === undefined) return null;
        const footprint = affected.get(room.id) ?? [];
        const contents = room.contents.filter((item) => !item.proposalId || visibleProposals.some((proposal) => proposal.id === item.proposalId));
        const task = footprint.find((order) => order.id === selectedTaskId) ?? footprint[0];
        const operating = connected && task !== undefined;
        const roomOperations = visibleProposals.flatMap((proposal) => proposal.operations.filter((operation) => operationMatches(operation, room.id) || node.assemblies?.some((assembly) => assembly.id === operation.entityId || assembly.representedIds?.includes(operation.entityId ?? ""))).map((operation) => ({ proposal, operation })));
        const relevant = roomOperations.length > 0 || room.contents.some((item) => operationsFor(room.id, item).length > 0) || placements.some((placement) => placement.roomId === room.id) || roomFor(selectedRoomId ?? "")?.id === room.id;
        if (layout.rooms.length > 20 && viewport && !relevant && (room.y + room.height < viewport.top || room.y > viewport.bottom)) return <g key={room.id} data-room-id={room.id} data-viewport-placeholder="true" aria-hidden="true"><rect x={room.x} y={room.y} width={room.width} height={room.height} fill="#222c2f" stroke="#465355" /><text x={room.x + 14} y={room.y + 28} fill="#91a69c" fontSize="10">{shortLabel(node.label, 32)}</text></g>;
        return (
          <g key={room.id} data-room-id={room.id} data-proposed-room={node.proposed || undefined}>

            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="url(#df-floor)" />
            <rect x={room.x} y={room.y} width={room.width} height="10" fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} M${room.x + room.width},${room.y} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#465355" strokeWidth="4" />
            <path d={`M${room.x + 4} ${room.y + 10}v${room.height - 14} M${room.x + room.width - 4} ${room.y + 10}v${room.height - 14}`} stroke="#141f23" strokeWidth="2" />
            <rect x={room.x + 4} y={room.y + 10} width={room.width - 8} height={room.height - 12} fill={operating ? "url(#df-lamplight)" : "#08131d"} opacity={operating ? 1 : .18} pointerEvents="none" />
            {appearance.scenery === "off" ? null : <g aria-hidden="true" opacity={appearance.scenery === "subtle" ? .35 : .6}>
              <path d={`M${room.door.x - 12} ${room.door.y - 8}h24 M${room.door.x - 7} ${room.door.y - 14}h14`} stroke="#53615c" strokeWidth="2" />
            </g>}
            {contents.map((item) => {
              const edits = operationsFor(room.id, item);
              const proposedPaths = [...new Set(edits.map(({ operation }) => operation.path))];
              const sourceHint = proposedPaths.find((path) => /\.(?:go|[cm]?[jt]sx?|py|rs|c|cpp|h|sh)$/.test(path));
              const pictured = node.proposed && item.resourceCounts === undefined && sourceHint ? { ...item, kind: "source" as const, responsibility: responsibility(sourceHint) } : item;
              return <g key={item.key} data-room-content={item.kind} data-entity-id={item.entityId} data-tooltip={item.entityId === undefined ? roomInfo(node) : `${item.label}${(item.representedIds?.length ?? 0) > 1 ? `\nAggregate of ${item.representedIds!.length} source areas` : ""}${item.sourceIncomplete ? "\nObserved proposed paths only; final contents and scale are not established." : ""}${item.parts?.length ? `\nFilename motifs: ${item.parts.map((part) => part.label).join(", ")}; not semantic analysis` : ""}\n${item.resourceCounts === undefined ? "Inventory unavailable" : Object.entries(item.resourceCounts).filter(([, count]) => count > 0).map(([kind, count]) => `${count} ${kind}`).join(" · ")}${edits.map(({ proposal, operation }) => `\n${proposal.title}: ${operation.kind} · ${operation.path}`).join("")}`} {...sceneAction(() => selectEntity(item.selectionId ?? item.entityId ?? room.id))} aria-label={`Inspect assembly ${item.label}`} className="dfFactoryScene__target">
                <rect className="dfFactoryScene__focus" x={item.x - 4} y={item.y - 17} width={(item.labelWidth ?? item.width) + 8} height={item.height + 24} fill="transparent" />
                <Equipment item={pictured} operating={operating} scenery={appearance.scenery} />
                {item.entityId === undefined ? null : <text x={item.x} y={item.y - 6} fill="#d7ddcf" fontFamily="ui-monospace, monospace" fontSize="11">{shortLabel(item.label, Math.floor((item.labelWidth ?? item.width) / 6.6))}</text>}
                {[...new Map(edits.map((edit) => [`${edit.proposal.id}:${edit.operation.kind}`, edit])).values()].slice(0, 4).map(({ proposal, operation }, index) => <ProposalMark key={`${proposal.id}:${operation.path}`} proposalId={proposal.id} item={item} kind={operation.kind} index={index} stale={proposal.state !== "active"} />)}
              </g>;
            })}
            <g {...sceneAction(() => selectEntity(room.id))} className="dfFactoryScene__target" data-tooltip={roomInfo(node)} aria-label={`Inspect ${node.label}`}>
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
            {(node.assemblies?.length ?? 0) <= 12 || node.proposed && proposals?.selected ? null : <text x={room.x + 136} y={room.door.y - 7} fill="#b8cabe" fontSize="10">+{node.assemblies!.length - (node.proposed ? 12 : 11)} {node.proposed ? "versions · select a Change" : "assemblies · search to inspect"}</text>}
            {node.proposed ? <rect x={room.x + 3} y={room.y + 3} width={room.width - 6} height={room.height - 6} fill="none" stroke="#84bfd3" strokeDasharray="6 4" pointerEvents="none" /> : null}
            {roomOperations.length === 0 ? null : <g {...sceneAction(() => proposals?.onSelect(roomOperations[0]!.proposal.id))} aria-label={`Inspect changes in ${node.label}`}><rect x={room.x + 8} y={room.door.y - 17} width="120" height="16" fill="#384643" /><text x={room.x + 12} y={room.door.y - 6} fill="#eed59c" fontSize="10">{roomOperations.length} proposed edits</text></g>}

          </g>
        );
      })}



      {appearance.scenery === "off" ? null : <g aria-hidden="true" fill="none" strokeLinecap="round" pointerEvents="none">
        <g opacity={appearance.scenery === "subtle" ? .3 : .5}>{cabling.trunks.map((trunk, index) => <path key={index} data-wire-trunk={trunk.count} d={trunk.d} stroke={trunk.wall ? "#9aa69c" : "#728078"} strokeWidth={Math.min(trunk.wall ? 4 : 8, 1.5 * Math.sqrt(trunk.count))} />)}</g>
        {cabling.routes.filter((wire) => linkedFrom === wire.from || linkedFrom === wire.to).map((wire) =>
          <path key={`${wire.from} ${wire.to}`} data-wire={`${wire.from} ${wire.to}`} d={wire.d} stroke="#e5c58b" strokeWidth="1.5" opacity=".9" />)}
      </g>}
      {proposedCables.map(({ d, edge, proposal }, index) => <g key={`${proposal.id}:${index}`} data-proposed-relationship={edge.status} data-proposal-id={proposal.id} {...sceneAction(() => proposals?.onSelect(proposal.id))} aria-label={`${proposal.title}: ${edge.status} dependency ${edge.fromPath} to ${edge.toPath}`}>
        <title>{`${proposal.title}: ${edge.status} ${edge.fromPath} → ${edge.toPath} · ${edge.weight} static links`}</title>
        <path d={d} fill="none" stroke="transparent" strokeWidth="12" />
        <path d={d} fill="none" stroke={edge.status === "added" ? "#a4d6e8" : "#e7a893"} strokeWidth="2" strokeDasharray={edge.status === "added" ? "8 4" : "2 5"} pointerEvents="none" />
        <text x={(layout.rooms.find((room) => room.id === edge.fromId)?.door.x ?? 0) + 8} y={(layout.rooms.find((room) => room.id === edge.fromId)?.door.y ?? 0) - 6} fill="#ead8a4" fontSize="12" pointerEvents="none">{edge.status === "added" ? "+ link" : "× link"}</text>
      </g>)}
      <g data-tooltip="Library · discussions" className={onOpenBoard ? "dfFactoryScene__target" : undefined} aria-label="Open project board" {...sceneAction(onOpenBoard === undefined ? undefined : () => onOpenBoard(projectId))} transform={`translate(${PADDING + 24} 90)`}><rect className="dfFactoryScene__focus" x="-8" y="-8" width="48" height="48" fill="transparent" /><g aria-hidden="true"><Frame name="prop.board" x={0} y={0} /><text x="0" y="30" fill="#d4ddd2" fontSize="10">DISCUSSIONS</text></g></g>
      {/* Somewhere to go other than the table: against the back wall, muted like the rest of the furniture. */}
      {nook?.furniture.filter((piece) => (!piece.roomId || nodes.has(piece.roomId)) && (appearance.scenery !== "off" || (piece.errand === "shelf" && onOpenLibrary))).map((piece) => <g key={piece.key} data-break-room={piece.errand} opacity=".8" transform={`translate(${piece.x} ${piece.y}) scale(${WORKER_SIZE / FRAME})`}>
        {piece.errand !== "shelf" || onOpenLibrary === undefined ? null : <g className="dfFactoryScene__target" data-tooltip="Library · documents" {...sceneAction(() => onOpenLibrary(piece.roomId ? nodes.get(piece.roomId)?.project?.id : projectId))} aria-label="Open project library"><rect className="dfFactoryScene__focus" x="-9" y="-9" width="35" height="35" fill="transparent" /></g>}
        <g aria-hidden="true" pointerEvents="none"><Frame name={piece.errand === "shelf" ? "prop.bookshelf" : "prop.coffeestation"} x={0} y={0} />
        {piece.errand === "shelf" ? <text x="8" y="23" textAnchor="middle" fill="#d4ddd2" fontSize="4">LIBRARY</text> : null}
        {piece.errand !== "coffee" ? null : <path d="M2 15v3 M14 15v3" stroke="#303b3b" strokeWidth="2" />}</g>
      </g>)}
      {[
        { label: "Break room", seats: seating.resting, planning: false, occupied: commonResting.length },
        { label: "Work tables", seats: seating.planning, planning: true, occupied: planning.length },
      ].filter(({ seats }) => seats.length > 0).map(({ label, seats, planning, occupied }) => {
        const top = seats[0]!.y;
        return <g key={label} role="group" aria-label={label}>
          <text x={seats[0]!.x - 18} y={top - 27} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">{label}</text>
          {/* A stool only where someone sits; the tables stand in front of them, drawn after the workers. */}
          {seats.slice(0, occupied).map((seat, index) => <g key={index} aria-hidden="true" data-common-seat={planning ? "planning" : "resting"} transform={`translate(${seat.x} ${seat.y})`}>
            <rect x="-7" y="3" width="14" height="6" rx="2" fill="#655948" stroke="#897c61" />
            <path d="M-5 9v3 M5 9v3" stroke="#3f4540" strokeWidth="3" />
          </g>)}
        </g>;
      })}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="24" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      {placements.filter((placement) => placement.area === "resting" && placement.roomId !== undefined).map((seat) => <g key={seat.id} data-nearby-rest={seat.roomId} aria-hidden="true"><rect x={seat.x - 12} y={seat.y + 5} width="24" height="7" fill="#655948" stroke="#9b8b6b" /><path d={`M${seat.x - 8} ${seat.y + 12}v5m16-5v5`} stroke="#74664e" strokeWidth="3" /></g>)}
      <SceneWorkers nearby={appearance.social === "nearby"} errands={appearance.scenery !== "off"} restingSeats={[...resting.filter((seat) => seat.roomId !== undefined), ...seating.resting]} tray={tray} peerQuestions={peerQuestions} furniture={tables} layout={layout} placements={placements} nodes={nodes} workers={workers} tasks={tasks} connected={connected} animate={appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectTask={onSelectTask} onSelectHumanRequest={onSelectHumanRequest} onSelectProposal={proposals?.onSelect} />
      <g data-common-table="planning" data-tooltip="Missions · inspect objectives" aria-label="Open Missions" className={onOpenMissions === undefined ? undefined : "dfFactoryScene__target"} {...sceneAction(onOpenMissions === undefined ? undefined : () => onOpenMissions(projectId))} transform={`translate(${station.missions.x} ${station.missions.y})`}>
        {onOpenMissions === undefined ? null : <rect className="dfFactoryScene__focus" x="-22" y="-22" width="44" height="44" fill="transparent" />}
        <rect x="-22" y="-8" width="44" height="16" fill="#455c5e" stroke="#8c8871" />
        <path d="M-17 8v7 M17 8v7" stroke="#393f3c" strokeWidth="3" />
        <rect x="-21" y="-7" width="42" height="14" fill="#9fae9e" /><path d="M-18 -5h24v4H-6v-4 M9 -4h6" fill="none" stroke="#536e70" />
        <text x="0" y="-12" textAnchor="middle" fill="#d4ddd2" fontFamily="ui-monospace, monospace" fontSize="10">MISSIONS</text>
      </g>
      <g aria-hidden="true" pointerEvents="none" data-floor-tray-desk="" transform={`translate(${tray.x} ${tray.y})`}>
        <rect x="-22" y="-8" width="44" height="16" fill="#5b5545" stroke="#a08f68" />
        <path d="M-17 8v7 M17 8v7" stroke="#393b35" strokeWidth="3" />
        <text x="0" y="-12" textAnchor="middle" fill="#d9c58d" fontFamily="ui-monospace, monospace" fontSize="10">TASKS</text>
      </g>
      {/* Waiting work, as the tray it would be on a real desk. The pile says
          how the queue is doing; the target opens the existing Tasks panel. */}
      <g data-floor-inbox={queued} data-tooltip={queued === 0 ? "Tasks · queue is empty" : `Tasks · ${queued} queued`} aria-label="Open Tasks" className={onOpenTasks === undefined ? undefined : "dfFactoryScene__target"} {...sceneAction(onOpenTasks === undefined ? undefined : () => onOpenTasks(projectId))} transform={`translate(${tray.x} ${tray.y})`}>
        {onOpenTasks === undefined ? null : <rect className="dfFactoryScene__focus" x="-22" y="-22" width="44" height="44" fill="transparent" />}
        {[...Array(Math.min(queued, 3))].map((_, index) => <rect key={index} x="-10" y={-2 - index * 3} width="18" height="3" fill="#e4dcc0" stroke={tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? "#80ddff" : "#a6a087"} />)}
        <path d="M-12 -4v6h24v-6 M-12 2h24" fill="none" stroke="#c2b184" strokeWidth="2" />
      </g>


    </svg>
    {tooltip === undefined ? null : <div ref={tooltipElement} className="dfFactoryTooltip" role="tooltip" style={{ left: tooltip.x, top: tooltip.y, maxHeight: tooltip.room }}>{tooltip.text}</div>}
    </div>
    {selectedRoom === undefined ? null : <section className="dfRoomDetails" aria-label="Source inspector" key={selectedRoom.id}>
      <h3>{selectedRoom.label}</h3>
      {selectedRoom.path === selectedRoom.label ? null : <p>{selectedRoom.path}</p>}
      <button type="button" onClick={() => focusEntity(selectedRoom.id)}>Focus on floor</button>
      {onDiscussSource ? <button type="button" onClick={onDiscussSource}>Discuss this source</button> : null}
      <button type="button" onClick={() => selectEntity(undefined)}>Close source</button>
      {selectedRoom.sourceIncomplete ? <p>Proposed contents and size are incomplete.</p> : null}
      {proposals === undefined ? null : <div aria-label="Changes affecting selected source">{proposalsForEntity(topology, proposals.items, selectedRoom.id).map((proposal) => <button type="button" key={proposal.id} onClick={() => proposals.onSelect(proposal.id)}>{proposal.title}</button>)}</div>}
      <details><summary>Source details</summary>
        <p>Source reference <code>{selectedRoom.id}</code>{selectedRoom.language ? ` · ${selectedRoom.language}` : ""}</p>
        {contents.length === 0 ? null : <ul aria-label="Contained source areas">{contents.map((component) => <li key={component.id}><button type="button" disabled={!availableNodes.has(component.id)} onClick={() => selectEntity(component.id)}>{component.label}</button></li>)}</ul>}
        {selectedRoom.sourcePaths?.length ? <ul>{selectedRoom.sourcePaths.map((path) => <li key={path}><code>{path}</code></li>)}</ul> : null}
        {selectedRoom.inventory === undefined ? <p>Inventory unavailable.</p> : <table><caption>{selectedRoom.sourceIncomplete ? "Observed proposed files" : "Scanned files"}</caption><thead><tr><th>Kind</th><th>Direct</th><th>Including children</th></tr></thead><tbody>{Object.entries(inventoryLabels).filter(([kind]) => selectedRoom.inventory!.direct[kind as keyof typeof inventoryLabels] > 0 || selectedRoom.inventory!.total[kind as keyof typeof inventoryLabels] > 0).map(([kind, label]) => <tr key={kind}><th>{label}</th><td>{selectedRoom.inventory!.direct[kind as keyof typeof inventoryLabels]}</td><td>{selectedRoom.inventory!.total[kind as keyof typeof inventoryLabels]}</td></tr>)}</tbody></table>}
        {sourceDetails}
        {selectedRoom.dependencies === undefined ? <p>Dependencies unavailable.</p> : <>
          {links.length === 0 ? null : <ul aria-label="Static dependencies">{links.map((link) => <li key={`${link.direction}:${link.nodeId}`}>
            {link.direction === "to" ? "Depends on " : "Used by "}
            <button type="button" disabled={!availableNodes.has(link.nodeId)} onClick={() => selectEntity(link.nodeId)}>{link.path || link.label}</button>
          </li>)}</ul>}
          {selectedRoom.dependencies.omitted === 0 ? null : <p>{selectedRoom.dependencies.omitted} relationships omitted from this topology.</p>}
        </>}
      </details>
    </section>}
    </>
  );
}

function sceneAction(select: (() => void) | undefined) {
  return select === undefined ? {} : { role: "button", tabIndex: 0, onClick: select, style: { cursor: "pointer" }, onKeyDown: (event: KeyboardEvent<SVGGElement>) => {
    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); }
  } };
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
  const routes = [...pairs].sort().map((pair) => {
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
  if (item.entityId !== undefined) {
    const scale = item.scale ?? 0, rich = item.width >= 240 && item.height >= 120;
    const color = operating ? "#b0d0c0" : "#91aca1";
    const resources = Object.entries(item.resourceCounts ?? {}).filter(([, count]) => count > 0);
    const associated = resources.filter(([kind]) => kind !== item.kind).slice(0, 3);
    const mainWidth = rich && associated.length ? item.width - 90 : item.width;
    const partColumns = Math.min(2, item.parts?.length ?? 0), partWidth = mainWidth / Math.max(1, partColumns);
    const machineWidth = Math.min(mainWidth - 8, [48, 80, 128, 180][scale]!);
    const machineHeight = Math.min(item.height - 30, [30, 46, 64, 80][scale]!);
    const total = resources.reduce((sum, [, count]) => sum + count, 0);
    const associatedStep = Math.min(40, (item.height - 24) / Math.max(1, associated.length));
    return <g transform={`translate(${item.x} ${item.y})`} data-equipment-scale={item.resourceCounts === undefined || item.sourceIncomplete ? "unknown" : scale} data-responsibility={item.responsibility}>
      {(item.parts?.length ?? 0) < 2 ? <ResourceMachine kind={item.kind} width={machineWidth} height={machineHeight} color={color} motif={item.responsibility} /> : <g data-assembly-parts="filename motifs">
        {item.parts!.map((part, index) => {
          const width = partWidth - 12, height = rich ? Math.min(44, (item.height - 24) / 2 - 20) : Math.min(34, item.height - 40), y = Math.floor(index / partColumns) * (item.height - 24) / 2;
          return <g key={part.label} transform={`translate(${index % partColumns * partWidth} ${y})`}><ResourceMachine kind="source" width={width} height={height} color={color} motif={part.motif} /><text y={height + 16} fill="#ded3af" fontFamily="ui-monospace, monospace" fontSize="10">{shortLabel(part.label, Math.floor(width / 6))}</text></g>;
        })}
      </g>}
      {associated.map(([kind, count], index) => <g key={kind} data-associated-equipment={kind} transform={`translate(${rich ? item.width - 76 : index * Math.min(24, item.width / Math.max(1, associated.length))} ${rich ? index * associatedStep : item.height - 28})`}>
        {rich ? <ResourceMachine kind={kind} width={62} height={associatedStep - 18} color="#aec2ab" /> : <ResourceGlyph kind={kind} />}
        {!rich ? null : <text x="0" y={associatedStep - 4} fill="#c9d2bc" fontSize="10">{inventoryLabels[kind as keyof typeof inventoryLabels]} {count}</text>}
      </g>)}
      {rich ? <g transform={`translate(0 ${item.height - Math.ceil(resources.length / 3) * 13})`}>{resources.map(([kind, count], index) => <g key={kind} data-resource-kind={kind} transform={`translate(${index % 3 * item.width / 3} ${Math.floor(index / 3) * 13})`}><ResourceGlyph kind={kind} /><text x="11" y="8" fill="#c3d1c6" fontFamily="ui-monospace, monospace" fontSize="10">{count} {inventoryLabels[kind as keyof typeof inventoryLabels].toLowerCase()}</text></g>)}</g>
        : <text y={item.height} fill="#c3d1c6" fontFamily="ui-monospace, monospace" fontSize="10">{item.resourceCounts === undefined ? "Unknown" : `${total} ${item.sourceIncomplete ? "observed" : "files"}`}</text>}

    </g>;
  }
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

function ResourceGlyph({ kind }: { kind: string }) {
  const path = kind === "source" ? "M0 2h6v6H0z M2 0v2m2-2v2m-2 6v2m2-2v2" : kind === "tests" ? "M0 0v6h6V0 M0 3h2l1-2 1 4 1-2h1 M1 8h4" : kind === "documentation" ? "M0 0h6v9H0z M2 1v7m2-6v5" : kind === "configuration" ? "M0 0h6v9H0z M1 2h4m-4 3h4m-3-4v2m1 1v2" : "M0 2h6v7H0z M0 5h6m-3-3v7";
  return <path d={path} fill="none" stroke="#c7c6a6" strokeWidth=".8" />;
}

function ResourceMachine({ kind, width: w, height: h, color, motif }: { kind: string; width: number; height: number; color: string; motif?: RoomContent["responsibility"] }) {
  if (kind === "documentation") return <g><path d={`M0 0h${w}v${h}H0z M0 ${h / 2}h${w}`} fill="#685f48" stroke={color} strokeWidth="2" />{Array.from({ length: Math.max(3, Math.floor(w / 9)) }, (_, i) => <path key={i} d={`M${5 + i * 8} 3v${h / 2 - 6}m2 7v${h / 2 - 6}`} stroke={i % 2 ? "#b0baa0" : "#9f846a"} strokeWidth="5" />)}</g>;
  if (kind === "configuration") return <g><rect width={w - 8} height={h + 3} fill="#465e61" stroke={color} strokeWidth="2" /><path d={`M${w / 2} 1v${h} M5 7h${w / 2 - 10}m0 7H5 M${w / 2 + 5} 7h${w / 2 - 18}m0 7H${w / 2 + 5}`} stroke="#ccd2b5" strokeWidth="2" /><circle cx={w - 18} cy={h - 7} r="3" fill="#c5aa6c" /></g>;
  if (kind === "tests") return <g><path d={`M0 ${h - 3}h${w}v6H0z M5 ${h + 3}v5m${w - 10}-5v5`} fill="#657263" stroke={color} /><rect x="5" y="0" width={w - 10} height={h - 4} rx="2" fill="#253e43" stroke={color} strokeWidth="2" /><path d={`M9 ${h / 2}h6l5-7 6 13 7-7h${Math.max(1, w - 43)}`} fill="none" stroke="#c9d49b" strokeWidth="2" /></g>;
  if (kind === "assets" || kind === "unclassified") return <g><path d={`M0 0v${h + 5}M${w} 0v${h + 5}M0 ${h}h${w}M0 ${h / 2}h${w}`} stroke={color} strokeWidth="3" />{[0, 1, 2].map((i) => <rect key={i} x={4 + i * (w - 6) / 3} y="3" width={(w - 15) / 3} height={h - 7} fill={i % 2 ? "#8c7b58" : "#677b72"} stroke="#afb298" />)}</g>;
  return <g>
    <path d={`M0 ${h - 3}h${w}v7H0z M5 ${h + 4}v5m${w - 10}-5v5`} fill="#667764" stroke={color} />
    {motif === "movement" ? <g><rect x="2" y="9" width={w - 4} height={h - 9} rx="6" fill="#3e5556" stroke={color} />{Array.from({ length: 3 + Math.floor(w / 25) }, (_, i) => <circle key={i} cx={8 + i * (w - 16) / (2 + Math.floor(w / 25))} cy={h - 7} r="4" fill="#223339" stroke="#b4c0aa" />)}<path d={`M8 4h${w - 21}l-4-3m4 3-4 3`} fill="none" stroke="#d4c394" strokeWidth="2" /></g>
      : motif === "messaging" ? <g><path d={`M8 5h${w - 16}v${h - 8}H8z`} fill="#40565a" stroke={color} /><path d={`M8 5l${(w - 16) / 2} 10L${w - 8} 5 M${w / 2} 4V-3m-5 0h10`} fill="none" stroke="#cfbc91" strokeWidth="2" /><circle cx={w / 2} cy="-5" r="2" fill="#cbd9b8" /></g>
      : motif === "admission" ? <g><path d={`M4 ${h}V2h${w - 8}v${h - 2}M${w / 2} 5v${h - 5}`} fill="#3e5052" stroke={color} strokeWidth="3" /><path d={`M10 ${h / 2}h${w - 20}l-5-4m5 4-5 4`} stroke="#d6bc82" fill="none" strokeWidth="2" /></g>
      : motif === "selection" ? <g><rect x="2" width={w - 4} height={h - 2} fill="#425451" stroke={color} /><path d={`M9 4v${h - 10}m-4 0h15m5-15v${h - 10}m-4 0h15`} stroke="#c2c99e" strokeWidth="2" /><circle cx={w - 11} cy="10" r="5" fill="none" stroke="#d9c18f" strokeWidth="2" /><path d={`M${w - 7} 14l5 5`} stroke="#d9c18f" strokeWidth="2" /></g>
      : motif === "interface" ? <g><rect x="2" width={w - 4} height={h - 6} fill="#334b53" stroke={color} strokeWidth="2" /><path d={`M6 4h${w - 12}v${h - 15}H6z M${w / 2} ${h - 6}v5m-8 0h16`} fill="none" stroke="#c4caae" strokeWidth="2" /></g>
      : motif === "storage" ? <g>{[0, 1, 2].map((i) => <g key={i} transform={`translate(${2 + i * (w - 4) / 3} 0)`}><path d={`M0 4v${h - 9}q${(w - 10) / 6} 6 ${(w - 10) / 3} 0V4`} fill="#537073" stroke={color} /><ellipse cx={(w - 10) / 6} cy="4" rx={(w - 10) / 6} ry="4" fill="#779186" stroke={color} /></g>)}</g>
      : <g><path d={`M3 5h${w - 6}v${h - 8}H3z M10 0h${w - 20}v5H10z`} fill="#455f5d" stroke={color} strokeWidth="2" /><circle cx={w / 2} cy={h / 2} r={6 + w / 18} fill="#263b40" stroke="#c4c7a7" strokeWidth="3" /><path d={`M${w / 2} ${h / 2 - 4}v8m-4-4h8`} stroke="#d5bf8d" strokeWidth="2" /></g>}
  </g>;
}

function ProposalMark({ proposalId, item, kind, index, stale }: { proposalId: string; item: RoomContent; kind: SceneProposal["operations"][number]["kind"]; index: number; stale: boolean }) {
  const color = stale ? "#b1a9a0" : kind === "removal" ? "#dfa48e" : "#a8d8ea";
  return <g data-proposal-id={proposalId} data-proposal-kind={kind} pointerEvents="none" opacity={stale ? .65 : 1}>
    {index !== 0 ? null : <><rect x={item.x - 2} y={item.y - 3} width={item.width + 4} height={item.height + 8} fill={kind === "addition" ? "#396d8950" : "none"} stroke={color} strokeWidth="2" strokeDasharray={kind === "addition" ? "5 3" : kind === "removal" ? "2 3" : undefined} />
      {kind === "removal" ? <path d={`M${item.x + 5} ${item.y}l${item.width - 10} ${item.height}m-${item.width - 10} 0l${item.width - 10} -${item.height}`} stroke={color} strokeWidth="2" /> : kind === "move" ? <path d={`M${item.x + 8} ${item.y + 12}h${item.width - 16}l-8-6m8 6-8 6`} fill="none" stroke={color} strokeWidth="3" /> : kind === "modification" ? <path d={`M${item.x + item.width - 20} ${item.y + 2}l12 12m-16-13 4-4 5 1-1 5-4 4m11 7 3 3`} fill="none" stroke="#e7c27f" strokeWidth="3" /> : null}</>}
    <rect x={item.x - 15} y={item.y + index * 15} width="12" height="12" fill="#203a46" stroke={color} />
    <text x={item.x - 9} y={item.y + index * 15 + 9} fill={color} textAnchor="middle" fontSize="8">{kind === "addition" ? "+" : kind === "removal" ? "×" : kind === "move" ? "→" : "M"}</text>
  </g>;
}

function roomInfo(node: SceneTopology["nodes"][number]) {
  const counts = node.inventory?.[node.inventoryScope === "direct" ? "direct" : "total"];
  // The cabling is scenery; its content is stated here for every reader and appearance.
  const links = [...new Set((node.dependencies?.links ?? []).map((link) => link.label))];
  const inventory = counts === undefined ? "Inventory unavailable" : Object.entries(inventoryLabels).filter(([kind]) => counts[kind as keyof typeof counts] > 0).map(([kind, label]) => `${counts[kind as keyof typeof counts]} ${label.toLowerCase()}`).join(" · ") || "No scanned files";
  return `${node.path === "." ? node.label : node.path}\n${node.language ? `${node.language} · ` : ""}${node.kind} · ${node.inventoryScope === "direct" ? "direct files" : "subtree"}\n${inventory}${links.length === 0 ? "" : `\nWired to ${links.slice(0, 6).join(", ")}${links.length > 6 ? ` +${links.length - 6}` : ""}`}`;
}
