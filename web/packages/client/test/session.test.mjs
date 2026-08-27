import assert from "node:assert/strict";
import test from "node:test";
import {
  BrowserSession,
  BrowserClient,
  CAPABILITIES,
  consumePairingChallenge,
  ProtocolError,
  SessionError,
  decodeClientControl,
  encodeHello,
  encodePairResult,
  encodeServerError,
  encodeAuthResult,
  encodeHumanRequestCancelRunResult,
  encodeHumanRequestDetail,
  encodeHumanRequestReplyResult,
  encodeStateEntity,
  encodeStateEvent,
  encodeStateRestart,
  encodeStateSnapshot,
  encodeTerminalAttached,
  encodeTerminalExit,
  encodeTerminalReset,
  encodeTerminalTarget,
} from "../dist/src/index.js";

const challenge = "11".repeat(32);
const daemonID = "22".repeat(16);
const bootID = "33".repeat(16);
const nonce = "44".repeat(32);
const clientID = "55".repeat(16);
const runID = "66".repeat(16);
const factory = (revision = 1n) => ({ dispatch_enabled: true, capacity: 8, active_runs: 0, revision });

class MemoryKeys {
  value = null;
  async load() { return this.value; }
  async save(value) { this.value = value; }
}

class Socket {
  readyState = 1;
  onopen = null;
  onmessage = null;
  onerror = null;
  onclose = null;
  sent = [];
  constructor(server) {
    this.server = server;
    queueMicrotask(() => this.onmessage?.({ data: encodeHello({ daemon_id: daemonID, boot_id: bootID, connection_nonce: nonce }) }));
  }
  send(data) { this.sent.push(data); this.server(this, decodeClientControl(data)); }
  reply(data) { queueMicrotask(() => this.onmessage?.({ data })); }
  close() { this.readyState = 3; this.onclose?.({ code: 1000 }); }
}

function serverFor(socket) {
  const frame = decodeClientControl(socket.sent.at(-1));
  const capabilities = CAPABILITIES.observe | CAPABILITIES.private_human_request_detail | CAPABILITIES.human_actions | CAPABILITIES.terminal_input;
  if (frame.type === "PAIR_PROVE") socket.reply(encodePairResult(frame.id, { client_id: clientID, capabilities }));
  if (frame.type === "AUTH_PROVE") socket.reply(encodeAuthResult(frame.id, { client_id: clientID, capabilities }));
  if (frame.type === "STATE_GET") replyStatePage(socket, frame, 1n);
}

const statePages = {
  null: ["factory", [factory()], "p"], p: ["project", [], "a"], a: ["agent", [], "t"], t: ["task", [], "r"], r: ["human_request", [], null],
};

function replyStatePage(socket, frame, head) {
  const [kind, originalItems, next_cursor] = statePages[String(frame.body.cursor)];
  const items = kind === "factory" ? [factory(head)] : originalItems;
  socket.reply(encodeStateSnapshot(frame.id, { head, kind, items, next_cursor }));
}

function tick() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function lastFrame(socket, type) {
  return decodeClientControl(socket.sent.findLast((wire) => decodeClientControl(wire).type === type));
}

async function openControlledStateSession(options = {}) {
  let socket;
  let automatic = true;
  const capabilities = CAPABILITIES.observe | CAPABILITIES.private_human_request_detail | CAPABILITIES.human_actions | CAPABILITIES.terminal_input;
  const server = (current, frame) => {
    if (frame.type === "PAIR_PROVE") current.reply(encodePairResult(frame.id, { client_id: clientID, capabilities }));
    if (frame.type === "AUTH_PROVE") current.reply(encodeAuthResult(frame.id, { client_id: clientID, capabilities }));
    if (frame.type === "STATE_GET" && automatic) replyStatePage(current, frame, 1n);
  };
  const session = new BrowserSession({
    url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example",
    challenge, keyStore: new MemoryKeys(), socketFactory: () => { socket = new Socket(server); return socket; }, ...options,
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(session.status, "ready");
  automatic = false;
  return { session, socket };
}

async function completePendingSnapshot(socket, head) {
  for (const cursor of [null, "p", "a", "t", "r"]) {
    const frame = decodeClientControl(socket.sent.at(-1));
    assert.equal(frame.type, "STATE_GET");
    assert.equal(frame.body.cursor, cursor);
    replyStatePage(socket, frame, head);
    await tick();
  }
}

const firstEntityID = "aa".repeat(16);
const secondEntityID = "bb".repeat(16);

async function beginRetiredEntityRace(options = {}) {
  const opened = await openControlledStateSession(options);
  const watch = lastFrame(opened.socket, "STATE_SUBSCRIBE");
  opened.socket.reply(encodeStateEvent(watch.id, { event: "entity_changed", sequence: 2n, head: 2n, entity_kind: "project", entity_id: firstEntityID, revision: 2n, deleted: false }));
  await tick();
  const first = lastFrame(opened.socket, "STATE_ENTITY_GET");
  opened.socket.reply(encodeStateEvent(watch.id, { event: "entity_changed", sequence: 3n, head: 3n, entity_kind: "agent", entity_id: secondEntityID, revision: 2n, deleted: false }));
  await tick();
  const second = lastFrame(opened.socket, "STATE_ENTITY_GET");
  opened.socket.reply(encodeStateRestart(watch.id, { head: 3n, floor: 1n, reason: "gap" }));
  await tick();
  assert.equal(opened.session.status, "syncing");
  const snapshot = lastFrame(opened.socket, "STATE_GET");
  return { ...opened, watch, first, second, snapshot };
}

async function openHumanSession(onError) {
  let socket;
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1",
    host: "127.0.0.1:43123",
    origin: "https://preview.example",
    challenge,
    keyStore: new MemoryKeys(),
    socketFactory: () => { socket = new Socket(serverFor); return socket; },
    onError,
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  return { session, socket };
}

function humanDetail(requestId, overrides = {}) {
  return {
    request_id: requestId,
    revision: 1n,
    question: "Choose",
    can_reply: true,
    reply_max_bytes: 8192,
    terminal_target: { run_id: runID, session_id: "99".repeat(16), run_revision: 1n, session_revision: 1n },
    cancel_run: { expected_request_revision: 1n, expected_run_revision: 1n },
    ...overrides,
  };
}

test("pairing signs through WebCrypto, persists the key, fetches all pages, and subscribes", async () => {
  const store = new MemoryKeys();
  const sockets = [];
  const states = [];
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1",
    host: "127.0.0.1:43123",
    origin: "https://preview.example",
    challenge,
    keyStore: store,
    socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; },
    onState: (state) => states.push(state),
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(session.status, "ready");
  assert.equal(session.clientId, clientID);
  assert.equal(store.value.key.extractable, false);
  assert.equal(states.at(-1).head, 1n);
  assert.equal(decodeClientControl(sockets[0].sent.at(-1)).type, "STATE_SUBSCRIBE");
  assert.equal(sockets[0].sent.some((wire) => wire.includes(challenge)), true, "challenge is used only in proof, never URL");
  session.close();
});

test("HumanRequest methods are fenced before HELLO and after close, without a raw getter", async () => {
  let socket;
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1",
    host: "127.0.0.1:43123",
    origin: "https://preview.example",
    keyStore: new MemoryKeys(),
    socketFactory: () => { socket = new Socket(() => {}); return socket; },
  });
  const connecting = session.connect();
  const detail = session.getHumanRequestDetail({ requestId: "77".repeat(16), expectedRevision: 1n });
  assert.equal("humanRequests" in session, false);
  assert.equal(socket.sent.length, 0);
  session.close();
  await assert.rejects(detail, (error) => error instanceof SessionError && error.code === "unauthorized");
  await assert.rejects(connecting, (error) => error instanceof SessionError && error.code === "closed");
  const closedDetail = session.getHumanRequestDetail({ requestId: "77".repeat(16), expectedRevision: 1n });
  assert.equal(socket.sent.length, 0);
  await assert.rejects(closedDetail, (error) => error instanceof SessionError && error.code === "closed");
});

test("authenticated HumanRequest methods emit exact frames and correlate results", async () => {
  const store = new MemoryKeys();
  let socket;
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1",
    host: "127.0.0.1:43123",
    origin: "https://preview.example",
    challenge,
    keyStore: store,
    socketFactory: () => { socket = new Socket(serverFor); return socket; },
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal("humanRequests" in session, false);

  const requestId = "77".repeat(16);
  const detailPending = session.getHumanRequestDetail({ requestId, expectedRevision: 1n });
  const detailFrame = decodeClientControl(socket.sent.at(-1));
  assert.equal(detailFrame.type, "HUMAN_REQUEST_DETAIL_GET");
  assert.deepEqual(detailFrame.body, { request_id: requestId, expected_revision: 1n });
  socket.reply(encodeHumanRequestDetail(detailFrame.id, {
    request_id: requestId, revision: 1n, question: "Choose", can_reply: true, reply_max_bytes: 8192,
    terminal_target: { run_id: runID, session_id: "99".repeat(16), run_revision: 1n, session_revision: 1n },
    cancel_run: { expected_request_revision: 1n, expected_run_revision: 1n },
  }));
  const detail = await detailPending;
  assert.equal(Object.isFrozen(detail), true);
  assert.equal(Object.isFrozen(detail.terminalTarget), true);
  assert.equal(Object.isFrozen(detail.cancelRun), true);
  assert.equal(typeof session.openTerminal(detail.terminalTarget).attach, "function");

  const reply = session.replyHumanRequest(detail, "ok");
  const replyFrame = decodeClientControl(socket.sent.at(-1));
  assert.equal(replyFrame.type, "HUMAN_REQUEST_REPLY");
  assert.deepEqual(replyFrame.body, { request_id: requestId, expected_revision: 1n, reply: "ok" });
  socket.reply(encodeHumanRequestReplyResult(replyFrame.id, { request_id: requestId, revision: 3n, status: "resolved" }));
  assert.equal((await reply).status, "resolved");
  await assert.rejects(session.replyHumanRequest(detail, "again"), (error) => error instanceof SessionError && error.code === "stale");
  await assert.rejects(session.cancelHumanRequest(detail.cancelRun), (error) => error instanceof SessionError && error.code === "stale");

  const cancelRequestId = "88".repeat(16);
  const cancelDetailPending = session.getHumanRequestDetail({ requestId: cancelRequestId, expectedRevision: 1n });
  const cancelDetailFrame = decodeClientControl(socket.sent.at(-1));
  socket.reply(encodeHumanRequestDetail(cancelDetailFrame.id, {
    request_id: cancelRequestId, revision: 1n, question: "Cancel?", can_reply: true, reply_max_bytes: 8192,
    terminal_target: { run_id: runID, session_id: "99".repeat(16), run_revision: 1n, session_revision: 1n },
    cancel_run: { expected_request_revision: 1n, expected_run_revision: 1n },
  }));
  const cancelDetail = await cancelDetailPending;
  const cancel = session.cancelHumanRequest(cancelDetail.cancelRun);
  const cancelFrame = decodeClientControl(socket.sent.at(-1));
  assert.equal(cancelFrame.type, "HUMAN_REQUEST_CANCEL_RUN");
  assert.deepEqual(cancelFrame.body, { request_id: cancelRequestId, expected_request_revision: 1n, expected_run_revision: 1n });
  socket.reply(encodeHumanRequestCancelRunResult(cancelFrame.id, { run_id: runID, run_revision: 2n, request_id: cancelRequestId, request_revision: 2n }));
  assert.equal((await cancel).run_id, runID);
  await assert.rejects(session.replyHumanRequest(cancelDetail, "too late"), (error) => error instanceof SessionError && error.code === "stale");
  session.close();
});

test("HumanRequest detail operations are bounded and exact-envelope correlated", async (t) => {
  await t.test("envelope, subject, and revision mismatches close the generation", async () => {
    for (const mismatch of ["envelope", "subject", "revision"]) {
      const errors = [];
      const { session, socket } = await openHumanSession((error) => errors.push(error));
      const requestId = "71".repeat(16);
      const pending = session.getHumanRequestDetail({ requestId, expectedRevision: 1n });
      const frame = decodeClientControl(socket.sent.at(-1));
      socket.reply(encodeHumanRequestDetail(mismatch === "envelope" ? "forged-detail" : frame.id, humanDetail(mismatch === "subject" ? "72".repeat(16) : requestId, { revision: mismatch === "revision" ? 2n : 1n, can_reply: false, terminal_target: null, cancel_run: null })));
      await assert.rejects(pending, (error) => error instanceof ProtocolError && error.code === "malformed");
      assert.equal(session.status, "closed", mismatch);
      assert.equal(errors.at(-1) instanceof ProtocolError, true, mismatch);
    }
  });

  await t.test("close settles pending detail without replay", async () => {
    const { session, socket } = await openHumanSession();
    const pending = session.getHumanRequestDetail({ requestId: "73".repeat(16), expectedRevision: 1n });
    const sent = socket.sent.length;
    session.close();
    await assert.rejects(pending, (error) => error instanceof SessionError && error.code === "closed");
    assert.equal(socket.sent.length, sent);
  });

  await t.test("capacity and same-subject gates are finite", async () => {
    const { session } = await openHumanSession();
    const pending = [];
    for (let index = 1; index <= 32; index += 1) {
      const requestId = index.toString(16).padStart(32, "0");
      pending.push(session.getHumanRequestDetail({ requestId, expectedRevision: 1n }).then(() => "resolved", (error) => error));
      if (index === 1) await assert.rejects(session.getHumanRequestDetail({ requestId, expectedRevision: 1n }), (error) => error instanceof SessionError && error.code === "rate_limited");
    }
    await assert.rejects(session.getHumanRequestDetail({ requestId: "ff".repeat(16), expectedRevision: 1n }), (error) => error instanceof SessionError && error.code === "rate_limited");
    session.close();
    const settled = await Promise.all(pending);
    assert.equal(settled.every((error) => error instanceof SessionError && error.code === "closed"), true);
  });
});

test("HumanRequest detail-bound reply and cancellation authority is one-shot", async () => {
  const first = await openHumanSession();
  const unavailableId = "74".repeat(16);
  const unavailablePending = first.session.getHumanRequestDetail({ requestId: unavailableId, expectedRevision: 1n });
  let frame = decodeClientControl(first.socket.sent.at(-1));
  first.socket.reply(encodeHumanRequestDetail(frame.id, humanDetail(unavailableId, { can_reply: false, terminal_target: null, cancel_run: null })));
  const unavailable = await unavailablePending;
  const unavailableSent = first.socket.sent.length;
  await assert.rejects(first.session.replyHumanRequest(unavailable, "no"), (error) => error instanceof SessionError && error.code === "stale");
  assert.equal(first.socket.sent.length, unavailableSent);

  const availableId = "75".repeat(16);
  const availablePending = first.session.getHumanRequestDetail({ requestId: availableId, expectedRevision: 1n });
  frame = decodeClientControl(first.socket.sent.at(-1));
  first.socket.reply(encodeHumanRequestDetail(frame.id, humanDetail(availableId)));
  const available = await availablePending;
  const beforeInvalid = first.socket.sent.length;
  await assert.rejects(first.session.replyHumanRequest(available, "x".repeat(8193)), (error) => error instanceof SessionError && error.code === "invalid_request");
  await assert.rejects(first.session.cancelHumanRequest(Object.freeze({ ...available.cancelRun })), (error) => error instanceof SessionError && error.code === "stale");
  assert.equal(first.socket.sent.length, beforeInvalid);

  first.session.close();
  const second = await openHumanSession();
  await assert.rejects(second.session.replyHumanRequest(available, "old"), (error) => error instanceof SessionError && error.code === "stale");
  await assert.rejects(second.session.cancelHumanRequest(available.cancelRun), (error) => error instanceof SessionError && error.code === "stale");

  const forgedId = "76".repeat(16);
  const forgedDetailPending = second.session.getHumanRequestDetail({ requestId: forgedId, expectedRevision: 1n });
  frame = decodeClientControl(second.socket.sent.at(-1));
  second.socket.reply(encodeHumanRequestDetail(frame.id, humanDetail(forgedId)));
  const forgedDetail = await forgedDetailPending;
  const cancel = second.session.cancelHumanRequest(forgedDetail.cancelRun);
  const cancelFrame = decodeClientControl(second.socket.sent.at(-1));
  second.socket.reply(encodeHumanRequestCancelRunResult(cancelFrame.id, { run_id: "aa".repeat(16), run_revision: 2n, request_id: forgedId, request_revision: 2n }));
  await assert.rejects(cancel, (error) => error instanceof ProtocolError && error.code === "malformed");
  assert.equal(second.session.status, "closed");
});

test("malformed and binary frames fail with finite errors and never leak frame data", async () => {
  const errors = [];
  let socket;
  const session = new BrowserSession({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    keyStore: new MemoryKeys(),
    socketFactory: () => { socket = new Socket(() => {}); return socket; },
    onError: (error) => errors.push(error),
  });
  const pending = session.connect();
  socket.onmessage({ data: new Uint8Array([1, 2, 3]) });
  await assert.rejects(pending, (error) => error instanceof ProtocolError && error.code === "malformed");
  assert.equal(errors.length, 1);
  assert.equal(errors[0].message, "malformed");
  assert.equal(String(errors[0]).includes("1,2,3"), false);
});

test("a closed pairing generation rejects as uncertain and never saves a permanent key", async () => {
  const store = new MemoryKeys();
  let socket;
  const session = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: store, socketFactory: () => { socket = new Socket(() => {}); return socket; } });
  const pending = session.connect();
  await new Promise((resolve) => setTimeout(resolve, 0));
  socket.close();
  await assert.rejects(pending, (error) => error instanceof SessionError && error.code === "pairing_uncertain");
  assert.equal(store.value, null);
});

test("event refresh is correlated to its entity and updates canonical state", async () => {
  const store = new MemoryKeys();
  const states = [];
  let socket;
  const session = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: store, socketFactory: () => { socket = new Socket(serverFor); return socket; }, onState: (state) => states.push(state) });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const watch = decodeClientControl(socket.sent.findLast((wire) => decodeClientControl(wire).type === "STATE_SUBSCRIBE"));
  socket.onmessage({ data: encodeStateEvent(watch.id, { event: "entity_changed", sequence: 2n, head: 2n, entity_kind: "factory", entity_id: "factory", revision: 2n, deleted: false }) });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const refresh = decodeClientControl(socket.sent.at(-1));
  assert.equal(refresh.type, "STATE_ENTITY_GET");
  socket.reply(encodeStateEntity(refresh.id, { head: 2n, kind: "factory", id: "factory", revision: 2n, deleted: false, item: factory(2n) }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(states.at(-1).factory[0].revision, 2n);
  session.close();
});

test("wire restart retires exact entity responses, then publishes one fresh monotonic snapshot on the same socket", async () => {
  const states = [];
  const errors = [];
  const { session, socket, watch, first, second } = await beginRetiredEntityRace({ onState: (state) => states.push(state), onError: (error) => errors.push(error) });
  const beforeRetired = states.length;
  socket.reply(encodeStateEntity(first.id, { head: 3n, kind: "project", id: firstEntityID, revision: 2n, deleted: true, item: null }));
  await tick();
  assert.equal(session.status, "syncing", errors.map((error) => `${error.name}:${error.code}`).join(","));
  assert.equal(states.length, beforeRetired, "a retired response must not publish its body");
  socket.reply(encodeServerError({ code: "not_found", retryable: false }, second.id));
  await tick();
  assert.equal(session.status, "syncing", errors.map((error) => `${error.name}:${error.code}`).join(","));

  await completePendingSnapshot(socket, 4n);
  assert.equal(session.status, "ready");
  assert.equal(socket.readyState, 1);
  assert.deepEqual(states.map((state) => state.head), [1n, 2n, 3n, 4n]);
  assert.equal(states.at(-1).projects.size, 0, "retired project body never entered the fresh snapshot");
  const replacementWatch = lastFrame(socket, "STATE_SUBSCRIBE");
  assert.notEqual(replacementWatch.id, watch.id);
  assert.equal(replacementWatch.body.after, 4n);
  session.close();
});

test("retired entity correlations match exactly and consume one entity or error response", async (t) => {
  for (const mismatch of ["kind", "id", "duplicate entity", "duplicate error"]) {
    await t.test(mismatch, async () => {
      const errors = [];
      const { session, socket, first } = await beginRetiredEntityRace({ onError: (error) => errors.push(error) });
      const exact = { head: 3n, kind: "project", id: firstEntityID, revision: 2n, deleted: true, item: null };
      if (mismatch === "kind") socket.reply(encodeStateEntity(first.id, { ...exact, kind: "task" }));
      else if (mismatch === "id") socket.reply(encodeStateEntity(first.id, { ...exact, id: secondEntityID }));
      else if (mismatch === "duplicate entity") {
        socket.reply(encodeStateEntity(first.id, exact));
        await tick();
        assert.equal(session.status, "syncing");
        socket.reply(encodeStateEntity(first.id, exact));
      } else {
        const error = encodeServerError({ code: "not_found", retryable: false }, first.id);
        socket.reply(error);
        await tick();
        assert.equal(session.status, "syncing");
        socket.reply(error);
      }
      await tick();
      assert.equal(session.status, "closed");
      assert.equal(mismatch === "duplicate error"
        ? errors.at(-1) instanceof SessionError && errors.at(-1).code === "not_found"
        : errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
    });
  }
});

test("unknown state entities, restart IDs, and snapshot IDs fail closed", async (t) => {
  await t.test("unknown entity", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    socket.reply(encodeStateEntity("unknown-entity", { head: 1n, kind: "project", id: firstEntityID, revision: 1n, deleted: true, item: null }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });

  await t.test("arbitrary restart", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    socket.reply(encodeStateRestart("forged-watch", { head: 1n, floor: 1n, reason: "gap" }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });

  await t.test("unknown snapshot", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    const watch = lastFrame(socket, "STATE_SUBSCRIBE");
    socket.reply(encodeStateRestart(watch.id, { head: 1n, floor: 1n, reason: "gap" }));
    await tick();
    socket.reply(encodeStateSnapshot("forged-snapshot", { head: 1n, kind: "factory", items: [factory()], next_cursor: "p" }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });
});

test("a fresh snapshot cannot overtake its retired entity responses", async () => {
  const errors = [];
  const { session, socket, snapshot } = await beginRetiredEntityRace({ onError: (error) => errors.push(error) });
  socket.reply(encodeStateSnapshot(snapshot.id, { head: 3n, kind: "factory", items: [factory(3n)], next_cursor: "p" }));
  await tick();
  assert.equal(session.status, "closed");
  assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
});

test("outstanding entity refreshes are capped at 32 and close retryably before a 33rd request", async () => {
  const errors = [];
  const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
  const watch = lastFrame(socket, "STATE_SUBSCRIBE");
  for (let index = 0; index < 33; index += 1) {
    const sequence = BigInt(index + 2);
    socket.reply(encodeStateEvent(watch.id, { event: "entity_changed", sequence, head: sequence, entity_kind: "project", entity_id: firstEntityID, revision: sequence, deleted: false }));
    await tick();
  }
  assert.equal(session.status, "closed");
  assert.equal(socket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_ENTITY_GET").length, 32);
  assert.equal(errors.at(-1) instanceof SessionError && errors.at(-1).code === "rate_limited" && errors.at(-1).retryable, true);
});

test("an exact pending snapshot restart starts a fresh bounded snapshot on the same socket", async () => {
  const { session, socket } = await openControlledStateSession();
  const watch = lastFrame(socket, "STATE_SUBSCRIBE");
  socket.reply(encodeStateRestart(watch.id, { head: 2n, floor: 1n, reason: "gap" }));
  await tick();
  const firstSnapshot = lastFrame(socket, "STATE_GET");
  socket.reply(encodeStateRestart(firstSnapshot.id, { head: 2n, floor: 1n, reason: "head_changed" }));
  await tick();
  const replacement = lastFrame(socket, "STATE_GET");
  assert.notEqual(replacement.id, firstSnapshot.id);
  await completePendingSnapshot(socket, 2n);
  assert.equal(session.status, "ready");
  assert.equal(session.state.head, 2n);
  session.close();
});

test("closing from the restart status callback cannot send a post-close snapshot request", async () => {
  let session;
  let becameReady = false;
  const opened = await openControlledStateSession({ onStatus: (status) => {
    if (status === "ready") becameReady = true;
    if (becameReady && status === "syncing") session?.close();
  } });
  session = opened.session;
  const stateGets = opened.socket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length;
  const watch = lastFrame(opened.socket, "STATE_SUBSCRIBE");
  opened.socket.reply(encodeStateRestart(watch.id, { head: 2n, floor: 1n, reason: "gap" }));
  await tick();
  assert.equal(session.status, "closed");
  assert.equal(opened.socket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length, stateGets);
});

test("late old-watch events and reducer-detected gaps close instead of installing another watch", async (t) => {
  await t.test("late old-watch event", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    const watch = lastFrame(socket, "STATE_SUBSCRIBE");
    socket.reply(encodeStateRestart(watch.id, { head: 2n, floor: 1n, reason: "gap" }));
    await tick();
    socket.reply(encodeStateEvent(watch.id, { event: "hidden_advance", sequence: 2n, head: 2n }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });

  await t.test("reducer gap", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    const initialGets = socket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length;
    const watch = lastFrame(socket, "STATE_SUBSCRIBE");
    socket.reply(encodeStateEvent(watch.id, { event: "hidden_advance", sequence: 3n, head: 3n }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(socket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length, initialGets);
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });
});

test("wire restart and the first fresh snapshot preserve a session-lifetime monotonic head floor", async (t) => {
  await t.test("restart cannot lower the published head", async () => {
    const errors = [];
    const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error) });
    const watch = lastFrame(socket, "STATE_SUBSCRIBE");
    socket.reply(encodeStateRestart(watch.id, { head: 0n, floor: 1n, reason: "gap" }));
    await tick();
    assert.equal(session.status, "closed");
    assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
  });

  for (const [label, head, accepted] of [["N-1", 4n, false], ["N", 5n, true], ["N+1", 6n, true]]) {
    await t.test(label, async () => {
      const errors = [];
      const states = [];
      const { session, socket } = await openControlledStateSession({ onError: (error) => errors.push(error), onState: (state) => states.push(state) });
      const watch = lastFrame(socket, "STATE_SUBSCRIBE");
      socket.reply(encodeStateRestart(watch.id, { head: 5n, floor: 1n, reason: "gap" }));
      await tick();
      if (!accepted) {
        const snapshot = lastFrame(socket, "STATE_GET");
        socket.reply(encodeStateSnapshot(snapshot.id, { head, kind: "factory", items: [factory(head)], next_cursor: "p" }));
        await tick();
        assert.equal(session.status, "closed");
        assert.equal(errors.at(-1) instanceof ProtocolError && errors.at(-1).code === "malformed", true);
        assert.deepEqual(states.map((state) => state.head), [1n]);
        return;
      }
      await completePendingSnapshot(socket, head);
      assert.equal(session.status, "ready");
      assert.deepEqual(states.map((state) => state.head), [1n, head]);
      session.close();
    });
  }
});

test("reconnect creates a fresh generation and stale socket frames are fenced", async () => {
  const store = new MemoryKeys();
  const sockets = [];
  const pairing = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: store, socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; } });
  await pairing.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  pairing.close();
  const errors = [];
  const timer = new VirtualTimer();
  const client = new BrowserClient({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", keyStore: store, timer, reconnectInitialDelayMs: 10, socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; }, onError: (error) => errors.push(error) });
  await client.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  sockets[1].close();
  timer.advance(10);
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(sockets.length, 3, errors.map((error) => error.code).join(","));
  sockets[1].reply(encodeHello({ daemon_id: "aa".repeat(16), boot_id: "bb".repeat(16), connection_nonce: "cc".repeat(32) }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.notEqual(client.session, undefined);
  assert.equal(client.session.status, "ready", errors.map((error) => error.code).join(","));
  client.close();
});

class VirtualTimer {
  now = 0;
  next = 1;
  tasks = new Map();
  delays = [];
  setTimeout(callback, delay) {
    const id = this.next++;
    this.tasks.set(id, { at: this.now + delay, callback });
    this.delays.push(delay);
    return id;
  }
  clearTimeout(id) { this.tasks.delete(id); }
  advance(milliseconds) {
    this.now += milliseconds;
    while (true) {
      const due = [...this.tasks.entries()].filter(([, task]) => task.at <= this.now).sort((a, b) => a[1].at - b[1].at)[0];
      if (due === undefined) return;
      this.tasks.delete(due[0]);
      due[1].callback();
    }
  }
}

test("successful pairing consumes challenge state and reconnects through AUTH after close", async () => {
  const store = new MemoryKeys();
  const timer = new VirtualTimer();
  const sockets = [];
  const client = new BrowserClient({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: store, timer, reconnectInitialDelayMs: 10, reconnectMaxDelayMs: 20, socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; } });
  await client.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(decodeClientControl(sockets[0].sent.find((wire) => decodeClientControl(wire).type === "PAIR_PROVE")).type, "PAIR_PROVE");
  sockets[0].close();
  assert.deepEqual(timer.delays, [10]);
  timer.advance(10);
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(sockets.length, 2);
  assert.equal(decodeClientControl(sockets[1].sent[0]).type, "AUTH_PROVE");
  client.close();
});

test("pairing challenge helper returns the one-shot value and clears the fragment", () => {
  const location = { hash: `#challenge=${challenge}`, pathname: "/", search: "?preview=1" };
  let replaced;
  const value = consumePairingChallenge(location, { replaceState: (_state, _title, url) => { replaced = url; } });
  assert.equal(value, challenge);
  assert.equal(replaced, "/?preview=1");
});

test("consumer callback exceptions cannot escape cleanup or stop a later state lifecycle", async () => {
  let malformedSocket;
  const malformed = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", keyStore: new MemoryKeys(), socketFactory: () => { malformedSocket = new Socket(() => {}); return malformedSocket; }, onError: () => { throw new Error("onError"); }, onStatus: () => { throw new Error("onStatus"); } });
  const rejected = malformed.connect();
  malformedSocket.onmessage({ data: new Uint8Array([0xff]) });
  await assert.rejects(rejected, (error) => error instanceof ProtocolError && error.code === "malformed");
  assert.equal(malformed.status, "closed");

  const states = [];
  const session = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: new MemoryKeys(), socketFactory: () => new Socket(serverFor), onState: (state) => { states.push(state); throw new Error("onState"); }, onStatus: () => { throw new Error("onStatus"); } });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(session.status, "ready");
  assert.ok(states.length > 0);
  session.close();
});

test("unavailable daemon reconnects use bounded exponential virtual time", async () => {
  const pairingStore = new MemoryKeys();
  const pairing = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: pairingStore, socketFactory: () => new Socket(serverFor) });
  await pairing.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  pairing.close();
  const timer = new VirtualTimer();
  let attempts = 0;
  const unavailable = new BrowserClient({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", keyStore: pairingStore, timer, reconnectInitialDelayMs: 10, reconnectMaxDelayMs: 20, socketFactory: () => { attempts += 1; throw new Error("no daemon"); } });
  await assert.rejects(unavailable.connect(), (error) => error instanceof SessionError && error.code === "connection");
  assert.equal(attempts, 1);
  timer.advance(9);
  assert.equal(attempts, 1);
  timer.advance(1);
  assert.equal(attempts, 2);
  timer.advance(20);
  assert.equal(attempts, 3);
  timer.advance(20);
  assert.equal(attempts, 4);
  unavailable.close();
  assert.equal(timer.tasks.size, 0);
});

test("close clears a pending reconnect timer and manual connect can schedule a new lifecycle", async () => {
  const pairingStore = new MemoryKeys();
  const pairing = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: pairingStore, socketFactory: () => new Socket(serverFor) });
  await pairing.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  pairing.close();
  const timer = new VirtualTimer();
  let available = false;
  let attempts = 0;
  const client = new BrowserClient({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", keyStore: pairingStore, timer, reconnectInitialDelayMs: 10, socketFactory: () => { attempts += 1; if (!available) throw new Error("no daemon"); return new Socket(serverFor); } });
  await assert.rejects(client.connect());
  assert.equal(timer.tasks.size, 1);
  client.close();
  timer.advance(100);
  assert.equal(attempts, 1);
  available = true;
  await client.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(client.status, "ready");
  assert.equal(attempts, 2);
  client.close();
});

test("close fences deferred load, sign, and pairing persistence completions", async () => {
  let releaseLoad;
  let loadStarted;
  const loadStore = { async load() { loadStarted?.(); await new Promise((resolve) => { releaseLoad = resolve; }); return null; }, async save() {} };
  let loadSocket;
  const loading = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: loadStore, socketFactory: () => { loadSocket = new Socket(() => {}); return loadSocket; } });
  const loadReady = new Promise((resolve) => { loadStarted = resolve; });
  const loadingConnect = loading.connect();
  await loadReady;
  loading.close();
  releaseLoad();
  await assert.rejects(loadingConnect, (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(loadSocket.sent.length, 0);

  const sourceStore = new MemoryKeys();
  const source = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: sourceStore, socketFactory: () => new Socket(serverFor) });
  await source.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  source.close();
  let releaseSign;
  let signStarted;
  const realCrypto = globalThis.crypto;
  const signingCrypto = { ...realCrypto, subtle: { ...realCrypto.subtle, sign: async (...args) => { signStarted?.(); await new Promise((resolve) => { releaseSign = resolve; }); return realCrypto.subtle.sign(...args); } } };
  let signSocket;
  const signing = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", keyStore: sourceStore, crypto: signingCrypto, socketFactory: () => { signSocket = new Socket(() => {}); return signSocket; } });
  const signReady = new Promise((resolve) => { signStarted = resolve; });
  const signingConnect = signing.connect();
  await signReady;
  signing.close();
  releaseSign();
  await assert.rejects(signingConnect, (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(signSocket.sent.length, 0);

  let releaseSave;
  let saveStarted;
  const saveStore = { async load() { return null; }, async save() { saveStarted?.(); await new Promise((resolve) => { releaseSave = resolve; }); } };
  let saveSocket;
  const saving = new BrowserSession({ url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: saveStore, socketFactory: () => { saveSocket = new Socket(serverFor); return saveSocket; } });
  const saveReady = new Promise((resolve) => { saveStarted = resolve; });
  const savingConnect = saving.connect();
  await saveReady;
  saving.close();
  releaseSave();
  await assert.rejects(savingConnect, (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(saveSocket.sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length, 0);
});

test("reentrant connecting status close rejects before creating a socket", async () => {
  let session;
  let sessionSockets = 0;
  session = new BrowserSession({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    keyStore: new MemoryKeys(),
    onStatus: (status) => { if (status === "connecting") session.close(); },
    socketFactory: () => { sessionSockets += 1; return new Socket(() => {}); },
  });
  await assert.rejects(session.connect(), (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(sessionSockets, 0);
  assert.equal(session.status, "closed");

  let factorySession;
  let factorySocket;
  factorySession = new BrowserSession({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    keyStore: new MemoryKeys(),
    socketFactory: () => {
      factorySocket = new Socket(() => {});
      factorySession.close();
      return factorySocket;
    },
  });
  await assert.rejects(factorySession.connect(), (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(factorySocket.readyState, 3);

  let client;
  let clientSockets = 0;
  client = new BrowserClient({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    keyStore: new MemoryKeys(),
    onStatus: (status) => { if (status === "connecting") client.close(); },
    socketFactory: () => { clientSockets += 1; return new Socket(() => {}); },
  });
  await assert.rejects(client.connect(), (error) => error instanceof SessionError && error.code === "closed");
  assert.equal(clientSockets, 0);
  assert.equal(client.status, "closed");
});

test("BrowserClient does not replay a one-shot proof while PairResult save is deferred", async () => {
  let releaseSave;
  let saveStarted;
  let saved;
  let persisted = null;
  const store = {
    async load() { return persisted; },
    async save(value) {
      saved = value;
      saveStarted?.();
      await new Promise((resolve) => { releaseSave = resolve; });
      persisted = value;
    },
  };
  const timer = new VirtualTimer();
  const sockets = [];
  const client = new BrowserClient({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    challenge,
    keyStore: store,
    timer,
    socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; },
  });
  const saveReady = new Promise((resolve) => { saveStarted = resolve; });
  const first = client.connect();
  await saveReady;
  assert.equal(sockets[0].sent.filter((wire) => decodeClientControl(wire).type === "PAIR_PROVE").length, 1);
  const second = client.connect();
  assert.strictEqual(second, first);
  client.close();
  releaseSave();
  await assert.rejects(first, (error) => error instanceof SessionError && error.code === "closed");
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(saved);
  assert.ok(persisted);
  assert.equal(client.status, "closed");
  assert.equal(sockets[0].sent.filter((wire) => decodeClientControl(wire).type === "STATE_GET").length, 0);
  await client.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(sockets.length, 2);
  assert.equal(decodeClientControl(sockets[1].sent[0]).type, "AUTH_PROVE");
  assert.equal(sockets[1].sent.filter((wire) => decodeClientControl(wire).type === "PAIR_PROVE").length, 0);
  client.close();
});

class HostileTimer {
  callbacks = [];
  clearCalls = [];
  setTimeout(callback, delay) {
    this.callbacks.push({ callback, delay });
    return undefined;
  }
  clearTimeout(handle) {
    this.clearCalls.push(handle);
    throw new Error("timer cleanup failed");
  }
}

test("reconnect timer ownership fences stale callbacks with undefined handles", async () => {
  const store = new MemoryKeys();
  const timer = new HostileTimer();
  let attempts = 0;
  const client = new BrowserClient({
    url: "ws://127.0.0.1/browser/v1",
    host: "127.0.0.1",
    origin: "https://preview.example",
    keyStore: store,
    timer,
    reconnectInitialDelayMs: 10,
    reconnectMaxDelayMs: 20,
    socketFactory: () => { attempts += 1; throw new Error("no daemon"); },
  });
  await assert.rejects(client.connect(), (error) => error instanceof SessionError && error.code === "connection");
  assert.equal(attempts, 1);
  assert.equal(timer.callbacks.length, 1);

  await assert.rejects(client.connect(), (error) => error instanceof SessionError && error.code === "connection");
  assert.equal(attempts, 2);
  assert.equal(timer.callbacks.length, 2);
  timer.callbacks[0].callback();
  assert.equal(attempts, 2, "stale callback must not consume the current schedule");
  timer.callbacks[1].callback();
  assert.equal(attempts, 3);
  assert.equal(timer.callbacks.length, 3);

  client.close();
  assert.ok(timer.clearCalls.length >= 2);
  assert.ok(timer.clearCalls.every((handle) => handle === undefined));
  timer.callbacks[2].callback();
  assert.equal(attempts, 3, "callback after close must remain fenced");
});

test("agent terminal discovery mints an opaque generation-bound target for openTerminal", async () => {
  const store = new MemoryKeys();
  const agentId = "77".repeat(16);
  const terminalSessionId = "88".repeat(16);
  let socket;
  const targetServer = (current) => {
    serverFor(current);
    const frame = decodeClientControl(current.sent.at(-1));
    if (frame.type === "TERMINAL_TARGET_GET") current.reply(encodeTerminalTarget(frame.id, {
      agent_id: frame.body.agent_id,
      agent_revision: frame.body.expected_agent_revision,
      head: frame.body.expected_head,
      target: { run_id: runID, session_id: terminalSessionId, run_revision: 3n, session_revision: 4n },
    }));
  };
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: store, socketFactory: () => { socket = new Socket(targetServer); return socket; },
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const pending = session.resolveAgentTerminal({ agentId, expectedAgentRevision: 9n, expectedHead: 12n });
  const targetGet = decodeClientControl(socket.sent.at(-1));
  assert.equal(targetGet.type, "TERMINAL_TARGET_GET");
  const target = await pending;
  assert.equal(Object.isFrozen(target), true);
  assert.equal("runId" in target, false);
  assert.throws(() => session.openTerminal({ ...target }), (error) => error instanceof SessionError && error.code === "stale");
  const terminal = session.openTerminal(target);
  const other = await openHumanSession();
  assert.throws(() => other.session.openTerminal(target), (error) => error instanceof SessionError && error.code === "stale");
  other.session.close();
  const attach = terminal.attach();
  const attachFrame = decodeClientControl(socket.sent.at(-1));
  assert.deepEqual(attachFrame.body, { run_id: runID, session_id: terminalSessionId, expected_run_revision: 3n, expected_session_revision: 4n, after_sequence: 0n });
  socket.reply(encodeTerminalAttached(attachFrame.id, { session_id: terminalSessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n }));
  await attach;
  const pendingDetach = terminal.detach();
  socket.close();
  await assert.rejects(pendingDetach, (error) => error instanceof SessionError && error.code === "connection");
  assert.equal(session.status, "closed");
  session.close();
  assert.throws(() => session.openTerminal(target), (error) => error instanceof SessionError && error.code === "closed");
});

test("terminal target discovery is bounded, exact-correlated, and null is explicit", async () => {
  const store = new MemoryKeys();
  const agentId = "79".repeat(16);
  let socket;
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: store, socketFactory: () => { socket = new Socket(serverFor); return socket; },
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const pending = Array.from({ length: 32 }, () => session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n }));
  await assert.rejects(session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n }), (error) => error instanceof SessionError && error.code === "rate_limited");
  const first = decodeClientControl(socket.sent.find((wire) => decodeClientControl(wire).type === "TERMINAL_TARGET_GET"));
  socket.reply(encodeTerminalTarget(first.id, { agent_id: agentId, agent_revision: 1n, head: 1n, target: null }));
  assert.equal(await pending[0], null);
  const next = session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n });
  session.close();
  await assert.rejects(next, (error) => error instanceof SessionError && error.code === "closed");
  await Promise.allSettled(pending.slice(1));

  let malformedSocket;
  const malformed = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: new MemoryKeys(), socketFactory: () => { malformedSocket = new Socket(serverFor); return malformedSocket; },
  });
  await malformed.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const bad = malformed.resolveAgentTerminal({ agentId, expectedAgentRevision: 2n, expectedHead: 4n });
  const badFrame = decodeClientControl(malformedSocket.sent.at(-1));
  malformedSocket.reply(encodeTerminalTarget(badFrame.id, { agent_id: agentId, agent_revision: 3n, head: 4n, target: null }));
  await assert.rejects(bad, (error) => error instanceof ProtocolError && error.code === "malformed");
  assert.equal(malformed.status, "closed");
});

test("EXIT closes the old handle and routes its duplicate away from a replacement", async () => {
  const agentId = "81".repeat(16);
  const terminalSessionId = "82".repeat(16);
  let socket;
  const targetServer = (current) => {
    serverFor(current);
    const frame = decodeClientControl(current.sent.at(-1));
    if (frame.type === "TERMINAL_TARGET_GET") current.reply(encodeTerminalTarget(frame.id, {
      agent_id: frame.body.agent_id,
      agent_revision: frame.body.expected_agent_revision,
      head: frame.body.expected_head,
      target: { run_id: runID, session_id: terminalSessionId, run_revision: 3n, session_revision: 4n },
    }));
  };
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: new MemoryKeys(), socketFactory: () => { socket = new Socket(targetServer); return socket; },
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const targetPending = session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n });
  const target = await targetPending;
  const terminal = session.openTerminal(target);
  const attach = terminal.attach();
  const attachFrame = decodeClientControl(socket.sent.at(-1));
  socket.reply(encodeTerminalAttached(attachFrame.id, { session_id: terminalSessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n }));
  await attach;
  const exit = { session_id: terminalSessionId, exit_code: 0, exit_signal: 0, aborted: false };
  socket.reply(encodeTerminalExit(attachFrame.id, exit));
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(terminal.closed, true);
  const before = socket.sent.length;
  for (const effect of [
    () => terminal.attach(),
    () => terminal.acquireInput(),
    () => terminal.releaseInput(),
    () => terminal.sendInput(new Uint8Array([1])),
    () => terminal.resize(24, 80),
    () => terminal.detach(),
  ]) assert.throws(effect, /closed/);
  assert.equal(socket.sent.length, before);

  const replacement = session.openTerminal(target);
  const replacementAttach = replacement.attach();
  const replacementFrame = decodeClientControl(socket.sent.at(-1));
  assert.equal(replacementFrame.body.after_sequence, 0n);
  socket.reply(encodeTerminalAttached(replacementFrame.id, { session_id: terminalSessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n }));
  await replacementAttach;
  socket.reply(encodeTerminalExit(attachFrame.id, exit));
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(replacement.closed, false);
  session.close();
});

test("RESET closes the old handle and a replacement may select a fresh after-sequence", async () => {
  const agentId = "83".repeat(16);
  const terminalSessionId = "84".repeat(16);
  let socket;
  const targetServer = (current) => {
    serverFor(current);
    const frame = decodeClientControl(current.sent.at(-1));
    if (frame.type === "TERMINAL_TARGET_GET") current.reply(encodeTerminalTarget(frame.id, {
      agent_id: frame.body.agent_id,
      agent_revision: frame.body.expected_agent_revision,
      head: frame.body.expected_head,
      target: { run_id: runID, session_id: terminalSessionId, run_revision: 3n, session_revision: 4n },
    }));
  };
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: new MemoryKeys(), socketFactory: () => { socket = new Socket(targetServer); return socket; },
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const targetPending = session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n });
  const target = await targetPending;
  const terminal = session.openTerminal(target);
  const attach = terminal.attach();
  const attachFrame = decodeClientControl(socket.sent.at(-1));
  socket.reply(encodeTerminalAttached(attachFrame.id, { session_id: terminalSessionId, floor: 0n, head: 8n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n }));
  await attach;
  const reset = { session_id: terminalSessionId, floor: 4n, head: 8n };
  socket.reply(encodeTerminalReset(attachFrame.id, reset));
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(terminal.closed, true);
  const before = socket.sent.length;
  for (const effect of [
    () => terminal.attach(),
    () => terminal.acquireInput(),
    () => terminal.releaseInput(),
    () => terminal.sendInput(new Uint8Array([1])),
    () => terminal.resize(24, 80),
    () => terminal.detach(),
  ]) assert.throws(effect, /closed/);
  assert.equal(socket.sent.length, before);

  const replacement = session.openTerminal(target, { afterSequence: 6n });
  const replacementAttach = replacement.attach();
  const replacementFrame = decodeClientControl(socket.sent.at(-1));
  assert.equal(replacementFrame.body.after_sequence, 6n);
  socket.reply(encodeTerminalAttached(replacementFrame.id, { session_id: terminalSessionId, floor: 4n, head: 8n, acknowledged_sequence: 6n, max_unacked_bytes: 65536n }));
  await replacementAttach;
  socket.reply(encodeTerminalReset(attachFrame.id, reset));
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(replacement.closed, false);
  session.close();
});

test("terminal replacement retains only the previous closed handle and fences evicted evidence", async () => {
  const agentId = "85".repeat(16);
  const terminalSessionId = "86".repeat(16);
  const errors = [];
  let socket;
  const targetServer = (current) => {
    serverFor(current);
    const frame = decodeClientControl(current.sent.at(-1));
    if (frame.type === "TERMINAL_TARGET_GET") current.reply(encodeTerminalTarget(frame.id, {
      agent_id: frame.body.agent_id,
      agent_revision: frame.body.expected_agent_revision,
      head: frame.body.expected_head,
      target: { run_id: runID, session_id: terminalSessionId, run_revision: 3n, session_revision: 4n },
    }));
  };
  const session = new BrowserSession({
    url: "ws://127.0.0.1:43123/browser/v1", host: "127.0.0.1:43123", origin: "https://preview.example",
    challenge, keyStore: new MemoryKeys(), socketFactory: () => { socket = new Socket(targetServer); return socket; },
    onError: (error) => errors.push(error),
  });
  await session.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  const target = await session.resolveAgentTerminal({ agentId, expectedAgentRevision: 1n, expectedHead: 1n });
  const attachFrames = [];
  let live;
  for (let cycle = 0; cycle < 64; cycle += 1) {
    live = session.openTerminal(target, { afterSequence: BigInt(cycle) });
    const attach = live.attach();
    const attachFrame = decodeClientControl(socket.sent.at(-1));
    attachFrames.push(attachFrame);
    socket.reply(encodeTerminalAttached(attachFrame.id, {
      session_id: terminalSessionId,
      floor: 0n,
      head: BigInt(cycle),
      acknowledged_sequence: BigInt(cycle),
      max_unacked_bytes: 65536n,
    }));
    await attach;
    socket.reply(encodeTerminalExit(attachFrame.id, { session_id: terminalSessionId, exit_code: 0, exit_signal: 0, aborted: false }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(live.closed, true);
    if (cycle > 0) {
      socket.reply(encodeTerminalExit(attachFrames[cycle - 1].id, { session_id: terminalSessionId, exit_code: 0, exit_signal: 0, aborted: false }));
      await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(live.closed, true, "immediately previous retired evidence cannot affect the current closed handle");
    }
  }

  const replacement = session.openTerminal(target);
  const replacementAttach = replacement.attach();
  const replacementFrame = decodeClientControl(socket.sent.at(-1));
  socket.reply(encodeTerminalAttached(replacementFrame.id, {
    session_id: terminalSessionId, floor: 0n, head: 0n, acknowledged_sequence: 0n, max_unacked_bytes: 65536n,
  }));
  await replacementAttach;
  socket.reply(encodeTerminalExit(attachFrames[0].id, { session_id: terminalSessionId, exit_code: 0, exit_signal: 0, aborted: false }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(session.status, "closed", "evidence older than the immediately previous handle is a protocol violation");
  assert.equal(replacement.closed, true);
  assert.equal(errors.filter((error) => error instanceof ProtocolError && error.code === "malformed").length, 1);
  session.close();
});
