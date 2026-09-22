/** Facts persisted by the operator production projection. */
export type ProductionRecord = Readonly<{
  project_id?: string;
  repository: string;
  kind: string;
  id: string;
  visual_id: string;
  observed_at: number;
  links_overflow?: boolean;
  document: unknown;
  tasks: readonly string[];
  missions: readonly string[];
}>;

export type ProductionJob = Readonly<{ id: string; name: string; state: string; conclusion: string; url?: string }>;
export type ProductionCheck = Readonly<{
  project_id?: string; repository: string; id: string; name: string; revision: string; scope: string;
  state: string; conclusion: string; url?: string; pull_requests: readonly number[]; jobs: readonly ProductionJob[]; overflow: number;
}>;
export type ProductionDelivery = Readonly<{
  project_id?: string; repository: string; id: string; kind: string; destination: string; revision: string;
  state: string; url?: string; pull_requests: readonly number[]; verified_at?: number; overflow?: number; updated_at?: number; phase?: string; reason?: string;
}>;
export type ProductionReviewer = Readonly<{
  project_id?: string; repository: string; id: string; number: number; head: string; name: string;
  provider: string; state: string; url?: string; findings?: string;
}>;

type PullRequest = Readonly<{
  number: number; title: string; url?: string; head: string; branch?: string; base?: string; state: string;
  merge?: string; merge_queue?: string; merged_at?: string; review?: Readonly<{ head?: string; state?: string; url?: string; findings?: string }>;
  next_action?: string;
}>;
type Construction = Readonly<{ title?: string; phase?: string; status?: string; head?: string; task_id?: string; blocked_reason?: string; has_changes?: boolean }>;
type Scoped = Readonly<{ projectId: string; repository: string }>;

export type ProductionContraption = Readonly<{
  visualId: string; projectId: string; repository: string; pullRequest?: PullRequest; construction?: Construction;
  tasks: readonly string[]; missions: readonly string[]; linksOverflow: boolean;
  review: Readonly<{ head: string; state: string; current: boolean; allowed: boolean; sourceFresh: boolean; findings: string; url: string }>;
  checks: readonly (ProductionCheck & { applicable: boolean })[];
  deliveries: readonly (ProductionDelivery & { verified: boolean })[];
  reviewers: readonly ProductionReviewer[];
  completed: boolean; completedAt: number; status: string; nextAction: string;
}>;
export type ProductionView = Readonly<{
  contraptions: Readonly<Record<string, ProductionContraption>>;
  checks: Readonly<Record<string, ProductionCheck>>;
  deliveries: Readonly<Record<string, ProductionDelivery>>;
  reviewers: Readonly<Record<string, ProductionReviewer>>;
}>;

const STALE_AFTER = 180_000;
const text = (value: unknown) => typeof value === "string" ? value : "";
const list = (value: unknown) => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
const numbers = (value: unknown) => Array.isArray(value) ? value.filter((item): item is number => typeof item === "number" && Number.isSafeInteger(item) && item > 0) : [];
const object = (value: unknown): Record<string, unknown> => value !== null && typeof value === "object" ? value as Record<string, unknown> : {};
const scoped = (record: Pick<ProductionRecord, "project_id" | "repository">): Scoped => ({ projectId: record.project_id ?? "", repository: record.repository });
const scopeKey = (value: Scoped, id: string) => `${value.projectId}\0${value.repository}\0${id}`;

function latestRecords(records: readonly ProductionRecord[]) {
  const latest = new Map<string, ProductionRecord>();
  for (const record of records) {
    const key = `${record.project_id ?? ""}\0${record.repository}\0${record.kind}\0${record.id}`;
    const prior = latest.get(key);
    if (prior === undefined || prior.observed_at <= record.observed_at) latest.set(key, record);
  }
  return [...latest.values()];
}
function fresh(record: ProductionRecord | undefined, now: number) {
  if (now <= 0) return true;
  if (record === undefined) return false;
  const document = object(record.document);
  const missing = text(document.unavailable).split(",").filter(Boolean);
  return record.observed_at > 0 && now - record.observed_at <= STALE_AFTER && missing.every((part) => ["jobs", "review_journal", "release_journal", "release_config"].includes(part));
}
function activityState(record: ProductionRecord, value: unknown, now: number) {
  const state = text(value);
  return ["running", "in_progress", "queued", "waiting"].includes(state) && !fresh(record, now) ? "stale" : state;
}
function verifiedDelivery(delivery: ProductionDelivery) {
  return delivery.verified_at !== undefined && delivery.verified_at > 0 && delivery.state === "verified";
}
// Timestamps have second resolution. Without stronger ordering evidence a tie
// cannot let a verified receipt hide an unresolved attempt.
function deliveryOrder(a: ProductionDelivery, b: ProductionDelivery) {
  return (b.updated_at || b.verified_at || 0) - (a.updated_at || a.verified_at || 0)
    || Number(verifiedDelivery(a)) - Number(verifiedDelivery(b))
    || a.id.localeCompare(b.id);
}
function latestDestinations(deliveries: readonly ProductionContraption["deliveries"][number][]) {
  const latest = new Map<string, typeof deliveries[number]>();
  for (const delivery of deliveries) {
    const previous = latest.get(delivery.destination);
    if (previous === undefined || deliveryOrder(delivery, previous) < 0) latest.set(delivery.destination, delivery);
  }
  return [...latest.values()];
}
function nextAction(pr: PullRequest, review: ProductionContraption["review"], checks: readonly (ProductionCheck & { applicable: boolean })[], deliveries: readonly ProductionContraption["deliveries"][number][]) {
  if (pr.state === "closed" && !pr.merge) return "Closed without merge.";
  if (pr.state === "merged") return deliveries.length > 0 && latestDestinations(deliveries).every((delivery) => delivery.verified) ? "Delivery verified at all recorded destinations." : "Merged; delivery verification is pending.";
  if (pr.next_action) return pr.next_action;
  if (review.current && review.state === "block") return "Changes requested; inspect the review findings.";
  if (!review.current) return review.head ? "Review is stale for the current head." : "Independent review is required.";
  if (!review.sourceFresh) return "Production source observation is stale or unavailable.";
  if (review.state !== "allow") return "Independent review is required.";
  if (checks.some((check) => check.applicable && ["running", "in_progress", "queued", "waiting"].includes(check.state))) return "Current-head checks are running.";
  if (checks.some((check) => check.applicable && ["unknown", "stale", "unavailable", "skipped", "notapplicable"].includes(check.state))) return "A current-head check is unavailable or incomplete.";
  if (checks.some((check) => check.applicable && check.conclusion && check.conclusion !== "success")) return "A current-head check is not successful.";
  return "Awaiting the next recorded publication observation.";
}

function constructionNextAction(doc: Record<string, unknown>) {
  if (text(doc.blocked_reason)) return text(doc.blocked_reason);
  switch (text(doc.status)) {
    case "running": return "Work is running.";
    case "queued": return "Waiting for a worker.";
    case "succeeded": return doc.has_changes === false ? "Work finished without a source change." : "Work finished; no linked publication is recorded.";
    case "failed": return "Work failed; inspect the task result.";
    case "cancelled": return "Work was cancelled.";
    case "blocked": return "Work is blocked; inspect the task.";
    default: return "Work state is unavailable.";
  }
}

/** Shared queue membership and stage labels for the floor and inspector. */
export function inProgressProduction(item: ProductionContraption): boolean {
  if (item.completed) return false;
  if (item.pullRequest) return true;
  return item.construction?.status !== "cancelled"
    && !(item.construction?.has_changes === false && item.construction.status === "succeeded");
}

export function productionStages(item: ProductionContraption): string[] {
  if (item.completed) return [item.status === "delivered" ? "Delivered" : "Closed"];
  const pr = item.pullRequest;
  if (!pr) {
    const status = item.construction?.status;
    return [status === "running" ? "Construction" : status === "queued" ? "Queued" : status === "blocked" ? "Blocked" : status === "failed" ? "Failed" : status === "cancelled" ? "Cancelled" : status === "succeeded" ? item.construction?.has_changes === false ? "Finished" : "Publication unrecorded" : "Work unknown"];
  }
  if (pr.state === "merged") {
    const destinations = latestDestinations(item.deliveries);
    return ["Merged", destinations.some((receipt) => receipt.state === "blocked" || receipt.state === "failed") ? "Delivery blocked" : destinations.some((receipt) => receipt.state === "running") ? "Deploying" : "Delivery unverified"];
  }
  const checks = item.checks.filter((check) => check.applicable);
  const failed = checks.some((check) => ["failure", "timed_out", "action_required"].includes(check.conclusion));
  const ci = !item.review.sourceFresh ? "CI stale" : failed ? "CI failed" : checks.some((check) => ["running", "in_progress"].includes(check.state)) ? "CI running" : checks.some((check) => ["queued", "waiting", "pending", "requested"].includes(check.state)) ? "CI queued" : checks.some((check) => check.state === "cancelled" || check.conclusion === "cancelled") ? "CI cancelled" : checks.some((check) => check.state === "skipped" || check.conclusion === "skipped") ? "CI skipped" : checks.length > 0 && checks.every((check) => check.state === "completed" && check.conclusion === "success") ? "CI passed" : "CI unknown";
  const correction = item.review.current && item.review.state === "block" || failed;
  const review = !item.review.sourceFresh ? "Review stale" : item.review.allowed ? "Review passed" : (item.review.current && item.review.state === "running" || item.reviewers.some((reviewer) => reviewer.state === "running")) ? "Review running" : "Review pending";
  const merge = pr.merge_queue && !["none", "unknown"].includes(pr.merge_queue) ? "Merge queued" : item.review.allowed && ci === "CI passed" ? "Merge" : undefined;
  return correction ? ["Correction", ci] : merge ? [merge, ci] : [review, ci];
}

/** Pure, bounded derivation. `now` is an explicit observation time, never a timer. */
export function deriveProductionView(records: readonly ProductionRecord[], now = 0): ProductionView {
  const latest = latestRecords(records);
  const health = new Map<string, ProductionRecord>();
  const checks: Record<string, ProductionCheck> = {}, deliveries: Record<string, ProductionDelivery> = {}, reviewers: Record<string, ProductionReviewer> = {};
  for (const record of latest) {
    const scope = scoped(record), key = scopeKey(scope, record.id), document = object(record.document);
    if (record.kind === "repository") health.set(scopeKey(scope, "repository"), record);
    if (record.kind === "check") checks[key] = { project_id: record.project_id, repository: record.repository, id: record.id, name: text(document.name), revision: text(document.revision), scope: text(document.scope), state: activityState(record, document.state, now), conclusion: text(document.conclusion), url: text(document.url), pull_requests: numbers(document.pull_requests), jobs: Array.isArray(document.jobs) ? document.jobs.map((job) => { const value = object(job); return { id: text(value.id), name: text(value.name), state: text(value.state), conclusion: text(value.conclusion), url: text(value.url) }; }) : [], overflow: typeof document.overflow === "number" ? document.overflow : 0 };
    if (record.kind === "delivery") deliveries[key] = { project_id: record.project_id, repository: record.repository, id: record.id, kind: text(document.kind), destination: text(document.destination), revision: text(document.revision), state: activityState({ ...record, observed_at: Number(document.updated_at) || record.observed_at }, document.state, now), url: text(document.url), pull_requests: numbers(document.pull_requests), phase: text(document.phase), reason: text(document.reason), updated_at: Number(document.updated_at) || 0, overflow: Number(document.overflow) || 0, verified_at: typeof document.verified_at === "number" ? document.verified_at : undefined };
    if (record.kind === "reviewer") reviewers[key] = { project_id: record.project_id, repository: record.repository, id: record.id, number: Number(document.number) || 0, head: text(document.head), name: text(document.name), provider: text(document.provider), state: activityState(record, document.state, now), url: text(document.url), findings: text(document.findings) };
  }
  const contraptions: Record<string, ProductionContraption> = {};
  const constructionRecords = latest.filter((record) => record.kind === "construction");
  const pullRecords = latest.filter((record) => record.kind === "pull_request");
  const allVisuals = new Map<string, { scope: Scoped; visualId: string; construction?: ProductionRecord; pull?: ProductionRecord }>();
  for (const record of [...constructionRecords, ...pullRecords]) {
    const key = scopeKey(scoped(record), record.visual_id), prior = allVisuals.get(key) ?? { scope: scoped(record), visualId: record.visual_id };
    if (record.kind === "construction") prior.construction = record; else prior.pull = record;
    allVisuals.set(key, prior);
  }
  for (const [key, item] of allVisuals) {
    const construction = item.construction, pull = item.pull, pr = pull ? object(pull.document) as PullRequest : undefined;
    const repositoryRecord = health.get(scopeKey(item.scope, "repository"));
    const sourceFresh = fresh(repositoryRecord, now) && (pull === undefined || fresh(pull, now));
    const pullChecks = pr ? Object.values(checks).filter((check) => (check.project_id ?? "") === item.scope.projectId && check.repository === item.scope.repository && check.pull_requests.includes(pr.number)).map((check) => ({ ...check, applicable: check.scope === "head" && check.revision === pr.head })) : [];
    const pullDeliveries = pr ? Object.values(deliveries).filter((delivery) => (delivery.project_id ?? "") === item.scope.projectId && delivery.repository === item.scope.repository && delivery.pull_requests.includes(pr.number)).map((delivery) => ({ ...delivery, verified: verifiedDelivery(delivery) })) : [];
    const assigned = pr ? Object.values(reviewers).filter((reviewer) => (reviewer.project_id ?? "") === item.scope.projectId && reviewer.repository === item.scope.repository && reviewer.number === pr.number && reviewer.head === pr.head) : [];
    const review = pr?.review ?? {};
    const reviewView = { head: text(review.head), state: text(review.state) || "unknown", current: text(review.head) !== "" && text(review.head) === pr?.head, allowed: sourceFresh && text(review.head) !== "" && text(review.head) === pr?.head && text(review.state) === "allow", sourceFresh, findings: text(review.findings), url: text(review.url) };
    const closedUnmerged = pr?.state === "closed" && !pr.merge;
    const completed = closedUnmerged || pr?.state === "merged" && pullDeliveries.length > 0 && latestDestinations(pullDeliveries).every((delivery) => delivery.verified);
    const doc = construction ? object(construction.document) : {};
    contraptions[key] = { visualId: item.visualId, projectId: item.scope.projectId, repository: item.scope.repository, construction: construction ? { title: text(doc.title), phase: text(doc.phase), status: text(doc.status), head: text(doc.head), task_id: text(doc.task_id), blocked_reason: text(doc.blocked_reason), has_changes: typeof doc.has_changes === "boolean" ? doc.has_changes : undefined } : undefined, pullRequest: pr, tasks: [...new Set([...(construction?.tasks ?? []), ...(pull?.tasks ?? [])])], missions: [...new Set([...(construction?.missions ?? []), ...(pull?.missions ?? [])])], linksOverflow: Boolean(construction?.links_overflow || pull?.links_overflow), review: reviewView, checks: pullChecks, deliveries: pullDeliveries, reviewers: assigned, completed: Boolean(completed), completedAt: Date.parse(pr?.merged_at ?? "") || pull?.observed_at || 0, status: completed ? (closedUnmerged ? "closed-unmerged" : "delivered") : text(pr?.state) || text(doc.status) || text(doc.phase) || "construction", nextAction: pr ? nextAction(pr, reviewView, pullChecks, pullDeliveries) : constructionNextAction(doc) };
  }
  return { contraptions, checks, deliveries, reviewers };
}
