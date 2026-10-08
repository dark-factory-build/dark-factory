import type { GraphNode, GraphSummary, OperationalGraphView } from "@dark-factory/client";
import { projectGraph } from "./console-view.js";
import type { SceneCrate, SceneWorker } from "./factory-scene/scene.js";

/** The public projection factoryd serves at /v1/public/<project>: an allowlist, never private labels. */
export type PublicWorld = Readonly<{
  generated_at: number;
  summary: GraphSummary;
  nodes: readonly Readonly<{ id: string; kind: GraphNode["kind"]; label: string; unit?: string; runtime?: GraphNode["runtime"]; trigger?: GraphNode["trigger"];
    evidence: GraphNode["evidence"]; observation: GraphNode["observation"]; state: GraphNode["state"]; activity: Activity; deployed?: boolean }>[];
  edges: readonly Readonly<{ from: string; to: string; kind: OperationalGraphView["edges"][number]["kind"]; evidence: GraphNode["evidence"]; observation: GraphNode["observation"]; state: GraphNode["state"]; activity: Activity }>[];
  workers: readonly Readonly<{ activity: SceneWorker["activity"]; unit?: string }>[];
  /** The outbound work line: a keyed id, station and fault, never a title, number or branch. Absent from older factories. */
  crates?: readonly Readonly<{ id: string; station: SceneCrate["station"]; fault?: boolean }>[];
  /** Present only for repositories public on GitHub. */
  ledger?: PublicLedger;
}>;

type LedgerItem = Readonly<{ number: number; title: string; url: string }>;
/** What is already public on GitHub about the factory's public repositories, as its records saw it. Times are UTC RFC 3339. */
export type PublicLedger = Readonly<{
  window_days: number;
  open: readonly LedgerItem[]; open_count: number;
  issues: readonly LedgerItem[]; issue_count: number;
  /** Merges within the window, newest first; merged_count counts them all when the list is cut. */
  merged: readonly (LedgerItem & Readonly<{ merged_at: string }>)[]; merged_count: number;
  releases: readonly Readonly<{ tag: string; url: string; published_at: string; prerelease?: boolean }>[];
  /** UTC days, oldest first, from the first recorded merge: 24 hourly merge counts each. */
  clock: readonly Readonly<{ date: string; hours: readonly number[] }>[];
}>;

type Activity = "none" | "low" | "medium" | "high";

// A bucket becomes a representative rate, so belts read busier or quieter without exact traffic.
const RATE: Record<Activity, number> = { none: 0, low: 30, medium: 600, high: 6000 };

/** The same world the operator sees, from the public projection alone. */
export function publicFloor(world: PublicWorld) {
  const graph: OperationalGraphView = {
    project_id: "public", digest: world.nodes.map((node) => node.id).join("").slice(0, 64).padEnd(64, "0"), observed_at: world.generated_at,
    sources: [], summary: world.summary, omitted: 0,
    nodes: world.nodes.map((node) => ({ id: node.id, kind: node.kind, label: node.label, ...(node.unit === undefined ? {} : { unit: node.unit }),
      ...(node.runtime === undefined ? {} : { runtime: node.runtime }), ...(node.trigger === undefined ? {} : { trigger: node.trigger }),
      paths: [], evidence: node.evidence, observation: node.observation, state: node.state, rate_per_hour: RATE[node.activity],
      // Public deploys say only "within the last day"; the stamp is the projection's own time.
      ...(node.deployed ? { deployed_at: world.generated_at } : {}) })),
    edges: world.edges.map((edge) => ({ from: edge.from, to: edge.to, kind: edge.kind, evidence: edge.evidence, observation: edge.observation, state: edge.state, rate_per_hour: RATE[edge.activity] })),
  };
  const prepared = projectGraph(new Map([["public", graph]]), ["public"]);
  const workers = world.workers.map((worker, index): SceneWorker => {
    const hall = worker.unit === undefined ? undefined : prepared.where.get(worker.unit)?.hall;
    return { id: `public-${index}`, name: `Worker ${index + 1}`, role: "worker", activity: worker.activity,
      location: worker.activity === "busy" ? hall === undefined ? "unobserved" : "working" : "resting", ...(hall === undefined ? {} : { nodeId: hall }) };
  });
  // Public crates carry no number or title; the scene names them by station alone.
  const crates = (world.crates ?? []).map((crate): SceneCrate => ({ id: crate.id, number: 0, title: "", station: crate.station, stage: "", fault: crate.fault === true, taskIds: [] }));
  return { graph: prepared.graph, workers, crates };
}
