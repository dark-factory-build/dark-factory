import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  BROWSER_MANIFEST,
  MAX_CONTROL_BYTES,
  MAX_HUMAN_QUESTION_BYTES,
  MAX_SQLITE_INTEGER,
  MAX_STATE_PAGE_ITEMS,
  ProtocolError,
  StateAccumulator,
  decodeClientControl,
  decodeServerControl,
  encodeClientControl,
  encodeServerControl,
  encodeStateEntity,
  encodeStateEvent,
  encodeStateGet,
  encodeStateSnapshot,
  encodeStateSubscribe,
} from "../dist/src/index.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixture = (name) => readFileSync(join(root, "protocol/browser/v1/fixtures", name), "utf8").trim();
const manifest = JSON.parse(fixture("../manifest.json"));
const expectMalformed = (operation) => assert.throws(operation, (error) => error instanceof ProtocolError && ["malformed", "wrong_direction", "unsupported_version", "oversized"].includes(error.code));
const ids = {
  project: "01010101010101010101010101010101",
  agent: "02020202020202020202020202020202",
  task: "03030303030303030303030303030303",
  request: "04040404040404040404040404040404",
  run: "05050505050505050505050505050505",
};
const factoryItem = (revision = 1n) => ({ dispatch_enabled: true, capacity: 8, active_runs: 2, revision });
const projectItem = (revision = 1n) => ({ id: ids.project, name: "Factory", revision });
const agentItem = (revision = 1n) => ({ id: ids.agent, project_id: ids.project, name: "Worker", role: "worker", paused: false, revision });
const taskItem = (revision = 1n, title = "Ship") => ({ id: ids.task, project_id: ids.project, assigned_agent_id: ids.agent, title, status: "queued", priority: 1, revision });
const requestItem = (revision = 1n) => ({ id: ids.request, project_id: ids.project, agent_id: ids.agent, task_id: ids.task, run_id: ids.run, created_at: 10n, updated_at: 11n, revision, kind: "question", status: "open", reply_max_bytes: 8192, can_reply: true, can_open_terminal: true });
const rawID = (value) => BigInt(value).toString(16).padStart(32, "0");
const itemForKind = (kind, value) => {
  const id = rawID(value);
  if (kind === "project") return { ...projectItem(), id };
  if (kind === "agent") return { ...agentItem(), id };
  if (kind === "task") return { ...taskItem(), id };
  return { ...requestItem(), id };
};
const itemsForKind = (kind, start, count) => Array.from({ length: count }, (_, index) => itemForKind(kind, start + index));
const responseFrame = (id, body) => ({ v: 1, type: "STATE_SNAPSHOT", id, body });
const beginRequest = (reducer, cursor) => {
  const result = reducer.beginSnapshot(cursor);
  assert.equal(result.kind, "requested");
  return result.request;
};
const exchange = (reducer, cursor, body) => {
  const request = beginRequest(reducer, cursor);
  return reducer.applySnapshot(responseFrame(request.id, body));
};
const publishState = (reducer, head, { projects = [], agents = [], tasks = [], requests = [] } = {}) => {
  assert.equal(exchange(reducer, null, { head, kind: "factory", items: [factoryItem()], next_cursor: "projects" }).kind, "staged");
  assert.equal(exchange(reducer, "projects", { head, kind: "project", items: projects, next_cursor: "agents" }).kind, "staged");
  assert.equal(exchange(reducer, "agents", { head, kind: "agent", items: agents, next_cursor: "tasks" }).kind, "staged");
  assert.equal(exchange(reducer, "tasks", { head, kind: "task", items: tasks, next_cursor: "requests" }).kind, "staged");
  return exchange(reducer, "requests", { head, kind: "human_request", items: requests, next_cursor: null });
};
const traversal = ["project", "agent", "task", "human_request"];
const stageToKind = (reducer, target, head = 1n) => {
  let cursor = "project-page";
  assert.equal(exchange(reducer, null, { head, kind: "factory", items: [factoryItem()], next_cursor: cursor }).kind, "staged");
  for (const kind of traversal) {
    if (kind === target) return cursor;
    const next = `${kind}-next`;
    assert.equal(exchange(reducer, cursor, { head, kind, items: [], next_cursor: next }).kind, "staged");
    cursor = next;
  }
  throw new Error("unknown target kind");
};

test("every Go control fixture is consumed by the matching closed TypeScript direction", () => {
  for (const entry of manifest.control) {
    const wire = fixture(entry.fixture);
    if (entry.direction === "client") assert.equal(encodeClientControl(decodeClientControl(wire)), wire, entry.type);
    else if (entry.direction === "server") assert.equal(encodeServerControl(decodeServerControl(wire)), wire, entry.type);
    else {
      assert.equal(encodeClientControl(decodeClientControl(wire)), wire, `${entry.type}/client`);
      assert.equal(encodeServerControl(decodeServerControl(wire)), wire, `${entry.type}/server`);
    }
  }
  assert.equal(typeof decodeServerControl(fixture("state_snapshot.json")).body.head, "bigint");
  assert.equal(decodeServerControl(fixture("state_snapshot.json")).body.head, 9_007_199_254_740_993n);
});

test("wire page cardinality fixes every continuation boundary", () => {
  const samples = { project: projectItem(), agent: agentItem(), task: taskItem() };
  for (const [kind, sample] of Object.entries(samples)) {
    for (const count of [0, MAX_STATE_PAGE_ITEMS]) {
      const body = { head: 1n, kind, items: Array.from({ length: count }, () => ({ ...sample })), next_cursor: "YWZ0ZXI" };
      const decoded = decodeServerControl(encodeStateSnapshot("page", body));
      assert.equal(decoded.body.items.length, count);
    }
    expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind, items: [{ ...sample }], next_cursor: null }));
    expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind, items: Array.from({ length: MAX_STATE_PAGE_ITEMS + 1 }, () => ({ ...sample })), next_cursor: null }));
  }
  for (const count of [0, MAX_STATE_PAGE_ITEMS - 1]) {
    assert.equal(decodeServerControl(encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: Array.from({ length: count }, () => requestItem()), next_cursor: null })).body.items.length, count);
    expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: Array.from({ length: count }, () => requestItem()), next_cursor: "more" }));
  }
  assert.equal(decodeServerControl(encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: Array.from({ length: MAX_STATE_PAGE_ITEMS }, () => requestItem()), next_cursor: "more" })).body.items.length, MAX_STATE_PAGE_ITEMS);
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: Array.from({ length: MAX_STATE_PAGE_ITEMS }, () => requestItem()), next_cursor: null }));
  assert.equal(decodeServerControl(encodeStateSnapshot("page", { head: 1n, kind: "factory", items: [factoryItem()], next_cursor: "projects" })).body.items.length, 1);
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "factory", items: [factoryItem()], next_cursor: null }));
  for (const count of [0, 2, MAX_STATE_PAGE_ITEMS, MAX_STATE_PAGE_ITEMS + 1]) {
    expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "factory", items: Array.from({ length: count }, () => factoryItem()), next_cursor: "projects" }));
  }
});

test("decimal chronology uses canonical strings and bigint across unsafe boundaries", () => {
  for (const value of [0n, 1n, 9_007_199_254_740_991n, 9_007_199_254_740_992n, 9_007_199_254_740_993n, MAX_SQLITE_INTEGER]) {
    const wire = encodeStateSubscribe("watch", { after: value });
    assert.match(wire, new RegExp(`"after":"${value}"`));
    assert.equal(decodeClientControl(wire).body.after, value);
  }
  for (const value of ["", "+1", "-1", "-0", "00", "01", " 1", "1 ", "9223372036854775808", "1.0", "1e0"]) {
    expectMalformed(() => decodeClientControl(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{"after":${JSON.stringify(value)}}}`));
  }
  for (const value of ["0", "1", "9007199254740991", "9007199254740992", "9223372036854775807", "-1"]) {
    expectMalformed(() => decodeClientControl(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{"after":${value}}}`));
  }
  expectMalformed(() => encodeStateSubscribe("watch", { after: MAX_SQLITE_INTEGER + 1n }));
  expectMalformed(() => encodeStateSubscribe("watch", { after: 1 }));
});

test("cursor, entity identity, item bounds and event variants remain closed", () => {
  for (const cursor of [null, "A", "_".repeat(256)]) assert.equal(decodeClientControl(encodeStateGet("state", { cursor })).body.cursor, cursor);
  for (const cursor of ["", "a=", "a/b", "a+b", "é", "a".repeat(257)]) expectMalformed(() => encodeStateGet("state", { cursor }));
  expectMalformed(() => decodeClientControl('{"v":1,"type":"STATE_GET","id":"state","body":{}}'));
  expectMalformed(() => encodeStateEvent("watch", { event: "entity_changed", sequence: 1n, head: 1n, entity_kind: "factory", entity_id: ids.project, revision: 1n, deleted: false }));
  expectMalformed(() => encodeStateEvent("watch", { event: "entity_changed", sequence: 1n, head: 1n, entity_kind: "project", entity_id: "0".repeat(32), revision: 1n, deleted: false }));
  expectMalformed(() => decodeServerControl('{"v":1,"type":"STATE_EVENT","id":"watch","body":{"event":"hidden_advance","sequence":"1","head":"1","entity_kind":"task"}}'));
  expectMalformed(() => decodeServerControl('{"v":1,"type":"STATE_EVENT","id":"watch","body":{"event":"entity_changed","sequence":"1","head":"1"}}'));
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "task", items: [{ ...taskItem(), title: "" }], next_cursor: null }));
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "task", items: [{ ...taskItem(), priority: 1_000_001 }], next_cursor: null }));
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: [{ ...requestItem(), status: "resolved" }], next_cursor: null }));
});

test("entity tombstones are null and live items match kind and identity", () => {
  const live = { head: 2n, kind: "task", id: ids.task, revision: 2n, deleted: false, item: taskItem(2n) };
  assert.equal(decodeServerControl(encodeStateEntity("entity", live)).body.item.revision, 2n);
  assert.equal(decodeServerControl(encodeStateEntity("entity", { head: 2n, kind: "task", id: ids.task, revision: 3n, deleted: true, item: null })).body.item, null);
  expectMalformed(() => encodeStateEntity("entity", { ...live, deleted: true }));
  expectMalformed(() => encodeStateEntity("entity", { ...live, item: { ...live.item, id: ids.project } }));
  expectMalformed(() => encodeStateEntity("entity", { ...live, revision: 3n }));
  expectMalformed(() => decodeServerControl(fixture("state_entity.json").replace('"revision":"2","deleted"', '"deleted"')));
  expectMalformed(() => decodeServerControl(fixture("state_entity.json").replace('"kind":"human_request"', '"kind":"task"')));
});

test("every JSON boolean rejects null exactly", () => {
  const samples = [
    [encodeStateSnapshot("boolean", { head: 1n, kind: "factory", items: [factoryItem()], next_cursor: "projects" }), '"dispatch_enabled":true'],
    [encodeStateSnapshot("boolean", { head: 1n, kind: "agent", items: [agentItem()], next_cursor: "tasks" }), '"paused":false'],
    [encodeStateSnapshot("boolean", { head: 1n, kind: "human_request", items: [requestItem()], next_cursor: null }), '"can_reply":true'],
    [encodeStateSnapshot("boolean", { head: 1n, kind: "human_request", items: [requestItem()], next_cursor: null }), '"can_open_terminal":true'],
    [encodeStateEvent("boolean", { event: "entity_changed", sequence: 1n, head: 1n, entity_kind: "task", entity_id: ids.task, revision: 1n, deleted: false }), '"deleted":false'],
    [encodeStateEntity("boolean", { head: 1n, kind: "task", id: ids.task, revision: 1n, deleted: false, item: taskItem() }), '"deleted":false'],
    [encodeServerControl({ v: 1, type: "ERROR", id: "boolean", body: { code: "invalid_request", retryable: false } }), '"retryable":false'],
  ];
  for (const [wire, field] of samples) expectMalformed(() => decodeServerControl(wire.replace(field, field.replace(/(true|false)$/, "null"))));
});

test("public HumanRequest state cannot carry private fields and detail is separately bounded", () => {
  const wire = encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: [requestItem()], next_cursor: null });
  for (const field of ["question", "reply", "project_name", "agent_name", "task_title", "summary", "why_human_needed"]) assert.equal(wire.includes(`"${field}":`), false, field);
  expectMalformed(() => encodeStateSnapshot("page", { head: 1n, kind: "human_request", items: [{ ...requestItem(), question: "private" }], next_cursor: null }));
  const detail = { v: 1, type: "HUMAN_REQUEST_DETAIL", id: "detail", body: { request_id: ids.request, revision: 1n, question: "\0".repeat(MAX_HUMAN_QUESTION_BYTES) } };
  const detailWire = encodeServerControl(detail);
  assert.ok(Buffer.byteLength(detailWire) < MAX_CONTROL_BYTES);
  assert.ok(Buffer.byteLength(detailWire) > 49_000);
  expectMalformed(() => encodeServerControl({ ...detail, body: { ...detail.body, question: "" } }));
  expectMalformed(() => encodeServerControl({ ...detail, body: { ...detail.body, question: "x".repeat(MAX_HUMAN_QUESTION_BYTES + 1) } }));
});

test("state parsing rejects case-folded/duplicate/unknown/trailing/depth/member/array/UTF-8/size violations", () => {
  const valid = fixture("state_snapshot.json");
  for (const wire of [
    valid.replace('"head"', '"Head"'),
    valid.replace('"title"', '"TITLE"'),
    valid.replace('"title"', '"extra":false,"title"'),
    valid.replace('"head":"9007199254740993"', '"head":"9007199254740993","head":"2"'),
    `${valid}{}`,
  ]) expectMalformed(() => decodeServerControl(wire));
  const members = Array.from({ length: 33 }, (_, index) => `"x${index}":0`).join(",");
  expectMalformed(() => decodeClientControl(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{${members}}}`));
  const array = Array.from({ length: 33 }, () => "{}").join(",");
  expectMalformed(() => decodeServerControl(`{"v":1,"type":"STATE_SNAPSHOT","id":"page","body":{"head":"1","kind":"task","items":[${array}],"next_cursor":null}}`));
  const nested = "[".repeat(18) + "0" + "]".repeat(18);
  expectMalformed(() => decodeClientControl(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{"after":"1","x":${nested}}}`));
  expectMalformed(() => decodeServerControl(new Uint8Array([0xff])));
  expectMalformed(() => decodeServerControl(new Uint8Array(MAX_CONTROL_BYTES + 1).fill(0x20)));
});

test("state accumulator publishes only one complete fixed traversal and never lowers head", () => {
  const reducer = new StateAccumulator();
  const published = publishState(reducer, 7n, { projects: [projectItem()] });
  assert.equal(published.kind, "published");
  assert.equal(reducer.current.head, 7n);
  assert.equal(reducer.current.projects.size, 1);

  assert.deepEqual(exchange(reducer, null, { head: 6n, kind: "factory", items: [factoryItem()], next_cursor: "projects" }), { kind: "restart", reason: "head_changed" });
  assert.equal(reducer.current, undefined);

  publishState(reducer, 7n);
  assert.equal(exchange(reducer, null, { head: 8n, kind: "factory", items: [factoryItem()], next_cursor: "projects" }).kind, "staged");
  assert.equal(reducer.current.head, 7n, "partial replacement became visible");
  assert.deepEqual(exchange(reducer, "projects", { head: 9n, kind: "project", items: [], next_cursor: "agents" }), { kind: "restart", reason: "head_changed" });
  assert.equal(reducer.current, undefined);
});

test("page length uniquely determines same-kind continuation or adjacent advance", () => {
  for (const kind of traversal) {
    const index = traversal.indexOf(kind);
    let reducer = new StateAccumulator();
    let cursor = stageToKind(reducer, kind);
    assert.equal(exchange(reducer, cursor, { head: 1n, kind, items: itemsForKind(kind, 1, MAX_STATE_PAGE_ITEMS), next_cursor: "same-kind" }).kind, "staged", `${kind}/full`);
    const shortResult = exchange(reducer, "same-kind", { head: 1n, kind, items: itemsForKind(kind, 9, 1), next_cursor: kind === "human_request" ? null : "adjacent" });
    assert.equal(shortResult.kind, kind === "human_request" ? "published" : "staged", `${kind}/full-same`);

    reducer = new StateAccumulator();
    cursor = stageToKind(reducer, kind);
    assert.equal(exchange(reducer, cursor, { head: 1n, kind, items: itemsForKind(kind, 1, 1), next_cursor: kind === "human_request" ? null : "adjacent" }).kind, kind === "human_request" ? "published" : "staged", `${kind}/short`);
    if (kind !== "human_request") {
      const adjacent = traversal[index + 1];
      const result = exchange(reducer, "adjacent", { head: 1n, kind: adjacent, items: [], next_cursor: adjacent === "human_request" ? null : "next" });
      assert.equal(result.kind, adjacent === "human_request" ? "published" : "staged", `${kind}/short-adjacent`);
    }

    reducer = new StateAccumulator();
    cursor = stageToKind(reducer, kind);
    assert.equal(exchange(reducer, cursor, { head: 1n, kind, items: itemsForKind(kind, 1, MAX_STATE_PAGE_ITEMS), next_cursor: "same-kind" }).kind, "staged");
    if (kind !== "human_request") {
      const adjacent = traversal[index + 1];
      assert.deepEqual(exchange(reducer, "same-kind", { head: 1n, kind: adjacent, items: [], next_cursor: adjacent === "human_request" ? null : "next" }), { kind: "restart", reason: "gap" }, `${kind}/full-skipped-same`);
    }

    if (kind !== "human_request") {
      reducer = new StateAccumulator();
      cursor = stageToKind(reducer, kind);
      assert.equal(exchange(reducer, cursor, { head: 1n, kind, items: itemsForKind(kind, 1, 1), next_cursor: "adjacent" }).kind, "staged");
      assert.deepEqual(exchange(reducer, "adjacent", { head: 1n, kind, items: itemsForKind(kind, 2, 1), next_cursor: "forged" }), { kind: "restart", reason: "gap" }, `${kind}/short-repeated-kind`);
    }
  }
});

test("snapshot response correlation is owned, single-flight, consumed, and restart-safe", () => {
  const factory = { head: 1n, kind: "factory", items: [factoryItem()], next_cursor: "projects" };
  let reducer = new StateAccumulator();
  assert.deepEqual(reducer.applySnapshot(responseFrame("absent", factory)), { kind: "restart", reason: "gap" });
  assert.deepEqual(reducer.beginSnapshot("forged"), { kind: "restart", reason: "gap" });

  reducer = new StateAccumulator();
  beginRequest(reducer, null);
  assert.deepEqual(reducer.applySnapshot({ v: 1, type: "STATE_SNAPSHOT", body: factory }), { kind: "restart", reason: "gap" });

  reducer = new StateAccumulator();
  const crossWire = beginRequest(reducer, null);
  assert.deepEqual(reducer.applySnapshot({ v: 1, type: "STATE_ENTITY", id: crossWire.id, body: { head: 1n } }), { kind: "restart", reason: "gap" });

  reducer = new StateAccumulator();
  beginRequest(reducer, null);
  assert.deepEqual(reducer.beginSnapshot(null), { kind: "restart", reason: "gap" });

  reducer = new StateAccumulator();
  beginRequest(reducer, null);
  assert.deepEqual(reducer.applySnapshot(responseFrame("wrong", factory)), { kind: "restart", reason: "gap" });

  reducer = new StateAccumulator();
  const first = beginRequest(reducer, null);
  const firstResponse = responseFrame(first.id, factory);
  assert.equal(reducer.applySnapshot(firstResponse).kind, "staged");
  assert.deepEqual(reducer.applySnapshot(firstResponse), { kind: "restart", reason: "gap" }, "duplicate response accepted");

  reducer = new StateAccumulator();
  const cursorRequest = beginRequest(reducer, null);
  assert.equal(reducer.applySnapshot(responseFrame(cursorRequest.id, factory)).kind, "staged");
  assert.deepEqual(reducer.beginSnapshot("forged"), { kind: "restart", reason: "gap" }, "wrong next cursor accepted");

  reducer = new StateAccumulator();
  const sameCursor = beginRequest(reducer, null);
  assert.equal(reducer.applySnapshot(responseFrame(sameCursor.id, factory)).kind, "staged");
  const repeatedCursor = beginRequest(reducer, "projects");
  assert.deepEqual(reducer.applySnapshot(responseFrame(repeatedCursor.id, { head: 1n, kind: "project", items: [], next_cursor: "projects" })), { kind: "restart", reason: "gap" }, "same cursor continuation accepted");

  reducer = new StateAccumulator();
  const cycle = beginRequest(reducer, null);
  assert.equal(reducer.applySnapshot(responseFrame(cycle.id, factory)).kind, "staged");
  assert.equal(exchange(reducer, "projects", { head: 1n, kind: "project", items: [], next_cursor: "agents" }).kind, "staged");
  const cycleContinuation = beginRequest(reducer, "agents");
  assert.deepEqual(reducer.applySnapshot(responseFrame(cycleContinuation.id, { head: 1n, kind: "agent", items: [], next_cursor: "projects" })), { kind: "restart", reason: "gap" }, "cursor cycle accepted");

  reducer = new StateAccumulator();
  const replay = beginRequest(reducer, null);
  const replayResponse = responseFrame(replay.id, factory);
  assert.equal(reducer.applySnapshot(replayResponse).kind, "staged");
  const next = beginRequest(reducer, "projects");
  assert.notEqual(next.id, replay.id);
  assert.deepEqual(reducer.applySnapshot(replayResponse), { kind: "restart", reason: "gap" }, "cross-wire replay accepted");

  reducer = new StateAccumulator();
  const pending = beginRequest(reducer, null);
  assert.deepEqual(reducer.applyRestart("pruned"), { kind: "restart", reason: "pruned" });
  assert.deepEqual(reducer.applySnapshot(responseFrame(pending.id, factory)), { kind: "restart", reason: "gap" }, "restart retained pending request");
});

test("minted snapshot IDs never repeat across restart or publication", () => {
  let reducer = new StateAccumulator();
  const minted = new Set();
  for (let index = 0; index < 3; index++) {
    const request = beginRequest(reducer, null);
    assert.equal(minted.has(request.id), false);
    minted.add(request.id);
    reducer.applyRestart("gap");
  }

  reducer = new StateAccumulator();
  const oldRequest = beginRequest(reducer, null);
  const oldResponse = responseFrame(oldRequest.id, { head: 1n, kind: "factory", items: [factoryItem()], next_cursor: "projects" });
  assert.equal(reducer.applySnapshot(oldResponse).kind, "staged");
  assert.equal(exchange(reducer, "projects", { head: 1n, kind: "project", items: [], next_cursor: "agents" }).kind, "staged");
  assert.equal(exchange(reducer, "agents", { head: 1n, kind: "agent", items: [], next_cursor: "tasks" }).kind, "staged");
  assert.equal(exchange(reducer, "tasks", { head: 1n, kind: "task", items: [], next_cursor: "requests" }).kind, "staged");
  assert.equal(exchange(reducer, "requests", { head: 1n, kind: "human_request", items: [], next_cursor: null }).kind, "published");
  const laterRequest = beginRequest(reducer, null);
  assert.notEqual(laterRequest.id, oldRequest.id);
  assert.deepEqual(reducer.applySnapshot(oldResponse), { kind: "restart", reason: "gap" });
  assert.equal(reducer.current, undefined);
});

test("every dynamic kind requires strictly increasing raw IDs within and across pages", () => {
  for (const kind of traversal) {
    for (const values of [[2, 1], [1, 1]]) {
      const reducer = new StateAccumulator();
      const cursor = stageToKind(reducer, kind);
      const items = values.map((value) => itemForKind(kind, value));
      assert.deepEqual(exchange(reducer, cursor, { head: 1n, kind, items, next_cursor: kind === "human_request" ? null : "next" }), { kind: "restart", reason: "gap" }, `${kind}/within/${values}`);
    }
    for (const repeated of [8, 7]) {
      const reducer = new StateAccumulator();
      const cursor = stageToKind(reducer, kind);
      assert.equal(exchange(reducer, cursor, { head: 1n, kind, items: itemsForKind(kind, 1, MAX_STATE_PAGE_ITEMS), next_cursor: "same-kind" }).kind, "staged");
      assert.deepEqual(exchange(reducer, "same-kind", { head: 1n, kind, items: [itemForKind(kind, repeated)], next_cursor: kind === "human_request" ? null : "next" }), { kind: "restart", reason: "gap" }, `${kind}/across/${repeated}`);
    }
  }
  const reducer = new StateAccumulator();
  const cursor = stageToKind(reducer, "project");
  assert.equal(exchange(reducer, cursor, { head: 1n, kind: "project", items: [itemForKind("project", 255), itemForKind("project", 256)], next_cursor: "agents" }).kind, "staged");
});

test("entity refresh during staging restarts without mutating the previously published view", () => {
  let reducer = new StateAccumulator();
  publishState(reducer, 1n, { tasks: [taskItem(1n, "published")] });
  const oldView = reducer.current;
  assert.equal(exchange(reducer, null, { head: 2n, kind: "factory", items: [factoryItem(2n)], next_cursor: "projects" }).kind, "staged");
  assert.deepEqual(reducer.applyEntity({ head: 2n, kind: "task", id: ids.task, revision: 2n, deleted: false, item: taskItem(2n, "refresh") }), { kind: "restart", reason: "gap" });
  assert.equal(reducer.current, undefined);
  assert.equal(oldView.tasks.get(ids.task).title, "published");

  reducer = new StateAccumulator();
  publishState(reducer, 1n, { tasks: [taskItem(1n, "published")] });
  const pendingView = reducer.current;
  beginRequest(reducer, null);
  assert.deepEqual(reducer.applyEntity({ head: 2n, kind: "task", id: ids.task, revision: 2n, deleted: false, item: taskItem(2n, "refresh") }), { kind: "restart", reason: "gap" });
  assert.equal(pendingView.tasks.get(ids.task).title, "published");
});

test("state accumulator advances hidden events, detects gaps, and rejects late/lower revisions", () => {
  const reducer = new StateAccumulator();
  publishState(reducer, 1n, { tasks: [taskItem(5n, "old")] });
  let result = reducer.applyEvent({ event: "hidden_advance", sequence: 2n, head: 2n });
  assert.equal(result.kind, "applied");
  assert.equal(reducer.current.sequence, 2n);

  result = reducer.applyEvent({ event: "entity_changed", sequence: 3n, head: 3n, entity_kind: "task", entity_id: ids.task, revision: 7n, deleted: false });
  assert.equal(result.kind, "applied");
  assert.equal(reducer.applyEntity({ head: 3n, kind: "task", id: ids.task, revision: 6n, deleted: false, item: taskItem(6n, "lower") }).kind, "ignored");
  assert.equal(reducer.current.tasks.get(ids.task).title, "old");
  assert.equal(reducer.applyEntity({ head: 3n, kind: "task", id: ids.task, revision: 7n, deleted: false, item: taskItem(7n, "current") }).kind, "applied");
  assert.equal(reducer.current.tasks.get(ids.task).title, "current");

  assert.equal(reducer.applyEvent({ event: "entity_changed", sequence: 4n, head: 4n, entity_kind: "task", entity_id: ids.task, revision: 6n, deleted: false }).kind, "ignored");
  assert.equal(reducer.current.sequence, 4n);
  assert.equal(reducer.applyEntity({ head: 3n, kind: "task", id: ids.task, revision: 8n, deleted: true, item: null }).kind, "ignored");
  assert.equal(reducer.current.tasks.has(ids.task), true);

  reducer.applyEvent({ event: "entity_changed", sequence: 5n, head: 5n, entity_kind: "task", entity_id: ids.task, revision: 8n, deleted: true });
  assert.equal(reducer.current.tasks.has(ids.task), false);
  assert.equal(reducer.applyEntity({ head: 5n, kind: "task", id: ids.task, revision: 8n, deleted: false, item: taskItem(8n, "resurrect") }).kind, "ignored");
  assert.equal(reducer.current.tasks.has(ids.task), false);

  assert.deepEqual(reducer.applyEvent({ event: "hidden_advance", sequence: 7n, head: 7n }), { kind: "restart", reason: "gap" });
  assert.equal(reducer.current, undefined);
});

test("same-head tombstones cannot delete newer state or permit equal-revision resurrection", () => {
  const reducer = new StateAccumulator();
  publishState(reducer, 5n, { tasks: [taskItem(5n, "live")] });
  assert.equal(reducer.applyEntity({ head: 5n, kind: "task", id: ids.task, revision: 4n, deleted: true, item: null }).kind, "ignored");
  assert.equal(reducer.current.tasks.get(ids.task).title, "live");
  assert.equal(reducer.applyEntity({ head: 5n, kind: "task", id: ids.task, revision: 5n, deleted: true, item: null }).kind, "applied");
  assert.equal(reducer.current.tasks.has(ids.task), false);
  assert.equal(reducer.applyEntity({ head: 5n, kind: "task", id: ids.task, revision: 5n, deleted: false, item: taskItem(5n, "stale resurrection") }).kind, "ignored");
  assert.equal(reducer.current.tasks.has(ids.task), false);
  assert.equal(reducer.applyEntity({ head: 5n, kind: "task", id: ids.task, revision: 6n, deleted: false, item: taskItem(6n, "new generation") }).kind, "applied");
  assert.equal(reducer.current.tasks.get(ids.task).title, "new generation");
});

test("manifest bounds and registry are an exact readable mirror", () => {
  assert.deepEqual(BROWSER_MANIFEST.control, manifest.control);
  assert.deepEqual(BROWSER_MANIFEST.bounds, {
    maxControlBytes: manifest.bounds.max_control_bytes,
    maxJSONDepth: manifest.bounds.max_json_depth,
    maxArrayItems: manifest.bounds.max_array_items,
    maxObjectMembers: manifest.bounds.max_object_members,
    maxStatePageItems: manifest.bounds.max_state_page_items,
    maxFactoryPageItems: manifest.bounds.max_factory_page_items,
    maxCursorBytes: manifest.bounds.max_cursor_bytes,
    maxProjectNameBytes: manifest.bounds.max_project_name_bytes,
    maxAgentNameBytes: manifest.bounds.max_agent_name_bytes,
    maxTaskTitleBytes: manifest.bounds.max_task_title_bytes,
    maxHumanQuestionBytes: manifest.bounds.max_human_question_bytes,
    maxHumanReplyBytes: manifest.bounds.max_human_reply_bytes,
    maxFactoryCapacity: manifest.bounds.max_factory_capacity,
    maxTaskPriority: manifest.bounds.max_task_priority,
    maxSQLiteInteger: BigInt(manifest.bounds.max_sqlite_integer),
  });
});
