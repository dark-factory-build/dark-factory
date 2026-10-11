import type { StateView, OperationalGraphView } from "@dark-factory/client";

export declare const fixtureState: StateView;
export declare const fixtureFloorState: StateView;
export declare const operationsSpecialistID: string;
export declare const securitySpecialistID: string;
export declare const fixtureGraph: OperationalGraphView;
export declare const fixtureGraphs: ReadonlyMap<string, OperationalGraphView>;
export declare const fixtureRunPaths: ReadonlyMap<string, Readonly<{ taskId: string; taskRevision: bigint; runId: string; projectId: string; paths: readonly string[] }>>;
export declare const fixtureCrowdedState: StateView;
export declare const fixtureCrowdedRunPaths: typeof fixtureRunPaths;
