import type { PeerQuestionItem } from "@dark-factory/client";
import type { SceneTask } from "../console-view.js";
import { pointOnRoute, type Route } from "./movement.js";
import type { ScenePoint } from "./scene.js";

/**
 * Things that really passed between people on the floor: work handed from the
 * tray to whoever starts it, a question one running task put to another, and
 * its answer. Each is seen as a change between two snapshots, shown once, and
 * forgotten; nothing here is stored or sent.
 */
export type FloorEvent = Readonly<{
  key: string;
  kind: "assign" | "ask" | "answer";
  /** The worker it comes from; work comes from the tray. */
  from?: string;
  to: string;
}>;

export type Seen = Readonly<{ tasks: ReadonlyMap<string, SceneTask["status"]>; questions: ReadonlyMap<string, boolean> }>;

/** The first look is history, not news: it shows nothing. A task is only handed over if it was seen waiting. */
export function observe(before: Seen | undefined, tasks: readonly SceneTask[], questions: readonly PeerQuestionItem[]): Readonly<{ seen: Seen; events: readonly FloorEvent[] }> {
  // ponytail: grows by one entry per question seen while this floor is open; prune by age if a floor ever stays open for weeks.
  // Questions are remembered for good: the snapshot only carries the newest, and one that drops out and comes back is not news.
  const seen = { tasks: new Map(tasks.map((task) => [task.id, task.status])), questions: new Map([...(before?.questions ?? []), ...questions.map((question) => [question.id, question.answered] as const)]) };
  if (before === undefined) return { seen, events: [] };
  const agent = new Map(tasks.map((task) => [task.id, task.agentId]));
  const events: FloorEvent[] = tasks.flatMap((task) => task.status === "running" && before.tasks.get(task.id) === "queued" && task.agentId !== "" ? [{ key: `assign ${task.id}`, kind: "assign" as const, to: task.agentId }] : []);
  for (const question of questions) {
    const asker = agent.get(question.source_task_id), asked = agent.get(question.target_task_id), was = before.questions.get(question.id);
    if (!asker || !asked || asker === asked || was === question.answered) continue;
    events.push(question.answered ? { key: `answer ${question.id}`, kind: "answer", from: asked, to: asker } : { key: `ask ${question.id}`, kind: "ask", from: asker, to: asked });
  }
  return { seen, events };
}

/** One event in flight. A pulse keeps to the corridors people walk; paper flies straight over everything. */
export type FloorMessage = FloorEvent & Readonly<{ startedAt: number; origin: ScenePoint; route?: Route; travel: number }>;

const PULSE_SPEED = 260;
const WIND_UP = 250;
export const HAIL = 600;
const REPLY = 1200;

export function send(event: FloorEvent, startedAt: number, origin: ScenePoint, route: Route | undefined, straight: number): FloorMessage {
  return { ...event, startedAt, origin, route, travel: event.kind === "assign" ? 900 : Math.max(500, (route?.length ?? straight) / PULSE_SPEED * 1000) };
}

/** When the last of it has been shown: the receiver's raised hand for paper, their reply bubble for a pulse. */
export const endsAt = (message: FloorMessage) => message.startedAt + (message.kind === "assign" ? message.travel + HAIL : WIND_UP + message.travel + REPLY);

/**
 * Where a message is and what the people at either end are doing about it:
 * the sender raises a hand as it leaves, the receiver as it lands.
 */
export function messageAt(message: FloorMessage, destination: ScenePoint, at: number): Readonly<{ point?: ScenePoint; trail: readonly ScenePoint[]; sending: boolean; hailing?: "from" | "to"; landed: boolean }> {
  const leaves = message.kind === "assign" ? 0 : WIND_UP, since = at - message.startedAt, flown = since - leaves;
  const landed = flown >= message.travel;
  const along = (progress: number): ScenePoint => message.route !== undefined
    ? pointOnRoute(message.origin, message.route, message.route.length * progress)
    : { x: message.origin.x + (destination.x - message.origin.x) * progress, y: message.origin.y + (destination.y - message.origin.y) * progress - Math.sin(Math.PI * progress) * Math.min(28, Math.hypot(destination.x - message.origin.x, destination.y - message.origin.y) / 4) };
  const progress = Math.min(1, Math.max(0, flown / message.travel));
  return {
    point: since < 0 || flown < 0 || landed ? undefined : along(progress),
    // Three fading dots a few pixels apart, however long the way is.
    trail: since < 0 || flown < 0 || landed || message.route === undefined ? [] : [7, 14, 21].map((behind) => along(Math.max(0, progress - behind / message.route!.length))),
    sending: since >= 0 && !landed && message.from !== undefined,
    hailing: since >= 0 && since < HAIL && message.from !== undefined ? "from" : landed && flown - message.travel < HAIL ? "to" : undefined,
    landed: landed && flown - message.travel < REPLY,
  };
}
