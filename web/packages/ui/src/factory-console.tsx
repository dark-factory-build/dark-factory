import type { FormEvent, ReactNode } from "react";
import type { AgentItem } from "@dark-factory/client";
import type { FactoryAgentSelection, FactoryAppSnapshot, FactoryHumanRequestView } from "./factory-app-controller.js";
import { AgentStrip, HomeScreen, QueueScreen } from "./console-screens.js";
import type { ConsoleScreen } from "./console-view.js";

export type FactoryConsoleProps = FactoryAppSnapshot & {
  screen?: ConsoleScreen;
  selectedAgent?: FactoryAgentSelection;
  onNavigate?: (screen: ConsoleScreen) => void;
  onSelectAgent?: (agent: AgentItem) => void;
  onOpenTerminalForHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onSelectHumanRequest?: (request: FactoryHumanRequestView["request"]) => void;
  onHumanReplyChange?: (reply: string) => void;
  onReplyHumanRequest?: () => void;
  onCancelHumanRequest?: () => void;
  onCloseHumanRequest?: () => void;
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

const DEFAULT_SCREEN: ConsoleScreen = { kind: "home" };

export function FactoryConsole({
  status,
  state,
  error,
  screen = DEFAULT_SCREEN,
  selectedHumanRequest,
  selectedAgent,
  onNavigate,
  onSelectAgent,
  onOpenTerminalForHumanRequest,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
  terminalContent,
}: FactoryConsoleProps) {
  const ready = status === "ready";

  return (
    <div className="dfConsoleShell">
      <main className="dfFactoryConsole" aria-label="Factory operator console">
        <header className="dfFactoryConsole__header">
          <div>
            <p className="dfFactoryConsole__eyebrow">OPERATOR VIEW</p>
            <h1>
              {screen.kind === "home" || onNavigate === undefined ? "FACTORY" : (
                <button type="button" className="dfFactoryConsole__homeLink" onClick={() => onNavigate({ kind: "home" })}>FACTORY</button>
              )}
              {screen.kind === "queue" ? " · QUEUE" : screen.kind === "needs-you" ? " · NEEDS YOU" : ""}
            </h1>
          </div>
          <div
            className={`dfFactoryConsole__connection${ready ? " dfFactoryConsole__visuallyHidden" : ""}`}
            aria-label={`Connection status: ${STATUS_LABELS[status]}`}
          >
            <p className="dfFactoryConsole__status" role="status" aria-live="polite" aria-atomic="true">
              <span className={`dfFactoryConsole__statusDot dfFactoryConsole__statusDot--${status}`} aria-hidden="true" />
              {STATUS_LABELS[status]}
            </p>
          </div>
        </header>

        <AgentStrip
          state={state}
          selectedAgentId={selectedAgent?.id}
          ready={ready}
          onSelectAgent={onSelectAgent}
          onNavigate={onNavigate}
        />

        {error === undefined ? null : (
          <p className="dfFactoryConsole__error" role="alert">
            {ERROR_LABELS.get(error.code) ?? "The connection could not continue."}
          </p>
        )}

        {screen.kind === "home" ? (
          <>
            <section className="dfFactoryConsole__section" aria-label="BUILDING">
              <div className="dfFactoryConsole__sectionHeading">
                <h2>BUILDING</h2>
                <span>{state === undefined ? "NO SNAPSHOT" : `HEAD ${state.head.toString()}`}</span>
              </div>
              {state === undefined ? (
                <p className="dfFactoryConsole__empty">BUILDING STATE UNAVAILABLE</p>
              ) : (
                <dl className="dfFactoryConsole__metrics">
                  <Metric label="DISPATCH" value={state.factory.dispatch_enabled ? "ENABLED" : "PAUSED"} />
                  <Metric label="CAPACITY" value={String(state.factory.capacity)} />
                  <Metric label="ACTIVE RUNS" value={String(state.factory.active_runs)} />
                  <Metric label="REVISION" value={state.factory.revision.toString()} />
                </dl>
              )}
            </section>
            <HomeScreen state={state} />
          </>
        ) : screen.kind === "queue" ? (
          <QueueScreen state={state} />
        ) : (
          <NeedsYouScreen
            state={state}
            status={status}
            selectedHumanRequest={selectedHumanRequest}
            onSelectHumanRequest={onSelectHumanRequest}
            onHumanReplyChange={onHumanReplyChange}
            onReplyHumanRequest={onReplyHumanRequest}
            onCancelHumanRequest={onCancelHumanRequest}
            onCloseHumanRequest={onCloseHumanRequest}
            onOpenTerminalForHumanRequest={onOpenTerminalForHumanRequest}
          />
        )}
      </main>
      {terminalContent}
    </div>
  );
}

function NeedsYouScreen({
  state,
  status,
  selectedHumanRequest,
  onSelectHumanRequest,
  onHumanReplyChange,
  onReplyHumanRequest,
  onCancelHumanRequest,
  onCloseHumanRequest,
  onOpenTerminalForHumanRequest,
}: Pick<FactoryConsoleProps,
  "state" | "status" | "selectedHumanRequest" | "onSelectHumanRequest" | "onHumanReplyChange" |
  "onReplyHumanRequest" | "onCancelHumanRequest" | "onCloseHumanRequest" | "onOpenTerminalForHumanRequest"
>) {
  const projects = state?.projects;
  return (
    <CollectionSection title="NEEDS YOU" count={state?.humanRequests.size}>
      {state === undefined ? <EmptyItem label="WAITING FOR SNAPSHOT" /> : state.humanRequests.size === 0 ? <EmptyItem label="all quiet — nothing needs you" /> : (
        <ul className="dfFactoryConsole__list dfFactoryConsole__list--requests">
          {[...state.humanRequests.values()].map((request) => {
            const selected = selectedHumanRequest?.request.id === request.id;
            const statusCopy = humanRequestStatusCopy(request.status);
            return (
              <li className="dfFactoryConsole__card" key={request.id}>
                <div className="dfFactoryConsole__cardTitle">
                  <strong>{entityLabel(state.agents, request.agent_id, "AGENT")} asks</strong>
                  <span>{statusCopy.label}</span>
                </div>
                <p>{projectLabel(projects, request.project_id)} · TASK {shortID(request.task_id)}</p>
                <small>{statusCopy.description}</small>
                {onSelectHumanRequest === undefined ? null : (
                  <button type="button" aria-pressed={selected} disabled={selected || status !== "ready"} onClick={() => onSelectHumanRequest(request)}>
                    {selected ? "OPEN" : "VIEW"}
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
          onOpenTerminal={onOpenTerminalForHumanRequest}
          terminalReady={status === "ready"}
        />
      )}
    </CollectionSection>
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
  onOpenTerminal,
  terminalReady,
}: {
  selected: FactoryHumanRequestView;
  project: string;
  agent: string;
  task: string;
  onReplyChange?: (reply: string) => void;
  onReply?: () => void;
  onCancel?: () => void;
  onClose?: () => void;
  onOpenTerminal?: (request: FactoryHumanRequestView["request"]) => void;
  terminalReady: boolean;
}) {
  const busy = selected.phase === "replying" || selected.phase === "cancelling";
  const submit = (event: FormEvent) => { event.preventDefault(); onReply?.(); };
  return (
    <article className="dfFactoryConsole__humanRequest" aria-label="Selected question" aria-live="polite">
      <div className="dfFactoryConsole__cardTitle">
        <strong>{agent} needs you</strong>
        <span>{selected.phase === "replying" ? "ANSWERING" : selected.phase.toUpperCase()}</span>
      </div>
      <p>{project} · {task}</p>
      {selected.phase === "loading" ? <p className="dfFactoryConsole__empty">LOADING THE QUESTION…</p> : (
        <>
          <p className="dfFactoryConsole__question">{selected.question}</p>
          {selected.canReply ? (
            <form className="dfFactoryConsole__reply" aria-label="Answer this question" onSubmit={submit}>
              <label htmlFor="dfHumanRequestReply">YOUR ANSWER</label>
              <textarea
                id="dfHumanRequestReply"
                value={selected.reply}
                maxLength={selected.replyMaxBytes}
                disabled={busy || onReplyChange === undefined}
                onChange={(event) => onReplyChange?.(event.currentTarget.value)}
              />
              <button type="submit" disabled={busy || onReply === undefined}>{selected.phase === "replying" ? "ANSWERING…" : "ANSWER"}</button>
            </form>
          ) : null}
          <div className="dfFactoryConsole__humanActions">
            {selected.canCancel ? <button type="button" disabled={busy || onCancel === undefined} onClick={onCancel}>Stop</button> : null}
            {onOpenTerminal === undefined ? null : <button type="button" disabled={busy || !terminalReady} onClick={() => onOpenTerminal(selected.request)}>OPEN TERMINAL</button>}
            <button type="button" disabled={busy || onClose === undefined} onClick={onClose}>CLOSE</button>
          </div>
        </>
      )}
    </article>
  );
}

function CollectionSection({ title, count, children }: { title: string; count: number | undefined; children: ReactNode }) {
  return (
    <section className="dfFactoryConsole__section" aria-label={title}>
      <div className="dfFactoryConsole__sectionHeading">
        <h2>{title}</h2>
        <span>{count ?? "—"} {count === 1 ? "ITEM" : "ITEMS"}</span>
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

function humanRequestStatusCopy(status: FactoryHumanRequestView["request"]["status"]): Readonly<{ label: string; description: string }> {
  switch (status) {
    case "open":
      return { label: "OPEN", description: "Awaiting your answer" };
    case "delivering":
      return { label: "DELIVERING", description: "Answer delivery in progress" };
    case "delivery_unknown":
      return { label: "DELIVERY UNKNOWN", description: "Answer delivery could not be confirmed" };
  }
}
