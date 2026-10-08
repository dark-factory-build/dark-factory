import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { breakRoomNook, layoutScene, placeWorkers } from "../../dist/src/factory-scene/scene.js";
import { routeBetween } from "../../dist/src/factory-scene/movement.js";
import { workerFrames } from "../../dist/src/factory-scene/appearance.js";
import { hallsOf } from "../../../../fixtures/scene.mjs";
import { endsAt, messageAt, observe, send } from "../../dist/src/factory-scene/messages.js";

const task = (id, agentId, status) => ({ id, agentId, projectId: "p", title: id, status, humanRequestIds: [] });
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
    { key: "answer q0", kind: "answer", from: "grace", to: "ada", subject: "q0" },
    { key: "ask q1", kind: "ask", from: "grace", to: "ada", subject: "q1" },
    { key: "answer q2", kind: "answer", from: "grace", to: "ada", subject: "q2" },
  ], "an answer goes back the way the question came; a question to nobody on this floor goes nowhere");
  // The snapshot carries only the newest questions: one that drops out of it and comes back unchanged was asked long ago.
  const without = observe(first.seen, tasks, []);
  assert.deepEqual(observe(without.seen, tasks, [question("q0", "t-ada", "t-grace")]).events, []);
  assert.deepEqual(observe(without.seen, tasks, [question("q0", "t-ada", "t-grace", true)]).events.map(({ key }) => key), ["answer q0"]);
  assert.deepEqual(observe(first.seen, [...tasks, task("t-ada2", "ada", "queued")], [question("q0", "t-ada", "t-grace"), question("q4", "t-ada", "t-ada2")]).events, [], "nobody signals themselves");
});

test("only new recorded reads and writes fly, one per agent, never after a fresh look", () => {
  const cue = (key, agentId, reading) => ({ key, agentId, reading });
  const first = observe(undefined, [], [], [cue("old", "ada", true)]);
  assert.deepEqual(first.events, [], "what was recorded before the floor looked is history");
  const next = observe(first.seen, [], [], [cue("old", "ada", true), cue("r1", "ada", true), cue("r2", "ada", true), cue("w1", "grace", false), cue("op", "", false)]);
  assert.deepEqual(next.events, [
    { key: "read r2", kind: "read", to: "ada", subject: "r2" },
    { key: "post w1", kind: "post", from: "grace", subject: "w1" },
  ], "a read comes to the agent, a write leaves it; a burst is its newest; an operator's write has nobody to fly from");
  assert.deepEqual(observe(next.seen, [], [], [cue("r2", "ada", true)]).events, [], "each operation flies once");
  const post = send(next.events[1], 0, { x: 0, y: 0 }, undefined, 100);
  assert.equal(post.travel, 900, "recorded operations are paper, timed as handed-over work");
  assert.deepEqual([messageAt(post, { x: 100, y: 0 }, 100).hailing, messageAt(post, { x: 100, y: 0 }, 950).hailing], ["from", undefined], "the writer raises a hand as it posts; the board does not");
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
  const graph = hallsOf([..."abcdefghi"]);
  const workers = [
    { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "a" },
    { id: "grace", name: "Grace", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "h" },
    { id: "linus", name: "Linus", role: "worker", provider: "codex", activity: "idle", location: "resting" },
    { id: "margaret", name: "Margaret", role: "worker", provider: "codex", activity: "busy", location: "unobserved" },
  ];
  const tasks = [task("t-ada", "ada", "running"), task("t-grace", "grace", "running"), task("t-linus", "linus", "queued"), task("t-margaret", "margaret", "queued")];
  const layout = layoutScene(graph), placements = placeWorkers(layout, workers);
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
  const scene = (props) => createElement(FactoryScene, { graph, workers, tasks, appearance, ...props });
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

    const started = tasks.map((item) => item.status === "queued" ? { ...item, status: "running" } : item);
    const seat = renderer.root.findByProps({ "data-worker-id": "linus" }).props.transform;
    await act(async () => { renderer.update(scene({ tasks: started, peerQuestions: [question("q", "t-ada", "t-grace", true)] })); });
    await tick(16);
    const tray = all("data-floor-inbox")[0].props.transform.match(/translate\(([\d.]+) ([\d.]+)\)/).slice(1).map(Number).map((at) => at + 10);
    const paper = () => all("data-paper")[0]?.props.transform.match(/translate\(([-\d.]+) ([-\d.]+)\)/).slice(1).map(Number);
    assert.deepEqual(all("data-paper").map((node) => node.props["data-paper"]), ["margaret"], "paper flies to whoever is at work; the idle fetch their own");
    assert.ok(Math.hypot(paper()[0] - tray[0], paper()[1] - tray[1]) < 12, `paper starts at the tray: ${paper()} vs ${tray}`);
    assert.equal(all("data-call").length, 0, "nobody throws it");
    for (let step = 0; step < 80 && paper() !== undefined; step += 1) await tick(16);
    assert.match(poseOf("margaret"), /wave\.0$/, "whoever it is for takes it");
    for (let step = 0; step < 200 && !/Recorded use · at the task tray/.test(tooltipOf("linus")); step += 1) await tick(16);
    assert.match(tooltipOf("linus"), /Recorded use · at the task tray/, "Linus walked over to the tray for his work");
    for (let step = 0; step < 400 && renderer.root.findByProps({ "data-worker-id": "linus" }).props.transform !== seat; step += 1) await tick(50);
    assert.equal(renderer.root.findByProps({ "data-worker-id": "linus" }).props.transform, seat, "and went back to his seat");

    // With the clock stopped nothing flies, and what is waiting is still said.
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, workers, tasks, appearance: { scenery: "off", animation: "off" }, peerQuestions: [question("q", "t-ada", "t-grace", true), question("q2", "t-grace", "t-ada")] })); });
    assert.equal(all("data-pulse").length + all("data-call").length, 0);
    assert.match(tooltipOf("grace"), /\nAsked Ada · waiting for an answer$/);
    await act(async () => { renderer.update(createElement(FactoryScene, { graph, workers, tasks, appearance: { scenery: "off", animation: "off" }, peerQuestions: [question("q2", "t-grace", "t-ada"), question("q3", "t-grace", "t-ada")] })); });
    assert.equal(tooltipOf("grace").split("Asked Ada").length, 2, "two questions to the same person are said once");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});

test("recorded operations fly between the agent and the shelf or board, open what they used, and never replay", async () => {
  const graph = hallsOf([..."abc"]);
  const workers = [
    { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "a" },
    { id: "grace", name: "Grace", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "c" },
    { id: "linus", name: "Linus", role: "worker", provider: "codex", activity: "idle", location: "resting" },
  ];
  const tasks = [{ ...task("t-ada", "ada", "running"), roomIds: ["a"] }, { ...task("t-grace", "grace", "running"), roomIds: ["c"] }];
  const opened = [], boards = [];
  const cue = (key, agentId, board, reading) => ({ key, agentId, board, reading, label: key, open: () => opened.push(key) });
  const saved = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document };
  let clock = 1000, next = 0, renderer, reduced = false;
  const timers = new Map(), frames = new Map();
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = (callback) => { timers.set(++next, callback); return next; };
  globalThis.clearTimeout = (id) => timers.delete(id);
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  globalThis.window = { matchMedia: () => ({ matches: reduced, addEventListener() {}, removeEventListener() {} }) };
  globalThis.document = { visibilityState: "visible", addEventListener() {}, removeEventListener() {} };
  const tick = async (ms) => { await act(async () => { clock += ms; const bag = frames.size > 0 ? frames : timers; const [id, callback] = [...bag].at(-1); bag.delete(id); callback(clock); }); };
  const marks = (at) => renderer.root.findAll((node) => node.props["data-knowledge-cue"] === at);
  const point = (node) => node.props.transform.match(/translate\(([-\d.]+) ([-\d.]+)\)/).slice(1).map(Number);
  const near = (a, b, within = 16) => Math.hypot(a[0] - b[0], a[1] - b[1]) < within;
  const scene = (props) => createElement(FactoryScene, { graph, workers, tasks, appearance: { scenery: "off", animation: "follow-device" }, onOpenLibrary() {}, onOpenBoard: (project) => boards.push(project), ...props });
  try {
    await act(async () => { renderer = create(scene({ workers: [], tasks: [], connected: false })); });
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "ada", false, true)] })); });
    await tick(16);
    assert.equal(marks("flight").length, 0, "what was recorded before the floor looked does not fly");
    assert.equal(marks("agent").length, 1, "but it is marked beside the agent");
    const shelf = point(renderer.root.find((node) => node.props["data-break-room"] === "shelf"));
    const ada = () => point(renderer.root.findByProps({ "data-worker-id": "ada" }));

    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "ada", false, true), cue("r1", "ada", false, true), cue("x", "absent", false, true)] })); });
    await tick(16);
    assert.deepEqual(marks("flight").map((node) => node.props["data-knowledge-key"]), ["r1"], "a new read flies; an agent not on this floor gets no flight");
    assert.ok(near(point(marks("flight")[0]), shelf, 24), `it leaves the shelf: ${point(marks("flight")[0])} vs ${shelf}`);
    assert.deepEqual(marks("shelf").map((node) => node.props["data-knowledge-key"]), ["x"], "the shelf still marks the newest read");
    assert.ok(!marks("agent").some((node) => node.props["data-knowledge-key"] === "r1"), "nothing beside the agent while it flies");
    marks("flight")[0].props.onKeyDown({ key: "Enter", preventDefault() {} });
    assert.deepEqual(opened, ["r1"], "keyboard activation opens the exact record");
    const before = ada();
    let last;
    for (let step = 0; step < 80 && marks("flight").length > 0; step += 1) { last = point(marks("flight")[0]); await tick(16); }
    assert.ok(near(last, ada(), 24), `it lands where the agent already is: ${last} vs ${ada()}`);
    assert.deepEqual(ada(), before, "the agent never moves for it");
    assert.ok(marks("agent").some((node) => node.props["data-knowledge-key"] === "r1"), "then it rests beside the agent");

    // The writer is walking to another hall when it posts: the paper leaves the sprite, not either seat.
    const seat = point(renderer.root.findByProps({ "data-worker-id": "grace" }));
    const moved = workers.map((worker) => worker.id === "grace" ? { ...worker, nodeId: "a" } : worker);
    await act(async () => { renderer.update(scene({ workers: moved, knowledgeCues: [cue("r1", "ada", false, true)] })); });
    for (let step = 0; step < 15; step += 1) await tick(16);
    assert.equal(renderer.root.findByProps({ "data-worker-id": "grace" }).props["data-worker-action"], "walking");
    await act(async () => { renderer.update(scene({ workers: moved, knowledgeCues: [cue("r1", "ada", false, true), cue("w1", "grace", true, false)] })); });
    await tick(16);
    const grace = point(renderer.root.findByProps({ "data-worker-id": "grace" }));
    assert.ok(!near(grace, seat, 24), "she has left her seat");
    assert.ok(near(point(marks("flight")[0]), grace), `a post leaves the writer where she is drawn: ${point(marks("flight")[0])} vs ${grace}`);
    for (let step = 0; step < 80 && marks("flight").length > 0; step += 1) { last = point(marks("flight")[0]); await tick(16); }
    const board = point(renderer.root.findByProps({ "aria-label": "Open discussion board" }));
    assert.ok(near(last, [board[0] + 8, board[1] + 8], 24), `and lands at the board: ${last} vs ${board}`);

    // A question's pulse opens the Board that lists it.
    await act(async () => { renderer.update(scene({ knowledgeCues: [], peerQuestions: [question("q", "t-ada", "t-grace")] })); });
    for (let step = 0; step < 40 && renderer.root.findAll((node) => node.props["data-pulse"] !== undefined).length === 0; step += 1) await tick(16);
    renderer.root.find((node) => node.props["data-pulse"] === "ask").props.onKeyDown({ key: " ", preventDefault() {} });
    assert.deepEqual(boards, ["p"]);

    // After a dropped connection, what was recorded meanwhile is history.
    await act(async () => { renderer.update(scene({ connected: false, knowledgeCues: [] })); });
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("r2", "ada", false, true)] })); });
    for (let step = 0; step < 10; step += 1) await tick(16);
    assert.equal(marks("flight").length, 0, "nothing replays after a reconnect");

    // Reduced motion: no flights, only the static marks.
    reduced = true;
    await act(async () => renderer.unmount());
    await act(async () => { renderer = create(scene({ knowledgeCues: [] })); });
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("r3", "ada", false, true)] })); });
    assert.equal(marks("flight").length, 0);
    assert.deepEqual(marks("agent").map((node) => node.props["data-knowledge-key"]), ["r3"]);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});

test("a recorded read sends an idle worker to the shelf once; a busy one stays put; nothing replays", async () => {
  const graph = hallsOf([..."ab"]);
  const workers = [
    { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "a" },
    { id: "linus", name: "Linus", role: "worker", provider: "codex", activity: "idle", location: "resting" },
  ];
  const tasks = [{ ...task("t-ada", "ada", "running"), roomIds: ["a"] }];
  const cue = (key, agentId, board = false) => ({ key, agentId, board, reading: !board, label: key });
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
  const where = (id) => renderer.root.findByProps({ "data-worker-id": id }).props.transform;
  const pose = (id) => renderer.root.findByProps({ "data-worker-id": id }).findAllByType("use").map((use) => use.props.href);
  const stand = breakRoomNook(layoutScene(graph), 0, 0).furniture.find((piece) => piece.errand === "shelf").stand;
  // Scenery off: no ambient errands, so any walk to the shelf is the recorded one.
  const scene = (props) => createElement(FactoryScene, { graph, workers, tasks, appearance: { scenery: "off", animation: "follow-device" }, ...props });
  // Counts arrivals at the shelf over a stretch of floor time.
  const watch = async (ms) => { let arrivals = 0, there = where("linus") === `translate(${stand.x} ${stand.y})`; for (let spent = 0; spent < ms; spent += 50) { await tick(50); const at = where("linus") === `translate(${stand.x} ${stand.y})`; if (at && !there) arrivals += 1; there = at; } return arrivals; };
  try {
    await act(async () => { renderer = create(scene({ workers: [], tasks: [], connected: false })); });
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "linus")] })); });
    const seat = where("linus"), bench = where("ada");
    assert.equal(await watch(4000), 0, "what was recorded before the floor looked sends nobody anywhere");
    await act(async () => { renderer.update(scene({ connected: false, knowledgeCues: [cue("old", "linus")] })); });
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "linus"), cue("missed", "linus")] })); });
    assert.equal(await watch(4000), 0, "nor does what was recorded while disconnected");

    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "linus"), cue("missed", "linus"), cue("r1", "linus"), cue("w1", "ada", true)] })); });
    await tick(16);
    assert.deepEqual(renderer.root.findAll((node) => node.props["data-knowledge-cue"] === "flight").map((node) => node.props["data-knowledge-key"]), ["w1"], "the busy writer's post flies; the idle reader goes in person");
    let arrived = false;
    for (let step = 0; step < 300 && !arrived; step += 1) { await tick(16); arrived = where("linus") === `translate(${stand.x} ${stand.y})`; }
    assert.ok(arrived, "the reader walks to the shelf");
    assert.ok(pose("linus").includes("#df-frame-person.held.book.chest"), "and stands there, book in hand");
    const cues = (at) => renderer.root.findAll((node) => node.props["data-knowledge-cue"] === at);
    assert.deepEqual(cues("agent").filter((node) => node.props["data-knowledge-key"] === "r1").length, 1, "marked as recorded, unlike an ambient errand");
    assert.deepEqual(cues("shelf"), [], "one mark per event: the visitor carries it, not the shelf as well");
    assert.deepEqual(cues("board").map((node) => node.props["data-knowledge-key"]), ["w1"], "the busy writer's post is marked on the board it flew to");
    // No mark covers a sign: not the one over a visitor's head at any implement, nor the one above the implement itself.
    const xy = (node) => node.props.transform.match(/translate\(([-\d.]+) ([-\d.]+)\)/).slice(1).map(Number);
    const box = ([x, y]) => ({ left: x - 9, right: x + 9, top: y - 9, bottom: y + 9 });
    const [overHead, overImplement] = [xy(cues("agent").find((node) => node.props["data-knowledge-key"] === "r1")), xy(cues("board")[0])];
    const nook = breakRoomNook(layoutScene(graph), 0, 0).furniture, board = nook.find((piece) => piece.errand === "board");
    const signs = { board: "BOARD", missions: "MISSIONS", tasks: "TASKS", shelf: "LIBRARY" };
    for (const piece of nook.filter((item) => signs[item.errand])) {
      // The sign: 4 sprite units of text, scaled to the floor, under the implement's 16-unit frame.
      const scale = 20 / 16, width = signs[piece.errand].length * 0.6 * 4 * scale, base = piece.y + 23 * scale;
      const sign = { left: piece.x + 8 * scale - width / 2, right: piece.x + 8 * scale + width / 2, top: base - 4 * scale, bottom: base + scale };
      for (const [what, mark] of [["over a visitor", box([overHead[0] - stand.x + piece.stand.x, overHead[1] - stand.y + piece.stand.y])], ["over the implement", box([overImplement[0] - board.x + piece.x, overImplement[1] - board.y + piece.y])]]) {
        assert.ok(mark.right <= sign.left || mark.left >= sign.right || mark.bottom <= sign.top || mark.top >= sign.bottom, `the mark ${what} covers the ${piece.errand} sign: ${JSON.stringify([mark, sign])}`);
      }
    }
    assert.equal(where("ada"), bench, "the busy writer is never moved");
    await act(async () => { renderer.update(scene({ knowledgeCues: [cue("old", "linus"), cue("missed", "linus"), cue("r1", "linus"), cue("w1", "ada", true)] })); });
    assert.equal(await watch(12000), 0, "the visit is brief and happens once");
    assert.equal(where("linus"), seat, "then the reader sits back down");
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});
