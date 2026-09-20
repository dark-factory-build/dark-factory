import { useEffect, useRef, useState } from "react";
import type { ProjectContentCall } from "./project-library.js";

export type ProductionRecord = {
  project_id: string; repository: string; kind: string; id: string; visual_id: string;
  observed_at: number; links_overflow?: boolean; document: Record<string, unknown>; tasks: string[]; missions: string[];
};

/** One bounded local read for scene and inspector. External observation belongs
 * to the existing host controller; neither sprites nor panels poll GitHub. */
export function useProduction(projects: readonly string[], call: ProjectContentCall | undefined) {
  const [records, setRecords] = useState<ProductionRecord[]>([]);
  const [error, setError] = useState("");
  const [overflow, setOverflow] = useState(0);
  const [pages, setPages] = useState(32);
  const generation = useRef(0);
  const caller = useRef(call); caller.current = call;
  const connected = call !== undefined;
  const key = JSON.stringify(projects);
  useEffect(() => {
    const current = ++generation.current;
    if (call === undefined || typeof document === "undefined") return;
    let stopped = false, busy = false;
    const refresh = async () => {
      if (busy || stopped || document.visibilityState === "hidden") return;
      busy = true;
      try {
        const next: ProductionRecord[] = [];
        let remaining = 0;
        for (const project_id of projects) {
          let offset = 0;
          // ponytail: at most 256 records per project per refresh; explicit
          // overflow keeps a crowded source visible with an explicit load-more control.
          for (let page = 0; page < pages; page++) {
            const result = await caller.current!("production", { project_id, offset, limit: 8 }) as { records: Omit<ProductionRecord, "project_id">[]; next_offset: number; total: number };
            if (stopped || generation.current !== current) return;
            next.push(...result.records.map((record) => ({ ...record, project_id })));
            offset = result.next_offset;
            if (offset === 0) break;
            if (page === pages - 1) remaining += Math.max(0, result.total - offset);
          }
        }
        if (!stopped) { setRecords(next); setOverflow(remaining); setError(""); }
      } catch { if (!stopped) setError("Production observation unavailable. Previously read work is retained."); }
      finally { busy = false; }
    };
    void refresh();
    const timer = setInterval(() => { void refresh(); }, 30000);
    const visible = () => { void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { stopped = true; clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [key, connected, pages]);
  const scoped = records.filter((record) => projects.includes(record.project_id));
  const notices = scoped.filter((record) => record.kind === "repository" && (record.document.unavailable || record.document.overflow)).map((record) => `${record.repository}: ${record.document.unavailable ? "some external evidence is unavailable" : "observation is bounded"}${record.document.overflow ? "; additional external records exist" : ""}.`);
  return { notices, records: records.filter((record) => projects.includes(record.project_id)), error, overflow, loadMore: () => setPages((value) => value + 32) };
}
