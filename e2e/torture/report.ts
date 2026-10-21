// globalTeardown for playwright.torture.config.ts: fold every
// <RUN_DIR>/<scenario>.json the scenarios wrote into <RUN_DIR>/report.md —
// one table of checks per scenario, red first, with the metrics and the
// error-line scan next to them. This is the artifact to read after
// `make display-torture`; the .log/.png sidecars carry the detail.
import { readdirSync, readFileSync, writeFileSync, existsSync, symlinkSync, unlinkSync } from 'node:fs';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(__dirname, '..', '..');
const RUN_DIR = process.env.WASH_TORTURE_DIR || join(REPO, 'tmp', 'torture', 'last');

interface Check { name: string; ok: boolean; detail: string }
interface Ev { scenario: string; startedAt: string; durationMs: number; metrics: Record<string, unknown>; checks: Check[]; errors: { kind: string; count: number; sample: string }[]; notes: string[] }

export default async function report(): Promise<void> {
  if (!existsSync(RUN_DIR)) return;
  const files = readdirSync(RUN_DIR).filter((f) => f.endsWith('.json') && f !== 'playwright.json').sort();
  const evs: Ev[] = files.map((f) => JSON.parse(readFileSync(join(RUN_DIR, f), 'utf8')) as Ev);
  const lines: string[] = [];
  const totalRed = evs.reduce((n, e) => n + e.checks.filter((c) => !c.ok).length, 0);
  const totalChecks = evs.reduce((n, e) => n + e.checks.length, 0);
  lines.push(`# wash-display torture report`);
  lines.push('');
  lines.push(`Run dir: \`${RUN_DIR}\`  ·  scenarios: ${evs.length}  ·  checks: ${totalChecks}  ·  red: **${totalRed}**`);
  lines.push('');
  lines.push('| scenario | red / checks | duration | compositor RSS start→end | errors in log |');
  lines.push('|---|---|---|---|---|');
  for (const e of evs) {
    const red = e.checks.filter((c) => !c.ok).length;
    const p = (e.metrics as any)['procs.base']?.compositor?.rssKb;
    const q = (e.metrics as any)['procs.end']?.compositor?.rssKb;
    lines.push(`| ${e.scenario} | ${red ? `**${red}**` : '0'} / ${e.checks.length} | ${(e.durationMs / 1000).toFixed(1)}s | ${p ?? '?'}→${q ?? '?'} KB | ${e.errors.length ? e.errors.map((x) => `${x.kind}×${x.count}`).join(', ') : '—'} |`);
  }
  for (const e of evs) {
    lines.push('');
    lines.push(`## ${e.scenario}`);
    lines.push('');
    const sorted = [...e.checks].sort((a, b) => Number(a.ok) - Number(b.ok));
    for (const c of sorted) lines.push(`- ${c.ok ? '✅' : '❌'} **${c.name}**${c.detail ? ` — ${c.detail}` : ''}`);
    if (e.errors.length) {
      lines.push('');
      lines.push('Log error scan:');
      for (const x of e.errors) lines.push(`- ${x.kind} ×${x.count}: \`${x.sample}\``);
    }
    if (e.notes.length) {
      lines.push('');
      lines.push('Notes:');
      for (const n of e.notes) lines.push(`- ${n}`);
    }
    const m = Object.entries(e.metrics).filter(([k]) => !k.startsWith('procs.'));
    if (m.length) {
      lines.push('');
      lines.push('Metrics:');
      for (const [k, v] of m) lines.push(`- ${k}: \`${JSON.stringify(v)}\``);
    }
    lines.push('');
    lines.push(`Evidence: \`${e.scenario}.log\`, \`${e.scenario}.*.png\`, \`guests/\``);
  }
  writeFileSync(join(RUN_DIR, 'report.md'), lines.join('\n') + '\n');
  // tmp/torture/last → this run, for the inner loop.
  const last = join(REPO, 'tmp', 'torture', 'last');
  if (resolve(RUN_DIR) !== resolve(last)) {
    try { unlinkSync(last); } catch { /* none */ }
    try { symlinkSync(RUN_DIR, last); } catch { /* best effort */ }
  }
  console.log(`\ntorture report: ${join(RUN_DIR, 'report.md')}  (${totalRed} red / ${totalChecks} checks)`);
}
