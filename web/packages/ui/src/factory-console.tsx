import type { FormEvent, ReactNode } from "react";
import type { FactoryAppSnapshot, FactoryHumanRequestView } from "./factory-app-controller.js";

export type FactoryConsoleProps = FactoryAppSnapshot & {
  onRetry?: () => void;
  onSelectHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onHumanReplyChange?: (reply: string) => void;
  onReplyHumanRequest?: () => void;
  onCancelHumanRequest?: () => void;
  onCloseHumanRequest?: () => void;
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
  ["pairing_uncertain", "Pairing result is uncertain. Wait before trying again."],
  ["storage_unavailable", "Browser key storage is unavailable."],
  ["crypto_unavailable", "Browser cryptography is unavailable."],
  ["malformed", "The server sent an invalid frame."],
  ["oversized", "The server frame exceeded the protocol limit."],
  ["unsupported_version", "The server protocol version is unsupported."],
  ["wrong_direction", "The server sent an invalid frame direction."],
  ["unauthorized", "This browser client is not authorized."],
  ["invalid_request", "The request was rejected."],
  ["rate_limited", "The request was rate limited."],
  ["not_found", "The requested item was not found."],
  ["stale", "The requested state is stale."],
  ["too_large", "The request was too large."],
  ["internal", "The server could not complete the request."],
]);

export function FactoryConsole({
  status,
  state,
  error,
  selectedHumanRequest,
  onRetry,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
}: FactoryConsoleProps) {
  const projects = state?.projects;
  const canRetry = onRetry !== undefined && (status === "idle" || status === "closed");

  return (
    <main className="dfFactoryConsole" aria-label="Factory operator console">
      <header className="dfFactoryConsole__header">
        <div>
          <p className="dfFactoryConsole__eyebrow">OPERATOR VIEW</p>
          <h1>FACTORY</h1>
        </div>
        <div className="dfFactoryConsole__connection" aria-label={`Connection status: ${STATUS_LABELS[status]}`}>
          <p className="dfFactoryConsole__status" role="status" aria-live="polite" aria-atomic="true">
            <span className={`dfFactoryConsole__statusDot dfFactoryConsole__statusDot--${status}`} aria-hidden="true" />
            {STATUS_LABELS[status]}
          </p>
          {canRetry ? <button type="button" onClick={onRetry}>RETRY CONNECTION</button> : null}
        </div>
      </header>

      {error === undefined ? null : (
        <p className="dfFactoryConsole__error" role="alert">
          {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
        </p>
      )}

      <section className="dfFactoryConsole__section" aria-label="BUILDING">
        <div className="dfFactoryConsole__sectionHeading">
          <h2>BUILDING</h2>
          <span>{state === undefined ? "NO SNAPSHOT" : `HEAD ${state.head.toString()}`}</span>
        </div>
        {state?.factory[0] === undefined ? (
          <p className="dfFactoryConsole__empty">BUILDING STATE UNAVAILABLE</p>
        ) : (
          <dl className="dfFactoryConsole__metrics">
            <Metric label="DISPATCH" value={state.factory[0].dispatch_enabled ? "ENABLED" : "PAUSED"} />
            <Metric label="CAPACITY" value={String(state.factory[0].capacity)} />
            <Metric label="ACTIVE RUNS" value={String(state.factory[0].active_runs)} />
            <Metric label="REVISION" value={state.factory[0].revision.toString()} />
          </dl>
        )}
      </section>

      <div className="dfFactoryConsole__columns">
        <CollectionSection title="AGENTS" count={state?.agents.size ?? 0}>
          {state === undefined ? <EmptyItem label="WAITING FOR SNAPSHOT" /> : state.agents.size === 0 ? <EmptyItem label="NO AGENTS" /> : (
            <ul className="dfFactoryConsole__list">
              {[...state.agents.values()].map((agent) => (
                <li className="dfFactoryConsole__card" key={agent.id}>
                  <div className="dfFactoryConsole__cardTitle">
                    <strong>{agent.name}</strong>
                    <span>{agent.paused ? "PAUSED" : "ACTIVE"}</span>
                  </div>
                  <p>{agent.role.toUpperCase()} · {projectLabel(projects, agent.project_id)}</p>
                  <small>ID {shortID(agent.id)}</small>
                </li>
              ))}
            </ul>
          )}
        </CollectionSection>

        <CollectionSection title="TASK QUEUE" count={state?.tasks.size ?? 0}>
          {state === undefined ? <EmptyItem label="WAITING FOR SNAPSHOT" /> : state.tasks.size === 0 ? <EmptyItem label="NO TASKS" /> : (
            <ul className="dfFactoryConsole__list">
              {[...state.tasks.values()].map((task) => (
                <li className="dfFactoryConsole__card" key={task.id}>
                  <div className="dfFactoryConsole__cardTitle">
                    <strong>{task.title}</strong>
                    <span>{task.status.toUpperCase()}</span>
                  </div>
                  <p>PRIORITY {String(task.priority)} · {projectLabel(projects, task.project_id)}</p>
                  <small>AGENT {shortID(task.assigned_agent_id)} · ID {shortID(task.id)}</small>
                </li>
              ))}
            </ul>
          )}
        </CollectionSection>
      </div>

      <CollectionSection title="NEEDS YOU" count={state?.humanRequests.size ?? 0}>
        {state === undefined ? <EmptyItem label="WAITING FOR SNAPSHOT" /> : state.humanRequests.size === 0 ? <EmptyItem label="NO OPEN REQUESTS" /> : (
          <ul className="dfFactoryConsole__list dfFactoryConsole__list--requests">
            {[...state.humanRequests.values()].map((request) => {
              const selected = selectedHumanRequest?.request.id === request.id;
              return (
                <li className="dfFactoryConsole__card" key={request.id}>
                  <div className="dfFactoryConsole__cardTitle">
                    <strong>REQUEST {shortID(request.id)}</strong>
                    <span>{request.status.toUpperCase()}</span>
                  </div>
                  <p>{projectLabel(projects, request.project_id)} · TASK {shortID(request.task_id)}</p>
                  <small>Awaiting operator response</small>
                  {onSelectHumanRequest === undefined ? null : (
                    <button type="button" aria-pressed={selected} disabled={selected || status !== "ready"} onClick={() => onSelectHumanRequest(request)}>
                      {selected ? "REQUEST OPEN" : "VIEW REQUEST"}
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {selectedHumanRequest === undefined ? null : (
          <HumanRequestPanel
            selected={selectedHumanRequest}
            project={projectLabel(projects, selectedHumanRequest.request.project_id)}
            agent={entityLabel(state?.agents, selectedHumanRequest.request.agent_id, "AGENT")}
            task={entityLabel(state?.tasks, selectedHumanRequest.request.task_id, "TASK")}
            onReplyChange={onHumanReplyChange}
            onReply={onReplyHumanRequest}
            onCancel={onCancelHumanRequest}
            onClose={onCloseHumanRequest}
          />
        )}
      </CollectionSection>
    </main>
  );
}

function HumanRequestPanel({
  selected,
  project,
  agent,
  task,
  onReplyChange,
  onReply,
  onCancel,
  onClose,
}: {
  selected: FactoryHumanRequestView;
  project: string;
  agent: string;
  task: string;
  onReplyChange?: (reply: string) => void;
  onReply?: () => void;
  onCancel?: () => void;
  onClose?: () => void;
}) {
  const busy = selected.phase === "replying" || selected.phase === "cancelling";
  const submit = (event: FormEvent) => { event.preventDefault(); onReply?.(); };
  return (
    <article className="dfFactoryConsole__humanRequest" aria-label="Selected human request" aria-live="polite">
      <div className="dfFactoryConsole__cardTitle">
        <strong>{agent} needs you</strong>
        <span>{selected.phase.toUpperCase()}</span>
      </div>
      <p>{project} · {task}</p>
      {selected.phase === "loading" ? <p className="dfFactoryConsole__empty">LOADING PRIVATE REQUEST…</p> : (
        <>
          <p className="dfFactoryConsole__question">{selected.question}</p>
          {selected.canReply ? (
            <form className="dfFactoryConsole__reply" aria-label="Reply to human request" onSubmit={submit}>
              <label htmlFor="dfHumanRequestReply">WRITE A REPLY</label>
              <textarea
                id="dfHumanRequestReply"
                value={selected.reply}
                maxLength={selected.replyMaxBytes}
                disabled={busy || onReplyChange === undefined}
                onChange={(event) => onReplyChange?.(event.currentTarget.value)}
              />
              <button type="submit" disabled={busy || onReply === undefined}>{selected.phase === "replying" ? "SENDING…" : "SEND REPLY"}</button>
            </form>
          ) : null}
          <div className="dfFactoryConsole__humanActions">
            {selected.canCancel ? <button type="button" disabled={busy || onCancel === undefined} onClick={onCancel}>CANCEL RUN</button> : null}
            <button type="button" disabled={busy || onClose === undefined} onClick={onClose}>CLOSE</button>
          </div>
        </>
      )}
    </article>
  );
}

function CollectionSection({ title, count, children }: { title: string; count: number; children: ReactNode }) {
  return (
    <section className="dfFactoryConsole__section" aria-label={title}>
      <div className="dfFactoryConsole__sectionHeading">
        <h2>{title}</h2>
        <span>{count} {count === 1 ? "ITEM" : "ITEMS"}</span>
      </div>
      {children}
    </section>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>;
}

function EmptyItem({ label }: { label: string }) {
  return <p className="dfFactoryConsole__empty">{label}</p>;
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
