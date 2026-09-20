import { hash } from "./factory-scene/appearance.js";

export type ContraptionProps = Readonly<{
  /** Durable record identity, never a commit SHA or presentation-derived value. */
  identity: string;
  construction: boolean;
  correction: boolean;
  active: boolean;
  reducedMotion: boolean;
  /** Shared scene clock in milliseconds; omitted clocks leave the machine at rest. */
  pulse?: number;
}>;

function tickFor(active: boolean, reducedMotion: boolean, pulse: number | undefined) {
  if (!active || reducedMotion || pulse === undefined || !Number.isFinite(pulse)) return 0;
  return Math.floor(Math.max(0, pulse) / 360) % 4;
}

function Bolt({ x, y }: { x: number; y: number }) {
  return <g transform={`translate(${x} ${y})`}><rect x="-2" y="-2" width="4" height="4" fill="#9aa69c" /><path d="M-1 0h2" stroke="#273134" /></g>;
}

function Gear({ x, y, turn }: { x: number; y: number; turn: number }) {
  return <g transform={`translate(${x} ${y}) rotate(${turn * 90})`}><path d="M-7-3h4v-4h6v4h4v6H3v4h-6V3h-4Z" fill="#65726b" stroke="#1d292c" /><circle r="3" fill="#263e40" stroke="#a08f68" /></g>;
}

function Spanner() {
  return <g data-contraption-tool="spanner" transform="translate(49 12) rotate(35)"><path d="M-3-10a5 5 0 0 0 5 7l9 9 3-3-9-9a5 5 0 0 0-7-5l3 3-3 3Z" fill="#c2b184" stroke="#273134" strokeWidth="2" /><circle cx="12" cy="4" r="2" fill="#273134" /></g>;
}

/** A compact, identity-stable factory machine. Supplied operation facts only add overlays. */
export function Contraption({ identity, construction, correction, active, reducedMotion, pulse }: ContraptionProps) {
  const seed = hash(identity), design = seed % 3, fitting = Math.floor(seed / 3) % 4;
  const tick = tickFor(active, reducedMotion, pulse);
  const lamp = active ? tick % 2 === 0 ? "#e5c58b" : "#80ddff" : "#53605b";
  return <svg width="64" height="64" viewBox="0 0 64 64" role="img" aria-label="Factory contraption" data-contraption-variant={design} data-contraption-fitting={fitting} data-contraption-active={active || undefined} style={{ display: "block", width: 64, height: 64, imageRendering: "pixelated" }}>
    <g data-contraption-frame={design} shapeRendering="crispEdges">
      <rect x="5" y="52" width="54" height="6" fill="#455653" stroke="#1d292c" strokeWidth="2" />
      <path d="M10 58v4M54 58v4" stroke="#273134" strokeWidth="4" />
      <rect x="11" y="24" width="42" height="28" fill={construction ? "none" : "#303e40"} stroke="#788379" strokeWidth="2" />
      <path d="M15 48h34M18 28v20M46 28v20" stroke="#20292d" strokeWidth="2" />
      {design === 0 ? <><rect x="25" y="15" width="14" height="27" fill="#536e70" stroke="#9aa69c" strokeWidth="2" /><path d="M22 15h20v5H22z" fill="#665f4e" stroke="#a08f68" /><Gear x={32} y={42} turn={tick} /></>
        : design === 1 ? <><path d="M17 23V14h15v9M32 20h15v28H32" fill="#455c5e" stroke="#9aa69c" strokeWidth="2" /><rect x="20" y="17" width="9" height="8" fill="#263e40" /><Gear x={40} y={39} turn={tick} /></>
          : <><path d="M15 46V17h8v18h18V17h8v29" fill="none" stroke="#9aa69c" strokeWidth="4" /><rect x="24" y="28" width="16" height="14" fill="#536e70" stroke="#c2b184" strokeWidth="2" /><Gear x={32} y={45} turn={tick} /></>}
      <Bolt x={14} y={28} /><Bolt x={50} y={28} /><Bolt x={14} y={48} /><Bolt x={50} y={48} />
      <rect x="45" y="31" width="5" height="5" fill={lamp} stroke="#1d292c" />
      {fitting === 0 ? <path d="M10 24V9h8v15M8 9h12M11 5h6" fill="none" stroke="#65726b" strokeWidth="3" />
        : fitting === 1 ? <><circle cx="49" cy="15" r="7" fill="#536e70" stroke="#9aa69c" strokeWidth="2" /><path d="M49 8v14M42 15h14" stroke="#263e40" strokeWidth="2" /></>
          : fitting === 2 ? <path d="M50 24V7m-5 5 5-5 5 5M44 24h12" fill="none" stroke="#9aa69c" strokeWidth="2" />
            : <><path d="M5 35h8v10H5z" fill="#665f4e" stroke="#a08f68" strokeWidth="2" /><path d="M7 40h4" stroke="#263e40" strokeWidth="2" /></>}
    </g>
    {construction ? <g data-contraption-build="partly-assembled" fill="none" stroke="#e5c58b" strokeWidth="2"><path d="M7 19h11v8H7z" strokeDasharray="3 2" /><path d="M12 27v13M8 40h8" /><circle cx="12" cy="17" r="2" fill="#e5c58b" /></g> : null}
    {correction ? <Spanner /> : null}
  </svg>;
}
