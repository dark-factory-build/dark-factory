import type { StateView, TopologyView } from "@dark-factory/client";

export declare const fixtureState: StateView;
export declare const fixtureFloorState: StateView;
export declare const fixtureTopology: TopologyView;
export declare const fixtureTopologies: ReadonlyMap<string, TopologyView>;
export declare const fixtureRunPaths: ReadonlyMap<string, Readonly<{ taskId: string; taskRevision: bigint; runId: string; projectId: string; paths: readonly string[] }>>;
export declare const fixtureCrowdedState: StateView;
export declare const fixtureCrowdedRunPaths: typeof fixtureRunPaths;
