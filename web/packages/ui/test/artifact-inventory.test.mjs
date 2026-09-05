import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { assertDirectoryInventory, inventory } from "../../../scripts/package-artifacts.mjs";

// The published artifact inventory is a deliberate allowlist (see
// package-artifacts.mjs). This checks it against the packages' already-built
// dist/src output, so a merge that adds a source export without updating the
// inventory fails here instead of at pack time.
const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../../");

test("client dist/src matches the published inventory", () => {
  assertDirectoryInventory(
    join(webRoot, "packages", "client", "dist", "src"),
    inventory.client.filter((name) => name !== "provenance.json"),
    "client build",
  );
});

test("UI dist/src matches the published inventory", () => {
  assertDirectoryInventory(
    join(webRoot, "packages", "ui", "dist", "src"),
    inventory.ui.filter((name) => name !== "provenance.json"),
    "UI build",
  );
});
