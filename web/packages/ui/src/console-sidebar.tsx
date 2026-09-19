import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { MAX_TASK_PRIORITY, type IntakeView, type IntakeBody, type DiscoveredAccount, type AccountItem, type AgentItem, type GitHubConnectionBody, type GitHubDelegationBody, type ProjectItem, type RepositoryMutation, type RepositoryView, type StateView, type TaskHistoryView, type TaskItem, type TaskListView, type TaskPeerQuestion } from "@dark-factory/client";
import type { FactoryEditView, FactoryHumanRequestView } from "./factory-app-controller.js";
import type { FactoryGitHubView } from "./factory-settings-coordinator.js";
import { rankLabel } from "./console-screens.js";
import { AgentSprite } from "./factory-scene/factory-scene.js";
import { agentStatus, agentCurrentTask, agentActivity } from "./console-view.js";
import { AnswerControls } from "./console-interactions.js";
import type { FloorAppearance } from "./floor-appearance.js";

/** Only the controls the operator actually changed; the rest are left alone. */
export type AgentConfigEdit = Readonly<{ model?: string; reasoningEffort?: string; accountId?: string; paused?: boolean; archived?: boolean; idlePolicy?: "wait" | "standing_instruction"; idleAfterSeconds?: number; idleInstruction?: string; idleRunBudget?: number }>;

export type TaskEdit = Readonly<{ title?: string; body?: string; priority?: number; assignedAgentId?: string; cancel?: boolean }>;
export type TaskBrief = Readonly<{ taskId: string; revision: bigint; head: bigint; instruction: string; feedback: string; outcome?: string; peerQuestions: readonly TaskPeerQuestion[]; nextPeerOffset?: bigint }>;
export type AgentPanelView = "terminal" | "config";

/** One private peer-conversation page, shared by queued and completed work. */
export function TaskConversation({ brief, onOlder, pending = false }: { brief: TaskBrief; onOlder?: () => void; pending?: boolean }) {
  return <section aria-label="Task conversation"><h3>Conversation</h3>{brief.peerQuestions.length === 0 ? <p>No peer questions</p> : <ol>{brief.peerQuestions.map((question) => <li key={question.id}><strong>Question · {question.source_task_id} → {question.target_task_id}</strong><span>{question.question}</span><small>Recipient delivery · {question.recipient_delivery_state.replaceAll("_", " ")}</small>{question.answer === undefined || question.answer === "" ? null : <><span>Answer · {question.answer}</span><small>Answer delivery · {question.answer_delivery_state.replaceAll("_", " ")}</small></>}</li>)}</ol>}{brief.nextPeerOffset === undefined || onOlder === undefined ? null : <button type="button" disabled={pending} onClick={onOlder}>Older conversation</button>}</section>;
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
  onEditAppearance,
  onEditTask,
  onLoadTaskDetail,
  onLoadTaskHistory,
  onLoadTaskList,
  terminalContent,
  panel: panelProp,
  onPanel,
}: {
  agent: AgentItem;
  state: StateView | undefined;
  edit?: FactoryEditView;
  ready: boolean;
  onSaveConfig?: (config: AgentConfigEdit) => void;
  onEditAppearance?: (agent: AgentItem) => void;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
  onLoadTaskList?: (agentId: string, cursor?: { beforeUpdatedAtMs?: bigint; beforeTaskId?: string }) => Promise<TaskListView>;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
  panel?: AgentPanelView;
  onPanel?: (panel: AgentPanelView) => void;
}) {
  const activity = state === undefined ? "ready" : agentStatus(agent, state);
  const current = state === undefined ? undefined : agentCurrentTask(agent, state);
  const queued = state === undefined ? [] : [...state.tasks.values()]
    .filter((task) => task.assigned_agent_id === agent.id && task.status === "queued");
  const [localPanel, setLocalPanel] = useState<AgentPanelView>("terminal");
  const panel = panelProp ?? localPanel;
  const selectPanel = onPanel ?? setLocalPanel;
  const errorCopy = edit?.target === agent.id ? editErrorCopy(edit) : undefined;
  const archived = agent.archived === true;
  const queueHint = !archived && agent.paused
    ? "QUEUE PAUSED"
    : current === undefined && queued.length > 0
      ? "QUEUED · WAITING FOR CAPACITY"
      : undefined;
  // A form remounts when the served value moves under it and when its own
  // edit is refused, so a rejected change reverts instead of being resent on
  // the next blur. A refusal never changes the revision, so it needs its own
  // token, and only the refused form's: a refused task edit must not throw
  // away what the operator has typed into the config form.
  const formKey = (id: string, revision: bigint) =>
    `${id}:${revision}:${errorCopy !== undefined && edit?.target === id ? "refused" : ""}`;
  return (
    <section className="dfConsoleSidebar__panel" aria-label={`Agent ${agent.name}`}>
      <div className="dfConsoleSidebar__heading">
        <button type="button" className="dfAgentSpriteEdit" aria-label={`Edit appearance for ${agent.name}`} onClick={() => onEditAppearance?.(agent)} disabled={archived || onEditAppearance === undefined}>
          <AgentSprite agent={agent} activity={state === undefined ? "waiting" : agentActivity(agent, state)} />
          <svg className="dfAgentSpriteEdit__icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 11.5 10.5 4 12 5.5 4.5 13 2 14Zm7-8L11.5 2 14 4.5 12.5 6Z" /></svg>
        </button>
        <div>
          <p className="dfFactoryConsole__eyebrow">{rankLabel(agent.role)} · {agent.provider}{agent.effective_model === "" ? "" : ` · ${agent.effective_model}`}</p>
          <h2>{agent.name}</h2>
        </div>
      </div>

      <p className="dfConsoleSidebar__status">{archived ? "archived" : activity === "needs-you" ? "! needs you" : activity}</p>
      {queueHint === undefined ? null : <p className="dfConsoleSidebar__inherit">{queueHint}</p>}

      {archived ? null : <div className="dfConsoleViewToggle" role="group" aria-label="Agent controls">
        <button type="button" aria-pressed={panel === "terminal"} onClick={() => selectPanel("terminal")}>Terminal</button>
        <button type="button" aria-pressed={panel === "config"} onClick={() => selectPanel("config")}>Settings</button>
      </div>}
      {archived ? null : <section className="dfConsoleSidebar__section dfConsoleSidebar__terminalSlot" aria-label="Terminal" hidden={panel !== "terminal"}>
        {terminalContent ?? <p className="dfFactoryConsole__empty">Opening terminal…</p>}
      </section>}

      <section className="dfConsoleSidebar__section" aria-label="Agent configuration" hidden={!archived && panel !== "config"}>
        <AgentConfig key={formKey(agent.id, agent.revision)} agent={agent} accounts={state === undefined ? [] : [...state.accounts.values()]} pending={edit?.pending === true} ready={ready} onSave={onSaveConfig} />
      </section>

      <RecentWork
        agent={agent}
        completionRevision={state === undefined ? "" : [...state.tasks.values()].filter((task) => task.assigned_agent_id === agent.id).map((task) => `${task.id}:${task.revision}`).join(" ")}
        onLoadTaskList={onLoadTaskList}
        onLoadTaskDetail={onLoadTaskDetail}
        onLoadTaskHistory={onLoadTaskHistory}
      />
    </section>
  );
}

function dateLabel(value: bigint | undefined): string {
  if (value === undefined || value > BigInt(Number.MAX_SAFE_INTEGER)) return "DATE UNAVAILABLE";
  const date = new Date(Number(value));
  return Number.isNaN(date.valueOf()) ? "DATE UNAVAILABLE" : date.toISOString().replace("T", " ").replace(".000Z", " UTC");
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

/** Private completed-task detail, kept bounded until the operator opens it. */
function RecentWork({
  agent, completionRevision, onLoadTaskList, onLoadTaskDetail, onLoadTaskHistory,
}: {
  agent: AgentItem;
  completionRevision: string;
  onLoadTaskList?: (agentId: string, cursor?: { beforeUpdatedAtMs: bigint; beforeTaskId: string }) => Promise<TaskListView>;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
}) {
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState<readonly TaskItem[]>([]);
  const [selectedId, setSelectedId] = useState<string>();
  const [total, setTotal] = useState<bigint>();
  const [hasMore, setHasMore] = useState(false);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const request = useRef(0);
  const load = (append: boolean) => {
    const loader = onLoadTaskList;
    if (loader === undefined || (append && (pending || !hasMore))) return;
    const cursor = append ? page.at(-1) : undefined;
    if (append && cursor?.updated_at_ms === undefined) return;
    const token = ++request.current;
    setPending(true);
    setFailed(false);
    void loader(agent.id, cursor === undefined ? undefined : { beforeUpdatedAtMs: cursor.updated_at_ms!, beforeTaskId: cursor.id }).then((result) => {
      if (request.current !== token) return;
      setPage((prior) => append ? [...prior, ...result.tasks] : result.tasks);
      setTotal(result.total);
      setHasMore(result.hasMore);
    }).catch(() => { if (request.current === token) setFailed(true); }).finally(() => { if (request.current === token) setPending(false); });
  };
  useEffect(() => {
    if (!open) return;
    if (!dialog.current?.open) dialog.current?.showModal();
    load(false);
    return () => { ++request.current; };
  }, [open, agent.id, completionRevision]);
  const selected = page.find((task) => task.id === selectedId) ?? page[0];
  return <div className="dfConsoleRecentWork dfConsoleSidebar__section">
    <button type="button" onClick={() => setOpen(true)}>RECENT WORK{total === undefined ? "" : ` · ${total}`}</button>
    {!open ? null : <dialog ref={dialog} className="dfConsoleDialog dfRecentWorkDialog" aria-label={`Recent work for ${agent.name}`} onClose={() => setOpen(false)} onClick={(event) => { if (event.target === dialog.current) dialog.current?.close(); }}>
      <div className="dfConsoleSidebar__panel">
        <div className="dfConsoleSidebar__heading"><h2>RECENT WORK · {agent.name}</h2><button type="button" onClick={() => dialog.current?.close()}>CLOSE</button></div>
        {failed ? <p role="alert">RECENT WORK UNAVAILABLE <button type="button" onClick={() => load(false)}>RETRY</button></p> : null}
        <div className="dfRecentWorkLayout">
          <nav aria-label="Completed work">
            <p>{total === undefined ? "Loading work…" : `${total} completed or blocked tasks`}</p>
            <ol className="dfConsoleItems">{page.map((task) => <li key={task.id}>
              <button type="button" className="dfRecentWorkRow" aria-pressed={selected?.id === task.id} onClick={() => setSelectedId(task.id)}>
                <strong>{task.title}</strong><span>{task.status.replaceAll("_", " ")}</span><time>{dateLabel(task.updated_at_ms)}</time>
              </button>
            </li>)}</ol>
            {!hasMore ? null : <button type="button" disabled={pending} onClick={() => load(true)}>{pending ? "LOADING" : "SHOW MORE"}</button>}
          </nav>
          {selected === undefined ? <p>{pending ? "Loading recent work…" : "No completed or blocked tasks."}</p> : <TaskDetail key={`${selected.id}:${selected.revision}`} task={selected} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} />}
        </div>
      </div>
    </dialog>}
  </div>;
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
    void detailLoader.current?.(task).then((loaded) => { if (live) setBrief(loaded); }).catch(() => { if (live) setDetailError(true); });
    void historyLoader.current?.(task).then((loaded) => { if (live) setHistory(loaded); }).catch(() => { if (live) setHistoryError(true); });
    return () => { live = false; };
  }, [task.id, task.revision]);
  const taskDate = dateLabel(task.updated_at_ms);
  const links = brief === undefined ? [] : pullRequests(`${brief.outcome ?? ""}\n${brief.feedback}`);
  return <article className="dfRecentWorkDetail" aria-label="Work details">
    <h3>{task.title}</h3>
    <p>{task.status.replaceAll("_", " ")}{taskDate === "DATE UNAVAILABLE" ? null : <> · <time title={taskDate}>{new Date(Number(task.updated_at_ms)).toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: "UTC" })} UTC</time></>}</p>
    {detailError || onLoadTaskDetail === undefined ? <p role="alert">DETAIL UNAVAILABLE</p> : brief === undefined ? <p>LOADING DETAILS</p> : <>
      {brief.outcome ? <><h4>OUTCOME</h4><p className="dfRecentWorkText">{brief.outcome}</p></> : ["running", "queued"].includes(task.status) ? null : <p>No recorded outcome.</p>}
      {links.length === 0 ? null : <p>{links.map((link) => <a key={link.href} href={link.href} target="_blank" rel="noreferrer">PR · {link.label}</a>)}</p>}
      {brief.instruction === "" ? null : <details><summary>Instruction</summary><p className="dfRecentWorkText">{brief.instruction}</p></details>}
      {brief.feedback === "" ? null : <details><summary>Review feedback</summary><p className="dfRecentWorkText">{brief.feedback}</p></details>}
      {brief.peerQuestions.length === 0 && brief.nextPeerOffset === undefined ? null : <TaskConversation brief={brief} pending={olderPending} onOlder={brief.nextPeerOffset === undefined ? undefined : () => {
        const load = detailLoader.current;
        if (load === undefined) return;
        setOlderPending(true);
        void load(task, brief.nextPeerOffset, brief.head).then(setBrief).catch(() => setDetailError(true)).finally(() => setOlderPending(false));
      }} />}
    </>}
    <details><summary>History</summary>
      {historyError ? <p role="alert">THE FACTORY REFUSED THIS HISTORY</p> : onLoadTaskHistory === undefined ? <p>HISTORY UNAVAILABLE</p> : history === undefined ? <p>LOADING HISTORY</p> : history.entries.length === 0 ? <p>No history.</p> : <ol>{history.entries.map((entry) => <li key={entry.operationId}><strong>{entry.kind} · {entry.status}</strong><p>{entry.actor}{entry.body === "" ? "" : ` · ${entry.body}`}</p><time>{dateLabel(entry.createdAtMs)}</time></li>)}</ol>}
      {history?.entries.length === 32 ? <p>Showing the newest 32 events.</p> : null}
    </details>
  </article>;
}

/** The single editable queue keeps the authoritative per-agent task order. */
export function QueuePanel({
  state,
  edit,
  ready,
  onEditTask,
  onLoadTaskDetail,
  selectedTaskId,
  onSelectTask,
}: {
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  state: StateView | undefined;
  edit?: FactoryEditView;
  ready: boolean;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
}) {
  const agents = state === undefined ? [] : [...state.agents.values()];
  const tasks = state === undefined ? [] : [...state.tasks.values()];
  const running = tasks.filter((task) => task.status === "running");
  const queued = [
    ...agents.flatMap((agent) => {
      const assigned = tasks
        .filter((task) => task.assigned_agent_id === agent.id && task.status === "queued");
      return assigned.length === 0 ? [] : [{ projectId: agent.project_id, tasks: assigned }];
    }),
    // Shared work waits under its project until an eligible worker claims it.
    ...(state === undefined ? [] : [...state.projects.values()]).flatMap((project) => {
      const shared = tasks.filter((task) => task.assigned_agent_id === "" && task.project_id === project.id && task.status === "queued");
      return shared.length === 0 ? [] : [{ projectId: project.id, tasks: shared }];
    }),
  ];
  return <section className="dfConsoleSidebar__panel" aria-label="Queue">
    {state === undefined ? <p className="dfFactoryConsole__empty">Waiting for the latest state…</p>
      : <>
        {running.length === 0 || selectedTaskId !== undefined ? null : <section className="dfConsoleSidebar__section" aria-label="Running tasks">
          <h3>Running <span>{running.length}</span></h3>
          <ul className="dfConsoleItems">{running.map((task) => <li className="dfConsoleItem" key={task.id}><div className="dfConsoleItem__summary">
            <button type="button" className="dfConsoleItem__taskTitle" disabled={!ready || onSelectTask === undefined} aria-pressed={selectedTaskId === task.id} onClick={() => onSelectTask?.(task.id)}>{task.title}</button>
            <span className="dfConsoleItem__meta">{agents.find((agent) => agent.id === task.assigned_agent_id)?.name ?? "AGENT"}</span>
          </div></li>)}</ul>
        </section>}
        {queued.length === 0 ? running.length > 0 ? null : <p className="dfFactoryConsole__empty">No queued tasks</p> : <>{selectedTaskId === undefined ? <h3>Queued <span>{queued.reduce((count, group) => count + group.tasks.length, 0)}</span></h3> : null}<ul className="dfConsoleItems">{queued.flatMap(({ projectId, tasks }) => {
          const peers = agents.filter((peer) => peer.project_id === projectId);
          return tasks.filter((task) => selectedTaskId === undefined || task.id === selectedTaskId).map((task) => <QueuedTask
              selected={selectedTaskId === task.id}
              onSelectTask={onSelectTask}
              key={task.id}
              task={task}
              peers={peers}
              pending={edit?.pending === true}
              ready={ready}
              onEditTask={onEditTask}
              onLoadTaskDetail={onLoadTaskDetail}
            />);
        })}</ul></>}
      </>}
  </section>;
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
}: {
  agent: AgentItem;
  accounts: readonly AccountItem[];
  pending: boolean;
  ready: boolean;
  onSave?: (config: AgentConfigEdit) => void;
}) {
  const [model, setModel] = useState(agent.model);
  const [reasoningEffort, setReasoningEffort] = useState(agent.reasoning_effort);
  const [accountId, setAccountId] = useState(agent.account_id);
  const [paused, setPaused] = useState(agent.paused);
  const [archiveConfirm, setArchiveConfirm] = useState(false);
  const [idlePolicy, setIdlePolicy] = useState(agent.idle_policy);
  const [idleAfterSeconds, setIdleAfterSeconds] = useState(String(agent.idle_after_seconds));
  const [idleInstruction, setIdleInstruction] = useState(agent.idle_instruction);
  if (onSave === undefined) return null;
  if (agent.archived) return <div className="dfConsoleSidebar__config">
    <button type="button" disabled={pending || !ready} onClick={() => onSave({ archived: false })}>Restore paused</button>
  </div>;
  // Sending a control the operator did not touch would make the daemon
  // revalidate it, so a stored pair it no longer accepts could not be paused.
  const idleAfter = Math.max(0, Math.floor(Number(idleAfterSeconds) || 0));
  const standing = idlePolicy === "standing_instruction";
  const supervising = agent.role === "orchestrator";
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
    });
  };
  return (
    <form className="dfConsoleSidebar__section dfConsoleSidebar__config" aria-label="Agent configuration" onSubmit={submit}>
      {agent.provider === "shell" ? <p className="dfConsoleSidebar__inherit">shell has no model</p> : (
        <>
          <label htmlFor={`df-model-${agent.id}`}>MODEL</label>
          <input id={`df-model-${agent.id}`} value={model} placeholder={agent.effective_model} disabled={pending} onChange={(event) => setModel(event.currentTarget.value)} />
          <label htmlFor={`df-effort-${agent.id}`}>REASONING EFFORT</label>
          <input id={`df-effort-${agent.id}`} value={reasoningEffort} placeholder={agent.effective_reasoning_effort} disabled={pending} onChange={(event) => setReasoningEffort(event.currentTarget.value)} />
          <p className="dfConsoleSidebar__inherit">{modelSourceCaption(agent)}</p>
          <label htmlFor={`df-account-${agent.id}`}>ACCOUNT</label>
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
        PAUSED
      </label>
      {agent.role !== "worker" || agent.archived === undefined ? null : archiveConfirm ? <span><button type="button" autoFocus disabled={pending || !ready} onClick={() => onSave({ archived: true })}>Confirm archive</button><button type="button" disabled={pending} onClick={() => setArchiveConfirm(false)}>Keep worker</button></span> : <button type="button" disabled={pending || !ready} onClick={() => setArchiveConfirm(true)}>Archive worker</button>}
      <h3>{supervising ? "SUPERVISION" : "RULES"}</h3>
      <label htmlFor={`df-idle-${agent.id}`}>{supervising ? "WHEN WORK CHANGES" : "WHEN READY"}</label>
      <select id={`df-idle-${agent.id}`} value={idlePolicy} disabled={pending} onChange={(event) => setIdlePolicy(event.currentTarget.value as typeof idlePolicy)}>
        <option value="wait">wait for work</option>
        <option value="standing_instruction">{supervising ? "supervise worker activity" : "run a standing instruction"}</option>
      </select>
      {idlePolicy === "standing_instruction" ? (
        <>
          <label htmlFor={`df-idle-after-${agent.id}`}>{supervising ? "COOLDOWN SECONDS" : "AFTER SECONDS READY"}</label>
          <input id={`df-idle-after-${agent.id}`} inputMode="numeric" value={idleAfterSeconds} disabled={pending} onChange={(event) => setIdleAfterSeconds(event.currentTarget.value)} />
          <label htmlFor={`df-idle-instruction-${agent.id}`}>INSTRUCTION</label>
          <textarea id={`df-idle-instruction-${agent.id}`} rows={3} value={idleInstruction} disabled={pending} onChange={(event) => setIdleInstruction(event.currentTarget.value)} />
          <p className="dfConsoleSidebar__inherit">{agent.idle_runs_used} idle runs</p>
          {supervising ? <p className="dfConsoleSidebar__inherit">initial inspection, then worker events</p> : null}
          {ruleIncomplete ? <p className="dfConsoleSidebar__inherit">a standing instruction needs at least a second and text</p> : null}
        </>
      ) : null}
      <button type="submit" disabled={pending || !ready || ruleIncomplete}>{pending ? "SAVING" : "SAVE"}</button>
    </form>
  );
}

/**
 * A priority is durable global scheduling input. Increasing or decreasing it
 * by one is honest about the effect even when several queued tasks tie.
 */
function QueuedTask({
  task,
  selected,
  onSelectTask,
  peers,
  pending,
  ready,
  onEditTask,
  onLoadTaskDetail,
}: {
  task: TaskItem;
  selected?: boolean;
  onSelectTask?: (taskId: string) => void;
  peers: readonly AgentItem[];
  pending: boolean;
  ready: boolean;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
}) {
  const [brief, setBrief] = useState<TaskBrief>();
  const [title, setTitle] = useState(task.title);
  const [instruction, setInstruction] = useState("");
  const [loading, setLoading] = useState(false);
  const [detailError, setDetailError] = useState(false);
  const [open, setOpen] = useState(false);
  const [openedRevision, setOpenedRevision] = useState<bigint>();
  const disabled = pending || !ready || onEditTask === undefined;
  const stale = open && openedRevision !== task.revision;
  const load = async (peerOffset?: bigint, expectedHead?: bigint) => {
    if (onLoadTaskDetail === undefined) return;
    setLoading(true);
    setDetailError(false);
    try {
      const loaded = await onLoadTaskDetail(task, peerOffset, expectedHead);
      setBrief(loaded);
      if (peerOffset === undefined) {
        setTitle(task.title);
        setInstruction(loaded.instruction);
        setOpenedRevision(task.revision);
        setOpen(true);
      }
    } catch {
      setDetailError(true);
    } finally {
      setLoading(false);
    }
  };
  if (onEditTask === undefined) {
    return <li className="dfConsoleItem"><button type="button" className="dfConsoleItem__summary dfConsoleItem__taskTitle" disabled={!ready || onSelectTask === undefined} aria-pressed={selected === true} onClick={() => onSelectTask?.(task.id)}>{task.title}</button></li>;
  }
  return (
    <li>
      <details className="dfConsoleItem" onToggle={(event) => { if (event.currentTarget.open && brief === undefined && !loading) void load(); }}>
        <summary className="dfConsoleItem__summary" onClick={() => onSelectTask?.(task.id)}><strong>{task.title}</strong><span className="dfConsoleItem__meta">{task.assigned_agent_id === "" ? "ANY ELIGIBLE WORKER" : peers.find((agent) => agent.id === task.assigned_agent_id)?.name ?? "AGENT"} · QUEUED · PRIORITY {task.priority}</span></summary>
        <div className="dfConsoleItem__detail">
        {open ? <>
          <label htmlFor={`df-title-${task.id}`}>TITLE</label>
          <input id={`df-title-${task.id}`} value={title} disabled={disabled || loading || stale} onChange={(event) => setTitle(event.currentTarget.value)} />
          <label htmlFor={`df-instruction-${task.id}`}>INSTRUCTION</label>
          <textarea id={`df-instruction-${task.id}`} rows={4} value={instruction} disabled={disabled || loading || stale} onChange={(event) => setInstruction(event.currentTarget.value)} />
          {brief?.feedback === "" || brief === undefined ? null : <><label>RETAINED REVIEW FEEDBACK</label><pre className="dfConsoleSidebar__feedback">{brief.feedback}</pre></>}
        {brief === undefined ? null : <TaskConversation brief={brief} pending={loading} onOlder={brief.nextPeerOffset === undefined ? undefined : () => { void load(brief.nextPeerOffset, brief.head); }} />}
        {stale ? <p role="alert">TASK CHANGED — REOPEN BRIEF TO SAVE</p> : null}
          <div className="dfConsoleSidebar__taskActions">
            <button type="button" disabled={disabled || loading || stale || title.trim() === ""} onClick={async () => { if (await onEditTask(task, { title, body: instruction })) setOpen(false); }}>{pending ? "SAVING" : "SAVE BRIEF"}</button>
            {stale ? <button type="button" disabled={loading} onClick={() => { void load(); }}>REOPEN BRIEF</button> : null}
            <button type="button" disabled={loading} onClick={() => setOpen(false)}>DISCARD</button>
          </div>
        </> : <button type="button" disabled={disabled || loading || onLoadTaskDetail === undefined} onClick={() => { void load(); }}>{loading ? "LOADING BRIEF" : "EDIT BRIEF"}</button>}
        {detailError ? <p role="alert">{open ? "COULD NOT LOAD DETAILS. SAVE OR DISCARD YOUR DRAFT, THEN REOPEN TO RETRY." : "COULD NOT LOAD DETAILS. REOPEN THE BRIEF TO RETRY."}</p> : null}
        <div className="dfConsoleSidebar__taskActions">
          <button type="button" aria-label={`Increase priority for ${task.title}`} disabled={disabled || task.priority === MAX_TASK_PRIORITY} onClick={() => { void onEditTask(task, { priority: task.priority + 1 }); }}>INCREASE PRIORITY</button>
          <button type="button" aria-label={`Decrease priority for ${task.title}`} disabled={disabled || task.priority === -MAX_TASK_PRIORITY} onClick={() => { void onEditTask(task, { priority: task.priority - 1 }); }}>DECREASE PRIORITY</button>
          <label className="dfFactoryConsole__visuallyHidden" htmlFor={`df-assign-${task.id}`}>Agent for {task.title}</label>
          <select
            id={`df-assign-${task.id}`}
            value={task.assigned_agent_id}
            disabled={disabled}
            onChange={(event) => { void onEditTask(task, { assignedAgentId: event.currentTarget.value }); }}
          >
            {[
              ...(task.assigned_agent_id === "" ? [<option key="" value="" disabled>Any eligible worker</option>] : []),
              ...peers.filter((peer) => !peer.archived && (task.assigned_agent_id !== "" || peer.role === "worker")).map((peer) => <option key={peer.id} value={peer.id}>{peer.name}</option>),
            ]}
          </select>
          <button type="button" disabled={disabled} onClick={() => { void onEditTask(task, { cancel: true }); }}>CANCEL</button>
        </div>
        </div>
      </details>
    </li>
  );
}

/**
 * The whole-factory readout, the address it is served from, and pairing, over
 * the console rather than squeezing it: <dialog> owns ESC, the backdrop, the
 * focus trap and focus return, so every exit goes through close().
 */
export function SettingsDialog({
  floorAppearance,
  onFloorAppearanceChange,
  onResetFloorAppearance,
  state,
  ready,
  address,
  edit,
  onSaveProjectLimits,
  accounts,
  accountsPending,
  accountsError,
  github,
  onGitHub,
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
  pairing,
  onClose,
}: {
  floorAppearance: FloorAppearance;
  onFloorAppearanceChange: (appearance: FloorAppearance) => void;
  onResetFloorAppearance: () => void;
  state: StateView | undefined;
  ready: boolean;
  address: string;
  edit?: FactoryEditView;
  onSaveProjectLimits?: (project: Pick<ProjectItem, "id" | "revision">, limits: { runBudget: bigint; maxRunSeconds: number }) => void;
  /** The logins the daemon found, once it has been asked. */
  accounts?: readonly DiscoveredAccount[];
  accountsPending?: boolean;
  accountsError?: string;
  github?: FactoryGitHubView;
  onGitHub?: (request: GitHubConnectionBody) => void;
  onLoadAccounts?: () => void;
  onLinkAccount?: (login: DiscoveredAccount, label: string) => void;
  onUpdateAccount?: (account: AccountItem, change: { label?: string; remove?: boolean }) => void;
  repositories?: ReadonlyMap<string, readonly RepositoryView[]>;
  repositoryPending?: ReadonlySet<string>;
  repositoryErrors?: ReadonlyMap<string, string>;
  onLoadRepositories?: (projectId: string) => void;
  onMutateRepository?: (request: RepositoryMutation) => void;
  onCreateProject?: (request: { name: string; root: string }) => void;
  intake?: ReadonlyMap<string, IntakeView>; intakePending?: ReadonlySet<string>; intakeErrors?: ReadonlyMap<string, string>; onLoadIntake?: (projectId: string) => void; onIntakeAction?: (projectId: string, request: IntakeBody) => void;
  /** A self-contained "PAIR A PHONE" surface mounts here. */
  pairing?: ReactNode;
  onClose?: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => { dialog.current?.showModal(); }, []);
  // Discovery is an observation of the daemon's machine, so it is asked for
  // when the dialog opens rather than carried in the durable snapshot.
  const load = useRef(onLoadAccounts);
  load.current = onLoadAccounts;
  useEffect(() => { load.current?.(); }, []);
  const loadGitHub = useRef(onGitHub);
  loadGitHub.current = onGitHub;
  useEffect(() => { if (ready) loadGitHub.current?.({ action: "status" }); }, [ready]);
  const close = () => dialog.current?.close();
  return (
    <dialog
      className="dfConsoleDialog"
      ref={dialog}
      aria-label="Settings"
      onClose={onClose}
      onClick={(event) => { if (event.target === dialog.current) close(); }}
    >
      <div className="dfConsoleSidebar__panel">
        <div className="dfConsoleSidebar__heading">
          <h2>Settings</h2>
          {onClose === undefined ? null : <button type="button" onClick={close}>CLOSE</button>}
        </div>
        <div className="dfConsoleSidebar__section" aria-label="PAIRING">
          <h3>Devices &amp; pairing</h3>
          {pairing ?? <p className="dfFactoryConsole__empty">Pairing unavailable</p>}
        </div><AccountsSection
          state={state}
          accounts={accounts}
          pending={accountsPending === true}
          error={accountsError}
          onLink={onLinkAccount}
          onUpdate={onUpdateAccount}
          onRefresh={onLoadAccounts}
        />
        <GitHubSection github={github} onGitHub={onGitHub} />
        <RepositoriesSection state={state} repositories={repositories} pending={repositoryPending} errors={repositoryErrors} onLoad={onLoadRepositories} onMutate={onMutateRepository} onCreateProject={onCreateProject} />
        <IntakeSection state={state} repositories={repositories} intake={intake} pending={intakePending} errors={intakeErrors} onLoad={onLoadIntake} onLoadRepositories={onLoadRepositories} onAction={onIntakeAction} />
        <details className="dfConsoleSidebar__section" aria-label="Run limits">
          <summary>Run limits</summary>
          <ProjectLimitsSection state={state} edit={edit} ready={ready} onSave={onSaveProjectLimits} />
        </details>
        <FloorAppearanceSection appearance={floorAppearance} onChange={onFloorAppearanceChange} onReset={onResetFloorAppearance} />
        <section className="dfConsoleSidebar__section" aria-label="Help and feedback">
          <h3>Help and feedback</h3>
          <p><a href="https://darkfactory.build/feedback?kind=bug" target="_blank" rel="noopener noreferrer">Report a Dark Factory problem</a></p>
          <p><a href="https://darkfactory.build/feedback?kind=feature" target="_blank" rel="noopener noreferrer">Request a feature</a></p>
          <p><a href="https://darkfactory.build/backlog" target="_blank" rel="noopener noreferrer">Public backlog · Vote on GitHub</a></p>
          <p>Review and submit reports on GitHub. Reporting and voting do not start factory work.</p>
        </section>
      </div>
    </dialog>
  );
}

function RepositoriesSection({ state, repositories, pending, errors, onLoad, onMutate, onCreateProject }: {
  state: StateView | undefined;
  repositories?: ReadonlyMap<string, readonly RepositoryView[]>;
  pending?: ReadonlySet<string>;
  errors?: ReadonlyMap<string, string>;
  onLoad?: (projectId: string) => void;
  onMutate?: (request: RepositoryMutation) => void;
  onCreateProject?: (request: { name: string; root: string }) => void;
}) {
  const projects = state === undefined ? [] : [...state.projects.values()];
  const load = useRef(onLoad);
  load.current = onLoad;
  const [open, setOpen] = useState(false);
  const [selection, setSelection] = useState("");
  const projectId = projects.find((project) => project.id === selection)?.id ?? projects[0]?.id;
  const canLoad = onLoad !== undefined;
  useEffect(() => { if (open && canLoad && projectId) load.current?.(projectId); }, [open, projectId, canLoad]);
  return <details className="dfConsoleSidebar__section" aria-label="Repositories" onToggle={(event) => { if (event.target === event.currentTarget) setOpen(event.currentTarget.open); }}>
    <summary>Repositories</summary>
    <p className="dfConsoleSidebar__inherit">Register an existing checkout on the factory host. Each task keeps its chosen repository.</p>
    {projects.length > 1 ? <label>Project<select value={projectId} onChange={(event) => setSelection(event.currentTarget.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label> : null}
    {projects.filter((project) => project.id === projectId).map((project) => <RepositoryProject key={project.id} project={project} items={repositories?.get(project.id)} pending={pending?.has(project.id) === true} error={errors?.get(project.id)} onMutate={onMutate} />)}
    <ProjectCreateForm onCreate={onCreateProject} error={errors?.get("create")} />
  </details>;
}

function ProjectCreateForm({ onCreate, error }: { onCreate?: (request: { name: string; root: string }) => void; error?: string }) {
  const [name, setName] = useState("");
  const [root, setRoot] = useState("");
  return <form className="dfConsoleSidebar__section" onSubmit={(event) => { event.preventDefault(); if (name.trim() && root.startsWith("/")) onCreate?.({ name: name.trim(), root }); }}>
    <h3>NEW PROJECT</h3>{error === undefined ? null : <p role="alert">{error}</p>}
    <label>Name<input value={name} onChange={(event) => setName(event.currentTarget.value)} /></label>
    <label>Existing checkout<input value={root} placeholder="/absolute/path" onChange={(event) => setRoot(event.currentTarget.value)} /></label>
    <button type="submit" disabled={onCreate === undefined || !name.trim() || !root.startsWith("/")}>CREATE PROJECT</button>
  </form>;
}

function RepositoryProject({ project, items, pending, error, onMutate }: { project: ProjectItem; items?: readonly RepositoryView[]; pending: boolean; error?: string; onMutate?: (request: RepositoryMutation) => void }) {
  const [name, setName] = useState(""); const [root, setRoot] = useState(""); const [baseRef, setBaseRef] = useState("");
  return <section className="dfConsoleSidebar__section" aria-label={`Repositories for ${project.name}`}>
    <h3>{project.name}</h3>{error === undefined ? null : <p role="alert">{error}</p>}
    {items === undefined ? <p className="dfFactoryConsole__empty">{pending ? "LOADING REPOSITORIES" : "REPOSITORIES UNAVAILABLE"}</p> : <ul className="dfFactoryConsole__list">{items.map((item) => <RepositoryRow key={`${item.id}:${item.revision}`} project={project} item={item} pending={pending} onMutate={onMutate} />)}</ul>}
    <details><summary>Add repository</summary><p>Connect GitHub above to publish reviewed work. Enter the local checkout and its intended base branch.</p><form onSubmit={(event) => { event.preventDefault(); if (name.trim() && root.startsWith("/") && baseRef.trim()) onMutate?.({ projectId: project.id, action: "add", name: name.trim(), root, baseRef: baseRef.trim() }); }}>
      <label>Name<input value={name} onChange={(event) => setName(event.currentTarget.value)} /></label>
      <label>Existing checkout<input value={root} placeholder="/absolute/path" onChange={(event) => setRoot(event.currentTarget.value)} /></label>
      <label>Base branch<input value={baseRef} placeholder="Repository’s base branch" onChange={(event) => setBaseRef(event.currentTarget.value)} /></label>
      <button type="submit" disabled={pending || onMutate === undefined || !name.trim() || !root.startsWith("/") || !baseRef.trim()}>ADD REPOSITORY</button>
    </form></details>
  </section>;
}

function RepositoryRow({ project, item, pending, onMutate }: { project: ProjectItem; item: RepositoryView; pending: boolean; onMutate?: (request: RepositoryMutation) => void }) {
  const [name, setName] = useState(item.name); const [baseRef, setBaseRef] = useState(item.base_ref);
  const request = (action: RepositoryMutation["action"]) => {
    if (action === "github" || action === "fetch") onMutate?.({ projectId: project.id, repositoryId: item.id, action });
    else if (action === "name") onMutate?.({ projectId: project.id, repositoryId: item.id, expectedRevision: item.revision, action, name });
    else if (action === "base") onMutate?.({ projectId: project.id, repositoryId: item.id, expectedRevision: item.revision, action, baseRef });
    else if (action === "enabled") onMutate?.({ projectId: project.id, repositoryId: item.id, expectedRevision: item.revision, action, enabled: !item.enabled });
    else if (action === "default" || action === "remove") onMutate?.({ projectId: project.id, repositoryId: item.id, expectedRevision: item.revision, action });
  };
  const fetchState = item.fetch_state ?? "unchecked";
  const publicationState = item.publication_state ?? "unchecked";
  const githubID = item.github_repository_id === undefined ? undefined : item.github_repository_id.toString();
  return <li className="dfConsoleSidebar__account"><p className="dfConsoleRow__title">{item.name}{item.default ? " · DEFAULT" : ""}</p><p>{item.base_ref}{item.enabled ? "" : " · Disabled"}</p>
    <div aria-label="Repository readiness"><p className="dfFactoryConsole__eyebrow">FETCH: {fetchState.toUpperCase()}</p><p className="dfFactoryConsole__eyebrow">PUBLICATION: {publicationState === "ready" ? "IDENTITY VERIFIED" : publicationState.toUpperCase()}</p>{fetchState === "setup_required" ? <p role="status">Check this checkout’s base branch and repository-local Git authentication on the host, then retry the fetch check.</p> : null}{item.readiness_message === undefined ? null : <p role="status">{item.readiness_message}</p>}
    </div>
    <details><summary>Edit</summary><p>{item.root}{githubID === undefined ? "" : ` · GitHub ID ${githubID}`}</p>
      <button type="button" disabled={pending} onClick={() => request("fetch")}>{fetchState === "ready" ? "REFRESH FETCH READINESS" : "CHECK FETCH READINESS"}</button><button type="button" disabled={pending} onClick={() => request("github")}>{publicationState === "ready" ? "REFRESH GITHUB BINDING" : "VERIFY GITHUB BINDING"}</button>
    <label>Name<input value={name} disabled={pending} onChange={(event) => setName(event.currentTarget.value)} /></label><button type="button" disabled={pending || !name.trim() || name === item.name} onClick={() => request("name")}>SAVE NAME</button>
    <label>Base<input value={baseRef} disabled={pending} onChange={(event) => setBaseRef(event.currentTarget.value)} /></label><button type="button" disabled={pending || !baseRef.trim() || baseRef === item.base_ref} onClick={() => request("base")}>SAVE BASE</button>
    <button type="button" disabled={pending || item.default} onClick={() => request("default")}>MAKE DEFAULT</button><button type="button" disabled={pending || item.default} onClick={() => request("enabled")}>{item.enabled ? "DISABLE" : "ENABLE"}</button><button type="button" disabled={pending || item.default} onClick={() => request("remove")}>REMOVE</button></details>
  </li>;
}

function nativeManageURL(value: string | undefined): string | undefined {
  if (value === undefined) return undefined;
  try {
    const url = new URL(value);
    const path = /^\/settings\/installations\/[1-9][0-9]*$/.test(url.pathname) || /^\/organizations\/[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?\/settings\/installations\/[1-9][0-9]*$/.test(url.pathname);
    return url.protocol === "https:" && url.hostname === "github.com" && url.username === "" && url.password === "" && path ? url.href : undefined;
  } catch { return undefined; }
}

function nativeInstallURL(value: string | undefined): string | undefined {
  if (value === undefined) return undefined;
  try {
    const url = new URL(value);
    const match = /^\/apps\/([A-Za-z0-9._-]{1,100})\/installations\/new$/.exec(url.pathname);
    return url.protocol === "https:" && url.hostname === "github.com" && url.port === "" && url.username === "" && url.password === "" && url.search === "" && url.hash === "" && match !== null && match[1] !== "." && match[1] !== ".." ? url.href : undefined;
  } catch { return undefined; }
}

function GitHubSection({ github, onGitHub }: { github?: FactoryGitHubView; onGitHub?: (request: GitHubConnectionBody) => void }) {
  const result = github?.result;
  const [code, setCode] = useState("");
  const [installationID, setInstallationID] = useState<number>();
  const [selected, setSelected] = useState<Record<string, GitHubDelegationBody>>({});
  const [installationSeen, setInstallationSeen] = useState(false);
  const [installationPage, setInstallationPage] = useState(1);
  const [repositoryPage, setRepositoryPage] = useState(1);
  const [loadedRepositories, setLoadedRepositories] = useState<{ installationID: number; value: NonNullable<NonNullable<FactoryGitHubView["result"]>["repositories"]> }>();
  const previousConnectionID = useRef("");
  const busy = github?.pending === true;
  const status = result?.status?.state ?? (result?.authorization !== undefined && result.state === "ok" ? "pending" : result?.state ?? "disconnected");
  const connectionID = result?.status?.connection_id ?? result?.authorization?.connection_id ?? "";
  const installations = result?.installations;
  const repositories = result?.repositories;
  const repositorySource = useRef(repositories);
  const selectedItems = Object.values(selected);
  const visibleRepositories = loadedRepositories !== undefined && loadedRepositories.installationID === installationID ? loadedRepositories.value : undefined;
  const load = (request: GitHubConnectionBody) => { if (!busy) onGitHub?.(request); };
  const connected = status === "connected";
  const canConnect = status === "disconnected" || status === "pending" || status === "awaiting_confirmation" || status === "disconnect_pending" || status === "denied" || status === "unavailable" || status === "invalid" || status === "already_connected" || status === "legacy_overseers_running" || github?.error !== undefined;
  const recoveryAction = status === "disconnected" ? connectionID === "" ? "connect" : "disconnect" : status === "pending" || status === "awaiting_confirmation" || status === "disconnect_pending" || status === "denied" || status === "legacy_overseers_running" && connectionID !== "" ? "disconnect" : status === "legacy_overseers_running" ? "connect" : "refresh";
  useEffect(() => {
    if (connectionID !== previousConnectionID.current) {
      previousConnectionID.current = connectionID;
      setInstallationID(undefined);
      setInstallationPage(1);
      setRepositoryPage(1);
      setInstallationSeen(false);
    }
    if (!connected || connectionID === "") {
      setSelected({});
      setInstallationID(undefined);
      setRepositoryPage(1);
      setInstallationSeen(false);
      return;
    }
    setSelected(Object.fromEntries((result?.status?.repositories ?? []).map((item) => [`${item.installation_id}:${item.repository_id}`, item])));
  }, [connected, connectionID, result?.status?.repositories]);
  useEffect(() => {
    if ((installations?.installations.length ?? 0) > 0) setInstallationSeen(true);
  }, [installations]);
  useEffect(() => {
    if (repositories === repositorySource.current) return;
    repositorySource.current = repositories;
    setLoadedRepositories(repositories === undefined || installationID === undefined ? undefined : { installationID, value: repositories });
  }, [repositories, installationID]);
  return <section className="dfConsoleSidebar__section" aria-label="GITHUB SETTINGS">
    <h3>GITHUB</h3>
    <p className="dfConsoleSidebar__inherit">PRIVATE OPERATOR CONNECTION · {status.toUpperCase().replaceAll("_", " ")}</p>
    {status === "legacy_overseers_running" ? <p className="dfFactoryConsole__terminalError" role="alert">STOP EXISTING LEGACY REVIEW OR CONTROLLER ACTIVITY THROUGH FACTORY CONTROLS BEFORE CONNECTING GITHUB.</p> : null}
    {status === "denied" ? <p className="dfFactoryConsole__terminalError" role="alert">GITHUB ACCESS WAS DENIED OR EXPIRED. REFRESH ACCESS.</p> : null}
    {status === "unavailable" ? <p className="dfFactoryConsole__terminalError" role="alert">GITHUB IS UNAVAILABLE. RETRY WHEN THE MAINTAINER IS REACHABLE.</p> : null}
    {github?.error === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">GITHUB SETTINGS UNAVAILABLE · {github.error.toUpperCase()}</p>}
    {canConnect ? <button type="button" disabled={busy || onGitHub === undefined} onClick={() => load({ action: recoveryAction })}>{status === "disconnected" && connectionID === "" ? "CONNECT GITHUB" : status === "disconnect_pending" ? "RETRY DISCONNECT" : status === "pending" || status === "awaiting_confirmation" || status === "denied" || status === "disconnected" ? "RESET GITHUB ACCESS" : "RETRY GITHUB ACCESS"}</button> : null}
    {result?.authorization?.authorization_url === undefined ? null : <p><a href={result.authorization.authorization_url} target="_blank" rel="noreferrer">AUTHORIZE ON GITHUB</a></p>}
    {result?.authorization !== undefined ? <form onSubmit={(event) => { event.preventDefault(); if (/^[0-9A-F]{10}$/.test(code)) load({ action: "confirm", code }); }}><label htmlFor="df-github-code">CALLBACK CODE</label><input id="df-github-code" value={code} maxLength={10} inputMode="text" onChange={(event) => setCode(event.currentTarget.value.toUpperCase())} /><button type="submit" disabled={busy || code.length !== 10}>CONFIRM</button></form> : null}
    {connected ? <div className="dfConsoleSidebar__taskActions"><button type="button" disabled={busy} onClick={() => { setInstallationPage(1); setInstallationSeen(false); load({ action: "refresh" }); }}>REFRESH ACCESS</button><button type="button" disabled={busy} onClick={() => load({ action: "disconnect" })}>DISCONNECT</button></div> : null}
    {!connected ? null : <>
      <h4>INSTALLATIONS</h4>
      {installations === undefined ? <p className="dfFactoryConsole__empty">REFRESH TO LIST INSTALLATIONS</p> : installations.installations.length === 0 ? <><p className="dfFactoryConsole__empty">NO INSTALLATIONS AVAILABLE · AN ORGANIZATION OWNER MAY NEED TO APPROVE THE DARK FACTORY APP IN GITHUB SETTINGS.</p>{!installationSeen && installations.next_page === undefined && nativeInstallURL(installations.installation_url) !== undefined ? <p><a href={nativeInstallURL(installations.installation_url)} target="_blank" rel="noreferrer">INSTALL OR REQUEST GITHUB APP ACCESS</a></p> : null}</> : <ul className="dfFactoryConsole__list">{installations.installations.map((item) => { const manage = nativeManageURL(item.html_url); return <li key={item.id} className="dfConsoleSidebar__account"><p className="dfConsoleRow__title">{item.account.login} · {item.eligibility || "available"}</p>{item.suspended_at !== null ? <p className="dfConsoleSidebar__inherit">SUSPENDED · ASK GITHUB TO RESTORE THIS INSTALLATION.</p> : null}<div className="dfConsoleSidebar__taskActions"><button type="button" disabled={busy || item.suspended_at !== null} onClick={() => { setInstallationID(item.id); setLoadedRepositories(undefined); setRepositoryPage(1); load({ action: "repositories", installation_id: item.id, page: 1 }); }}>CHOOSE REPOSITORIES</button>{manage === undefined ? null : <a href={manage} target="_blank" rel="noreferrer">MANAGE ON GITHUB</a>}</div></li>; })}</ul>}
      <div className="dfConsoleSidebar__taskActions">{installationPage > 1 ? <button type="button" disabled={busy} onClick={() => { const page = installationPage - 1; setInstallationPage(page); load({ action: "installations", page }); }}>PREVIOUS INSTALLATIONS</button> : null}{installations?.next_page === undefined ? null : <button type="button" disabled={busy} onClick={() => { const page = installations.next_page!; setInstallationPage(page); load({ action: "installations", page }); }}>MORE INSTALLATIONS</button>}</div>
      {installationID === undefined || visibleRepositories === undefined ? null : <><h4>REPOSITORIES · PAGE {repositoryPage}</h4>{visibleRepositories.repositories.length === 0 ? <p className="dfFactoryConsole__empty">NO REPOSITORIES AVAILABLE</p> : <ul className="dfConsoleSidebar__list">{visibleRepositories.repositories.map((item) => { const key = `${installationID}:${item.id}`; const chosen = selected[key]; return <li key={item.id}><label><input type="checkbox" checked={chosen !== undefined} disabled={busy || selectedItems.length >= 100 && chosen === undefined} onChange={(event) => setSelected((current) => { const next = { ...current }; if (event.currentTarget.checked) next[key] = { installation_id: installationID, repository_id: item.id, repository: item.full_name }; else delete next[key]; return next; })} /> {item.full_name}{item.permissions.push ? "" : " · READ ONLY"}</label></li>; })}</ul>}<div className="dfConsoleSidebar__taskActions"><button type="button" disabled={busy} onClick={() => load({ action: "delegate", repositories: selectedItems })}>DELEGATE SELECTED ({selectedItems.length}/100)</button>{visibleRepositories.next_page === undefined ? null : <button type="button" disabled={busy} onClick={() => { const page = visibleRepositories.next_page!; setRepositoryPage(page); load({ action: "repositories", installation_id: installationID, page }); }}>MORE REPOSITORIES</button>}</div></>}
    </>}
  </section>;
}

function FloorAppearanceSection({ appearance, onChange, onReset }: {
  appearance: FloorAppearance;
  onChange: (appearance: FloorAppearance) => void;
  onReset: () => void;
}) {
  return <section className="dfConsoleSidebar__section" aria-label="FLOOR APPEARANCE">
    <h3>Floor appearance</h3>
    <p>Saved in this browser. Does not change how the factory runs.</p>
    <label>Scenery<select value={appearance.scenery} onChange={(event) => onChange({ ...appearance, scenery: event.currentTarget.value as FloorAppearance["scenery"] })}><option value="off">Off</option><option value="subtle">Subtle</option><option value="rich">Rich</option></select></label>
    <label>Animation<select value={appearance.animation} onChange={(event) => onChange({ ...appearance, animation: event.currentTarget.value as FloorAppearance["animation"] })}><option value="follow-device">Follow device</option><option value="off">Off</option></select></label>
    <button type="button" onClick={onReset}>Reset floor appearance</button>
  </section>;
}

function ProjectLimitsSection({ state, edit, ready, onSave }: {
  state: StateView | undefined;
  edit?: FactoryEditView;
  ready: boolean;
  onSave?: (project: { id: string; revision: bigint }, limits: { runBudget: bigint; maxRunSeconds: number }) => void;
}) {
  const projects = state === undefined ? [] : [...state.projects.values()];
  return <div className="dfConsoleSidebar__section" aria-label="Project limits">
    <h3>Project limits</h3>
    {projects.length === 0 ? <p className="dfFactoryConsole__empty">No projects</p> : projects.map((project) => <ProjectLimitsForm key={`${project.id}:${project.revision}:${edit?.target === project.id && edit.error !== undefined ? "refused" : ""}`} project={project} edit={edit} ready={ready} onSave={onSave} />)}
  </div>;
}

function ProjectLimitsForm({ project, edit, ready, onSave }: {
  project: ProjectItem;
  edit?: FactoryEditView;
  ready: boolean;
  onSave?: (project: { id: string; revision: bigint }, limits: { runBudget: bigint; maxRunSeconds: number }) => void;
}) {
  const remaining = project.run_budget_limit === 0n ? 0n : project.run_budget_limit > project.runs_used ? project.run_budget_limit - project.runs_used : 0n;
  const [unlimited, setUnlimited] = useState(project.run_budget_limit === 0n);
  const [runs, setRuns] = useState(project.run_budget_limit === 0n ? "" : remaining.toString());
  const [seconds, setSeconds] = useState(String(project.max_run_seconds));
  const [localError, setLocalError] = useState<string>();
  const pending = edit?.pending === true;
  const refused = edit?.target === project.id && edit.error !== undefined;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (unlimited) {
      const duration = Number(seconds);
      if (!/^\d+$/.test(seconds) || !Number.isSafeInteger(duration) || duration > 86400) { setLocalError("DURATION MUST BE 0–86400 SECONDS"); return; }
      setLocalError(undefined);
      onSave?.(project, { runBudget: 0n, maxRunSeconds: duration });
      return;
    }
    let allowance: bigint;
    try { allowance = BigInt(runs); } catch { setLocalError("ENTER A WHOLE NUMBER OF FUTURE RUNS"); return; }
    const duration = Number(seconds);
    if (allowance < 1n) { setLocalError("FINITE ALLOWANCE MUST BE AT LEAST 1 FUTURE RUN"); return; }
    if (!/^\d+$/.test(seconds) || !Number.isSafeInteger(duration) || duration > 86400) { setLocalError("DURATION MUST BE 0–86400 SECONDS"); return; }
    setLocalError(undefined);
    onSave?.(project, { runBudget: allowance, maxRunSeconds: duration });
  };
  const error = localError ?? (refused ? editErrorCopy(edit) : undefined);
  return <form className="dfConsoleSidebar__config" onSubmit={submit} aria-label={`Limits for ${project.name}`}>
    <h4>{project.name}</h4>
    <p className="dfConsoleSidebar__inherit">{project.runs_used.toString()} RUNS USED · {project.run_budget_limit === 0n ? "UNLIMITED" : `${remaining.toString()} FUTURE RUNS LEFT`}</p>
    <label><input type="checkbox" checked={unlimited} disabled={pending || !ready} onChange={(event) => { setUnlimited(event.currentTarget.checked); setLocalError(undefined); }} /> UNLIMITED RUNS</label>
    <label htmlFor={`df-project-runs-${project.id}`}>REMAINING RUN ALLOWANCE</label>
    <input id={`df-project-runs-${project.id}`} inputMode="numeric" value={runs} disabled={pending || !ready || unlimited} onChange={(event) => { setRuns(event.currentTarget.value); setLocalError(undefined); }} />
    <label htmlFor={`df-project-seconds-${project.id}`}>MAX SECONDS PER RUN (0 = UNLIMITED)</label>
    <input id={`df-project-seconds-${project.id}`} inputMode="numeric" value={seconds} disabled={pending || !ready} onChange={(event) => { setSeconds(event.currentTarget.value); setLocalError(undefined); }} />
    {error === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">{error}</p>}
    {remaining === 0n && project.run_budget_limit !== 0n && !unlimited ? <p className="dfConsoleSidebar__inherit">THIS ALLOWANCE IS EXHAUSTED · ENTER A POSITIVE RENEWAL OR CHECK UNLIMITED</p> : null}
    <button type="submit" disabled={pending || !ready || onSave === undefined}>{pending ? "SAVING" : "SAVE"}</button>
  </form>;
}

/**
 * The provider logins on this machine. Linking registers one that already
 * exists; signing a CLI in is that CLI's own job, so there is no button for it.
 */
function AccountsSection({
  state,
  accounts,
  pending,
  error,
  onLink,
  onUpdate,
  onRefresh,
}: {
  state: StateView | undefined;
  accounts?: readonly DiscoveredAccount[];
  pending: boolean;
  error?: string;
  onLink?: (login: DiscoveredAccount, label: string) => void;
  onUpdate?: (account: AccountItem, change: { label?: string; remove?: boolean }) => void;
  onRefresh?: () => void;
}) {
  const [labels, setLabels] = useState<Record<string, string>>({});
  const linked = state === undefined ? [] : [...state.accounts.values()];
  const unlinked = (accounts ?? []).filter((login) => login.linked_id === "");
  return (
    <div className="dfConsoleSidebar__section" aria-label="ACCOUNTS">
      <h3>PROVIDER LOGINS</h3>
      <p className="dfConsoleSidebar__inherit">{accounts !== undefined && linked.length === 0 && unlinked.length === 0 ? "Sign in with your provider CLI on this Mac, then refresh." : "Link a login already available on this Mac."}</p>
      <button type="button" disabled={pending || onRefresh === undefined} onClick={onRefresh}>{pending ? "REFRESHING" : "REFRESH ACCOUNTS"}</button>
      {error === undefined ? null : <p className="dfFactoryConsole__terminalError" role="alert">{EDIT_ERRORS.get(error) ?? "THE FACTORY REFUSED THIS"}</p>}
      {linked.length === 0 ? <p className="dfFactoryConsole__empty">NO ACCOUNTS LINKED</p> : (
        <ul className="dfFactoryConsole__list">
          {linked.map((account) => {
            const login = (accounts ?? []).find((candidate) => candidate.linked_id === account.id);
            const identity = [login?.email, login?.organization].filter((part) => part !== undefined && part !== "").join(" · ");
            return (
              <li key={`${account.id}:${account.revision}`} className="dfConsoleSidebar__account">
                <p className="dfConsoleRow__title">{account.label} · {account.provider}</p>
                <p>{identity === "" ? `${account.provider} login linked` : identity}</p>
                {!login?.unavailable_reason ? null : <p className="dfConsoleSidebar__inherit">ACCOUNT UNAVAILABLE · {login.unavailable_reason}. Sign in again using <code>{account.home}</code>, then refresh.</p>}
                <div className="dfConsoleSidebar__taskActions">
                  <label className="dfFactoryConsole__visuallyHidden" htmlFor={`df-linked-account-${account.id}`}>Label for {account.label}</label>
                  <input id={`df-linked-account-${account.id}`} value={labels[`${account.id}:${account.revision}`] ?? account.label} disabled={pending || onUpdate === undefined} onChange={(event) => { const value = event.currentTarget.value; setLabels((current) => ({ ...current, [`${account.id}:${account.revision}`]: value })); }} />
                  <button type="button" disabled={pending || onUpdate === undefined || !(labels[`${account.id}:${account.revision}`] ?? account.label).trim() || (labels[`${account.id}:${account.revision}`] ?? account.label) === account.label} onClick={() => onUpdate?.(account, { label: labels[`${account.id}:${account.revision}`] ?? account.label })}>SAVE LABEL</button>
                  <button type="button" disabled={pending || onUpdate === undefined || [...(state?.agents.values() ?? [])].some((agent) => agent.account_id === account.id)} onClick={() => onUpdate?.(account, { remove: true })}>UNLINK</button>
                </div>
                {[...(state?.agents.values() ?? [])].some((agent) => agent.account_id === account.id) ? <p className="dfConsoleSidebar__inherit">In use. Reassign its agents before unlinking.</p> : null}
              </li>
            );
          })}
        </ul>
      )}
      <p className="dfConsoleSidebar__inherit">Unlinking keeps the provider login and credentials.</p>
      <h3>AVAILABLE LOGINS</h3>
      {accounts === undefined ? <p className="dfFactoryConsole__empty">{pending ? "LOOKING" : "NOT LOOKED YET"}</p>
        : unlinked.length === 0 ? <p className="dfFactoryConsole__empty">NO UNLINKED LOGINS FOUND</p> : (
        <ul className="dfFactoryConsole__list">
          {unlinked.map((login, index) => (
            <li key={login.home} className="dfConsoleSidebar__account">
              <p className="dfConsoleRow__title">{login.provider} login</p>
              <p className="dfFactoryConsole__eyebrow">{[login.email, login.organization].filter((part) => part !== "").join(" · ") || "Ready to link"}</p>
              <div className="dfConsoleSidebar__taskActions">
                <label className="dfFactoryConsole__visuallyHidden" htmlFor={`df-account-label-${login.provider}-${index}`}>Label for {login.provider} login</label>
                <input
                  id={`df-account-label-${login.provider}-${index}`}
                  value={labels[login.home] ?? login.label}
                  disabled={pending || onLink === undefined}
                  onChange={(event) => { const value = event.currentTarget.value; setLabels((current) => ({ ...current, [login.home]: value })); }}
                />
                <button
                  type="button"
                  disabled={pending || onLink === undefined || (labels[login.home] ?? login.label).trim() === ""}
                  onClick={() => onLink?.(login, labels[login.home] ?? login.label)}
                >
                  LINK
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Private decision and controls inside the selected Needs You row. */
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
      {selected.phase === "loading" ? <p className="dfFactoryConsole__empty">LOADING THE QUESTION…</p> : (
        <>
          <h3>DECISION NEEDED</h3>
          <p className="dfFactoryConsole__question">{selected.question}</p>
          <AnswerControls surface="factory" options={selected.options} canReply={selected.canReply} reply={selected.reply} replyMaxBytes={selected.replyMaxBytes} busy={busy} onReplyChange={onReplyChange} onReply={onReply} submitLabel="ANSWER" submittingLabel="ANSWERING…" />
          {selected.canReply ? null : <p className="dfFactoryConsole__empty">{selected.request.status === "open" ? "THIS OPEN DECISION IS READ-ONLY IN THIS VIEW." : `THIS DECISION IS ${selected.request.status.replaceAll("_", " ").toUpperCase()}.`}</p>}
          <div className="dfFactoryConsole__humanActions">
            {selected.canCancel ? <button type="button" disabled={busy || onCancel === undefined} onClick={onCancel}>STOP TASK</button> : null}
            {onOpenTerminal === undefined ? null : <button type="button" disabled={busy || !terminalReady} onClick={() => onOpenTerminal(selected.request)}>Open terminal</button>}
          </div>
        </>
      )}
    </article>
  );
}

function IntakeSection({ state, repositories, intake, pending, errors, onLoad, onLoadRepositories, onAction }: { state: StateView | undefined; repositories?: ReadonlyMap<string, readonly RepositoryView[]>; intake?: ReadonlyMap<string, IntakeView>; pending?: ReadonlySet<string>; errors?: ReadonlyMap<string, string>; onLoad?: (projectId: string) => void; onLoadRepositories?: (projectId: string) => void; onAction?: (projectId: string, request: IntakeBody) => void }) {
  const projects = state === undefined ? [] : [...state.projects.values()];
  const linkedTask = (id: string) => <span title={`Task ${id}`}>{state?.tasks.get(id)?.title ?? `Task ${id.slice(0, 8)}`}</span>;
  const [candidateSources, setCandidateSources] = useState<Record<string, string>>({});
  const load = useRef(onLoad);
  load.current = onLoad;
  const [open, setOpen] = useState(false);
  const [selection, setSelection] = useState("");
  const projectId = projects.find((project) => project.id === selection)?.id ?? projects[0]?.id;
  const canLoad = onLoad !== undefined;
  useEffect(() => { if (open && canLoad && projectId) load.current?.(projectId); }, [open, projectId, canLoad]);
  const loadRepositories = useRef(onLoadRepositories);
  loadRepositories.current = onLoadRepositories;
  const canLoadRepositories = onLoadRepositories !== undefined;
  useEffect(() => { if (open && canLoadRepositories && projectId) loadRepositories.current?.(projectId); }, [open, projectId, canLoadRepositories]);
  return <details className="dfConsoleSidebar__section" aria-label="Issue intake" onToggle={(event) => { if (event.target === event.currentTarget) setOpen(event.currentTarget.open); }}><summary>Issue inbox</summary><p className="dfConsoleSidebar__inherit">Review issues before they become work. Preview reads GitHub without starting an agent.</p>{projects.length > 1 ? <label>Project<select value={projectId} onChange={(event) => setSelection(event.currentTarget.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label> : null}{projects.filter((project) => project.id === projectId).map((project) => {
    const result = intake?.get(project.id);
    const sources = result?.sources ?? [];
    const candidateSource = sources.find((source) => source.id === candidateSources[project.id]);
    const busy = pending?.has(project.id) ?? false;
    return <section key={project.id}><h3>{project.name}</h3>
      {errors?.get(project.id) === undefined ? null : <p role="alert">{errors.get(project.id)}</p>}
      <button type="button" disabled={busy} onClick={() => onLoad?.(project.id)}>REFRESH SOURCES</button>
      {result === undefined ? <p>{busy ? "LOADING PRIVATE SOURCES…" : "PRIVATE SOURCES UNAVAILABLE"}</p> : sources.length === 0 ? <p>NO ISSUE SOURCES</p> : null}
      {result?.state === "withdrawal_pending" ? <p role="status">WITHDRAWAL PENDING{result.task_id === undefined ? "" : <> · {linkedTask(result.task_id)}</>}</p> : result !== undefined && result.state !== "ok" ? <p role="status">{result.state.replaceAll("_", " ").toUpperCase()}{result.task_id === undefined ? "" : <> · {linkedTask(result.task_id)}</>}</p> : null}
      {result?.imported_tasks === undefined || result.imported_tasks.length === 0 ? null : <p role="status">IMPORTED TASKS · {result.imported_tasks.length}</p>}
      <IntakeForm project={project} state={state} repositories={repositories?.get(project.id)} onAction={onAction}/>
      {sources.map((source) => {
        const reviewed = candidateSources[project.id] === source.id ? result?.reviewed_revision : undefined;
        const canEnable = reviewed === source.revision;
        return <div key={source.id}><p>{source.repository} · {source.enabled ? "ENABLED" : "PAUSED"}</p><p>Work goes to {repositories?.get(project.id)?.find((item) => item.id === source.target_repository_id)?.name ?? "the configured repository"}.</p>
          {source.sync === undefined ? <p role="status">Waiting for the first check. Check <code>factoryctl service status --home /path/to/factory-home</code> on the host.</p> : <p role="status">LAST SUCCESS · {source.sync.last_success_at === 0n ? "none yet" : new Date(Number(source.sync.last_success_at) * 1000).toLocaleString()} · {source.sync.imported_tasks} IMPORTED · {source.sync.state.toUpperCase()}</p>}
          {source.sync?.error === "" || source.sync?.error === undefined ? null : <p role="alert">SYNC ERROR · {source.sync.error} · CHECK <code>factoryctl service status --home /path/to/factory-home</code> ON THE HOST.</p>}
          {source.enabled ? <button type="button" disabled={busy} onClick={() => onAction?.(project.id, { action: "pause", source_id: source.id, expected_revision: source.revision })}>PAUSE</button> : <button type="button" disabled={busy || !canEnable} onClick={() => onAction?.(project.id, { action: "enable", source_id: source.id, expected_revision: source.revision, reviewed_revision: reviewed! })}>ENABLE AFTER PREVIEW</button>}
          {!source.enabled && !canEnable ? <p>Preview this source before enabling. Pausing stops new imports; existing work continues.</p> : null}
          <button type="button" disabled={busy} onClick={() => { setCandidateSources((current) => ({ ...current, [project.id]: source.id })); onAction?.(project.id, { action: "preview", source_id: source.id, page: 1 }); }}>PREVIEW</button>
          <IntakeForm key={`${source.id}:${source.revision}`} project={project} state={state} repositories={repositories?.get(project.id)} source={source} onAction={onAction}/>
        </div>;
      })}
      {candidateSource === undefined || result?.next_page === undefined ? null : <button type="button" disabled={busy} onClick={() => onAction?.(project.id, { action: "preview", source_id: candidateSource.id, page: result.next_page! })}>NEXT ISSUE PAGE</button>}
      {result?.candidates?.map((candidate) => <article key={`${candidate.number}:${candidate.content_hash}`}><p><a href={candidate.url} target="_blank" rel="noreferrer">{candidate.title}</a> · {candidate.reason.replaceAll("_", " ")}{candidate.task_id === undefined ? "" : <> · {linkedTask(candidate.task_id)}</>}</p><details><summary>Review issue content</summary><p style={{ whiteSpace: "pre-wrap" }}>{candidate.body}</p><p>Accepting uses this exact title and body. Later edits need your approval again.</p>{candidate.truncated ? <p>CONTENT TOO LARGE TO ACCEPT</p> : candidate.acceptance_id !== undefined && candidate.reason !== "content_changed" ? null : candidateSource === undefined ? <p>PREVIEW A SOURCE BEFORE ACCEPTING.</p> : result?.reviewed_revision !== candidateSource.revision ? <p>PREVIEW THIS SOURCE AGAIN BEFORE ACCEPTING.</p> : <button type="button" disabled={busy} onClick={() => onAction?.(project.id, { action: "accept", source_id: candidateSource.id, expected_revision: candidateSource.revision, issue_number: candidate.number, content_hash: candidate.content_hash })}>ACCEPT REVIEWED CONTENT</button>}{candidate.acceptance_id === undefined || candidate.reason === "withdrawn" || candidate.reason === "withdrawal_pending" ? null : <button type="button" disabled={busy} onClick={() => onAction?.(project.id, { action: "withdraw", acceptance_id: candidate.acceptance_id })}>WITHDRAW</button>}</details></article>)}
    </section>;
  })}</details>;
}

function IntakeForm({ project, state, repositories, source, onAction }: { project: ProjectItem; state: StateView | undefined; repositories?: readonly RepositoryView[]; source?: import("@dark-factory/client").IntakeSource; onAction?: (projectId: string, request: IntakeBody) => void }) {
  const [repository, setRepository] = useState(source?.repository ?? ""); const [label, setLabel] = useState(source?.label ?? ""); const [target, setTarget] = useState(source?.target_repository_id ?? ""); const [agent, setAgent] = useState(source?.overseer_agent_id ?? ""); const [policy, setPolicy] = useState(source?.policy ?? "manual"); const [authors, setAuthors] = useState(source?.trusted_authors.join(", ") ?? ""); const [poll, setPoll] = useState(String(source?.poll_seconds ?? 60)); const [limit, setLimit] = useState(String(source?.admission_limit ?? 25));
  const agents = state === undefined ? [] : [...state.agents.values()].filter((item) => item.project_id === project.id && item.role === "orchestrator");
  const soleAgent = agents.length === 1 ? agents[0]?.id : undefined;
  useEffect(() => { if (source === undefined && agent === "" && soleAgent) setAgent(soleAgent); }, [source, agent, soleAgent]);
  const targets = repositories?.filter((item) => item.enabled) ?? [];
  const targetIds = targets.map((item) => item.id).join(" ");
  useEffect(() => { if (source === undefined && target === "") setTarget(targets.find((item) => item.default)?.id ?? (targets.length === 1 ? targets.at(0)?.id ?? "" : "")); }, [source, target, targetIds]);
  const submit = (event: FormEvent) => { event.preventDefault(); const pollSeconds = Number(poll), admissionLimit = Number(limit); if (!/^\d+$/.test(poll) || !/^\d+$/.test(limit) || !repository.trim() || !target || !agent || policy === "trusted_authors" && authors.trim() === "") return; const configuration = { ...(source?.priority_default === undefined ? {} : { priority_default: source.priority_default }), ...(source?.priority_by_label === undefined ? {} : { priority_by_label: source.priority_by_label }), repository: repository.trim(), target_repository_id: target, overseer_agent_id: agent, label: label.trim(), policy, trusted_authors: authors.split(",").map((value) => value.trim()).filter(Boolean), poll_seconds: pollSeconds, admission_limit: admissionLimit } as const; onAction?.(project.id, source === undefined ? { action: "create", project_id: project.id, configuration } : { action: "update", source_id: source.id, project_id: project.id, expected_revision: source.revision, configuration }); };
  return <details><summary>{source === undefined ? "Add issue source" : "Source settings"}</summary><form onSubmit={submit}><label>GitHub repository<input value={repository} placeholder="owner/repository" onChange={(event) => setRepository(event.currentTarget.value)}/></label><label>Destination repository<select value={target} onChange={(event) => setTarget(event.currentTarget.value)}><option value="">{targets.length === 0 ? "No enabled checkout" : "Choose a checkout"}</option>{targets.map((item) => <option key={item.id} value={item.id}>{item.name}{item.default ? " (default)" : ""}</option>)}</select></label>{targets.length === 0 ? <p role="status">ADD AN ENABLED CHECKOUT IN REPOSITORIES BEFORE SAVING THIS SOURCE.</p> : null}<label>Label filter (optional)<input value={label} onChange={(event) => setLabel(event.currentTarget.value)}/></label><label>Overseer<select value={agent} onChange={(event) => setAgent(event.currentTarget.value)}><option value="">Choose an overseer</option>{agents.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>{agents.length === 0 ? <p role="status">CREATE AN ORCHESTRATOR FOR THIS PROJECT BEFORE SAVING THIS SOURCE.</p> : null}<label>Which issues can become work?<select value={policy} onChange={(event) => { setPolicy(event.currentTarget.value as "manual" | "trusted_authors"); if (event.currentTarget.value === "trusted_authors" && authors === "") setAuthors("@me"); }}><option value="manual">Require my approval</option><option value="trusted_authors">Allow trusted authors</option></select></label>{policy === "trusted_authors" ? <><label>Trusted authors<input value={authors} onChange={(event) => setAuthors(event.currentTarget.value)} placeholder="@me, login"/></label><p>@me means your connected GitHub account. Other authors wait for your approval. Edited issue content always needs approval again.</p></> : null}<details><summary>Advanced</summary><label>Check every (seconds)<input value={poll} onChange={(event) => setPoll(event.currentTarget.value)}/></label><label>Maximum imports per check<input value={limit} onChange={(event) => setLimit(event.currentTarget.value)}/></label></details><button type="submit" disabled={!target || !agent || agents.length === 0}>{source === undefined ? "CREATE PAUSED SOURCE" : "SAVE PAUSED SOURCE"}</button></form></details>;
}
