import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { AgentItem, OperationalNodeView, TaskItem } from "@dark-factory/client";
import { type FactoryAgentSelection, type FactoryAppSnapshot, type FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentList, FactoryFloor } from "./console-screens.js";
import { AgentPanel, ConsoleDialog, HumanRequestPanel, WorkPanel, TaskDetail, editErrorCopy, type TaskEdit, type TaskBrief, type TaskScope, type AddTask, type WorkFilter } from "./console-sidebar.js";
import type { ProjectContentCall } from "./factory-app-controller.js";
import { useProduction } from "./production-data.js";
import { deriveProductionView, inProgressProduction } from "./production-view.js";
import { MissionsPanel } from "./missions-panel.js";
import { agentTelemetry, needsYou, workRows } from "./console-view.js";
import { IconButton } from "./icons.js";
import { Badge } from "./console-kit.js";

export type ConsoleDetail = "work" | "agent" | "floor";

export type FactoryConsoleProps = FactoryAppSnapshot & {
  selectedTaskId?: string;
  onSelectTask?: (taskId: string | undefined) => void;
  detail?: ConsoleDetail;
  onDetail?: (detail: ConsoleDetail) => void;
  selectedAgent?: FactoryAgentSelection;
  onSelectAgent?: (agent: AgentItem) => void;
  onSetDispatch?: (expectedRevision: bigint, enabled: boolean) => Promise<{ revision: bigint; enabled: boolean }>;
  onProjectContent?: ProjectContentCall;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onAddTask?: AddTask;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadNode?: (projectId: string, nodeId: string) => Promise<OperationalNodeView>;
  onOpenTerminalForHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onSelectHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onHumanReplyChange?: (reply: string) => void;
  onReplyHumanRequest?: () => void;
  onCancelHumanRequest?: () => void;
  onCloseHumanRequest?: () => void;
  intake?: ReadonlyMap<string, import("@dark-factory/client").IntakeView>;
  intakePending?: ReadonlySet<string>;
  intakeErrors?: ReadonlyMap<string, string>;
  onLoadIntake?: (projectId: string) => void;
  onIntakeAction?: (projectId: string, request: import("@dark-factory/client").IntakeBody) => void;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
};

const STATUS_LABELS: Record<FactoryAppSnapshot["status"], string> = {
  idle: "Idle",
  connecting: "Connecting",
  authenticating: "Authenticating",
  syncing: "Syncing",
  ready: "Ready",
  closed: "Closed",
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

const text = (value: unknown) => typeof value === "string" ? value : "";
const asTask = (v: Record<string, unknown>, project: string): TaskItem => ({ id: text(v.task_id) || text(v.id), project_id: text(v.project_id) || project, assigned_agent_id: text(v.assigned_agent_id), title: text(v.title) || "Untitled task", status: text(v.status) as TaskItem["status"] || "blocked", blocked_reason: text(v.blocked_reason) || undefined, priority: Number(v.priority) || 0, revision: BigInt(text(v.revision) || "0"), updated_at_ms: text(v.updated_at_ms) ? BigInt(text(v.updated_at_ms)) : undefined });
const pullRequestHref = (item: { repository: string; pullRequest?: { number: number; url?: string } }) => item.pullRequest?.url || (item.pullRequest ? `https://github.com/${item.repository}/pull/${item.pullRequest.number}` : undefined);

/** One screen: a factory or agent list beside one operator detail panel. */
export function FactoryConsole({
  status,
  state,
  error,
  graphs,
  graphErrors,
  onLoadNode,
  runPaths,
  lastRunPaths,
  edit,
  detail,
  onDetail,
  selectedTaskId,
  onSelectTask,
  selectedHumanRequest,
  selectedAgent,
  onSelectAgent,
  onSetDispatch,
  onProjectContent,
  onEditTask,
  onAddTask,
  onLoadTaskDetail,
  onOpenTerminalForHumanRequest,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
  intake,
  intakePending,
  intakeErrors,
  onLoadIntake,
  onIntakeAction,
  terminalContent,
}: FactoryConsoleProps) {
  // This is browser presentation only: it deliberately shares neither the
  // controller nor its durable/runtime settings path.
  const [browsingAgents, setBrowsingAgents] = useState(false);
  useEffect(() => { setBrowsingAgents(false); }, [selectedAgent?.id]);
  const chooseAgent = onSelectAgent === undefined ? undefined : (agent: AgentItem) => {
    setBrowsingAgents(false);
    onSelectAgent(agent);
  };
  const showAgents = () => { setBrowsingAgents(true); onDetail?.("agent"); };
  const [dispatchPending, setDispatchPending] = useState(false);
  const [dispatchNotice, setDispatchNotice] = useState("");
  const [dispatchError, setDispatchError] = useState("");
  const [chosenProjectId, setProjectId] = useState<string>();
  const projectId = chosenProjectId !== undefined && state?.projects.has(chosenProjectId) ? chosenProjectId : state?.projects.size === 1 ? [...state.projects.keys()][0] : undefined;
  const selectProject = (next: string | undefined) => {
    setProjectId(next);
    if (next !== projectId && selectedTaskId !== undefined) onSelectTask?.(undefined);
  };
  const pickRequest = onSelectHumanRequest === undefined ? undefined : (request: Parameters<NonNullable<typeof onSelectHumanRequest>>[0]) => { selectProject(request.project_id); onSelectHumanRequest(request); };
  const scopedState = useMemo(() => state === undefined || projectId === undefined ? state : {
    ...state,
    projects: new Map([...state.projects].filter(([id]) => id === projectId)),
    agents: new Map([...state.agents].filter(([, agent]) => agent.project_id === projectId)),
    tasks: new Map([...state.tasks].filter(([, task]) => task.project_id === projectId)),
    humanRequests: new Map([...state.humanRequests].filter(([, request]) => request.project_id === projectId)),
  }, [state, projectId]);
  const selectedTask = selectedTaskId === undefined ? undefined : scopedState?.tasks.get(selectedTaskId);
  const selectTask = onSelectTask === undefined ? undefined : (id: string) => { onSelectTask(id); onDetail?.("work"); };
  const ready = status === "ready";
  const productionData = useProduction([...(scopedState?.projects.keys() ?? [])].sort(), ready ? onProjectContent : undefined);
  const productionView = useMemo(() => deriveProductionView(productionData.records, Date.now()), [productionData.records]);
  const productionItems = useMemo(() => Object.values(productionView.contraptions).sort((a, b) => Number(a.completed) - Number(b.completed) || (a.completed ? a.completedAt - b.completedAt : 0) || a.visualId.localeCompare(b.visualId)), [productionView]);
  const inProgressItems = useMemo(() => productionItems.filter(inProgressProduction), [productionItems]);
  const [requestedMission, setRequestedMission] = useState<{ projectId: string; id: string }>();
  const [byMission, setByMission] = useState(false);
  const [workFilter, setWorkFilter] = useState<WorkFilter>("all");
  const showWork = () => { onDetail?.("work"); };
  const openWork = (id: string | undefined, missions: boolean) => { selectProject(id); setByMission(missions); if (!missions) setWorkFilter("all"); showWork(); };
  const openMission = (projectId: string, id: string) => { setRequestedMission({ projectId, id }); openWork(projectId, true); };
  // Routes into Work from outside it reset the view first so the answer form or task is never hidden.
  const selectRequest = pickRequest === undefined ? undefined : (request: Parameters<typeof pickRequest>[0]) => { openWork(request.project_id, false); pickRequest(request); };
  const goTask = selectTask === undefined ? undefined : (id: string) => { openWork(projectId, false); selectTask(id); };
  const sources = projectId === undefined ? undefined : intake?.get(projectId)?.sources;
  const rows = useMemo(() => workRows(scopedState, inProgressItems, sources), [scopedState, inProgressItems, sources]);
  const waiting = rows.filter(needsYou).length;
  const agent = selectedAgent === undefined ? undefined : scopedState?.agents.get(selectedAgent.id);
  const selectedDetail = (detail === "floor" ? "work" : detail) ?? (selectedAgent === undefined ? "work" : "agent");
  // Work's inbox is the daemon's last intake poll: re-read that local list (never the backlog) while Work is shown.
  useEffect(() => {
    if (!ready || projectId === undefined || onLoadIntake === undefined) return;
    onLoadIntake(projectId);
    if (selectedDetail !== "work") return;
    const timer = setInterval(() => onLoadIntake(projectId), 30_000);
    return () => clearInterval(timer);
  }, [ready, projectId, onLoadIntake !== undefined, selectedDetail === "work"]);
  const [relatedTask, setRelatedTask] = useState<TaskItem>();
  useEffect(() => { setRelatedTask(undefined); }, [selectedDetail, projectId]);
  const inspectedTask = relatedTask ? state?.tasks.get(relatedTask.id) ?? relatedTask : (onDetail === undefined || selectedDetail === "work") ? selectedTask : undefined;
  const editError = edit !== undefined && state?.projects.has(edit.target) ? undefined : editErrorCopy(edit);

  return (
    <div className="dfConsoleShell">
      <main className="dfFactoryConsole" aria-label="Factory operator console" data-mobile-view={onDetail === undefined ? undefined : detail === "floor" ? "floor" : "detail"}>
        <header className="dfFactoryConsole__header">
          <div>
            <h1>Dark Factory</h1>
          </div>
          {state?.projects.size === 1 ? null : <label>Project <select aria-label="Project" value={projectId ?? ""} onChange={(event) => selectProject(event.currentTarget.value || undefined)}>
            <option value="">All projects</option>
            {[...state?.projects.values() ?? []].sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id)).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
          </select></label>}
          <div className="dfConsoleBar__actions">
            <IconButton icon={state?.factory.dispatch_enabled ? "pause" : "play"} disabled={!ready || state === undefined || onSetDispatch === undefined || dispatchPending} title={onSetDispatch === undefined ? "Administrator access is required to change new-work admission." : "Controls admission of new work. Active processes continue."} onClick={() => {
              if (state === undefined || onSetDispatch === undefined || dispatchPending) return;
              setDispatchPending(true); setDispatchError(""); setDispatchNotice("");
              void onSetDispatch(state.factory.revision, !state.factory.dispatch_enabled).then((result) => setDispatchNotice(result.enabled ? "New work resumed." : "New work paused. Active processes continue."), () => setDispatchError("The admission change could not be confirmed. Check connection and administrator access, then refresh before retrying.")).finally(() => setDispatchPending(false));
            }}>{dispatchPending ? "Waiting…" : state?.factory.dispatch_enabled ? "Pause new work" : state ? "Resume new work" : "New work"}</IconButton>
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

        {dispatchNotice === "" ? null : <p className="dfConsoleSidebar__status" role="status">{dispatchNotice}</p>}
        {dispatchError === "" ? null : <p role="alert">{dispatchError}</p>}
        {error === undefined ? null : (
          <p className="dfFactoryConsole__error" role="alert">
            {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
          </p>
        )}

        <div className="dfConsoleLayout">
          <nav className="dfConsoleViewToggle" aria-label="Console views">
            <button type="button" aria-pressed={detail === "floor"} disabled={!ready || onDetail === undefined} onClick={() => { onDetail?.("floor"); }}>Floor</button>
            <button type="button" aria-pressed={detail !== "floor" && selectedDetail === "agent"} disabled={!ready || onDetail === undefined} onClick={showAgents}>Agents</button>
            <IconButton icon="list" aria-pressed={detail !== "floor" && selectedDetail === "work"} disabled={!ready || onDetail === undefined} onClick={showWork}>Work <Badge n={waiting} /></IconButton>
          </nav>
          <section className="dfConsoleLayout__left dfFactoryConsole__section" aria-label="Factory floor">
            <FactoryFloor changes={productionItems} changesRead={productionData.read} onProjectContent={onProjectContent} onOpenTasks={ready ? (id) => openWork(id, false) : undefined} onOpenMissions={ready ? (id) => openWork(id, true) : undefined} projectId={projectId} selectedTaskId={selectedTask?.id} onSelectTask={ready ? goTask : undefined} selectedAgentId={selectedDetail === "agent" ? selectedAgent?.id : undefined} state={scopedState} graphs={graphs} graphErrors={graphErrors} onLoadNode={onLoadNode} onAddTask={ready ? onAddTask : undefined} runPaths={runPaths} lastRunPaths={lastRunPaths} onSelectAgent={ready ? chooseAgent : undefined} onSelectHumanRequest={ready ? selectRequest : undefined} connected={ready} />
          </section>

          <aside className="dfConsoleSidebar" aria-label="Selected detail">
            {editError === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">{editError}</p>}
            <div hidden={selectedDetail !== "work"}>
              <WorkPanel
                state={scopedState}
                rows={rows}
                projectId={projectId}
                filter={workFilter}
                onFilter={(value) => { setWorkFilter(value); setByMission(false); }}
                byMission={byMission}
                onByMission={() => setByMission(!byMission)}
                missions={<MissionsPanel production={productionItems} requestedMission={requestedMission} state={scopedState} projectId={projectId} active={selectedDetail === "work" && byMission} call={ready ? onProjectContent : undefined} onSelectAgent={ready ? chooseAgent : undefined} onOpenTask={ready ? setRelatedTask : undefined} />}
                edit={edit}
                ready={ready}
                onEditTask={onEditTask}
                onAddTask={onAddTask}
                onLoadTaskDetail={onLoadTaskDetail}
                selectedTaskId={selectedTask?.id}
                onSelectTask={ready ? selectTask : undefined}
                selectedHumanRequest={selectedHumanRequest}
                onSelectHumanRequest={pickRequest}
                onCloseHumanRequest={onCloseHumanRequest}
                requestContent={selectedHumanRequest === undefined ? null : <HumanRequestPanel
                  selected={selectedHumanRequest}
                  onReplyChange={onHumanReplyChange}
                  onReply={onReplyHumanRequest}
                  onCancel={onCancelHumanRequest}
                  onOpenTerminal={onOpenTerminalForHumanRequest === undefined ? undefined : (request) => { selectProject(request.project_id); onOpenTerminalForHumanRequest(request); }}
                  terminalReady={ready}
                />}
                onMission={(task) => openMission(task.project_id, task.mission_id!)}
                onIntakeAction={ready ? onIntakeAction : undefined}
                intake={projectId === undefined ? undefined : { state: intake?.get(projectId)?.state, failed: intakeErrors?.has(projectId) === true, busy: intakePending?.has(projectId) === true }}
              />
            </div>
            <div hidden={selectedDetail !== "agent"}>
              <div hidden={agent !== undefined && !browsingAgents} aria-label="Agents">
                {selectedDetail !== "agent" || agent !== undefined && !browsingAgents ? null : <AgentList state={scopedState} selectedAgentId={selectedAgent?.id} ready={ready} onSelectAgent={ready ? chooseAgent : undefined} />}
              </div>
              <div hidden={agent === undefined || browsingAgents}>
              <button type="button" disabled={!ready} onClick={showAgents}>All agents</button>
              {agent === undefined ? null : <AgentPanel
                key={agent.id}
                agent={agent}
                state={scopedState}
                terminalContent={terminalContent}
                telemetry={agentTelemetry(agent, scopedState, runPaths)}
              />}
              </div>
            </div>
            {inspectedTask === undefined ? null : <ConsoleDialog key={inspectedTask.id} label="Task details" title="Task" onClose={() => { if (relatedTask) setRelatedTask(undefined); else onSelectTask?.(undefined); }}>
              <TaskDetail key={inspectedTask.id} task={inspectedTask} onLoadTaskDetail={onLoadTaskDetail} />
              {relatedTask?.status === "queued" && state?.tasks.has(relatedTask.id) && selectTask ? <button type="button" disabled={!ready} onClick={() => { setRelatedTask(undefined); goTask?.(relatedTask.id); }}>Open in Work</button> : null}
            </ConsoleDialog>}
          </aside>
        </div>
      </main>
    </div>
  );
}
