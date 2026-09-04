import { SessionError, type BrowserSocket, type BrowserSocketFactory } from "../session.js";
import { mintProof, parseTicket } from "./tokens.js";

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 3;

export const RELAY_SUBPROTOCOL = "dark-factory-relay";
/** A client-side refusal: the node answered, but not as the daemon we bound. */
export const RELAY_DAEMON_MISMATCH = 4900;
/** A client-side refusal: the ticket this dial would present is past its deadline. */
export const RELAY_TICKET_EXPIRED = 4901;
/** The relay's own reasons, which the manager turns into a factory status. */
export const RELAY_CLOSE = { factoryOffline: 4001, controllerRevoked: 4002 } as const;
/** This browser's clock is not the one that set `expires`; allow for the drift. */
const SKEW_SECONDS = 60;

export type RelaySocketOptions = {
  relayOrigin: string;
  nodeId: string;
  /**
   * Read at every dial, because each successful authentication mints the next
   * control ticket and a reconnect days later must present that one, not the
   * one this factory was built with.
   */
  ticket: () => string;
  key?: CryptoKey;
  expectedDaemonId: string;
  /** Unix seconds, read at every dial: the clock the ticket is judged against. */
  now?: () => number;
  webSocket?: typeof WebSocket;
  onTicket?: (ticket: string) => void;
  /**
   * One record per dial: the close code, whether this socket ever opened, and
   * whether this side ended it. A refusal this browser made is never a code a
   * peer could have sent instead.
   */
  onRelayClose?: (code: number, opened: boolean, local: boolean) => void;
};

export function relayControllerURL(relayOrigin: string, nodeId: string): string {
  return `${relayOrigin}/controller/${nodeId}`;
}

/**
 * A BrowserSocketFactory that reaches one factory through the relay. Everything
 * relay-shaped lives here: the session above it sends and receives exactly the
 * frames it would over loopback, and knows nothing about tickets or proofs.
 */
export function createRelaySocketFactory(options: RelaySocketOptions): BrowserSocketFactory {
  return () => new RelaySocket(options);
}

/**
 * Opening is asynchronous because a control dial must sign a fresh proof first,
 * so this wrapper reports CONNECTING until the inner socket exists. Every
 * generation calls the factory again, so every connection carries a new proof.
 */
class RelaySocket implements BrowserSocket {
  binaryType: BinaryType = "arraybuffer";
  onopen: ((event: unknown) => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  onclose: ((event: { code?: number; reason?: string }) => void) | null = null;

  #options: RelaySocketOptions;
  #url: string;
  #inner: WebSocket | undefined;
  #opened = false;
  #closed = false;

  constructor(options: RelaySocketOptions) {
    this.#options = options;
    this.#url = relayControllerURL(options.relayOrigin, options.nodeId);
    // No event may reach a caller before it has finished wiring its handlers,
    // so opening never begins inside the constructor.
    queueMicrotask(() => { void this.#open(); });
  }

  get readyState(): number {
    if (this.#closed) return CLOSED;
    return this.#inner === undefined ? CONNECTING : this.#inner.readyState;
  }

  send(data: string | Uint8Array): void {
    const inner = this.#inner;
    if (this.#closed || inner === undefined || inner.readyState !== OPEN) throw new SessionError("connection");
    inner.send(data as unknown as string);
  }

  close(code = 1000, reason = ""): void {
    if (this.#closed) return;
    this.#closed = true;
    const inner = this.#inner;
    this.#inner = undefined;
    detach(inner);
    try { inner?.close(code, reason); } catch { /* finite close semantics */ }
    // However a dial ends the owner hears how: a session that gives up on the
    // error a refused dial fires is the only close this socket ever sees.
    try { this.#options.onRelayClose?.(code, this.#opened, true); } catch { /* consumer callbacks cannot break socket ownership */ }
  }

  async #open(): Promise<void> {
    let socket: WebSocket;
    try {
      const ticket = this.#options.ticket();
      const parsed = parseTicket(ticket);
      // Every dial reads the deadline, including the ones BrowserClient starts
      // on its own backoff. Past it by more than the clock allowance the ticket
      // is not presented at all; inside that window the relay is the judge.
      if (parsed.expires + SKEW_SECONDS <= (this.#options.now?.() ?? Math.floor(Date.now() / 1000))) {
        this.#terminate(RELAY_TICKET_EXPIRED, "expired", true);
        return;
      }
      const protocols = [RELAY_SUBPROTOCOL, ticket];
      // A pair ticket is presented alone: the device key it will authorise does
      // not exist yet, so there is nothing to prove with.
      if (parsed.purpose === "control") {
        const key = this.#options.key;
        if (key === undefined) throw new SessionError("crypto_unavailable");
        protocols.push(await mintProof(parsed.ticket, key));
      }
      if (this.#closed) return;
      const construct: typeof WebSocket | undefined = this.#options.webSocket ?? globalThis.WebSocket;
      if (construct === undefined) throw new SessionError("connection");
      socket = new construct(this.#url, protocols);
    } catch {
      this.#terminate(1006, "connect", true);
      return;
    }
    if (this.#closed) {
      try { socket.close(1000, "closed"); } catch { /* the wrapper is already closed */ }
      return;
    }
    this.#inner = socket;
    try { socket.binaryType = "arraybuffer"; } catch { /* a fake socket may not expose it */ }
    socket.onopen = (event) => { this.#opened = true; this.onopen?.(event); };
    socket.onmessage = (event) => this.#deliver((event as { data: unknown }).data);
    socket.onerror = (event) => { if (!this.#closed) this.onerror?.(event); };
    socket.onclose = (event) => this.#terminate((event as { code?: number }).code ?? 1006, (event as { reason?: string }).reason ?? "", false);
  }

  /**
   * Relay routing is the only thing this wrapper reads, and it arrives in its
   * own frame. Everything else is handed on byte for byte: nothing here ever
   * re-encodes an application frame, so no decoder tolerance is required.
   */
  #deliver(data: unknown): void {
    if (this.#closed) return;
    if (typeof data !== "string") { this.onmessage?.({ data }); return; }
    let frame: { type?: unknown; ticket?: unknown; body?: { daemon_id?: unknown } } | null;
    try { frame = JSON.parse(data) as typeof frame; } catch { this.onmessage?.({ data }); return; }
    if (frame?.type === "RELAY_TICKET") {
      // The daemon mints the next control ticket beside the frame that proves
      // this connection. It is routing, not application state, so it is taken
      // here and never reaches the session.
      const ticket = frame.ticket;
      if (typeof ticket === "string") try { this.#options.onTicket?.(ticket); } catch { /* consumer callbacks cannot break socket ownership */ }
      return;
    }
    // The relay routes by node id, and a node id is a hash of the node key, but
    // the binding was made with one daemon. A different daemon answering here
    // is refused before its HELLO can seed a transcript.
    if (frame?.type === "HELLO" && frame.body?.daemon_id !== this.#options.expectedDaemonId) {
      this.#terminate(RELAY_DAEMON_MISMATCH, "daemon", true);
      return;
    }
    this.onmessage?.({ data });
  }

  #terminate(code: number, reason: string, local: boolean): void {
    if (this.#closed) return;
    this.#closed = true;
    const inner = this.#inner;
    this.#inner = undefined;
    detach(inner);
    if (local) try { inner?.close(code, reason); } catch { /* finite close semantics */ }
    try { this.#options.onRelayClose?.(code, this.#opened, local); } catch { /* consumer callbacks cannot break socket ownership */ }
    if (local) this.onerror?.({ code, reason });
    this.onclose?.({ code, reason });
  }
}

function detach(socket: WebSocket | undefined): void {
  if (socket === undefined) return;
  socket.onopen = null;
  socket.onmessage = null;
  socket.onerror = null;
  socket.onclose = null;
}
