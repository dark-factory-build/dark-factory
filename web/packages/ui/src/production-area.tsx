import { AgentSprite } from "./factory-scene/factory-scene.js";
import { useRef, type KeyboardEvent } from "react";
import { productionStickers, type ProductionContraption } from "./production-view.js";

export const productionColumns = (width: number) => Math.max(1, Math.min(4, Math.floor(width / 160)));
export const sharedChecks = (items: readonly ProductionContraption[]) => [...new Map(items.filter((item) => item.pullRequest?.state !== "merged").flatMap((item) => item.checks.filter((check) => check.applicable || check.scope === "merge_group").map((check) => [`${check.repository}:${check.id}`, check] as const))).values()];
export const productionHeight = (width: number, count: number, checks = 0) => 68 + Math.ceil(count / productionColumns(width)) * 132 + Math.ceil(checks / productionColumns(width)) * 104 + 48;
export type ProductionRoomRect = Readonly<{ x: number; y: number; width: number; height: number }>;
export const productionConnector = (upper: ProductionRoomRect, lower: ProductionRoomRect) => {
  const width = Math.min(32, upper.width, lower.width);
  return { x: upper.x, y: upper.y + upper.height, width, height: lower.y - (upper.y + upper.height) };
};
const label = (text: string, max = 23) => text.length > max ? `${text.slice(0, Math.max(0, max - 1))}…` : text;
const activate = (select: () => void) => ({ role: "button", tabIndex: 0, onClick: select, onKeyDown: (event: KeyboardEvent<SVGGElement>) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); } } });

/** Station props show concurrent evidence beside the same machine. No animation
 * advances work or moves a PR past an authority's decision. */
export function ProductionArea({ items, hiddenItems = 0, width, top, pulse, onSelect, selected, upperRoom }: {
  hiddenItems?: number; items: readonly ProductionContraption[]; width: number; top: number; pulse?: number;
  onSelect: (id: string) => void; selected?: string; upperRoom: ProductionRoomRect;
}) {
  const columns = productionColumns(width), cell = width / columns;
  const lowerRoom = { x: 8, y: top + 8, width: width - 16, height: productionHeight(width, items.length) - 16 };
  const connector = productionConnector(upperRoom, lowerRoom), localConnector = { ...connector, y: connector.y - top };
  const openingEnd = connector.x + connector.width;
  const reviewMotion = useRef(new Map<string, { reviewer: ProductionContraption["reviewers"][number]; at: number; running: boolean }>());
  return <g transform={`translate(0 ${top})`} role="group" aria-label="Production area">
    <rect {...localConnector} fill="url(#df-floor)" />
    <rect {...lowerRoom} y={lowerRoom.y - top} fill="url(#df-floor)" />
    <path d={`M${lowerRoom.x} 8H${connector.x} M${openingEnd} 8H${lowerRoom.x + lowerRoom.width} V${lowerRoom.y + lowerRoom.height - top} H${lowerRoom.x} V8`} fill="none" stroke="#465355" strokeWidth="1" />
    <text x="24" y="30" fill="#c2b184" fontFamily="ui-monospace, monospace" fontSize="11">PRODUCTION · INSPECT WORK</text>
    {items.length === 0 ? <text x="24" y="52" fill="#8fa4ac" fontFamily="ui-monospace, monospace" fontSize="10">No work in progress.</text> : null}
    {items.map((item, index) => {
      const x = 16 + index % columns * cell, y = 52 + Math.floor(index / columns) * 132;
      const title = item.pullRequest?.title ?? item.construction?.title ?? "Work";
      const stickers = productionStickers(item);
      const blocked = item.blockedReason !== "";
      const blockedLabel = label(`Blocked: ${item.blockedReason}`, Math.max(1, Math.floor((cell - 48) / 4.8)));
      const reviewRunning = !item.completed && item.review.sourceFresh && item.review.current && item.review.state === "running";
      const reviewer = item.reviewers.find((candidate) => candidate.state === "running");
      const prefix = `${item.projectId}:${item.visualId}:`;
      const priorEntry = [...reviewMotion.current.entries()].find(([key, value]) => key.startsWith(prefix) && (value.running || pulse === undefined || pulse - value.at <= 900));
      const prior = priorEntry?.[1];
      const reviewerKey = reviewer === undefined ? priorEntry?.[0] ?? "" : `${prefix}${reviewer.id}`;
      if (reviewRunning && reviewer !== undefined && pulse !== undefined && !reviewMotion.current.has(reviewerKey)) reviewMotion.current.set(reviewerKey, { reviewer, at: pulse, running: true });
      if (!reviewRunning && prior !== undefined && pulse !== undefined) {
        const entry = [...reviewMotion.current.entries()].find(([key, value]) => key.startsWith(prefix) && value.running);
        if (entry !== undefined) reviewMotion.current.set(entry[0], { ...entry[1], at: pulse, running: false });
      }
      const motion = reviewerKey ? reviewMotion.current.get(reviewerKey) : undefined;
      const reviewerTime = motion === undefined || pulse === undefined ? undefined : Math.max(0, pulse - motion.at);
      if (motion !== undefined && reviewerTime !== undefined && reviewerTime >= 900 && !motion.running) reviewMotion.current.delete(reviewerKey);
      const walking = reviewerTime !== undefined && !motion?.running;
      const reviewerX = reviewerTime === undefined ? Math.max(80, cell - 70) : motion?.running ? Math.max(34, cell - 78) + Math.min(1, reviewerTime / 900) * 24 : Math.max(34, cell - 54) + (1 - Math.min(1, reviewerTime / 900)) * 24;
      return <g key={item.projectId + ":" + item.visualId} transform={`translate(${x} ${y})`}>
        <g {...activate(() => onSelect(item.projectId + ":" + item.visualId))} aria-label={`${label(title)}${blocked ? `, Blocked: ${item.blockedReason}` : ""}. ${stickers.join(", ") || "No recorded results"}`} className="dfFactoryScene__target">
          <rect x="4" y="8" width={cell - 32} height="108" rx="4" fill="#263638" stroke={selected === item.projectId + ":" + item.visualId ? "#80ddff" : "#788379"} strokeWidth="2" />
          <text x="14" y="27" fill="#c9d3d0" fontSize="10" fontWeight="bold" fontFamily="ui-monospace, monospace">{label(title)}</text>
          {stickers.map((sticker, stickerIndex) => <g key={sticker} transform={`translate(14 ${(stickerIndex + 1) * 17 + 24})`}><rect width={Math.min(cell - 60, Math.max(66, sticker.length * 6.2))} height="13" rx="2" fill={sticker.includes("failed") || sticker === "Blocked" || sticker.includes("stale") ? "#714c48" : sticker.includes("running") || sticker.includes("pending") || sticker.includes("queued") ? "#665d43" : "#34534b"} /><text x="4" y="10" fill="#f0e4c4" fontSize="8" fontFamily="ui-monospace, monospace">{sticker}</text></g>)}
          {blocked ? <text x="14" y="105" fill="#f0b0a0" fontSize="8" fontFamily="ui-monospace, monospace">{blockedLabel}</text> : null}
          {motion && reviewerTime !== undefined && (motion.running || reviewerTime < 900) ? <g transform={`translate(${reviewerX} 18)`} aria-label={`${motion.reviewer.name}, reviewer, ${motion.running ? "working" : "leaving"}`}><AgentSprite size={28} agent={{ id: motion.reviewer.id, name: motion.reviewer.name, role: "worker", provider: motion.reviewer.provider === "claude_code" ? "claude_code" : motion.reviewer.provider === "codex" ? "codex" : "shell" }} activity="busy" motion={walking ? { action: "walking", direction: motion.running ? "east" : "west", frame: Math.floor((reviewerTime ?? 0) / 150) % 2 as 0 | 1, at: reviewerTime } : { action: "interacting", frame: Math.floor((reviewerTime ?? 0) / 380) % 2 as 0 | 1, at: reviewerTime }} /></g> : null}
          <rect className="dfFactoryScene__focus" x="0" y="0" width={cell - 24} height="128" fill="transparent" stroke="none" />
        </g>
      </g>;
    })}
    {hiddenItems > 0 ? <g transform={`translate(16 ${productionHeight(width, items.length) - 40})`} {...activate(() => onSelect(""))} aria-label={`View ${hiddenItems} more in-progress items`} className="dfFactoryScene__target"><rect className="dfFactoryScene__focus" width={width - 40} height="32" fill="#263638" /><text x="8" y="21" fill="#c2b184" fontSize="10" fontFamily="ui-monospace, monospace">{hiddenItems} more in progress · open list</text></g> : null}
  </g>;
}
