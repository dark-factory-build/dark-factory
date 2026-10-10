import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

test("the console bundle factoryd embeds is one self-contained module", async () => {
  const web = fileURLToPath(new URL("../../../", import.meta.url));
  execFileSync(process.execPath, [join(web, "scripts", "console.mjs")], { stdio: "pipe" });
  // Imported from a directory with no node_modules, so any bare import left in it fails.
  const directory = mkdtempSync(join(tmpdir(), "dark-factory-console-"));
  try {
    copyFileSync(join(web, "..", "internal", "browser", "console", "console.js"), join(directory, "console.mjs"));
    const bundle = await import(join(directory, "console.mjs"));
    for (const name of ["mount", "FactoryApp", "RemoteApp", "FactoryScene", "publicFloor", "createElement", "createRoot"]) {
      assert.equal(typeof bundle[name], "function", name);
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
