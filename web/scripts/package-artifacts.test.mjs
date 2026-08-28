import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
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

function fixture() {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  return { tempRoot, output: join(tempRoot, "output") };
}

test("public artifacts bind clean HEAD, protocol, exact dependencies, and bytes", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  const output = join(tempRoot, "output");
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

    const consumer = join(tempRoot, "consumer");
    mkdirSync(consumer);
    writeFileSync(join(consumer, "package.json"), JSON.stringify({ name: "artifact-consumer", private: true, type: "module" }));
    const env = { ...process.env, npm_config_cache: join(tempRoot, "npm-cache"), npm_config_offline: "true", npm_config_registry: "http://127.0.0.1:9/" };
    execFileSync("npm", ["install", "--offline", "--ignore-scripts", "--no-package-lock", join(output, client.artifact.filename)], { cwd: consumer, env, stdio: "pipe" });
    execFileSync(process.execPath, ["--input-type=module", "-e", "import { PROTOCOL_VERSION } from '@dark-factory/client'; const p = await import('@dark-factory/client/provenance', { with: { type: 'json' } }); if (PROTOCOL_VERSION !== 1 || p.default.package.name !== '@dark-factory/client') throw new Error('bad provenance export');"], { cwd: consumer, env, stdio: "pipe" });
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("artifact pack refuses caller provenance and dirty source", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  const output = join(tempRoot, "output");
  const dirty = join(webRoot, "packages", "client", "src", ".artifact-dirty-sentinel");
  const sourceManifest = join(webRoot, "packages", "client", "src", "manifest.ts");
  const sourceText = readFileSync(sourceManifest, "utf8");
  try {
    expectFailure(() => run("pack", output, "--source-sha", "0000000000000000000000000000000000000000"), "cannot be caller-selected");
    writeFileSync(dirty, "dirty\n");
    expectFailure(() => run("pack", output), "worktree is dirty");
    assert.equal(existsSync(output), false);
    rmSync(dirty);
    writeFileSync(sourceManifest, `${sourceText}\n// protocol version 999\n`);
    expectFailure(() => run("pack", output), "worktree is dirty");
  } finally {
    writeFileSync(sourceManifest, sourceText);
    rmSync(dirty, { force: true });
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("verification rejects stale protocol identity and changed tarball bytes", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-public-artifacts-"));
  const output = join(tempRoot, "output");
  try {
    run("pack", output);
    const path = join(output, "dark-factory-public-artifacts.json");
    const original = manifest(output);
    const staleProtocol = structuredClone(original);
    staleProtocol.protocol.version = 2;
    writeFileSync(path, `${JSON.stringify(staleProtocol, null, 2)}\n`);
    expectFailure(() => run("verify", output), "manifest protocol is invalid");
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
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("verification rejects archive inventory, runtime protocol, and strict manifest mutations", () => {
  const { tempRoot, output } = fixture();
  try {
    run("pack", output);
    const path = join(output, "dark-factory-public-artifacts.json");
    const originalText = readFileSync(path, "utf8");
    const original = manifest(output);
    const writeManifest = (value) => writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`);

    const unknown = structuredClone(original);
    unknown.unexpected = true;
    writeManifest(unknown);
    expectFailure(() => run("verify", output), "manifest has unknown or missing keys");
    writeFileSync(path, originalText);

    writeFileSync(path, originalText.replace('"schemaVersion": 1', '"schemaVersion": 1,\n  "schemaVersion": 1'));
    expectFailure(() => run("verify", output), "duplicate key");
    writeFileSync(path, originalText);

    writeFileSync(path, originalText.replace("{\n", "{\n\n"));
    expectFailure(() => run("verify", output), "not canonical JSON");
    writeFileSync(path, originalText);

    const typeConfused = structuredClone(original);
    typeConfused.protocol.version = "1";
    writeManifest(typeConfused);
    expectFailure(() => run("verify", output), "manifest.protocol.version must be an integer");
    writeFileSync(path, originalText);

    const traversal = structuredClone(original);
    traversal.packages[clientName].artifact.filename = "../client.tgz";
    writeManifest(traversal);
    expectFailure(() => run("verify", output), "package identity is stale or forged");
    writeFileSync(path, originalText);

    const floating = structuredClone(original);
    floating.packages[uiName].dependency.version = "workspace:*";
    writeManifest(floating);
    expectFailure(() => run("verify", output), "exact client artifact and version");
    writeFileSync(path, originalText);

    const extra = join(output, "stale.txt");
    writeFileSync(extra, "stale\n");
    expectFailure(() => run("verify", output), "stale or unexpected files");
    rmSync(extra);

    const clientArchive = join(output, original.packages[clientName].artifact.filename);
    const backup = join(tempRoot, "client-backup.tgz");
    renameSync(clientArchive, backup);
    symlinkSync(backup, clientArchive);
    expectFailure(() => run("verify", output), "output contains a symlink");
    rmSync(clientArchive);
    renameSync(backup, clientArchive);

    const compiledManifest = join(webRoot, "packages", "client", "dist", "src", "manifest.js");
    const compiledText = readFileSync(compiledManifest, "utf8");
    assert.match(compiledText, /PROTOCOL_VERSION = 1/);
    writeFileSync(compiledManifest, compiledText.replace("PROTOCOL_VERSION = 1", "PROTOCOL_VERSION = 2"));
    expectFailure(() => run("verify", output), "compiled client protocol version");
    writeFileSync(compiledManifest, compiledText);
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("pack uses trusted absolute tools and leaves no partial output", () => {
  const { tempRoot, output } = fixture();
  const fakeBin = join(tempRoot, "fake-bin");
  mkdirSync(fakeBin);
  for (const name of ["git", "tar", "corepack"]) {
    const path = join(fakeBin, name);
    writeFileSync(path, "#!/bin/sh\nexit 97\n");
    chmodSync(path, 0o700);
  }
  try {
    run("pack", output);
    const env = { PATH: fakeBin, npm_config_offline: "true", npm_config_registry: "http://127.0.0.1:9/" };
    execFileSync(process.execPath, [script, "verify", "--output", output], { cwd: webRoot, env, stdio: "pipe" });

    const existing = join(tempRoot, "existing");
    mkdirSync(existing);
    writeFileSync(join(existing, "sentinel"), "keep\n");
    expectFailure(() => run("pack", existing), "output already exists");
    assert.equal(readFileSync(join(existing, "sentinel"), "utf8"), "keep\n");

    const symlinkTarget = join(tempRoot, "symlink-target");
    mkdirSync(symlinkTarget);
    const symlinkOutput = join(tempRoot, "symlink-output");
    symlinkSync(symlinkTarget, symlinkOutput);
    expectFailure(() => run("pack", symlinkOutput), "output must not be a symlink");

    const inside = join(webRoot, ".artifact-output-must-not-exist");
    expectFailure(() => run("pack", inside), "outside the source tree");
    assert.equal(existsSync(inside), false);
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});
