import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import test from "node:test";

test("packed UI is importable by a clean consumer with its stylesheet export", () => {
  const packageRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
  const clientRoot = join(packageRoot, "..", "client");
  const webRoot = join(packageRoot, "..", "..");
  const consumer = mkdtempSync(join(tmpdir(), "dark-factory-ui-consumer-"));
  try {
    const env = { ...process.env, npm_config_cache: join(consumer, "npm-cache"), npm_config_update_notifier: "false" };
    execFileSync("corepack", ["pnpm", "pack", "--pack-destination", consumer], { cwd: clientRoot, stdio: "pipe", env });
    execFileSync("corepack", ["pnpm", "pack", "--pack-destination", consumer], { cwd: packageRoot, stdio: "pipe", env });
    const clientTarball = join(consumer, "dark-factory-client-0.1.0.tgz");
    const uiTarball = join(consumer, "dark-factory-ui-0.1.0.tgz");
    writeFileSync(join(consumer, "package.json"), JSON.stringify({ name: "clean-ui-consumer", private: true, type: "module" }));
    execFileSync("npm", ["install", "--offline", "--ignore-scripts", "--legacy-peer-deps", "--no-package-lock", clientTarball, uiTarball], { cwd: consumer, stdio: "pipe", env });

    const packageSources = new Map([
      ["react", join(packageRoot, "node_modules", "react")],
      ["react-dom", join(packageRoot, "node_modules", "react-dom")],
      ["scheduler", join(webRoot, "node_modules", ".pnpm", "node_modules", "scheduler")],
    ]);
    for (const [name, packagePath] of packageSources) {
      const source = realpathSync(packagePath);
      const target = join(consumer, "node_modules", name);
      if (!existsSync(target)) cpSync(source, target, { recursive: true });
    }
    const probe = join(consumer, "probe.mjs");
    writeFileSync(probe, "import { FactoryConsole } from '@dark-factory/ui'; const css = await import.meta.resolve('@dark-factory/ui/styles.css'); if (typeof FactoryConsole !== 'function' || !css.endsWith('/factory-console.css')) throw new Error('bad UI package exports');");
    execFileSync(process.execPath, [probe], { cwd: consumer, stdio: "pipe" });
    const installedManifest = JSON.parse(readFileSync(join(consumer, "node_modules", "@dark-factory", "ui", "package.json"), "utf8"));
    assert.deepEqual(installedManifest.peerDependencies, { react: "19.1.0" });
    assert.equal(existsSync(join(consumer, "node_modules", "@dark-factory", "ui", "dist", "src", "index.d.ts")), true);
    assert.match(readFileSync(join(consumer, "node_modules", "@dark-factory", "ui", "dist", "src", "factory-console.css"), "utf8"), /\.dfFactoryConsole\b/);
  } finally {
    rmSync(consumer, { recursive: true, force: true });
  }
});
