import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, renameSync, rmSync, symlinkSync, truncateSync, writeFileSync } from "node:fs";
import { tmpdir, userInfo } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { inspectExecutable, toolTreeDigest } from "./package-artifacts.mjs";

const webRoot = new URL("..", import.meta.url).pathname.slice(0, -1);
const script = join(webRoot, "scripts", "package-artifacts.mjs");
const clientName = "@dark-factory/client";
const uiName = "@dark-factory/ui";

function runWithEnv(command, output, envOverrides, ...extra) {
  return execFileSync(process.execPath, [script, command, "--output", output, ...extra], {
    cwd: webRoot,
    encoding: "utf8",
    env: { ...process.env, COREPACK_ENABLE_NETWORK: "0", npm_config_registry: "http://127.0.0.1:9/", ...envOverrides },
    stdio: "pipe",
  });
}

function run(command, output, ...extra) {
  return runWithEnv(command, output, {}, ...extra);
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

function archiveMembers(root) {
  const members = [];
  const walk = (directory, prefix) => {
    for (const name of readdirSync(directory)) {
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const stat = lstatSync(path);
      if (stat.isDirectory()) walk(path, relative);
      else members.push(relative);
    }
  };
  walk(root, "");
  return members;
}

function rewriteArchive(archive, destination, mutate) {
  const unpack = mkdtempSync(join(tmpdir(), "dark-factory-archive-edit-"));
  try {
    execFileSync("/usr/bin/tar", ["-xzf", archive, "-C", unpack], { stdio: "pipe" });
    mutate(unpack);
    execFileSync("/usr/bin/tar", ["-czf", destination, "-C", unpack, ...archiveMembers(unpack)], { stdio: "pipe" });
  } finally {
    rmSync(unpack, { recursive: true, force: true });
  }
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
    assert.deepEqual(packed.buildTools.node, {
      version: "22.20.0",
      bytes: 111332720,
      mode: "0755",
      sha512: "1476738e01ca6e0a7c4468e30645c91b59c56a2d0eda60510a20a28a72be790954694e06f6074da784cb17f4ea61cac1a1c349326d1eb2e378ba2000548d3599",
    });
    assert.deepEqual(packed.buildTools.tar, {
      version: "bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8",
      bytes: 275184,
      mode: "0755",
      sha512: "f7c721e9624aad59ec244739017b1354cf21a90cc86e32314419c57271d90d95af574885d3c36e2da34ff928032eb9e5954cf2044422d022b4239b845b06c323",
    });
    assert.equal(Object.hasOwn(packed.buildTools, "corepack"), false);
    const reviewed = JSON.parse(readFileSync(join(webRoot, "toolchain-integrity.json")));
    assert.equal(packed.buildTools.pnpmTreeSha512, reviewed.pnpm.treeSha512);
    assert.equal(packed.buildTools.typescriptTreeSha512, reviewed.typescript.treeSha512);
    assert.equal(packed.buildTools.typescriptIntegrity, reviewed.typescript.lockfileIntegrity);
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
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
    writeFileSync(path, `${JSON.stringify(original, null, 2)}\n`);

    const staleBinding = structuredClone(original);
    staleBinding.packages[uiName].dependency.integrity = "sha512-forged";
    writeFileSync(path, `${JSON.stringify(staleBinding, null, 2)}\n`);
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
    writeFileSync(path, `${JSON.stringify(original, null, 2)}\n`);

    const tarball = join(output, original.packages[clientName].artifact.filename);
    const bytes = readFileSync(tarball);
    writeFileSync(tarball, Buffer.concat([bytes, Buffer.from("mutation\n")]));
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
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
    expectFailure(() => run("verify", output), "not canonical JSON");
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
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
    writeFileSync(path, originalText);

    const floating = structuredClone(original);
    floating.packages[uiName].dependency.version = "workspace:*";
    writeManifest(floating);
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
    writeFileSync(path, originalText);

    const extra = join(output, "stale.txt");
    writeFileSync(extra, "stale\n");
    expectFailure(() => run("verify", output), "stale or unexpected files");
    rmSync(extra);

    const clientArchive = join(output, original.packages[clientName].artifact.filename);
    const backup = join(tempRoot, "client-backup.tgz");
    renameSync(clientArchive, backup);
    symlinkSync(backup, clientArchive);
    expectFailure(() => run("verify", output), "artifact output contains a symlink");
    rmSync(clientArchive);
    renameSync(backup, clientArchive);

    const sourceManifest = join(webRoot, "packages", "client", "src", "manifest.ts");
    const sourceText = readFileSync(sourceManifest, "utf8");
    writeFileSync(sourceManifest, sourceText.replace("PROTOCOL_VERSION = 1", "PROTOCOL_VERSION = 2"));
    expectFailure(() => run("verify", output), "worktree is dirty");
    writeFileSync(sourceManifest, sourceText);
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
    const env = { PATH: fakeBin, npm_config_offline: "true", npm_config_registry: "http://127.0.0.1:9/" };
    runWithEnv("pack", output, env);
    runWithEnv("verify", output, env);

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

test("tool tree commitment has framed content and rejects unsafe copied trees", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-tool-tree-"));
  const pnpmRoot = join(userInfo().homedir, ".cache", "node", "corepack", "v1", "pnpm", "11.19.0");
  try {
    const first = join(tempRoot, "first");
    const second = join(tempRoot, "second");
    mkdirSync(first);
    mkdirSync(second);
    writeFileSync(join(first, "a"), "bc");
    writeFileSync(join(second, "ab"), "c");
    assert.notEqual(toolTreeDigest(first), toolTreeDigest(second));

    const copy = join(tempRoot, "pnpm");
    cpSync(pnpmRoot, copy, { recursive: true });
    const expected = toolTreeDigest(pnpmRoot);
    assert.equal(toolTreeDigest(copy), expected);
    const existingName = readdirSync(copy)[0];
    writeFileSync(join(copy, existingName), `${readFileSync(join(copy, existingName), "utf8")}mutation`);
    assert.notEqual(toolTreeDigest(copy), expected);

    const symlinkTree = join(tempRoot, "symlink");
    mkdirSync(symlinkTree);
    writeFileSync(join(symlinkTree, "file"), "x");
    symlinkSync("file", join(symlinkTree, "link"));
    assert.throws(() => toolTreeDigest(symlinkTree), /contains symlink/);

    const caseTree = join(tempRoot, "case");
    mkdirSync(caseTree);
    writeFileSync(join(caseTree, "Ʃ"), "x");
    writeFileSync(join(caseTree, "ʃ"), "x");
    // The macOS case/Unicode-insensitive filesystem may collapse this pair
    // while creating it; Linux CI retains both and exercises the guard.
    if (readdirSync(caseTree).length === 2) assert.throws(() => toolTreeDigest(caseTree), /case-confusable/);

    const countTree = join(tempRoot, "count");
    mkdirSync(countTree);
    for (let index = 0; index < 4097; index += 1) writeFileSync(join(countTree, `file-${index}`), "x");
    assert.throws(() => toolTreeDigest(countTree), /too many files/);

    const sizeTree = join(tempRoot, "size");
    mkdirSync(sizeTree);
    const oversized = join(sizeTree, "oversized");
    writeFileSync(oversized, "x");
    truncateSync(oversized, 128 * 1024 * 1024 + 1);
    assert.throws(() => toolTreeDigest(sizeTree), /too large/);
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("pack ignores caller Corepack home and rejects changed reviewed tool content", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-toolchain-"));
  const output = join(tempRoot, "output");
  const fakeCorepackHome = join(tempRoot, "fake-corepack");
  const pnpmRoot = join(userInfo().homedir, ".cache", "node", "corepack", "v1", "pnpm", "11.19.0");
  const tscRoot = realpathSync(join(webRoot, "node_modules", "typescript"));
  const tscWrapper = join(tscRoot, "bin", "tsc");
  const tscBytes = readFileSync(tscWrapper);
  try {
    mkdirSync(join(fakeCorepackHome, "v1", "pnpm"), { recursive: true });
    cpSync(pnpmRoot, join(fakeCorepackHome, "v1", "pnpm", "11.19.0"), { recursive: true });
    writeFileSync(join(fakeCorepackHome, "v1", "pnpm", "11.19.0", "package.json"), "not the reviewed package\n");
    runWithEnv("pack", output, { COREPACK_HOME: fakeCorepackHome });
    runWithEnv("verify", output, { COREPACK_HOME: fakeCorepackHome });
    writeFileSync(tscWrapper, Buffer.concat([tscBytes, Buffer.from("\n// changed after pack\n")]));
    expectFailure(() => run("verify", output), "installed TypeScript content differs");
    writeFileSync(tscWrapper, tscBytes);
    rmSync(output, { recursive: true, force: true });

    writeFileSync(tscWrapper, Buffer.concat([tscBytes, Buffer.from("\n// changed same-version wrapper\n")]));
    expectFailure(() => run("pack", output), "installed TypeScript content differs");
    assert.equal(existsSync(output), false);
  } finally {
    writeFileSync(tscWrapper, tscBytes);
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("pack rejects a changed reviewed digest or lockfile integrity", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-toolchain-"));
  const output = join(tempRoot, "output");
  const integrityPath = join(webRoot, "toolchain-integrity.json");
  const lockfilePath = join(webRoot, "pnpm-lock.yaml");
  const integrityText = readFileSync(integrityPath, "utf8");
  const lockfileText = readFileSync(lockfilePath, "utf8");
  try {
    writeFileSync(integrityPath, integrityText.replace(/"treeSha512": "[0-9a-f]{128}"/, `"treeSha512": "${"0".repeat(128)}"`));
    expectFailure(() => run("pack", output), "cached pnpm content differs");
    writeFileSync(integrityPath, integrityText);

    const originalSRI = "sha512-p1diW6TqL9L07nNxvRMM7hMMw4c5XOo/1ibL4aAIGmSAt9slTE1Xgw5KWuof2uTOvCg9BY7ZRi+GaF+7sfgPeQ==";
    writeFileSync(lockfilePath, lockfileText.replace(originalSRI, `${originalSRI.slice(0, -3)}abc`));
    expectFailure(() => run("pack", output), "lockfile TypeScript integrity differs");
  } finally {
    writeFileSync(integrityPath, integrityText);
    writeFileSync(lockfilePath, lockfileText);
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("reviewed executable bytes reject same-version Node and tar mutations", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-executables-"));
  const reviewed = JSON.parse(readFileSync(join(webRoot, "toolchain-integrity.json")));
  try {
    const nodeCopy = join(tempRoot, "node");
    cpSync(process.execPath, nodeCopy);
    assert.doesNotThrow(() => inspectExecutable(nodeCopy, "Node copy", reviewed.node));
    writeFileSync(nodeCopy, Buffer.concat([readFileSync(nodeCopy), Buffer.from("mutation")]));
    assert.throws(() => inspectExecutable(nodeCopy, "Node copy", reviewed.node), /content differs/);

    const tarCopy = join(tempRoot, "tar");
    writeFileSync(tarCopy, readFileSync(realpathSync("/usr/bin/tar")));
    chmodSync(tarCopy, 0o755);
    assert.doesNotThrow(() => inspectExecutable(tarCopy, "tar copy", reviewed.tar));
    writeFileSync(tarCopy, Buffer.concat([readFileSync(tarCopy), Buffer.from("mutation")]));
    assert.throws(() => inspectExecutable(tarCopy, "tar copy", reviewed.tar), /content differs/);
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("pack produces two byte-identical reconstructions and verify rebuilds independently", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-determinism-"));
  const first = join(tempRoot, "first");
  const second = join(tempRoot, "second");
  try {
    run("pack", first);
    run("pack", second);
    for (const name of ["dark-factory-client-0.1.0.tgz", "dark-factory-ui-0.1.0.tgz", "dark-factory-public-artifacts.json"]) {
      assert.deepEqual(readFileSync(join(first, name)), readFileSync(join(second, name)), name);
    }
    run("verify", second);
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("verify rejects forged self-consistent archive claims by clean reconstruction", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-forge-"));
  const output = join(tempRoot, "output");
  try {
    run("pack", output);
    const path = join(output, "dark-factory-public-artifacts.json");
    const forged = manifest(output);
    const clientArchive = join(output, forged.packages[clientName].artifact.filename);
    const clientBytes = Buffer.concat([readFileSync(clientArchive), Buffer.from("forged client bytes\n")]);
    writeFileSync(clientArchive, clientBytes);
    const clientHash = createHash("sha512").update(clientBytes).digest();
    forged.packages[clientName].artifact.bytes = clientBytes.length;
    forged.packages[clientName].artifact.sha512 = clientHash.toString("hex");
    forged.packages[clientName].artifact.integrity = `sha512-${clientHash.toString("base64")}`;
    forged.packages[uiName].dependency.integrity = forged.packages[clientName].artifact.integrity;
    const uiArchive = join(output, forged.packages[uiName].artifact.filename);
    const rewrittenUi = join(tempRoot, "rewritten-ui.tgz");
    rewriteArchive(uiArchive, rewrittenUi, (root) => {
      const packagePath = join(root, "package/package.json");
      const packageValue = JSON.parse(readFileSync(packagePath, "utf8"));
      packageValue.dependencies[clientName] = forged.packages[uiName].dependency.version;
      writeFileSync(packagePath, JSON.stringify(packageValue, null, 2));
      const provenancePath = join(root, "package/dist/src/provenance.json");
      const provenanceValue = JSON.parse(readFileSync(provenancePath, "utf8"));
      provenanceValue.dependency.integrity = forged.packages[uiName].dependency.integrity;
      writeFileSync(provenancePath, `${JSON.stringify(provenanceValue, null, 2)}\n`);
    });
    const rewrittenUiBytes = readFileSync(rewrittenUi);
    writeFileSync(uiArchive, rewrittenUiBytes);
    const uiHash = createHash("sha512").update(rewrittenUiBytes).digest();
    forged.packages[uiName].artifact.bytes = rewrittenUiBytes.length;
    forged.packages[uiName].artifact.sha512 = uiHash.toString("hex");
    forged.packages[uiName].artifact.integrity = `sha512-${uiHash.toString("base64")}`;
    writeFileSync(path, `${JSON.stringify(forged, null, 2)}\n`);
    expectFailure(() => run("verify", output), "differs from clean reconstruction");
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("verify rejects archive members with unsafe shape, mode, JSON, count, and size", () => {
  const tempRoot = mkdtempSync(join(tmpdir(), "dark-factory-archive-guards-"));
  const output = join(tempRoot, "output");
  try {
    run("pack", output);
    const archive = join(output, "dark-factory-client-0.1.0.tgz");
    const original = readFileSync(archive);
    const mutateAndCheck = (mutate, message) => {
      const edited = join(tempRoot, "edited.tgz");
      rewriteArchive(archive, edited, mutate);
      writeFileSync(archive, readFileSync(edited));
      expectFailure(() => run("verify", output), message);
      writeFileSync(archive, original);
    };
    mutateAndCheck((root) => chmodSync(join(root, "package/dist/src/control.js"), 0o777), "mode 0644");
    mutateAndCheck((root) => {
      const target = join(root, "package/dist/src/control.js");
      rmSync(target);
      symlinkSync("index.js", target);
    }, "mode 0644");
    mutateAndCheck((root) => {
      const target = join(root, "package/dist/src/control.js");
      rmSync(target);
      mkdirSync(target);
      writeFileSync(join(target, "nested"), "x");
    }, "inventory mismatch");
    mutateAndCheck((root) => {
      const packagePath = join(root, "package/package.json");
      const text = readFileSync(packagePath, "utf8");
      writeFileSync(packagePath, text.replace('"name": "@dark-factory/client",', '"name": "@dark-factory/client",\n  "name": "@dark-factory/client",'));
    }, "not canonical JSON");
    mutateAndCheck((root) => truncateSync(join(root, "package/dist/src/control.js"), 512 * 1024 + 1), "member is too large");
    mutateAndCheck((root) => {
      for (let index = 0; index < 50; index += 1) writeFileSync(join(root, `extra-${index}`), "x");
    }, "too many members");
    mutateAndCheck((root) => {
      for (const name of ["control.js", "errors.js", "index.js", "manifest.js", "session.js", "state.js", "terminal.js", "terminal_session.js", "transcript.js", "control.d.ts", "errors.d.ts"]) {
        writeFileSync(join(root, "package/dist/src", name), Buffer.alloc(200 * 1024, 7));
      }
    }, "members are too large in total");
    const oversized = Buffer.concat([original, Buffer.alloc(2 * 1024 * 1024, 7)]);
    writeFileSync(archive, oversized);
    expectFailure(() => run("verify", output), "archive is too large");
  } finally {
    rmSync(tempRoot, { recursive: true, force: true });
  }
});
