import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { act, create } from "react-test-renderer";
import { ProjectLibrary } from "../dist/src/project-library.js";
import { fixtureState } from "../../../fixtures/state.mjs";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const metadata = { id: "ab".repeat(16), revision: 2, latest_revision: 3, kind: "procedure", title: "Optional guide", description: "Release instructions", author: "worker", source_references: "" };
const words = (node) => typeof node === "string" ? node : (node.children ?? []).map(words).join("");
const button = (renderer, label) => { const found = renderer.root.findAllByType("button").find((item) => words(item).includes(label)); assert.ok(found, label); return found; };
const click = async (renderer, label) => { await act(async () => button(renderer, label).props.onClick()); };
const mount = async (t, props) => { let renderer; await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, ...props })); }); t.after(async () => { await act(async () => renderer.unmount()); }); return renderer; };
const documents = (renderer) => renderer.root.findByProps({ "aria-label": "Library documents" }).findAllByType("strong").map(words);
const defer = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

test("Settings remains lazy; opening loads bounded search pages and appends results", async (t) => {
  const calls = [];
  const call = async (operation, input) => {
    calls.push({ operation, input });
    assert.equal(operation, "search"); assert.equal(input.limit, 4);
    return { items: Array.from({ length: 4 }, (_, index) => ({ ...metadata, id: String(input.offset + index), title: `Guide ${input.offset + index}` })), next_offset: input.offset === 4 ? 0 : 4 };
  };
  const renderer = await mount(t, { call });
  assert.equal(calls.length, 0);
  await act(async () => renderer.root.findByType("details").props.onToggle({ currentTarget: { open: true } }));
  assert.equal(calls.length, 1);
  assert.deepEqual(documents(renderer), Array.from({ length: 4 }, (_, index) => `Guide ${index}`));
  await act(async () => renderer.root.findByProps({ type: "search" }).props.onChange({ target: { value: "changed filter" } }));
  await click(renderer, "Load more documents");
  assert.equal(calls.at(-1).input.query, "", "paging retains the query that produced the existing results");
  assert.equal(calls.length, 2);
  assert.deepEqual(documents(renderer), Array.from({ length: 8 }, (_, index) => `Guide ${index}`));
  await act(async () => renderer.root.findByProps({ role: "search" }).props.onSubmit({ preventDefault() {} }));
  assert.equal(documents(renderer).length, 4, "refresh replaces the old page");
});

test("intentional entry is flat and a document click reads its exact revision and first body page", async (t) => {
  const calls = [];
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "search") return { items: [metadata] };
    if (operation === "read") return { ...metadata, revision: input.revision };
    if (operation === "body") return input.offset === 0 ? { body: "read me", next_offset: 7, complete: false } : { body: " fully", complete: true };
    throw new Error("unexpected operation");
  };
  const renderer = await mount(t, { call, open: true });
  assert.equal(renderer.root.findByProps({ "aria-label": "Project library" }).type, "section");
  assert.deepEqual(calls.map((call) => call.operation), ["search"], "no background body reads");
  await click(renderer, "Optional guide");
  assert.deepEqual(calls.map((call) => call.operation), ["search", "read", "body"]);
  assert.equal(calls[2].input.revision, 2); assert.equal(calls[2].input.limit, 8192);
  assert.deepEqual(renderer.root.findByType("pre").children, ["read me"]);
  await click(renderer, "Read more");
  assert.equal(calls[3].input.revision, 2); assert.equal(calls[3].input.offset, 7);
  assert.deepEqual(renderer.root.findByType("pre").children, ["read me fully"]);
  assert.equal(button(renderer, "Edit document").props.disabled, true, "superseded revision cannot be edited as current");
  assert.equal(renderer.root.findAllByType("details").length, 0, "open library has no nested disclosures");
  assert.ok(button(renderer, "Retire document"));
  await click(renderer, "Sources & revisions");
  const originalFormData = globalThis.FormData;
  globalThis.FormData = class { get() { return "1"; } };
  try { await act(async () => renderer.root.findByProps({ "aria-label": "Read a revision" }).props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = originalFormData; }
  assert.deepEqual(calls.slice(-2).map(({ operation, input }) => [operation, input.revision]), [["read", 1], ["body", 1]]);
});

test("a failed initial body page is retryable at the selected revision", async (t) => {
  let attempts = 0;
  const renderer = await mount(t, { open: true, call: async (operation, input) => {
    if (operation === "search") return { items: [metadata] };
    if (operation === "read") return metadata;
    assert.equal(input.revision, 2); assert.equal(input.offset, 0);
    if (++attempts === 1) throw new Error("Document temporarily unavailable");
    return { body: "recovered", complete: true };
  } });
  await click(renderer, "Optional guide");
  assert.match(words(renderer.root.findByProps({ role: "alert" })), /temporarily unavailable/);
  await click(renderer, "Retry document text");
  assert.deepEqual(renderer.root.findByType("pre").children, ["recovered"]);
});

test("changing projects discards late body results and resets the selected document", async (t) => {
  const firstProject = [...fixtureState.projects.values()][0];
  const secondProject = { ...firstProject, id: "ef".repeat(16), name: "Second project" };
  const state = { ...fixtureState, projects: new Map([[firstProject.id, firstProject], [secondProject.id, secondProject]]) };
  const oldBody = defer();
  const calls = [];
  const renderer = await mount(t, { state, open: true, call: async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "search") return { items: input.project_id === firstProject.id ? [metadata] : [] };
    if (operation === "read") return metadata;
    return oldBody.promise;
  } });
  await click(renderer, "Optional guide");
  await act(async () => renderer.root.findAllByType("select")[0].props.onChange({ target: { value: secondProject.id } }));
  assert.equal(renderer.root.findAllByType("pre").length, 0);
  assert.deepEqual(documents(renderer), []);
  await act(async () => oldBody.resolve({ body: "OLD PROJECT BODY", complete: true }));
  assert.equal(renderer.root.findAllByType("pre").length, 0);
  assert.equal(renderer.root.findAllByProps({ role: "alert" }).length, 0);
  assert.equal(calls.at(-1).input.project_id, secondProject.id);
});

test("late metadata after project scope replacement cannot request an old document body", async (t) => {
  const oldRead = defer();
  const calls = [];
  const call = async (operation, input) => { calls.push({ operation, input }); if (operation === "search") return { items: [metadata] }; if (operation === "read") return oldRead.promise; throw new Error("unexpected body request"); };
  const renderer = await mount(t, { open: true, call });
  await click(renderer, "Optional guide");
  const project = { ...[...fixtureState.projects.values()][0], id: "ef".repeat(16) };
  await act(async () => renderer.update(createElement(ProjectLibrary, { open: true, call, state: { ...fixtureState, projects: new Map([[project.id, project]]) } })));
  await act(async () => oldRead.resolve(metadata));
  assert.deepEqual(calls.map(({ operation }) => operation), ["search", "read", "search"]);
  assert.equal(renderer.root.findAllByType("pre").length, 0);
});

test("disconnected and empty libraries have an honest visible state", async (t) => {
  const renderer = await mount(t, { open: true });
  assert.match(words(renderer.root), /Connect to read project documents/);
  let calls = 0;
  await act(async () => renderer.update(createElement(ProjectLibrary, { open: true, state: fixtureState, call: async () => { calls++; return { items: [] }; } })));
  assert.equal(calls, 1);
  assert.match(words(renderer.root), /No documents yet/);
});

test("board shares immutable threads, resolves by revision, and retains linked conclusions", async () => {
  const calls = [], source = `${[...fixtureState.projects.keys()][0]}:node-stable`;
  const metadata = { id: "ab".repeat(16), revision: 2, latest_revision: 2, repository_id: "cd".repeat(16), kind: "discussion", title: "Root cause", description: "finding", author: "reviewer", source_references: JSON.stringify({ status: "tentative", entities: [source], evidence: ["test:regression"], branch: "topic", environment: "staging", pinned: false, resolved: false }) };
  let current = metadata;
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "search") return { items: input.kind === "discussion_reply" ? [] : [current], next_offset: 0 };
    if (operation === "read") return current;
    if (operation === "body") return { body: "Concrete evidence, not a permission grant.", complete: true };
    if (operation === "revise") { current = { ...current, ...input, revision: current.revision + 1, latest_revision: current.revision + 1 }; return current; }
    if (operation === "create") return { ...input, revision: 1, latest_revision: 1, author: "operator" };
    if (operation === "accesses") return { items: [{ content_revision: input.revision, kind: "read", run_id: "run", offset: 0, byte_length: 12 }] };
    throw new Error(`unexpected ${operation}`);
  };
  const opened = [];
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, call, board: true, entity: source, repository: metadata.repository_id, onSource: (ref) => opened.push(ref) })); });
  const click = async (label) => { const button = renderer.root.findAllByType("button").find((button) => words(button).includes(label)); assert.ok(button, label); await act(async () => { button.props.onClick(); }); };
  assert.equal(calls.length, 1, "Board entry loads bounded discussion metadata");
  await click("New discussion");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "repository_id").props.defaultValue, metadata.repository_id, "entity entry point seeds the editor repository");
  await click("Cancel");

  assert.equal(calls[0].input.entity, source);
  assert.equal(calls[0].input.limit, 4);
  await click("Root cause");
  assert.equal(renderer.root.findAllByType("button").find((button) => words(button) === "Resolve discussion").props.disabled, false, "selection reads the complete root body");
  assert.deepEqual(calls.at(-1).input, { project_id: [...fixtureState.projects.keys()][0], repository_id: metadata.repository_id, branch: "topic", environment: "staging", thread_id: metadata.id, kind: "discussion_reply", offset: 0, limit: 4 });
  await click("Task access");
  assert.ok(renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")));
  await click("Resolve discussion");
  assert.equal(calls.filter(({ operation, input }) => operation === "search" && input.kind === "discussion_reply").length, 2, "updated discussion reloads replies automatically");
  assert.ok(!renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")), "new root revision clears previous revision receipts");
  const revised = calls.find((value) => value.operation === "revise").input;
  assert.equal(revised.expected_revision, 2);
  assert.equal(revised.body, "Concrete evidence, not a permission grant.");
  assert.equal(JSON.parse(revised.source_references).resolved, true);
  assert.equal(current.revision, 3);
  await click("Sources & revisions"); await click("View source"); assert.deepEqual(opened, [source]);
  await click("Task access"); assert.equal(calls.at(-1).input.revision, 3);
  await click("Read"); await click("Reply");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "kind").props.defaultValue, "discussion_reply");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "thread_id").props.defaultValue, metadata.id);
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "repository_id").props.defaultValue, metadata.repository_id);
  await click("Cancel");
  assert.deepEqual(renderer.root.findByType("pre").children, ["Concrete evidence, not a permission grant."], "cancelling a reply preserves the loaded root body");
  await click("Save conclusion");
  assert.equal(renderer.root.findAllByType("input").find((input) => input.props.name === "kind").props.defaultValue, "lesson");
  assert.match(renderer.root.findAllByType("textarea").find((input) => input.props.name === "evidence").props.defaultValue, /content:abab.*@3/);
  const originalFormData = globalThis.FormData;
  const values = { kind: "lesson", repository_id: metadata.repository_id, title: "Retained conclusion", body: "Use exact authority", status: "tentative", evidence: `content:${metadata.id}@3`, entities: source, thread_id: metadata.id, branch: "topic", environment: "staging" };
  globalThis.FormData = class { get(name) { return values[name] ?? null; } };
  try { await act(async () => renderer.root.findByProps({ "aria-label": "Knowledge editor" }).props.onSubmit({ preventDefault() {}, currentTarget: {} })); } finally { globalThis.FormData = originalFormData; }
  assert.ok(!renderer.root.findAllByType("p").some((node) => node.children.join("").includes("· Run run")), "new conclusion clears another document's receipts");
  assert.equal(calls.at(-1).operation, "search", "saved content refreshes the list automatically");
  assert.equal(calls.findLast(call => call.operation === "create").input.repository_id, metadata.repository_id);
  assert.ok(!calls.some((value) => /task|reply|deliver/.test(value.operation)), "discussion actions never invoke task delivery");
  await act(async () => renderer.unmount());
});

test("ID-only links resolve latest through explicit metadata revisions; historical selections stay pinned", async () => {
  const rootID = "ab".repeat(16), lessonID = "cd".repeat(16), priorID = "ef".repeat(16), calls = [];
  const documents = new Map([[rootID, { title: "Discussion", kind: "discussion", latest_revision: 3, source_references: JSON.stringify({ status: "current" }) }], [lessonID, { title: "Lesson", kind: "lesson", latest_revision: 2, source_references: JSON.stringify({ status: "current", thread_id: rootID, supersedes: priorID }) }], [priorID, { title: "Earlier lesson", kind: "lesson", latest_revision: 4, source_references: JSON.stringify({ status: "superseded" }) }]]);
  const call = async (operation, input) => {
    calls.push({ operation, input });
    if (operation === "read") { assert.ok(Number.isSafeInteger(input.revision) && input.revision > 0, "real store rejects revision zero"); const doc = documents.get(input.id); assert.ok(doc); return { ...doc, id: input.id, revision: input.revision }; }
    if (operation === "search") return { items: [{ ...documents.get(lessonID), id: lessonID, revision: 1 }], next_offset: 0 };
    if (operation === "body") return { body: `Revision ${input.revision}`, complete: true };
    throw new Error(`unexpected ${operation}`);
  };
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, call, initialID: lessonID })); });
  const click = async (label) => { await act(async () => renderer.root.findAllByType("button").find((button) => words(button).includes(label)).props.onClick()); };
  const reads = () => calls.filter((call) => call.operation === "read").map(({ input }) => [input.id, input.revision]);
  assert.deepEqual(reads(), [[lessonID, 1], [lessonID, 2]], "initial ID opens the current immutable revision");
  await click("Sources & revisions"); await click("View discussion");
  assert.deepEqual(reads().slice(-2), [[rootID, 1], [rootID, 3]]);
  await click("Lesson");
  assert.deepEqual(reads().at(-1), [lessonID, 1], "explicit historical selection never advances to latest");
  await click("Sources & revisions"); await click("Earlier document");
  assert.deepEqual(reads().slice(-2), [[priorID, 1], [priorID, 4]]);
  await act(async () => renderer.unmount());
});


test("scoped entry selects its project and clears source filters when switching projects", async (t) => {
  const first = [...fixtureState.projects.values()][0], second = { ...first, id: "ef".repeat(16), name: "Second project" };
  const calls = [], entity = `${second.id}:source`, repository = "cd".repeat(16);
  const renderer = await mount(t, { open: true, initialProjectId: second.id, entity, repository,
    state: { ...fixtureState, projects: new Map([[first.id, first], [second.id, second]]) },
    call: async (operation, input) => { calls.push({ operation, input }); return { items: [] }; },
  });
  assert.equal(calls[0].input.project_id, second.id);
  assert.equal(calls[0].input.entity, entity); assert.equal(calls[0].input.repository_id, repository);
  await act(async () => renderer.root.findAllByType("select")[0].props.onChange({ target: { value: first.id } }));
  assert.equal(calls.at(-1).input.project_id, first.id);
  assert.equal(calls.at(-1).input.entity, ""); assert.equal(calls.at(-1).input.repository_id, "");
});


const visibleWords = (node) => typeof node === "string" ? node : node.type === "details" && !node.props.open ? words(node.findByType("summary")) : (node.children ?? []).map(visibleWords).join(" ");

test("reader keeps named actions visible and opens one workspace at a time", async (t) => {
  const source = "project:opaque-one", other = "project:opaque-two", task = "aa".repeat(16), opened = [];
  const doc = { ...metadata, revision: 3, title: "Deployment guide", description: "Deployment guide", author: "run:opaque-author", repository_id: "bb".repeat(16), commit: "d".repeat(40), path: ".dark-factory/content/doc.md", source_references: JSON.stringify({ status: "current", source_revision: "c".repeat(40), branch: "private-branch", environment: "test-environment", entities: [source, other], record_type: "task", record_id: task, task_id: task }) };
  const renderer = await mount(t, { open: true, onSource: (ref) => opened.push(ref), onRecord: (...args) => opened.push(args), call: async (operation) => operation === "search" ? { items: [doc] } : operation === "read" ? doc : { body: "Useful deployment steps.", complete: true } });
  await click(renderer, "Deployment guide");
  const reader = renderer.root.findByProps({ "aria-label": "Selected library revision" });
  assert.match(visibleWords(reader), /Deployment guide.*Revision.*current.*Useful deployment steps/);
  assert.match(visibleWords(reader), /Edit document.*Retire document.*Attach to a task.*Sources & revisions.*Task access/);
  assert.doesNotMatch(visibleWords(reader), /opaque|Repository|Attach revision|unspecified|Manage/);
  assert.equal(renderer.root.findAllByType("details").length, 0);
  assert.equal(renderer.root.findByProps({ type: "search" }).parent.type, "label", "search is visible at entry");
  await click(renderer, "Attach to a task");
  assert.ok(renderer.root.findByProps({ "aria-label": "Attach to a task" }));
  assert.equal(reader.findAllByType("pre").length, 0, "form replaces the body rather than stacking below it");
  await click(renderer, "Cancel");
  assert.deepEqual(reader.findByType("pre").children, ["Useful deployment steps."]);
  await click(renderer, "Sources & revisions");
  assert.equal(reader.findAllByType("pre").length, 0);
  assert.equal(renderer.root.findAllByProps({ "aria-label": "Record a test result" }).length, 0);
  const sources = reader;
  assert.match(words(sources), /Revision 3.*opaque-author/);
  assert.match(words(sources), /private-branch/); assert.match(words(sources), /test-environment/);
  assert.match(words(sources), new RegExp(doc.commit));
  assert.equal(reader.findAllByType("p").filter((item) => words(item) === doc.title).length, 0, "description does not repeat the title");
  await act(async () => sources.findByProps({ "aria-label": "View source 2" }).props.onClick());
  assert.equal(opened[0], other);
  const recordButtons = sources.findAllByType("button").filter((item) => /View (linked )?task/.test(words(item)));
  assert.equal(recordButtons.length, 1, "one link for the same task in both metadata fields");
  await act(async () => recordButtons[0].props.onClick());
  assert.deepEqual(opened[1], ["task", task, [...fixtureState.projects.keys()][0]]);
});

test("consequential knowledge and historical states remain visible outside disclosures", async (t) => {
  for (const [status, warning] of [["current", "current"], ["needs_revalidation", "needs revalidation"], ["superseded", "superseded"]]) {
    const doc = { ...metadata, revision: 3, projected_status: status, source_references: JSON.stringify({ status: "current" }) };
    const renderer = await mount(t, { open: true, call: async (operation) => operation === "search" ? { items: [doc] } : operation === "read" ? doc : { body: "Guidance", complete: true } });
    await click(renderer, "Optional guide");
    assert.match(visibleWords(renderer.root.findByProps({ "aria-label": "Selected library revision" })), new RegExp(warning));
  }
  const renderer = await mount(t, { open: true, call: async (operation) => operation === "search" ? { items: [metadata] } : operation === "read" ? { ...metadata, source_references: JSON.stringify({ status: "current" }) } : { body: "Old guidance", complete: true } });
  await click(renderer, "Optional guide");
  const visible = visibleWords(renderer.root.findByProps({ "aria-label": "Selected library revision" }));
  assert.match(visible, /Older revision/); assert.doesNotMatch(visible, /current/);
});


test("editor preserves scope fields in one form, and Outcomes is a separate project view", async (t) => {
  const calls = [], doc = { ...metadata, revision: 3, source_references: JSON.stringify({ status: "current", scope: "repository", entities: ["project:node"], evidence: ["source:test"], branch: "topic", environment: "staging" }) };
  const renderer = await mount(t, { open: true, call: async (operation, input) => { calls.push({ operation, input }); return operation === "search" ? { items: [doc] } : operation === "read" ? doc : operation === "body" ? { body: "Existing text", complete: true } : { items: [] }; } });
  await click(renderer, "Optional guide"); await click(renderer, "Edit document");
  assert.equal(renderer.root.findAllByProps({ "aria-label": "Selected library revision" }).length, 0);
  const form = renderer.root.findByProps({ "aria-label": "Knowledge editor" });
  assert.equal(form.findAllByType("details").length, 0);
  for (const [name, value] of [["scope", "repository"], ["entities", "project:node"], ["evidence", "source:test"], ["branch", "topic"], ["environment", "staging"]]) assert.equal(form.findByProps({ name }).props.defaultValue, value);
  await click(renderer, "Cancel"); assert.deepEqual(renderer.root.findByType("pre").children, ["Existing text"]);
  assert.ok(!calls.some(({ operation }) => operation.startsWith("outcome")), "documents never fetch outcomes");
  await click(renderer, "Outcomes");
  assert.equal(renderer.root.findAllByProps({ "aria-label": "Library documents" }).length, 0);
  assert.ok(renderer.root.findByProps({ "aria-label": "Project outcomes" }));
  assert.equal(renderer.root.findAllByType("details").length, 0);
  assert.deepEqual(calls.at(-1), { operation: "outcome_list", input: { project_id: [...fixtureState.projects.keys()][0], offset: 0, limit: 1 } });
  await click(renderer, "All documents"); assert.ok(renderer.root.findByProps({ type: "search" }));
});


test("leaving a deep link during its list request never starts the document read", async () => {
  const page = defer(), calls = [];
  let renderer;
  await act(async () => { renderer = create(createElement(ProjectLibrary, { state: fixtureState, open: true, initialID: metadata.id, call: async (operation) => { calls.push(operation); return page.promise; } })); });
  await act(async () => renderer.unmount());
  await act(async () => page.resolve({ items: [metadata] }));
  assert.deepEqual(calls, ["search"]);
});
