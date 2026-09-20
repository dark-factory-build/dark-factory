import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { deflateSync, inflateSync } from 'node:zlib';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

// One 16-colour palette, including transparency. "u" in grids is a uniform slot.
const palette = {
  '.': '#00000000', o: '#0b0b0b', a: '#edbc91', b: '#b77c58',
  h: '#493329', c: '#e0743a', t: '#3ec7a0', s: '#8b95a5',
  y: '#f2c94c', r: '#ff5f56', k: '#5ad38a', d: '#191d24',
  m: '#303741', l: '#59616e', w: '#85583b', p: '#e5d5ad',
};
const rgba = Object.fromEntries(Object.entries(palette).map(([key, hex]) =>
  [key, Buffer.from(hex.slice(1) + (hex.length === 7 ? 'ff' : ''), 'hex')]));
const grid = text => {
  const rows = text.trim().split('\n').map(row => row.trim());
  assert(rows.every(row => row.length === rows[0].length), 'Ragged pixel grid');
  return rows;
};
const tint = (rows, colour) => rows.map(row => row.replaceAll('u', colour));
const mirror = rows => rows.map(row => [...row].reverse().join(''));
const blank = () => Array.from({ length: 16 }, () => Array(16).fill('.'));
function draw(target, rows, x = 0, y = 0) {
  rows.forEach((row, dy) => [...row].forEach((pixel, dx) => {
    assert(x + dx >= 0 && x + dx < 16 && y + dy >= 0 && y + dy < 16, 'Grid overflow');
    if (pixel !== '.') {
      assert(pixel in palette, `Unknown palette key ${pixel}`);
      target[y + dy][x + dx] = pixel;
    }
  }));
}
const keep = (rows, keys) => rows.map(row => [...row].map(key => keys.includes(key) ? key : '.').join(''));

// Art lives here: shared face, chosen hair/clothes/shoes, then pose/equipment.
// a/b are skin/light-shadow; u is the sleeve colour. All parts face forward.
const head = grid(`
  .oooo.
  oaaaao
  oaooao
  oaaaao
  .obbo.
  ..ba..
`);
const hair = [
  grid(`
    ..oooo..
    .ouuuuo.
    .ouuuuo.
    ..u.....
  `),
  grid(`
    .oooooo.
    .ouumuo.
    .ouu.uo.
    ..u...u.
  `),
  grid(`
    ..oo.oo.
    .ouuouuo
    ouuuuuuo
    .u....u.
  `),
  grid(`
    ..oooo..
    .ouuuuo.
    .ouuuuo.
    ..u...uo
    ......uo
    ......uo
  `),
];
const outfits = [
  grid(`
    ........
    ..o..o..
    .ouppuo.
    ouoppouo
    ouuppuuo
    ouuppuuo
    ouo..ouo
  `),
  grid(`
    ........
    ........
    .oummuo.
    opuppupo
    opuuuupo
    ouuuuuuo
    .ouuuuo.
  `),
  grid(`
    ........
    ........
    ..ouuo..
    .ouoouo.
    .ouuuuo.
    .ouuuuo.
    ..oooo..
  `),
  grid(`
    .ou..uo.
    ouu..uuo
    ouummuuo
    .ouuuuo.
    oupuupuo
    ouummuuo
    .oooooo.
  `),
];
// Stable slots: revise a part in place rather than reorder identities.
const hairStyles = ['Cropped', 'Side part', 'Curls', 'Tied'];
const hairColours = [
  { name: 'black', label: 'Black', colour: 'd' },
  { name: 'brown', label: 'Brown', colour: 'h' },
  { name: 'auburn', label: 'Auburn', colour: 'w' },
  { name: 'blonde', label: 'Blonde', colour: 'y' },
];
const outfitStyles = ['Jacket', 'Overalls', 'Shirt', 'Hoodie'];
const clothesColours = [
  { name: 'copper', label: 'Copper', colour: 'c' },
  { name: 'slate', label: 'Slate', colour: 's' },
  { name: 'linen', label: 'Linen', colour: 'p' },
  { name: 'steel', label: 'Steel', colour: 'l' },
];
const legs = grid(`
  .omoomo.
  .omoomo.
  .oo..oo.
`);
const shoes = [
  { name: 'coal', label: 'Coal', colour: 'm' },
  { name: 'slate', label: 'Slate', colour: 's' },
  { name: 'brown', label: 'Brown', colour: 'w' },
  { name: 'cream', label: 'Cream', colour: 'p' },
];
const shoe = grid(`
  ........
  .uu..uu.
  ouo..ouo
`);
const relaxedArm = grid(`
  ou
  ou
  oa
  oo
`);
const foldedArms = grid(`
  ouabbaao
  .oooooo.
`);
const raisedArm = grid(`
  .oo
  oaa
  oao
  ouo
  ouo
  .ou
  ..o
`);
const typingArms = grid(`
  ou....uo
  oa....ao
  ..aaaa..
`);
const keyboard = grid(`
  .oooooo.
  olslsllo
  .oooooo.
`);
const clipboard = grid(`
  .ss.
  oppo
  osso
  opmo
  oooo
`);
const hat = grid(`
  ..oooo..
  .oyyyyo.
  oyyyyyyo
`);
const alert = grid(`
  oro
  oro
  .o.
  oro
`);
const faceFeatures = [
  { name: 'none', label: 'None', part: [] },
  { name: 'glasses', label: 'Glasses', part: grid(`sosos`), x: 5, y: 4 },
  { name: 'beard', label: 'Beard', part: grid(`hhhh\n.hh.`), y: 6 },
  { name: 'moustache', label: 'Moustache', part: grid(`hhh`), y: 5 },
];
// One cup, whether carried about, left on the table or lifted to drink.
const cup = grid(`opp\nopp`);
const tools = [
  { name: 'none', label: 'None', part: [], x: 0, y: 0 },
  { name: 'clipboard', label: 'Clipboard', part: clipboard, x: 11, y: 9 },
  { name: 'wrench', label: 'Wrench', part: grid(`s.s\n.s.\n.s.\n.s.\noso`), x: 12, y: 8 },
  { name: 'mug', label: 'Mug', part: cup, x: 12, y: 10 },
  { name: 'tablet', label: 'Tablet', part: grid(`oooo\notto\nokto\nooso`), x: 11, y: 10 },
];
const headwear = [
  { name: 'none', label: 'None', part: [], x: 0, y: 0 },
  { name: 'hard-hat', label: 'Hard hat', part: hat, x: 4, y: 0 },
  { name: 'cap', label: 'Cap', part: grid(`..oooo..\n.otttto.\notttttoo\n.oottttt`), x: 4, y: 0 },
  { name: 'headset', label: 'Headset', part: grid(`..ssssss..\n.s......s.\n.s......s.\nss......ss\nst......ts\n........so\n......sss.`), x: 3, y: 0 },
];
const skinTones = [
  { name: 'light', label: 'Light', skin: 'p', shadow: 'a' },
  { name: 'warm', label: 'Warm', skin: 'a', shadow: 'b' },
  { name: 'brown', label: 'Brown', skin: 'b', shadow: 'w' },
  { name: 'deep', label: 'Deep', skin: 'w', shadow: 'h' },
];
const optionGroups = {
  skin: skinTones.map(({ name, label }) => ({ name, label })),
  hair: hairStyles.map((label, index) => ({ name: `style-${index}`, label })),
  hair_colour: hairColours.map(({ name, label }) => ({ name, label })),
  face: faceFeatures.map(({ name, label }) => ({ name, label })),
  outfit: outfitStyles.map((label, index) => ({ name: `outfit-${index}`, label })),
  clothes_colour: clothesColours.map(({ name, label }) => ({ name, label })),
  shoes: shoes.map(({ name, label }) => ({ name, label })),
  tool: tools.map(({ name, label }) => ({ name, label })),
  headwear: headwear.map(({ name, label }) => ({ name, label })),
};
// Complete tile silhouettes; repeatable structure rather than random texture.
const floor = grid(`
  oooooooooooooooo
  oddddddddddddddo
  odldddddddddlddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  odldddddddddlddo
  oddddddddddddddo
  oooooooooooooooo
`);
const wall = grid(`
  llllllllllllllll
  mmmmmmmmmmmmmmmm
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  oooooooooooooooo
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  oooooooooooooooo
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  dddddddddddddddd
  oooooooooooooooo
`);

const sprites = new Map();
const providers = { claude_code: 'c', codex: 't', shell: 's' };
// A pose is the body's shape, independent of who wears it or why. Only the
// layers that bend (skin, sleeves, legs, shoes) are baked per pose; hair, face,
// headwear, tools and badges are drawn once and ride on every pose.
const poses = ['idle', 'waiting', 'wave.0', 'wave.1', 'type.0', 'type.1', 'walk.0', 'walk.1', 'sip'];
const add = (name, build) => { const pixels = blank(); build(pixels); sprites.set(name, pixels); };
const shortArm = grid(`
  ou
  oa
  oo
`);
const wavingArm = grid(`
  oo.
  aao
  oao
  ouo
  ouo
  .ou
  ..o
`);
// The cup itself is a held layer, so every skin and sleeve shares one.
const sippingArm = grid(`
  .ao
  .uo
  .uo
`);
const arms = pose => ({
  waiting: [foldedArms],
  'type.0': [typingArms],
  'type.1': [grid(`ou....uo\noaaa..ao\n....aa..`)],
  'wave.0': [relaxedArm, mirror(relaxedArm), raisedArm],
  'wave.1': [relaxedArm, mirror(relaxedArm), wavingArm],
  // The forward arm foreshortens; the pair alternates with the legs.
  'walk.0': [shortArm, mirror(relaxedArm)],
  'walk.1': [relaxedArm, mirror(shortArm)],
  sip: [relaxedArm, sippingArm],
})[pose] ?? [relaxedArm, mirror(relaxedArm)];
const armPosition = (pose, index) => index === 0 ? [pose === 'waiting' || pose.startsWith('type') ? 4 : 3, pose === 'waiting' ? 11 : 9]
  : index === 1 ? pose === 'sip' ? [10, 7] : [11, 9] : [2, 4];
for (const [skinIndex, tone] of skinTones.entries()) {
  const colour = rows => rows.map(row => row.replace(/[ab]/g, key => key === 'a' ? tone.skin : tone.shadow));
  for (const pose of poses) add(`person.skin.${skinIndex}.${pose}`, pixels => {
    draw(pixels, colour(head), 5, 2);
    arms(pose).forEach((part, index) => draw(pixels, keep(colour(part).map(row => row.replaceAll('u', tone.skin)), [tone.skin, tone.shadow]), ...armPosition(pose, index)));
  });
  // Closed eyes are the face's own shadow, so a blink suits every tone.
  add(`person.blink.${skinIndex}`, pixels => draw(pixels, [tone.shadow + tone.shadow], 7, 4));
}
// Behind glasses the eyes are the lenses, so a blink closes those instead.
add('person.blink.glasses', pixels => draw(pixels, ['s.s'], 6, 4));
for (const [outfitIndex, outfit] of outfits.entries()) for (const [colourIndex, colour] of clothesColours.entries()) for (const pose of poses) add(`person.outfit.${outfitIndex}.${colourIndex}.${pose}`, pixels => {
  draw(pixels, tint(outfit, colour.colour), 4, 6);
  // Overalls begin their legs a row early, inside the bib: that row belongs to
  // the torso's frame so the split between the legs survives the layering.
  if (outfitIndex === 1) draw(pixels, [legs[0].replaceAll('m', colour.colour)], 4, 12);
  arms(pose).forEach((part, index) => {
    const [x, y] = armPosition(pose, index);
    if (outfitIndex === 2) part = part.map((row, dy) => y + dy === 9 ? row : row.replaceAll('u', 'a'));
    // Hands and short sleeves reveal the skin layer beneath the clothes.
    part.forEach((row, dy) => [...row].forEach((key, dx) => {
      if (key === 'a' || key === 'b') pixels[y + dy][x + dx] = '.';
    }));
    draw(pixels, keep(tint(part, colour.colour), ['o', colour.colour]), x, y);
  });
});
// Legs sit beneath the torso and sleeves. Overalls carry their colour down.
const striding = grid(`
  .omoomo.
  .omoomo.
  .....oo.
`);
const lap = grid(`
  ommoommo
  .omoomo.
  .oo..oo.
`);
const legPoses = { stand: legs, 'walk.0': striding, 'walk.1': mirror(striding), sit: lap };
for (const [cloth, colour] of [['plain', 'm'], ...clothesColours.map(({ colour }, index) => [index, colour])]) for (const [pose, part] of Object.entries(legPoses)) {
  add(`person.legs.${cloth}.${pose}`, pixels => draw(pixels, part.map(row => row.replaceAll('m', colour)), 4, cloth === 'plain' ? 13 : 12));
}
const lifted = grid(`
  .uu.....
  ouo..uu.
  .....ouo
`);
for (const [shoeIndex, colour] of shoes.entries()) for (const [pose, part] of Object.entries({ stand: shoe, 'walk.0': lifted, 'walk.1': mirror(lifted) })) {
  add(`person.shoes.${shoeIndex}.${pose}`, pixels => draw(pixels, tint(part, colour.colour), 4, 13));
}
for (const [hairIndex, style] of hair.entries()) for (const [colourIndex, colour] of hairColours.entries()) add(`person.hair.${hairIndex}.${colourIndex}`, pixels => draw(pixels, tint(style, colour.colour), 4, 1));
for (const [featureIndex, feature] of faceFeatures.entries()) add(`person.face.${featureIndex}`, pixels => draw(pixels, feature.part, feature.x ?? 6, feature.y ?? 4));
// A tool is carried in the right hand, so it rises with that arm's forward swing.
for (const [toolIndex, tool] of tools.entries()) for (const [grip, lift] of [['low', 0], ['high', 1]]) add(`person.tool.${toolIndex}.${grip}`, pixels => draw(pixels, tool.part, tool.x, tool.y - lift));
for (const [hatIndex, item] of headwear.entries()) add(`person.headwear.${hatIndex}`, pixels => draw(pixels, item.part, item.x, item.y));
for (const role of ['worker', 'overseer']) for (const [provider, colour] of Object.entries(providers)) add(`person.system.${role}.${provider}`, pixels => {
  draw(pixels, [colour], 9, 8);
  if (role === 'overseer') draw(pixels, ['yy'], 7, 8);
});
add('person.alert', pixels => draw(pixels, alert, 13, 0));
// What the hands are busy with belongs to the moment, not the person.
add('person.held.keyboard', pixels => draw(pixels, keyboard, 2, 12));
add('person.held.cup', pixels => draw(pixels, cup, 8, 6));
add('person.held.pencil.0', pixels => draw(pixels, grid(`..y\n.y.\no..`), 10, 9));
// The pencil tips upright between strokes, beside the hand rather than over it.
add('person.held.pencil.1', pixels => draw(pixels, grid(`y\ny\no`), 11, 8));
// Walking away and walking across are their own drawings, not the front view
// slid sideways. The back has no face and carries its tool on the other side;
// the profile has one eye, one visible arm and scissoring legs. West is east
// mirrored by the scene, so only east is drawn.
const headBack = grid(`
  .oooo.
  oaaaao
  oaaaao
  oaaaao
  .obbo.
  ..bb..
`);
const headSide = grid(`
  .oooo..
  oaaaao.
  oaaaoo.
  oaaaaao
  .obbbo.
  ..ba...
`);
const hairBack = [
  grid(`
    ..oooo..
    .ouuuuo.
    .ouuuuo.
    .ouuuuo.
  `),
  grid(`
    .oooooo.
    .ouumuo.
    .ouumuo.
    .ouuuuo.
  `),
  grid(`
    ..oo.oo.
    .ouuouuo
    ouuuuuuo
    ouuuuuuo
    .ouuuuo.
  `),
  grid(`
    ..oooo..
    .ouuuuo.
    .ouuuuo.
    .ouuuuo.
    ..ouuo..
    ...uu...
    ...uu...
  `),
];
const hairSide = [
  grid(`
    ..oooo..
    .ouuuuo.
    .ouu....
    .ou.....
  `),
  grid(`
    .oooooo.
    .ouumuo.
    .ouu....
    .ou.....
  `),
  grid(`
    ..oo.oo.
    .ouuouuo
    ouuu....
    ouu.....
    .u......
  `),
  grid(`
    ..oooo..
    .ouuuuo.
    ouuu....
    uou.....
    uo......
    uo......
  `),
];
const outfitsBack = [
  grid(`
    ........
    ..o..o..
    .ouuuuo.
    ouuuuuuo
    ouuuuuuo
    ouuuuuuo
    ouo..ouo
  `),
  grid(`
    ........
    ........
    .ouppuo.
    oppuuppo
    opuuuupo
    ouuuuuuo
    .ouuuuo.
  `),
  grid(`
    ........
    ........
    ..ouuo..
    .ouuuuo.
    .ouuuuo.
    .ouuuuo.
    ..oooo..
  `),
  grid(`
    .ouuuuo.
    ouuuuuuo
    ouummuuo
    .ouuuuo.
    ouuuuuuo
    ouuuuuuo
    .oooooo.
  `),
];
const outfitsSide = [
  grid(`
    ........
    ...oo...
    .ouuupo.
    .ouuupo.
    .ouuupo.
    .ouuupo.
    .ouuuuo.
  `),
  grid(`
    ........
    ........
    .opuuuo.
    .opuuuo.
    .ouuuuo.
    .ouuuuo.
    .ouuuuo.
  `),
  grid(`
    ........
    ........
    ..ouuo..
    ..ouuuo.
    ..ouuuo.
    ..ouuuo.
    ..ooooo.
  `),
  grid(`
    ..ouo...
    .ouuuo..
    .ouuuuo.
    .ouuuuo.
    .ouupuo.
    .ouuumo.
    .oooooo.
  `),
];
// The near arm swings ahead of the body on one step and behind it on the next.
// It is outlined on its trailing edge only, so the torso keeps its colour.
const swingingArm = grid(`
  .uo.
  .uo.
  ..ao
  ..oo
`);
const views = {
  back: { head: headBack, hair: hairBack, outfits: outfitsBack, arms: pose => arms(pose).map((part, index) => [part, ...armPosition(pose, index)]) },
  side: { head: headSide, hair: hairSide, outfits: outfitsSide, arms: pose => [pose === 'walk.0' ? [swingingArm, 7, 9] : [mirror(swingingArm), 4, 9]] },
};
const strides = ['walk.0', 'walk.1'];
for (const [view, art] of Object.entries(views)) {
  for (const [skinIndex, tone] of skinTones.entries()) for (const pose of strides) add(`person.skin.${skinIndex}.${view}.${pose}`, pixels => {
    const colour = rows => rows.map(row => row.replace(/[ab]/g, key => key === 'a' ? tone.skin : tone.shadow));
    draw(pixels, colour(art.head), 5, 2);
    for (const [part, x, y] of art.arms(pose)) draw(pixels, keep(colour(part).map(row => row.replaceAll('u', tone.skin)), [tone.skin, tone.shadow]), x, y);
  });
  for (const [outfitIndex, outfit] of art.outfits.entries()) for (const [colourIndex, colour] of clothesColours.entries()) for (const pose of strides) add(`person.outfit.${outfitIndex}.${colourIndex}.${view}.${pose}`, pixels => {
    draw(pixels, tint(outfit, colour.colour), 4, 6);
    for (let [part, x, y] of art.arms(pose)) {
      if (outfitIndex === 2) part = part.map((row, dy) => y + dy === 9 ? row : row.replaceAll('u', 'a'));
      part.forEach((row, dy) => [...row].forEach((key, dx) => { if (key === 'a' || key === 'b') pixels[y + dy][x + dx] = '.'; }));
      draw(pixels, keep(tint(part, colour.colour), ['o', colour.colour]), x, y);
    }
  });
  for (const [hairIndex, style] of art.hair.entries()) for (const [colourIndex, colour] of hairColours.entries()) add(`person.hair.${hairIndex}.${colourIndex}.${view}`, pixels => draw(pixels, tint(style, colour.colour), 4, 1));
}
// From behind a cap has no peak and a headset no boom; in profile the headset
// shows one cup with the boom reaching forward. A hard hat is round all the way.
const headwearViews = {
  back: [[], hat, grid(`..oooo..\n.otttto.\n.otttto.`), grid(`..ssssss..\n.s......s.\n.s......s.\nss......ss\nss......ss`)],
  side: [[], hat, headwear[2].part, grid(`...ssss...\n...s......\n...s......\n...sss....\n...sts....\n....ssss..\n.......s..`)],
};
for (const [view, parts] of Object.entries(headwearViews)) for (const [hatIndex, part] of parts.entries()) add(`person.headwear.${hatIndex}.${view}`, pixels => draw(pixels, part, headwear[hatIndex].x, headwear[hatIndex].y));
const faceSide = [[], grid(`sso`), grid(`hh\n.h`), grid(`hh`)];
for (const [featureIndex, part] of faceSide.entries()) add(`person.face.${featureIndex}.side`, pixels => draw(pixels, part, featureIndex === 1 ? 7 : 8, featureIndex === 1 ? 4 : 6));
const sideStride = { 'walk.0': grid(`..ommo..\n.omoomo.\n.oo..oo.`), 'walk.1': grid(`..ommo..\n..ommo..\n..oooo..`) };
for (const [cloth, colour] of [['plain', 'm'], ...clothesColours.map(({ colour }, index) => [index, colour])]) for (const [pose, part] of Object.entries(sideStride)) {
  add(`person.legs.${cloth}.side.${pose}`, pixels => draw(pixels, part.map(row => row.replaceAll('m', colour)), 4, cloth === 'plain' ? 13 : 12));
}
const sideShoes = { 'walk.0': grid(`........\n.uu..uu.\n.ouo.ouo`), 'walk.1': grid(`........\n...uu...\n...ouuo.`) };
for (const [shoeIndex, colour] of shoes.entries()) for (const [pose, part] of Object.entries(sideShoes)) add(`person.shoes.${shoeIndex}.side.${pose}`, pixels => draw(pixels, tint(part, colour.colour), 4, 13));
// The tool stays in its hand: mirrored across the body from behind, and out
// ahead or trailing with the swinging arm in profile.
for (const [toolIndex, tool] of tools.entries()) {
  const wide = tool.part[0]?.length ?? 0;
  for (const [grip, lift] of [['low', 0], ['high', 1]]) add(`person.tool.${toolIndex}.back.${grip}`, pixels => draw(pixels, mirror(tool.part), 16 - tool.x - wide, tool.y - lift));
  add(`person.tool.${toolIndex}.side.walk.0`, pixels => draw(pixels, tool.part, 11, tool.y));
  add(`person.tool.${toolIndex}.side.walk.1`, pixels => draw(pixels, mirror(tool.part), 4 - wide, tool.y));
}
// Compare finished portraits: transparent layer differences can disappear in composition.
const legPose = pose => pose.startsWith('walk') ? pose : 'stand';
// Busy hands put the tool down: a cup, a keyboard or a pencil takes its place.
const carries = pose => pose !== 'sip' && !pose.startsWith('type');
const portrait = (pose, appearance = {}) => {
  const v = { skin: 1, hair: 0, hair_colour: 1, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0, ...appearance };
  const pixels = blank();
  for (const name of [`skin.${v.skin}.${pose}`, `legs.${v.outfit === 1 ? v.clothes_colour : 'plain'}.${legPose(pose)}`, `outfit.${v.outfit}.${v.clothes_colour}.${pose}`, `hair.${v.hair}.${v.hair_colour}`, `face.${v.face}`, `shoes.${v.shoes}.${legPose(pose)}`, `headwear.${v.headwear}`, ...(carries(pose) ? [`tool.${v.tool}.${pose === 'walk.1' ? 'high' : 'low'}`] : []), 'system.worker.codex']) {
    draw(pixels, sprites.get(`person.${name}`).map(row => row.join('')));
  }
  return pixels;
};
for (const pose of poses) {
  for (let colour = 0; colour < clothesColours.length; colour++) {
    const dressed = outfits.map((_, outfit) => portrait(pose, { outfit, clothes_colour: colour }).flat());
    for (let first = 0; first < dressed.length; first++) for (let second = first + 1; second < dressed.length; second++) {
      const difference = dressed[first].filter((pixel, index) => pixel !== dressed[second][index]).length;
      assert(difference >= 8, `Clothing needs more than trim differences: ${pose}/${colour}/${first}/${second}: ${difference} pixels`);
    }
  }
  for (const [group, options] of Object.entries(optionGroups)) {
    if (group === 'tool' && !carries(pose)) continue;
    assert.equal(new Set(options.map((_, index) => JSON.stringify(portrait(pose, { [group]: index })))).size, options.length, `Indistinguishable ${group}: ${pose}`);
  }
  for (const [skin, tone] of skinTones.entries()) {
    const pixels = portrait(pose, { skin });
    if (pose === 'idle') {
      assert.equal(portrait(pose, { skin, outfit: 2 })[10][4], tone.skin, 'Shirt must expose forearms');
      assert.equal(pixels[10][4], clothesColours[0].colour, 'Jacket must retain long sleeves');
    }
    arms(pose).forEach((part, index) => {
      const [x, y] = armPosition(pose, index);
      part.forEach((row, dy) => [...row].forEach((key, dx) => {
        if (key === 'a' || key === 'b') assert.equal(pixels[y + dy][x + dx], key === 'a' ? tone.skin : tone.shadow, `Hidden hand: ${pose}/${skin}`);
      }));
    });
  }
}
// What a person wears must never swallow what their body is doing: in every
// pose, under every hat, face and hairstyle, and with whatever the pose holds,
// both hands and the held thing itself stay on show.
const heldBy = { sip: ['cup'], 'type.0': ['keyboard', 'pencil.0'], 'type.1': ['keyboard', 'pencil.1'] };
const report = [];
for (const pose of poses) for (const [group, options] of Object.entries(optionGroups)) for (const [option] of options.entries()) for (const held of [undefined, ...(heldBy[pose] ?? [])]) {
  if (group === 'tool' && (held !== undefined || !carries(pose))) continue;
  const pixels = portrait(pose, { [group]: option, ...(held === undefined ? {} : { tool: 0 }) });
  const tone = skinTones[group === 'skin' ? option : 1];
  const heldPixels = held === undefined ? undefined : sprites.get(`person.held.${held}`);
  if (heldPixels) heldPixels.forEach((row, y) => row.forEach((key, x) => { if (key !== '.') pixels[y][x] = key; }));
  // Only the badge rides above what is held: a cup passes in front of a headset's boom.
  if (heldPixels) sprites.get('person.system.worker.codex').forEach((row, y) => row.forEach((key, x) => { if (key !== '.') pixels[y][x] = key; }));
  arms(pose).forEach((part, index) => {
    const [x, y] = armPosition(pose, index);
    part.forEach((row, dy) => [...row].forEach((key, dx) => {
      const gripped = group === 'tool' && option > 0 && index === 1;
      if (gripped && key === 'a') {
        const tool = sprites.get(`person.tool.${option}.${pose === 'walk.1' ? 'high' : 'low'}`);
        const near = [-1, 0, 1].some(ny => [-1, 0, 1].some(nx => tool[y + dy + ny]?.[x + dx + nx] !== undefined && tool[y + dy + ny][x + dx + nx] !== '.'));
        if (!near) report.push(`tool floats free of the hand: ${pose} tool=${option}`);
      } else if ((key === 'a' || key === 'b') && pixels[y + dy][x + dx] !== (key === 'a' ? tone.skin : tone.shadow)) report.push(`hand hidden: ${pose} ${group}=${option} held=${held} at ${x + dx},${y + dy} by ${pixels[y + dy][x + dx]}`);
    }));
  });
  if (heldPixels) heldPixels.forEach((row, y) => row.forEach((key, x) => { if (key !== '.' && pixels[y][x] !== key) report.push(`held hidden: ${pose} ${group}=${option} held=${held} at ${x},${y}`); }));
}
assert.deepEqual([...new Set(report)], [], 'A feature hides a pose');
for (const pose of ['idle', 'wave.0', 'walk.0', 'sip']) assert.equal(portrait(pose, { outfit: 1 })[12].slice(7, 9).join(''), 'oo', `Overalls lost the split between their legs: ${pose}`);
// The same promises hold walking away and walking across: every choice a person
// can make still tells them apart where it can be seen, both steps differ, and
// nothing worn hides the swinging hand.
const turned = (view, pose, appearance = {}) => {
  const v = { skin: 1, hair: 0, hair_colour: 1, face: 0, outfit: 0, clothes_colour: 0, shoes: 0, tool: 0, headwear: 0, ...appearance };
  const cloth = v.outfit === 1 ? v.clothes_colour : 'plain';
  const names = view === 'back'
    ? [`skin.${v.skin}.back.${pose}`, `legs.${cloth}.${pose}`, `outfit.${v.outfit}.${v.clothes_colour}.back.${pose}`, `hair.${v.hair}.${v.hair_colour}.back`, `shoes.${v.shoes}.${pose}`, `headwear.${v.headwear}.back`, `tool.${v.tool}.back.${pose === 'walk.0' ? 'high' : 'low'}`]
    : [`skin.${v.skin}.side.${pose}`, `legs.${cloth}.side.${pose}`, `outfit.${v.outfit}.${v.clothes_colour}.side.${pose}`, `hair.${v.hair}.${v.hair_colour}.side`, `face.${v.face}.side`, `shoes.${v.shoes}.side.${pose}`, `headwear.${v.headwear}.side`, `tool.${v.tool}.side.${pose}`, 'system.worker.codex'];
  const pixels = blank();
  for (const name of names) draw(pixels, sprites.get(`person.${name}`).map(row => row.join('')));
  return pixels;
};
for (const [view, art] of Object.entries(views)) {
  assert.notDeepEqual(turned(view, 'walk.0'), turned(view, 'walk.1'), `Indistinguishable steps: ${view}`);
  assert.notDeepEqual(turned(view, 'walk.0'), portrait('walk.0'), `${view} reads as the front`);
  for (const pose of strides) for (const [group, options] of Object.entries(optionGroups)) {
    if (view === 'back' && group === 'face') continue; // A face cannot be seen from behind.
    assert.equal(new Set(options.map((_, index) => JSON.stringify(turned(view, pose, { [group]: index })))).size, options.length, `Indistinguishable ${group}: ${view}/${pose}`);
    if (group === 'tool') continue;
    for (const [option] of options.entries()) {
      const pixels = turned(view, pose, { [group]: option }), tone = skinTones[group === 'skin' ? option : 1];
      for (const [part, x, y] of art.arms(pose)) part.forEach((row, dy) => [...row].forEach((key, dx) => {
        if (key === 'a' || key === 'b') assert.equal(pixels[y + dy][x + dx], key === 'a' ? tone.skin : tone.shadow, `Hidden hand: ${view}/${pose} ${group}=${option}`);
      }));
    }
  }
}
// Every pose must read as a different body, or the animation is invisible.
assert.equal(new Set(poses.map(pose => JSON.stringify(portrait(pose)))).size, poses.length, 'Indistinguishable poses');
function tile(name, rows) {
  const pixels = blank();
  draw(pixels, rows);
  sprites.set(name, pixels);
  return pixels;
}
tile('tile.floor.0', floor);
const floorVariant = tile('tile.floor.1', mirror(floor));
draw(floorVariant, grid(`mmm`), 6, 1);
tile('tile.wall', wall);

// The physical scene is assembled from these small, opaque 16px pieces. Keep
// each object inside one frame so SVG can scale it crisply without inventing a
// second coordinate system. Names are stable integration points for room
// composition; variants encode state, rather than asking the renderer to draw
// smooth vector furniture.
tile('tile.floor.2', grid(`
  oooooooooooooooo
  oddddddddddddddo
  oddldddddddldddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddddddddddddddo
  oddldddddddldddo
  oddddddddddddddo
  oooooooooooooooo
`));
tile('tile.wall.corner', grid(`
  lllllllllllllooo
  mmmmmmmmmmmmmooo
  mmmmmmmommmmmooo
  mmmmmmmommmmmooo
  mmmmmmmommmmmooo
  mmmmmmmommmmmooo
  oooooooooooooooo
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  oooooooooooooooo
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  dddddddddddddddd
  oooooooooooooooo
`));
tile('tile.wall.door', grid(`
  llllllllllllllll
  mmmmmmmmmmoommmm
  mmmmmmmmmmoodmmm
  mmmmmmmmmmoodmmm
  mmmmmmmmmmoodmmm
  mmmmmmmmmmoodmmm
  oooooooooooooooo
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  mmmommmmmmmommmm
  oooooooooooooooo
  mmmmmmmommmmmmmm
  mmmmmmmommmmmmmm
  dddddddddddddddd
  oooooooooooooooo
`));
const worldObjects = {
  'prop.workbench': grid(`
    ................
    ....oooooooo....
    ...ommmmmmmmo...
    ...omppppppmo...
    ...omppppppmo...
    ...ooooooooo....
    .....oww........
    .....oww........
    ..owwwwwwwwo....
    ..ow......wo....
    ..ow......wo....
    ..ow......wo....
    ..ow......wo....
    ..ow......wo....
    ..ooooooooo.....
    ................
  `),
  'prop.bench': grid(`
    ................
    ...oooooooooo...
    ..ommmmmmmmmo...
    ..owwwwwwwww....
    ...oooooooo.....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....ow....wo....
    ....oooooooo....
    ................
  `),
  'prop.rack': grid(`
    ....oo..oo......
    ....om..mo......
    ....om..mo......
    ..oooooooooo....
    ..ommmmmmmmo....
    ..omppppppmo....
    ..oooooooooo....
    ..ommmmmmmmo....
    ..omssssssmo....
    ..oooooooooo....
    ..ommmmmmmmo....
    ..omllllllmo....
    ..oooooooooo....
    ....ow..wo......
    ....ow..wo......
    ....ow..wo......
  `),
  'prop.cabinet': grid(`
    ...ooooooooo....
    ..ommmmmmmmo....
    ..omppppppmo....
    ..oms..s..mo....
    ..ommmmmmmmo....
    ..omppppppmo....
    ..oms..s..mo....
    ..ommmmmmmmo....
    ..omllllllmo....
    ..ommmmmmmmo....
    ...ow....wo.....
    ...ow....wo.....
    ...ow....wo.....
    ...ow....wo.....
    ...oooooooo.....
    ................
  `),
  'prop.terminal': grid(`
    ................
    ....oooooo......
    ...omttttmo.....
    ...omttttmo.....
    ...omttttmo.....
    ....oooooo......
    .....oww........
    ....owwwwo......
    ...ow....wo.....
    ...ow....wo.....
    ...ow....wo.....
    ...ow....wo.....
    ...oooooooo.....
    ................
    ................
    ................
  `),
  'prop.coffee': grid(`
    ................
    ......oooo......
    .....oppppo.....
    .....oppppoo....
    ......oooo......
    .......ww.......
    ....owwwwww.....
    ...ow......wo...
    ...ow......wo...
    ...ow......wo...
    ...ow......wo...
    ...ow......wo...
    ...ow......wo...
    ...ow......wo...
    ...owwwwwwwwo...
    ................
  `),
  'prop.cup': grid(`
    ................
    ......oooo......
    .....oppppo.....
    .....oppppoo....
    ......oooo.o....
    .......oo.......
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'prop.book': grid(`
    ................
    ....oooooooo....
    ...oppppppppo...
    ...oppppppppo...
    ...oppppppppo...
    ...oppppppppo...
    ....oooooooo....
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'prop.board': grid(`
    ...ooooooooo....
    ..opppppppppo...
    ..optytytyppo...
    ..opppppppppo...
    ..oprrppppppo...
    ..opppppppppo...
    ..oooooooooo....
    .....oww........
    .....oww........
    .....oww........
    ....owwww.......
    ...ow....wo.....
    ...ow....wo.....
    ...ow....wo.....
    ...oooooooo.....
    ................
  `),
  'prop.lamp.on': grid(`
    .......y........
    ......yyy.......
    .....yyyyy......
    ....yyyyyyy.....
    .......o........
    .......o........
    ......ooo.......
    ................
    .......o........
    ......ooo.......
    .....ooooo......
    .......o........
    .......o........
    .......o........
    .......o........
    ................
  `),
  'prop.lamp.off': grid(`
    .......o........
    ......ooo.......
    .....omomo......
    ....ommmmo......
    .......o........
    .......o........
    ......ooo.......
    ................
    .......o........
    ......ooo.......
    .....ooooo......
    .......o........
    .......o........
    .......o........
    .......o........
    ................
  `),
  'prop.cable.horizontal': grid(`
    ................
    ................
    ................
    ...oooooooooo...
    ...otttttttto...
    ...oooooooooo...
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'prop.cable.vertical': grid(`
    .......o........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......t........
    .......o........
  `),
  'indicator.ok': grid(`
    ................
    ................
    .......kk.......
    ......kkk.......
    .....kk.kk......
    .....kk..kk.....
    ......kkk.......
    .......kk.......
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'indicator.attention': grid(`
    ................
    .......y........
    ......yyy.......
    .....yyyyy......
    .......y........
    .......y........
    .......o........
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'indicator.chat': grid(`
    ................
    ...oooo..oooo...
    ..oppppo.opppo..
    ..oppppo.opppo..
    ...oooo..oooo...
    ................
    .....oo.........
    ....oooo........
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
  'indicator.fault': grid(`
    ................
    .......r........
    ......rrr.......
    .....rrrrr......
    .......r........
    .......r........
    .......o........
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
    ................
  `),
};
for (const [name, rows] of Object.entries(worldObjects)) tile(name, rows);

const frame = 16;
const width = 8 * frame;
const height = Math.ceil(sprites.size / 8) * frame;
const pixels = Buffer.alloc(width * height * 4);
const atlas = { frame, sheet: 'sprites.png', frames: {} };
let index = 0;
for (const [name, rows] of sprites) {
  const x = (index % 8) * frame;
  const y = Math.floor(index++ / 8) * frame;
  atlas.frames[name] = { x, y };
  rows.forEach((row, dy) => row.forEach((key, dx) => {
    rgba[key].copy(pixels, ((y + dy) * width + x + dx) * 4);
  }));
}

function crc32(bytes) {
  let crc = 0xffffffff;
  for (const byte of bytes) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  return (crc ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const out = Buffer.alloc(data.length + 12);
  out.writeUInt32BE(data.length, 0);
  out.write(type, 4, 4, 'ascii');
  data.copy(out, 8);
  out.writeUInt32BE(crc32(out.subarray(4, -4)), out.length - 4);
  return out;
}
const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(width, 0);
ihdr.writeUInt32BE(height, 4);
ihdr[8] = 8; // Eight bits per channel.
ihdr[9] = 6; // RGBA, no interlace.
const stride = width * 4;
const scanlines = Buffer.alloc((stride + 1) * height);
for (let y = 0; y < height; y++) pixels.copy(scanlines, y * (stride + 1) + 1, y * stride, (y + 1) * stride);
const png = Buffer.concat([
  signature, chunk('IHDR', ihdr),
  chunk('IDAT', deflateSync(scanlines, { level: 9 })), chunk('IEND', Buffer.alloc(0)),
]);
const dataUrl = `data:image/png;base64,${png.toString('base64')}`;
const atlasJson = JSON.stringify(atlas);
const preview = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Dark Factory · Sprite atlas</title>
<style>
  * { box-sizing: border-box; }
  body { margin: 24px; background: #08131d; color: #e5d5ad; font: 13px system-ui,sans-serif; }
  h1 { font-size: 24px; margin-bottom: 8px; }
  p, figcaption { color: #8b95a5; }
  h2 { font-size: 15px; margin-top: 24px; }
  section { display: grid; grid-template-columns: repeat(auto-fit,minmax(240px,1fr)); gap: 12px; max-width: 1100px; }
  figure { margin: 0; padding: 12px; border: 1px solid #303741; background: #101f2b; }
  .scales { display: flex; align-items: end; justify-content: space-between; margin: 12px 0; }
  canvas { display: block; image-rendering: pixelated; }
  small { display: block; margin-top: 6px; text-align: center; color: #8b95a5; }
  label { display: inline-block; margin: 0 16px 12px 0; }
  select { font: inherit; color: inherit; background: #191d24; padding: 6px; }
</style>
<h1>Dark Factory / Sprite workbench</h1>
<p>${sprites.size} reusable layers · shared 16-colour palette · 16 × 16 footprint.</p>
<label>Role <select id="role"><option value="worker">Worker</option><option value="overseer">Overseer</option></select></label>
<label>Provider badge <select id="provider">${Object.keys(providers).map(provider => `<option>${provider}</option>`).join('')}</select></label>
<p>Each pose: native 1×, normal display 3×, enlarged 8×, over the factory floor tile.</p>
${hairStyles.map((style, index) => `<h2>${index} · ${style}</h2><section>${poses.map(activity => `<figure><figcaption>${activity}</figcaption><div class="scales">${[1, 3, 8].map(scale => `<div><canvas width="16" height="16" style="width:${16 * scale}px;height:${16 * scale}px" data-identity="${index}" data-activity="${activity}" role="img" aria-label="${style}, ${activity}, ${scale}×"></canvas><small>${scale}×</small></div>`).join('')}</div></figure>`).join('')}</section>`).join('')}
<h2>Floor / equipment bays</h2>
<section>${[...sprites.keys()].filter(name => /^(tile|bay)\./.test(name)).map(name => `<figure><figcaption>${name}</figcaption><canvas width="16" height="16" style="width:48px;height:48px" data-tile="${name}" role="img" aria-label="${name}"></canvas></figure>`).join('')}</section>
<p>Edit hair, outfits, identities or equipment in gen-sprites.mjs, run <code>node web/packages/ui/src/factory-scene/sprites/gen-sprites.mjs</code>, reload this page, then inspect the console fixture floor.</p>
<script>
const atlas = ${JSON.stringify(atlas)};
const image = new Image();
const role = document.getElementById('role');
const provider = document.getElementById('provider');
function paint() {
  for (const canvas of document.querySelectorAll('canvas')) {
    const context = canvas.getContext('2d');
    context.clearRect(0, 0, 16, 16);
    const floor = atlas.frames['tile.floor.0'];
    context.drawImage(image, floor.x, floor.y, 16, 16, 0, 0, 16, 16);
    const identity = Number(canvas.dataset.identity);
    const activity = canvas.dataset.activity;
    const names = canvas.dataset.tile ? [canvas.dataset.tile] : [
      'person.skin.' + (identity < 2 ? 1 : 2) + '.' + activity,
      'person.legs.' + (identity === 1 ? identity : 'plain') + '.' + (activity.startsWith('walk') ? activity : 'stand'),
      'person.outfit.' + identity + '.' + identity + '.' + activity,
      'person.hair.' + identity + '.' + identity,
      'person.face.0',
      'person.shoes.' + identity + '.' + (activity.startsWith('walk') ? activity : 'stand'),
      ...(activity === 'sip' ? ['person.held.cup'] : activity.startsWith('type') ? ['person.held.keyboard'] : ['person.tool.' + (role.value === 'overseer' ? 1 : 0) + (activity === 'walk.1' ? '.high' : '.low')]),
      'person.headwear.' + (role.value === 'overseer' ? 1 : 0),
      'person.system.' + role.value + '.' + provider.value,
      ...(activity.startsWith('wave') ? ['person.alert'] : []),
    ];
    for (const name of names) { const {x,y} = atlas.frames[name]; context.drawImage(image, x, y, 16, 16, 0, 0, 16, 16); }
  }
}
image.onload = () => {
  paint();
  for (const select of [role, provider]) select.onchange = paint;
};
image.src = ${JSON.stringify(dataUrl)};
</script>
</html>
`;
const root = new URL('./', import.meta.url);
for (const [name, data] of Object.entries({
  'sprites.png': png,
  'sprites.generated.ts': `export const spriteSheet = ${JSON.stringify(dataUrl)};\nexport const spriteSheetSize = ${JSON.stringify({ width, height })} as const;\nexport const spriteAtlas = ${atlasJson} as const;\nexport const spriteOptions = ${JSON.stringify(optionGroups)} as const;\n`,
  'preview.html': preview,
})) writeFileSync(new URL(name, root), data);

// Decode the actual file: verify chunk boundaries, CRCs, format, and every RGBA byte.
const decodedFile = readFileSync(new URL('sprites.png', root));
assert.deepEqual(decodedFile.subarray(0, 8), signature);
const idats = [];
const chunkTypes = [];
let offset = 8;
while (offset < decodedFile.length) {
  const length = decodedFile.readUInt32BE(offset);
  const end = offset + length + 12;
  assert(end <= decodedFile.length, 'Truncated PNG chunk');
  const type = decodedFile.toString('ascii', offset + 4, offset + 8);
  const data = decodedFile.subarray(offset + 8, end - 4);
  assert.equal(decodedFile.readUInt32BE(end - 4), crc32(decodedFile.subarray(offset + 4, end - 4)));
  chunkTypes.push(type);
  if (type === 'IHDR') assert.deepEqual(data, ihdr);
  if (type === 'IDAT') idats.push(data);
  offset = end;
}
assert.equal(offset, decodedFile.length);
assert.deepEqual(chunkTypes, ['IHDR', 'IDAT', 'IEND']);
const inflated = inflateSync(Buffer.concat(idats));
assert.equal(inflated.length, (stride + 1) * height);
for (let y = 0; y < height; y++) {
  assert.equal(inflated[y * (stride + 1)], 0, 'Expected PNG filter zero');
  assert.deepEqual(inflated.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1)), pixels.subarray(y * stride, (y + 1) * stride));
}
const expectedNames = [...sprites.keys()];
const generated = readFileSync(new URL('sprites.generated.ts', root), 'utf8');
const savedAtlas = JSON.parse(generated.slice(generated.indexOf('spriteAtlas = ') + 14, generated.indexOf(' as const;\nexport const spriteOptions')));
assert.deepEqual(savedAtlas, atlas);
assert.match(generated, new RegExp(`spriteSheetSize = \\{"width":${width},"height":${height}\\}`));
assert.deepEqual(Object.keys(savedAtlas.frames).sort(), expectedNames.sort());
const occupied = new Set();
for (const {x, y} of Object.values(savedAtlas.frames)) {
  assert(Number.isInteger(x) && Number.isInteger(y) && x >= 0 && y >= 0);
  assert(x % 16 === 0 && y % 16 === 0 && x + 16 <= width && y + 16 <= height);
  assert(!occupied.has(`${x},${y}`), 'Overlapping atlas entries');
  occupied.add(`${x},${y}`);
}
assert.equal(Object.keys(palette).length, 16);
assert.equal(sprites.get('person.alert').flat().filter(key => key === 'r').length, 3);
console.log(`Verified PNG decode, 16-colour palette, exact atlas names, bounds, and sprite layers.`);
console.log(`Sheet: ${width} × ${height} RGBA; frames: ${sprites.size}`);
for (const name of ['gen-sprites.mjs', 'sprites.png', 'sprites.generated.ts', 'preview.html']) {
  const path = fileURLToPath(new URL(name, root));
  console.log(`${name}: ${statSync(path).size} bytes; sha256 ${createHash('sha256').update(readFileSync(path)).digest('hex')}`);
}
