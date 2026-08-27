import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(webRoot, "..");
const packages = {
  client: {
    name: "@dark-factory/client",
    root: join(webRoot, "packages", "client"),
  },
  ui: {
    name: "@dark-factory/ui",
    root: join(webRoot, "packages", "ui"),
  },
};
const manifestName = "dark-factory-public-artifacts.json";

const commandEnvironment = {
  ...process.env,
  COREPACK_DEFAULT_TO_LATEST: "0",
  COREPACK_ENABLE_NETWORK: "0",
  npm_config_ignore_scripts: "true",
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

function git(...args) {
  return execFileSync("git", args, {
    cwd: repositoryRoot,
    env: { ...commandEnvironment, GIT_OPTIONAL_LOCKS: "0" },
    encoding: "utf8",
  }).trim();
}

function currentSource() {
  const commit = git("rev-parse", "--verify", "HEAD^{commit}");
  const status = git("status", "--porcelain=v1", "--untracked-files=all");
  if (status) fail("source worktree is dirty; commit or remove all changes first");
  return commit;
}

function packageJson(packageInfo) {
  return readJson(join(packageInfo.root, "package.json"));
}

function sourceIdentity(sourceCommit) {
  const webPackage = readJson(join(webRoot, "package.json"));
  const protocolManifest = readJson(join(repositoryRoot, "protocol", "browser", "v1", "manifest.json"));
  const clientManifestSource = readFileSync(join(packages.client.root, "src", "manifest.ts"), "utf8");
  const clientProtocolMatch = clientManifestSource.match(/PROTOCOL_VERSION\s*=\s*(\d+)/);
  if (!clientProtocolMatch) fail("client protocol version source is missing");
  const protocolVersion = protocolManifest.version;
  if (clientProtocolMatch[1] !== String(protocolVersion)) {
    fail("client protocol version differs from protocol/browser/v1/manifest.json");
  }
  const packageManager = webPackage.packageManager;
  const packageManagerMatch = packageManager?.match(/^pnpm@(\d+\.\d+\.\d+)$/);
  if (!packageManagerMatch) fail("web packageManager must pin a pnpm version");
  const typescript = webPackage.devDependencies?.typescript;
  if (!/^\d+\.\d+\.\d+$/.test(typescript ?? "")) fail("web TypeScript version must be exact");
  return {
    schemaVersion: 1,
    source: { commit: sourceCommit, clean: true },
    protocol: { name: "dark-factory/browser/v1", version: protocolVersion },
    buildTools: {
      node: process.versions.node,
      pnpm: packageManagerMatch[1],
      typescript,
    },
  };
}

function packageFilename(packageJsonValue) {
  return `${packageJsonValue.name.replace(/^@/, "").replace("/", "-")}-${packageJsonValue.version}.tgz`;
}

function digest(path) {
  const bytes = readFileSync(path);
  const hash = createHash("sha512").update(bytes).digest();
  return {
    bytes: bytes.length,
    sha512: hash.toString("hex"),
    integrity: `sha512-${hash.toString("base64")}`,
  };
}

function writeStableJson(path, value) {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
}

function runBuilds(pnpmVersion) {
  const pnpm = `pnpm@${pnpmVersion}`;
  execFileSync("corepack", [pnpm, "--filter", packages.client.name, "build"], {
    cwd: webRoot,
    env: commandEnvironment,
    stdio: "inherit",
  });
  execFileSync("corepack", [pnpm, "--filter", packages.ui.name, "build"], {
    cwd: webRoot,
    env: commandEnvironment,
    stdio: "inherit",
  });
}

function writeEmbeddedProvenance(packageInfo, identity, dependency) {
  // The archive digest is deliberately detached: putting a digest of an
  // archive inside that same archive would make the record self-referential.
  // The embedded record binds source/protocol/package identity; the adjacent
  // aggregate manifest binds the resulting bytes to SHA-512/SRI.
  const value = {
    ...identity,
    package: {
      name: packageInfo.name,
      version: packageJson(packageInfo).version,
    },
  };
  if (dependency) value.dependency = dependency;
  writeStableJson(join(packageInfo.root, "dist", "src", "provenance.json"), value);
}

function stageAndPack(packageInfo, output, staging, dependency, pnpmVersion) {
  const sourcePackage = packageJson(packageInfo);
  const stage = join(staging, packageInfo.name.replace("@dark-factory/", ""));
  mkdirSync(stage, { recursive: true });
  cpSync(join(packageInfo.root, "dist", "src"), join(stage, "dist", "src"), { recursive: true });
  const stagedPackage = { ...sourcePackage };
  if (packageInfo === packages.ui) {
    stagedPackage.dependencies = {
      ...stagedPackage.dependencies,
      [packages.client.name]: dependency.version,
    };
  }
  writeStableJson(join(stage, "package.json"), stagedPackage);
  execFileSync("corepack", [`pnpm@${pnpmVersion}`, "pack", "--ignore-scripts", "--pack-destination", output], {
    cwd: stage,
    env: commandEnvironment,
    stdio: "pipe",
  });
  const filename = packageFilename(stagedPackage);
  const tarball = join(output, filename);
  if (!existsSync(tarball)) fail(`pnpm pack did not produce ${filename}`);
  return { ...digest(tarball), filename, version: stagedPackage.version };
}

function packageProvenance(packageInfo, identity, dependency) {
  const value = {
    ...identity,
    package: {
      name: packageInfo.name,
      version: packageJson(packageInfo).version,
    },
  };
  if (dependency) value.dependency = dependency;
  return value;
}

function pack(output) {
  const sourceCommit = currentSource();
  const identity = sourceIdentity(sourceCommit);
  mkdirSync(output, { recursive: true });
  const staging = mkdtempSync(join(output, ".staging-"));
  try {
    runBuilds(identity.buildTools.pnpm);
    writeEmbeddedProvenance(packages.client, identity);
    const clientArtifact = stageAndPack(packages.client, output, staging, undefined, identity.buildTools.pnpm);
    const clientDependency = {
      name: packages.client.name,
      version: clientArtifact.version,
      tarball: clientArtifact.filename,
      integrity: clientArtifact.integrity,
    };
    const uiDependency = {
      ...clientDependency,
    };
    writeEmbeddedProvenance(packages.ui, identity, uiDependency);
    const uiArtifact = stageAndPack(packages.ui, output, staging, clientDependency, identity.buildTools.pnpm);
    const manifest = {
      ...identity,
      packages: {
        [packages.client.name]: {
          ...packageProvenance(packages.client, identity),
          artifact: clientArtifact,
        },
        [packages.ui.name]: {
          ...packageProvenance(packages.ui, identity, uiDependency),
          artifact: uiArtifact,
        },
      },
    };
    writeStableJson(join(output, manifestName), manifest);
    verify(output);
    return manifest;
  } finally {
    rmSync(staging, { recursive: true, force: true });
  }
}

function packageJsonFromTarball(tarball) {
  const text = execFileSync("tar", ["-xOf", tarball, "package/package.json"], {
    encoding: "utf8",
    env: commandEnvironment,
  });
  return JSON.parse(text);
}

function embeddedProvenanceFromTarball(tarball) {
  const text = execFileSync("tar", ["-xOf", tarball, "package/dist/src/provenance.json"], {
    encoding: "utf8",
    env: commandEnvironment,
  });
  return JSON.parse(text);
}

function assertExternalDependencies(packageManifest, packageName) {
  for (const section of ["dependencies", "optionalDependencies", "peerDependencies", "devDependencies"]) {
    for (const [name, version] of Object.entries(packageManifest[section] ?? {})) {
      if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
        fail(`${packageName} has non-exact external dependency ${name}@${version}`);
      }
    }
  }
}

function verify(output) {
  const manifestPath = join(output, manifestName);
  const manifest = readJson(manifestPath);
  const sourceCommit = currentSource();
  const identity = sourceIdentity(sourceCommit);
  if (manifest.schemaVersion !== identity.schemaVersion) fail("unsupported provenance schema");
  if (JSON.stringify(manifest.source) !== JSON.stringify(identity.source)) fail("source provenance is stale or forged");
  if (JSON.stringify(manifest.protocol) !== JSON.stringify(identity.protocol)) fail("protocol provenance is stale or forged");
  if (JSON.stringify(manifest.buildTools) !== JSON.stringify(identity.buildTools)) fail("build-tool provenance is stale or forged");

  const clientEntry = manifest.packages?.[packages.client.name];
  const uiEntry = manifest.packages?.[packages.ui.name];
  if (!clientEntry || !uiEntry) fail("both public package entries are required");
  for (const packageInfo of Object.values(packages)) {
    const entry = manifest.packages[packageInfo.name];
    const packageSource = packageJson(packageInfo);
    if (entry.package?.name !== packageInfo.name || entry.package?.version !== packageSource.version) {
      fail(`${packageInfo.name} package identity is stale or forged`);
    }
    const tarball = join(output, entry.artifact?.filename ?? "");
    if (!existsSync(tarball)) fail(`${packageInfo.name} tarball is missing`);
    const actual = digest(tarball);
    if (JSON.stringify(actual) !== JSON.stringify(entry.artifact)) fail(`${packageInfo.name} tarball integrity failed`);
    const packedPackage = packageJsonFromTarball(tarball);
    if (packedPackage.name !== packageInfo.name || packedPackage.version !== packageSource.version) {
      fail(`${packageInfo.name} tarball package identity failed`);
    }
    assertExternalDependencies(packedPackage, packageInfo.name);
    if (packedPackage.exports?.["./provenance"] !== "./dist/src/provenance.json") {
      fail(`${packageInfo.name} provenance export is missing`);
    }
    const embedded = embeddedProvenanceFromTarball(tarball);
    const expected = packageProvenance(packageInfo, identity, packageInfo === packages.ui ? entry.dependency : undefined);
    if (JSON.stringify(embedded) !== JSON.stringify(expected)) fail(`${packageInfo.name} embedded provenance failed`);
  }
  const clientArtifact = clientEntry.artifact;
  if (uiEntry.dependency?.name !== packages.client.name || uiEntry.dependency.version !== clientEntry.package.version || uiEntry.dependency.tarball !== clientArtifact.filename || uiEntry.dependency.integrity !== clientArtifact.integrity) {
    fail("UI does not bind the exact client artifact and version");
  }
  const packedUi = packageJsonFromTarball(join(output, uiEntry.artifact.filename));
  if (packedUi.dependencies?.[packages.client.name] !== clientEntry.package.version) {
    fail("UI tarball has a floating or mismatched client dependency");
  }
  return manifest;
}

function parseArgs(argv) {
  const [command, ...rest] = argv;
  if (rest.includes("--source-sha") || rest.some((value) => value.startsWith("--source-sha="))) {
    fail("source SHA is derived from the clean HEAD and cannot be caller-selected");
  }
  if (command !== "pack" && command !== "verify") fail("usage: package-artifacts.mjs <pack|verify> --output <directory>");
  if (rest.length !== 2 || rest[0] !== "--output" || !rest[1]) fail("usage: package-artifacts.mjs <pack|verify> --output <directory>");
  return { command, output: resolve(rest[1]) };
}

try {
  const { command, output } = parseArgs(process.argv.slice(2));
  const manifest = command === "pack" ? pack(output) : verify(output);
  process.stdout.write(`${JSON.stringify(manifest, null, 2)}\n`);
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}
