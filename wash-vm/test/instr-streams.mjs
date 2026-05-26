// Playwright assertion: the new browser-side instrumentation streams
// reach the server log, AND the ctl-`dump` round-trip produces
// machine + CPU + trap output.
//
// What it checks against /tmp/wash-demo-server.log (the WS sink the
// server.mjs writes alongside stdout):
//   [viz]            initial visibilityState frame on page load
//   [heap] sample    periodic ~10s WASM/JS heap line
//   [rate]           periodic iter-rate sampler (~3s)
//   [longtask]       PerformanceObserver entry (Chromium-only — soft fail
//                    if it never fires, but never on Firefox)
//   [wsq]            only emits on reconnect/watermark — informational
//   [ctl] dump       echo of the dump request
//   [tinyemu.stderr] WASH-MACH + WASH-CPU lines from wash_dump_global
//                    (proves Module._wash_dump_global landed in the WASM
//                    export table and the dispatcher reached the CPU
//                    class table)
//
// Usage:
//   node wash-vm/test/instr-streams.mjs            # headless
//   HEADLESS=false node wash-vm/test/instr-streams.mjs   # see the page
//
// Assumes the wash demo server is already running on :5180.

import { chromium } from '/home/mick/wash/e2e/node_modules/@playwright/test/index.mjs';
import { readFileSync, statSync } from 'node:fs';

const TARGET = 'http://localhost:5180/';
const ADMIN_DUMP = 'http://localhost:5180/admin/dump';
const LOG_FILE = process.env.WASH_LOG_FILE || '/tmp/wash-demo-server.log';
const HEADLESS = process.env.HEADLESS !== 'false';
const BOOT_WAIT_S = Number(process.env.BOOT_WAIT_S || 30);
const POST_DUMP_WAIT_S = Number(process.env.POST_DUMP_WAIT_S || 4);

// Remember log size on entry so we only grep what this run produced.
const startOffset = (() => {
  try { return statSync(LOG_FILE).size; } catch { return 0; }
})();
function readNew() {
  const buf = readFileSync(LOG_FILE);
  return buf.subarray(startOffset).toString('utf8');
}

console.log(`[instr] HEADLESS=${HEADLESS}  log=${LOG_FILE}  startOffset=${startOffset}`);
console.log(`[instr] launching chromium → ${TARGET}`);

const browser = await chromium.launch({ headless: HEADLESS, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
const page = await ctx.newPage();

page.on('pageerror', (e) => console.log('[page-error]', String(e).slice(0, 200)));
page.on('console', (msg) => {
  if (msg.type() === 'error') console.log('[page-error]', msg.text().slice(0, 200));
});

await page.goto(TARGET, { waitUntil: 'domcontentloaded' });
console.log(`[instr] page loaded, waiting ${BOOT_WAIT_S}s for VM boot + at least one heap/rate cycle…`);
await new Promise(r => setTimeout(r, BOOT_WAIT_S * 1000));

console.log(`[instr] requesting dump via POST ${ADMIN_DUMP}`);
const dumpRes = await fetch(ADMIN_DUMP, { method: 'POST' });
console.log(`[instr] dump returned http=${dumpRes.status}`);
await new Promise(r => setTimeout(r, POST_DUMP_WAIT_S * 1000));

const newLog = readNew();
await browser.close();

// Match per-line so a [foo] inside a larger frame doesn't satisfy the
// check — we want to see the actual source-tag prefix the cli/tail
// renders.
// Log line format from server.mjs is `YYYY-MM-DD HH:MM:SS.sss  [src] body`.
// Anchor on a line that's NOT inside the timestamp itself.
const checks = [
  { tag: '[viz]',            re: /\s\[viz\]\s/,                                       required: true  },
  { tag: '[heap] sample',    re: /\s\[heap\]\s+sample\s/,                             required: true  },
  { tag: '[heap] on-dump',   re: /\s\[heap\]\s+on-dump\s/,                            required: true  },
  { tag: '[rate]',           re: /\s\[rate\]\s/,                                      required: true  },
  { tag: '[ctl] dump',       re: /\s\[ctl\]\s+dump\s+requested/,                      required: true  },
  // WASH-MACH/CPU/TRAP routes through whatever stderr capture path
  // reached it first. With our Module.printErr override that's
  // [tinyemu.stderr]; if Emscripten hooked console.error before our
  // wrap landed, the [console.error] tap surfaces it instead. Accept
  // either — the data is what matters.
  { tag: 'WASH-MACH',        re: /\[(tinyemu\.stderr|console\.error)\]\s+\[WASH-MACH\]/, required: true  },
  { tag: 'WASH-CPU',         re: /\[(tinyemu\.stderr|console\.error)\]\s+\[WASH-CPU\d+\]/, required: true  },
  { tag: 'WASH-TRAP',        re: /\[(tinyemu\.stderr|console\.error)\]\s+\[WASH-TRAP\]/, required: true  },
  // longtask: Chromium-only API. Soft-required.
  { tag: '[longtask]',       re: /\s\[longtask\]\s/,                                  required: false },
  // wsq: only emits on reconnect/watermark.
  { tag: '[wsq]',            re: /\s\[wsq\]\s/,                                       required: false },
];

let failed = 0;
console.log('\n[instr] results (new log slice = ' + newLog.length + ' bytes):');
for (const c of checks) {
  const hit = c.re.test(newLog);
  const status = hit ? 'ok' : (c.required ? 'MISSING' : 'absent (ok)');
  console.log(`  ${hit ? '✓' : (c.required ? '✗' : '○')} ${c.tag.padEnd(20)} ${status}`);
  if (!hit && c.required) failed++;
}

if (failed > 0) {
  console.error(`\n[instr] FAIL — ${failed} required stream(s) missing`);
  console.error('[instr] log slice tail:\n' + newLog.split('\n').slice(-40).join('\n'));
  process.exit(1);
}
console.log('\n[instr] PASS — all required streams present');
