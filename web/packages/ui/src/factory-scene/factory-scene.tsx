import { useLayoutEffect, useEffect, useMemo, useRef, useState, type MouseEvent, type FocusEvent, type PointerEvent, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import type { OperationalNodeView, PeerQuestionItem } from "@dark-factory/client";
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
  proposalsForEntity,
  placeCrates,
  LINE_SLOTS,
  type RoomContent,
  type SceneCrate,
  type SceneFlow,
  type SceneGraph,
  type SceneHall,
  type SceneLayout,
  type SceneMachine,
  type ScenePoint,
  type SceneProposal,
  type SceneReading,
  type SceneRoomLayout,
  type SceneWorker,
} from "./scene.js";
import { IconButton } from "../icons.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { breakRoomHabit, restingItem, workerFrames, workerPhase } from "./appearance.js";
import { catAt, catBed, chats, gossip, type Seat } from "./idle-life.js";
import { endsAt, messageAt, observe, observeCrates, send, type FloorMessage, type Seen } from "./messages.js";
import { directionBetween, pointOnRoute, routeFromCurrent, routeBetween, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
  proposals?: { items: readonly SceneProposal[]; selected?: string; onSelect: (id: string) => void };
  /** Change requests on the outbound line; selecting one uses `proposals.onSelect`. */
  crates?: readonly SceneCrate[];
  tools?: ReactNode;
  /** Read one machine's evidence when its inspector opens. */
  onLoadNode?: (nodeId: string) => Promise<OperationalNodeView>;
  /** Queue an investigation of a machine as an ordinary task. */
  onInvestigate?: (machine: SceneMachine, hall?: SceneHall) => void;
  onDiscussSource?: (nodeId: string) => void;
  requestedEntity?: { id: string };
  onSelectEntity?: (entityId: string | undefined) => void;
  onOpenLibrary?: (projectId?: string) => void;
  onOpenBoard?: (projectId?: string) => void;
  appearance?: FloorAppearance;
  graph: SceneGraph;
  /** Current project scope, when the floor has one. */
  projectId?: string;
  workers: readonly SceneWorker[];
  tasks?: readonly SceneTask[];
  /** Questions between live tasks: shown as they are asked and answered, and stated while they wait. */
  peerQuestions?: readonly PeerQuestionItem[];
  /** Board and Library operations just recorded, oldest first. Drawn where things already are; nobody moves for them. */
  knowledgeCues?: readonly KnowledgeCueView[];
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  /** Open the existing project task queue from the floor inbox. */
  onOpenTasks?: (projectId?: string) => void;
  /** Open the operator's objective view from the planning table. */
  onOpenMissions?: (projectId?: string) => void;
  onSelectHumanRequest?: (requestId: string) => void;
  /** A dropped session reconciles to its latest snapshot instead of replaying local motion. */
  connected?: boolean;
  /** The plant has not been read yet: an empty floor says so rather than claiming nothing was inferred. */
  reading?: boolean;
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
const DAY = 24 * 60 * 60_000, CHANGEOVER = 10 * 60_000;
const NO_QUESTIONS: readonly PeerQuestionItem[] = [];
export type KnowledgeCueView = Readonly<{ key: string; agentId: string; board: boolean; reading: boolean; label: string; open?: () => void }>;
const NO_CUES: readonly KnowledgeCueView[] = [];
const NO_CRATES: readonly SceneCrate[] = [];

/** A recorded operation, unlike ambient life, is labelled, focusable and opens what it used. */
function KnowledgeCueMark({ cue, x, y, at }: { cue: KnowledgeCueView; x: number; y: number; at: string }) {
  return <g className="dfKnowledgeCue dfFactoryScene__target" data-knowledge-cue={at} data-knowledge-key={cue.key} data-tooltip={`Recorded · ${cue.label}`} aria-label={`Recorded: ${cue.label}`} {...sceneAction(cue.open)} transform={`translate(${x} ${y})`}>
    <rect className="dfFactoryScene__focus" x="-9" y="-9" width="18" height="18" rx="3" fill="#f3ecd6" stroke={cue.board ? "#80ddff" : "#e5c58b"} strokeWidth="2" />
    <path aria-hidden="true" d={cue.reading ? "M-5 -4h4.5v8H-5Z M0.5 -4H5v8H0.5Z" : "M-5 -3h10v7h-10Z M-5 -3l5 4l5 -4"} fill="none" stroke="#172330" strokeWidth="1.3" />
  </g>;
}
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
function useSceneMotion(layout: ReturnType<typeof layoutScene>, seated: ReturnType<typeof placeWorkers>, floorDigest: string, connected: boolean, reduced: boolean, active: ReadonlySet<string>, workers: FactorySceneProps["workers"], errands: boolean, nearby: boolean, restless: (at: number) => boolean) {
  const motions = useRef(new Map<string, MotionState>());
  const priorFloor = useRef<string | undefined>(undefined);
  const priorConnected = useRef<boolean | undefined>(undefined);
  const motionsFloor = useRef(floorDigest);
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
  const errandClock = moving && errands && motionsFloor.current === floorDigest ? Math.floor(movingTime.current.total / 2000) * 2000 : undefined;
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
    const floorChanged = priorFloor.current !== undefined && priorFloor.current !== floorDigest;
    const reconnected = priorConnected.current === false && connected;
    priorFloor.current = floorDigest;
    priorConnected.current = connected;
    const previous = motions.current;
    const next = new Map<string, MotionState>();
    for (const placement of placements) {
      const old = previous.get(placement.id);
      if (old === undefined || reduced || hidden || floorChanged || reconnected || !connected) {
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
    if (motionsFloor.current !== floorDigest) movingTime.current = { total: 0, last: undefined };
    motionsFloor.current = floorDigest;
    setClock(at);
  }, [connected, layout, placements, reduced, floorDigest]);

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
    const state = moving && motionsFloor.current === floorDigest ? motions.current.get(placement.id) : undefined;
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
function SceneWorkers({ knowledgeCues, nearby, errands, furniture, restingSeats, tray, peerQuestions, layout, placements: seated, labels, workers, tasks, connected, animate, selectedWorkerId, onSelectWorker, onSelectTask, onSelectHumanRequest, onSelectProposal }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectTask" | "onSelectHumanRequest"> & {
  onSelectProposal?: (id: string) => void;
  layout: ReturnType<typeof layoutScene>;
  placements: ReturnType<typeof placeWorkers>;
  labels: ReadonlyMap<string, string>;
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
  knowledgeCues: readonly KnowledgeCueView[];
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
        const room = placement.roomId === undefined ? undefined : { label: labels.get(placement.roomId) ?? "" };
        const location = worker.location === "working"
          ? `at the machine its observed changes touch${worker.locationLabel === undefined ? "" : ` in ${worker.locationLabel}`}; ${placement.area === "room" ? "representative position, not exact file inspection" : placement.area === "outside" ? "outside displayed halls" : "worker area at capacity"}`
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
            <g role="img" className="dfFactoryScene__target" data-tooltip={`${worker.name} · ${worker.activity}${worker.review ? `\nReview assignment: ${worker.review.scope}; representative visit, not exact file inspection` : ""}\n${placement.area === "room" ? `Working at ${worker.locationLabel ?? room?.label ?? "observed changes"}` : placement.errand === "shelf" ? "Taking a break · at the bookshelf" : placement.errand === "coffee" ? "Taking a break · at the coffee station" : placement.area === "resting" ? `${worker.paused ? "Paused · taking a break" : "Taking a break"}${stroking === undefined ? "" : " · fussing the cat"}${said}` : worker.location === "unobserved" ? "Planning · location not yet observed" : "Planning · work outside the halls shown"}${[...new Set(asking)].join("")}`} aria-label={`${worker.name}, ${worker.review ? "reviewer" : worker.role}, ${worker.activity}, ${worker.review?.scope ?? location}`} {...sceneAction(worker.review && onSelectProposal ? () => onSelectProposal(worker.review!.proposalId) : onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
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
      {/* What an agent just recorded, beside wherever it already is, newest per agent. */}
      {[...new Map(knowledgeCues.filter((cue) => positions.has(cue.agentId)).map((cue) => [cue.agentId, cue])).values()].map((cue) => { const position = positions.get(cue.agentId)!; return <KnowledgeCueMark key={cue.key} cue={cue} x={position.x - 20} y={position.y - 32} at="agent" />; })}
      {/* Said and felt, over everyone's heads: never words on the floor, those are in the tooltip. */}
      <g aria-hidden="true" pointerEvents="none">
        {[...calling].map(([id, glyph]) => { const position = positions.get(id); return position === undefined ? null : <g key={`call ${id}`} data-bubble={glyph} data-call={id} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${glyph}`} x={1} y={-21} /></g>; })}
        {flights.map(({ message, point }) => point === undefined || message.kind !== "assign" ? null : <g key={message.key} data-paper={message.to} transform={`translate(${point.x} ${point.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`paper.${Math.floor(at! / 120) % 2}`} x={-8} y={-8} /></g>)}
        {talk.map(({ speaking }) => { const position = speaking && positions.get(speaking.id); return !position ? null : <g key={speaking.id} data-bubble={speaking.glyph} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${speaking.glyph}`} x={1} y={-21} /></g>; })}
        {cat?.pettedBy === undefined || cat.pettedFor! < 600 ? null : <g data-heart="" opacity={cat.pettedFor! > 2800 ? .5 : 1} transform={`translate(${positions.get(cat.pettedBy)!.x} ${positions.get(cat.pettedBy)!.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name="heart" x={-4} y={-24 - Math.floor((cat.pettedFor! - 600) / 450)} /></g>}
      </g></>;
}

/** A disposable SVG projection of the operational world and current factory state. */
export function FactoryScene({ proposals, crates = NO_CRATES, tools, onLoadNode, onInvestigate, onDiscussSource, requestedEntity, onSelectEntity, onOpenLibrary, onOpenBoard, graph, workers, appearance = DEFAULT_FLOOR_APPEARANCE, selectedWorkerId, onSelectWorker, tasks = [], peerQuestions = NO_QUESTIONS, knowledgeCues = NO_CUES, selectedTaskId, onSelectTask, onOpenTasks, onOpenMissions, onSelectHumanRequest, projectId, connected = true, reading = false }: FactorySceneProps) {
  const [selectedId, setSelectedId] = useState<string>();
  // The crate pointed at or focused: the machines its change touches are lit.
  const [lit, setLit] = useState<string>();
  const litIds = new Set(proposals?.items.find((proposal) => proposal.id === lit)?.operations.map((operation) => operation.entityId));
  const [search, setSearch] = useState("");
  const mapElement = useRef<HTMLDivElement>(null);
  const floorElement = useRef<SVGSVGElement>(null);
  // Scene pixels per scene unit; undefined fits the floor to its pane.
  const [zoom, setZoom] = useState<number>();
  // The overview's window follows scrolling without rerendering the floor.
  const viewElement = useRef<SVGRectElement>(null);
  const anchor = useRef<{ x: number; y: number; px: number; py: number }>(undefined);
  const selectEntity = (id: string | undefined) => { setSelectedId(id); onSelectEntity?.(id); };
  const [tooltip, setTooltip] = useState<{ text: string; x: number; y: number; top: number; bottom: number; room: number }>();
  const tooltipElement = useRef<HTMLDivElement>(null);
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
  const inspect = (event: PointerEvent<HTMLDivElement> | FocusEvent<HTMLDivElement> | MouseEvent<HTMLDivElement>) => showTooltip((event.target as Element).closest("[data-tooltip]"));
  const layout = useMemo(() => layoutScene(graph), [graph.digest]);
  const placements = useMemo(() => placeWorkers(layout, workers, appearance.social), [layout, workers, appearance.social]);
  // Live readings change without moving anything: they are looked up by id at draw time.
  const machines = new Map<string, SceneMachine>([...graph.halls.flatMap((hall) => [[hall.id, { id: hall.id, kind: "processor", label: hall.label, reading: hall.reading }] as const, ...hall.machines.map((machine) => [machine.id, machine] as const)]),
    ...[...graph.shared, ...graph.parties, ...graph.quarantine].map((machine) => [machine.id, machine] as const)]);
  const hallOf = new Map(graph.halls.flatMap((hall) => [[hall.id, hall] as const, ...hall.machines.map((machine) => [machine.id, hall] as const)]));
  const labels = new Map<string, string>([...graph.halls.map((hall) => [hall.id, hall.label] as const), ["yard", "Shared yard"], ["quarantine", "Quarantine"]]);
  const visibleProposals = proposals?.items.filter((proposal) => !proposals.selected || proposal.id === proposals.selected) ?? [];
  const contentById = new Map(layout.rooms.flatMap((room) => room.contents.map((item) => [item.entityId, { item, room }] as const)));
  const focusEntity = (id: string) => { selectEntity(id); const found = contentById.get(id); if (found) Array.from(mapElement.current?.querySelectorAll("[data-room-id]") ?? []).find((element) => element.getAttribute("data-room-id") === found.room.id)?.scrollIntoView({ block: "center", inline: "center", behavior: "instant" }); };
  useEffect(() => { if (requestedEntity) { setSearch(""); focusEntity(requestedEntity.id); } }, [requestedEntity]);
  const operationsFor = (id: string) => visibleProposals.flatMap((proposal) => proposal.operations.filter((operation) => operation.entityId === id).map((operation) => ({ proposal, operation })));
  const resting = placements.filter((placement) => placement.area === "resting");
  const planning = placements.filter((placement) => placement.area !== "room" && placement.area !== "resting");
  const commonResting = resting.filter((placement) => placement.roomId === undefined);
  const seating = commonSeating(layout, commonResting.length, planning.length);
  const seats = [...seating.resting, ...seating.planning];
  const commonBottom = Math.max(layout.restingTop, ...seats.map(({ y }) => y)) + 32;
  const nook = breakRoomNook(layout, commonResting.length, planning.length, appearance.social === "nearby");
  const station = { missions: { x: PADDING + 40, y: 48 }, tasks: { x: PADDING + 120, y: 48 } };
  const sceneWidth = layout.width;
  const sceneHeight = Math.max(layout.height, commonBottom, 112, ...placements.map((placement) => placement.y + 24)) + 2 * PADDING;
  const busy = new Map(layout.rooms.map((room) => [room.id, tasks.filter((order) => order.status === "running" && order.roomIds.includes(room.id))]));
  const belts = useMemo(() => [...hallIntake(layout, graph.halls), ...routeFlows(layout, graph.flows)], [layout, graph.halls, graph.flows]);
  const queued = tasks.filter((order) => order.status === "queued").length;
  const tray = station.tasks;
  const animate = connected && appearance.animation !== "off";
  // Changeovers are dated against the graph's own observation time, never the viewer's clock.
  const observedAt = graph.observedAt ?? 0;
  const tables = [{ seats: seating.resting, planning: false }, { seats: seating.planning, planning: true }].flatMap(({ seats, planning }) =>
    [...new Set(seats.map((seat) => seat.y))].map((y) => { const row = seats.filter((seat) => seat.y === y), first = row[0]!;
      const tableWidth = row.at(-1)!.x - first.x + 36;
      return <g key={`${planning} ${y}`} data-common-table={planning ? "planning-workers" : "resting"} aria-hidden="true" pointerEvents="none" transform={`translate(${first.x} ${y + TABLE_DROP})`}>
        <rect x="-18" y="-21" width={tableWidth} height="10" fill={planning ? "#455c5e" : "#655d4c"} stroke="#8c8871" />
        {!planning ? null : <g><rect x="-17" y="-20" width={tableWidth - 2} height="8" fill="#9fae9e" /><path d={`M-14 -18h${Math.max(12, tableWidth - 18)}v3h-7v-3 M${tableWidth - 22} -17h4`} fill="none" stroke="#536e70" /></g>}
      </g>; }));
  const selected = selectedId === undefined ? undefined : machines.get(selectedId);
  // Where the pane and the floor are, in client pixels, and how many pixels a scene unit is.
  const frame = () => {
    const map = mapElement.current, floor = floorElement.current;
    if (typeof map?.getBoundingClientRect !== "function" || typeof floor?.getBoundingClientRect !== "function") return undefined;
    const pane = map.getBoundingClientRect(), box = floor.getBoundingClientRect();
    return { map, pane, box, scale: box.width / sceneWidth };
  };
  const measure = () => {
    const at = frame(), view = viewElement.current;
    if (!at || !view) return;
    const x = Math.max(0, (at.pane.left - at.box.left) / at.scale), y = Math.max(0, (at.pane.top - at.box.top) / at.scale);
    view.setAttribute?.("x", String(x));
    view.setAttribute?.("y", String(y));
    view.setAttribute?.("width", String(Math.min(sceneWidth - x, at.map.clientWidth / at.scale)));
    view.setAttribute?.("height", String(Math.min(sceneHeight - y, at.map.clientHeight / at.scale)));
  };
  /** Zoom about a point of the pane (its centre by default), keeping that point of the floor under it. */
  const zoomBy = (factor: number, px?: number, py?: number) => {
    const at = frame();
    if (!at) return;
    const fit = Math.min(sceneWidth, Math.max(at.map.clientWidth, Math.min(sceneWidth, 864))) / sceneWidth;
    const x = px ?? at.pane.width / 2, y = py ?? at.pane.height / 2;
    const scaled = Math.min(MAX_ZOOM, at.scale * factor), next = scaled <= fit * 1.01 ? undefined : scaled;
    // A zoom that changes nothing leaves no anchor for a later change to apply.
    if (next === zoom) return;
    anchor.current = { x: (at.pane.left + x - at.box.left) / at.scale, y: (at.pane.top + y - at.box.top) / at.scale, px: x, py: y };
    setZoom(next);
  };
  /** Centre the pane on a point of the floor. */
  const centreOn = (x: number, y: number) => {
    const at = frame();
    if (!at) return;
    at.map.scrollLeft += at.box.left + x * at.scale - (at.pane.left + at.map.clientWidth / 2);
    at.map.scrollTop += at.box.top + y * at.scale - (at.pane.top + at.map.clientHeight / 2);
  };
  useLayoutEffect(() => {
    const at = frame(), point = anchor.current;
    anchor.current = undefined;
    if (at && point) {
      at.map.scrollLeft += at.box.left + point.x * at.scale - (at.pane.left + point.px);
      at.map.scrollTop += at.box.top + point.y * at.scale - (at.pane.top + point.py);
    }
    measure();
  }, [zoom, sceneWidth, sceneHeight]);
  useEffect(() => {
    const map = mapElement.current;
    if (!map || typeof window === "undefined") return;
    // Pinch on a trackpad arrives as a ctrl-wheel; it must not zoom the page.
    const wheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      const pane = map.getBoundingClientRect();
      zoomBy(Math.exp(-event.deltaY / 200), event.clientX - pane.left, event.clientY - pane.top);
    };
    map.addEventListener?.("wheel", wheel, { passive: false });
    window.addEventListener?.("resize", measure);
    return () => { map.removeEventListener?.("wheel", wheel); window.removeEventListener?.("resize", measure); };
  }, [sceneWidth, sceneHeight]);
  const searchable = [...machines.values()];

  return (
    <>
    <div className="dfFactoryEntityTools">
      {graph.summary === undefined ? null : <Coverage summary={graph.summary} />}
      <label>Find machine <input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Route, unit, store or party" /></label>
      {search === "" ? null : <div className="dfFactoryEntityTools__results">{searchable.filter((machine) => `${machine.label} ${machine.kind}`.toLowerCase().includes(search.toLowerCase())).slice(0, 30).map((machine) => <button type="button" key={machine.id} onClick={() => focusEntity(machine.id)}>{machine.label} · {machine.kind}</button>)}</div>}
      {tools}
      {proposals?.selected && proposals.items.some((item) => item.id === proposals.selected) ? <p className="dfFactoryEntityTools__notice"><span className="dfFactoryEntityTools__selection">Viewing: {proposals.items.find((item) => item.id === proposals.selected)?.title}</span><button type="button" onClick={() => proposals.onSelect("")}>Clear selection</button></p> : null}
    </div>
    <div className="dfFactoryFloor__viewport">
    <div ref={mapElement} className="dfFactoryFloor__map" onClick={inspect} onPointerOver={inspect} onFocus={inspect} onPointerLeave={retainFocusedTooltip} onBlur={() => setTooltip(undefined)} onScroll={() => { retainFocusedTooltip(); measure(); }} onKeyDown={(event) => { if (event.key === "Escape") setTooltip(undefined); }} role="region" aria-label="Scrollable factory floor" tabIndex={0}>
    <svg
      ref={floorElement}
      viewBox={`0 0 ${sceneWidth} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory operational floor"
      data-graph-digest={graph.digest}
      className={animate ? "dfPlant dfPlant--moving" : "dfPlant"}
      style={zoom === undefined ? { display: "block", width: "100%", minWidth: Math.min(sceneWidth, 864), maxWidth: sceneWidth, height: "auto", margin: "0 auto" } : { display: "block", width: Math.round(sceneWidth * zoom), height: "auto" }}
    >
      <desc>{`${graph.halls.length} units, ${graph.shared.length} shared stores, ${graph.parties.length} external parties, ${graph.quarantine.length} unexplained runtime paths, ${workers.length} workers`}</desc>
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
        <pattern id="df-blueprint" patternUnits="userSpaceOnUse" width="6" height="6"><rect width="6" height="6" fill="#4cc3e60f" /><path d="M0 6h6M6 0v6" stroke="#4cc3e633" strokeWidth="1" /></pattern>
        <pattern id="df-fog" patternUnits="userSpaceOnUse" width="6" height="6" patternTransform="rotate(45)"><rect width="6" height="6" fill="#2c3139" /><path d="M0 0v6" stroke="#4a515b" strokeWidth="2" /></pattern>
        <pattern id="df-hazard" patternUnits="userSpaceOnUse" width="12" height="12" patternTransform="rotate(45)"><rect width="12" height="12" fill="#1a1d22" /><rect width="6" height="12" fill="#e6b93a" /></pattern>
        <radialGradient id="df-lamplight" cx="50%" cy="45%" r="60%">
          <stop offset="0" stopColor="#f2dab0" stopOpacity=".24" />
          <stop offset="1" stopColor="#f2dab0" stopOpacity="0" />
        </radialGradient>
      </defs>
      <rect width={sceneWidth} height={sceneHeight} fill="#08131d" />
      {layout.corridors.map((corridor, index) => <rect key={index} data-corridor="" {...corridor} fill="url(#df-floor)" />)}
      <rect x={PADDING} y={16} width={ROOM_LEFT - PADDING} height={commonBottom - 16} fill="url(#df-floor)" />
      {layout.headings.map((heading) => <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} className="dfPlant__heading" fontSize="9">{heading.label}</text>)}

      {layout.rooms.map((room) => {
        const hall = graph.halls.find((candidate) => candidate.id === room.id);
        const footprint = busy.get(room.id) ?? [];
        const task = footprint.find((order) => order.id === selectedTaskId) ?? footprint[0];
        const operating = connected && task !== undefined;
        return <g key={room.id} data-room-id={room.id} data-room-kind={room.kind}>
          <rect x={room.x} y={room.y} width={room.width} height={room.height} fill={room.kind === "quarantine" ? "#1b1f25" : "url(#df-floor)"} />
          {room.kind === "quarantine" ? <rect x={room.x + 2} y={room.y + 2} width={room.width - 4} height={room.height - 4} fill="none" stroke="url(#df-hazard)" strokeWidth="4" /> : <>
            <rect x={room.x} y={room.y} width={room.width} height="10" fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} M${room.x + room.width},${room.y} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#465355" strokeWidth="4" />
          </>}
          <rect x={room.x + 4} y={room.y + 10} width={room.width - 8} height={room.height - 12} fill={operating ? "url(#df-lamplight)" : "#08131d"} opacity={operating ? 1 : .18} pointerEvents="none" />
          {hall === undefined ? null : <g className="dfFactoryScene__target" {...sceneAction(() => selectEntity(hall.id))} data-tooltip={machineInfo({ id: hall.id, kind: "processor", label: hall.label, reading: hall.reading }, hall)} aria-label={`Inspect ${hall.label}`}>
            <rect className="dfFactoryScene__focus dfPlant__plate" x={room.x + 8} y={room.y + 12} width={Math.min(room.width - 44, 6 * Math.min(hall.label.length, Math.floor((room.width - 60) / 6)) + 14)} height="20" rx="2" />
            <text x={room.x + 14} y={room.y + 26} className="dfPlant__plateText" fontSize="10">{shortLabel(hall.label, Math.floor((room.width - 60) / 6))}</text>
            {hall.runtime === undefined ? null : <text x={room.x + 10} y={room.y + 42} className="dfPlant__small" fontSize="7">{hall.runtime}</text>}
            {hall.reading.deployedAt === undefined || observedAt - hall.reading.deployedAt > DAY ? null : <g data-changeover={hall.reading.deployedAt}>
              {observedAt - hall.reading.deployedAt < CHANGEOVER ? <path d={`M${room.x + 6} ${room.y + 10}h${room.width - 12}M${room.x + 10} ${room.y + 10}v${room.height - 20}M${room.x + room.width - 10} ${room.y + 10}v${room.height - 20}`} className="dfPlant__scaffold" /> : null}
              <rect x={room.x + room.width - 30} y={room.y + 14} width="22" height="14" className="dfPlant__changed" /><text x={room.x + room.width - 19} y={room.y + 24} textAnchor="middle" className="dfPlant__changedText" fontSize="10">Δ</text>
            </g>}
          </g>}
          {task === undefined ? null : <g data-work-footprint={room.id} data-workbench-task-id={task.id} data-tooltip={`${connected ? "Working" : "Disconnected · last observed work"} · ${task.title}${footprint.length > 1 ? `\n${footprint.length - 1} more tasks here` : ""}`}
            aria-label={`${connected ? "Working" : "Disconnected · last observed work"}: ${task.title}`} {...sceneAction(onSelectTask === undefined ? undefined : () => onSelectTask(task.id))}>
            <rect x={room.x + room.width - 32} y={room.y + room.height - 30} width="24" height="24" fill="transparent" />
            <path d={`M${room.x + room.width - 26} ${room.y + room.height - 24}h14v10h-14Z`} fill="#303e40" stroke="#53605b" />
            <rect x={room.x + room.width - 24} y={room.y + room.height - 22} width="10" height="5" fill={operating ? "#e5c58b" : "#626c64"} />
          </g>}
        </g>;
      })}

      <g aria-hidden="true" pointerEvents="none">{belts.map((belt) => <Belt key={belt.key} belt={belt} />)}</g>

      {layout.rooms.flatMap((room) => room.contents.map((item) => {
        const machine = machines.get(item.entityId) ?? item.machine;
        const edits = operationsFor(item.entityId);
        return <g key={item.key} data-entity-id={item.entityId} data-observation={machine.reading.observation} data-state={machine.reading.state} data-shape={item.shape}
          data-tooltip={`${machineInfo(machine, hallOf.get(machine.id))}${edits.map(({ proposal, operation }) => `\n${proposal.title}: ${operation.kind} · ${operation.path}`).join("")}`}
          {...sceneAction(() => selectEntity(item.entityId))} aria-label={`Inspect ${machine.label}`} className="dfFactoryScene__target">
          <rect className="dfFactoryScene__focus" x={item.x - 4} y={item.y - 14} width={item.width + 8} height={item.height + 18} fill="transparent" />
          <Station item={item} machine={machine} selected={selectedId === item.entityId} />
          {litIds.has(item.entityId) ? <rect data-lit="" className="dfFactoryScene__selection" x={item.x - 6} y={item.y - 6} width={item.width + 12} height={item.height + 12} /> : null}
          {[...new Map(edits.map((edit) => [`${edit.proposal.id}:${edit.operation.kind}`, edit])).values()].slice(0, 3).map(({ proposal, operation }, index) => <ProposalMark key={`${proposal.id}:${operation.path}`} proposalId={proposal.id} item={item} kind={operation.kind} index={index} stale={proposal.state !== "active"} />)}
        </g>;
      }))}

      <g data-fence="">
        <path d={`M${layout.fence} 24V${layout.height}`} className="dfPlant__fence" />
        {layout.gates.map((gate) => {
          const machine = machines.get(gate.machine.id) ?? gate.machine;
          return <g key={gate.machine.id} data-entity-id={gate.machine.id} data-observation={machine.reading.observation} data-state={machine.reading.state} className={`dfFactoryScene__target s-${machine.reading.observation} op-${machine.reading.state}`}
            data-tooltip={machineInfo(machine)} {...sceneAction(() => selectEntity(gate.machine.id))} aria-label={`Inspect ${machine.label}`}>
            <rect className="dfFactoryScene__focus" x={gate.x - 4} y={gate.y - 4} width={130} height={gate.height + 8} fill="transparent" />
            <rect x={gate.x} y={gate.y} width={gate.width} height={gate.height} className={machine.reading.evidence === "runtime" ? "dfPlant__gate dfPlant__gate--runtime" : "dfPlant__gate"} />
            <rect x={gate.x + 2} y={gate.y + gate.height / 2 - 2} width="3" height="4" className="lamp" />
            <text x={gate.x + gate.width + 6} y={gate.y + 12} className="dfPlant__label" fontSize="8">{shortLabel(machine.label, 20)}</text>
            <text x={gate.x + gate.width + 6} y={gate.y + 23} className="dfPlant__small" fontSize="7">{machine.reading.evidence === "runtime" ? "seen, not in code" : machine.reading.ratePerHour > 0 ? `${rate(machine.reading.ratePerHour)} · caller side` : "opaque"}</text>
          </g>;
        })}
      </g>

      <WorkLine layout={layout} crates={crates} tasks={tasks} placements={placements} connected={connected} live={animate} onLight={setLit} onSelect={proposals?.onSelect} />

      <g data-tooltip="Discussion board" className={onOpenBoard ? "dfFactoryScene__target" : undefined} aria-label="Open discussion board" {...sceneAction(onOpenBoard === undefined ? undefined : () => onOpenBoard(projectId))} transform={`translate(${PADDING + 24} 90)`}><rect className="dfFactoryScene__focus" x="-8" y="-8" width="48" height="48" fill="transparent" /><g aria-hidden="true"><Frame name="prop.board" x={0} y={0} /><text x="0" y="30" fill="#d4ddd2" fontSize="10">BOARD</text></g></g>
      {(() => { const cue = knowledgeCues.filter((item) => item.board).at(-1); return cue === undefined ? null : <KnowledgeCueMark cue={cue} x={PADDING + 58} y={88} at="board" />; })()}
      {nook?.furniture.filter((piece) => (!piece.roomId || labels.has(piece.roomId)) && (appearance.scenery !== "off" || (piece.errand === "shelf" && onOpenLibrary))).map((piece) => <g key={piece.key} data-break-room={piece.errand} opacity=".8" transform={`translate(${piece.x} ${piece.y}) scale(${WORKER_SIZE / FRAME})`}>
        {piece.errand !== "shelf" || onOpenLibrary === undefined ? null : <g className="dfFactoryScene__target" data-tooltip="Library · documents" {...sceneAction(() => onOpenLibrary(projectId))} aria-label="Open project library"><rect className="dfFactoryScene__focus" x="-9" y="-9" width="35" height="35" fill="transparent" /></g>}
        <g aria-hidden="true" pointerEvents="none"><Frame name={piece.errand === "shelf" ? "prop.bookshelf" : "prop.coffeestation"} x={0} y={0} />
        {piece.errand === "shelf" ? <text x="8" y="23" textAnchor="middle" fill="#d4ddd2" fontSize="4">LIBRARY</text> : null}
        {piece.errand !== "coffee" ? null : <path d="M2 15v3 M14 15v3" stroke="#303b3b" strokeWidth="2" />}</g>
      </g>)}
      {(() => { const cue = knowledgeCues.filter((item) => !item.board).at(-1), shelf = nook?.furniture.find((piece) => piece.errand === "shelf" && (!piece.roomId || labels.has(piece.roomId)) && (appearance.scenery !== "off" || onOpenLibrary)); return cue === undefined || shelf === undefined ? null : <KnowledgeCueMark cue={cue} x={shelf.x + WORKER_SIZE} y={shelf.y} at="shelf" />; })()}
      {[
        { label: "Break room · ambient", seats: seating.resting, planning: false, occupied: commonResting.length },
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
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="24" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">{reading ? "READING THE PLANT…" : "NO OPERATIONAL STRUCTURE INFERRED YET"}</text> : null}

      {placements.filter((placement) => placement.area === "resting" && placement.roomId !== undefined).map((seat) => <g key={seat.id} data-nearby-rest={seat.roomId} aria-hidden="true"><rect x={seat.x - 12} y={seat.y + 5} width="24" height="7" fill="#655948" stroke="#9b8b6b" /><path d={`M${seat.x - 8} ${seat.y + 12}v5m16-5v5`} stroke="#74664e" strokeWidth="3" /></g>)}
      <SceneWorkers knowledgeCues={knowledgeCues} nearby={appearance.social === "nearby"} errands={appearance.scenery !== "off"} restingSeats={[...resting.filter((seat) => seat.roomId !== undefined), ...seating.resting]} tray={tray} peerQuestions={peerQuestions} furniture={tables} layout={layout} placements={placements} labels={labels} workers={workers} tasks={tasks} connected={connected} animate={appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectTask={onSelectTask} onSelectHumanRequest={onSelectHumanRequest} onSelectProposal={proposals?.onSelect} />
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
    {layout.rooms.length === 0 ? null : <div className="dfPlantOverview">
      <div className="dfPlantOverview__zoom" role="group" aria-label="Zoom">
        <IconButton icon="minus" aria-label="Zoom out" disabled={zoom === undefined} onClick={() => zoomBy(1 / 1.5)} />
        <IconButton icon="plus" aria-label="Zoom in" disabled={zoom !== undefined && zoom >= MAX_ZOOM} onClick={() => zoomBy(1.5)} />
        <IconButton icon="fit" aria-label="Fit floor" disabled={zoom === undefined} onClick={() => setZoom(undefined)} />
      </div>
      <PlantMap layout={layout} width={sceneWidth} height={sceneHeight} machines={machines} graph={graph} placements={placements} workers={workers} viewElement={viewElement} onCentre={centreOn} />
    </div>}
    </div>
    {selected === undefined ? null : <MachineInspector key={selected.id} machine={selected} hall={hallOf.get(selected.id)} onLoadNode={onLoadNode} onInvestigate={onInvestigate}
      proposals={proposals === undefined ? [] : proposalsForEntity(proposals.items, selected.id)} onSelectProposal={proposals?.onSelect}
      onFocus={() => focusEntity(selected.id)} onDiscuss={onDiscussSource === undefined ? undefined : () => onDiscussSource(selected.id)} onClose={() => selectEntity(undefined)} />}
    </>
  );
}

const MAX_ZOOM = 4;

/**
 * Change requests on the outbound line to the fence. A crate stands where its
 * records put it and moves only when they change: once along the line, or,
 * when just opened, from the agent whose task opened it. Reconnecting is a
 * first look, never a replay.
 */
function WorkLine({ layout, crates, tasks, placements, connected, live, onLight, onSelect }: {
  layout: SceneLayout; crates: readonly SceneCrate[]; tasks: readonly SceneTask[]; placements: ReturnType<typeof placeWorkers>;
  connected: boolean; live: boolean; onLight: (id: string | undefined) => void; onSelect?: (id: string) => void;
}) {
  const placed = useMemo(() => placeCrates(layout, crates), [layout, crates]);
  const reduced = useReducedMotion();
  const moving = live && !reduced;
  const seen = useRef<ReadonlyMap<string, SceneCrate["station"]>>(undefined);
  const resting = useRef(new Map<string, ScenePoint>());
  const flights = useRef<readonly FloorMessage[]>([]);
  const [clock, setClock] = useState(0);
  useEffect(() => {
    if (!connected) { seen.current = undefined; flights.current = []; return; }
    const { seen: next, moves } = observeCrates(seen.current, crates);
    seen.current = next;
    const started = now(), slots = new Map(placed.map((spot) => [spot.crate.id, spot]));
    const sent = (moving ? moves : []).flatMap(({ crate, from }) => {
      const to = slots.get(crate.id)!;
      const origin = from === undefined ? placements.find((placement) => tasks.some((task) => crate.taskIds.includes(task.id) && task.agentId === placement.id)) : resting.current.get(crate.id);
      // Along the line it slides; from its author it is handed over like paper from the tray.
      return !to.shown || origin === undefined ? [] : [send({ key: crate.id, kind: "assign", to: crate.id }, started, origin, from === undefined ? undefined : { points: [to], length: Math.hypot(to.x - origin.x, to.y - origin.y) }, 0)];
    });
    flights.current = [...new Map([...flights.current, ...sent].filter((flight) => flight.startedAt + flight.travel > started).map((flight) => [flight.key, flight])).values()].slice(-16);
    resting.current = new Map(placed.map((spot) => [spot.crate.id, spot]));
    setClock(started);
  }, [placed, connected]);
  useEffect(() => {
    if (!moving || !flights.current.some((flight) => flight.startedAt + flight.travel > clock) || typeof requestAnimationFrame !== "function") return;
    const frame = requestAnimationFrame((time) => setClock(time));
    return () => cancelAnimationFrame(frame);
  }, [clock, moving]);
  if (crates.length === 0) return null;
  const belt = layout.line[0]!.y + 28;
  return <g data-work-line="">
    <path aria-hidden="true" d={`M${layout.line[0]!.x} ${belt + 3}H${layout.fence}`} className="b-base" />
    {layout.line.map((station, index) => { const count = crates.filter((crate) => crate.station === index).length; return <g key={station.label} aria-hidden="true">
      <rect x={station.x} y={station.y} width="2" height={station.height} className="md" />
      <text x={station.x + 6} y={station.y + 9} className="dfPlant__label" fontSize="8">{station.label.toUpperCase()} · {count}</text>
      {count > LINE_SLOTS ? <text data-crate-overflow={count - LINE_SLOTS} x={station.x + 10 + LINE_SLOTS * 18} y={belt + 3} className="dfPlant__small" fontSize="7">+{count - LINE_SLOTS}</text> : null}
    </g>; })}
    {placed.filter((spot) => spot.shown).map(({ crate, x, y }) => {
      const flight = moving ? flights.current.find((candidate) => candidate.key === crate.id) : undefined;
      const point = (flight === undefined ? undefined : messageAt(flight, { x, y }, clock).point) ?? { x, y };
      return <g key={crate.id} data-crate={crate.id} data-crate-station={crate.station} data-fault={crate.fault ? "" : undefined} className="dfFactoryScene__target"
        data-tooltip={`PR #${crate.number} · ${crate.title}\n${layout.line[crate.station]!.label} · ${crate.stage}`} aria-label={`PR #${crate.number} ${crate.title}: ${crate.stage}`}
        {...sceneAction(onSelect === undefined ? undefined : () => onSelect(crate.id))} onPointerEnter={() => onLight(crate.id)} onPointerLeave={() => onLight(undefined)} onFocus={() => onLight(crate.id)} onBlur={() => onLight(undefined)}
        transform={`translate(${point.x} ${point.y})`}>
        <rect className="dfFactoryScene__focus" x="-9" y="-12" width="18" height="16" fill="transparent" />
        <rect x="-7" y="-9" width="14" height="10" className="crate" />
        {crate.fault ? <rect x="3" y="-12" width="6" height="6" className="redtag" /> : null}
      </g>;
    })}
  </g>;
}

/**
 * The plant at a glance, always in view: halls painted by coverage, gates on
 * the fence, where workers are and the window the floor shows. Pointing at it
 * moves the floor there.
 */
function PlantMap({ layout, width, height, machines, graph, placements, workers, viewElement, onCentre }: {
  layout: SceneLayout; width: number; height: number; machines: ReadonlyMap<string, SceneMachine>; graph: SceneGraph;
  placements: ReturnType<typeof placeWorkers>; workers: readonly SceneWorker[]; viewElement: RefObject<SVGRectElement | null>; onCentre: (x: number, y: number) => void;
}) {
  const go = (event: PointerEvent<SVGSVGElement>) => {
    if (event.type === "pointermove" && event.buttons !== 1) return;
    const box = event.currentTarget.getBoundingClientRect();
    onCentre((event.clientX - box.left) / box.width * width, (event.clientY - box.top) / box.height * height);
  };
  const needs = new Set(workers.filter((worker) => worker.activity === "needs-you").map((worker) => worker.id));
  const halls = graph.halls.length;
  // At most 12rem wide or 10rem tall, whichever the plant's shape reaches first.
  return <svg className="dfPlantMap" viewBox={`0 0 ${width} ${height}`} style={{ width: `min(12rem, ${(10 * width / height).toFixed(2)}rem)` }} role="img" aria-label={`Plant overview: ${halls} ${halls === 1 ? "unit" : "units"}, ${graph.parties.length} external, ${workers.length} workers`}
    onPointerDown={go} onPointerMove={go}>
    <rect width={width} height={height} className="dfPlantMap__ground" />
    {layout.rooms.map((room) => {
      const reading = machines.get(room.id)?.reading;
      return <rect key={room.id} data-overview-room={room.id} x={room.x} y={room.y} width={room.width} height={room.height}
        className={`dfPlantMap__room dfPlantMap__room--${room.kind} s-${reading?.observation ?? "unobserved"} op-${reading?.state ?? "unknown"}`} />;
    })}
    <path d={`M${layout.fence} 24V${layout.height}`} className="dfPlantMap__fence" />
    {layout.gates.map((gate) => <rect key={gate.machine.id} x={gate.x} y={gate.y} width={gate.width} height={gate.height} className={`dfPlantMap__gate s-${(machines.get(gate.machine.id) ?? gate.machine).reading.observation}`} />)}
    {placements.map((placement) => <circle key={placement.id} cx={placement.x} cy={placement.y} r="10" className={needs.has(placement.id) ? "dfPlantMap__worker dfPlantMap__worker--needs" : "dfPlantMap__worker"} />)}
    <rect ref={viewElement} data-overview-view="" width={width} height={height} className="dfPlantMap__view" />
  </svg>;
}

function sceneAction(select: (() => void) | undefined) {
  return select === undefined ? {} : { role: "button", tabIndex: 0, onClick: select, style: { cursor: "pointer" }, onKeyDown: (event: KeyboardEvent<SVGGElement>) => {
    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); }
  } };
}

/** Events per hour, in the unit a person reads quickly. */
function rate(perHour: number) {
  return perHour >= 120 ? `${Math.round(perHour / 60)}/min` : `${perHour}/h`;
}

const OBSERVATION_TEXT: Record<SceneReading["observation"], string> = {
  observed: "Observed", quiet: "Observed, quiet", partial: "Partly observed", stale: "Stale: no current reading", unobserved: "No telemetry: activity unknown", opaque: "External: its own state cannot be seen",
};
const EVIDENCE_TEXT: Record<SceneReading["evidence"], string> = {
  static: "Inferred from code", runtime: "Seen at runtime only; not in code", both: "In code and seen at runtime", uncertain: "Inferred from a name or literal", contradicted: "Runtime evidence contradicts the code",
};

/** What a machine is, what we know of it and how. Never claims more than the reading does. */
function machineInfo(machine: SceneMachine, hall?: SceneHall) {
  const reading = machine.reading;
  const deployed = reading.deployedAt === undefined ? "" : `\nDeployed ${new Date(reading.deployedAt).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}`;
  const activity = reading.state === "unknown" ? "" : reading.state === "idle" ? " · idle" : ` · ${reading.state} · ${rate(reading.ratePerHour)}${reading.errorPermille > 0 ? ` · ${reading.errorPermille / 10}% errors` : ""}${reading.latencyMs > 0 ? ` · p95 ${reading.latencyMs} ms` : ""}`;
  return `${machine.label}\n${machine.represented === undefined ? machine.kind : `${machine.represented.length} ${machine.kind} nodes`}${hall !== undefined && hall.id !== machine.id ? ` in ${hall.label}` : machine.owner ? ` from ${machine.owner}` : ""}\n${OBSERVATION_TEXT[reading.observation]}${activity}\n${EVIDENCE_TEXT[reading.evidence]}${deployed}`;
}

function Coverage({ summary }: { summary: NonNullable<SceneGraph["summary"]> }) {
  return <p className="dfPlantCoverage" role="status" aria-label="Observation coverage">
    <strong>{summary.inferred}</strong> inferred · <strong className="dfPlantCoverage--observed">{summary.observed + summary.quiet}</strong> observed · <strong className="dfPlantCoverage--partial">{summary.partial}</strong> partial · <strong className="dfPlantCoverage--unobserved">{summary.unobserved}</strong> unobserved
    <small> · {summary.quiet} quiet · {summary.stale} stale · {summary.opaque} external{summary.runtime_only > 0 ? ` · ${summary.runtime_only} runtime-only` : ""}{summary.contradicted > 0 ? ` · ${summary.contradicted} contradicted` : ""}</small>
  </p>;
}

/** One machine, pictured by its kind; its observation and state are its paint, lamp and motion. */
function Station({ item, machine, selected }: { item: RoomContent; machine: SceneMachine; selected: boolean }) {
  const { x, y, width: w, height: h } = item, reading = machine.reading;
  const lamp = <g><rect x={x + w - 9} y={y + 3} width="3" height="4" className="lamp" /><rect x={x + w - 6} y={y + 3} width="3" height="4" className="lamp l2" /></g>;
  const quarter = <rect x={x + w / 2} y={y} width={w / 2} height={h / 2} className="pq" />;
  const scrap = reading.errorPermille > 0 && (reading.observation === "observed" || reading.observation === "partial")
    ? <g data-scrap={reading.errorPermille}><rect x={x + w + 2} y={y + h - 14} width="10" height="14" className="dfPlant__bin" /><rect x={x + w + 4} y={y + h - 2 - Math.max(1, Math.round(10 * reading.errorPermille / 1000))} width="6" height={Math.max(1, Math.round(10 * reading.errorPermille / 1000))} className="scrap" /></g> : null;
  // Latency is dwell: a slow machine holds more work in its window.
  const held = reading.state === "unknown" || reading.state === "idle" ? 0 : Math.min(4, 1 + Math.floor(Math.log2(1 + reading.latencyMs / 50)));
  const window = (wx: number, wy: number, ww: number, wh: number) => <g><rect x={wx} y={wy} width={ww} height={wh} className="win" />
    {Array.from({ length: held }, (_, index) => <rect key={index} x={wx + 3 + index * 6} y={wy + wh / 2 - 2} width="4" height="4" className="crate dfPlant__dwell" />)}</g>;
  const plaque = reading.observation === "unobserved" && w >= 40 ? <text x={x + w / 2} y={y + h / 2 + 3} textAnchor="middle" className="bptext" fontSize="6">NO TELEMETRY</text> : null;
  const stale = reading.observation === "stale" ? <text x={x} y={y + h + 9} className="dfPlant__small" fontSize="7">last seen {reading.lastSeen === undefined ? "earlier" : new Date(reading.lastSeen).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</text> : null;
  const tag = reading.evidence === "runtime" ? <g><rect x={x - 5} y={y - 6} width="10" height="11" className="qtag" /><text x={x} y={y + 3} textAnchor="middle" className="qmark" fontSize="8">?</text></g>
    : reading.evidence === "contradicted" ? <rect x={x - 4} y={y - 4} width="8" height="8" className="redtag" /> : null;
  // Docks have the wall beside them for a label; everything else is labelled under its own footprint.
  const label = item.shape === "line" ? null : item.shape === "dock" || item.shape === "manifold"
    ? <text x={x} y={y - 3} className="dfPlant__label" fontSize="8">{shortLabel(machine.label, 22)}</text>
    : <text x={x + w / 2} y={y + h + 9} textAnchor="middle" className="dfPlant__label" fontSize="8">{shortLabel(machine.label, Math.max(6, Math.floor((w + 10) / 5)))}</text>;
  let body: ReactNode;
  switch (item.shape) {
    case "line": body = <g><rect x={x + 3} y={y + h} width={w - 2} height="3" className="shd" /><rect x={x} y={y} width={w} height={h} className="m" /><rect x={x + 1} y={y + 1} width={w - 2} height="4" className="mh" />
      {window(x + 8, y + 12, w - 30, h - 22)}<rect x={x + w - 18} y={y + 12} width="10" height={h - 22} className="md" />{lamp}{quarter}
      <text x={x + 4} y={y + h + 12} className="dfPlant__label" fontSize="8">main line</text></g>; break;
    case "dock": body = <g><rect x={x} y={y} width={w} height={h} className="dock" />{[0, 1, 2].map((index) => <rect key={index} x={x + 3 + index * 6} y={y + h - 5} width="4" height="3" className="chev" />)}<rect x={x + w - 14} y={y + 4} width="10" height={h - 8} className="win" />{lamp}{quarter}</g>; break;
    case "manifold": body = <g><rect x={x} y={y} width={w} height={h} className="m" />{[0, 1, 2, 3].map((index) => <rect key={index} x={x + 4} y={y + 5 + index * 8} width={w - 18} height="4" className="dock" />)}{lamp}{quarter}
      <text x={x + w / 2} y={y + h + 10} textAnchor="middle" className="dfPlant__small" fontSize="7">{machine.represented?.length} routes</text></g>; break;
    case "clock": body = <g><circle cx={x + w / 2} cy={y + h / 2} r={w / 2 - 1} className="m" /><circle cx={x + w / 2} cy={y + h / 2} r={w / 2 - 4} className="win" /><path d={`M${x + w / 2} ${y + h / 2}v-6m0 6h5`} className="dfPlant__hand" /></g>; break;
    case "cell": body = <g><rect x={x} y={y + h - 10} width={w} height="10" className="m" /><rect x={x + w / 2 - 3} y={y + 4} width="6" height={h - 14} className="md arm" /><rect x={x + w / 2 - 8} y={y + 2} width="16" height="5" className="mh arm" />{lamp}{quarter}</g>; break;
    case "silo": body = <g><rect x={x + 2} y={y + h} width={w - 2} height="3" className="shd" /><rect x={x + 3} y={y} width={w - 6} height={h} className="m" /><rect x={x} y={y + 3} width={w} height={h - 6} className="m" /><rect x={x + 3} y={y + 2} width={w - 6} height="3" className="mh" />{window(x + 6, y + 10, w - 12, 8)}{quarter}</g>; break;
    case "conveyor": body = <g><path d={`M${x} ${y + h / 2}h${w}`} className="b-base" />{window(x + 4, y + 2, w - 8, h - 4)}</g>; break;
    default: body = <g><rect x={x} y={y} width={w} height={h} className="crate" /><text x={x + w / 2} y={y + h / 2 + 3} textAnchor="middle" className="qmark" fontSize="8">?</text></g>;
  }
  return <g className={`s-${reading.observation} op-${reading.state}${selected ? " dfPlant--selected" : ""}`}>{label}{body}{plaque}{scrap}{stale}{tag}</g>;
}

type BeltRoute = Readonly<{ key: string; points: readonly ScenePoint[]; reading: SceneReading; kind: string }>;

/**
 * Material moves only where a flow was observed: belt density reads as rate
 * and speed is fixed. An edge nobody can see is a dashed blueprint run, never
 * an empty belt.
 */
function Belt({ belt }: { belt: BeltRoute }) {
  const d = `M${belt.points.map((point) => `${point.x} ${point.y}`).join(" L")}`;
  const reading = belt.reading;
  if (reading.observation === "unobserved" || reading.observation === "stale" || reading.observation === "opaque" && reading.ratePerHour === 0 || reading.observation === "partial" && reading.state === "unknown") {
    return <path d={d} className="b-inf" data-belt={belt.kind} data-observation={reading.observation} />;
  }
  const spacing = reading.ratePerHour === 0 ? 0 : Math.max(5, Math.round(48 / Math.log2(2 + reading.ratePerHour / 60)));
  return <g data-belt={belt.kind} data-observation={reading.observation} data-state={reading.state}>
    <path d={d} className="b-base" />
    {spacing === 0 ? null : <path d={d} className={`dfPlant__material${reading.state === "failing" || reading.state === "degraded" ? " dfPlant__material--faulty" : ""}`} strokeDasharray={`3 ${spacing}`} />}
    {reading.evidence === "runtime" ? <circle cx={belt.points[Math.floor(belt.points.length / 2)]!.x} cy={belt.points[Math.floor(belt.points.length / 2)]!.y} r="4" className="qtag" /> : null}
  </g>;
}

/**
 * A hall's own intake: the unit's reading carried from its door to its main
 * line. A source that counts traffic per service but not per route (a
 * platform's per-script analytics) can say a unit is busy without saying
 * which machine handled it, so the hall moves while its machines stay
 * partial. A unit with no known state has no intake drawn at all.
 */
function hallIntake(layout: SceneLayout, halls: readonly SceneHall[]): readonly BeltRoute[] {
  return halls.flatMap((hall) => {
    const room = layout.rooms.find((candidate) => candidate.id === hall.id);
    const line = room?.contents.find((item) => item.entityId === hall.id);
    if (room === undefined || line === undefined || hall.reading.state === "unknown") return [];
    const x = line.x + line.width / 2, below = line.y + line.height;
    return [{ key: `intake ${hall.id}`, kind: "intake", reading: hall.reading, points: [room.door, { x: room.door.x, y: below + 8 }, { x, y: below + 8 }, { x, y: below }] }];
  });
}

/**
 * Orthogonal belt runs: inside a hall directly between machines; between
 * halls out of the door, along corridors and the spine, in at the far door;
 * to a party along the corridor to the fence.
 */
function routeFlows(layout: SceneLayout, flows: readonly SceneFlow[]): readonly BeltRoute[] {
  const at = new Map<string, { item?: RoomContent; room?: SceneRoomLayout; gate?: SceneLayout["gates"][number] }>();
  for (const room of layout.rooms) for (const item of room.contents) at.set(item.entityId, { item, room });
  for (const gate of layout.gates) at.set(gate.machine.id, { gate });
  const spineX = ROOM_LEFT - 16;
  const lane = (room: SceneRoomLayout) => room.door.y + 16;
  const center = (item: RoomContent): ScenePoint => ({ x: item.x + item.width / 2, y: item.y + item.height / 2 });
  return flows.flatMap((flow) => {
    // Handles runs from the dock into the unit; draw every belt in the direction work moves.
    const from = at.get(flow.from), to = at.get(flow.to);
    if (from === undefined || to === undefined) return [];
    const key = `${flow.from} ${flow.to}`;
    if (from.item && to.item && from.room === to.room) {
      const a = center(from.item), b = center(to.item);
      const sx = a.x < b.x ? from.item.x + from.item.width : from.item.x, ex = a.x < b.x ? to.item.x : to.item.x + to.item.width;
      return [{ key, kind: flow.kind, reading: flow.reading, points: [{ x: sx, y: a.y }, { x: (sx + ex) / 2, y: a.y }, { x: (sx + ex) / 2, y: b.y }, { x: ex, y: b.y }] }];
    }
    if (from.room === undefined || from.item === undefined) return [];
    const start = center(from.item), out: ScenePoint[] = [{ x: start.x, y: from.item.y + from.item.height }, { x: start.x, y: from.room.door.y - 6 }, { x: from.room.door.x, y: from.room.door.y - 6 }, from.room.door, { x: from.room.door.x, y: lane(from.room) }];
    if (to.gate) {
      const gateY = to.gate.y + to.gate.height / 2;
      return [{ key, kind: flow.kind, reading: flow.reading, points: [...out, { x: layout.fence - 12, y: lane(from.room) }, { x: layout.fence - 12, y: gateY }, { x: to.gate.x, y: gateY }] }];
    }
    if (to.room === undefined || to.item === undefined) return [];
    const end = center(to.item);
    const into: ScenePoint[] = [{ x: to.room.door.x, y: lane(to.room) }, to.room.door, { x: to.room.door.x, y: to.room.door.y - 6 }, { x: end.x, y: to.room.door.y - 6 }, { x: end.x, y: to.item.y + to.item.height }];
    const across = lane(from.room) === lane(to.room) ? [] : [{ x: spineX, y: lane(from.room) }, { x: spineX, y: lane(to.room) }];
    return [{ key, kind: flow.kind, reading: flow.reading, points: [...out, ...across, ...into] }];
  });
}

function ProposalMark({ proposalId, item, kind, index, stale }: { proposalId: string; item: RoomContent; kind: SceneProposal["operations"][number]["kind"]; index: number; stale: boolean }) {
  const color = stale ? "#b1a9a0" : kind === "removal" ? "#dfa48e" : "#a8d8ea";
  return <g data-proposal-id={proposalId} data-proposal-kind={kind} pointerEvents="none" opacity={stale ? .65 : 1}>
    {index !== 0 ? null : <rect x={item.x - 3} y={item.y - 3} width={item.width + 6} height={item.height + 6} fill="none" stroke={color} strokeWidth="2" strokeDasharray={kind === "addition" ? "5 3" : kind === "removal" ? "2 3" : undefined} />}
    <rect x={item.x - 15} y={item.y + index * 13} width="11" height="11" fill="#203a46" stroke={color} />
    <text x={item.x - 9.5} y={item.y + index * 13 + 8} fill={color} textAnchor="middle" fontSize="8">{kind === "addition" ? "+" : kind === "removal" ? "×" : kind === "move" ? "→" : "M"}</text>
  </g>;
}

/** The inspector loads evidence only when opened; runtime-only values are shown as data. */
function MachineInspector({ machine, hall, onLoadNode, onInvestigate, proposals, onSelectProposal, onFocus, onDiscuss, onClose }: {
  machine: SceneMachine; hall?: SceneHall; onLoadNode?: (id: string) => Promise<OperationalNodeView>; onInvestigate?: (machine: SceneMachine, hall?: SceneHall) => void;
  proposals: readonly SceneProposal[]; onSelectProposal?: (id: string) => void; onFocus: () => void; onDiscuss?: () => void; onClose: () => void;
}) {
  const [detail, setDetail] = useState<OperationalNodeView | "unavailable">();
  const [asked, setAsked] = useState(false);
  useEffect(() => {
    if (onLoadNode === undefined || machine.represented !== undefined) return;
    let live = true;
    onLoadNode(machine.id).then((value) => { if (live) setDetail(value); }, () => { if (live) setDetail("unavailable"); });
    return () => { live = false; };
  }, [machine.id]);
  const reading = machine.reading;
  const worth = reading.state === "failing" || reading.state === "degraded" || reading.evidence === "runtime" || reading.evidence === "contradicted" || reading.observation === "unobserved";
  return <section className="dfRoomDetails" aria-label="Machine inspector">
    <h3>{machine.label}</h3>
    <p>{machine.represented === undefined ? machine.kind : `${machine.represented.length} folded ${machine.kind} nodes`}{hall !== undefined && hall.id !== machine.id ? ` in ${hall.label}` : ""}</p>
    <p className={`dfPlantReading s-${reading.observation} op-${reading.state}`}>{OBSERVATION_TEXT[reading.observation]}{reading.state === "unknown" ? "" : ` · ${reading.state}`}{reading.ratePerHour > 0 ? ` · ${rate(reading.ratePerHour)}` : ""}{reading.errorPermille > 0 ? ` · ${reading.errorPermille / 10}% errors` : ""}{reading.latencyMs > 0 ? ` · p95 ${reading.latencyMs} ms` : ""}</p>
    <p>{EVIDENCE_TEXT[reading.evidence]}</p>
    <button type="button" onClick={onFocus}>Focus on floor</button>
    {onDiscuss ? <button type="button" onClick={onDiscuss}>Discuss this machine</button> : null}
    {onInvestigate && worth ? <button type="button" disabled={asked} onClick={() => { setAsked(true); onInvestigate(machine, hall); }}>{asked ? "Investigation queued" : "Investigate"}</button> : null}
    <button type="button" onClick={onClose}>Close</button>
    {proposals.length === 0 ? null : <div aria-label="Changes affecting this machine">{proposals.map((proposal) => <button type="button" key={proposal.id} onClick={() => onSelectProposal?.(proposal.id)}>{proposal.title}</button>)}</div>}
    <details><summary>Evidence</summary>
      <p>Node <code>{machine.id}</code></p>
      {detail === undefined ? <p>{onLoadNode === undefined || machine.represented !== undefined ? "Evidence is available on each folded route." : "Loading evidence…"}</p> : detail === "unavailable" ? <p>Evidence unavailable.</p> : <>
        {Object.keys(detail.selectors).length === 0 ? null : <dl>{Object.entries(detail.selectors).map(([key, value]) => <div key={key}><dt>{key}</dt><dd><code>{value}</code></dd></div>)}</dl>}
        <ul aria-label="Evidence">{detail.evidence.map((item, index) => <li key={index}>{item.origin} · {item.source} · {item.confidence}{item.detail ? <> · <code>{item.detail}</code></> : null}</li>)}</ul>
        {detail.sources.length === 0 ? null : <ul aria-label="Source locations">{detail.sources.map((location) => <li key={`${location.repository_id}:${location.path}:${location.line ?? 0}`}><code>{location.path}{location.line ? `:${location.line}` : ""}</code></li>)}</ul>}
        {detail.modules.length === 0 ? null : <p>{detail.modules.length} code areas run in this unit.</p>}
        <p>{detail.observers.length === 0 ? "No runtime source has reported on it." : `Seen by ${detail.observers.join(", ")}.`}</p>
      </>}
    </details>
  </section>;
}
