import { useEffect, useRef, useState } from "react";
import type { ProjectContentCall } from "./project-library.js";

type Value = Record<string, unknown>;
const string = (value: unknown): string => typeof value === "string" ? value : "";
const records = (value: unknown): Value[] => Array.isArray(value) ? value as Value[] : [];
/** Outcomes are explicit human judgments over existing work, never task status. */
export function ProjectOutcomes({ project, call }: { project: string; call?: ProjectContentCall }) {
  const newID = useRef<string | undefined>(undefined);
  const [items, setItems] = useState<Value[]>([]);
  const listing = useRef(0), reading = useRef(0), scope = useRef(0);
  const [loading, setLoading] = useState(false), [listError, setListError] = useState("");
  const [selected, setSelected] = useState<Value>();
  const [editing, setEditing] = useState(false);
  const [kind, setKind] = useState("outcome");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const document = (selected?.document ?? {}) as Value;
  const available = call !== undefined && project !== "";
  const run = async (work: () => Promise<void>) => {
    const generation = scope.current;
    setPending(true); setError("");
    try { await work(); } catch (error) { if (generation === scope.current) setError(error instanceof Error ? error.message : "Request failed"); }
    finally { if (generation === scope.current) setPending(false); }
  };
  const request = async (operation: "outcome_list" | "outcome_read" | "outcome_write", input: Value) => {
    if (!call || !project) throw new Error("Connect to read outcomes");
    const generation = scope.current;
    const value = await call(operation, { ...input, project_id: project });
    if (generation !== scope.current) throw new Error("Library view changed");
    return value;
  };
  const list = async () => {
    const generation = ++listing.current;
    setLoading(true); setListError("");
    const found = new Map<string, Value>();
    try {
      // ponytail: the browser bounds each page to one outcome; use compact server summaries if large projects make this slow.
      let offset = 0;
      do {
        const value = await request("outcome_list", { offset, limit: 1 });
        if (generation !== listing.current) return;
        for (const item of records(value.items)) found.set(string(item.id), item);
        setItems([...found.values()]);
        const next = Number(value.next_offset ?? 0);
        if (next === 0) break;
        if (!Number.isSafeInteger(next) || next <= offset) throw new Error("Could not load remaining outcomes");
        offset = next;
      } while (true);
    } catch (error) { if (generation === listing.current) setListError(error instanceof Error ? error.message : "Could not load outcomes"); }
    finally { if (generation === listing.current) setLoading(false); }
  };
  useEffect(() => {
    setItems([]); setSelected(undefined); setEditing(false); setPending(false); setError("");
    if (available) void list();
    return () => { scope.current++; listing.current++; reading.current++; };
  }, [available, project]);
  const read = async (id: string, revision = 0) => { const generation = ++reading.current; const value = await request("outcome_read", { id, revision }); if (generation !== reading.current) return; setSelected(value); setEditing(false); setKind(string((value.document as Value)?.kind)); };
  return <section className="dfOutcomes" aria-label="Project outcomes">
    <fieldset disabled={pending || call === undefined || project === ""} aria-busy={pending}>
      {selected || editing ? null : <>
        <div className="dfProjectLibrary__actions"><h3>Outcomes</h3><button type="button" onClick={() => { newID.current = crypto.randomUUID().replaceAll("-", ""); setSelected(undefined); setKind("outcome"); setEditing(true); }}>New outcome</button></div>
        <ul className="dfOutcomes__list" aria-label="All outcomes">{items.map((item) => <li key={string(item.id)}><button type="button" onClick={() => void run(() => read(string(item.id)))}><strong>{string(item.objective)}</strong><span className="dfStatus" data-stage={string(item.state)}>{string(item.state)}{item.stale === true ? " · needs revalidation" : ""}</span></button></li>)}</ul>
        {loading ? <p role="status">Loading outcomes…</p> : items.length || listError ? null : <p>No outcomes yet.</p>}
        {!listError ? null : <p role="alert">{listError} <button type="button" onClick={() => void list()}>Retry</button></p>}
      </>}
      {selected === undefined || editing ? null : <article className="dfOutcomes__detail">
        <div className="dfProjectLibrary__actions"><button type="button" onClick={() => setSelected(undefined)}><svg aria-hidden="true" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2"><path d="m12 5-7 7 7 7M5 12h15" /></svg> All outcomes</button><button type="button" onClick={() => setEditing(true)}>Edit outcome</button></div>
        <h3>{string(document.objective)}</h3>
        <p className="dfOutcomes__meta"><span className="dfStatus" data-stage={string(document.state)}>{string(document.state)}</span> · Revision {String(selected.revision)} · {string(selected.author)} ({string(selected.authority)})</p>
        <form className="dfProjectLibrary__revision" aria-label="Outcome revision" onSubmit={(event) => { event.preventDefault(); const revision = Number(new FormData(event.currentTarget).get("revision")); if (Number.isSafeInteger(revision) && revision > 0) void run(() => read(string(selected.id), revision)); }}><label>Revision <input name="revision" type="number" min="1" defaultValue={Number(selected.revision)} key={`${selected.id}:${selected.revision}`} required /></label><button>Read revision</button></form>
        {selected.stale === true ? <p role="status">Objective changed · needs revalidation</p> : null}
        {!Array.isArray(selected.missing_references) || selected.missing_references.length === 0 ? null : <p role="status">Missing references: {selected.missing_references.join(", ")}</p>}
        {[["Criteria", document.criteria], ["Conclusion", document.conclusion], ["Remaining work", document.remaining_work], ["Reason", document.reason], ["Judgment", document.judgment]].map(([label, value]) => !string(value) ? null : <section key={string(label)}><h4>{string(label)}</h4><p className="dfOutcomes__prose">{string(value)}</p></section>)}
        <section><h4>References</h4>
          {string(document.source_issue) ? <p>{/^https?:\/\//i.test(string(document.source_issue)) ? <a href={string(document.source_issue)} target="_blank" rel="noreferrer">{string(document.source_issue)}</a> : string(document.source_issue)}</p> : null}
          {string(document.anchor_task_id) ? <p>Task <code>{string(document.anchor_task_id)}</code> · Work revision {String(document.anchor_work_revision)}</p> : null}
          {records(document.links).map((link, index) => <div className="dfOutcomes__reference" key={index}><p>{string(link.relation)}</p><p>Task <code>{string(link.task_id)}</code> · Work revision {String(link.task_work_revision)}</p>{[link.run_id, link.source].filter(Boolean).map((value, index) => <p key={index}><code>{string(value)}</code></p>)}</div>)}
        </section>

      </article>}
      {!editing ? null : <form key={`${selected?.id ?? "new"}:${selected?.revision ?? 0}`} onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); const updated: Value = { ...document, kind, objective: data.get("objective"), criteria: data.get("criteria"), state: data.get("state"), reason: data.get("reason"), conclusion: data.get("conclusion"), remaining_work: data.get("remaining_work"), judgment: data.get("judgment") }; for (const key of ["anchor_task_id", "source_issue", "milestone_of"]) { const value = String(data.get(key) ?? ""); if (value === "") delete updated[key]; else updated[key] = value; } if (updated.anchor_task_id) updated.anchor_work_revision = Number(data.get("anchor_work_revision")); else delete updated.anchor_work_revision; if (String(data.get("link_task_id") ?? "") !== "") { updated.links = [...records(document.links), { relation: data.get("link_relation"), task_id: data.get("link_task_id"), task_work_revision: Number(data.get("link_work_revision")), source: data.get("link_source") }]; } void run(async () => { const value = await request("outcome_write", { id: selected?.id ?? newID.current, expected_revision: selected?.revision ?? 0, document: updated }); setSelected(value); setEditing(false); void list(); }); }}>
        <h3>{selected ? "Edit outcome" : "New outcome"}</h3>
        {kind === "mission" ? <p>Mission outcome</p> : null}
        <label>Objective <textarea name="objective" defaultValue={string(document.objective)} required /></label>
        <label>Acceptance criteria <textarea name="criteria" defaultValue={string(document.criteria)} required /></label>
        <label>Task anchor <input name="anchor_task_id" defaultValue={string(document.anchor_task_id)} /></label><label>Task work revision <input name="anchor_work_revision" type="number" min="1" defaultValue={Number(document.anchor_work_revision ?? 1)} /></label>
        <label>Or source issue URL <input name="source_issue" type="url" defaultValue={string(document.source_issue)} /></label>
        {kind !== "mission" ? null : <label>Parent mission ID (optional) <input name="milestone_of" defaultValue={string(document.milestone_of)} /></label>}
        <label>State <select name="state" defaultValue={string(document.state) || "open"}><option>open</option><option>proposed</option><option>accepted</option><option>reopened</option></select></label>
        <label>Conclusion <textarea name="conclusion" defaultValue={string(document.conclusion)} /></label>
        <label>Remaining work <textarea name="remaining_work" defaultValue={string(document.remaining_work)} /></label>
        <label>Reason for acceptance or reopening <textarea name="reason" defaultValue={string(document.reason)} /></label>
        <label>Judgment <textarea placeholder="Start with authorized judgment: to accept or reopen" name="judgment" defaultValue={string(document.judgment)} /></label>
        <fieldset><legend>Link existing work (optional)</legend>
          <label>Task ID <input name="link_task_id" /></label><label>Work revision <input name="link_work_revision" type="number" min="1" defaultValue="1" /></label>
          <label>Relation <select name="link_relation"><option>implementation</option><option>investigation</option><option>review</option><option>delivery</option></select></label>
          <label>Source or delivery reference <input name="link_source" /></label>
        </fieldset>
        <button>Save outcome revision</button><button type="button" onClick={() => setEditing(false)}>Cancel</button>
      </form>}
    </fieldset>{error === "" ? null : <p role="alert">{error}</p>}
  </section>;
}
