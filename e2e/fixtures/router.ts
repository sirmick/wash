// Per-test router fixture. Spawns the wash-router binary as a
// subprocess on a unique loopback port, captures its stderr into an
// in-memory buffer, exposes the URL + the log buffer to tests, and
// tears the process down on test end.
//
// Tests use this via the typed Playwright fixture exported below.

import { test as base, expect } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import { mkdtempSync, copyFileSync, existsSync, chmodSync, writeFileSync, mkdirSync, symlinkSync, rmSync, readdirSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer, createConnection } from 'node:net';

// Repo root: e2e/fixtures/router.ts → up two.
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..');
const ROUTER_BIN = join(REPO_ROOT, 'out', 'wash-router');
const SESSION_BIN = join(REPO_ROOT, 'out', 'wash-session');
const ABOUT_BIN = join(REPO_ROOT, 'out', 'wash-about');
const TEST_BIN = join(REPO_ROOT, 'out', 'wash-test');
const TERM_BIN = join(REPO_ROOT, 'out', 'wash-term');
const LAUNCH_BIN = join(REPO_ROOT, 'out', 'wash-launch');
const FM_BIN = join(REPO_ROOT, 'out', 'wash-fm');
const BULK_BIN = join(REPO_ROOT, 'out', 'wash-bulk');
const EDIT_BIN = join(REPO_ROOT, 'out', 'wash-edit');
const PRIV_BIN = join(REPO_ROOT, 'out', 'wash-priv');
const JOURNAL_BIN = join(REPO_ROOT, 'out', 'wash-journal');
const SETTINGS_BIN = join(REPO_ROOT, 'out', 'wash-settings');
const TOP_BIN = join(REPO_ROOT, 'out', 'wash-top');
const DISKS_BIN = join(REPO_ROOT, 'out', 'wash-disks');
const SYSLOGS_BIN = join(REPO_ROOT, 'out', 'wash-syslogs');
const SERVICES_BIN = join(REPO_ROOT, 'out', 'wash-services');
const PACKAGES_BIN = join(REPO_ROOT, 'out', 'wash-packages');
const NOTIFY_BIN = join(REPO_ROOT, 'out', 'wash-notify');
const VSCODE_BIN = join(REPO_ROOT, 'out', 'wash-vscode');
const VSCODE_WB_BIN = join(REPO_ROOT, 'out', 'wash-vscode-workbench');
const DISPLAY_BIN = join(REPO_ROOT, 'out', 'wash-display');
const NET_BIN = join(REPO_ROOT, 'out', 'wash-net');
const NETD_BIN = join(REPO_ROOT, 'out', 'wash-netd');
const WASHAMP_BIN = join(REPO_ROOT, 'out', 'wash-washamp');
const MUSIC_BIN = join(REPO_ROOT, 'out', 'wash-music');
const AUDIO_BIN = join(REPO_ROOT, 'out', 'wash-audio');
const FAKESUDO_BIN = join(REPO_ROOT, 'out', 'wash-priv-fakesudo');
export const SUDO_BIN = join(REPO_ROOT, 'out', 'wash-sudo');

// wash-display is the native (C++/CMake/wlroots) compositor — not built by
// build.sh and not present on a dep-less checkout or CI. Specs that need
// the live compositor call this in a beforeEach and test.skip() on a
// non-null reason, so they're an opt-in capstone (mirrors vmSkipReason).
export function displaySkipReason(): string | null {
  if (!existsSync(DISPLAY_BIN)) {
    return `${DISPLAY_BIN} missing (cmake --build wash-display/build; needs libwlroots-dev)`;
  }
  return null;
}

export interface RouterHandle {
  url: string;
  /** absolute path to wash-launch on this host (for tests that invoke it from a shell) */
  launchBin: string;
  /** the per-test apps dir (where the staged binaries live). Tests that
   *  exec apps directly (e.g. terminal-attach tests) must use binaries
   *  from here so /proc/<pid>/exe matches the registered path. */
  appsDir: string;
  /** the control socket path this router was started with */
  controlSocket: string;
  /** the directory POST /screenshot writes to */
  screenshotDir: string;
  /** per-test fm sandbox root. Empty when fmRoot wasn't requested. */
  fmRoot: string;
  /** per-test XDG_CONFIG_HOME (wash configs live under <here>/wash/).
   *  Empty when xdgConfig wasn't requested. */
  xdgConfigHome: string;
  /**
   * Path to the fakesudo audit log when fakesudo:true was set;
   * empty otherwise. Tests read this to assert which targets
   * wash-priv invoked, with what argv, after which password.
   */
  fakesudoLog: string;
  log(): string;
  waitForLog(pattern: RegExp, timeout?: number): Promise<string>;
  /**
   * Round-trip a single JSON request over the control socket. Used
   * by BE-driven tests to launch/spawn apps and drive APP_MSGs into
   * them without going through the browser. The reply is whatever
   * the router writes back as one JSON line — caller inspects "t".
   */
  controlRequest(req: Record<string, unknown>, timeoutMs?: number): Promise<Record<string, unknown>>;
  /**
   * Convenience: send an APP_MSG to instanceID and (if data has a
   * string `id` field) wait for the matching BE reply. Returns the
   * raw reply payload; caller inspects .kind. Throws on router-side
   * errors (timeout, unknown instance).
   */
  sendAppMsg(instanceID: string, data: Record<string, unknown>, timeoutMs?: number): Promise<Record<string, unknown>>;
  proc: ChildProcess;
}

export interface RouterOptions {
  /** kiosk mode: --no-session + --initial-app=<appID>. */
  kiosk?: string;
  /** include these binaries in the apps dir; defaults to all five. */
  apps?: ('session' | 'about' | 'test' | 'term' | 'fm' | 'bulk' | 'priv' | 'journal' | 'settings' | 'top' | 'disks' | 'syslogs' | 'services' | 'packages' | 'edit' | 'vscode' | 'display' | 'net' | 'netd')[];
  /** include manifest.hidden apps in the catalog. */
  showHidden?: boolean;
  /** extra wash-router args. */
  extraArgs?: string[];
  /**
   * If true, wire the wash-priv fakesudo binary into wash-priv's env
   * via WASH_PRIV_SUDO_BIN, and create a temp FAKESUDO_LOG file the
   * test can read back. Implies apps:['priv', ...]. The fakesudo
   * accepts FAKESUDO_PASSWORD (default "test123") and otherwise
   * exec's whatever target was passed after `--`.
   */
  fakesudo?: boolean;
  /** override the password fakesudo accepts. Default "test123". */
  fakesudoPassword?: string;
  /**
   * If true, create a per-test sandbox dir, seed it (via fmSeed if
   * provided, otherwise leave empty), and pass it to the spawned
   * router as WASH_FM_ROOT. The fm BE then refuses any path outside
   * the dir, so a wayward test can't touch the user's filesystem.
   */
  fmRoot?: boolean;
  /**
   * Optional seeder for the fm sandbox. Called with the absolute
   * path of the just-created sandbox dir; populate it with the
   * fixture tree your test needs. Implies fmRoot:true.
   */
  fmSeed?: (root: string) => void;
  /**
   * If true, point XDG_CONFIG_HOME at a per-test tmpdir so wash-settings
   * (and the wash-session config watcher) read/write into an isolated
   * tree. Without this, settings tests would clobber ~/.config/wash on
   * the test runner.
   */
  xdgConfig?: boolean;
  /**
   * Extra env vars merged into the router process's env after the
   * other options have been applied. Useful for tests that need to
   * inject mock binaries into PATH (e.g. wash-services with a stub
   * systemctl) or override SDK-side env that the BE reads at start.
   * Empty-string values delete the var, matching the shell convention.
   */
  extraEnv?: Record<string, string>;
}

async function freePort(): Promise<number> {
  return new Promise((resolveP, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (!addr || typeof addr === 'string') {
        reject(new Error('no address'));
        return;
      }
      const port = addr.port;
      srv.close(() => resolveP(port));
    });
  });
}

function stageApps(binaries: string[]): string {
  const dir = mkdtempSync(join(tmpdir(), 'wash-e2e-apps-'));
  // Multi-call mode (WASH_E2E_MULTICALL=1): copy out/wash once and
  // create a symlink per requested app pointing at it. Exercises the
  // busybox-style layout end-to-end through the same e2e suite as
  // the standalone layout — same router, same Playwright tests,
  // different binary topology.
  if (process.env.WASH_E2E_MULTICALL === '1') {
    const washBin = join(REPO_ROOT, 'out', 'wash');
    if (!existsSync(washBin)) {
      throw new Error(`WASH_E2E_MULTICALL=1 set but out/wash missing — run \`make TEST_APP=1 out/wash\` first`);
    }
    const washDest = join(dir, 'wash');
    copyFileSync(washBin, washDest);
    chmodSync(washDest, 0o755);
    for (const bin of binaries) {
      const name = bin.split('/').pop()!;
      // wash-sudo and wash-priv-fakesudo stay as real separate
      // binaries — they're never built into the multi-call wash. Copy
      // them in as-is so the priv chain still works.
      if (name === 'wash-sudo' || name === 'wash-priv-fakesudo') {
        const dest = join(dir, name);
        copyFileSync(bin, dest);
        chmodSync(dest, 0o755);
        continue;
      }
      symlinkSync('wash', join(dir, name));
    }
    return dir;
  }
  // Default: each app is its own separate binary (today's layout).
  for (const bin of binaries) {
    const dest = join(dir, bin.split('/').pop()!);
    copyFileSync(bin, dest);
    chmodSync(dest, 0o755);
  }
  return dir;
}

export async function startRouter(opts: RouterOptions = {}): Promise<RouterHandle> {
  for (const b of [ROUTER_BIN, SESSION_BIN, ABOUT_BIN, TEST_BIN, TERM_BIN, FM_BIN, BULK_BIN, EDIT_BIN, LAUNCH_BIN]) {
    if (!existsSync(b)) {
      throw new Error(`missing binary: ${b}\n(make TEST_APP=1 from the repo root)`);
    }
  }
  const wanted = opts.apps ?? ['session', 'about', 'test', 'term', 'fm', 'bulk', 'edit', 'notify'];
  // fakesudo:true implies wash-priv in the apps dir — the BE is what
  // reads WASH_PRIV_SUDO_BIN, so without it the test wires the env
  // var into a process that never receives it.
  if (opts.fakesudo && !wanted.includes('priv')) {
    wanted.push('priv');
  }
  const bins: string[] = [];
  if (wanted.includes('session')) bins.push(SESSION_BIN);
  if (wanted.includes('about')) bins.push(ABOUT_BIN);
  if (wanted.includes('test')) bins.push(TEST_BIN);
  if (wanted.includes('term')) bins.push(TERM_BIN);
  if (wanted.includes('fm')) bins.push(FM_BIN);
  if (wanted.includes('bulk')) bins.push(BULK_BIN);
  if (wanted.includes('edit')) bins.push(EDIT_BIN);
  if (wanted.includes('display')) {
    if (!existsSync(DISPLAY_BIN)) {
      throw new Error(`missing wash-display: ${DISPLAY_BIN} (cmake --build wash-display/build)`);
    }
    bins.push(DISPLAY_BIN);
  }
  if (wanted.includes('priv')) {
    if (!existsSync(PRIV_BIN)) {
      throw new Error(`missing wash-priv: ${PRIV_BIN}`);
    }
    bins.push(PRIV_BIN);
  }
  if (wanted.includes('journal')) {
    if (!existsSync(JOURNAL_BIN)) {
      throw new Error(`missing wash-journal: ${JOURNAL_BIN}`);
    }
    bins.push(JOURNAL_BIN);
  }
  if (wanted.includes('settings')) {
    if (!existsSync(SETTINGS_BIN)) {
      throw new Error(`missing wash-settings: ${SETTINGS_BIN}`);
    }
    bins.push(SETTINGS_BIN);
  }
  if (wanted.includes('top')) {
    if (!existsSync(TOP_BIN)) {
      throw new Error(`missing wash-top: ${TOP_BIN}`);
    }
    bins.push(TOP_BIN);
  }
  if (wanted.includes('disks')) {
    if (!existsSync(DISKS_BIN)) {
      throw new Error(`missing wash-disks: ${DISKS_BIN}`);
    }
    bins.push(DISKS_BIN);
  }
  if (wanted.includes('syslogs')) {
    if (!existsSync(SYSLOGS_BIN)) {
      throw new Error(`missing wash-syslogs: ${SYSLOGS_BIN}`);
    }
    bins.push(SYSLOGS_BIN);
  }
  if (wanted.includes('services')) {
    if (!existsSync(SERVICES_BIN)) {
      throw new Error(`missing wash-services: ${SERVICES_BIN}`);
    }
    bins.push(SERVICES_BIN);
  }
  if (wanted.includes('packages')) {
    if (!existsSync(PACKAGES_BIN)) {
      throw new Error(`missing wash-packages: ${PACKAGES_BIN}`);
    }
    bins.push(PACKAGES_BIN);
  }
  if (wanted.includes('notify')) {
    if (!existsSync(NOTIFY_BIN)) {
      throw new Error(`missing wash-notify: ${NOTIFY_BIN}`);
    }
    bins.push(NOTIFY_BIN);
  }
  // The Network window app + its privileged backing service. netd is a
  // background singleton (Surface=background), auto-spawned by the router once
  // its binary is in the apps dir — net relays validate/apply to it cross-app.
  // netd defaults to the FAKE applier (real NM only when WASH_NETD_BACKEND=nm),
  // so this whole stack runs deterministically on the host — no VM. The in-VM
  // real-NM capstone is net-vm-gate.spec.ts.
  if (wanted.includes('net')) {
    if (!existsSync(NET_BIN)) {
      throw new Error(`missing wash-net: ${NET_BIN}`);
    }
    bins.push(NET_BIN);
  }
  if (wanted.includes('netd')) {
    if (!existsSync(NETD_BIN)) {
      throw new Error(`missing wash-netd: ${NETD_BIN}`);
    }
    bins.push(NETD_BIN);
  }
  if (wanted.includes('washamp')) {
    if (!existsSync(WASHAMP_BIN)) {
      throw new Error(`missing wash-washamp: ${WASHAMP_BIN}`);
    }
    bins.push(WASHAMP_BIN);
  }
  if (wanted.includes('music')) {
    if (!existsSync(MUSIC_BIN)) {
      throw new Error(`missing wash-music: ${MUSIC_BIN}`);
    }
    bins.push(MUSIC_BIN);
  }
  if (wanted.includes('audio')) {
    if (!existsSync(AUDIO_BIN)) {
      throw new Error(`missing wash-audio: ${AUDIO_BIN}`);
    }
    bins.push(AUDIO_BIN);
  }
  if (wanted.includes('vscode')) {
    // The manager window (owns code-server) + the hidden workbench window.
    for (const b of [VSCODE_BIN, VSCODE_WB_BIN]) {
      if (!existsSync(b)) {
        throw new Error(`missing vscode binary: ${b}`);
      }
      bins.push(b);
    }
  }
  const appsDir = stageApps(bins);
  // wash-priv claims a reservedID (com.wash.priv) which the registry
  // refuses from a non-root-owned binary by default. The e2e dir is
  // owned by the test runner; opt it into the trusted list so the
  // priv binary registers correctly.
  // netd (com.wash.netd) claims a reserved id too, so it needs the same trust.
  const needsTrust = wanted.includes('priv') || wanted.includes('netd');
  const trustForPriv = needsTrust ? appsDir : '';

  const port = await freePort();
  // Each test gets its own control-socket path so concurrent test
  // runs don't trample each other.
  const controlSocket = join(appsDir, 'control.sock');
  const screenshotDir = join(appsDir, 'screenshots');
  const args = [
    '--listen',
    `127.0.0.1:${port}`,
    '--apps-dir',
    appsDir,
    '--control-socket',
    controlSocket,
    '--screenshot-dir',
    screenshotDir,
  ];
  if (opts.kiosk) {
    args.push('--no-session', `--initial-app=${opts.kiosk}`);
  }
  if (opts.showHidden) {
    args.push('--show-hidden');
  }
  if (opts.extraArgs) args.push(...opts.extraArgs);

  // Optional fm sandbox: a per-test tmpdir gets created (and
  // seeded) and passed via WASH_FM_ROOT. The fm BE then refuses
  // any path outside this dir, so an off-by-one test path can't
  // touch the user's files.
  const env: NodeJS.ProcessEnv = { ...process.env };
  let fmRoot = '';
  if (opts.fmRoot || opts.fmSeed) {
    fmRoot = mkdtempSync(join(tmpdir(), 'wash-e2e-fm-'));
    if (opts.fmSeed) opts.fmSeed(fmRoot);
    env.WASH_FM_ROOT = fmRoot;
  }
  if (trustForPriv) {
    env.WASH_TRUSTED_APPS_DIRS = trustForPriv;
  }
  // Force netd onto its fake applier (eth0..eth3, deterministic). Since
  // netd defaults to autodetecting a live backend, on a dev host with
  // NetworkManager/networkd running it would otherwise bind the real
  // backend and list the box's real NICs (enp3s0, …) — breaking the
  // host-side net tests that assert on eth0. (The real-backend path is
  // covered separately by net-vm-gate.spec.ts, which runs the in-guest
  // netd inside a real microvm via the vm fixture, not this one.) A test
  // can still override via extraEnv, which is applied last.
  if (wanted.includes('netd')) {
    env.WASH_NETD_BACKEND = 'fake';
  }
  // Isolate the user's real ~/.config/wash. wash-settings.write()
  // overwrites desktop.json; without this every settings spec would
  // trash the developer's chrome between runs.
  let xdgConfigHome = '';
  if (opts.xdgConfig) {
    xdgConfigHome = mkdtempSync(join(tmpdir(), 'wash-e2e-xdg-'));
    env.XDG_CONFIG_HOME = xdgConfigHome;
  }
  // fakesudo wiring: WASH_PRIV_SUDO_BIN is read by wash-priv at
  // startup; FAKESUDO_LOG is read by fakesudo on every invocation
  // so tests can prove which targets it was asked to exec. We add
  // them to the router's env, which is inherited by every app the
  // router spawns (including wash-priv).
  let fakesudoLog = '';
  if (opts.fakesudo) {
    if (!existsSync(FAKESUDO_BIN)) {
      throw new Error(`missing wash-priv-fakesudo: ${FAKESUDO_BIN}\n(make out/wash-priv-fakesudo)`);
    }
    fakesudoLog = join(appsDir, 'fakesudo.log');
    env.WASH_PRIV_SUDO_BIN = FAKESUDO_BIN;
    env.FAKESUDO_LOG = fakesudoLog;
    if (opts.fakesudoPassword) env.FAKESUDO_PASSWORD = opts.fakesudoPassword;
    // Disable idle timer so e2e doesn't race the password expiry.
    env.WASH_PRIV_IDLE = '0';
    // Point the audit log somewhere bounded.
    env.WASH_PRIV_AUDIT_PATH = join(appsDir, 'priv-audit.log');
  }
  // extraEnv applied last so tests can override anything the fixture
  // set above (rare, but useful — e.g. point WASH_PRIV_IDLE back on
  // for a timeout test). Empty string deletes the var.
  if (opts.extraEnv) {
    for (const [k, v] of Object.entries(opts.extraEnv)) {
      if (v === '') delete env[k];
      else env[k] = v;
    }
  }

  const proc = spawn(ROUTER_BIN, args, { stdio: ['ignore', 'pipe', 'pipe'], env });
  let logBuf = '';
  proc.stdout.on('data', (chunk: Buffer) => {
    logBuf += chunk.toString('utf8');
  });
  proc.stderr.on('data', (chunk: Buffer) => {
    logBuf += chunk.toString('utf8');
  });
  const exitPromise: Promise<{ code: number | null; signal: NodeJS.Signals | null }> = new Promise((res) => {
    proc.once('exit', (code, signal) => res({ code, signal }));
  });

  // Wait for the "listening on" line, or for the process to die.
  await Promise.race([
    waitForRegex(() => logBuf, /listening on /, 5_000),
    exitPromise.then((r) => {
      throw new Error(`wash-router exited before listening: code=${r.code} signal=${r.signal}\n${logBuf}`);
    }),
  ]);

  const handle: RouterHandle = {
    url: `http://127.0.0.1:${port}/`,
    launchBin: LAUNCH_BIN,
    appsDir,
    controlSocket,
    screenshotDir,
    fmRoot,
    xdgConfigHome,
    fakesudoLog,
    log: () => logBuf,
    waitForLog: (re, timeout = 5_000) => waitForRegex(() => logBuf, re, timeout),
    controlRequest: (req, timeoutMs = 5_000) => controlRoundtrip(controlSocket, req, timeoutMs),
    async sendAppMsg(instanceID, data, timeoutMs = 5_000) {
      const req: Record<string, unknown> = {
        t: 'msg',
        instance_id: instanceID,
        data,
      };
      // If the caller tagged the data with an id, ask the router
      // to wait for the matching BE reply and hand it back. This
      // is how BE-only tests assert "the BE actually saw and
      // responded to my request" without going through a browser.
      if (typeof data.id === 'string' && data.id !== '') {
        req.await_id = data.id;
        req.timeout_ms = timeoutMs;
      }
      const resp = await controlRoundtrip(controlSocket, req, timeoutMs + 1_000);
      if (resp.t === 'error') {
        throw new Error(`sendAppMsg: ${resp.code}: ${resp.msg}`);
      }
      if (resp.t === 'msg.ok') {
        return {};
      }
      if (resp.t === 'msg.reply') {
        return (resp.data ?? {}) as Record<string, unknown>;
      }
      throw new Error(`sendAppMsg: unexpected response t=${resp.t}`);
    },
    proc,
  };
  return handle;
}

// controlRoundtrip dials the control socket, writes one JSON
// request as a line, reads one JSON response line, and returns it
// parsed. Helper used by RouterHandle.{controlRequest,sendAppMsg}.
function controlRoundtrip(
  socketPath: string,
  req: Record<string, unknown>,
  timeoutMs: number,
): Promise<Record<string, unknown>> {
  return new Promise((resolveP, rejectP) => {
    const conn = createConnection(socketPath);
    let buf = '';
    let settled = false;
    const finish = (err: Error | null, payload?: Record<string, unknown>) => {
      if (settled) return;
      settled = true;
      try { conn.destroy(); } catch { /* ignore */ }
      if (err) rejectP(err);
      else resolveP(payload!);
    };
    const timer = setTimeout(() => finish(new Error(`control socket timeout after ${timeoutMs}ms`)), timeoutMs);
    conn.once('connect', () => {
      conn.write(JSON.stringify(req) + '\n');
    });
    conn.on('data', (chunk: Buffer) => {
      buf += chunk.toString('utf8');
      const nl = buf.indexOf('\n');
      if (nl < 0) return;
      const line = buf.slice(0, nl);
      try {
        clearTimeout(timer);
        finish(null, JSON.parse(line));
      } catch (err) {
        clearTimeout(timer);
        finish(err instanceof Error ? err : new Error(String(err)));
      }
    });
    conn.on('error', (err) => {
      clearTimeout(timer);
      finish(err);
    });
    conn.on('end', () => {
      // The router writes one line and closes — if we haven't
      // settled by now it's because the line lacked a \n.
      if (!settled) {
        clearTimeout(timer);
        finish(new Error('control socket closed without complete line'));
      }
    });
  });
}

/**
 * seedSimpleTree — a canned fmSeed populating a useful starter tree
 * for fm tests:
 *
 *   <root>/
 *     hello.txt           "hello world\n"
 *     binary.bin          \x00\x01\x02\x03\x00\x04
 *     docs/
 *       readme.md         "# readme\n"
 *
 * Tests that need different shapes write their own seeder; this
 * exists so the common case is one line.
 */
export function seedSimpleTree(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  writeFileSync(join(root, 'binary.bin'), Buffer.from([0, 1, 2, 3, 0, 4]));
  mkdirSync(join(root, 'docs'));
  writeFileSync(join(root, 'docs', 'readme.md'), '# readme\n');
}

async function waitForRegex(read: () => string, re: RegExp, timeout: number): Promise<string> {
  const deadline = Date.now() + timeout;
  for (;;) {
    const m = read().match(re);
    if (m) return m[0];
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${re}\nlog so far:\n${read()}`);
    }
    await new Promise((r) => setTimeout(r, 50));
  }
}

// Kill any process whose argv still references this test's per-test apps
// dir. Killing the router *should* take its children with it, but the
// wash-display compositor (a background singleton, auto-spawned on shell
// connect) survives — so display-spec runs otherwise leak a wash-display
// per test, and a session's worth piles up until the live-compositor query
// in settings.spec gets confused (the compositor flavor of
// feedback_e2e_orphan_accumulation). Sweeping by argv catches it and any
// other straggler under the dir. Best-effort; SIGKILL since we're already
// tearing the router down.
function killProcsUnder(dir: string): void {
  let pids: string[];
  try {
    pids = readdirSync('/proc');
  } catch {
    return;
  }
  for (const pid of pids) {
    if (!/^\d+$/.test(pid)) continue;
    const n = parseInt(pid, 10);
    if (n === process.pid) continue;
    let argv = '';
    try {
      argv = readFileSync(`/proc/${pid}/cmdline`, 'utf8'); // NUL-separated; substring match is fine
    } catch {
      continue; // raced exit / not ours
    }
    if (argv.includes(dir)) {
      try {
        process.kill(n, 'SIGKILL');
      } catch {
        /* already gone */
      }
    }
  }
}

export async function stopRouter(h: RouterHandle): Promise<void> {
  h.proc.kill('SIGTERM');
  await new Promise<void>((resolveP) => {
    const t = setTimeout(() => {
      try {
        h.proc.kill('SIGKILL');
      } catch {
        /* ignore */
      }
      resolveP();
    }, 1_500);
    h.proc.once('exit', () => {
      clearTimeout(t);
      resolveP();
    });
  });
  // Reap any child the router left behind (notably the wash-display
  // compositor) before we rm the dir its argv points at.
  if (h.appsDir) killProcsUnder(h.appsDir);
  // Remove the per-test temp dirs so /tmp doesn't accumulate. The
  // staged apps dir is a full copy of the binary set; thousands of
  // runs otherwise pile up to hundreds of GB. force:true ignores a
  // still-dying child's open files.
  for (const d of [h.appsDir, h.fmRoot, h.xdgConfigHome]) {
    if (d) {
      try {
        rmSync(d, { recursive: true, force: true });
      } catch {
        /* best-effort */
      }
    }
  }
}

// Typed fixtures: any test can pull a `router` from the function args
// and it'll be auto-spawned/torn down. Defaults to chrome mode with
// all three apps. Override via test.use({ kiosk: 'com.wash.test' }).
type Fixtures = {
  routerOpts: RouterOptions;
  router: RouterHandle;
};

export const test = base.extend<Fixtures>({
  routerOpts: [{}, { option: true }],
  router: async ({ routerOpts }, use, testInfo) => {
    const h = await startRouter(routerOpts);
    try {
      await use(h);
    } finally {
      // On test failure, attach the router's stderr+stdout so
      // post-mortem debugging doesn't require re-running with
      // bespoke instrumentation.
      if (testInfo.status !== testInfo.expectedStatus) {
        const log = h.log();
        if (log) {
          await testInfo.attach('router.log', {
            contentType: 'text/plain',
            body: log,
          });
        }
      }
      await stopRouter(h);
    }
  },
});

export { expect };
