import type { ButtonHTMLAttributes, ReactNode } from "react";

const PATHS = {
  gear: "M12.5 8a4.5 4.5 0 1 1-9 0 4.5 4.5 0 0 1 9 0ZM8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4",
  pause: "M5.5 3v10M10.5 3v10", play: "M5 3l8 5-8 5Z",
  plus: "M8 3v10M3 8h10", refresh: "M13 8a5 5 0 1 1-1.5-3.5M13 2.5v2.5h-2.5",
  close: "M3.5 3.5l9 9M12.5 3.5l-9 9", "chevron-left": "M10 3 5 8l5 5",
  inbox: "M2 9l1.5-5.5h9L14 9v4H2ZM2 9h3.5l1 1.5h3L11 9h3",
  list: "M5.5 4h8M5.5 8h8M5.5 12h8M2.5 4h.01M2.5 8h.01M2.5 12h.01",
  "git-pull-request": "M4.5 5.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3ZM4.5 13.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3ZM11.5 13.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3ZM4.5 5.5v5M11.5 10.5V6a2 2 0 0 0-2-2H8M9.5 2.5 8 4l1.5 1.5",
  terminal: "M2 3.5h12v9H2ZM4.5 6.5 6.5 8l-2 1.5M8 10h3", pencil: "M3 11.5 10.5 4 12 5.5 4.5 13 2 14Zm7-8L11.5 2 14 4.5 12.5 6Z",
  history: "M2.5 8a5.5 5.5 0 1 0 1.8-4.1M2.5 2.5v3h3M8 5v3.5l2 1.5",
  book: "M3 2.5h8a2 2 0 0 1 2 2v9H5a2 2 0 0 1-2-2ZM3 11.5a2 2 0 0 1 2-2h8", trash: "M3 4.5h10M6 4.5V3h4v1.5M4.5 4.5l.5 8.5h6l.5-8.5M7 7v4M9 7v4",
};
export type IconName = keyof typeof PATHS;

export function Icon({ name, className = "" }: { name: IconName; className?: string }) {
  return <svg className={`dfIcon ${className}`.trim()} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={PATHS[name]} /></svg>;
}

type IconButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName } & ({ children: ReactNode } | { "aria-label": string });

export function IconButton({ icon, children, ...rest }: IconButtonProps) {
  return <button type="button" title={rest["aria-label"]} {...rest}><Icon name={icon} />{children}</button>;
}
