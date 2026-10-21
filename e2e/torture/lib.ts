// Shared plumbing for the wash-display torture scenarios (../playwright.torture.config.ts).
//
// Design: the fixture in ../fixtures/router.ts already boots a throwaway
// router (which autostarts the real compositor on shell connect) and gives us
// its log + pid. This file adds the three things a torture run needs on top:
//
//   1. DISCOVERY — parse DISPLAY / WAYLAND_DISPLAY / compositor pid / Xwayland
//      pid out of the router log, so guests can be spawned DIRECTLY from Node
//      (no typing into a wash terminal) with the compositor's sockets in env.
//   2. GUESTS — spawn real clients (the scriptable Tk guest in
//      tools/display-tkguest.py, plain X apps, Chromium) and read what they
//      actually received (their GUEST: log), so assertions are grounded in the
//      client, not only in compositor log lines.
//   3. EVIDENCE — per-scenario metrics (RSS/fds/threads of compositor, router,
//      Xwayland; orphan scan; log error scan; canvas pixel stats) and named
//      checks, written to tmp/torture/<run>/<scenario>.json + sidecar
//      .log/.png files; torture/report.ts folds them into report.md.
import { expect, type Page, type Locator } from '@playwright/test';
import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, readlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { RouterHandle } from '../fixtures/router';

const __dirname = dirname(fileURLToPath(import.meta.url));
export const REPO = resolve(__dirname, '..', '..');
export const TK_GUEST = join(REPO, 'tools', 'display-tkguest.py');

// Run directory: one per `make display-torture` (WASH_TORTURE_DIR), else a
// fixed tmp/torture/last so an ad-hoc `playwright test -c …` still lands
// somewhere predictable.
export const RUN_DIR = process.env.WASH_TORTURE_DIR || join(REPO, 'tmp', 'torture', 'last');
mkdirSync(RUN_DIR, { recursive: true });

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// ---- 1. discovery ---------------------------------------------------------

export interface DisplayInfo {
  x: string;        // ":N"
  wayland: string;  // "wayland-N"
  compositorPid: number;
  xwaylandPid: number;
  env: NodeJS.ProcessEnv; // env for spawning guests against this compositor
}

// bootDisplay: open the shell (the compositor autostarts on shell connect),
// wait for Xwayland + the env.publish that carries the display names, and
// return everything needed to talk to this compositor from outside wash.
export async function bootDisplay(page: Page, router: RouterHandle): Promise<DisplayInfo> {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 30_000);
  await router.waitForLog(/env\.publish from .*keys=.*WASH_X_DISPLAY/, 30_000);
  const log = router.log();
  const x = /Xwayland ready on DISPLAY=(:\d+)/.exec(log)?.[1] ?? '';
  const wayland = /compositor up on WAYLAND_DISPLAY=(\S+)/.exec(log)?.[1] ?? '';
  const compositorPid = Number(/wash-display: starting version=\S+ proto=\d+ pid=(\d+)/.exec(log)?.[1] ?? 0);
  expect(x, 'DISPLAY parsed from router log').not.toBe('');
  expect(compositorPid, 'compositor pid parsed from router log').toBeGreaterThan(0);
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    DISPLAY: x,
    WAYLAND_DISPLAY: wayland,
  };
  delete env.XAUTHORITY; // wlroots' Xwayland runs without -auth
  // Xwayland is LAZY: "Xwayland ready" is logged at create time, the X server
  // process only exists once the first X client connects. resolveXwayland()
  // fills the pid in after a guest has mapped.
  return { x, wayland, compositorPid, xwaylandPid: 0, env };
}

// Xwayland is forked by wlroots (double fork → reparented to init), so find it
// by argv ("Xwayland :N …"). Call after the first X client is up.
export function resolveXwayland(info: DisplayInfo): number {
  if (info.xwaylandPid && alive(info.xwaylandPid)) return info.xwaylandPid;
  info.xwaylandPid = findPid((argv) => /(^|\/)Xwayland\0/.test(argv) && argv.includes(`\0${info.x}\0`));
  return info.xwaylandPid;
}

export function findPid(match: (argv: string) => boolean): number {
  for (const pid of readdirSync('/proc')) {
    if (!/^\d+$/.test(pid)) continue;
    let argv = '';
    try {
      argv = readFileSync(`/proc/${pid}/cmdline`, 'utf8');
    } catch {
      continue;
    }
    if (argv && match(argv)) return Number(pid);
  }
  return 0;
}

export function alive(pid: number): boolean {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

// ---- 2. guests ------------------------------------------------------------

export interface GuestOpts {
  title?: string;
  width?: number;
  height?: number;
  env?: NodeJS.ProcessEnv;
}

// A scriptable Tk guest (tools/display-tkguest.py): control fifo in, GUEST:
// log out. send() writes one command; waitLog() blocks on a GUEST: line.
export class TkGuest {
  readonly name: string;
  readonly logPath: string;
  readonly fifo: string;
  proc: ChildProcess;
  pid = 0;
  private dir: string;

  constructor(info: DisplayInfo, name: string, opts: GuestOpts = {}) {
    this.name = name;
    this.dir = join(RUN_DIR, 'guests');
    mkdirSync(this.dir, { recursive: true });
    this.logPath = join(this.dir, `${name}.log`);
    this.fifo = join(tmpdir(), `wash-tort-${process.pid}-${name}.fifo`);
    if (!existsSync(this.fifo)) execFileSync('mkfifo', [this.fifo]);
    const args = [TK_GUEST, '--ctl', this.fifo, '--log', this.logPath, '--title', opts.title ?? name];
    if (opts.width) args.push('--width', String(opts.width));
    if (opts.height) args.push('--height', String(opts.height));
    this.proc = spawn('python3', args, { env: { ...info.env, ...(opts.env ?? {}) }, stdio: ['ignore', 'ignore', 'pipe'] });
    this.pid = this.proc.pid ?? 0;
    this.proc.stderr?.on('data', (d) => writeFileSync(this.logPath, `STDERR: ${d}`, { flag: 'a' }));
  }

  log(): string {
    try {
      return readFileSync(this.logPath, 'utf8');
    } catch {
      return '';
    }
  }
  cursor(): number {
    return this.log().length;
  }

  // Wait until the guest's own log shows `re` (scanning from `from`).
  async waitLog(re: RegExp, timeoutMs = 10_000, from = 0): Promise<string> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const m = re.exec(this.log().slice(from));
      if (m) return m[0];
      if (Date.now() > deadline) throw new Error(`guest ${this.name}: timed out waiting for ${re}\nlog tail:\n${this.log().slice(-2000)}`);
      await sleep(50);
    }
  }

  async ready(): Promise<void> {
    await this.waitLog(/GUEST: .*ready pid=/, 15_000);
  }

  // One command per open/write/close so the guest's reader never blocks us.
  async send(cmd: string): Promise<void> {
    await new Promise<void>((res, rej) => {
      const w = spawn('sh', ['-c', `printf '%s\\n' "$1" > "$2"`, 'sh', cmd, this.fifo], { stdio: 'ignore' });
      const t = setTimeout(() => { w.kill('SIGKILL'); rej(new Error(`guest ${this.name}: fifo write of ${JSON.stringify(cmd)} hung (guest dead?)`)); }, 5_000);
      w.on('exit', () => { clearTimeout(t); res(); });
    });
  }
  async cmd(cmd: string, timeoutMs = 10_000): Promise<void> {
    const from = this.cursor();
    await this.send(cmd);
    await this.waitLog(new RegExp(`GUEST: .*ack ${escapeRe(cmd)}`), timeoutMs, from);
  }

  count(re: RegExp): number {
    return countMatches(this.log(), re);
  }

  kill(sig: NodeJS.Signals = 'SIGKILL'): void {
    try {
      this.proc.kill(sig);
    } catch {
      /* gone */
    }
  }
  async exited(timeoutMs = 5_000): Promise<boolean> {
    const deadline = Date.now() + timeoutMs;
    while (alive(this.pid) && Date.now() < deadline) await sleep(50);
    return !alive(this.pid);
  }
}

// A plain client (xeyes, xcalc, chromium …) spawned against the compositor.
export function spawnClient(info: DisplayInfo, cmd: string, args: string[] = [], extraEnv: NodeJS.ProcessEnv = {}): ChildProcess {
  const logPath = join(RUN_DIR, 'guests', `${cmd.replace(/[^a-z0-9]/gi, '_')}-${Date.now()}.stderr`);
  mkdirSync(dirname(logPath), { recursive: true });
  const p = spawn(cmd, args, { env: { ...info.env, ...extraEnv }, stdio: ['ignore', 'ignore', 'pipe'] });
  p.stderr?.on('data', (d) => writeFileSync(logPath, d, { flag: 'a' }));
  return p;
}

export function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
export function countMatches(s: string, re: RegExp): number {
  const g = new RegExp(re.source, re.flags.includes('g') ? re.flags : re.flags + 'g');
  return (s.match(g) ?? []).length;
}

// ---- shell-side helpers ---------------------------------------------------

export interface ShellWin { id: number; title: string; x: number; y: number; w: number; h: number; instanceID: string; element: string; focused: boolean; state: string }

// window.wash.windows() returns the shell's WindowInfo (windowID / instanceID);
// normalise to `id` so scenarios read naturally.
export async function shellWindows(page: Page): Promise<ShellWin[]> {
  return page.evaluate(() =>
    ((window as any).wash.windows() as any[]).map((w) => ({ ...w, id: w.windowID })) as ShellWin[],
  );
}
export async function displayWindows(page: Page): Promise<ShellWin[]> {
  return (await shellWindows(page)).filter((w) => w.element === 'wash-app-display');
}
// Wait for a display window whose title matches, return its shell record.
export async function waitDisplayWindow(page: Page, title: RegExp, timeoutMs = 20_000): Promise<ShellWin> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const w = (await displayWindows(page)).find((x) => title.test(x.title));
    if (w) {
      await page.locator(`wash-app-display[data-wash-window="${w.id}"]`).waitFor({ state: 'visible', timeout: timeoutMs });
      return w;
    }
    if (Date.now() > deadline) throw new Error(`no display window titled ${title}; have: ${(await displayWindows(page)).map((x) => x.title).join(', ') || '(none)'}`);
    await sleep(100);
  }
}
export function displayEl(page: Page, id: number): Locator {
  return page.locator(`wash-app-display[data-wash-window="${id}"]`);
}
export async function focusWin(page: Page, id: number): Promise<void> {
  await page.evaluate((w) => (window as any).wash.focusWindow(w), id);
}
export async function resizeWin(page: Page, id: number, w: number, h: number): Promise<void> {
  await page.evaluate(([id, w, h]) => (window as any).wash.resizeWindow(id, w, h), [id, w, h]);
}
export async function closeWin(page: Page, id: number): Promise<void> {
  await page.evaluate((w) => (window as any).wash.closeWindow(w), id);
}
export async function popupOverlays(page: Page): Promise<number> {
  return page.locator('body > canvas[style*="fixed"]').count();
}

// Pixel truth for a display element's canvas (sampled).
export interface CanvasStats { w: number; h: number; uniqueColors: number; nonBlankPct: number; mean: [number, number, number] }
export async function canvasStats(el: Locator): Promise<CanvasStats> {
  return el.evaluate((node) => {
    const c = (node as HTMLElement).querySelector('canvas') as HTMLCanvasElement | null;
    if (!c || !c.width || !c.height) return { w: 0, h: 0, uniqueColors: 0, nonBlankPct: 0, mean: [0, 0, 0] as [number, number, number] };
    const ctx = c.getContext('2d')!;
    const { width: w, height: h } = c;
    const data = ctx.getImageData(0, 0, w, h).data;
    const seen = new Set<number>();
    let nonBlank = 0, sampled = 0, r = 0, g = 0, b = 0;
    const total = w * h;
    const step = Math.max(1, Math.floor(total / 5000));
    for (let i = 0; i < total; i += step) {
      const p = i * 4;
      seen.add((data[p] << 16) | (data[p + 1] << 8) | data[p + 2]);
      if (!(data[p] === 0 && data[p + 1] === 0 && data[p + 2] === 0) && data[p + 3] !== 0) nonBlank++;
      r += data[p]; g += data[p + 1]; b += data[p + 2];
      sampled++;
    }
    return { w, h, uniqueColors: seen.size, nonBlankPct: Math.round((nonBlank / sampled) * 100), mean: [r / sampled, g / sampled, b / sampled] as [number, number, number] };
  });
}
// Mean colour of a small patch (canvas pixel coords) — quadrant checks.
export async function patchMean(el: Locator, x: number, y: number, size = 6): Promise<[number, number, number]> {
  return el.evaluate((node, [x, y, size]) => {
    const c = (node as HTMLElement).querySelector('canvas') as HTMLCanvasElement;
    const d = c.getContext('2d')!.getImageData(x, y, size, size).data;
    let r = 0, g = 0, b = 0;
    const n = size * size;
    for (let i = 0; i < n; i++) { r += d[i * 4]; g += d[i * 4 + 1]; b += d[i * 4 + 2]; }
    return [r / n, g / n, b / n] as [number, number, number];
  }, [x, y, size]);
}
export function near(a: [number, number, number], b: [number, number, number], tol = 40): boolean {
  return Math.abs(a[0] - b[0]) <= tol && Math.abs(a[1] - b[1]) <= tol && Math.abs(a[2] - b[2]) <= tol;
}

// ---- 3. evidence ----------------------------------------------------------

export interface ProcStats { pid: number; alive: boolean; rssKb: number; vmDataKb: number; threads: number; fds: number }
export function procStats(pid: number): ProcStats {
  const out: ProcStats = { pid, alive: alive(pid), rssKb: 0, vmDataKb: 0, threads: 0, fds: 0 };
  if (!out.alive) return out;
  try {
    const st = readFileSync(`/proc/${pid}/status`, 'utf8');
    out.rssKb = Number(/VmRSS:\s+(\d+)/.exec(st)?.[1] ?? 0);
    // VmData catches heap that was allocated but never touched (Go's zeroed
    // large allocations come straight from fresh mmap pages, so a leaked
    // 256 KiB ring buffer that is never written never shows in RSS).
    out.vmDataKb = Number(/VmData:\s+(\d+)/.exec(st)?.[1] ?? 0);
    out.threads = Number(/Threads:\s+(\d+)/.exec(st)?.[1] ?? 0);
    out.fds = readdirSync(`/proc/${pid}/fd`).length;
  } catch {
    /* raced exit */
  }
  return out;
}
// Socket/pipe open-file-descriptions two processes share (same inode = the
// same open file, i.e. an inherited fd). Each is classified from
// /proc/net/unix: a LISTENING socket (SO_ACCEPTCON) or a socket bound to a
// path is a server socket handed down on purpose (wlroots passes the X11
// listen sockets to Xwayland via -listenfd); fds 0-2 in the child are
// stdio (the compositor shares the router's log pipes by design); anything
// else — a CONNECTED stream socket with no path — is a client connection
// the child was never meant to hold (e.g. the router wire socket).
export interface SharedFd { target: string; fdA: string; fdB: string; kind: 'stdio' | 'listening' | 'connected' | 'pipe' }
export function sharedFds(pidA: number, pidB: number): SharedFd[] {
  const targets = (pid: number) => {
    const s = new Map<string, string>();
    try {
      for (const fd of readdirSync(`/proc/${pid}/fd`)) {
        try {
          const t = readlinkSync(`/proc/${pid}/fd/${fd}`);
          if (/^(socket|pipe):/.test(t)) s.set(t, fd);
        } catch { /* closed meanwhile */ }
      }
    } catch { /* gone */ }
    return s;
  };
  const listening = new Set<string>();
  try {
    // Num RefCount Protocol Flags Type St Inode Path
    for (const line of readFileSync('/proc/net/unix', 'utf8').split('\n').slice(1)) {
      const f = line.trim().split(/\s+/);
      if (f.length < 7) continue;
      const flags = parseInt(f[3], 16), inode = f[6], path = f[7];
      if ((flags & 0x10000) || path) listening.add(inode);
    }
  } catch { /* no procfs table */ }
  const a = targets(pidA), b = targets(pidB);
  const shared: SharedFd[] = [];
  for (const [t, fdA] of a) {
    if (!b.has(t)) continue;
    const fdB = b.get(t)!;
    const inode = /\[(\d+)\]/.exec(t)?.[1] ?? '';
    let kind: SharedFd['kind'] = t.startsWith('pipe:') ? 'pipe' : listening.has(inode) ? 'listening' : 'connected';
    if (Number(fdB) <= 2) kind = 'stdio';
    shared.push({ target: t, fdA, fdB, kind });
  }
  return shared;
}
export const fmtShared = (s: SharedFd[]) => s.map((x) => `${x.target} ${x.kind} (fd ${x.fdA} → fd ${x.fdB})`).join('; ') || 'none';
// Every live process whose argv names this router's apps dir, besides the
// router itself: the orphan set after teardown-by-SIGTERM.
export function procsUnder(dir: string, exceptPid = 0): { pid: number; argv: string }[] {
  const out: { pid: number; argv: string }[] = [];
  for (const pid of readdirSync('/proc')) {
    if (!/^\d+$/.test(pid) || Number(pid) === exceptPid || Number(pid) === process.pid) continue;
    try {
      const argv = readFileSync(`/proc/${pid}/cmdline`, 'utf8');
      if (argv.includes(dir)) out.push({ pid: Number(pid), argv: argv.replace(/\0/g, ' ').trim() });
    } catch { /* raced */ }
  }
  return out;
}

// Lines in the router/compositor log that should never appear in a healthy run.
const ERROR_PATTERNS: [string, RegExp][] = [
  ['compositor crash', /wash-display: FATAL|backtrace \(\d+ frames\)/],
  ['compositor exited', /run_compositor returned|wash-display: received SIG/],
  // wlroots' xwm logs an xcb BadWindow (op 18 ChangeProperty / code 3) when a
  // client's window is already gone — benign race noise on every X teardown;
  // counted separately below, not an error.
  // …and "Failed to get window property" when an X client disowns a
  // selection (XSetSelectionOwner None) and xwm's TARGETS query finds no
  // owner window — the next owner's copy still bridges fine.
  ['wlroots error', /\[ERROR\] (?!\[xwayland\/xwm\.c:\d+\] (xcb error: op \d+:\d+, code 3,|Failed to get window property))/],
  ['wlroots xwm BadWindow/no-owner noise (informational)', /\[ERROR\] \[xwayland\/xwm\.c:\d+\] (xcb error: op \d+:\d+, code 3,|Failed to get window property)/],
  ['wire error', /window\.create\.err|channel\.open\.err/],
  ['popup dropped', /no (parent|mapped root) toplevel — dropping/],
  ['unknown win', /win cmd .* for unknown win/],
  ['spawn/attach failure', /attach timeout|disabled .*wash-display/],
  ['protocol error', /protocol error|wl_display_error|Violating/i],
];
export function scanErrors(log: string, includeInformational = false): { kind: string; count: number; sample: string }[] {
  const out: { kind: string; count: number; sample: string }[] = [];
  for (const [kind, re] of ERROR_PATTERNS) {
    if (!includeInformational && /\(informational\)/.test(kind)) continue;
    const g = new RegExp(re.source, re.flags.includes('g') ? re.flags : re.flags + 'g');
    const m = log.match(g);
    if (m && m.length) {
      const line = log.split('\n').find((l) => re.test(l)) ?? m[0];
      out.push({ kind, count: m.length, sample: line.slice(0, 240) });
    }
  }
  return out;
}

export interface Check { name: string; ok: boolean; detail: string }
export interface EvidenceFile { scenario: string; startedAt: string; durationMs: number; metrics: Record<string, unknown>; checks: Check[]; errors: ReturnType<typeof scanErrors>; notes: string[] }

// Evidence collects named checks + metrics for one scenario and writes
// <RUN_DIR>/<scenario>.json (+ .log slice, .png shots) on finish(). Checks
// are SOFT by default (recorded, reported red) so one failed invariant
// doesn't hide the others; call assertAll() at the end to fail the test.
export class Evidence {
  readonly scenario: string;
  private t0 = Date.now();
  private checks: Check[] = [];
  private metrics: Record<string, unknown> = {};
  private notes: string[] = [];
  constructor(scenario: string) {
    this.scenario = scenario;
  }
  metric(k: string, v: unknown): void {
    this.metrics[k] = v;
  }
  note(s: string): void {
    this.notes.push(s);
    console.log(`  [${this.scenario}] ${s}`);
  }
  check(name: string, ok: boolean, detail = ''): boolean {
    this.checks.push({ name, ok, detail });
    console.log(`  [${this.scenario}] ${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ' — ' + detail : ''}`);
    return ok;
  }
  // Growth bound: v may exceed base by at most `pct` percent plus `abs`.
  bounded(name: string, base: number, v: number, pct: number, abs = 0, unit = ''): boolean {
    const limit = base * (1 + pct / 100) + abs;
    return this.check(name, v <= limit, `${base}${unit} → ${v}${unit} (limit ${Math.round(limit)}${unit}, +${pct}%${abs ? `+${abs}` : ''})`);
  }
  async shot(page: Page, tag: string): Promise<void> {
    try {
      await page.screenshot({ path: join(RUN_DIR, `${this.scenario}.${tag}.png`) });
    } catch { /* page gone */ }
  }
  procs(tag: string, info: DisplayInfo, routerPid: number): Record<string, ProcStats> {
    const s = { compositor: procStats(info.compositorPid), xwayland: procStats(info.xwaylandPid), router: procStats(routerPid) };
    this.metric(`procs.${tag}`, s);
    return s;
  }
  finish(router: RouterHandle, extra: Record<string, unknown> = {}): EvidenceFile {
    const log = router.log();
    const errors = scanErrors(log, true);
    const file: EvidenceFile = {
      scenario: this.scenario,
      startedAt: new Date(this.t0).toISOString(),
      durationMs: Date.now() - this.t0,
      metrics: { ...this.metrics, ...extra },
      checks: this.checks,
      errors,
      notes: this.notes,
    };
    writeFileSync(join(RUN_DIR, `${this.scenario}.json`), JSON.stringify(file, null, 2));
    // The log slice that matters, next to the verdicts.
    const lines = log.split('\n').filter((l) => /wash-display|window\.create|window\.destroy|popup|inject|clipboard|frame seq|Xwayland|env\.publish|channel|FATAL|backtrace|spawn/.test(l));
    writeFileSync(join(RUN_DIR, `${this.scenario}.log`), lines.join('\n') + '\n');
    return file;
  }
  // Fail the test if any check is red (after finish(), so evidence is on disk).
  assertAll(): void {
    const red = this.checks.filter((c) => !c.ok);
    expect(red.map((c) => `${c.name}: ${c.detail}`), `${red.length} failed check(s) — see ${RUN_DIR}/${this.scenario}.json`).toEqual([]);
  }
}

// Convenience: the routine health checks every scenario ends with.
export function healthChecks(ev: Evidence, router: RouterHandle, info: DisplayInfo, base: Record<string, ProcStats>, opts: { rssPct?: number; fdAbs?: number; threadAbs?: number } = {}): void {
  const hadX = info.xwaylandPid > 0;
  resolveXwayland(info);
  const now = ev.procs('end', info, router.proc.pid ?? 0);
  ev.check('compositor alive', now.compositor.alive, `pid ${info.compositorPid}`);
  // Xwayland is lazy: a pure-Wayland scenario never starts it, which is fine.
  if (hadX || info.xwaylandPid) ev.check('xwayland alive', now.xwayland.alive, `pid ${info.xwaylandPid}`);
  else ev.note('Xwayland never started in this scenario (no X client) — alive check skipped');
  const noise = scanErrors(router.log(), true).filter((e) => /informational/.test(e.kind));
  if (noise.length) ev.metric('xwmBadWindowNoise', noise[0].count);
  ev.bounded('compositor RSS bounded', base.compositor.rssKb, now.compositor.rssKb, opts.rssPct ?? 25, 8192, 'KB');
  ev.bounded('compositor fds bounded', base.compositor.fds, now.compositor.fds, 0, opts.fdAbs ?? 4);
  ev.bounded('compositor threads bounded', base.compositor.threads, now.compositor.threads, 0, opts.threadAbs ?? 1);
  ev.bounded('router RSS bounded', base.router.rssKb, now.router.rssKb, opts.rssPct ?? 25, 16384, 'KB');
  const errs = scanErrors(router.log());
  ev.check('no error lines in router/compositor log', errs.length === 0, errs.map((e) => `${e.kind}×${e.count}: ${e.sample}`).join(' | '));
}

// After stopping guests: the compositor should have torn every window down.
export async function expectNoDisplayWindows(ev: Evidence, page: Page, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let n = (await displayWindows(page)).length;
  while (n > 0 && Date.now() < deadline) {
    await sleep(100);
    n = (await displayWindows(page)).length;
  }
  ev.check('all display windows torn down', n === 0, `${n} left`);
  const ov = await popupOverlays(page);
  ev.check('no popup overlay canvases left', ov === 0, `${ov} left`);
}
