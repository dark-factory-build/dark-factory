import { useEffect, useId, useRef, useState } from "react";
import type { AgentItem, StateView, TaskItem } from "@dark-factory/client";
import type { ProjectContentCall } from "./project-library.js";
import { inProgressProduction, productionStages, productionKey, type ProductionContraption, type ProductionDelivery } from "./production-view.js";

const text = (v: unknown) => typeof v === "string" ? v : "";
const href = (v: unknown) => { try { const u = new URL(text(v)); return u.protocol === "https:" && !u.username && !u.password ? u.href : undefined; } catch { return undefined; } };
const observedAt = (value: number | undefined) => {
  if (value === undefined || !Number.isFinite(value) || value <= 0) return "";
  try { return new Date(value).toISOString(); } catch { return ""; }
};

/** The receipts recorded for one pull request's own deliveries. */
function DeliveryEvidence({ deliveries, current = true }: { deliveries: readonly ProductionDelivery[]; current?: boolean }) {
  const [limit, setLimit] = useState(8);
  const remaining = deliveries.length - limit, remainingId = useId();
  return <>{deliveries.length === 0 ? <p>No delivery evidence recorded.</p> : deliveries.slice(0, limit).map((delivery) => <details key={`${delivery.repository}:${delivery.id}`}>
    <summary style={{overflowWrap:"anywhere"}}>{delivery.destination} · {current ? delivery.state : `last recorded ${delivery.state}; not current confirmation`} · {delivery.pull_requests.length} linked PR{delivery.pull_requests.length === 1 ? "" : "s"}</summary>
    <p>{delivery.phase}{delivery.reason ? ` · ${delivery.reason}` : ""}</p>
    <p>Revision <code style={{overflowWrap:"anywhere"}}>{delivery.revision || "not observed"}</code></p>
    {observedAt(delivery.updated_at) ? <p>Updated {observedAt(delivery.updated_at)}</p> : null}
    {observedAt(delivery.verified_at) ? <p>Verified {observedAt(delivery.verified_at)}</p> : <p>Delivery not verified.</p>}
    {href(delivery.url) ? <p><a href={delivery.url} target="_blank" rel="noreferrer">Open delivery</a></p> : null}
    <ul>{delivery.pull_requests.map((number) => <li key={number}><a href={`https://github.com/${delivery.repository}/pull/${number}`} target="_blank" rel="noreferrer">{delivery.repository} #{number}</a></li>)}</ul>
    {delivery.overflow ? <p>{delivery.overflow} additional links are outside this observation.</p> : null}
  </details>)}{remaining > 0 ? <><p id={remainingId}>{remaining} more delivery receipts available.</p><button type="button" aria-describedby={remainingId} onClick={() => setLimit((value) => value + 8)}>Show more</button></> : null}</>;
}

const reviewProse = (v: string) => v.replace(/(?:^|\n)<!-- dark-factory-operation:[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}:[0-9a-f]{64} -->\s*$/i, "").trim();
function ReviewNotes({ findings }: { findings: string }) { const prose = reviewProse(findings); return prose ? <p className="dfRecentWorkText">{prose}</p> : null; }
const title = (v: ProductionContraption) => v.pullRequest?.title || v.construction?.title || "Work";
const keyOf = productionKey;
const sourceStatus = ({ source }: ProductionContraption) => source.kind === "unavailable" ? "Source details unavailable" : source.stale ? "Source details out of date" : source.omitted > 0 || source.relationshipsOmitted > 0 || source.relationshipsUnavailable ? "Source details incomplete" : "";
export const asTask = (v: Record<string, unknown>, project: string): TaskItem => ({ id: text(v.task_id) || text(v.id), project_id: text(v.project_id) || project, assigned_agent_id: text(v.assigned_agent_id), title: text(v.title) || "Untitled task", status: text(v.status) as TaskItem["status"] || "blocked", blocked_reason: text(v.blocked_reason) || undefined, priority: Number(v.priority) || 0, revision: BigInt(text(v.revision) || "0"), updated_at_ms: text(v.updated_at_ms) ? BigInt(text(v.updated_at_ms)) : undefined });

export function ProductionPanel({ items, selected, onSelect, state, call, connected = true, error, overflow = 0, loadMore, loading = false, active = true, onMission, onAgent, onOpenTask }: {
  items: readonly ProductionContraption[]; selected?: string; onSelect: (key: string) => void; state?: StateView; call?: ProjectContentCall; connected?: boolean; error?: string; overflow?: number; loadMore?: () => void; loading?: boolean; active?: boolean; onMission?: (projectId: string, missionId: string) => void; onAgent?: (agent: AgentItem) => void; onOpenTask?: (task: TaskItem) => void;
}) {
  const [shown, setShown] = useState(12), [selectedTaskId, setSelectedTaskId] = useState<string>(), [loadedTasks, setLoadedTasks] = useState<Record<string, TaskItem>>({}), [taskError, setTaskError] = useState("");
  const epoch = useRef(0), selectedTaskRef = useRef<string | undefined>(undefined);
  const item = items.find((v) => keyOf(v) === selected), queue = items.filter(inProgressProduction), visible = queue.slice(0, shown);
  const taskFor = (v: ProductionContraption, id: string) => state?.tasks.get(id) ?? loadedTasks[`${v.projectId}:${id}`];
  useEffect(() => { epoch.current++; selectedTaskRef.current = undefined; setSelectedTaskId(undefined); setTaskError(""); }, [selected, connected, active]);
  useEffect(() => () => { epoch.current++; }, []);
  const inspect = (key: string) => { epoch.current++; selectedTaskRef.current = undefined; setSelectedTaskId(undefined); setTaskError(""); onSelect(key); };
  const back = () => onSelect("");
  const loadTask = async (entry: ProductionContraption, id: string) => {
    if (!connected || !active || !onOpenTask) return;
    const known = taskFor(entry, id);
    if (known) { epoch.current++; selectedTaskRef.current = id; setSelectedTaskId(undefined); setTaskError(""); onOpenTask(known); return; }
    if (!call) { setTaskError("Task details are unavailable."); return; }
    const generation = ++epoch.current; selectedTaskRef.current = id; setSelectedTaskId(id); setTaskError("");
    try { const result = await call("task_read", { project_id: entry.projectId, task_id: id }); if (generation === epoch.current && selected === keyOf(entry) && selectedTaskRef.current === id) { const task = asTask(result, entry.projectId); setLoadedTasks((old) => ({ ...old, [`${entry.projectId}:${id}`]: task })); setSelectedTaskId(undefined); onOpenTask(task); } } catch { if (generation === epoch.current) setTaskError("Task details are unavailable."); }
  };
  const incomplete = queue.filter(sourceStatus).length;
  const project = item && state?.projects.get(item.projectId), owner = item?.tasks.map((id) => taskFor(item, id)).find(Boolean)?.assigned_agent_id, ownerAgent = owner ? state?.agents.get(owner) : undefined;
  return <section className="dfConsoleSidebar__panel dfProduction" aria-label="Production inspection">
    <div className="dfProduction__heading"><h2>Changes</h2></div>
    {incomplete > 0 ? <p role="status">{incomplete} {incomplete === 1 ? "change is" : "changes are"} not fully shown on the floor.</p> : null}
    {!connected || error || overflow > 0 ? <details className="dfProduction__notice"><summary>{!connected ? "Last observed state" : error ? "Observation needs attention" : "Partial observation"}</summary>{!connected ? <p>Disconnected. This is the last observed state.</p> : null}{error ? <p role="alert">{error}</p> : null}{overflow > 0 ? <p>{overflow} more records are available.</p> : null}</details> : null}
    {item ? <article className="dfProduction__detail" aria-label={`Production details for ${title(item)}`}>
      <button type="button" className="dfConsoleBack" onClick={back}>← Back to Changes</button><h3>{title(item)}</h3><p className="dfProduction__stages">{productionStages(item).map((stage) => <span className="dfStatus" data-stage={stage} key={stage}>{stage}</span>)}</p><p>{item.nextAction}</p>
      {sourceStatus(item) ? <p role="status">{sourceStatus(item)}</p> : null}
      <details aria-label="Before and proposed source"><summary>Source changes</summary>
        <p>{item.source.kind} observation{item.source.stale ? " · stale" : ""}{item.source.observedAt ? ` · ${observedAt(item.source.observedAt)}` : ""}</p>
        <p>{item.source.target ? <>Target <code>{item.source.target}</code> · </> : null}Base <code>{item.source.base || "unavailable"}</code> → Head <code>{item.source.head || "unavailable"}</code>{item.source.observation ? <> · Working observation <code>{item.source.observation}</code></> : null}</p>
        {item.source.kind === "working-tree" ? <p>Observed local edits are not a reviewed commit. Commit checks and approval do not cover these edits.</p> : null}
        {item.source.reason ? <p role="status">{item.source.reason}</p> : null}
        {item.source.paths.length === 0 ? <p>{item.source.kind === "unavailable" ? "File edits unavailable." : item.source.omitted > 0 ? "File edits are outside this observation." : "No observed file edits."}</p> : <table><caption>Observed source modifications</caption><thead><tr><th>Operation</th><th>Before</th><th>Proposed</th></tr></thead><tbody>{item.source.paths.map((path, index) => <tr key={`${path.path}:${index}`}><th>{path.status}</th><td><code>{path.status === "added" ? "—" : path.old_path || path.path}</code></td><td><code>{path.status === "deleted" ? "dismantle" : path.path}</code></td></tr>)}</tbody></table>}
        <h4>Proposed static relationships</h4>
        {item.source.kind === "unavailable" || item.source.relationshipsUnavailable ? <p role="status">{item.source.relationshipsUnavailable || "Static dependency changes unavailable."}</p> : item.source.relationships.length === 0 ? <p>{item.source.relationshipsOmitted > 0 ? "Dependency changes are outside this observation." : "No static dependency changes in this observation."}</p> : <ul>{item.source.relationships.map((edge, index) => <li key={index}>{edge.status}: <code>{edge.from_path}</code> → <code>{edge.to_path}</code> · {edge.weight} static links</li>)}</ul>}
        {item.source.relationshipsOmitted > 0 ? <p>{item.source.relationshipsOmitted} relationship changes omitted from the bounded observation.</p> : null}
        {item.source.omitted > 0 ? <p role="status">{item.source.omitted} paths are outside this bounded observation. The linked diff contains the full proposal.</p> : null}
      </details>
      <details><summary>Evidence</summary><p>{project?.name || item.projectId} · {item.repository}{item.pullRequest ? ` · PR #${item.pullRequest.number}` : ""}</p>{item.pullRequest ? <p>Revision <code>{item.pullRequest.head}</code> · {item.pullRequest.merge ? `merged ${item.pullRequest.merge}` : item.pullRequest.state === "closed" ? "closed without merge" : "not merged"}{item.pullRequest.merge_queue ? ` · ${item.pullRequest.merge_queue}` : ""}</p> : null}{item.pullRequest && href(item.pullRequest.url) ? <p><a href={item.pullRequest.url} target="_blank" rel="noreferrer">Open pull request</a> <a href={`${item.pullRequest.url}/files`} target="_blank" rel="noreferrer">Open diff</a></p> : null}{item.construction?.head ? <p>Revision <code>{item.construction.head}</code></p> : null}{item.construction?.blocked_reason ? <p role="alert">{item.construction.blocked_reason}</p> : null}{item.linksOverflow ? <p>Additional task or mission links are outside this observation.</p> : null}
        <details><summary>Review · {item.review.state}</summary><p>{item.review.current ? "Current head" : item.review.head ? "Stale head" : "No head recorded"}{item.review.sourceFresh ? "" : " · observation is stale or unavailable"}</p><ReviewNotes findings={item.review.findings} />{href(item.review.url) ? <a href={item.review.url} target="_blank" rel="noreferrer">Open review</a> : null}{item.reviewers.map((v) => <details key={v.id}><summary>{v.name} · {v.provider} · {v.state}</summary><p>Head <code>{v.head}</code></p><ReviewNotes findings={v.findings ?? ""} />{href(v.url) ? <a href={v.url} target="_blank" rel="noreferrer">Evidence</a> : null}</details>)}</details>
        <details><summary>Checks · {item.checks.length}</summary>{item.checks.length ? item.checks.map((v) => <details key={`${v.repository}:${v.id}`}><summary>{v.name} · {v.state} · {v.conclusion || "pending"}</summary><p>{v.scope} · {v.applicable ? "current head" : "separate revision"}{v.overflow ? ` · +${v.overflow} omitted` : ""}{v.pull_requests.length > 1 ? ` · shared by ${v.pull_requests.length} PRs` : ""}</p>{v.jobs.map((job) => <p key={job.id}>{job.name} · {job.state} · {job.conclusion}{href(job.url) ? <> · <a href={job.url} target="_blank" rel="noreferrer">Job</a></> : null}</p>)}{href(v.url) ? <a href={v.url} target="_blank" rel="noreferrer">Open check</a> : null}</details>) : <p>No check evidence recorded.</p>}</details>
        <details><summary>Deployments · {item.deliveries.length}</summary><DeliveryEvidence deliveries={item.deliveries} current={connected && !error} /></details>
      </details>
      {ownerAgent ? <button type="button" disabled={!connected || !onAgent} onClick={() => onAgent?.(ownerAgent)}>Talk to {ownerAgent.name}</button> : null}{item.missions.map((id) => <button key={id} type="button" disabled={!connected || !onMission} onClick={() => onMission?.(item.projectId, id)}>Open mission {id.slice(0, 8)}</button>)}
      {item.tasks.length ? <details><summary>Tasks · {item.tasks.length}</summary>{item.tasks.map((id) => { const task = taskFor(item, id); return <button key={id} type="button" disabled={!connected || !onOpenTask} onClick={() => void loadTask(item, id)}>Open {task?.title || id} · {task?.status || "inspect"}</button>; })}{selectedTaskId && !taskError ? <p role="status">Loading task…</p> : null}{taskError ? <p role="alert">{taskError}</p> : null}</details> : null}
    </article> : <>
      {visible.length ? <div className="dfProduction__queue">{visible.map((v) => <button key={keyOf(v)} type="button" className="dfProduction__row" onClick={() => inspect(keyOf(v))}><strong>{title(v)}</strong><span className="dfProduction__stages">{productionStages(v).map((stage) => <span className="dfStatus" data-stage={stage} key={stage}>{stage}</span>)}</span><small>{v.nextAction}</small>{sourceStatus(v) ? <small>{sourceStatus(v)}</small> : null}</button>)}</div> : <p className="dfFactoryConsole__empty">{loading ? "Loading work…" : "No work is in progress."}</p>}{queue.length > shown ? <button type="button" disabled={!connected} onClick={() => setShown((n) => n + 12)}>Show more</button> : null}{queue.length <= shown && overflow > 0 && loadMore ? <button type="button" disabled={!connected || loading} onClick={loadMore}>{loading ? "Loading…" : "Show more"}</button> : null}
    </>}
    <details><summary>Change history</summary>{items.filter((entry) => !inProgressProduction(entry)).map((entry) => <p key={keyOf(entry)}><button type="button" onClick={() => inspect(keyOf(entry))}>{title(entry)} · {entry.pullRequest?.state || entry.construction?.status || entry.status}</button></p>)}</details>
  </section>;
}
