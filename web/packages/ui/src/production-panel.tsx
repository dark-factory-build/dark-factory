import { useEffect, useRef, useState } from "react";
import type { AgentItem, StateView, TaskHistoryView, TaskItem } from "@dark-factory/client";
import { TaskDetail, type TaskBrief } from "./console-sidebar.js";
import type { ProjectContentCall } from "./project-library.js";
import type { ProductionContraption } from "./production-view.js";

type ContentCall = (operation: string, input: Record<string, unknown>) => Promise<Record<string, unknown>>;
const text = (value: unknown) => typeof value === "string" ? value : "";
const href = (value: unknown) => {
  if (typeof value !== "string") return undefined;
  try { const url = new URL(value); return url.protocol === "https:" && url.username === "" && url.password === "" ? url.href : undefined; } catch { return undefined; }
};
const extra = (value: unknown, key: string) => value !== null && typeof value === "object" ? (value as Record<string, unknown>)[key] : undefined;
const asTask = (value: Record<string, unknown>, projectId: string): TaskItem => ({
  id: text(value.task_id) || text(value.id), project_id: text(value.project_id) || projectId, assigned_agent_id: text(value.assigned_agent_id),
  title: text(value.title) || "Untitled task", status: text(value.status) as TaskItem["status"] || "blocked", blocked_reason: text(value.blocked_reason) || undefined,
  priority: Number(value.priority) || 0, revision: BigInt(text(value.revision) || "0"), updated_at_ms: BigInt(text(value.updated_at_ms) || "0"),
});

export function ProductionPanel({ items, selected, onSelect, state, call, connected = true, error, overflow = 0, loadMore, onMission, onAgent, onLoadTaskDetail, onLoadTaskHistory }: {
  items: readonly ProductionContraption[]; selected?: string; onSelect: (key: string) => void; state?: StateView; call?: ProjectContentCall;
  connected?: boolean; error?: string; overflow?: number; loadMore?: () => void; onMission?: (projectId: string, missionId: string) => void; onAgent?: (agent: AgentItem) => void;
  onLoadTaskDetail?: (task: TaskItem, peerOffset?: bigint, expectedHead?: bigint) => Promise<TaskBrief>; onLoadTaskHistory?: (task: TaskItem) => Promise<TaskHistoryView>;
}) {
  const [localSelected, setLocalSelected] = useState<string>();
  const [selectedTaskId, setSelectedTaskId] = useState<string>();
  const [loadedTasks, setLoadedTasks] = useState<Record<string, TaskItem>>({});
  const [taskError, setTaskError] = useState("");
  const [completedLimit, setCompletedLimit] = useState(8);
  const epoch = useRef(0);
  const previousActive = useRef<string | undefined>(undefined);
  const selectedTaskRef = useRef<string | undefined>(undefined);
  const active = selected ?? localSelected;
  const item = items.find((entry) => `${entry.projectId}:${entry.visualId}` === active);
  const tasks = state?.tasks;
  useEffect(() => {
    if (previousActive.current !== active) { previousActive.current = active; epoch.current++; selectedTaskRef.current = undefined; setSelectedTaskId(undefined); setTaskError(""); }
    if (active !== undefined && !items.some((entry) => `${entry.projectId}:${entry.visualId}` === active)) setLocalSelected(undefined);
  }, [active, items]);
  useEffect(() => () => { epoch.current++; }, []);
  const inspect = (key: string) => { epoch.current++; selectedTaskRef.current = undefined; setSelectedTaskId(undefined); setTaskError(""); setLocalSelected(key); onSelect(key); };
  const taskFor = (entry: ProductionContraption, taskId: string) => tasks?.get(taskId) ?? loadedTasks[`${entry.projectId}:${taskId}`];
  const loadTask = async (entry: ProductionContraption, taskId: string) => {
    if (!connected) return;
    const known = taskFor(entry, taskId);
    if (known !== undefined) { setSelectedTaskId(taskId); return; }
    if (call === undefined) { setTaskError("Task detail is unavailable while disconnected."); return; }
    const generation = ++epoch.current;
    selectedTaskRef.current = taskId; setSelectedTaskId(taskId); setTaskError("");
    try {
      const result = await (call as unknown as ContentCall)("task_read", { project_id: entry.projectId, task_id: taskId });
      if (generation !== epoch.current || active !== `${entry.projectId}:${entry.visualId}` || selectedTaskRef.current !== taskId) return;
      setLoadedTasks((previous) => ({ ...previous, [`${entry.projectId}:${taskId}`]: asTask(result, entry.projectId) }));
    } catch { if (generation === epoch.current) setTaskError("Task detail is unavailable."); }
  };
  const project = item === undefined ? undefined : state?.projects.get(item.projectId);
  const owner = item?.tasks.map((id) => taskFor(item, id)).find((task) => task !== undefined)?.assigned_agent_id;
  const ownerAgent = owner === undefined ? undefined : state?.agents.get(owner);
  const activeItems = items.filter((entry) => !entry.completed);
  const completedItems = items.filter((entry) => entry.completed);
  const visibleItems = [...activeItems, ...completedItems.slice(-completedLimit)];
  const moreItems = completedItems.length > completedLimit || overflow > 0;
  const selectedTask = item === undefined || selectedTaskId === undefined ? undefined : taskFor(item, selectedTaskId);
  return <section className="dfConsoleSidebar__panel" aria-label="Production inspection">
    <h2>Production</h2>
    <p className="dfConsoleSidebar__inherit">Published work and external evidence, grouped by durable Change identity.</p>
    <p className="dfConsoleSidebar__inherit">This paired browser can inspect evidence; merge and CI authority remain with the linked repository and external services.</p>
    {!connected ? <p role="status">Disconnected. Showing the last observed production state.</p> : null}
    {error ? <p role="alert">{error}</p> : null}
    {overflow > 0 ? <p role="status">Showing a bounded page; {overflow} records are omitted.</p> : null}
    {items.length === 0 ? <p className="dfFactoryConsole__empty">No production records in this scope.</p> : <label>Inspect work<select value={active ?? ""} onChange={(event) => inspect(event.target.value)}>{visibleItems.map((entry) => { const key = `${entry.projectId}:${entry.visualId}`; const title = entry.pullRequest?.title || entry.construction?.title || "Work"; return <option key={key} value={key}>{title} · {entry.repository} · {entry.status}</option>; })}</select></label>}
    {moreItems ? <button type="button" disabled={!connected} onClick={() => { setCompletedLimit((limit) => limit + 8); loadMore?.(); }}>More completed work</button> : null}
    {item === undefined ? null : <article className="dfRecentWorkDetail" aria-label={`Production details for ${item.pullRequest?.title || item.construction?.title || "work"}`}>
      <h3>{item.pullRequest?.title || item.construction?.title || "Work"}</h3>
      <p>{project?.name || item.projectId} · {item.repository} · {item.status}</p>
      <p><strong>Next action:</strong> {item.nextAction}</p>
      {item.pullRequest ? <><p>PR #{item.pullRequest.number} · {item.pullRequest.state} · head <code style={{ overflowWrap: "anywhere" }}>{item.pullRequest.head}</code></p><p>{href(item.pullRequest.url) ? <><a href={item.pullRequest.url} target="_blank" rel="noreferrer">Open pull request</a> <a href={`${item.pullRequest.url}/files`} target="_blank" rel="noreferrer">Open diff</a></> : null}</p></> : null}
      {item.construction ? <><h4>Construction</h4><p>{item.construction.phase} · {item.construction.status}</p>{item.construction.blocked_reason ? <p role="alert">{item.construction.blocked_reason}</p> : null}</> : null}
      <h4>Review</h4><p>{item.review.state} · {item.review.current ? "current head" : item.review.head ? "stale head" : "no head recorded"}{item.review.sourceFresh ? "" : " · source observation stale or unavailable"}</p>{item.review.findings ? <p className="dfRecentWorkText">{item.review.findings}</p> : null}{href(item.review.url) ? <a href={item.review.url} target="_blank" rel="noreferrer">Open review</a> : null}
      {item.reviewers.length ? <ul>{item.reviewers.map((reviewer) => <li key={reviewer.id}>{reviewer.name} · {reviewer.provider} · {reviewer.state} · head <code style={{ overflowWrap: "anywhere" }}>{reviewer.head}</code>{reviewer.findings ? ` · ${reviewer.findings}` : ""}{href(reviewer.url) ? <> · <a href={reviewer.url} target="_blank" rel="noreferrer">Evidence</a></> : null}</li>)}</ul> : <p>No reviewer assignment recorded.</p>}
      <h4>Checks</h4>{item.checks.length === 0 ? <p>No check evidence recorded.</p> : <ul>{item.checks.map((check) => <li key={`${check.repository}:${check.id}`}>{check.name} · {check.scope} · {check.state} · {check.conclusion || "no conclusion"}{check.applicable ? " · current head" : " · separate revision"}{check.overflow ? ` · +${check.overflow} omitted` : ""}{text(extra(check, "shared_id")) ? ` · shared ${text(extra(check, "shared_id"))}` : ""}{extra(check, "member_count") !== undefined ? ` · ${String(extra(check, "member_count"))} members` : ""}{check.jobs.length ? <ul>{check.jobs.map((job) => <li key={job.id}>{job.name} · {job.state} · {job.conclusion}{href(job.url) ? <> · <a href={job.url} target="_blank" rel="noreferrer">Job</a></> : null}</li>)}</ul> : null}{href(check.url) ? <a href={check.url} target="_blank" rel="noreferrer">Open check</a> : null}</li>)}</ul>}
      <h4>Merge and delivery</h4><p>{item.pullRequest?.merge ? `Merged at ${item.pullRequest.merge}` : item.pullRequest?.state === "closed" ? "Closed without merge" : "Not merged"}</p>{item.deliveries.length === 0 ? <p>No delivery evidence recorded.</p> : <ul>{item.deliveries.map((delivery) => <li key={`${delivery.repository}:${delivery.id}`}>{delivery.destination} · {delivery.state} · revision <code style={{ overflowWrap: "anywhere" }}>{delivery.revision}</code>{delivery.verified ? " · verified" : " · not verified"}{text(extra(delivery, "shared_id")) ? ` · shared ${text(extra(delivery, "shared_id"))}` : ""}{extra(delivery, "member_count") !== undefined ? ` · ${String(extra(delivery, "member_count"))} members` : ""}{href(delivery.url) ? <> · <a href={delivery.url} target="_blank" rel="noreferrer">Open delivery</a></> : null}</li>)}</ul>}
      {ownerAgent ? <button type="button" disabled={!connected || onAgent === undefined} onClick={() => onAgent?.(ownerAgent)}>Talk to {ownerAgent.name}</button> : null}
      {item.missions.map((mission) => <button key={mission} type="button" disabled={!connected || onMission === undefined} onClick={() => onMission?.(item.projectId, mission)}>Open mission {mission.slice(0, 8)}</button>)}
      {item.tasks.length ? <><h4>Related tasks</h4><ul>{item.tasks.map((taskId) => { const task = taskFor(item, taskId); return <li key={taskId}><button type="button" disabled={!connected} onClick={() => void loadTask(item, taskId)}>{task?.title || taskId} · {task?.status || "inspect"}</button></li>; })}</ul>{selectedTask ? <TaskDetail task={selectedTask} onLoadTaskDetail={onLoadTaskDetail} onLoadTaskHistory={onLoadTaskHistory} /> : null}</> : null}
      {taskError ? <p role="alert">{taskError}</p> : null}
    </article>}
  </section>;
}
