import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { MAX_TASK_ATTACHMENTS, MAX_TASK_ATTACHMENT_BYTES, type IntakeView, type IntakeBody, type DiscoveredAccount, type AccountItem, type AgentItem, type IdleWakeOn, type GitHubConnectionBody, type GitHubDelegationBody, type HumanRequestItem, type ProjectItem, type RunTelemetry, type RepositoryView, type StateView, type TaskHistoryView, type TaskItem, type TaskPeerQuestion, type TaskContentPin } from "@dark-factory/client";
import type { FactoryEditView, FactoryHumanRequestView } from "./factory-app-controller.js";
import { rankLabel } from "./console-screens.js";
import { activityLabel, type KnowledgeActivity } from "./project-board.js";
import { AgentSprite } from "./factory-scene/factory-scene.js";
import { telemetryLine } from "./factory-scene/scene.js";
import { agentStatus, agentCurrentTask, agentActivity, isSpecialist, needsYou, specialistStatus, type WorkRow, type WorkState } from "./console-view.js";
import { productionStages } from "./production-view.js";
import { AnswerControls } from "./console-interactions.js";
import { Icon, IconButton } from "./icons.js";
import { Badge, SectionHeader, Status, formatTime } from "./console-kit.js";
import type { PublishedRelease, RuntimeBuild } from "./production-data.js";

/** Only the controls the operator actually changed; the rest are left alone. */
export type AgentConfigEdit = Readonly<{ model?: string; reasoningEffort?: string; accountId?: string; paused?: boolean; archived?: boolean; idlePolicy?: "wait" | "standing_instruction"; idleAfterSeconds?: number; idleInstruction?: string; idleWakeOn?: IdleWakeOn }>;

export type TaskEdit = Readonly<{ cancel?: boolean; retry?: boolean }>;
export type TaskBrief = Readonly<{ taskId: string; revision: bigint; head: bigint; instruction: string; feedback: string; outcome?: string; peerQuestions: readonly TaskPeerQuestion[]; nextPeerOffset?: bigint }>;
export type AgentPanelView = "terminal" | "config";
export type TaskScope = { agent_id: string } | { project_id: string };
type TaskCursor = { beforeUpdatedAtMs?: bigint; beforeTaskId?: string };

/** One private peer-conversation page, shared by queued and completed work. */
export function TaskConversation({ brief, onOlder, pending = false }: { brief: TaskBrief; onOlder?: () => void; pending?: boolean }) {
  const waiting = brief.peerQuestions.filter((question) => question.answer === undefined || question.answer === "").length;
  return <section className="dfTaskConversation" aria-label={`Task conversation${waiting === 0 ? "" : ` · ${waiting} awaiting answer`}`}><h3>Conversation {waiting === 0 ? null : <span aria-label={`${waiting} unanswered peer questions`}>· {waiting} awaiting answer</span>}</h3>{brief.peerQuestions.length === 0 ? <p>No peer questions</p> : <ol>{brief.peerQuestions.map((question) => <li key={question.id}><strong>Question · {question.source_task_id} → {question.target_task_id}</strong><span>{question.question}</span>{question.answer === undefined || question.answer === "" ? null : <span>Answer · {question.answer}</span>}</li>)}</ol>}{brief.nextPeerOffset === undefined || onOlder === undefined ? null : <button type="button" disabled={pending} onClick={onOlder}>Older conversation</button>}</section>;
}
const EDIT_ERRORS = new Map<string, string>([
  ["stale", "Someone else changed this. Reopen it and try again."],
  ["invalid_request", "The factory refused this edit."],
  ["not_found", "This no longer exists."],
  ["too_large", "That is too long."],
  ["rate_limited", "Too many edits at once. Try again shortly."],
  ["unauthorized", "This browser cannot edit it."],
  ["unsupported", "The factory does not support this yet."],
]);

export function editErrorCopy(edit: FactoryEditView | undefined): string | undefined {
  if (edit?.error === undefined) return undefined;
  return EDIT_ERRORS.get(edit.error.code) ?? "The edit did not complete.";
}

/** One agent: what it is doing, how it is configured, and what it owes. */
export function AgentPanel({
  agent,
  state,
  edit,
  ready,
  onSaveConfig,
  onLoadTaskDetail,
  onLoadTaskHistory,
  terminalContent,
  panel: panelProp,
  onPanel,
  telemetry,
  contributions = [],
  onOpenActivity,
}: {
  agent: AgentItem;
  state: StateView | undefined;
  edit?: FactoryEditView;
  /** A specialist's recorded Board and Library operations, already filtered to it. */
  contributions?: readonly KnowledgeActivity[];
  onOpenActivity?: (item: KnowledgeActivity) => void;
  ready: boolean;
  onSaveConfig?: (config: AgentConfigEdit) => void;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
  panel?: AgentPanelView;
  onPanel?: (panel: AgentPanelView) => void;
  /** What the current run's agent CLI recorded; absent when it recorded nothing. */
  telemetry?: RunTelemetry;
}) {
  const effort = telemetry === undefined ? "" : telemetryLine(telemetry);
  const activity = state === undefined ? "ready" : agentStatus(agent, state);
  const current = state === undefined ? undefined : agentCurrentTask(agent, state);
  const queued = state === undefined ? [] : [...state.tasks.values()]
    .filter((task) => task.assigned_agent_id === agent.id && task.status === "queued");
  const [localPanel, setLocalPanel] = useState<AgentPanelView>("terminal");
  const panel = panelProp ?? localPanel;
  const selectPanel = onPanel ?? setLocalPanel;
  const errorCopy = edit?.target === agent.id ? editErrorCopy(edit) : undefined;
  const archived = agent.archived === true;
  const specialist = isSpecialist(agent);
  const queueHint = !archived && agent.paused
    ? "Queue paused"
    : current === undefined && queued.length > 0
      ? "Queued · waiting for capacity"
      : undefined;
  // A form remounts when the served value moves under it and when its own
  // edit is refused, so a rejected change reverts instead of being resent on
  // the next blur. A refusal never changes the revision, so it needs its own
  // token, and only the refused form's: a refused task edit must not throw
  // away what the operator has typed into the config form.
  const formKey = (id: string) =>
    `${id}:${errorCopy !== undefined && edit?.target === id ? "refused" : ""}`;
  return (
    <section className="dfConsoleSidebar__panel" aria-label={`Agent ${agent.name}`}>
      <div className="dfConsoleSidebar__heading">
        <AgentSprite agent={agent} activity={state === undefined ? "waiting" : agentActivity(agent, state)} />
        <div>
          <p className="dfFactoryConsole__eyebrow">{rankLabel(specialist ? "specialist" : agent.role)} · {agent.provider}{agent.effective_model === "" ? "" : ` · ${agent.effective_model}`}</p>
          <h2>{agent.name}</h2>
          <p className="dfConsoleItem__meta">{state?.projects.get(agent.project_id)?.name ?? agent.project_id}</p>
        </div>
      </div>

      {specialist && state !== undefined ? <SpecialistSummary agent={agent} state={state} pending={edit?.pending === true} ready={ready} contributions={contributions} onOpenActivity={onOpenActivity} onSaveConfig={onSaveConfig} /> : <p className="dfConsoleSidebar__status"><Status stage={archived ? "archived" : activity} /></p>}
      {queueHint === undefined || specialist ? null : <p className="dfConsoleSidebar__inherit">{queueHint}</p>}
      {effort === "" ? null : <p className="dfConsoleSidebar__inherit" aria-label={`Recorded by the agent: ${effort}`}>{effort}</p>}

      {archived ? null : <div className="dfConsoleViewToggle" role="group" aria-label="Agent controls">
        <button type="button" aria-pressed={panel === "terminal"} onClick={() => selectPanel("terminal")}>Terminal</button>
        <button type="button" aria-pressed={panel === "config"} onClick={() => selectPanel("config")}>Settings</button>
      </div>}
      {archived ? null : <section className="dfConsoleSidebar__section dfConsoleSidebar__terminalSlot" aria-label="Terminal" hidden={panel !== "terminal"}>
        {terminalContent ?? <p className="dfFactoryConsole__empty">Opening terminal…</p>}
      </section>}

      <section className="dfConsoleSidebar__section" aria-label="Agent configuration" hidden={!archived && panel !== "config"}>
        <AgentConfig key={formKey(agent.id)} agent={agent} accounts={state === undefined ? [] : [...state.accounts.values()]} pending={edit?.pending === true} ready={ready} onSave={onSaveConfig} running={current !== undefined} />
      </section>

    </section>
  );
}

/** What a specialist is doing, owes and has done; nothing here is inferred beyond what the factory serves. */
function SpecialistSummary({ agent, state, pending, ready, contributions, onOpenActivity, onSaveConfig }: {
  agent: AgentItem; state: StateView; pending: boolean; ready: boolean;
  contributions: readonly KnowledgeActivity[]; onOpenActivity?: (item: KnowledgeActivity) => void; onSaveConfig?: (config: AgentConfigEdit) => void;
}) {
  const { stage, text, next, remit } = specialistStatus(agent, state);
  const mine = contributions.filter((item) => item.agent_id === agent.id);
  return <>
    <p className="dfConsoleSidebar__status"><Status stage={stage}>{text}</Status></p>
    {remit === "" ? null : <p className="dfConsoleItem__meta" aria-label="Remit">{remit}</p>}
    {next === "" ? null : <p className="dfConsoleSidebar__inherit">{next}</p>}
    {agent.archived || onSaveConfig === undefined ? null : <button type="button" disabled={pending || !ready} onClick={() => onSaveConfig({ paused: !agent.paused })}>{agent.paused ? "Resume" : "Pause"}</button>}
    <section className="dfConsoleSidebar__section" aria-label="Contributions">
      <h3>Contributions</h3>
      {mine.length === 0 ? <p className="dfFactoryConsole__empty">No recorded contributions yet.</p> : <ul>{mine.map((item) => <li key={item.key}><button type="button" disabled={!onOpenActivity} onClick={() => onOpenActivity?.(item)}>{activityLabel(item, state)}</button></li>)}</ul>}
    </section>
  </>;
}

function pullRequests(value: string): readonly Readonly<{ href: string; label: string }>[] {
  const found = new Map<string, string>();
  for (const match of value.matchAll(/https:\/\/github\.com\/([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)\/([A-Za-z0-9_.-]+)\/pull\/([1-9][0-9]*)\b/g)) {
    const [, owner, repository, number] = match;
    const href = `https://github.com/${owner}/${repository}/pull/${number}`;
    found.set(href, `${owner}/${repository}#${number}`);
  }
  return [...found].map(([href, label]) => ({ href, label }));
}

/**
 * The one modal shell: <dialog> owns ESC, the backdrop, the focus trap and
 * focus return, so every exit goes through close().
 */
export function ConsoleDialog({ label, title, className = "", onClose, children }: { label: string; title: string; className?: string; onClose?: () => void; children: ReactNode }) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); }, []);
  const close = () => dialog.current?.close();
  return <dialog ref={dialog} className={`dfConsoleDialog ${className}`.trim()} aria-label={label} onClose={onClose} onClick={(event) => { if (event.target === dialog.current) close(); }}>
    <div className="dfConsoleSidebar__panel">
      <SectionHeader as="h2" title={title} actions={onClose === undefined ? null : <IconButton icon="close" aria-label="Close" onClick={close} />} />
      {children}
    </div>
  </dialog>;
}

export function TaskDetail({ task, onLoadTaskDetail, onLoadTaskHistory }: {
  task: TaskItem;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
}) {
  const detailLoader = useRef(onLoadTaskDetail);
  const historyLoader = useRef(onLoadTaskHistory);
  detailLoader.current = onLoadTaskDetail;
  historyLoader.current = onLoadTaskHistory;
  const [brief, setBrief] = useState<TaskBrief>();
  const [detailError, setDetailError] = useState(false);
  const [history, setHistory] = useState<TaskHistoryView>();
  const [historyError, setHistoryError] = useState(false);
  const [olderPending, setOlderPending] = useState(false);
  useEffect(() => {
    let live = true;
    setDetailError(false);
    setHistoryError(false);
    void detailLoader.current?.(task).then((loaded) => { if (live) { setBrief(loaded); setDetailError(false); } }).catch(() => { if (live) setDetailError(true); });
    void historyLoader.current?.(task).then((loaded) => { if (live) { setHistory(loaded); setHistoryError(false); } }).catch(() => { if (live) setHistoryError(true); });
    return () => { live = false; };
  }, [task.id, task.revision]);
  const taskDate = formatTime(task.updated_at_ms);
  const links = brief === undefined ? [] : pullRequests(`${brief.outcome ?? ""}\n${brief.feedback}`);
  return <article className="dfRecentWorkDetail" aria-label="Work details">
    <h3>{task.title}</h3>
    <p><Status stage={task.status} />{taskDate === "" ? null : <> · <time>{taskDate}</time></>}</p>
    {detailError || onLoadTaskDetail === undefined ? <p role="alert">Detail unavailable</p> : brief === undefined ? <p>Loading details</p> : <>
      {brief.outcome ? <><h4>Outcome</h4><p className="dfRecentWorkText">{brief.outcome}</p></> : ["running", "queued"].includes(task.status) ? null : <p>No recorded outcome.</p>}
      {links.length === 0 ? null : <p>{links.map((link) => <a key={link.href} href={link.href} target="_blank" rel="noreferrer">PR · {link.label}</a>)}</p>}
      {brief.instruction === "" ? null : <details><summary>Instruction</summary><p className="dfRecentWorkText">{brief.instruction}</p></details>}
      {brief.feedback === "" ? null : <details><summary>Review feedback</summary><p className="dfRecentWorkText">{brief.feedback}</p></details>}
      {brief.peerQuestions.length === 0 && brief.nextPeerOffset === undefined ? null : <TaskConversation brief={brief} pending={olderPending} onOlder={brief.nextPeerOffset === undefined ? undefined : () => {
        const load = detailLoader.current;
        if (load === undefined) return;
        setOlderPending(true);
        void load(task, brief.nextPeerOffset, brief.head).then((loaded) => { setBrief(loaded); setDetailError(false); }).catch(() => setDetailError(true)).finally(() => setOlderPending(false));
      }} />}
    </>}
    <details><summary>History</summary>
      {historyError ? <p role="alert">The factory refused this history</p> : onLoadTaskHistory === undefined ? <p>History unavailable</p> : history === undefined ? <p>Loading history</p> : history.entries.length === 0 ? <p>No history.</p> : <ol>{history.entries.map((entry) => <li key={entry.operationId}><strong>{entry.kind} · {entry.status}</strong><p>{entry.actor}{entry.body === "" ? "" : ` · ${entry.body}`}</p><time>{formatTime(entry.createdAtMs)}</time></li>)}</ol>}
      {history?.entries.length === 32 ? <p>Showing the newest 32 events.</p> : null}
    </details>
  </article>;
}

export type WorkFilter = "all" | WorkState;
const FILTERS: readonly [WorkFilter, string][] = [["all", "All"], ["needs-you", "Needs you"], ["in-review", "In review"]];

/** One Work row: title, then chips, then meta, always the same shape; the title button stretches over the whole row. */
function WorkRowShell({ title, chips, meta, onOpen, href, disabled = false, pressed, expanded, children }: { title: ReactNode; chips: ReactNode; meta: ReactNode; onOpen?: () => void; href?: string; disabled?: boolean; pressed?: boolean; expanded?: boolean; children?: ReactNode }) {
  return <li className="dfConsoleItem dfWorkRow">
    <div className="dfWorkRow__head">
      {href ? <a className="dfConsoleItem__taskTitle dfWorkRow__title" href={href} target="_blank" rel="noreferrer">{title}</a> : <button type="button" className="dfConsoleItem__taskTitle dfWorkRow__title" disabled={disabled || onOpen === undefined} aria-pressed={pressed} aria-expanded={expanded} onClick={onOpen}>{title}</button>}
      <span className="dfWorkMeta">{chips}</span>
      {meta ? <span className="dfConsoleItem__meta">{meta}</span> : null}
    </div>
    {children}
  </li>;
}

/** A Library revision the operator chose for the next task, named for its chip. */
export type TaskContentChip = TaskContentPin & { title: string; project_id: string };
export type AddTask = (agent: AgentItem, instruction: string, mode: "queue" | "any", files?: readonly File[], content?: readonly TaskContentChip[]) => Promise<boolean>;

/** One list for everything open: what needs you, what is in review, running and queued. */
export function WorkPanel({
  state, rows, projectId, filter, onFilter, byMission, onByMission, missions, edit, ready, onEditTask, onAddTask, taskContent = [], onTaskContent = () => undefined, onLoadTaskDetail, onLoadTaskHistory,
  selectedTaskId, onSelectTask, selectedHumanRequest, onSelectHumanRequest, onCloseHumanRequest, requestContent, onMission, onIntakeAction, intake,
}: {
  onIntakeAction?: (projectId: string, request: IntakeBody) => void;
  /** The shown project's last intake outcome. */
  intake?: Readonly<{ state?: string; failed: boolean; busy: boolean }>;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  state: StateView | undefined;
  rows: readonly WorkRow[];
  projectId?: string;
  filter: WorkFilter;
  onFilter: (filter: WorkFilter) => void;
  byMission: boolean;
  onByMission: () => void;
  /** The mission list and detail, shown in place of the rows. */
  missions: ReactNode;
  edit?: FactoryEditView;
  ready: boolean;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onAddTask?: AddTask;
  /** Library revisions chosen with "Use in a new task", pinned when the task is added. */
  taskContent?: readonly TaskContentChip[];
  onTaskContent?: (content: readonly TaskContentChip[]) => void;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
  selectedHumanRequest?: FactoryHumanRequestView;
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  onCloseHumanRequest?: () => void;
  requestContent?: ReactNode;
  onMission: (task: TaskItem) => void;
}) {
  const [overseer, setOverseer] = useState(false);
  if (state === undefined) return <section className="dfConsoleSidebar__panel" aria-label="Work"><p className="dfFactoryConsole__empty">Waiting for the latest state…</p></section>;
  const agents = [...state.agents.values()];
  const shown = rows.filter((row) => (filter === "all" || row.state === filter || filter === "needs-you" && needsYou(row)) && (overseer || row.origin !== "overseer" && row.origin !== "specialist" || row.state === "needs-you"));
  const busy = selectedHumanRequest?.phase === "replying" || selectedHumanRequest?.phase === "cancelling";
  const owner = (task?: TaskItem) => task === undefined ? "" : task.assigned_agent_id === "" ? "Any eligible worker" : state.agents.get(task.assigned_agent_id)?.name ?? "Agent";
  const chips = (row: WorkRow) => <>
    <Status stage={row.state} />{row.request === undefined || row.request.status === "open" ? null : <Status stage={row.request.status} />}
    {row.origin === "mission" && row.task ? <button type="button" onClick={() => onMission(row.task!)}>mission</button> : row.origin === undefined ? null : <span className="dfConsoleItem__meta">{row.origin}</span>}
    {row.pr?.pullRequest ? <><a href={row.pr.pullRequest.url || `https://github.com/${row.pr.repository}/pull/${row.pr.pullRequest.number}`} target="_blank" rel="noreferrer">PR #{row.pr.pullRequest.number}</a>{productionStages(row.pr).map((stage) => <Status stage={stage} key={stage} />)}</> : null}
  </>;
  const item = (row: WorkRow) => {
    const { task, request } = row;
    if (row.intake !== undefined) {
      const { source, candidate } = row.intake;
      return <WorkRowShell key={row.key} title={row.title} chips={chips(row)} meta={`${source.repository} · by ${candidate.author}`}>
        <div className="dfConsoleSidebar__taskActions"><button type="button" aria-label={`Accept ${row.title}`} disabled={!ready || !source.enabled || intake?.busy === true || onIntakeAction === undefined} onClick={() => onIntakeAction?.(source.project_id, { action: "accept", source_id: source.id, expected_revision: source.revision, issue_number: candidate.number, content_hash: candidate.content_hash })}>Accept</button></div>
      </WorkRowShell>;
    }
    const named = projectId === undefined && state.projects.size > 1 ? state.projects.get(task?.project_id ?? request?.project_id ?? row.pr?.projectId ?? "")?.name : undefined;
    const blocked = task?.status === "blocked";
    const meta = [named, request === undefined ? owner(task) : `${state.agents.get(request.agent_id)?.name ?? "Agent"} asks`, blocked && formatTime(task.updated_at_ms) ? `since ${formatTime(task.updated_at_ms)}` : ""].filter(Boolean).join(" · ");
    if (request !== undefined) {
      const selected = selectedHumanRequest?.request.id === request.id;
      const disabled = !ready || busy || (selected ? onCloseHumanRequest === undefined : onSelectHumanRequest === undefined);
      return <WorkRowShell key={row.key} title={row.title} chips={chips(row)} meta={meta} disabled={disabled} expanded={selected} onOpen={() => { if (selected) onCloseHumanRequest?.(); else onSelectHumanRequest?.(request); }}>
        {selected ? <div className="dfConsoleItem__detail">{requestContent}</div> : null}
      </WorkRowShell>;
    }
    return <WorkRowShell key={row.key} title={row.title} chips={chips(row)} meta={meta} disabled={!ready} pressed={task && selectedTaskId === task.id} href={task ? undefined : row.pr?.pullRequest ? row.pr.pullRequest.url || `https://github.com/${row.pr.repository}/pull/${row.pr.pullRequest.number}` : undefined} onOpen={task ? (onSelectTask && (() => onSelectTask(task.id))) : undefined}>
      {blocked && task.blocked_reason ? <p className="dfConsoleItem__meta">{task.blocked_reason}</p> : null}
      {!blocked || onEditTask === undefined ? null : <div className="dfConsoleSidebar__taskActions"><button type="button" aria-label={`Retry ${task.title}`} disabled={!ready || edit?.pending === true} onClick={() => { void onEditTask(task, { retry: true }); }}>Retry</button><button type="button" aria-label={`Cancel ${task.title}`} disabled={!ready || edit?.pending === true} onClick={() => { void onEditTask(task, { cancel: true }); }}>Cancel</button></div>}
    </WorkRowShell>;
  };
  return <section className="dfConsoleSidebar__panel" aria-label="Work">
    {state.factory.dispatch_enabled ? null : <p role="status">New work is paused. Queued tasks wait; active processes continue. An administrator can resume new work above.</p>}
    {onAddTask === undefined ? null : <NewTask agents={agents} state={state} disabled={!ready || edit?.pending === true} onAddTask={onAddTask} content={taskContent} onContent={onTaskContent} />}
    <div className="dfWorkBar">
      <div className="dfConsoleViewToggle" role="group" aria-label="Work filter">
        {FILTERS.map(([value, label]) => <button key={value} type="button" aria-pressed={!byMission && filter === value} disabled={!ready} onClick={() => onFilter(value)}>{label} {value === "needs-you" ? <Badge n={rows.filter(needsYou).length} /> : null}</button>)}
        <button type="button" aria-pressed={byMission} disabled={!ready} onClick={onByMission}>By mission</button>
      </div>
      {byMission ? null : <IconButton icon="terminal" aria-label="Show overseer and specialist passes" aria-pressed={overseer} disabled={!ready} onClick={() => setOverseer(!overseer)} />}
    </div>
    <div hidden={byMission}>
      {rows.some((row) => row.intake !== undefined) && (intake?.failed || !["ok", "accepted", "imported", "withdrawn", "withdrawal_pending", undefined].includes(intake?.state)) ? <p role="alert">Could not accept: {intake?.failed ? "try again" : intake?.state?.replaceAll("_", " ")}.</p> : null}
      {shown.length === 0 ? <p className="dfFactoryConsole__empty">{filter === "all" ? "No open work" : "Nothing here"}</p> : <ul className="dfConsoleItems">{shown.map(item)}</ul>}
    </div>
    <div hidden={!byMission}>{missions}</div>
  </section>;
}

/**
 * The target is one agent, or "any:" plus a project whose first worker names the shared queue.
 * Library context limits the targets to its project, since a pin cannot cross projects.
 */
function NewTask({ agents, state, disabled, onAddTask, content, onContent }: {
  agents: readonly AgentItem[];
  state: StateView;
  disabled: boolean;
  onAddTask: AddTask;
  content: readonly TaskContentChip[];
  onContent: (content: readonly TaskContentChip[]) => void;
}) {
  const [open, setOpen] = useState(content.length > 0);
  useEffect(() => { if (content.length > 0) setOpen(true); }, [content]);
  const [files, setFiles] = useState<readonly File[]>([]);
  const [fileError, setFileError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const busy = disabled || submitting;
  const addFiles = (incoming: readonly File[]) => {
    if (busy || incoming.length === 0) return;
    const next = [...files, ...incoming];
    if (next.length > MAX_TASK_ATTACHMENTS || next.reduce((total, file) => total + file.size, 0) > MAX_TASK_ATTACHMENT_BYTES) { setFileError("Attach up to 8 files, totalling 8 MiB."); return; }
    if (incoming.some((file) => file.size === 0 || new TextEncoder().encode(file.name).length > 255 || /[\u0000-\u001f\u007f-\u009f]/u.test(file.name))) { setFileError("Choose nonempty files with names under 256 bytes and no control characters."); return; }
    setFiles(next); setFileError("");
  };
  const scope = content[0]?.project_id;
  const live = agents.filter((agent) => !agent.archived && (scope === undefined || agent.project_id === scope));
  const shared = [...state.projects.values()].flatMap((project) => {
    const worker = live.find((agent) => agent.project_id === project.id && agent.role === "worker");
    return worker === undefined ? [] : [{ project, worker }];
  });
  return <details className="dfConsoleSidebar__section" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}><summary>New task</summary>
    <form className="dfFactoryConsole__reply" aria-label="New task"
      onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); }}
      onDrop={(event) => { if (event.dataTransfer.files.length > 0) { event.preventDefault(); addFiles(Array.from(event.dataTransfer.files)); } }}
      onPaste={(event) => { const pasted = Array.from(event.clipboardData.files); if (pasted.length > 0) { event.preventDefault(); addFiles(pasted); } }}
      onSubmit={(event) => {
      event.preventDefault();
      const form = event.currentTarget;
      const data = new FormData(form);
      const target = String(data.get("target"));
      const agent = target.startsWith("any:") ? shared.find(({ project }) => project.id === target.slice(4))?.worker : state.agents.get(target);
      const instruction = String(data.get("instruction"));
      if (busy || agent === undefined || instruction.trim() === "") return;
      setSubmitting(true);
      void onAddTask(agent, instruction, target.startsWith("any:") ? "any" : "queue", files, content).then((added) => { if (added) { form.reset(); setFiles([]); setFileError(""); onContent([]); } }).catch(() => setFileError("Submission failed. Your files are still here; try again.")).finally(() => setSubmitting(false));
    }}>
      <label htmlFor="df-new-task-target">For</label>
      <select id="df-new-task-target" name="target" required disabled={busy}>
        {shared.map(({ project }) => <option key={project.id} value={`any:${project.id}`}>Any eligible worker · {project.name}</option>)}
        {live.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
      </select>
      {content.length === 0 ? null : <ul aria-label="Library context">{content.map((item) => <li key={item.content_id}><span>{item.title} · Revision {String(item.revision)}</span><button type="button" aria-label={`Remove ${item.title}`} disabled={busy} onClick={() => onContent(content.filter((other) => other !== item))}>Remove</button></li>)}</ul>}
      <label htmlFor="df-new-task-instruction">Instruction</label>
      <textarea id="df-new-task-instruction" name="instruction" rows={4} required disabled={busy} />
      <label htmlFor="df-new-task-files">Attach files</label>
      <input id="df-new-task-files" type="file" multiple disabled={busy} onChange={(event) => { addFiles(Array.from(event.currentTarget.files ?? [])); event.currentTarget.value = ""; }} />
      <small>Paste images or drop files here. Up to 8 files, 8 MiB total.</small>
      {files.length === 0 ? null : <ul aria-label="Task attachments">{files.map((file, index) => <li key={index}><AttachmentPreview file={file} /><span>{file.name} · {Math.ceil(file.size / 1024)} KiB</span><button type="button" aria-label={`Remove ${file.name}`} disabled={busy} onClick={() => { setFiles(files.filter((_, i) => i !== index)); setFileError(""); }}>Remove</button></li>)}</ul>}
      {fileError === "" ? null : <p role="alert">{fileError}</p>}
      <IconButton icon="plus" type="submit" disabled={busy}>{submitting ? "Adding task…" : "Add to queue"}</IconButton>
    </form>
  </details>;
}

function AttachmentPreview({ file }: { file: File }) {
  const [url, setURL] = useState<string>();
  useEffect(() => {
    if (!["image/png", "image/jpeg", "image/gif", "image/webp"].includes(file.type)) { setURL(undefined); return; }
    const next = URL.createObjectURL(file); setURL(next);
    return () => URL.revokeObjectURL(next);
  }, [file]);
  return url === undefined ? null : <img src={url} alt={`Preview of ${file.name}`} style={{ maxWidth: 160, maxHeight: 120, objectFit: "contain" }} />;
}

/**
 * The two inputs hold the agent's OWN override, so empty means inherit and the
 * placeholder shows what the run will use instead. This says where that came
 * from, because "inherited" is useless without the file that decided it.
 */
function modelSourceCaption(agent: AgentItem): string {
  if (agent.model_source === "agent") return "set on this agent";
  if (agent.model_source === "") return "CLI default (not visible to the factory)";
  return `inherited from ${agent.model_source}`;
}

function AgentConfig({
  agent,
  accounts,
  pending,
  ready,
  onSave,
  running = false,
}: {
  agent: AgentItem;
  accounts: readonly AccountItem[];
  pending: boolean;
  ready: boolean;
  onSave?: (config: AgentConfigEdit) => void;
  /** A live run: the daemon refuses to archive under it. */
  running?: boolean;
}) {
  const [model, setModel] = useState(agent.model);
  const [reasoningEffort, setReasoningEffort] = useState(agent.reasoning_effort);
  const [accountId, setAccountId] = useState(agent.account_id);
  const [paused, setPaused] = useState(agent.paused);
  const [archiveConfirm, setArchiveConfirm] = useState(false);
  const [idlePolicy, setIdlePolicy] = useState(agent.idle_policy);
  const [idleAfterSeconds, setIdleAfterSeconds] = useState(String(agent.idle_after_seconds));
  const [idleInstruction, setIdleInstruction] = useState(agent.idle_instruction);
  const [wakeOn, setWakeOn] = useState<IdleWakeOn>(agent.idle_wake_on ?? "");
  const served = useRef({ model: agent.model, reasoningEffort: agent.reasoning_effort, accountId: agent.account_id, paused: agent.paused, idlePolicy: agent.idle_policy, idleAfterSeconds: String(agent.idle_after_seconds), idleInstruction: agent.idle_instruction });
  useEffect(() => {
    const previous = served.current;
    if (model === previous.model) setModel(agent.model);
    if (reasoningEffort === previous.reasoningEffort) setReasoningEffort(agent.reasoning_effort);
    if (accountId === previous.accountId) setAccountId(agent.account_id);
    if (paused === previous.paused) setPaused(agent.paused);
    if (idlePolicy === previous.idlePolicy) setIdlePolicy(agent.idle_policy);
    if (idleAfterSeconds === previous.idleAfterSeconds) setIdleAfterSeconds(String(agent.idle_after_seconds));
    if (idleInstruction === previous.idleInstruction) setIdleInstruction(agent.idle_instruction);
    served.current = { model: agent.model, reasoningEffort: agent.reasoning_effort, accountId: agent.account_id, paused: agent.paused, idlePolicy: agent.idle_policy, idleAfterSeconds: String(agent.idle_after_seconds), idleInstruction: agent.idle_instruction };
  }, [agent, model, reasoningEffort, accountId, paused, idlePolicy, idleAfterSeconds, idleInstruction]);
  if (onSave === undefined) return null;
  if (agent.archived) return <div className="dfConsoleSidebar__config">
    <button type="button" disabled={pending || !ready} onClick={() => onSave({ archived: false })}>Restore paused</button>
  </div>;
  // Sending a control the operator did not touch would make the daemon
  // revalidate it, so a stored pair it no longer accepts could not be paused.
  const idleAfter = Math.max(0, Math.floor(Number(idleAfterSeconds) || 0));
  const standing = idlePolicy === "standing_instruction";
  const supervising = agent.role === "orchestrator";
  const specialist = isSpecialist(agent);
  const toggleWake = (class_: "failures" | "merges", on: boolean) => {
    const classes = new Set(wakeOn === "" ? [] : wakeOn.split(","));
    if (on) classes.add(class_); else classes.delete(class_);
    setWakeOn(["failures", "merges"].filter((item) => classes.has(item)).join(",") as IdleWakeOn);
  };
  // Submit the complete standing rule when its wait or instruction changes.
  const ruleMoved = idlePolicy !== agent.idle_policy || idleAfter !== agent.idle_after_seconds || idleInstruction !== agent.idle_instruction;
  const ruleIncomplete = standing && (idleAfter < 1 || idleInstruction.trim() === "");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (ruleIncomplete) return;
    onSave({
      ...(model === agent.model ? {} : { model }),
      ...(reasoningEffort === agent.reasoning_effort ? {} : { reasoningEffort }),
      ...(accountId === agent.account_id ? {} : { accountId }),
      ...(paused === agent.paused ? {} : { paused }),
      ...(!ruleMoved ? {} : standing ? { idlePolicy, idleAfterSeconds: idleAfter, idleInstruction } : { idlePolicy }),
      ...(!standing || supervising || wakeOn === (agent.idle_wake_on ?? "") ? {} : { idleWakeOn: wakeOn }),
    });
  };
  return (
    <form className="dfConsoleSidebar__section dfConsoleSidebar__config" aria-label="Agent configuration" onSubmit={submit}>
      {agent.provider === "shell" ? <p className="dfConsoleSidebar__inherit">shell has no model</p> : (
        <>
          <label htmlFor={`df-model-${agent.id}`}>Model</label>
          <input id={`df-model-${agent.id}`} value={model} placeholder={agent.effective_model} disabled={pending} onChange={(event) => setModel(event.currentTarget.value)} />
          <label htmlFor={`df-effort-${agent.id}`}>Reasoning effort</label>
          <input id={`df-effort-${agent.id}`} value={reasoningEffort} placeholder={agent.effective_reasoning_effort} disabled={pending} onChange={(event) => setReasoningEffort(event.currentTarget.value)} />
          <p className="dfConsoleSidebar__inherit">{modelSourceCaption(agent)}</p>
          <label htmlFor={`df-account-${agent.id}`}>Account</label>
          <select id={`df-account-${agent.id}`} value={accountId} disabled={pending} onChange={(event) => setAccountId(event.currentTarget.value)}>
            <option value="">provider default</option>
            {accounts.filter((account) => account.provider === agent.provider).map((account) => (
              <option key={account.id} value={account.id}>{account.label}</option>
            ))}
          </select>
        </>
      )}
      <label className="dfConsoleSidebar__toggle" htmlFor={`df-paused-${agent.id}`}>
        <input id={`df-paused-${agent.id}`} type="checkbox" checked={paused} disabled={pending} onChange={(event) => setPaused(event.currentTarget.checked)} />
        Paused
      </label>
      {agent.role !== "worker" || agent.archived === undefined ? null : archiveConfirm ? <span><button type="button" className="dfDanger" autoFocus disabled={pending || !ready} onClick={() => onSave({ archived: true })}>{specialist ? (running ? "Confirm stop (ends the running review)" : "Confirm stop") : "Confirm archive"}</button><button type="button" disabled={pending} onClick={() => setArchiveConfirm(false)}>{specialist ? "Keep specialist" : "Keep worker"}</button></span> : <button type="button" className="dfDanger" disabled={pending || !ready} onClick={() => setArchiveConfirm(true)}>{specialist ? "Stop specialist" : "Archive worker"}</button>}
      <h3>{supervising ? "Supervision" : "Rules"}</h3>
      <label htmlFor={`df-idle-${agent.id}`}>{supervising ? "When work changes" : "When ready"}</label>
      <select id={`df-idle-${agent.id}`} value={idlePolicy} disabled={pending} onChange={(event) => setIdlePolicy(event.currentTarget.value as typeof idlePolicy)}>
        <option value="wait">wait for work</option>
        <option value="standing_instruction">{supervising ? "supervise worker activity" : "run a standing instruction"}</option>
      </select>
      {idlePolicy === "standing_instruction" ? (
        <>
          <label htmlFor={`df-idle-after-${agent.id}`}>{supervising ? "Cooldown seconds" : "After seconds ready"}</label>
          <input id={`df-idle-after-${agent.id}`} inputMode="numeric" value={idleAfterSeconds} disabled={pending} onChange={(event) => setIdleAfterSeconds(event.currentTarget.value)} />
          <label htmlFor={`df-idle-instruction-${agent.id}`}>Instruction</label>
          <textarea id={`df-idle-instruction-${agent.id}`} rows={3} value={idleInstruction} disabled={pending} onChange={(event) => setIdleInstruction(event.currentTarget.value)} />
          {supervising ? null : <>
            <fieldset disabled={pending}><legend>Also review sooner on</legend>
              <label><input type="checkbox" checked={wakeOn.includes("failures")} onChange={(event) => toggleWake("failures", event.currentTarget.checked)} /> failures</label>
              <label><input type="checkbox" checked={wakeOn.includes("merges")} onChange={(event) => toggleWake("merges", event.currentTarget.checked)} /> merges</label>
            </fieldset>
          </>}
          <p className="dfConsoleSidebar__inherit">{agent.idle_runs_used} idle runs</p>
          {supervising ? <p className="dfConsoleSidebar__inherit">initial inspection, then worker events</p> : null}
          {ruleIncomplete ? <p className="dfConsoleSidebar__inherit">a standing instruction needs at least a second and text</p> : null}
        </>
      ) : null}
      <button type="submit" disabled={pending || !ready || ruleIncomplete}>{pending ? "Saving" : "Save"}</button>
    </form>
  );
}

/** Two observations and one command. Nothing here installs anything, and a
 * missing release is no answer from GitHub rather than proof of being current. */
export function HumanRequestPanel({
  selected,
  onReplyChange,
  onReply,
  onCancel,
  onOpenTerminal,
  terminalReady,
}: {
  selected: FactoryHumanRequestView;
  onReplyChange?: (reply: string) => void;
  onReply?: () => void;
  onCancel?: () => void;
  onOpenTerminal?: (request: FactoryHumanRequestView["request"]) => void;
  terminalReady: boolean;
}) {
  const busy = selected.phase === "replying" || selected.phase === "cancelling";
  return (
    <article className="dfFactoryConsole__humanRequest" aria-label="Selected question" aria-live="polite">
      {selected.phase === "loading" ? <p className="dfFactoryConsole__empty">Loading the question…</p> : (
        <>
          <h3>Decision needed</h3>
          <p className="dfFactoryConsole__question">{selected.question}</p>
          <AnswerControls surface="factory" options={selected.options} canReply={selected.canReply} reply={selected.reply} replyMaxBytes={selected.replyMaxBytes} busy={busy} onReplyChange={onReplyChange} onReply={onReply} submitLabel="Answer" submittingLabel="Answering…" />
          {selected.canReply ? null : <p className="dfFactoryConsole__empty">{selected.request.status === "open" ? "This open decision is read-only in this view." : `This decision is ${selected.request.status.replaceAll("_", " ")}.`}</p>}
          <div className="dfFactoryConsole__humanActions">
            {selected.canCancel ? <button type="button" disabled={busy || onCancel === undefined} onClick={onCancel}>Stop task</button> : null}
            {onOpenTerminal === undefined ? null : <button type="button" disabled={busy || !terminalReady} onClick={() => onOpenTerminal(selected.request)}>Open terminal</button>}
          </div>
        </>
      )}
    </article>
  );
}
