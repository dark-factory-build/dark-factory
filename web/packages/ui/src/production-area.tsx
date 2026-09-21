import { AgentSprite } from "./factory-scene/factory-scene.js";
import type { KeyboardEvent } from "react";
import { Contraption } from "./contraption.js";
import { productionStages, sharedDeliveries, type ProductionContraption, type ProductionView } from "./production-view.js";

export const productionColumns = (width: number) => Math.max(1, Math.min(4, Math.floor(width / 160)));
export const sharedChecks = (items: readonly ProductionContraption[]) => [...new Map(items.filter((item) => item.pullRequest?.state !== "merged").flatMap((item) => item.checks.filter((check) => check.applicable || check.scope === "merge_group").map((check) => [`${check.repository}:${check.id}`, check] as const))).values()];
export const productionHeight = (width: number, count: number, checks = 0) => 68 + Math.ceil(count / productionColumns(width)) * 132 + Math.ceil(checks / productionColumns(width)) * 104 + Math.ceil(2 / productionColumns(width)) * 120 + 48;
const label = (text: string) => text.length > 23 ? `${text.slice(0, 22)}…` : text;
const activate = (select: () => void) => ({ role: "button", tabIndex: 0, onClick: select, onKeyDown: (event: KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); } } });

/** Station props show concurrent evidence beside the same machine. No animation
 * advances work or moves a PR past an authority's decision. */
export function ProductionArea({ view, items, hiddenItems = 0, width, top, pulse, onSelect, selected }: {
  hiddenItems?: number; view: ProductionView; items: readonly ProductionContraption[]; width: number; top: number; pulse?: number;
  onSelect: (id: string) => void; selected?: string;
}) {
  const columns = productionColumns(width), cell = width / columns, executions = sharedChecks(items);
  const deliveries = sharedDeliveries(view);
  const factoryDeliveries = deliveries.filter((delivery) => delivery.destination.startsWith("runtime:") || delivery.destination === "site:app.darkfactory.build");
  const projectDeliveries = deliveries.filter((delivery) => !factoryDeliveries.includes(delivery));
  const dockTop = 68 + Math.ceil(items.length / columns) * 132 + Math.ceil(executions.length / columns) * 104;
  return <g transform={`translate(0 ${top})`} role="group" aria-label="Production area">
    <path d="M24 -24v32h24" stroke="#526360" strokeWidth="18" fill="none" />
    <rect x="8" y="8" width={width - 16} height={productionHeight(width, items.length, executions.length) - 16} fill="url(#df-floor)" stroke="#465355" strokeWidth="3" />
    <text x="24" y="30" fill="#c2b184" fontFamily="ui-monospace, monospace" fontSize="11">PRODUCTION · INSPECT WORK</text>
    {items.length === 0 ? <text x="24" y="52" fill="#8fa4ac" fontFamily="ui-monospace, monospace" fontSize="10">No work in progress.</text> : null}
    {items.map((item, index) => {
      const x = 16 + index % columns * cell, y = 52 + Math.floor(index / columns) * 132;
      const checks = item.checks.filter((check) => check.applicable);
      const failure = checks.some((check) => check.state === "completed" && ["failure", "timed_out", "action_required"].includes(check.conclusion));
      const running = checks.some((check) => ["running", "in_progress"].includes(check.state));
      const correction = item.review.current && item.review.state === "block" || failure || item.construction?.status === "blocked";
      const active = pulse !== undefined && (!item.pullRequest || item.review.sourceFresh) && (running || item.construction?.status === "running");
      const title = item.pullRequest?.title ?? item.construction?.title ?? "Work";
      const stages = productionStages(item);
      const color = correction ? "#d49b7d" : "#8fa4ac";
      return <g key={item.projectId + ":" + item.visualId} transform={`translate(${x} ${y})`}>
        <path d={`M0 79h${cell - 16}`} stroke="#455653" strokeWidth="12" />
        {Array.from({length: 7}, (_, i) => <path key={i} d={`M${8 + i * (cell - 32) / 7} 75v8`} stroke="#273134" strokeWidth="2" />)}
        <g aria-hidden="true" pointerEvents="none">
          <g transform="translate(8 10)"><Contraption identity={item.visualId} construction={item.construction !== undefined} correction={correction} active={active} reducedMotion={pulse === undefined} pulse={pulse} /></g>
          {/* Inspection bench, test chamber and merge gate occupy the same bay. */}
          <path d="M83 53h36v5H83 M87 58v12M115 58v12" fill="#655d4c" stroke="#9a927c" />
          <rect x="90" y="40" width="18" height="12" fill="#b4b4a0" stroke="#655d4c" />
          <path d="M93 44h12m-12 4h8" stroke="#536e70" />
          {item.reviewers.slice(0, 2).map((reviewer, reviewerIndex) => <g key={reviewer.id} transform={`translate(${85 + reviewerIndex * 20} 8)`} aria-label={`${reviewer.name}, external reviewer, ${reviewer.state}`}>
            <rect x="-2" y="-2" width="24" height="28" fill="#182c35" stroke="#638095" /><rect x="18" y="-2" width="4" height="4" fill={reviewer.state === "running" && item.review.sourceFresh ? "#80ddff" : reviewer.state === "block" ? "#d49b7d" : "#638095"} />
            <AgentSprite size={20} agent={{ id: reviewer.id, name: reviewer.name, role: "worker", provider: reviewer.provider === "claude_code" ? "claude_code" : "codex" }} activity={reviewer.state === "running" && item.review.sourceFresh && pulse !== undefined ? "busy" : "waiting"} />
            {reviewer.state === "running" && item.review.sourceFresh ? <path d={pulse !== undefined && Math.floor(pulse / 500) % 2 ? "M15 22l5 3" : "M15 24h5"} stroke="#c2b184" strokeWidth="2" /> : null}
          </g>)}
          {item.reviewers.length > 2 ? <text x={85 + Math.min(2, item.reviewers.length) * 20} y="24" fill="#c2b184" fontSize="9" fontFamily="ui-monospace, monospace">+{item.reviewers.length - 2}</text> : null}
          <path d="M122 57v17m0-17h9" stroke={item.pullRequest?.state === "merged" ? "#9fe7b0" : "#a08f68"} strokeWidth="3" />
          <text x="0" y="96" fill="#c9d3d0" fontSize="10" fontFamily="ui-monospace, monospace">{label(title)}</text>
          <text x="0" y="110" fill={color} fontSize="9" fontFamily="ui-monospace, monospace">{stages[0]}</text>
          <text x="0" y="123" fill="#8fa4ac" fontSize="9" fontFamily="ui-monospace, monospace">{stages.slice(1).join(" · ")}</text>
        </g>
        <g {...activate(() => onSelect(item.projectId + ":" + item.visualId))} aria-label={`Inspect ${title}`} className="dfFactoryScene__target">
          <rect className="dfFactoryScene__focus" x="0" y="0" width={cell - 24} height="128" fill="transparent" stroke={selected === item.projectId + ":" + item.visualId ? "#80ddff" : "none"} />
        </g>
      </g>;
    })}
    {executions.map((check, index) => {
      const item = items.find((item) => item.checks.some((candidate) => candidate.repository === check.repository && candidate.id === check.id));
      const running = item?.review.sourceFresh && ["running", "in_progress"].includes(check.state) && pulse !== undefined;
      const passed = item?.review.sourceFresh && check.state === "completed" && check.conclusion === "success";
      return <g key={`${check.repository}:${check.id}`} transform={`translate(${16 + index % columns * cell} ${60 + Math.ceil(items.length / columns) * 132 + Math.floor(index / columns) * 104})`} {...activate(() => { if (item) onSelect(item.projectId + ":" + item.visualId); })} aria-label={`Inspect shared check ${check.name}`} className="dfFactoryScene__target">
        <rect className="dfFactoryScene__focus" width={cell - 24} height="92" fill="transparent" />
        <rect x="8" y="4" width="100" height="46" fill="#303e40" stroke="#788379" strokeWidth="2" />
        <rect x="16" y="12" width="38" height="26" fill="#182c35" stroke="#536e70" />
        <path d={running && Math.floor(pulse / 400) % 2 ? "M20 26h6l4-8 5 14 5-6h9" : "M20 26h29"} stroke={passed ? "#9fe7b0" : check.conclusion === "failure" ? "#d49b7d" : "#8fa4ac"} fill="none" />
        <path d="M64 18h30m-30 8h30m-30 8h20M16 50v6m84-6v6" stroke="#9aa69c" strokeWidth="2" />
        <text y="70" fill="#c9d3d0" fontSize="10" fontFamily="ui-monospace, monospace">{label(check.name)}</text>
        <text y="84" fill="#8fa4ac" fontSize="9" fontFamily="ui-monospace, monospace">{check.pull_requests.length} PRs · {item?.review.sourceFresh ? check.conclusion || check.state : "stale"}</text>
      </g>;
    })}
    {[{ key: "maintenance", title: "FACTORY UPDATES", receipts: factoryDeliveries }, { key: "delivery", title: "DEPLOYMENTS", receipts: projectDeliveries }].map((dock, index) => {
      const latest = dock.receipts[0], applying = latest?.state === "running";
      return <g key={dock.key} transform={`translate(${16 + index % columns * cell} ${dockTop + Math.floor(index / columns) * 120})`}>
        <g aria-hidden="true" pointerEvents="none">
          <rect x="4" y="4" width={cell - 32} height="72" fill="#263638" stroke="#788379" strokeWidth="2" />
          <path d={`M10 68h${cell - 44}M10 62h${cell - 44}`} stroke="#455653" strokeWidth="3" />
          {dock.key === "maintenance" ? <><rect x="16" y="17" width="33" height="42" fill="#182c35" stroke="#9aa69c" /><path d="M21 24h20m-20 8h20m-20 8h20" stroke="#536e70" strokeWidth="3" /><path d="M62 26l14 18m-17-21 5-5 5 7-5 5m10 12 5 5" stroke="#c2b184" strokeWidth="3" /></> : <><path d="M15 58V28h38v30M22 28v-8h24v8" fill="#665f4e" stroke="#a08f68" strokeWidth="2" /><path d="M34 30v27m-18-19h36M68 59h22v-26H68z" fill="none" stroke="#9aa69c" strokeWidth="2" /></>}
          <rect x={cell - 48} y="16" width="7" height="7" fill={applying ? pulse !== undefined && Math.floor(pulse / 500) % 2 ? "#e5c58b" : "#a08f68" : latest?.state === "blocked" ? "#d49b7d" : "#536e70"} />
          <text x="8" y="91" fill="#c2b184" fontFamily="ui-monospace, monospace" fontSize="10">{dock.title}</text>
          <text x="8" y="105" fill="#8fa4ac" fontFamily="ui-monospace, monospace" fontSize="9">{dock.key === "maintenance" ? "Host + console versions" : "Project releases"}</text>
        </g>
        <g {...activate(() => onSelect(dock.key))} aria-label={`Inspect ${dock.title.toLowerCase()}`} className="dfFactoryScene__target"><rect className="dfFactoryScene__focus" width={cell - 24} height="112" fill="transparent" stroke={selected === dock.key ? "#80ddff" : "none"} /></g>
      </g>;
    })}
    {hiddenItems > 0 ? <g transform={`translate(16 ${productionHeight(width, items.length, executions.length) - 40})`} {...activate(() => onSelect(""))} aria-label={`View ${hiddenItems} more in-progress items`} className="dfFactoryScene__target"><rect className="dfFactoryScene__focus" width={width - 40} height="32" fill="#263638" /><text x="8" y="21" fill="#c2b184" fontSize="10" fontFamily="ui-monospace, monospace">{hiddenItems} more in progress · open list</text></g> : null}
  </g>;
}
