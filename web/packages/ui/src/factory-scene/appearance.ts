import type { SpriteAppearance } from "@dark-factory/client";
import { spriteOptions } from "./sprites/sprites.generated.js";
import type { SceneWorker } from "./scene.js";
import type { WorkerMotion } from "./movement.js";

export { spriteOptions };
export type { SpriteAppearance };

export const appearanceFields = ["skin", "hair", "hair_colour", "face", "outfit", "clothes_colour", "shoes", "tool", "headwear"] as const;

function hash(id: string): number {
  let value = 2166136261;
  for (let i = 0; i < id.length; i++) value = Math.imul(value ^ id.charCodeAt(i), 16777619) >>> 0;
  return value;
}

/** Each worker keeps their own time, so a floor of people never moves in step. */
export function workerPhase(id: string): number {
  return hash(id) % 5000;
}

export function automaticAppearance(worker: Pick<SceneWorker, "id" | "role">): SpriteAppearance {
  const slot = hash(worker.id) % 4;
  return {
    automatic: true,
    skin: slot < 2 ? 1 : 2,
    hair: slot,
    hair_colour: slot,
    face: 0,
    outfit: slot,
    clothes_colour: slot,
    shoes: slot,
    tool: worker.role === "orchestrator" ? 1 : 0,
    headwear: worker.role === "orchestrator" ? 1 : 0,
  };
}

export function resolvedAppearance(worker: Pick<SceneWorker, "id" | "role" | "appearance">): SpriteAppearance {
  const value = worker.appearance;
  if (value === undefined || value.automatic) return automaticAppearance(worker);
  return Object.fromEntries([
    ["automatic", false],
    ...appearanceFields.map((field) => [field, Number.isInteger(value[field]) && value[field] >= 0 && value[field] < spriteOptions[field].length ? value[field] : 0]),
  ]) as SpriteAppearance;
}

// How often each thing on the table is picked up, and for how long it is used.
// Food and drink pause at the chest on the way to the mouth and back; anything
// read stays at the chest. A new thing to rest with is one entry here and its
// art in the generator.
const RESTING = {
  cup: { every: 5600, use: 1000, mouth: true },
  snack: { every: 4700, use: 600, mouth: true },
  book: { every: 12000, use: 7000, mouth: false },
  clipboard: { every: 10000, use: 5000, mouth: false },
  tablet: { every: 11000, use: 6000, mouth: false },
} as const;
const LIFT = 500;

/** What a worker rests with, and where it is right now: what they carry if it suits a table, else their own habit. */
export function restingItem(worker: SceneWorker, at?: number): Readonly<{ item: keyof typeof RESTING; where: "table" | "chest" | "mouth" }> {
  const carried = spriteOptions.tool[resolvedAppearance(worker).tool]?.name;
  const item = carried === "mug" ? "cup" : carried === "clipboard" || carried === "tablet" ? carried : (["cup", "book", "snack"] as const)[hash(worker.id) % 3]!;
  const { every, use, mouth } = RESTING[item];
  if (at === undefined || !Number.isFinite(at) || at < 0) return { item, where: "table" };
  const t = at % every;
  if (!mouth) return { item, where: t < use ? "chest" : "table" };
  return { item, where: t < LIFT ? "chest" : t < LIFT + use ? "mouth" : t < LIFT * 2 + use ? "chest" : "table" };
}

/**
 * The aligned atlas layers for one worker. Status and motion choose a pose; the
 * person's own layers ride on it unchanged. `motion.at` is the worker's own
 * running clock: absent, every pose rests on its first frame.
 */
export function workerFrames(worker: SceneWorker, motion?: WorkerMotion, seat?: "resting" | "planning"): readonly string[] {
  const appearance = resolvedAppearance(worker);
  const role = worker.role === "orchestrator" ? "overseer" : "worker";
  const provider = worker.provider === "claude_code" || worker.provider === "codex" ? worker.provider : "shell";
  // Only a running clock animates; anything else rests on the first frame.
  const at = motion?.at !== undefined && Number.isFinite(motion.at) && motion.at >= 0 ? motion.at : undefined;
  const walking = motion?.action === "walking";
  const seated = seat !== undefined && !walking;
  const rest = restingItem(worker, at);
  // Planners write in bursts; resting workers pick up what is in front of them now and then.
  const scribble = at !== undefined && at % 2600 < 1300 ? Math.floor(at / 325) % 2 : 0;
  // At the bench a wrench, tablet or clipboard is worked with: the carrying arm
  // reaches forward and back. Empty hands, or a mug, leave the keyboard.
  const wielding = motion?.action === "interacting" && ["clipboard", "wrench", "tablet"].includes(spriteOptions.tool[appearance.tool]?.name ?? "");
  const pose = walking ? `walk.${motion.frame}`
    : wielding ? motion.frame === 1 ? "walk.1" : "idle"
    : motion?.action === "interacting" ? `type.${motion.frame}`
    : worker.activity === "needs-you" ? `wave.${at === undefined ? 0 : Math.floor(at / 400) % 2}`
    : seat === "planning" ? `type.${scribble}`
    : seat === "resting" ? rest.where === "mouth" ? "sip" : rest.where === "chest" ? "hold" : "idle"
    : worker.activity === "busy" ? "type.0"
    : worker.activity === "waiting" ? "waiting" : "idle";
  const cloth = appearance.outfit === 1 ? appearance.clothes_colour : "plain";
  const alert = worker.activity === "needs-you" ? ["person.alert"] : [];
  // Walking away shows a back, walking across a profile (the scene mirrors east
  // for west). Only the front has a face to blink or a chest to badge.
  if (walking && motion.direction === "north") return [
    `person.skin.${appearance.skin}.back.${pose}`,
    `person.legs.${cloth}.${pose}`,
    `person.outfit.${appearance.outfit}.${appearance.clothes_colour}.back.${pose}`,
    `person.hair.${appearance.hair}.${appearance.hair_colour}.back`,
    `person.shoes.${appearance.shoes}.${pose}`,
    `person.headwear.${appearance.headwear}.back`,
    // Seen from behind the carrying arm is on the other side, and so is its step.
    `person.tool.${appearance.tool}.back.${pose === "walk.0" ? "high" : "low"}`,
    ...alert,
  ];
  if (walking && (motion.direction === "east" || motion.direction === "west")) return [
    `person.skin.${appearance.skin}.side.${pose}`,
    `person.legs.${cloth}.side.${pose}`,
    `person.outfit.${appearance.outfit}.${appearance.clothes_colour}.side.${pose}`,
    `person.hair.${appearance.hair}.${appearance.hair_colour}.side`,
    `person.face.${appearance.face}.side`,
    `person.shoes.${appearance.shoes}.side.${pose}`,
    `person.headwear.${appearance.headwear}.side`,
    `person.tool.${appearance.tool}.side.${pose}`,
    `person.system.${role}.${provider}`,
    ...alert,
  ];
  const step = walking ? pose : "stand";
  const blinking = at !== undefined && at % (3000 + hash(worker.id) % 4000) < 200;
  const glasses = spriteOptions.face[appearance.face]?.name === "glasses";
  const held = pose === "sip" || pose === "hold" ? [`person.held.${rest.item}.${rest.where}`]
    : seat === "planning" && pose.startsWith("type") ? [`person.held.pencil.${scribble}`]
    : pose.startsWith("type") ? ["person.held.keyboard"]
    : seated ? [] : [`person.tool.${appearance.tool}.${pose === "walk.1" ? "high" : "low"}`];
  return [
    `person.skin.${appearance.skin}.${pose}`,
    `person.legs.${cloth}.${seated ? "sit" : step}`,
    `person.outfit.${appearance.outfit}.${appearance.clothes_colour}.${pose}`,
    `person.hair.${appearance.hair}.${appearance.hair_colour}`,
    ...(blinking && !glasses ? [`person.blink.${appearance.skin}`] : []),
    `person.face.${appearance.face}`,
    ...(blinking && glasses ? ["person.blink.glasses"] : []),
    `person.shoes.${appearance.shoes}.${step}`,
    `person.headwear.${appearance.headwear}`,
    `person.system.${role}.${provider}`,
    // Held last: in front of a headset's boom, and over the chest badge.
    ...held,
    ...alert,
  ];
}

export function randomAppearance(random = Math.random): SpriteAppearance {
  return Object.fromEntries([
    ["automatic", false],
    ...appearanceFields.map((field) => [field, Math.floor(random() * spriteOptions[field].length)]),
  ]) as SpriteAppearance;
}
