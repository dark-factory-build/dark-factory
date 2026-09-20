import assert from "node:assert/strict";
import test from "node:test";
import { FactorySettingsCoordinator } from "../dist/src/factory-settings-coordinator.js";

const authorization = { connection_id: "new", authorization_url: "https://github.com/login/oauth/authorize", expires_at: 123n };
const installation = { id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, html_url: "https://github.com/settings/installations/7", eligibility: "available" };

test("GitHub settings pages stay scoped to the current connection", async () => {
  const calls = [];
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request);
      if (request.action === "connect") return { state: "ok", authorization };
      if (request.action === "confirm") return { state: "ok" };
      if (request.action === "refresh") return { state: "ok", status: { connection_id: "new", state: "connected", repositories: [] } };
      if (request.action === "installations") return { state: "ok", installations: { installations: [installation], next_page: null } };
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "connect" });
  assert.equal(coordinator.github.result.installations, undefined);
  await coordinator.githubConnection({ action: "confirm", code: "0123456789" });
  assert.deepEqual(calls.map(({ action }) => action), ["connect", "confirm", "refresh", "installations"]);
  assert.equal(coordinator.github.result.status.connection_id, "new");
  assert.equal(coordinator.github.result.installations.installations[0].id, 7);
  await coordinator.githubConnection({ action: "connect" });
  assert.equal(coordinator.github.result.installations, undefined);
});

test("pending authorization survives a settings status refresh", async () => {
  const calls = [];
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request.action);
      if (request.action === "connect") return { state: "ok", authorization };
      if (request.action === "status") return { state: "ok", status: { connection_id: "new", state: "pending", repositories: [] } };
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "connect" });
  await coordinator.githubConnection({ action: "status" });
  assert.deepEqual(calls, ["connect", "status"]);
  assert.equal(coordinator.github.result.authorization.authorization_url, authorization.authorization_url);
});

test("a failed confirmation keeps its form and does not refresh away the retry", async () => {
  const calls = [];
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request.action);
      if (request.action === "connect") return { state: "ok", authorization };
      if (request.action === "confirm") return { state: "denied" };
      if (request.action === "status") return { state: "denied" };
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "connect" });
  await coordinator.githubConnection({ action: "confirm", code: "0123456789" });
  assert.deepEqual(calls, ["connect", "confirm"]);
  assert.equal(coordinator.github.result.authorization.authorization_url, authorization.authorization_url);
  await coordinator.githubConnection({ action: "status" });
  assert.deepEqual(calls, ["connect", "confirm", "status"]);
  assert.equal(coordinator.github.result.authorization.authorization_url, authorization.authorization_url);
});

test("a connected refresh drops discovery pages before reloading installations", async () => {
  const calls = [];
  let refreshes = 0;
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request.action);
      if (request.action === "refresh") { refreshes += 1; return { state: "ok", status: { connection_id: "new", state: "connected", repositories: [] } }; }
      if (request.action === "installations") return refreshes === 1 ? { state: "ok", installations: { installations: [{ id: 7, account: { id: 8, login: "factory-org" }, suspended_at: null, eligibility: "available" }] } } : { state: "ok", installations: { installations: [] } };
      if (request.action === "repositories") return { state: "ok", repositories: { repositories: [{ id: 9, full_name: "factory-org/worker", permissions: { pull: true, push: true, maintain: true, admin: true } }] } };
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "refresh" });
  await coordinator.githubConnection({ action: "repositories", installation_id: 7, page: 1 });
  await coordinator.githubConnection({ action: "refresh" });
  assert.deepEqual(calls, ["refresh", "installations", "repositories", "refresh", "installations"]);
  assert.equal(coordinator.github.result.installations.installations.length, 0);
  assert.equal(coordinator.github.result.repositories, undefined);
});

test("clearing GitHub state drops private observations before a replacement session", async () => {
  const owner = { session: () => ({ capabilities: 1, clientId: "client", async githubConnection() { return { state: "ok", status: { connection_id: "new", state: "connected", repositories: [] } }; } }), ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "status" });
  assert.equal(coordinator.github.result.status.connection_id, "new");
  coordinator.clearGitHub();
  assert.equal(coordinator.github.result, undefined);
});

test("reopening settings reloads installations from page one", async () => {
  const calls = [];
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request);
      if (request.action === "status") return { state: "ok", status: { connection_id: "same", state: "connected", repositories: [] } };
      if (request.action === "installations") return { state: "ok", installations: { installations: [], next_page: undefined } };
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.githubConnection({ action: "status" });
  assert.deepEqual(calls, [{ action: "status" }, { action: "installations", page: 1 }]);
  assert.equal(coordinator.github.result.installations.installations.length, 0);
});

test("reopening settings queues status behind a pending installation page", async () => {
  const calls = [];
  let release;
  let installationRequest = 0;
  const session = {
    capabilities: 1,
    clientId: "client",
    async githubConnection(request) {
      calls.push(request);
      if (request.action === "status") return { state: "ok", status: { connection_id: "same", state: "connected", repositories: [] } };
      if (request.action === "installations") {
        installationRequest += 1;
        if (installationRequest === 1) return await new Promise((resolve) => { release = resolve; });
        return { state: "ok", installations: { installations: [{ id: 9, account: { id: 10, login: "page-one" }, suspended_at: null, eligibility: "available" }], next_page: undefined } };
      }
      throw new Error(`unexpected ${request.action}`);
    },
  };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish: () => {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  const first = coordinator.githubConnection({ action: "status" });
  await new Promise((resolve) => setTimeout(resolve, 0));
  await coordinator.githubConnection({ action: "status" });
  release({ state: "ok", installations: { installations: [{ id: 8, account: { id: 9, login: "page-two" }, suspended_at: null, eligibility: "available" }], next_page: 3 } });
  await first;
  assert.deepEqual(calls.map(({ action, page }) => page === undefined ? { action } : { action, page }), [
    { action: "status" },
    { action: "installations", page: 1 },
    { action: "status" },
    { action: "installations", page: 1 },
  ]);
  assert.equal(coordinator.github.result.installations.installations[0].account.login, "page-one");
});

test("project settings discard private content and fence old replies across reconnect", async () => {
  let finishOld;
  let session = { async getRepositories() { return [{ root: "/private/checkout" }]; }, async intake() { return { sources: [{ repository: "private/source" }] }; } };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish() {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.loadRepositories("project");
  await coordinator.loadIntake("project");
  assert.equal(coordinator.repositories.size, 1);
  assert.equal(coordinator.intake.size, 1);
  session.intake = () => new Promise((resolve) => { finishOld = resolve; });
  const oldRead = coordinator.loadIntake("project");
  coordinator.clearProjectSettings();
  assert.equal(coordinator.repositories.size, 0);
  assert.equal(coordinator.intake.size, 0);
  assert.equal(coordinator.intakePending.size, 0);
  session = { async intake() { return { sources: [{ repository: "current/source" }] }; } };
  await coordinator.loadIntake("project");
  finishOld({ sources: [{ repository: "private/source" }] });
  await oldRead;
  assert.equal(coordinator.intake.get("project").sources[0].repository, "current/source");
});

test("failed preview cannot enable and reaccepting clears an older withdrawal receipt", async () => {
  const source = { id: "01".repeat(16), revision: 2n, repository: "example/source", enabled: false };
  const oldCandidate = { number: 1n, acceptance_id: "02".repeat(16), reason: "content_changed" };
  let previewFails = false;
  const session = { capabilities: 1, clientId: "client", async intake(request) {
    if (request.action === "list") return { state: "ok", sources: [source] };
    if (request.action === "preview") return previewFails ? { state: "unavailable" } : { state: "ok", reviewed_revision: 2n, candidates: [oldCandidate] };
    if (request.action === "accept") return { state: "accepted", acceptance_id: "03".repeat(16) };
    throw new Error(request.action);
  } };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish() {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.loadIntake("project");
  await coordinator.intakeAction("project", { action: "preview", source_id: source.id, page: 1 });
  assert.equal(coordinator.intake.get("project").reviewed_revision, 2n);
  previewFails = true;
  await coordinator.intakeAction("project", { action: "preview", source_id: source.id, page: 1 });
  assert.equal(coordinator.intake.get("project").reviewed_revision, undefined);
  assert.equal(coordinator.intake.get("project").candidates, undefined);
  previewFails = false;
  await coordinator.intakeAction("project", { action: "preview", source_id: source.id, page: 1 });
  await coordinator.intakeAction("project", { action: "accept", source_id: source.id, expected_revision: 2n, issue_number: 1n, content_hash: "ab".repeat(32) });
  assert.equal(coordinator.intake.get("project").candidates, undefined);
  assert.equal(coordinator.intake.get("project").reviewed_revision, undefined);
});

test("successful withdrawal immediately retires its candidate action", async () => {
  const source = { id: "01".repeat(16), revision: 2n, repository: "example/source", enabled: true };
  const acceptanceId = "02".repeat(16);
  const candidate = { number: 1n, acceptance_id: acceptanceId, reason: "already_accepted" };
  const session = { capabilities: 1, clientId: "client", async intake(request) {
    if (request.action === "list") return { state: "ok", sources: [source], candidates: [candidate] };
    if (request.action === "withdraw") return { state: "withdrawal_pending", acceptance_id: acceptanceId };
    throw new Error(request.action);
  } };
  const owner = { session: () => session, ready: () => true, generation: () => 1, current: () => true, errorCode: () => "error", publish() {} };
  const coordinator = new FactorySettingsCoordinator(owner);
  await coordinator.loadIntake("project");
  await coordinator.intakeAction("project", { action: "withdraw", acceptance_id: acceptanceId });
  assert.equal(coordinator.intake.get("project").candidates[0].reason, "withdrawal_pending");
});

test("approval adds to queue and refreshes the same source without another click", async () => {
  const calls = [];
  const session = { async intake(request) {
    calls.push(request);
    if (request.action === "accept") return {state:"accepted",acceptance_id:"receipt"};
    if (request.action === "import") return {state:"imported",task_id:"task"};
    if (request.action === "preview") return {state:"ok",source_id:"source",reviewed_revision:1n,candidates:[{reason:"already_accepted",task_id:"task"}]};
    throw new Error(request.action);
  }};
  const coordinator = new FactorySettingsCoordinator({session:()=>session,ready:()=>true,generation:()=>1,current:()=>true,errorCode:()=>"error",publish(){}});
  await coordinator.intakeAction("project",{action:"accept",source_id:"source",expected_revision:1n,issue_number:7n,content_hash:"ab".repeat(32)});
  assert.deepEqual(calls.slice(1),[{action:"import",acceptance_id:"receipt"},{action:"preview",source_id:"source",page:1}]);
  assert.equal(coordinator.intake.get("project").candidates[0].task_id,"task");
});

test("an acceptance response from a replaced session cannot trigger import", async () => {
  let resolve, current=true;
  const calls=[];
  const session={intake(request){calls.push(request);return new Promise((done)=>{resolve=done;});}};
  const coordinator=new FactorySettingsCoordinator({session:()=>session,ready:()=>true,generation:()=>1,current:()=>current,errorCode:()=>"error",publish(){}});
  const pending=coordinator.intakeAction("project",{action:"accept",source_id:"source"});
  current=false;
  resolve({state:"accepted",acceptance_id:"receipt"});
  await pending;
  assert.equal(calls.length,1);
});
