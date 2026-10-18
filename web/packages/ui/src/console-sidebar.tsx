import type { ReactNode } from "react";
import { type IntakeView, type IntakeBody, type DiscoveredAccount, type AgentItem, type GitHubConnectionBody, type GitHubDelegationBody, type HumanRequestItem, type ProjectItem, type RepositoryView, type StateView, type TaskItem } from "@dark-factory/client";
import type { FactoryEditView, FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentSprite } from "./factory-scene/factory-scene.js";
import { agentActivity, type WorkRow } from "./console-view.js";
import { productionStages } from "./production-view.js";
import { AnswerControls } from "./console-interactions.js";
import { SectionHeader, Status, formatTime } from "./console-kit.js";
import type { PublishedRelease, RuntimeBuild } from "./production-data.js";

export type TaskEdit = Readonly<{ cancel?: boolean; retry?: boolean }>;
export type TaskScope = { agent_id: string } | { project_id: string };
type TaskCursor = { beforeUpdatedAtMs?: bigint; beforeTaskId?: string };

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

/** One agent: its sprite, its name and its terminal. The floor tooltip and the terminal's own state say what it is doing. */
export function AgentPanel({
  agent,
  state,
  terminalContent,
}: {
  agent: AgentItem;
  state: StateView | undefined;
  /** The selected agent's mounted terminal and durable composer. */
  terminalContent?: ReactNode;
}) {
  return (
    <section className="dfConsoleSidebar__panel" aria-label={`Agent ${agent.name}`}>
      <div className="dfConsoleSidebar__heading">
        <AgentSprite agent={agent} activity={state === undefined ? "waiting" : agentActivity(agent, state)} />
        <h2>{agent.name}</h2>
      </div>

      <section className="dfConsoleSidebar__section dfConsoleSidebar__terminalSlot" aria-label="Terminal">
        {terminalContent ?? <p className="dfFactoryConsole__empty">Opening terminal…</p>}
      </section>
    </section>
  );
}

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

/** One list for everything open: what needs you, what is in review, running and queued. */
export function WorkPanel({
  state, rows, projectId, edit, ready, onEditTask,
  selectedTaskId, onSelectTask, selectedHumanRequest, onSelectHumanRequest, onCloseHumanRequest, requestContent, onIntakeAction, intake,
}: {
  onIntakeAction?: (projectId: string, request: IntakeBody) => void;
  /** The shown project's last intake outcome. */
  intake?: Readonly<{ state?: string; failed: boolean; busy: boolean }>;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string) => void;
  state: StateView | undefined;
  rows: readonly WorkRow[];
  projectId?: string;
  edit?: FactoryEditView;
  ready: boolean;
  onEditTask?: (task: TaskItem, change: TaskEdit) => Promise<boolean>;
  selectedHumanRequest?: FactoryHumanRequestView;
  onSelectHumanRequest?: (request: HumanRequestItem) => void;
  onCloseHumanRequest?: () => void;
  requestContent?: ReactNode;
}) {
  if (state === undefined) return <section className="dfConsoleSidebar__panel" aria-label="Work"><p className="dfFactoryConsole__empty">Waiting for the latest state…</p></section>;
  const busy = selectedHumanRequest?.phase === "replying" || selectedHumanRequest?.phase === "cancelling";
  const owner = (task?: TaskItem) => task === undefined ? "" : task.assigned_agent_id === "" ? "Any eligible worker" : state.agents.get(task.assigned_agent_id)?.name ?? "Agent";
  const chips = (row: WorkRow) => <>
    <Status stage={row.state} />{row.request === undefined || row.request.status === "open" ? null : <Status stage={row.request.status} />}
    {row.origin === undefined ? null : <span className="dfConsoleItem__meta">{row.origin}</span>}
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
    {state.factory.dispatch_enabled ? null : <p role="status">New work is paused. Queued tasks wait; active processes continue. Resume with <code>factoryctl dispatch on</code>.</p>}
    {rows.some((row) => row.intake !== undefined) && (intake?.failed || !["ok", "accepted", "imported", "withdrawn", "withdrawal_pending", undefined].includes(intake?.state)) ? <p role="alert">Could not accept: {intake?.failed ? "try again" : intake?.state?.replaceAll("_", " ")}.</p> : null}
    {rows.length === 0 ? <p className="dfFactoryConsole__empty">No open work</p> : <ul className="dfConsoleItems">{rows.map(item)}</ul>}
  </section>;
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
