import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ProductionArea, productionConnector, sharedChecks, productionHeight } from "../dist/src/production-area.js";

test("shared CI is one execution across PRs but repository identities remain distinct", () => {
  const check = { id: "ci:1", repository: "owner/one", scope: "merge_group" };
  const items = [{ checks: [check] }, { checks: [check, { ...check, repository: "owner/two" }] }];
  assert.equal(sharedChecks(items).length, 2);
  assert.equal(sharedChecks([{ checks: [check], pullRequest: { state: "merged" } }]).length, 0);
  assert.ok(productionHeight(320, 0) < productionHeight(320, 1));
  assert.ok(productionHeight(320, 20) > productionHeight(320, 2));
});

test("production connector is rendered from the two room rectangles", () => {
  const upper = { x: 48, y: 120, width: 200, height: 80 };
  const lower = { x: 8, y: 240, width: 304, height: 120 };
  assert.deepEqual(productionConnector(upper, lower), { x: 48, y: 200, width: 32, height: 40 });
  const markup = renderToStaticMarkup(createElement(ProductionArea, {
    items: [], width: 320, top: lower.y - 8, upperRoom: upper, onSelect() {},
  }));
  assert.match(markup, /<rect x="48" y="-32" width="32" height="40" fill="url\(#df-floor\)"><\/rect>/);
  assert.match(markup, /M8 8H48 M80 8H312/);
});
