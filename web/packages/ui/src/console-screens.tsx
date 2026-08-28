import type { ReactNode } from "react";
import type { AgentItem, StateView, TaskItem } from "@dark-factory/client";
import {
  STAGE_SEQUENCE,
  agentActivity,
  agentCurrentTask,
  agentGlyph,
  factoryCounters,
  orderTasksForHome,
  stageMeterFill,
  stageOfTask,
  type ConsoleExtras,
  type ConsoleScreen,
  type QueueActions,
  type SuggestionItem,
  type TaskRecord,
  type TaskStage,
} from "./console-view.js";

const STAGE_GLYPHS: Record<TaskStage, string> = {
  queued: "▒",
  building: "░",
  reviewing: "░",
  merging: "░",
  done: "✓",
  failed: "×",
};

const NOT_SERVED = "not yet served by the daemon";

function shortID(value: string): string {
  return value.slice(0, 8);
}

function projectLabel(state: StateView | undefined, projectID: string): string {
  return state?.projects.get(projectID)?.name ?? `project ${shortID(projectID)}`;
}

function agentLabel(state: StateView | undefined, agentID: string): string {
  return state?.agents.get(agentID)?.name ?? `agent ${shortID(agentID)}`;
}

/** The load-bearing cross-screen status bar: agents plus factory counters. */
export function AgentStrip({ state, extras, selectedAgentId, ready, onSelectAgent, onNavigate }: {
  state: StateView | undefined;
  extras?: ConsoleExtras;
  selectedAgentId?: string;
  ready: boolean;
  onSelectAgent?: (agent: AgentItem) => void;
  onNavigate?: (screen: ConsoleScreen) => void;
}) {
  const counters = factoryCounters(state, extras);
  return (
    <nav className="dfConsoleStrip" aria-label="Agents and factory counters">
      <ul className="dfConsoleStrip__agents">
        {state === undefined || state.agents.size === 0 ? (
          <li className="dfConsoleStrip__empty">no agents</li>
        ) : (
          [...state.agents.values()].map((agent) => {
            const activity = agentActivity(agent, state);
            const task = agentCurrentTask(agent, state);
            const phase = activity === "busy" && task !== undefined ? stageOfTask(task, extras) : activity;
            const cell = (
              <>
                <span className="dfConsoleStrip__glyph" aria-hidden="true">{agentGlyph(agent, extras)}</span>
                <span className="dfConsoleStrip__agentName">{agent.name}</span>
                <span className="dfConsoleStrip__agentPhase">{activity === "needs-you" ? "! needs you" : phase}</span>
              </>
            );
            const className = `dfConsoleStrip__agent dfConsoleStrip__agent--${activity}`;
            return (
              <li key={agent.id}>
                {onSelectAgent === undefined ? (
                  <span className={className} aria-label={`${agent.name}: ${phase}`}>{cell}</span>
                ) : (
                  <button
                    type="button"
                    className={className}
                    aria-pressed={selectedAgentId === agent.id}
                    aria-label={`${agent.name}: ${phase}`}
                    disabled={!ready}
                    onClick={() => onSelectAgent(agent)}
                  >
                    {cell}
                  </button>
                )}
              </li>
            );
          })
        )}
      </ul>
      <div className="dfConsoleStrip__counters">
        <Counter glyph="▒" label={`${counters.queued} queued`} onActivate={onNavigate === undefined ? undefined : () => onNavigate({ kind: "queue" })} />
        <span className="dfConsoleStrip__counter" title={counters.awaitingDeploy === undefined ? `awaiting-deploy count ${NOT_SERVED}` : undefined}>
          <span aria-hidden="true">✓</span> {counters.awaitingDeploy === undefined ? "—" : counters.awaitingDeploy} awaiting deploy
        </span>
        <Counter
          glyph="!"
          label={`${counters.needsYou} NEEDS YOU`}
          alert={counters.needsYou > 0}
          onActivate={onNavigate === undefined ? undefined : () => onNavigate({ kind: "needs-you" })}
        />
      </div>
    </nav>
  );
}

function Counter({ glyph, label, alert, onActivate }: { glyph: string; label: string; alert?: boolean; onActivate?: () => void }) {
  const className = `dfConsoleStrip__counter${alert === true ? " dfConsoleStrip__counter--alert" : ""}`;
  if (onActivate === undefined) {
    return <span className={className}><span aria-hidden="true">{glyph}</span> {label}</span>;
  }
  return (
    <button type="button" className={className} onClick={onActivate}>
      <span aria-hidden="true">{glyph}</span> {label}
    </button>
  );
}

/** Segments fill only on store-backed stage; failed marks without filling. */
export function StageMeter({ stage }: { stage: TaskStage }) {
  const filled = stageMeterFill(stage);
  return (
    <span className="dfStageMeter" role="img" aria-label={`stage: ${stage}`}>
      {STAGE_SEQUENCE.map((name, index) => (
        <span key={name} className={`dfStageMeter__segment${index < filled ? " dfStageMeter__segment--filled" : ""}`} aria-hidden="true" />
      ))}
      <span className={`dfStageMeter__terminal${stage === "done" ? " dfStageMeter__terminal--done" : ""}${stage === "failed" ? " dfStageMeter__terminal--failed" : ""}`} aria-hidden="true">
        {stage === "done" ? "✓" : stage === "failed" ? "×" : ""}
      </span>
    </span>
  );
}

function ReviewChip({ taskId, extras }: { taskId: string; extras?: ConsoleExtras }) {
  const review = extras?.reviews?.get(taskId);
  if (review === undefined) return null;
  return (
    <span className={`dfConsoleRow__review dfConsoleRow__review--${review.outcome}`}>
      review: {review.outcome}
    </span>
  );
}

function DiffChip({ taskId, extras }: { taskId: string; extras?: ConsoleExtras }) {
  const diff = extras?.diffs?.get(taskId);
  if (diff === undefined) return null;
  return <span className="dfConsoleRow__diff">+{diff.additions} −{diff.deletions}</span>;
}

/** Home: scannable task rows. Rows are observational; a click opens the record. */
export function HomeScreen({ state, extras, ready, onOpenTask }: {
  state: StateView | undefined;
  extras?: ConsoleExtras;
  ready: boolean;
  onOpenTask?: (taskId: string) => void;
}) {
  if (state === undefined) return <p className="dfFactoryConsole__empty">waiting for the factory</p>;
  const tasks = orderTasksForHome(state, extras);
  return (
    <section className="dfFactoryConsole__section" aria-label="Tasks">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>TASKS</h2>
        <span>{tasks.length} {tasks.length === 1 ? "task" : "tasks"}</span>
      </div>
      {tasks.length === 0 ? <p className="dfFactoryConsole__empty">no tasks yet</p> : (
        <ul className="dfConsoleRows">
          {tasks.map((task) => {
            const stage = stageOfTask(task, extras);
            const tick = extras?.rowTicks?.get(task.id);
            const content = (
              <>
                <span className="dfConsoleRow__glyph" aria-hidden="true">{STAGE_GLYPHS[stage]}</span>
                <span className="dfConsoleRow__title">{task.title}</span>
                <span className="dfConsoleRow__agent">{agentLabel(state, task.assigned_agent_id)}</span>
                {tick === undefined ? null : <span className="dfConsoleRow__tick">{tick}</span>}
                <DiffChip taskId={task.id} extras={extras} />
                <ReviewChip taskId={task.id} extras={extras} />
                <StageMeter stage={stage} />
              </>
            );
            return (
              <li key={task.id}>
                {onOpenTask === undefined ? (
                  <div className="dfConsoleRow">{content}</div>
                ) : (
                  <button type="button" className="dfConsoleRow" disabled={!ready} onClick={() => onOpenTask(task.id)}>{content}</button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function UnavailableButton({ label, result }: { label: string; result: { needs: string } }) {
  return (
    <button type="button" disabled title={`needs: ${result.needs}`} aria-disabled="true">
      {label}
    </button>
  );
}

/** Queue: pending tasks plus the suggestions lane. Same three-verb budget. */
export function QueueScreen({ state, extras, actions, onOpenTask, ready }: {
  state: StateView | undefined;
  extras?: ConsoleExtras;
  actions: QueueActions;
  onOpenTask?: (taskId: string) => void;
  ready: boolean;
}) {
  const queued = state === undefined ? [] : [...state.tasks.values()].filter((task) => stageOfTask(task, extras) === "queued");
  const suggestions = extras?.suggestions;
  return (
    <>
      <section className="dfFactoryConsole__section" aria-label="Queue">
        <div className="dfFactoryConsole__sectionHeading">
          <h2>QUEUE</h2>
          <span>{queued.length} queued</span>
        </div>
        <div className="dfConsoleQueue__addWork">
          <UnavailableButton label="+ add work" result={actions.addWork()} />
        </div>
        {queued.length === 0 ? <p className="dfFactoryConsole__empty">the queue is empty</p> : (
          <ul className="dfConsoleRows">
            {queued.map((task) => (
              <li key={task.id} className="dfConsoleQueue__row">
                {onOpenTask === undefined ? (
                  <div className="dfConsoleRow">
                    <span className="dfConsoleRow__glyph" aria-hidden="true">▒</span>
                    <span className="dfConsoleRow__title">{task.title}</span>
                    <span className="dfConsoleRow__agent">priority {task.priority} · {projectLabel(state, task.project_id)}</span>
                    <StageMeter stage="queued" />
                  </div>
                ) : (
                  <button type="button" className="dfConsoleRow" disabled={!ready} onClick={() => onOpenTask(task.id)}>
                    <span className="dfConsoleRow__glyph" aria-hidden="true">▒</span>
                    <span className="dfConsoleRow__title">{task.title}</span>
                    <span className="dfConsoleRow__agent">priority {task.priority} · {projectLabel(state, task.project_id)}</span>
                    <StageMeter stage="queued" />
                  </button>
                )}
                <details className="dfConsoleQueue__menu">
                  <summary aria-label={`actions for ${task.title}`}>···</summary>
                  <div>
                    <UnavailableButton label="edit" result={actions.editTask(task.id)} />
                    <UnavailableButton label="comment" result={actions.commentTask(task.id)} />
                    <UnavailableButton label="delete" result={actions.deleteTask(task.id)} />
                  </div>
                </details>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="dfFactoryConsole__section dfConsoleSuggestions" aria-label="Suggestions">
        <div className="dfFactoryConsole__sectionHeading">
          <h2>SUGGESTIONS</h2>
          <span>{suggestions === undefined ? "—" : `${suggestions.length} proposed`}</span>
        </div>
        <p className="dfConsoleSuggestions__note">proposed work waits here until you accept it</p>
        {suggestions === undefined ? (
          <p className="dfFactoryConsole__empty">suggestions are {NOT_SERVED}</p>
        ) : suggestions.length === 0 ? (
          <p className="dfFactoryConsole__empty">no suggestions</p>
        ) : (
          <ul className="dfConsoleRows">
            {suggestions.map((suggestion: SuggestionItem) => (
              <li key={suggestion.id} className="dfConsoleSuggestions__row">
                <div className="dfConsoleSuggestions__body">
                  <span className="dfConsoleSuggestions__origin">{suggestion.origin}</span>
                  <span className="dfConsoleRow__title">{suggestion.title}</span>
                  <span className="dfConsoleSuggestions__detail">{suggestion.detail}</span>
                </div>
                <div className="dfConsoleSuggestions__actions">
                  <UnavailableButton label="accept" result={actions.acceptSuggestion(suggestion.id)} />
                  <UnavailableButton label="dismiss" result={actions.dismissSuggestion(suggestion.id)} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

/** The digestible record of one task. No terminal here; the sidebar owns it. */
export function TaskScreen({ state, extras, taskId, onBack }: {
  state: StateView | undefined;
  extras?: ConsoleExtras;
  taskId: string;
  onBack?: () => void;
}) {
  const task = state?.tasks.get(taskId);
  if (task === undefined) {
    return (
      <section className="dfFactoryConsole__section" aria-label="Task record">
        <p className="dfFactoryConsole__empty">this task is no longer in the factory state</p>
        {onBack === undefined ? null : <button type="button" onClick={onBack}>back</button>}
      </section>
    );
  }
  const stage = stageOfTask(task, extras);
  const record = extras?.records?.get(taskId);
  return (
    <section className="dfFactoryConsole__section dfConsoleRecord" aria-label="Task record">
      <div className="dfFactoryConsole__sectionHeading">
        <h2>{task.title}</h2>
        <span>{agentLabel(state, task.assigned_agent_id)} · {projectLabel(state, task.project_id)}</span>
      </div>
      <p className="dfConsoleRecord__stage">
        <StageMeter stage={stage} /> {stage}{record === undefined ? "" : ` · run ${record.runNumber}`}
      </p>
      <RecordSection title="what was asked">
        <p>{record?.asked ?? task.title}</p>
      </RecordSection>
      <RecordSection title="what happened">
        {record === undefined ? <Unserved /> : <p>{record.happened}</p>}
      </RecordSection>
      <RecordSection title="review">
        {record?.review === undefined ? <Unserved /> : (
          <>
            <p className={`dfConsoleRecord__reviewOutcome dfConsoleRecord__reviewOutcome--${record.review.outcome}`}>review: {record.review.outcome}</p>
            {record.review.findings.length === 0 ? null : (
              <ul>{record.review.findings.map((finding, index) => <li key={index}>{finding}</li>)}</ul>
            )}
          </>
        )}
      </RecordSection>
      <RecordSection title="checks">
        {record?.checks === undefined ? <Unserved /> : <ul>{record.checks.map((check, index) => <li key={index}>{check}</li>)}</ul>}
      </RecordSection>
      <RecordSection title="changes">
        {record?.files === undefined ? <Unserved /> : record.files.length === 0 ? <p>no files changed</p> : (
          record.files.map((file, index) => (
            <details key={file.path} open={index === 0} className="dfConsoleRecord__file">
              <summary>{file.path} <span className="dfConsoleRow__diff">+{file.additions} −{file.deletions}</span></summary>
              <pre>{file.patch}</pre>
            </details>
          ))
        )}
      </RecordSection>
      <RecordSection title="transcript">
        {record?.transcript === undefined ? <Unserved /> : <pre className="dfConsoleRecord__transcript">{record.transcript}</pre>}
      </RecordSection>
      {onBack === undefined ? null : <button type="button" onClick={onBack}>back</button>}
    </section>
  );
}

function RecordSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="dfConsoleRecord__part" aria-label={title}>
      <h3>{title}</h3>
      {children}
    </section>
  );
}

function Unserved() {
  return <p className="dfFactoryConsole__empty">{NOT_SERVED}</p>;
}

/** One-line ambient event stream; the full feed is a daemon gap. */
export function Ticker({ extras }: { extras?: ConsoleExtras }) {
  const entries = extras?.ticker;
  const latest = entries?.[entries.length - 1];
  return (
    <footer className="dfConsoleTicker" aria-label="Latest factory event">
      {latest === undefined ? (
        <span className="dfFactoryConsole__empty">event feed {NOT_SERVED}</span>
      ) : (
        <span><span aria-hidden="true">▸</span> {latest.at} {latest.text}</span>
      )}
    </footer>
  );
}

export type { TaskItem, TaskRecord };
