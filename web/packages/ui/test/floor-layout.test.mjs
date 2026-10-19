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
  for (const [selector, controls] of [[".dfFactoryEntityTools", "summary, button, select"]]) {
    for (const width of [390, 1440]) {
      const measurement = measure(fixture, width, selector, controls);
      assert.equal(measurement.root.scrollWidth, measurement.root.clientWidth, `${selector} horizontally overflows at ${width}px`);
      for (const control of measurement.controls) assert.equal(control.within, true, `${control.text} leaves ${selector} at ${width}px`);
      for (const control of measurement.controls) assert.equal(control.operable, true, `${control.text} is disabled at ${width}px`);
      if (width === 1440) assert.equal(measurement.root.rows, 1, `${selector} wraps on desktop`);
    }
  }
});

test("the floor and a sidebar scrolling Work above the selected agent's terminal stay on screen together and fill wide, desktop and phone widths", { skip: browser === undefined ? "set DF_LAYOUT_BROWSER to opt into live layout measurement" : false }, () => {
  const dir = mkdtempSync(join(tmpdir(), "df-console-layout-"));
  const fixture = join(dir, "fixture.html");
  writeFileSync(fixture, `<!doctype html><style>${consoleCSS}</style><main class="dfFactoryConsole"><p class="dfFactoryConsole__error" role="alert">Error banner</p><div class="dfConsoleLayout"><section class="dfConsoleLayout__left dfFactoryConsole__section"><div style="height:600px">Floor</div></section><aside class="dfConsoleSidebar"><section class="dfConsoleSidebar__panel" aria-label="Work"><p role="status">Visible status banner</p><div style="height:600px">Work rows</div></section><section class="dfConsoleSidebar__panel" aria-label="Agent"><section class="dfConsoleSidebar__section dfConsoleSidebar__terminalSlot" aria-label="Terminal"><div class="dfFactoryConsole__terminal">terminal</div></section></section></aside></div></main>`);
  const script = `<script>requestAnimationFrame(() => { const main = document.querySelector('.dfFactoryConsole'); const floor = document.querySelector('.dfConsoleLayout__left'); const sidebar = document.querySelector('.dfConsoleSidebar'); const pad = parseFloat(getComputedStyle(main).paddingLeft) + parseFloat(getComputedStyle(main).paddingRight); const layout = document.querySelector('.dfConsoleLayout'); const gap = parseFloat(getComputedStyle(layout).columnGap); const l = layout.getBoundingClientRect(); const e = document.querySelector('.dfFactoryConsole__error').getBoundingClientRect(); const f = floor.getBoundingClientRect(); const s = sidebar.getBoundingClientRect(); const b = sidebar.querySelector('[role="status"]').getBoundingClientRect(); const w = sidebar.querySelector('[aria-label="Work"]').getBoundingClientRect(); const t = sidebar.querySelector('[aria-label="Terminal"]').getBoundingClientRect(); const onScreen = (r) => r.width > 0 && r.height > 0 && r.top < innerHeight && r.left < innerWidth; document.body.insertAdjacentHTML('beforeend', '<pre id="df-layout">' + JSON.stringify({ fillsWidth: Math.abs(l.width + pad - main.clientWidth) < 1, errorFillsWidth: Math.abs(e.width + pad - main.clientWidth) < 1, floorFillsColumn: innerWidth > 1024 ? Math.abs(f.right + gap - s.left) < 1 : Math.abs(f.width - l.width) < 1, floorOnScreen: onScreen(f), sidebarOnScreen: onScreen(s), beside: innerWidth > 1024 ? f.right <= s.left + .1 : f.bottom <= s.top + .1, bannerVisible: b.top >= s.top && b.bottom <= s.bottom, workAboveTerminal: w.bottom <= t.top + .1, scrollable: sidebar.scrollHeight > sidebar.clientHeight }) + '</pre>'); });</script>`;
  const wrapper = `${fixture}.wrapper.html`;
  writeFileSync(wrapper, `${readFileSync(fixture, "utf8")}${script}`);
  for (const width of [2560, 1440, 390]) {
    const dom = execFileSync(browser, ["--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-dev-shm-usage", `--user-data-dir=${mkdtempSync(join(tmpdir(), "df-chrome-"))}`, "--dump-dom", `--window-size=${width},844`, `file://${wrapper}`], { encoding: "utf8" });
    const match = dom.match(/<pre id="df-layout">([^<]+)/);
    assert.ok(match, `browser did not emit the console layout measurement at ${width}px`);
    assert.deepEqual(JSON.parse(match[1]), { fillsWidth: true, errorFillsWidth: true, floorFillsColumn: true, floorOnScreen: true, sidebarOnScreen: true, beside: true, bannerVisible: true, workAboveTerminal: true, scrollable: true }, `${width}px`);
  }
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
