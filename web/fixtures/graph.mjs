// Builders for the wire-shaped operational graph (OperationalGraphView), for tests that need their own shape.
export const hex = (n) => n.toString(16).padStart(2, "0").repeat(16);
export const quiet = { evidence: "static", observation: "quiet", state: "idle" };
export const observed = { observation: "observed", state: "active", rate_per_hour: 60 };
export const node = (n, kind, label, extra = {}) => ({ id: hex(n), kind, label, paths: [], evidence: "static", observation: "unobserved", state: "unknown", ...extra });
export const unit = (n, label, paths, extra = {}) => node(n, "processor", label, { runtime: "server", paths, ...extra });
export const graphWith = (nodes, edges = []) => ({ project_id: "project", digest: "d".repeat(64), observed_at: 0, sources: [], nodes, edges,
  summary: { components: nodes.length, inferred: 0, observed: 0, quiet: 0, partial: 0, stale: 0, unobserved: 0, opaque: 0, runtime_only: 0, contradicted: 0 }, omitted: 0 });
