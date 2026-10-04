import type { FactoryConsoleProps } from "@dark-factory/ui";
import type { TopologyView } from "@dark-factory/client";
import actualTopology, { actualFiles } from "./actual-topology.js";
import { fixtureCrowdedState, fixtureFloorState, fixtureRunPaths } from "../../../fixtures/state.mjs";

export type InhabitedPhase = "observed" | "stale" | "abandoned" | "integrated";
export type InhabitedPopulation = "working" | "resting" | "crowded" | "empty";
const repository = "dark-factory-build/dark-factory";
const projectId = actualTopology.projectId;
const base = actualTopology.sourceRevision;
const proposalHead = "a1".repeat(20), overlapHead = "b2".repeat(20), mergedHead = "c3".repeat(20);
const workerId = fixtureFloorState.agents.keys().next().value!;
const taskId = fixtureRunPaths.get(workerId)!.taskId;
const counts = (source = 0, tests = 0, configuration = 0, documentation = 0) => ({ source, tests, configuration, documentation, assets: 0, unclassified: 0 });
const sourceRelationships = [{ status: "removed", from_path: "internal/daemon", to_path: "internal/topology", weight: 1 }, { status: "added", from_path: "internal/dispatch-gates", to_path: "internal/topology", weight: 1 }];
const sourcePaths = [
  { status: "modified", path: "internal/daemon/topology.go", resource: "source" },
  { status: "modified", path: "web/packages/ui/src/factory-scene/movement.ts", resource: "source" },
  { status: "added", path: "web/packages/ui/src/factory-scene/routing.test.ts", resource: "tests" },
  { status: "deleted", path: "docs/development/WORKFLOW.md", resource: "documentation" },
  { status: "renamed", old_path: "internal/topology/topology.go", path: "internal/topology/scanner.go", resource: "source" },
  { status: "added", path: "internal/dispatch-gates/admission.go", resource: "source" },
  { status: "added", path: "internal/dispatch-gates/admission_test.go", resource: "tests" },
  { status: "added", path: "internal/dispatch-gates/README.md", resource: "documentation" },
  { status: "added", path: "internal/dispatch-gates/rules.json", resource: "configuration" },
];
const libraryDocument = { id: "f0".repeat(16), title: "Factory source map", kind: "procedure", description: "Fixture document opened through the existing project library.", author: "Fixture author", revision: 1, latest_revision: 1, deprecated: false, source_references: "web/packages/ui/src/factory-scene · internal/topology" };

export function inhabitedState(population: InhabitedPopulation) {
  const state = population === "crowded" ? fixtureCrowdedState : fixtureFloorState;
  const agents = new Map([...state.agents].map(([id, agent]) => [id, { ...agent, project_id: projectId }]));
  const tasks = new Map([...state.tasks].map(([id, task]) => [id, { ...task, project_id: projectId, ...(population === "resting" ? { status: "succeeded" as const } : {}) }]));
  return { ...state, projects: population === "empty" ? new Map() : new Map([[projectId, { ...state.projects.get(projectId)!, name: "dark-factory" }]]), agents: population === "empty" ? new Map() : agents, tasks: population === "empty" ? new Map() : tasks, humanRequests: new Map(), factory: { ...state.factory, active_runs: population === "working" ? 2 : population === "crowded" ? 12 : 0 } };
}

function integratedFiles() {
  const files = actualFiles.filter((file) => !sourcePaths.some((change) => change.status === "deleted" && change.path === file.path)).map((file) => ({ ...file, path: sourcePaths.find((change) => change.status === "renamed" && change.old_path === file.path)?.path ?? file.path }));
  for (const change of sourcePaths.filter((change) => change.status === "added")) files.push({ path: change.path, kind: change.path.endsWith("_test.go") || change.path.endsWith(".test.ts") ? "tests" : change.path.endsWith(".md") ? "documentation" : change.path.endsWith(".json") ? "configuration" : "source", bytes: 240 });
  return files.sort((a, b) => a.path.localeCompare(b.path));
}
const kindPriority = { repository: 0, directory: 1, module: 2, package: 3 };
const owns = (nodes: TopologyView["nodes"], path: string) => [...nodes].filter((node) => node.path === "." || path.startsWith(`${node.path}/`)).sort((a, b) => b.path.length - a.path.length || kindPriority[b.kind] - kindPriority[a.kind])[0];

/** Only the explicit integrated fixture snapshot changes the finished source. */
export function inhabitedTopology(phase: InhabitedPhase): TopologyView {
  if (phase !== "integrated") return actualTopology;
  const parent = actualTopology.nodes.find((node) => node.path === "internal") ?? actualTopology.nodes.find((node) => node.kind === "repository")!;
  const nodes = [...actualTopology.nodes, { id: "d4".repeat(32), parent_id: parent.id, kind: "package" as const, path: "internal/dispatch-gates", label: "dispatch-gates", language: "go", size_bucket: "tiny" as const }].map((node) => ({ ...node, inventory: { direct: counts(), total: counts(), samples: [] as string[], samples_omitted: 0 } }));
  for (const file of integratedFiles()) {
    const node = owns(nodes, file.path) as typeof nodes[number] | undefined;
    if (!node) continue;
    const kind = file.kind as keyof ReturnType<typeof counts>;
    node.inventory.direct[kind]++;
    if (node.inventory.samples.length < 32) node.inventory.samples.push(file.path.split("/").pop()!); else node.inventory.samples_omitted++;
    let ancestor: typeof nodes[number] | undefined = node;
    const seen = new Set<string>();
    while (ancestor && !seen.has(ancestor.id)) { seen.add(ancestor.id); ancestor.inventory.total[kind]++; ancestor = nodes.find((entry) => entry.id === ancestor?.parent_id); }
  }
  const byPath = (path: string) => [...nodes].filter((node) => node.path === path).sort((a, b) => kindPriority[b.kind] - kindPriority[a.kind])[0]?.id;
  const dependencies = actualTopology.dependencies ? { ...actualTopology.dependencies, edges: [
    ...actualTopology.dependencies.edges.filter((edge) => !sourceRelationships.some((change) => change.status === "removed" && edge.from === byPath(change.from_path) && edge.to === byPath(change.to_path))),
    ...sourceRelationships.filter((change) => change.status === "added").map((change) => ({ from: byPath(change.from_path)!, to: byPath(change.to_path)!, weight: change.weight })),
  ] } : undefined;
  return { ...actualTopology, dependencies, sourceRevision: mergedHead, digest: "fixture-integrated-conflict-resolution", nodes, sources: actualTopology.sources?.map((source) => ({ ...source, revision: mergedHead })) };
}

// Deliberately simulated interface data. Durable/provider proof lives in daemon tests.
type FixtureDocument = Record<string, unknown>;
const knowledgeDocuments = new Map<string, FixtureDocument[]>([[libraryDocument.id, [{ ...libraryDocument, project_id: projectId, body: "Source links identify integrated code entities. Shelf visits are ambient activity, not knowledge retrieval. This deterministic document proves that the visible bookshelf uses the existing library interface." }]]]);
const knowledgeMeta = (item: FixtureDocument) => { try { return JSON.parse(String(item.source_references || "{}")) as Record<string, unknown>; } catch { return {}; } };
const withoutBody = (item: FixtureDocument): FixtureDocument => { const { body: _body, ...metadata } = item; return metadata; };

export function inhabitedContent(phase: InhabitedPhase): NonNullable<FactoryConsoleProps["onProjectContent"]> {
  return async (operation, input) => {
    const now = Date.now();
    const record = (kind: string, id: string, visual_id: string, document: Record<string, unknown>, tasks: string[] = []) => ({ repository, kind, id, visual_id, observed_at: now, document, tasks, missions: [] });
    const source = { base, head: proposalHead, kind: "committed", observed_at: now, paths: sourcePaths, omitted: 0, relationships: sourceRelationships, relationships_omitted: 0 };
    if (operation === "production") {
      if (input.project_id !== projectId) return { records: [], total: 0, next_offset: 0 };
      const records = [
        record("repository", repository, "", { unavailable: "", integrated_revision: phase === "integrated" ? mergedHead : base }),
        record("construction", "fixture-main", "change:fixture-main", { title: "Rework routes and add dispatch gates", status: phase === "abandoned" ? "cancelled" : phase === "integrated" ? "succeeded" : "running", head: proposalHead, has_changes: true, task_id: taskId, source }, [taskId]),
        record("pull_request", "101", "change:fixture-main", { number: 101, title: "Rework routes and add dispatch gates", head: proposalHead, base, branch: "fixture/inhabited", state: phase === "abandoned" ? "closed" : phase === "integrated" ? "merged" : "open", ...(phase === "integrated" ? { merge: mergedHead, merged_at: new Date(now).toISOString() } : {}), review: { head: phase === "stale" ? "e5".repeat(20) : proposalHead, state: phase === "stale" ? "allow" : "running", findings: "Known assignment: affected assemblies. No exact file inspection is claimed." }, source }, [taskId]),
        record("pull_request", "102", "pr:fixture-overlap", { number: 102, title: "Alternative route clearance", head: overlapHead, base, state: "open", review: { head: overlapHead, state: "block", findings: "Fixture finding: the proposed route would cross a cabinet access lane." }, source: { ...source, head: overlapHead, paths: [sourcePaths[1]], kind: "working-tree", relationships: [], relationships_unavailable: "Working-tree static relationships are unavailable." } }),
        record("reviewer", "fixture-review-run-1", "", { number: 101, head: proposalHead, name: "Morgan · visual review", provider: "codex", state: phase === "observed" || phase === "stale" ? "running" : "completed", findings: "Assignment covers changed assemblies; no exact file-inspection telemetry." }),
        record("check", "fixture-source-gate", "", { name: "Source gate", revision: phase === "stale" ? "e5".repeat(20) : proposalHead, scope: "head", state: "completed", conclusion: "success", pull_requests: [101], jobs: [{ id: "fixtures", name: "Deterministic fixture checks", state: "completed", conclusion: "success" }] }),
      ];
      const offset = Number(input.offset) || 0, limit = Number(input.limit) || 8;
      return { records: records.slice(offset, offset + limit), total: records.length, next_offset: offset + limit < records.length ? offset + limit : 0 };
    }
    if (operation === "source_files") {
      const topology = inhabitedTopology(phase);
      if (input.tested_source !== topology.sourceRevision) return { revision: topology.sourceRevision, files: [], total: 0, next_offset: 0, unavailable: "Integrated target changed; refresh the source observation." };
      const allFiles = phase === "integrated" ? integratedFiles() : actualFiles;
      const files = allFiles.filter((file) => owns(topology.nodes, file.path)?.id === input.id);
      const offset = Number(input.offset) || 0, limit = Math.min(32, Number(input.limit) || 32);
      return { revision: topology.sourceRevision, files: files.slice(offset, offset + limit), total: files.length, next_offset: offset + limit < files.length ? offset + limit : 0 };
    }
    if (input.project_id !== projectId) throw new Error("Fixture project mismatch");
    if (operation === "list" || operation === "search") {
      const found = [...knowledgeDocuments.values()].map((revisions) => revisions[revisions.length - 1]!).filter((item) => {
        const metadata = knowledgeMeta(item);
        return (!input.open_only || !metadata.resolved) && (!input.kind || item.kind === input.kind) && (!input.thread_id || metadata.thread_id === input.thread_id) && (!input.entity || (metadata.entities as string[] || []).includes(String(input.entity))) && (!input.query || `${item.title} ${item.description}`.toLowerCase().includes(String(input.query).toLowerCase())) && (!metadata.branch || metadata.branch === input.branch) && (!metadata.environment || metadata.environment === input.environment);
      });
      const offset = Number(input.offset) || 0, limit = Number(input.limit) || 4;
      return { items: found.slice(offset, offset + limit).map(withoutBody), next_offset: offset + limit < found.length ? offset + limit : 0 };
    }
    if (operation === "create" || operation === "revise" || operation === "deprecate") {
      const key = String(input.id), revisions = knowledgeDocuments.get(key) || [], prior = revisions[revisions.length - 1];
      if (operation === "create" ? revisions.length !== 0 : Number(input.expected_revision) !== revisions.length) throw new Error("Revision conflict");
      const revision = revisions.length + 1;
      const item = { ...(operation === "deprecate" ? prior : input), id: key, project_id: projectId, revision, latest_revision: revision, author: "human: isolated browser fixture", deprecated: operation === "deprecate" };
      revisions.push(item); knowledgeDocuments.set(key, revisions);
      return withoutBody(item);
    }
    if (operation === "read" || operation === "body") {
      const revisions = knowledgeDocuments.get(String(input.id));
      if (!Number.isSafeInteger(input.revision) || Number(input.revision) < 1) throw new Error("An explicit positive document revision is required");
      const item = revisions?.[Number(input.revision) - 1];
      if (!item) throw new Error("Document revision not found");
      if (operation === "read") return { ...withoutBody(item), latest_revision: revisions!.length };
      const offset = Number(input.offset) || 0, limit = Number(input.limit) || 8192, body = String(item.body || "");
      return { body: body.slice(offset, offset + limit), complete: offset + limit >= body.length, next_offset: offset + limit < body.length ? offset + limit : 0 };
    }
    if (operation === "attach") return { task_id: input.task_id, content_id: input.content_id, content_revision: input.content_revision };
    if (operation === "accesses") return { items: [], next_offset: 0 };
    if (operation === "outcome_list" || operation === "evidence_list") return { items: [], next_offset: 0 };
    if (operation === "task_read") return { ...fixtureFloorState.tasks.get(taskId), id: taskId, task_id: taskId, project_id: projectId, revision: "12" };
    throw new Error("Fixture has no response for this operation; no daemon action was performed.");
  };
}
