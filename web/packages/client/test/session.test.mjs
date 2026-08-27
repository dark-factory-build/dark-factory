import assert from "node:assert/strict";
import test from "node:test";
import {
  BrowserSession,
  BrowserClient,
  CAPABILITIES,
  ProtocolError,
  SessionError,
  decodeClientControl,
  encodeHello,
  encodePairResult,
  encodeAuthResult,
  encodeStateEntity,
  encodeStateEvent,
  encodeStateSnapshot,
} from "../dist/src/index.js";

const challenge = "11".repeat(32);
const daemonID = "22".repeat(16);
const bootID = "33".repeat(16);
const nonce = "44".repeat(32);
const clientID = "55".repeat(16);
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

function pages(socket, id, head = 1n) {
  socket.reply(encodeStateSnapshot(id, { head, kind: "factory", items: [factory()], next_cursor: "p" }));
  const empty = (kind, next) => socket.reply(encodeStateSnapshot(idFor(socket, kind), { head, kind, items: [], next_cursor: next }));
  // The fake sends one response per request; page IDs are read from the socket.
  void empty;
}

function idFor(socket, kind) {
  const frames = socket.sent.map((wire) => decodeClientControl(wire));
  return frames.findLast((frame) => frame.type === "STATE_GET" && frame.body.cursor === kind)?.id ?? frames.findLast((frame) => frame.type === "STATE_GET")?.id;
}

function serverFor(socket) {
  const frame = decodeClientControl(socket.sent.at(-1));
  if (frame.type === "PAIR_PROVE") socket.reply(encodePairResult(frame.id, { client_id: clientID, capabilities: CAPABILITIES.observe }));
  if (frame.type === "AUTH_PROVE") socket.reply(encodeAuthResult(frame.id, { client_id: clientID, capabilities: CAPABILITIES.observe }));
  if (frame.type === "STATE_GET") {
    const pagesByCursor = {
      null: ["factory", [factory()], "p"], p: ["project", [], "a"], a: ["agent", [], "t"], t: ["task", [], "r"], r: ["human_request", [], null],
    };
    const [kind, items, next_cursor] = pagesByCursor[String(frame.body.cursor)];
    socket.reply(encodeStateSnapshot(frame.id, { head: 1n, kind, items, next_cursor }));
  }
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

test("reconnect creates a fresh generation and stale socket frames are fenced", async () => {
  const store = new MemoryKeys();
  const sockets = [];
  const options = { url: "ws://127.0.0.1/browser/v1", host: "127.0.0.1", origin: "https://preview.example", challenge, keyStore: store, socketFactory: () => { const socket = new Socket(serverFor); sockets.push(socket); return socket; } };
  const client = new BrowserClient(options);
  await client.connect();
  await new Promise((resolve) => setTimeout(resolve, 10));
  options.challenge = undefined;
  sockets[0].close();
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(sockets.length, 2);
  sockets[0].reply(encodeHello({ daemon_id: "aa".repeat(16), boot_id: "bb".repeat(16), connection_nonce: "cc".repeat(32) }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.notEqual(client.session, undefined);
  assert.equal(client.session.status, "ready");
  client.close();
});
