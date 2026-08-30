import type {
  AgentItem,
  FactoryItem,
  HumanRequestItem,
  ProjectItem,
  RestartReason,
  StateEntityBody,
  StateEntityFrame,
  StateEventBody,
  StateEventFrame,
  StateGetFrame,
  StateKind,
  StateRestartFrame,
  StateSnapshotBody,
  StateSnapshotFrame,
  TaskItem,
} from "./control.js";
import { MAX_STATE_PAGE_ITEMS } from "./manifest.js";

export type StateView = {
  head: bigint;
  sequence: bigint;
  factory: readonly FactoryItem[];
  projects: ReadonlyMap<string, ProjectItem>;
  agents: ReadonlyMap<string, AgentItem>;
  tasks: ReadonlyMap<string, TaskItem>;
  humanRequests: ReadonlyMap<string, HumanRequestItem>;
};

// Snapshot application also needs the cursor from its correlated STATE_GET,
// so the context-free frame reducer intentionally excludes it.
export type StateReducerFrame = StateRestartFrame | StateEventFrame | StateEntityFrame;
export type StateReducerResult =
  | { kind: "requested"; request: StateGetFrame }
  | { kind: "staged" }
  | { kind: "published"; state: StateView }
  | { kind: "applied"; state: StateView }
  | { kind: "ignored"; state: StateView }
  | { kind: "restart"; reason: RestartReason };

type MutableState = {
  head: bigint;
  sequence: bigint;
  factory: FactoryItem[];
  projects: Map<string, ProjectItem>;
  agents: Map<string, AgentItem>;
  tasks: Map<string, TaskItem>;
  humanRequests: Map<string, HumanRequestItem>;
  revisions: Map<string, { revision: bigint; deleted: boolean }>;
};

type StagingState = {
  state: MutableState;
  expectedKindIndex: number;
  nextCursor: string;
  consumedCursors: Set<string>;
  lastIDs: Map<StateKind, string>;
};

type PendingSnapshot = { requestID: string; cursor: string | null };

const STATE_TRAVERSAL: readonly StateKind[] = ["factory", "project", "agent", "task", "human_request"];

/**
 * One framework-neutral causal state accumulator. Same-head pages are staged
 * away from readers and become visible only after a terminal HumanRequest
 * page. One accumulator belongs to exactly one WebSocket/session and owns a
 * lifetime-monotonic STATE_GET correlation sequence, so an untrusted response
 * cannot splice or replay a page chain.
 */
export class StateAccumulator {
  #published: MutableState | undefined;
  #staging: StagingState | undefined;
  #pending: PendingSnapshot | undefined;
  #nextRequest = 1n;

  get current(): StateView | undefined {
    return this.#published === undefined ? undefined : view(this.#published);
  }

  /** The cursor required for the next page in the currently staged snapshot. */
  get nextCursor(): string | null {
    return this.#staging?.nextCursor ?? null;
  }

  apply(frame: StateReducerFrame): StateReducerResult {
    switch (frame.type) {
      case "STATE_RESTART": return this.applyRestart(frame.body.reason);
      case "STATE_EVENT": return this.applyEvent(frame.body);
      case "STATE_ENTITY": return this.applyEntity(frame.body);
    }
  }

  beginSnapshot(cursor: string | null): StateReducerResult {
    if (this.#pending !== undefined) return this.#restart("gap");
    const expectedCursor = this.#staging?.nextCursor ?? null;
    if (cursor !== expectedCursor) return this.#restart("gap");
    const requestID = `state-${this.#nextRequest}`;
    if (!validRequestID(requestID)) return this.#restart("gap");
    this.#nextRequest += 1n;
    const request = { v: 1, type: "STATE_GET", id: requestID, body: { cursor } } as const;
    this.#pending = { requestID, cursor };
    return { kind: "requested", request };
  }

  applySnapshot(response: StateSnapshotFrame): StateReducerResult {
    const pending = this.#pending;
    if (response?.v !== 1 || response.type !== "STATE_SNAPSHOT" || response.body === undefined || pending === undefined || !validRequestID(response.id) || response.id !== pending.requestID) return this.#restart("gap");
    this.#pending = undefined;
    const expectedCursor = this.#staging?.nextCursor ?? null;
    if (pending.cursor !== expectedCursor) return this.#restart("gap");
    const page = response.body;
    const pageKindIndex = STATE_TRAVERSAL.indexOf(page.kind);
    if (this.#staging === undefined) {
      if (pending.cursor !== null || pageKindIndex !== 0 || page.items.length !== 1 || page.next_cursor === null) return this.#restart("gap");
      if (this.#published !== undefined && page.head < this.#published.head) return this.#restart("head_changed");
      const state = emptyState(page.head);
      const lastIDs = new Map<StateKind, string>();
      if (!stagePage(state, page, lastIDs)) return this.#restart("gap");
      this.#staging = { state, expectedKindIndex: 1, nextCursor: page.next_cursor, consumedCursors: new Set(), lastIDs };
      return { kind: "staged" };
    }

    const staging = this.#staging;
    if (staging.state.head !== page.head) return this.#restart("head_changed");
    if (pending.cursor === null || staging.consumedCursors.has(pending.cursor)) return this.#restart("gap");
    if (pageKindIndex !== staging.expectedKindIndex || page.items.length > MAX_STATE_PAGE_ITEMS) return this.#restart("gap");
    const full = page.items.length === MAX_STATE_PAGE_ITEMS;
    if (page.kind === "human_request") {
      if (!full && page.next_cursor !== null) return this.#restart("gap");
    } else if (page.next_cursor === null) {
      return this.#restart("gap");
    }
    if (page.next_cursor !== null && (page.next_cursor === pending.cursor || staging.consumedCursors.has(page.next_cursor))) return this.#restart("gap");
    if (!stagePage(staging.state, page, staging.lastIDs)) return this.#restart("gap");
    staging.consumedCursors.add(pending.cursor);
    if (page.next_cursor !== null) {
      staging.nextCursor = page.next_cursor;
      staging.expectedKindIndex = full ? pageKindIndex : pageKindIndex + 1;
      return { kind: "staged" };
    }

    staging.state.sequence = page.head;
    this.#published = staging.state;
    this.#staging = undefined;
    return { kind: "published", state: view(this.#published) };
  }

  applyRestart(reason: RestartReason): StateReducerResult { return this.#restart(reason); }

  applyEvent(event: StateEventBody): StateReducerResult {
    const state = this.#published;
    if (state === undefined || this.#staging !== undefined || this.#pending !== undefined || event.sequence !== state.sequence + 1n) return this.#restart("gap");
    if (event.head < state.head) return this.#restart("head_changed");
    state.sequence = event.sequence;
    state.head = event.head;
    if (event.event === "hidden_advance") return { kind: "applied", state: view(state) };

    const key = entityKey(event.entity_kind, event.entity_id);
    const known = state.revisions.get(key);
    if (known !== undefined && (event.revision < known.revision || event.revision === known.revision && known.deleted && !event.deleted)) return { kind: "ignored", state: view(state) };
    if (event.deleted) {
      state.revisions.set(key, { revision: event.revision, deleted: true });
      removeEntity(state, event.entity_kind, event.entity_id);
    } else if (known === undefined || event.revision > known.revision) {
      state.revisions.set(key, { revision: event.revision, deleted: false });
    }
    return { kind: "applied", state: view(state) };
  }

  applyEntity(entity: StateEntityBody): StateReducerResult {
    const state = this.#published;
    if (state === undefined || this.#staging !== undefined || this.#pending !== undefined) return this.#restart("gap");
    if (entity.head < state.head) return { kind: "ignored", state: view(state) };
    const key = entityKey(entity.kind, entity.id);
    const known = state.revisions.get(key);
    if (known !== undefined && (entity.revision < known.revision || entity.revision === known.revision && known.deleted && !entity.deleted)) return { kind: "ignored", state: view(state) };
    if (entity.deleted) {
      state.revisions.set(key, { revision: entity.revision, deleted: true });
      removeEntity(state, entity.kind, entity.id);
      return { kind: "applied", state: view(state) };
    }
    state.revisions.set(key, { revision: entity.revision, deleted: false });
    setEntity(state, entity);
    return { kind: "applied", state: view(state) };
  }

  #restart(reason: RestartReason): StateReducerResult {
    this.#published = undefined;
    this.#staging = undefined;
    this.#pending = undefined;
    return { kind: "restart", reason };
  }
}

function emptyState(head: bigint): MutableState {
  return { head, sequence: head, factory: [], projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map(), revisions: new Map() };
}

function stagePage(state: MutableState, page: StateSnapshotBody, lastIDs: Map<StateKind, string>): boolean {
  switch (page.kind) {
    case "factory":
      if (state.factory.length !== 0) return false;
      for (const item of page.items) { state.factory.push(item); state.revisions.set(entityKey("factory", "factory"), { revision: item.revision, deleted: false }); }
      break;
    case "project": if (!IDsIncrease(page.kind, page.items, lastIDs)) return false; for (const item of page.items) { state.projects.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), { revision: item.revision, deleted: false }); } break;
    case "agent": if (!IDsIncrease(page.kind, page.items, lastIDs)) return false; for (const item of page.items) { state.agents.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), { revision: item.revision, deleted: false }); } break;
    case "task": if (!IDsIncrease(page.kind, page.items, lastIDs)) return false; for (const item of page.items) { state.tasks.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), { revision: item.revision, deleted: false }); } break;
    case "human_request": if (!IDsIncrease(page.kind, page.items, lastIDs)) return false; for (const item of page.items) { state.humanRequests.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), { revision: item.revision, deleted: false }); } break;
  }
  return true;
}

function IDsIncrease(kind: StateKind, items: readonly { id: string }[], lastIDs: Map<StateKind, string>): boolean {
  let previous = lastIDs.get(kind);
  for (const item of items) {
    if (previous !== undefined && compareEntityIDs(previous, item.id) >= 0) return false;
    previous = item.id;
  }
  if (previous !== undefined) lastIDs.set(kind, previous);
  return true;
}

// IDs are already canonical lowercase encodings of 16 raw bytes. Compare the
// bytes directly rather than relying on locale or JavaScript string ordering.
function compareEntityIDs(left: string, right: string): number {
  for (let index = 0; index < 16; index++) {
    const offset = index * 2;
    const difference = Number.parseInt(left.slice(offset, offset + 2), 16) - Number.parseInt(right.slice(offset, offset + 2), 16);
    if (difference !== 0) return difference;
  }
  return 0;
}

function validRequestID(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 64 && [...value].every((character) => character.charCodeAt(0) >= 0x21 && character.charCodeAt(0) <= 0x7e);
}

function setEntity(state: MutableState, entity: Exclude<StateEntityBody, { deleted: true }>): void {
  switch (entity.kind) {
    case "factory": state.factory = [entity.item]; break;
    case "project": state.projects.set(entity.id, entity.item); break;
    case "agent": state.agents.set(entity.id, entity.item); break;
    case "task": state.tasks.set(entity.id, entity.item); break;
    case "human_request": state.humanRequests.set(entity.id, entity.item); break;
  }
}

function removeEntity(state: MutableState, kind: StateKind, id: string): void {
  switch (kind) {
    case "factory": state.factory = []; break;
    case "project": state.projects.delete(id); break;
    case "agent": state.agents.delete(id); break;
    case "task": state.tasks.delete(id); break;
    case "human_request": state.humanRequests.delete(id); break;
  }
}

function entityKey(kind: StateKind, id: string): string { return `${kind}:${id}`; }

function view(state: MutableState): StateView {
  return {
    head: state.head,
    sequence: state.sequence,
    factory: state.factory.map((item) => ({ ...item })),
    projects: cloneMap(state.projects),
    agents: cloneMap(state.agents),
    tasks: cloneMap(state.tasks),
    humanRequests: cloneMap(state.humanRequests),
  };
}

function cloneMap<T extends object>(source: Map<string, T>): ReadonlyMap<string, T> {
  return new Map([...source].map(([id, item]) => [id, { ...item }]));
}
