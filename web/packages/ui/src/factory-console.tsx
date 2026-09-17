import { useEffect, useRef, useState, type ReactNode } from "react";
import type { DiscoveredAccount, AccountItem, AgentItem, ProjectItem, SpriteAppearance, TaskHistoryView, TaskItem, TaskListView } from "@dark-factory/client";
import { BROWSER_HOST, type FactoryAgentSelection, type FactoryAppSnapshot, type FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentList, FactoryFloor } from "./console-screens.js";
import { AgentPanel, HumanRequestPanel, QueuePanel, TaskDetail, SettingsDialog, editErrorCopy, type AgentConfigEdit, type AgentPanelView, type TaskEdit, type TaskBrief } from "./console-sidebar.js";
import { ProjectLibrary, type ProjectContentCall } from "./project-library.js";
import { RemoteInvitePanel } from "./remote-invite.js";
import { factoryCounters } from "./console-view.js";
import { SpriteEditor } from "./factory-scene/sprite-editor.js";
import { DEFAULT_FLOOR_APPEARANCE, loadFloorAppearance, resetFloorAppearance, saveFloorAppearance, type FloorAppearance } from "./floor-appearance.js";

export type ConsoleView = "floor" | "agents";
export type ConsoleDetail = "needs-you" | "queue" | "agent" | "floor";

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
  onProjectContent?: ProjectContentCall;
  onDraftLibraryTask?: (agent: AgentItem, instruction: string) => void;
  onSaveAgentConfig?: (config: AgentConfigEdit) => void;
  onSaveAgentAppearance?: (agentId: string, appearance: SpriteAppearance) => Promise<boolean>;
  appearanceAgentId?: string;
  onEditAppearance?: (agent: AgentItem) => void;
  onCloseAppearance?: () => void;
  onSaveProjectLimits?: (project: Pick<ProjectItem, "id" | "revision">, limits: { runBudget: bigint; maxRunSeconds: number }) => void;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
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
  onProjectContent,
  onDraftLibraryTask,
  onSaveAgentConfig,
  onSaveAgentAppearance,
  appearanceAgentId,
  onEditAppearance,
  onCloseAppearance,
  onSaveProjectLimits,
  onEditTask,
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
  onLoadAccounts,
  onLinkAccount,
  onUpdateAccount,
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
  const selectedTask = selectedTaskId === undefined ? undefined : state?.tasks.get(selectedTaskId);
  const selectTask = onSelectTask === undefined ? undefined : (id: string) => { onSelectTask(id); onDetail?.("queue"); };
  const ready = status === "ready";
  const counters = factoryCounters(state);
  const agent = selectedAgent === undefined ? undefined : state?.agents.get(selectedAgent.id);
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
          <div className="dfConsoleBar__actions">
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

        {error === undefined ? null : (
          <p className="dfFactoryConsole__error" role="alert">
            {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
          </p>
        )}

        {onDetail === undefined ? null : <nav className="dfMobileNav dfConsoleViewToggle" aria-label="Console views">
          <button type="button" aria-pressed={detail === "floor" && view === "floor"} disabled={!ready} onClick={() => { onView?.("floor"); onDetail("floor"); }}>Floor</button>
          <button type="button" aria-pressed={detail === "floor" && view === "agents"} disabled={!ready || onView === undefined} onClick={() => { onView?.("agents"); onDetail("floor"); }}>Agents</button>
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
              ? <FactoryFloor floorAppearance={floorAppearance} selectedTaskId={selectedTask?.id} onSelectTask={ready ? selectTask : undefined} selectedAgentId={selectedDetail === "agent" ? selectedAgent?.id : undefined} state={state} topologies={topologies} runPaths={runPaths} lastRunPaths={lastRunPaths} onSelectAgent={ready ? onSelectAgent : undefined} onSelectHumanRequest={ready ? onSelectHumanRequest : undefined} onOpenQueue={ready && onDetail !== undefined ? () => onDetail("queue") : undefined} connected={ready} />
              : <AgentList state={state} selectedAgentId={selectedAgent?.id} ready={ready} onSelectAgent={ready ? onSelectAgent : undefined} />}
          </section>

          <aside className="dfConsoleSidebar" aria-label="Selected detail">
            <div className="dfConsoleViewToggle" role="group" aria-label="Right panel">
              <button type="button" aria-pressed={selectedDetail === "needs-you"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("needs-you")}>Needs you <span>{counters.needsYou ?? "—"}</span></button>
              <button type="button" aria-pressed={selectedDetail === "queue"} disabled={!ready || onDetail === undefined} onClick={() => onDetail?.("queue")}>Tasks</button>
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
                  onOpenTerminal={onOpenTerminalForHumanRequest}
                  terminalReady={ready}
                />}
              />
            </div>
            <div hidden={selectedDetail !== "queue"}>
              {selectedTask === undefined ? null : <TaskBack key={selectedTask.id} onBack={() => onSelectTask?.(undefined)} />}
              <div hidden={selectedTask !== undefined && !(selectedTask.status === "queued" && onEditTask !== undefined)}><QueuePanel
                state={state}
                edit={edit}
                ready={ready}
                onEditTask={onEditTask}
                onLoadTaskDetail={onLoadTaskDetail}
                selectedTaskId={selectedTask?.id}
                onSelectTask={ready ? selectTask : undefined}
              /></div>
              {selectedTask === undefined || (selectedTask.status === "queued" && onEditTask !== undefined) ? null : <section className="dfConsoleSidebar__panel" aria-label="Task details">
                <TaskDetail key={`${selectedTask.id}:${selectedTask.revision}`} task={selectedTask} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} />
              </section>}
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
            {ready && onProjectContent !== undefined ? <ProjectLibrary state={state} call={onProjectContent} draft={onDraftLibraryTask} /> : null}
          </aside>
        </div>
      </main>
      {settingsOpen !== true ? null : (
        <SettingsDialog
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
          edit={edit}
          onSaveProjectLimits={onSaveProjectLimits}
          pairing={pairing ?? (!remoteInviteAllowed ? undefined : (
            <RemoteInvitePanel invite={remoteInvite} error={remoteInviteError} onInvite={onInviteRemote} onDismiss={onDismissRemoteInvite} devices={devices} devicesError={devicesError} ownClientId={ownClientId} onLoadDevices={onLoadDevices} onRevokeDevice={onRevokeDevice} />
          ))}
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
    <section className="dfConsoleSidebar__panel" aria-label="NEEDS YOU">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>NEEDS YOU</h2>
        <span>{requests?.length ?? "—"} {requests?.length === 1 ? "ITEM" : "ITEMS"}</span>
      </div>
      {requests === undefined ? <p className="dfFactoryConsole__empty">WAITING FOR SNAPSHOT</p>
        : requests.length === 0 ? <p className="dfFactoryConsole__empty">all quiet — nothing needs you</p> : (
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

function TaskBack({ onBack }: { onBack: () => void }) {
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => { button.current?.focus(); }, []);
  return <button ref={button} type="button" className="dfTaskBack" onClick={onBack}>Back to tasks</button>;
}
