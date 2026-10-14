import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { AgentItem, OperationalNodeView, TaskHistoryView, TaskItem } from "@dark-factory/client";
import { type FactoryAgentSelection, type FactoryAppSnapshot, type FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentList, FactoryFloor } from "./console-screens.js";
import { AgentPanel, ConsoleDialog, HumanRequestPanel, WorkPanel, TaskDetail, editErrorCopy, type AgentConfigEdit, type AgentPanelView, type TaskEdit, type TaskBrief, type TaskScope, type TaskContentChip, type AddTask, type WorkFilter } from "./console-sidebar.js";
import { ProjectLibrary, type ProjectContentCall } from "./project-library.js";
import { activityTarget, onBoard, useKnowledgeActivity, type KnowledgeActivity } from "./project-board.js";
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
  agentPanel?: AgentPanelView;
  onAgentPanel?: (panel: AgentPanelView) => void;
  selectedAgent?: FactoryAgentSelection;
  onSelectAgent?: (agent: AgentItem) => void;
  onSetDispatch?: (expectedRevision: bigint, enabled: boolean) => Promise<{ revision: bigint; enabled: boolean }>;
  onProjectContent?: ProjectContentCall;
  onSaveAgentConfig?: (config: AgentConfigEdit) => void;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onAddTask?: AddTask;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
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
  agentPanel,
  onAgentPanel,
  selectedHumanRequest,
  selectedAgent,
  onSelectAgent,
  onSetDispatch,
  onProjectContent,
  onSaveAgentConfig,
  onEditTask,
  onAddTask,
  onLoadTaskDetail,
  onLoadTaskHistory,
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
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [taskContent, setTaskContent] = useState<readonly TaskContentChip[]>([]);
  const [knowledgeView, setKnowledgeView] = useState<{ board?: boolean; project?: string; entity?: string; id?: string; revision?: number; repository?: string }>({});
  const [requestedEntity, setRequestedEntity] = useState<{ id: string }>();
  const openKnowledge = (board: boolean, project?: string, entity?: string, id?: string, repository?: string, revision?: number) => { setKnowledgeView({ board, project: project ?? projectId, ...(entity ? { entity } : {}), ...(id ? { id } : {}), ...(revision ? { revision } : {}), ...(repository ? { repository } : {}) }); setLibraryOpen(true); };
  const knowledgeActivity = useKnowledgeActivity([...(scopedState?.projects.keys() ?? [])].sort(), ready ? onProjectContent : undefined);
  const openActivity = (item: KnowledgeActivity) => { const target = activityTarget(item); openKnowledge(onBoard(item), item.project_id, undefined, target.id, undefined, target.revision); };
  const [requestedMission, setRequestedMission] = useState<{ projectId: string; id: string }>();
  const [byMission, setByMission] = useState(false);
  const [workFilter, setWorkFilter] = useState<WorkFilter>("all");
  const showWork = () => { onDetail?.("work"); };
  const openWork = (id: string | undefined, missions: boolean) => { selectProject(id); setByMission(missions); if (!missions) setWorkFilter("all"); showWork(); };
  const openMission = (projectId: string, id: string) => { setRequestedMission({ projectId, id }); openWork(projectId, true); };
  // Routes into Work from outside it (floor, Library, Settings) reset the view first so the answer form or task is never hidden.
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
  const openKnowledgeRecord = async (kind: string, id: string, project: string) => {
    if (kind === "peer_question") { const question = state?.peerQuestions?.get(id); if (!question) throw new Error("Question is outside active state. Follow its retained task link to read the original conversation."); id = question.source_task_id; kind = "task"; }
    if (kind === "task") {
      const known = state?.tasks.get(id);
      if (known) setRelatedTask(known);
      else if (onProjectContent) setRelatedTask(asTask(await onProjectContent("task_read", { project_id: project, task_id: id }), project));
      else throw new Error("Task details unavailable.");
    } else if (kind === "human_request") {
      const request = state?.humanRequests.get(id); if (!request) throw new Error("Request is outside active state. Its retained identity remains linked here."); selectRequest?.(request);
    } else {
      const record = productionData.records.find((item) => item.project_id === project && item.id === id);
      const item = productionItems.find((item) => item.projectId === project && item.visualId === (record?.visual_id ?? id));
      if (!item) throw new Error("Record is outside the loaded Changes. Its retained identity remains linked here.");
      const href = pullRequestHref(item);
      if (!href) throw new Error("Pull request link unavailable.");
      globalThis.open?.(href, "_blank", "noopener,noreferrer");
    }
    setLibraryOpen(false);
  };
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

        {onDetail === undefined ? null : <nav className="dfMobileNav dfConsoleViewToggle" aria-label="Console views">
          <button type="button" aria-pressed={detail === "floor"} disabled={!ready} onClick={() => { onDetail("floor"); }}>Floor</button>
          <button type="button" aria-pressed={detail !== "floor" && selectedDetail === "agent"} disabled={!ready} onClick={showAgents}>Agents</button>
          <IconButton icon="list" aria-pressed={detail !== "floor" && selectedDetail === "work"} disabled={!ready} onClick={showWork}>Work <Badge n={waiting} /></IconButton>
          <IconButton icon="chat" disabled={!ready} onClick={() => openKnowledge(true)}>Board</IconButton>
          <IconButton icon="book" disabled={!ready} onClick={() => openKnowledge(false)}>Library</IconButton>
        </nav>}
        <div className="dfConsoleLayout">
          <section className="dfConsoleLayout__left dfFactoryConsole__section" aria-label="Factory floor">
            <FactoryFloor activity={knowledgeActivity.recent} activityCues={knowledgeActivity.cues} onOpenActivity={ready ? openActivity : undefined} requestedEntity={requestedEntity} onOpenBoard={(project, entity, id, repository) => openKnowledge(true, project, entity, id, repository)} changes={productionItems} changesRead={productionData.read} onProjectContent={onProjectContent} onOpenLibrary={(id) => openKnowledge(false, id)} onOpenTasks={ready ? (id) => openWork(id, false) : undefined} onOpenMissions={ready ? (id) => openWork(id, true) : undefined} projectId={projectId} selectedTaskId={selectedTask?.id} onSelectTask={ready ? goTask : undefined} selectedAgentId={selectedDetail === "agent" ? selectedAgent?.id : undefined} state={scopedState} graphs={graphs} graphErrors={graphErrors} onLoadNode={onLoadNode} onAddTask={ready ? onAddTask : undefined} runPaths={runPaths} lastRunPaths={lastRunPaths} onSelectAgent={ready ? chooseAgent : undefined} onSelectHumanRequest={ready ? selectRequest : undefined} connected={ready} />
          </section>

          <aside className="dfConsoleSidebar" aria-label="Selected detail">
            <div className="dfConsoleViewToggle" role="group" aria-label="Right panel">
              <IconButton icon="list" aria-pressed={selectedDetail === "work"} disabled={!ready || onDetail === undefined} onClick={showWork}>Work <Badge n={waiting} /></IconButton>
              <IconButton icon="terminal" aria-pressed={selectedDetail === "agent"} disabled={!ready || onDetail === undefined} onClick={showAgents}>Agents</IconButton>
              <IconButton icon="chat" disabled={!ready} onClick={() => openKnowledge(true)}>Board</IconButton>
              <IconButton icon="book" disabled={!ready} onClick={() => openKnowledge(false)}>Library</IconButton>
            </div>
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
                taskContent={taskContent}
                onTaskContent={setTaskContent}
                onLoadTaskDetail={onLoadTaskDetail}
                onLoadTaskHistory={onLoadTaskHistory}
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
                edit={edit}
                ready={ready}
                onSaveConfig={onSaveAgentConfig}
                onLoadTaskDetail={onLoadTaskDetail}
                onLoadTaskHistory={onLoadTaskHistory}
                terminalContent={terminalContent}
                panel={agentPanel}
                onPanel={onAgentPanel}
                telemetry={agentTelemetry(agent, scopedState, runPaths)}
                contributions={knowledgeActivity.recent}
                onOpenActivity={ready ? openActivity : undefined}
              />}
              </div>
            </div>
            {inspectedTask === undefined ? null : <ConsoleDialog key={inspectedTask.id} label="Task details" title="Task" onClose={() => { if (relatedTask) setRelatedTask(undefined); else onSelectTask?.(undefined); }}>
              <TaskDetail key={inspectedTask.id} task={inspectedTask} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} />
              {relatedTask?.status === "queued" && state?.tasks.has(relatedTask.id) && selectTask ? <button type="button" disabled={!ready} onClick={() => { setRelatedTask(undefined); goTask?.(relatedTask.id); }}>Open in Work</button> : null}
            </ConsoleDialog>}
          </aside>
        </div>
      </main>
      {!libraryOpen ? null : <ConsoleDialog key={knowledgeView.board ? "board" : "library"} label={knowledgeView.board ? "Discussion board" : "Project library"} title={knowledgeView.board ? "Board" : "Library"} className="dfLibraryDialog" onClose={() => setLibraryOpen(false)}>
        <ProjectLibrary open initialProjectId={knowledgeView.project} key={`${knowledgeView.project}:${knowledgeView.board}:${knowledgeView.entity}:${knowledgeView.id}:${knowledgeView.revision}`} board={knowledgeView.board} repository={knowledgeView.repository} entity={knowledgeView.entity} initialID={knowledgeView.id} initialRevision={knowledgeView.revision} onSource={(entity) => { selectProject(entity.split(":")[0]); setRequestedEntity({ id: entity.slice(entity.indexOf(":") + 1) }); setLibraryOpen(false); onDetail?.("floor"); }} onRecord={openKnowledgeRecord} state={state} call={ready ? onProjectContent : undefined} onUseInTask={onAddTask === undefined ? undefined : (chip) => { setTaskContent((chips) => [...chips.filter((item) => item.project_id === chip.project_id && item.content_id !== chip.content_id), chip]); setLibraryOpen(false); openWork(chip.project_id, false); }} />
      </ConsoleDialog>}
    </div>
  );
}
