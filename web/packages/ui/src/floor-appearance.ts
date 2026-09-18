export type FloorAppearance = Readonly<{
  scenery: "off" | "subtle" | "rich";
  animation: "follow-device" | "off";
}>;

const STORAGE_KEY = "dark-factory.floor-appearance";

export const DEFAULT_FLOOR_APPEARANCE: FloorAppearance = {
  scenery: "rich",
  animation: "follow-device",
};

function oneOf<T extends string>(value: unknown, values: readonly T[], fallback: T): T {
  return typeof value === "string" && values.includes(value as T) ? value as T : fallback;
}

/** Invalid and absent fields fall back independently, preserving valid choices. */
export function readFloorAppearance(value: string | null): FloorAppearance {
  let parsed: Record<string, unknown> = {};
  try {
    const candidate: unknown = value === null ? {} : JSON.parse(value);
    if (candidate !== null && typeof candidate === "object" && !Array.isArray(candidate)) parsed = candidate as Record<string, unknown>;
  } catch { /* browser-local preferences must never break the console */ }
  return {
    scenery: oneOf(parsed.scenery, ["off", "subtle", "rich"], DEFAULT_FLOOR_APPEARANCE.scenery),
    animation: oneOf(parsed.animation, ["follow-device", "off"], DEFAULT_FLOOR_APPEARANCE.animation),
  };
}

export function loadFloorAppearance(): FloorAppearance {
  try { return typeof window === "undefined" ? DEFAULT_FLOOR_APPEARANCE : readFloorAppearance(window.localStorage.getItem(STORAGE_KEY)); }
  catch { return DEFAULT_FLOOR_APPEARANCE; }
}

export function saveFloorAppearance(appearance: FloorAppearance): void {
  try { window.localStorage.setItem(STORAGE_KEY, JSON.stringify(appearance)); } catch { /* storage may be blocked */ }
}

export function resetFloorAppearance(): void {
  try { window.localStorage.removeItem(STORAGE_KEY); } catch { /* storage may be blocked */ }
}
