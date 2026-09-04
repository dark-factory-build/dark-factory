// Cross-checks relay/fixtures/tokens.json — the shared vector fixture also
// consumed by internal/relayhost/vectors_test.go on the Go side — against the
// real verification functions in src/tokens.ts, so the two implementations of
// the relay credential formats documented in README.md cannot drift apart.
//
// This is the "importable" branch of that requirement: src/tokens.ts can be
// imported directly under `node --test` (see ts-loader.mjs for the one gap
// that needs filling), and verifyHostToken/verifyProof already take a
// now/nowSeconds parameter, so every fixture check below runs at the fixture's
// own recorded instant — no wall clock involved, no wrangler dev child needed.
//
// There is deliberately no live-worker counterpart here. The FactoryRelay
// Durable Object (src/relay.ts) hardcodes `Math.floor(Date.now() / 1000)` for
// its skew and expiry checks, so a token whose `issued` was fixed at
// fixture-generation time cannot be presented to a running worker except in
// the ~60s after it was written. Testing that live would mean either
// weakening the worker's clock check or re-signing a fresh token instead of
// the fixture's — this file does neither.

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { register } from 'node:module';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

register('./ts-loader.mjs', import.meta.url);

const { verifyHostToken, verifyTicket, verifyProof, nodeIdForKey } = await import('../src/tokens.ts');

const fixture = JSON.parse(await readFile(fileURLToPath(new URL('../fixtures/tokens.json', import.meta.url)), 'utf8'));

function decode(text, expectedLength) {
	const bytes = Buffer.from(text, 'base64url');
	assert.equal(bytes.length, expectedLength, `${text} did not decode to ${expectedLength} bytes`);
	return new Uint8Array(bytes);
}

/** Flips one character of the signature half, leaving the token well-formed. */
function corrupt(token) {
	const [payload, signature] = token.split('.');
	return `${payload}.${signature[0] === 'A' ? 'B' : 'A'}${signature.slice(1)}`;
}

test('the node id derives from the fixture public key', async () => {
	assert.equal(await nodeIdForKey(decode(fixture.publicKey, 32)), fixture.nodeId);
});

test('the fixture host token verifies at its own issued instant', async () => {
	const token = await verifyHostToken(fixture.hostToken.token, fixture.nodeId, fixture.hostToken.issued);
	assert.ok(token, 'host token did not verify');
	assert.deepEqual(token, {
		node: fixture.nodeId,
		key: fixture.publicKey,
		generation: fixture.generation,
		sequence: fixture.hostToken.sequence,
		issued: fixture.hostToken.issued,
	});
	assert.equal(fixture.hostToken.token.split('.')[0], fixture.hostToken.payloadText);
});

test('the fixture host token is refused outside the 60s skew window', async () => {
	assert.equal(await verifyHostToken(fixture.hostToken.token, fixture.nodeId, fixture.hostToken.issued + 1000), null);
});

test('the fixture host token with a flipped signature byte is refused', async () => {
	assert.equal(await verifyHostToken(corrupt(fixture.hostToken.token), fixture.nodeId, fixture.hostToken.issued), null);
});

test('the fixture pair ticket verifies and carries no device', async () => {
	const ticket = await verifyTicket(fixture.pairTicket.token, fixture.nodeId, decode(fixture.publicKey, 32));
	assert.ok(ticket, 'pair ticket did not verify');
	assert.deepEqual(ticket, {
		node: fixture.nodeId,
		controller: fixture.pairTicket.controller,
		purpose: 'pair',
		ticket: fixture.pairTicket.ticketId,
		expires: fixture.pairTicket.expires,
		device: null,
	});
});

test('the fixture control ticket verifies and carries the fixture device point', async () => {
	const ticket = await verifyTicket(fixture.controlTicket.token, fixture.nodeId, decode(fixture.publicKey, 32));
	assert.ok(ticket, 'control ticket did not verify');
	assert.deepEqual(ticket, {
		node: fixture.nodeId,
		controller: fixture.controlTicket.controller,
		purpose: 'control',
		ticket: fixture.controlTicket.ticketId,
		expires: fixture.controlTicket.expires,
		device: fixture.device.point,
	});
});

test('a ticket with a flipped signature byte is refused', async () => {
	assert.equal(await verifyTicket(corrupt(fixture.controlTicket.token), fixture.nodeId, decode(fixture.publicKey, 32)), null);
});

test('the fixture proof verifies against the fixture device key and control ticket id', async () => {
	const proof = await verifyProof(
		fixture.proof.token,
		decode(fixture.device.point, 65),
		fixture.controlTicket.ticketId,
		fixture.proof.issued,
	);
	assert.ok(proof, 'proof did not verify');
	assert.deepEqual(proof, {
		ticket: fixture.controlTicket.ticketId,
		issued: fixture.proof.issued,
		nonce: fixture.proof.nonce,
	});
});

test('the fixture proof is refused for a different ticket id or outside its skew window', async () => {
	const devicePoint = decode(fixture.device.point, 65);
	assert.equal(await verifyProof(fixture.proof.token, devicePoint, 'not-the-fixture-ticket-id', fixture.proof.issued), null);
	assert.equal(
		await verifyProof(fixture.proof.token, devicePoint, fixture.controlTicket.ticketId, fixture.proof.issued + 1000),
		null,
	);
});

test('the fixture proof with a flipped signature byte is refused', async () => {
	const devicePoint = decode(fixture.device.point, 65);
	assert.equal(
		await verifyProof(corrupt(fixture.proof.token), devicePoint, fixture.controlTicket.ticketId, fixture.proof.issued),
		null,
	);
});
