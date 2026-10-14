import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const preview = fileURLToPath(new URL("../examples/floor-preview.mjs", import.meta.url));
const browser = process.env.DF_LAYOUT_BROWSER;

test("the documented floor fixtures fit controls at phone width and stay one row on desktop", { skip: browser === undefined ? "set DF_LAYOUT_BROWSER to opt into live layout measurement" : false }, () => {
  const dir = mkdtempSync(join(tmpdir(), "df-floor-layout-"));
  const floor = join(dir, "floor.html"), fixture = join(dir, "fixture.html");
  execFileSync(process.execPath, [preview, floor]);
  execFileSync(process.execPath, [preview, "fixture", fixture]);
  for (const [file, selector, controls] of [[floor, ".dfFactoryEntityTools", "summary"], [fixture, ".dfWorkBar", "summary, .dfConsoleViewToggle button, .dfWorkBar > button"]]) {
    for (const width of [390, 1440]) {
      const measurement = measure(file, width, selector, controls);
      assert.equal(measurement.root.scrollWidth, measurement.root.clientWidth, `${selector} horizontally overflows at ${width}px`);
      for (const control of measurement.controls) assert.equal(control.within, true, `${control.text} leaves ${selector} at ${width}px`);
      if (width === 1440) assert.equal(measurement.root.rows, 1, `${selector} wraps on desktop`);
    }
  }
});

function measure(file, width, selector, controls) {
  const script = `<script>requestAnimationFrame(() => { const root = document.querySelector(${JSON.stringify(selector)}); const bounds = root.getBoundingClientRect(); const elements = [...root.querySelectorAll(${JSON.stringify(controls)})]; const items = elements.map((element) => { const rect = element.getBoundingClientRect(); return { text: element.textContent.trim(), within: rect.left >= bounds.left - .1 && rect.right <= bounds.right + .1 && rect.top >= bounds.top - .1 && rect.bottom <= bounds.bottom + .1, top: rect.top }; }); const rows = new Set(items.map((item) => item.top.toFixed(2))).size; document.body.insertAdjacentHTML("beforeend", "<pre id=\"df-layout\">" + JSON.stringify({ root: { scrollWidth: root.scrollWidth, clientWidth: root.clientWidth, rows }, controls: items }) + "</pre>"); });</script>`;
  const wrapper = `${file}.wrapper.html`;
  writeFileSync(wrapper, `${readFileSync(file, "utf8")}${script}`);
  const dom = execFileSync(browser, ["--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-dev-shm-usage", `--user-data-dir=${mkdtempSync(join(tmpdir(), "df-chrome-"))}`, "--dump-dom", `--window-size=${width},844`, `file://${wrapper}`], { encoding: "utf8" });
  const match = dom.match(/<pre id="df-layout">([^<]+)/);
  assert.ok(match, `browser did not emit a ${width}px measurement`);
  return JSON.parse(match[1]);
}
