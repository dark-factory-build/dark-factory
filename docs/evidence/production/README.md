# Production box visual evidence

These four PNGs are durable browser captures of the actual `ProductionArea` component at desktop (1280px) and phone (390px) widths. The before captures were rendered from base commit `8121a49cac066eebf55e25ebed3bf240295836c3`; the after captures were regenerated from the exact published PR #988 head `15b9f7ebf93224e80b0b08b5f09bb168635e1b02` fetched from `refs/pull/988/head`.

- `production-before-desktop.png`: base component, 1280px viewport.
- `production-after-desktop.png`: fixed component, 1280px viewport.
- `production-before-phone.png`: base component, 390px viewport.
- `production-after-phone.png`: fixed component, 390px viewport.

Capture procedure: build the UI package at each source revision, server-render `ProductionArea` with representative production projection facts using React DOM server, set the viewport to 1280×720 or 390×900 in the Playwright browser, and take a full-page PNG screenshot. The after source was verified at commit `15b9f7ebf93224e80b0b08b5f09bb168635e1b02` with tree `b43b4689b9999308e294b8c7e552d14d17356e1d`; no application markup was hand-authored for the captures.

Exact after-source blob hashes:

- `web/packages/ui/src/production-area.tsx`: `1f62b7f398ba07df89bf3062563ec3c72d2c1f79`
- `web/packages/ui/src/production-view.ts`: `4c104f49afef34a284ed07c817cab61cfbbfe81d`
- `web/packages/ui/src/factory-scene/factory-scene.tsx`: `5702dbd1e7f7966d1384812e7975a016ec42a475`
- `web/packages/ui/src/factory-console.css`: `fd03c51c31ab992e9a44c9e16b73accbacd2d7b7`

Capture SHA-256 hashes:

- `production-before-desktop.png`: `7662848be6c8b66c47f36de7c6c83ffa31936e527d73c0c092c8ec91c65903fb`
- `production-before-phone.png`: `2a5d6ae26aec03c550ba9f0e9b9dc33663a7269236447b32324ae30cd0958395`
- `production-after-desktop.png`: `5d418600abadba3b253051747a8773c28d1c5314843ce5b40d8cd50af311fbb3`
- `production-after-phone.png`: `2ba5484fb32a00b41df8d4824ab66eb1faa180303d0ec7826cfc621e36789c0d`
