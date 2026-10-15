import {
  type BrowserClientsView,
  MAX_TERMINAL_PAYLOAD,
  CAPABILITIES,
  ProtocolError,
  SessionError,
  consumePairingChallenge,
  createBrowserClient,
  type BrowserClient,
  type ProjectContentOperation,
  type ProjectContentInput,
  type ProjectContentOutput,
  type BrowserSession,
  type BrowserSessionOptions,
  type AgentItem,
  type AgentControlAction,
  type HumanRequestItem,
  type SessionErrorCode,
  type SessionStatus,
  type DiscoveredAccountView,
  type RepositoryView,
  type IntakeView,
  type StateView,
  type TaskItem,
  type TerminalReset,
  type RunPathsView,
  randomOperationID,
  type OperationalGraphView,
} from "@dark-factory/client";

export type ProjectContentCall = (operation: ProjectContentOperation, input: ProjectContentInput) => Promise<ProjectContentOutput>;
import { agentCurrentTask, type RunPathSample } from "./console-view.js";
import { HumanRequestFlow, type HumanRequestPhase } from "./human-request-flow.js";
import { MAX_PENDING_INPUT_BYTES, TerminalController, type TerminalControllerSnapshot, type TerminalErrorSource, type TerminalSurface } from "./terminal-controller.js";

type BrowserEndpoint = Readonly<{ url: string; host: string }>;

export function browserEndpoint(port = 43123): BrowserEndpoint {
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new SessionError("connection");
  const host = `127.0.0.1:${port}`;
  return { url: `ws://${host}/browser`, host };
}

const DEFAULT_BROWSER_ENDPOINT = browserEndpoint();
// The daemon caches run paths for five seconds, so one timer at ten never
// outruns the cache and never lets a room go more than a cycle stale.
const RUN_PATHS_POLL_MS = 10_000;
// Structure changes as slowly as an edit lands, so the floor re-reads it once a
// minute off the same timer rather than watching a filesystem it cannot see.
// The graph carries live readings, so it is refreshed every other tick.
const GRAPH_POLL_TICKS = 2;

export type FactoryHumanRequestView = Readonly<{
  request: HumanRequestItem;
  phase: Exclude<HumanRequestPhase, "ended">;
  question?: string;
  options: readonly string[];
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
  /** Current running task title, when this selected agent has one. */
  taskTitle?: string;
  /** The terminal ended before durable task finalization was published. */
  finishing: boolean;
  phase: "idle" | "resolving" | "attaching" | "acquiring" | "ready" | "closing" | "closed";
  writable: boolean;
  error?: SessionError | ProtocolError;
  /** Whether terminal attachment, input, or the mounted display reported the error. */
  errorSource?: TerminalErrorSource;
  paused: boolean;
  controlPending?: AgentControlAction;
  controlError?: SessionError | ProtocolError;
  controlStatus?: "delivered" | "delivery_unknown" | "rejected" | "stopping" | "queued";
  controlReady: boolean;
  queued: boolean;
  /** The mounted Xterm scrollback belongs to this agent's completed work. */
  hasOutputSurface: boolean;
  /** Server replay resets survived by this terminal view; > 0 shows the banner. */
  resets: number;
  surfaceVersion: number;
}>;

/** One console edit at a time: the sidebar shows exactly one form. */
export type FactoryEditView = Readonly<{
  /** The agent or task the edit was for; a refusal belongs to its form alone. */
  target: string;
  pending: boolean;
  error?: SessionError | ProtocolError;
}>;

export type FactoryAppSnapshot = Readonly<{
  status: SessionStatus;
  state?: StateView;
  error?: SessionError | ProtocolError;
  selectedHumanRequest?: FactoryHumanRequestView;
  selectedAgent?: FactoryAgentSelection;
  terminal?: FactoryTerminalView;
  /** Regenerable structure per project, empty until the daemon serves it. */
  graphs?: ReadonlyMap<string, OperationalGraphView>;
  /** The error code of each project's last refused graph read, until one is served. */
  graphErrors?: ReadonlyMap<string, string>;
  /** Repository directories each running agent's live run is changing. */
  runPaths?: ReadonlyMap<string, RunPathSample>;
  /** Most recent observed paths remain an annotation after that run ends. */
  lastRunPaths?: ReadonlyMap<string, RunPathSample>;
  edit?: FactoryEditView;
  /** True only while a ready session carries the full loopback grant. */
  dispatchAllowed?: boolean;
  repositories?: ReadonlyMap<string, readonly RepositoryView[]>;
  intake?: ReadonlyMap<string, IntakeView>;
  intakePending?: ReadonlySet<string>;
  intakeErrors?: ReadonlyMap<string, string>;
}>;

export type FactoryAppStatus =
  | Readonly<{ status: Exclude<SessionStatus, "closed"> }>
  | Readonly<{ status: "closed"; reason: SessionErrorCode }>;

type HumanSession = Pick<BrowserSession, "getHumanRequestDetail" | "replyHumanRequest" | "cancelHumanRequest">;
type TerminalSession = Pick<BrowserSession, "resolveAgentTerminal" | "openTerminal" | "close">;
type AgentTaskSession = Pick<BrowserSession, "enqueueAgentTaskWithFiles" | "enqueueAgentTask" | "controlAgent" | "resolveAgentTerminal">;
type ConsoleSession = Pick<BrowserSession, "createProject" | "getRepositories" | "intake" | "updateTask" | "getOperationalGraph" | "getRunPaths" | "discoverAccounts" | "linkAccount" | "listBrowserClients" | "revokeBrowserClient" | "githubConnection" | "clientId">;
type RemoteInviteSession = Pick<BrowserSession, "inviteRemote" | "capabilities">;
type ControlledClient = Pick<BrowserClient, "connect" | "close"> & { readonly session?: HumanSession & TerminalSession & AgentTaskSession & ConsoleSession & RemoteInviteSession & Partial<Pick<BrowserSession, "projectContent" | "setDispatch">> };
type ClientFactory = (options: BrowserSessionOptions) => ControlledClient;

export type FactoryAppControllerOptions = {
  origin: string;
  location: Pick<Location, "hash" | "pathname" | "search">;
  history: Pick<History, "replaceState" | "state">;
  onChange: (snapshot: FactoryAppSnapshot) => void;
  onStatusChange?: (status: FactoryAppStatus) => void;
  /** Package-internal endpoint chosen by FactoryApp's validated public prop. */
  browser?: BrowserEndpoint;
  /** Package-internal construction boundary used by DOM-free causal tests. */
  clientFactory?: ClientFactory;
};

type AgentTerminalSelection = {
  agent: AgentItem;
  head: bigint;
  /** Public task identity that terminal discovery is allowed to resolve. */
  task?: Pick<TaskItem, "id" | "revision">;
  finishing: boolean;
  /** Server replay resets survived by this terminal view (banner state). */
  resets: number;
  /** Server session and cursor at which a replay reset can safely resume. */
  resume?: Pick<TerminalReset, "sessionId" | "head">;
  controlPending?: AgentControlAction;
  controlError?: SessionError | ProtocolError;
  controlStatus?: FactoryTerminalView["controlStatus"];
  queuedTaskID?: string;
};

type TerminalReplacement = {
  agentId?: string;
  agentRevision?: bigint;
  error?: SessionError | ProtocolError;
};

/** Keep queue controls disabled until the snapshot carries this accepted write. */
type TaskEditConfirmation = {
  edit: FactoryEditView;
  previousRevision: bigint;
  revision: bigint;
};

function agentQueuedTask(agent: AgentItem, state: StateView) {
  for (const task of state.tasks.values()) {
    if (task.assigned_agent_id === agent.id && task.status === "queued") return task;
  }
  return undefined;
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
  readonly #human = new HumanRequestFlow<void>({
    active: () => !this.#closed && this.#status === "ready",
    currentRequest: (_scope, requestID) => this.#state?.humanRequests.get(requestID),
    session: () => this.#client?.session,
    onChange: () => this.#publish(),
    onError: (error) => { this.#error = error; },
    afterAction: "clear",
    unavailableError: new SessionError("stale"),
  });
  #selectedAgent: AgentTerminalSelection | undefined;
  #terminal: TerminalController | undefined;
  #terminalSurface: TerminalSurface | undefined;
  #terminalSurfaceToken: object | undefined;
  #terminalDisplayError: SessionError | undefined;
  #terminalSurfaceVersion = 0;
  #terminalResetBurst = 0;
  #terminalRetry: { head: bigint; stale: boolean } | undefined;
  #terminalReplacement: TerminalReplacement | undefined;
  #pendingTerminalInput = new Uint8Array(0);
  #pendingTerminalResize: { rows: number; cols: number } | undefined;
  #graphs: ReadonlyMap<string, OperationalGraphView> = new Map();
  #graphErrors: ReadonlyMap<string, string> = new Map();
  #graphPending = new Set<string>();
  #runPaths: ReadonlyMap<string, RunPathSample> = new Map();
  #lastRunPaths: ReadonlyMap<string, RunPathSample> = new Map();
  #runPathsTimer: ReturnType<typeof setInterval> | undefined;
  #runPathsTicks = 0;
  #runPathsPending = false;
  #runPathsDue = false;
  #edit: FactoryEditView | undefined;
  #taskEditConfirmation: TaskEditConfirmation | undefined;
  readonly #repositories = new Map<string, readonly RepositoryView[]>();
  readonly #intake = new Map<string, IntakeView>();
  readonly #intakePending = new Set<string>();
  readonly #intakeErrors = new Map<string, string>();
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
      this.#error = new SessionError("connection");
      this.#statusReason = this.#error.code;
      this.#publish();
      return;
    }

    const factory = this.#options.clientFactory ?? createBrowserClient;
    let client: ControlledClient;
    try {
      const browser = this.#options.browser ?? DEFAULT_BROWSER_ENDPOINT;
      client = factory({
        url: browser.url,
        host: browser.host,
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
    this.#discardTaskEditConfirmation();
    ++this.#generation;
    this.#human.clear(true);
    this.#selectedAgent = undefined;
    this.#terminalReplacement = undefined;
    this.#dropPendingTerminalInput();
    this.#closeTerminal();
    this.watchRunPaths(false);
    this.#client?.close();
  }

  async setDispatch(expectedRevision: bigint, enabled: boolean): Promise<{ revision: bigint; enabled: boolean }> {
    const session = this.#client?.session;
    if (this.#closed || this.#status !== "ready" || session?.setDispatch === undefined) throw new SessionError("unsupported");
    return session.setDispatch({ expectedRevision, enabled });
  }

  async projectContent(operation: ProjectContentOperation, input: ProjectContentInput): Promise<ProjectContentOutput> {
    const session = this.#client?.session;
    if (session?.projectContent === undefined) throw new SessionError("unsupported");
    return session.projectContent(operation, input);
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
    this.#replaceTerminal({ agentId: current.id, agentRevision: current.revision });
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

  /**
   * The floor's operational graph, for every configured project and no other.
   * It is regenerable, not durable state, so it is fetched on demand, and a
   * project the daemon cannot serve keeps the graph last served.
   */
  loadGraphs(): void {
    const session = this.#client?.session;
    const state = this.#state;
    if (this.#closed || this.#status !== "ready" || session === undefined || state === undefined) return;
    // A project still answering the last round is not asked again: one slow
    // walk costs the floor its own room's refresh, never every other project's.
    const asked = [...state.projects.keys()].filter((projectId) => !this.#graphPending.has(projectId));
    if (asked.length === 0) return;
    const generation = this.#generation;
    for (const projectId of asked) {
      this.#graphPending.add(projectId);
      void session.getOperationalGraph(projectId).then(
        (graph) => {
          this.#graphPending.delete(projectId);
          // Live readings move without the structure moving; the floor lays
          // out by digest, so a new reading never moves a machine.
          if (!this.#current(generation)) return;
          const first = !this.#graphs.has(projectId);
          this.#graphs = new Map(this.#graphs).set(projectId, graph);
          this.#graphErrors = new Map([...this.#graphErrors].filter(([id]) => id !== projectId));
          this.#publish();
          // A project served for the first time has machines its running agents can stand at:
          // ask now, or as soon as the round in flight is answered. A refresh waits for the tick.
          if (!first || this.#runPathsTimer === undefined) return;
          if (this.#runPathsPending) this.#runPathsDue = true;
          else this.#pollRunPaths();
        },
        // A refused answer keeps the structure last served for that project
        // rather than emptying its block of rooms for one cycle, and says why.
        (error: unknown) => {
          this.#graphPending.delete(projectId);
          if (this.#current(generation)) { this.#graphErrors = new Map(this.#graphErrors).set(projectId, finiteError(error).code); this.#publish(); }
        },
      );
    }
  }

  /**
   * Where each running agent is working, and every other tick the graph it
   * is working on. Both are live hints, not durable state, so they are polled
   * only while the floor is on screen and the poll stops the moment the floor
   * is hidden or the session leaves ready.
   */
  watchRunPaths(active: boolean): void {
    if (!active || this.#closed || this.#status !== "ready") {
      if (this.#runPathsTimer !== undefined) clearInterval(this.#runPathsTimer);
      this.#runPathsTimer = undefined;
      this.#runPathsDue = false;
      return;
    }
    if (this.#runPathsTimer !== undefined) return;
    this.#runPathsTicks = 0;
    this.#runPathsTimer = setInterval(() => {
      this.#pollRunPaths();
      this.#runPathsTicks += 1;
      if (this.#runPathsTicks % GRAPH_POLL_TICKS === 0) this.loadGraphs();
    }, RUN_PATHS_POLL_MS);
    this.#pollRunPaths();
  }

  /**
   * One round: every agent on a running task in a project whose structure is
   * served, and nobody else. Without rooms a path places no one, and every
   * answer costs the daemon a walk.
   */
  #pollRunPaths(): void {
    const session = this.#client?.session;
    const state = this.#state;
    if (session === undefined || state === undefined || this.#runPathsPending) return;
    const running = [...state.agents.values()]
      .map((agent) => {
        const task = agentCurrentTask(agent, state);
        return task === undefined || !this.#graphs.has(task.project_id) ? undefined : { agentId: agent.id, taskId: task.id, taskRevision: task.revision, projectId: task.project_id };
      })
      .filter((task): task is { agentId: string; taskId: string; taskRevision: bigint; projectId: string } => task !== undefined);
    if (running.length === 0) {
      // Nothing to ask leaves no round in flight, so the round a served
      // structure triggers is not lost behind an empty one.
      if (this.#runPaths.size !== 0) {
        this.#runPaths = new Map();
        this.#publish();
      }
      return;
    }
    this.#runPathsPending = true;
    const generation = this.#generation;
    void Promise.all(running.map(({ agentId, taskId, taskRevision, projectId }) => session.getRunPaths(agentId).then(
      (answer) => [agentId, sampleFor(taskId, taskRevision, projectId, answer)] as const,
      // A refused answer keeps the last sample as a retained observation.
      () => [agentId, this.#runPaths.get(agentId)] as const,
    ))).then((answers) => {
      this.#runPathsPending = false;
      // A round owed to a structure that arrived meanwhile is asked now, and
      // only while the floor is still shown; an abandoned round owes nothing.
      const due = this.#runPathsDue;
      this.#runPathsDue = false;
      if (!this.#current(generation) || this.#status !== "ready") return;
      if (due && this.#runPathsTimer !== undefined) this.#pollRunPaths();
      // An agent that stopped running loses its entry; an unchanged round is
      // not a new snapshot, so the floor does not re-render on a heartbeat.
      const current = new Map(answers.filter((entry): entry is readonly [string, RunPathSample] => {
        const sample = entry[1];
        const agent = this.#state?.agents.get(entry[0]);
        const task = sample === undefined || agent === undefined || this.#state === undefined ? undefined : agentCurrentTask(agent, this.#state);
        return sample !== undefined && task?.id === sample.taskId && task.revision === sample.taskRevision && task.project_id === sample.projectId;
      }));
      const last = new Map(this.#lastRunPaths);
      for (const [agentId, sample] of answers) if (sample !== undefined && sample.paths.length > 0) last.set(agentId, sample);
      if (sameSamples(this.#runPaths, current) && sameSamples(this.#lastRunPaths, last)) return;
      this.#runPaths = current;
      this.#lastRunPaths = last;
      this.#publish();
    });
  }

  /** Cancel or retry one task against its exact revision. */
  async editTask(task: Pick<TaskItem, "id" | "revision">, change: { cancel?: boolean; retry?: boolean }): Promise<boolean> {
    const session = this.#client?.session;
    if (this.#closed || this.#status !== "ready" || session === undefined || this.#edit?.pending === true) return false;
    const generation = this.#generation;
    const edit: FactoryEditView = { target: task.id, pending: true };
    this.#edit = edit;
    this.#publish();
    try {
      const result = await session.updateTask({ taskId: task.id, expectedRevision: task.revision, ...change });
      if (!this.#current(generation) || this.#edit !== edit) return false;
      if (result.revision !== task.revision + 1n) throw new SessionError("stale");
      const current = this.#state?.tasks.get(task.id);
      if (current !== undefined && current.revision >= result.revision) {
        this.#edit = undefined;
        this.#publish();
        return true;
      }
      if (current === undefined || current.revision !== task.revision) {
        this.#edit = { target: task.id, pending: false, error: new SessionError("stale") };
        this.#publish();
        return false;
      }
      this.#taskEditConfirmation = { edit, previousRevision: task.revision, revision: result.revision };
      // The row stays disabled until STATE carries this revision, but a
      // successful write retains the caller's existing close-on-acceptance
      // contract.
      return true;
    } catch (error) {
      if (!this.#current(generation) || this.#edit !== edit) return false;
      this.#edit = { target: task.id, pending: false, error: finiteError(error) };
    }
    this.#publish();
	return false;
  }

  /** Queue work from the Tasks panel: for one agent, or for any eligible worker in its project. */
  async addTask(agent: Pick<AgentItem, "id" | "revision">, instruction: string, mode: "queue" | "any", files: readonly File[] = []): Promise<boolean> {
    const session = this.#client?.session;
    if (this.#closed || this.#status !== "ready" || session === undefined || this.#edit?.pending === true) return false;
    const generation = this.#generation;
    const edit: FactoryEditView = { target: agent.id, pending: true };
    this.#edit = edit;
    this.#publish();
    try {
      const request = { agentId: agent.id, expectedAgentRevision: agent.revision, instruction: instruction.trim(), mode };
      if (files.length === 0) await session.enqueueAgentTask(request);
      else await session.enqueueAgentTaskWithFiles(request, files);
      if (!this.#current(generation) || this.#edit !== edit) return false;
      this.#edit = undefined;
      this.#publish();
      return true;
    } catch (error) {
      if (!this.#current(generation) || this.#edit !== edit) return false;
      this.#edit = { target: agent.id, pending: false, error: finiteError(error) };
      this.#publish();
      return false;
    }
  }

  clearAgentTerminal(): void {
    if (this.#closed) return;
    this.#replaceTerminal({});
  }

  /**
   * Leave the terminal without dropping the agent the sidebar is showing.
   * Replacing the terminal with the same exact agent detaches through the one
   * teardown path, so the protocol handle is released exactly as CLOSE does.
   */
  closeAgentTerminal(): void {
    const selected = this.#selectedAgent;
    if (this.#closed) return;
    this.#replaceTerminal(selected === undefined ? {} : { agentId: selected.agent.id, agentRevision: selected.agent.revision });
  }

  /** Send one durable receipt to the active task; raw terminal keys stay raw. */
  async controlAgent(action: AgentControlAction, instruction = ""): Promise<boolean> {
    const selected = this.#selectedAgent;
    const session = this.#client?.session;
    const task = selected?.task;
    if (
      this.#closed || this.#status !== "ready" || selected === undefined || task === undefined || selected.finishing ||
      session === undefined || selected.controlPending !== undefined || this.#terminal?.snapshot.phase !== "ready"
    ) return false;
    const current = this.#state?.tasks.get(task.id);
    const target = this.#terminal.target;
    if (current === undefined || current.revision !== task.revision || current.status !== "running" || current.assigned_agent_id !== selected.agent.id || target === undefined) {
      selected.controlError = new SessionError("stale");
      this.#publish();
      return false;
    }
    const body = instruction.trim();
    if ((action === "message" || action === "replace") && body === "") return false;
    let operationId: string, successorTaskId = "", successorIncarnationId = "";
    try {
      operationId = randomOperationID();
      if (action === "replace") {
        successorTaskId = randomOperationID();
        successorIncarnationId = randomOperationID();
      }
    } catch (error) {
      selected.controlError = finiteError(error);
      this.#publish();
      return false;
    }
    const generation = this.#generation;
    selected.controlPending = action;
    selected.controlError = undefined;
    selected.controlStatus = undefined;
    this.#publish();
    try {
      const result = await session.controlAgent({
        operationId,
        taskId: task.id,
        expectedTaskRevision: task.revision,
        target,
        action,
        instruction: action === "message" || action === "replace" ? body : "",
        successorTaskId,
        successorIncarnationId,
      });
      if (!this.#current(generation) || this.#selectedAgent !== selected) return false;
      selected.controlPending = undefined;
      selected.controlStatus = result.status;
      selected.controlError = result.status === "delivery_unknown" ? new SessionError("connection") : undefined;
      this.#publish();
      return result.status !== "delivery_unknown" && result.status !== "rejected";
    } catch (error) {
      if (!this.#current(generation) || this.#selectedAgent !== selected) return false;
      selected.controlPending = undefined;
      selected.controlError = finiteError(error);
      this.#publish();
      return false;
    }
  }

  /** The provider logins on this machine; SETTINGS explicitly asks for them. */
  async loadRepositories(projectId: string): Promise<void> {
    const session = this.#client?.session;
    if (!this.#current(this.#generation) || session === undefined || typeof session.getRepositories !== "function") return;
    const generation = this.#generation;
    try {
      const repositories = await session.getRepositories(projectId);
      if (!this.#current(generation) || this.#status !== "ready") return;
      this.#repositories.set(projectId, repositories);
    } catch {
      if (!this.#current(generation) || this.#status !== "ready") return;
      this.#repositories.delete(projectId);
    }
    this.#publish();
  }
  async loadIntake(projectId: string): Promise<void> {
    const session = this.#client?.session;
    if (!this.#current(this.#generation) || session === undefined) return;
    const generation = this.#generation;
    this.#intakePending.add(projectId); this.#publish();
    try {
      const result = await session.intake({ action: "list", project_id: projectId });
      if (!this.#current(generation) || this.#status !== "ready") return;
      this.#intake.set(projectId, result); this.#intakeErrors.delete(projectId);
    }
    catch (error) { this.#intakeErrors.set(projectId, finiteError(error).code); }
    finally { this.#intakePending.delete(projectId); this.#publish(); }
  }
  async intakeAction(projectId: string, request: Parameters<BrowserSession["intake"]>[0]): Promise<void> {
    const session = this.#client?.session;
    if (!this.#current(this.#generation) || session === undefined) return;
    const generation = this.#generation;
    if (request.action === "preview") {
      const current = this.#intake.get(projectId);
      if (current !== undefined) this.#intake.set(projectId, { ...current, candidates: undefined, reviewed_revision: undefined, next_page: undefined });
    }
    try {
      let result = await session.intake(request);
      if (request.action === "accept" && result.state === "accepted" && result.acceptance_id !== undefined) {
        result = await session.intake({ action: "import", acceptance_id: result.acceptance_id });
        if (!this.#current(generation) || this.#status !== "ready") return;
        if (result.state === "imported") result = await session.intake({ action: "list", project_id: projectId });
      }
      if (request.action === "update") result = await session.intake({ action: "list", project_id: projectId });
      if (!this.#current(generation) || this.#status !== "ready") return;
      const current = this.#intake.get(projectId);
      this.#intake.set(projectId, result.sources === undefined && current !== undefined ? { ...current, ...result } : result);
      if (["content_changed", "ineligible", "withdrawn", "withdrawal_pending"].includes(result.state)) this.#intakeErrors.set(projectId, result.state);
      else this.#intakeErrors.delete(projectId);
    }
    catch (error) { if (this.#current(generation) && this.#status === "ready") this.#intakeErrors.set(projectId, finiteError(error).code); }
    this.#publish();
  }

  beginTerminalSurface(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion) return;
    if (this.#terminalSurfaceToken !== undefined && this.#terminalSurfaceToken !== token) return;
    this.#terminalSurfaceToken = token;
  }

  endTerminalSurface(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    this.closeAgentTerminal();
  }

  setTerminalSurface(token: object, surface: TerminalSurface | undefined, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    if (surface === undefined) {
      this.endTerminalSurface(token, surfaceVersion);
      return;
    }
    this.#terminalSurface = surface;
    this.#terminalDisplayError = undefined;
    this.#error = undefined;
    this.#reconcileTerminal();
    this.#publish();
  }

  terminalError(token: object, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#closed || this.#selectedAgent === undefined || surfaceVersion !== this.#terminalSurfaceVersion) return;
    if (this.#terminalSurfaceToken !== undefined && this.#terminalSurfaceToken !== token) return;
    this.#terminalSurfaceToken = undefined;
    this.#terminalDisplayError = new SessionError("internal");
    this.#publish();
  }

  sendTerminalText(token: object, value: string, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#selectedAgent === undefined || this.#terminalSurfaceToken === undefined || surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token || typeof value !== "string") return;
    if (value.length > MAX_PENDING_INPUT_BYTES) {
      this.#disarmTerminal(new SessionError("too_large"));
      return;
    }
    this.#queueTerminalInput(new TextEncoder().encode(value));
  }

  sendTerminalBinary(token: object, value: string, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (this.#selectedAgent === undefined || this.#terminalSurfaceToken === undefined || surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token || typeof value !== "string") return;
    if (value.length > MAX_PENDING_INPUT_BYTES) {
      this.#disarmTerminal(new SessionError("too_large"));
      return;
    }
    const bytes = new Uint8Array(value.length);
    for (let index = 0; index < value.length; index += 1) {
      const code = value.charCodeAt(index);
      if (code > 0xff) {
        this.#disarmTerminal(new ProtocolError("malformed"));
        return;
      }
      bytes[index] = code;
    }
    this.#queueTerminalInput(bytes);
  }

  resizeTerminal(token: object, rows: number, cols: number, surfaceVersion = this.#terminalSurfaceVersion): void {
    if (surfaceVersion !== this.#terminalSurfaceVersion || this.#terminalSurfaceToken !== token) return;
    this.#pendingTerminalResize = { rows, cols };
    this.#flushTerminalResize();
  }

  async selectHumanRequest(request: HumanRequestItem): Promise<void> { this.#error = undefined; return this.#human.open(undefined, request); }

  setHumanReply(reply: string): void {
    this.#error = undefined;
    this.#human.setReply(reply);
  }

  clearHumanRequest(): void {
    this.#human.clear();
  }

  async replyHumanRequest(): Promise<void> { this.#error = undefined; return this.#human.reply(); }

  async cancelHumanRequest(): Promise<void> { this.#error = undefined; return this.#human.cancel(); }

  #connect(generation: number): void {
    const client = this.#client;
    if (client === undefined || !this.#current(generation)) return;
    void client.connect().catch((error) => {
      if (!this.#current(generation) || this.#status === "closed") return;
      this.#status = "closed";
      this.#error = finiteError(error);
      this.#statusReason = this.#error.code;
      this.#human.clear(true);
      this.#terminalReplacement = undefined;
      this.#dropPendingTerminalInput();
      if (this.#terminal !== undefined) this.#closeTerminal();
      this.#publish();
    });
  }

  #receiveStatus(generation: number, status: SessionStatus): void {
    if (!this.#current(generation)) return;
    this.#status = status;
    this.#statusReason = status === "closed" ? this.#closedReason() : undefined;
    if (status !== "ready") {
      this.#discardTaskEditConfirmation();
      this.#human.clear(true);
      // A reconnect must not show a code minted for the connection that dropped.
      this.#repositories.clear();
      this.#intake.clear();
    }
    // A wire-level state restart resnapshots on the same authenticated socket;
    // exact terminal discovery and handles remain owned by that session.
    if (status !== "ready" && status !== "syncing") {
      this.#terminalReplacement = undefined;
      this.#dropPendingTerminalInput();
      if (this.#terminal !== undefined) this.#closeTerminal();
    }
    if (status !== "closed") this.#error = undefined;
    if (status !== "ready") this.watchRunPaths(false);
    this.#publish();
    if (status === "ready") this.#reconcileTerminal();
  }

  #receiveState(generation: number, state: StateView): void {
    if (!this.#current(generation)) return;
    this.#state = state;
    const confirmation = this.#taskEditConfirmation;
    if (confirmation !== undefined) {
      const current = state.tasks.get(confirmation.edit.target);
      if (current !== undefined && current.revision >= confirmation.revision) {
        this.#taskEditConfirmation = undefined;
        if (this.#edit === confirmation.edit) this.#edit = undefined;
      } else if (current === undefined || current.revision !== confirmation.previousRevision) {
        this.#taskEditConfirmation = undefined;
        if (this.#edit === confirmation.edit) {
          this.#edit = { target: confirmation.edit.target, pending: false, error: new SessionError("stale") };
        }
      }
    }
    // A graph belongs to a project; a project that is gone has no machines and no refusal.
    if ([...this.#graphs.keys(), ...this.#graphErrors.keys()].some((projectId) => !state.projects.has(projectId))) {
      this.#graphs = new Map([...this.#graphs].filter(([projectId]) => state.projects.has(projectId)));
      this.#graphErrors = new Map([...this.#graphErrors].filter(([projectId]) => state.projects.has(projectId)));
    }
    const selectedAgent = this.#selectedAgent;
    const replacementAgentID = this.#terminalReplacement?.agentId;
    if (replacementAgentID !== undefined) {
      const currentAgent = state.agents.get(replacementAgentID);
      this.#terminalReplacement = currentAgent === undefined
        ? { error: new SessionError("stale") }
        : { agentId: currentAgent.id, agentRevision: currentAgent.revision };
    } else if (selectedAgent !== undefined && this.#terminalReplacement === undefined) {
      const currentAgent = state.agents.get(selectedAgent.agent.id);
      if (currentAgent === undefined) {
        this.#replaceTerminal({ error: new SessionError("stale") });
      } else if (currentAgent.revision !== selectedAgent.agent.revision) {
        this.#replaceTerminal({ agentId: currentAgent.id, agentRevision: currentAgent.revision });
      } else {
        selectedAgent.agent = { ...currentAgent };
        const headChanged = selectedAgent.head !== state.head;
        if (this.#terminal === undefined) {
          this.#refreshTerminalTask(selectedAgent, state);
          if (headChanged) {
            selectedAgent.head = state.head;
            this.#terminalRetry = undefined;
          }
        } else {
          // The open terminal remains bound to its already-authorized stream,
          // but a later public snapshot is the only valid observation for a
          // durable control. Refresh the same task's revision without
          // detaching its surface or crossing to another task's stream.
          const running = agentCurrentTask(currentAgent, state);
          if (running !== undefined && selectedAgent.task?.id === running.id && selectedAgent.task.revision !== running.revision) this.#refreshTerminalTask(selectedAgent, state);
          else this.#refreshQueuedTask(selectedAgent, state);
          if (headChanged) selectedAgent.head = state.head;
        }
      }
    }
    this.#human.reconcile();
    this.#publish();
    this.#reconcileTerminal();
  }

  #receiveError(generation: number, error: SessionError | ProtocolError): void {
    if (!this.#current(generation)) return;
    this.#error = error;
    if (this.#status === "closed") this.#statusReason = this.#closedReason();
    this.#publish();
  }

  /** A retryable close is transport-shaped for hosts: the client is already reconnecting on it. */
  #closedReason(): SessionErrorCode {
    const error = this.#error;
    return error instanceof SessionError && error.retryable ? "connection" : error?.code ?? "closed";
  }

  #current(generation: number): boolean {
    return !this.#closed && generation === this.#generation;
  }

  #discardTaskEditConfirmation(): void {
    const confirmation = this.#taskEditConfirmation;
    if (confirmation === undefined || this.#edit !== confirmation.edit) return;
    this.#taskEditConfirmation = undefined;
    this.#edit = undefined;
  }

  #reconcileTerminal(): void {
    const selected = this.#selectedAgent;
    const surface = this.#terminalSurface;
    const session = this.#client?.session;
    const stateAgent = selected === undefined ? undefined : this.#state?.agents.get(selected.agent.id);
    if (this.#closed || this.#status !== "ready" || selected === undefined || selected.task === undefined || selected.finishing || surface === undefined || session === undefined || stateAgent === undefined || stateAgent.revision !== selected.agent.revision || this.#state?.head !== selected.head || this.#terminalRetry?.head === selected.head || this.#terminal !== undefined) return;
    const controller = new TerminalController({
      session,
      agentId: selected.agent.id,
      expectedAgentRevision: selected.agent.revision,
      expectedHead: selected.head,
      surface,
      resume: selected.resume,
      retainOnCleanClose: true,
      onChange: (snapshot) => this.#receiveTerminalSnapshot(controller, snapshot),
    });
    this.#terminal = controller;
    controller.start();
  }

  #receiveTerminalSnapshot(controller: TerminalController, snapshot: TerminalControllerSnapshot): void {
    if (this.#closed || controller !== this.#terminal) return;
    if (snapshot.phase === "closed") {
      if (this.#terminalReplacement !== undefined && snapshot.error !== undefined && snapshot.error.code !== "closed") {
        this.#disarmTerminal(snapshot.error);
        return;
      }
      if (this.#finishTerminalReplacement()) return;
      if (snapshot.reset !== undefined && this.#recoverFromTerminalReset(snapshot.reset)) return;
      const selected = this.#selectedAgent;
      const state = this.#state;
      const endedTask = selected?.task;
      const staleDiscovery = snapshot.error?.code === "stale";
      const retryDiscovery =
        snapshot.retryDiscovery &&
        selected !== undefined &&
        state !== undefined &&
        (staleDiscovery || agentCurrentTask(selected.agent, state) !== undefined);
      const cleanExit = snapshot.error === undefined || snapshot.error.code === "closed";
      this.#retireTerminal(cleanExit);
      if (retryDiscovery && selected !== undefined && state !== undefined) {
        this.#refreshTerminalTask(selected, state);
        if (staleDiscovery) {
          // A concurrent state observation already advanced selected.head.
          // Retry its one stale terminal-target lookup when the view remounts.
          this.#terminalRetry = undefined;
        } else if (selected.head === state.head) {
          this.#terminalRetry = { head: selected.head, stale: staleDiscovery };
        }
        else selected.head = state.head;
        this.#error = undefined;
        this.#publish();
        return;
      }
      const current = selected === undefined ? undefined : state?.agents.get(selected.agent.id);
      if (cleanExit && selected !== undefined && state !== undefined && current !== undefined) {
        selected.agent = { ...current };
        selected.head = state.head;
        if (selected.queuedTaskID === endedTask?.id) selected.queuedTaskID = undefined;
        this.#refreshQueuedTask(selected, state);
        const runningTask = agentCurrentTask(current, state);
        const running = runningTask === undefined ? undefined : { id: runningTask.id, revision: runningTask.revision };
        if (running !== undefined && !sameTaskIdentity(endedTask, running)) {
          selected.task = running;
          selected.finishing = false;
          this.#terminalRetry = undefined;
        } else if (running !== undefined) {
          // The provider terminal can close before durable task finalization
          // publishes its terminal state. Keep the exact task selected across
          // that gap; exposing the idle composer here would allow a second
          // instruction while the first task is still running.
          selected.task = running;
          selected.finishing = true;
          this.#terminalRetry = { head: state.head, stale: false };
        } else {
          selected.task = undefined;
          selected.finishing = false;
          this.#terminalRetry = running === undefined ? undefined : { head: state.head, stale: false };
        }
        this.#dropPendingTerminalInput();
      } else {
        this.#selectedAgent = undefined;
        this.#dropPendingTerminalInput();
      }
      this.#error = snapshot.error?.code === "stale" || snapshot.error?.code === "internal" ? snapshot.error : undefined;
      this.#publish();
      this.#reconcileTerminal();
      return;
    }
    if (snapshot.phase === "closing") {
      if (snapshot.error !== undefined && !snapshot.reset) this.#disarmTerminal(snapshot.error);
      return;
    }
    if (snapshot.phase === "ready") {
      if (this.#selectedAgent !== undefined) this.#selectedAgent.finishing = false;
      this.#terminalResetBurst = 0;
      if (!snapshot.writable) this.#dropPendingTerminalInput();
    }
    this.#publish();
    if (snapshot.writable) {
      this.#flushTerminalInput();
      this.#flushTerminalResize();
    }
  }

  /**
   * A server replay reset ends the protocol handle by design. Recover in
   * place: keep the terminal selection, remount the display (a fresh empty
   * surface — the retained output the old scrollback showed is gone), and
   * reconcile a new controller against current state. Bounded so a reset
   * storm cannot loop; past the bound the ordinary stale teardown stands.
   */
  #recoverFromTerminalReset(reset: TerminalReset): boolean {
    const selected = this.#selectedAgent;
    if (selected === undefined || this.#status !== "ready" || this.#terminalResetBurst >= 3) return false;
    const current = this.#state?.agents.get(selected.agent.id);
    if (current === undefined) return false;
    this.#terminalResetBurst += 1;
    selected.resets += 1;
    selected.resume = { sessionId: reset.sessionId, head: reset.head };
    selected.agent = { ...current };
    if (this.#state !== undefined) this.#refreshTerminalTask(selected, this.#state);
    selected.head = this.#state?.head ?? selected.head;
    this.#terminal = undefined;
    this.#dropPendingTerminalInput();
    this.#terminalRetry = undefined;
    this.#terminalSurface = undefined;
    this.#terminalSurfaceToken = undefined;
    this.#terminalDisplayError = undefined;
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

  #queueTerminalInput(bytes: Uint8Array): void {
    if (bytes.length === 0) return;
    const terminal = this.#terminal;
    const phase = terminal?.snapshot.phase;
    if (this.#selectedAgent?.task === undefined || (this.#status !== "ready" && this.#status !== "syncing") || phase === "closing" || phase === "closed" || (phase === "ready" && !terminal?.snapshot.writable)) return;
    if (bytes.length > MAX_TERMINAL_PAYLOAD || this.#pendingTerminalInput.length + bytes.length > MAX_PENDING_INPUT_BYTES) {
      this.#disarmTerminal(new SessionError("too_large"));
      return;
    }
    const next = new Uint8Array(this.#pendingTerminalInput.length + bytes.length);
    next.set(this.#pendingTerminalInput);
    next.set(bytes, this.#pendingTerminalInput.length);
    this.#pendingTerminalInput = next;
    this.#flushTerminalInput();
  }

  #flushTerminalInput(): void {
    const terminal = this.#terminal;
    if (terminal === undefined || !terminal.snapshot.writable) return;
    while (this.#pendingTerminalInput.length > 0) {
      const payload = this.#pendingTerminalInput.length <= MAX_TERMINAL_PAYLOAD
        ? this.#pendingTerminalInput
        : this.#pendingTerminalInput.slice(0, MAX_TERMINAL_PAYLOAD);
      if (!terminal.sendInput(payload)) return;
      this.#pendingTerminalInput = this.#pendingTerminalInput.slice(payload.length);
    }
  }

  #dropPendingTerminalInput(): void {
    this.#pendingTerminalInput = new Uint8Array(0);
  }

  #refreshTerminalTask(selected: AgentTerminalSelection, state: StateView): void {
    const task = agentCurrentTask(selected.agent, state);
    this.#refreshQueuedTask(selected, state);
    const current = task === undefined ? undefined : { id: task.id, revision: task.revision };
    if (sameTaskIdentity(selected.task, current) && selected.task?.revision === current?.revision) return;
    const previous = selected.task;
    const changedTask = selected.task?.id !== current?.id;
    selected.task = current;
    selected.finishing = false;
    if (changedTask) this.#dropPendingTerminalInput();
  }

  #refreshQueuedTask(selected: AgentTerminalSelection, state: StateView): void {
    if (selected.queuedTaskID !== undefined) {
      const queued = state.tasks.get(selected.queuedTaskID);
      if (
        queued !== undefined &&
        (queued.assigned_agent_id !== selected.agent.id || (queued.status !== "queued" && queued.status !== "running"))
      ) selected.queuedTaskID = undefined;
    }
    if (selected.queuedTaskID === undefined) selected.queuedTaskID = agentQueuedTask(selected.agent, state)?.id;
  }

  #replaceTerminal(replacement: TerminalReplacement): void {
    const terminal = this.#terminal;
    if (terminal === undefined) {
      this.#terminalReplacement = replacement;
      this.#finishTerminalReplacement();
      return;
    }
    this.#terminalReplacement = replacement;
    this.#error = undefined;
    // Release the display token before detaching: the view may unmount the
    // surface during teardown, and that is this teardown, not a fault.
    this.#terminalSurfaceToken = undefined;
    void terminal.detach().catch(() => undefined);
    this.#publish();
  }

  #finishTerminalReplacement(): boolean {
    const replacement = this.#terminalReplacement;
    if (replacement === undefined) return false;
    this.#terminalReplacement = undefined;
    this.#dropPendingTerminalInput();
    this.#retireTerminal();
    const prior = this.#selectedAgent;
    const candidate = replacement.agentId === undefined ? undefined : this.#state?.agents.get(replacement.agentId);
    const agent = candidate?.revision === replacement.agentRevision ? candidate : undefined;
    const task = agent === undefined || this.#state === undefined ? undefined : agentCurrentTask(agent, this.#state);
    const queuedTask = agent === undefined || this.#state === undefined ? undefined : agentQueuedTask(agent, this.#state);
    const sameAgent = agent !== undefined && prior?.agent.id === agent.id;
    // A new selection must not inherit an error. Keep the one serialized
    // pending edit while its selected agent is rebound to a newer revision.
    if (!sameAgent || this.#edit?.pending !== true) {
      this.#discardTaskEditConfirmation();
      this.#edit = undefined;
    }
    this.#selectedAgent = agent === undefined ? undefined : {
      agent: { ...agent },
      head: this.#state?.head ?? 0n,
      task: task === undefined ? undefined : { id: task.id, revision: task.revision },
      finishing: false,
      resets: 0,
      queuedTaskID: queuedTask?.id,
    };
    this.#error = replacement.error ?? (replacement.agentId !== undefined && agent === undefined ? new SessionError("stale") : undefined);
    this.#publish();
    return true;
  }

  #retireTerminal(keepSurface = false): TerminalController | undefined {
    const terminal = this.#terminal;
    this.#terminal = undefined;
    this.#terminalResetBurst = 0;
    this.#terminalRetry = undefined;
    if (!keepSurface) {
      this.#terminalSurface = undefined;
      this.#terminalSurfaceToken = undefined;
    }
    this.#terminalDisplayError = undefined;
    this.#pendingTerminalResize = undefined;
    if (!keepSurface) ++this.#terminalSurfaceVersion;
    return terminal;
  }

  #closeTerminal(): void {
    const terminal = this.#retireTerminal();
    if (terminal !== undefined) void terminal.close();
  }

  #disarmTerminal(error: SessionError | ProtocolError): void {
    this.#selectedAgent = undefined;
    this.#terminalReplacement = undefined;
    this.#dropPendingTerminalInput();
    this.#closeTerminal();
    this.#error = error;
    this.#publish();
  }

  #snapshot(): FactoryAppSnapshot {
    const selection = this.#human.selection;
    return {
      status: this.#status,
      state: this.#state,
      error: this.#error,
      graphs: this.#graphs,
      graphErrors: this.#graphErrors,
      runPaths: this.#runPaths,
      lastRunPaths: this.#lastRunPaths,
      edit: this.#edit,
      selectedHumanRequest: selection === undefined || selection.phase === "ended" ? undefined : {
        request: selection.request,
        phase: selection.phase,
        question: selection.detail?.question,
        options: selection.detail?.options ?? [],
        canReply: selection.detail?.canReply ?? false,
        canCancel: selection.detail?.cancelRun !== null && selection.detail?.cancelRun !== undefined,
        replyMaxBytes: selection.detail?.replyMaxBytes ?? 0,
        reply: selection.reply,
      },
      dispatchAllowed: this.#status === "ready" && ((this.#client?.session?.capabilities ?? 0) & CAPABILITIES.administration) !== 0,
      repositories: this.#repositories,
      intake: this.#intake,
      intakePending: this.#intakePending,
      intakeErrors: this.#intakeErrors,
      selectedAgent: this.#selectedAgent === undefined ? undefined : {
        id: this.#selectedAgent.agent.id,
        name: this.#selectedAgent.agent.name,
        revision: this.#selectedAgent.agent.revision,
      },
      terminal: this.#selectedAgent === undefined ? undefined : {
        agentId: this.#selectedAgent.agent.id,
        agentName: this.#selectedAgent.agent.name,
        agentRevision: this.#selectedAgent.agent.revision,
        taskTitle: this.#selectedAgent.task === undefined || this.#state === undefined
          ? undefined
          : this.#state.tasks.get(this.#selectedAgent.task.id)?.title,
        finishing: this.#selectedAgent.finishing,
        phase: this.#terminal?.snapshot.phase ?? "idle",
        writable: this.#terminal?.snapshot.writable ?? false,
        error: this.#terminal?.snapshot.error ?? this.#terminalDisplayError,
        errorSource: this.#terminal?.snapshot.errorSource ?? (this.#terminalDisplayError === undefined ? undefined : "display"),
        paused: this.#selectedAgent.agent.paused,
        controlPending: this.#selectedAgent.controlPending,
        controlError: this.#selectedAgent.controlError,
        controlStatus: this.#selectedAgent.controlStatus,
        controlReady: this.#selectedAgent.task !== undefined && !this.#selectedAgent.finishing && this.#terminal?.snapshot.phase === "ready",
        queued: this.#selectedAgent.queuedTaskID !== undefined && this.#selectedAgent.task === undefined,
        hasOutputSurface: this.#terminalSurface !== undefined,
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

function sampleFor(taskId: string, taskRevision: bigint, projectId: string, answer: RunPathsView): RunPathSample | undefined {
  if (typeof answer.runId !== "string" || answer.runId === "") return undefined;
  return Object.freeze({ taskId, taskRevision, projectId, runId: answer.runId, paths: Object.freeze([...answer.paths]), ...(answer.telemetry === undefined ? {} : { telemetry: answer.telemetry }) });
}

function sameSamples(left: ReadonlyMap<string, RunPathSample>, right: ReadonlyMap<string, RunPathSample>): boolean {
  return left.size === right.size && [...left].every(([agentId, sample]) => {
    const candidate = right.get(agentId);
    return candidate !== undefined && candidate.taskId === sample.taskId && candidate.taskRevision === sample.taskRevision && candidate.projectId === sample.projectId && candidate.runId === sample.runId
      && candidate.paths.length === sample.paths.length && candidate.paths.every((path, index) => path === sample.paths[index])
      && JSON.stringify(candidate.telemetry) === JSON.stringify(sample.telemetry);
  });
}

function sameStatus(left: FactoryAppStatus | undefined, right: FactoryAppStatus): boolean {
  if (left === undefined || left.status !== right.status) return false;
  return left.status !== "closed" || (right.status === "closed" && left.reason === right.reason);
}

function sameTaskIdentity(
  left: Pick<TaskItem, "id" | "revision"> | undefined,
  right: Pick<TaskItem, "id" | "revision"> | undefined,
): boolean {
  return left === undefined ? right === undefined : right !== undefined && left.id === right.id && left.revision === right.revision;
}
