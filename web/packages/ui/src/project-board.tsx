import { useEffect, useRef, useState } from "react";
import type { StateView } from "@dark-factory/client";
import type { ProjectContentCall } from "./project-library.js";

/** One recorded knowledge operation, as the daemon's read-only activity listing reports it. */
export type KnowledgeActivity = Readonly<{
  key: string; project_id: string; operation: "posted" | "revised" | "retired" | "delivered" | "read";
  content_id: string; content_revision: number; kind: string; title: string; author: string;
  agent_id: string; task_id: string; run_id: string; thread_id: string; at_ms: number;
}>;
export type KnowledgeCue = KnowledgeActivity & Readonly<{ shownAt: number }>;

/** Conversations belong on the Board; everything else is a Library document. */
export const onBoard = (item: Pick<KnowledgeActivity, "kind">) => item.kind === "discussion" || item.kind === "discussion_reply";
/** A reply opens its thread; anything else opens the exact revision the operation used. */
export const activityTarget = (item: KnowledgeActivity) => item.kind === "discussion_reply" && item.thread_id ? { id: item.thread_id } : { id: item.content_id, revision: item.content_revision };

export const CUE_MS = 6000, CUE_LIMIT = 4, ACTIVITY_POLL_MS = 10000;
const VERBS: Record<KnowledgeActivity["operation"], string> = { posted: "posted", revised: "revised", retired: "retired", delivered: "was given", read: "read" };

/** Provenance is authority-derived; anything unrecognised stays in Sources & history only. */
export function authorName(author: string, state?: StateView): string {
  const agent = /(?:^| )agent:([0-9a-f]+)/.exec(author)?.[1];
  if (agent) return state?.agents.get(agent)?.name ?? "an agent";
  return author.startsWith("operator:") || author.startsWith("browser:") ? "Operator" : "";
}

export function activityLabel(item: KnowledgeActivity, state?: StateView): string {
  const who = item.agent_id ? state?.agents.get(item.agent_id)?.name ?? `Agent ${item.agent_id.slice(0, 8)}` : authorName(item.author, state) || "Someone";
  const what = item.kind === "discussion_reply" ? "a reply" : onBoard(item) ? "thread" : "document";
  return `${who} ${VERBS[item.operation]} ${what === "a reply" ? what : `${what} “${item.title}”`} · revision ${item.content_revision}`;
}

/**
 * Polls the recorded activity of the shown projects. The first look after
 * connecting, reconnecting or changing scope is history: it is listed, never
 * cued. Later operations are cued once, at most CUE_LIMIT at a time, for CUE_MS.
 * Reading this listing records nothing and retrieves no document text.
 */
export function useKnowledgeActivity(projects: readonly string[], call: ProjectContentCall | undefined) {
  const [recent, setRecent] = useState<KnowledgeActivity[]>([]);
  const [cues, setCues] = useState<KnowledgeCue[]>([]);
  // ponytail: one key per operation seen while this console is open; prune by age if a console stays open for weeks.
  const seen = useRef(new Set<string>());
  const caller = useRef(call); caller.current = call;
  const key = JSON.stringify(projects);
  useEffect(() => {
    setCues([]);
    if (call === undefined || typeof document === "undefined") return;
    let stopped = false, busy = false, history = true;
    const refresh = async () => {
      if (busy || stopped || document.visibilityState === "hidden") return;
      busy = true;
      try {
        const found = new Map<string, KnowledgeActivity>();
        for (const project_id of projects) {
          const result = await caller.current!("activity", { project_id, limit: 8 });
          if (stopped) return;
          for (const row of Array.isArray(result.items) ? result.items as Omit<KnowledgeActivity, "key" | "project_id">[] : []) {
            const key = `${project_id}:${row.operation}:${row.content_id}:${row.content_revision}:${row.run_id}`;
            found.set(key, { ...row, project_id, key });
          }
        }
        const items = [...found.values()];
        items.sort((a, b) => b.at_ms - a.at_ms || a.key.localeCompare(b.key));
        const fresh = history ? [] : items.filter((item) => !seen.current.has(item.key));
        for (const item of items) seen.current.add(item.key);
        history = false;
        setRecent(items.slice(0, 8));
        const at = Date.now();
        setCues((old) => { const kept = old.filter((cue) => at - cue.shownAt < CUE_MS); return fresh.length === 0 && kept.length === old.length ? old : [...kept, ...fresh.reverse().map((item) => ({ ...item, shownAt: at }))].slice(-CUE_LIMIT); });
      } catch { /* The list keeps what was last read; the next poll retries. */ }
      finally { busy = false; }
    };
    void refresh();
    const timer = setInterval(() => { void refresh(); }, ACTIVITY_POLL_MS);
    const visible = () => { void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { stopped = true; clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [key, call !== undefined]);
  useEffect(() => {
    if (cues.length === 0) return;
    const timer = setTimeout(() => { const at = Date.now(); setCues((old) => old.filter((cue) => at - cue.shownAt < CUE_MS)); }, Math.max(0, CUE_MS - (Date.now() - cues[0]!.shownAt)));
    return () => clearTimeout(timer);
  }, [cues]);
  return { recent, cues };
}

/** The floor's list of recorded operations: the keyboard path, and the only one for agents not drawn. */
export function KnowledgeActivityList({ items, state, onOpen }: { items: readonly KnowledgeActivity[]; state?: StateView; onOpen?: (item: KnowledgeActivity) => void }) {
  return <details className="dfFactoryHelp dfKnowledgeActivity"><summary>Activity · {items.length}</summary>
    <p>Recorded Board and Library operations. A read records the text served, not understanding.</p>
    {items.length === 0 ? <p>No recorded activity yet.</p> : <ul>{items.map((item) => <li key={item.key}><button type="button" disabled={!onOpen} onClick={() => onOpen?.(item)}>{activityLabel(item, state)}</button> <small>{onBoard(item) ? "Board" : "Library"}</small></li>)}</ul>}
  </details>;
}

/**
 * Direct questions are the runtime's task-to-task messages: delivered to the
 * asked task and answered through the attempt API. The Board only lists them;
 * the source task's conversation shows exact delivery states.
 */
export function DirectQuestions({ state, projectID, onOpen }: { state?: StateView; projectID: string; onOpen?: (questionID: string) => void }) {
  const tasks = state?.tasks;
  const questions = [...(state?.peerQuestions?.values() ?? [])].filter((question) => tasks?.get(question.source_task_id)?.project_id === projectID);
  const agent = (taskID: string) => { const task = tasks?.get(taskID); return state?.agents.get(task?.assigned_agent_id ?? "")?.name ?? task?.title ?? `Task ${taskID.slice(0, 8)}`; };
  return <section className="dfBoardQuestions" aria-label="Direct questions">
    <h3>Direct questions</h3>
    <p>Questions one running task asked another. The runtime delivers these; Board threads are not delivered to anyone.</p>
    {questions.length === 0 ? <p>No open direct questions.</p> : <ul>{questions.map((question) => <li key={question.id}>
      <strong>{agent(question.source_task_id)} → {agent(question.target_task_id)}</strong> <span>{question.answered ? "Answered" : "Awaiting answer"}</span>{" "}
      <button type="button" disabled={!onOpen} onClick={() => onOpen?.(question.id)}>Open conversation</button>
    </li>)}</ul>}
  </section>;
}
