import { SessionError } from "../session.js";
import { parseTicket, unixSeconds } from "./tokens.js";

/**
 * A remote invitation is the whole authority to pair one browser with one
 * factory: it names the node, the daemon, a one-shot pairing challenge, and the
 * pair-purpose relay ticket that carries the browser to that node once. The
 * relay and the loopback host the daemon signs into its transcript are named
 * only when they are not the ordinary ones. It is delivered in a URL fragment,
 * so it never reaches the PWA's server.
 */
export type RemoteInvitation = Readonly<{
  relay: string;
  node: string;
  daemon: string;
  host: string;
  challenge: string;
  ticket: string;
  expires: number;
}>;

const MARKER = "df_remote";
const FRAGMENT_KEY = /(?:^|[?#&])df_remote(?:[&=]|$)/i;
const DEFAULT_RELAY = "wss://relay.darkfactory.build";
const DEFAULT_HOST = "127.0.0.1:43123";
const NODE_ID = /^[a-z2-7]{32}$/;
const DAEMON_ID = /^[0-9a-f]{32}$/;
const CHALLENGE = /^[0-9a-f]{64}$/;
const ZERO = /^0+$/;
const SECONDS = /^[0-9]{1,10}$/;
const LOOPBACK = /^(?:127(?:\.(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])){3}|localhost|\[::1\]):([0-9]{1,5})$/;
const MAX_FRAGMENT_BYTES = 4096;

/**
 * Parses `#df_remote&node=…`, or the whole link that carries it. Every member
 * is checked before any of them becomes authority, an unknown member is
 * ignored, and an invitation whose own deadline or whose ticket has passed is
 * refused here rather than at the relay.
 */
export function parseInvitation(fragment: string, nowSeconds?: number): RemoteInvitation {
  if (typeof fragment !== "string" || fragment.length > MAX_FRAGMENT_BYTES) throw new SessionError("invalid_request");
  const hash = fragment.indexOf("#");
  const text = hash < 0 ? fragment : fragment.slice(hash + 1);
  // The marker is the first token, so a link that merely mentions df_remote
  // further along is never read as an invitation.
  if (!text.startsWith(`${MARKER}&`)) throw new SessionError("invalid_request");
  const members = new URLSearchParams(text.slice(MARKER.length + 1));
  const node = exact(members.get("node"), NODE_ID);
  const daemon = exact(members.get("daemon"), DAEMON_ID);
  const challenge = exact(members.get("challenge"), CHALLENGE);
  if (ZERO.test(daemon) || ZERO.test(challenge)) throw new SessionError("invalid_request");
  // An absent relay or host is the ordinary one; an empty one is a mistake.
  const relay = members.has("relay") ? relayOrigin(members.get("relay")) : DEFAULT_RELAY;
  const host = members.has("host") ? loopbackHost(members.get("host")) : DEFAULT_HOST;
  const expires = unixSeconds(SECONDS.test(members.get("expires") ?? "") ? Number(members.get("expires")) : undefined);
  const ticket = members.get("ticket");
  if (ticket === null) throw new SessionError("invalid_request");
  const parsed = parseTicket(ticket);
  // The ticket carries the node it is good for; an invitation that names a
  // different node would send a pairing proof to the wrong factory.
  if (parsed.purpose !== "pair" || parsed.node !== node) throw new SessionError("invalid_request");
  const now = nowSeconds ?? Math.floor(Date.now() / 1000);
  if (expires <= now || parsed.expires <= now) throw new SessionError("invalid_request");
  return Object.freeze({ relay, node, daemon, host, challenge, ticket, expires });
}

/**
 * Read-and-clear, exactly like the loopback pairing challenge: the fragment is
 * scrubbed from the address bar and from history before its content is
 * examined, so a one-shot invitation is never left where a reload can replay it.
 */
export function consumeInvitation(
  location: Pick<Location, "hash" | "pathname" | "search">,
  history?: Pick<History, "replaceState" | "state">,
  nowSeconds?: number,
): RemoteInvitation | null {
  const hash = location.hash;
  let decoded = hash;
  try { decoded = decodeURIComponent(hash); } catch { /* a malformed attempt still matches its raw key */ }
  if (!FRAGMENT_KEY.test(hash) && !FRAGMENT_KEY.test(decoded)) return null;
  const browserHistory = history ?? globalThis.history;
  browserHistory.replaceState(browserHistory.state, "", `${location.pathname}${location.search}`);
  try { return parseInvitation(hash, nowSeconds); } catch { return null; }
}

/** A bare `ws(s)://host:port` origin: no path, query, fragment or userinfo. */
export function relayOrigin(value: unknown): string {
  if (typeof value !== "string") throw new SessionError("invalid_request");
  let parsed: URL;
  try { parsed = new URL(value); } catch { throw new SessionError("invalid_request"); }
  if (parsed.origin !== value || parsed.username !== "" || parsed.password !== "") throw new SessionError("invalid_request");
  // factoryd's own rule: wss to anywhere, plain ws only to a loopback address,
  // which is the relay a `wrangler dev --local` run puts on this machine.
  if (parsed.protocol === "ws:") loopbackHost(parsed.host);
  else if (parsed.protocol !== "wss:") throw new SessionError("invalid_request");
  return value;
}

export function loopbackHost(value: unknown): string {
  if (typeof value !== "string") throw new SessionError("invalid_request");
  const match = LOOPBACK.exec(value);
  if (match === null) throw new SessionError("invalid_request");
  const port = match[1] as string;
  const numeric = Number(port);
  if (String(numeric) !== port || numeric < 1 || numeric > 65535) throw new SessionError("invalid_request");
  return value;
}

function exact(value: unknown, pattern: RegExp): string {
  if (typeof value !== "string" || !pattern.test(value)) throw new SessionError("invalid_request");
  return value;
}
