import { useEffect, useMemo, useRef, useState } from "react";
import type { ProjectContentCall } from "./project-library.js";

export type RuntimeBuild = { version: string; source: string; target: string; build_id: string; release: boolean };
/** The newest release the daemon has observed. Absent is no answer, not current. */
export type PublishedRelease = { version: string; url: string };

export type ProductionRecord = {
  project_id: string; repository: string; kind: string; id: string; visual_id: string;
  observed_at: number; links_overflow?: boolean; document: Record<string, unknown>; tasks: string[]; missions: string[];
};

const PAGES = 32;

/** One bounded local read for scene and inspector. Neither sprites nor panels
 * poll GitHub. */
export function useProduction(projects: readonly string[], call: ProjectContentCall | undefined) {
  const [runtime, setRuntime] = useState<RuntimeBuild>();
  const [release, setRelease] = useState<PublishedRelease>();
  const [records, setRecords] = useState<ProductionRecord[]>([]);
  const [error, setError] = useState("");
  const [overflow, setOverflow] = useState(0);
  const generation = useRef(0);
  const runtimeGeneration = useRef(-1);
  // The project list those records were read for: a switch is a first read too.
  const readKey = useRef("");
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
        let currentRuntime: RuntimeBuild | undefined, currentRelease: PublishedRelease | undefined;
        // The daemon's own build and the published release ride on every
        // production read. A factory with no project still asks once, with no
        // project, so those two facts never depend on a project existing.
        for (const project_id of projects.length > 0 ? projects : [""]) {
          let offset = 0;
          // ponytail: at most 256 records per project per refresh; explicit
          // overflow keeps a crowded source visible.
          for (let page = 0; page < PAGES; page++) {
            const result = await caller.current!("production", { project_id, offset, limit: 8 }) as { records: Omit<ProductionRecord, "project_id">[]; next_offset: number; total: number; runtime?: RuntimeBuild; release?: PublishedRelease };
            if (stopped || generation.current !== current) return;
            currentRuntime = result.runtime;
            currentRelease = result.release;
            next.push(...result.records.map((record) => ({ ...record, project_id })));
            offset = result.next_offset;
            if (offset === 0) break;
            if (page === PAGES - 1) remaining += Math.max(0, result.total - offset);
          }
        }
        if (!stopped) { runtimeGeneration.current = current; readKey.current = key; setRuntime(currentRuntime); setRelease(currentRelease); setRecords(next); setOverflow(remaining); setError(""); }
      } catch { if (!stopped) setError("Production observation unavailable. Previously read work is retained."); }
      finally { busy = false; }
    };
    void refresh();
    const timer = setInterval(() => { void refresh(); }, 30000);
    const visible = () => { void refresh(); };
    document.addEventListener("visibilitychange", visible);
    return () => { stopped = true; clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [key, connected]);
  const scoped = useMemo(() => records.filter((record) => projects.includes(record.project_id)), [records, key]);
  const notices = scoped.filter((record) => record.kind === "repository" && (record.document.unavailable || record.document.overflow)).map((record) => `${record.repository}: ${record.document.unavailable ? "some external evidence is unavailable" : "observation is bounded"}${record.document.overflow ? "; additional external records exist" : ""}.`);
  // The runtime identity belongs to this connection and is dropped when it goes;
  // the published release is a repository fact that no reconnect invalidates.
  const read = connected && runtimeGeneration.current === generation.current && readKey.current === key;
  // `read`: the records were read on this connection, so their changes are news, not history.
  return { runtime: read ? runtime : undefined, read, release, notices, records: scoped, error, overflow };
}
