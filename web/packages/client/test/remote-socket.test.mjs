import assert from "node:assert/strict";
import test from "node:test";
import {
  CAPABILITIES,
  base64urlDecode,
  base64urlEncode,
  createRelaySocketFactory,
  encodeAuthResult,
  encodePairResult,
  encodeStateChanged,
  RELAY_SUBPROTOCOL,
} from "../dist/src/index.js";
import { ALL_CAPABILITIES, FakeFactory, FakeRelay, RELAY_ORIGIN, bytes, mintTicket, nodeId, settle } from "./remote-fake.mjs";

const node = nodeId("a");
const daemonId = "22".repeat(16);
const clientId = "55".repeat(16);
/** The domain the relay verifies under, stated here so a change to it fails. */
const signedBytes = (payload) => new TextEncoder().encode(`dark-factory-relay/proof\n${payload}`);

function harness(options = {}) {
  const relay = new FakeRelay();
  const factory = relay.add(new FakeFactory({ node, daemonId, clientId, withTicket: false, ...options.factory }));
  const messages = [];
  const closes = [];
  const relayCloses = [];
  const tickets = [];
  const errors = [];
  const { ticket = mintTicket({ node, purpose: "pair" }), ...overrides } = options.socket ?? {};
  const socketFactory = createRelaySocketFactory({
    relayOrigin: RELAY_ORIGIN,
    nodeId: node,
    ticket: () => ticket,
    expectedDaemonId: daemonId,
    webSocket: relay.WebSocket,
    onTicket: (value) => tickets.push(value),
    onRelayClose: (code, opened, local) => relayCloses.push([code, opened, local]),
    ...overrides,
  });
  const socket = socketFactory(`${RELAY_ORIGIN}/controller/${node}`);
  socket.onmessage = (event) => messages.push(event.data);
  socket.onclose = (event) => closes.push(event);
  socket.onerror = (event) => errors.push(event);
  return { relay, factory, socket, messages, closes, relayCloses, tickets, errors };
}

test("a control dial offers exactly the subprotocol, the ticket and a fresh proof", async () => {
  const keys = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
  const ticket = mintTicket({ node, purpose: "control", ticketId: base64urlEncode(bytes(16, 5)) });
  const { relay, socket, messages } = harness({ socket: { ticket, key: keys.privateKey } });
  assert.equal(socket.readyState, 0, "a control dial reports CONNECTING while it signs");
  await settle();
  const inner = relay.for(node)[0];
  assert.equal(inner.url, `${RELAY_ORIGIN}/controller/${node}`);
  assert.equal(inner.binaryType, "arraybuffer");
  assert.equal(inner.protocols.length, 3);
  assert.equal(inner.protocols[0], RELAY_SUBPROTOCOL);
  assert.equal(inner.protocols[1], ticket);
  const [payload, signature] = inner.protocols[2].split(".");
  assert.equal(await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, keys.publicKey, base64urlDecode(signature), signedBytes(payload)), true);
  assert.equal(JSON.parse(new TextDecoder().decode(base64urlDecode(payload))).ticket, base64urlEncode(bytes(16, 5)));
  assert.equal(socket.readyState, 1);
  assert.equal(messages.length, 1, "HELLO from the bound daemon is delivered");
  socket.close();
  assert.equal(socket.readyState, 3);
  assert.deepEqual(inner.closes, [{ code: 1000, reason: "" }]);
});

test("a pair dial offers no proof and needs no device key", async () => {
  const ticket = mintTicket({ node, purpose: "pair" });
  const { relay } = harness({ socket: { ticket } });
  await settle();
  assert.deepEqual(relay.for(node)[0].protocols, [RELAY_SUBPROTOCOL, ticket]);
});

test("a control dial without a device key never reaches the relay", async () => {
  const { relay, closes, errors, relayCloses } = harness({ socket: { ticket: mintTicket({ node, purpose: "control" }) } });
  await settle();
  assert.equal(relay.for(node).length, 0);
  assert.equal(closes.length, 1);
  assert.equal(errors.length, 1);
  assert.deepEqual(relayCloses, [[1006, false, true]], "a dial that never opened is reported as one");
});

test("a HELLO from another daemon closes with 4900 and is never delivered", async () => {
  const { socket, messages, closes, relayCloses, errors } = harness({ factory: { helloDaemonId: "99".repeat(16) } });
  await settle();
  assert.deepEqual(messages, [], "a foreign daemon's HELLO must not seed a transcript");
  assert.equal(closes.length, 1);
  assert.equal(closes[0].code, 4900);
  assert.deepEqual(relayCloses, [[4900, true, true]], "this browser refused it; a peer's 4900 would not be local");
  assert.equal(errors.length, 1);
  assert.equal(socket.readyState, 3);
});

test("HELLO passes byte for byte when the daemon is the bound one", async () => {
  const { relay, messages } = harness();
  await settle();
  assert.equal(messages.length, 1);
  assert.equal(JSON.parse(messages[0]).body.daemon_id, daemonId);
  assert.equal(relay.for(node)[0].readyState, 1);
});

test("a RELAY_TICKET frame is consumed and the result frame is delivered untouched", async () => {
  const { relay, messages, tickets } = harness();
  await settle();
  const inner = relay.for(node)[0];
  const control = mintTicket({ node, purpose: "control" });
  const cases = [
    encodePairResult("pair-1", { client_id: clientId, capabilities: ALL_CAPABILITIES }),
    encodeAuthResult("auth-1", { client_id: clientId, capabilities: CAPABILITIES.observe }),
  ];
  for (const wire of cases) {
    inner.emit(wire);
    inner.emit(JSON.stringify({ type: "RELAY_TICKET", ticket: control }));
    await settle(2);
    assert.equal(messages.at(-1), wire, "the session sees the exact bytes the daemon sent");
  }
  assert.deepEqual(tickets, [control, control]);
  assert.equal(messages.length, 3, "HELLO and the two results; routing is never delivered");
});

test("a RELAY_TICKET frame without a string ticket is dropped and calls nothing", async () => {
  const { relay, messages, tickets } = harness();
  await settle();
  const inner = relay.for(node)[0];
  const before = messages.length;
  for (const wire of ['{"type":"RELAY_TICKET"}', '{"type":"RELAY_TICKET","ticket":7}', '{"type":"RELAY_TICKET","ticket":null}']) {
    inner.emit(wire);
    await settle(2);
  }
  assert.deepEqual(tickets, []);
  assert.equal(messages.length, before, "a routing frame never reaches the session, good or bad");
});

test("frames the wrapper has no business reading pass through unchanged", async () => {
  const { relay, messages, tickets } = harness();
  await settle();
  const inner = relay.for(node)[0];
  const changed = encodeStateChanged("watch-1", { head: 7n });
  const binary = new Uint8Array([1, 2, 3]);
  const malformed = '{"type":"HELLO","body":';
  const foreign = `{"type":"STATE_CHANGED","id":"watch-1","body":{"head":"8","relay_ticket":"x"}}`;
  for (const value of [changed, binary, malformed, foreign]) {
    inner.emit(value);
    await settle(2);
    assert.equal(messages.at(-1), value);
  }
  assert.deepEqual(tickets, [], "only the frames that prove a connection carry routing");
});

test("a relay close code reaches the manager before the session sees the close", async () => {
  const order = [];
  const local = [];
  const relay = new FakeRelay();
  const factory = relay.add(new FakeFactory({ node, daemonId, withTicket: false }));
  const socketFactory = createRelaySocketFactory({
    relayOrigin: RELAY_ORIGIN,
    nodeId: node,
    ticket: () => mintTicket({ node, purpose: "pair" }),
    expectedDaemonId: daemonId,
    webSocket: relay.WebSocket,
    onRelayClose: (code, _opened, isLocal) => { order.push(`relay:${code}`); local.push(isLocal); },
  });
  const socket = socketFactory(`${RELAY_ORIGIN}/controller/${node}`);
  socket.onclose = (event) => order.push(`session:${event.code}`);
  await settle();
  factory.dropAll(4001, "offline");
  await settle();
  assert.deepEqual(order, ["relay:4001", "session:4001"]);
  assert.deepEqual(local, [false], "a close the relay sent is never reported as this side's refusal");
  assert.equal(socket.readyState, 3);
});
