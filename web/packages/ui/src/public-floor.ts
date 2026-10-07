import type { GraphNode, GraphSummary, OperationalGraphView } from "@dark-factory/client";
import { projectGraph, type FloorDetail } from "./console-view.js";
import type { SceneWorker } from "./factory-scene/scene.js";

/** The public projection factoryd serves at /v1/public/<project>: an allowlist, never private labels. */
export type PublicWorld = Readonly<{
  generated_at: number;
  summary: GraphSummary;
  nodes: readonly Readonly<{ id: string; kind: GraphNode["kind"]; label: string; unit?: string; runtime?: GraphNode["runtime"]; trigger?: GraphNode["trigger"];
    evidence: GraphNode["evidence"]; observation: GraphNode["observation"]; state: GraphNode["state"]; activity: Activity; deployed?: boolean }>[];
  edges: readonly Readonly<{ from: string; to: string; kind: OperationalGraphView["edges"][number]["kind"]; evidence: GraphNode["evidence"]; observation: GraphNode["observation"]; state: GraphNode["state"]; activity: Activity }>[];
  workers: readonly Readonly<{ activity: SceneWorker["activity"]; unit?: string }>[];
}>;

type Activity = "none" | "low" | "medium" | "high";

// A bucket becomes a representative rate, so belts read busier or quieter without exact traffic.
const RATE: Record<Activity, number> = { none: 0, low: 30, medium: 600, high: 6000 };

/** The same world the operator sees, from the public projection alone. */
export function publicFloor(world: PublicWorld, detail: FloorDetail = "auto") {
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
  const prepared = projectGraph(new Map([["public", graph]]), ["public"], detail);
  const workers = world.workers.map((worker, index): SceneWorker => {
    const hall = worker.unit === undefined ? undefined : prepared.where.get(worker.unit)?.hall;
    return { id: `public-${index}`, name: `Worker ${index + 1}`, role: "worker", activity: worker.activity,
      location: worker.activity === "busy" ? hall === undefined ? "unobserved" : "working" : "resting", ...(hall === undefined ? {} : { nodeId: hall }) };
  });
  return { graph: prepared.graph, workers };
}
