import { useLayoutEffect, useEffect, useMemo, useRef, useState, type MouseEvent, type FocusEvent, type PointerEvent, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import type { OperationalNodeView, PeerQuestionItem } from "@dark-factory/client";
import type { SceneTask } from "../console-view.js";
import {
  PADDING,
  MARGIN,
  furnish,
  placeMachinery,
  WORKER_SIZE,
  labelChars,
  placeWorkers,
  placeErrands,
  breakRoomNook,
  proposalsForEntity,
  placeCrates,
  stationOf,
  LINE_SLOTS,
  type SceneCrate,
  type SceneGraph,
  type SceneLayout,
  type SceneMachinery,
  type SceneMachine,
  type ScenePoint,
  type SceneProposal,
  type SceneReading,
  type SceneStation,
  type SceneUnit,
  type SceneWorker,
  type BreakRoomErrand,
  recordedEffort,
} from "./scene.js";
import { IconButton } from "../icons.js";
import { DEFAULT_FLOOR_APPEARANCE, type FloorAppearance } from "../floor-appearance.js";
import { breakRoomHabit, restingItem, workerFrames, workerPhase } from "./appearance.js";
import { catAt, catBed, chats, gossip, type Seat } from "./idle-life.js";
import { endsAt, isPaper, messageAt, observe, observeCrates, send, type FloorMessage, type Seen } from "./messages.js";
import { beltRoutes, type BeltStub, directionBetween, findRoute, pointOnRoute, samePoint, type WorkerMotion } from "./movement.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";



export type FactorySceneProps = Readonly<{
  proposals?: { items: readonly SceneProposal[]; selected?: string; onSelect: (id: string) => void };
  /** Change requests on the outbound line; selecting one uses `proposals.onSelect`. Undefined until read on this connection. */
  crates?: readonly SceneCrate[];
  tools?: ReactNode;
  /** Read one machine's evidence when its inspector opens. */
  onLoadNode?: (nodeId: string) => Promise<OperationalNodeView>;
  /** Queue an investigation of a machine as an ordinary task. */
  onInvestigate?: (machine: SceneMachine, unit?: SceneUnit) => void;
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
const DAY = 24 * 60 * 60_000;
const NO_QUESTIONS: readonly PeerQuestionItem[] = [];
export type KnowledgeCueView = Readonly<{ key: string; agentId: string; board: boolean; reading: boolean; label: string; open?: () => void }>;
const NO_CUES: readonly KnowledgeCueView[] = [];
type Implement = ReturnType<typeof breakRoomNook>["furniture"][number];
const IMPLEMENT_NAMES: Record<BreakRoomErrand, string> = { shelf: "bookshelf", coffee: "coffee station", board: "discussion board", missions: "planning table", tasks: "task tray" };
// A recorded visit: the walk over (the far end of the nook is a few seconds away by the aisles), a while at the implement, then back.
const VISIT = 10000;

/** A recorded operation, unlike ambient life, is labelled, focusable and opens what it used. */
function KnowledgeCueMark({ cue, x, y, at }: { cue: KnowledgeCueView; x: number; y: number; at: string }) {
  return <g className="dfKnowledgeCue dfFactoryScene__target" data-knowledge-cue={at} data-knowledge-key={cue.key} data-tooltip={`Recorded · ${cue.label}`} aria-label={`Recorded: ${cue.label}`} {...sceneAction(cue.open)} transform={`translate(${x} ${y})`}>
    <rect className="dfFactoryScene__focus" x="-9" y="-9" width="18" height="18" rx="3" fill="#f3ecd6" stroke={cue.board ? "#80ddff" : "#e5c58b"} strokeWidth="2" />
    <path aria-hidden="true" d={cue.reading ? "M-4 -5h5l3 3v7h-8Z M-2 -1h4 M-2 1.5h4" : "M-5 -3h10v7h-10Z M-5 -3l5 4l5 -4"} fill="none" stroke="#172330" strokeWidth="1.3" />
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
  route?: ReturnType<typeof findRoute>;
  startedAt?: number;
  /** No walk exists to where it was sent: it is drawn there, and says so. */
  stranded?: boolean;
}>;

const WALK_SPEED = 96;

function now() { return typeof performance === "undefined" ? 0 : performance.now(); }

function motionPoint(motion: MotionState, at: number) {
  if (motion.route === undefined || motion.startedAt === undefined) return { point: motion.point, walking: false };
  const travelled = Math.max(0, (at - motion.startedAt) / 1000 * WALK_SPEED);
  if (travelled >= motion.route.length) return { point: motion.route.points.at(-1) ?? motion.point, walking: false };
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
function useSceneMotion(layout: SceneLayout, seated: ReturnType<typeof placeWorkers>, floorDigest: string, connected: boolean, reduced: boolean, active: ReadonlySet<string>, workers: FactorySceneProps["workers"], errands: boolean, restless: (at: number) => boolean, visits: ReadonlyMap<string, Implement>) {
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
  // The fixtures keep no clock of their own: every couple of seconds of moving time it asks who has got up.
  const errandClock = moving && errands && motionsFloor.current === floorDigest ? Math.floor(movingTime.current.total / 2000) * 2000 : undefined;
  // Who holds which piece, so that a visit outlasts changes among the others resting.
  const errandsBefore = useRef<ReturnType<typeof placeWorkers>>([]);
  const visitKey = [...visits].map(([id, piece]) => `${id} ${piece.key}`).join(" ");
  const placements = useMemo(() => {
    const byId = new Map(workers.map((worker) => [worker.id, worker]));
    // With the scenery off there are no ambient errands, only recorded visits.
    const ambient = errandsBefore.current = errandClock === undefined ? seated
      : placeErrands(seated, breakRoomNook(layout), (id) => { const worker = byId.get(id); return worker === undefined ? undefined : breakRoomHabit(worker); }, errandClock, errandsBefore.current);
    // A recorded visit outranks an ambient one: whoever idled at that piece sits back down.
    const taken = new Set([...visits.values()].map((piece) => piece.key));
    return ambient.map((placement, index) => { const piece = visits.get(placement.id); return piece !== undefined && placement.area === "resting" ? { ...seated[index]!, errand: piece.errand, errandKey: `use ${piece.key}`, ...piece.stand } : placement.errandKey !== undefined && taken.has(placement.errandKey) ? seated[index]! : placement; });
  }, [seated, errandClock, layout, workers, visitKey]);

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
      const unchanged = old.placement.area === placement.area && old.placement.stationId === placement.stationId && samePoint(old.placement, placement);
      if (unchanged) {
        next.set(placement.id, motionPoint(old, at).walking ? { ...old, point: old.point } : { placement, point: placement });
        continue;
      }
      // Retargeted from where it is drawn; with no walk there it is put there, marked, never drawn through a machine.
      const route = findRoute(layout, current, placement);
      next.set(placement.id, route === undefined || route.length === 0
        ? { placement, point: placement, ...(route === undefined ? { stranded: true } : {}) }
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
  const output = new Map<string, Readonly<{ x: number; y: number; motion: WorkerMotion; placement: ReturnType<typeof placeWorkers>[number]; stranded?: boolean }>>();
  for (const placement of placements) {
    const state = moving && motionsFloor.current === floorDigest ? motions.current.get(placement.id) : undefined;
    const current = state === undefined ? { point: placement, walking: false } : motionPoint(state, clock);
    const at = moving ? clock + workerPhase(placement.id) : undefined;
    output.set(placement.id, {
      // The placement this motion is actually on: pose and position are always of one moment.
      placement: state?.placement ?? placement,
      ...(state?.stranded ? { stranded: true } : {}),
      ...current.point,
      motion: current.walking
        ? { action: "walking", direction: current.direction!, frame: Math.floor((at ?? clock) / 150) % 2 as 0 | 1, at }
        : { action: active.has(placement.id) ? "interacting" : "still", frame: at === undefined ? 0 : Math.floor(at / (380 + workerPhase(placement.id) % 140)) % 2 as 0 | 1, at },
    });
  }
  return { placements, positions: output, pulse: moving ? clock : undefined };
}

/** The animation clock updates worker elements without rerendering the floor or atlas. */
function SceneWorkers({ knowledgeCues, nook, onOpenBoard, errands, peerQuestions, layout, placements: seated, labels, workers, tasks, connected, animate, selectedWorkerId, onSelectWorker, onSelectTask, onSelectHumanRequest, onSelectProposal }: Pick<FactorySceneProps, "workers" | "selectedWorkerId" | "onSelectWorker" | "onSelectTask" | "onSelectHumanRequest"> & {
  onSelectProposal?: (id: string) => void;
  layout: SceneLayout;
  placements: ReturnType<typeof placeWorkers>;
  labels: ReadonlyMap<string, string>;
  tasks: readonly SceneTask[];
  connected: boolean;
  animate: boolean;
  /** Whether the break-room furniture is there to be visited, and the cat to be found. */
  errands: boolean;
  /** The floor's fixtures: where handed-over work and recorded Library or Board operations come from or go to. */
  nook: readonly Implement[];
  onOpenBoard?: (projectId?: string) => void;
  peerQuestions: readonly PeerQuestionItem[];
  knowledgeCues: readonly KnowledgeCueView[];
}) {
  // Inventory/dependency metadata may change without changing a route's geometry.
  const geometryKey = useMemo(() => JSON.stringify([layout.width, layout.height, layout.solids]), [layout]);
  const active = useMemo(() => new Set(workers.filter((worker) => worker.location === "working" && worker.activity === "busy" && seated.some((placement) => placement.id === worker.id && placement.area === "work")).map((worker) => worker.id)), [workers, seated]);
  const reduced = useReducedMotion();
  const mail = useRef<readonly (FloorMessage & { end?: { x: number; y: number } })[]>([]);
  // Who is up using an implement because of a recorded event, and until when.
  const visits = useRef(new Map<string, { piece: Implement; until: number }>());
  const visiting = new Map(reduced ? [] : [...visits.current].filter(([, visit]) => visit.until > now()).map(([id, visit]) => [id, visit.piece]));
  const { placements, positions, pulse } = useSceneMotion(layout, seated, geometryKey, connected && animate, reduced, active, workers, errands, (at) => mail.current.some((message) => endsAt(message) > at) || errands && catAt(tableOf(seatRows(seated.filter((seat) => seat.area === "resting"))) ?? [], at)?.moving === true, visiting);
  const centre = (errand: BreakRoomErrand) => { const piece = nook.find((item) => item.errand === errand); return piece && { x: piece.x + WORKER_SIZE / 2, y: piece.y + WORKER_SIZE / 2 }; };
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  const visitor = (id: string) => positions.get(id)?.placement.errandKey?.startsWith("use ") === true;
  // Nobody on the floor, no pulse: what lives there rests as it does under any stopped clock.
  const at = placements.length === 0 ? undefined : pulse;
  // What changed since the last look is sent across the floor once; with the clock stopped it is only noted.
  const seen = useRef<Seen>(undefined);
  const live = useRef(false);
  live.current = at !== undefined;
  useEffect(() => {
    // The floor is mounted before there is any state, and may lose it again: a look only counts while
    // connected, so the first snapshot after connecting or reconnecting is history, never a burst of old news.
    if (!connected) { seen.current = undefined; visits.current.clear(); return; }
    const { seen: next, events } = observe(seen.current, tasks, peerQuestions, knowledgeCues);
    seen.current = next;
    const started = now();
    const kept = mail.current.filter((message) => endsAt(message) > started);
    // A recorded operation flies between the agent, wherever it already is, and the shelf or board; one per agent at a time.
    const recording = new Set(kept.flatMap((message) => message.kind === "read" || message.kind === "post" ? [message.from ?? message.to] : []));
    visits.current = new Map([...visits.current].filter(([, visit]) => visit.until > started));
    mail.current = [...kept, ...(!live.current ? [] : events).flatMap((event) => {
      const cue = knowledgeCues.find((item) => item.key === event.subject), furniture = cue === undefined ? undefined : centre(cue.board ? "board" : "shelf");
      // Someone idling beside a machine gets up and uses the implement instead; anyone at work stays put and the message flies.
      const who = event.kind === "post" ? event.from : event.kind === "ask" || event.kind === "answer" ? undefined : event.to;
      const piece = nook.find((item) => item.errand === (event.kind === "assign" ? "tasks" : cue?.board ? "board" : "shelf"));
      if (who !== undefined && piece !== undefined && !reduced && seated.some((placement) => placement.id === who && placement.area === "resting")) {
        if (!visits.current.has(who)) visits.current.set(who, { piece, until: started + VISIT });
        return [];
      }
      const seat = seated.find((placement) => placement.id === event.to), from = seated.find((placement) => placement.id === event.from), to = event.to === undefined ? furniture : seat;
      // A write leaves the writer where it is drawn, walking or on an errand, not its seat.
      const origin = (event.kind === "post" ? positions.get(event.from!) : from) ?? (event.kind === "read" ? furniture : centre("tasks")), agent = cue?.agentId;
      if (to === undefined || origin === undefined || event.from !== undefined && from === undefined || kept.some((message) => message.key === event.key) || agent !== undefined && recording.has(agent)) return [];
      if (agent !== undefined) recording.add(agent);
      return [{ ...send(event, started, origin, from === undefined || seat === undefined || isPaper(event.kind) ? undefined : findRoute(layout, from, seat), Math.hypot(to.x - origin.x, to.y - origin.y)), end: seat === undefined ? to : undefined }];
    })].slice(-16);
  }, [tasks, peerQuestions, connected, knowledgeCues.map((cue) => cue.key).join(" ")]);
  const flights = at === undefined ? [] : mail.current.flatMap((message) => { const to = message.end ?? positions.get(message.to!); return to === undefined ? [] : [{ message, ...messageAt(message, to, at) }]; });
  // The sender raises a hand and says what it is as it leaves; the receiver raises one as it lands.
  const hailing = new Map<string, 0 | 1>(), calling = new Map<string, "ask" | "tell" | "hum">();
  for (const { message, hailing: who, sending, landed } of flights) {
    if (who !== undefined) hailing.set(who === "from" ? message.from! : message.to!, who === "from" ? 1 : 0);
    if (sending) calling.set(message.from!, message.kind === "ask" ? "ask" : "tell");
    else if (landed && !isPaper(message.kind)) calling.set(message.to!, "hum");
  }
  const projectOf = (questionId?: string) => tasks.find((task) => task.id === peerQuestions.find((question) => question.id === questionId)?.source_task_id)?.projectId;
  const cueOf = new Map(knowledgeCues.map((cue) => [cue.key, cue]));
  const agentOf = new Map(tasks.map((task) => [task.id, task.agentId]));
  const waiting = peerQuestions.filter((question) => !question.answered).map((question) => ({ asker: agentOf.get(question.source_task_id), asked: agentOf.get(question.target_task_id) }));
  const peerQuestionsByAgent = new Map<string, number>();
  for (const { asker, asked } of waiting) {
    if (asker !== undefined) peerQuestionsByAgent.set(asker, (peerQuestionsByAgent.get(asker) ?? 0) + 1);
    if (asked !== undefined) peerQuestionsByAgent.set(asked, (peerQuestionsByAgent.get(asked) ?? 0) + 1);
  }
  // Idle life belongs to people sitting still beside a machine with nothing to ask of anyone.
  const rows = seatRows(seated.filter((seat) => seat.area === "resting").map((seat) => {
    const id = placements.find((placement) => placement.id === seat.id && samePoint(placement, seat))?.id;
    return { x: seat.x, y: seat.y, stationId: seat.stationId, id, free: id !== undefined && positions.get(id)?.motion.action === "still" && workerById.get(id)?.activity !== "needs-you" };
  }));
  // The cat keeps to the first place two sit side by side; every such row has its talk.
  const table = tableOf(rows), bed = errands && table !== undefined ? catBed(table) : undefined;
  const cat = errands && table !== undefined ? catAt(table, at) : undefined;
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
      {cat !== undefined && cat.y !== table?.[0]?.y ? puss : null}
      {/* A question and its answer run along the floor people walk, under their feet; each opens the Board that lists it. */}
      {flights.map(({ message, point, trail }) => point === undefined || isPaper(message.kind) ? null : <g key={message.key} data-pulse={message.kind} fill={message.kind === "ask" ? "#80ddff" : "#9fe7b0"} {...(onOpenBoard === undefined ? { "aria-hidden": true, pointerEvents: "none" } : { className: "dfFactoryScene__target", "aria-label": `Direct ${message.kind === "ask" ? "question" : "answer"} · open the Board`, ...sceneAction(() => onOpenBoard(projectOf(message.subject))) })}>
        {trail.map((spot, index) => <circle key={index} cx={spot.x} cy={spot.y} r="1.5" opacity={.5 - index * .15} />)}
        <circle cx={point.x} cy={point.y} r="6" opacity=".22" /><circle cx={point.x} cy={point.y} r="2.5" />
      </g>)}
      {placements.map((told) => {
        const worker = workerById.get(told.id);
        if (worker === undefined) return null;
        const position = positions.get(told.id) ?? { ...told, placement: told, motion: { action: "still", frame: 0 } as WorkerMotion };
        // Drawn as where their motion has them, which is a render behind where they have just been told to go.
        const placement = position.placement;
        const at = placement.stationId === undefined ? undefined : labels.get(placement.stationId);
        const location = worker.location === "working"
          ? `at the machine its observed changes touch${worker.locationLabel === undefined ? "" : ` in ${worker.locationLabel}`}; ${placement.area === "work" ? "representative position, not exact file inspection" : placement.area === "outside" ? "outside the machines shown" : "worker area at capacity"}`
          : worker.location === "unobserved" ? "working; location not yet observed"
          : worker.location === "last-observed" && worker.locationLabel !== undefined ? `last observed near changes in ${worker.locationLabel}; resting area`
          : worker.paused ? "paused in resting area" : "ready in resting area";

        const attention = tasks.flatMap((order) => order.agentId === worker.id ? order.humanRequestIds : []);
        const sitting = placement.area !== "work" && placement.errand === undefined && position.motion.action !== "walking";
        const stroking = cat?.pettedBy === worker.id ? cat.pettedFor : undefined;
        const chat = talk.find(({ between }) => between.includes(worker.id));
        const said = chat === undefined ? "" : `\n${workerById.get(chat.between[0])!.name}: ${chat.remark.line}${chat.replied ? `\n${workerById.get(chat.between[1])!.name}: ${chat.remark.reply}` : ""}`;
        const frames = workerFrames(worker, position.motion, placement.area === "work" || placement.errand !== undefined ? undefined : placement.area === "resting" ? "resting" : "planning", placement.errand, stroking, hailing.get(worker.id));
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
            data-worker-route={position.stranded ? "unavailable" : undefined}
            transform={`translate(${position.x} ${position.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g aria-hidden="true" pointerEvents="none">
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              <g data-seated={sitting ? placement.area === "resting" ? "coffee" : "planning" : undefined} data-active-pose={position.motion.action === "interacting" ? position.motion.frame : undefined}><g transform={`scale(${WORKER_SIZE / FRAME})${bob === 0 ? "" : ` translate(0 ${bob})`}${facingWest ? " scale(-1 1)" : ""}`}>{frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}</g>
              </g>
            </g>
            <g role="img" className="dfFactoryScene__target" data-tooltip={`${worker.name} · ${worker.activity}${worker.review ? `\nReview assignment: ${worker.review.scope}; representative visit, not exact file inspection` : ""}\n${placement.area === "work" ? `Working at ${worker.locationLabel ?? at ?? "observed changes"}` : placement.errand !== undefined ? `${placement.errandKey?.startsWith("use ") ? "Recorded use" : "Taking a break"} · at the ${IMPLEMENT_NAMES[placement.errand]}` : placement.area === "resting" ? `${worker.paused ? "Paused · taking a break" : "Taking a break"}${stroking === undefined ? "" : " · fussing the cat"}${said}` : worker.location === "unobserved" ? "Planning · location not yet observed" : "Planning · work outside the machines shown"}${worker.telemetry === undefined ? "" : recordedEffort(worker.telemetry)}${[...new Set(asking)].join("")}`} aria-label={`${worker.name}, ${worker.review ? "reviewer" : worker.role}, ${worker.activity}, ${worker.review?.scope ?? location}`} {...sceneAction(worker.review && onSelectProposal ? () => onSelectProposal(worker.review!.proposalId) : onSelectWorker === undefined ? undefined : () => onSelectWorker(worker.id))}>
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
      {/* On the table in front of each of them: a planner's lit lamp, or the one thing a resting worker has until it is in their hand. */}
      {placements.map((told) => {
        const worker = workerById.get(told.id), position = positions.get(told.id), placement = position?.placement ?? told;
        if (worker === undefined || placement.area === "work" || placement.errand !== undefined || position?.motion.action === "walking") return null;
        if (placement.area !== "resting") return !connected ? null : <g key={placement.id} data-planning-light="" aria-hidden="true" pointerEvents="none" transform={`translate(${position?.x ?? placement.x} ${(position?.y ?? placement.y) + TABLE_DROP})`}><circle cx="7" cy="-16" r="14" fill="url(#df-lamplight)" /><path d="M12 -18v-5h-5" fill="none" stroke="#788379" strokeWidth="2" /><path d="M4 -20h6" stroke="#dfc38f" strokeWidth="3" /></g>;
        const rest = restingItem(worker, worker.activity === "needs-you" || cat?.pettedBy === worker.id || hailing.has(worker.id) ? undefined : position?.motion.at);
        return rest.where !== "table" ? null : <g key={placement.id} aria-hidden="true" pointerEvents="none" data-table-item={rest.item} transform={`translate(${position?.x ?? placement.x} ${position?.y ?? placement.y}) scale(${WORKER_SIZE / FRAME})`}>
          <Frame name={`person.held.${rest.item}.chest`} x={-8} y={-8 + SET_DOWN} />
        </g>;
      })}
      {cat !== undefined && cat.y === table?.[0]?.y ? puss : null}
      {/* What an agent just recorded flies between it and the shelf or board, then rests beside it, newest per agent. */}
      {flights.map(({ message, point }) => { const cue = cueOf.get(message.subject ?? ""); return point === undefined || cue === undefined ? null : <KnowledgeCueMark key={message.key} cue={cue} x={point.x} y={point.y} at="flight" />; })}
      {[...new Map(knowledgeCues.filter((cue) => positions.has(cue.agentId) && !flights.some(({ message, point }) => point !== undefined && message.subject === cue.key)).map((cue) => [cue.agentId, cue])).values()].map((cue) => { const position = positions.get(cue.agentId)!, using = visitor(cue.agentId); return <KnowledgeCueMark key={cue.key} cue={cue} x={position.x + (using ? 0 : -20)} y={position.y - (using ? 24 : 32)} at="agent" />; })}
      {/* The newest operation nobody walked over for is marked above the board or shelf it used, clear of its sign: one mark per event. */}
      {(["board", "shelf"] as const).map((errand) => { const cue = knowledgeCues.filter((item) => item.board === (errand === "board") && !visitor(item.agentId)).at(-1), piece = nook.find((item) => item.errand === errand); return cue === undefined || piece === undefined ? null : <KnowledgeCueMark key={errand} cue={cue} x={piece.x + WORKER_SIZE / 2} y={piece.y - 9} at={errand} />; })}
      {/* Said and felt, over everyone's heads: never words on the floor, those are in the tooltip. */}
      <g aria-hidden="true" pointerEvents="none">
        {[...calling].map(([id, glyph]) => { const position = positions.get(id); return position === undefined ? null : <g key={`call ${id}`} data-bubble={glyph} data-call={id} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${glyph}`} x={1} y={-21} /></g>; })}
        {flights.map(({ message, point }) => point === undefined || message.kind !== "assign" ? null : <g key={message.key} data-paper={message.to} transform={`translate(${point.x} ${point.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`paper.${Math.floor(at! / 120) % 2}`} x={-8} y={-8} /></g>)}
        {talk.map(({ speaking }) => { const position = speaking && positions.get(speaking.id); return !position ? null : <g key={speaking.id} data-bubble={speaking.glyph} transform={`translate(${position.x} ${position.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name={`bubble.${speaking.glyph}`} x={1} y={-21} /></g>; })}
        {cat?.pettedBy === undefined || cat.pettedFor! < 600 ? null : <g data-heart="" opacity={cat.pettedFor! > 2800 ? .5 : 1} transform={`translate(${positions.get(cat.pettedBy)!.x} ${positions.get(cat.pettedBy)!.y}) scale(${WORKER_SIZE / FRAME})`}><Frame name="heart" x={-4} y={-24 - Math.floor((cat.pettedFor! - 600) / 450)} /></g>}
      </g></>;
}

/** The rows of people resting side by side at one machine, left to right: where small talk and the cat happen. */
type Sitter = Seat & { stationId?: string };
function seatRows(seats: readonly Sitter[]) {
  const rows = new Map<string, Sitter[]>();
  for (const seat of seats) rows.set(`${seat.stationId} ${seat.y}`, [...rows.get(`${seat.stationId} ${seat.y}`) ?? [], seat]);
  return [...rows.values()].map((row) => row.sort((left, right) => left.x - right.x));
}
const tableOf = (rows: readonly (readonly Sitter[])[]) => rows.find((row) => row.length > 1);

/** The fixtures this floor offers: each opens its panel; the coffee station is scenery. */
export function offeredFixtures({ appearance = DEFAULT_FLOOR_APPEARANCE, onOpenBoard, onOpenMissions, onOpenTasks, onOpenLibrary }: Pick<FactorySceneProps, "appearance" | "onOpenBoard" | "onOpenMissions" | "onOpenTasks" | "onOpenLibrary">): readonly BreakRoomErrand[] {
  return ([["board", onOpenBoard], ["missions", onOpenMissions], ["tasks", onOpenTasks], ["shelf", onOpenLibrary], ["coffee", appearance.scenery === "off" ? undefined : true]] as const).flatMap(([errand, offer]) => offer === undefined ? [] : [errand]);
}

/** A disposable SVG projection of the operational world and current factory state. */
export function FactoryScene({ proposals, crates, tools, onLoadNode, onInvestigate, onDiscussSource, requestedEntity, onSelectEntity, onOpenLibrary, onOpenBoard, graph, workers, appearance = DEFAULT_FLOOR_APPEARANCE, selectedWorkerId, onSelectWorker, tasks = [], peerQuestions = NO_QUESTIONS, knowledgeCues = NO_CUES, selectedTaskId, onSelectTask, onOpenTasks, onOpenMissions, onSelectHumanRequest, projectId, connected = true, reading = false }: FactorySceneProps) {
  const [selectedId, setSelectedId] = useState<string>();
  // The crate pointed at or focused: the machines its change touches are lit.
  const [lit, setLit] = useState<string>();
  const litIds = new Set(proposals?.items.find((proposal) => proposal.id === lit)?.operations.map((operation) => operation.entityId));
  const [search, setSearch] = useState("");
  const mapElement = useRef<HTMLDivElement>(null);
  const floorElement = useRef<SVGSVGElement>(null);
  // Scene pixels per scene unit; undefined fits the whole floor to its pane.
  const [zoom, setZoom] = useState<number>();
  // Past fit, the overview can be folded to a button; the choice is this browser's.
  const [mapHidden, setMapHiddenState] = useState(() => { try { return typeof localStorage !== "undefined" && localStorage.getItem(OVERVIEW_KEY) === "hidden"; } catch { return false; } });
  const setMapHidden = (hidden: boolean) => { setMapHiddenState(hidden); try { localStorage.setItem(OVERVIEW_KEY, hidden ? "hidden" : "shown"); } catch { /* blocked storage: the choice lasts this visit */ } };
  // Fitted, the whole floor is in view, never enlarged into a poster. The pane's size is set by CSS, never by the floor inside it, so fitting cannot feed back into it.
  const [pane, setPane] = useState<{ width: number; height: number }>();
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
  // Structure alone lays the floor out; a structural change keeps what still exists where it stood. Readings, workers, zoom and the pane never do.
  // Machinery and fixtures from structure alone; which fixtures are offered never moves anything.
  const previousFloor = useRef<SceneMachinery>(undefined);
  const machinery = useMemo(() => previousFloor.current = placeMachinery(graph, previousFloor.current), [graph.digest]);
  const offered = offeredFixtures({ appearance, onOpenBoard, onOpenMissions, onOpenTasks, onOpenLibrary });
  const layout = useMemo(() => furnish(machinery, offered), [machinery, offered.join()]);
  const placements = useMemo(() => placeWorkers(layout, workers), [layout, workers]);
  // Live readings change without moving anything: they are looked up by id at draw time.
  const machines = new Map<string, SceneMachine>([...graph.units.flatMap((unit) => [[unit.id, { id: unit.id, kind: "processor", label: unit.label, reading: unit.reading }] as const, ...unit.machines.map((machine) => [machine.id, machine] as const)]),
    ...[...graph.shared, ...graph.parties, ...graph.quarantine].map((machine) => [machine.id, machine] as const)]);
  const unitOf = new Map(graph.units.flatMap((unit) => [[unit.id, unit] as const, ...unit.machines.map((machine) => [machine.id, unit] as const)]));
  const labels = new Map([...machines].map(([id, machine]) => [id, machine.label] as const));
  const focusEntity = (id: string) => { selectEntity(id); const station = stationOf(layout, id); if (station) Array.from(mapElement.current?.querySelectorAll("[data-entity-id]") ?? []).find((element) => element.getAttribute("data-entity-id") === station.entityId)?.scrollIntoView({ block: "center", inline: "center", behavior: "instant" }); };
  useEffect(() => { if (requestedEntity) { setSearch(""); focusEntity(requestedEntity.id); } }, [requestedEntity]);
  const nook = breakRoomNook(layout);
  const sceneWidth = layout.width;
  // A crowd beside one machine stands below it, past the floor's edge: the scene grows, the floor does not move.
  const sceneHeight = Math.max(layout.height, ...placements.map((placement) => placement.y + 24 + PADDING));
  const fit = pane === undefined ? undefined : Math.min(1, pane.width / sceneWidth, pane.height / sceneHeight);
  // Zoomed to twice the fitted scale or more, a manifold lists its routes inside its own footprint.
  const close = zoom !== undefined && zoom >= 2 * (fit ?? 1);
  // Belt runs depend on where machines stand and which flows exist; readings are joined at draw time.
  const flowKey = graph.flows.map((flow) => `${flow.from} ${flow.to}`).join(",");
  const runs = useMemo(() => beltRuns(machinery, graph), [machinery, flowKey]);
  const flowAt = new Map(graph.flows.map((flow) => [`${flow.from} ${flow.to}`, flow]));
  const belts = runs.runs.flatMap((run): BeltRoute[] => {
    const flow = flowAt.get(run.key), reading = run.kind === "intake" ? unitOf.get(run.to)?.reading : flow?.reading;
    return reading === undefined || run.kind === "intake" && reading.state === "unknown" ? [] : [{ ...run, reading, kind: run.kind === "intake" ? "intake" : flow!.kind }];
  });
  // Selecting a unit's main machine lights every machine it owns and every belt touching them, wherever they stand.
  const litUnit = selectedId !== undefined && unitOf.get(selectedId)?.id === selectedId ? selectedId : undefined;
  const members = new Set(litUnit === undefined ? [] : layout.stations.filter((station) => station.unit === litUnit).map((station) => station.entityId));
  const queued = tasks.filter((order) => order.status === "queued").length;
  // The implements are always there: each opens its panel and the tray shows the queue. Coffee is scenery only.
  const implementsShown = nook.furniture;
  const opens: Partial<Record<BreakRoomErrand, readonly [((projectId?: string) => void) | undefined, string, string, string]>> = {
    board: [onOpenBoard, "Discussion board", "Open discussion board", "BOARD"],
    missions: [onOpenMissions, "Missions · inspect objectives", "Open Missions", "MISSIONS"],
    tasks: [onOpenTasks, queued === 0 ? "Tasks · queue is empty" : `Tasks · ${queued} queued`, "Open Tasks", "TASKS"],
    shelf: [onOpenLibrary, "Library · documents", "Open project library", "LIBRARY"],
  };
  const animate = connected && appearance.animation !== "off";
  // Changeovers are dated against the graph's own observation time, never the viewer's clock.
  const observedAt = graph.observedAt ?? 0;
  // A folded member is inspected through its fold, with that member chosen.
  const fold = selectedId === undefined || machines.has(selectedId) ? undefined : [...machines.values()].find((machine) => machine.represented?.includes(selectedId));
  const selected = selectedId === undefined ? undefined : machines.get(selectedId) ?? fold;
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
    const x = px ?? at.pane.width / 2, y = py ?? at.pane.height / 2;
    const scaled = Math.min(MAX_ZOOM, at.scale * factor), next = scaled <= (fit ?? 0) * 1.01 ? undefined : scaled;
    // A zoom that changes nothing leaves no anchor for a later change to apply.
    if (next === zoom) return;
    anchor.current = { x: (at.pane.left + x - at.box.left) / at.scale, y: (at.pane.top + y - at.box.top) / at.scale, px: x, py: y };
    setZoom(next);
  };
  // The wheel listener outlives renders, so it zooms through whichever zoomBy saw the latest zoom.
  const zoomer = useRef(zoomBy);
  zoomer.current = zoomBy;
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
  }, [zoom, pane, sceneWidth, sceneHeight]);
  useLayoutEffect(() => {
    const map = mapElement.current;
    if (!map) return;
    // A hidden (zero-sized) pane keeps the last fit.
    const size = () => { const width = map.clientWidth, height = map.clientHeight; if (width > 0 && height > 0) setPane((old) => old?.width === width && old.height === height ? old : { width, height }); };
    size();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(size);
    observer.observe(map);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const map = mapElement.current;
    if (!map || typeof window === "undefined") return;
    // Pinch on a trackpad arrives as a ctrl-wheel; it must not zoom the page.
    const wheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      const pane = map.getBoundingClientRect();
      zoomer.current(Math.exp(-event.deltaY / 200), event.clientX - pane.left, event.clientY - pane.top);
    };
    map.addEventListener?.("wheel", wheel, { passive: false });
    return () => map.removeEventListener?.("wheel", wheel);
  }, []);
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
    <div ref={mapElement} className="dfFactoryFloor__map" onClick={inspect} onPointerOver={inspect} onFocus={inspect} onPointerLeave={retainFocusedTooltip} onBlur={() => setTooltip(undefined)} onScroll={() => { retainFocusedTooltip(); measure(); }} onKeyDown={(event) => { if (event.key === "Escape") setTooltip(undefined); }} role="region" aria-label="Scrollable factory floor" tabIndex={0} style={zoom === undefined ? { overflow: "hidden" } : undefined}>
    <svg
      ref={floorElement}
      viewBox={`0 0 ${sceneWidth} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory operational floor"
      data-graph-digest={graph.digest}
      className={animate ? "dfPlant dfPlant--moving" : "dfPlant"}
      style={(zoom ?? fit) === undefined ? { display: "block", width: "100%", maxWidth: sceneWidth, height: "auto", margin: "0 auto" } : { display: "block", width: Math.floor(sceneWidth * (zoom ?? fit)!), height: Math.floor(sceneHeight * (zoom ?? fit)!), margin: "0 auto" }}
    >
      <desc>{`${graph.units.length} units, ${graph.shared.length} shared stores, ${graph.parties.length} external parties, ${graph.quarantine.length} unexplained runtime paths, ${workers.length} workers`}</desc>
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
        <pattern id="df-fog" patternUnits="userSpaceOnUse" width="6" height="6" patternTransform="rotate(45)"><rect width="6" height="6" fill="#2c3139" /><path d="M0 0v6" stroke="#4a515b" strokeWidth="2" /></pattern>
        <radialGradient id="df-lamplight" cx="50%" cy="45%" r="60%">
          <stop offset="0" stopColor="#f2dab0" stopOpacity=".24" />
          <stop offset="1" stopColor="#f2dab0" stopOpacity="0" />
        </radialGradient>
      </defs>
      {/* One floor: every machine stands on it and every worker walks it. */}
      <rect width={sceneWidth} height={sceneHeight} fill="url(#df-floor)" />
      {/* Areas: one faint patch per neighbourhood of a unit's machines, named once. Never a wall, never a hit target. */}
      <g data-regions="" aria-hidden="true" pointerEvents="none">{layout.regions.map((region) => <g key={`${region.unit} ${region.lobe}`} data-region={region.unit} fill={tint(region.hue)}>
        {region.rects.map((rect, index) => <rect key={index} {...rect} />)}
        <path d={region.outline} className="dfPlant__regionEdge" stroke={tint(region.hue, .4)} />
        <text data-region-label={region.unit} x={region.label.x} y={region.label.y} className="dfPlant__regionLabel" fontSize="7">{shortLabel(unitOf.get(region.unit)?.label ?? "", Math.max(4, Math.floor(region.label.width / 5)))}</text>
      </g>)}</g>
      {/* Development: the outbound line, apart from the running system. */}
      <rect data-development="" x={layout.facilities.x + 8} y={layout.facilities.y + 8} width={layout.facilities.width - 16} height={layout.facilities.height - 16} rx="6" className="dfPlant__development" />
      <text x={layout.facilities.x + MARGIN} y={layout.facilities.y + 6} className="dfPlant__regionLabel" fontSize="7">DEVELOPMENT</text>

      <g aria-hidden="true" pointerEvents="none">
        {belts.map((belt) => <Belt key={belt.key} belt={belt} lit={members.has(belt.from) || members.has(belt.to)} />)}
        {/* Where belts share a port they join, a dot; where they cross, always square-on, one bridges the other. */}
        {[...junctions(belts), ...runs.junctions].map((point) => <circle key={`${point.x} ${point.y}`} data-junction="" cx={point.x} cy={point.y} r="2.5" className="dfPlant__junction" />)}
        {runs.crossings.map((crossing) => <g key={`${crossing.x} ${crossing.y}`} data-crossing={crossing.over} transform={`translate(${crossing.x} ${crossing.y})${crossing.over === "v" ? " rotate(90)" : ""}`}>
          <rect x="-7" y="-6" width="14" height="12" className="dfPlant__bridgeDeck" /><path d="M-8 0h16" className="b-base" /><path d="M-8 -5h16M-8 5h16" className="dfPlant__bridgeRail" />
        </g>)}
      </g>

      {layout.stations.map((station) => {
        const machine = machines.get(station.entityId) ?? station.machine, unit = unitOf.get(station.entityId), deployed = station.unit === station.entityId ? unit?.reading.deployedAt : undefined;
        return <g key={station.entityId} data-entity-id={station.entityId} data-observation={machine.reading.observation} data-state={machine.reading.state} data-shape={station.shape} data-unit={station.unit}
          data-tooltip={machineInfo(machine, unit)}
          {...sceneAction(() => selectEntity(station.entityId))} aria-label={`Inspect ${machine.label}`} className="dfFactoryScene__target">
          <rect className="dfFactoryScene__focus" x={station.x - 4} y={station.y - 14} width={station.shape === "gate" ? 130 : station.width + 8} height={station.height + 18} fill="transparent" />
          <Station item={station} machine={machine} selected={selectedId === station.entityId || selectedId !== undefined && station.representedIds?.includes(selectedId) === true} close={close} />
          {/* A recent deploy is a quiet tag on the machine, not a warning. */}
          {deployed === undefined || observedAt - deployed > DAY ? null : <text data-changeover={deployed} x={station.x + 4} y={station.y + 10} className="dfPlant__small" fontSize="7">deployed</text>}
          {members.has(station.entityId) && station.entityId !== litUnit ? <rect data-unit-member={litUnit} className="dfFactoryScene__selection" x={station.x - 4} y={station.y - 4} width={station.width + 8} height={station.height + 8} /> : null}
          {litIds.has(station.entityId) ? <rect data-lit="" className="dfFactoryScene__selection" x={station.x - 6} y={station.y - 6} width={station.width + 12} height={station.height + 12} /> : null}
        </g>;
      })}

      <WorkLine layout={layout} crates={crates} tasks={tasks} placements={placements} connected={connected} live={animate} onLight={setLit} onSelect={proposals?.onSelect} />

      {implementsShown.map((piece) => { const [open, tooltip, label, sign] = opens[piece.errand] ?? []; return <g key={piece.key} data-break-room={piece.errand} data-floor-inbox={piece.errand === "tasks" ? queued : undefined}
        {...(open === undefined ? {} : { className: "dfFactoryScene__target", "data-tooltip": tooltip, "aria-label": label, ...sceneAction(() => open(projectId)) })} transform={`translate(${piece.x} ${piece.y}) scale(${WORKER_SIZE / FRAME})`}>
        {open === undefined ? null : <rect className="dfFactoryScene__focus" x="-2" y="-2" width="20" height="28" fill="transparent" />}
        <g aria-hidden="true" pointerEvents="none" opacity=".8">
          {piece.errand === "missions" ? <g><rect x="0" y="7" width="16" height="5" fill="#455c5e" stroke="#8c8871" strokeWidth=".5" /><rect x=".5" y="7.5" width="15" height="3" fill="#9fae9e" /><path d="M2 8.5h7v1.5H5 M11 9h2" fill="none" stroke="#536e70" strokeWidth=".5" /><path d="M2 12v4 M14 12v4" stroke="#393f3c" strokeWidth="1.5" /></g>
            : piece.errand === "tasks" ? <g><rect x="0" y="9" width="16" height="4" fill="#5b5545" stroke="#a08f68" strokeWidth=".5" /><path d="M2 13v3 M14 13v3" stroke="#393b35" strokeWidth="1.5" />
              {/* Waiting work, as the pile in a real tray: it says how the queue is doing. */}
              {[...Array(Math.min(queued, 3))].map((_, index) => <rect key={index} x="4" y={7 - index * 1.5} width="8" height="1.5" fill="#e4dcc0" strokeWidth=".4" stroke={tasks.some((task) => task.id === selectedTaskId && task.status === "queued") ? "#80ddff" : "#a6a087"} />)}
              <path d="M3 7v2h10v-2" fill="none" stroke="#c2b184" /></g>
            : <Frame name={piece.errand === "shelf" ? "prop.bookshelf" : piece.errand === "board" ? "prop.board" : "prop.coffeestation"} x={0} y={0} />}
          {sign === undefined ? <path d="M2 15v3 M14 15v3" stroke="#303b3b" strokeWidth="2" /> : <text x="8" y="23" textAnchor="middle" fill="#d4ddd2" fontSize="4">{sign}</text>}
        </g>
      </g>; })}
      {layout.stations.length === 0 ? <text x={PADDING} y="24" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">{reading ? "READING THE PLANT…" : "NO OPERATIONAL STRUCTURE INFERRED YET"}</text> : null}

      {placements.filter((placement) => placement.area !== "work").map((seat) => <g key={seat.id} data-nearby-rest={seat.stationId} aria-hidden="true"><rect x={seat.x - 12} y={seat.y + 5} width="24" height="7" fill="#655948" stroke="#9b8b6b" /><path d={`M${seat.x - 8} ${seat.y + 12}v5m16-5v5`} stroke="#74664e" strokeWidth="3" /></g>)}
      <SceneWorkers knowledgeCues={knowledgeCues} nook={implementsShown} onOpenBoard={onOpenBoard} errands={appearance.scenery !== "off"} peerQuestions={peerQuestions} layout={layout} placements={placements} labels={labels} workers={workers} tasks={tasks} connected={connected} animate={appearance.animation !== "off"} selectedWorkerId={selectedWorkerId} onSelectWorker={onSelectWorker} onSelectTask={onSelectTask} onSelectHumanRequest={onSelectHumanRequest} onSelectProposal={proposals?.onSelect} />
    </svg>
    {tooltip === undefined ? null : <div ref={tooltipElement} className="dfFactoryTooltip" role="tooltip" style={{ left: tooltip.x, top: tooltip.y, maxHeight: tooltip.room }}>{tooltip.text}</div>}
    </div>
    {layout.stations.length === 0 ? null : <div className="dfPlantOverview">
      <div className="dfPlantOverview__zoom" role="group" aria-label="Zoom">
        <IconButton icon="minus" aria-label="Zoom out" disabled={zoom === undefined} onClick={() => zoomBy(1 / 1.5)} />
        <IconButton icon="plus" aria-label="Zoom in" disabled={zoom !== undefined && zoom >= MAX_ZOOM} onClick={() => zoomBy(1.5)} />
        <IconButton icon="fit" aria-label="Fit floor" disabled={zoom === undefined} onClick={() => setZoom(undefined)} />
        {zoom === undefined ? null : <button type="button" className="dfPlantOverview__toggle" aria-pressed={!mapHidden} aria-label={mapHidden ? "Show overview" : "Hide overview"} onClick={() => setMapHidden(!mapHidden)}>{mapHidden ? "Map" : "×"}</button>}
      </div>
      {zoom === undefined || mapHidden ? null : <PlantMap layout={layout} width={sceneWidth} height={sceneHeight} machines={machines} graph={graph} placements={placements} workers={workers} viewElement={viewElement} onCentre={centreOn} pane={pane} />}
    </div>}
    </div>
    {selected === undefined ? null : <MachineInspector key={selectedId} machine={selected} chosen={fold === undefined ? undefined : fold.represented!.indexOf(selectedId!)} unit={unitOf.get(selected.id)} onLoadNode={onLoadNode} onInvestigate={onInvestigate}
      proposals={proposals === undefined ? [] : proposalsForEntity(proposals.items, selected.id)} onSelectProposal={proposals?.onSelect}
      onFocus={() => focusEntity(selected.id)} onDiscuss={onDiscussSource === undefined ? undefined : () => onDiscussSource(selected.id)} onClose={() => selectEntity(undefined)} />}
    </>
  );
}

const MAX_ZOOM = 4;
const OVERVIEW_KEY = "dfFloorOverview";

/**
 * Change requests on the outbound line. A crate stands where its
 * records put it and moves only when they change: once along the line, or,
 * when just opened, from the agent whose task opened it. Reconnecting is a
 * first look, never a replay.
 */
function WorkLine({ layout, crates, tasks, placements, connected, live, onLight, onSelect }: {
  layout: SceneLayout; crates: readonly SceneCrate[] | undefined; tasks: readonly SceneTask[]; placements: ReturnType<typeof placeWorkers>;
  connected: boolean; live: boolean; onLight: (id: string | undefined) => void; onSelect?: (id: string) => void;
}) {
  const placed = useMemo(() => placeCrates(layout, crates ?? []), [layout, crates]);
  const reduced = useReducedMotion();
  const moving = live && !reduced;
  const seen = useRef<ReadonlyMap<string, SceneCrate["station"]>>(undefined);
  const resting = useRef(new Map<string, ScenePoint>());
  const flights = useRef<readonly FloorMessage[]>([]);
  const [clock, setClock] = useState(0);
  useEffect(() => {
    // Records not yet read on this connection are no look at all: the first read is history, never news.
    if (!connected || crates === undefined) { seen.current = undefined; flights.current = []; return; }
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
    // A new snapshot may land in the same millisecond as the last, so it asks for frames itself.
  }, [clock, moving, placed]);
  // The line is always there: one row and one belt per station, top to bottom.
  const belt = (station: SceneLayout["line"][number]) => station.y + 19;
  return <g data-work-line="">
    <path aria-hidden="true" d={layout.line.map((station) => `M${station.x} ${belt(station)}H${station.x + station.width}`).join("")} className="b-base" />
    {layout.line.map((station, index) => { const count = (crates ?? []).filter((crate) => crate.station === index).length; return <g key={station.label} aria-hidden="true">
      <rect x={station.x} y={station.y} width="2" height={station.height} className="md" />
      <text x={station.x + 6} y={station.y + 9} className="dfPlant__label" fontSize="8">{station.label.toUpperCase()} · {count}</text>
      {count > LINE_SLOTS ? <text data-crate-overflow={count - LINE_SLOTS} x={station.x + 10 + LINE_SLOTS * 18} y={belt(station)} className="dfPlant__small" fontSize="7">+{count - LINE_SLOTS}</text> : null}
    </g>; })}
    {placed.filter((spot) => spot.shown).map(({ crate, x, y }) => {
      const flight = moving ? flights.current.find((candidate) => candidate.key === crate.id) : undefined;
      const point = (flight === undefined ? undefined : messageAt(flight, { x, y }, clock).point) ?? { x, y };
      return <g key={crate.id} data-crate={crate.id} data-crate-station={crate.station} data-fault={crate.fault ? "" : undefined} className="dfFactoryScene__target"
        data-tooltip={crate.number > 0 ? `PR #${crate.number} · ${crate.title}\n${layout.line[crate.station]!.label}${crate.stage ? ` · ${crate.stage}` : ""}` : `A change request\n${layout.line[crate.station]!.label}${crate.fault ? " · needs correction" : ""}`}
        aria-label={crate.number > 0 ? `PR #${crate.number} ${crate.title}: ${crate.stage || layout.line[crate.station]!.label}` : `A change request: ${layout.line[crate.station]!.label}${crate.fault ? ", needs correction" : ""}`}
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
 * The plant at a glance once zoomed past fit: machines painted by coverage,
 * areas, where workers are and the window the floor shows. Pointing at it
 * moves the floor there. On a phone-width pane it is a quarter of the pane.
 */
function PlantMap({ layout, width, height, machines, graph, placements, workers, viewElement, onCentre, pane }: {
  layout: SceneLayout; width: number; height: number; machines: ReadonlyMap<string, SceneMachine>; graph: SceneGraph;
  placements: ReturnType<typeof placeWorkers>; workers: readonly SceneWorker[]; viewElement: RefObject<SVGRectElement | null>; onCentre: (x: number, y: number) => void;
  pane?: { width: number; height: number };
}) {
  const go = (event: PointerEvent<SVGSVGElement>) => {
    if (event.type === "pointermove" && event.buttons !== 1) return;
    const box = event.currentTarget.getBoundingClientRect();
    onCentre((event.clientX - box.left) / box.width * width, (event.clientY - box.top) / box.height * height);
  };
  const needs = new Set(workers.filter((worker) => worker.activity === "needs-you").map((worker) => worker.id));
  const units = graph.units.length;
  // At most 12rem wide or 10rem tall, whichever the plant's shape reaches first; on a narrow pane, a quarter of it.
  const narrow = pane !== undefined && pane.width < 600;
  return <svg className="dfPlantMap" viewBox={`0 0 ${width} ${height}`} style={{ width: narrow ? `${Math.round(Math.min(pane.width / 4, 0.4 * pane.height * width / height))}px` : `min(12rem, ${(10 * width / height).toFixed(2)}rem)` }} role="img" aria-label={`Plant overview: ${units} ${units === 1 ? "unit" : "units"}, ${graph.parties.length} external, ${workers.length} workers`}
    onPointerDown={go} onPointerMove={go}>
    <rect width={width} height={height} className="dfPlantMap__ground" />
    {layout.regions.map((region) => <g key={`${region.unit} ${region.lobe}`} fill={tint(region.hue)}>{region.rects.map((rect, index) => <rect key={index} {...rect} />)}</g>)}
    <rect {...layout.facilities} className="dfPlantMap__development" />
    {layout.stations.map((station) => {
      const reading = (machines.get(station.entityId) ?? station.machine).reading;
      return <rect key={station.entityId} data-overview-station={station.entityId} x={station.x} y={station.y} width={station.width} height={station.height}
        className={`dfPlantMap__station${station.shape === "gate" ? " dfPlantMap__station--external" : ""} s-${reading.observation} op-${reading.state}`} />;
    })}
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

// What a unit's runtime is, in words that never read as the factory's own workers.
const RUNTIME_TEXT: Record<NonNullable<SceneUnit["runtime"]>, string> = {
  browser: "browser", server: "server", worker: "edge", process: "daemon", cli: "cli", ci: "ci",
};
const OBSERVATION_TEXT: Record<SceneReading["observation"], string> = {
  observed: "Observed", quiet: "Observed, quiet", partial: "Partly observed", stale: "Stale: no current reading", unobserved: "No telemetry: activity unknown", opaque: "External: its own state cannot be seen",
};
const EVIDENCE_TEXT: Record<SceneReading["evidence"], string> = {
  static: "Inferred from code", runtime: "Seen at runtime only; not in code", both: "In code and seen at runtime", uncertain: "Inferred from a name or literal", contradicted: "Runtime evidence contradicts the code",
};

/** What a machine is, what we know of it and how. Never claims more than the reading does. */
function machineInfo(machine: SceneMachine, unit?: SceneUnit) {
  const reading = machine.reading;
  const deployed = reading.deployedAt === undefined ? "" : `\nDeployed ${new Date(reading.deployedAt).toLocaleString([], { dateStyle: "short", timeStyle: "short" })}`;
  const activity = reading.state === "unknown" ? "" : reading.state === "idle" ? " · idle" : ` · ${reading.state} · ${rate(reading.ratePerHour)}${reading.errorPermille > 0 ? ` · ${reading.errorPermille / 10}% errors` : ""}${reading.latencyMs > 0 ? ` · p95 ${reading.latencyMs} ms` : ""}`;
  return `${machine.label}\n${kindText(machine, unit)}${unit !== undefined && unit.id !== machine.id ? ` in ${unit.label}` : machine.owner ? ` from ${machine.owner}` : ""}\n${OBSERVATION_TEXT[reading.observation]}${activity}\n${EVIDENCE_TEXT[reading.evidence]}${deployed}`;
}

function Coverage({ summary }: { summary: NonNullable<SceneGraph["summary"]> }) {
  return <p className="dfPlantCoverage" role="status" aria-label="Observation coverage">
    {(["observed", "quiet", "partial", "stale", "unobserved", "opaque"] as const).map((key, index) => <span key={key}>{index === 0 ? "" : " · "}<strong className={`dfPlantCoverage--${key}`}>{summary[key]}</strong> {OBSERVATION_TEXT[key].split(":")[0]!.toLowerCase()}</span>)}
    <small>{summary.runtime_only > 0 ? ` · ${summary.runtime_only} runtime-only` : ""}{summary.contradicted > 0 ? ` · ${summary.contradicted} contradicted` : ""}{summary.unobserved > 0 ? " · grey machines have no telemetry: unknown, not idle" : ""}</small>
  </p>;
}

/** One machine, pictured by its kind; its observation and state are its paint, lamp and motion. */
function Station({ item, machine, selected, close }: { item: SceneStation; machine: SceneMachine; selected: boolean; close: boolean }) {
  const { x, y, width: w, height: h } = item, reading = machine.reading;
  // A cell's body is its base; every other station carries its lamp at the top.
  const ly = item.shape === "cell" ? y + h - 7 : y + 3;
  const lamp = <g><rect x={x + w - 9} y={ly} width="3" height="4" className="lamp" /><rect x={x + w - 6} y={ly} width="3" height="4" className="lamp l2" /></g>;
  const scrap = reading.errorPermille > 0 && (reading.observation === "observed" || reading.observation === "partial")
    ? <g data-scrap={reading.errorPermille}><rect x={x + w + 2} y={y + h - 14} width="10" height="14" className="dfPlant__bin" /><rect x={x + w + 4} y={y + h - 2 - Math.max(1, Math.round(10 * reading.errorPermille / 1000))} width="6" height={Math.max(1, Math.round(10 * reading.errorPermille / 1000))} className="scrap" /></g> : null;
  // Latency is dwell: a slow machine holds more work in its window.
  const held = reading.state === "unknown" || reading.state === "idle" ? 0 : Math.min(4, 1 + Math.floor(Math.log2(1 + reading.latencyMs / 50)));
  const window = (wx: number, wy: number, ww: number, wh: number) => <g><rect x={wx} y={wy} width={ww} height={wh} className="win" />
    {Array.from({ length: held }, (_, index) => <rect key={index} x={wx + 3 + index * 6} y={wy + wh / 2 - 2} width="4" height="4" className="crate dfPlant__dwell" />)}</g>;
  const plaque = reading.observation === "unobserved" && w >= 48 ? <text x={x + w / 2} y={y + h / 2 + 3} textAnchor="middle" className="bptext" fontSize="6">NO TELEMETRY</text> : null;
  const stale = reading.observation === "stale" ? <text x={x} y={y + h + 9} className="dfPlant__small" fontSize="7">last seen {reading.lastSeen === undefined ? "earlier" : new Date(reading.lastSeen).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</text> : null;
  const tag = reading.evidence === "runtime" ? <g><rect x={x - 5} y={y - 6} width="10" height="11" className="qtag" /><text x={x} y={y + 3} textAnchor="middle" className="qmark" fontSize="8">?</text></g>
    : reading.evidence === "contradicted" ? <rect x={x - 4} y={y - 4} width="8" height="8" className="redtag" /> : null;
  // Docks are labelled above, a gate beside it, everything else under its own footprint; layout reserved exactly this much.
  const label = item.shape === "gate" ? null : item.shape === "dock" || item.shape === "manifold"
    ? <text x={x} y={y - 3} className="dfPlant__label" fontSize="8">{shortLabel(machine.label, labelChars(item.shape, w))}</text>
    : <text x={x + w / 2} y={y + h + 9} textAnchor="middle" className="dfPlant__label" fontSize="8">{shortLabel(machine.label, labelChars(item.shape, w))}</text>;
  let body: ReactNode;
  switch (item.shape) {
    case "line": body = <g><rect x={x + 3} y={y + h} width={w - 2} height="3" className="shd" /><rect x={x} y={y} width={w} height={h} className="m" /><rect x={x + 1} y={y + 1} width={w - 2} height="4" className="mh" />
      {window(x + 8, y + 12, w - 30, h - 22)}<rect x={x + w - 18} y={y + 12} width="10" height={h - 22} className="md" />{lamp}</g>; break;
    case "dock": body = <g><rect x={x} y={y} width={w} height={h} className="dock" />{[0, 1, 2].map((index) => <rect key={index} x={x + 3 + index * 6} y={y + h - 5} width="4" height="3" className="chev" />)}<rect x={x + w - 14} y={y + 4} width="10" height={h - 8} className="win" />{lamp}</g>; break;
    case "manifold": {
      const routes = machine.routes ?? [], rows = Math.floor((h - 4) / 5);
      const lines = routes.length > rows ? [...routes.slice(0, rows - 1).map((route) => shortLabel(route, 14)), `+${routes.length - rows + 1} more`] : routes.map((route) => shortLabel(route, 14));
      body = <g><rect x={x} y={y} width={w} height={h} className="m" />{close && lines.length > 0
        ? <g data-manifold-routes="">{lines.map((line, index) => <text key={index} x={x + 3} y={y + 7 + index * 5} className="dfPlant__small" fontSize="4">{line}</text>)}</g>
        : [0, 1, 2, 3].map((index) => <rect key={index} x={x + 4} y={y + 5 + index * 8} width={w - 18} height="4" className="dock" />)}{lamp}</g>; break;
    }
    case "clock": body = <g><circle cx={x + w / 2} cy={y + h / 2} r={w / 2 - 1} className="m" /><circle cx={x + w / 2} cy={y + h / 2} r={w / 2 - 4} className="win" /><path d={`M${x + w / 2} ${y + h / 2}v-6m0 6h5`} className="dfPlant__hand" /></g>; break;
    case "cell": body = <g><rect x={x} y={y + h - 10} width={w} height="10" className="m" /><rect x={x + w / 2 - 3} y={y + 4} width="6" height={h - 14} className="md arm" /><rect x={x + w / 2 - 8} y={y + 2} width="16" height="5" className="mh arm" />{lamp}</g>; break;
    case "silo": body = <g><rect x={x + 2} y={y + h} width={w - 2} height="3" className="shd" /><rect x={x + 3} y={y} width={w - 6} height={h} className="m" /><rect x={x} y={y + 3} width={w} height={h - 6} className="m" /><rect x={x + 3} y={y + 2} width={w - 6} height="3" className="mh" />{window(x + 6, y + 10, w - 12, 8)}{lamp}</g>; break;
    case "conveyor": body = <g><path d={`M${x} ${y + h / 2}h${w}`} className="b-base" />{window(x + 4, y + 2, w - 8, h - 4)}</g>; break;
    // An outside party: a gate whose inside cannot be seen, labelled beside it with how much of it is.
    case "gate": body = <g><rect x={x} y={y} width={w} height={h} className={reading.evidence === "runtime" ? "dfPlant__gate dfPlant__gate--runtime" : "dfPlant__gate"} />
      <rect x={x + 2} y={y + h / 2 - 2} width="3" height="4" className="lamp" />
      <text x={x + w + 6} y={y + 12} className="dfPlant__label" fontSize="8">{shortLabel(machine.label, labelChars("gate", w))}</text>
      <text x={x + w + 6} y={y + 23} className="dfPlant__small" fontSize="7">{reading.evidence === "runtime" ? "seen, not in code" : reading.ratePerHour > 0 ? `${rate(reading.ratePerHour)} · measured from our side` : "outside · can't see inside"}</text></g>; break;
    default: body = <g><rect x={x} y={y} width={w} height={h} className="crate" /><text x={x + w / 2} y={y + h / 2 + 3} textAnchor="middle" className="qmark" fontSize="8">?</text></g>;
  }
  return <g className={`s-${reading.observation} op-${reading.state}${selected ? " dfPlant--selected" : ""}`}>{label}{body}{plaque}{scrap}{stale}{tag}</g>;
}

type BeltRun = Readonly<{ key: string; from: string; to: string; kind: string; points: readonly ScenePoint[]; link: boolean; stubs?: readonly (BeltStub | undefined)[] }>;
type BeltRoute = BeltRun & Readonly<{ reading: SceneReading }>;

/**
 * Material moves only where a flow was observed: belt density reads as rate
 * and speed is fixed. An edge nobody can see is a dashed blueprint run, never
 * an empty belt. Every belt lies on a dark casing, so where two cross one
 * plainly passes over the other.
 */
function Belt({ belt, lit }: { belt: BeltRoute; lit: boolean }) {
  const d = `M${belt.points.map((point) => `${point.x} ${point.y}`).join(" L")}`;
  const reading = belt.reading, casing = <path d={d} className={lit ? "dfPlant__casing dfPlant__casing--lit" : "dfPlant__casing"} />;
  // A connection the belts could not lay is an overhead link: faint, dashed, round machines and labels, never a belt.
  if (belt.stubs !== undefined) return <g data-belt={belt.kind} data-stub="" data-observation={reading.observation}>{belt.stubs.map((stub, index) => stub === undefined ? null : <g key={index}>
    <title>{`Connected to ${stub.name}`}</title>
    <path d={`M${stub.points.map((point) => `${point.x} ${point.y}`).join(" L")}`} className={lit ? "dfPlant__link dfPlant__link--lit" : "dfPlant__link"} />
    {stub.text === undefined ? null : <text data-stub-name="" x={stub.text.x} y={stub.text.y + 6} className="dfPlant__small" fontSize="6">{stub.text.value}</text>}
  </g>)}</g>;
  if (belt.link) return <path d={d} className={lit ? "dfPlant__link dfPlant__link--lit" : "dfPlant__link"} data-belt={belt.kind} data-link="" data-observation={reading.observation} />;
  if (reading.observation === "unobserved" || reading.observation === "stale" || reading.observation === "opaque" && reading.ratePerHour === 0 || reading.observation === "partial" && reading.state === "unknown") {
    return <g data-belt={belt.kind} data-observation={reading.observation}>{casing}<path d={d} className="b-inf" /></g>;
  }
  const spacing = reading.ratePerHour === 0 ? 0 : Math.max(5, Math.round(48 / Math.log2(2 + reading.ratePerHour / 60)));
  return <g data-belt={belt.kind} data-observation={reading.observation} data-state={reading.state}>
    {casing}<path d={d} className="b-base" />
    {spacing === 0 ? null : <path d={d} className={`dfPlant__material${reading.state === "failing" || reading.state === "degraded" ? " dfPlant__material--faulty" : ""}`} strokeDasharray={`3 ${spacing}`} />}
    {reading.evidence === "runtime" ? <circle cx={belt.points[Math.floor(belt.points.length / 2)]!.x} cy={belt.points[Math.floor(belt.points.length / 2)]!.y} r="4" className="qtag" /> : null}
  </g>;
}

/**
 * Every belt's run, from where machines stand: one per flow, port to port,
 * and a unit's own intake from its only dock (or the manifold folding its
 * docks) to its main machine, where no flow already joins them. A source that
 * counts traffic per service but not per route can say a unit is busy
 * without saying which machine handled it: the intake carries that.
 */
function beltRuns(layout: SceneMachinery, graph: SceneGraph) {
  const at = new Map(layout.stations.map((station) => [station.entityId, station]));
  const joined = new Set(graph.flows.flatMap((flow) => [`${flow.from} ${flow.to}`, `${flow.to} ${flow.from}`]));
  const runs = [...graph.units.flatMap((unit) => {
    const docks = layout.stations.filter((station) => station.unit === unit.id && (station.shape === "dock" || station.shape === "manifold")), main = at.get(unit.id);
    return docks.length !== 1 || main === undefined || joined.has(`${docks[0]!.entityId} ${unit.id}`) ? [] : [{ key: `intake ${unit.id}`, from: docks[0]!, to: main, kind: "intake" }];
  }), ...graph.flows.flatMap((flow) => {
    const from = at.get(flow.from), to = at.get(flow.to);
    return from === undefined || to === undefined ? [] : [{ key: `${flow.from} ${flow.to}`, from, to, kind: flow.kind }];
  })];
  const { routes, crossings, junctions, links, stubs } = beltRoutes(layout, runs);
  // Every connection is drawn: a belt, an overhead link, or, past every budget, a stub at each end naming the other.
  return { runs: runs.map((run) => ({ key: run.key, from: run.from.entityId, to: run.to.entityId, kind: run.kind, points: routes.get(run.key) ?? stubs.get(run.key)!.find((end) => end !== undefined)?.points ?? [], link: links.has(run.key),
    ...(stubs.has(run.key) ? { stubs: stubs.get(run.key)! } : {}) })), crossings, junctions };
}

/** Ports two or more belts share: a junction, drawn as one. */
function junctions(belts: readonly BeltRoute[]) {
  const counts = new Map<string, { point: ScenePoint; count: number }>();
  for (const belt of belts) if (belt.stubs === undefined) for (const point of [belt.points[0]!, belt.points.at(-1)!]) { const key = `${point.x} ${point.y}`; counts.set(key, { point, count: (counts.get(key)?.count ?? 0) + 1 }); }
  return [...counts.values()].filter(({ count }) => count > 1).map(({ point }) => point);
}

/** An area's faint colour: its hue, chosen so neighbouring areas differ. */
function tint(hue: number, alpha = .13) {
  return `hsl(${hue} 40% 60% / ${alpha})`;
}

/** What a machine is: its kind, a fold's count, and for a unit's main machine the runtime it runs on. */
function kindText(machine: SceneMachine, unit?: SceneUnit) {
  return machine.represented !== undefined ? `${machine.represented.length} ${machine.kind} nodes`
    : unit?.id === machine.id && unit.runtime !== undefined ? `${RUNTIME_TEXT[unit.runtime]} unit` : machine.kind;
}

/** The inspector loads evidence only when opened; runtime-only values are shown as data. */
function MachineInspector({ machine, chosen, unit, onLoadNode, onInvestigate, proposals, onSelectProposal, onFocus, onDiscuss, onClose }: {
  machine: SceneMachine; chosen?: number; unit?: SceneUnit; onLoadNode?: (id: string) => Promise<OperationalNodeView>; onInvestigate?: (machine: SceneMachine, unit?: SceneUnit) => void;
  proposals: readonly SceneProposal[]; onSelectProposal?: (id: string) => void; onFocus: () => void; onDiscuss?: () => void; onClose: () => void;
}) {
  // A fold lists its members; the one chosen is inspected here, and only its own answer is ever shown.
  const folded = machine.represented, [member, setMember] = useState<number | undefined>(chosen), [returned, setReturned] = useState<number>();
  const nodeId = folded === undefined ? machine.id : member === undefined ? undefined : folded[member];
  const name = (index: number) => machine.routes?.[index] ?? folded![index]!;
  const [loaded, setLoaded] = useState<{ id: string; value: OperationalNodeView | "unavailable" }>();
  const detail = loaded !== undefined && loaded.id === nodeId ? loaded.value : undefined;
  const [asked, setAsked] = useState(false);
  useEffect(() => {
    if (onLoadNode === undefined || nodeId === undefined) return;
    let live = true;
    onLoadNode(nodeId).then((value) => { if (live) setLoaded({ id: nodeId, value }); }, () => { if (live) setLoaded({ id: nodeId, value: "unavailable" }); });
    return () => { live = false; };
  }, [nodeId]);
  const reading = machine.reading;
  const worth = reading.state === "failing" || reading.state === "degraded" || reading.evidence === "runtime" || reading.evidence === "contradicted" || reading.observation === "unobserved";
  return <section className="dfMachineInspector" aria-label="Machine inspector">
    <h3>{machine.label}</h3>
    <p>{machine.represented === undefined ? kindText(machine, unit) : `${machine.represented.length} folded ${machine.kind} nodes`}{unit !== undefined && unit.id !== machine.id ? ` in ${unit.label}` : ""}</p>
    <p className={`dfPlantReading s-${reading.observation} op-${reading.state}`}>{OBSERVATION_TEXT[reading.observation]}{reading.state === "unknown" ? "" : ` · ${reading.state}`}{reading.ratePerHour > 0 ? ` · ${rate(reading.ratePerHour)}` : ""}{reading.errorPermille > 0 ? ` · ${reading.errorPermille / 10}% errors` : ""}{reading.latencyMs > 0 ? ` · p95 ${reading.latencyMs} ms` : ""}</p>
    <p>{EVIDENCE_TEXT[reading.evidence]}</p>
    <button type="button" onClick={onFocus}>Focus on floor</button>
    {onDiscuss ? <button type="button" onClick={onDiscuss}>Discuss this machine</button> : null}
    {onInvestigate && worth ? <button type="button" disabled={asked} onClick={() => { setAsked(true); onInvestigate(machine, unit); }}>{asked ? "Investigation queued" : "Investigate"}</button> : null}
    <button type="button" onClick={onClose}>Close</button>
    {proposals.length === 0 ? null : <div aria-label="Changes affecting this machine">{proposals.map((proposal) => <button type="button" key={proposal.id} onClick={() => onSelectProposal?.(proposal.id)}>{proposal.title}</button>)}</div>}
    {folded === undefined ? null : member === undefined
      ? <ul className="dfMachineInspector__members" aria-label="Folded routes">{folded.map((id, index) => <li key={id}>{onLoadNode === undefined ? name(index) : <button type="button" autoFocus={index === returned} onClick={() => setMember(index)}>{name(index)}</button>}</li>)}</ul>
      : <p><button type="button" autoFocus onClick={() => { setReturned(member); setMember(undefined); }}>Back to folded routes</button> {name(member)}</p>}
    {nodeId === undefined ? null : <details open={folded !== undefined || undefined}><summary>Evidence</summary>
      <p>Node <code>{nodeId}</code></p>
      {detail === undefined ? <p>{onLoadNode === undefined ? "Evidence is not available here." : "Loading evidence…"}</p> : detail === "unavailable" ? <p>Evidence unavailable.</p> : <>
        {Object.keys(detail.selectors).length === 0 ? null : <dl>{Object.entries(detail.selectors).map(([key, value]) => <div key={key}><dt>{key}</dt><dd><code>{value}</code></dd></div>)}</dl>}
        <ul aria-label="Evidence">{detail.evidence.map((item, index) => <li key={index}>{item.origin} · {item.source} · {item.confidence}{item.detail ? <> · <code>{item.detail}</code></> : null}</li>)}</ul>
        {detail.sources.length === 0 ? null : <ul aria-label="Source locations">{detail.sources.map((location) => <li key={`${location.repository_id}:${location.path}:${location.line ?? 0}`}><code>{location.path}{location.line ? `:${location.line}` : ""}</code></li>)}</ul>}
        {detail.modules.length === 0 ? null : <p>{detail.modules.length} code areas run in this unit.</p>}
        <p>{detail.observers.length === 0 ? "No runtime source has reported on it." : `Seen by ${detail.observers.join(", ")}.`}</p>
      </>}
    </details>}
  </section>;
}
