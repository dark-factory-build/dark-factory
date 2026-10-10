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
  const firstOutput = await first.ready;
  assert.match(firstOutput, /http:\/\/127\.0\.0\.1:5196\/\?fixture/);
  assert.equal(firstOutput.trim(), "http://127.0.0.1:5196/?fixture");
  const second = spawnPreview();
  const [secondError, secondOutput] = await Promise.all([second.stderr, second.stdout]);
  await second.exit;
  assert.match(secondError, /EADDRINUSE/);
  assert.doesNotMatch(secondOutput, /http:\/\/127\.0\.0\.1:5196/);
  first.kill("SIGINT");
  await first.exit;
  assert.match(readFileSync(preview, "utf8"), /setTimeout\(stop, 30 \* 60 \* 1000\)/);
});

function spawnPreview() {
  const child = spawn(process.execPath, [preview, "fixture"]);
  const stdout = captureOutput(child.stdout, child, "stdout", true);
  const stderr = captureOutput(child.stderr, child, "stderr", false);
  return {
    ready: stdout.ready,
    stdout: stdout.output,
    stderr: stderr.output,
    exit: new Promise((resolve) => child.once("close", resolve)),
    kill: (signal) => child.kill(signal),
  };
}

function captureOutput(stream, child, label, required) {
  let value = "";
  let readyResolve;
  let readyReject;
  let outputResolve;
  let outputReject;
  let readySettled = false;
  const ready = new Promise((resolve, reject) => { readyResolve = resolve; readyReject = reject; });
  const output = new Promise((resolve, reject) => { outputResolve = resolve; outputReject = reject; });
  const fail = () => {
    const error = new Error(`${label} closed before producing output`);
    if (!readySettled) { readySettled = true; (required ? readyReject : readyResolve)(required ? error : value); }
    outputReject(error);
  };
  child.once("error", fail);
  stream.on("data", (chunk) => {
    value += chunk;
    if (!readySettled) { readySettled = true; readyResolve(value); }
  });
  stream.once("close", () => {
    if (!readySettled) {
      readySettled = true;
      const error = new Error(`${label} closed before producing output`);
      (required ? readyReject : readyResolve)(required ? error : value);
    }
    outputResolve(value);
  });
  return { ready, output };
}
