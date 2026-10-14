import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const preview = fileURLToPath(new URL("../examples/floor-preview.mjs", import.meta.url));
const browser = process.env.DF_LAYOUT_BROWSER;
const consoleCSS = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");

test("the documented floor fixtures fit controls at phone width and stay one row on desktop", { skip: browser === undefined ? "set DF_LAYOUT_BROWSER to opt into live layout measurement" : false }, () => {
  const dir = mkdtempSync(join(tmpdir(), "df-floor-layout-"));
  const fixture = join(dir, "fixture.html");
  execFileSync(process.execPath, [preview, "fixture", fixture]);
  for (const [selector, controls] of [[".dfFactoryEntityTools", "summary, button"], [".dfWorkBar", "summary, .dfConsoleViewToggle button, .dfWorkBar > button"]]) {
    for (const width of [390, 1440]) {
      const measurement = measure(fixture, width, selector, controls);
      assert.equal(measurement.root.scrollWidth, measurement.root.clientWidth, `${selector} horizontally overflows at ${width}px`);
      for (const control of measurement.controls) assert.equal(control.within, true, `${control.text} leaves ${selector} at ${width}px`);
      for (const control of measurement.controls) assert.equal(control.operable, true, `${control.text} is disabled at ${width}px`);
      if (width === 1440) assert.equal(measurement.root.rows, 1, `${selector} wraps on desktop`);
    }
  }
});

test("desktop console navigation stays above a scrollable sidebar", { skip: browser === undefined ? "set DF_LAYOUT_BROWSER to opt into live layout measurement" : false }, () => {
  const dir = mkdtempSync(join(tmpdir(), "df-console-layout-"));
  const fixture = join(dir, "fixture.html");
  writeFileSync(fixture, `<!doctype html><style>${consoleCSS}</style><main class="dfFactoryConsole"><header class="dfFactoryConsole__header"><h1>Dark Factory</h1></header><div class="dfConsoleLayout"><nav class="dfConsoleViewToggle" aria-label="Console views"><button>Floor</button><button>Agents</button><button>Work</button></nav><section class="dfConsoleLayout__left"><div style="height:600px">Floor</div></section><aside class="dfConsoleSidebar" style="height:240px"><div class="dfConsoleSidebar__panel"><p role="status">Visible status banner</p><div style="height:600px">Scrollable sidebar content</div></div></aside></div></main>`);
  const script = `<script>requestAnimationFrame(() => { const nav = document.querySelector('[aria-label="Console views"]'); const sidebar = document.querySelector('.dfConsoleSidebar'); const banner = sidebar.querySelector('[role="status"]'); const n = nav.getBoundingClientRect(); const s = sidebar.getBoundingClientRect(); const b = banner.getBoundingClientRect(); document.body.insertAdjacentHTML('beforeend', '<pre id="df-layout">' + JSON.stringify({ above: n.bottom <= s.top + .1, bannerVisible: b.top >= s.top && b.bottom <= s.bottom, scrollable: sidebar.scrollHeight > sidebar.clientHeight }) + '</pre>'); });</script>`;
  const wrapper = `${fixture}.wrapper.html`;
  writeFileSync(wrapper, `${readFileSync(fixture, "utf8")}${script}`);
  const dom = execFileSync(browser, ["--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-dev-shm-usage", `--user-data-dir=${mkdtempSync(join(tmpdir(), "df-chrome-"))}`, "--dump-dom", "--window-size=1440,844", `file://${wrapper}`], { encoding: "utf8" });
  const match = dom.match(/<pre id="df-layout">([^<]+)/);
  assert.ok(match, "browser did not emit the console layout measurement");
  assert.deepEqual(JSON.parse(match[1]), { above: true, bannerVisible: true, scrollable: true });
});

function measure(file, width, selector, controls) {
  const script = `<script>requestAnimationFrame(() => { const root = document.querySelector(${JSON.stringify(selector)}); const bounds = root.getBoundingClientRect(); const elements = [...root.querySelectorAll(${JSON.stringify(controls)})]; const items = elements.map((element) => { const rect = element.getBoundingClientRect(); return { text: element.textContent.trim(), within: rect.left >= bounds.left - .1 && rect.right <= bounds.right + .1 && rect.top >= bounds.top - .1 && rect.bottom <= bounds.bottom + .1, operable: element.disabled !== true, top: rect.top }; }); const rows = new Set(items.map((item) => item.top.toFixed(2))).size; document.body.insertAdjacentHTML("beforeend", "<pre id=\"df-layout\">" + JSON.stringify({ root: { scrollWidth: root.scrollWidth, clientWidth: root.clientWidth, rows }, controls: items }) + "</pre>"); });</script>`;
  const wrapper = `${file}.wrapper.html`;
  writeFileSync(wrapper, `${readFileSync(file, "utf8")}${script}`);
  const dom = execFileSync(browser, ["--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-dev-shm-usage", `--user-data-dir=${mkdtempSync(join(tmpdir(), "df-chrome-"))}`, "--dump-dom", `--window-size=${width},844`, `file://${wrapper}`], { encoding: "utf8" });
  const match = dom.match(/<pre id="df-layout">([^<]+)/);
  assert.ok(match, `browser did not emit a ${width}px measurement`);
  return JSON.parse(match[1]);
}
