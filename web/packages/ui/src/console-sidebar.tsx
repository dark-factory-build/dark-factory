import type { ReactNode } from "react";
import { type DiscoveredAccount, type AgentItem, type GitHubConnectionBody, type GitHubDelegationBody, type HumanRequestItem, type ProjectItem, type RepositoryView, type StateView, type TaskItem } from "@dark-factory/client";
import type { FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentSprite } from "./factory-scene/factory-scene.js";
import { agentActivity, type WorkRow } from "./console-view.js";
import { productionStages } from "./production-view.js";
import { AnswerControls } from "./console-interactions.js";
import { SectionHeader, Status, formatTime } from "./console-kit.js";

export type TaskScope = { agent_id: string } | { project_id: string };
type TaskCursor = { beforeUpdatedAtMs?: bigint; beforeTaskId?: string };

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

/** One Work row: title, then chips, then meta, always the same shape; a title that opens something stretches over the whole row. */
function WorkRowShell({ title, chips, meta, onOpen, href, disabled, expanded, children }: { title: ReactNode; chips: ReactNode; meta: ReactNode; onOpen?: () => void; href?: string; disabled?: boolean; expanded?: boolean; children?: ReactNode }) {
  return <li className="dfConsoleItem dfWorkRow">
    <div className="dfWorkRow__head">
      {href ? <a className="dfConsoleItem__taskTitle dfWorkRow__title" href={href} target="_blank" rel="noreferrer">{title}</a> : onOpen ? <button type="button" className="dfConsoleItem__taskTitle dfWorkRow__title" disabled={disabled} aria-expanded={expanded} onClick={onOpen}>{title}</button> : <span className="dfConsoleItem__taskTitle dfWorkRow__title">{title}</span>}
      <span className="dfWorkMeta">{chips}</span>
      {meta ? <span className="dfConsoleItem__meta">{meta}</span> : null}
    </div>
    {children}
  </li>;
}

/** One list for everything open: what needs you, what is in review, running and queued. */
export function WorkPanel({
  state, rows, projectId, ready,
  selectedHumanRequest, onSelectHumanRequest, onCloseHumanRequest, requestContent,
}: {
  state: StateView | undefined;
  rows: readonly WorkRow[];
  projectId?: string;
  ready: boolean;
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
    return <WorkRowShell key={row.key} title={row.title} chips={chips(row)} meta={meta} href={task ? undefined : row.pr?.pullRequest ? row.pr.pullRequest.url || `https://github.com/${row.pr.repository}/pull/${row.pr.pullRequest.number}` : undefined}>
      {blocked && task.blocked_reason ? <p className="dfConsoleItem__meta">{task.blocked_reason}</p> : null}
    </WorkRowShell>;
  };
  return <section className="dfConsoleSidebar__panel" aria-label="Work">
    {state.factory.dispatch_enabled ? null : <p role="status">New work is paused. Queued tasks wait; active processes continue. Resume with <code>factoryctl dispatch on</code>.</p>}
    {rows.length === 0 ? <p className="dfFactoryConsole__empty">No open work</p> : <ul className="dfConsoleItems">{rows.map(item)}</ul>}
  </section>;
}

/** The selected question: its text, the answer controls and the one stop command. */
export function HumanRequestPanel({
  selected,
  onReplyChange,
  onReply,
  onCancel,
}: {
  selected: FactoryHumanRequestView;
  onReplyChange?: (reply: string) => void;
  onReply?: () => void;
  onCancel?: () => void;
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
          </div>
        </>
      )}
    </article>
  );
}
