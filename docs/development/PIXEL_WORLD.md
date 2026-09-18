# Factory floor pixel world

The existing SVG scene, 16 × 16 generated atlas, room layout, routes, and worker
frames remain the drawing and motion system. The old room outlines, equipment,
task markers, and common-area fixtures used smooth SVG shapes; these are the
physical pieces being replaced with atlas frames. Room details, settings,
queue controls, and other text-heavy panels remain ordinary application UI.

Use the atlas's 16-colour workshop palette and integer pixel edges. Objects
have a dark one-pixel outline, highlight on the upper-left edge, and shadow on
the lower-right edge. The camera is a flat, slightly elevated room plan: back
walls stand above floor tiles; benches and cabinets sit on the floor; workers
stand at the same scale as equipment. Repeat 16-pixel tiles for long runs and
compose larger objects from distinct frames and repeated modules. Render the
scene at whole-number scale where space permits and keep `image-rendering:
pixelated` on the sheet. A readable label or focused control can remain SVG
text or an application overlay; physical status lamps and task objects use
pixel frames.

Inventory, dependencies, room names, worker observations, and task indicators
must still come from served or live state. Static atmosphere can be
deterministic, but it must not imply work or communication that did not occur.
