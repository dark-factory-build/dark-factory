import { ROOM_LEFT, type SceneLayout, type SceneWorker, type SceneWorkerPlacement, type ScenePoint } from "./scene.js";

export type AmbientLife = "off" | "quiet" | "lively";
export type AmbientDestination = "notice-board" | "break-counter" | "shared-table";
type Seat = { destination: AmbientDestination; point: ScenePoint };

function hash(value: string) {
  let result = 2166136261;
  for (const character of value) result = Math.imul(result ^ character.charCodeAt(0), 16777619) >>> 0;
  return result >>> 0;
}

/** Common-area fixtures are bounded by the existing spine and resting floor. */
export function ambientSeats(layout: SceneLayout): readonly Seat[] {
  const left = ROOM_LEFT + 8;
  const right = Math.max(left + 32, layout.width - 32);
  const clamp = (x: number) => Math.min(right - 8, Math.max(left + 8, x));
  return [
    { destination: "notice-board", point: { x: clamp(left + 16), y: layout.restingTop + 16 } },
    { destination: "notice-board", point: { x: clamp(left + 32), y: layout.restingTop + 16 } },
    { destination: "break-counter", point: { x: clamp(left + 80), y: layout.restingTop + 16 } },
    { destination: "break-counter", point: { x: clamp(left + 96), y: layout.restingTop + 16 } },
    { destination: "shared-table", point: { x: clamp(left + 144), y: layout.restingTop + 44 } },
    { destination: "shared-table", point: { x: clamp(left + 160), y: layout.restingTop + 44 } },
  ];
}

/** Assign stable, bounded common-area seats with sparse phase changes. */
export function ambientPlacements(layout: SceneLayout, placements: readonly SceneWorkerPlacement[], workers: readonly SceneWorker[], mode: AmbientLife = "quiet", phase = 0): readonly SceneWorkerPlacement[] {
  if (mode === "off") return placements;
  const eligible = new Set(workers.filter((worker) => worker.location !== "working" && worker.activity !== "needs-you" && !worker.paused && (mode === "lively" || worker.activity === "idle" || worker.activity === "waiting")).map((worker) => worker.id));
  const seats = ambientSeats(layout);
  const occupied = new Set<string>();
  const byId = new Map<string, SceneWorkerPlacement>();
  for (const placement of placements.filter((candidate) => candidate.area === "resting" && eligible.has(candidate.id)).sort((a, b) => hash(a.id) - hash(b.id))) {
    const preferred = (hash(placement.id) + phase) % seats.length;
    let chosen = preferred;
    let assigned = false;
    for (let attempt = 0; attempt < seats.length; attempt++, chosen = (chosen + 1) % seats.length)
      if (!occupied.has(String(chosen))) { occupied.add(String(chosen)); assigned = true; break; }
    if (!assigned) continue;
    const seat = seats[chosen]!;
    byId.set(placement.id, { ...placement, x: seat.point.x, y: seat.point.y, ambient: seat.destination });
  }
  const occupancy = new Map<AmbientDestination, number>();
  for (const placement of byId.values()) occupancy.set(placement.ambient!, (occupancy.get(placement.ambient!) ?? 0) + 1);
  for (const [id, placement] of byId) if (placement.ambient === "shared-table" && occupancy.get("shared-table") === 2)
    byId.set(id, { ...placement, ambientSocial: true });
  return placements.map((placement) => byId.get(placement.id) ?? placement);
}
