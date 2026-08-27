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

export type TerminalControllerSnapshot = Readonly<{
  phase: TerminalPhase;
  writable: boolean;
  error?: SessionError | ProtocolError;
}>;

export type TerminalControllerSession = Pick<BrowserSession, "resolveAgentTerminal" | "openTerminal">;

export type TerminalControllerOptions = Readonly<{
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
export class TerminalController {
  readonly #options: TerminalControllerOptions;
  #phase: TerminalPhase = "idle";
  #writable = false;
  #error: SessionError | ProtocolError | undefined;
  #handle: TerminalHandle | undefined;
  #handleClosed = false;
  #started = false;
  #closing = false;
  #detachStarted = false;
  #generation = 0;
  #inputQueue: Input[] = [];
  #pendingInputBytes = 0;
  #pendingResize: Resize | undefined;
  #effectTask: Promise<void> | undefined;
  #bootstrapTask: Promise<void> | undefined;
  #outputTask: Promise<void> | undefined;
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
    const task = this.#bootstrap(generation);
    this.#bootstrapTask = task;
    void task.then(() => this.#taskFinished(task), () => this.#taskFinished(task));
  }

  sendInput(bytes: Uint8Array): boolean {
    if (!this.#writable || this.#closing || this.#handle === undefined || !(bytes instanceof Uint8Array) || bytes.length === 0) return false;
    if (bytes.length > MAX_TERMINAL_PAYLOAD || this.#pendingInputBytes + bytes.length > MAX_PENDING_INPUT_BYTES) {
      this.#fail(new SessionError("too_large"));
      return false;
    }
    this.#inputQueue.push(bytes.slice());
    this.#pendingInputBytes += bytes.length;
    this.#pumpEffects();
    return true;
  }

  sendText(value: string): boolean {
    if (typeof value !== "string") return false;
    return this.sendInput(new TextEncoder().encode(value));
  }

  sendBinary(value: string): boolean {
    if (typeof value !== "string") return false;
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
    if (!this.#writable || this.#closing || this.#handle === undefined) return false;
    if (!Number.isSafeInteger(rows) || rows < 1 || rows > MAX_TERMINAL_ROWS || !Number.isSafeInteger(cols) || cols < 1 || cols > MAX_TERMINAL_COLS) {
      this.#fail(new ProtocolError("malformed"));
      return false;
    }
    this.#pendingResize = { rows, cols };
    this.#pumpEffects();
    return true;
  }

  close(): Promise<void> {
    if (this.#closePromise !== undefined) return this.#closePromise;
    this.#closing = true;
    this.#writable = false;
    this.#inputQueue = [];
    this.#pendingInputBytes = 0;
    this.#pendingResize = undefined;
    try { this.#options.surface.abort(); } catch { if (this.#error === undefined) this.#error = new SessionError("internal"); }
    if (this.#phase !== "closed") this.#phase = "closing";
    this.#publish();
    this.#closePromise = this.#finishClose();
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
      const attached = await handle.attach();
      if (!this.#current(generation)) return;
      if ("kind" in attached) {
        this.#fail(new SessionError("stale"));
        return;
      }
      this.#phase = "acquiring";
      this.#publish();
      try {
        await handle.acquireInput();
        if (!this.#current(generation)) return;
        this.#writable = true;
      } catch (error) {
        if (!this.#current(generation)) return;
        this.#error = finiteError(error);
      }
      if (!this.#current(generation)) return;
      this.#phase = "ready";
      this.#publish();
    } catch (error) {
      if (this.#current(generation)) this.#fail(finiteError(error));
    }
  }

  async #writeOutput(generation: number, payload: Uint8Array): Promise<void> {
    if (!this.#current(generation)) return;
    let write: Promise<void>;
    try { write = Promise.resolve(this.#options.surface.write(payload.slice())); } catch (error) { write = Promise.reject(error); }
    this.#outputTask = write;
    try {
      await write;
    } catch (error) {
      if (this.#current(generation)) this.#fail(new SessionError("internal"));
      throw error;
    } finally {
      if (this.#outputTask === write) this.#outputTask = undefined;
    }
  }

  #pumpEffects(): void {
    if (this.#effectTask !== undefined || !this.#writable || this.#closing) return;
    const task = this.#runEffects();
    this.#effectTask = task;
    void task.then(() => this.#taskFinished(task), () => this.#taskFinished(task));
  }

  async #runEffects(): Promise<void> {
    const handle = this.#handle;
    if (handle === undefined) return;
    while (!this.#closing && this.#writable) {
      const input = this.#inputQueue.shift();
      if (input !== undefined) {
        this.#pendingInputBytes -= input.length;
        let result: TerminalInputResult;
        try { result = await handle.sendInput(input); } catch (error) { this.#fail(finiteError(error)); return; }
        if (result.status === "partial" || result.status === "uncertain") {
          this.#fail(new SessionError("connection"));
          return;
        }
        if (result.status === "rejected") {
          this.#error = new SessionError("invalid_request");
          this.#publish();
        }
        continue;
      }
      const resize = this.#pendingResize;
      this.#pendingResize = undefined;
      if (resize !== undefined) {
        try { await handle.resize(resize.rows, resize.cols); } catch (error) { this.#fail(finiteError(error)); return; }
        continue;
      }
      return;
    }
  }

  async #finishClose(): Promise<void> {
    await this.#waitForEffects();
    const handle = this.#handle;
    if (handle !== undefined && !this.#handleClosed && !this.#detachStarted) {
      this.#detachStarted = true;
      try { await handle.detach(); } catch (error) { if (this.#error === undefined) this.#error = finiteError(error); }
    }
    this.#writable = false;
    this.#phase = "closed";
    this.#publish();
  }

  async #waitForEffects(): Promise<void> {
    while (this.#bootstrapTask !== undefined || this.#effectTask !== undefined || this.#outputTask !== undefined) {
      const pending = [this.#bootstrapTask, this.#effectTask, this.#outputTask].filter((task): task is Promise<void> => task !== undefined);
      await Promise.all(pending.map((task) => task.catch(() => undefined)));
    }
  }

  #taskFinished(task: Promise<void>): void {
    if (this.#bootstrapTask === task) this.#bootstrapTask = undefined;
    if (this.#effectTask === task) this.#effectTask = undefined;
  }

  #handleEnded(error: SessionError | ProtocolError): void {
    this.#handleClosed = true;
    this.#writable = false;
    if (this.#phase !== "closing" && this.#phase !== "closed") {
      this.#error = error;
      this.#phase = "closed";
      this.#publish();
    }
  }

  #fail(error: SessionError | ProtocolError): void {
    if (this.#phase === "closed" && this.#closing) return;
    this.#error = error;
    this.#writable = false;
    this.#inputQueue = [];
    this.#pendingInputBytes = 0;
    this.#pendingResize = undefined;
    this.#publish();
    void this.close();
  }

  #current(generation: number): boolean { return !this.#closing && generation === this.#generation; }

  #snapshot(): TerminalControllerSnapshot {
    return { phase: this.#phase, writable: this.#writable, error: this.#error };
  }

  #publish(): void { this.#options.onChange(this.#snapshot()); }
}

function finiteError(error: unknown): SessionError | ProtocolError {
  return error instanceof SessionError || error instanceof ProtocolError ? error : new SessionError("connection");
}

export { MAX_PENDING_INPUT_BYTES };
