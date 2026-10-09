import { RemoteDaemonMismatchError } from "@dark-factory/client";
import type {
  RemoteFactoryStatus,
  StateView,
  TaskItem,
} from "@dark-factory/client";
import { orderTasksForHome } from "../console-view.js";

/**
 * The remote console shows one factory's connection state as a glyph, in the
 * same vocabulary the floor uses: a filled mark is live, a ring is in flight,
 * and a refusal is never dressed up as a transient.
 */
export const REMOTE_STATUS_GLYPH: Record<RemoteFactoryStatus, string> = {
  offline: "·",
  connecting: "◌",
  pairing: "◌",
  authenticating: "◌",
  syncing: "◌",
  ready: "◆",
  revoked: "×",
  mismatch: "×",
  expired: "×",
  error: "!",
};

const DELIVERY_UNKNOWN = "Delivery unknown — check the factory";
const NOT_DELIVERED = "Not delivered";
export const REQUEST_CLOSED = "This question is no longer open";
export const FACTORY_UNREACHABLE = "Factory offline — nothing was sent";

/**
 * The banner a factory's own connection state earns. A state a person can do
 * nothing about is named plainly; a state that is merely in flight is not
 * shouted at them.
 */
export function remoteFactoryBanner(status: RemoteFactoryStatus): string | undefined {
  switch (status) {
    case "revoked":
      return "Access revoked";
    // The node still routes, but the daemon behind it is not the bound one.
    case "mismatch":
      return "Factory identity mismatch";
    // The relay would refuse this ticket, so it is never presented again.
    case "expired":
      return "Invitation expired · pair again";
    // The manager only reports error for a binding no reconnection repairs.
    case "error":
      return "Factory refused · pair again";
    case "offline":
    case "connecting":
      return "Factory offline";
    default:
      return undefined;
  }
}

/**
 * Every action control in this console is gated on one predicate: the device
 * has a network and this factory has a live authenticated session. Nothing is
 * offered that could only fail.
 */
export function remoteActionable(status: RemoteFactoryStatus | undefined, online: boolean): boolean {
  return online && status === "ready";
}

/**
 * The codes the daemon only produces by refusing before it acts — the same set
 * the desktop console names NOT SENT. A bad response frame is not among them:
 * a reply result whose revision has moved on is what a delivered answer looks
 * like, so every other failure leaves the effect genuinely unknown.
 */
const REFUSED = new Set([
  "invalid_request",
  "unauthorized",
  "stale",
  "too_large",
  "rate_limited",
  "not_found",
  "crypto_unavailable",
  "unsupported",
]);

function errorCode(error: unknown): string | undefined {
  const code = (error as { code?: unknown } | null | undefined)?.code;
  return typeof code === "string" ? code : undefined;
}

/** One-shot effects have exactly two honest outcomes when they do not succeed. */
export function remoteDeliveryNotice(error: unknown): string {
  const code = errorCode(error);
  return code !== undefined && REFUSED.has(code) ? NOT_DELIVERED : DELIVERY_UNKNOWN;
}

export const INVITATION_UNREADABLE = "That link is not a factory invitation";
export const INVITATION_SPENT = "This invitation has expired or was already used";

const PAIR_FAILURES = new Map<string, string>([
  ["invalid_request", INVITATION_UNREADABLE],
  ["unauthorized", "The factory refused this invitation"],
  ["pairing_required", "The factory refused this invitation"],
  ["pairing_uncertain", "Pairing result unknown — check the factory before pairing again"],
  ["storage_unavailable", "This browser cannot store a factory key"],
  ["crypto_unavailable", "This browser cannot pair"],
  ["connection", "The relay refused the connection"],
  ["closed", "The relay refused the connection"],
  ["malformed", "The factory answered with something this device could not read"],
]);

export function remotePairFailure(error: unknown): string {
  // An identity failure, not a refusal: the invitation was answered, but not
  // by the daemon it named.
  if (error instanceof RemoteDaemonMismatchError) return "A different factory answered for this node";
  const code = errorCode(error);
  return (code === undefined ? undefined : PAIR_FAILURES.get(code)) ?? "Pairing did not complete";
}

export type RemoteProjectGroup = Readonly<{
  id: string;
  name: string;
  tasks: readonly TaskItem[];
}>;

/**
 * The factory's work, grouped by the project that owns it. Task order inside a
 * group is the console's own home order, so a phone and a desktop rank the
 * same work the same way.
 */
export function remoteProjectGroups(state: StateView): readonly RemoteProjectGroup[] {
  const groups = new Map<string, TaskItem[]>();
  for (const task of orderTasksForHome(state)) {
    const existing = groups.get(task.project_id);
    if (existing === undefined) groups.set(task.project_id, [task]);
    else existing.push(task);
  }
  // A project with nothing queued is still part of the factory's shape.
  for (const project of state.projects.values()) if (!groups.has(project.id)) groups.set(project.id, []);
  return [...groups.entries()].map(([id, tasks]) => Object.freeze({
    id,
    name: state.projects.get(id)?.name ?? `project ${id.slice(0, 8)}`,
    tasks: Object.freeze(tasks),
  }));
}

export function shortRemoteID(value: string): string {
  return value.slice(0, 8);
}
