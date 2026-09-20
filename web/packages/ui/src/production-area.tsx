import { AgentSprite } from "./factory-scene/factory-scene.js";
import type { KeyboardEvent } from "react";
import { Contraption } from "./contraption.js";
import type { ProductionContraption, ProductionView } from "./production-view.js";

export const productionColumns = (width: number) => Math.max(1, Math.min(4, Math.floor(width / 160)));
export const sharedChecks = (items: readonly ProductionContraption[]) => [...new Map(items.flatMap((item) => item.checks.filter((check) => check.applicable || check.scope === "merge_group").map((check) => [check.id, check] as const))).values()];
export const productionHeight = (width: number, count: number, checks = 0) => 68 + Math.max(1, Math.ceil(count / productionColumns(width))) * 144 + Math.ceil(checks / productionColumns(width)) * 104;
const label = (text: string) => text.length > 23 ? `${text.slice(0, 22)}…` : text;
const activate = (select: () => void) => ({ role: "button", tabIndex: 0, onClick: select, onKeyDown: (event: KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); } } });

/** Station props show concurrent evidence beside the same machine. No animation
 * advances work or moves a PR past an authority's decision. */
export function ProductionArea({ view, items, width, top, pulse, onSelect, selected }: {
  view: ProductionView; items: readonly ProductionContraption[]; width: number; top: number; pulse?: number;
  onSelect: (id: string) => void; selected?: string;
}) {
  const columns = productionColumns(width), cell = width / columns, executions = sharedChecks(items);
  return <g transform={`translate(0 ${top})`} role="group" aria-label="Production area">
    <path d="M24 -24v32h24" stroke="#526360" strokeWidth="18" fill="none" />
    <rect x="8" y="8" width={width - 16} height={productionHeight(width, items.length, executions.length) - 16} fill="url(#df-floor)" stroke="#465355" strokeWidth="3" />
    <text x="24" y="30" fill="#c2b184" fontFamily="ui-monospace, monospace" fontSize="11">PRODUCTION · INSPECT WORK</text>
    {items.length === 0 ? <text x="24" y="72" fill="#8fa4ac" fontFamily="ui-monospace, monospace" fontSize="10">No production records in this scope.</text> : null}
    {items.map((item, index) => {
      const x = 16 + index % columns * cell, y = 52 + Math.floor(index / columns) * 144;
      const checks = item.checks.filter((check) => check.applicable);
      const failure = checks.some((check) => check.state === "completed" && ["failure", "timed_out", "action_required"].includes(check.conclusion));
      const running = checks.some((check) => ["running", "in_progress"].includes(check.state));
      const queued = checks.some((check) => ["queued", "waiting", "pending", "requested"].includes(check.state));
      const cancelled = checks.some((check) => check.state === "cancelled" || check.conclusion === "cancelled");
      const skipped = checks.some((check) => check.state === "skipped" || check.conclusion === "skipped");
      const correction = item.review.current && item.review.state === "block" || failure || item.construction?.status === "blocked";
      const active = pulse !== undefined && (!item.pullRequest || item.review.sourceFresh) && (running || item.construction?.status === "running");
      const title = item.pullRequest?.title ?? item.construction?.title ?? "Work";
      const checkState = !item.review.sourceFresh ? "stale" : failure ? "failed" : running ? "running" : queued ? "queued" : cancelled ? "cancelled" : skipped ? "skipped" : checks.length === 0 ? "unknown" : checks.every((check) => check.state === "completed" && check.conclusion === "success") ? "passed" : "inspect";
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
          <text x="0" y="110" fill={color} fontSize="9" fontFamily="ui-monospace, monospace">{item.construction ? "Assembly · " : ""}Review {item.review.current ? item.review.state : "unknown"}</text>
          <text x="0" y="123" fill="#8fa4ac" fontSize="9" fontFamily="ui-monospace, monospace">{`CI ${item.review.sourceFresh ? checkState : "stale"} · ${item.pullRequest?.merge_queue || (item.pullRequest?.state === "merged" ? item.completed ? "delivered" : "delivery pending" : item.construction?.status ?? item.pullRequest?.state ?? "unpublished")}`}</text>
        </g>
        <g {...activate(() => onSelect(item.projectId + ":" + item.visualId))} aria-label={`Inspect ${title}`} className="dfFactoryScene__target">
          <rect className="dfFactoryScene__focus" x="0" y="0" width={cell - 24} height="128" fill="transparent" stroke={selected === item.projectId + ":" + item.visualId ? "#80ddff" : "none"} />
        </g>
      </g>;
    })}
    {executions.map((check, index) => {
      const item = items.find((item) => item.checks.some((candidate) => candidate.id === check.id));
      const running = item?.review.sourceFresh && ["running", "in_progress"].includes(check.state) && pulse !== undefined;
      const passed = item?.review.sourceFresh && check.state === "completed" && check.conclusion === "success";
      return <g key={check.id} transform={`translate(${16 + index % columns * cell} ${60 + Math.max(1, Math.ceil(items.length / columns)) * 144 + Math.floor(index / columns) * 104})`} {...activate(() => { if (item) onSelect(item.projectId + ":" + item.visualId); })} aria-label={`Inspect shared check ${check.name}`} className="dfFactoryScene__target">
        <rect className="dfFactoryScene__focus" width={cell - 24} height="92" fill="transparent" />
        <rect x="8" y="4" width="100" height="46" fill="#303e40" stroke="#788379" strokeWidth="2" />
        <rect x="16" y="12" width="38" height="26" fill="#182c35" stroke="#536e70" />
        <path d={running && Math.floor(pulse / 400) % 2 ? "M20 26h6l4-8 5 14 5-6h9" : "M20 26h29"} stroke={passed ? "#9fe7b0" : check.conclusion === "failure" ? "#d49b7d" : "#8fa4ac"} fill="none" />
        <path d="M64 18h30m-30 8h30m-30 8h20M16 50v6m84-6v6" stroke="#9aa69c" strokeWidth="2" />
        <text y="70" fill="#c9d3d0" fontSize="10" fontFamily="ui-monospace, monospace">{label(check.name)}</text>
        <text y="84" fill="#8fa4ac" fontSize="9" fontFamily="ui-monospace, monospace">{check.pull_requests.length} PRs · {check.conclusion || check.state}</text>
      </g>;
    })}
  </g>;
}
