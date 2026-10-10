import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const preview = fileURLToPath(new URL("../examples/floor-preview.mjs", import.meta.url));

test("the documented ?fixture page is the whole console, labelled, over the fixture state", () => {
  const out = join(mkdtempSync(join(tmpdir(), "df-fixture-")), "fixture.html");
  execFileSync(process.execPath, [preview, "fixture", out]);
  const page = readFileSync(out, "utf8");
  assert.match(page, /<body[^>]*><p[^>]*>Fixture: simulated data, static render/);
  assert.match(page, /class="dfFactoryConsole[ "]/);
  for (const text of ["Review the state projection", "Tighten the queue ordering", "Builder One", "North Workshop"]) assert.ok(page.includes(text), text);
});

test("fixture server advertises only after binding and has a bounded lifecycle", async () => {
  const first = spawnPreview();
  const firstOutput = await first.stdout;
  assert.match(firstOutput, /http:\/\/127\.0\.0\.1:5196\/\?fixture/);
  assert.equal(firstOutput.trim(), "http://127.0.0.1:5196/?fixture");
  const second = spawnPreview();
  assert.match(await second.stderr, /EADDRINUSE/);
  assert.doesNotMatch(second.stdoutValue, /http:\/\/127\.0\.0\.1:5196/);
  first.kill("SIGINT");
  second.kill("SIGINT");
  assert.match(readFileSync(preview, "utf8"), /setTimeout\(stop, 30 \* 60 \* 1000\)/);
});

function spawnPreview() {
  const child = spawn(process.execPath, [preview, "fixture"]);
  const stdout = captureOutput(child.stdout, child, "stdout");
  const stderr = captureOutput(child.stderr, child, "stderr");
  return {
    stdout: stdout.promise,
    stderr: stderr.promise,
    get stdoutValue() { return stdout.value; },
    kill: (signal) => child.kill(signal),
  };
}

function captureOutput(stream, child, label) {
  let value = "";
  const promise = new Promise((resolve, reject) => {
    const fail = () => reject(new Error(`${label} closed before producing output`));
    child.once("error", fail);
    stream.once("close", fail);
    stream.on("data", (chunk) => { value += chunk; resolve(value); });
  });
  return { promise, get value() { return value; } };
}
