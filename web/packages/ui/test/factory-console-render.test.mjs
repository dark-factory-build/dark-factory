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
