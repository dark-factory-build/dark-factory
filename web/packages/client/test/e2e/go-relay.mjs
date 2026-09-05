// The remote relay acceptance proof. Every component here is the real one: a
// `wrangler dev --local` relay Worker, two factoryd homes dialing it outbound,
// real shell-provider attempts raising real HumanRequests, and the shipped
// RemoteManager driving them from Node over `ws`. Nothing is faked, so a
// failure here means the deployed path is wrong.
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdirSync, readFileSync, existsSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { createServer } from "node:net";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import WebSocket from "ws";
import { BrowserSession, createRemoteManager, MemoryRemoteStore } from "../../dist/src/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = join(here, "..", "..", "..", "..", "..");
const relayRoot = join(repositoryRoot, "relay");
// Set to run this proof against a deployed relay instead of a local
// `wrangler dev` Worker. helpers.mjs imports the relay's own `ws`
// devDependency at module scope, so it is loaded only when this is unset --
// the only reason scripts/go-relay-e2e.sh can skip relay/'s npm ci otherwise.
const RELAY_ORIGIN = process.env.DARK_FACTORY_RELAY_ORIGIN || undefined;
const { readPersistedObjects, startWorker } =
  RELAY_ORIGIN === undefined ? await import(join(relayRoot, "tests", "helpers.mjs")) : {};

// The daemon binds every remote pairing challenge to the production browser
// origin and refuses to mint an invitation unless the listener allows it, so
// this is the only origin a real remote pairing can carry. The relay's own
// PWA_ORIGIN is the same value by default, which is why no --var is needed.
const PWA_ORIGIN = "https://app.darkfactory.build";
const GIT = "/Library/Developer/CommandLineTools/usr/bin/git";
const FACTORYD = required("DARK_FACTORY_E2E_FACTORYD");
const FACTORYCTL = required("DARK_FACTORY_E2E_FACTORYCTL");

// Distinctive strings, so the secrecy sweep can only pass by the relay having
// genuinely never seen the payloads it forwards.
const SECRET = {
  projects: ["relay-secret-project-alpha", "relay-secret-project-beta", "relay-secret-project-gamma"],
  body: "relay-secret-task-body",
  questions: ["relay-secret-question-alpha", "relay-secret-question-beta"],
};
const ANSWER = "relay-answer";

function required(name) {
  const value = process.env[name];
  if (value === undefined || !value.startsWith("/") || !existsSync(value)) throw new Error(`${name} must name an existing absolute path`);
  return value;
}

async function until(predicate, label, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await predicate();
    if (value !== undefined && value !== false && value !== null) return value;
    if (Date.now() >= deadline) throw new Error(`timed out after ${timeoutMs}ms waiting for ${label}`);
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// -- the two factories ------------------------------------------------------

/** A loopback port this process has proven free, so a restart can reuse it. */
async function freePort() {
  const server = createServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address();
  server.close();
  await once(server, "close");
  return port;
}

class Factory {
  constructor(root, name, relayOrigin, browserPort) {
    this.name = name;
    this.home = join(root, name);
    this.repositories = [];
    this.relayOrigin = relayOrigin;
    // The pairing and authentication transcripts bind this exact address, so
    // it must survive a restart: an ephemeral port would invalidate the
    // binding the browser holds.
    this.browserAddress = `127.0.0.1:${browserPort}`;
    this.output = "";
    this.child = undefined;
    this.control({}, "init", "--home", this.home);
  }

  repository(root, label) {
    const path = join(root, label);
    mkdirSync(path, { recursive: true, mode: 0o700 });
    execFileSync(GIT, ["init", "--quiet", "--initial-branch=main"], { cwd: path });
    execFileSync(GIT, ["-c", "user.name=relay-e2e", "-c", "user.email=relay-e2e@invalid", "commit", "--quiet", "--allow-empty", "--message", "seed"], { cwd: path });
    this.repositories.push(path);
    return path;
  }

  control(options, ...args) {
    const output = execFileSync(FACTORYCTL, args, {
      encoding: "utf8",
      timeout: 20_000,
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        DARK_FACTORY_SOCKET: join(this.home, "runtimes", "factory.sock"),
        DARK_FACTORY_OPERATOR_TOKEN_FILE: join(this.home, "operator.token"),
      },
      ...options,
    });
    return output;
  }

  json(...args) { return JSON.parse(this.control({}, ...args)); }

  start() {
    const child = spawn(FACTORYD, ["--home", this.home, "--development-browser-address", this.browserAddress, "--relay-origin", this.relayOrigin], { stdio: ["ignore", "pipe", "pipe"] });
    const capture = (chunk) => { this.output = `${this.output}${chunk}`.slice(-200_000); };
    child.stdout.on("data", capture);
    child.stderr.on("data", capture);
    this.child = child;
    return child;
  }

  /** Connected to the relay is the only readiness this proof accepts. */
  async ready() {
    return until(() => {
      try { return this.json("remote", "status").connected === true; } catch { return false; }
    }, `${this.name} to report a connected relay`, 90_000);
  }

  async stop(signal = "SIGTERM") {
    const child = this.child;
    if (child === undefined || child.exitCode !== null) return;
    this.child = undefined;
    child.kill(signal);
    await once(child, "exit");
  }

  /**
   * One invitation, minted the way the console mints one: the daemon's own
   * pair page hands out a loopback pairing challenge, a real client session
   * pairs with it and holds the full loopback grant, and its REMOTE_INVITE
   * returns the link -- decomposed here into the invitation the PWA parses.
   */
  async invitation() {
    // Node's fetch refuses to send a Sec-Fetch-* header, which the daemon's
    // Fetch Metadata gate requires, so this is the browser's exact request
    // sent through the stdlib client instead.
    const response = await new Promise((resolve, reject) => {
      const post = httpRequest(`http://${this.browserAddress}/pair`, {
        method: "POST",
        headers: { "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin" },
      }, resolve);
      post.on("error", reject);
      post.end();
    });
    response.resume();
    const location = response.headers.location ?? "";
    const challenge = location.split("#df_pair=")[1] ?? "";
    assert.match(challenge, /^[0-9a-f]{64}$/, `${this.name} answered its own pair form with ${response.statusCode} ${location}`);
    const session = new BrowserSession({
      url: `ws://${this.browserAddress}/browser`,
      host: this.browserAddress,
      origin: PWA_ORIGIN,
      challenge,
      // This session pairs once and is closed, so nothing ever loads a key back.
      keyStore: { load: async () => null, save: async () => {} },
      socketFactory: (url) => new WebSocket(url, { origin: PWA_ORIGIN, perMessageDeflate: false }),
    });
    let link;
    try {
      await session.connect();
      ({ link } = await session.inviteRemote());
    } finally {
      session.close();
    }
    const fragment = link.slice(link.indexOf("#df_remote&") + "#df_remote&".length);
    const members = new URLSearchParams(fragment);
    // parseInvitation() refuses a ws:// relay, so a local relay cannot go
    // through it; every other member is read exactly as it does. The daemon
    // omits `relay=` exactly when it is already dialing the origin it was
    // started with, so an absent member falls back to that -- the same rule
    // parseInvitation() applies with its own default relay.
    return Object.freeze({
      relay: members.get("relay") ?? this.relayOrigin,
      node: members.get("node"),
      daemon: members.get("daemon"),
      host: members.get("host"),
      challenge: members.get("challenge"),
      ticket: members.get("ticket"),
      expires: Number(members.get("expires")),
      link,
    });
  }
}

// -- the controller side ----------------------------------------------------

/**
 * Every controller socket, in dial order, with the frames it sent. A browser
 * sets Origin itself; Node has to be told, and the relay checks it exactly.
 */
const connections = [];

class RelayWebSocket extends WebSocket {
  constructor(url, protocols) {
    try {
      super(url, protocols, { origin: PWA_ORIGIN, perMessageDeflate: false, followRedirects: false });
    } catch (error) {
      // A malformed url (eg. a relay member that never got its default
      // applied) throws here, before any record exists to carry it -- log it
      // to the same place a real dial's outcome goes, or a future failure
      // like this again shows only the generic error a rejected pairing carries.
      connections.push({ url: String(url), node: String(url).split("/").pop(), sent: [], received: [], closed: `constructor threw: ${error.message}` });
      throw error;
    }
    this.record = { url: String(url), node: String(url).split("/").pop(), sent: [], received: [], closed: undefined };
    connections.push(this.record);
    this.on("message", (data, binary) => { this.record.received.push(binary ? "BINARY" : (JSON.parse(String(data)).type ?? "?")); });
    this.on("close", (code, reason) => { this.record.closed = `${code}:${String(reason)}`; });
  }

  send(data, ...rest) {
    this.record.sent.push(typeof data === "string" ? data : "<binary>");
    return super.send(data, ...rest);
  }
}

const dialsTo = (nodeId) => connections.filter((record) => record.node === nodeId);
const frameTypes = (record) => record.sent.map((frame) => { try { return JSON.parse(frame).type; } catch { return "BINARY"; } });

function newManager() {
  const changes = [];
  const manager = createRemoteManager({
    store: new MemoryRemoteStore(),
    origin: PWA_ORIGIN,
    webSocket: RelayWebSocket,
    onChange: () => changes.push(manager.factories().map((view) => `${view.nodeId}:${view.status}`).join(",")),
  });
  manager.changes = changes;
  return manager;
}

const view = (manager, nodeId) => manager.factories().find((entry) => entry.nodeId === nodeId);
const statusOf = (manager, nodeId) => view(manager, nodeId)?.status;
const openRequests = (manager, nodeId) => manager.needsYou().filter((entry) => entry.nodeId === nodeId);

// -- the proof --------------------------------------------------------------

const timings = [];
async function step(name, work) {
  const started = Date.now();
  try {
    const value = await work();
    timings.push(`${name} ${Date.now() - started}ms`);
    return value;
  } catch (error) {
    throw new Error(`step ${name}: ${error.message}`, { cause: error });
  }
}

async function main() {
  // The wrapper owns this directory so a stage timeout, which kills this
  // process outright, still leaves nothing behind.
  const root = required("DARK_FACTORY_E2E_RELAY_ROOT");
  const managers = [];
  let worker;
  let persistence;
  const factories = [];
  try {
    let relayOrigin;
    if (RELAY_ORIGIN === undefined) {
      // One real Worker, spawned exactly as the relay's own gate spawns it.
      persistence = join(root, "persist");
      process.chdir(relayRoot);
      worker = await startWorker(persistence);
      process.chdir(repositoryRoot);
      relayOrigin = worker.origin.replace("http://", "ws://");
    } else {
      relayOrigin = RELAY_ORIGIN;
    }

    const alpha = new Factory(root, "a", relayOrigin, await freePort());
    const beta = new Factory(root, "b", relayOrigin, await freePort());
    factories.push(alpha, beta);
    const replyFile = join(root, "beta-reply.txt");

    await step("boot", async () => {
      alpha.start();
      beta.start();
      await alpha.ready();
      await beta.ready();
    });

    await step("work", async () => {
      const waiting = (question) => `set -eu\n"$DARK_FACTORY_FACTORYCTL" attempt request-human --idempotency-key ${"1".repeat(32)} --question '${question}'\nIFS= read -r ignored\n`;
      const answering = (question) => `set -eu\n"$DARK_FACTORY_FACTORYCTL" attempt request-human --idempotency-key ${"2".repeat(32)} --question '${question}'\n/bin/stty -icanon min 1 time 0\nreply=$(/bin/dd bs=1 count=${ANSWER.length} 2>/dev/null)\nprintf '%s\\n' "$reply" >> '${replyFile}'\n"$DARK_FACTORY_FACTORYCTL" attempt succeed --result ${SECRET.body}\n`;
      // Factory A: two projects, one live attempt that waits on its question.
      const first = alpha.json("project", "create", "--name", SECRET.projects[0], "--root", alpha.repository(root, "alpha-one")).id;
      alpha.json("project", "create", "--name", SECRET.projects[1], "--root", alpha.repository(root, "alpha-two"));
      const alphaAgent = alpha.json("agent", "create", "--project", first, "--name", "alpha-worker", "--provider", "shell", "--tool-budget", "8").id;
      alpha.json("task", "add", "--project", first, "--agent", alphaAgent, "--title", SECRET.body, "--body", waiting(SECRET.questions[0]));
      // Factory B: one project, one attempt that consumes the reply it is given.
      const only = beta.json("project", "create", "--name", SECRET.projects[2], "--root", beta.repository(root, "beta-one")).id;
      const betaAgent = beta.json("agent", "create", "--project", only, "--name", "beta-worker", "--provider", "shell", "--tool-budget", "8").id;
      beta.betaTask = beta.json("task", "add", "--project", only, "--agent", betaAgent, "--title", SECRET.body, "--body", answering(SECRET.questions[1])).id;
      alpha.control({}, "dispatch", "on");
      beta.control({}, "dispatch", "on");
    });

    const invitationA = await alpha.invitation();
    const invitationB = await beta.invitation();
    const nodeA = invitationA.node;
    const nodeB = invitationB.node;
    assert.notEqual(nodeA, nodeB, "the two homes minted the same node identity");

    const manager = newManager();
    managers.push(manager);
    await manager.start();

    await step("1-pair", async () => {
      const boundA = await manager.pair(invitationA);
      const boundB = await manager.pair(invitationB);
      assert.equal(manager.factories().length, 2, "pairing two factories did not produce two bindings");
      await until(() => statusOf(manager, nodeA) === "ready" && statusOf(manager, nodeB) === "ready", "both factories ready");
      const bindings = manager.bindings();
      for (const binding of bindings) {
        assert.ok(typeof binding.relayTicket === "string" && binding.relayTicket.includes("."), `binding ${binding.nodeId} kept no control ticket`);
        assert.ok(/^[0-9a-f]{32}$/.test(binding.clientId ?? ""), `binding ${binding.nodeId} kept no client id`);
        assert.equal(binding.key?.extractable, false, `binding ${binding.nodeId} stored an exportable identity key`);
      }
      assert.notEqual(bindings[0].clientId, bindings[1].clientId, "the two factories minted the same client id");
      assert.equal(boundA.nodeId, nodeA);
      assert.equal(boundB.nodeId, nodeB);
    });

    const headB = await step("2-state", async () => {
      // Pairing selects the factory it just bound, so this is a real flip in
      // both directions, and the selected view is the one a console renders.
      assert.equal(manager.selected(), nodeB, "pairing did not select the factory it bound");
      const selected = async (nodeId, projects) => {
        manager.select(nodeId);
        assert.equal(manager.selected(), nodeId, `select(${nodeId}) did not flip the selection`);
        const entry = await until(() => { const value = view(manager, manager.selected()); return value.state === undefined ? undefined : value; }, `the selected factory ${nodeId} to carry state`);
        assert.equal(entry.nodeId, nodeId, "the selected view named another factory");
        assert.equal(entry.state.projects.size, projects, `the selected factory exposed ${entry.state.projects.size} projects, want ${projects}`);
        return entry.state;
      };
      const stateA = await selected(nodeA, 2);
      const stateB = await selected(nodeB, 1);
      // Selecting one factory must not cost the other its live session.
      assert.equal(statusOf(manager, nodeA), "ready", "selecting B dropped A");
      assert.equal(stateA.projects.size, 2, "factory A lost a project while B was selected");
      return stateB.head;
    });

    const requestA = await step("3-needs-you", async () => {
      await until(() => manager.needsYou().length === 2, "one open HumanRequest from each factory", 120_000);
      const [fromA] = openRequests(manager, nodeA);
      const [fromB] = openRequests(manager, nodeB);
      assert.ok(fromA !== undefined && fromB !== undefined, "the two open requests were not one per factory");
      assert.equal(fromA.request.status, "open");
      assert.equal(fromB.request.status, "open");
      assert.equal(fromA.label, nodeA.slice(0, 8));
      assert.equal(fromB.label, nodeB.slice(0, 8));
      assert.equal("question" in fromA.request, false, "private question text leaked into public state");
      return fromA.request;
    });

    await step("4-reply", async () => {
      const session = manager.client(nodeB).session;
      const [pending] = openRequests(manager, nodeB);
      const detail = await session.getHumanRequestDetail({ requestId: pending.request.id, expectedRevision: pending.request.revision });
      assert.equal(detail.question, SECRET.questions[1], "factory B returned a different question than it asked");
      const result = await session.replyHumanRequest(detail, ANSWER);
      // "resolved" is the delivered arm of this reply; "delivery_unknown" is
      // the honest uncertainty arm and is not a pass.
      assert.equal(result.status, "resolved", `factory B reply status = ${result.status}`);
      await until(() => existsSync(replyFile) && readFileSync(replyFile, "utf8").split(ANSWER).length - 1 === 1, "exactly one answer observed by the provider");
      await until(() => view(manager, nodeB).state?.tasks.get(beta.betaTask)?.status === "succeeded", "factory B task to complete", 90_000);
      assert.equal(readFileSync(replyFile, "utf8").split(ANSWER).length - 1, 1, "the provider observed the answer more than once");
      await assert.rejects(() => session.replyHumanRequest(detail, ANSWER), (error) => error.code === "stale", "a second reply on the same detail was not refused");
      // That fence is the client's own memory. Go back to the daemon for a
      // fresh detail and try again: whichever of the two calls it refuses, the
      // refusal must be typed and no second answer may reach the provider.
      const refusal = await session
        .getHumanRequestDetail({ requestId: pending.request.id, expectedRevision: pending.request.revision })
        .then((fresh) => session.replyHumanRequest(fresh, ANSWER))
        .then(() => undefined, (error) => error);
      assert.ok(refusal !== undefined, "the daemon accepted a second reply to a resolved request");
      assert.equal(refusal.code, "stale", `the daemon's refusal was ${refusal.code}, not a typed staleness`);
      await sleep(500);
      assert.equal(readFileSync(replyFile, "utf8").split(ANSWER).length - 1, 1, "the refused second reply still reached the provider");
      const [stillOpen] = openRequests(manager, nodeA);
      assert.ok(stillOpen !== undefined, "answering B closed A's request");
      assert.equal(stillOpen.request.revision, requestA.revision, "A's untouched request changed revision");
    });

    await step("5-restart", async () => {
      const dialsA = dialsTo(nodeA).length;
      const dialsB = dialsTo(nodeB).length;
      const seenA = new Set([statusOf(manager, nodeA)]);
      const watch = setInterval(() => seenA.add(statusOf(manager, nodeA)), 20);
      try {
        await beta.stop("SIGKILL");
        await until(() => statusOf(manager, nodeB) !== "ready", "factory B to go offline");
        // The relay requires a strictly greater host (generation, sequence),
        // and generation is the daemon's start time in whole seconds.
        await sleep(1_500);
        beta.start();
        await beta.ready();
        await until(() => statusOf(manager, nodeB) === "ready", "factory B to come back ready", 90_000);
      } finally {
        clearInterval(watch);
      }
      assert.deepEqual([...seenA], ["ready"], `factory A did not stay ready: ${[...seenA].join(",")}`);
      assert.equal(dialsTo(nodeA).length, dialsA, "factory A redialed while only B restarted");
      const killed = dialsTo(nodeB)[dialsB - 1];
      assert.equal(frameTypes(killed).includes("HUMAN_REQUEST_REPLY"), true, "the killed connection never carried the reply, so its absence proves nothing");
      const reconnects = dialsTo(nodeB).slice(dialsB);
      assert.ok(reconnects.length >= 1, "factory B never redialed");
      const revived = reconnects.at(-1);
      // The reply happened before the kill, so a replayed session would send it
      // again. The revived one starts the transcript from nothing: the daemon
      // greets it, it proves itself, and it asks for a whole new snapshot.
      assert.equal(revived.received[0], "HELLO", "the revived connection did not start from a fresh daemon greeting");
      assert.deepEqual(frameTypes(revived).slice(0, 2), ["AUTH_PROVE", "STATE_GET"], "the revived connection did not open with a fresh authentication and snapshot");
      assert.equal(frameTypes(revived).includes("HUMAN_REQUEST_REPLY"), false, "the revived connection replayed the reply");
      assert.equal(readFileSync(replyFile, "utf8").split(ANSWER).length - 1, 1, "the restart made the provider observe a second answer");
      const state = view(manager, nodeB).state;
      assert.ok(state.head >= headB, "the post-restart snapshot regressed below the pre-kill head");
      assert.equal(state.projects.size, 1, "the post-restart snapshot lost factory B's project");
    });

    await step("6-two-controllers", async () => {
      const second = newManager();
      managers.push(second);
      await second.start();
      await second.pair(await alpha.invitation());
      await until(() => statusOf(second, nodeA) === "ready", "the second controller to reach ready");
      assert.equal(statusOf(manager, nodeA), "ready", "a second controller displaced the first");
      const secondClient = second.bindings()[0].clientId;
      assert.notEqual(secondClient, manager.bindings().find((binding) => binding.nodeId === nodeA).clientId, "the second controller reused the first client id");
      const listed = alpha.json("web", "list-clients").clients.find((client) => client.id === secondClient);
      assert.ok(listed !== undefined, "factory A never recorded the second controller");
      alpha.json("web", "revoke", secondClient, "--revision", String(listed.revision));
      // The daemon tells the relay before it tears the loopback sessions down,
      // so the controller learns why it lost the factory: the relay's own
      // REVOKE close, not a generic disconnect it would retry.
      await until(() => statusOf(second, nodeA) === "revoked", "the revoked controller to observe its revocation", 15_000);
      await sleep(1_000);
      assert.equal(statusOf(second, nodeA), "revoked", "the revoked controller recovered its session");
      assert.equal(view(second, nodeA).state, undefined, "the revoked controller kept a state snapshot");
      assert.equal(view(second, nodeA).error?.code, "unauthorized", "revocation was not reported as an authority failure");
      assert.equal(statusOf(manager, nodeA), "ready", "revoking the second controller disturbed the first");
      assert.equal(view(manager, nodeA).state?.projects.size, 2, "the surviving controller lost factory A's state");
    });

    for (const value of managers) value.close();
    for (const factory of factories) await factory.stop();
    if (worker !== undefined) await worker.stop();

    let note = "";
    if (RELAY_ORIGIN === undefined) {
      await step("2-relay-knows-nothing", async () => {
        const objects = await readPersistedObjects(persistence);
        assert.equal(objects.length, 2, `the relay persisted ${objects.length} objects, want one per factory`);
        for (const object of objects) {
          assert.deepEqual(object.records.map(({ key }) => key), ["host"], `object ${object.node} persisted more than the host record`);
        }
        const transcript = worker.transcript();
        for (const secret of [...SECRET.projects, SECRET.body, ...SECRET.questions, ANSWER]) {
          assert.equal(transcript.includes(secret), false, `the relay's own output carried ${secret}`);
        }
      });
    } else {
      await step("2-relay-reachable", async () => {
        const healthzOrigin = RELAY_ORIGIN.replace(/^wss:/, "https:").replace(/^ws:/, "http:");
        const response = await fetch(`${healthzOrigin}/healthz`);
        assert.equal(response.status, 200, `GET ${healthzOrigin}/healthz returned ${response.status}`);
      });
      note = " (note: persistence and log secrecy were proven against the local worker only, not the deployed relay)";
    }

    process.stdout.write(`go-relay: PASS ${timings.join(" | ")}${note}\n`);
  } catch (error) {
    process.stderr.write(`go-relay: FAIL ${error.stack}\n`);
    for (const value of managers) process.stderr.write(`--- manager ---\n${value.factories().map((entry) => `${entry.nodeId} ${entry.status} ${entry.error?.code ?? ""}`).join("\n")}\n`);
    process.stderr.write(`--- dials ---\n${connections.map((record) => `${record.node} closed=${record.closed} sent=[${frameTypes(record).join(",")}] got=[${record.received.join(",")}]`).join("\n")}\n`);
    for (const factory of factories) process.stderr.write(`--- factoryd ${factory.name} ---\n${factory.output.slice(-6000)}\n`);
    if (worker !== undefined) process.stderr.write(`--- wrangler ---\n${worker.transcript().slice(-6000)}\n`);
    process.exitCode = 1;
  } finally {
    for (const value of managers) { try { value.close(); } catch { /* closing is finite */ } }
    for (const factory of factories) await factory.stop("SIGKILL").catch(() => {});
    if (worker !== undefined) await worker.stop().catch(() => {});
  }
}

await main();
