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
  StateKind,
  StateRestartFrame,
  StateSnapshotBody,
  StateSnapshotFrame,
  TaskItem,
} from "./control.js";

export type StateView = {
  head: bigint;
  sequence: bigint;
  factory: readonly FactoryItem[];
  projects: ReadonlyMap<string, ProjectItem>;
  agents: ReadonlyMap<string, AgentItem>;
  tasks: ReadonlyMap<string, TaskItem>;
  humanRequests: ReadonlyMap<string, HumanRequestItem>;
};

export type StateReducerFrame = StateSnapshotFrame | StateRestartFrame | StateEventFrame | StateEntityFrame;
export type StateReducerResult =
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
  revisions: Map<string, bigint>;
};

/**
 * One framework-neutral causal state accumulator. Same-head pages are staged
 * away from readers and become visible only when next_cursor is null.
 */
export class StateAccumulator {
  #published: MutableState | undefined;
  #staging: MutableState | undefined;

  get current(): StateView | undefined {
    return this.#published === undefined ? undefined : view(this.#published);
  }

  apply(frame: StateReducerFrame): StateReducerResult {
    switch (frame.type) {
      case "STATE_SNAPSHOT": return this.applySnapshot(frame.body);
      case "STATE_RESTART": return this.applyRestart(frame.body.reason);
      case "STATE_EVENT": return this.applyEvent(frame.body);
      case "STATE_ENTITY": return this.applyEntity(frame.body);
    }
  }

  applySnapshot(page: StateSnapshotBody): StateReducerResult {
    if (this.#staging !== undefined && this.#staging.head !== page.head) return this.#restart("head_changed");
    this.#staging ??= emptyState(page.head);
    stagePage(this.#staging, page);
    if (page.next_cursor !== null) return { kind: "staged" };
    this.#staging.sequence = page.head;
    this.#published = this.#staging;
    this.#staging = undefined;
    return { kind: "published", state: view(this.#published) };
  }

  applyRestart(reason: RestartReason): StateReducerResult { return this.#restart(reason); }

  applyEvent(event: StateEventBody): StateReducerResult {
    const state = this.#published;
    if (state === undefined || this.#staging !== undefined || event.sequence !== state.sequence + 1n) return this.#restart("gap");
    if (event.head < state.head) return this.#restart("head_changed");
    state.sequence = event.sequence;
    state.head = event.head;
    if (event.event === "hidden_advance") return { kind: "applied", state: view(state) };

    const key = entityKey(event.entity_kind, event.entity_id);
    const known = state.revisions.get(key);
    if (known !== undefined && event.revision < known) return { kind: "ignored", state: view(state) };
    if (known === undefined || event.revision > known) state.revisions.set(key, event.revision);
    if (event.deleted) {
      state.revisions.set(key, event.revision);
      removeEntity(state, event.entity_kind, event.entity_id);
    }
    return { kind: "applied", state: view(state) };
  }

  applyEntity(entity: StateEntityBody): StateReducerResult {
    const state = this.#published;
    if (state === undefined) return this.#restart("gap");
    if (entity.head < state.head) return { kind: "ignored", state: view(state) };
    const key = entityKey(entity.kind, entity.id);
    if (entity.deleted) {
      removeEntity(state, entity.kind, entity.id);
      return { kind: "applied", state: view(state) };
    }
    const revision = entity.item.revision;
    const known = state.revisions.get(key);
    if (known !== undefined && revision < known) return { kind: "ignored", state: view(state) };
    state.revisions.set(key, revision);
    setEntity(state, entity);
    return { kind: "applied", state: view(state) };
  }

  #restart(reason: RestartReason): StateReducerResult {
    this.#published = undefined;
    this.#staging = undefined;
    return { kind: "restart", reason };
  }
}

function emptyState(head: bigint): MutableState {
  return { head, sequence: head, factory: [], projects: new Map(), agents: new Map(), tasks: new Map(), humanRequests: new Map(), revisions: new Map() };
}

function stagePage(state: MutableState, page: StateSnapshotBody): void {
  switch (page.kind) {
    case "factory":
      for (const item of page.items) { state.factory.push(item); state.revisions.set(entityKey("factory", "factory"), item.revision); }
      break;
    case "project": for (const item of page.items) { state.projects.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), item.revision); } break;
    case "agent": for (const item of page.items) { state.agents.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), item.revision); } break;
    case "task": for (const item of page.items) { state.tasks.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), item.revision); } break;
    case "human_request": for (const item of page.items) { state.humanRequests.set(item.id, item); state.revisions.set(entityKey(page.kind, item.id), item.revision); } break;
  }
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
