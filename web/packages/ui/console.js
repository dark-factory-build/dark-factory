// The console bundle factoryd embeds and serves signed by its node key
// (internal/browser/console.go). A page shell verifies it, imports it from a
// blob: URL and calls mount. React, the client and the UI all come from this
// one build, so a browser always runs the console its daemon was built with.
import { createElement } from "react";
import { createRoot } from "react-dom/client";
import { FactoryApp, RemoteApp } from "./src/index.js";

export * from "./src/index.js";
export { createElement, createRoot };

const views = { factory: FactoryApp, remote: RemoteApp };

/** Renders one view into root with the console's own stylesheet. */
export function mount(root, view, props = {}) {
  const sheet = new CSSStyleSheet();
  sheet.replaceSync(CONSOLE_STYLES); // defined at build time by web/scripts/console.mjs
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  createRoot(root).render(createElement(views[view], props));
}
