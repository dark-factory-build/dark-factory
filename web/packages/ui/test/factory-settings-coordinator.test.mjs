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
