import assert from "node:assert/strict";
import test from "node:test";
import {
  SessionError,
  base64urlDecode,
  base64urlEncode,
  mintProof,
  parseTicket,
} from "../dist/src/index.js";
import { base64url, bytes, mintTicket, nodeId } from "./remote-fake.mjs";

const node = nodeId("a");
const expectInvalid = (fn) => assert.throws(fn, (error) => error instanceof SessionError && error.code === "invalid_request");
/** The domain the relay verifies under, stated here so a change to it fails. */
const signedBytes = (payload) => new TextEncoder().encode(`dark-factory-relay/proof\n${payload}`);

async function deviceKey() {
  return crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
}

test("base64url round-trips every length and refuses noncanonical text", () => {
  for (let length = 0; length <= 66; length++) {
    const value = new Uint8Array(length).map((_, index) => (index * 37 + length) & 0xff);
    const text = base64urlEncode(value);
    assert.match(text, /^[A-Za-z0-9_-]*$/, `length ${length}`);
    assert.deepEqual(base64urlDecode(text), value, `length ${length}`);
  }
  for (const text of ["A", "AAAAA", "AA==", "A+/A", "AAAA.", "éAAA"]) expectInvalid(() => base64urlDecode(text));
  // The unused low bits of a final character must be zero, so one encoding
  // exists for each byte string and a token cannot be silently rewritten.
  assert.deepEqual(base64urlDecode("AQ"), new Uint8Array([1]));
  expectInvalid(() => base64urlDecode("AR"));
});

test("a proof carries a fresh nonce and verifies over the exact domain prefix", async () => {
  const keys = await deviceKey();
  const ticketId = base64urlEncode(bytes(16, 9));
  const proof = await mintProof(ticketId, keys.privateKey);
  const parts = proof.split(".");
  assert.equal(parts.length, 2);
  const payload = JSON.parse(new TextDecoder().decode(base64urlDecode(parts[0])));
  assert.deepEqual(Object.keys(payload), ["ticket", "issued", "nonce"]);
  assert.equal(payload.ticket, ticketId);
  assert.ok(Math.abs(payload.issued - Math.floor(Date.now() / 1000)) <= 2, "issued is this dial's clock, in seconds");
  assert.equal(base64urlDecode(payload.nonce).length, 16);
  const signature = base64urlDecode(parts[1]);
  assert.equal(signature.length, 64);
  const algorithm = { name: "ECDSA", hash: "SHA-256" };
  assert.equal(await crypto.subtle.verify(algorithm, keys.publicKey, signature, signedBytes(parts[0])), true);
  // The domain prefix is part of the signed bytes: the same signature over the
  // bare payload, or over another domain, is not a proof.
  assert.equal(await crypto.subtle.verify(algorithm, keys.publicKey, signature, new TextEncoder().encode(parts[0])), false);
  assert.equal(await crypto.subtle.verify(algorithm, keys.publicKey, signature, new TextEncoder().encode(`dark-factory-relay/ticket\n${parts[0]}`)), false);
  const other = await mintProof(ticketId, keys.privateKey);
  assert.notEqual(other, proof, "each dial must carry its own nonce");
  assert.notEqual(JSON.parse(new TextDecoder().decode(base64urlDecode(other.split(".")[0]))).nonce, payload.nonce);
});

test("proof minting refuses an exportable key, a foreign key and a bad ticket id", async () => {
  const exportable = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  await assert.rejects(mintProof(base64urlEncode(bytes(16)), exportable.privateKey), (error) => error instanceof SessionError && error.code === "crypto_unavailable");
  const rsa = await crypto.subtle.generateKey({ name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" }, false, ["sign", "verify"]);
  await assert.rejects(mintProof(base64urlEncode(bytes(16)), rsa.privateKey), (error) => error instanceof SessionError && error.code === "crypto_unavailable");
  const keys = await deviceKey();
  for (const ticketId of ["", base64urlEncode(bytes(15)), base64urlEncode(bytes(17)), "not/base64"]) {
    await assert.rejects(mintProof(ticketId, keys.privateKey), (error) => error instanceof SessionError && error.code === "invalid_request");
  }
});

test("a ticket is read without verification and every member is checked", () => {
  const control = mintTicket({ node, purpose: "control", expires: 1_800_000_000 });
  const parsed = parseTicket(control);
  assert.equal(parsed.node, node);
  assert.equal(parsed.purpose, "control");
  assert.equal(parsed.expires, 1_800_000_000);
  assert.equal(base64urlDecode(parsed.ticket).length, 16);
  assert.equal(base64urlDecode(parsed.device)[0], 4);
  assert.equal(Object.isFrozen(parsed), true);
  // The relay is the only verifier: a wrong signature still parses, because a
  // browser reads a ticket only to learn what to prove and when to stop.
  assert.equal(parseTicket(mintTicket({ node, purpose: "pair", signature: base64urlEncode(bytes(64, 1)) })).purpose, "pair");
  assert.equal(parseTicket(mintTicket({ node, purpose: "pair" })).device, undefined);
  // Unknown payload members are ignored and reach no field of the result.
  const extra = `${base64url(JSON.stringify({ node, controller: base64urlEncode(bytes(16)), purpose: "pair", ticket: base64urlEncode(bytes(16)), expires: 10, audience: "elsewhere" }))}.${base64urlEncode(bytes(64))}`;
  assert.deepEqual(Object.keys(parseTicket(extra)), ["node", "controller", "purpose", "ticket", "expires"]);
  for (const token of [
    "", ".", "a.b.c", `${base64url("{}")}.${base64urlEncode(bytes(64))}`,
    `${base64url(JSON.stringify({ node, controller: base64urlEncode(bytes(16)), purpose: "pair", ticket: base64urlEncode(bytes(16)), expires: 10 }))}.${base64urlEncode(bytes(63))}`,
    mintTicket({ node: node.toUpperCase(), purpose: "pair" }),
    mintTicket({ node: "1".repeat(32), purpose: "pair" }),
    mintTicket({ node, purpose: "observe" }),
    mintTicket({ node, purpose: "pair", ticketId: base64urlEncode(bytes(15)) }),
    mintTicket({ node, purpose: "pair", controller: base64urlEncode(bytes(8)) }),
    mintTicket({ node, purpose: "pair", expires: 0 }),
    mintTicket({ node, purpose: "control", device: base64urlEncode(bytes(65, 3)) }),
    mintTicket({ node, purpose: "control", device: base64urlEncode(bytes(33, 4)) }),
    // A pair ticket that names a device would claim a key pairing has not minted.
    `${base64url(JSON.stringify({ node, controller: base64urlEncode(bytes(16)), purpose: "pair", ticket: base64urlEncode(bytes(16)), expires: 10, device: base64urlEncode(bytes(65, 4)) }))}.${base64urlEncode(bytes(64))}`,
  ]) expectInvalid(() => parseTicket(token));
});
