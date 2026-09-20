import { useEffect, useRef, useState, type ReactNode } from "react";
import type { DiscoveredAccount, AccountItem, AgentItem, GitHubConnectionBody, ProjectItem, RepositoryMutation, RepositoryView, SpriteAppearance, TaskHistoryView, TaskItem, TaskListView } from "@dark-factory/client";
import { BROWSER_HOST, type FactoryAgentSelection, type FactoryAppSnapshot, type FactoryHumanRequestView } from "./factory-app-controller.js";
import type { FactoryGitHubView } from "./factory-settings-coordinator.js";
import { AgentList, FactoryFloor } from "./console-screens.js";
import { AgentPanel, ConsoleDialog, HumanRequestPanel, QueuePanel, TaskDetail, SettingsDialog, editErrorCopy, type AgentConfigEdit, type AgentPanelView, type TaskEdit, type TaskBrief } from "./console-sidebar.js";
import { ProjectLibrary, type ProjectContentCall } from "./project-library.js";
import { useProduction } from "./production-data.js";
import { deriveProductionView } from "./production-view.js";
import { ProductionPanel } from "./production-panel.js";
import { MissionsPanel } from "./missions-panel.js";
import { RemoteInvitePanel } from "./remote-invite.js";
import { factoryCounters } from "./console-view.js";
import { SpriteEditor } from "./factory-scene/sprite-editor.js";
import { DEFAULT_FLOOR_APPEARANCE, loadFloorAppearance, resetFloorAppearance, saveFloorAppearance, type FloorAppearance } from "./floor-appearance.js";

export type ConsoleView = "floor" | "agents";
export type ConsoleDetail = "needs-you" | "queue" | "missions" | "production" | "agent" | "floor";

export type FactoryConsoleProps = FactoryAppSnapshot & {
  view?: ConsoleView;
  onView?: (view: ConsoleView) => void;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string | undefined) => void;
  detail?: ConsoleDetail;
  onDetail?: (detail: ConsoleDetail) => void;
  agentPanel?: AgentPanelView;
  onAgentPanel?: (panel: AgentPanelView) => void;
  settingsOpen?: boolean;
  onToggleSettings?: () => void;
  selectedAgent?: FactoryAgentSelection;
  onSelectAgent?: (agent: AgentItem) => void;
  onSetDispatch?: (expectedRevision: bigint, enabled: boolean) => Promise<{ revision: bigint; enabled: boolean }>;
  onAttachmentRetention?: (enabled?: boolean) => Promise<boolean>;
  onProjectContent?: ProjectContentCall;
  onDraftLibraryTask?: (agent: AgentItem, instruction: string) => void;
  onSaveAgentConfig?: (config: AgentConfigEdit) => void;
  onSaveAgentAppearance?: (agentId: string, appearance: SpriteAppearance) => Promise<boolean>;
  appearanceAgentId?: string;
  onEditAppearance?: (agent: AgentItem) => void;
  onCloseAppearance?: () => void;
  onSaveProjectLimits?: (project: Pick<ProjectItem, "id" | "revision">, limits: { runBudget: bigint; maxRunSeconds: number }) => void;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onAddTask?: (agent: AgentItem, instruction: string, mode: "queue" | "any", files?: readonly File[]) => Promise<boolean>;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
  onLoadTaskList?: (agentId: string, cursor?: { beforeUpdatedAtMs?: bigint; beforeTaskId?: string }) => Promise<TaskListView>;
  onOpenTerminalForHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onSelectHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onHumanReplyChange?: (reply: string) => void;
  onReplyHumanRequest?: () => void;
  onCancelHumanRequest?: () => void;
  onCloseHumanRequest?: () => void;
  onInviteRemote?: () => void;
  onDismissRemoteInvite?: () => void;
  onLoadDevices?: () => void;
  onRevokeDevice?: (device: { clientId: string; expectedRevision: bigint }) => void;
  onLoadAccounts?: () => void;
  onLinkAccount?: (login: DiscoveredAccount, label: string) => void;
  onUpdateAccount?: (account: AccountItem, change: { label?: string; remove?: boolean }) => void;
  onLoadRepositories?: (projectId: string) => void;
  onMutateRepository?: (request: RepositoryMutation) => void;
  onCreateProject?: (request: { name: string; root: string }) => void;
  onLoadIntake?: (projectId: string) => void; onIntakeAction?: (projectId: string, request: import("@dark-factory/client").IntakeBody) => void;
  onGitHub?: (request: GitHubConnectionBody) => void;
  /** The loopback address this console is served from. */
  address?: string;
  /** Overrides the pairing surface the settings modal mounts by default. */
  pairing?: ReactNode;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
};

const STATUS_LABELS: Record<FactoryAppSnapshot["status"], string> = {
  idle: "IDLE",
  connecting: "CONNECTING",
  authenticating: "AUTHENTICATING",
  syncing: "SYNCING",
  ready: "READY",
  closed: "CLOSED",
};

const ERROR_LABELS = new Map<string, string>([
  ["connection", "Connection unavailable."],
  ["closed", "Connection closed."],
  ["pairing_required", "Pair this browser client before connecting."],
  ["pairing_uncertain", "Pairing result is uncertain. Pair this browser client again."],
  ["storage_unavailable", "Browser key storage is unavailable."],
  ["crypto_unavailable", "Browser cryptography is unavailable."],
  ["malformed", "The server sent an invalid frame."],
  ["oversized", "The server frame exceeded the protocol limit."],
  ["wrong_direction", "The server sent an invalid frame direction."],
  ["unauthorized", "This browser client is not authorized."],
  ["invalid_request", "The request was rejected."],
  ["rate_limited", "The request was rate limited."],
  ["not_found", "The requested item was not found."],
  ["stale", "The requested state is stale."],
  ["too_large", "The request was too large."],
  ["internal", "The server could not complete the request."],
  ["unsupported", "The factory does not support this request yet."],
]);

/** One screen: a factory or agent list beside one operator detail panel. */
export function FactoryConsole({
  status,
  state,
  error,
  topologies,
  runPaths,
  lastRunPaths,
  edit,
  view = "floor",
  onView,
  detail,
  onDetail,
  selectedTaskId,
  onSelectTask,
  agentPanel,
  onAgentPanel,
  settingsOpen,
  onToggleSettings,
  selectedHumanRequest,
  selectedAgent,
  onSelectAgent,
  onSetDispatch,
  onAttachmentRetention,
  onProjectContent,
  onDraftLibraryTask,
  onSaveAgentConfig,
  onSaveAgentAppearance,
  appearanceAgentId,
  onEditAppearance,
  onCloseAppearance,
  onSaveProjectLimits,
  onEditTask,
  onAddTask,
  onLoadTaskDetail,
  onLoadTaskHistory,
  onLoadTaskList,
  onOpenTerminalForHumanRequest,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
  remoteInviteAllowed,
  remoteInvite,
  remoteInviteError,
  onInviteRemote,
  onDismissRemoteInvite,
  devices,
  devicesError,
  ownClientId,
  onLoadDevices,
  onRevokeDevice,
  accounts,
  accountsPending,
  accountsError,
  github,
  onLoadAccounts,
  onLinkAccount,
  onUpdateAccount,
  repositories,
  repositoryPending,
  repositoryErrors,
  onLoadRepositories,
  onMutateRepository,
  onCreateProject,
  intake,
  intakePending,
  intakeErrors,
  onLoadIntake,
  onIntakeAction,
  onGitHub,
  address = BROWSER_HOST,
  pairing,
  terminalContent,
}: FactoryConsoleProps) {
  // This is browser presentation only: it deliberately shares neither the
  // controller nor its durable/runtime settings path.
  const [floorAppearance, setFloorAppearance] = useState<FloorAppearance>(DEFAULT_FLOOR_APPEARANCE);
  const floorAppearanceLoaded = useRef(false);
  useEffect(() => {
    setFloorAppearance(loadFloorAppearance());
    floorAppearanceLoaded.current = true;
  }, []);
  const changeFloorAppearance = (appearance: FloorAppearance) => {
    setFloorAppearance(appearance);
    if (floorAppearanceLoaded.current) saveFloorAppearance(appearance);
  };
  const resetAppearance = () => {
    resetFloorAppearance();
    setFloorAppearance(DEFAULT_FLOOR_APPEARANCE);
  };
  const [dispatchPending, setDispatchPending] = useState(false);
  const [dispatchNotice, setDispatchNotice] = useState("");
  const [dispatchError, setDispatchError] = useState("");
  const [chosenProjectId, setProjectId] = useState<string>();
  const projectId = chosenProjectId !== undefined && state?.projects.has(chosenProjectId) ? chosenProjectId : undefined;
  const selectProject = (next: string | undefined) => {
    setProjectId(next);
    if (next !== projectId && selectedTaskId !== undefined) onSelectTask?.(undefined);
  };
  const scopedState = state === undefined || projectId === undefined ? state : {
    ...state,
    projects: new Map([...state.projects].filter(([id]) => id === projectId)),
    agents: new Map([...state.agents].filter(([, agent]) => agent.project_id === projectId)),
    tasks: new Map([...state.tasks].filter(([, task]) => task.project_id === projectId)),
  };
  const selectedTask = selectedTaskId === undefined ? undefined : scopedState?.tasks.get(selectedTaskId);
  const selectTask = onSelectTask === undefined ? undefined : (id: string) => { onSelectTask(id); onDetail?.("queue"); };
  const ready = status === "ready";
  const productionData = useProduction([...(scopedState?.projects.keys() ?? [])].sort(), ready ? onProjectContent : undefined);
  const productionView = deriveProductionView(productionData.records, Date.now());
  const productionItems = Object.values(productionView.contraptions).sort((a, b) => (a.completed && b.completed ? a.completedAt - b.completedAt : 0) || a.visualId.localeCompare(b.visualId));
  const productionFloor = [...productionItems.filter((item) => !item.completed), ...productionItems.filter((item) => item.completed).slice(-8)];
  const [selectedProduction, setSelectedProduction] = useState<string>();
  const [requestedMission, setRequestedMission] = useState<{ projectId: string; id: string }>();
  const selectProduction = (key: string) => { setSelectedProduction(key); onDetail?.("production"); };
  const counters = factoryCounters(state);
  const agent = selectedAgent === undefined ? undefined : scopedState?.agents.get(selectedAgent.id);
  const selectedDetail = (detail === "floor" ? "needs-you" : detail) ?? (selectedAgent === undefined ? "needs-you" : "agent");
  const appearanceAgent = appearanceAgentId === undefined ? undefined : state?.agents.get(appearanceAgentId);
  const editError = edit !== undefined && state?.projects.has(edit.target) ? undefined : editErrorCopy(edit);

  return (
    <div className="dfConsoleShell">
      <main className="dfFactoryConsole" aria-label="Factory operator console" data-mobile-view={onDetail === undefined ? undefined : detail === "floor" ? "floor" : "detail"}>
        <header className="dfFactoryConsole__header">
          <div>
            <h1>DARK FACTORY</h1>
          </div>
          <label>Project <select aria-label="Project" value={projectId ?? ""} onChange={(event) => selectProject(event.currentTarget.value || undefined)}>
            <option value="">All projects</option>
            {[...state?.projects.values() ?? []].sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id)).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
          </select></label>
          <div className="dfConsoleBar__actions">
            <button type="button" disabled={!ready || state === undefined || onSetDispatch === undefined || dispatchPending} title={onSetDispatch === undefined ? "Administrator access is required to change new-work admission." : "Controls admission of new work. Active processes continue."} onClick={() => {
              if (state === undefined || onSetDispatch === undefined || dispatchPending) return;
              setDispatchPending(true); setDispatchError(""); setDispatchNotice("");
              void onSetDispatch(state.factory.revision, !state.factory.dispatch_enabled).then((result) => setDispatchNotice(result.enabled ? "New work resumed." : "New work paused. Active processes continue."), () => setDispatchError("The admission change could not be confirmed. Check connection and administrator access, then refresh before retrying.")).finally(() => setDispatchPending(false));
            }}>{dispatchPending ? "Waiting…" : state?.factory.dispatch_enabled ? "Pause new work" : "Resume new work"}</button>
            <button type="button" aria-pressed={settingsOpen === true} disabled={onToggleSettings === undefined} onClick={onToggleSettings}>Settings</button>
          </div>
          <div
            className={ready || error !== undefined ? "dfFactoryConsole__visuallyHidden" : "dfFactoryConsole__connection"}
            aria-label={`Connection status: ${STATUS_LABELS[status]}`}
          >
            <p className="dfFactoryConsole__status" role="status" aria-live="polite" aria-atomic="true">
              <span className={`dfFactoryConsole__statusDot dfFactoryConsole__statusDot--${status}`} aria-hidden="true" />
              {STATUS_LABELS[status]}
            </p>
          </div>
        </header>

        {ready && onSetDispatch === undefined ? <p className="dfConsoleSidebar__inherit">Administrator access is required to pause or resume new work.</p> : null}
        {dispatchNotice === "" ? null : <p className="dfConsoleSidebar__status" role="status">{dispatchNotice}</p>}
        {dispatchError === "" ? null : <p role="alert">{dispatchError}</p>}
        {error === undefined ? null : (
          <p className="dfFactoryConsole__error" role="alert">
            {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
          </p>
        )}

        {onDetail === undefined ? null : <nav className="dfMobileNav dfConsoleViewToggle" aria-label="Console views">
          <button type="button" aria-pressed={detail === "floor" && view === "floor"} disabled={!ready} onClick={() => { onView?.("floor"); onDetail("floor"); }}>Floor</button>
          <button type="button" aria-pressed={detail === "floor" && view === "agents"} disabled={!ready || onView === undefined} onClick={() => { onView?.("agents"); onDetail("floor"); }}>Agents</button>
          <button type="button" aria-pressed={detail === "missions"} disabled={!ready} onClick={() => onDetail("missions")}>Missions</button>
          <button type="button" aria-pressed={detail === "production"} onClick={() => onDetail("production")}>Production</button>
          <button type="button" aria-pressed={detail === "queue"} disabled={!ready} onClick={() => onDetail("queue")}>Tasks</button>
          <button type="button" aria-pressed={detail !== "floor" && selectedDetail === "needs-you"} disabled={!ready} onClick={() => onDetail("needs-you")}>Needs you {counters.needsYou || ""}</button>
        </nav>}
        <div className="dfConsoleLayout">
          <section className="dfConsoleLayout__left dfFactoryConsole__section" aria-label={view === "floor" ? "Factory floor" : "Agents"}>
            <div className="dfFactoryConsole__sectionHeading">
              <h2 className="dfFactoryConsole__visuallyHidden">{view === "floor" ? "FACTORY FLOOR" : "AGENTS"}</h2>
              <div className="dfConsoleViewToggle" role="group" aria-label="Left view">
                {(["floor", "agents"] as const).map((option) => (
                  <button
                    key={option}
                    type="button"
                    aria-pressed={view === option}
                    disabled={!ready || onView === undefined}
                    onClick={() => onView?.(option)}
                  >
                    {option === "floor" ? "Floor" : "Agents"}
                  </button>
                ))}
              </div>
            </div>
            {view === "floor"
              ? <FactoryFloor production={{ view: productionView, items: productionFloor, selected: selectedProduction, onSelect: selectProduction }} onOpenTasks={ready ? (id) => { selectProject(id); onDetail?.("queue"); } : undefined} onOpenMissions={ready ? (id) => { selectProject(id); onDetail?.("missions"); } : undefined} projectId={projectId} onProject={selectProject} floorAppearance={floorAppearance} selectedTaskId={selectedTask?.id} onSelectTask={ready ? selectTask : undefined} selectedAgentId={selectedDetail === "agent" ? selectedAgent?.id : undefined} state={scopedState} topologies={topologies} runPaths={runPaths} lastRunPaths={lastRunPaths} onSelectAgent={ready ? onSelectAgent : undefined} onSelectHumanRequest={ready ? onSelectHumanRequest : undefined} connected={ready} />
              : <AgentList state={scopedState} selectedAgentId={selectedAgent?.id} ready={ready} onSelectAgent={ready ? onSelectAgent : undefined} />}
          </section>

          <aside className="dfConsoleSidebar" aria-label="Selected detail">
            <div className="dfConsoleViewToggle" role="group" aria-label="Right panel">
              <button type="button" aria-pressed={selectedDetail === "needs-you"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("needs-you")}>Needs you <span>{counters.needsYou ?? "—"}</span></button>
              <button type="button" aria-pressed={selectedDetail === "missions"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("missions")}>Missions</button>
              <button type="button" aria-pressed={selectedDetail === "queue"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("queue")}>Tasks</button>
              <button type="button" aria-pressed={selectedDetail === "production"} disabled={onDetail === undefined} onClick={() => onDetail?.("production")}>Production</button>
              <button type="button" aria-pressed={selectedDetail === "agent"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("agent")}>Agent</button>
            </div>
            {editError === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">{editError}</p>}
            <div hidden={selectedDetail !== "needs-you"}>
              <NeedsYouColumn
                state={state}
                status={status}
                selectedHumanRequest={selectedHumanRequest}
                onSelectHumanRequest={onSelectHumanRequest}
                onCloseHumanRequest={onCloseHumanRequest}
                selectedContent={selectedHumanRequest === undefined ? null : <HumanRequestPanel
                  selected={selectedHumanRequest}
                  onReplyChange={onHumanReplyChange}
                  onReply={onReplyHumanRequest}
                  onCancel={onCancelHumanRequest}
                  onOpenTerminal={onOpenTerminalForHumanRequest === undefined ? undefined : (request) => { selectProject(request.project_id); onOpenTerminalForHumanRequest(request); }}
                  terminalReady={ready}
                />}
              />
            </div>
            <div hidden={selectedDetail !== "production"}><ProductionPanel items={productionItems} selected={selectedProduction} onSelect={selectProduction} state={state} call={ready ? onProjectContent : undefined} connected={ready} error={[productionData.error, ...productionData.notices].filter(Boolean).join(" ")} overflow={productionData.overflow} loadMore={productionData.loadMore} onMission={(projectId, id) => { selectProject(projectId); setRequestedMission({ projectId, id }); onDetail?.("missions"); }} onAgent={ready ? onSelectAgent : undefined} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} /></div>
            <div hidden={selectedDetail !== "missions"}><MissionsPanel production={productionItems} onProduction={selectProduction} requestedMission={requestedMission} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} state={state} projectId={projectId} active={selectedDetail === "missions"} call={ready ? onProjectContent : undefined} onProject={selectProject} onSelectAgent={ready ? onSelectAgent : undefined} onSelectTask={ready ? selectTask : undefined} /></div>
            <div hidden={selectedDetail !== "queue"}>
              <QueuePanel
                state={scopedState}
                edit={edit}
                ready={ready}
                onEditTask={onEditTask}
                onAddTask={onAddTask}
                onLoadTaskDetail={onLoadTaskDetail}
                selectedTaskId={selectedTask?.id}
                onSelectTask={ready ? selectTask : undefined}
              />
            </div>
            <div hidden={selectedDetail !== "agent"}>
              {agent === undefined ? <p className="dfFactoryConsole__empty">SELECT AN AGENT TO OPEN CONTROLS</p> : <AgentPanel
                key={agent.id}
                agent={agent}
                state={state}
                edit={edit}
                ready={ready}
                onSaveConfig={onSaveAgentConfig}
                onEditAppearance={ready && !agent.archived ? onEditAppearance : undefined}
                onEditTask={onEditTask}
                onLoadTaskDetail={onLoadTaskDetail}
                onLoadTaskHistory={onLoadTaskHistory}
                onLoadTaskList={onLoadTaskList}
                terminalContent={terminalContent}
                panel={agentPanel}
                onPanel={onAgentPanel}
              />}
            </div>
            {(onDetail !== undefined && selectedDetail !== "queue") || selectedTask === undefined || (selectedTask.status === "queued" && onEditTask !== undefined) ? null : <ConsoleDialog key={selectedTask.id} label="Task details" title="TASK" onClose={() => onSelectTask?.(undefined)}>
              <TaskDetail key={selectedTask.id} task={selectedTask} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} />
            </ConsoleDialog>}
          </aside>
        </div>
      </main>
      {settingsOpen !== true ? null : (
        <SettingsDialog
          onAttachmentRetention={ready ? onAttachmentRetention : undefined}
          projectId={projectId}
          floorAppearance={floorAppearance}
          onFloorAppearanceChange={changeFloorAppearance}
          onResetFloorAppearance={resetAppearance}
          state={state}
          ready={ready}
          address={address}
          accounts={accounts}
          accountsPending={accountsPending}
          accountsError={accountsError}
          onLoadAccounts={onLoadAccounts}
          onLinkAccount={onLinkAccount}
          onUpdateAccount={onUpdateAccount}
          repositories={repositories}
          repositoryPending={repositoryPending}
          repositoryErrors={repositoryErrors}
          onLoadRepositories={onLoadRepositories}
          onMutateRepository={onMutateRepository}
          onCreateProject={onCreateProject}
          intake={intake}
          intakePending={intakePending}
          intakeErrors={intakeErrors}
          onLoadIntake={onLoadIntake}
          onIntakeAction={onIntakeAction}
          onSelectTask={selectTask === undefined ? undefined : (id) => { onToggleSettings?.(); selectTask(id); }}
          github={github}
          onGitHub={onGitHub}
          edit={edit}
          onSaveProjectLimits={onSaveProjectLimits}
          pairing={pairing ?? (!remoteInviteAllowed ? undefined : (
            <RemoteInvitePanel invite={remoteInvite} error={remoteInviteError} onInvite={onInviteRemote} onDismiss={onDismissRemoteInvite} devices={devices} devicesError={devicesError} ownClientId={ownClientId} onLoadDevices={onLoadDevices} onRevokeDevice={onRevokeDevice} />
          ))}
          library={ready && onProjectContent !== undefined ? <ProjectLibrary state={state} call={onProjectContent} draft={(agent, instruction) => { onToggleSettings?.(); onDraftLibraryTask?.(agent, instruction); }} /> : undefined}
          onClose={onToggleSettings}
        />
      )}
      {appearanceAgent === undefined || onSaveAgentAppearance === undefined || onCloseAppearance === undefined ? null : <SpriteEditor agent={appearanceAgent} pending={edit?.target === appearanceAgent.id && edit.pending} error={edit?.target === appearanceAgent.id ? editError : undefined} onSave={(appearance) => onSaveAgentAppearance(appearanceAgent.id, appearance)} onClose={onCloseAppearance} />}
    </div>
  );
}

function NeedsYouColumn({
  state,
  status,
  selectedHumanRequest,
  onSelectHumanRequest,
  onCloseHumanRequest,
  selectedContent,
}: Pick<FactoryConsoleProps, "state" | "status" | "selectedHumanRequest" | "onSelectHumanRequest" | "onCloseHumanRequest"> & { selectedContent?: ReactNode }) {
  const requests = state === undefined ? undefined : [...state.humanRequests.values()];
  const busy = selectedHumanRequest?.phase === "replying" || selectedHumanRequest?.phase === "cancelling";
  return (
    <section className="dfConsoleSidebar__panel" aria-label="Needs you">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>Needs you</h2>
        <span>{requests?.length ?? "—"} {requests?.length === 1 ? "item" : "items"}</span>
      </div>
      {requests === undefined ? <p className="dfFactoryConsole__empty">Waiting for the latest state…</p>
        : requests.length === 0 ? <p className="dfFactoryConsole__empty">Nothing needs your attention</p> : (
          <ul className="dfConsoleItems">
            {requests.map((request) => {
              const selected = selectedHumanRequest?.request.id === request.id;
              const label = request.status.replaceAll("_", " ").toUpperCase();
              const disabled = status !== "ready" || busy || (selected ? onCloseHumanRequest === undefined : onSelectHumanRequest === undefined);
              return (
                <li key={request.id}>
                  <details className="dfConsoleItem" open={selected}>
                    <summary className="dfConsoleItem__summary" aria-disabled={disabled} onClick={(event) => {
                      event.preventDefault();
                      if (disabled) return;
                      if (selected) onCloseHumanRequest?.();
                      else onSelectHumanRequest?.(request);
                    }}>
                      <strong>{entityLabel(state?.agents, request.agent_id, "AGENT")} asks</strong>
                      <span className="dfConsoleItem__meta">{label} · {projectLabel(state?.projects, request.project_id)} · {entityLabel(state?.tasks, request.task_id, "TASK")}</span>
                    </summary>
                    {selected ? <div className="dfConsoleItem__detail">{selectedContent}</div> : null}
                  </details>
                </li>
              );
            })}
          </ul>
        )}
    </section>
  );
}

function projectLabel(projects: ReadonlyMap<string, { name: string }> | undefined, projectID: string): string {
  return projects?.get(projectID)?.name ?? `PROJECT ${shortID(projectID)}`;
}

function entityLabel(entities: ReadonlyMap<string, { name?: string; title?: string }> | undefined, id: string, fallback: string): string {
  const entity = entities?.get(id);
  return entity?.name ?? entity?.title ?? `${fallback} ${shortID(id)}`;
}

function shortID(value: string): string {
  return value.slice(0, 8);
}
