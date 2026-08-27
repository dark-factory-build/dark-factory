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
