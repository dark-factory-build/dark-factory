import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

test("packed client is importable by a clean consumer through package exports", () => {
  const packageRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
  const consumer = mkdtempSync(join(tmpdir(), "dark-factory-client-consumer-"));
  try {
    const env = { ...process.env, npm_config_cache: join(consumer, "npm-cache"), npm_config_update_notifier: "false" };
    const tarball = execFileSync("npm", ["pack", "--ignore-scripts", "--pack-destination", consumer], { cwd: packageRoot, encoding: "utf8", env }).trim().split("\n").at(-1);
    assert.ok(tarball);
    writeFileSync(join(consumer, "package.json"), JSON.stringify({ name: "clean-consumer", private: true, type: "module" }));
    execFileSync("npm", ["install", "--offline", "--ignore-scripts", "--no-package-lock", join(consumer, tarball)], { cwd: consumer, stdio: "pipe", env });
    const probe = "import { decodeServerControl, StateAccumulator, ProtocolError } from '@dark-factory/client'; const f=decodeServerControl('{\\\"v\\\":1,\\\"type\\\":\\\"STATE_RESTART\\\",\\\"id\\\":\\\"watch\\\",\\\"body\\\":{\\\"head\\\":\\\"9007199254740993\\\",\\\"floor\\\":\\\"1\\\",\\\"reason\\\":\\\"gap\\\"}}'); if (f.type !== 'STATE_RESTART' || f.body.head !== 9007199254740993n || !(new StateAccumulator())) throw new Error('bad export'); try { decodeServerControl(null); throw new Error('accepted malformed'); } catch (e) { if (!(e instanceof ProtocolError) || e.code !== 'malformed') throw e; }";
    execFileSync(process.execPath, ["--input-type=module", "-e", probe], { cwd: consumer, stdio: "pipe" });
  } finally {
    rmSync(consumer, { recursive: true, force: true });
  }
});
