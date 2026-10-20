// Render public/wallpapers/<id>.svg → public/wallpapers/thumbs/<id>.jpg, the
// small previews the Settings pack gallery shows (Pack.thumbnail in
// web/lib/src/packs.ts). The wallpapers are up to 3.4 MB of SVG; a 320×180
// JPEG is ~15 KB. Not part of the build — the JPEGs are committed. Re-run after
// changing or adding a wallpaper:
//
//   node web/shell/wallpaper-thumbs.mjs
//
// Uses the e2e suite's Playwright Chromium (cd e2e && pnpm install first).
import pw from '../../e2e/node_modules/@playwright/test/index.js';
import { mkdirSync, readdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const { chromium } = pw;
const DIR = join(dirname(fileURLToPath(import.meta.url)), 'public', 'wallpapers');
const OUT = join(DIR, 'thumbs');
// 2× the 160×90 card, cropped the way the card's `center/cover` crops.
const W = 320, H = 180;
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: W, height: H } });
// A file:// origin, so setContent's <img> may load the local SVGs.
await page.goto(pathToFileURL(DIR + '/').href);
for (const name of readdirSync(DIR).filter((n) => n.endsWith('.svg')).sort()) {
  const id = name.slice(0, -4);
  await page.setContent(
    `<body style="margin:0"><img src="${pathToFileURL(join(DIR, name))}" ` +
    `style="width:${W}px;height:${H}px;object-fit:cover;display:block"></body>`);
  await page.locator('img').evaluate((img) => img.decode());
  const out = join(OUT, `${id}.jpg`);
  await page.screenshot({ path: out, type: 'jpeg', quality: 85 });
  console.log(`${id}: ${(statSync(join(DIR, name)).size / 1024).toFixed(0)} KB svg → ${(statSync(out).size / 1024).toFixed(1)} KB jpg`);
}
await browser.close();
