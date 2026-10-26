// Build each screensaver into a standalone ES module in the shell assets:
// web/shell/public/screensavers/<id>.js (+ its <id>.jpg thumbnail), which the
// shell build copies into dist and the router embeds. The shell loads one only
// when it starts (web/lib/src/screensaver.ts loadSaver), over the asset
// channel, so the effects cost nothing until then.
//
// Each entry (src/entries/<id>.ts) exports `options` and `mount` — the
// SaverModule contract in screensaver.ts. Keep the ids in step with
// SCREENSAVERS there.

import { copyFileSync, mkdirSync, readdirSync } from 'node:fs';
import { dirname, resolve, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const here = dirname(fileURLToPath(import.meta.url));
const out = resolve(here, '..', 'shell', 'public', 'screensavers');
mkdirSync(out, { recursive: true });

const entries = readdirSync(resolve(here, 'src', 'entries')).filter((f) => /\.ts$/.test(f) && f !== 'canvas.ts');
await Promise.all(
  entries.map((f) =>
    build({
      entryPoints: [resolve(here, 'src', 'entries', f)],
      outfile: resolve(out, basename(f, '.ts') + '.js'),
      bundle: true,
      format: 'esm',
      platform: 'browser',
      target: 'es2022',
      minify: true,
      legalComments: 'inline',
      logLevel: 'warning',
    }),
  ),
);
for (const f of readdirSync(resolve(here, 'thumbs'))) copyFileSync(resolve(here, 'thumbs', f), resolve(out, f));
console.log(`screensavers: ${entries.length} modules → ${out}`);
