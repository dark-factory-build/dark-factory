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
const documentState = (item: RecordValue, metadata = knowledgeMetadata(item.source_references)): string => {
  const version = item.deprecated === true ? "Deprecated" : Number(item.revision) < Number(item.latest_revision) ? "Older revision" : "";
  const status = text(item.projected_status || metadata.status);
  return [version, status === "current" && version ? "" : status.replaceAll("_", " "), item.kind === "discussion" ? metadata.resolved === true ? "resolved" : "open" : "", metadata.pinned === true ? "pinned" : ""].filter(Boolean).join(" · ");
};

type LibraryProps = {
  state?: StateView; call?: ProjectContentCall; draft?: (agent: AgentItem, instruction: string) => void;
  board?: boolean; entity?: string; repository?: string; initialID?: string; onSource?: (entity: string) => void; onRecord?: (kind: string, id: string, project: string) => void | Promise<void>;
};

/** An intentional entry loads metadata; an unused Settings panel stays lazy. */
export function ProjectLibrary({ state, call, draft, board = false, entity = "", repository = "", initialID = "", onSource, onRecord, open = false, initialProjectId = "" }: LibraryProps & { open?: boolean; initialProjectId?: string }) {
  const [expanded, setExpanded] = useState(false);
  const [project, setProject] = useState(initialProjectId);
  const projects = [...(state?.projects.values() ?? [])];
  const projectID = projects.find((item) => item.id === project)?.id ?? projects[0]?.id ?? "";
  const scoped = projectID === (initialProjectId || projects[0]?.id);
  const contents = <>
    {projects.length < 2 ? null : <label>Project <select value={projectID} onChange={(event) => setProject(event.target.value)}>{projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    <LibraryDocuments key={`${projectID}:${board}:${scoped ? `${entity}:${repository}:${initialID}` : ""}`} projectID={projectID} state={state} call={call} draft={draft} board={board} entity={scoped ? entity : ""} repository={scoped ? repository : ""} initialID={scoped ? initialID : ""} onSource={onSource} onRecord={onRecord} />
  </>;
  return open || board || initialID ? <section className="dfProjectLibrary" aria-label={board ? "Project board" : "Project library"}>{contents}</section>
    : <details className="dfConsoleSidebar__panel dfProjectLibrary" onToggle={(event) => setExpanded(event.currentTarget.open)}><summary>Project library</summary>{expanded ? contents : null}</details>;
}

function LibraryDocuments({ state, call, draft, projectID, board = false, entity = "", repository = "", initialID = "", onSource, onRecord }: LibraryProps & { projectID: string }) {
  const [items, setItems] = useState<RecordValue[]>([]), [next, setNext] = useState(0);
  const [selected, setSelected] = useState<RecordValue>();
  const [loaded, setLoaded] = useState(false);
  const [body, setBody] = useState(""), [bodyNext, setBodyNext] = useState<number>(0), [complete, setComplete] = useState(false);
  const [evidence, setEvidence] = useState<RecordValue[]>([]), [evidenceNext, setEvidenceNext] = useState(0);
  const [replies, setReplies] = useState<RecordValue[]>([]), [replyNext, setReplyNext] = useState(0);
  const [accesses, setAccesses] = useState<RecordValue[]>([]), [accessNext, setAccessNext] = useState(0);
  const [pending, setPending] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [editing, setEditing] = useState(false), [newKind, setNewKind] = useState(""), [seed, setSeed] = useState<RecordValue>({});
  const [filters, setFilters] = useState({ query: "", repository_id: repository, branch: "", environment: "", kind: board ? "discussion" : "", entity });
  const epoch = useRef(0), newID = useRef(""), evidenceID = useRef(id()), listQuery = useRef<ProjectContentInput>({});
  const [showResolved, setShowResolved] = useState(false);
  const metadata = knowledgeMetadata(selected?.source_references);
  const discussion = selected?.kind === "discussion";
  const threadID = discussion ? text(selected?.id) : text(metadata.thread_id);
  const run = async (work: () => Promise<void>) => {
    const generation = ++epoch.current;
    setPending(true); setError(""); setNotice("");
    try { await work(); } catch (error) { if (generation === epoch.current) setError(error instanceof Error ? error.message : "Request failed"); }
    finally { if (generation === epoch.current) setPending(false); }
  };
  const request = (operation: ProjectContentOperation, input: ProjectContentInput = {}) => {
    if (call === undefined || projectID === "") return Promise.reject(new Error("Connect and select a project"));
    return call(operation, { ...input, project_id: projectID });
  };
  const list = async (offset = 0) => {
    const generation = epoch.current;
    if (offset === 0) listQuery.current = { ...filters, open_only: board && !showResolved };
    const result = await request("search", { ...listQuery.current, offset, limit: 4 });
    if (generation !== epoch.current) return;
    setItems((existing) => [...new Map([...(offset === 0 ? [] : existing), ...rows(result.items)].map((item) => [text(item.id), item])).values()]);
    setNext(Number(result.next_offset ?? 0)); setLoaded(true);
  };
  const selectRevision = (value: RecordValue, loadedBody?: string) => {
    evidenceID.current = id();
    setSelected(value); setBody(loadedBody ?? ""); setBodyNext(0); setComplete(loadedBody !== undefined); setEvidence([]); setEvidenceNext(0); setReplies([]); setReplyNext(0); setAccesses([]); setAccessNext(0); setEditing(false);
  };
  const read = async (contentID: string, revision?: number) => {
    const generation = epoch.current;
    let result = await request("read", { id: contentID, revision: revision ?? 1 });
    if (generation !== epoch.current) return;
    if (revision === undefined && Number(result.latest_revision) > 1) result = await request("read", { id: contentID, revision: Number(result.latest_revision) });
    if (generation !== epoch.current) return;
    selectRevision(result);
    await loadBody(result, 0);
  };
  const available = call !== undefined && projectID !== "";
  useEffect(() => {
    if (available) void run(() => initialID ? read(initialID) : list());
    return () => { epoch.current++; };
  }, [available, initialID]);
  const loadBody = async (content = selected, offset = bodyNext) => {
    if (content === undefined) return;
    const generation = epoch.current;
    const result = await request("body", { id: content.id, revision: content.revision, offset, limit: 8192 });
    if (generation !== epoch.current) return;
    setBody((value) => offset === 0 ? text(result.body) : value + text(result.body)); setComplete(result.complete === true); setBodyNext(Number(result.next_offset ?? 0));
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
    selectRevision(value, body); setNotice("Discussion updated.");
  };
  const entry = (item: RecordValue) => {
    const meta: RecordValue = { ...knowledgeMetadata(item.source_references), status: item.projected_status || knowledgeMetadata(item.source_references).status };
    return <button type="button" data-knowledge-status={text(meta.status)} key={text(item.id)} aria-pressed={selected?.id === item.id && selected?.revision === item.revision} onClick={() => void run(() => read(text(item.id), Number(item.revision)))}><strong>{text(item.title)}</strong><span>{text(item.kind).replaceAll("_", " ")}{documentState(item, meta) ? ` · ${documentState(item, meta)}` : ""}</span></button>;
  };
  const editorKind = newKind || text(selected?.kind);
  const editorMeta = newKind ? seed : metadata;
  return <>
    <fieldset disabled={pending || !available} aria-busy={pending}>
      <details><summary>Search &amp; filters</summary><form onSubmit={(event) => { event.preventDefault(); void run(() => list()); }}>
        <label>Search knowledge <input type="search" value={filters.query} onChange={(event) => setFilters({ ...filters, query: event.target.value })} /></label>
        <label>Category <select value={filters.kind} onChange={(event) => setFilters({ ...filters, kind: event.target.value })}><option value="">All knowledge</option>{kinds.map((kind) => <option key={kind} value={kind}>{kind.replaceAll("_", " ")}</option>)}</select></label>
        {(["repository_id", "branch", "environment", "entity"] as const).map((key) => <label key={key}>{key.replaceAll("_", " ")} <input value={filters[key]} onChange={(event) => setFilters({ ...filters, [key]: event.target.value })} /></label>)}
        <label><input type="checkbox" checked={showResolved} onChange={(event) => setShowResolved(event.target.checked)} /> Include resolved discussions</label>
        <button>Search</button>
      </form></details>
      <div className="dfProjectLibrary__browser">
      <nav className="dfProjectLibrary__documents" aria-label={board ? "Discussions" : "Library documents"}>
        <button type="button" onClick={() => void run(() => list())}>{board ? "Refresh discussions" : "Refresh documents"}</button>
        <button type="button" onClick={() => begin(board ? "discussion" : "observation", { status: "tentative", entities: filters.entity ? [filters.entity] : [] })}>{board ? "New discussion" : "New document"}</button>
        {items.filter((item) => !board || showResolved || !knowledgeMetadata(item.source_references).resolved).map(entry)}
        {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next))}>Load more documents</button>}
      </nav>
      <div className="dfProjectLibrary__reader">
      {selected !== undefined || editing ? null : <p>{!available ? "Connect to read project documents." : !loaded ? "Loading documents…" : items.length === 0 ? "No documents yet. Add project guidance or a discussion." : "Select a document to read it."}</p>}
      {selected === undefined ? null : <section aria-label="Selected library revision">
        <h3>{text(selected.title)}</h3>
        {documentState(selected, metadata) ? <p data-knowledge-status={text(selected.projected_status || metadata.status)}>{documentState(selected, metadata)}</p> : null}
        <pre>{body}</pre>
        {complete ? null : <button type="button" onClick={() => void run(() => loadBody())}>{bodyNext === 0 ? "Retry document text" : "Read more"}</button>}
        {!discussion ? null : <div aria-label="Thread actions">
          <button type="button" onClick={() => void run(() => loadRelated("search"))}>Read replies</button>
          <button type="button" onClick={() => begin("discussion_reply", { ...metadata, thread_id: selected.id, resolved: false, pinned: false, status: "tentative" })}>Reply</button>
          {replies.map(entry)}{replyNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("search", replyNext))}>Next replies</button>}
        </div>}
        <details><summary>Use in a task</summary>
        <form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("attach", { task_id: data.get("task"), content_id: selected.id, content_revision: selected.revision }); setNotice("Attached to the task."); }); }}>
          <label>Attach to queued task <select name="task" required defaultValue=""><option value="" disabled>Select task</option>{[...(state?.tasks.values() ?? [])].filter((task) => task.project_id === projectID && task.status === "queued").map((task) => <option key={task.id} value={task.id}>{task.title}</option>)}</select></label><button>Attach revision</button>
        </form>
        <form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const agent = state?.agents.get(String(data.get("agent"))); if (agent !== undefined) draft?.(agent, `Use project library ${selected.id} revision ${selected.revision} (${selected.title}) if relevant. Read its body lazily.\n\n${String(data.get("instruction"))}`); }}>
          <label>Draft ordinary work <select name="agent" required defaultValue=""><option value="" disabled>Select agent</option>{[...(state?.agents.values() ?? [])].filter((agent) => agent.project_id === projectID && !agent.archived).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select></label>
          <label>Instruction <textarea name="instruction" required /></label><button disabled={draft === undefined}>Open task draft</button>
        </form>
        </details>
        <details><summary>Sources &amp; access</summary>
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
        <button type="button" onClick={() => void run(() => loadRelated("accesses"))}>Task access</button>
        {accesses.map((item, index) => <p key={index}>Revision {String(item.content_revision ?? item.revision)} · {text(item.kind) || text(item.access_kind)} · Task {text(item.task_id)} · Run {text(item.run_id)} · Bytes {String(item.offset ?? 0)}–{Number(item.offset ?? 0) + Number(item.byte_length ?? 0)} · {String(item.created_at_ms ?? item.at_ms ?? "")}</p>)}
        {accessNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("accesses", accessNext))}>More task access</button>}
        <button type="button" onClick={() => void run(() => loadRelated("evidence_list"))}>Test results</button>
        {evidence.map((item) => <article key={text(item.id)}><p>{text(item.result)} · {text(item.judgment)}</p><p>Source: {text(item.tested_source)} · Environment: {text(item.environment)}</p><p>Evidence: {text(item.location)} · Evaluator: {text(item.evaluator)}</p></article>)}
        {evidenceNext === 0 ? null : <button type="button" onClick={() => void run(() => loadRelated("evidence_list", evidenceNext))}>Next evidence</button>}
        </details>
        <details><summary>Manage</summary>
        <label>Read revision <input type="number" min="1" max={Number(selected.latest_revision)} defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} onBlur={(event) => { const revision = Number(event.target.value); if (Number.isSafeInteger(revision) && revision > 0 && revision !== selected.revision) void run(() => read(text(selected.id), revision)); }} /></label>
        <button type="button" disabled={!complete || selected.kind === "discussion_reply" || selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => { setNewKind(""); setEditing(true); }}>Revise document</button>
        <button type="button" disabled={selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => void run(async () => { selectRevision(await request("deprecate", { id: selected.id, expected_revision: selected.revision }), complete ? body : undefined); setNotice("Deprecated. Existing task references remain."); })}>Deprecate</button>
        {!discussion ? null : <div>
          <button type="button" disabled={!complete || selected.revision !== selected.latest_revision} onClick={() => void run(() => updateThread({ resolved: !metadata.resolved }))}>{metadata.resolved ? "Reopen discussion" : "Resolve discussion"}</button>
          <button type="button" disabled={!complete || selected.revision !== selected.latest_revision} onClick={() => void run(() => updateThread({ pinned: !metadata.pinned }))}>{metadata.pinned ? "Unpin discussion" : "Pin discussion"}</button>
          <button type="button" onClick={() => begin("lesson", { ...metadata, resolved: false, pinned: false, thread_id: selected.id, evidence: [...strings(metadata.evidence), `content:${selected.id}@${selected.revision}`], status: "tentative" })}>Retain conclusion</button>
        </div>}
        <h4>Record a test result</h4><form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("evidence", { id: evidenceID.current, content_id: selected.id, content_revision: selected.revision, tested_source: data.get("tested_source"), environment: data.get("environment"), result: data.get("result"), location: data.get("location"), judgment: data.get("judgment") }); evidenceID.current = id(); setNotice("Test result saved."); }); }}>
          <label>Tested source <input name="tested_source" required /></label><label>Environment <input name="environment" required /></label><label>Observed result <select name="result"><option>passed</option><option>failed</option><option>incomplete</option><option>not_run</option></select></label><label>Existing report or result reference <input name="location" required /></label><label>Judgment <textarea name="judgment" /></label><button>Record observation</button>
        </form>
        </details>
      </section>}
      {!editing ? null : <form aria-label="Knowledge editor" key={newKind ? newID.current : `${selected?.id}:${selected?.revision}`} onSubmit={(event) => {
        event.preventDefault(); const data = new FormData(event.currentTarget);
        const details: Record<string, unknown> = { ...editorMeta, status: data.get("status"), scope: data.get("scope"), evidence: lines(data.get("evidence")), entities: lines(data.get("entities")), mentions: lines(data.get("mentions")) };
        for (const key of ["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"]) { const value = text(data.get(key)); if (value) details[key] = value; else delete details[key]; }
        void run(async () => { const value = await request(newKind ? "create" : "revise", { id: newKind ? newID.current : selected?.id, expected_revision: newKind ? 0 : selected?.revision, repository_id: data.get("repository_id"), kind: data.get("kind"), title: data.get("title"), description: data.get("description"), body: data.get("body"), source_references: (kinds.includes(String(data.get("kind"))) && (data.get("kind") !== "procedure" || Boolean(newKind) || Object.keys(metadata).length > 0)) ? JSON.stringify(details) : data.get("source_references") }); selectRevision(value, String(data.get("body"))); setNotice("Revision saved."); });
      }}>
        <label>Kind <input name="kind" defaultValue={editorKind} required maxLength={64} list="df-library-kinds" /></label><datalist id="df-library-kinds">{kinds.map((kind) => <option key={kind} value={kind} />)}<option value="acceptance_scenario" /></datalist>
        <label>Title <input name="title" defaultValue={newKind ? "" : text(selected?.title)} required /></label>
        <label>Body <textarea name="body" rows={12} defaultValue={newKind ? "" : body} required /></label>
        <label>Status <select name="status" defaultValue={text(editorMeta.status) || "tentative"}>{["tentative", "current", "needs_revalidation", "superseded"].map((status) => <option key={status} value={status}>{status.replaceAll("_", " ")}</option>)}</select></label>
        <label>Supporting evidence (one reference per line) <textarea name="evidence" defaultValue={strings(editorMeta.evidence).join("\n")} /></label>
        <details><summary>Metadata &amp; links</summary>
        <label>Repository ID (blank uses the project default) <input name="repository_id" defaultValue={newKind && newKind !== "discussion_reply" && !text(seed.thread_id) ? filters.repository_id : text(selected?.repository_id) || filters.repository_id} /></label>
        <label>Description <textarea name="description" defaultValue={newKind ? "" : text(selected?.description)} /></label>
        <label>Scope <select name="scope" defaultValue={text(editorMeta.scope) || (editorKind === "project_brief" ? "project" : "repository")}><option value="repository">This repository</option><option value="project">Whole project</option></select></label>
        <label>Code entities (one stable reference per line) <textarea name="entities" defaultValue={strings(editorMeta.entities).join("\n")} /></label>
        <label>Mentions (one agent ID per line) <textarea name="mentions" defaultValue={strings(editorMeta.mentions).join("\n")} /></label>
        {["source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "supersedes"].map((key) => <label key={key}>{key.replaceAll("_", " ")} <input name={key} defaultValue={text(editorMeta[key])} /></label>)}<label>Source references for legacy documents <textarea name="source_references" defaultValue={newKind ? "" : text(selected?.source_references)} /></label></details>
        <button>Save revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
      </div>
      </div>
    </fieldset>
    {board ? null : <ProjectOutcomes key={projectID} project={projectID} call={call} />}
    {error === "" ? null : <p role="alert">{error}</p>}{notice === "" ? null : <p role="status">{notice}</p>}
  </>;
}
