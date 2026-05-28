// wash-priv: full BE-side flow exercised through the router's control
// socket, with Node-side crypto matching what the browser FE does.
//
// Coverage:
//   1. Cross-app request — wash-test SendAppMsgTo's wash-priv with a
//      {kind:"spawn", app_id:"com.wash.about"}. wash-priv spawns on
//      demand (singleton); the router-attested sender is
//      com.wash.test (proven by reaching wash-priv's queue at all,
//      since payload-claimed identity would not have routed through
//      OnAppMsgFrom).
//   2. Approve while locked — wash-priv replies need_password with a
//      fresh ECDH-P256 public key.
//   3. Unlock — we derive the shared secret with Node's webcrypto,
//      HKDF-SHA256 → AES-256-GCM, encrypt the FAKESUDO_PASSWORD, and
//      send {kind:"unlock"}. wash-priv validates against fakesudo
//      ("sudo -S -k -v") and caches the password.
//   4. Spawn — wash-priv calls PrepareSpawn, then exec's fakesudo
//      wrapping the registered target. fakesudo logs one line per
//      dispatch (eagerly, before c.Run) so the test can assert the
//      argv crossed the sudo boundary without waiting for the long-
//      lived wash app to terminate.
//
// Wrong-password coverage exercises the "bad password" branch of
// validateSudo, asserting fakesudo logged the validate failure and
// never reached the exec branch.
//
// Future Playwright follow-up: drive the FE password modal in a real
// browser, target wash-term --exec (which auto-exits), and assert
// the {kind:"result"} reply + exit-code-populated audit row.

import { test, expect, SUDO_BIN } from '../fixtures/router';
import { readFileSync, existsSync } from 'node:fs';
import { webcrypto } from 'node:crypto';
import { spawn } from 'node:child_process';

const PASSWORD = 'test123';
const HKDF_INFO = 'wash-priv/password/v1';
const PRIV_APP_ID = 'com.wash.priv';

test.use({
  routerOpts: {
    apps: ['session', 'about', 'test', 'term', 'priv'],
    showHidden: true,
    fakesudo: true,
  },
});

// b64encode/b64decode mirror the FE helpers; the router base64-
// encodes CBOR byte fields when normalising to JSON for the control
// socket, so binary fields cross the wire as strings.
function b64encode(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64');
}
function b64decode(s: string): Uint8Array {
  return new Uint8Array(Buffer.from(s, 'base64'));
}

// encryptPassword is the exact FE-side dance, but in Node. Returns
// base64 strings for the three binary fields wash-priv expects.
async function encryptPassword(
  password: string,
  bePubRaw: Uint8Array,
): Promise<{ ciphertext: string; fe_pubkey: string; nonce: string }> {
  const subtle = webcrypto.subtle;
  const bePub = await subtle.importKey(
    'raw',
    bePubRaw,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );
  const fe = await subtle.generateKey(
    { name: 'ECDH', namedCurve: 'P-256' },
    true,
    ['deriveBits'],
  );
  const shared = new Uint8Array(
    await subtle.deriveBits({ name: 'ECDH', public: bePub }, fe.privateKey, 256),
  );
  const hkdfKey = await subtle.importKey('raw', shared, { name: 'HKDF' }, false, ['deriveKey']);
  const aesKey = await subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode(HKDF_INFO),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  );
  const nonce = webcrypto.getRandomValues(new Uint8Array(12));
  const pwBytes = new TextEncoder().encode(password);
  const ct = new Uint8Array(
    await subtle.encrypt({ name: 'AES-GCM', iv: nonce }, aesKey, pwBytes),
  );
  const fePubRaw = new Uint8Array(await subtle.exportKey('raw', fe.publicKey));
  return {
    ciphertext: b64encode(ct),
    fe_pubkey: b64encode(fePubRaw),
    nonce: b64encode(nonce),
  };
}

// drivePriv shares the per-test boilerplate: launch wash-priv and
// wash-test, send `hello` to seed wash-priv's page nonce, return the
// instance ids so the test can keep driving them.
async function drivePriv(router: import('../fixtures/router').RouterHandle) {
  // wash-priv is singleton; we don't need to launch it explicitly —
  // the first sentinel-addressed msg spawns it. But spawning ahead
  // gives us its instance_id for direct addressing.
  const privLaunched = await router.controlRequest({ t: 'launch', app_id: PRIV_APP_ID });
  expect(privLaunched.t).toBe('launched');
  const privInst = privLaunched.instance_id as string;

  const testLaunched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
  expect(testLaunched.t).toBe('launched');
  const testInst = testLaunched.instance_id as string;

  // Seed wash-priv's page nonce so it doesn't surprise-lock on the
  // first real message. The control socket can't carry a `from`
  // envelope, so this hello looks like an own-FE hello — which is
  // exactly what we want.
  await router.controlRequest({
    t: 'msg',
    instance_id: privInst,
    data: { kind: 'hello', page_nonce: 'e2e-test-nonce' },
  });

  return { privInst, testInst };
}

test('cross-app spawn: enqueue → approve → unlock → fakesudo exec → result', async ({ router }) => {
  const { privInst, testInst } = await drivePriv(router);

  // 1. wash-test → wash-priv via SendAppMsgTo. The router fills `from`
  // with com.wash.test/<testInst>; wash-priv routes that to
  // OnAppMsgFrom, which enqueues the request.
  //
  // We target wash-about as the spawn — it's a tiny windowed app
  // that doesn't open raw channels on mount, so it can complete its
  // handshake without a browser connected. The {kind:"run"} sugar
  // would target wash-term, whose pty raw channel needs a shell-side
  // binding to complete — a full-browser Playwright test should
  // cover that path next.
  await router.controlRequest({
    t: 'msg',
    instance_id: testInst,
    data: {
      kind: 'send_to',
      target_app: PRIV_APP_ID,
      payload: {
        kind: 'spawn',
        req_id: 'r-cross-1',
        app_id: 'com.wash.about',
        reason: 'e2e cross-app test',
      },
    },
  });
  // The send_to traverses an async path; give wash-priv a tick to
  // ingest before driving the approval.
  await new Promise((r) => setTimeout(r, 200));

  // 2. Approve. wash-priv is locked, so we expect need_password
  // back. We can't directly capture it through the control socket
  // (which doesn't surface BE→FE app_msgs by default), so we use
  // sendAppMsg's await_id facility — but wash-priv's replies don't
  // tag with `id`. Instead we use waitForLog as the readiness
  // signal: wash-priv prints "wash-priv ready" early but for need_
  // password we have no log. Drop in a `resync` to pull the
  // current state through a separate path.
  //
  // Simplest robust route: drive the approval, then poll fakesudo's
  // log for the `validate` line (which only appears after a
  // successful unlock).
  await router.controlRequest({
    t: 'msg',
    instance_id: privInst,
    data: { kind: 'approve', req_id: 'r-cross-1' },
  });

  // 3. wash-priv minted a fresh BE pubkey and shipped a
  // need_password event we never saw. We need that pubkey to encrypt
  // the password. Capture path: hook the shell's WebSocket. For BE-
  // only tests, use waitForLog: nothing prints the pubkey today.
  //
  // Compromise: extend wash-priv's BE to log a marker line. We add
  // "wash-priv: need_password be_pubkey=<base64>" so the test can
  // sniff it. Logged in the test branch; not a leak (the pubkey is
  // public by definition).
  const m = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
  const bePubB64 = m.replace(/^.*be_pubkey=/, '');
  const bePub = b64decode(bePubB64);

  // 4. Encrypt and ship the password.
  const enc = await encryptPassword(PASSWORD, bePub);
  await router.controlRequest({
    t: 'msg',
    instance_id: privInst,
    data: {
      kind: 'unlock',
      ciphertext: enc.ciphertext,
      fe_pubkey: enc.fe_pubkey,
      nonce: enc.nonce,
    },
  });

  // 5. Wait for fakesudo to have been invoked twice: once for -v
  // (validation) and once for the actual spawn. The log is a
  // jsonl file the harness pointed FAKESUDO_LOG at.
  await pollUntil(() => fakesudoEntries(router.fakesudoLog).length >= 2, 5000);
  const entries = fakesudoEntries(router.fakesudoLog);
  const validate = entries.find((e) => e.mode === 'validate');
  const exec = entries.find((e) => e.mode === 'exec');
  expect(validate, 'fakesudo received the -v validate call').toBeTruthy();
  expect(validate!.pw_ok).toBe(true);
  expect(exec, 'fakesudo received the exec call').toBeTruthy();
  expect(exec!.target).toBe(`${router.appsDir}/wash-about`);
  // exit==-1 is fakesudo's "dispatched but not yet exited" sentinel
  // — wash-about is long-lived and won't exit on its own.
  expect(exec!.exit).toBe(-1);
  // The audit log records "approve" only after runSudo returns, so
  // the finalised exit-code line won't be present here. That code
  // path is covered by the wrong-password test (which records
  // "password_failed") and would also fire on a target like wash-
  // term --exec that auto-exits in a future Playwright follow-up.
});

// wash-sudo CLI: full streaming inline flow. The CLI sends priv.run
// on the control socket, the headless approve+unlock drives wash-
// priv, fakesudo exec's whoami, the byte stream comes back through
// the cli session, wash-sudo prints to stdout and exits 0.
test('wash-sudo inline flow streams stdout and propagates exit', async ({ router }) => {
  if (!existsSync(SUDO_BIN)) test.skip(true, 'wash-sudo binary missing');
  const { privInst } = await drivePriv(router);

  // Fire wash-sudo as a subprocess. WASH_CONTROL_SOCKET is the only
  // env it needs. We capture stdout/stderr to assert the streamed
  // bytes landed.
  const proc = spawn(SUDO_BIN, ['whoami'], {
    env: { ...process.env, WASH_CONTROL_SOCKET: router.controlSocket },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '';
  let stderr = '';
  proc.stdout.on('data', (b: Buffer) => { stdout += b.toString('utf8'); });
  proc.stderr.on('data', (b: Buffer) => { stderr += b.toString('utf8'); });

  // Router logs every CLI invocation with the requester-supplied
  // req_id. We use that as the sync point before approving.
  const m = await router.waitForLog(/priv\.run req_id=(ws-[a-f0-9]+)/, 5000);
  const reqMatch = m.match(/req_id=(ws-[a-f0-9]+)/);
  expect(reqMatch, 'log carries a req_id').toBeTruthy();
  const reqID = reqMatch![1];

  // Approve via the control socket (delivers as OnAppMsg — own-FE
  // path — which is what HandleApprove expects).
  await router.controlRequest({
    t: 'msg', instance_id: privInst,
    data: { kind: 'approve', req_id: reqID },
  });

  // wash-priv replies need_password; sniff the BE pubkey from the
  // log and ship a real unlock with the FAKESUDO_PASSWORD.
  const km = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
  const bePub = b64decode(km.replace(/^.*be_pubkey=/, ''));
  const enc = await encryptPassword(PASSWORD, bePub);
  await router.controlRequest({
    t: 'msg', instance_id: privInst,
    data: {
      kind: 'unlock',
      ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce,
    },
  });

  // Wait for the process to exit. The streamed "mick\n" (or whatever
  // whoami prints on this box) should be on stdout by then.
  const exitCode: number = await new Promise((resolve) => {
    proc.on('exit', (code) => resolve(code ?? -1));
  });
  expect(exitCode, `wash-sudo exit (stderr: ${stderr})`).toBe(0);
  expect(stdout.trim().length, 'whoami produced some output').toBeGreaterThan(0);

  // fakesudo's log proves the right binary was invoked with the
  // right argv on the sudo boundary. exec.exit==-1 is fakesudo's
  // "dispatched before c.Run returned" sentinel — for fast commands
  // like whoami it may also have a real exit code, so we just
  // check the exec line exists.
  const entries = fakesudoEntries(router.fakesudoLog);
  const exec = entries.find((e) => e.mode === 'exec' && e.target === 'whoami');
  expect(exec, 'fakesudo dispatched whoami').toBeTruthy();
});

test('wrong password rejects the unlock without touching the cache', async ({ router }) => {
  const { privInst, testInst } = await drivePriv(router);
  await router.controlRequest({
    t: 'msg',
    instance_id: testInst,
    data: {
      kind: 'send_to',
      target_app: PRIV_APP_ID,
      payload: {
        kind: 'run',
        req_id: 'r-bad-1',
        argv: ['/bin/true'],
      },
    },
  });
  await new Promise((r) => setTimeout(r, 200));
  await router.controlRequest({
    t: 'msg',
    instance_id: privInst,
    data: { kind: 'approve', req_id: 'r-bad-1' },
  });
  const m = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
  const bePub = b64decode(m.replace(/^.*be_pubkey=/, ''));
  const enc = await encryptPassword('not-the-password', bePub);
  await router.controlRequest({
    t: 'msg',
    instance_id: privInst,
    data: {
      kind: 'unlock',
      ciphertext: enc.ciphertext,
      fe_pubkey: enc.fe_pubkey,
      nonce: enc.nonce,
    },
  });
  // fakesudo should have run -v (validation) and reported pw_ok:false;
  // no exec entry. We poll briefly then assert no exec landed.
  await pollUntil(() => fakesudoEntries(router.fakesudoLog).length >= 1, 5000);
  const entries = fakesudoEntries(router.fakesudoLog);
  expect(entries.some((e) => e.mode === 'validate' && e.pw_ok === false)).toBe(true);
  expect(entries.some((e) => e.mode === 'exec')).toBe(false);
});

// SDK PrivRunInlineSync from a wash-app BE. Exercises the round-
// trip where wash-test is the requester (not the wash-sudo CLI):
// SendAppMsgTo → wash-priv → approval → unlock → run_inline → bytes
// stream back to wash-test → sudo_whoami_result reaches wash-test's
// FE event log via sendEvent.
test('SDK PrivRunInlineSync from wash-test reaches wash-priv and streams stdout back', async ({ router }) => {
  const { privInst } = await drivePriv(router);

  // Launch wash-test and trigger its sudo_whoami button programmatically
  // via the control socket. This exercises the BE → BE cross-app path:
  // the request's sender is com.wash.test (router-attested), and
  // PrivRunInlineSync's reply interceptor in the SDK pulls bytes off
  // the wash-priv stream messages.
  const testLaunched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
  const testInst = testLaunched.instance_id as string;
  await router.controlRequest({
    t: 'msg', instance_id: testInst,
    data: { kind: 'sudo_whoami' },
  });

  // wash-priv logs every enqueue with the requester-supplied req_id.
  // For PrivRunInlineSync that id starts with "p-" (the SDK helper's
  // prefix; wash-sudo uses "ws-").
  const m = await router.waitForLog(/wash-priv enqueue: req_id=(p-[a-f0-9]+) kind=run_inline sender=com\.wash\.test/, 5000);
  const reqID = m.match(/req_id=(p-[a-f0-9]+)/)![1];
  await router.controlRequest({
    t: 'msg', instance_id: privInst,
    data: { kind: 'approve', req_id: reqID },
  });

  const km = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
  const bePub = b64decode(km.replace(/^.*be_pubkey=/, ''));
  const enc = await encryptPassword(PASSWORD, bePub);
  await router.controlRequest({
    t: 'msg', instance_id: privInst,
    data: {
      kind: 'unlock',
      ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce,
    },
  });

  // fakesudo logs both the validate and the exec call. The exec
  // target is "whoami" (no path — fakesudo resolves via PATH which
  // includes WASH_BIN_DIR plus the user's PATH).
  await pollUntil(() => fakesudoEntries(router.fakesudoLog).some((e) => e.mode === 'exec'), 5000);
  const entries = fakesudoEntries(router.fakesudoLog);
  const exec = entries.find((e) => e.mode === 'exec');
  expect(exec!.target).toBe('whoami');
  expect(exec!.pw_ok).toBe(true);
});

// wash-sudo bare-form heuristic: when argv[0] matches a registered
// wash-app binary basename, the router auto-promotes to spawn mode.
// Verifies the heuristic itself (the spawn outcome is covered by the
// existing cross-app spawn test).
test('wash-sudo bare-form auto-promotes a known app binary to spawn mode', async ({ router }) => {
  if (!existsSync(SUDO_BIN)) test.skip(true, 'wash-sudo binary missing');
  await drivePriv(router);

  // Bare invocation: wash-sudo wash-about. The router should log the
  // heuristic line; the spawn path proceeds the same way the
  // explicit --app form does. We're proving the promotion happened.
  const proc = spawn(SUDO_BIN, ['wash-about'], {
    env: { ...process.env, WASH_CONTROL_SOCKET: router.controlSocket },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  // The process will hang waiting for approval; we kill it after we
  // see the heuristic log. The router's "priv.run heuristic" line is
  // the smoking gun.
  await router.waitForLog(/priv\.run heuristic: .*wash-about → spawn app_id=com\.wash\.about/, 5000);
  proc.kill();
  await new Promise((r) => proc.on('exit', r));
});

// Auto-prompt on enqueue: when wash-priv is locked and a request
// arrives, need_password should fire WITHOUT an explicit Approve
// click. The request stays in the queue until the user unlocks it
// by entering the password.
test('locked + new request → auto-prompts for password without Approve click', async ({ router }) => {
  const { privInst, testInst } = await drivePriv(router);

  // No prior unlock. Fire a cross-app spawn from wash-test; wash-priv
  // should auto-fire need_password at enqueue time.
  await router.controlRequest({
    t: 'msg', instance_id: testInst,
    data: {
      kind: 'send_to', target_app: PRIV_APP_ID,
      payload: { kind: 'spawn', req_id: 'auto-1', app_id: 'com.wash.about' },
    },
  });
  // The "(auto-prompt req=…)" suffix is the smoking gun — this
  // distinguishes auto-prompt from a subsequent HandleApprove
  // need_password.
  await router.waitForLog(/need_password be_pubkey=[A-Za-z0-9+/=]+ \(auto-prompt req=auto-1\)/, 5000);
  // No "approve" was sent — only the enqueue triggered the prompt.
});

// NOTE: M7 dropped the page-nonce auto-lock-on-refresh feature
// (was: priv tracked a per-FE page_nonce, cleared the password cache
// when it changed). The feature relied on an own-FE hello path; with
// surface=background, priv has no FE and the session FE's lifecycle
// (which is a singleton across tab refreshes) doesn't generate a
// useful nonce signal. The idle timer (15min default) is the
// remaining lock-when-walked-away protection. Re-adding the auto-lock
// would require the session FE to generate a per-mount nonce and the
// session BE gateway to forward it as a priv "refresh" event — left
// for M8 if it lands. Two e2e tests that exercised that path were
// removed here; the unit-level coverage in apps/priv/be/queue_test.go
// stays intact.

// Only the prompting request runs on unlock. Two requests are
// enqueued — A auto-prompts; B waits. Unlock-via-A's-password
// runs only A; B stays queued.
test('unlock only runs the request that prompted it', async ({ router }) => {
  const { privInst, testInst } = await drivePriv(router);

  // First request auto-prompts.
  await router.controlRequest({
    t: 'msg', instance_id: testInst,
    data: {
      kind: 'send_to', target_app: PRIV_APP_ID,
      payload: { kind: 'spawn', req_id: 'pa-A', app_id: 'com.wash.about' },
    },
  });
  await router.waitForLog(/need_password be_pubkey=([A-Za-z0-9+/=]+) \(auto-prompt req=pa-A\)/, 5000);

  // Second request enqueues but does NOT trigger another prompt
  // (pendingApproveReq is already set).
  await router.controlRequest({
    t: 'msg', instance_id: testInst,
    data: {
      kind: 'send_to', target_app: PRIV_APP_ID,
      payload: { kind: 'spawn', req_id: 'pa-B', app_id: 'com.wash.about' },
    },
  });
  await router.waitForLog(/enqueue: req_id=pa-B/, 5000);

  // Unlock using the auto-prompt's pubkey.
  const allText = router.log();
  const lastNP = [...allText.matchAll(/need_password be_pubkey=([A-Za-z0-9+/=]+)/g)].pop();
  const bePub = b64decode(lastNP![1]);
  const enc = await encryptPassword(PASSWORD, bePub);
  await router.controlRequest({
    t: 'msg', instance_id: privInst,
    data: { kind: 'unlock', ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce },
  });

  // Exactly ONE fakesudo exec should fire — the one for pa-A.
  // pa-B remains queued and never gets dispatched.
  await pollUntil(() => fakesudoEntries(router.fakesudoLog).some((e) => e.mode === 'exec'), 5000);
  // Sleep a beat to make sure no SECOND exec sneaks in.
  await new Promise((r) => setTimeout(r, 500));
  const execs = fakesudoEntries(router.fakesudoLog).filter((e) => e.mode === 'exec');
  expect(execs.length, 'exactly one exec for the prompting request').toBe(1);
});

// pollUntil keeps re-running pred until it returns truthy or the
// deadline passes. Returns the final value or throws on timeout.
async function pollUntil<T>(pred: () => T, timeoutMs: number): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const v = pred();
    if (v) return v;
    if (Date.now() > deadline) {
      throw new Error('pollUntil timeout');
    }
    await new Promise((r) => setTimeout(r, 50));
  }
}

interface FakeSudoEntry {
  mode: 'validate' | 'exec' | 'error';
  pw_ok: boolean;
  target?: string;
  target_args?: string[];
  exit: number;
}

function fakesudoEntries(path: string): FakeSudoEntry[] {
  if (!path || !existsSync(path)) return [];
  const raw = readFileSync(path, 'utf8');
  const out: FakeSudoEntry[] = [];
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    try {
      out.push(JSON.parse(line) as FakeSudoEntry);
    } catch {
      /* ignore partial writes */
    }
  }
  return out;
}
