import {
  ProtocolError,
  SessionError,
  consumePairingChallenge,
  createBrowserClient,
  type BrowserClient,
  type BrowserSession,
  type BrowserSessionOptions,
  type HumanRequestDetail,
  type HumanRequestItem,
  type SessionStatus,
  type StateView,
} from "@dark-factory/client";

export const DEFAULT_BROWSER_URL = "ws://127.0.0.1:43123/browser/v1";

export type FactoryHumanRequestView = Readonly<{
  request: HumanRequestItem;
  phase: "loading" | "ready" | "replying" | "cancelling";
  question?: string;
  canReply: boolean;
  canCancel: boolean;
  reply: string;
}>;

export type FactoryAppSnapshot = Readonly<{
  status: SessionStatus;
  state?: StateView;
  error?: SessionError | ProtocolError;
  selectedHumanRequest?: FactoryHumanRequestView;
}>;

type HumanSession = Pick<BrowserSession, "getHumanRequestDetail" | "replyHumanRequest" | "cancelHumanRequest">;
type ControlledClient = Pick<BrowserClient, "connect" | "close"> & { readonly session?: HumanSession };
type ClientFactory = (options: BrowserSessionOptions) => ControlledClient;

export type FactoryAppControllerOptions = {
  url: string;
  origin: string;
  location: Pick<Location, "hash" | "pathname" | "search">;
  history: Pick<History, "replaceState" | "state">;
  onChange: (snapshot: FactoryAppSnapshot) => void;
  /** Package-internal construction boundary used by DOM-free causal tests. */
  clientFactory?: ClientFactory;
};

type Selection = {
  request: HumanRequestItem;
  detail?: HumanRequestDetail;
  phase: FactoryHumanRequestView["phase"];
  reply: string;
  token: number;
};

/** Owns one mounted FactoryApp lifecycle and its exact HumanRequest authority. */
export class FactoryAppController {
  readonly #options: FactoryAppControllerOptions;
  #client: ControlledClient | undefined;
  #status: SessionStatus = "idle";
  #state: StateView | undefined;
  #error: SessionError | ProtocolError | undefined;
  #selection: Selection | undefined;
  #selectionToken = 0;
  #detailPending = false;
  #generation = 0;
  #started = false;
  #closed = false;

  constructor(options: FactoryAppControllerOptions) {
    this.#options = options;
  }

  get snapshot(): FactoryAppSnapshot { return this.#snapshot(); }

  start(): void {
    if (this.#started || this.#closed) return;
    this.#started = true;
    const generation = ++this.#generation;
    let challenge: string | null;
    try {
      challenge = consumePairingChallenge(this.#options.location, this.#options.history);
    } catch {
      this.#status = "closed";
      this.#error = new SessionError("storage_unavailable");
      this.#publish();
      return;
    }

    let endpoint: URL;
    try {
      endpoint = new URL(this.#options.url);
    } catch {
      this.#status = "closed";
      this.#error = new SessionError("invalid_request");
      this.#publish();
      return;
    }

    const factory = this.#options.clientFactory ?? createBrowserClient;
    try {
      this.#client = factory({
        url: endpoint.toString(),
        host: endpoint.host,
        origin: this.#options.origin,
        challenge: challenge ?? undefined,
        onStatus: (status) => this.#receiveStatus(generation, status),
        onState: (state) => this.#receiveState(generation, state),
        onError: (error) => this.#receiveError(generation, error),
      });
    } catch {
      this.#status = "closed";
      this.#error = new SessionError("connection");
      this.#publish();
      return;
    }
    this.#connect(generation);
  }

  retry(): void {
    if (this.#closed || this.#client === undefined) return;
    this.#clearSelection();
    this.#error = undefined;
    this.#publish();
    this.#connect(this.#generation);
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    ++this.#generation;
    this.#clearSelection();
    this.#client?.close();
  }

  async selectHumanRequest(request: HumanRequestItem): Promise<void> {
    if (this.#closed || this.#status !== "ready" || this.#detailPending || this.#selection !== undefined) return;
    const current = this.#state?.humanRequests.get(request.id);
    const session = this.#client?.session;
    if (current === undefined || current.revision !== request.revision || session === undefined) {
      this.#error = new SessionError("stale");
      this.#publish();
      return;
    }

    const generation = this.#generation;
    const token = ++this.#selectionToken;
    this.#selection = { request: current, phase: "loading", reply: "", token };
    this.#detailPending = true;
    this.#error = undefined;
    this.#publish();
    try {
      const detail = await session.getHumanRequestDetail({ requestId: current.id, expectedRevision: current.revision });
      if (!this.#ownsSelection(generation, token)) return;
      const latest = this.#state?.humanRequests.get(current.id);
      if (latest === undefined || latest.revision !== current.revision) {
        this.#clearSelection();
      } else {
        this.#selection = { request: latest, detail, phase: "ready", reply: "", token };
      }
      this.#publish();
    } catch (error) {
      if (!this.#ownsSelection(generation, token)) return;
      this.#clearSelection();
      this.#error = finiteError(error);
      this.#publish();
    } finally {
      this.#detailPending = false;
    }
  }

  setHumanReply(reply: string): void {
    if (this.#selection?.phase !== "ready") return;
    this.#selection.reply = reply;
    this.#publish();
  }

  clearHumanRequest(): void {
    if (this.#selection?.phase === "replying" || this.#selection?.phase === "cancelling") return;
    this.#clearSelection();
    this.#publish();
  }

  async replyHumanRequest(): Promise<void> {
    const selection = this.#selection;
    const session = this.#client?.session;
    if (selection?.phase !== "ready" || selection.detail === undefined || session === undefined) return;
    const generation = this.#generation;
    const token = selection.token;
    const detail = selection.detail;
    const reply = selection.reply;
    selection.phase = "replying";
    this.#error = undefined;
    this.#publish();
    try {
      await session.replyHumanRequest(detail, reply);
      if (!this.#ownsSelection(generation, token)) return;
      this.#clearSelection();
      this.#publish();
    } catch (error) {
      if (!this.#ownsSelection(generation, token)) return;
      this.#clearSelection();
      this.#error = finiteError(error);
      this.#publish();
    }
  }

  async cancelHumanRequest(): Promise<void> {
    const selection = this.#selection;
    const session = this.#client?.session;
    const cancelRun = selection?.detail?.cancelRun;
    if (selection?.phase !== "ready" || cancelRun === undefined || cancelRun === null || session === undefined) return;
    const generation = this.#generation;
    const token = selection.token;
    selection.phase = "cancelling";
    this.#error = undefined;
    this.#publish();
    try {
      await session.cancelHumanRequest(cancelRun);
      if (!this.#ownsSelection(generation, token)) return;
      this.#clearSelection();
      this.#publish();
    } catch (error) {
      if (!this.#ownsSelection(generation, token)) return;
      this.#clearSelection();
      this.#error = finiteError(error);
      this.#publish();
    }
  }

  #connect(generation: number): void {
    const client = this.#client;
    if (client === undefined) return;
    void client.connect().catch((error) => {
      if (!this.#current(generation) || this.#status === "closed") return;
      this.#status = "closed";
      this.#error = finiteError(error);
      this.#clearSelection();
      this.#publish();
    });
  }

  #receiveStatus(generation: number, status: SessionStatus): void {
    if (!this.#current(generation)) return;
    this.#status = status;
    if (status !== "ready") this.#clearSelection();
    if (status !== "closed") this.#error = undefined;
    this.#publish();
  }

  #receiveState(generation: number, state: StateView): void {
    if (!this.#current(generation)) return;
    this.#state = state;
    const selected = this.#selection;
    if (selected !== undefined) {
      const current = state.humanRequests.get(selected.request.id);
      if (current === undefined || current.revision !== selected.request.revision) this.#clearSelection();
      else selected.request = current;
    }
    this.#publish();
  }

  #receiveError(generation: number, error: SessionError | ProtocolError): void {
    if (!this.#current(generation)) return;
    this.#error = error;
    this.#publish();
  }

  #current(generation: number): boolean {
    return !this.#closed && generation === this.#generation;
  }

  #ownsSelection(generation: number, token: number): boolean {
    return this.#current(generation) && this.#selection?.token === token;
  }

  #clearSelection(): void {
    ++this.#selectionToken;
    this.#selection = undefined;
  }

  #snapshot(): FactoryAppSnapshot {
    const selection = this.#selection;
    return {
      status: this.#status,
      state: this.#state,
      error: this.#error,
      selectedHumanRequest: selection === undefined ? undefined : {
        request: selection.request,
        phase: selection.phase,
        question: selection.detail?.question,
        canReply: selection.detail?.canReply ?? false,
        canCancel: selection.detail?.cancelRun !== null && selection.detail?.cancelRun !== undefined,
        reply: selection.reply,
      },
    };
  }

  #publish(): void {
    if (!this.#closed) this.#options.onChange(this.#snapshot());
  }
}

function finiteError(error: unknown): SessionError | ProtocolError {
  return error instanceof SessionError || error instanceof ProtocolError ? error : new SessionError("connection");
}
