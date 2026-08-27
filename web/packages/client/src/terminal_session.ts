import {
  encodeHumanRequestCancelRun,
  encodeHumanRequestReply,
  encodeTerminalAck,
  encodeTerminalAttach,
  encodeTerminalDetach,
  encodeTerminalLeaseAcquire,
  encodeTerminalLeaseRelease,
  encodeTerminalLeaseRenew,
  encodeTerminalResize,
  type HumanRequestActionResultBody,
  type HumanRequestReplyResultBody,
  type ServerControlFrame,
  type TerminalInputResultBody,
  type TerminalLeaseResultBody,
  type TerminalServerControlFrame,
} from "./control.js";
import { ProtocolError } from "./errors.js";
import { MAX_SQLITE_INTEGER, MAX_TERMINAL_COLS, MAX_TERMINAL_ROWS } from "./manifest.js";
import { decodeTerminalOutput, encodeTerminalInput, type TerminalFrame } from "./terminal.js";

export type TerminalOutput = {
  sequence: bigint;
  payload: Uint8Array;
};

export type TerminalAttached = {
  sessionId: string;
  floor: bigint;
  head: bigint;
  acknowledgedSequence: bigint;
  maxUnackedBytes: bigint;
};
export type TerminalAttachReset = TerminalReset & { kind: "reset"; freshAttachRequired: true };
export type TerminalAttachOutcome = TerminalAttached | TerminalAttachReset;

export type TerminalLease = {
  generation: bigint;
  expiresAtMs: bigint;
  lastInputSequence: bigint;
  runRevision: bigint;
  sessionRevision: bigint;
};

export type TerminalInputResult = TerminalInputResultBody;
export type TerminalExit = { sessionId: string; exitCode: number; exitSignal: number; aborted: boolean };
export type TerminalReset = { sessionId: string; floor: bigint; head: bigint };

export type TerminalHandleOptions = {
  runId: string;
  sessionId: string;
  expectedRunRevision: bigint;
  expectedSessionRevision: bigint;
  afterSequence?: bigint;
  onOutput?: (output: TerminalOutput) => void | Promise<void>;
  onEOF?: (event: { sessionId: string }) => void;
  onExit?: (event: TerminalExit) => void;
  onReset?: (event: TerminalReset) => void;
  onClose?: (error?: SessionErrorLike) => void;
};

export type TerminalReplyRequest = {
  runId: string;
  requestId: string;
  expectedRevision: bigint;
  reply: string;
};

export type TerminalCancelRunRequest = {
  runId: string;
  requestId: string;
  expectedRequestRevision: bigint;
  expectedRunRevision: bigint;
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
  #requiresFreshLeaseAfterGeneration: bigint | undefined;
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
  get attachedState(): TerminalAttached | undefined { return this.#attachedState; }
  get lease(): TerminalLease | undefined { return this.#lease; }
  get acknowledgedSequence(): bigint { return this.#acknowledgedSequence; }
  get nextInputSequence(): bigint { return this.#nextInputSequence; }

  attach(): Promise<TerminalAttachOutcome> {
    this.#ensureOpen();
    if (this.#attached || this.#hasPending("attach")) return Promise.reject(new SessionErrorLikeError("already attached"));
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
    if (this.#lease !== undefined || this.#hasLeaseOperation() || this.#hasPending("resize") || this.#hasPending("input")) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-acquire");
    return this.#request(id, { kind: "lease", leaseOperation: "acquired", resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseAcquire(id, {
      run_id: this.runId, session_id: this.sessionId,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  renewLease(): Promise<TerminalLease> {
    const lease = this.#requireLease();
    if (this.#hasLeaseOperation() || this.#hasPending("resize") || this.#hasPending("input")) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-renew");
    return this.#request(id, { kind: "lease", leaseOperation: "renewed", generation: lease.generation, resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseRenew(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  releaseLease(): Promise<TerminalLeaseResultBody> {
    const lease = this.#requireLease();
    if (this.#hasLeaseOperation() || this.#hasPending("resize") || this.#hasPending("input")) return Promise.reject(new SessionErrorLikeError("lease operation pending"));
    const id = this.#nextID("terminal-lease-release");
    return this.#request(id, { kind: "lease", leaseOperation: "released", generation: lease.generation, resolve: () => undefined, reject: () => undefined }, encodeTerminalLeaseRelease(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision,
    }));
  }

  resize(rows: number, cols: number): Promise<{ sessionId: string; generation: bigint; rows: number; cols: number }> {
    const lease = this.#requireLease();
    if (this.#hasPending("resize") || this.#hasLeaseOperation() || this.#hasPending("input")) return Promise.reject(new SessionErrorLikeError("terminal operation pending"));
    if (!Number.isSafeInteger(rows) || rows < 1 || rows > MAX_TERMINAL_ROWS || !Number.isSafeInteger(cols) || cols < 1 || cols > MAX_TERMINAL_COLS) return Promise.reject(new ProtocolError("malformed"));
    const id = this.#nextID("terminal-resize");
    return this.#request(id, { kind: "resize", generation: lease.generation, rows, cols, resolve: () => undefined, reject: () => undefined }, encodeTerminalResize(id, {
      run_id: this.runId, session_id: this.sessionId, generation: lease.generation,
      expected_run_revision: this.expectedRunRevision,
      expected_session_revision: this.expectedSessionRevision, rows, cols,
    }));
  }

  sendInput(payload: Uint8Array): Promise<TerminalInputResult> {
    const lease = this.#requireLease();
    if (!(payload instanceof Uint8Array)) return Promise.reject(new ProtocolError("malformed"));
    if (this.#hasPending("input") || this.#hasLeaseOperation() || this.#hasPending("resize")) return Promise.reject(new SessionErrorLikeError("terminal operation pending"));
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
    if (!this.#attached) { this.close(); return Promise.resolve(); }
    if (this.#hasPending("detach")) return Promise.reject(new SessionErrorLikeError("detach pending"));
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
    try { this.#options.onClose?.(error); } catch { /* callbacks never own transport */ }
  }

  /** Called only by BrowserSession while routing a server terminal frame. */
  receive(frame: TerminalServerControlFrame): boolean {
    if (this.#closed) return frame.body.session_id === this.sessionId;
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
        this.#attachedState = { sessionId: frame.body.session_id, floor: frame.body.floor, head: frame.body.head, acknowledgedSequence: frame.body.acknowledged_sequence, maxUnackedBytes: frame.body.max_unacked_bytes };
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
          this.#lease = undefined;
          pending.resolve(frame.body);
        } else {
          if (frame.body.expires_at_ms === undefined) throw new ProtocolError("malformed");
          if (frame.body.operation === "acquired") {
            if (pending.generation !== undefined || frame.body.last_input_sequence !== 0n || this.#requiresFreshLeaseAfterGeneration !== undefined && frame.body.generation <= this.#requiresFreshLeaseAfterGeneration) throw new ProtocolError("malformed");
            this.#requiresFreshLeaseAfterGeneration = undefined;
            this.#nextInputSequence = 1n;
            this.#lease = { generation: frame.body.generation, expiresAtMs: frame.body.expires_at_ms, lastInputSequence: 0n, runRevision: frame.body.run_revision, sessionRevision: frame.body.session_revision };
          } else {
            const lease = this.#lease;
            if (pending.generation === undefined || frame.body.generation !== pending.generation || lease === undefined || lease.generation !== pending.generation || frame.body.last_input_sequence !== lease.lastInputSequence || frame.body.expires_at_ms < lease.expiresAtMs) throw new ProtocolError("malformed");
            this.#pending.delete(frame.id);
            this.#lease = { ...lease, expiresAtMs: frame.body.expires_at_ms };
          }
          if (frame.body.operation === "acquired") this.#pending.delete(frame.id);
          pending.resolve(this.#lease);
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
    if (this.#closed) return true;
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
    if (this.#closed) return sameBytes(frame.sessionId, hexSessionID(this.sessionId));
    if (!this.#attached || !sameBytes(frame.sessionId, hexSessionID(this.sessionId)) || frame.leaseGeneration !== 0n) return false;
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

  receiveEOF(id: string, body: { session_id: string }): boolean { if (this.#closed) return id === this.#attachmentID && body.session_id === this.sessionId; if (!this.#attached || id !== this.#attachmentID || body.session_id !== this.sessionId) return false; try { this.#options.onEOF?.({ sessionId: body.session_id }); } catch { /* observation callbacks are isolated */ } return true; }
  receiveExit(id: string, body: TerminalExit): boolean { if (this.#closed) return id === this.#attachmentID && body.sessionId === this.sessionId; if (!this.#attached || id !== this.#attachmentID || body.sessionId !== this.sessionId) return false; try { this.#options.onExit?.(body); } catch { /* observation callbacks are isolated */ } return true; }
  receiveReset(id: string, body: { session_id: string; floor: bigint; head: bigint }): boolean {
    if (this.#closed) return id === this.#attachmentID && body.session_id === this.sessionId;
    if (id !== this.#attachmentID || body.session_id !== this.sessionId) return false;
    const reset = { sessionId: body.session_id, floor: body.floor, head: body.head };
    try { this.#options.onReset?.(reset); } catch { /* observation callbacks are isolated */ }
    const pending = this.#pending.get(id);
    if (pending?.kind === "attach") {
      this.#pending.delete(id);
      pending.resolve({ ...reset, kind: "reset", freshAttachRequired: true });
    }
    this.close();
    return true;
  }

  #inputResult(body: TerminalInputResultBody, pending: Pending): void {
    if (pending.kind !== "input" || body.generation !== pending.generation || body.sequence !== pending.sequence || body.accepted_bytes > BigInt(pending.payloadLength ?? 0)) throw new ProtocolError("malformed");
    if (body.status === "accepted" && body.accepted_bytes !== BigInt(pending.payloadLength ?? 0) || body.status === "partial" && body.accepted_bytes === BigInt(pending.payloadLength ?? 0)) throw new ProtocolError("malformed");
    this.#nextInputSequence = body.sequence + 1n;
    if (body.status === "accepted") {
      const lease = this.#lease;
      if (lease === undefined) throw new ProtocolError("malformed");
      this.#lease = { ...lease, lastInputSequence: body.sequence };
    } else if (body.status === "partial" || body.status === "uncertain") {
      this.#lease = undefined;
      this.#requiresFreshLeaseAfterGeneration = body.generation;
    }
    pending.resolve(body);
  }

  #request<T>(id: string, pending: Pending, payload: string | Uint8Array): Promise<T> {
    const result = new Promise<T>((resolve, reject) => { pending.resolve = resolve as (value: unknown) => void; pending.reject = reject; this.#pending.set(id, pending); });
    try { this.#send(id, payload); } catch (error) {
      this.#pending.delete(id);
      const safeError = new SessionErrorLikeError("connection");
      if (pending.kind === "input") this.#freezeInput(pending, safeError);
      pending.reject(safeError);
    }
    return result;
  }

  #hasPending(kind: Pending["kind"]): boolean { for (const pending of this.#pending.values()) if (pending.kind === kind) return true; return false; }
  #hasLeaseOperation(): boolean { return this.#hasPending("lease"); }
  #freezeInput(pending: Pending, error: unknown): void {
    this.#nextInputSequence = (pending.sequence ?? this.#nextInputSequence) + 1n;
    this.#lease = undefined;
    this.#requiresFreshLeaseAfterGeneration = pending.generation;
    this.close(error as SessionErrorLike);
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

export type HumanReplyResult = HumanRequestReplyResultBody;
export type HumanCancelResult = HumanRequestActionResultBody;

export class HumanRequestClient {
  readonly #send: Send;
  readonly #nextID: NextID;
  readonly #pending = new Map<string, { kind: "reply" | "cancel"; resolve: (value: unknown) => void; reject: (error: unknown) => void; requestId: string; runId: string; expectedRevision: bigint; expectedRunRevision: bigint }>();
  readonly #used = new Set<string>();
  constructor(send: Send, nextID: NextID) { this.#send = send; this.#nextID = nextID; }
  reply(request: TerminalReplyRequest): Promise<HumanReplyResult> {
    if (this.#used.has(request.requestId)) return Promise.reject(new SessionErrorLikeError("human request already submitted"));
    this.#used.add(request.requestId);
    const id = this.#nextID("human-reply");
    return this.#request(id, "reply", request, encodeHumanRequestReply(id, { run_id: request.runId, request_id: request.requestId, expected_revision: request.expectedRevision, reply: request.reply }));
  }
  cancelRun(request: TerminalCancelRunRequest): Promise<HumanCancelResult> {
    if (this.#used.has(request.requestId)) return Promise.reject(new SessionErrorLikeError("human request already submitted"));
    this.#used.add(request.requestId);
    const id = this.#nextID("human-cancel");
    return this.#request(id, "cancel", request, encodeHumanRequestCancelRun(id, { run_id: request.runId, request_id: request.requestId, expected_request_revision: request.expectedRequestRevision, expected_run_revision: request.expectedRunRevision }));
  }
  receive(frame: ServerControlFrame): boolean {
    if (frame.type !== "HUMAN_REQUEST_REPLY_RESULT" && frame.type !== "HUMAN_REQUEST_ACTION_RESULT") return false;
    const pending = this.#pending.get(frame.id);
    if (pending === undefined || pending.requestId !== frame.body.request_id || pending.kind !== (frame.type === "HUMAN_REQUEST_REPLY_RESULT" ? "reply" : "cancel")) return false;
    if (frame.type === "HUMAN_REQUEST_ACTION_RESULT" && (frame.body.run_id !== pending.runId || frame.body.run_revision <= pending.expectedRunRevision || frame.body.request_revision <= pending.expectedRevision || frame.body.action !== "cancel_run" || frame.body.status !== "resolved")) throw new ProtocolError("malformed");
    if (frame.type === "HUMAN_REQUEST_REPLY_RESULT" && frame.body.revision <= pending.expectedRevision) throw new ProtocolError("malformed");
    this.#pending.delete(frame.id); pending.resolve(frame.body); return true;
  }
  receiveError(id: string, error: SessionErrorLike): boolean { const pending = this.#pending.get(id); if (!pending) return false; this.#pending.delete(id); pending.reject(error); return true; }
  close(error: SessionErrorLike = new SessionErrorLikeError("closed")): void { for (const pending of this.#pending.values()) pending.reject(error); this.#pending.clear(); }
  #request<T>(id: string, kind: "reply" | "cancel", request: TerminalReplyRequest | TerminalCancelRunRequest, payload: string): Promise<T> {
    let expectedRevision = 0n;
    let expectedRunRevision = 0n;
    if (kind === "reply") expectedRevision = (request as TerminalReplyRequest).expectedRevision;
    else { const cancel = request as TerminalCancelRunRequest; expectedRevision = cancel.expectedRequestRevision; expectedRunRevision = cancel.expectedRunRevision; }
    const result = new Promise<T>((resolve, reject) => this.#pending.set(id, { kind, requestId: request.requestId, runId: request.runId, expectedRevision, expectedRunRevision, resolve: resolve as (value: unknown) => void, reject }));
    try { this.#send(id, payload); } catch (error) {
      const failed = this.#pending.get(id);
      this.#pending.delete(id);
      failed?.reject(error);
    }
    return result;
  }
}
