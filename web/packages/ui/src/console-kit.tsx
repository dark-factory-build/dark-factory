import type { ReactNode } from "react";

/** The shared console pieces: count pill (hidden at zero), state chip, heading row, one date format ("6 Oct, 19:38 UTC"). */
export const Badge = ({ n }: { n?: number }) => n ? <span className="dfBadge">{n}</span> : null;

export const Status = ({ stage, children }: { stage: string; children?: ReactNode }) => <span className="dfStatus" data-stage={stage}>{children ?? stage.replaceAll(/[-_]/g, " ")}</span>;

export function SectionHeader({ title, as: Heading = "h3", count, actions, hidden = false }: { title: ReactNode; as?: "h2" | "h3" | "h4"; count?: number; actions?: ReactNode; hidden?: boolean }) {
  return <div className="dfSectionHeader"><Heading className={hidden ? "dfFactoryConsole__visuallyHidden" : undefined}>{title}</Heading><Badge n={count} />{actions ? <span className="dfSectionHeader__actions">{actions}</span> : null}</div>;
}

export function formatTime(value: bigint | number | undefined): string {
  if (value === undefined || value <= 0 || value > BigInt(Number.MAX_SAFE_INTEGER)) return "";
  const date = new Date(Number(value));
  return Number.isNaN(date.valueOf()) ? "" : `${date.toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: "UTC" })} UTC`;
}
