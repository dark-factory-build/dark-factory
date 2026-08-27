import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  BROWSER_MANIFEST,
  ProtocolError,
  buildAuthTranscript,
  buildPairTranscript,
  decodeClientControl,
  decodeServerControl,
  decodeTerminalInput,
  decodeTerminalOutput,
  encodeClientControl,
  encodeServerControl,
  encodeTerminalInput,
  encodeTerminalOutput,
  MAX_TERMINAL_COLS,
  MAX_TERMINAL_ROWS,
  MAX_TERMINAL_UNACKED_BYTES,
  hexBytes,
  MAX_TERMINAL_PAYLOAD,
  verifyP256Signature,
} from "../dist/src/index.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixture = (name) => readFileSync(join(root, "protocol/browser/v1/fixtures", name), "utf8").trim();
const json = (name) => JSON.parse(fixture(name));
const bytes = (hex) => hexBytes(hex);
const expectMalformed = (fn) => assert.throws(fn, (e) => e instanceof ProtocolError && ["malformed", "wrong_direction", "unsupported_version"].includes(e.code));

test("canonical control fixtures decode by sender role and re-encode exactly", () => {
  const client = ["pair_prove", "auth_prove"];
  const server = ["hello", "pair_result", "auth_result"];
  for (const name of client) assert.equal(encodeClientControl(decodeClientControl(fixture(`${name}.json`))), fixture(`${name}.json`));
  for (const name of server) assert.equal(encodeServerControl(decodeServerControl(fixture(`${name}.json`))), fixture(`${name}.json`));
  const error = fixture("error.json");
  assert.equal(encodeClientControl(decodeClientControl(error)), error);
  assert.equal(encodeServerControl(decodeServerControl(error)), error);
});

test("control role, envelope, field and capability validation is closed", () => {
  expectMalformed(() => decodeClientControl(fixture("hello.json")));
  expectMalformed(() => decodeServerControl(fixture("pair_prove.json")));
  for (const mutation of [
    (s) => s.replace('"v":1', '"v":2'),
    (s) => s.replace('"type":"AUTH_RESULT"', '"type":"NOPE"'),
    (s) => s.replace('"client_id":', '"extra":1,"client_id":'),
    (s) => s.replace('"client_id":"', '"client_id":"0'),
    (s) => s.replace('"capabilities":9', '"capabilities":8'),
    (s) => s.replace('"capabilities":9', '"capabilities":16'),
    (s) => s.replace('{"v":1', '{"v":1,"v":1'),
    (s) => s.replace('"id":"auth-1"', '"id":"auth-1","id":"other"'),
  ]) expectMalformed(() => decodeServerControl(mutation(fixture("auth_result.json"))));
  expectMalformed(() => decodeClientControl('{"v":1,"type":"ERROR","body":{"code":"secret","retryable":false}}'));
  expectMalformed(() => decodeClientControl('{"v":1,"type":"ERROR","body":{"code":"internal","retryable":false,"message":"private"}}'));
  expectMalformed(() => decodeClientControl(fixture("auth_prove.json").replace('"signature"', '"public_key_sec1":"046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5","signature"')));
  for (const number of ["1.0", "1e0", "01", "+1", "9007199254740992", "-9007199254740992"]) {
    expectMalformed(() => decodeServerControl(`{"v":1,"type":"AUTH_RESULT","id":"auth-1","body":{"client_id":"606162636465666768696a6b6c6d6e6f","capabilities":${number}}}`));
  }
  assert.equal(decodeServerControl('{"v":1,"type":"AUTH_RESULT","id":"auth-1","body":{"client_id":"606162636465666768696a6b6c6d6e6f","capabilities":1} }').body.capabilities, 1);
});

test("browser terminal and HumanRequest controls are typed, directional, and bounded", () => {
  const client = ["human_request_reply", "human_request_cancel_run", "terminal_attach", "terminal_ack", "terminal_lease_acquire", "terminal_lease_renew", "terminal_lease_release", "terminal_resize", "terminal_detach"];
  const server = ["human_request_reply_result", "human_request_action_result", "terminal_attached", "terminal_lease_result", "terminal_resized", "terminal_detached", "terminal_input_result", "terminal_eof", "terminal_exit", "terminal_reset"];
  for (const name of client) {
    const frame = decodeClientControl(fixture(`${name}.json`));
    assert.equal(typeof frame.body, "object");
    assert.equal(encodeClientControl(frame), fixture(`${name}.json`));
    expectMalformed(() => decodeServerControl(fixture(`${name}.json`)));
  }
  for (const name of server) {
    const frame = decodeServerControl(fixture(`${name}.json`));
    assert.equal(typeof frame.body, "object");
    assert.equal(encodeServerControl(frame), fixture(`${name}.json`));
    expectMalformed(() => decodeClientControl(fixture(`${name}.json`)));
  }
  const ack = fixture("terminal_ack.json");
  expectMalformed(() => decodeClientControl(ack.replace('"body"', '"id":"ack","body"')));
  expectMalformed(() => decodeClientControl(ack.replace('"next_sequence":"1"', '"next_sequence":"0"')));
  const attached = fixture("terminal_attached.json");
  assert.equal(decodeServerControl(attached).body.max_unacked_bytes, BigInt(MAX_TERMINAL_UNACKED_BYTES));
  expectMalformed(() => decodeServerControl(attached.replace('"max_unacked_bytes":"65536"', '"max_unacked_bytes":"65535"')));
  for (const [name, role, field, value] of [["terminal_resize", "client", "rows", MAX_TERMINAL_ROWS], ["terminal_resize", "client", "cols", MAX_TERMINAL_COLS], ["terminal_resized", "server", "rows", MAX_TERMINAL_ROWS], ["terminal_resized", "server", "cols", MAX_TERMINAL_COLS]]) {
    const raw = fixture(`${name}.json`);
    const decode = role === "client" ? decodeClientControl : decodeServerControl;
    const atMaximum = raw.replace(new RegExp(`"${field}":\\d+`), `"${field}":${value}`);
    assert.doesNotThrow(() => decode(atMaximum));
    expectMalformed(() => decode(atMaximum.replace(`"${field}":${value}`, `"${field}":${value + 1}`)));
  }
  for (const escape of ["\\ud800", "\\udc00", "\\udc00\\ud800", "\\ud800\\u0041"]) {
    expectMalformed(() => decodeClientControl(fixture("human_request_reply.json").replace('"reply":"ok"', `"reply":"${escape}"`)));
  }
  for (const literal of ["\ud800", "\udc00"]) {
    expectMalformed(() => decodeClientControl(fixture("human_request_reply.json").replace('"reply":"ok"', `"reply":"${literal}"`)));
  }
  expectMalformed(() => decodeClientControl(fixture("human_request_reply.json").replace('"reply":"ok"', `"reply":"${"x".repeat(8193)}"`)));
  const pairedReply = decodeClientControl(fixture("human_request_reply.json").replace('"reply":"ok"', '"reply":"\\ud83d\\ude00"'));
  assert.equal(pairedReply.body.reply, "😀");
  const lease = fixture("terminal_lease_result.json");
  expectMalformed(() => decodeServerControl(lease.replace('"expires_at_ms":"10000"', '"expires_at_ms":null')));
  expectMalformed(() => decodeServerControl(lease.replace(',"expires_at_ms":"10000"', '')));
  expectMalformed(() => decodeServerControl(lease.replace('"expires_at_ms":"10000"', '"expires_at_ms":"0"')));
  const released = lease.replace('"operation":"acquired"', '"operation":"released"').replace(',"expires_at_ms":"10000"', '');
  assert.doesNotThrow(() => decodeServerControl(released));
  expectMalformed(() => decodeServerControl(released.replace('"operation":"released"', '"operation":"released","expires_at_ms":null')));
  for (const field of ["exit_code", "exit_signal", "aborted"]) {
    const exit = fixture("terminal_exit.json").replace(new RegExp(`"${field}":(?:0|false)`), `"${field}":null`);
    expectMalformed(() => decodeServerControl(exit));
  }
});

test("state restart accepts only the canonical empty chronology", () => {
  const restart = (head, floor) => `{"v":1,"type":"STATE_RESTART","id":"empty","body":{"head":"${head}","floor":"${floor}","reason":"gap"}}`;
  const empty = decodeServerControl(restart(0, 1));
  assert.equal(empty.body.head, 0n);
  assert.equal(empty.body.floor, 1n);
  assert.equal(encodeServerControl(empty), restart(0, 1));
  for (const [head, floor] of [[0, 0], [0, 2], [2, 3]]) {
    expectMalformed(() => decodeServerControl(restart(head, floor)));
  }
});

test("pair and auth transcripts are byte-exact and verify with WebCrypto P-1363", async () => {
  const value = json("transcript_v1.json");
  const pair = value.pair;
  const pairTranscript = buildPairTranscript(pair);
  assert.equal(Buffer.from(pairTranscript).toString("hex"), pair.transcript);
  assert.equal(await verifyP256Signature(bytes(pair.public_key_sec1), bytes(pair.signature), pairTranscript), true);
  const auth = value.auth;
  const authTranscript = buildAuthTranscript(auth);
  assert.equal(Buffer.from(authTranscript).toString("hex"), auth.transcript);
  assert.equal(await verifyP256Signature(bytes(auth.public_key_sec1), bytes(auth.signature), authTranscript), true);
  const altered = authTranscript.slice(); altered[altered.length - 1] ^= 1;
  assert.equal(await verifyP256Signature(bytes(auth.public_key_sec1), bytes(auth.signature), altered), false);
});

test("transcript fixed fields and text are validated before signing", () => {
  const pair = json("transcript_v1.json").pair;
  for (const field of ["daemon_id", "boot_id", "connection_nonce", "challenge", "public_key_sec1"]) {
    const altered = { ...pair, [field]: pair[field].slice(2) };
    expectMalformed(() => buildPairTranscript(altered));
  }
  expectMalformed(() => buildPairTranscript({ ...pair, host: "" }));
  expectMalformed(() => buildPairTranscript({ ...pair, origin: "https://x\u0000.example" }));
  expectMalformed(() => hexBytes("AA"));
  expectMalformed(() => buildPairTranscript(null));
  expectMalformed(() => buildPairTranscript({ ...pair, host: "https://bad\ud800" }));
  expectMalformed(() => buildPairTranscript({ ...pair, origin: "https://bad\udfff" }));
});

test("transcript preserves astral Unicode as UTF-8 and rejects only lone surrogates", () => {
  const pair = json("transcript_v1.json").pair;
  const host = `${pair.host}🚀`;
  const origin = `${pair.origin}🌟`;
  const actual = buildPairTranscript({ ...pair, host, origin });
  const expected = [];
  const add = (value) => { const field = typeof value === "string" ? new TextEncoder().encode(value) : value; expected.push(new Uint8Array([field.length >>> 24, field.length >>> 16 & 255, field.length >>> 8 & 255, field.length & 255]), field); };
  expected.push(new TextEncoder().encode("dark-factory/browser/v1/pair\0"), new Uint8Array([0, 1]));
  for (const field of [pair.daemon_id, pair.boot_id, pair.connection_nonce, pair.challenge, pair.public_key_sec1]) add(bytes(field));
  add(host); add(origin);
  const joined = new Uint8Array(expected.reduce((n, field) => n + field.length, 0)); let offset = 0;
  for (const field of expected) { joined.set(field, offset); offset += field.length; }
  assert.deepEqual(actual, joined);
});

test("terminal fixtures round-trip and preserve 64-bit values as bigint", () => {
  const session = bytes("101112131415161718191a1b1c1d1e1f");
  for (const [name, decode, encode] of [["terminal_input", decodeTerminalInput, (f) => encodeTerminalInput(f.sessionId, f.sequence, f.leaseGeneration, f.payload)], ["terminal_output", decodeTerminalOutput, (f) => encodeTerminalOutput(f.sessionId, f.sequence, f.payload)]]) {
    const raw = bytes(fixture(`${name}.hex`));
    const frame = decode(raw);
    assert.equal(Buffer.from(encode(frame)).toString("hex"), Buffer.from(raw).toString("hex"));
    assert.equal(typeof frame.sequence, "bigint");
    assert.deepEqual(frame.sessionId, session);
  }
  const high = encodeTerminalOutput(session, 0x0020_0000_0000_0001n, new Uint8Array([1]));
  assert.equal(decodeTerminalOutput(high).sequence, 0x0020_0000_0000_0001n);
});

test("terminal framing rejects wrong direction, identity, bounds and overflow", () => {
  const session = bytes("101112131415161718191a1b1c1d1e1f");
  const input = encodeTerminalInput(session, 7n, 5n, new Uint8Array([1, 2]));
  const output = encodeTerminalOutput(session, 7n, new Uint8Array([1, 2]));
  expectMalformed(() => decodeTerminalOutput(input));
  expectMalformed(() => decodeTerminalInput(output));
  for (const mutate of [
    (b) => { b[0] = 0; }, (b) => { b[2] = 2; }, (b) => { b[3] = 9; },
    (b) => { b[4] = 0; b[5] = 0; b[6] = 0; b[7] = 0; b[8] = 0; b[9] = 0; b[10] = 0; b[11] = 0; b[12] = 0; b[13] = 0; b[14] = 0; b[15] = 0; b[16] = 0; b[17] = 0; b[18] = 0; b[19] = 0; },
    (b) => { b[39] = 0; }, (b) => { b[36] = 0xff; b[37] = 0xff; b[38] = 0xff; b[39] = 0xff; },
  ]) { const copy = input.slice(); mutate(copy); expectMalformed(() => decodeTerminalInput(copy)); }
  expectMalformed(() => encodeTerminalInput(session, 0n, 1n, new Uint8Array([1])));
  expectMalformed(() => encodeTerminalInput(session, 1n, 0n, new Uint8Array([1])));
  expectMalformed(() => encodeTerminalOutput(session, 0xffff_ffff_ffff_ffffn, new Uint8Array([1, 2])));
  const maxEnd = encodeTerminalOutput(session, 0xffff_ffff_ffff_ffffn - 1n, new Uint8Array([1]));
  assert.equal(decodeTerminalOutput(maxEnd).sequence, 0xffff_ffff_ffff_ffffn - 1n);
  expectMalformed(() => encodeTerminalOutput(session, 0xffff_ffff_ffff_ffffn, new Uint8Array([1])));
  expectMalformed(() => encodeTerminalOutput(session, 1n, new Uint8Array(MAX_TERMINAL_PAYLOAD + 1)));
  expectMalformed(() => decodeTerminalInput(null));
  expectMalformed(() => encodeTerminalInput(null, 1n, 1n, new Uint8Array([1])));
  expectMalformed(() => encodeTerminalInput(session, 1, 1n, new Uint8Array([1])));
  expectMalformed(() => encodeTerminalInput(session, 1n, 1n, null));
});

test("terminal payload boundary matches the runner limit", () => {
  const session = bytes("101112131415161718191a1b1c1d1e1f");
  for (const [encode, decode] of [[encodeTerminalInput, decodeTerminalInput], [encodeTerminalOutput, decodeTerminalOutput]]) {
    const frame = encode === encodeTerminalInput
      ? encode(session, 1n, 1n, new Uint8Array(MAX_TERMINAL_PAYLOAD))
      : encode(session, 1n, new Uint8Array(MAX_TERMINAL_PAYLOAD));
    assert.equal(decode(frame).payload.length, MAX_TERMINAL_PAYLOAD);
  }
  expectMalformed(() => encodeTerminalInput(session, 1n, 1n, new Uint8Array(MAX_TERMINAL_PAYLOAD + 1)));
  expectMalformed(() => encodeTerminalOutput(session, 1n, new Uint8Array(MAX_TERMINAL_PAYLOAD + 1)));
});

test("all public malformed boundaries return the finite ProtocolError", async () => {
  await assert.rejects(() => verifyP256Signature(null, new Uint8Array(64), new Uint8Array(1)), (error) => error instanceof ProtocolError && error.code === "malformed");
  expectMalformed(() => decodeServerControl(Symbol("attacker")));
  expectMalformed(() => decodeServerControl("{"));
  expectMalformed(() => hexBytes({}));
});

test("manifest has exactly one public mapping for every v1 stable entry", () => {
  const source = json("../manifest.json");
  assert.deepEqual(Object.keys(BROWSER_MANIFEST.capabilities), source.capabilities.map((x) => x.name));
  assert.deepEqual(Object.values(BROWSER_MANIFEST.capabilities), source.capabilities.map((x) => x.value));
  const camel = (key) => key.split("_").map((word, index) => index === 0 ? word : ({ json: "JSON", sqlite: "SQLite" }[word] ?? `${word[0].toUpperCase()}${word.slice(1)}`)).join("");
  const bounds = Object.fromEntries(Object.entries(source.bounds).map(([key, value]) => [camel(key), key === "max_sqlite_integer" ? BigInt(value) : value]));
  assert.deepEqual(BROWSER_MANIFEST.bounds, bounds);
  for (const key of Object.keys(bounds)) {
    const mutated = { ...bounds };
    delete mutated[key];
    assert.notDeepEqual(mutated, BROWSER_MANIFEST.bounds);
    const changed = { ...bounds, [key]: typeof bounds[key] === "bigint" ? bounds[key] + 1n : bounds[key] + 1 };
    assert.notDeepEqual(changed, BROWSER_MANIFEST.bounds);
  }
  assert.deepEqual(BROWSER_MANIFEST.control, source.control);
  assert.equal(BROWSER_MANIFEST.terminal.headerBytes, source.terminal.header_bytes);
  assert.equal(BROWSER_MANIFEST.terminal.maxPayloadBytes, source.terminal.max_payload_bytes);
  assert.deepEqual(Object.keys(BROWSER_MANIFEST.terminal.opcodes), source.terminal.opcodes.map((x) => x.name));
  assert.deepEqual(Object.values(BROWSER_MANIFEST.terminal.opcodes), source.terminal.opcodes.map((x) => x.value));
});
