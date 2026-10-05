import { useRef, useState } from "react";
import type { ProjectContentCall } from "./project-library.js";

type Value = Record<string, unknown>;
const string = (value: unknown): string => typeof value === "string" ? value : "";
const records = (value: unknown): Value[] => Array.isArray(value) ? value as Value[] : [];
/** Outcomes are explicit human judgments over existing work, never task status. */
export function ProjectOutcomes({ project, call }: { project: string; call?: ProjectContentCall }) {
  const newID = useRef<string | undefined>(undefined);
  const [items, setItems] = useState<Value[]>([]);
  const [next, setNext] = useState(0);
  const [selected, setSelected] = useState<Value>();
  const [editing, setEditing] = useState(false);
  const [kind, setKind] = useState("outcome");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const document = (selected?.document ?? {}) as Value;
  const run = async (work: () => Promise<void>) => { setPending(true); setError(""); try { await work(); } catch (error) { setError(error instanceof Error ? error.message : "Request failed"); } finally { setPending(false); } };
  const request = (operation: "outcome_list" | "outcome_read" | "outcome_write", input: Value) => call === undefined ? Promise.reject(new Error("Connect to read outcomes")) : call(operation, { ...input, project_id: project });
  const list = async (offset = 0) => { const value = await request("outcome_list", { offset, limit: 1 }); setItems(records(value.items)); setNext(Number(value.next_offset ?? 0)); };
  const read = async (id: string, revision = 0) => { const value = await request("outcome_read", { id, revision }); setSelected(value); setEditing(false); setKind(string((value.document as Value)?.kind)); };
  return <details><summary>Outcomes &amp; milestones</summary>
    <p>Successful work is not acceptance.</p>
    <fieldset disabled={pending || call === undefined || project === ""}>
      <button type="button" onClick={() => void run(() => list())}>Browse outcomes</button>
      <button type="button" onClick={() => { newID.current = crypto.randomUUID().replaceAll("-", ""); setSelected(undefined); setKind("outcome"); setEditing(true); }}>New outcome</button>
      {items.map((item) => <button type="button" key={string(item.id)} onClick={() => void run(() => read(string(item.id)))}>{string(item.objective)} · {string(item.state)}{item.stale === true ? " · stale" : ""}</button>)}
      {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next))}>Next outcomes</button>}
      {selected === undefined ? null : <article>
        <h3>{string(document.objective)}</h3><p>Criteria: {string(document.criteria)}</p>
        <p>{string(document.state)} · r{String(selected.revision)} · {string(selected.author)} ({string(selected.authority)})</p>
        {selected.stale === true ? <p role="status">Objective changed: this revision does not establish current acceptance.</p> : null}
        {records(selected.missing_references).length === 0 && !Array.isArray(selected.missing_references) ? null : <p>Missing references: {Array.isArray(selected.missing_references) ? selected.missing_references.join(", ") : ""}</p>}
        <p>Anchor: {string(document.anchor_task_id) || string(document.source_issue)} {document.anchor_work_revision === undefined ? "" : `work r${document.anchor_work_revision}`}</p>
        <p>{string(document.conclusion)}</p><p>Remaining: {string(document.remaining_work)}</p><p>{string(document.reason)}</p><p>{string(document.judgment)}</p>
        {records(document.links).map((link, index) => <p key={index}>{string(link.relation)} · task {string(link.task_id)} r{String(link.task_work_revision)} · run {string(link.run_id)} · {string(link.source)}</p>)}
        <label>Read revision <input type="number" min="1" defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} onBlur={(event) => { const revision = Number(event.target.value); if (Number.isSafeInteger(revision) && revision > 0 && revision !== selected.revision) void run(() => read(string(selected.id), revision)); }} /></label>
        <button type="button" onClick={() => setEditing(true)}>Revise, accept or reopen</button>
      </article>}
      {!editing ? null : <form key={`${selected?.id ?? "new"}:${selected?.revision ?? 0}`} onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const updated: Value = { ...document, kind, objective: data.get("objective"), criteria: data.get("criteria"), state: data.get("state"), reason: data.get("reason"), conclusion: data.get("conclusion"), remaining_work: data.get("remaining_work"), judgment: data.get("judgment") }; for (const key of ["anchor_task_id", "source_issue", "milestone_of"]) { const value = String(data.get(key) ?? ""); if (value === "") delete updated[key]; else updated[key] = value; } if (updated.anchor_task_id) updated.anchor_work_revision = Number(data.get("anchor_work_revision")); else delete updated.anchor_work_revision; if (String(data.get("link_task_id") ?? "") !== "") { updated.links = [...records(document.links), { relation: data.get("link_relation"), task_id: data.get("link_task_id"), task_work_revision: Number(data.get("link_work_revision")), source: data.get("link_source") }]; }void run(async () => { const value = await request("outcome_write", { id: selected?.id ?? newID.current, expected_revision: selected?.revision ?? 0, document: updated }); setSelected(value); setEditing(false); }); }}>
        <label>Kind <select value={kind} onChange={(event) => setKind(event.target.value)}><option value="outcome">Outcome</option><option value="mission">Mission or milestone</option></select></label>
        <label>Objective <textarea name="objective" defaultValue={string(document.objective)} required /></label>
        <label>Acceptance criteria <textarea name="criteria" defaultValue={string(document.criteria)} required /></label>
        <label>Task anchor <input name="anchor_task_id" defaultValue={string(document.anchor_task_id)} /></label><label>Task work revision <input name="anchor_work_revision" type="number" min="1" defaultValue={Number(document.anchor_work_revision ?? 1)} /></label>
        <label>Or source issue URL <input name="source_issue" type="url" defaultValue={string(document.source_issue)} /></label>
        {kind !== "mission" ? null : <label>Parent mission ID (optional) <input name="milestone_of" defaultValue={string(document.milestone_of)} /></label>}
        <label>State <select name="state" defaultValue={string(document.state) || "open"}><option>open</option><option>proposed</option><option>accepted</option><option>reopened</option></select></label>
        <label>Conclusion <textarea name="conclusion" defaultValue={string(document.conclusion)} /></label>
        <label>Remaining work <textarea name="remaining_work" defaultValue={string(document.remaining_work)} /></label>
        <label>Reason for acceptance or reopening <textarea name="reason" defaultValue={string(document.reason)} /></label>
        <label>Explicit judgment (start with “authorized judgment:” to accept or reopen) <textarea name="judgment" defaultValue={string(document.judgment)} /></label>
        <fieldset><legend>Link existing work (optional)</legend>
          <label>Task ID <input name="link_task_id" /></label><label>Work revision <input name="link_work_revision" type="number" min="1" defaultValue="1" /></label>
          <label>Relation <select name="link_relation"><option>implementation</option><option>investigation</option><option>review</option><option>delivery</option></select></label>
          <label>Source or delivery reference <input name="link_source" /></label>
        </fieldset>
        <button>Save outcome revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
    </fieldset>{error === "" ? null : <p role="alert">{error}</p>}
  </details>;
}
