import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { FactoryScene } from "../../dist/src/factory-scene/factory-scene.js";
import { catAt, catBed, chats, gossip } from "../../dist/src/factory-scene/idle-life.js";
import { workerFrames } from "../../dist/src/factory-scene/appearance.js";
import { spriteAtlas } from "../../dist/src/factory-scene/sprites/sprites.generated.js";

const seats = (ids, free = () => true) => ids.map((id, slot) => ({ x: 72 + slot * 40, y: 400, id, free: id !== undefined && free(id) }));
const HOUR = 3_600_000;

test("the cat keeps to its table, prowls only behind it, and is stroked only by someone free to", () => {
  const row = seats(["ada", undefined, "grace", "linus"], (id) => id !== "grace");
  assert.deepEqual(catAt(row, undefined), { x: 200, y: 370, frame: "sleep.0", west: false, moving: false }, "a stopped clock finds it asleep in its bed");
  assert.deepEqual(catBed(row), { x: 200, y: 370 }, "behind the far end of the table, away from the room's sign");
  assert.equal(catAt(row.slice(0, 2), 0).moving, false, "one gap is still somewhere to be");
  assert.equal(catAt(row.slice(0, 1), 0), undefined, "no table, no cat");
  const frames = new Set(), petters = new Set(), spots = new Set(), naps = new Set();
  let previous;
  for (let at = 0; at < HOUR; at += 100) {
    const cat = catAt(row, at);
    frames.add(cat.frame);
    assert.ok(cat.x >= 72 && cat.x <= 200 && cat.y <= 400 && cat.y >= 370, `off the table at ${at}`);
    if (cat.moving) {
      assert.match(cat.frame, /^walk/);
      // Along the floor behind the table or straight up and down between two seats: never across anyone.
      if (previous?.moving && cat.x !== previous.x) assert.ok(cat.y === 370 || previous.y === 370, `crossed the table at ${at}`);
      if (previous?.moving && cat.x !== previous.x) assert.equal(cat.west, cat.x < previous.x, `walked backwards at ${at}`);
    } else {
      spots.add(`${cat.x} ${cat.y}`);
    }
    if (cat.frame.startsWith("sleep")) naps.add(cat.y);
    if (cat.pettedBy !== undefined) { assert.equal(cat.y, 400, "nobody reaches over the back of their seat"); petters.add(cat.pettedBy); assert.equal(cat.frame, "pet"); }
    assert.equal(cat.frame === "pet", cat.pettedBy !== undefined);
    previous = cat;
  }
  assert.deepEqual([...naps].sort(), [370, 400], "it sleeps in its bed, and sometimes where it sat");
  // With everyone free to, still nobody reaches over the back of their seat.
  let stroked = 0;
  for (let at = 0; at < HOUR; at += 500) { const cat = catAt(seats(["ada", "grace", "linus", "ken"]), at); if (cat.pettedBy !== undefined) { stroked += 1; assert.equal(cat.y, 400, `stroked in its bed at ${at}`); } }
  assert.ok(stroked > 0);
  assert.deepEqual([...frames].sort(), ["pet", "sit.0", "sit.1", "sleep.0", "sleep.1", "walk.0", "walk.1"]);
  for (const frame of frames) assert.ok(`cat.${frame}` in spriteAtlas.frames);
  assert.deepEqual([...spots].sort(), ["112 400", "152 400", "200 370", "72 400"], "every gap of the table gets its turn, and so does the bed; the far end never");
  assert.deepEqual([...petters].sort(), ["ada"], "an empty seat and a busy neighbour stroke nothing");
});

test("a worker stroking the cat reaches for it with an empty hand, unless someone needs them", () => {
  const worker = { id: "ada", name: "Ada", role: "worker", provider: "codex", activity: "idle" };
  const stroking = [0, 400].map((stroking) => workerFrames(worker, { action: "still", frame: 0, at: 0 }, "resting", undefined, stroking));
  assert.ok(stroking[0].some((name) => /^person\.skin\.\d\.pet\.0$/.test(name)));
  assert.ok(stroking[1].some((name) => /^person\.skin\.\d\.pet\.1$/.test(name)), "the hand moves");
  for (const frames of stroking) assert.ok(!frames.some((name) => name.startsWith("person.held.") || name.startsWith("person.tool.")));
  assert.ok(workerFrames({ ...worker, activity: "needs-you" }, { action: "still", frame: 0, at: 0 }, "resting", undefined, 0).some((name) => /wave/.test(name)));
});

test("gossip says only what is true of the floor", () => {
  const workers = [
    { id: "ada", name: "Ada", role: "worker", activity: "busy" },
    { id: "grace", name: "Grace", role: "worker", activity: "needs-you" },
    { id: "linus", name: "Linus", role: "worker", activity: "idle", paused: true },
  ];
  const task = (id, agentId, status, title = id) => ({ id, agentId, projectId: "p", title, status, roomIds: [], humanRequestIds: [] });
  assert.deepEqual(gossip(workers, []), [
    { line: "Grace is still waiting on an answer.", reply: "Aren't we all.", about: ["grace"] },
    { line: "Linus has been paused.", reply: "Lucky them.", about: ["linus"] },
  ]);
  assert.deepEqual(gossip(workers, [task("t1", "ada", "running", "Rewrite the whole scheduler from first principles, again"), task("t2", "gone", "running"), task("t3", "ada", "queued"), task("t4", "ada", "queued"), task("t5", "ada", "blocked"), task("t6", "ada", "succeeded")]).map(({ line }) => line), [
    "Ada has “Rewrite the whole scheduler from fi…”.",
    "Grace is still waiting on an answer.",
    "Linus has been paused.",
    "2 waiting in the tray.",
    "1 blocked, I hear.",
  ]);
  assert.deepEqual(gossip([], []), []);
});

test("neighbours talk one conversation at a time, in turn, and never about themselves", () => {
  const ids = ["ada", "grace", "linus", "ken", "barbara"];
  const news = [{ line: "Ada has “x”.", reply: "Rather them than me.", about: ["ada"] }, { line: "3 waiting in the tray.", reply: "I can't see it from here." }];
  assert.deepEqual(chats(seats(ids), news, undefined), [], "a stopped clock says nothing");
  assert.deepEqual(chats(seats(ids, () => false), news, 5000), []);
  for (let at = 0; at < HOUR; at += 500) {
    const talking = chats(seats(ids), news, at).flatMap(({ between }) => between);
    assert.equal(new Set(talking).size, talking.length, `someone is in two conversations at ${at}`);
  }
  const glyphs = new Set(), lines = new Set(), pairs = new Set();
  let lastWords = 0;
  for (let at = 0; at < HOUR; at += 100) {
    const row = seats(["ada", "grace", undefined, "linus", "ken", "barbara"], (id) => id !== "ken");
    const found = chats(row, news, at);
    const talking = found.flatMap(({ between }) => between);
    assert.equal(new Set(talking).size, talking.length, `someone is in two conversations at ${at}`);
    for (const { between, remark, speaking, replied } of found) {
      pairs.add([...between].sort().join("+"));
      lines.add(remark.line);
      assert.ok(!remark.about?.some((id) => between.includes(id)), `${between} gossip about themselves`);
      if (speaking === undefined) continue;
      glyphs.add(speaking.glyph);
      assert.ok(between.includes(speaking.id));
      assert.ok(`bubble.${speaking.glyph}` in spriteAtlas.frames);
      if (!replied) assert.equal(speaking.id, between[0], "the opener speaks first");
      if (replied && speaking.id === between[0]) lastWords += 1;
    }
  }
  // Across an empty seat, or with someone who is not free, there is nobody to talk to.
  assert.deepEqual([...pairs].sort(), ["ada+grace"]);
  assert.deepEqual([...glyphs].sort(), ["ask", "hum", "joke", "tell"]);
  assert.ok(lines.has("3 waiting in the tray.") && !lines.has("Ada has “x”.") && lines.size > 3, "news and small talk both get said");
  assert.ok(lastWords > 0);
});

test("the floor shows the cat, what is said and the heart, and none of it without scenery or a clock", async () => {
  const topology = { digest: "idle", nodes: [..."abcdefghi"].map((id) => ({ id, parentId: "", path: id, label: id, kind: "package", sizeBucket: "small" })) };
  const workers = ["ada", "grace", "linus"].map((id) => ({ id, name: id[0].toUpperCase() + id.slice(1), role: "worker", provider: "codex", activity: "idle", location: "resting" }));
  const still = renderToStaticMarkup(createElement(FactoryScene, { topology, workers }));
  // The server's clock is 0: the cat is wherever this epoch's haunt is, settled.
  assert.match(still, /data-cat="(sit|sleep)\.0"/);
  assert.match(still, /The cat\n(Supervising|Asleep)/);
  assert.doesNotMatch(still, /data-bubble|data-heart/);
  assert.match(still, /data-cat-bed/);
  assert.doesNotMatch(renderToStaticMarkup(createElement(FactoryScene, { topology, workers, appearance: { scenery: "off", animation: "follow-device" } })), /data-cat/, "no scenery: no cat, no bed");

  const saved = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, performance: globalThis.performance, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, window: globalThis.window, document: globalThis.document };
  let clock = 0, next = 0, renderer;
  const timers = new Map(), frames = new Map();
  globalThis.performance = { now: () => clock };
  globalThis.setTimeout = (callback) => { timers.set(++next, callback); return next; };
  globalThis.clearTimeout = (id) => timers.delete(id);
  globalThis.requestAnimationFrame = (callback) => { frames.set(++next, callback); return next; };
  globalThis.cancelAnimationFrame = (id) => frames.delete(id);
  globalThis.window = { matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) };
  globalThis.document = { visibilityState: "visible", addEventListener() {}, removeEventListener() {} };
  try {
    await act(async () => { renderer = create(createElement(FactoryScene, { topology, workers, tasks: [{ id: "t", agentId: "nobody", projectId: "p", title: "t", status: "queued", roomIds: [], humanRequestIds: [] }] })); });
    const seen = { bubbles: new Set(), said: new Set(), cat: new Set(), hearts: 0, smooth: 0 };
    let wasted = 0;
    for (let tick = 0; tick < 6000; tick += 1) {
      const moving = frames.size > 0;
      await act(async () => { clock += moving ? 50 : 200; const [id, callback] = [...(moving ? frames : timers)].at(-1); (moving ? frames : timers).delete(id); callback(clock); });
      const cat = renderer.root.findByProps({ "data-cat": renderer.root.findAll((node) => node.props["data-cat"] !== undefined)[0].props["data-cat"] }).props;
      seen.cat.add(cat["data-cat"]);
      // Off the table it is behind the people at it; on the table, in front of them.
      const order = renderer.root.findAll((node) => node.props["data-cat"] !== undefined || node.props["data-worker-id"] !== undefined).map((node) => node.props["data-cat"] !== undefined);
      const seatY = renderer.root.findAll((node) => node.props["data-common-seat"] === "resting")[0].props.transform.match(/ ([\d.]+)\)/)[1];
      assert.equal(order.indexOf(true), cat.transform.includes(` ${seatY})`) ? order.length - 1 : 0, `cat layered wrongly at ${clock}`);
      if (cat["data-cat"].startsWith("walk")) { seen.smooth += 1; assert.ok(frames.size > 0, "a prowling cat is drawn every frame"); }
      // Someone setting off for the bookshelf asks for frames a render before they are drawn walking.
      else { wasted = frames.size > 0 && renderer.root.findAllByProps({ "data-worker-action": "walking" }).length === 0 ? wasted + 1 : 0; assert.ok(wasted < 3, "a settled cat costs no more than the floor's slow pulse"); }
      for (const bubble of renderer.root.findAll((node) => node.props["data-bubble"] !== undefined)) seen.bubbles.add(bubble.props["data-bubble"]);
      const hearts = renderer.root.findAll((node) => node.props["data-heart"] !== undefined);
      seen.hearts += hearts.length;
      for (const worker of renderer.root.findAll((node) => node.props["data-tooltip"]?.includes(": "))) seen.said.add(worker.props["data-tooltip"].split("\n").slice(2).join("\n"));
      if (hearts.length > 0) {
        assert.match(cat["data-tooltip"], /^The cat\nBeing fussed over by (Ada|Grace)$/);
        const fussing = renderer.root.findAll((node) => node.props["data-tooltip"]?.includes("fussing the cat"));
        assert.equal(fussing.length, 1);
        assert.ok(fussing[0].findAllByType("use").some((use) => /pet\.[01]$/.test(use.props.href)), "the one with the heart is the one reaching for the cat");
        assert.ok(!fussing[0].findAllByType("use").some((use) => use.props.href.includes("person.held.")));
        assert.ok(renderer.root.findAll((node) => node.props["data-table-item"] !== undefined).some((item) => item.props.transform.startsWith(fussing[0].parent.props.transform)), "what they had stays on the table");
      }
    }
    assert.ok(seen.hearts > 0 && seen.smooth > 0);
    assert.ok(seen.bubbles.size >= 3, [...seen.bubbles].join());
    assert.ok([...seen.said].some((said) => /^\w+: 1 waiting in the tray\.\n\w+: I can't see it from here\.$/.test(said)), [...seen.said].join(" | "));
    assert.ok([...seen.said].some((said) => /^\w+: [^\n]+$/.test(said)), "the opener is heard before the reply");
    // An empty floor's clock is stopped, whatever moment it is mounted at: even mid-prowl, nothing asks for frames.
    for (let at = 0; at < 180_000; at += 500) {
      await act(async () => { clock = 10_000_000 + at; renderer.update(createElement(FactoryScene, { topology, workers: [], tasks: [{ id: String(at), agentId: "nobody", projectId: "p", title: "t", status: "queued", roomIds: [], humanRequestIds: [] }] })); });
      assert.equal(frames.size, 0, `an empty floor asked for frames at ${at}`);
    }
    await act(async () => { renderer.update(createElement(FactoryScene, { topology, workers, appearance: { scenery: "rich", animation: "off" } })); });
    assert.equal(renderer.root.findAll((node) => node.props["data-cat"] !== undefined)[0].props["data-cat"], "sleep.0", "animation off: the cat sleeps");
    assert.equal(renderer.root.findAll((node) => node.props["data-bubble"] !== undefined || node.props["data-heart"] !== undefined).length, 0);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; }
  }
});

test("someone sitting down or leaving moves the cat only if its gap is gone, and an empty floor finds it asleep", () => {
  let kept = 0;
  for (let at = 0; at < HOUR; at += 1000) {
    const four = catAt(seats(["ada", "grace", "linus", "ken"]), at), five = catAt(seats(["ada", "grace", "linus", "ken", "barbara"]), at);
    // Settled on a gap both tables have, it is in the same place at both. (Its bed stands behind the last gap, so that moves with the table's end.)
    if (!five.moving && !four.moving && five.y === 400 && five.x <= 152) { kept += 1; assert.deepEqual([four.x, four.y], [five.x, five.y], `jumped at ${at}`); }
  }
  assert.ok(kept > 100);
  const topology = { digest: "empty", nodes: [{ id: "repo", parentId: "", path: ".", label: "Repository", kind: "repository", sizeBucket: "large" }] };
  const empty = renderToStaticMarkup(createElement(FactoryScene, { topology, workers: [] }));
  assert.match(empty, /data-cat="sleep.0"[^>]*aria-label="The cat, asleep"/);
});
