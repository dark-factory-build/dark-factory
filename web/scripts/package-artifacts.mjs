import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, join, resolve, sep } from "node:path";
import { tmpdir, userInfo } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(webRoot, "..");
const manifestName = "dark-factory-public-artifacts.json";
const gitPath = "/usr/bin/git";
const tarPath = "/usr/bin/tar";
const toolchainIntegrityPath = join(webRoot, "toolchain-integrity.json");
const toolTreeVersion = "dark-factory/tool-tree/v1";
const maxToolTreeFiles = 4096;
const maxToolTreeBytes = 128 * 1024 * 1024;
const maxToolTreePathBytes = 4096;
const packages = {
  client: { key: "client", name: "@dark-factory/client", root: join(webRoot, "packages", "client") },
  ui: { key: "ui", name: "@dark-factory/ui", root: join(webRoot, "packages", "ui") },
};

// This is the deliberate public artifact surface. Adding a source export or
// build output requires an inventory change, so stale and accidental files
// cannot silently enter an artifact.
const inventory = {
  client: [
    "control.d.ts", "control.js", "errors.d.ts", "errors.js", "index.d.ts", "index.js",
    "manifest.d.ts", "manifest.js", "session.d.ts", "session.js", "state.d.ts", "state.js",
    "terminal.d.ts", "terminal.js", "terminal_session.d.ts", "terminal_session.js",
    "transcript.d.ts", "transcript.js", "provenance.json",
  ],
  ui: [
    "factory-app-controller.d.ts", "factory-app-controller.js", "factory-app.d.ts", "factory-app.js",
    "factory-console.css", "factory-console.d.ts", "factory-console.js", "index.d.ts", "index.js",
    "terminal-controller.d.ts", "terminal-controller.js", "xterm-terminal.d.ts", "xterm-terminal.js",
    "provenance.json",
  ],
};

const commandEnvironment = {
  CI: "true",
  COREPACK_DEFAULT_TO_LATEST: "0",
  COREPACK_ENABLE_NETWORK: "0",
  HOME: tmpdir(),
  LANG: "C",
  LC_ALL: "C",
  TMPDIR: tmpdir(),
  GIT_CONFIG_GLOBAL: "/dev/null",
  GIT_CONFIG_SYSTEM: "/dev/null",
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_TERMINAL_PROMPT: "0",
  GIT_PAGER: "cat",
  npm_config_ignore_scripts: "true",
  npm_config_offline: "true",
  npm_config_registry: "http://127.0.0.1:9/",
  npm_config_update_notifier: "false",
};

function fail(message) {
  throw new Error(`package artifacts: ${message}`);
}

function readJson(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    fail(`could not read JSON ${path}: ${error.message}`);
  }
}

function executable(path, label) {
  let target;
  try {
    target = realpathSync(path);
    const stat = lstatSync(target);
    const uid = typeof process.getuid === "function" ? process.getuid() : -1;
    if (!stat.isFile() || (stat.mode & 0o111) === 0 || (uid !== -1 && stat.uid !== uid && stat.uid !== 0)) {
      fail(`${label} is not a trusted executable`);
    }
  } catch (error) {
    if (error.message.startsWith("package artifacts:")) throw error;
    fail(`could not resolve ${label}: ${error.message}`);
  }
  return target;
}

function git(...args) {
  return execFileSync(executable(gitPath, "git"), args, {
    cwd: repositoryRoot,
    env: { ...commandEnvironment, GIT_OPTIONAL_LOCKS: "0" },
    encoding: "utf8",
  }).trim();
}

function currentSource() {
  const commit = git("rev-parse", "--verify", "HEAD^{commit}");
  if (git("status", "--porcelain=v1", "--untracked-files=all")) {
    fail("source worktree is dirty; commit or remove all changes first");
  }
  return commit;
}

function packageJson(packageInfo) {
  return readJson(join(packageInfo.root, "package.json"));
}

function strictJsonText(text, label) {
  if (text.length > 65536) fail(`${label} is too large`);
  let value;
  try { value = JSON.parse(text); } catch (error) { fail(`${label} is invalid JSON: ${error.message}`); }
  // Canonical re-serialization also rejects duplicate keys: JSON.parse drops
  // the duplicate, making the original bytes differ from the canonical form.
  if (stableJson(value) !== text) fail(`${label} is not canonical JSON`);
  return value;
}

function strictJsonFile(path, label) {
  return strictJsonText(readFileSync(path, "utf8"), label);
}

function exactKeys(value, keys, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) fail(`${label} must be an object`);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) fail(`${label} has unknown or missing keys`);
}

function stringField(value, key, label) {
  if (typeof value[key] !== "string") fail(`${label}.${key} must be a string`);
  return value[key];
}

function integerField(value, key, label) {
  if (!Number.isSafeInteger(value[key])) fail(`${label}.${key} must be an integer`);
  return value[key];
}

function toolTreeDigest(root) {
  let rootStat;
  try { rootStat = lstatSync(root); } catch (error) { fail(`could not inspect tool tree ${root}: ${error.message}`); }
  if (rootStat.isSymbolicLink() || !rootStat.isDirectory()) fail(`tool tree ${root} must be a regular directory`);
  const files = [];
  const foldedPaths = new Set();
  let totalBytes = 0;
  const walk = (directory, prefix) => {
    let entries;
    try { entries = readdirSync(directory).sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b))); } catch (error) { fail(`could not read tool tree ${directory}: ${error.message}`); }
    const foldedNames = new Set();
    for (const name of entries) {
      if (name === "." || name === ".." || name.normalize("NFC") !== name) fail(`tool tree contains a noncanonical path component ${name}`);
      const foldedName = name.normalize("NFC").toLocaleLowerCase("en-US");
      if (foldedNames.has(foldedName)) fail(`tool tree contains case-confusable entries in ${directory}`);
      foldedNames.add(foldedName);
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const relativeBytes = Buffer.from(relative);
      if (relativeBytes.length > maxToolTreePathBytes) fail("tool tree path is too long");
      let stat;
      try { stat = lstatSync(path); } catch (error) { fail(`could not inspect tool tree entry ${relative}: ${error.message}`); }
      if (stat.isSymbolicLink()) fail(`tool tree contains symlink ${relative}`);
      if (stat.isDirectory()) {
        walk(path, relative);
      } else if (stat.isFile()) {
        const foldedPath = relative.normalize("NFC").toLocaleLowerCase("en-US");
        if (foldedPaths.has(foldedPath)) fail(`tool tree contains case-confusable path ${relative}`);
        foldedPaths.add(foldedPath);
        if (files.length >= maxToolTreeFiles) fail("tool tree contains too many files");
        if (!Number.isSafeInteger(stat.size) || stat.size > maxToolTreeBytes || totalBytes > maxToolTreeBytes - stat.size) fail("tool tree is too large");
        totalBytes += stat.size;
        files.push({ path, relative, relativeBytes, size: stat.size });
      } else {
        fail(`tool tree contains non-regular entry ${relative}`);
      }
    }
  };
  walk(root, "");
  files.sort((a, b) => Buffer.compare(a.relativeBytes, b.relativeBytes));
  const hash = createHash("sha512");
  hash.update(Buffer.from(`${toolTreeVersion}\0`));
  const count = Buffer.alloc(4);
  count.writeUInt32BE(files.length);
  hash.update(count);
  for (const file of files) {
    const length = Buffer.alloc(4);
    length.writeUInt32BE(file.relativeBytes.length);
    hash.update(length);
    hash.update(file.relativeBytes);
    const bytes = readFileSync(file.path);
    if (bytes.length !== file.size) fail(`tool tree file changed while hashing ${file.relative}`);
    const size = Buffer.alloc(8);
    size.writeBigUInt64BE(BigInt(file.size));
    hash.update(size);
    hash.update(bytes);
  }
  return hash.digest("hex");
}

function reviewedToolchain() {
  const value = strictJsonFile(toolchainIntegrityPath, "toolchain integrity");
  exactKeys(value, ["schemaVersion", "pnpm", "typescript"], "toolchain integrity");
  if (value.schemaVersion !== 1) fail("toolchain integrity schema version is unsupported");
  exactKeys(value.pnpm, ["version", "treeSha512"], "toolchain integrity.pnpm");
  exactKeys(value.typescript, ["version", "treeSha512", "lockfileIntegrity"], "toolchain integrity.typescript");
  for (const key of ["version", "treeSha512"]) stringField(value.pnpm, key, "toolchain integrity.pnpm");
  for (const key of ["version", "treeSha512", "lockfileIntegrity"]) stringField(value.typescript, key, "toolchain integrity.typescript");
  if (!/^\d+\.\d+\.\d+$/.test(value.pnpm.version) || !/^[0-9a-f]{128}$/.test(value.pnpm.treeSha512)) fail("toolchain integrity.pnpm is invalid");
  if (!/^\d+\.\d+\.\d+$/.test(value.typescript.version) || !/^[0-9a-f]{128}$/.test(value.typescript.treeSha512) || !/^sha512-[A-Za-z0-9+/]{86}={0,2}$/.test(value.typescript.lockfileIntegrity)) fail("toolchain integrity.typescript is invalid");
  return value;
}

function lockfileTypescriptIntegrity(version) {
  const lockfile = readFileSync(join(webRoot, "pnpm-lock.yaml"), "utf8");
  const marker = `  typescript@${version}:\n`;
  const start = lockfile.indexOf(marker);
  if (start < 0) fail(`pnpm lockfile has no exact typescript@${version} package entry`);
  const remainder = lockfile.slice(start + marker.length);
  const nextPackage = remainder.search(/\n  \S/);
  const block = lockfile.slice(start, start + marker.length + (nextPackage < 0 ? remainder.length : nextPackage));
  const matches = [...block.matchAll(/^    resolution: \{integrity: (sha512-[^}]+)\}$/gm)];
  if (matches.length !== 1) fail(`pnpm lockfile has an invalid typescript@${version} package entry`);
  return matches[0][1];
}

function pnpmRoot(version) {
  let home;
  try { home = userInfo().homedir; } catch (error) { fail(`could not determine the current user's home: ${error.message}`); }
  return join(home, ".cache", "node", "corepack", "v1", "pnpm", version);
}

function trustedTools() {
  const node = executable(process.execPath, "Node");
  const corepack = executable(join(dirname(node), "corepack"), "Corepack");
  const reviewed = reviewedToolchain();
  const webPackage = readJson(join(webRoot, "package.json"));
  const packageManager = webPackage.packageManager?.match(/^pnpm@(\d+\.\d+\.\d+)$/);
  const typescript = webPackage.devDependencies?.typescript;
  if (!packageManager || !/^\d+\.\d+\.\d+$/.test(typescript ?? "")) fail("package/tool versions must be exact");
  if (packageManager[1] !== reviewed.pnpm.version) fail("package.json pnpm version differs from reviewed toolchain");
  if (typescript !== reviewed.typescript.version) fail("package.json TypeScript version differs from reviewed toolchain");
  const pnpmRootPath = pnpmRoot(packageManager[1]);
  const pnpmPackagePath = join(pnpmRootPath, "package.json");
  const pnpmPackage = readJson(pnpmPackagePath);
  if (pnpmPackage.name !== "pnpm" || pnpmPackage.version !== packageManager[1]) fail("cached pnpm differs from web/package.json");
  const pnpmTreeSha512 = toolTreeDigest(pnpmRootPath);
  if (pnpmTreeSha512 !== reviewed.pnpm.treeSha512) fail("cached pnpm content differs from reviewed toolchain");
  if (lockfileTypescriptIntegrity(typescript) !== reviewed.typescript.lockfileIntegrity) fail("pnpm lockfile TypeScript integrity differs from reviewed toolchain");
  const tscPackagePath = realpathSync(join(webRoot, "node_modules", "typescript", "package.json"));
  const nodeModulesRoot = realpathSync(join(webRoot, "node_modules"));
  if (!tscPackagePath.startsWith(`${nodeModulesRoot}${sep}`)) fail("TypeScript resolves outside the installed web dependency tree");
  const tscRoot = dirname(tscPackagePath);
  const tsc = executable(join(tscRoot, "bin", "tsc"), "TypeScript");
  const tscPackage = readJson(tscPackagePath);
  if (tscPackage.name !== "typescript" || tscPackage.version !== typescript) fail("installed TypeScript differs from web/package.json");
  const typescriptTreeSha512 = toolTreeDigest(tscRoot);
  if (typescriptTreeSha512 !== reviewed.typescript.treeSha512) fail("installed TypeScript content differs from reviewed toolchain");
  const safeEnv = { ...commandEnvironment, PATH: `${dirname(node)}:/usr/bin:/bin` };
  const runCorepack = (args) => execFileSync(node, [corepack, ...args], { env: safeEnv, encoding: "utf8" }).trim();
  const corepackVersion = runCorepack(["--version"]);
  const pnpm = executable(join(pnpmRootPath, "bin", "pnpm.mjs"), "pnpm");
  const pnpmVersion = execFileSync(node, [pnpm, "--version"], { env: safeEnv, encoding: "utf8" }).trim();
  const tscVersion = execFileSync(node, [tsc, "--version"], { cwd: webRoot, env: safeEnv, encoding: "utf8" }).trim();
  if (pnpmVersion !== packageManager[1] || tscVersion !== `Version ${typescript}`) fail("tool executable versions do not match pinned versions");
  return {
    paths: { node, corepack, pnpm, tsc, tar: executable(tarPath, "tar") },
    versions: {
      node: process.version,
      corepack: corepackVersion,
      pnpm: pnpmVersion,
      pnpmTreeSha512,
      typescript,
      typescriptTreeSha512,
      typescriptIntegrity: reviewed.typescript.lockfileIntegrity,
    },
  };
}

function sourceIdentity(sourceCommit, tools, runtimeProtocolVersion) {
  const protocol = readJson(join(repositoryRoot, "protocol", "browser", "v1", "manifest.json"));
  if (!Number.isSafeInteger(protocol.version) || runtimeProtocolVersion !== protocol.version) {
    fail("compiled client protocol version differs from protocol/browser/v1/manifest.json");
  }
  return {
    schemaVersion: 1,
    source: { commit: sourceCommit, clean: true },
    protocol: { name: "dark-factory/browser/v1", version: protocol.version },
    buildTools: tools.versions,
  };
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function canonicalValue(value) {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (value && typeof value === "object") return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalValue(value[key])]));
  return value;
}

function writeStableJson(path, value) {
  writeFileSync(path, stableJson(value), { mode: 0o600 });
}

function packageFilename(packageValue) {
  return `${packageValue.name.replace(/^@/, "").replace("/", "-")}-${packageValue.version}.tgz`;
}

function digest(path) {
  const bytes = readFileSync(path);
  const hash = createHash("sha512").update(bytes).digest();
  return { bytes: bytes.length, sha512: hash.toString("hex"), integrity: `sha512-${hash.toString("base64")}` };
}

function provenance(packageInfo, identity, dependency) {
  const value = { ...identity, package: { name: packageInfo.name, version: packageJson(packageInfo).version } };
  if (dependency) value.dependency = dependency;
  return value;
}

function expectedPackageJson(packageInfo, dependency) {
  const value = packageJson(packageInfo);
  if (dependency) value.dependencies = { ...value.dependencies, [packages.client.name]: dependency.version };
  return value;
}

function distRoot(packageInfo) {
  return join(packageInfo.root, "dist", "src");
}

function clearBuildOutput(packageInfo) {
  const dist = join(packageInfo.root, "dist");
  if (existsSync(dist) && lstatSync(dist).isSymbolicLink()) fail(`${packageInfo.name} dist is a symlink`);
  rmSync(dist, { recursive: true, force: true });
}

function assertDirectoryInventory(root, expected, label) {
  const expectedFiles = new Set(expected);
  const seen = new Set();
  const walk = (directory, prefix) => {
    for (const entry of readdirSync(directory)) {
      const path = join(directory, entry);
      const relativePath = prefix ? `${prefix}/${entry}` : entry;
      const stat = lstatSync(path);
      if (stat.isSymbolicLink()) fail(`${label} contains symlink ${relativePath}`);
      if (stat.isDirectory()) walk(path, relativePath);
      else if (stat.isFile()) {
        if (!expectedFiles.has(relativePath) || seen.has(relativePath)) fail(`${label} contains unexpected file ${relativePath}`);
        seen.add(relativePath);
      } else fail(`${label} contains non-regular entry ${relativePath}`);
    }
  };
  if (!existsSync(root) || !lstatSync(root).isDirectory()) fail(`${label} is missing`);
  walk(root, "");
  if (seen.size !== expectedFiles.size) fail(`${label} is missing an expected file`);
}

async function compiledProtocolVersion() {
  const path = join(distRoot(packages.client), "manifest.js");
  try {
    const module = await import(pathToFileURL(path).href);
    return module.PROTOCOL_VERSION;
  } catch (error) {
    fail(`could not load compiled client manifest: ${error.message}`);
  }
}

function runTypeScript(tools, packageInfo, config) {
  execFileSync(tools.paths.node, [tools.paths.tsc, "-p", config], {
    cwd: packageInfo.root,
    env: { ...commandEnvironment, PATH: `${dirname(tools.paths.node)}:/usr/bin:/bin` },
    stdio: "inherit",
  });
}

function build(tools) {
  clearBuildOutput(packages.client);
  clearBuildOutput(packages.ui);
  runTypeScript(tools, packages.client, "../../tsconfig.build.json");
  runTypeScript(tools, packages.ui, "tsconfig.json");
  copyFileSync(join(packages.ui.root, "src", "factory-console.css"), join(distRoot(packages.ui), "factory-console.css"));
  assertDirectoryInventory(distRoot(packages.client), inventory.client.filter((name) => name !== "provenance.json"), "client build");
  assertDirectoryInventory(distRoot(packages.ui), inventory.ui.filter((name) => name !== "provenance.json"), "UI build");
}

function archiveExpected(info) {
  const files = ["package/package.json", ...inventory[info.key].map((name) => `package/dist/src/${name}`)];
  return new Set(files);
}

function tarEntries(tarPathValue, archive, info) {
  const listing = execFileSync(tarPathValue, ["-tf", archive], { encoding: "utf8", env: commandEnvironment });
  const entries = listing.split("\n").filter(Boolean).map((entry) => entry.replace(/\/$/, ""));
  if (new Set(entries).size !== entries.length) fail(`${info.name} archive contains duplicate entries`);
  for (const entry of entries) {
    if (entry.startsWith("/") || entry.includes("..") || entry.includes("\\")) fail(`${info.name} archive contains unsafe path`);
  }
  const expected = archiveExpected(info);
  if (entries.length !== expected.size || entries.some((entry) => !expected.has(entry))) fail(`${info.name} archive inventory mismatch`);
  const details = execFileSync(tarPathValue, ["-tvf", archive], { encoding: "utf8", env: commandEnvironment });
  for (const line of details.split("\n").filter(Boolean)) {
    const mode = line.trimStart()[0];
    if (mode !== "-" && mode !== "d") fail(`${info.name} archive contains a non-regular member`);
  }
}

function tarMember(tarPathValue, archive, member) {
  return execFileSync(tarPathValue, ["-xOf", archive, member], { encoding: "utf8", env: commandEnvironment });
}

function externalDependencies(packageValue, packageName) {
  for (const section of ["dependencies", "optionalDependencies", "peerDependencies", "devDependencies"]) {
    for (const [name, version] of Object.entries(packageValue[section] ?? {})) {
      if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) fail(`${packageName} has non-exact dependency ${name}@${version}`);
    }
  }
}

function outputDestination(requested, requireAbsent) {
  const lexicalOutput = resolve(requested);
  const repositoryReal = realpathSync(repositoryRoot);
  const parent = dirname(lexicalOutput);
  let parentReal;
  try { parentReal = realpathSync(parent); } catch (error) { fail(`output parent is unavailable: ${error.message}`); }
  const output = join(parentReal, basename(lexicalOutput));
  if (output === repositoryReal || output.startsWith(`${repositoryReal}${sep}`)) fail("output must be outside the source tree");
  let existing;
  try { existing = lstatSync(output); } catch (error) { if (error.code !== "ENOENT") fail(`could not inspect output: ${error.message}`); }
  if (existing) {
    if (existing.isSymbolicLink()) fail("output must not be a symlink");
    if (requireAbsent) fail("output already exists");
  }
  return output;
}

function validateManifestShape(manifest) {
  exactKeys(manifest, ["schemaVersion", "source", "protocol", "buildTools", "packages"], "manifest");
  if (manifest.schemaVersion !== 1) fail("manifest schema version is unsupported");
  exactKeys(manifest.source, ["commit", "clean"], "manifest.source");
  if (!/^[0-9a-f]{40}$/.test(stringField(manifest.source, "commit", "manifest.source")) || manifest.source.clean !== true) fail("manifest source is invalid");
  exactKeys(manifest.protocol, ["name", "version"], "manifest.protocol");
  if (stringField(manifest.protocol, "name", "manifest.protocol") !== "dark-factory/browser/v1" || integerField(manifest.protocol, "version", "manifest.protocol") !== 1) fail("manifest protocol is invalid");
  exactKeys(manifest.buildTools, ["node", "corepack", "pnpm", "pnpmTreeSha512", "typescript", "typescriptTreeSha512", "typescriptIntegrity"], "manifest.buildTools");
  for (const key of ["node", "corepack", "pnpm", "pnpmTreeSha512", "typescript", "typescriptTreeSha512", "typescriptIntegrity"]) stringField(manifest.buildTools, key, "manifest.buildTools");
  if (!/^[0-9a-f]{128}$/.test(manifest.buildTools.pnpmTreeSha512) || !/^[0-9a-f]{128}$/.test(manifest.buildTools.typescriptTreeSha512) || !/^sha512-[A-Za-z0-9+/]{86}={0,2}$/.test(manifest.buildTools.typescriptIntegrity)) fail("manifest build tool content digests are invalid");
  exactKeys(manifest.packages, [packages.client.name, packages.ui.name], "manifest.packages");
  for (const info of Object.values(packages)) {
    const entry = manifest.packages[info.name];
    exactKeys(entry, info === packages.ui ? ["schemaVersion", "source", "protocol", "buildTools", "package", "dependency", "artifact"] : ["schemaVersion", "source", "protocol", "buildTools", "package", "artifact"], `${info.name} provenance`);
    exactKeys(entry.package, ["name", "version"], `${info.name}.package`);
    exactKeys(entry.artifact, ["bytes", "sha512", "integrity", "filename", "version"], `${info.name}.artifact`);
    integerField(entry.artifact, "bytes", `${info.name}.artifact`);
    if (entry.artifact.bytes <= 0 || !/^[0-9a-f]{128}$/.test(entry.artifact.sha512) || !/^sha512-[A-Za-z0-9+/]{86}={0,2}$/.test(entry.artifact.integrity)) fail(`${info.name} artifact digest is invalid`);
    for (const key of ["sha512", "integrity", "filename", "version"]) stringField(entry.artifact, key, `${info.name}.artifact`);
    if (info === packages.ui) {
      exactKeys(entry.dependency, ["name", "version", "tarball", "integrity"], `${info.name}.dependency`);
      for (const key of ["name", "version", "tarball", "integrity"]) stringField(entry.dependency, key, `${info.name}.dependency`);
    }
  }
}

async function verify(requested, tools = trustedTools()) {
  const output = outputDestination(requested, false);
  if (!existsSync(output) || !lstatSync(output).isDirectory() || lstatSync(output).isSymbolicLink() || realpathSync(output) !== output) fail("output must be an exact non-symlink directory");
  const expectedFilenames = new Set([packageFilename(packageJson(packages.client)), packageFilename(packageJson(packages.ui)), manifestName]);
  const outputFiles = readdirSync(output);
  if (outputFiles.length !== expectedFilenames.size || outputFiles.some((name) => !expectedFilenames.has(name))) fail("output contains stale or unexpected files");
  for (const filename of outputFiles) {
    if (!lstatSync(join(output, filename)).isFile()) fail("output contains a symlink or non-regular file");
  }
  const manifest = strictJsonFile(join(output, manifestName), "artifact manifest");
  validateManifestShape(manifest);
  const sourceCommit = currentSource();
  const runtimeVersion = await compiledProtocolVersion();
  const identity = sourceIdentity(sourceCommit, tools, runtimeVersion);
  if (JSON.stringify(manifest.source) !== JSON.stringify(identity.source) || JSON.stringify(manifest.protocol) !== JSON.stringify(identity.protocol) || JSON.stringify(manifest.buildTools) !== JSON.stringify(identity.buildTools)) fail("manifest provenance is stale or forged");
  for (const info of Object.values(packages)) {
    const entry = manifest.packages[info.name];
    if (entry.schemaVersion !== identity.schemaVersion || JSON.stringify(entry.source) !== JSON.stringify(identity.source) || JSON.stringify(entry.protocol) !== JSON.stringify(identity.protocol) || JSON.stringify(entry.buildTools) !== JSON.stringify(identity.buildTools)) fail(`${info.name} provenance identity is stale or forged`);
    const sourcePackage = packageJson(info);
    const expectedFilename = packageFilename(sourcePackage);
    if (entry.package.name !== info.name || entry.package.version !== sourcePackage.version || entry.artifact.filename !== expectedFilename || entry.artifact.version !== sourcePackage.version) fail(`${info.name} package identity is stale or forged`);
    if (info === packages.ui && (entry.dependency.name !== packages.client.name || entry.dependency.version !== manifest.packages[packages.client.name].package.version || entry.dependency.tarball !== manifest.packages[packages.client.name].artifact.filename || entry.dependency.integrity !== manifest.packages[packages.client.name].artifact.integrity)) fail("UI does not bind the exact client artifact and version");
    const archive = join(output, entry.artifact.filename);
    const actual = digest(archive);
    if (actual.bytes !== entry.artifact.bytes || actual.sha512 !== entry.artifact.sha512 || actual.integrity !== entry.artifact.integrity) fail(`${info.name} tarball integrity failed`);
    tarEntries(tools.paths.tar, archive, info);
    const packedPackage = JSON.parse(tarMember(tools.paths.tar, archive, "package/package.json"));
    const expectedPackage = expectedPackageJson(info, info === packages.ui ? entry.dependency : undefined);
    if (JSON.stringify(canonicalValue(packedPackage)) !== JSON.stringify(canonicalValue(expectedPackage))) fail(`${info.name} tarball package metadata is stale or forged`);
    externalDependencies(packedPackage, info.name);
    const embeddedText = tarMember(tools.paths.tar, archive, "package/dist/src/provenance.json");
    const embedded = strictJsonText(embeddedText, `${info.name} embedded provenance`);
    if (JSON.stringify(embedded) !== JSON.stringify(provenance(info, identity, info === packages.ui ? entry.dependency : undefined))) fail(`${info.name} embedded provenance failed`);
  }
  const packedUi = JSON.parse(tarMember(tools.paths.tar, join(output, manifest.packages[packages.ui.name].artifact.filename), "package/package.json"));
  if (packedUi.dependencies?.[packages.client.name] !== manifest.packages[packages.client.name].package.version) fail("UI tarball has a floating or mismatched client dependency");
  return manifest;
}

async function pack(requested) {
  const output = outputDestination(requested, true);
  const tools = trustedTools();
  const sourceCommit = currentSource();
  const transaction = mkdtempSync(join(dirname(output), ".dark-factory-public-artifacts-"));
  const artifactDir = join(transaction, "output");
  const stageRoot = join(transaction, "packages");
  mkdirSync(artifactDir);
  mkdirSync(stageRoot);
  try {
    build(tools);
    const identity = sourceIdentity(sourceCommit, tools, await compiledProtocolVersion());
    writeStableJson(join(distRoot(packages.client), "provenance.json"), provenance(packages.client, identity));
    const clientArtifact = stageAndPack(tools, packages.client, artifactDir, stageRoot);
    const clientDependency = { name: packages.client.name, version: clientArtifact.version, tarball: clientArtifact.filename, integrity: clientArtifact.integrity };
    writeStableJson(join(distRoot(packages.ui), "provenance.json"), provenance(packages.ui, identity, clientDependency));
    const uiArtifact = stageAndPack(tools, packages.ui, artifactDir, stageRoot, clientDependency);
    const manifest = {
      ...identity,
      packages: {
        [packages.client.name]: { ...provenance(packages.client, identity), artifact: clientArtifact },
        [packages.ui.name]: { ...provenance(packages.ui, identity, clientDependency), artifact: uiArtifact },
      },
    };
    writeStableJson(join(artifactDir, manifestName), manifest);
    await verify(artifactDir, tools);
    if (existsSync(output)) fail("output appeared before atomic publish");
    renameSync(artifactDir, output);
    return manifest;
  } finally {
    rmSync(transaction, { recursive: true, force: true });
  }
}

function stageAndPack(tools, info, artifactDir, stageRoot, dependency) {
  const sourcePackage = expectedPackageJson(info, dependency);
  const stage = join(stageRoot, info.key);
  mkdirSync(join(stage, "dist", "src"), { recursive: true });
  assertDirectoryInventory(distRoot(info), inventory[info.key], `${info.name} source build`);
  for (const filename of inventory[info.key]) copyFileSync(join(distRoot(info), filename), join(stage, "dist", "src", filename));
  writeStableJson(join(stage, "package.json"), sourcePackage);
  const filename = packageFilename(sourcePackage);
  const archive = join(artifactDir, filename);
  if (existsSync(archive)) fail(`stale ${filename} already exists`);
  // npm_config_ignore_scripts is the lifecycle policy boundary for packing.
  execFileSync(tools.paths.node, [tools.paths.pnpm, "pack", "--pack-destination", artifactDir], {
    cwd: stage,
    env: { ...commandEnvironment, PATH: `${dirname(tools.paths.node)}:/usr/bin:/bin` },
    stdio: "pipe",
  });
  if (!existsSync(archive) || lstatSync(archive).isSymbolicLink()) fail(`${filename} was not produced as a regular file`);
  return { ...digest(archive), filename, version: sourcePackage.version };
}

function parseArgs(argv) {
  const [command, ...rest] = argv;
  if (rest.some((value) => value === "--source-sha" || value.startsWith("--source-sha="))) fail("source SHA is derived from clean HEAD and cannot be caller-selected");
  if (command !== "pack" && command !== "verify") fail("usage: package-artifacts.mjs <pack|verify> --output <directory>");
  if (rest.length !== 2 || rest[0] !== "--output" || !rest[1]) fail("usage: package-artifacts.mjs <pack|verify> --output <directory>");
  return { command, output: rest[1] };
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  try {
    const { command, output } = parseArgs(process.argv.slice(2));
    const manifest = command === "pack" ? await pack(output) : await verify(output);
    process.stdout.write(stableJson(manifest));
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

// Exported only for focused tests of the content commitment; pack/verify always
// derive and validate their production tool roots internally.
export { toolTreeDigest };
