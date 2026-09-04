import assert from "node:assert/strict";
import test from "node:test";
import {
  SessionError,
  consumeInvitation,
  parseInvitation,
} from "../dist/src/index.js";
import { invitationFragment, invitationMembers, mintTicket, nodeId } from "./remote-fake.mjs";

const NOW = 1_700_000_000;
const node = nodeId("a");
const expectInvalid = (fragment) => assert.throws(
  () => parseInvitation(fragment, NOW),
  (error) => error instanceof SessionError && error.code === "invalid_request",
  typeof fragment === "string" ? fragment.slice(0, 48) : String(fragment),
);

test("a canonical invitation parses every member and accepts the whole link", () => {
  const members = invitationMembers({ node });
  const fragment = invitationFragment(members);
  const invitation = parseInvitation(fragment, NOW);
  assert.deepEqual({ ...invitation }, {
    relay: members.relay,
    node,
    daemon: members.daemon,
    host: members.host,
    challenge: members.challenge,
    ticket: members.ticket,
    expires: members.expires,
  });
  assert.equal(Object.isFrozen(invitation), true);
  assert.equal(parseInvitation(`https://app.darkfactory.build/remote${fragment}`, NOW).node, node);
  assert.equal(parseInvitation(fragment.slice(1), NOW).node, node);
  // Additive members are tolerated exactly as they are on the wire contract.
  assert.equal(parseInvitation(invitationFragment({ ...members, future: "member" }), NOW).node, node);
});

test("an ordinary factory names neither its relay nor its loopback host", () => {
  const { relay, host, ...members } = invitationMembers({ node });
  const invitation = parseInvitation(invitationFragment(members), NOW);
  assert.equal(invitation.relay, "wss://relay.darkfactory.build");
  assert.equal(invitation.host, "127.0.0.1:43123");
  // Named but empty is a mistake, not a request for the ordinary one.
  for (const override of [{ relay: "" }, { host: "" }]) expectInvalid(invitationFragment({ ...members, ...override }));
});

test("a relay is wss anywhere, or plain ws only to a loopback address", () => {
  const base = invitationMembers({ node });
  // The relay a `wrangler dev --local` run puts on this machine is plain ws.
  for (const relay of ["ws://127.0.0.1:8787", "ws://localhost:8787", "ws://[::1]:8787"]) {
    assert.equal(parseInvitation(invitationFragment({ ...base, relay }), NOW).relay, relay, relay);
  }
  for (const relay of ["ws://relay.example", "ws://10.0.0.1:8787", "ws://127.0.0.1:8787/", "ws://user:pass@127.0.0.1:8787"]) {
    expectInvalid(invitationFragment({ ...base, relay }));
  }
});

test("every invitation member is validated before any of it becomes authority", () => {
  const base = invitationMembers({ node });
  for (const override of [
    { relay: "ws://relay.example" },
    { relay: "https://relay.example" },
    { relay: "wss://relay.example/" },
    { relay: "wss://relay.example:443" },
    { relay: "wss://relay.example/controller" },
    { relay: "wss://user:pass@relay.example" },
    { node: node.slice(1) },
    { node: "A".repeat(32) },
    { node: "1".repeat(32) },
    { daemon: "22".repeat(15) },
    { daemon: "0".repeat(32) },
    { daemon: "GG".repeat(16) },
    { host: "127.0.0.1" },
    { host: "example.com:443" },
    { host: "10.0.0.1:43123" },
    { host: "127.0.0.1:0" },
    { host: "127.0.0.1:70000" },
    { host: "http://127.0.0.1:43123" },
    { challenge: "11".repeat(31) },
    { challenge: "0".repeat(64) },
    { expires: 0 },
    { expires: "4e9" },
    { expires: "-1" },
    { expires: " 4000000000" },
    { expires: NOW },
    { ticket: mintTicket({ node, purpose: "control" }) },
    { ticket: mintTicket({ node: nodeId("b"), purpose: "pair" }) },
    { ticket: mintTicket({ node, purpose: "pair", expires: NOW }) },
    { ticket: "not-a-ticket" },
  ]) expectInvalid(invitationFragment({ ...base, ...override }));
  for (const member of ["node", "daemon", "challenge", "ticket", "expires"]) {
    const missing = { ...base };
    delete missing[member];
    expectInvalid(invitationFragment(missing));
  }
  const query = new URLSearchParams(base);
  for (const fragment of [
    "", "#", "#df_remote", `#df_remote&`, `#df_remote=${query}`,
    // The marker is the first token or the link is not an invitation.
    `#node=${node}&df_remote&${query}`, `#DF_REMOTE&${query}`, `##df_remote&${query}`,
  ]) expectInvalid(fragment);
});

test("consuming an invitation clears the fragment before it is read", () => {
  const members = invitationMembers({ node });
  const state = { route: "remote" };
  const calls = [];
  const invitation = consumeInvitation(
    { hash: invitationFragment(members), pathname: "/remote", search: "?source=qr" },
    { state, replaceState: (nextState, _title, url) => calls.push([nextState, url]) },
    NOW,
  );
  assert.equal(invitation?.node, node);
  assert.deepEqual(calls, [[state, "/remote?source=qr"]]);
});

test("a refused invitation fragment is still scrubbed and never returns authority", () => {
  for (const hash of [
    "#df_remote",
    "#df_remote&node=nope",
    "#df_remote=anything",
    `#df_remote%26node=${node}`,
    `#DF_REMOTE&node=${node}`,
    invitationFragment(invitationMembers({ node, expires: 1 })),
  ]) {
    const calls = [];
    const invitation = consumeInvitation(
      { hash, pathname: "/remote", search: "" },
      { state: null, replaceState: (_state, _title, url) => calls.push(url) },
      NOW,
    );
    assert.equal(invitation, null, hash.slice(0, 40));
    assert.deepEqual(calls, ["/remote"], hash.slice(0, 40));
  }
});

test("an ordinary anchor is neither invitation authority nor scrubbed", () => {
  for (const hash of ["", "#remote", "#df_pair=" + "11".repeat(32), "#factory-2", "#df_remotely"]) {
    let replaced = false;
    const invitation = consumeInvitation(
      { hash, pathname: "/remote", search: "" },
      { state: null, replaceState: () => { replaced = true; } },
      NOW,
    );
    assert.equal(invitation, null, hash);
    assert.equal(replaced, false, hash);
  }
});
