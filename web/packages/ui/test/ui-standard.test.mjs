import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Badge, formatTime, Status } from "../dist/src/console-kit.js";
import { IconButton } from "../dist/src/icons.js";

test("every console font size is a scale token and every colour token is used once", () => {
  const css = readFileSync(new URL("../src/factory-console.css", import.meta.url), "utf8");
  const sizes = [...css.matchAll(/(?<![-\w])font-size:\s*([^;}]+)/g)].map((match) => match[1].trim());
  assert.deepEqual(sizes.filter((size) => !size.startsWith("var(--df-text-")), []);
  assert.equal(/#64d8b1|#dca85b/i.test(css), false);
});

test("badges hide at zero, icon buttons name themselves, and dates share one format", () => {
  assert.equal(renderToStaticMarkup(createElement(Badge, { n: 0 })), "");
  assert.match(renderToStaticMarkup(createElement(Badge, { n: 3 })), />3</);
  assert.match(renderToStaticMarkup(createElement(IconButton, { icon: "gear", "aria-label": "Settings" })), /title="Settings" aria-label="Settings"/);
  assert.match(renderToStaticMarkup(createElement(Status, { stage: "needs-you" })), /data-stage="needs-you">needs you</);
  assert.equal(formatTime(1759779480000n), "6 Oct, 19:38 UTC");
  assert.equal(formatTime(undefined), "");
});
