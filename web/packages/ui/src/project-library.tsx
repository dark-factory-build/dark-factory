import { ProjectOutcomes } from "./project-outcomes.js";
import { DirectQuestions, authorName } from "./project-board.js";
import { useEffect, useRef, useState } from "react";
import type { AgentItem, ProjectContentInput, ProjectContentOperation, ProjectContentOutput, StateView } from "@dark-factory/client";

export type ProjectContentCall = (operation: ProjectContentOperation, input: ProjectContentInput) => Promise<ProjectContentOutput>;
type RecordValue = Readonly<Record<string, unknown>>;
const text = (value: unknown): string => typeof value === "string" ? value : "";
const rows = (value: unknown): RecordValue[] => Array.isArray(value) ? value as RecordValue[] : [];
const strings = (value: unknown): string[] => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
const id = (): string => crypto.randomUUID().replaceAll("-", "");
const kindLabel = (kind: string): string => ({ project_brief: "Project brief", decision: "Decision", procedure: "Procedure", lesson: "Lesson", observation: "Note", discussion: "Discussion", discussion_reply: "Reply" }[kind] ?? kind.replaceAll("_", " "));
const kinds = ["project_brief", "decision", "procedure", "lesson", "observation", "discussion", "discussion_reply"];
const documentKinds = kinds.filter((kind) => !kind.startsWith("discussion"));
export function knowledgeMetadata(value: unknown): RecordValue {
  try { const parsed: unknown = JSON.parse(text(value)); return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as RecordValue : {}; } catch { return {}; }
}
const lines = (value: FormDataEntryValue | null): string[] => String(value ?? "").split("\n").map((item) => item.trim()).filter(Boolean);
const documentState = (item: RecordValue, metadata = knowledgeMetadata(item.source_references)): string => {
  const version = item.deprecated === true ? "Retired" : Number(item.revision) < Number(item.latest_revision) ? "Older revision" : "";
  const status = text(item.projected_status || metadata.status);
  return [version, status === "current" && version ? "" : status.replaceAll("_", " "), item.kind === "discussion" ? metadata.resolved === true ? "resolved" : "open" : "", metadata.pinned === true ? "pinned" : ""].filter(Boolean).join(" · ");
};

type LibraryProps = {
  state?: StateView; call?: ProjectContentCall; draft?: (agent: AgentItem, instruction: string) => void;
  board?: boolean; entity?: string; repository?: string; initialID?: string; initialRevision?: number; onSource?: (entity: string) => void; onRecord?: (kind: string, id: string, project: string) => void | Promise<void>;
};

/** An intentional entry loads metadata; an unused Settings panel stays lazy. */
export function ProjectLibrary({ state, call, draft, board = false, entity = "", repository = "", initialID = "", initialRevision, onSource, onRecord, open = false, initialProjectId = "" }: LibraryProps & { open?: boolean; initialProjectId?: string }) {
  const [expanded, setExpanded] = useState(false);
  const [view, setView] = useState("documents");
  const [project, setProject] = useState(initialProjectId);
  const projects = [...(state?.projects.values() ?? [])];
  const projectID = projects.find((item) => item.id === project)?.id ?? projects[0]?.id ?? "";
  const scoped = projectID === (initialProjectId || projects[0]?.id);
  const contents = <>
    {projects.length < 2 ? null : <label>Project <select value={projectID} onChange={(event) => setProject(event.target.value)}>{projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    {board ? <p className="dfProjectLibrary__purpose">Ask questions, share findings and follow replies with agents and operators. Threads are retained for anyone to read; reusable guidance belongs in the Library.</p>
      : <nav className="dfProjectLibrary__actions dfProjectLibrary__views" aria-label="Library views">{[["documents", "All documents"], ["outcomes", "Outcomes"]].map(([value, label]) => <button key={value} type="button" aria-pressed={view === value} onClick={() => setView(value!)}>{label}</button>)}</nav>}
    {view === "outcomes" && !board ? <ProjectOutcomes key={projectID} project={projectID} call={call} /> : <LibraryDocuments key={`${projectID}:${scoped ? `${entity}:${repository}:${initialID}:${initialRevision}` : ""}`} projectID={projectID} state={state} call={call} draft={draft} board={board} entity={scoped ? entity : ""} repository={scoped ? repository : ""} initialID={scoped ? initialID : ""} initialRevision={scoped ? initialRevision : undefined} onSource={onSource} onRecord={onRecord} />}
    {board ? <DirectQuestions state={state} projectID={projectID} onOpen={onRecord === undefined ? undefined : (id) => void Promise.resolve(onRecord("peer_question", id, projectID)).catch(() => undefined)} /> : null}
  </>;
  return open || board || initialID ? <section className="dfProjectLibrary" aria-label={board ? "Project board" : "Project library"}>{contents}</section>
    : <details className="dfConsoleSidebar__panel dfProjectLibrary" onToggle={(event) => setExpanded(event.currentTarget.open)}><summary>Project library</summary>{expanded ? contents : null}</details>;
}

function LibraryDocuments({ state, call, draft, projectID, board = false, entity = "", repository = "", initialID = "", initialRevision, onSource, onRecord }: LibraryProps & { projectID: string }) {
  const [items, setItems] = useState<RecordValue[]>([]), [next, setNext] = useState(0);
  const [selected, setSelected] = useState<RecordValue>();
  const [loaded, setLoaded] = useState(false);
  const [body, setBody] = useState(""), [bodyNext, setBodyNext] = useState<number>(0), [complete, setComplete] = useState(false);
  const [replies, setReplies] = useState<RecordValue[]>([]), [replyNext, setReplyNext] = useState(0);
  const [accesses, setAccesses] = useState<RecordValue[]>([]), [accessNext, setAccessNext] = useState(0);
  const [pending, setPending] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [editing, setEditing] = useState(false), [newKind, setNewKind] = useState(""), [seed, setSeed] = useState<RecordValue>({});
  const [filters, setFilters] = useState({ query: "", repository_id: repository, branch: "", environment: "", kind: board ? "discussion" : "", entity });
  const initialRead = useRef("");
  const searchTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const epoch = useRef(0), newID = useRef(""), listQuery = useRef<ProjectContentInput>({});
  const [showResolved, setShowResolved] = useState(false);
  const [sourceFilters, setSourceFilters] = useState(false);
  const [panel, setPanel] = useState("read");
  const metadata = knowledgeMetadata(selected?.source_references);
  const discussion = selected?.kind === "discussion";
  const threadID = discussion ? text(selected?.id) : text(metadata.thread_id);
  const run = async (work: () => Promise<void>) => {
    clearTimeout(searchTimer.current);
    const generation = ++epoch.current;
    setPending(true); setError(""); setNotice("");
    try { await work(); } catch (error) { if (generation === epoch.current) setError(error instanceof Error ? error.message : "Request failed"); }
    finally { if (generation === epoch.current) setPending(false); }
  };
  const request = async (operation: ProjectContentOperation, input: ProjectContentInput = {}) => {
    if (call === undefined || projectID === "") return Promise.reject(new Error("Connect and select a project"));
    const generation = epoch.current;
    const value = await call(operation, { ...input, project_id: projectID });
    if (generation !== epoch.current) throw new Error("Library view changed");
    return value;
  };
  const list = async (offset = 0) => {
    const generation = epoch.current;
    if (offset === 0) listQuery.current = { ...filters, kind: board ? "discussion" : filters.kind, open_only: board && !showResolved, documents_only: !board };
    const result = await request("search", { ...listQuery.current, offset, limit: 4 });
    if (generation !== epoch.current) return;
    setItems((existing) => [...new Map([...(offset === 0 ? [] : existing), ...rows(result.items)].map((item) => [text(item.id), item])).values()]);
    setNext(Number(result.next_offset ?? 0)); setLoaded(true);
  };
  const selectRevision = (value: RecordValue, loadedBody?: string) => {
    setPanel("read"); newID.current = "";
    setSelected(value); setBody(loadedBody ?? ""); setBodyNext(0); setComplete(loadedBody !== undefined); setReplies([]); setReplyNext(0); setAccesses([]); setAccessNext(0); setEditing(false);
  };
  const read = async (contentID: string, revision?: number) => {
    const generation = epoch.current;
    let result = await request("read", { id: contentID, revision: revision ?? 1 });
    if (generation !== epoch.current) return;
    if (revision === undefined && Number(result.latest_revision) > 1) result = await request("read", { id: contentID, revision: Number(result.latest_revision) });
    if (generation !== epoch.current) return;
    selectRevision(result);
    await loadBody(result, 0);
    if (generation === epoch.current && result.kind === "discussion") await loadRelated("search", 0, result);
  };
  const available = call !== undefined && projectID !== "";
  useEffect(() => {
    const load = () => { if (available) void run(async () => { const generation = epoch.current; await list(); if (generation === epoch.current && initialID && initialRead.current !== initialID) { initialRead.current = initialID; await read(initialID, initialRevision); } }); };
    const timer = searchTimer.current = filters.query ? setTimeout(load, 250) : undefined;
    if (!timer) load();
    return () => { clearTimeout(timer); epoch.current++; };
  }, [available, initialID, initialRevision, filters, showResolved]);
  const loadBody = async (content = selected, offset = bodyNext) => {
    if (content === undefined) return;
    const generation = epoch.current;
    const result = await request("body", { id: content.id, revision: content.revision, offset, limit: 8192 });
    if (generation !== epoch.current) return;
    setBody((value) => offset === 0 ? text(result.body) : value + text(result.body)); setComplete(result.complete === true); setBodyNext(Number(result.next_offset ?? 0));
  };
  const loadRelated = async (type: "accesses" | "search", offset = 0, content = selected) => {
    if (!content) return;
    const source = knowledgeMetadata(content.source_references);
    const generation = epoch.current;
    const result = await request(type, type === "search" ? { repository_id: text(content.repository_id) || filters.repository_id, branch: text(source.branch), environment: text(source.environment), thread_id: content.id, kind: "discussion_reply", offset, limit: 4 } : { id: content.id, revision: content.revision, offset, limit: 4 });
    if (generation !== epoch.current) return;
    const related = rows(result.items);
    if (type === "search") {
      for (let index = 0; index < related.length; index++) {
        const item = related[index]!;
        const content = await request("body", { id: item.id, revision: item.revision, offset: 0, limit: 8192 });
        if (generation !== epoch.current) return;
        related[index] = { ...item, body: text(content.body), complete: content.complete === true };
      }
    }
    (type === "search" ? setReplies : setAccesses)((previous) => offset === 0 ? related : [...previous, ...related]);
    (type === "search" ? setReplyNext : setAccessNext)(Number(result.next_offset ?? 0));
  };
  const begin = (kind: string, details: RecordValue = {}) => { newID.current = id(); setNewKind(kind); setSeed({ ...details, ...(kind === "discussion" ? {} : { resolved: false, pinned: false }) }); setPanel("read"); setEditing(true); };
  const updateThread = async (changes: RecordValue) => {
    if (!selected || !complete) return;
    const value = await request("revise", { id: selected.id, expected_revision: selected.revision, kind: selected.kind, title: selected.title, description: selected.description, body, source_references: JSON.stringify({ ...metadata, ...changes }) });
    selectRevision(value, body); setNotice("Discussion updated."); await loadRelated("search", 0, value); await list();
  };
  const retire = async () => { if (!selected) return; selectRevision(await request("deprecate", { id: selected.id, expected_revision: selected.revision }), body); setNotice("Document retired."); await list(); };
  const entry = (item: RecordValue) => {
    const meta: RecordValue = { ...knowledgeMetadata(item.source_references), status: item.projected_status || knowledgeMetadata(item.source_references).status };
    return <button type="button" data-knowledge-status={text(meta.status)} key={text(item.id)} aria-pressed={selected?.id === item.id && selected?.revision === item.revision} onClick={() => void run(() => read(text(item.id), Number(item.revision)))}><strong>{text(item.title)}</strong><span>{kindLabel(text(item.kind))}{documentState(item, meta) ? ` · ${documentState(item, meta)}` : ""}</span></button>;
  };
  const editorKind = newKind || text(selected?.kind);
  const editorMeta = newKind ? seed : metadata;
  const editingSources = panel === "sources";
  const structured = kinds.includes(editorKind) && (editorKind !== "procedure" || Boolean(newKind) || Object.keys(metadata).length > 0);
  const editable = selected !== undefined && complete && selected.kind !== "discussion_reply" && selected.deprecated !== true && selected.revision === selected.latest_revision;
  return <>
    <fieldset disabled={pending || !available} aria-busy={pending}>
      <form role="search" onSubmit={(event) => { event.preventDefault(); void run(() => list()); }}>
        <div className="dfProjectLibrary__toolbar">
          <label className="dfProjectLibrary__search">Search {board ? "discussions" : "documents"}<input type="search" value={filters.query} onChange={(event) => setFilters({ ...filters, query: event.target.value })} /></label>
          {board ? <label><input type="checkbox" checked={showResolved} onChange={(event) => setShowResolved(event.target.checked)} /> Include resolved</label> : <label>Category <select value={filters.kind} onChange={(event) => setFilters({ ...filters, kind: event.target.value })}><option value="">All categories</option>{documentKinds.map((kind) => <option key={kind} value={kind}>{kindLabel(kind)}</option>)}</select></label>}
          {board && ![filters.repository_id, filters.branch, filters.environment, filters.entity].some(Boolean) ? null : <button type="button" aria-expanded={sourceFilters} onClick={() => setSourceFilters(!sourceFilters)}>Source filters{[filters.repository_id, filters.branch, filters.environment, filters.entity].filter(Boolean).length ? ` (${[filters.repository_id, filters.branch, filters.environment, filters.entity].filter(Boolean).length})` : ""}</button>}
          {board ? <button type="button" onClick={() => begin("discussion", { status: "tentative", entities: filters.entity ? [filters.entity] : [] })}>Start a thread</button> : <button type="button" onClick={() => begin("observation", { status: "tentative", entities: filters.entity ? [filters.entity] : [] })}>New document</button>}
        </div>
        {!sourceFilters ? null : <fieldset className="dfProjectLibrary__fields"><legend>Filter by source</legend>{(["repository_id", "branch", "environment", "entity"] as const).map((key) => <label key={key}>{key.replaceAll("_", " ")} <input value={filters[key]} onChange={(event) => setFilters({ ...filters, [key]: event.target.value })} /></label>)}</fieldset>}
      </form>
      <div className="dfProjectLibrary__browser" data-reading={selected !== undefined || editing}>
      <nav className="dfProjectLibrary__documents" aria-label={board ? "Discussions" : "Library documents"}>
        {!available ? <p>Connect to read project {board ? "discussions" : "documents"}.</p> : !loaded ? <p>Loading…</p> : items.length ? null : <p>{[filters.query, filters.repository_id, filters.branch, filters.environment, filters.entity, board ? "" : filters.kind].some(Boolean) ? "No matches." : board ? showResolved ? "No discussions yet." : "No open discussions." : "No documents yet."}</p>}
        {items.filter((item) => !board || showResolved || !knowledgeMetadata(item.source_references).resolved).map(entry)}
        {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next))}>{board ? "More discussions" : "Load more documents"}</button>}
      </nav>
      <div className="dfProjectLibrary__reader">
      {selected === undefined || editing ? null : <section aria-label="Selected library revision">
        <h3>{text(selected.title)}</h3>
        <p className="dfProjectLibrary__meta" data-knowledge-status={text(selected.projected_status || metadata.status)}>{kindLabel(text(selected.kind))} · Revision {String(selected.revision)}{authorName(text(selected.author), state) ? ` · by ${authorName(text(selected.author), state)}` : ""}{documentState(selected, metadata) ? ` · ${documentState(selected, metadata)}` : ""}</p>
        {discussion && (text(metadata.task_id) || text(metadata.record_id)) ? <p><button type="button" disabled={!onRecord} onClick={() => void run(async () => { await onRecord?.(text(metadata.record_id) ? text(metadata.record_type) : "task", text(metadata.record_id) || text(metadata.task_id), projectID); })}>View linked {text(metadata.record_id) ? text(metadata.record_type) || "record" : "task"}</button></p> : null}
        {panel === "read" ? null : <button type="button" onClick={() => setPanel("read")}>← Read document</button>}
        {threadID && !discussion ? <p><button type="button" onClick={() => void run(() => read(threadID))}>Back to discussion</button></p> : null}
        <div className="dfProjectLibrary__workspace">
        {panel !== "read" ? null : <>
        <pre>{body}</pre>
        {complete ? null : <button type="button" onClick={() => void run(() => loadBody())}>{bodyNext === 0 ? "Retry document text" : "Read more"}</button>}
        <div className="dfProjectLibrary__actions" aria-label="Document actions">
          <button type="button" disabled={!editable} onClick={() => { setNewKind(""); setEditing(true); }}>{discussion ? "Edit post" : "Edit document"}</button>
          <button type="button" onClick={() => setPanel("attach")}>Use in a task</button>
          <button type="button" onClick={() => setPanel("sources")}>Sources &amp; history</button>
        </div>
        {!discussion ? null : <section aria-label="Replies">
          <div className="dfProjectLibrary__actions">
            <h4>Discussion</h4>
            <button type="button" disabled={!editable} onClick={() => void run(() => updateThread({ resolved: !metadata.resolved }))}>{metadata.resolved ? "Reopen discussion" : "Resolve discussion"}</button>
            <button type="button" disabled={!editable} onClick={() => void run(() => updateThread({ pinned: !metadata.pinned }))}>{metadata.pinned ? "Unpin discussion" : "Pin discussion"}</button>
          </div>
          {replies.map((reply) => <article className="dfProjectLibrary__reply" key={text(reply.id)}><p className="dfProjectLibrary__meta">{authorName(text(reply.author), state) || "Reply"} · Revision {String(reply.revision)}</p><pre>{text(reply.body)}</pre>{reply.complete ? null : <button type="button" onClick={() => void run(() => read(text(reply.id), Number(reply.revision)))}>Read full reply</button>}</article>)}{replyNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("search", replyNext))}>More replies</button>}
          <form key={text(selected.id)} aria-label="Reply to discussion" onSubmit={(event) => {
            event.preventDefault(); const form = event.currentTarget, reply = String(new FormData(form).get("reply") ?? "").trim();
            if (!reply) return;
            newID.current ||= id();
            void run(async () => { await request("create", { id: newID.current, kind: "discussion_reply", repository_id: selected.repository_id, title: "Reply", body: reply, source_references: JSON.stringify({ ...metadata, thread_id: selected.id, resolved: false, pinned: false, status: "tentative" }) }); newID.current = ""; form.reset(); await loadRelated("search"); setNotice("Reply posted."); });
          }}>
            <label>Reply <textarea name="reply" rows={3} required /></label><button>Post reply</button>
          </form>
          <button type="button" onClick={() => begin("lesson", { ...metadata, resolved: false, pinned: false, thread_id: selected.id, evidence: [...strings(metadata.evidence), `content:${selected.id}@${selected.revision}`], status: "tentative" })}>Save conclusion</button>
        </section>}

        </>}
        {panel !== "attach" ? null : <>
        <h4>Use in a task</h4><form aria-label="Attach to a task" onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("attach", { task_id: data.get("task"), content_id: selected.id, content_revision: selected.revision }); setPanel("read"); setNotice("Attached to the task."); }); }}>
          <label>Attach to queued task <select name="task" autoFocus required defaultValue=""><option value="" disabled>Select task</option>{[...(state?.tasks.values() ?? [])].filter((task) => task.project_id === projectID && task.status === "queued").map((task) => <option key={task.id} value={task.id}>{task.title}</option>)}</select></label><button>Attach revision</button>
        </form>
        </>}
        {panel !== "attach" || draft === undefined ? null : <>
        <h4>Draft a new task</h4><form aria-label="New task" onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const agent = state?.agents.get(String(data.get("agent"))); if (agent !== undefined) draft?.(agent, `Use project library ${selected.id} revision ${selected.revision} (${selected.title}) if relevant. Read its body lazily.\n\n${String(data.get("instruction"))}`); }}>
          <label>Agent <select name="agent" required defaultValue=""><option value="" disabled>Select agent</option>{[...(state?.agents.values() ?? [])].filter((agent) => agent.project_id === projectID && !agent.archived).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
          <label>Instruction <textarea name="instruction" required /></label><button disabled={draft === undefined}>Open task draft</button>
        </form>
        </>}
        {panel !== "sources" ? null : <>
        <div className="dfProjectLibrary__actions"><button type="button" disabled={!editable} onClick={() => { setNewKind(""); setEditing(true); }}>Edit sources</button><button type="button" onClick={() => { setPanel("accesses"); void run(() => loadRelated("accesses")); }}>Task access</button></div>
        {text(selected.description) && text(selected.description) !== text(selected.title) ? <p>{text(selected.description)}</p> : null}
        <p>Revision {String(selected.revision)}{text(selected.author) ? ` · ${text(selected.author)}` : ""}</p>
        {text(metadata.scope) ? <p>Scope: {text(metadata.scope)}</p> : null}
        {text(selected.repository_id) ? <p>Repository: <code>{text(selected.repository_id)}</code></p> : null}
        {text(selected.commit) === "" ? null : <p>Document source: <code>{text(selected.commit)}</code> · <code>{text(selected.path)}</code></p>}
        {Object.keys(metadata).length === 0 ? text(selected.source_references) ? <p>{text(selected.source_references)}</p> : null : <>
          {["source_revision", "branch", "environment"].filter((key) => text(metadata[key])).map((key) => <p key={key}>{key.replaceAll("_", " ")}: <code>{text(metadata[key])}</code></p>)}
          {strings(metadata.entities).map((ref, index) => <p key={ref}><button type="button" aria-label={`View source ${index + 1}`} disabled={!onSource} onClick={() => onSource?.(ref)}>View source{strings(metadata.entities).length > 1 ? ` ${index + 1}` : ""}</button> <code>{ref}</code></p>)}
          {strings(metadata.evidence).map((reference) => <p key={reference}>Evidence: {reference}</p>)}
          {text(metadata.record_id) ? <p><button type="button" disabled={!onRecord} onClick={() => void run(async () => { await onRecord?.(text(metadata.record_type), text(metadata.record_id), projectID); })}>View linked {text(metadata.record_type) || "record"}</button> <code>{text(metadata.record_id)}</code></p> : null}
          {text(metadata.task_id) && !(metadata.record_type === "task" && metadata.record_id === metadata.task_id) ? <p><button type="button" disabled={!onRecord} onClick={() => void run(async () => { await onRecord?.("task", text(metadata.task_id), projectID); })}>View task</button> <code>{text(metadata.task_id)}</code></p> : null}
          {text(metadata.supersedes) ? <p><button type="button" onClick={() => void run(() => read(text(metadata.supersedes)))}>Earlier document</button> <code>{text(metadata.supersedes)}</code></p> : null}
          {threadID && !discussion ? <p><button type="button" onClick={() => void run(() => read(threadID))}>View discussion</button> <code>{threadID}</code></p> : null}
        </>}
        {selected.kind === "discussion_reply" && complete && !selected.deprecated && selected.revision === selected.latest_revision ? <button type="button" onClick={() => void run(retire)}>Retire reply</button> : null}
        <form className="dfProjectLibrary__revision" aria-label="Read a revision" onSubmit={(event) => { event.preventDefault(); const revision = Number(new FormData(event.currentTarget).get("revision")); if (Number.isSafeInteger(revision) && revision > 0 && revision <= Number(selected.latest_revision)) void run(() => read(text(selected.id), revision)); }}><label>Revision <input name="revision" type="number" min="1" max={Number(selected.latest_revision)} defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} required /></label><button>Read revision</button></form>
        </>}
        {panel !== "accesses" ? null : <><h4>Task access</h4><p>Recorded deliveries and reads of this revision.</p>
        {accesses.map((item, index) => <p key={index}>Revision {String(item.content_revision ?? item.revision)} · {text(item.kind) || text(item.access_kind)} · Task {text(item.task_id)} · Run {text(item.run_id)} · Bytes {String(item.offset ?? 0)}–{Number(item.offset ?? 0) + Number(item.byte_length ?? 0)} · {String(item.created_at_ms ?? item.at_ms ?? "")}</p>)}
        {accessNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("accesses", accessNext))}>More task access</button>}
        {!pending && !error && accesses.length === 0 ? <p>No task access recorded.</p> : null}</>}
        {["attach", "draft"].includes(panel) ? <button type="button" onClick={() => setPanel("read")}>Cancel</button> : null}
        </div>
      </section>}
      {!editing ? null : <form aria-label="Knowledge editor" key={newKind ? newID.current : `${selected?.id}:${selected?.revision}`} onSubmit={(event) => {
        event.preventDefault(); const data = new FormData(event.currentTarget);
        const details: Record<string, unknown> = { ...editorMeta };
        const kind = newKind ? String(data.get("kind") || newKind) : editorKind;
        if (editingSources) {
          Object.assign(details, { scope: data.get("scope"), evidence: lines(data.get("evidence")), entities: lines(data.get("entities")), mentions: lines(data.get("mentions")) });
          for (const key of ["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"]) { const value = text(data.get(key)); if (value) details[key] = value; else delete details[key]; }
        } else if (kinds.includes(kind)) details.status = data.get("status") || editorMeta.status || "tentative";
        if (newKind) { details.scope ??= kind === "project_brief" ? "project" : "repository"; if (["decision", "lesson", "project_brief"].includes(kind)) details.evidence = lines(data.get("evidence")); }
        const updatedBody = editingSources ? body : String(data.get("body"));
        void run(async () => { const value = await request(newKind ? "create" : "revise", { id: newKind ? newID.current : selected?.id, expected_revision: newKind ? 0 : selected?.revision, repository_id: editingSources ? data.get("repository_id") : newKind && !seed.thread_id ? filters.repository_id : selected?.repository_id, kind, title: editingSources ? selected?.title : data.get("title"), description: editingSources ? data.get("description") : newKind ? "" : selected?.description, body: updatedBody, source_references: (kinds.includes(kind) && (kind !== "procedure" || Boolean(newKind) || Object.keys(metadata).length > 0)) ? JSON.stringify(details) : editingSources ? data.get("source_references") : selected?.source_references }); selectRevision(value, updatedBody); setNotice("Saved."); await list(); });
      }}>
        <h3>{editingSources ? "Edit sources" : newKind === "discussion" ? "Start a thread" : newKind === "lesson" && seed.thread_id ? "Save conclusion" : newKind ? "New document" : "Edit document"}</h3>
        {newKind === "discussion" ? <p>Anyone with access to this project can read and reply. Posting does not assign work or notify an agent; use Work to assign work.</p> : null}
        {newKind === "lesson" && seed.thread_id ? <p>Saved to the Library with this thread as evidence. The thread and its history stay on the Board.</p> : null}
        {editingSources ? null : <>
          {!newKind || newKind === "discussion" ? <p>{kindLabel(editorKind)}</p> : <label>Category <select name="kind" defaultValue={editorKind} onChange={(event) => setNewKind(event.target.value)}>{documentKinds.map(kind => <option key={kind} value={kind}>{kindLabel(kind)}</option>)}</select></label>}
          <label>Title <input name="title" autoFocus defaultValue={newKind ? newKind === "lesson" && seed.thread_id ? `Conclusion: ${text(selected?.title)}` : "" : text(selected?.title)} required /></label>
          <label>Text <textarea name="body" rows={12} defaultValue={newKind ? "" : body} required /></label>
          {!newKind || !["decision", "lesson", "project_brief"].includes(editorKind) ? null : <label>Supporting references <textarea name="evidence" defaultValue={strings(editorMeta.evidence).join("\n")} required={editorKind !== "project_brief"} /></label>}
          {!structured ? null : <label>Status <select name="status" defaultValue={text(editorMeta.status) || "tentative"}>{["tentative", "current", "needs_revalidation", "superseded"].map((status) => <option key={status} value={status}>{status.replaceAll("_", " ")}</option>)}</select></label>}
        </>}
        {!editingSources ? null : <>
        <label>Supporting evidence (one reference per line) <textarea name="evidence" defaultValue={strings(editorMeta.evidence).join("\n")} /></label>
        <fieldset className="dfProjectLibrary__fields"><legend>Scope and sources</legend>
        <label>Repository ID (blank uses the project default) <input name="repository_id" defaultValue={newKind && newKind !== "discussion_reply" && !text(seed.thread_id) ? filters.repository_id : text(selected?.repository_id) || filters.repository_id} /></label>
        <label>Description <textarea name="description" defaultValue={newKind ? "" : text(selected?.description)} /></label>
        <label>Scope <select name="scope" defaultValue={text(editorMeta.scope) || (editorKind === "project_brief" ? "project" : "repository")}><option value="repository">This repository</option><option value="project">Whole project</option></select></label>
        <label>Code entities (one stable reference per line) <textarea name="entities" defaultValue={strings(editorMeta.entities).join("\n")} /></label>
        <label>Mentions (one agent ID per line) <textarea name="mentions" defaultValue={strings(editorMeta.mentions).join("\n")} /></label>
        {["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"].map((key) => <label key={key}>{key.replaceAll("_", " ")} <input name={key} defaultValue={text(editorMeta[key])} /></label>)}<label>Source references for legacy documents <textarea name="source_references" defaultValue={newKind ? "" : text(selected?.source_references)} /></label></fieldset></>}
        <div className="dfProjectLibrary__actions"><button>Save</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
        {newKind || !selected ? null : <button type="button" disabled={!editable} onClick={() => void run(retire)}>Retire document</button>}</div>
      </form>}
      </div>
      </div>
    </fieldset>
    {error === "" ? null : <p role="alert">{error}</p>}{notice === "" ? null : <p role="status">{notice}</p>}
  </>;
}
