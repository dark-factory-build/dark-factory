import assert from "node:assert/strict";
import test from "node:test";
import { SessionError } from "@dark-factory/client";
import { FactoryAppController } from "../dist/src/factory-app-controller.js";
import { FactoryConsole } from "../dist/src/factory-console.js";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fixtureState } from "../../../fixtures/state.mjs";

const challenge = "51".repeat(32);
const request = [...fixtureState.humanRequests.values()][0];

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse; });
  return { promise, resolve, reject };
}

function detailFor(item = request, suffix = "", replyMaxBytes = 8192) {
  return Object.freeze({
    requestId: item.id,
    revision: item.revision,
    question: `Should I continue${suffix}?`,
    canReply: true,
    replyMaxBytes,
    terminalTarget: Object.freeze({ target: suffix }),
    cancelRun: Object.freeze({ requestId: item.id, expectedRequestRevision: item.revision, expectedRunRevision: 17n }),
  });
}

function stateWithRequests(items) {
  return { ...fixtureState, humanRequests: new Map(items.map((item) => [item.id, item])) };
}

function harness(overrides = {}) {
  const snapshots = [];
  const calls = { connect: 0, close: 0 };
  const session = {
    getHumanRequestDetail: overrides.getDetail ?? (async () => detailFor()),
    replyHumanRequest: overrides.reply ?? (async () => ({ status: "resolved" })),
    cancelHumanRequest: overrides.cancel ?? (async () => ({ request_id: request.id })),
  };
  const client = {
    session,
    connect: () => { calls.connect += 1; return overrides.connect?.() ?? Promise.resolve(); },
    close: () => { calls.close += 1; overrides.close?.(); },
  };
  let clientOptions;
  const historyState = { route: "factory" };
  const controller = new FactoryAppController({
    origin: overrides.origin ?? "https://app.darkfactory.build",
    location: { hash: overrides.hash ?? `#df_pair=${challenge}`, pathname: "/factory", search: "?preview=1" },
    history: {
      state: historyState,
      replaceState: (state, _title, url) => {
        overrides.order?.push("scrub");
        overrides.replaceState?.(state, url);
      },
    },
    onChange: (snapshot) => snapshots.push(snapshot),
    clientFactory: (options) => {
      overrides.order?.push("create");
      clientOptions = options;
      overrides.clientFactory?.(options);
      return client;
    },
  });
  return {
    controller,
    client,
    clientOptions: () => clientOptions,
    snapshots,
    calls,
    emitStatus: (status) => clientOptions.onStatus(status),
    emitState: (state) => clientOptions.onState(state),
    emitError: (error) => clientOptions.onError(error),
    latest: () => snapshots.at(-1),
    historyState,
  };
}

test("pairing is scrubbed before exact client construction and connection", () => {
  const order = [];
  let replacement;
  const context = harness({
    order,
    replaceState: (state, url) => { replacement = { state, url }; },
    connect: () => { order.push("connect"); return Promise.resolve(); },
  });
  context.controller.start();
  assert.deepEqual(order, ["scrub", "create", "connect"]);
  assert.deepEqual(replacement, { state: context.historyState, url: "/factory?preview=1" });
  assert.equal(context.clientOptions().url, "ws://127.0.0.1:43123/browser/v1");
  assert.equal(context.clientOptions().host, "127.0.0.1:43123");
  assert.equal(context.clientOptions().origin, "https://app.darkfactory.build");
  assert.equal(context.clientOptions().challenge, challenge);
});

test("a failed fragment scrub creates no client or browser effect", () => {
  let clients = 0;
  const snapshots = [];
  const controller = new FactoryAppController({
    origin: "https://app.darkfactory.build",
    location: { hash: `#df_pair=${challenge}`, pathname: "/", search: "" },
    history: { state: null, replaceState: () => { throw new Error("history unavailable"); } },
    onChange: (snapshot) => snapshots.push(snapshot),
    clientFactory: () => { clients += 1; throw new Error("must not construct"); },
  });
  controller.start();
  assert.equal(clients, 0);
  const snapshot = snapshots.at(-1);
  assert.equal(snapshot.status, "closed");
  assert.equal(snapshot.canRetry, false);
  assert.equal(snapshot.error.code, "connection");
  assert.equal(snapshot.error.retryable, false);
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { ...snapshot, onRetry: () => {} }));
  assert.equal(markup.includes("RETRY CONNECTION"), false);
});

test("a failed client construction is closed and cannot advertise retry", () => {
  const snapshots = [];
  const controller = new FactoryAppController({
    origin: "https://app.darkfactory.build",
    location: { hash: "", pathname: "/", search: "" },
    history: { state: null, replaceState: () => {} },
    onChange: (snapshot) => snapshots.push(snapshot),
    clientFactory: () => { throw new Error("construction failed"); },
  });
  controller.start();
  const snapshot = snapshots.at(-1);
  assert.equal(snapshot.status, "closed");
  assert.equal(snapshot.canRetry, false);
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { ...snapshot, onRetry: () => {} }));
  assert.equal(markup.includes("RETRY CONNECTION"), false);
});

test("factory construction cannot retain a client after a reentrant close", () => {
  const snapshots = [];
  let controller;
  let callbacks;
  let connects = 0;
  let closes = 0;
  controller = new FactoryAppController({
    origin: "https://app.darkfactory.build",
    location: { hash: "", pathname: "/", search: "" },
    history: { state: null, replaceState: () => {} },
    onChange: (snapshot) => {
      snapshots.push(snapshot);
      controller.close();
    },
    clientFactory: (options) => {
      callbacks = options;
      options.onStatus("ready");
      return {
        session: {},
        connect: () => { connects += 1; return Promise.resolve(); },
        close: () => {
          closes += 1;
          options.onStatus("connecting");
          options.onState(fixtureState);
        },
      };
    },
  });

  controller.start();
  const published = snapshots.length;
  callbacks.onStatus("closed");
  callbacks.onState(fixtureState);
  assert.equal(connects, 0);
  assert.equal(closes, 1);
  assert.equal(snapshots.length, published);
});

test("a reentrant close during connect closes the exact installed client once", () => {
  const snapshots = [];
  let controller;
  let callbacks;
  let connects = 0;
  let closes = 0;
  controller = new FactoryAppController({
    origin: "https://app.darkfactory.build",
    location: { hash: "", pathname: "/", search: "" },
    history: { state: null, replaceState: () => {} },
    onChange: (snapshot) => {
      snapshots.push(snapshot);
      if (snapshot.status === "ready") controller.close();
    },
    clientFactory: (options) => {
      callbacks = options;
      return {
        session: {},
        connect: () => {
          connects += 1;
          options.onStatus("ready");
          return Promise.resolve();
        },
        close: () => { closes += 1; options.onStatus("connecting"); },
      };
    },
  });

  controller.start();
  const published = snapshots.length;
  callbacks.onStatus("closed");
  assert.equal(connects, 1);
  assert.equal(closes, 1);
  assert.equal(snapshots.length, published);
});

test("current callbacks publish while close is once-only and fences synchronous or late callbacks", () => {
  let context;
  context = harness({
    close: () => {
      context.emitStatus("connecting");
      context.emitState(fixtureState);
      context.emitError(new SessionError("connection", true));
    },
  });
  context.controller.start();
  context.emitStatus("ready");
  assert.equal(context.latest().status, "ready");
  const beforeClose = context.snapshots.length;
  context.controller.close();
  context.controller.close();
  context.emitStatus("closed");
  context.emitState(fixtureState);
  assert.equal(context.calls.close, 1);
  assert.equal(context.snapshots.length, beforeClose);
});

test("retry reuses the BrowserClient recovery owner and clears transient state", () => {
  const context = harness();
  context.controller.start();
  context.emitError(new SessionError("connection", true));
  assert.equal(context.latest().canRetry, true);
  context.controller.retry();
  assert.equal(context.calls.connect, 2);
  assert.equal(context.latest().error, undefined);
});

test("detail selection is exact, unique, view-only, and reconnect clears private state", async () => {
  const pending = deferred();
  let detailRequest;
  let detailRequests = 0;
  let replies = 0;
  let cancels = 0;
  const context = harness({
    getDetail: (value) => { detailRequests += 1; detailRequest = value; return pending.promise; },
    reply: async () => { replies += 1; },
    cancel: async () => { cancels += 1; },
  });
  context.controller.start();
  context.emitState(fixtureState);
  context.emitStatus("ready");
  const selected = context.controller.selectHumanRequest(request);
  await context.controller.selectHumanRequest(request);
  assert.equal(detailRequests, 1);
  assert.deepEqual(detailRequest, { requestId: request.id, expectedRevision: request.revision });
  assert.equal(replies, 0);
  assert.equal(cancels, 0);
  assert.equal(context.latest().selectedHumanRequest.phase, "loading");
  const detail = detailFor();
  pending.resolve(detail);
  await selected;
  assert.equal(context.latest().selectedHumanRequest.question, detail.question);
  assert.equal(context.latest().selectedHumanRequest.canCancel, true);
  context.emitStatus("connecting");
  assert.equal(context.latest().selectedHumanRequest, undefined);
});

test("reply and cancel each consume only their exact returned authority once", async () => {
  const replyDone = deferred();
  const cancelDone = deferred();
  const replyCalls = [];
  const cancelCalls = [];
  let nextDetail = detailFor();
  const context = harness({
    getDetail: async () => nextDetail,
    reply: (detail, value) => { replyCalls.push([detail, value]); return replyDone.promise; },
    cancel: (descriptor) => { cancelCalls.push(descriptor); return cancelDone.promise; },
  });
  context.controller.start();
  context.emitState(fixtureState);
  context.emitStatus("ready");

  await context.controller.selectHumanRequest(request);
  context.controller.setHumanReply("Continue with the simpler path.");
  const firstReply = context.controller.replyHumanRequest();
  const duplicateReply = context.controller.replyHumanRequest();
  assert.deepEqual(replyCalls, [[nextDetail, "Continue with the simpler path."]]);
  replyDone.resolve({ status: "resolved" });
  await Promise.all([firstReply, duplicateReply]);
  assert.equal(context.latest().selectedHumanRequest, undefined);

  nextDetail = detailFor(request, " after review");
  await context.controller.selectHumanRequest(request);
  const firstCancel = context.controller.cancelHumanRequest();
  const duplicateCancel = context.controller.cancelHumanRequest();
  assert.deepEqual(cancelCalls, [nextDetail.cancelRun]);
  cancelDone.resolve({ request_id: request.id });
  await Promise.all([firstCancel, duplicateCancel]);
  assert.equal(context.latest().selectedHumanRequest, undefined);
});

test("reply drafts obey the exact daemon byte bound before retention or delivery", async () => {
  const replies = [];
  const boundedDetail = detailFor(request, "", 8);
  const context = harness({
    getDetail: async () => boundedDetail,
    reply: async (detail, value) => { replies.push([detail, value]); },
  });
  context.controller.start();
  context.emitState(fixtureState);
  context.emitStatus("ready");
  await context.controller.selectHumanRequest(request);

  context.controller.setHumanReply("ok");
  assert.equal(context.latest().selectedHumanRequest.reply, "ok");

  const OriginalTextEncoder = globalThis.TextEncoder;
  let encoded = 0;
  globalThis.TextEncoder = class {
    encode() { encoded += 1; throw new Error("oversized strings must not be encoded"); }
  };
  try {
    context.controller.setHumanReply("x".repeat(1_000_000));
  } finally {
    globalThis.TextEncoder = OriginalTextEncoder;
  }
  assert.equal(encoded, 0);
  assert.equal(context.latest().error.code, "too_large");
  assert.equal(context.latest().selectedHumanRequest.reply, "ok");

  context.controller.setHumanReply("😀😀");
  assert.equal(context.latest().selectedHumanRequest.reply, "😀😀");
  context.controller.setHumanReply("😀😀😀");
  assert.equal(context.latest().error.code, "too_large");
  assert.equal(context.latest().selectedHumanRequest.reply, "😀😀");

  await context.controller.replyHumanRequest();
  assert.deepEqual(replies, [[boundedDetail, "😀😀"]]);
});

test("an empty reply is refused without consuming its exact request authority", async () => {
  let replies = 0;
  const context = harness({ reply: async () => { replies += 1; } });
  context.controller.start();
  context.emitState(fixtureState);
  context.emitStatus("ready");
  await context.controller.selectHumanRequest(request);

  await context.controller.replyHumanRequest();
  assert.equal(replies, 0);
  assert.equal(context.latest().error.code, "invalid_request");
  assert.equal(context.latest().selectedHumanRequest.phase, "ready");
});

test("deletion or revision change clears detail and fences a late private response", async () => {
  const pending = deferred();
  let getDetail = () => pending.promise;
  const context = harness({ getDetail: (value) => getDetail(value) });
  context.controller.start();
  context.emitState(fixtureState);
  context.emitStatus("ready");
  const selection = context.controller.selectHumanRequest(request);
  context.emitState(stateWithRequests([]));
  assert.equal(context.latest().selectedHumanRequest, undefined);
  pending.resolve(detailFor());
  await selection;
  assert.equal(context.latest().selectedHumanRequest, undefined);

  const revised = { ...request, revision: request.revision + 1n };
  getDetail = async () => detailFor(revised);
  context.emitState(stateWithRequests([revised]));
  await context.controller.selectHumanRequest(revised);
  assert.equal(context.latest().selectedHumanRequest.question, detailFor().question);
  context.emitState(stateWithRequests([{ ...revised, revision: revised.revision + 1n }]));
  assert.equal(context.latest().selectedHumanRequest, undefined);
});
