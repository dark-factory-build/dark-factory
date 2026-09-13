import {
  PADDING,
  ROOM_LEFT,
  layoutScene,
  placeWorkers,
  type SceneTopology,
  type SceneWorker,
} from "./scene.js";
import { workerFrames } from "./appearance.js";
import { spriteAtlas, spriteSheet, spriteSheetSize } from "./sprites/sprites.generated.js";

export type {
  SceneHeading,
  SceneLayout,
  SceneNode,
  SceneRoomLayout,
  SceneTopology,
  SceneWorker,
  SceneWorkerPlacement,
} from "./scene.js";

export type FactorySceneProps = Readonly<{
  topology: SceneTopology;
  workers: readonly SceneWorker[];
  /** Current changed locations omitted by the bounded room map. */
  omittedLocations?: number;
  /** The selected agent is highlighted without changing its deterministic placement. */
  selectedWorkerId?: string;
  /** Pointer convenience only; the AGENTS list is the keyboard path. */
  onSelectWorker?: (workerId: string) => void;
}>;

export type AgentSpriteProps = Readonly<{
  agent: Pick<SceneWorker, "id" | "name" | "role" | "provider" | "appearance">;
  activity: SceneWorker["activity"];
}>;

const FRAME = spriteAtlas.frame;

function shortLabel(label: string) {
  const glyphs = [...label];
  return glyphs.length > 18 ? `${glyphs.slice(0, 17).join("")}…` : label;
}

/** One 16px frame of the sheet, sized and placed in scene coordinates. */
function Frame({ name, x, y, className }: { name: string; x: number; y: number; className?: string }) {
  return <use href={`#df-frame-${name}`} x={x} y={y} width={FRAME} height={FRAME} className={className} />;
}

/** A standalone crop of the shared sheet for lists and detail panels. */
export function AgentSprite({ agent, activity }: AgentSpriteProps) {
  const frames = workerFrames({ ...agent, activity });
  return <svg viewBox={`0 0 ${FRAME} ${FRAME}`} role="img" aria-label={`${agent.name}, ${agent.role}, ${activity}`} className="dfAgentSprite">
    {frames.map((frame) => { const cell = spriteAtlas.frames[frame as keyof typeof spriteAtlas.frames]; return <image key={frame} href={spriteSheet} x={-cell.x} y={-cell.y} width={spriteSheetSize.width} height={spriteSheetSize.height} style={{ imageRendering: "pixelated" }} />; })}
  </svg>;
}

/** A disposable SVG projection of topology and current factory state. */
export function FactoryScene({ topology, workers, omittedLocations = 0, selectedWorkerId, onSelectWorker }: FactorySceneProps) {
  const layout = layoutScene(topology);
  const placements = placeWorkers(layout, workers);
  const nodes = new Map(topology.nodes.map((node) => [node.id, node]));
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  const resting = placements.filter((placement) => placement.area === "resting");
  const staging = placements.filter((placement) => placement.area === "staging");
  const overflow = placements.filter((placement) => placement.area === "overflow");
  const outside = placements.filter((placement) => placement.area === "outside");
  const sceneHeight = Math.max(layout.height, ...placements.map((placement) => placement.y + 24)) + PADDING;
  // A wide column must not blow 16px frames up to poster size: the scene stops
  // at three CSS pixels per sheet pixel and centres in whatever is left.
  const maxWidth = layout.width * 3;

  return (
    <svg
      viewBox={`0 0 ${layout.width} ${sceneHeight}`}
      role="group"
      aria-label="Dark Factory codebase floor"
      data-topology-digest={topology.digest}
      style={{ display: "block", width: "100%", minWidth: layout.width, maxWidth, height: "auto", margin: "0 auto", background: "#08131d" }}
    >
      <title>Dark Factory codebase floor</title>
      <desc>{`${layout.rooms.length} topology spaces, ${workers.length} workers${omittedLocations === 0 ? "" : `, ${omittedLocations} current locations omitted by the room cap`}`}</desc>
      <defs>
        {/* The sheet enters the document once; every frame is a window on it. */}
        <image id="df-sheet" href={spriteSheet} width={spriteSheetSize.width} height={spriteSheetSize.height} style={{ imageRendering: "pixelated" }} />
        {Object.entries(spriteAtlas.frames).map(([name, cell]) => (
          <symbol key={name} id={`df-frame-${name}`} viewBox={`${cell.x} ${cell.y} ${FRAME} ${FRAME}`}>
            <use href="#df-sheet" />
          </symbol>
        ))}
        <pattern id="df-floor" patternUnits="userSpaceOnUse" width={FRAME * 2} height={FRAME * 2}>
          <Frame name="tile.floor.0" x={0} y={0} />
          <Frame name="tile.floor.1" x={FRAME} y={0} />
          <Frame name="tile.floor.1" x={0} y={FRAME} />
          <Frame name="tile.floor.0" x={FRAME} y={FRAME} />
        </pattern>
        <pattern id="df-wall" patternUnits="userSpaceOnUse" width={FRAME} height={FRAME}>
          <Frame name="tile.wall" x={0} y={0} />
        </pattern>
      </defs>
      <rect width={layout.width} height={sceneHeight} fill="#08131d" />
      <text x={PADDING} y="20" fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="10" fontWeight="700">
        FACTORY FLOOR · {layout.rooms.length} SPACES
      </text>

      {layout.corridors.map((corridor, index) => <rect key={index} data-corridor="" {...corridor} fill="url(#df-floor)" />)}
      <rect x={PADDING} y={layout.restingTop - 32} width={ROOM_LEFT - PADDING} height={sceneHeight - layout.restingTop + 32} fill="url(#df-floor)" />
      {layout.headings.map((heading) => (
        <text key={heading.y} data-floor-heading={heading.label} x={heading.x} y={heading.y + 11} fill="#b9cad5" fontFamily="ui-monospace, monospace" fontSize="9" fontWeight="700">
          {shortLabel(heading.label)}
        </text>
      ))}

      {layout.rooms.map((room) => {
        const node = nodes.get(room.id);
        if (node === undefined) return null;
        return (
          <g key={room.id} data-room-id={room.id}>
            <title>{node.path}</title>
            <rect x={room.x} y={room.y} width={room.width} height={room.height} fill="url(#df-floor)" />
            <rect x={room.x} y={room.y} width={room.width} height={FRAME} fill="url(#df-wall)" />
            <path data-room-walls="" d={`M${room.door.x - 16},${room.door.y} H${room.x} V${room.y} H${room.x + room.width} V${room.door.y} H${room.door.x + 16}`} fill="none" stroke="#638095" strokeWidth="4" />
            <Frame name="tile.workstation" x={room.workstation.x} y={room.workstation.y} />
            <text x={room.x + 8} y={room.y + 18} fill="#f2f6f8" fontFamily="ui-monospace, monospace" fontSize="11" fontWeight="700">
              {shortLabel(node.label)}
            </text>
            <text x={room.x + 8} y={room.y + 34} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">
              {node.kind.toUpperCase()}{node.sizeBucket === undefined ? "" : ` · ${node.sizeBucket.toUpperCase()}`}
            </text>
          </g>
        );
      })}

      <Area label={`RESTING AREA · ${resting.length}`} width={layout.width - ROOM_LEFT - PADDING} top={layout.restingTop - 28} bottom={Math.max(layout.restingTop + 24, ...resting.map((placement) => placement.y + 24))} />
      {staging.length === 0 ? null : <Area label={`UNKNOWN LOCATION · ${staging.length}`} width={layout.width - ROOM_LEFT - PADDING} top={staging[0]!.y - 28} bottom={Math.max(...staging.map((placement) => placement.y + 24))} />}
      {outside.length === 0 ? null : <Area label={`OUTSIDE DISPLAYED ROOMS · ${outside.length}`} width={layout.width - ROOM_LEFT - PADDING} top={outside[0]!.y - 28} bottom={Math.max(...outside.map((placement) => placement.y + 24))} />}
      {overflow.length === 0 ? null : <Area label={`WORKER AREA AT CAPACITY · ${overflow.length}`} width={layout.width - ROOM_LEFT - PADDING} top={overflow[0]!.y - 28} bottom={Math.max(...overflow.map((placement) => placement.y + 24))} />}
      {layout.rooms.length === 0 ? <text x={ROOM_LEFT} y="38" fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="10">EMPTY FLOOR</text> : null}

      {placements.map((placement) => {
        const worker = workerById.get(placement.id);
        if (worker === undefined) return null;
        const room = placement.roomId === undefined ? undefined : nodes.get(placement.roomId);
        const location = worker.location === "working"
          ? `representative location near observed changes${worker.locationLabel === undefined && room === undefined ? "" : ` in ${worker.locationLabel ?? room?.label}`}; ${placement.area === "room" ? "at workstation" : placement.area === "outside" ? "outside displayed rooms" : "worker area at capacity"}`
          : worker.location === "unobserved" ? "working; location not yet observed"
          : worker.location === "last-observed" && worker.locationLabel !== undefined ? `last observed near changes in ${worker.locationLabel}; resting area`
          : worker.paused ? "paused in resting area" : "ready in resting area";
        const frames = workerFrames(worker);
        return (
          <g
            key={worker.id}
            data-worker-id={worker.id}
            data-worker-location={worker.location ?? "resting"}
            transform={`translate(${placement.x} ${placement.y})`}
            className={worker.id === selectedWorkerId ? "dfFactoryScene__worker dfFactoryScene__worker--selected" : "dfFactoryScene__worker"}
          >
            <g role="img" aria-label={`${worker.name}, ${worker.role}, ${worker.activity}, ${location}`} {...(onSelectWorker === undefined ? {} : { onClick: () => onSelectWorker(worker.id), style: { cursor: "pointer" } })}>
              <title>{`${worker.name} · ${location}`}</title>
              <rect x={-12} y={-12} width="24" height="24" fill="transparent" />
              {worker.id === selectedWorkerId ? <circle className="dfFactoryScene__selection" cx="0" cy="0" r="12" /> : null}
              {frames.map((frame) => <Frame key={frame} name={frame} x={-8} y={-8} />)}
            </g>
          </g>
        );
      })}

    </svg>
  );
}

function Area({ label, width, top, bottom }: { label: string; width: number; top: number; bottom: number }) {
  return (
    <g role="group" aria-label={label}>
      <rect x={ROOM_LEFT} y={top} width={width} height={bottom - top} fill="url(#df-floor)" />
      <text x={ROOM_LEFT + 6} y={top + 15} fill="#9db1be" fontFamily="ui-monospace, monospace" fontSize="8">{label}</text>
    </g>
  );
}
