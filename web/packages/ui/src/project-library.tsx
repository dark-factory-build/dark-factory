import { ProjectOutcomes } from "./project-outcomes.js";
import { useRef, useState } from "react";
import type { AgentItem, ProjectContentInput, ProjectContentOperation, ProjectContentOutput, StateView } from "@dark-factory/client";

export type ProjectContentCall = (operation: ProjectContentOperation, input: ProjectContentInput) => Promise<ProjectContentOutput>;
type RecordValue = Readonly<Record<string, unknown>>;
const text = (value: unknown): string => typeof value === "string" ? value : "";
const rows = (value: unknown): RecordValue[] => Array.isArray(value) ? value as RecordValue[] : [];
const id = (): string => crypto.randomUUID().replaceAll("-", "");

/** All reads start with an explicit gesture; mounting never loads the library. */
export function ProjectLibrary({ state, call, draft }: { state?: StateView; call?: ProjectContentCall; draft?: (agent: AgentItem, instruction: string) => void }) {
  const [project, setProject] = useState("");
  const [items, setItems] = useState<RecordValue[]>([]);
  const [next, setNext] = useState(0);
  const [selected, setSelected] = useState<RecordValue>();
  const [body, setBody] = useState("");
  const [bodyNext, setBodyNext] = useState<number>();
  const [complete, setComplete] = useState(false);
  const [evidence, setEvidence] = useState<RecordValue[]>([]);
  const [evidenceNext, setEvidenceNext] = useState(0);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState(false);
  const epoch = useRef(0);
  const evidenceID = useRef("");
  const newID = useRef<string | undefined>(undefined);
  const projects = [...(state?.projects.values() ?? [])];
  const projectID = project || projects[0]?.id || "";
  const run = async (work: () => Promise<void>) => {
    setPending(true); setError(""); setNotice("");
    try { await work(); } catch (error) { setError(error instanceof Error ? error.message : "Request failed"); }
    finally { setPending(false); }
  };
  const request = (operation: ProjectContentOperation, input: ProjectContentInput = {}) => {
    if (call === undefined || projectID === "") return Promise.reject(new Error("Connect and select a project"));
    return call(operation, { ...input, project_id: projectID });
  };
  const list = async (offset = 0) => {
    const generation = epoch.current;
    const result = await request("list", { offset, limit: 1 });
    if (generation !== epoch.current) return;
    setItems(rows(result.items)); setNext(Number(result.next_offset ?? 0));
  };
  const read = async (contentID: string, revision = 0) => {
    const generation = ++epoch.current;
    const result = await request("read", { id: contentID, revision });
    if (generation !== epoch.current) return;
    evidenceID.current = id();
    setSelected(result); setBody(""); setBodyNext(0); setComplete(false); setEvidence([]); setEvidenceNext(0); setEditing(false);
  };
  const loadBody = async () => {
    if (selected === undefined || bodyNext === undefined) return;
    const generation = epoch.current;
    const result = await request("body", { id: selected.id, revision: selected.revision, offset: bodyNext, limit: 8192 });
    if (generation !== epoch.current) return;
    setBody((value) => value + text(result.body)); setComplete(result.complete === true);
    setBodyNext(result.complete === true ? undefined : Number(result.next_offset));
  };
  const loadEvidence = async (offset = 0) => {
    if (selected === undefined) return;
    const generation = epoch.current;
    const result = await request("evidence_list", { content_id: selected.id, content_revision: selected.revision, offset, limit: 1 });
    if (generation !== epoch.current) return;
    setEvidence(rows(result.items)); setEvidenceNext(Number(result.next_offset ?? 0));
  };
  return <details className="dfConsoleSidebar__panel dfProjectLibrary">
    <summary>Project library &amp; outcomes</summary>
    <p>Reusable instructions and test procedures for agents. A task keeps the exact version you attach.</p>
    <label>Project <select value={projectID} disabled={pending} onChange={(event) => { epoch.current++; setProject(event.target.value); setItems([]); setSelected(undefined); setEditing(false); setNext(0); }}>
      {projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    <fieldset disabled={pending || call === undefined || projectID === ""}>
      <button type="button" onClick={() => void run(() => list())}>Browse library</button>
      <button type="button" onClick={() => { newID.current = id(); setSelected(undefined); setBody(""); setComplete(true); setEditing(true); }}>New document</button>
      {items.map((item) => <button type="button" key={text(item.id)} onClick={() => void run(() => read(text(item.id), Number(item.revision)))}>{text(item.title)} · {text(item.kind)} · r{String(item.revision)}{item.deprecated === true ? " · deprecated" : ""}</button>)}
      {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next))}>Next documents</button>}
      {selected === undefined ? null : <section aria-label="Selected library revision">
        <h3>{text(selected.title)}</h3>
        <p>{text(selected.description)}</p>
        <p>Revision {String(selected.revision)} · {selected.deprecated === true ? "Deprecated" : Number(selected.revision) < Number(selected.latest_revision) ? "Superseded" : "Current"} · {text(selected.author)}</p>
        <p>{text(selected.source_references)}</p>
        <label>Read revision <input type="number" min="1" max={Number(selected.latest_revision)} defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} onBlur={(event) => { const revision = Number(event.target.value); if (Number.isSafeInteger(revision) && revision > 0 && revision !== selected.revision) void run(() => read(text(selected.id), revision)); }} /></label>
        <pre>{body}</pre>
        {complete ? null : <button type="button" onClick={() => void run(loadBody)}>{bodyNext === 0 ? "Read body" : "Read next body page"}</button>}
        <button type="button" disabled={!complete || selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => setEditing(true)}>Revise document</button>
        <button type="button" disabled={selected.deprecated === true || selected.revision !== selected.latest_revision} onClick={() => void run(async () => { const value = await request("deprecate", { id: selected.id, expected_revision: selected.revision }); setSelected(value); setNotice("Deprecated. Tasks already using it are unchanged."); })}>Deprecate</button>
        <button type="button" onClick={() => void run(() => loadEvidence())}>Test results</button>
        {evidence.map((item) => <article key={text(item.id)}><p>{text(item.result)} · {text(item.judgment)}</p><p>Source: {text(item.tested_source)} · Environment: {text(item.environment)}</p><p>Evidence: {text(item.location)} · Evaluator: {text(item.evaluator)}</p></article>)}
        {evidenceNext === 0 ? null : <button type="button" onClick={() => void run(() => loadEvidence(evidenceNext))}>Next evidence</button>}
        <details><summary>Record a test result</summary><form onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await request("evidence", { id: data.get("id"), content_id: selected.id, content_revision: selected.revision, tested_source: data.get("tested_source"), environment: data.get("environment"), result: data.get("result"), location: data.get("location"), judgment: data.get("judgment") }); evidenceID.current = id(); setNotice("Test result saved."); }); }}>
          <input type="hidden" name="id" value={evidenceID.current} />
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
      {!editing ? null : <form key={`${selected?.id ?? "new"}:${selected?.revision ?? 0}`} onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const contentID = selected?.id ?? newID.current; void run(async () => { const value = await request(selected === undefined ? "create" : "revise", { id: contentID, expected_revision: selected?.revision ?? 0, kind: data.get("kind"), title: data.get("title"), description: data.get("description"), body: data.get("body"), source_references: data.get("source_references") }); setSelected(value); setBody(String(data.get("body"))); setComplete(true); setEditing(false); setNotice("Saved as a new version."); }); }}>
        <label>Kind <input name="kind" defaultValue={text(selected?.kind) || "procedure"} required maxLength={64} list="df-library-kinds" /></label><datalist id="df-library-kinds"><option value="procedure" /><option value="acceptance_scenario" /></datalist>
        <label>Title <input name="title" defaultValue={text(selected?.title)} required /></label>
        <label>Description <textarea name="description" defaultValue={text(selected?.description)} /></label>
        <label>Source references <textarea name="source_references" defaultValue={text(selected?.source_references)} /></label>
        <label>Body <textarea name="body" rows={12} defaultValue={body} required /></label>
        <button>Save revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
    </fieldset>
    <ProjectOutcomes key={projectID} project={projectID} call={call} />
    {error === "" ? null : <p role="alert">{error}</p>}{notice === "" ? null : <p role="status">{notice}</p>}
  </details>;
}
