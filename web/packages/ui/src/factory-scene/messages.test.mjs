import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { layoutScene, placeWorkers } from "../../dist/src/factory-scene/scene.js";
import { routeBetween } from "../../dist/src/factory-scene/movement.js";
import { workerFrames } from "../../dist/src/factory-scene/appearance.js";
import { endsAt, messageAt, observe, send } from "../../dist/src/factory-scene/messages.js";

const task = (id, agentId, status) => ({ id, agentId, projectId: "p", title: id, status, roomIds: [], humanRequestIds: [] });
const question = (id, source, target, answered = false) => ({ id, source_task_id: source, target_task_id: target, answered, revision: 1n });

test("only what changes between two looks at the floor is news", () => {
  const tasks = [task("t-ada", "ada", "running"), task("t-grace", "grace", "queued"), task("t-shared", "", "queued")];
  const first = observe(undefined, tasks, [question("q0", "t-ada", "t-grace")]);
  assert.deepEqual(first.events, [], "the first look is history");
  assert.deepEqual(observe(first.seen, tasks, [question("q0", "t-ada", "t-grace")]).events, [], "nothing changed");
  const started = [task("t-ada", "ada", "running"), task("t-grace", "grace", "running"), task("t-shared", "", "running"), task("t-new", "linus", "running")];
  assert.deepEqual(observe(first.seen, started, [question("q0", "t-ada", "t-grace")]).events, [{ key: "assign t-grace", kind: "assign", to: "grace" }],
    "work is handed over when it was seen waiting and now runs for someone: not work with nobody, nor work first seen already running");
  assert.deepEqual(observe(first.seen, tasks, [question("q0", "t-ada", "t-grace", true), question("q1", "t-grace", "t-ada"), question("q2", "t-ada", "t-grace", true), question("q3", "t-ada", "t-gone"), question("q4", "t-ada", "t-ada2")]).events, [
    { key: "answer q0", kind: "answer", from: "grace", to: "ada" },
    { key: "ask q1", kind: "ask", from: "grace", to: "ada" },
    { key: "answer q2", kind: "answer", from: "grace", to: "ada" },
  ], "an answer goes back the way the question came; a question to nobody on this floor goes nowhere");
  // The snapshot carries only the newest questions: one that drops out of it and comes back unchanged was asked long ago.
  const without = observe(first.seen, tasks, []);
  assert.deepEqual(observe(without.seen, tasks, [question("q0", "t-ada", "t-grace")]).events, []);
  assert.deepEqual(observe(without.seen, tasks, [question("q0", "t-ada", "t-grace", true)]).events.map(({ key }) => key), ["answer q0"]);
  assert.deepEqual(observe(first.seen, [...tasks, task("t-ada2", "ada", "queued")], [question("q0", "t-ada", "t-grace"), question("q4", "t-ada", "t-ada2")]).events, [], "nobody signals themselves");
});

test("a pulse keeps to its route, paper flies over, and each end raises a hand in turn", () => {
  const route = { points: [{ x: 0, y: 100 }, { x: 300, y: 100 }], length: 400 };
  const pulse = send({ key: "ask q", kind: "ask", from: "ada", to: "grace" }, 1000, { x: 0, y: 0 }, route, 316);
  assert.equal(messageAt(pulse, { x: 300, y: 100 }, 999).point, undefined);
  const leaving = messageAt(pulse, { x: 300, y: 100 }, 1100);
  assert.deepEqual([leaving.point, leaving.sending, leaving.hailing], [undefined, true, "from"], "the hand goes up before it leaves");
  let last = -1;
  for (let at = 1250; at < 1250 + pulse.travel; at += 40) {
    const { point, sending, landed } = messageAt(pulse, { x: 300, y: 100 }, at);
    assert.ok(point.x === 0 || point.y === 100, `off its route at ${at}: ${JSON.stringify(point)}`);
    const gone = point.y + point.x; assert.ok(gone > last); last = gone;
    assert.deepEqual([sending, landed], [true, false]);
  }
  const arrived = messageAt(pulse, { x: 300, y: 100 }, 1250 + pulse.travel + 10);
  assert.deepEqual([arrived.point, arrived.sending, arrived.hailing, arrived.landed], [undefined, false, "to", true]);
  const after = messageAt(pulse, { x: 300, y: 100 }, endsAt(pulse) + 1);
  assert.deepEqual([after.point, after.sending, after.hailing, after.landed], [undefined, false, undefined, false]);
  const paper = send({ key: "assign t", kind: "assign", to: "grace" }, 0, { x: 0, y: 0 }, undefined, 100);
  const mid = messageAt(paper, { x: 100, y: 0 }, 450);
  assert.ok(mid.point.x > 40 && mid.point.x < 60 && mid.point.y < -20 && mid.point.y >= -25, "paper arcs over the floor, no higher than the throw is long");
  assert.deepEqual([mid.sending, mid.hailing, mid.trail], [false, undefined, []], "nobody throws it: it comes from the tray");
  assert.equal(messageAt(paper, { x: 100, y: 0 }, 950).hailing, "to");
  // A raised hand interrupts bench work and table habits alike, with nothing else in it but what is carried.
  const worker = { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "busy" };
  for (const [motion, seat] of [[{ action: "interacting", frame: 1, at: 0 }, undefined], [{ action: "still", frame: 0, at: 0 }, "resting"], [{ action: "still", frame: 0, at: 0 }, "planning"]]) {
    for (const hailing of [0, 1]) assert.ok(workerFrames(worker, motion, seat, undefined, undefined, hailing).some((name) => name.endsWith(`.wave.${hailing}`)), `${seat}/${hailing}`);
    assert.ok(!workerFrames(worker, motion, seat, undefined, undefined, 1).some((name) => name.startsWith("person.held.")));
  }
  assert.ok(workerFrames(worker, { action: "walking", frame: 0, direction: "south", at: 0 }, undefined, undefined, undefined, 1).some((name) => name.endsWith(".walk.0")), "nobody stops walking to wave");
});

test("the floor sends a question down the corridors, its answer back, and new work from the tray", async () => {
  const topology = { digest: "mail", nodes: [..."abcdefghi"].map((id) => ({ id, parentId: "", path: id, label: id, kind: "package", sizeBucket: "small", inventoryScope: "direct", inventory: { direct: { source: 9, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 }, total: { source: 9, tests: 0, documentation: 0, configuration: 0, assets: 0, unclassified: 0 }, samples: [], samples_omitted: 0 } })) };
  const workers = [
    { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "a" },
    { id: "grace", name: "Grace", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "h" },
    { id: "linus", name: "Linus", role: "worker", provider: "codex", activity: "idle", location: "resting" },
  ];
  const tasks = [{ ...task("t-ada", "ada", "running"), roomIds: ["a"] }, { ...task("t-grace", "grace", "running"), roomIds: ["h"] }, task("t-linus", "linus", "queued")];
  const layout = layoutScene(topology), placements = placeWorkers(layout, workers);
  const route = routeBetween(layout, placements.find(({ id }) => id === "ada"), placements.find(({ id }) => id === "grace"));
  const onRoute = (point) => { let from = placements.find(({ id }) => id === "ada"); for (const to of route.points) { const d = Math.hypot(to.x - from.x, to.y - from.y), d1 = Math.hypot(point.x - from.x, point.y - from.y), d2 = Math.hypot(to.x - point.x, to.y - point.y); if (Math.abs(d1 + d2 - d) < 0.01) return true; from = to; } return false; };

  const saved = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document };
  let clock = 1000, next = 0, renderer;
  const timers = new Map(), frames = new Map();
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = (callback) => { timers.set(++next, callback); return next; };
  globalThis.clearTimeout = (id) => timers.delete(id);
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  globalThis.window = { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) };
  globalThis.document = { visibilityState: "visible", addEventListener() {}, removeEventListener() {} };
  const tick = async (ms) => { await act(async () => { clock += ms; const bag = frames.size > 0 ? frames : timers; const [id, callback] = [...bag].at(-1); bag.delete(id); callback(clock); }); };
  const all = (name) => renderer.root.findAll((node) => node.props[name] !== undefined);
  const poseOf = (id) => renderer.root.findByProps({ "data-worker-id": id }).findAllByType("use").map((use) => use.props.href).find((href) => href.includes("person.skin."));
  const tooltipOf = (id) => renderer.root.findByProps({ "data-worker-id": id }).findAll((node) => node.props["data-tooltip"] !== undefined)[0].props["data-tooltip"];
  // Pets off: only messages may ask for animation frames here.
  const appearance = { scenery: "off", animation: "follow-device" };
  const scene = (props) => createElement(FactoryScene, { topology, workers, tasks, appearance, ...props });
  try {
    // The console mounts its floor before any state has arrived, then gets state and "connected" in one render.
    await act(async () => { renderer = create(scene({ workers: [], tasks: [], peerQuestions: [], connected: false })); });
    await act(async () => { renderer.update(scene({ peerQuestions: [question("old", "t-grace", "t-ada", true), question("open", "t-grace", "t-ada")] })); });
    await tick(200);
    await tick(200);
    assert.equal(all("data-pulse").length + all("data-paper").length + all("data-call").length, 0, "what was already so when the floor opened is not replayed");
    assert.match(tooltipOf("grace"), /Asked Ada · waiting for an answer/, "but it is said");
    // The same after a dropped connection: what happened meanwhile is history by the time it is seen.
    await act(async () => { renderer.update(scene({ connected: false, peerQuestions: [question("old", "t-grace", "t-ada", true), question("open", "t-grace", "t-ada")] })); });
    await act(async () => { renderer.update(scene({ peerQuestions: [question("old", "t-grace", "t-ada", true), question("open", "t-grace", "t-ada", true), question("missed", "t-ada", "t-grace")] })); });
    await tick(200);
    await tick(200);
    assert.equal(all("data-pulse").length + all("data-call").length, 0, "nor is what happened while disconnected");
    await act(async () => { renderer.update(scene({ peerQuestions: [question("old", "t-grace", "t-ada", true)] })); });
    await tick(200);

    await act(async () => { renderer.update(scene({ peerQuestions: [question("q", "t-ada", "t-grace")] })); });
    await tick(16);
    assert.match(poseOf("ada"), /wave\.1$/, "the asker raises a hand");
    assert.deepEqual(all("data-call").map((node) => [node.props["data-call"], node.props["data-bubble"]]), [["ada", "ask"]]);
    assert.match(tooltipOf("ada"), /\nAsked Grace · waiting for an answer$/);
    assert.match(tooltipOf("grace"), /\nHas a question from Ada$/);
    let flown = 0, answered = false;
    for (let step = 0; step < 600 && !answered; step += 1) {
      await tick(16);
      const pulses = all("data-pulse");
      if (pulses.length === 1) {
        flown += 1;
        assert.equal(pulses[0].props["data-pulse"], "ask");
        assert.ok(frames.size > 0, "a pulse in flight is drawn every frame");
        const core = pulses[0].findAllByType("circle").at(-1).props;
        assert.ok(onRoute({ x: core.cx, y: core.cy }), `the pulse left the corridors at ${JSON.stringify(core)}`);
        assert.doesNotMatch(poseOf("grace"), /wave/);
      } else if (flown > 0) {
        assert.match(poseOf("grace"), /wave\.0$/, "whoever is asked looks up as it lands");
        assert.deepEqual(all("data-call").map((node) => [node.props["data-call"], node.props["data-bubble"]]), [["grace", "hum"]]);
        answered = true;
      }
    }
    assert.ok(flown > 20 && answered, `${flown}`);
    for (let step = 0; step < 120; step += 1) await tick(16);
    assert.equal(all("data-call").length, 0);
    assert.equal(frames.size, 0, "a quiet floor goes back to its slow pulse");
    assert.doesNotMatch(poseOf("ada") + poseOf("grace"), /wave/);

    await act(async () => { renderer.update(scene({ peerQuestions: [question("q", "t-ada", "t-grace", true)] })); });
    await tick(16);
    assert.match(poseOf("grace"), /wave\.1$/);
    assert.deepEqual(all("data-call").map((node) => [node.props["data-call"], node.props["data-bubble"]]), [["grace", "tell"]]);
    assert.doesNotMatch(tooltipOf("ada"), /waiting for an answer/);
    for (let step = 0; step < 40; step += 1) await tick(16);
    assert.equal(all("data-pulse")[0].props["data-pulse"], "answer");
    for (let step = 0; step < 700 && frames.size > 0; step += 1) await tick(16);

    const started = tasks.map((item) => item.id === "t-linus" ? { ...item, status: "running" } : item);
    await act(async () => { renderer.update(scene({ tasks: started, peerQuestions: [question("q", "t-ada", "t-grace", true)] })); });
    await tick(16);
    const tray = all("data-floor-inbox")[0].props.transform.match(/translate\(([\d.]+) ([\d.]+)\)/).slice(1).map(Number);
    const paper = () => all("data-paper")[0]?.props.transform.match(/translate\(([-\d.]+) ([-\d.]+)\)/).slice(1).map(Number);
    assert.ok(Math.hypot(paper()[0] - tray[0] - 10, paper()[1] - tray[1] + 4) < 12, `paper starts at the tray: ${paper()} vs ${tray}`);
    assert.equal(all("data-call").length, 0, "nobody throws it");
    for (let step = 0; step < 80 && paper() !== undefined; step += 1) await tick(16);
    assert.match(poseOf("linus"), /wave\.0$/, "whoever it is for takes it");

    // With the clock stopped nothing flies, and what is waiting is still said.
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, tasks, appearance: { scenery: "off", animation: "off" }, peerQuestions: [question("q", "t-ada", "t-grace", true), question("q2", "t-grace", "t-ada")] })); });
    assert.equal(all("data-pulse").length + all("data-call").length, 0);
    assert.match(tooltipOf("grace"), /\nAsked Ada · waiting for an answer$/);
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, tasks, appearance: { scenery: "off", animation: "off" }, peerQuestions: [question("q2", "t-grace", "t-ada"), question("q3", "t-grace", "t-ada")] })); });
    assert.equal(tooltipOf("grace").split("Asked Ada").length, 2, "two questions to the same person are said once");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});
