import { useEffect, useMemo, useRef, useState } from "react";
import type { ProjectContentCall } from "./factory-app-controller.js";

export type ProductionRecord = {
  project_id: string; repository: string; kind: string; id: string; visual_id: string;
  observed_at: number; document: Record<string, unknown>; tasks: string[]; missions: string[];
};

/** One local read of the relevant production set for scene and inspector. Neither sprites nor panels
 * poll GitHub. A failed read keeps the previously read work. */
export function useProduction(projects: readonly string[], call: ProjectContentCall | undefined): readonly ProductionRecord[] {
  const [records, setRecords] = useState<ProductionRecord[]>([]);
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
        for (const project_id of projects) {
          // The daemon bounds the set by relevance, so every page is read.
          let offset = 0;
          do {
            const result = await caller.current!("production", { project_id, offset, limit: 8 }) as { records: Omit<ProductionRecord, "project_id">[]; next_offset: number; total: number };
            if (stopped || generation.current !== current) return;
            next.push(...result.records.map((record) => ({ ...record, project_id })));
            offset = result.next_offset;
          } while (offset !== 0);
        }
        if (!stopped) setRecords(next);
      } catch { /* retained */ }
      finally { busy = false; }
    };
    void refresh();
    const timer = setInterval(() => { void refresh(); }, 30000);
    const visible = () => { void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { stopped = true; clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [key, connected]);
  return useMemo(() => records.filter((record) => projects.includes(record.project_id)), [records, key]);
}
