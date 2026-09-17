import { type BrowserSession, type HumanRequestDetail, type HumanRequestItem, ProtocolError, SessionError } from "@dark-factory/client";

type HumanSession = Pick<BrowserSession, "getHumanRequestDetail" | "replyHumanRequest" | "cancelHumanRequest">;
export type HumanRequestPhase = "loading" | "ready" | "replying" | "cancelling" | "ended";

export type HumanRequestFlowSelection<Scope> = Readonly<{
  scope: Scope;
  request: HumanRequestItem;
  phase: HumanRequestPhase;
  detail?: HumanRequestDetail;
  reply: string;
  notice?: string;
  token: number;
}>;

export type HumanRequestFlowOptions<Scope> = Readonly<{
  active: (scope: Scope) => boolean;
  currentRequest: (scope: Scope, requestID: string) => HumanRequestItem | undefined;
  session: (scope: Scope) => HumanSession | undefined;
  onChange: () => void;
  onError?: (error: SessionError | ProtocolError) => void;
  /** The local console waits for its state stream; a remote reads back once. */
  afterAction: "clear" | "refresh";
  replaceReady?: boolean;
  unavailableNotice?: string;
  unavailableError?: SessionError | ProtocolError;
  absentNotice?: string;
  actionFailureNotice?: (error: unknown) => string | undefined;
}>;

/** One exact request authority at a time, shared by the local and remote consoles. */
export class HumanRequestFlow<Scope> {
  readonly #options: HumanRequestFlowOptions<Scope>;
  #selection: HumanRequestFlowSelection<Scope> | undefined;
  #token = 0;

  constructor(options: HumanRequestFlowOptions<Scope>) { this.#options = options; }

  get selection(): HumanRequestFlowSelection<Scope> | undefined { return this.#selection; }
  get busy(): boolean { return this.#selection !== undefined && busy(this.#selection); }

  open(scope: Scope, request: HumanRequestItem): Promise<void> {
    const current = this.#selection;
    if (!this.#options.active(scope) || (current !== undefined && (busy(current) || !this.#options.replaceReady))) return Promise.resolve();
    const latest = this.#options.currentRequest(scope, request.id);
    if (latest === undefined || latest.revision !== request.revision) { this.#fail(new SessionError("stale")); return Promise.resolve(); }
    return this.#load({ scope, request: latest, phase: "loading", reply: "", token: ++this.#token });
  }

  reconcile(): void {
    const selected = this.#selection;
    if (selected === undefined) return;
    const latest = this.#options.currentRequest(selected.scope, selected.request.id);
    if (latest === undefined || latest.revision !== selected.request.revision) this.clear(true);
    else this.#put({ ...selected, request: latest });
  }

  clear(force = false): void {
    if (this.#selection === undefined || (!force && busy(this.#selection))) return;
    ++this.#token;
    this.#selection = undefined;
    this.#options.onChange();
  }

  setReply(reply: string): void {
    const selected = this.#selection;
    const detail = selected?.detail;
    const maximum = detail?.replyMaxBytes;
    if (selected?.phase !== "ready" || detail === undefined || maximum === undefined || !detail.canReply || !this.#options.active(selected.scope)) return;
    if (reply.length > maximum || new TextEncoder().encode(reply).length > maximum) return this.#fail(new SessionError("too_large"));
    this.#put({ ...selected, reply, notice: undefined });
  }

  reply(): Promise<void> {
    const selected = this.#selection;
    if (selected?.phase !== "ready" || selected.detail === undefined || !selected.detail.canReply || !this.#options.active(selected.scope)) return Promise.resolve();
    if (selected.reply.length === 0) { this.#fail(new SessionError("invalid_request")); return Promise.resolve(); }
    const session = this.#options.session(selected.scope);
    if (session === undefined) { this.#end(selected, this.#options.unavailableNotice); return Promise.resolve(); }
    const authority = selected.detail;
    this.#put({ ...selected, phase: "replying", notice: undefined });
    return session.replyHumanRequest(authority, selected.reply).then(
      () => this.#afterAction(selected),
      (error) => this.#afterAction(selected, this.#options.actionFailureNotice?.(error)),
    );
  }

  cancel(): Promise<void> {
    const selected = this.#selection;
    const authority = selected?.detail?.cancelRun;
    if (selected?.phase !== "ready" || authority === undefined || authority === null || !this.#options.active(selected.scope)) return Promise.resolve();
    const session = this.#options.session(selected.scope);
    if (session === undefined) { this.#end(selected, this.#options.unavailableNotice); return Promise.resolve(); }
    this.#put({ ...selected, phase: "cancelling", notice: undefined });
    return session.cancelHumanRequest(authority).then(
      () => this.#afterAction(selected),
      (error) => this.#afterAction(selected, this.#options.actionFailureNotice?.(error)),
    );
  }

  #load(next: HumanRequestFlowSelection<Scope>, notice?: string): Promise<void> {
    this.#put({ ...next, notice });
    const session = this.#options.session(next.scope);
    if (session === undefined) { this.#end(next, notice ?? this.#options.unavailableNotice, this.#options.unavailableError); return Promise.resolve(); }
    return session.getHumanRequestDetail({ requestId: next.request.id, expectedRevision: next.request.revision }).then(
      (detail) => {
        if (!this.#owns(next) || !this.#options.active(next.scope)) return;
        const latest = this.#options.currentRequest(next.scope, next.request.id);
        if (latest === undefined || latest.revision !== next.request.revision) return this.#end(next, notice ?? this.#options.absentNotice);
        this.#put({ ...next, request: latest, detail, phase: "ready", reply: "", notice });
      },
      (error) => {
        if (!this.#owns(next)) return;
        this.#end(next, notice ?? this.#options.absentNotice, error);
      },
    );
  }

  #afterAction(after: HumanRequestFlowSelection<Scope>, notice?: string): Promise<void> {
    if (!this.#owns(after)) return Promise.resolve();
    if (this.#options.afterAction === "clear") { this.clear(true); return Promise.resolve(); }
    const request = this.#options.currentRequest(after.scope, after.request.id);
    if (request === undefined) { this.#end(after, notice ?? this.#options.absentNotice); return Promise.resolve(); }
    return this.#load({ ...after, request, detail: undefined, phase: "loading", reply: "" }, notice);
  }

  #end(after: HumanRequestFlowSelection<Scope>, notice?: string, error?: unknown): void {
    if (!this.#owns(after)) return;
    if (this.#options.afterAction === "clear") {
      this.#selection = undefined;
      ++this.#token;
      if (error !== undefined) this.#options.onError?.(finiteError(error));
      this.#options.onChange();
      return;
    }
    this.#put({ ...after, phase: "ended", detail: undefined, notice });
  }

  #fail(error: SessionError | ProtocolError): void { this.#options.onError?.(error); this.#options.onChange(); }
  #owns(selection: HumanRequestFlowSelection<Scope>): boolean { return this.#selection?.token === selection.token; }
  #put(selection: HumanRequestFlowSelection<Scope>): void { this.#selection = selection; this.#options.onChange(); }
}

function busy(selection: HumanRequestFlowSelection<unknown>): boolean {
  return selection.phase === "loading" || selection.phase === "replying" || selection.phase === "cancelling";
}

function finiteError(error: unknown): SessionError | ProtocolError {
  return error instanceof SessionError || error instanceof ProtocolError ? error : new SessionError("connection");
}
