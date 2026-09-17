import { useRef, useState } from "react";
import type { ProjectContentCall } from "./project-library.js";

type Value = Record<string, unknown>;
const string = (value: unknown): string => typeof value === "string" ? value : "";
const records = (value: unknown): Value[] => Array.isArray(value) ? value as Value[] : [];
const candidateFields = ["task_id", "task_work_revision", "run_id", "change_id", "source", "environment", "evidence_id", "scenario_evidence_id", "evidence_kind"] as const;
function Candidate({ prefix, value = {} }: { prefix: string; value?: Value }) {
  return <fieldset><legend>{prefix === "baseline" ? "Baseline" : "Candidate"}</legend>{candidateFields.map((field) => <label key={field}>{field.replaceAll("_", " ")}<input name={`${prefix}.${field}`} type={field === "task_work_revision" ? "number" : "text"} min={field === "task_work_revision" ? 1 : undefined} defaultValue={String(value[field] ?? (field === "task_work_revision" ? 1 : ""))} required={field === "task_id" || field === "task_work_revision"} /></label>)}</fieldset>;
}
function candidate(data: FormData, prefix: string): Value {
  return Object.fromEntries(candidateFields.flatMap((field) => { const value = String(data.get(`${prefix}.${field}`) ?? ""); return value === "" ? [] : [[field, field === "task_work_revision" ? Number(value) : value]]; }));
}

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
  const [candidateCount, setCandidateCount] = useState(2);
  const run = async (work: () => Promise<void>) => { setPending(true); setError(""); try { await work(); } catch (error) { setError(error instanceof Error ? error.message : "Request failed"); } finally { setPending(false); } };
  const request = (operation: "outcome_list" | "outcome_read" | "outcome_write", input: Value) => call === undefined ? Promise.reject(new Error("Connect to read outcomes")) : call(operation, { ...input, project_id: project });
  const list = async (offset = 0) => { const value = await request("outcome_list", { offset, limit: 1 }); setItems(records(value.items)); setNext(Number(value.next_offset ?? 0)); };
  const read = async (id: string, revision = 0) => { const value = await request("outcome_read", { id, revision }); setSelected(value); setEditing(false); setKind(string((value.document as Value)?.kind)); setCandidateCount(Math.max(2, records((value.document as Value)?.candidates).length)); };
  return <details><summary>Outcomes, comparisons &amp; milestones</summary>
    <p>Successful work is not acceptance. Record implementation, review, merge and deployment evidence separately.</p>
    <fieldset disabled={pending || call === undefined || project === ""}>
      <button type="button" onClick={() => void run(() => list())}>Browse outcomes</button>
      <button type="button" onClick={() => { newID.current = crypto.randomUUID().replaceAll("-", ""); setSelected(undefined); setKind("outcome"); setCandidateCount(2); setEditing(true); }}>New outcome</button>
      {items.map((item) => <button type="button" key={string(item.id)} onClick={() => void run(() => read(string(item.id)))}>{string(item.objective)} · {string(item.state)}{item.stale === true ? " · stale" : ""}</button>)}
      {next === 0 ? null : <button type="button" onClick={() => void run(() => list(next))}>Next outcomes</button>}
      {selected === undefined ? null : <article>
        <h3>{string(document.objective)}</h3><p>Criteria: {string(document.criteria)}</p>
        <p>{string(document.state)} · r{String(selected.revision)} · {string(selected.author)} ({string(selected.authority)})</p>
        {selected.stale === true ? <p role="status">Objective changed: this revision does not establish current acceptance.</p> : null}
        {records(selected.missing_references).length === 0 && !Array.isArray(selected.missing_references) ? null : <p>Missing references: {Array.isArray(selected.missing_references) ? selected.missing_references.join(", ") : ""}</p>}
        <p>Anchor: {string(document.anchor_task_id) || string(document.source_issue)} {document.anchor_work_revision === undefined ? "" : `work r${document.anchor_work_revision}`}</p>
        <p>{string(document.conclusion)}</p><p>Remaining: {string(document.remaining_work)}</p><p>{string(document.reason)}</p><p>{string(document.judgment)}</p>
        {records(document.links).map((link, index) => <p key={index}>{string(link.relation)} · {string(link.stage)} · task {string(link.task_id)} r{String(link.task_work_revision)} · run {string(link.run_id)} · {string(link.source)}</p>)}
        {document.kind !== "comparison" ? null : <><p>Question: {string(document.question)}</p><p>Decision: {string(document.decision) || "Not decided"} {string(document.selected_candidate_task_id)}</p><table><thead><tr><th>Candidate</th><th>Source / environment</th><th>Evidence</th></tr></thead><tbody>{[document.baseline as Value, ...records(document.candidates)].filter(Boolean).map((row, index) => <tr key={index}><td>{index === 0 ? "Baseline: " : ""}{string(row.task_id)} r{String(row.task_work_revision)}<br />Run {string(row.run_id)}</td><td>{string(row.source)}<br />{string(row.environment)}</td><td>{string(row.evidence_kind)}<br />{string(row.evidence_id)}<br />Scenario evidence {string(row.scenario_evidence_id)}</td></tr>)}</tbody></table></>}
        <label>Read revision <input type="number" min="1" defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} onBlur={(event) => { const revision = Number(event.target.value); if (Number.isSafeInteger(revision) && revision > 0 && revision !== selected.revision) void run(() => read(string(selected.id), revision)); }} /></label>
        <button type="button" onClick={() => setEditing(true)}>Revise, accept or reopen</button>
      </article>}
      {!editing ? null : <form key={`${selected?.id ?? "new"}:${selected?.revision ?? 0}`} onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const updated: Value = { ...document, kind, objective: data.get("objective"), criteria: data.get("criteria"), state: data.get("state"), reason: data.get("reason"), conclusion: data.get("conclusion"), remaining_work: data.get("remaining_work"), judgment: data.get("judgment"), evidence: String(data.get("evidence") ?? "").split(/\s+/).filter(Boolean) }; for (const key of ["anchor_task_id", "source_issue", "milestone_of"]) { const value = String(data.get(key) ?? ""); if (value === "") delete updated[key]; else updated[key] = value; } if (updated.anchor_task_id) updated.anchor_work_revision = Number(data.get("anchor_work_revision")); else delete updated.anchor_work_revision; if (String(data.get("link_task_id") ?? "") !== "") { updated.links = [...records(document.links), { relation: data.get("link_relation"), stage: data.get("link_stage"), task_id: data.get("link_task_id"), task_work_revision: Number(data.get("link_work_revision")), evidence_id: data.get("link_evidence"), source: data.get("link_source") }]; } if (kind === "comparison") { updated.question = data.get("question"); updated.decision = data.get("decision"); updated.baseline = candidate(data, "baseline"); updated.candidates = Array.from({ length: candidateCount }, (_, index) => candidate(data, `candidate${index}`)); updated.selected_candidate_task_id = data.get("selected_candidate_task_id"); } else { for (const key of ["question", "decision", "baseline", "candidates", "selected_candidate_task_id"]) delete updated[key]; } void run(async () => { const value = await request("outcome_write", { id: selected?.id ?? newID.current, expected_revision: selected?.revision ?? 0, document: updated }); setSelected(value); setEditing(false); }); }}>
        <label>Kind <select value={kind} onChange={(event) => setKind(event.target.value)}><option value="outcome">Outcome</option><option value="comparison">Comparison</option><option value="mission">Mission or milestone</option></select></label>
        <label>Objective <textarea name="objective" defaultValue={string(document.objective)} required /></label>
        <label>Acceptance criteria <textarea name="criteria" defaultValue={string(document.criteria)} required /></label>
        <label>Task anchor <input name="anchor_task_id" defaultValue={string(document.anchor_task_id)} /></label><label>Task work revision <input name="anchor_work_revision" type="number" min="1" defaultValue={Number(document.anchor_work_revision ?? 1)} /></label>
        <label>Or source issue URL <input name="source_issue" type="url" defaultValue={string(document.source_issue)} /></label>
        {kind !== "mission" ? null : <label>Parent mission ID (optional) <input name="milestone_of" defaultValue={string(document.milestone_of)} /></label>}
        <label>State <select name="state" defaultValue={string(document.state) || "open"}><option>open</option><option>proposed</option><option>accepted</option><option>reopened</option></select></label>
        <label>Conclusion <textarea name="conclusion" defaultValue={string(document.conclusion)} /></label>
        <label>Remaining work <textarea name="remaining_work" defaultValue={string(document.remaining_work)} /></label>
        <label>Reason for acceptance or reopening <textarea name="reason" defaultValue={string(document.reason)} /></label>
        <label>Existing evidence IDs <textarea name="evidence" defaultValue={Array.isArray(document.evidence) ? document.evidence.join("\n") : ""} /></label>
        <label>Explicit judgment (use “authorized judgment:” when accepting without evidence) <textarea name="judgment" defaultValue={string(document.judgment)} /></label>
        {kind !== "comparison" ? null : <><label>Question <textarea name="question" defaultValue={string(document.question)} required /></label><Candidate prefix="baseline" value={document.baseline as Value | undefined} />{Array.from({ length: candidateCount }, (_, index) => <Candidate key={index} prefix={`candidate${index}`} value={records(document.candidates)[index]} />)}<button type="button" disabled={candidateCount >= 32} onClick={() => setCandidateCount((count) => count + 1)}>Add existing candidate</button><label>Decision <select name="decision" defaultValue={string(document.decision)}><option value="">Undecided</option><option value="select">Select candidate</option><option value="keep_baseline">Keep baseline</option><option value="inconclusive">Inconclusive</option></select></label><label>Selected candidate task ID <input name="selected_candidate_task_id" defaultValue={string(document.selected_candidate_task_id)} /></label></>}
        <fieldset><legend>Link existing work (optional)</legend>
          <label>Task ID <input name="link_task_id" /></label><label>Work revision <input name="link_work_revision" type="number" min="1" defaultValue="1" /></label>
          <label>Relation <select name="link_relation"><option>implementation</option><option>investigation</option><option>review</option><option>delivery</option></select></label>
          <label>Stage <select name="link_stage"><option value="">Not established</option><option>implemented</option><option>reviewed</option><option>merged</option><option>deployed</option></select></label>
          <label>Evidence ID <input name="link_evidence" /></label><label>Source or delivery reference <input name="link_source" /></label>
        </fieldset>
        <button>Save outcome revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
    </fieldset>{error === "" ? null : <p role="alert">{error}</p>}
  </details>;
}
