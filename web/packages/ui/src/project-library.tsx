import { ProjectOutcomes } from "./project-outcomes.js";
import { useEffect, useRef, useState } from "react";
import type { AgentItem, ProjectContentInput, ProjectContentOperation, ProjectContentOutput, StateView } from "@dark-factory/client";

export type ProjectContentCall = (operation: ProjectContentOperation, input: ProjectContentInput) => Promise<ProjectContentOutput>;
type RecordValue = Readonly<Record<string, unknown>>;
const text = (value: unknown): string => typeof value === "string" ? value : "";
const rows = (value: unknown): RecordValue[] => Array.isArray(value) ? value as RecordValue[] : [];
const strings = (value: unknown): string[] => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
const id = (): string => crypto.randomUUID().replaceAll("-", "");
const kinds = ["project_brief", "decision", "procedure", "lesson", "observation", "discussion", "discussion_reply"];
export function knowledgeMetadata(value: unknown): RecordValue {
  try { const parsed: unknown = JSON.parse(text(value)); return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as RecordValue : {}; } catch { return {}; }
}
const lines = (value: FormDataEntryValue | null): string[] => String(value ?? "").split("\n").map((item) => item.trim()).filter(Boolean);

/** Bodies are read only by an explicit gesture. Floor and panel entry points share these records. */
export function ProjectLibrary({ state, call, draft, board = false, entity = "", repository = "", initialID = "", onSource, onRecord }: {
  state?: StateView; call?: ProjectContentCall; draft?: (agent: AgentItem, instruction: string) => void;
  board?: boolean; entity?: string; repository?: string; initialID?: string; onSource?: (entity: string) => void; onRecord?: (kind: string, id: string, project: string) => void | Promise<void>;
}) {
  const [project, setProject] = useState("");
  const [items, setItems] = useState<RecordValue[]>([]), [next, setNext] = useState(0);
  const [selected, setSelected] = useState<RecordValue>();
  const [body, setBody] = useState(""), [bodyNext, setBodyNext] = useState<number>(0), [complete, setComplete] = useState(false);
  const [evidence, setEvidence] = useState<RecordValue[]>([]), [evidenceNext, setEvidenceNext] = useState(0);
  const [replies, setReplies] = useState<RecordValue[]>([]), [replyNext, setReplyNext] = useState(0);
  const [accesses, setAccesses] = useState<RecordValue[]>([]), [accessNext, setAccessNext] = useState(0);
  const [pending, setPending] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [editing, setEditing] = useState(false), [newKind, setNewKind] = useState(""), [seed, setSeed] = useState<RecordValue>({});
  const [filters, setFilters] = useState({ query: "", repository_id: repository, branch: "", environment: "", kind: board ? "discussion" : "", entity });
  const epoch = useRef(0), newID = useRef(""), evidenceID = useRef(id()), searchMode = useRef(true);
  const [showResolved, setShowResolved] = useState(false);
  const projects = [...(state?.projects.values() ?? [])];
  const projectID = project || projects[0]?.id || "";
  const metadata = knowledgeMetadata(selected?.source_references);
  const discussion = selected?.kind === "discussion";
  const threadID = discussion ? text(selected?.id) : text(metadata.thread_id);
  useEffect(() => () => { epoch.current++; }, []);
  const run = async (work: () => Promise<void>) => {
    const generation = epoch.current;
    setPending(true); setError(""); setNotice("");
    try { await work(); } catch (error) { if (generation === epoch.current) setError(error instanceof Error ? error.message : "Request failed"); }
    finally { if (generation === epoch.current) setPending(false); }
  };
  const request = (operation: ProjectContentOperation, input: ProjectContentInput = {}) => {
    if (call === undefined || projectID === "") return Promise.reject(new Error("Connect and select a project"));
    return call(operation, { ...input, project_id: projectID });
  };
  const list = async (offset = 0, search = true) => {
    const generation = epoch.current;
    searchMode.current = search;
    const result = await request(search ? "search" : "list", { ...(search ? { ...filters, open_only: board && !showResolved } : {}), offset, limit: search ? 4 : 1 });
    if (generation !== epoch.current) return;
    setItems(rows(result.items)); setNext(Number(result.next_offset ?? 0));
  };
  const selectRevision = (value: RecordValue, loadedBody?: string) => {
    setSelected(value); setBody(loadedBody ?? ""); setBodyNext(0); setComplete(loadedBody !== undefined); setEvidence([]); setEvidenceNext(0); setReplies([]); setReplyNext(0); setAccesses([]); setAccessNext(0); setEditing(false);
  };
  const read = async (contentID: string, revision?: number) => {
    const generation = epoch.current;
    let result = await request("read", { id: contentID, revision: revision ?? 1 });
    if (generation !== epoch.current) return;
    if (revision === undefined && Number(result.latest_revision) > 1) result = await request("read", { id: contentID, revision: Number(result.latest_revision) });
    if (generation !== epoch.current) return;
    selectRevision(result);
  };
  useEffect(() => { if (initialID) void run(() => read(initialID)); }, [initialID]);
  const loadBody = async () => {
    if (selected === undefined || complete) return;
    const generation = epoch.current;
    const result = await request("body", { id: selected.id, revision: selected.revision, offset: bodyNext, limit: 8192 });
    if (generation !== epoch.current) return;
    setBody((value) => value + text(result.body)); setComplete(result.complete === true); setBodyNext(Number(result.next_offset ?? 0));
  };
  const loadRelated = async (type: "evidence_list" | "accesses" | "search", offset = 0) => {
    if (!selected) return;
    const generation = epoch.current;
    const result = await request(type, type === "search" ? { repository_id: text(selected.repository_id) || filters.repository_id, branch: text(metadata.branch), environment: text(metadata.environment), thread_id: selected.id, kind: "discussion_reply", offset, limit: 4 } : type === "accesses" ? { id: selected.id, revision: selected.revision, offset, limit: 4 } : { content_id: selected.id, content_revision: selected.revision, offset, limit: 1 });
    if (generation !== epoch.current) return;
    (type === "search" ? setReplies : type === "accesses" ? setAccesses : setEvidence)(rows(result.items));
    (type === "search" ? setReplyNext : type === "accesses" ? setAccessNext : setEvidenceNext)(Number(result.next_offset ?? 0));
  };
  const begin = (kind: string, details: RecordValue = {}) => { newID.current = id(); setNewKind(kind); setSeed({ ...details, ...(kind === "discussion" ? {} : { resolved: false, pinned: false }) }); setEditing(true); };
  const updateThread = async (changes: RecordValue) => {
    if (!selected || !complete) return;
    const value = await request("revise", { id: selected.id, expected_revision: selected.revision, kind: selected.kind, title: selected.title, description: selected.description, body, source_references: JSON.stringify({ ...metadata, ...changes }) });
    selectRevision(value, body); setNotice("Thread updated. No task state or delivery changed.");
  };
  const entry = (item: RecordValue) => {
    const meta: RecordValue = { ...knowledgeMetadata(item.source_references), status: item.projected_status || knowledgeMetadata(item.source_references).status };
    return <button type="button" data-knowledge-status={text(meta.status)} key={text(item.id)} onClick={() => void run(() => read(text(item.id), Number(item.revision)))}>{text(item.title)} · {text(item.kind)} · r{String(item.revision)}{meta.resolved === true ? " · resolved" : item.kind === "discussion" ? " · open" : ""}{meta.pinned === true ? " · pinned" : ""}{text(meta.status) ? ` · ${text(meta.status).replaceAll("_", " ")}` : ""}{item.deprecated === true ? " · deprecated" : ""}</button>;
  };
  const editorKind = newKind || text(selected?.kind);
  const editorMeta = newKind ? seed : metadata;
  return <details className="dfConsoleSidebar__panel dfProjectLibrary" open={board || Boolean(initialID)}>
    <summary>{board ? "Project board" : "Instructions & outcomes"}</summary>
    <p>{board ? "Questions, findings and handovers. Replies retain their authors and evidence; board actions do not deliver task replies." : "Source-linked project knowledge. Tasks keep the exact versions supplied or attached. Notes do not grant permissions."}</p>
    <label>Project <select value={projectID} disabled={pending} onChange={(event) => { epoch.current++; setProject(event.target.value); setFilters({ query: "", repository_id: "", branch: "", environment: "", kind: board ? "discussion" : "", entity: "" }); setItems([]); setSelected(undefined); setEditing(false); setNext(0); }}>
      {projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    <fieldset disabled={pending || call === undefined || projectID === ""}>
      <form onSubmit={(event) => { event.preventDefault(); void run(() => list()); }}>
        <label>Search knowledge <input type="search" value={filters.query} onChange={(event) => setFilters({ ...filters, query: event.target.value })} /></label>
        <label>Category <select value={filters.kind} onChange={(event) => setFilters({ ...filters, kind: event.target.value })}><option value="">All knowledge</option>{kinds.map((kind) => <option key={kind} value={kind}>{kind.replaceAll("_", " ")}</option>)}</select></label>
        <details><summary>Applicability filters</summary>{(["repository_id", "branch", "environment", "entity"] as const).map((key) => <label key={key}>{key.replaceAll("_", " ")} <input value={filters[key]} onChange={(event) => setFilters({ ...filters, [key]: event.target.value })} /></label>)}</details>
        <button>Search</button>
      </form>
      <button type="button" onClick={() => void run(() => list(0, board))}>{board ? "Browse discussions" : "Browse library"}</button>
      <button type="button" onClick={() => begin(board ? "discussion" : "observation", { status: "tentative", entities: filters.entity ? [filters.entity] : [] })}>{board ? "New discussion" : "New document"}</button>
      <label><input type="checkbox" checked={showResolved} onChange={(event) => setShowResolved(event.target.checked)} /> Include resolved discussions</label>
      <div aria-label={board ? "Discussions" : "Library results"}>{items.filter((item) => !board || showResolved || !knowledgeMetadata(item.source_references).resolved).map(entry)}</div>
      {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next, searchMode.current))}>Next documents</button>}
      {selected === undefined ? null : <section aria-label="Selected library revision">
        <h3>{text(selected.title)}</h3><p>{text(selected.description)}</p><p>Scope: {metadata.scope === "project" ? "whole project" : "repository"} · Repository: {text(selected.repository_id) || "project default"}</p>
        <p>Revision {String(selected.revision)} · {selected.deprecated === true ? "Deprecated" : Number(selected.revision) < Number(selected.latest_revision) ? "Superseded revision" : "Current revision"} · {text(selected.author)}</p>
        <p>Knowledge status: {text(selected.projected_status || metadata.status).replaceAll("_", " ") || "unspecified"}{discussion ? metadata.resolved ? " · resolved discussion" : " · unresolved discussion" : ""}{metadata.pinned ? " · pinned" : ""}</p>
        {Object.keys(metadata).length === 0 ? <p>{text(selected.source_references)}</p> : <>
          <p>Source revision: <code>{text(metadata.source_revision) || "unspecified"}</code> · Branch: {text(metadata.branch) || "any"} · Environment: {text(metadata.environment) || "any"}</p>
          {strings(metadata.entities).map((ref) => <button key={ref} type="button" disabled={!onSource} onClick={() => onSource?.(ref)}>Source: {ref}</button>)}
          {strings(metadata.evidence).map((reference) => <p key={reference}>Evidence: {reference}</p>)}
          {text(metadata.record_id) ? <button type="button" disabled={!onRecord} onClick={() => void run(async () => { await onRecord?.(text(metadata.record_type), text(metadata.record_id), projectID); })}>Open existing {text(metadata.record_type)}: {text(metadata.record_id)}</button> : null}
          {text(metadata.task_id) ? <button type="button" disabled={!onRecord} onClick={() => void run(async () => { await onRecord?.("task", text(metadata.task_id), projectID); })}>Open task: {text(metadata.task_id)}</button> : null}
          {text(metadata.supersedes) ? <button type="button" onClick={() => void run(() => read(text(metadata.supersedes)))}>Supersedes: {text(metadata.supersedes)}</button> : null}
          {threadID && !discussion ? <button type="button" onClick={() => void run(() => read(threadID))}>Original discussion: {threadID}</button> : null}
        </>}
        <label>Read revision <input type="number" min="1" max={Number(selected.latest_revision)} defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} onBlur={(event) => { const revision = Number(event.target.value); if (Number.isSafeInteger(revision) && revision > 0 && revision !== selected.revision) void run(() => read(text(selected.id), revision)); }} /></label>
        <pre>{body}</pre>
        {complete ? null : <button type="button" onClick={() => void run(loadBody)}>{bodyNext === 0 ? "Read body" : "Read next body page"}</button>}
        <button type="button" disabled={!complete || selected.kind === "discussion_reply" || selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => { setNewKind(""); setEditing(true); }}>Revise document</button>
        <button type="button" disabled={selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => void run(async () => { selectRevision(await request("deprecate", { id: selected.id, expected_revision: selected.revision }), complete ? body : undefined); setNotice("Deprecated. Existing task references remain."); })}>Deprecate</button>
        {!discussion ? null : <div aria-label="Thread actions">
          <button type="button" onClick={() => void run(() => loadRelated("search"))}>Read replies</button>
          <button type="button" onClick={() => begin("discussion_reply", { ...metadata, thread_id: selected.id, resolved: false, pinned: false, status: "tentative" })}>Reply</button>
          <button type="button" disabled={!complete || selected.revision !== selected.latest_revision} onClick={() => void run(() => updateThread({ resolved: !metadata.resolved }))}>{metadata.resolved ? "Reopen discussion" : "Resolve discussion"}</button>
          <button type="button" disabled={!complete || selected.revision !== selected.latest_revision} onClick={() => void run(() => updateThread({ pinned: !metadata.pinned }))}>{metadata.pinned ? "Unpin discussion" : "Pin discussion"}</button>
          <button type="button" onClick={() => begin("lesson", { ...metadata, resolved: false, pinned: false, thread_id: selected.id, evidence: [...strings(metadata.evidence), `content:${selected.id}@${selected.revision}`], status: "tentative" })}>Retain conclusion</button>
          {replies.map(entry)}{replyNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("search", replyNext))}>Next replies</button>}
        </div>}
        <p>Retrieval records show exact bytes supplied or read, not understanding or application.</p>
        <button type="button" onClick={() => void run(() => loadRelated("accesses"))}>Retrieval records</button>
        {accesses.map((item, index) => <p key={index}>Revision {String(item.content_revision ?? item.revision)} · {text(item.kind) || text(item.access_kind)} · Task {text(item.task_id)} · Run {text(item.run_id)} · Bytes {String(item.offset ?? 0)}–{Number(item.offset ?? 0) + Number(item.byte_length ?? 0)} · {String(item.created_at_ms ?? item.at_ms ?? "")}</p>)}
        {accessNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("accesses", accessNext))}>Next retrieval records</button>}
        <button type="button" onClick={() => void run(() => loadRelated("evidence_list"))}>Test results</button>
        {evidence.map((item) => <article key={text(item.id)}><p>{text(item.result)} · {text(item.judgment)}</p><p>Source: {text(item.tested_source)} · Environment: {text(item.environment)}</p><p>Evidence: {text(item.location)} · Evaluator: {text(item.evaluator)}</p></article>)}
        {evidenceNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("evidence_list", evidenceNext))}>Next evidence</button>}
        <details><summary>Record a test result</summary><form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("evidence", { id: evidenceID.current, content_id: selected.id, content_revision: selected.revision, tested_source: data.get("tested_source"), environment: data.get("environment"), result: data.get("result"), location: data.get("location"), judgment: data.get("judgment") }); evidenceID.current = id(); setNotice("Test result saved."); }); }}>
          <label>Tested source <input name="tested_source" required /></label><label>Environment <input name="environment" required /></label><label>Observed result <select name="result"><option>passed</option><option>failed</option><option>incomplete</option><option>not_run</option></select></label><label>Existing report or result reference <input name="location" required /></label><label>Judgment <textarea name="judgment" /></label><button>Record observation</button>
        </form></details>
        <form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("attach", { task_id: data.get("task"), content_id: selected.id, content_revision: selected.revision }); setNotice("Attached to the task."); }); }}>
          <label>Attach to queued task <select name="task" required defaultValue=""><option value="" disabled>Select task</option>{[...(state?.tasks.values() ?? [])].filter((task) => task.project_id === projectID && task.status === "queued").map((task) => <option key={task.id} value={task.id}>{task.title}</option>)}</select></label><button>Attach revision</button>
        </form>
        <form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const agent = state?.agents.get(String(data.get("agent"))); if (agent !== undefined) draft?.(agent, `Use project library ${selected.id} revision ${selected.revision} (${selected.title}) if relevant. Read its body lazily.\n\n${String(data.get("instruction"))}`); }}>
          <label>Draft ordinary work <select name="agent" required defaultValue=""><option value="" disabled>Select agent</option>{[...(state?.agents.values() ?? [])].filter((agent) => agent.project_id === projectID && !agent.archived).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
          <label>Instruction <textarea name="instruction" required /></label><button disabled={draft === undefined}>Open task draft</button>
        </form>
      </section>}
      {!editing ? null : <form aria-label="Knowledge editor" key={newKind ? newID.current : `${selected?.id}:${selected?.revision}`} onSubmit={(event) => {
        event.preventDefault(); const data = new FormData(event.currentTarget);
        const details: Record<string, unknown> = { ...editorMeta, status: data.get("status"), scope: data.get("scope"), evidence: lines(data.get("evidence")), entities: lines(data.get("entities")), mentions: lines(data.get("mentions")) };
        for (const key of ["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"]) { const value = text(data.get(key)); if (value) details[key] = value; else delete details[key]; }
        void run(async () => { const value = await request(newKind ? "create" : "revise", { id: newKind ? newID.current : selected?.id, expected_revision: newKind ? 0 : selected?.revision, repository_id: data.get("repository_id"), kind: data.get("kind"), title: data.get("title"), description: data.get("description"), body: data.get("body"), source_references: (kinds.includes(String(data.get("kind"))) && (data.get("kind") !== "procedure" || Boolean(newKind) || Object.keys(metadata).length > 0)) ? JSON.stringify(details) : data.get("source_references") }); selectRevision(value, String(data.get("body"))); setNotice("Saved as an immutable revision."); });
      }}>
        <label>Kind <input name="kind" defaultValue={editorKind} required maxLength={64} list="df-library-kinds" /></label><datalist id="df-library-kinds">{kinds.map((kind) => <option key={kind} value={kind} />)}<option value="acceptance_scenario" /></datalist>
        <label>Repository ID (blank uses the project default) <input name="repository_id" defaultValue={newKind && newKind !== "discussion_reply" && !text(seed.thread_id) ? filters.repository_id : text(selected?.repository_id) || filters.repository_id} /></label>
        <label>Title <input name="title" defaultValue={newKind ? "" : text(selected?.title)} required /></label>
        <label>Description <textarea name="description" defaultValue={newKind ? "" : text(selected?.description)} /></label>
        <label>Scope <select name="scope" defaultValue={text(editorMeta.scope) || (editorKind === "project_brief" ? "project" : "repository")}><option value="repository">This repository</option><option value="project">Whole project (operator guidance)</option></select></label>
        <label>Status <select name="status" defaultValue={text(editorMeta.status) || "tentative"}>{["tentative", "current", "needs_revalidation", "superseded"].map((status) => <option key={status} value={status}>{status.replaceAll("_", " ")}</option>)}</select></label>
        <label>Supporting evidence (one reference per line) <textarea name="evidence" defaultValue={strings(editorMeta.evidence).join("\n")} /></label>
        <label>Code entities (one stable reference per line) <textarea name="entities" defaultValue={strings(editorMeta.entities).join("\n")} /></label>
        <label>Mentions (one agent ID per line) <textarea name="mentions" defaultValue={strings(editorMeta.mentions).join("\n")} /></label>
        <details><summary>Source and existing record links</summary>{["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"].map((key) => <label key={key}>{key.replaceAll("_", " ")} <input name={key} defaultValue={text(editorMeta[key])} /></label>)}<label>Source references for legacy documents <textarea name="source_references" defaultValue={newKind ? "" : text(selected?.source_references)} /></label></details>
        <label>Body <textarea name="body" rows={12} defaultValue={newKind ? "" : body} required /></label>
        <button>Save revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
    </fieldset>
    {board ? null : <ProjectOutcomes key={projectID} project={projectID} call={call} />}
    {error === "" ? null : <p role="alert">{error}</p>}{notice === "" ? null : <p role="status">{notice}</p>}
  </details>;
}
