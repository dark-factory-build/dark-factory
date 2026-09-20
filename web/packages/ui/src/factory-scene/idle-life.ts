import type { SceneTask } from "../console-view.js";
import { hash } from "./appearance.js";
import type { ScenePoint, SceneWorker } from "./scene.js";

/**
 * What goes on in the break room when nobody is asking anything of it: a cat,
 * and small talk between neighbours. All of it is a function of the floor's
 * clock and who is sitting where; it is never stored, served or sent.
 */

/** One seat of the break room's first table, left to right. `free` people are seated, still, and not asking for anyone. */
export type Seat = ScenePoint & Readonly<{ id?: string; free?: boolean }>;

export type Cat = ScenePoint & Readonly<{
  frame: "sit.0" | "sit.1" | "pet" | "sleep.0" | "sleep.1" | "walk.0" | "walk.1";
  west: boolean;
  /** Only a moving cat needs more than the floor's slow pulse. */
  moving: boolean;
  pettedBy?: string;
  /** Milliseconds into the stroking, for the heart that rises from it. */
  pettedFor?: number;
}>;

/** A well-mixed number from a weak one: every bit of the result depends on every bit given. */
const roll = (value: number) => { value = Math.imul(value ^ value >>> 16, 0x85ebca6b); value = Math.imul(value ^ value >>> 13, 0xc2b2ae35); return (value ^ value >>> 16) >>> 0; };

const CAT_EPOCH = 45_000;
const HOP = 300;
const PROWL = 48;
// The floor behind the table: the cat's other haunt, and its way along.
const BEHIND = -30;
const PETTING = { after: 5000, lasts: 3200 };

/** Where the cat's bed stands: on the floor behind the far end of the first table, clear of the room's sign. */
export function catBed(row: readonly Seat[]): ScenePoint | undefined {
  const end = row.at(-1);
  // A little past the last seat: out from under that seat's speech bubble, short of the bookshelf.
  return row.length < 2 || end === undefined ? undefined : { x: end.x + 8, y: end.y + BEHIND };
}

/**
 * The cat keeps to the first table. Its haunts are the gaps between seats on
 * the table, and its bed on the floor behind its far end; each epoch it picks one, hops
 * off, prowls along behind and hops up, then sits or sleeps. On the table,
 * whoever sits to its left strokes it once. A stopped clock finds it asleep.
 */
export function catAt(row: readonly Seat[], at: number | undefined): Cat | undefined {
  const gaps = row.slice(0, -1);
  if (gaps.length === 0) return undefined;
  if (at === undefined || !Number.isFinite(at) || at < 0) return { ...catBed(row)!, frame: "sleep.0", west: false, moving: false };
  // Where it goes and whether it naps there are separate rolls: fields of one hash line up whenever the number of
  // gaps is a power of two, and then it only ever sleeps at some gaps and is only ever stroked at the others.
  const haunt = (epoch: number) => { const pick = roll(hash(`cat ${epoch}`)); return pick % 3 === 0 ? { ...catBed(row)!, y: row[0]!.y, behind: true } : { ...gaps[(pick >>> 8) % gaps.length]!, behind: false }; };
  const epoch = Math.floor(at / CAT_EPOCH), t = at % CAT_EPOCH;
  const to = haunt(epoch), from = epoch === 0 ? to : haunt(epoch - 1);
  const up = from.behind ? 0 : HOP, prowl = Math.abs(to.x - from.x) / PROWL * 1000, down = to.behind ? 0 : HOP;
  const travel = from.x === to.x && from.behind === to.behind ? 0 : up + prowl + down;
  if (t < travel) {
    const moving = { frame: `walk.${Math.floor(t / 140) % 2}` as "walk.0" | "walk.1", west: to.x < from.x, moving: true };
    if (t < up) return { x: from.x, y: from.y + BEHIND * t / up, ...moving };
    if (t < up + prowl) return { x: from.x + (to.x - from.x) * (t - up) / prowl, y: to.y + BEHIND, ...moving };
    return { x: to.x, y: to.y + BEHIND * (travel - t) / down, ...moving };
  }
  const settled = t - travel, stroked = settled - PETTING.after;
  const still = { x: to.x, y: to.y + (to.behind ? BEHIND : 0), west: false, moving: false };
  // Its bed is for sleeping. Elsewhere, a roll of its own decides.
  const sleepy = to.behind || roll(hash(`cat ${epoch}`) + 1) % 2 === 0;
  // In its bed it goes straight to sleep; on the table it sits a while first.
  if (sleepy && (to.behind || settled > 4000)) return { ...still, frame: `sleep.${Math.floor(settled / 1400) % 2}` as "sleep.0" | "sleep.1" };
  if (!sleepy && !to.behind && to.free && to.id !== undefined && stroked >= 0 && stroked < PETTING.lasts) return { ...still, frame: "pet", pettedBy: to.id, pettedFor: stroked };
  return { ...still, frame: settled % 2600 > 2200 ? "sit.1" : "sit.0" };
}

export type Remark = Readonly<{ line: string; reply: string; about?: readonly string[] }>;

// Small talk never states anything about the factory: that is what `gossip` is
// for, and everything it says is true of the floor it is said on.
const SMALL_TALK: readonly Remark[] = [
  { line: "Did you see the size of that diff?", reply: "I stopped scrolling." },
  { line: "Whose turn is it to feed the cat?", reply: "The cat says yours." },
  { line: "Tabs or spaces? Be honest.", reply: "Whatever the formatter says." },
  { line: "It worked in my worktree.", reply: "It always does." },
  { line: "One day I'm going to read the whole README.", reply: "Tell me how it ends." },
  { line: "Do you ever dream in YAML?", reply: "Only the indentation." },
  { line: "The cat walked across my keyboard again.", reply: "Best commit all week." },
  { line: "I could have sworn I fixed that already.", reply: "You did. Twice." },
  { line: "Rebase or merge?", reply: "Not before coffee." },
  { line: "I heard the overseer never blinks.", reply: "Don't look now." },
  { line: "What day is it out there?", reply: "There's an out there?" },
  { line: "I still think it should have been a shell script.", reply: "You always do." },
];

const quoted = (title: string) => title.length <= 36 ? title : `${title.slice(0, 35).trimEnd()}…`;

/** Things worth saying about this floor, each of them true of it right now. */
export function gossip(workers: readonly SceneWorker[], tasks: readonly SceneTask[]): readonly Remark[] {
  const named = new Map(workers.map((worker) => [worker.id, worker.name]));
  const count = (status: SceneTask["status"]) => tasks.filter((task) => task.status === status).length;
  const queued = count("queued"), blocked = count("blocked");
  return [
    ...tasks.flatMap((task) => task.status === "running" && named.has(task.agentId) ? [{ line: `${named.get(task.agentId)} has “${quoted(task.title)}”.`, reply: "Rather them than me.", about: [task.agentId] }] : []),
    ...workers.flatMap((worker) => worker.activity === "needs-you" ? [{ line: `${worker.name} is still waiting on an answer.`, reply: "Aren't we all.", about: [worker.id] }]
      : worker.paused ? [{ line: `${worker.name} has been paused.`, reply: "Lucky them.", about: [worker.id] }] : []),
    ...(queued === 0 ? [] : [{ line: `${queued} waiting in the tray.`, reply: "I can't see it from here." }]),
    ...(blocked === 0 ? [] : [{ line: `${blocked} blocked, I hear.`, reply: "Someone should tell the boss." }]),
  ];
}

export type Chat = Readonly<{
  /** Opener first. */
  between: readonly [string, string];
  remark: Remark;
  /** Who has a bubble up right now, and which. */
  speaking?: Readonly<{ id: string; glyph: "ask" | "tell" | "joke" | "hum" }>;
  replied: boolean;
}>;

const CHAT_EPOCH = 20_000;
const TURN = 2400;

/**
 * Neighbours with nothing to do talk now and then: one says something, the
 * other answers, and sometimes the first has the last word. Nobody is in two
 * conversations, and nobody is the subject of their own gossip.
 */
export function chats(row: readonly Seat[], news: readonly Remark[], at: number | undefined): readonly Chat[] {
  if (at === undefined || !Number.isFinite(at) || at < 0) return [];
  const epoch = Math.floor(at / CHAT_EPOCH), t = at % CHAT_EPOCH, found: Chat[] = [];
  for (let seat = 0; seat + 1 < row.length; seat += 1) {
    const left = row[seat]!, right = row[seat + 1]!;
    if (!left.free || !right.free || left.id === undefined || right.id === undefined) continue;
    const seed = hash(`${left.id} ${right.id} ${epoch}`);
    if (seed % 2 === 1) continue;
    const since = t - (seed >>> 1) % (CHAT_EPOCH - TURN * 3), turn = Math.floor(since / TURN);
    const lastWord = (seed >>> 4) % 3 === 0;
    if (since < 0 || turn > (lastWord ? 2 : 1)) continue;
    const between = (seed >>> 2) % 2 === 0 ? [left.id, right.id] as const : [right.id, left.id] as const;
    const fresh = news.filter((remark) => !remark.about?.some((id) => between.includes(id)));
    const remark = fresh.length > 0 && (seed >>> 3) % 2 === 0 ? fresh[(seed >>> 8) % fresh.length]! : SMALL_TALK[(seed >>> 8) % SMALL_TALK.length]!;
    // A bubble comes down a moment before the next goes up.
    const speaking = since % TURN > TURN - 300 ? undefined
      : turn === 0 ? { id: between[0], glyph: remark.line.endsWith("?") ? "ask" as const : fresh.includes(remark) ? "tell" as const : "joke" as const }
      : turn === 1 ? { id: between[1], glyph: remark.reply.endsWith("?") ? "ask" as const : "hum" as const }
      : { id: between[0], glyph: "joke" as const };
    found.push({ between, remark, speaking, replied: turn >= 1 });
    seat += 1;
  }
  return found;
}
