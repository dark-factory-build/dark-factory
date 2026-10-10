import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FactoryConsole } from "../dist/src/index.js";
import { fixtureState } from "../../../fixtures/state.mjs";

const render = (props) => renderToStaticMarkup(createElement(FactoryConsole, { status: "closed", detail: "work", ...props }));

test("a never-connected console claims no remembered state", () => {
  const never = render({ state: undefined });
  for (const claim of ["last known", "Last known", "last observed", "Last observed", "Resume new work"]) assert.equal(never.includes(claim), false, claim);
  assert.match(never, /Not connected yet/);
  assert.match(never, />New work</);
  assert.match(render({ state: fixtureState }), /Last observed state/);
});
