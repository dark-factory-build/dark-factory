import { useEffect, useRef, useState } from "react";
import type { AgentItem, StateView, TaskItem, TaskHistoryView } from "@dark-factory/client";
import { TaskDetail, type TaskBrief } from "./console-sidebar.js";
import type { ProjectContentCall } from "./project-library.js";

type RecordValue = Record<string, unknown>;
const text = (value: unknown): string => typeof value === "string" ? value : "";
const rows = (value: unknown): RecordValue[] => Array.isArray(value) ? value as RecordValue[] : [];
type Draft = { objective: string; criteria: string; owner: string; id: string };

/** Missions use the durable outcome record; work success never accepts an objective. */
export function MissionsPanel({ state, projectId, active, call, onProject, onSelectAgent, onSelectTask, onLoadTaskDetail, onLoadTaskHistory }: {
  state?: StateView; projectId?: string; active: boolean; call?: ProjectContentCall;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>;
  onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
  onProject: (id: string) => void; onSelectAgent?: (agent: AgentItem) => void; onSelectTask?: (id: string) => void;
}) {
  const [items, setItems] = useState<RecordValue[]>([]);
  const [selected, setSelected] = useState<RecordValue>();
  const [tasks, setTasks] = useState<TaskItem[]>([]);
  const [taskNext, setTaskNext] = useState(0);
  const [inspectedTask, setInspectedTask] = useState<TaskItem>();
  const [next, setNext] = useState(0);
  const [loaded, setLoaded] = useState(false);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const generation = useRef(0);
  const project = projectId === undefined ? undefined : state?.projects.get(projectId);
  const draft = projectId === undefined ? undefined : drafts[projectId];
  const owners = [...state?.agents.values() ?? []].filter((agent) => agent.project_id === projectId && agent.role === "orchestrator" && !agent.archived);
  const document = (selected?.document ?? {}) as RecordValue;
  const anchor = tasks.find((task) => task.id === document.anchor_task_id) ?? state?.tasks.get(text(document.anchor_task_id));
  const owner = state?.agents.get(text(selected?.owner_agent_id) || anchor?.assigned_agent_id || "");

  useEffect(() => { generation.current++; setItems([]); setSelected(undefined); setTasks([]); setTaskNext(0); setInspectedTask(undefined); setLoaded(false); setNext(0); setPending(false); setNotice(""); setError(""); }, [projectId]);
  useEffect(() => () => { generation.current++; }, []);
  const run = async (work: () => Promise<void>, background = false) => {
    if (pending || call === undefined || projectId === undefined) return;
    const current = generation.current;
    setPending(true); setError(""); if (!background) setNotice("");
    try { await work(); } catch { if (current === generation.current) setError("The request could not be confirmed. Refresh to reconcile before trying again."); }
    finally { if (current === generation.current) setPending(false); }
  };
  const list = async (offset = 0) => {
    const current = generation.current;
    const value = await call!("outcome_list", { project_id: projectId, kind: "mission", offset, limit: 1 });
    if (current !== generation.current) return;
    setItems((previous) => offset === 0 ? rows(value.items) : [...previous, ...rows(value.items).filter((item) => !previous.some((old) => old.id === item.id))]);
    setNext(Number(value.next_offset ?? 0)); setLoaded(true);
  };
  const loadTasks = async (id: string, offset = 0) => {
    const current = generation.current;
    const value = await call!("mission_tasks", { project_id: projectId, id, offset, limit: 8 });
    if (current !== generation.current) return;
    const entries: TaskItem[] = rows(value.tasks).map((task) => ({ id: text(task.task_id), project_id: text(task.project_id), assigned_agent_id: text(task.assigned_agent_id), title: text(task.title), status: text(task.status) as TaskItem["status"], blocked_reason: text(task.blocked_reason) || undefined, priority: Number(task.priority), revision: BigInt(text(task.revision)) }));
    setTasks((previous) => offset === 0 ? entries : [...previous, ...entries.filter((task) => !previous.some((old) => old.id === task.id))]);
    setTaskNext(Number(value.next_offset ?? 0));
  };
  const read = async (id: string, inspect = true) => {
    const current = generation.current;
    const value = await call!("outcome_read", { project_id: projectId, id });
    if (current === generation.current) { lastObservedHead.current = state?.head; setSelected(value); if (inspect) setInspectedTask(undefined); await loadTasks(id); }
  };
  useEffect(() => { if (active && projectId !== undefined && call !== undefined && !loaded && !pending && error === "") void run(() => list()); }, [active, projectId, call, loaded, pending, error]);
  const lastObservedHead = useRef<bigint | undefined>(undefined);
  useEffect(() => {
    if (call === undefined) { lastObservedHead.current = undefined; return; }
    if (!active || selected === undefined || pending || lastObservedHead.current === state?.head) return;
    if (typeof globalThis.document !== "undefined" && globalThis.document.hidden) return;
    lastObservedHead.current = state?.head;
    void run(() => read(text(selected.id), false), true);
  }, [active, call, selected?.id, state?.head, pending]);
  const changeDraft = (change: Partial<Draft>) => { if (projectId !== undefined && draft !== undefined) setDrafts((previous) => ({ ...previous, [projectId]: { ...draft, ...change } })); };
  return <section className="dfMissions dfConsoleSidebar__panel" aria-label="Missions">
    <h2>Missions</h2>
    <p className="dfConsoleSidebar__inherit">Objectives owned by an overseer, with explicit acceptance criteria.</p>
    {project === undefined ? <label>Choose a project<select aria-label="Mission project" value="" onChange={(event) => onProject(event.target.value)}><option value="" disabled>Select project</option>{[...state?.projects.values() ?? []].map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label> : <>
      <p className="dfConsoleSidebar__inherit">{project.name}</p>
      <div className="dfConsoleViewToggle">
        <button type="button" disabled={pending || call === undefined} onClick={() => void run(async () => { await list(); if (selected !== undefined) await read(text(selected.id), false); })}>Refresh</button>
        <button type="button" disabled={pending || call === undefined || owners.length === 0} onClick={() => { if (draft === undefined) setDrafts((previous) => ({ ...previous, [project.id]: { objective: "", criteria: "", owner: owners[0]!.id, id: crypto.randomUUID().replaceAll("-", "") } })); }}>New mission</button>
      </div>
      {owners.length === 0 ? <p>Add an overseer in project settings to create a mission.</p> : null}
      {draft === undefined ? null : <form className="dfConsoleSidebar__config" onSubmit={(event) => { event.preventDefault(); void run(async () => {
        const assigned = state?.agents.get(draft.owner);
        if (assigned === undefined) throw new Error("owner unavailable");
        const current = generation.current;
        const value = await call!("mission_create", { id: draft.id, project_id: project.id, owner_agent_id: assigned.id, expected_agent_revision: Number(assigned.revision), objective: draft.objective, criteria: draft.criteria });
        if (current !== generation.current) return;
        lastObservedHead.current = state?.head; setSelected(value); setDrafts((previous) => { const next = { ...previous }; delete next[project.id]; return next; }); setNotice("Mission saved and queued for its overseer."); await list(); await loadTasks(text(value.id));
      }); }}>
        <label>Objective<textarea required maxLength={8192} value={draft.objective} disabled={pending} onChange={(event) => changeDraft({ objective: event.target.value })} /></label>
        <label>Acceptance criteria<textarea required maxLength={8192} value={draft.criteria} disabled={pending} onChange={(event) => changeDraft({ criteria: event.target.value })} /></label>
        <label>Overseer<select value={draft.owner} disabled={pending} onChange={(event) => changeDraft({ owner: event.target.value })}>{owners.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
        <button disabled={pending || call === undefined}>Create mission</button>
        <button type="button" disabled={pending} onClick={() => setDrafts((previous) => { const next = { ...previous }; delete next[project.id]; return next; })}>Discard draft</button>
      </form>}
      {loaded && items.length === 0 ? <p>No missions yet. Standalone tasks remain in Tasks.</p> : null}
      <ul className="dfMissions__list">{items.map((item) => <li key={text(item.id)}><button type="button" aria-pressed={selected?.id === item.id} disabled={pending || call === undefined} onClick={() => void run(() => read(text(item.id)))}>{text(item.objective)} <span>{text(item.state)}{item.stale ? " · stale" : ""}</span></button></li>)}</ul>
      {next === 0 ? null : <button type="button" disabled={pending || call === undefined} onClick={() => void run(() => list(next))}>More missions</button>}
      {selected === undefined ? null : <article className="dfConsoleSidebar__section">
        <h3>{text(document.objective)}</h3>
        <h4>Acceptance criteria</h4>
        <p className="dfMissions__text">{text(document.criteria)}</p>
        <p className="dfConsoleSidebar__inherit">{text(document.state)} · {owner?.name ?? (text(selected.owner_name) || "Owner unavailable")}</p>
        {selected.stale ? <p role="status">The objective changed. Earlier acceptance is stale.</p> : null}
        <h4>Next action</h4><p className="dfMissions__text">{text(document.remaining_work) || anchor?.blocked_reason || (document.state === "accepted" ? "Acceptance recorded." : "Awaiting the overseer’s next recorded action.")}</p>
        {text(document.reason) === "" ? null : <p className="dfMissions__text">{text(document.reason)}</p>}
        {text(document.conclusion) === "" ? null : <p className="dfMissions__text">{text(document.conclusion)}</p>}
        {owner === undefined ? null : <button type="button" disabled={call === undefined || onSelectAgent === undefined} onClick={() => onSelectAgent?.(owner)}>Talk to {owner.name}</button>}
        <h4>Related work</h4>
        <ul className="dfMissions__list">{tasks.map((task) => <li key={task.id}><button type="button" onClick={() => { if (state?.tasks.has(task.id) && onSelectTask !== undefined) onSelectTask(task.id); else setInspectedTask(task); }}>{task.title}<span>{task.status}{task.blocked_reason ? ` · ${task.blocked_reason}` : ""}</span></button></li>)}</ul>
        {taskNext === 0 ? null : <button type="button" disabled={pending || call === undefined} onClick={() => void run(() => loadTasks(text(selected.id), taskNext))}>More related work</button>}
        {inspectedTask === undefined ? null : <TaskDetail key={inspectedTask.id} task={tasks.find((task) => task.id === inspectedTask.id) ?? inspectedTask} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} />}
        {rows(document.links).map((link, index) => <p key={index}>{text(link.relation)}{text(link.stage) ? ` · ${text(link.stage)}` : ""}{/^https:\/\//.test(text(link.source)) ? <> · <a href={text(link.source)} target="_blank" rel="noopener noreferrer">Evidence / PR</a></> : null}</p>)}
        {(!Array.isArray(selected.missing_references) || selected.missing_references.length === 0) ? null : <p>Some evidence is unavailable.</p>}
      </article>}
    </>}
    {pending ? <p role="status">Waiting for acknowledgement…</p> : null}
    {notice === "" ? null : <p role="status">{notice}</p>}
    {error === "" ? null : <p role="alert">{error}</p>}
    {call === undefined ? <p role="status">Disconnected. Showing the last observed mission state.</p> : null}
  </section>;
}
