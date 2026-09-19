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

/**
 * The aligned atlas layers for one worker. Status and motion choose a pose; the
 * person's own layers ride on it unchanged. `motion.at` is the worker's own
 * running clock: absent, every pose rests on its first frame.
 */
export function workerFrames(worker: SceneWorker, motion?: WorkerMotion, seat?: "resting" | "planning"): readonly string[] {
  const appearance = resolvedAppearance(worker);
  const role = worker.role === "orchestrator" ? "overseer" : "worker";
  const provider = worker.provider === "claude_code" || worker.provider === "codex" ? worker.provider : "shell";
  const at = motion?.at;
  const walking = motion?.action === "walking";
  const seated = seat !== undefined && !walking;
  // Planners write in bursts; resting workers lift the cup now and then.
  const scribble = at !== undefined && at % 2600 < 1300 ? Math.floor(at / 325) % 2 : 0;
  const pose = walking ? `walk.${motion.frame}`
    : motion?.action === "interacting" ? `type.${motion.frame}`
    : worker.activity === "needs-you" ? `wave.${at === undefined ? 0 : Math.floor(at / 400) % 2}`
    : seat === "planning" ? `type.${scribble}`
    : seat === "resting" ? at !== undefined && at % 5200 < 900 ? "sip" : "idle"
    : worker.activity === "busy" ? "type.0"
    : worker.activity === "waiting" ? "waiting" : "idle";
  const step = walking ? pose : "stand";
  const held = pose === "sip" ? ["person.held.cup"]
    : seat === "planning" && pose.startsWith("type") ? [`person.held.pencil.${scribble}`]
    : pose.startsWith("type") ? ["person.held.keyboard"]
    : seated ? [] : [`person.tool.${appearance.tool}.${pose === "walk.1" ? "high" : "low"}`];
  return [
    `person.skin.${appearance.skin}.${pose}`,
    `person.legs.${appearance.outfit === 1 ? appearance.clothes_colour : "plain"}.${seated ? "sit" : step}`,
    `person.outfit.${appearance.outfit}.${appearance.clothes_colour}.${pose}`,
    `person.hair.${appearance.hair}.${appearance.hair_colour}`,
    ...(at !== undefined && at % (3000 + hash(worker.id) % 4000) < 200 ? [`person.blink.${appearance.skin}`] : []),
    `person.face.${appearance.face}`,
    `person.shoes.${appearance.shoes}.${step}`,
    `person.headwear.${appearance.headwear}`,
    ...held,
    `person.system.${role}.${provider}`,
    ...(worker.activity === "needs-you" ? ["person.alert"] : []),
  ];
}

export function randomAppearance(random = Math.random): SpriteAppearance {
  return Object.fromEntries([
    ["automatic", false],
    ...appearanceFields.map((field) => [field, Math.floor(random() * spriteOptions[field].length)]),
  ]) as SpriteAppearance;
}
