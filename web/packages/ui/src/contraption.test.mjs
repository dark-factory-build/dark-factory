import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Contraption } from "../dist/src/contraption.js";

test("a contraption's durable identity fixes its silhouette while operation adds only state", () => {
  const render = (props) => renderToStaticMarkup(createElement(Contraption, { identity: "change-identity", construction: false, correction: false, active: false, reducedMotion: true, ...props }));
  const still = render();
  const running = render({ active: true, reducedMotion: false, pulse: 0 });
  const advanced = render({ active: true, reducedMotion: false, pulse: 360 });
  const repaired = render({ construction: true, correction: true });
  const shape = (markup) => markup.match(/data-contraption-(?:variant|fitting)="[^"]+"/g).join(" ");
  assert.equal(shape(still), shape(running));
  assert.equal(shape(still), shape(repaired));
  assert.match(running, /data-contraption-active="true"/);
  assert.notEqual(running, advanced, "the shared pulse advances machine motion");
  assert.match(repaired, /data-contraption-build="partly-assembled"[\s\S]*data-contraption-tool="spanner"/);
});
