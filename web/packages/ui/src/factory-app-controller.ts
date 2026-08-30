import {
  ProtocolError,
  SessionError,
  consumePairingChallenge,
  createBrowserClient,
  type BrowserClient,
  type BrowserSession,
  type BrowserSessionOptions,
  type AgentItem,
  type HumanRequestDetail,
  type HumanRequestItem,
  type SessionErrorCode,
  type SessionStatus,
  type StateView,
} from "@dark-factory/client";
import { TerminalController, type TerminalControllerSnapshot, type TerminalLeaseOperation, type TerminalSurface } from "./terminal-controller.js";
import { unavailableQueueActions, type QueueActions } from "./console-view.js";

const BROWSER_ENDPOINT = new URL("ws://127.0.0.1:43123/browser/v1");
const BROWSER_URL = BROWSER_ENDPOINT.toString();
const BROWSER_HOST = BROWSER_ENDPOINT.host;

export type FactoryHumanRequestView = Readonly<{
  request: HumanRequestItem;
  phase: "loading" | "ready" | "replying" | "cancelling";
  question?: string;
  canReply: boolean;
  canCancel: boolean;
  replyMaxBytes: number;
  reply: string;
}>;

export type FactoryAgentSelection = Readonly<{
  id: string;
  name: string;
  revision: bigint;
}>;

export type FactoryTerminalView = Readonly<{
  agentId: string;
  agentName: string;
  agentRevision: bigint;
  phase: "idle" | "resolving" | "attaching" | "acquiring" | "ready" | "closing" | "closed";
  writable: boolean;
  leaseOperation: TerminalLeaseOperation;
  error?: SessionError | ProtocolError;
  /** Server replay resets survived by this sidebar view; > 0 shows the banner. */
  resets: number;
  surfaceVersion: number;
}>;

export type FactoryAppSnapshot = Readonly<{
  status: SessionStatus;
  state?: StateView;
  error?: SessionError | ProtocolError;
  selectedHumanRequest?: FactoryHumanRequestView;
  selectedAgent?: FactoryAgentSelection;
  terminal?: FactoryTerminalView;
}>;

export type FactoryAppStatus =
  | Readonly<{ status: Exclude<SessionStatus, "closed"> }>
  | Readonly<{ status: "closed"; reason: SessionErrorCode }>;

type HumanSession = Pick<BrowserSession, "getHumanRequestDetail" | "replyHumanRequest" | "cancelHumanRequest">;
type TerminalSession = Pick<BrowserSession, "resolveAgentTerminal" | "openTerminal" | "close">;
type ControlledClient = Pick<BrowserClient, "connect" | "close"> & { readonly session?: HumanSession & TerminalSession };
type ClientFactory = (options: BrowserSessionOptions) => ControlledClient;

export type FactoryAppControllerOptions = {
  origin: string;
  location: Pick<Location, "hash" | "pathname" | "search">;
  history: Pick<History, "replaceState" | "state">;
  onChange: (snapshot: FactoryAppSnapshot) => void;
  onStatusChange?: (status: FactoryAppStatus) => void;
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

type AgentTerminalSelection = {
  agent: AgentItem;
  head: bigint;
  /** Server replay resets survived by this sidebar view (banner state). */
  resets: number;
};

function agentHasRunningTask(agent: AgentItem, state: StateView): boolean {
  for (const task of state.tasks.values()) {
    if (task.assigned_agent_id === agent.id && task.status === "running") return true;
  }
  return false;
}

/** Owns one mounted FactoryApp lifecycle and its exact HumanRequest authority. */
export class FactoryAppController {
  readonly #options: FactoryAppControllerOptions;
  #client: ControlledClient | undefined;
  #status: SessionStatus = "idle";
  #statusReason: SessionErrorCode | undefined;
  #lastStatus: FactoryAppStatus | undefined;
  #state: StateView | undefined;
  #error: SessionError | ProtocolError | undefined;
  #selection: Selection | undefined;
  #selectionToken = 0;
  #detailPending = false;
  #selectedAgent: AgentTerminalSelection | undefined;
  #terminal: TerminalController | undefined;
  #terminalSurface: TerminalSurface | undefined;
  #terminalSurfaceToken: object | undefined;
  #terminalSurfaceVersion = 0;
  #terminalGeneration = 0;
  #terminalResetBurst = 0;
  #terminalRetry: { head: bigint; stale: boolean } | undefined;
  #pendingTerminalResize: { rows: number; cols: number } | undefined;
  #generation = 0;
  #started = false;
  #closed = false;

  /**
   * Queue mutations the daemon has no API for yet. Every method returns a
   * typed unavailable result and mutates nothing; the console renders the
   * reason instead of pretending success.
   */
  readonly queueActions: QueueActions = unavailableQueueActions();

  constructor(options: FactoryAppControllerOptions) {
    this.#options = options;
  }

  get snapshot(): FactoryAppSnapshot { return this.#snapshot(); }

  /** Take control of the open terminal's input. Read-only observer on failure. */
  takeTerminalControl(): boolean {
    if (this.#closed) return false;
    return this.#terminal?.takeControl() ?? false;
  }

  /** Hand the open terminal's input back; the display stays attached. */
  handBackTerminalControl(): boolean {
    if (this.#closed) return false;
    return this.#terminal?.handBack() ?? false;
  }

  start(): void {
    if (this.#started || this.#closed) return;
    this.#started = true;
    const generation = ++this.#generation;
    let challenge: string | null;
    try {
      challenge = consumePairingChallenge(this.#options.location, this.#options.history);
    } catch {
      this.#status = "closed";
      this.#error = new SessionError("connection");
      this.#statusReason = this.#error.code;
      this.#publish();
      return;
    }

    const factory = this.#options.clientFactory ?? createBrowserClient;
    let client: ControlledClient;
    try {
      client = factory({
        url: BROWSER_URL,
        host: BROWSER_HOST,
        origin: this.#options.origin,
        challenge: challenge ?? undefined,
        onStatus: (status) => this.#receiveStatus(generation, status),
        onState: (state) => this.#receiveState(generation, state),
        onError: (error) => this.#receiveError(generation, error),
      });
    } catch {
      this.#status = "closed";
      this.#error = new SessionError("connection");
      this.#statusReason = this.#error.code;
      this.#publish();
      return;
    }
    if (!this.#current(generation)) {
      client.close();
      return;
    }
    this.#client = client;
    this.#connect(generation);
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    ++this.#generation;
    this.#clearSelection();
    this.#selectedAgent = undefined;
    this.#dropTerminal(true);
    this.#client?.close();
  }

  selectAgent(agent: AgentItem): void {
    if (this.#closed || this.#status !== "ready") return;
    const current = this.#state?.agents.get(agent.id);
    if (current === undefined || current.revision !== agent.revision) {
      this.#error = new SessionError("stale");
      this.#publish();
      return;
    }
    const selected = this.#selectedAgent;
    if (selected?.agent.id === current.id && selected.agent.revision === current.revision && (selected.head === this.#state?.head || this.#terminal !== undefined)) return;
    this.#dropTerminal(true);
    this.#selectedAgent = { agent: { ...current }, head: this.#state?.head ?? 0n, resets: 0 };
    this.#error = undefined;
    this.#publish();
  }

  openTerminalForHumanRequest(request: HumanRequestItem): void {
    if (this.#closed || this.#status !== "ready") return;
    const currentRequest = this.#state?.humanRequests.get(request.id);
    const agent = currentRequest === undefined || currentRequest.revision !== request.revision ? undefined : this.#state?.agents.get(currentRequest.agent_id);
    if (agent === undefined) {
      this.#error = new SessionError("stale");
      this.#publish();
      return;
    }
    this.selectAgent(agent);
  }

  clearAgentTerminal(): void {
    if (this.#closed) return;
    this.#selectedAgent = undefined;
    this.#dropTerminal(true);
    this.#error = undefined;
    this.#publish();
  }

  beginTerminalSurface(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion) return;
    if (this.#terminalSurfaceToken !== undefined && this.#terminalSurfaceToken !== token) return;
    this.#terminalSurfaceToken = token;
  }

  endTerminalSurface(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    this.#selectedAgent = undefined;
    this.#dropTerminal(true);
    this.#error = new SessionError("internal");
    this.#publish();
  }

  setTerminalSurface(token: object, surface: TerminalSurface | undefined, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    if (surface === undefined) {
      this.endTerminalSurface(token, surfaceVersion);
      return;
    }
    this.#terminalSurface = surface;
    this.#error = undefined;
    this.#reconcileTerminal();
    this.#publish();
  }

  terminalError(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion) return;
    if (this.#terminalSurfaceToken !== undefined && this.#terminalSurfaceToken !== token) return;
    this.#selectedAgent = undefined;
    this.#dropTerminal(true);
    this.#error = new SessionError("internal");
    this.#publish();
  }

  sendTerminalText(token: object, value: string, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion === this.#terminalSurfaceVersion && this.#terminalSurfaceToken === token) this.#terminal?.sendText(value);
  }

  sendTerminalBinary(token: object, value: string, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion === this.#terminalSurfaceVersion && this.#terminalSurfaceToken === token) this.#terminal?.sendBinary(value);
  }

  resizeTerminal(token: object, rows: number, cols: number, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    this.#pendingTerminalResize = { rows, cols };
    this.#flushTerminalResize();
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
    const selection = this.#selection;
    const maximum = selection?.detail?.replyMaxBytes;
    if (selection?.phase !== "ready" || maximum === undefined) return;
    if (reply.length > maximum || new TextEncoder().encode(reply).length > maximum) {
      this.#error = new SessionError("too_large");
      this.#publish();
      return;
    }
    selection.reply = reply;
    this.#error = undefined;
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
    if (reply.length === 0) {
      this.#error = new SessionError("invalid_request");
      this.#publish();
      return;
    }
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
    if (client === undefined || !this.#current(generation)) return;
    void client.connect().catch((error) => {
      if (!this.#current(generation) || this.#status === "closed") return;
      this.#status = "closed";
      this.#error = finiteError(error);
      this.#statusReason = this.#error.code;
      this.#clearSelection();
      if (this.#terminal !== undefined) this.#dropTerminal(true);
      this.#publish();
    });
  }

  #receiveStatus(generation: number, status: SessionStatus): void {
    if (!this.#current(generation)) return;
    this.#status = status;
    this.#statusReason = status === "closed" ? this.#error?.code ?? "closed" : undefined;
    if (status !== "ready") this.#clearSelection();
    // A wire-level state restart resnapshots on the same authenticated socket;
    // exact terminal discovery and handles remain owned by that session.
    if (status !== "ready" && status !== "syncing" && this.#terminal !== undefined) this.#dropTerminal(true);
    if (status !== "closed") this.#error = undefined;
    this.#publish();
    if (status === "ready") this.#reconcileTerminal();
  }

  #receiveState(generation: number, state: StateView): void {
    if (!this.#current(generation)) return;
    this.#state = state;
    const selectedAgent = this.#selectedAgent;
    if (selectedAgent !== undefined) {
      const currentAgent = state.agents.get(selectedAgent.agent.id);
      if (currentAgent === undefined || currentAgent.revision !== selectedAgent.agent.revision) {
        this.#selectedAgent = undefined;
        this.#dropTerminal(true);
        this.#error = new SessionError("stale");
      } else {
        selectedAgent.agent = { ...currentAgent };
        if (this.#terminal === undefined) {
          if (selectedAgent.head !== state.head) {
            selectedAgent.head = state.head;
            this.#terminalRetry = undefined;
          } else if (this.#terminalRetry !== undefined && !this.#terminalRetry.stale && !agentHasRunningTask(selectedAgent.agent, state)) {
            this.#selectedAgent = undefined;
            this.#dropTerminal(false);
          }
        }
      }
    }
    const selected = this.#selection;
    if (selected !== undefined) {
      const current = state.humanRequests.get(selected.request.id);
      if (current === undefined || current.revision !== selected.request.revision) this.#clearSelection();
      else selected.request = current;
    }
    this.#publish();
    this.#reconcileTerminal();
  }

  #receiveError(generation: number, error: SessionError | ProtocolError): void {
    if (!this.#current(generation)) return;
    this.#error = error;
    if (this.#status === "closed") this.#statusReason = error.code;
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

  #reconcileTerminal(): void {
    const selected = this.#selectedAgent;
    const surface = this.#terminalSurface;
    const session = this.#client?.session;
    const stateAgent = selected === undefined ? undefined : this.#state?.agents.get(selected.agent.id);
    if (this.#closed || this.#status !== "ready" || selected === undefined || surface === undefined || session === undefined || stateAgent === undefined || stateAgent.revision !== selected.agent.revision || this.#state?.head !== selected.head || this.#terminalRetry?.head === selected.head || this.#terminal !== undefined) return;
    const generation = ++this.#terminalGeneration;
    const controller = new TerminalController({
      session,
      agentId: selected.agent.id,
      expectedAgentRevision: selected.agent.revision,
      expectedHead: selected.head,
      surface,
      onChange: (snapshot) => this.#receiveTerminalSnapshot(generation, controller, snapshot),
    });
    this.#terminal = controller;
    controller.start();
  }

  #receiveTerminalSnapshot(generation: number, controller: TerminalController, snapshot: TerminalControllerSnapshot): void {
    if (this.#closed || generation !== this.#terminalGeneration || controller !== this.#terminal) return;
    if (snapshot.phase === "closed") {
      if (snapshot.reset && this.#recoverFromTerminalReset()) return;
      const selected = this.#selectedAgent;
      const state = this.#state;
      const staleDiscovery = snapshot.error?.code === "stale";
      const retryDiscovery =
        snapshot.retryDiscovery &&
        selected !== undefined &&
        state !== undefined &&
        (staleDiscovery || agentHasRunningTask(selected.agent, state));
      this.#dropTerminal(false);
      if (retryDiscovery && selected !== undefined && state !== undefined) {
        if (selected.head === state.head) {
          this.#terminalRetry = { head: selected.head, stale: staleDiscovery };
        }
        else selected.head = state.head;
        this.#error = undefined;
        this.#publish();
        return;
      }
      this.#selectedAgent = undefined;
      this.#error = snapshot.error?.code === "stale" || snapshot.error?.code === "internal" ? snapshot.error : undefined;
      this.#publish();
      return;
    }
    if (snapshot.phase === "closing") {
      if (snapshot.error !== undefined && !snapshot.reset) this.#disarmTerminal(snapshot.error);
      return;
    }
    if (snapshot.phase === "ready") this.#terminalResetBurst = 0;
    this.#publish();
    if (snapshot.writable) this.#flushTerminalResize();
  }

  /**
   * A server replay reset ends the protocol handle by design. Recover in
   * place: keep the sidebar selection, remount the display (a fresh empty
   * surface — the retained output the old scrollback showed is gone), and
   * reconcile a new controller against current state. Bounded so a reset
   * storm cannot loop; past the bound the ordinary stale teardown stands.
   */
  #recoverFromTerminalReset(): boolean {
    const selected = this.#selectedAgent;
    if (selected === undefined || this.#status !== "ready" || this.#terminalResetBurst >= 3) return false;
    const current = this.#state?.agents.get(selected.agent.id);
    if (current === undefined) return false;
    this.#terminalResetBurst += 1;
    selected.resets += 1;
    selected.agent = { ...current };
    selected.head = this.#state?.head ?? selected.head;
    this.#terminal = undefined;
    ++this.#terminalGeneration;
    this.#terminalRetry = undefined;
    this.#terminalSurface = undefined;
    this.#terminalSurfaceToken = undefined;
    this.#pendingTerminalResize = undefined;
    ++this.#terminalSurfaceVersion;
    this.#error = undefined;
    this.#publish();
    return true;
  }

  #flushTerminalResize(): void {
    const terminal = this.#terminal;
    const resize = this.#pendingTerminalResize;
    if (terminal === undefined || resize === undefined || !terminal.snapshot.writable) return;
    this.#pendingTerminalResize = undefined;
    terminal.resize(resize.rows, resize.cols);
  }

  #dropTerminal(closeSession: boolean): void {
    const terminal = this.#terminal;
    this.#terminal = undefined;
    this.#terminalResetBurst = 0;
    this.#terminalRetry = undefined;
    ++this.#terminalGeneration;
    this.#terminalSurface = undefined;
    this.#terminalSurfaceToken = undefined;
    this.#pendingTerminalResize = undefined;
    ++this.#terminalSurfaceVersion;
    if (closeSession && terminal !== undefined) void terminal.close();
  }

  #disarmTerminal(error: SessionError | ProtocolError): void {
    const terminal = this.#terminal;
    this.#selectedAgent = undefined;
    this.#dropTerminal(false);
    this.#error = error;
    if (terminal !== undefined) void terminal.close();
    this.#publish();
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
        replyMaxBytes: selection.detail?.replyMaxBytes ?? 0,
        reply: selection.reply,
      },
      selectedAgent: this.#selectedAgent === undefined ? undefined : {
        id: this.#selectedAgent.agent.id,
        name: this.#selectedAgent.agent.name,
        revision: this.#selectedAgent.agent.revision,
      },
      terminal: this.#selectedAgent === undefined ? undefined : {
        agentId: this.#selectedAgent.agent.id,
        agentName: this.#selectedAgent.agent.name,
        agentRevision: this.#selectedAgent.agent.revision,
        phase: this.#terminal?.snapshot.phase ?? "idle",
        writable: this.#terminal?.snapshot.writable ?? false,
        leaseOperation: this.#terminal?.snapshot.leaseOperation ?? "none",
        error: this.#terminal?.snapshot.error,
        resets: this.#selectedAgent.resets,
        surfaceVersion: this.#terminalSurfaceVersion,
      },
    };
  }

  #publish(): void {
    if (!this.#closed) this.#options.onChange(this.#snapshot());
    if (this.#closed) return;
    const status: FactoryAppStatus = this.#status === "closed"
      ? { status: "closed", reason: this.#statusReason ?? "closed" }
      : { status: this.#status };
    if (sameStatus(this.#lastStatus, status)) return;
    this.#lastStatus = status;
    try { this.#options.onStatusChange?.(status); } catch { /* host callbacks cannot break controller ownership */ }
  }
}

function finiteError(error: unknown): SessionError | ProtocolError {
  return error instanceof SessionError || error instanceof ProtocolError ? error : new SessionError("connection");
}

function sameStatus(left: FactoryAppStatus | undefined, right: FactoryAppStatus): boolean {
  if (left === undefined || left.status !== right.status) return false;
  return left.status !== "closed" || (right.status === "closed" && left.reason === right.reason);
}
