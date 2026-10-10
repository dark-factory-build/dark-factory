// Development preview of the shared floor: a PublicWorld fixture through the
// exported publicFloor + FactoryScene, rendered once to static HTML. No daemon,
// no Go, no provider login. Run `corepack pnpm run preview:floor` in web/,
// open the printed file, edit this world or packages/ui/src, run it again.
// ponytail: static markup (no animation or clicks); a bundler would make it live.
import { readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryScene, publicFloor } from "@dark-factory/ui";

const seen = { evidence: "both", observation: "observed", state: "active" };
const quiet = { evidence: "static", observation: "unobserved", state: "unknown", rate_per_hour: 0 };
/** @type {import("@dark-factory/ui").PublicWorld} */
const world = {
  generated_at: Date.now(),
  summary: { components: 6, inferred: 6, observed: 3, quiet: 0, partial: 0, stale: 0, unobserved: 3, opaque: 0, runtime_only: 0, contradicted: 0 },
  nodes: [
    { id: "api", kind: "processor", label: "API server", runtime: "server", ...seen, rate_per_hour: 600 },
    { id: "api-orders", kind: "ingress", label: "POST /orders", unit: "api", trigger: "request", ...seen, rate_per_hour: 600 },
    { id: "api-nightly", kind: "job", label: "nightly report", unit: "api", ...quiet },
    { id: "web", kind: "processor", label: "Web app", runtime: "browser", ...seen, rate_per_hour: 30 },
    { id: "db", kind: "store", label: "orders db", ...quiet },
    { id: "pay", kind: "external", label: "payments", ...quiet },
  ],
  edges: [
    { from: "web", to: "api-orders", kind: "calls", ...seen, rate_per_hour: 30 },
    { from: "api", to: "db", kind: "uses", ...seen, rate_per_hour: 600 },
    { from: "api", to: "pay", kind: "calls", ...quiet },
  ],
  workers: [{ activity: "busy", unit: "api" }, { activity: "idle" }],
  crates: [{ id: "a".repeat(32), station: 1 }],
};

const { graph, workers, crates } = publicFloor(world);
const scene = renderToStaticMarkup(createElement(FactoryScene, { graph, workers, crates, connected: true }));
const css = readFileSync(new URL(import.meta.resolve("@dark-factory/ui/styles.css")), "utf8");
const out = process.argv[2] ?? join(tmpdir(), "dark-factory-floor-preview.html");
writeFileSync(out, `<!doctype html><meta charset="utf-8"><title>Floor preview</title><style>${css}</style><body style="margin:0;background:#08131d">${scene}`);
console.log(out);
