import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  BROWSER_MANIFEST,
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
  hexBytes,
  verifyP256Signature,
} from "../dist/src/index.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixture = (name) => readFileSync(join(root, "protocol/browser/v1/fixtures", name), "utf8").trim();
const json = (name) => JSON.parse(fixture(name));
const bytes = (hex) => hexBytes(hex);
const expectMalformed = (fn) => assert.throws(fn, (e) => ["malformed", "wrong_direction", "unsupported_version"].includes(e?.code));

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
  expectMalformed(() => encodeTerminalOutput(session, 1n, new Uint8Array(65537)));
});

test("manifest has exactly one public mapping for every v1 stable entry", () => {
  const source = json("../manifest.json");
  assert.deepEqual(Object.keys(BROWSER_MANIFEST.capabilities), source.capabilities.map((x) => x.name));
  assert.deepEqual(Object.values(BROWSER_MANIFEST.capabilities), source.capabilities.map((x) => x.value));
  assert.deepEqual(BROWSER_MANIFEST.control, source.control);
  assert.equal(BROWSER_MANIFEST.terminal.headerBytes, source.terminal.header_bytes);
  assert.equal(BROWSER_MANIFEST.terminal.maxPayloadBytes, source.terminal.max_payload_bytes);
  assert.deepEqual(Object.keys(BROWSER_MANIFEST.terminal.opcodes), source.terminal.opcodes.map((x) => x.name));
  assert.deepEqual(Object.values(BROWSER_MANIFEST.terminal.opcodes), source.terminal.opcodes.map((x) => x.value));
});
