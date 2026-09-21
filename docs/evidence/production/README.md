# Production box visual evidence

These four PNGs are durable browser captures of the actual `ProductionArea` component at desktop (1280px) and phone (390px) widths. The before captures were rendered from base commit `8121a49cac066eebf55e25ebed3bf240295836c3`; the after captures were rendered from fixed commit `68e97cda9c4c0f2c85667e872a00468e32e4c996`.

- `production-before-desktop.png`: base component, 1280px viewport.
- `production-after-desktop.png`: fixed component, 1280px viewport.
- `production-before-phone.png`: base component, 390px viewport.
- `production-after-phone.png`: fixed component, 390px viewport.

Capture procedure: build the UI package at each source revision, server-render `ProductionArea` with representative production projection facts using React DOM server, navigate the resulting HTML data URL in the Playwright browser, set the viewport to the named width, and take a full-page PNG screenshot. The base render was isolated in a temporary checkout made with `git archive`; no application markup was hand-authored for the captures.
