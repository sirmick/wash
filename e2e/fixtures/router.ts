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
// Binary layout: the default/shipped MULTICALL image (wash + wash-* symlinks)
// lives directly in out/; the standalone per-app real ELFs live under
// out/singlecall/. Point app binaries at whichever layout this run exercises so
// `./test.sh --multicall` (no standalone build) resolves them too.
const MULTICALL = process.env.WASH_E2E_MULTICALL === '1';
const BIN_DIR = MULTICALL ? join(REPO_ROOT, 'out') : join(REPO_ROOT, 'out', 'singlecall');
// The always-real binaries are never folded into the dispatcher and never become
// symlinks — they live in out/ in BOTH layouts (the native C++ compositor, plus
// wash-sudo / wash-priv-fakesudo / wash-login). Everything else resolves under
// BIN_DIR (out/ for multicall, out/singlecall/ for standalone).
const ALWAYS_OUT = new Set(['wash-display', 'wash-sudo', 'wash-priv-fakesudo', 'wash-login']);
const binPath = (name: string): string =>
  ALWAYS_OUT.has(name) ? join(REPO_ROOT, 'out', name) : join(BIN_DIR, name);

// Single source of truth: every app a test can request → the wash-* binaries
// staged into its apps dir when requested. Adding an app is ONE line here —
// the AppName type, the existence checks, and staging all derive from it.
// (vscode also pulls its hidden workbench window; display is the compositor.)
const APP_BINS = {
  session: ['wash-session'], about: ['wash-about'], test: ['wash-test'],
  term: ['wash-term'], fm: ['wash-fm'], bulk: ['wash-bulk'], edit: ['wash-edit'],
  notify: ['wash-notify'], priv: ['wash-priv'], journal: ['wash-journal'],
  settings: ['wash-settings'], top: ['wash-top'], disks: ['wash-disks'],
  syslogs: ['wash-syslogs'], services: ['wash-services'], packages: ['wash-packages'],
  net: ['wash-net'], netd: ['wash-netd'], washamp: ['wash-washamp'],
  music: ['wash-music'], radio: ['wash-radio'], audio: ['wash-audio'],
  connect: ['wash-connect'], remote: ['wash-remote'],
  imageview: ['wash-imageview'],
  agentd: ['wash-agentd'], ai: ['wash-ai'], hostgw: ['wash-hostgw'],
  vscode: ['wash-vscode', 'wash-vscode-workbench'],
  display: ['wash-display'],
} satisfies Record<string, readonly string[]>;
type AppName = keyof typeof APP_BINS;

// Binaries every router needs (the dispatcher + the default app set + the
// launch CLI), checked up front so a missing build fails with a clear error.
// wash-fswatch is the shared filesystem-watch service: fm/edit/filepicker/
// settings all relay watch to com.wash.fswatch, which the router auto-spawns on
// first reference — so it must be staged in every router or watching is dead.
const REQUIRED = ['wash-router', 'wash-session', 'wash-about', 'wash-test',
  'wash-term', 'wash-fm', 'wash-bulk', 'wash-edit', 'wash-launch', 'wash-fswatch'];

// Binaries referenced directly (not via the apps table): the spawn target, the
// launch CLI, the compositor skip-check, fakesudo wiring, and the exported
// sudo path used by priv.spec.
const ROUTER_BIN = binPath('wash-router');
const LAUNCH_BIN = binPath('wash-launch');
const DISPLAY_BIN = binPath('wash-display');
const FAKESUDO_BIN = binPath('wash-priv-fakesudo');
export const SUDO_BIN = binPath('wash-sudo');

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
   * per-test XDG_STATE_HOME. ALWAYS set, unlike xdgConfigHome — agentd
   * writes agent-sessions.json and a full transcript per session under
   * <here>/wash/, so without this every agent spec would append to the
   * developer's real history. It also made specs contaminate each OTHER:
   * the acp-fake uses a fixed session id, so a second run reopened the
   * first run's transcript and saw its lines twice.
   */
  xdgStateHome: string;
  /**
   * Path to the fakesudo audit log when fakesudo:true was set;
   * empty otherwise. Tests read this to assert which targets
   * wash-priv invoked, with what argv, after which password.
   */
  fakesudoLog: string;
  log(): string;
  /**
   * Wait for `pattern` to appear in the router's captured output.
   *
   * Matching starts at `from` (default 0 — the whole log). Pass logCursor()
   * when the line you're waiting for is one a router emits REPEATEDLY, or the
   * wait is satisfied instantly by an earlier occurrence and you race whatever
   * you were trying to sequence behind it (docs/TEST_FLAKES.md A10 — this is
   * how the display specs launched terminals before DISPLAY was published).
   */
  waitForLog(pattern: RegExp, timeout?: number, from?: number): Promise<string>;
  /** Current length of the captured log; pass to waitForLog's `from`. */
  logCursor(): number;
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
  /** apps to stage into the apps dir; see APP_BINS. Defaults to the core set. */
  apps?: AppName[];
  /** include manifest.hidden apps in the catalog. */
  showHidden?: boolean;
  /** extra wash-router args. */
  extraArgs?: string[];
  /**
   * Pin the listen port instead of grabbing a fresh ephemeral one. Used by
   * the reconnect spec to restart a router on the SAME url a browser is
   * already pointed at, so its FE reconnect loop re-dials the replacement.
   */
  port?: number;
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

function stageApps(paths: string[]): string {
  const dir = mkdtempSync(join(tmpdir(), 'wash-e2e-apps-'));
  // Dedupe by basename: infrastructure binaries (fswatch, agentd,
  // agent-hook) are staged unconditionally AND may be named by a test's
  // apps list, and staging one twice used to fail with an EEXIST from
  // symlinkSync — an opaque fixture error for a harmless duplicate.
  const seen = new Set<string>();
  const binaries = paths.filter((p) => {
    const name = p.split('/').pop()!;
    if (seen.has(name)) return false;
    seen.add(name);
    return true;
  });
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
      // wash-sudo, wash-priv-fakesudo, and wash-display stay as real
      // separate binaries — none are built into the multi-call wash
      // (wash-display is the native C++ compositor). Copy them in as-is;
      // symlinking them to `wash` would dispatch the Go multicall instead,
      // so the priv chain / compositor would never launch.
      if (name === 'wash-sudo' || name === 'wash-priv-fakesudo' || name === 'wash-display') {
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
  for (const name of REQUIRED) {
    const p = binPath(name);
    if (!existsSync(p)) {
      throw new Error(`missing binary: ${p}\n(make TEST_APP=1 from the repo root)`);
    }
  }
  const wanted: AppName[] = opts.apps ?? ['session', 'about', 'test', 'term', 'fm', 'bulk', 'edit', 'notify'];
  // fakesudo:true implies wash-priv in the apps dir — the BE is what
  // reads WASH_PRIV_SUDO_BIN, so without it the test wires the env
  // var into a process that never receives it.
  if (opts.fakesudo && !wanted.includes('priv')) {
    wanted.push('priv');
  }
  // Stage every requested app's binaries (deduped — a default-set app the
  // caller also names explicitly shouldn't be staged twice). netd is a
  // background singleton the router auto-spawns once its binary is present;
  // net relays validate/apply to it cross-app (fake applier on the host unless
  // WASH_NETD_BACKEND=nm — the in-VM real-NM capstone is net-vm-gate.spec.ts).
  const bins: string[] = [];
  for (const app of new Set(wanted)) {
    for (const name of APP_BINS[app]) {
      const p = binPath(name);
      if (!existsSync(p)) {
        throw new Error(`missing ${name} (for app "${app}"): ${p}`);
      }
      bins.push(p);
    }
  }
  // Always stage the shared filesystem-watch service: fm/edit/filepicker/
  // settings relay watch to com.wash.fswatch, which the router auto-spawns on
  // first reference — so its binary must be present in every router's apps dir,
  // independent of which apps a test requested.
  bins.push(binPath('wash-fswatch'));
  // wash-agentd is the coding-agent roster singleton: wash-term addresses
  // it by app id, so the router spawns it the first time any terminal sees
  // an agent. Staged everywhere for the same reason as wash-fswatch — a
  // missing binary would silently drop every roster event.
  bins.push(binPath('wash-agentd'));
  // wash-hostgw is the host-awareness gateway (docs/SIDEBAR.md M1): the shell
  // subscribes to it by app id on every attach, so the router spawns it on
  // first reference. Staged everywhere for the same reason as the two above —
  // and specifically so a router that was never asked for it doesn't answer
  // the shell's subscribe with an "app_id not registered" log line that reads
  // like a failure in whatever spec happens to be running.
  bins.push(binPath('wash-hostgw'));
  const appsDir = stageApps(bins);
  // wash-priv claims a reservedID (com.wash.priv) which the registry
  // refuses from a non-root-owned binary by default. The e2e dir is
  // owned by the test runner; opt it into the trusted list so the
  // priv binary registers correctly.
  // netd (com.wash.netd) claims a reserved id too, so it needs the same trust.
  const needsTrust = wanted.includes('priv') || wanted.includes('netd');
  const trustForPriv = needsTrust ? appsDir : '';

  const port = opts.port ?? await freePort();
  // Each test gets its own control-socket path so concurrent test
  // runs don't trample each other.
  const controlSocket = join(appsDir, 'control.sock');
  const screenshotDir = join(appsDir, 'screenshots');
  const args = [
    '--listen',
    `127.0.0.1:${port}`,
    // The standalone ws listener token-gates GET /, /ws and /screenshot
    // by default (wash-auth-harden). This harness is the documented
    // trusted-loopback case: every router is per-test, bound to
    // 127.0.0.1 on an ephemeral port, and torn down with the test —
    // and the auth gate itself is covered by auth-harden.spec.ts via
    // the login fixture's per-user routers.
    '--no-auth',
    // Serve plain HTTP: the router defaults to self-signed HTTPS, but this
    // per-test harness is the documented trusted-loopback case (127.0.0.1,
    // ephemeral port, torn down with the test) and the specs fetch the
    // desktop over http://. HTTPS-by-default is exercised by tls.spec / the
    // runner unit tests.
    '--http',
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
  // Isolate the user's real ~/.local/state/wash. Unconditional, because
  // there is no version of "correct" where a test appends to the
  // developer's agent history — and because a shared state dir made the
  // agent specs fail each other (see xdgStateHome above). A test that
  // wants to READ what agentd wrote uses router.xdgStateHome.
  const xdgStateHome = mkdtempSync(join(tmpdir(), 'wash-e2e-state-'));
  env.XDG_STATE_HOME = xdgStateHome;
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
    xdgStateHome,
    fakesudoLog,
    log: () => logBuf,
    waitForLog: (re, timeout = 5_000, from = 0) => waitForRegex(() => logBuf.slice(from), re, timeout),
    logCursor: () => logBuf.length,
    // 12s (was 5s): a BE→router control round-trip can exceed 5s under the
    // full-parallel e2e load (8 workers × routers + ~40 BE apps), flaking
    // fm-be etc. with "control socket timeout". 12s stays under the 15s
    // per-test timeout so a genuine hang still fails the test.
    controlRequest: (req, timeoutMs = 12_000) => controlRoundtrip(controlSocket, req, timeoutMs),
    async sendAppMsg(instanceID, data, timeoutMs = 12_000) {
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
  for (const d of [h.appsDir, h.fmRoot, h.xdgConfigHome, h.xdgStateHome]) {
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
