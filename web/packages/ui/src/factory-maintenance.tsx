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
  deliveries: readonly (ProductionDelivery & Readonly<{ phase?: string; reason?: string; updated_at?: number }>)[];
}>;

const link = (value: string) => {
  try { const url = new URL(value); return url.protocol === "https:" && url.username === "" && url.password === "" ? url.href : undefined; } catch { return undefined; }
};
const observedAt = (value: number | undefined) => {
  if (value === undefined || !Number.isFinite(value) || value <= 0) return "";
  try { return new Date(value).toISOString(); } catch { return ""; }
};

function Identity({ value, state }: { value: FactoryBuild; state?: string }) {
  return <>
    <p>{state || "unknown"} · {value.release ? "release receipt" : "no release receipt"}</p>
    <p>version <code>{value.version || "not observed"}</code> · target <code>{value.target || "not observed"}</code></p>
    <p>source <code style={{ overflowWrap: "anywhere" }}>{value.source || "not observed"}</code></p>
    <p>build <code style={{ overflowWrap: "anywhere" }}>{value.build_id || "not observed"}</code></p>
  </>;
}

/** Read-only maintenance evidence. Each source is deliberately shown without equivalence claims. */
export function FactoryMaintenancePanel({ maintenance, runtime, connected, sourceFresh, hostedSource, deliveries }: FactoryMaintenanceProps) {
  const current = connected && sourceFresh;
  const serviceSourcesMatch = current && maintenance !== undefined && maintenance.installed.source !== "" && maintenance.installed.source === maintenance.running.source;
  return <section className="dfFactoryConsole__section" aria-label="Factory service maintenance">
    <div className="dfFactoryConsole__sectionHeading"><h2>Factory service</h2><span>{maintenance?.destination || "host observation"}</span></div>
    {!current ? <p role="status">Disconnected or stale observation. No runtime update is confirmed.</p> : null}
    <p className="dfFactoryConsole__empty">Release, installed files, service reports, private runtime, and this console are separate observations.</p>
    {serviceSourcesMatch ? <p role="status">Installed service files and the running service report the same source in this observation.</p> : null}
    <div className="dfFactoryConsole__columns">
      <article className="dfFactoryConsole__card"><h3>Available release</h3>{maintenance ? <><p>{maintenance.available.state} · <code>{maintenance.available.version || "not observed"}</code></p>{link(maintenance.available.url) ? <p><a href={maintenance.available.url} target="_blank" rel="noreferrer">Open release</a></p> : <p>No release link observed.</p>}</> : <p>Not observed.</p>}</article>
      <article className="dfFactoryConsole__card"><h3>Installed service files</h3>{maintenance ? <Identity value={maintenance.installed} state={maintenance.installed.state} /> : <p>Not observed.</p>}</article>
      <article className="dfFactoryConsole__card"><h3>Running service report</h3>{maintenance ? <Identity value={maintenance.running} state={maintenance.running.state} /> : <p>Not observed.</p>}</article>
      <article className="dfFactoryConsole__card"><h3>Private runtime observation</h3>{runtime ? <Identity value={runtime} /> : <p>Not observed.</p>}</article>
      <article className="dfFactoryConsole__card"><h3>Loaded hosted console</h3><p>source <code style={{ overflowWrap: "anywhere" }}>{hostedSource || "not provided"}</code></p><p>This browser metadata is not a service receipt.</p></article>
      <article className="dfFactoryConsole__card"><h3>Delivery evidence</h3>{deliveries.length === 0 ? <p>No delivery evidence recorded.</p> : <ul>{deliveries.map((delivery) => <li key={`${delivery.repository}:${delivery.id}`}>{delivery.destination} · {current ? delivery.state : `last recorded ${delivery.state}; not current confirmation`}{delivery.phase ? ` · ${delivery.phase}` : ""}{delivery.reason ? ` · ${delivery.reason}` : ""}{observedAt(delivery.updated_at) ? ` · updated ${observedAt(delivery.updated_at)}` : ""} · revision <code style={{ overflowWrap: "anywhere" }}>{delivery.revision || "not observed"}</code>{link(delivery.url || "") ? <> · <a href={delivery.url} target="_blank" rel="noreferrer">Open delivery</a></> : null}</li>)}</ul>}</article>
    </div>
    <p>Console updates are unsupported. Use the configured release-only controller with its existing <code>scripts/deploy-runtime.py</code> and <code>scripts/verify-live-runtime.py</code> command arrays for an exact merged release; this view cannot start an update.</p>
  </section>;
}
