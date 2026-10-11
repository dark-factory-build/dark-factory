// Builds the one-file console bundle factoryd embeds: the UI and client
// sources, React and the stylesheet, minified into internal/browser/console.
import { build } from "esbuild";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const styles = await build({ entryPoints: [resolve(web, "packages/ui/src/factory-console.css")], bundle: true, minify: true, write: false });
await build({
  entryPoints: [resolve(web, "packages/ui/console.js")],
  outfile: resolve(web, "../internal/browser/console/console.js"),
  alias: { "@dark-factory/client": resolve(web, "packages/client/src/index.ts") },
  define: { CONSOLE_STYLES: JSON.stringify(styles.outputFiles[0].text), "process.env.NODE_ENV": '"production"' },
  bundle: true,
  format: "esm",
  minify: true,
});
