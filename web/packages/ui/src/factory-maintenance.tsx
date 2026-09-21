import { useState } from "react";
import type { ProductionDelivery } from "./production-view.js";

export type FactoryBuild = Readonly<{ version: string; source: string; target: string; build_id: string; release: boolean }>;
export type FactoryMaintenance = Readonly<{
  destination: string; state: string;
  available: Readonly<{ version: string; url: string; state: string }>;
  installed: FactoryBuild & Readonly<{ state: string }>;
  running: FactoryBuild & Readonly<{ state: string }>;
}>;
export type FactoryMaintenanceProps = Readonly<{
  maintenance?: FactoryMaintenance; runtime?: FactoryBuild; connected: boolean; sourceFresh: boolean; hostedSource?: string;
  deliveries: readonly ProductionDelivery[];
}>;

const link = (value: string) => {
  try { const url = new URL(value); return url.protocol === "https:" && url.username === "" && url.password === "" ? url.href : undefined; } catch { return undefined; }
};
const observedAt = (value: number | undefined) => {
  if (value === undefined || !Number.isFinite(value) || value <= 0) return "";
  try { return new Date(value).toISOString(); } catch { return ""; }
};

/** One receipt inspector for project deliveries and factory maintenance. */
export function DeliveryEvidence({ deliveries, current = true }: { deliveries: readonly ProductionDelivery[]; current?: boolean }) {
  const [limit, setLimit] = useState(8);
  const remaining = deliveries.length - limit;
  return <>{deliveries.length === 0 ? <p>No delivery evidence recorded.</p> : deliveries.slice(0, limit).map((delivery) => <details key={`${delivery.repository}:${delivery.id}`}>
    <summary style={{overflowWrap:"anywhere"}}>{delivery.destination} · {current ? delivery.state : `last recorded ${delivery.state}; not current confirmation`} · {delivery.pull_requests.length} linked PR{delivery.pull_requests.length === 1 ? "" : "s"}</summary>
    <p>{delivery.phase}{delivery.reason ? ` · ${delivery.reason}` : ""}</p>
    <p>Revision <code style={{overflowWrap:"anywhere"}}>{delivery.revision || "not observed"}</code></p>
    {observedAt(delivery.updated_at) ? <p>Updated {observedAt(delivery.updated_at)}</p> : null}
    {observedAt(delivery.verified_at) ? <p>Verified {observedAt(delivery.verified_at)}</p> : <p>Delivery not verified.</p>}
    {link(delivery.url || "") ? <p><a href={delivery.url} target="_blank" rel="noreferrer">Open delivery</a></p> : null}
    <ul>{delivery.pull_requests.map((number) => <li key={number}><a href={`https://github.com/${delivery.repository}/pull/${number}`} target="_blank" rel="noreferrer">{delivery.repository} #{number}</a></li>)}</ul>
    {delivery.overflow ? <p>{delivery.overflow} additional links are outside this observation.</p> : null}
  </details>)}{remaining > 0 ? <><p id="delivery-evidence-remaining">{remaining} more delivery receipts available.</p><button type="button" aria-describedby="delivery-evidence-remaining" onClick={() => setLimit((value) => value + 8)}>Show more delivery evidence</button></> : null}</>;
}

function Identity({ value, state }: { value: FactoryBuild; state?: string }) {
  return <>
    <p>{state || "unknown"} · {value.release ? "release receipt" : "no release receipt"}</p>
    <p>Version <code>{value.version || "not observed"}</code></p>
    <details><summary>Build details</summary><p>Source <code style={{ overflowWrap: "anywhere" }}>{value.source || "not observed"}</code></p><p>Target <code>{value.target || "not observed"}</code></p><p>Build <code style={{ overflowWrap: "anywhere" }}>{value.build_id || "not observed"}</code></p></details>
  </>;
}

/** Read-only maintenance evidence. Each source is deliberately shown without equivalence claims. */
export function FactoryMaintenancePanel({ maintenance, runtime, connected, sourceFresh, hostedSource, deliveries }: FactoryMaintenanceProps) {
  const current = connected && sourceFresh;
  const running = runtime ?? maintenance?.running;
  const serviceSourcesMatch = current && runtime?.release === true && maintenance?.installed.state === "verified" && maintenance.installed.source !== "" && maintenance.installed.source === runtime.source;
  return <section aria-label="Factory service maintenance">
    <h3>Factory updates</h3><details><summary>Host destination</summary><p style={{overflowWrap:"anywhere"}}>{maintenance?.destination || "not observed"}</p></details>
    {!current ? <p role="status">Disconnected or stale observation. No runtime update is confirmed.</p> : null}
    {serviceSourcesMatch ? <p role="status">The running host matches the verified installed source.</p> : null}
    <div className="dfFactoryConsole__list">
      <section aria-labelledby="factory-host-heading"><h4 id="factory-host-heading">Host</h4>
        <article className="dfFactoryConsole__card"><h5>Available release</h5>{maintenance ? <><p>{maintenance.available.state} · <code>{maintenance.available.version || "not observed"}</code></p>{link(maintenance.available.url) ? <p><a href={maintenance.available.url} target="_blank" rel="noreferrer">Open release</a></p> : <p>No release link observed.</p>}</> : <p>Not observed.</p>}</article>
        <article className="dfFactoryConsole__card"><h5>Installed service files</h5>{maintenance ? <Identity value={maintenance.installed} state={maintenance.installed.state} /> : <p>Not observed.</p>}</article>
        <article className="dfFactoryConsole__card"><h5>Running host</h5>{running ? <Identity value={running} state={runtime && connected ? "observed through this connection" : "last observed"} /> : <p>Not observed.</p>}</article>
      </section>
      <section aria-labelledby="factory-hosted-heading"><h4 id="factory-hosted-heading">Hosted console</h4>
        <article className="dfFactoryConsole__card"><p>Source <code style={{ overflowWrap: "anywhere" }}>{hostedSource || "not provided"}</code></p><p>The console bundle and host runtime can run different revisions.</p></article>
        <article className="dfFactoryConsole__card"><h5>Delivery evidence</h5><DeliveryEvidence deliveries={deliveries} current={current} /></article>
      </section>
    </div>
    <details><summary>Host update instructions</summary><p>This console has no host installation command. On the configured host, use the existing safe runtime deployment path with an exact merged revision:</p><code style={{overflowWrap:"anywhere"}}>python3 scripts/deploy-runtime.py --home FACTORY_HOME MERGED_SHA</code><p>The release controller uses this same path. It prepares the update, preserves adoptable work, verifies the running version and restores admission. An available release does not enable automatic updates.</p></details>
  </section>;
}
