/** Facts persisted by the operator production projection. */
export type ProductionRecord = Readonly<{
  repository: string;
  kind: string;
  id: string;
  visual_id: string;
  observed_at: number;
  document: unknown;
  tasks: readonly string[];
  missions: readonly string[];
}>;

export type ProductionCheck = Readonly<{
  id: string;
  name: string;
  revision: string;
  scope: string;
  state: string;
  conclusion: string;
  url?: string;
  pull_requests: readonly number[];
}>;

export type ProductionDelivery = Readonly<{
  id: string;
  kind: string;
  destination: string;
  revision: string;
  state: string;
  url?: string;
  pull_requests: readonly number[];
  verified_at?: number;
}>;

export type ProductionReviewer = Readonly<{
  id: string;
  number: number;
  head: string;
  name: string;
  provider: string;
  state: string;
  url?: string;
  findings?: string;
}>;

type PullRequest = Readonly<{
  number: number;
  title: string;
  url?: string;
  head: string;
  branch?: string;
  base?: string;
  state: string;
  merge?: string;
  review?: Readonly<{ head?: string; state?: string; url?: string; findings?: string }>;
  next_action?: string;
}>;

type Construction = Readonly<{
  title?: string;
  phase?: string;
  status?: string;
  head?: string;
  task_id?: string;
  blocked_reason?: string;
}>;

export type ProductionContraption = Readonly<{
  visualId: string;
  repository: string;
  pullRequest?: PullRequest;
  construction?: Construction;
  tasks: readonly string[];
  missions: readonly string[];
  review: Readonly<{ head: string; state: string; current: boolean; allowed: boolean; findings: string; url: string }>;
  checks: readonly (ProductionCheck & { applicable: boolean })[];
  delivery?: ProductionDelivery & { verified: boolean };
  completed: boolean;
  status: string;
  nextAction: string;
}>;

export type ProductionView = Readonly<{
  contraptions: Readonly<Record<string, ProductionContraption>>;
  checks: Readonly<Record<string, ProductionCheck>>;
  deliveries: Readonly<Record<string, ProductionDelivery>>;
  reviewers: Readonly<Record<string, ProductionReviewer>>;
}>;

const text = (value: unknown) => typeof value === "string" ? value : "";
const list = (value: unknown) => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
const numbers = (value: unknown) => Array.isArray(value) ? value.filter((item): item is number => typeof item === "number" && Number.isSafeInteger(item) && item > 0) : [];
const object = (value: unknown): Record<string, unknown> => value !== null && typeof value === "object" ? value as Record<string, unknown> : {};
const byObserved = <T extends { observed_at: number }>(left: T, right: T) => left.observed_at - right.observed_at;

function latestRecords(records: readonly ProductionRecord[]) {
  const latest = new Map<string, ProductionRecord>();
  for (const record of records) {
    const key = `${record.repository}\0${record.kind}\0${record.id}`;
    const prior = latest.get(key);
    if (prior === undefined || byObserved(prior, record) <= 0) latest.set(key, record);
  }
  return [...latest.values()];
}

function verifiedDelivery(delivery: ProductionDelivery | undefined) {
  return delivery !== undefined && delivery.verified_at !== undefined && delivery.verified_at > 0 && delivery.state === "verified";
}

function nextAction(pr: PullRequest, review: ProductionContraption["review"], checks: readonly (ProductionCheck & { applicable: boolean })[], delivery: ProductionContraption["delivery"]) {
  if (pr.next_action) return pr.next_action;
  if (pr.state === "merged" && !delivery?.verified) return "Merged; delivery verification is pending.";
  if (pr.state === "closed" && !pr.merge) return "Closed without merge.";
  if (!review.current || review.state !== "allow") return review.current ? "Independent review is required." : "Review is stale for the current head.";
  if (checks.some((check) => check.applicable && ["unknown", "unavailable", "skipped", "notapplicable"].includes(check.state))) return "A current-head check is unavailable or incomplete.";
  if (checks.some((check) => check.applicable && check.conclusion !== "success")) return "A current-head check is not successful.";
  return "Awaiting the next recorded publication observation.";
}

/** Pure, bounded derivation for the production view. `now` is reserved for future staleness policy. */
export function deriveProductionView(records: readonly ProductionRecord[], _now = 0): ProductionView {
  const latest = latestRecords(records);
  const prs = latest.filter((record) => record.kind === "pull_request");
  const constructions = latest.filter((record) => record.kind === "construction");
  const checks: Record<string, ProductionCheck> = {};
  const deliveries: Record<string, ProductionDelivery> = {};
  const reviewers: Record<string, ProductionReviewer> = {};
  for (const record of latest) {
    const document = object(record.document);
    if (record.kind === "check") checks[record.id] = { id: record.id, name: text(document.name), revision: text(document.revision), scope: text(document.scope), state: text(document.state), conclusion: text(document.conclusion), url: text(document.url), pull_requests: numbers(document.pull_requests) };
    if (record.kind === "delivery") deliveries[record.id] = { id: record.id, kind: text(document.kind), destination: text(document.destination), revision: text(document.revision), state: text(document.state), url: text(document.url), pull_requests: numbers(document.pull_requests), verified_at: typeof document.verified_at === "number" ? document.verified_at : undefined };
    if (record.kind === "reviewer") reviewers[record.id] = { id: record.id, number: Number(document.number) || 0, head: text(document.head), name: text(document.name), provider: text(document.provider), state: text(document.state), url: text(document.url), findings: text(document.findings) };
  }
  const contraptions: Record<string, ProductionContraption> = {};
  for (const record of [...prs, ...constructions].sort((a, b) => a.visual_id.localeCompare(b.visual_id) || a.kind.localeCompare(b.kind))) {
    const document = object(record.document);
    const existing = contraptions[record.visual_id];
    if (record.kind === "construction") {
      contraptions[record.visual_id] = { visualId: record.visual_id, repository: record.repository, construction: { title: text(document.title), phase: text(document.phase), status: text(document.status), head: text(document.head), task_id: text(document.task_id), blocked_reason: text(document.blocked_reason) }, tasks: [...record.tasks], missions: [...record.missions], review: existing?.review ?? { head: "", state: "unknown", current: false, allowed: false, findings: "", url: "" }, checks: existing?.checks ?? [], completed: false, status: text(document.status) || text(document.phase) || "construction", nextAction: text(document.blocked_reason) || "Construction is in progress." };
      continue;
    }
    const pr = document as PullRequest;
    const review = pr.review ?? {};
    const current = text(review.head) === text(pr.head) && text(pr.head) !== "";
    const reviewView = { head: text(review.head), state: text(review.state) || "unknown", current, allowed: current && text(review.state) === "allow", findings: text(review.findings), url: text(review.url) };
    const applicable = Object.values(checks).filter((check) => check.pull_requests.includes(pr.number)).map((check) => ({ ...check, applicable: check.scope === "head" && check.revision === pr.head }));
    const delivery = Object.values(deliveries).find((item) => item.pull_requests.includes(pr.number));
    const verified = verifiedDelivery(delivery);
    const closedUnmerged = pr.state === "closed" && !pr.merge;
    contraptions[record.visual_id] = { visualId: record.visual_id, repository: record.repository, pullRequest: pr, tasks: [...record.tasks], missions: [...record.missions], review: reviewView, checks: applicable, delivery: delivery ? { ...delivery, verified } : undefined, completed: verified || closedUnmerged, status: verified ? "delivered" : closedUnmerged ? "closed-unmerged" : pr.state || "unknown", nextAction: nextAction(pr, reviewView, applicable, delivery ? { ...delivery, verified } : undefined) };
  }
  return { contraptions, checks, deliveries, reviewers };
}
