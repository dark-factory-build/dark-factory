import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

test("the documented ?fixture page is the whole console, labelled, over the fixture state", () => {
  const out = join(mkdtempSync(join(tmpdir(), "df-fixture-")), "fixture.html");
  execFileSync(process.execPath, [fileURLToPath(new URL("../examples/floor-preview.mjs", import.meta.url)), "fixture", out]);
  const page = readFileSync(out, "utf8");
  assert.match(page, /<body[^>]*><p[^>]*>Fixture: simulated data, static render/);
  assert.match(page, /class="dfFactoryConsole[ "]/);
  for (const text of ["Review the state projection", "Tighten the queue ordering", "Builder One", "North Workshop"]) assert.ok(page.includes(text), text);
});
