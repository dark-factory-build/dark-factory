import {
  MAX_TERMINAL_COLS,
  MAX_TERMINAL_PAYLOAD,
  MAX_TERMINAL_ROWS,
  ProtocolError,
  SessionError,
  type BrowserSession,
  type TerminalHandle,
  type TerminalInputResult,
  type TerminalTarget,
} from "@dark-factory/client";

const MAX_PENDING_INPUT_BYTES = 64 * 1024;

export type TerminalSurface = Readonly<{
  write(payload: Uint8Array): Promise<void>;
  abort(): void;
}>;

export type TerminalPhase = "idle" | "resolving" | "attaching" | "acquiring" | "ready" | "closing" | "closed";

export type TerminalLeaseOperation = "none" | "taking" | "releasing";

export type TerminalControllerSnapshot = Readonly<{
  phase: TerminalPhase;
  writable: boolean;
  leaseOperation: TerminalLeaseOperation;
  error?: SessionError | ProtocolError;
}>;

type TerminalControllerSession = Pick<BrowserSession, "resolveAgentTerminal" | "openTerminal" | "close">;

type TerminalControllerOptions = Readonly<{
  session: TerminalControllerSession;
  agentId: string;
  expectedAgentRevision: bigint;
  expectedHead: bigint;
  surface: TerminalSurface;
  onChange: (snapshot: TerminalControllerSnapshot) => void;
}>;

type Input = Uint8Array;
type Resize = { rows: number; cols: number };

/** Owns one exact target, handle, display surface and input lease. */
class TerminalController {
  readonly #options: TerminalControllerOptions;
  #phase: TerminalPhase = "idle";
  #writable = false;
  #error: SessionError | ProtocolError | undefined;
  #handle: TerminalHandle | undefined;
  #handleClosed = false;
  #started = false;
  #closing = false;
  #generation = 0;
  #surfaceAborted = false;
  #inputBuffer: Input = new Uint8Array(0);
  #inputInFlightBytes = 0;
  #leaseOperation: TerminalLeaseOperation = "none";
  #pendingResize: Resize | undefined;
  #effectTask: Promise<void> | undefined;
  #closePromise: Promise<void> | undefined;

  constructor(options: TerminalControllerOptions) {
    this.#options = options;
  }

  get snapshot(): TerminalControllerSnapshot { return this.#snapshot(); }

  start(): void {
    if (this.#started || this.#closing) return;
    this.#started = true;
    const generation = ++this.#generation;
    this.#phase = "resolving";
    this.#publish();
    if (!this.#current(generation)) return;
    void this.#bootstrap(generation);
  }

  sendInput(bytes: Uint8Array): boolean {
    if (!this.#writable || !this.#canEffect() || !(bytes instanceof Uint8Array) || bytes.length === 0) return false;
    const total = this.#inputInFlightBytes + this.#inputBuffer.length;
    if (bytes.length > MAX_TERMINAL_PAYLOAD || bytes.length > MAX_PENDING_INPUT_BYTES || total + bytes.length > MAX_PENDING_INPUT_BYTES) {
      this.#fail(new SessionError("too_large"));
      return false;
    }
    const next = new Uint8Array(this.#inputBuffer.length + bytes.length);
    next.set(this.#inputBuffer);
    next.set(bytes, this.#inputBuffer.length);
    this.#inputBuffer = next;
    this.#pumpEffects();
    return true;
  }

  sendText(value: string): boolean {
    if (typeof value !== "string") return false;
    if (value.length > MAX_PENDING_INPUT_BYTES) {
      this.#fail(new SessionError("too_large"));
      return false;
    }
    return this.sendInput(new TextEncoder().encode(value));
  }

  sendBinary(value: string): boolean {
    if (typeof value !== "string") return false;
    if (value.length > MAX_PENDING_INPUT_BYTES) {
      this.#fail(new SessionError("too_large"));
      return false;
    }
    const bytes = new Uint8Array(value.length);
    for (let index = 0; index < value.length; index += 1) {
      const code = value.charCodeAt(index);
      if (code > 0xff) {
        this.#fail(new ProtocolError("malformed"));
        return false;
      }
      bytes[index] = code;
    }
    return this.sendInput(bytes);
  }

  resize(rows: number, cols: number): boolean {
    if (!this.#writable || !this.#canEffect()) return false;
    if (!Number.isSafeInteger(rows) || rows < 1 || rows > MAX_TERMINAL_ROWS || !Number.isSafeInteger(cols) || cols < 1 || cols > MAX_TERMINAL_COLS) {
      this.#fail(new ProtocolError("malformed"));
      return false;
    }
    this.#pendingResize = { rows, cols };
    this.#pumpEffects();
    return true;
  }

  /**
   * Take control: acquire the input lease for an attached read-only
   * observer. Occupied or failed acquisition keeps the truthful read-only
   * observer alive; it never closes the display.
   */
  takeControl(): boolean {
    if (this.#phase !== "ready" || this.#writable || this.#leaseOperation !== "none" || !this.#liveHandle()) return false;
    const handle = this.#handle;
    if (handle === undefined) return false;
    const generation = this.#generation;
    this.#leaseOperation = "taking";
    this.#publish();
    void (async () => {
      try {
        await handle.acquireInput();
        if (!this.#current(generation) || !this.#liveHandle(handle)) return;
        if (handle.writable) {
          this.#writable = true;
          this.#error = undefined;
        } else {
          this.#error = new SessionError("closed");
        }
        this.#publish();
      } catch (error) {
        if (!this.#current(generation) || !this.#liveHandle(handle)) return;
        this.#error = finiteError(error);
        this.#publish();
      } finally {
        if (this.#current(generation)) {
          this.#leaseOperation = "none";
          this.#publish();
        }
      }
    })();
    return true;
  }

  /**
   * Hand back: release the input lease and remain a read-only observer.
   * Input stops being accepted before the release is attempted; a release
   * failure is authority uncertainty and closes the controller.
   */
  handBack(): boolean {
    if (this.#phase !== "ready" || !this.#writable || this.#leaseOperation !== "none" || !this.#liveHandle()) return false;
    const handle = this.#handle;
    if (handle === undefined) return false;
    const generation = this.#generation;
    this.#leaseOperation = "releasing";
    this.#writable = false;
    this.#inputBuffer = new Uint8Array(0);
    this.#pendingResize = undefined;
    this.#publish();
    void (async () => {
      try {
        const drain = this.#effectTask;
        if (drain !== undefined) await drain.catch(() => undefined);
        if (!this.#current(generation) || !this.#liveHandle(handle)) return;
        await handle.releaseInput();
        if (!this.#current(generation)) return;
        this.#leaseOperation = "none";
        this.#publish();
      } catch (error) {
        if (!this.#current(generation)) return;
        // A concurrent client operation (a lease renewal, a pending output
        // callback) rejects the release before anything is sent, and the
        // lease is still ours: that is retryable busyness, not authority
        // uncertainty. Only a release that may have reached the daemon
        // fails closed.
        let leaseStillHeld = false;
        try {
          leaseStillHeld = this.#liveHandle(handle) && handle.writable;
        } catch {
          leaseStillHeld = false;
        }
        if (leaseStillHeld) {
          this.#leaseOperation = "none";
          this.#writable = true;
          this.#publish();
          return;
        }
        this.#leaseOperation = "none";
        this.#fail(finiteError(error));
      }
    })();
    return true;
  }

  close(): Promise<void> {
    if (this.#closePromise !== undefined) return this.#closePromise;
    // Install the latch before any callback can re-enter the owner.
    this.#closePromise = Promise.resolve();
    this.#closing = true;
    ++this.#generation;
    this.#writable = false;
    this.#leaseOperation = "none";
    this.#inputBuffer = new Uint8Array(0);
    this.#pendingResize = undefined;
    this.#phase = "closing";
    this.#abortSurface();
    try { this.#options.session.close(); } catch { if (this.#error === undefined) this.#error = new SessionError("connection"); }
    this.#publish();
    this.#phase = "closed";
    this.#publish();
    return this.#closePromise;
  }

  async #bootstrap(generation: number): Promise<void> {
    try {
      const target = await this.#options.session.resolveAgentTerminal({
        agentId: this.#options.agentId,
        expectedAgentRevision: this.#options.expectedAgentRevision,
        expectedHead: this.#options.expectedHead,
      });
      if (!this.#current(generation)) return;
      if (target === null) {
        this.#fail(new SessionError("not_found"));
        return;
      }
      const handle = this.#options.session.openTerminal(target as TerminalTarget, {
        onOutput: (output) => this.#writeOutput(generation, output.payload),
        onExit: () => this.#handleEnded(new SessionError("closed")),
        onReset: () => this.#handleEnded(new SessionError("stale")),
        onClose: (error) => this.#handleEnded(finiteError(error)),
      });
      this.#handle = handle;
      if (!this.#current(generation)) return;
      this.#phase = "attaching";
      this.#publish();
      if (!this.#current(generation)) return;
      const attached = await handle.attach();
      if (!this.#current(generation)) return;
      if ("kind" in attached) {
        this.#fail(new SessionError("stale"));
        return;
      }
      this.#phase = "acquiring";
      this.#publish();
      if (!this.#current(generation)) return;
      try {
        await handle.acquireInput();
        if (!this.#current(generation)) return;
        if (!handle.writable) {
          this.#handleEnded(new SessionError("closed"));
          return;
        }
        this.#writable = true;
      } catch (error) {
        // A lease can be occupied by another observer. Keep the attached
        // display alive as a truthful read-only observer; the session/handle
        // still owns transport and terminal-end failures.
        if (this.#current(generation)) {
          this.#error = finiteError(error);
          this.#phase = "ready";
          this.#publish();
        }
        return;
      }
      if (!this.#current(generation)) return;
      this.#phase = "ready";
      this.#publish();
    } catch (error) {
      if (this.#current(generation)) this.#fail(finiteError(error));
    }
  }

  async #writeOutput(generation: number, payload: Uint8Array): Promise<void> {
    if (!this.#current(generation) || !this.#liveHandle()) return Promise.reject(new SessionError("closed"));
    try {
      await this.#options.surface.write(payload.slice());
    } catch {
      const live = this.#current(generation) && this.#liveHandle();
      if (live) this.#fail(new SessionError("internal"));
      throw new SessionError(live ? "internal" : "closed");
    }
    if (!this.#current(generation) || !this.#liveHandle()) throw new SessionError("closed");
  }

  #pumpEffects(): void {
    if (this.#effectTask !== undefined || !this.#writable || !this.#liveHandle()) return;
    const task = this.#runEffects();
    this.#effectTask = task;
    void task.then(() => this.#effectFinished(task), () => this.#effectFinished(task));
  }

  async #runEffects(): Promise<void> {
    const handle = this.#handle;
    const generation = this.#generation;
    if (handle === undefined || !this.#canEffect(handle)) return;
    let inputSinceResize = false;
    while (this.#current(generation) && this.#canEffect(handle) && this.#writable) {
      if (this.#inputBuffer.length > 0 && !inputSinceResize) {
        const payload = this.#inputBuffer.length <= MAX_TERMINAL_PAYLOAD ? this.#inputBuffer : this.#inputBuffer.slice(0, MAX_TERMINAL_PAYLOAD);
        this.#inputBuffer = this.#inputBuffer.slice(payload.length);
        this.#inputInFlightBytes = payload.length;
        let result: TerminalInputResult;
        try {
          if (!this.#canEffect(handle)) return;
          result = await handle.sendInput(payload);
        } catch (error) {
          this.#inputInFlightBytes = 0;
          if (this.#current(generation)) this.#fail(finiteError(error));
          return;
        }
        this.#inputInFlightBytes = 0;
        if (!this.#current(generation) || !this.#canEffect(handle)) return;
        if (result.status === "partial" || result.status === "uncertain") {
          this.#fail(new SessionError("connection"));
          return;
        }
        if (result.status === "rejected") {
          this.#error = new SessionError("invalid_request");
          this.#publish();
          if (!this.#current(generation) || !this.#canEffect(handle)) return;
        }
        inputSinceResize = true;
        continue;
      }
      const resize = this.#pendingResize;
      if (resize !== undefined) {
        this.#pendingResize = undefined;
        if (!this.#canEffect(handle)) return;
        try { await handle.resize(resize.rows, resize.cols); } catch (error) {
          if (this.#current(generation)) this.#fail(finiteError(error));
          return;
        }
        if (!this.#current(generation) || !this.#canEffect(handle)) return;
        inputSinceResize = false;
        continue;
      }
      inputSinceResize = false;
      if (this.#inputBuffer.length === 0) return;
    }
  }

  #effectFinished(task: Promise<void>): void {
    if (this.#effectTask === task) this.#effectTask = undefined;
  }

  #handleEnded(error: SessionError | ProtocolError): void {
    if (this.#handleClosed) return;
    this.#handleClosed = true;
    this.#writable = false;
    this.#leaseOperation = "none";
    this.#inputBuffer = new Uint8Array(0);
    this.#inputInFlightBytes = 0;
    this.#pendingResize = undefined;
    ++this.#generation;
    this.#abortSurface();
    if (!this.#closing && this.#phase !== "closed") {
      this.#error = error;
      this.#phase = "closed";
      this.#publish();
    }
  }

  #fail(error: SessionError | ProtocolError): void {
    if (this.#closing || this.#phase === "closed") return;
    this.#error = error;
    this.#writable = false;
    this.#leaseOperation = "none";
    this.#inputBuffer = new Uint8Array(0);
    this.#pendingResize = undefined;
    this.#phase = "closing";
    this.#publish();
    void this.close();
  }

  #liveHandle(handle = this.#handle): boolean {
    return handle !== undefined && handle === this.#handle && !this.#handleClosed && !this.#closing && this.#phase !== "closed";
  }

  #abortSurface(): void {
    if (this.#surfaceAborted) return;
    this.#surfaceAborted = true;
    try { this.#options.surface.abort(); } catch { if (this.#error === undefined) this.#error = new SessionError("internal"); }
  }

  #canEffect(handle = this.#handle): boolean {
    if (handle === undefined || !this.#liveHandle(handle)) return false;
    try {
      if (handle.writable) return true;
    } catch {
      // A broken handle is no longer an authority boundary.
    }
    this.#handleEnded(new SessionError("closed"));
    return false;
  }

  #current(generation: number): boolean {
    return generation === this.#generation && !this.#closing && this.#phase !== "closing" && this.#phase !== "closed";
  }

  #snapshot(): TerminalControllerSnapshot {
    return { phase: this.#phase, writable: this.#writable, leaseOperation: this.#leaseOperation, error: this.#error };
  }

  #publish(): void {
    try { this.#options.onChange(this.#snapshot()); } catch { /* presentation callbacks never own the session */ }
  }
}

function finiteError(error: unknown): SessionError | ProtocolError {
  return error instanceof SessionError || error instanceof ProtocolError ? error : new SessionError("connection");
}

export { MAX_PENDING_INPUT_BYTES, TerminalController };
export type { TerminalControllerOptions, TerminalControllerSession };
