import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const webRoot = new URL("..", import.meta.url).pathname.slice(0, -1);
const script = join(webRoot, "scripts", "package-artifacts.mjs");
const clientName = "@dark-factory/client";
const uiName = "@dark-factory/ui";

function run(command, output, ...extra) {
  return execFileSync(process.execPath, [script, command, "--output", output, ...extra], {
    cwd: webRoot,
    encoding: "utf8",
    env: { ...process.env, COREPACK_ENABLE_NETWORK: "0", npm_config_registry: "http://127.0.0.1:9/" },
    stdio: "pipe",
  });
}

function manifest(output) {
  return JSON.parse(readFileSync(join(output, "dark-factory-public-artifacts.json"), "utf8"));
}

function expectFailure(fn, text) {
  assert.throws(fn, (error) => error.status === 1 && error.stderr.includes(text));
}

test("public artifacts bind clean HEAD, protocol, exact dependencies, and bytes", () => {
  const output = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  try {
    const packed = JSON.parse(run("pack", output));
    assert.equal(packed.schemaVersion, 1);
    assert.match(packed.source.commit, /^[0-9a-f]{40}$/);
    assert.equal(packed.source.clean, true);
    assert.deepEqual(packed.protocol, { name: "dark-factory/browser/v1", version: 1 });
    assert.deepEqual(packed.buildTools.pnpm, "11.19.0");
    assert.deepEqual(packed.buildTools.typescript, "5.8.3");
    const client = packed.packages[clientName];
    const ui = packed.packages[uiName];
    for (const entry of [client, ui]) {
      const bytes = readFileSync(join(output, entry.artifact.filename));
      const hash = createHash("sha512").update(bytes).digest();
      assert.equal(entry.artifact.bytes, bytes.length);
      assert.equal(entry.artifact.sha512, hash.toString("hex"));
      assert.equal(entry.artifact.integrity, `sha512-${hash.toString("base64")}`);
    }
    assert.deepEqual(ui.dependency, {
      name: clientName,
      version: client.package.version,
      tarball: client.artifact.filename,
      integrity: client.artifact.integrity,
    });
    assert.equal(ui.artifact.version, "0.1.0");
    assert.equal(client.package.name, clientName);
    assert.equal(ui.package.name, uiName);
    assert.doesNotMatch(JSON.stringify(ui), /workspace:|file:|link:|\^|~|latest/);
    assert.doesNotMatch(JSON.stringify(client), /workspace:|file:|link:|\^|~|latest/);
    run("verify", output);
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});

test("artifact pack refuses caller provenance and dirty source", () => {
  const output = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  const dirty = join(webRoot, "packages", "client", "src", ".artifact-dirty-sentinel");
  try {
    expectFailure(() => run("pack", output, "--source-sha", "0000000000000000000000000000000000000000"), "cannot be caller-selected");
    writeFileSync(dirty, "dirty\n");
    expectFailure(() => run("pack", output), "worktree is dirty");
  } finally {
    rmSync(dirty, { force: true });
    rmSync(output, { recursive: true, force: true });
  }
});

test("verification rejects stale protocol identity and changed tarball bytes", () => {
  const output = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  try {
    run("pack", output);
    const path = join(output, "dark-factory-public-artifacts.json");
    const original = manifest(output);
    const staleProtocol = structuredClone(original);
    staleProtocol.protocol.version = 2;
    writeFileSync(path, `${JSON.stringify(staleProtocol, null, 2)}\n`);
    expectFailure(() => run("verify", output), "protocol provenance is stale or forged");
    writeFileSync(path, `${JSON.stringify(original, null, 2)}\n`);

    const stalePackage = structuredClone(original);
    stalePackage.packages[clientName].package.version = "9.9.9";
    writeFileSync(path, `${JSON.stringify(stalePackage, null, 2)}\n`);
    expectFailure(() => run("verify", output), "package identity is stale or forged");
    writeFileSync(path, `${JSON.stringify(original, null, 2)}\n`);

    const staleBinding = structuredClone(original);
    staleBinding.packages[uiName].dependency.integrity = "sha512-forged";
    writeFileSync(path, `${JSON.stringify(staleBinding, null, 2)}\n`);
    expectFailure(() => run("verify", output), "exact client artifact and version");
    writeFileSync(path, `${JSON.stringify(original, null, 2)}\n`);

    const tarball = join(output, original.packages[clientName].artifact.filename);
    const bytes = readFileSync(tarball);
    writeFileSync(tarball, Buffer.concat([bytes, Buffer.from("mutation\n")]));
    expectFailure(() => run("verify", output), "tarball integrity failed");
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});
