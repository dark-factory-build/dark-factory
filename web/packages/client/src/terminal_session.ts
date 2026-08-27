import {
  encodeTerminalAck,
  encodeTerminalAttach,
  encodeTerminalDetach,
  encodeTerminalLeaseAcquire,
  encodeTerminalLeaseRelease,
  encodeTerminalLeaseRenew,
  encodeTerminalResize,
  type ServerControlFrame,
  type TerminalInputResultBody,
  type TerminalLeaseResultBody,
  type TerminalServerControlFrame,
} from "./control.js";
import { ProtocolError } from "./errors.js";
import { MAX_ARRAY_ITEMS, MAX_SQLITE_INTEGER, MAX_TERMINAL_COLS, MAX_TERMINAL_PAYLOAD, MAX_TERMINAL_ROWS } from "./manifest.js";
import { decodeTerminalOutput, encodeTerminalInput, type TerminalFrame } from "./terminal.js";

export type TerminalOutput = {
  sequence: bigint;
  payload: Uint8Array;
};

export type TerminalAttached = Readonly<{
  sessionId: string;
  floor: bigint;
  head: bigint;
  acknowledgedSequence: bigint;
  maxUnackedBytes: bigint;
}>;
export type TerminalAttachReset = TerminalReset & { kind: "reset"; freshAttachRequired: true };
export type TerminalAttachOutcome = TerminalAttached | TerminalAttachReset;

export type TerminalLease = Readonly<{
  generation: bigint;
  expiresAtMs: bigint;
  lastInputSequence: bigint;
  runRevision: bigint;
  sessionRevision: bigint;
}>;
export type TerminalLeaseResult = Readonly<TerminalLeaseResultBody>;

export type TerminalInputResult = TerminalInputResultBody;
export type TerminalExit = { sessionId: string; exitCode: number; exitSignal: number; aborted: boolean };
export type TerminalReset = Readonly<{ sessionId: string; floor: bigint; head: bigint }>;

export type TerminalHandleOptions = {
  runId: string;
  sessionId: string;
  expectedRunRevision: bigint;
  expectedSessionRevision: bigint;
  afterSequence?: bigint;
  onOutput?: (output: TerminalOutput) => void | Promise<void>;
  onEOF?: (event: { sessionId: string }) => void | Promise<void>;
  onExit?: (event: TerminalExit) => void | Promise<void>;
  onReset?: (event: TerminalReset) => void | Promise<void>;
  onClose?: (error?: SessionErrorLike) => void;
};

export type SessionErrorLike = Error & { code?: string; retryable?: boolean };
type Send = (id: string | undefined, payload: string | Uint8Array) => void;
type NextID = (prefix: string) => string;

type Pending = {
  kind: "attach" | "lease" | "resize" | "detach" | "input";
  resolve: (value: unknown) => void;
  reject: (error: unknown) => void;
  generation?: bigint;
  leaseOperation?: "acquired" | "renewed" | "released";
  sequence?: bigint;
  payloadLength?: number;
  rows?: number;
  cols?: number;
};

type RetiredResponse = {
  id: string;
  type: "TERMINAL_ATTACHED" | "TERMINAL_LEASE_RESULT" | "TERMINAL_RESIZED" | "TERMINAL_DETACHED" | "TERMINAL_INPUT_RESULT" | "TERMINAL_RESET";
  generation?: bigint;
  sequence?: bigint;
};

const MAX_RETIRED_RESPONSES = 8;

/**
 * The sole owner of one browser terminal attachment. A handle is bound to
 * one socket generation and is intentionally not reconnectable: callers
 * must create a fresh handle and attach from their last accepted ACK.
 */
export class TerminalHandle {
  readonly runId: string;
  readonly sessionId: string;
  readonly expectedRunRevision: bigint;
  readonly expectedSessionRevision: bigint;
  readonly #send: Send;
  readonly #nextID: NextID;
  readonly #options: TerminalHandleOptions;
  readonly #pending = new Map<string, Pending>();
  readonly #retired: RetiredResponse[] = [];
  #attached = false;
  #closed = false;
  #attachedState: TerminalAttached | undefined;
  #attachmentID: string | undefined;
  #requestedAfterSequence = 0n;
  #lease: TerminalLease | undefined;
  #nextOutputSequence = 0n;
  #acknowledgedSequence = 0n;
  #nextInputSequence = 0n;
  #outputInFlight = false;
  #generationFloor = 0n;
  #inputSequenceExhausted = false;
  #outputCloseResolve: (() => void) | undefined;

  constructor(options: TerminalHandleOptions, send: Send, nextID: NextID) {
    this.#options = { ...options };
    this.runId = options.runId;
    this.sessionId = options.sessionId;
    this.expectedRunRevision = options.expectedRunRevision;
    this.expectedSessionRevision = options.expectedSessionRevision;
    this.#send = send;
    this.#nextID = nextID;
  }

  get attached(): boolean { return this.#attached; }
  get closed(): boolean { return this.#closed; }
  get attachedState(): TerminalAttached | undefined { return this.#attachedState === undefined ? undefined : frozenAttached(this.#attachedState); }
  get lease(): TerminalLease | undefined { return this.#lease === undefined ? undefined : frozenLease(this.#lease); }
  get acknowledgedSequence(): bigint { return this.#acknowledgedSequence; }
  get nextInputSequence(): bigint { return this.#nextInputSequence; }

  attach(): Promise<TerminalAttachOutcome> {
    this.#ensureOpen();
    if (this.#attached || this.#operationPending()) return Promise.reject(new SessionErrorLikeError("already attached"));
    const id = this.#nextID("terminal-attach");
    this.#attachmentID = id;
    const afterSequence = this.#options.afterSequence ?? this.#acknowledgedSequence;
    if (afterSequence < 0n) return Promise.reject(new ProtocolError("malformed"));
    this.#requestedAfterSequence = afterSequence;
    return this.#request(id, { kind: "attach", resolve: () => undefined, reject: () => undefined },
      encodeTerminalAttach(id, {
        run_id: this.runId,
        session_id: this.sessionId,
        expected_run_revision: this.expectedRunRevision,
        expected_session_revision: this.expectedSessionRevision,
        after_sequence: afterSequence,
      }));
  }

  acquireLease(): Promise<TerminalLease> {
    this.#ensureAttached();
    if (this.#generationFloor === MAX_SQLITE_INTEGER) return Promise.reject(new SessionErrorLikeError("generation exhausted"));
    if (this.#lease !== undefined || this.#operationPending()) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-acquire");
    return this.#request(id, { kind: "lease", leaseOperation: "acquired", resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseAcquire(id, {
      run_id: this.runId, session_id: this.sessionId,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  renewLease(): Promise<TerminalLease> {
    const lease = this.#requireLease();
    if (this.#operationPending()) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-renew");
    return this.#request(id, { kind: "lease", leaseOperation: "renewed", generation: lease.generation, resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseRenew(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  releaseLease(): Promise<TerminalLeaseResult> {
    const lease = this.#requireLease();
    if (lease.generation === MAX_SQLITE_INTEGER) return Promise.reject(new SessionErrorLikeError("generation exhausted"));
    if (this.#operationPending()) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-release");
    return this.#request(id, { kind: "lease", leaseOperation: "released", generation: lease.generation, resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseRelease(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  resize(rows: number, cols: number): Promise<{ sessionId: string; generation: bigint; rows: number; cols: number }> {
    const lease = this.#requireLease();
    if (this.#operationPending()) return Promise.reject(new SessionErrorLikeError("terminal operation pending"));
    if (!Number.isSafeInteger(rows) || rows < 1 || rows > MAX_TERMINAL_ROWS || !Number.isSafeInteger(cols) || cols < 1 || cols > MAX_TERMINAL_COLS) return Promise.reject(new ProtocolError("malformed"));
    const id = this.#nextID("terminal-resize");
    return this.#request(id, { kind: "resize", generation: lease.generation, rows, cols, resolve: () => undefined, reject: () => undefined }, encodeTerminalResize(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision, rows, cols,
    }));
  }

  sendInput(payload: Uint8Array): Promise<TerminalInputResult> {
    this.#ensureAttached();
    if (this.#inputSequenceExhausted) return Promise.reject(new SessionErrorLikeError("input sequence exhausted"));
    const lease = this.#requireLease();
    if (!(payload instanceof Uint8Array)) return Promise.reject(new ProtocolError("malformed"));
    if (this.#operationPending()) return Promise.reject(new SessionErrorLikeError("terminal operation pending"));
    const bytes = payload.slice();
    const sequence = this.#nextInputSequence;
    const encoded = encodeTerminalInput(hexSessionID(this.sessionId), sequence, lease.generation, bytes);
    const pending = this.#request(this.#nextID("terminal-input"), { kind: "input", generation: lease.generation, sequence, payloadLength: bytes.length, resolve: () => undefined, reject: () => undefined }, encoded);
    // A single handle does not pipeline input. This makes the sequence
    // chronology unambiguous even when a caller races two sendInput calls.
    return pending as Promise<TerminalInputResult>;
  }

  detach(): Promise<void> {
    this.#ensureOpen();
    if (this.#operationPending()) return Promise.reject(new SessionErrorLikeError("detach pending"));
    if (!this.#attached) { this.close(); return Promise.resolve(); }
    const id = this.#nextID("terminal-detach");
    return this.#request(id, { kind: "detach", resolve: () => undefined, reject: () => undefined }, encodeTerminalDetach(id, { session_id: this.sessionId }));
  }

  close(error?: SessionErrorLike): void {
    if (this.#closed) return;
    this.#closed = true;
    const wakeOutput = this.#outputCloseResolve;
    this.#outputCloseResolve = undefined;
    wakeOutput?.();
    for (const pending of this.#pending.values()) pending.reject(error ?? new SessionErrorLikeError("closed"));
    this.#pending.clear();
    this.#lease = undefined;
    this.#attached = false;
    this.#attachedState = undefined;
    try { this.#options.onClose?.(error); } catch { /* callbacks never own transport */ }
  }

  /** Called only by BrowserSession while routing a server terminal frame. */
  receive(frame: TerminalServerControlFrame): boolean {
    if (this.#closed) return this.#isRetired(frame);
    if (frame.body.session_id !== this.sessionId) return false;
    if (frame.type === "TERMINAL_INPUT_RESULT") {
      if (frame.id !== this.#attachmentID) return false;
      for (const [id, pending] of this.#pending) {
        if (pending.kind === "input" && pending.generation === frame.body.generation && pending.sequence === frame.body.sequence) {
          this.#inputResult(frame.body, pending);
          this.#pending.delete(id);
          return true;
        }
      }
      return false;
    }
    const pending = this.#pending.get(frame.id);
    if (pending === undefined) return false;
    switch (frame.type) {
      case "TERMINAL_ATTACHED": {
        if (pending.kind !== "attach" || frame.body.floor > frame.body.acknowledged_sequence || frame.body.acknowledged_sequence > frame.body.head || frame.body.acknowledged_sequence !== this.#requestedAfterSequence) throw new ProtocolError("malformed");
        this.#pending.delete(frame.id);
        this.#attached = true;
        this.#attachedState = frozenAttached({ sessionId: frame.body.session_id, floor: frame.body.floor, head: frame.body.head, acknowledgedSequence: frame.body.acknowledged_sequence, maxUnackedBytes: frame.body.max_unacked_bytes });
        this.#acknowledgedSequence = frame.body.acknowledged_sequence;
        this.#nextOutputSequence = frame.body.acknowledged_sequence;
        this.#nextInputSequence = 0n;
        pending.resolve(this.#attachedState);
        return true;
      }
      case "TERMINAL_LEASE_RESULT": {
        if (pending.kind !== "lease" || pending.leaseOperation !== frame.body.operation || frame.body.run_id !== this.runId || frame.body.session_id !== this.sessionId || frame.body.run_revision !== this.expectedRunRevision || frame.body.session_revision !== this.expectedSessionRevision) throw new ProtocolError("malformed");
        if (frame.body.operation === "released") {
          if (pending.generation === undefined || pending.generation === MAX_SQLITE_INTEGER || frame.body.generation !== pending.generation + 1n || frame.body.last_input_sequence !== 0n) throw new ProtocolError("malformed");
          this.#pending.delete(frame.id);
          this.#generationFloor = frame.body.generation > this.#generationFloor ? frame.body.generation : this.#generationFloor;
          this.#lease = undefined;
          pending.resolve(frozenLeaseResult(frame.body));
        } else {
          if (frame.body.expires_at_ms === undefined) throw new ProtocolError("malformed");
          if (frame.body.operation === "acquired") {
            if (pending.generation !== undefined || frame.body.last_input_sequence !== 0n || frame.body.generation <= this.#generationFloor) throw new ProtocolError("malformed");
            this.#generationFloor = frame.body.generation;
            this.#inputSequenceExhausted = false;
            this.#nextInputSequence = 1n;
            this.#lease = { generation: frame.body.generation, expiresAtMs: frame.body.expires_at_ms, lastInputSequence: 0n, runRevision: frame.body.run_revision, sessionRevision: frame.body.session_revision };
          } else {
            const lease = this.#lease;
            if (pending.generation === undefined || frame.body.generation !== pending.generation || lease === undefined || lease.generation !== pending.generation || frame.body.last_input_sequence !== lease.lastInputSequence || frame.body.expires_at_ms < lease.expiresAtMs) throw new ProtocolError("malformed");
            this.#pending.delete(frame.id);
            this.#generationFloor = frame.body.generation > this.#generationFloor ? frame.body.generation : this.#generationFloor;
            this.#lease = { ...lease, expiresAtMs: frame.body.expires_at_ms };
          }
          if (frame.body.operation === "acquired") this.#pending.delete(frame.id);
          pending.resolve(frozenLease(this.#lease!));
        }
        return true;
      }
      case "TERMINAL_RESIZED":
        if (pending.kind !== "resize" || frame.body.generation !== pending.generation || frame.body.rows !== pending.rows || frame.body.cols !== pending.cols) throw new ProtocolError("malformed");
        this.#pending.delete(frame.id);
        pending.resolve({ sessionId: frame.body.session_id, generation: frame.body.generation, rows: frame.body.rows, cols: frame.body.cols }); return true;
      case "TERMINAL_DETACHED":
        if (pending.kind !== "detach") throw new ProtocolError("malformed");
        this.#pending.delete(frame.id);
        this.#attached = false; this.#lease = undefined; pending.resolve(undefined); this.close(); return true;
      default: return false;
    }
  }

  receiveError(id: string, error: SessionErrorLike): boolean {
    if (this.#closed) return false;
    let pending = this.#pending.get(id);
    if (pending === undefined && id === this.#attachmentID) {
      for (const [pendingID, candidate] of this.#pending) {
        if (candidate.kind === "input") { this.#pending.delete(pendingID); pending = candidate; break; }
      }
    }
    if (pending === undefined) return false;
    this.#pending.delete(id);
    if (pending.kind === "input") this.#freezeInput(pending, error);
    pending.reject(error);
    return true;
  }

  async receiveBinary(frame: TerminalFrame): Promise<boolean> {
    if (this.#closed) return false;
    if (!this.#attached || frame.direction !== "output" || !(frame.payload instanceof Uint8Array) || frame.payload.length === 0 || frame.payload.length > MAX_TERMINAL_PAYLOAD || !sameBytes(frame.sessionId, hexSessionID(this.sessionId)) || frame.leaseGeneration !== 0n) return false;
    if (this.#outputInFlight) { this.close(new SessionErrorLikeError("output callback reentrant")); return true; }
    if (frame.sequence !== this.#nextOutputSequence) return false;
    this.#outputInFlight = true;
    const output = { sequence: frame.sequence, payload: frame.payload.slice() };
    let closeResolve: (() => void) | undefined;
    const closed = new Promise<void>((resolve) => { closeResolve = resolve; this.#outputCloseResolve = resolve; });
    try {
      let accepted: Promise<void>;
      try { accepted = Promise.resolve(this.#options.onOutput?.(output)); } catch { accepted = Promise.reject(new Error("consumer")); }
      try { await Promise.race([accepted, closed]); } catch {
        this.close(new SessionErrorLikeError("terminal output consumer failed"));
        return true;
      }
      if (this.#closed || !this.#attached) return true;
      const nextSequence = this.#nextOutputSequence + BigInt(frame.payload.length);
      try { this.#send(undefined, encodeTerminalAck({ session_id: this.sessionId, next_sequence: nextSequence })); } catch {
        this.close(new SessionErrorLikeError("terminal ACK send failed"));
        return true;
      }
      if (this.#closed || !this.#attached) return true;
      this.#nextOutputSequence = nextSequence;
      this.#acknowledgedSequence = nextSequence;
      return true;
    } finally {
      if (this.#outputCloseResolve === closeResolve) this.#outputCloseResolve = undefined;
      this.#outputInFlight = false;
    }
  }

  receiveEOF(id: string, body: { session_id: string }): boolean { if (this.#closed) return false; if (!this.#attached || id !== this.#attachmentID || body.session_id !== this.sessionId) return false; this.#observe(this.#options.onEOF, { sessionId: body.session_id }); return true; }
  receiveExit(id: string, body: TerminalExit): boolean { if (this.#closed) return false; if (!this.#attached || id !== this.#attachmentID || body.sessionId !== this.sessionId) return false; this.#observe(this.#options.onExit, body); return true; }
  receiveReset(id: string, body: { session_id: string; floor: bigint; head: bigint }): boolean {
    if (this.#closed) return this.#matchesRetired("TERMINAL_RESET", id, body.session_id);
    if (id !== this.#attachmentID || body.session_id !== this.sessionId) return false;
    const reset = frozenReset({ sessionId: body.session_id, floor: body.floor, head: body.head });
    this.#forgetRetired(id, "TERMINAL_ATTACHED");
    const pending = this.#pending.get(id);
    if (pending?.kind === "attach") {
      this.#rememberRetired({ id, type: "TERMINAL_RESET" });
      this.#pending.delete(id);
      pending.resolve(Object.freeze({ ...reset, kind: "reset", freshAttachRequired: true }));
    }
    this.close();
    this.#rememberRetired({ id, type: "TERMINAL_RESET" });
    this.#observe(this.#options.onReset, reset);
    return true;
  }

  #inputResult(body: TerminalInputResultBody, pending: Pending): void {
    if (pending.kind !== "input" || body.generation !== pending.generation || body.sequence !== pending.sequence || body.accepted_bytes > BigInt(pending.payloadLength ?? 0)) throw new ProtocolError("malformed");
    if (body.status === "accepted" && body.accepted_bytes !== BigInt(pending.payloadLength ?? 0) || body.status === "partial" && body.accepted_bytes === BigInt(pending.payloadLength ?? 0)) throw new ProtocolError("malformed");
    if (body.status === "rejected") {
      pending.resolve(body);
      return;
    }
    if (body.sequence === MAX_SQLITE_INTEGER) {
      this.#nextInputSequence = MAX_SQLITE_INTEGER;
      this.#inputSequenceExhausted = true;
    } else this.#nextInputSequence = body.sequence + 1n;
    if (body.status === "accepted") {
      const lease = this.#lease;
      if (lease === undefined) throw new ProtocolError("malformed");
      if (this.#inputSequenceExhausted) {
        this.#lease = undefined;
        this.#fenceGeneration(body.generation);
      } else this.#lease = { ...lease, lastInputSequence: body.sequence };
    } else if (body.status === "partial" || body.status === "uncertain") {
      this.#lease = undefined;
      this.#fenceGeneration(body.generation);
    }
    pending.resolve(body);
  }

  #request<T>(id: string, pending: Pending, payload: string | Uint8Array): Promise<T> {
    const result = new Promise<T>((resolve, reject) => { pending.resolve = resolve as (value: unknown) => void; pending.reject = reject; this.#pending.set(id, pending); });
    this.#rememberSent(id, pending);
    try { this.#send(id, payload); } catch {
      const safeError = new SessionErrorLikeError("connection");
      if (pending.kind === "input") this.#freezeInput(pending, safeError);
      else this.close(safeError);
    }
    return result;
  }

  #operationPending(): boolean { return this.#pending.size !== 0; }
  #freezeInput(pending: Pending, error: unknown): void {
    const sequence = pending.sequence ?? this.#nextInputSequence;
    if (sequence >= MAX_SQLITE_INTEGER) {
      this.#nextInputSequence = MAX_SQLITE_INTEGER;
      this.#inputSequenceExhausted = true;
    } else this.#nextInputSequence = sequence + 1n;
    this.#lease = undefined;
    if (pending.generation !== undefined) this.#fenceGeneration(pending.generation);
    this.close(error as SessionErrorLike);
  }

  #fenceGeneration(generation: bigint): void {
    const next = generation >= MAX_SQLITE_INTEGER ? MAX_SQLITE_INTEGER : generation + 1n;
    if (next > this.#generationFloor) this.#generationFloor = next;
  }

  #rememberSent(id: string, pending: Pending): void {
    const responseID = pending.kind === "input" ? this.#attachmentID : id;
    if (responseID === undefined) return;
    const type = pending.kind === "attach" ? "TERMINAL_ATTACHED" : pending.kind === "lease" ? "TERMINAL_LEASE_RESULT" : pending.kind === "resize" ? "TERMINAL_RESIZED" : pending.kind === "detach" ? "TERMINAL_DETACHED" : "TERMINAL_INPUT_RESULT";
    this.#rememberRetired({ id: responseID, type, generation: pending.generation, sequence: pending.sequence });
  }

  #rememberRetired(record: RetiredResponse): void {
    if (this.#retired.some((candidate) => candidate.id === record.id && candidate.type === record.type && candidate.generation === record.generation && candidate.sequence === record.sequence)) return;
    this.#retired.push(record);
    if (this.#retired.length > MAX_RETIRED_RESPONSES) this.#retired.shift();
  }

  #forgetRetired(id: string, type: RetiredResponse["type"]): void {
    for (let index = this.#retired.length - 1; index >= 0; index--) {
      const candidate = this.#retired[index];
      if (candidate !== undefined && candidate.id === id && candidate.type === type) this.#retired.splice(index, 1);
    }
  }

  #isRetired(frame: TerminalServerControlFrame): boolean {
    if (frame.type === "TERMINAL_EOF" || frame.type === "TERMINAL_EXIT") return false;
    const body = frame.body;
    const inputGeneration = frame.type === "TERMINAL_INPUT_RESULT" ? frame.body.generation : undefined;
    const inputSequence = frame.type === "TERMINAL_INPUT_RESULT" ? frame.body.sequence : undefined;
    return this.#matchesRetired(frame.type, frame.id, body.session_id, inputGeneration, inputSequence);
  }

  #matchesRetired(type: RetiredResponse["type"], id: string, sessionId: string, generation?: bigint, sequence?: bigint): boolean {
    return this.#retired.some((record) => record.id === id && record.type === type && sessionId === this.sessionId && (type !== "TERMINAL_INPUT_RESULT" || record.generation === generation && record.sequence === sequence));
  }

  #observe<T>(callback: ((event: T) => void | Promise<void>) | undefined, event: T): void {
    if (callback === undefined) return;
    try { void Promise.resolve(callback(event)).catch(() => undefined); } catch { /* observation callbacks are isolated */ }
  }

  #requireLease(): TerminalLease { this.#ensureAttached(); if (this.#lease === undefined) throw new SessionErrorLikeError("terminal lease required"); return this.#lease; }
  #ensureAttached(): void { this.#ensureOpen(); if (!this.#attached) throw new SessionErrorLikeError("terminal not attached"); }
  #ensureOpen(): void { if (this.#closed) throw new SessionErrorLikeError("closed"); }
}

/** Internal error with a deliberately finite message and no wire details. */
export class SessionErrorLikeError extends Error {
  readonly code: string;
  readonly retryable = false;
  constructor(code: string) { super(code); this.name = "SessionError"; this.code = code; }
}

export function terminalControlFrame(frame: ServerControlFrame): frame is TerminalServerControlFrame {
  return frame.type.startsWith("TERMINAL_");
}

export function decodeTerminalServer(data: Uint8Array): TerminalFrame { return decodeTerminalOutput(data); }

export function hexSessionID(value: string): Uint8Array {
  if (!/^[0-9a-f]{32}$/.test(value) || /^0+$/.test(value)) throw new ProtocolError("malformed");
  const result = new Uint8Array(16);
  for (let index = 0; index < 16; index++) result[index] = Number.parseInt(value.slice(index * 2, index * 2 + 2), 16);
  return result;
}

function sameBytes(a: Uint8Array, b: Uint8Array): boolean { return a.length === b.length && a.every((value, index) => value === b[index]); }

function frozenAttached(value: TerminalAttached): TerminalAttached { return Object.freeze({ ...value }); }
function frozenLease(value: TerminalLease): TerminalLease { return Object.freeze({ ...value }); }
function frozenLeaseResult(value: TerminalLeaseResultBody): TerminalLeaseResult { return Object.freeze({ ...value }); }
function frozenReset(value: TerminalReset): TerminalReset { return Object.freeze({ ...value }); }
