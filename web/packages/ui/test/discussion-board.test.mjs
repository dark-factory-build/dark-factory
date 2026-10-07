import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create } from "react-test-renderer";
import { FactoryConsole } from "../dist/src/index.js";
import { FactoryFloor } from "../dist/src/console-screens.js";
import { ProjectLibrary } from "../dist/src/project-library.js";
import { CUE_LIMIT, CUE_MS, activityTarget, useKnowledgeActivity } from "../dist/src/project-board.js";
import { FactoryScene } from "../dist/src/factory-scene/factory-scene.js";
import { fixtureState, fixtureGraphs } from "../../../fixtures/state.mjs";
import { hall, machine, sceneGraph } from "../../../fixtures/scene.mjs";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const words = (node) => typeof node === "string" ? node : (node.children ?? []).map(words).join("");
const project = [...fixtureState.projects.keys()][0];
const [builder, , reader] = [...fixtureState.agents.keys()];
const runningTask = [...fixtureState.tasks.values()].find((task) => task.status === "running").id;
const doneTask = [...fixtureState.tasks.values()].find((task) => task.status === "succeeded").id;
const row = (operation, id, at, extra = {}) => ({ operation, content_id: id.repeat(16), content_revision: 2, kind: "lesson", title: `Doc ${id}`, author: "operator:local", agent_id: "", task_id: "", run_id: "", thread_id: "", at_ms: at, ...extra });
const withDocument = async (work) => {
  const previous = globalThis.document;
  globalThis.document = Object.assign(new EventTarget(), { visibilityState: "visible" });
  try { await work(); } finally { if (previous === undefined) delete globalThis.document; else globalThis.document = previous; }
};

test("the Board is its own destination; the Library keeps documents only", async () => {
  await withDocument(async () => {
    const calls = [];
    let tree;
    await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, onDetail() {}, onProjectContent: async (operation, input) => { calls.push({ operation, input }); return { items: [] }; } })); });
    const nav = (label) => tree.root.findByProps({ "aria-label": label }).findAllByType("button").map(words);
    assert.ok(nav("Console views").includes("Board") && nav("Console views").includes("Library"));
    assert.ok(nav("Right panel").includes("Board") && nav("Right panel").includes("Library"));
    await act(async () => tree.root.findByProps({ "aria-label": "Right panel" }).findAllByType("button").find((button) => words(button) === "Board").props.onClick());
    const dialog = () => tree.root.findByType("dialog");
    assert.equal(dialog().props["aria-label"], "Discussion board");
    assert.equal(words(dialog().findByType("h2")), "Board");
    assert.match(words(dialog()), /Ask questions, share findings and follow replies/);
    assert.equal(tree.root.findAllByProps({ "aria-label": "Library views" }).length, 0, "no Library tabs inside the Board");
    assert.ok(dialog().findByProps({ "aria-label": "Direct questions" }));
    assert.ok(dialog().findAllByType("button").some((button) => words(button) === "Start a thread"));
    assert.equal(calls.findLast((call) => call.operation === "search").input.kind, "discussion");
    // The floor's board opens the same section directly.
    await act(async () => tree.root.findByType(FactoryFloor).props.onOpenBoard(project));
    assert.equal(dialog().props["aria-label"], "Discussion board");
    await act(async () => tree.root.findByProps({ "aria-label": "Console views" }).findAllByType("button").find((button) => words(button) === "Library").props.onClick());
    assert.equal(dialog().props["aria-label"], "Project library");
    assert.deepEqual(nav("Library views"), ["All documents", "Outcomes"]);
    const search = calls.findLast((call) => call.operation === "search").input;
    assert.equal(search.documents_only, true, "the Library asks for documents, not threads");
    assert.ok(!dialog().findAllByType("option").some((option) => /Discussion|Reply/.test(words(option))));
    await act(async () => tree.unmount());
  });
});

test("starting a thread states its purpose and never dispatches work", async () => {
  const calls = [];
  let tree;
  const call = async (operation, input) => { calls.push({ operation, input }); return operation === "create" ? { ...input, revision: 1, latest_revision: 1, author: "browser:client" } : operation === "body" ? { body: input.id, complete: true } : { items: [] }; };
  await act(async () => { tree = create(createElement(ProjectLibrary, { state: fixtureState, call, board: true })); });
  await act(async () => tree.root.findAllByType("button").find((button) => words(button) === "Start a thread").props.onClick());
  const editor = tree.root.findByProps({ "aria-label": "Knowledge editor" });
  assert.match(words(editor), /Start a thread.*does not assign work or notify an agent/);
  assert.equal(editor.findAllByProps({ name: "kind" }).length, 0, "a thread is not a document category");
  const previous = globalThis.FormData;
  globalThis.FormData = class { get(name) { return { title: "Who owns retries?", body: "Looking for the owner.", status: "tentative" }[name] ?? null; } };
  try { await act(async () => editor.props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = previous; }
  const created = calls.find((call) => call.operation === "create").input;
  assert.equal(created.kind, "discussion");
  assert.equal(created.title, "Who owns retries?");
  assert.ok(!calls.some(({ operation }) => /task|attach|mission/.test(operation)), "posting is not dispatch");
  await act(async () => tree.unmount());
});

test("direct questions list truthful waiting state and open the existing task conversation", async () => {
  const opened = [];
  const state = { ...fixtureState, peerQuestions: new Map([["q1", { id: "q1", source_task_id: runningTask, target_task_id: doneTask, answered: false, revision: 1n }], ["q2", { id: "q2", source_task_id: doneTask, target_task_id: runningTask, answered: true, revision: 2n }]]) };
  let tree;
  await act(async () => { tree = create(createElement(ProjectLibrary, { state, call: async () => ({ items: [] }), board: true, onRecord: (...args) => opened.push(args) })); });
  const section = tree.root.findByProps({ "aria-label": "Direct questions" });
  assert.match(words(section), /Builder One → Builder Two.*Awaiting answer.*Builder Two → Builder One.*Answered/);
  await act(async () => section.findAllByType("button")[0].props.onClick());
  assert.deepEqual(opened, [["peer_question", "q1", project]], "the existing runtime conversation, not a second messaging path");
  await act(async () => tree.unmount());
});

test("activity cues come only from new recorded operations, are bounded, expire and never replay after reconnect", async () => {
  await withDocument(async () => {
    const realNow = Date.now;
    let now = 1_000_000, page = [row("posted", "aa", 10)], state;
    const calls = [];
    const call = async (operation, input) => { calls.push({ operation, input }); return { items: page }; };
    function Probe({ call }) { state = useKnowledgeActivity([project], call); return null; }
    Date.now = () => now;
    let tree;
    try {
      await act(async () => { tree = create(createElement(Probe, { call })); });
      assert.equal(state.recent.length, 1, "history is listed");
      assert.equal(state.cues.length, 0, "history is never cued");
      page = [row("read", "bb", 20, { agent_id: reader, run_id: "r1", task_id: doneTask }), ...page];
      await act(async () => { globalThis.document.dispatchEvent(new Event("visibilitychange")); });
      assert.deepEqual(state.cues.map((cue) => cue.operation), ["read"]);
      await act(async () => { globalThis.document.dispatchEvent(new Event("visibilitychange")); });
      assert.equal(state.cues.length, 1, "an operation is cued once");
      page = [...Array.from({ length: 6 }, (_, index) => row("posted", String(index + 1).repeat(2), 30 + index)), ...page];
      await act(async () => { globalThis.document.dispatchEvent(new Event("visibilitychange")); });
      assert.equal(state.cues.length, CUE_LIMIT, "simultaneous activity is bounded");
      now += CUE_MS;
      await act(async () => { globalThis.document.dispatchEvent(new Event("visibilitychange")); });
      assert.equal(state.cues.length, 0, "cues expire");
      await act(async () => tree.update(createElement(Probe, {})));
      page = [row("posted", "cc", 90), ...page];
      await act(async () => tree.update(createElement(Probe, { call })));
      assert.equal(state.cues.length, 0, "the first look after reconnecting is history");
      assert.equal(state.recent[0].content_id, "cc".repeat(16), "and it is still listed");
      assert.ok(calls.every(({ operation, input }) => operation === "activity" && input.limit === 8), "cues read only the bounded activity listing: no bodies, receipts or writes");
    } finally { Date.now = realNow; if (tree) await act(async () => tree.unmount()); }
  });
});

test("activity from a previous project scope is never shown or opened from the new one", async () => {
  await withDocument(async () => {
    let state, fail = false;
    const call = async () => { if (fail) throw new Error("unavailable"); return { items: [row("posted", "aa", 10)] }; };
    function Probe({ projects }) { state = useKnowledgeActivity(projects, call); return null; }
    let tree;
    try {
      await act(async () => { tree = create(createElement(Probe, { projects: [project] })); });
      assert.equal(state.recent.length, 1);
      fail = true;
      await act(async () => tree.update(createElement(Probe, { projects: ["other-project"] })));
      assert.equal(state.recent.length, 0, "the old project's activity stays listed after a failed read of the new one");
    } finally { if (tree) await act(async () => tree.unmount()); }
  });
});

test("cues appear where agents already are, without moving anyone, and stay inspectable", () => {
  const graph = sceneGraph([hall("repo", { band: 1 }), hall("src", { machines: [machine("src-job", "job")] })]);
  const workers = [
    { id: "busy", name: "Busy", role: "worker", provider: "codex", activity: "busy", location: "working", nodeId: "src" },
    { id: "idle", name: "Idle", role: "worker", provider: "codex", activity: "idle" },
  ];
  const cue = (key, agentId, board, reading) => ({ key, agentId, board, reading, label: `${agentId} ${key}`, open() {} });
  const cues = [cue("near", "busy", false, true), cue("far", "idle", true, false), cue("absent", "not-on-this-floor", true, false)];
  const plain = renderToStaticMarkup(createElement(FactoryScene, { graph, workers, onOpenLibrary() {}, onOpenBoard() {} }));
  const cued = renderToStaticMarkup(createElement(FactoryScene, { graph, workers, onOpenLibrary() {}, onOpenBoard() {}, knowledgeCues: cues }));
  const where = (markup, attribute) => [...markup.matchAll(new RegExp(`${attribute}[^>]*transform="translate\\(([-0-9.]+) ([-0-9.]+)\\)"`, "g"))].map((match) => [Number(match[1]), Number(match[2])]);
  for (const id of ["busy", "idle"]) assert.deepEqual(where(cued, `data-worker-id="${id}"`), where(plain, `data-worker-id="${id}"`), `${id} is not relocated`);
  for (const [key, id] of [["near", "busy"], ["far", "idle"]]) {
    const [[x, y]] = where(cued, `data-worker-id="${id}"`), [[cx, cy]] = where(cued, `data-knowledge-cue="agent" data-knowledge-key="${key}"`);
    assert.deepEqual([cx - x, cy - y], [-20, -32], `${key} cue sits at the agent's current position`);
  }
  assert.ok(!cued.includes('data-knowledge-key="absent"') || /data-knowledge-cue="board"[^>]*data-knowledge-key="absent"/.test(cued), "no sprite: only the furniture marks it");
  assert.match(cued, /data-knowledge-cue="board"[^>]*data-knowledge-key="absent"/, "the board marks its newest operation");
  if (cued.includes('data-break-room="shelf"')) assert.match(cued, /data-knowledge-cue="shelf"[^>]*data-knowledge-key="near"/);
  assert.match(cued, /aria-label="Recorded: busy near"[^>]*role="button"[^>]*tabindex="0"/, "a recorded cue is a labelled, focusable control");
  assert.doesNotMatch(plain, /data-knowledge-cue/, "ambient life never draws a recorded cue");
  let opened = 0, tree;
  act(() => { tree = create(createElement(FactoryScene, { graph, workers, knowledgeCues: [{ ...cues[0], open: () => opened++ }] })); });
  const mark = tree.root.find((node) => node.props["data-knowledge-cue"] === "agent");
  act(() => mark.props.onKeyDown({ key: "Enter", preventDefault() {} }));
  assert.equal(opened, 1, "keyboard activation opens the record");
  act(() => tree.unmount());
  const css = readFileSync(new URL("../dist/src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /prefers-reduced-motion: reduce\) \{ \.dfKnowledgeCue \{ animation: none; \} \}/);
});

test("the floor's activity list opens the exact thread or revision, even with no sprite drawn", async () => {
  await withDocument(async () => {
    const reply = row("posted", "dd", 50, { kind: "discussion_reply", thread_id: "ee".repeat(16), agent_id: builder, run_id: "r2" });
    const read = row("read", "ff", 40, { agent_id: "99".repeat(16), run_id: "r3" });
    assert.deepEqual(activityTarget(reply), { id: "ee".repeat(16) });
    assert.deepEqual(activityTarget(read), { id: "ff".repeat(16), revision: 2 });
    const calls = [];
    let tree;
    await act(async () => { tree = create(createElement(FactoryConsole, { status: "ready", state: fixtureState, graphs: fixtureGraphs, onDetail() {}, onProjectContent: async (operation, input) => {
      calls.push({ operation, input });
      if (operation === "activity") return { items: input.project_id === project ? [reply, read] : [] };
      if (operation === "read") return { ...read, id: input.id, revision: input.revision, latest_revision: 3, kind: "lesson", source_references: "{}" };
      if (operation === "body") return { body: "text", complete: true };
      return { items: [] };
    } })); });
    const list = tree.root.findByProps({ className: "dfKnowledgeActivity" });
    const buttons = list.findAllByType("button");
    assert.deepEqual(buttons.map(words), ["Builder One posted a reply · revision 2", "Agent 99999999 read document “Doc ff” · revision 2"]);
    assert.ok(!calls.some(({ operation }) => !["activity", "production"].includes(operation)), "listing activity reads no record");
    await act(async () => buttons[1].props.onClick());
    assert.equal(tree.root.findByType("dialog").props["aria-label"], "Project library");
    assert.deepEqual(calls.filter(({ operation }) => operation === "read").map(({ input }) => [input.id, input.revision]), [["ff".repeat(16), 2]], "the exact revision read, not the latest");
    await act(async () => tree.root.findByProps({ className: "dfKnowledgeActivity" }).findAllByType("button")[0].props.onClick());
    assert.equal(tree.root.findByType("dialog").props["aria-label"], "Discussion board");
    await act(async () => tree.unmount());
  });
});
