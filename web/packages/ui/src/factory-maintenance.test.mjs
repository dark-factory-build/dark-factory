import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryMaintenancePanel } from "../dist/src/factory-maintenance.js";

const build = { version: "v1.2.3", source: "a".repeat(40), target: "darwin/arm64", build_id: "build-1", release: true };
const render = (props = {}) => renderToStaticMarkup(createElement(FactoryMaintenancePanel, { maintenance: { destination: "runtime:/factory", state: "ready", available: { version: "v1.2.4", url: "https://github.com/dark-factory-build/dark-factory/releases/tag/v1.2.4", state: "available" }, installed: { ...build, state: "verified" }, running: { ...build, state: "ready" } }, runtime: build, connected: true, sourceFresh: true, hostedSource: "hosted-sha", deliveries: [], ...props }));

test("maintenance keeps release, files, host and console observations separate", () => {
  const markup = render();
  for (const heading of ["Available release", "Installed service files", "Running service report", "Private runtime observation", "Loaded hosted console"]) assert.match(markup, new RegExp(heading));
  assert.match(markup, /Open release/);
  assert.match(markup, /scripts\/deploy-runtime\.py/);
  assert.doesNotMatch(markup, /up to date/i);
});

test("stale or disconnected evidence does not confirm an update", () => {
  const markup = render({ connected: false, sourceFresh: false, deliveries: [{ repository: "dark-factory-build/dark-factory", id: "runtime", kind: "release", destination: "runtime", revision: "b".repeat(40), state: "verified", pull_requests: [] }] });
  assert.match(markup, /No runtime update is confirmed/);
  assert.match(markup, /last recorded verified/);
});
