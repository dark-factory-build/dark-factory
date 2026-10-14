import test from "node:test";
import assert from "node:assert/strict";
import React, { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryConsole } from "../dist/src/factory-console.js";

test("console header and default floor have no browser settings surface", () => {
  const markup = renderToStaticMarkup(createElement(FactoryConsole, { status: "closed", state: undefined }));
  assert.doesNotMatch(markup, /Settings/);
  assert.match(markup, /dfFactoryFloor/);
  assert.match(markup, /dfPlant/);
});
