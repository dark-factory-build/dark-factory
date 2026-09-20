import assert from "node:assert/strict";
import test from "node:test";
import { sharedChecks, productionHeight } from "../dist/src/production-area.js";

test("shared CI is one execution across PRs but repository identities remain distinct", () => {
  const check = { id: "ci:1", repository: "owner/one", scope: "merge_group" };
  const items = [{ checks: [check] }, { checks: [check, { ...check, repository: "owner/two" }] }];
  assert.equal(sharedChecks(items).length, 2);
  assert.ok(productionHeight(320, 20) > productionHeight(320, 2));
});
