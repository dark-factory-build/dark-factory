import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import React, { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryConsole } from "../dist/src/factory-console.js";

test("console header and default floor have no browser settings surface", () => {
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "closed", state: undefined }));
  assert.doesNotMatch(markup, /Settings/);
  assert.match(markup, /dfFactoryFloor/);
  assert.match(markup, /dfPlant/);
});

test("factory floor disables animation under reduced motion", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.match(css, /@media \(prefers-reduced-motion: reduce\) \{ \.dfPlant \* \{ animation: none !important; \} \}/);
});

test("the console grid has no navigation row and never hides the floor or the sidebar", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  assert.doesNotMatch(css, /dfConsoleViewToggle|Console views|data-mobile-view/);
  assert.match(css, /\.dfConsoleLayout \{\s*display: grid;\s*grid-template-columns: minmax\(0, 2fr\) minmax\(0, 1fr\);/, "desktop: floor beside the sidebar");
  assert.match(css, /@media \(max-width: 1024px\) \{\s*\.dfConsoleLayout \{ grid-template-columns: minmax\(0, 1fr\); grid-template-rows: minmax\(0, 1fr\) minmax\(0, 1fr\); \}/, "narrow: floor above the sidebar, both on screen");
  assert.match(css, /\.dfConsoleSidebar,\s*\.dfConsoleSidebar__panel:has\(> \.dfConsoleSidebar__terminalSlot\),\s*\.dfConsoleSidebar__terminalSlot \{ display: flex; flex: 1 1 auto; flex-direction: column; \}/, "the terminal fills the height Work leaves");
  assert.match(css, /\.dfConsoleSidebar__panel \+ \.dfConsoleSidebar__panel \{ margin-top: 1rem; \}/);
});
