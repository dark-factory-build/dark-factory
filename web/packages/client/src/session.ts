import {
  decodeServerControl,
  encodeAuthProve,
  encodePairProve,
  encodeStateEntityGet,
  encodeStateGet,
  encodeStateSubscribe,
  type AuthResultFrame,
  type EntityChangedEvent,
  type ErrorFrame,
  type HelloBody,
  type PairResultFrame,
  type ServerControlFrame,
  type StateEntityFrame,
  type StateEventFrame,
  type StateSnapshotFrame,
} from "./control.js";
import { ProtocolError, type ProtocolErrorCode } from "./errors.js";
import { CAPABILITIES, type CapabilityMask, type ErrorCode } from "./manifest.js";
import { StateAccumulator, type StateView } from "./state.js";
import { buildAuthTranscript, buildPairTranscript, hexBytes } from "./transcript.js";

const CLOSED = 3;
const PUBLIC_KEY_BYTES = 65;
const SIGNATURE_BYTES = 64;

export interface BrowserTimer {
  setTimeout(callback: () => void, delayMs: number): unknown;
  clearTimeout(handle: unknown): void;
}

const REAL_TIMER: BrowserTimer = {
  setTimeout: (callback, delayMs) => globalThis.setTimeout(callback, delayMs),
  clearTimeout: (handle) => globalThis.clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/** The intentionally tiny socket surface makes the client usable without a DOM test framework. */
export interface BrowserSocket {
  readonly readyState: number;
  binaryType?: BinaryType;
  onopen: ((event: unknown) => void) | null;
  onmessage: ((event: { data: unknown }) => void) | null;
  onerror: ((event: unknown) => void) | null;
  onclose: ((event: { code?: number; reason?: string }) => void) | null;
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

export type BrowserSocketFactory = (url: string) => BrowserSocket;

export type StoredClientKey = {
  clientId: string;
  publicKeySEC1: Uint8Array;
  key: CryptoKey;
  capabilities: CapabilityMask;
};

/** Storage is deliberately narrower than IndexedDB: tests can provide an in-memory store. */
export interface BrowserKeyStore {
  load(): Promise<StoredClientKey | null>;
  save(value: StoredClientKey): Promise<void>;
}

export type SessionErrorCode =
  | "connection"
  | "closed"
  | "pairing_required"
  | "pairing_uncertain"
  | "storage_unavailable"
  | "crypto_unavailable"
  | ProtocolErrorCode
  | ErrorCode;

export class SessionError extends Error {
  readonly code: SessionErrorCode;
  readonly retryable: boolean;

  constructor(code: SessionErrorCode, retryable = false) {
    super(code);
    this.name = "SessionError";
    this.code = code;
    this.retryable = retryable;
  }
}

export type SessionStatus = "idle" | "connecting" | "authenticating" | "syncing" | "ready" | "closed";

export type BrowserSessionOptions = {
  url: string;
  /** Exact values supplied by the already validated HTTP request. */
  host: string;
  origin: string;
  challenge?: string;
  keyStore?: BrowserKeyStore;
  socketFactory?: BrowserSocketFactory;
  crypto?: Crypto;
  timer?: BrowserTimer;
  reconnectInitialDelayMs?: number;
  reconnectMaxDelayMs?: number;
  onStatus?: (status: SessionStatus) => void;
  onState?: (state: StateView) => void;
  onError?: (error: SessionError | ProtocolError) => void;
};

type Pending = "pair" | "auth" | "snapshot" | "entity";
type PendingEntity = { kind: EntityChangedEvent["entity_kind"]; id: string };

/**
 * One connection generation. It owns one socket and one accumulator and is
 * never reused after close. BrowserClient is the optional reconnect owner.
 */
export class BrowserSession {
  #accumulator = new StateAccumulator();
  #options: BrowserSessionOptions;
  #socket: BrowserSocket | undefined;
  #status: SessionStatus = "idle";
  #hello: HelloBody | undefined;
  #clientId: string | undefined;
  #authenticated = false;
  #capabilities = 0;
  #key: CryptoKey | undefined;
  #publicKey: Uint8Array | undefined;
  #pending = new Map<string, Pending>();
  #entities = new Map<string, PendingEntity>();
  #subscriptionID: string | undefined;
  #requestNumber = 1;
  #closed = false;
  #pairing = false;
  #authPromise: Promise<void> | undefined;
  #incoming = Promise.resolve();
  #connectPromise: Promise<void> | undefined;
  #resolveConnect: (() => void) | undefined;
  #rejectConnect: ((error: unknown) => void) | undefined;

  constructor(options: BrowserSessionOptions) {
    this.#options = { ...options, keyStore: options.keyStore ?? createIndexedDBKeyStore() };
  }

  get status(): SessionStatus { return this.#status; }
  get state(): StateView | undefined { return this.#accumulator.current; }
  get clientId(): string | undefined { return this.#clientId; }
  get capabilities(): CapabilityMask { return this.#capabilities; }

  connect(): Promise<void> {
    if (this.#connectPromise !== undefined) return this.#connectPromise;
    if (this.#closed) return Promise.reject(new SessionError("closed"));
    this.#setStatus("connecting");
    this.#connectPromise = new Promise<void>((resolve, reject) => {
      this.#resolveConnect = resolve;
      this.#rejectConnect = reject;
      try {
        const factory = this.#options.socketFactory ?? ((url: string) => new WebSocket(url) as unknown as BrowserSocket);
        const socket = factory(this.#options.url);
        this.#socket = socket;
        socket.binaryType = "arraybuffer";
        socket.onopen = () => { /* HELLO is the first meaningful frame. */ };
        // Keep one reader owner: WebSocket callbacks may arrive while an
        // earlier handler is awaiting WebCrypto or IndexedDB.
        socket.onmessage = (event) => {
          this.#incoming = this.#incoming.then(() => this.#receive(event.data));
        };
        socket.onerror = () => this.#fail(new SessionError("connection"));
        socket.onclose = () => this.#closedByPeer();
      } catch {
        this.#fail(new SessionError("connection"));
      }
    });
    return this.#connectPromise;
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#pending.clear();
    this.#entities.clear();
    this.#accumulator.applyRestart("gap");
    const socket = this.#socket;
    this.#socket = undefined;
    try { socket?.close(1000, "closed"); } catch { /* finite close semantics */ }
    this.#rejectConnect?.(new SessionError("closed"));
    this.#rejectConnect = undefined;
    this.#resolveConnect = undefined;
    this.#setStatus("closed");
  }

  #setStatus(status: SessionStatus): void {
    this.#status = status;
    notify(this.#options.onStatus, status);
  }

  async #receive(data: unknown): Promise<void> {
    if (this.#closed) return;
    if (typeof data !== "string") {
      this.#fail(new ProtocolError("malformed"));
      return;
    }
    let frame: ServerControlFrame;
    try { frame = decodeServerControl(data); } catch (error) {
      this.#fail(error instanceof ProtocolError ? error : new ProtocolError("malformed"));
      return;
    }
    try {
      if (this.#hello === undefined) {
        if (frame.type !== "HELLO") throw new ProtocolError("wrong_direction");
        this.#hello = frame.body;
        await this.#authenticate();
        return;
      }
      if (!this.#authenticated) {
        await this.#authenticationFrame(frame);
        return;
      }
      this.#applicationFrame(frame);
    } catch (error) {
      this.#fail(error instanceof ProtocolError || error instanceof SessionError ? error : new SessionError("connection"));
    }
  }

  async #authenticate(): Promise<void> {
    if (this.#authPromise !== undefined) return this.#authPromise;
    this.#setStatus("authenticating");
    this.#authPromise = (async () => {
      let stored: StoredClientKey | null;
      try { stored = await this.#options.keyStore!.load(); } catch { throw new SessionError("storage_unavailable"); }
      this.#ensureLive();
      if (stored === null) {
        if (this.#options.challenge === undefined) throw new SessionError("pairing_required");
        await this.#pair();
      } else {
        this.#validateStored(stored);
        this.#key = stored.key;
        this.#publicKey = stored.publicKeySEC1.slice();
        this.#clientId = stored.clientId;
        this.#capabilities = stored.capabilities;
        const signature = await this.#sign(buildAuthTranscript({ ...this.#transcriptBase(), client_id: stored.clientId }));
        this.#ensureLive();
        const id = this.#nextID("auth");
        this.#sendAuth("auth", id, encodeAuthProve(id, { client_id: stored.clientId, signature }));
      }
    })();
    return this.#authPromise;
  }

  async #pair(): Promise<void> {
    this.#pairing = true;
    const crypto = this.#crypto();
    let generated: CryptoKeyPair;
    try {
      generated = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]) as CryptoKeyPair;
      const publicKey = await crypto.subtle.exportKey("raw", generated.publicKey);
      if (!(publicKey instanceof ArrayBuffer) || publicKey.byteLength !== PUBLIC_KEY_BYTES) throw new SessionError("crypto_unavailable");
      this.#key = generated.privateKey;
      this.#publicKey = new Uint8Array(publicKey);
      this.#ensureLive();
    } catch (error) {
      if (error instanceof SessionError) throw error;
      throw new SessionError("crypto_unavailable");
    }
    const challenge = this.#options.challenge;
    if (challenge === undefined) throw new SessionError("pairing_required");
    const signature = await this.#sign(buildPairTranscript({ ...this.#transcriptBase(), challenge, public_key_sec1: toHex(this.#publicKey) }));
    this.#ensureLive();
    const id = this.#nextID("pair");
    this.#sendAuth("pair", id, encodePairProve(id, { challenge, public_key_sec1: toHex(this.#publicKey), signature }));
  }

  async #authenticationFrame(frame: ServerControlFrame): Promise<void> {
    if (frame.type === "ERROR") throw new SessionError(frame.body.code, frame.body.retryable);
    if (frame.type === "PAIR_RESULT") {
      if (!this.#pairing || !this.#pending.has(frame.id) || this.#pending.get(frame.id) !== "pair") throw new ProtocolError("malformed");
      this.#pending.delete(frame.id);
      await this.#finishPair(frame);
    } else if (frame.type === "AUTH_RESULT") {
      if (this.#pairing || !this.#pending.has(frame.id) || this.#pending.get(frame.id) !== "auth") throw new ProtocolError("malformed");
      this.#pending.delete(frame.id);
      this.#finishAuth(frame);
    } else {
      throw new ProtocolError("wrong_direction");
    }
  }

  async #finishPair(frame: PairResultFrame): Promise<void> {
    const key = this.#key;
    const publicKey = this.#publicKey;
    if (key === undefined || publicKey === undefined) throw new SessionError("crypto_unavailable");
    this.#ensureLive();
    try {
      await this.#options.keyStore!.save({ clientId: frame.body.client_id, publicKeySEC1: publicKey.slice(), key, capabilities: frame.body.capabilities });
    } catch { throw new SessionError("storage_unavailable"); }
    this.#ensureLive();
    this.#pairing = false;
    this.#options = { ...this.#options, challenge: undefined };
    this.#clientId = frame.body.client_id;
    this.#capabilities = frame.body.capabilities;
    this.#authenticated = true;
    this.#ready();
  }

  #finishAuth(frame: AuthResultFrame): void {
    if (frame.body.client_id !== this.#clientId) throw new ProtocolError("unauthorized");
    this.#capabilities = frame.body.capabilities;
    this.#authenticated = true;
    this.#ready();
  }

  #ready(): void {
    this.#setStatus("syncing");
    this.#ensureLive();
    this.#resolveConnect?.();
    this.#resolveConnect = undefined;
    this.#rejectConnect = undefined;
    this.#beginSnapshot(null);
  }

  #applicationFrame(frame: ServerControlFrame): void {
    switch (frame.type) {
      case "STATE_SNAPSHOT": this.#snapshot(frame); return;
      case "STATE_RESTART": this.#resync(frame.body.reason); return;
      case "STATE_EVENT": this.#event(frame); return;
      case "STATE_ENTITY": this.#entity(frame); return;
      case "ERROR": this.#errorFrame(frame); return;
      default: throw new ProtocolError("wrong_direction");
    }
  }

  #snapshot(frame: StateSnapshotFrame): void {
    if (this.#pending.get(frame.id) === "snapshot") this.#pending.delete(frame.id);
    const result = this.#accumulator.applySnapshot(frame);
    if (result.kind === "restart") { this.#resync(result.reason); return; }
    if (result.kind === "published") {
      notify(this.#options.onState, result.state);
      this.#ensureLive();
      this.#setStatus("ready");
      const id = this.#nextID("watch");
      this.#subscriptionID = id;
      this.#send(id, encodeStateSubscribe(id, { after: result.state.head }));
    } else if (result.kind === "staged") {
      this.#beginSnapshot(this.#accumulator.nextCursor);
    }
  }

  #event(frame: StateEventFrame): void {
    if (frame.id !== this.#subscriptionID) throw new ProtocolError("malformed");
    const result = this.#accumulator.apply(frame);
    if (result.kind === "restart") { this.#resync(result.reason); return; }
    if (result.kind === "applied" || result.kind === "ignored") {
      notify(this.#options.onState, result.state);
      this.#ensureLive();
      if (frame.body.event === "entity_changed" && !frame.body.deleted) this.#refresh(frame.body);
    }
  }

  #refresh(event: EntityChangedEvent): void {
    const id = this.#nextID("entity");
    this.#entities.set(id, { kind: event.entity_kind, id: event.entity_id });
    this.#pending.set(id, "entity");
    this.#send(id, encodeStateEntityGet(id, { kind: event.entity_kind, id: event.entity_id }));
  }

  #entity(frame: StateEntityFrame): void {
    const expected = this.#entities.get(frame.id);
    if (expected === undefined || expected.kind !== frame.body.kind || expected.id !== frame.body.id) throw new ProtocolError("malformed");
    this.#entities.delete(frame.id);
    this.#pending.delete(frame.id);
    const result = this.#accumulator.applyEntity(frame.body);
    if (result.kind === "restart") this.#resync(result.reason);
    else if (result.kind === "applied" || result.kind === "ignored") {
      notify(this.#options.onState, result.state);
      this.#ensureLive();
    }
  }

  #errorFrame(frame: ErrorFrame): void {
    const id = frame.id;
    if (id !== undefined && this.#pending.has(id)) {
      this.#pending.delete(id);
    }
    throw new SessionError(frame.body.code, frame.body.retryable);
  }

  #beginSnapshot(cursor: string | null): void {
    const result = this.#accumulator.beginSnapshot(cursor);
    if (result.kind === "restart") { this.#resync(result.reason); return; }
    if (result.kind !== "requested") throw new ProtocolError("malformed");
    this.#pending.set(result.request.id, "snapshot");
    this.#send(result.request.id, encodeStateGet(result.request.id, { cursor }));
  }

  #resync(_reason: string): void {
    this.#pending.clear();
    this.#entities.clear();
    this.#subscriptionID = undefined;
    this.#accumulator.applyRestart("gap");
    // A restart clears the accumulator's published and staged state. No old
    // request is replayed; this is a fresh canonical fetch on this generation.
    this.#beginSnapshot(null);
  }

  #sendAuth(kind: "pair" | "auth", id: string, payload: string): void {
    this.#pending.set(id, kind);
    this.#send(id, payload);
  }

  #send(id: string, payload: string): void {
    if (this.#closed || this.#socket === undefined || this.#socket.readyState === CLOSED) throw new SessionError("closed");
    try { this.#socket.send(payload); } catch { throw new SessionError("connection"); }
  }

  #nextID(prefix: string): string { return `${prefix}-${this.#requestNumber++}`; }

  #transcriptBase(): { daemon_id: string; boot_id: string; connection_nonce: string; host: string; origin: string } {
    const hello = this.#hello;
    if (hello === undefined) throw new ProtocolError("unauthorized");
    return { daemon_id: hello.daemon_id, boot_id: hello.boot_id, connection_nonce: hello.connection_nonce, host: this.#options.host, origin: this.#options.origin };
  }

  #crypto(): Crypto {
    const value = this.#options.crypto ?? globalThis.crypto;
    if (value === undefined) throw new SessionError("crypto_unavailable");
    return value;
  }

  async #sign(message: Uint8Array): Promise<string> {
    const key = this.#key;
    if (key === undefined || key.extractable || key.algorithm.name !== "ECDSA") throw new SessionError("crypto_unavailable");
    try {
      const signed = await this.#crypto().subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, message as unknown as BufferSource);
      this.#ensureLive();
      if (!(signed instanceof ArrayBuffer) || signed.byteLength !== SIGNATURE_BYTES) throw new SessionError("crypto_unavailable");
      return toHex(new Uint8Array(signed));
    } catch (error) {
      if (error instanceof SessionError) throw error;
      throw new SessionError("crypto_unavailable");
    }
  }

  #validateStored(value: StoredClientKey): void {
    const knownCapabilities = CAPABILITIES.observe | CAPABILITIES.private_human_request_detail | CAPABILITIES.human_actions | CAPABILITIES.terminal_input;
    if (value === null || typeof value !== "object") throw new SessionError("storage_unavailable");
    const algorithm = value.key?.algorithm as EcKeyAlgorithm | undefined;
    if (typeof value.clientId !== "string" || !(value.publicKeySEC1 instanceof Uint8Array) || value.key === undefined || value.key === null || value.publicKeySEC1.length !== PUBLIC_KEY_BYTES || value.publicKeySEC1[0] !== 4 || value.key.extractable || algorithm?.name !== "ECDSA" || algorithm.namedCurve !== "P-256" || !Number.isSafeInteger(value.capabilities) || value.capabilities < CAPABILITIES.observe || (value.capabilities & ~knownCapabilities) !== 0) throw new SessionError("storage_unavailable");
    // Exercise the same strict fixed-width boundary used by the wire codec.
    try { hexBytes(value.clientId, 16); } catch { throw new SessionError("storage_unavailable"); }
  }

  #fail(error: SessionError | ProtocolError): void {
    if (this.#closed) return;
    if (this.#pairing && !(error instanceof ProtocolError) && (error.code === "connection" || error.code === "closed")) error = new SessionError("pairing_uncertain");
    this.#closed = true;
    this.#pending.clear();
    this.#entities.clear();
    this.#accumulator.applyRestart("gap");
    const socket = this.#socket;
    this.#socket = undefined;
    try { socket?.close(1000, "protocol"); } catch { /* ignore close failures */ }
    this.#rejectConnect?.(error);
    this.#rejectConnect = undefined;
    this.#resolveConnect = undefined;
    notify(this.#options.onError, error);
    this.#setStatus("closed");
  }

  #ensureLive(): void {
    if (this.#closed) throw new SessionError("closed");
  }

  #closedByPeer(): void {
    if (this.#closed) return;
    this.#fail(this.#pairing ? new SessionError("pairing_uncertain") : new SessionError("connection"));
  }
}

/** Reconnect owner. Each connect creates a new BrowserSession and accumulator. */
export class BrowserClient {
  #options: BrowserSessionOptions;
  #session: BrowserSession | undefined;
  #generation = 0;
  #closed = false;
  #reconnect = true;
  #timer: BrowserTimer;
  #timerHandle: unknown;
  #reconnectAttempt = 0;
  #initialDelay: number;
  #maxDelay: number;

  constructor(options: BrowserSessionOptions) {
    this.#options = options;
    this.#timer = options.timer ?? REAL_TIMER;
    this.#initialDelay = reconnectDelay(options.reconnectInitialDelayMs, 100);
    this.#maxDelay = Math.max(this.#initialDelay, reconnectDelay(options.reconnectMaxDelayMs, 5_000));
  }
  get session(): BrowserSession | undefined { return this.#session; }
  get state(): StateView | undefined { return this.#session?.state; }
  get status(): SessionStatus { return this.#session?.status ?? "idle"; }

  connect(): Promise<void> {
    this.#cancelTimer();
    this.#reconnectAttempt = 0;
    this.#closed = false;
    this.#reconnect = true;
    return this.#newSession().connect();
  }

  close(): void {
    this.#closed = true;
    this.#reconnect = false;
    this.#cancelTimer();
    this.#session?.close();
  }

  #newSession(): BrowserSession {
    const generation = ++this.#generation;
    this.#session?.close();
    let reconnectable = true;
    const session = new BrowserSession({
      ...this.#options,
      onStatus: (status) => {
        if (generation !== this.#generation) return;
        if (session.clientId !== undefined && this.#options.challenge !== undefined) this.#options = { ...this.#options, challenge: undefined };
        if (status === "ready") this.#reconnectAttempt = 0;
        if (status === "closed" && reconnectable && this.#reconnect && !this.#closed && (this.#options.challenge === undefined || session.clientId !== undefined)) this.#schedule(generation);
        notify(this.#options.onStatus, status);
      },
      onState: (state) => { if (generation === this.#generation) notify(this.#options.onState, state); },
      onError: (error) => {
        if (generation !== this.#generation) return;
        reconnectable = error instanceof SessionError && (error.code === "connection" || error.retryable);
        notify(this.#options.onError, error);
      },
    });
    this.#session = session;
    return session;
  }

  #schedule(generation: number): void {
    if (this.#timerHandle !== undefined || generation !== this.#generation) return;
    const exponent = Math.min(this.#reconnectAttempt, 6);
    const delay = Math.min(this.#maxDelay, this.#initialDelay * 2 ** exponent);
    this.#reconnectAttempt += 1;
    try {
      this.#timerHandle = this.#timer.setTimeout(() => {
        // Clear ownership before every exit path, including a stale generation.
        this.#timerHandle = undefined;
        if (this.#closed || !this.#reconnect || generation !== this.#generation) return;
        const next = this.#newSession();
        void next.connect().catch(() => { /* status/error callback is the public signal */ });
      }, delay);
    } catch {
      this.#timerHandle = undefined;
    }
  }

  #cancelTimer(): void {
    const handle = this.#timerHandle;
    if (handle === undefined) return;
    this.#timerHandle = undefined;
    try { this.#timer.clearTimeout(handle); } catch { /* timer cleanup is best-effort */ }
  }
}

function reconnectDelay(value: number | undefined, fallback: number): number {
  return value !== undefined && Number.isFinite(value) && value >= 1 ? Math.min(Math.floor(value), 60_000) : fallback;
}

function notify<T extends readonly unknown[]>(callback: ((...args: T) => void) | undefined, ...args: T): void {
  try { callback?.(...args); } catch { /* consumer callbacks cannot break protocol ownership */ }
}

export function createBrowserClient(options: BrowserSessionOptions): BrowserClient { return new BrowserClient(options); }

/** Read-and-clear a one-shot challenge fragment before constructing a session. */
export function consumePairingChallenge(location: Pick<Location, "hash" | "pathname" | "search">, history?: Pick<History, "replaceState">): string | null {
  const hash = location.hash.startsWith("#") ? location.hash.slice(1) : location.hash;
  const value = new URLSearchParams(hash).get("challenge");
  if (value === null) return null;
  const target = `${location.pathname}${location.search}`;
  (history ?? globalThis.history).replaceState(null, "", target);
  return value;
}

/** Native IndexedDB store for the non-exportable profile key. No localStorage fallback exists. */
export class IndexedDBKeyStore implements BrowserKeyStore {
  #database: string;
  #object = "client";
  constructor(database = "dark-factory-browser-v1") { this.#database = database; }

  async load(): Promise<StoredClientKey | null> {
    const record = await this.#request("readonly", (store) => store.get("profile"));
    return record === undefined ? null : record as StoredClientKey;
  }

  async save(value: StoredClientKey): Promise<void> {
    if (value.key.extractable) throw new SessionError("storage_unavailable");
    await this.#request("readwrite", (store) => store.put(value, "profile"));
  }

  #request(mode: IDBTransactionMode, operation: (store: IDBObjectStore) => IDBRequest): Promise<unknown> {
    const indexed = globalThis.indexedDB;
    if (indexed === undefined) return Promise.reject(new SessionError("storage_unavailable"));
    return new Promise((resolve, reject) => {
      const open = indexed.open(this.#database, 1);
      open.onerror = () => reject(new SessionError("storage_unavailable"));
      open.onupgradeneeded = () => { if (!open.result.objectStoreNames.contains(this.#object)) open.result.createObjectStore(this.#object); };
      open.onsuccess = () => {
        const database = open.result;
        try {
          const request = operation(database.transaction(this.#object, mode).objectStore(this.#object));
          request.onerror = () => reject(new SessionError("storage_unavailable"));
          request.onsuccess = () => resolve(request.result);
        } catch { reject(new SessionError("storage_unavailable")); }
        database.close();
      };
    });
  }
}

export function createIndexedDBKeyStore(database?: string): BrowserKeyStore { return new IndexedDBKeyStore(database); }

function toHex(value: Uint8Array | undefined): string {
  if (value === undefined) throw new SessionError("crypto_unavailable");
  return [...value].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}
