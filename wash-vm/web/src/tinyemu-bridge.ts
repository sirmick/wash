// Bridge between Bellard's jslinux.js and the wash demo chrome.
//
// Responsibilities:
//   1. Term tap → forward each VM-console byte to debug-server /log.
//   2. Module lifecycle tracking (preRun / onAbort / calledRun).
//   3. XHR tracer — surfaces which asset fetch triggered an abort.
//   4. Resize listener — keeps Bellard's Term filling #term-host on
//      window resize (no fixed 80×24 grid).
//   5. View toggle — terminal ⇄ wash via title-bar buttons + Ctrl-`.
//   6. Title-bar VM-spec population — parses the loaded .cfg and
//      writes machine / mem / kernel / drive / bios into the strip.
//   7. /cmd + /ctl bridge — debug-server drop-files for keystroke
//      injection and page-reload control.

import * as dbg from './dbg';
// xterm CSS for the shell + washlog tabs. Vite handles the .css
// import; the byte-cost is paid once at module load.
import '@xterm/xterm/css/xterm.css';

interface JsLinuxTerm {
  write: (s: string) => void;
  setKeyHandler?: (fn: (ch: number) => void) => void;
  /** Bellard's Term — resize to pixel dimensions; recomputes cols/rows
   *  from char_width/char_height internally. */
  resizePixel?: (w: number, h: number) => boolean;
}

declare global {
  interface Window {
    term?: JsLinuxTerm;
    /** WASM-exported function jslinux assigns; takes a single char
     *  code and injects it into the VM's console. */
    console_write1?: (ch: number) => void;
  }
}

dbg.installErrorCapture();
dbg.log('demo', 'tinyemu page loaded');

// Flush the inline-HTML stdio buffer captured BEFORE this module loaded.
// The wasm exits synchronously inside Module.calledRun — the buffer
// is the only place TinyEMU's diagnostic prints (e.g. the HTIF error
// path that calls exit) survive long enough for us to forward to /ws.
(() => {
  const w = window as unknown as {
    __washDbgBuf?: Array<{ cat: string; line: string }>;
    __washDbg?: (cat: string, line: string) => void;
  };
  const buf = w.__washDbgBuf || [];
  for (const m of buf) dbg.log(m.cat, m.line);
  w.__washDbgBuf = [];
  // Subsequent pushes (from later in this same page lifecycle, e.g.
  // a soft reset that triggers another print) go straight to dbg.
  w.__washDbg = (cat, line) => dbg.log(cat, line);
})();

// --- 0. Stage indicator -----------------------------------------------------
// Title-bar status text + dot color. State machine is monotone-ish:
// boot → ready → wash. `error` is a terminal red state any phase can
// jump into. The bridge calls setStage(label, state?) on lifecycle
// events; CSS in index.html maps data-state to dot color.
type State = 'boot' | 'ready' | 'wash' | 'error';
let currentState: State = 'boot';
function setStage(label: string, state?: State): void {
  const titlebar = document.getElementById('titlebar');
  const statusEl = document.getElementById('status');
  if (statusEl) statusEl.textContent = label;
  if (state && titlebar) {
    // Don't downgrade from ready/wash back to boot.
    const rank: Record<State, number> = { boot: 0, ready: 1, wash: 2, error: 99 };
    if (rank[state] >= rank[currentState] || state === 'error') {
      currentState = state;
      titlebar.dataset.state = state;
    }
  }
  dbg.stage(label, state);
}

// --- 1. XHR tracer ---------------------------------------------------------
// Wrap XMLHttpRequest to log which URL's onload triggered an abort —
// pinpoints whether cfg / bios / kernel / drive fetch is the offender.
// Also lets us snoop the cfg body to extract VM specs for the title bar.
const cfgBodies: Record<string, string> = {};
(() => {
  const OrigXHR = window.XMLHttpRequest;
  class TracedXHR extends OrigXHR {
    private _washTraceUrl: string = '';
    open(method: string, url: string | URL, ...rest: unknown[]): void {
      const u = String(url);
      this._washTraceUrl = u;
      dbg.log('tinyemu', `xhr.open ${method} ${u}`);
      // Surface what's being fetched in the title bar — gives the user
      // a live "fetching kernel.bin" signal instead of opaque "loading…".
      const base = u.split('?')[0].split('/').pop() || u;
      setStage(`fetching ${base}`);
      this.addEventListener('load', () => {
        dbg.log('tinyemu', `xhr.load(${this.status}) ${u} len=${this.response?.byteLength ?? this.responseText?.length ?? '?'}`);
        if (u.endsWith('.cfg') && this.status === 200) {
          try {
            const text = typeof this.response === 'string'
              ? this.response
              : new TextDecoder().decode(this.response as ArrayBuffer);
            cfgBodies[u] = text;
            populateTitleBar(text);
          } catch (e) {
            dbg.log('tinyemu', `cfg snoop failed: ${(e as Error).message}`);
          }
        }
      });
      this.addEventListener('error', () => {
        dbg.log('tinyemu', `xhr.error ${u}`);
        setStage(`fetch failed: ${base}`, 'error');
      });
      // @ts-expect-error rest-args passthrough to base open
      return super.open(method, u, ...rest);
    }
  }
  (window as unknown as { XMLHttpRequest: typeof XMLHttpRequest }).XMLHttpRequest = TracedXHR;
})();

// --- 2. Title bar -----------------------------------------------------------
// Parse the JS-object-literal cfg (close enough to JSON after a regex
// strip of /* ... */ comments) and write the human-meaningful fields
// into the title-bar spans.
function populateTitleBar(cfgText: string): void {
  const machine = match(cfgText, /machine:\s*"([^"]+)"/);
  const mem = match(cfgText, /memory_size:\s*(\d+)/);
  const kernel = match(cfgText, /kernel:\s*"([^"]+)"/);
  const bios = match(cfgText, /bios:\s*"([^"]+)"/);
  const drive0 = match(cfgText, /drive0:\s*\{\s*file:\s*"([^"]+)"/);

  setSpec('spec-machine', machine ? machine.toUpperCase() : 'unknown');
  setSpec('spec-mem', mem ? `${mem}M RAM` : '');
  setSpec('spec-kernel', kernel ? `kernel ${basename(kernel)}` : '');
  setSpec('spec-bios', bios ? `bios ${basename(bios)}` : '');
  if (drive0) {
    // Probe drive size via HEAD for the title-bar size annotation.
    head(drive0).then(size => {
      setSpec('spec-drive', size ? `disk ${humanize(size)}` : `disk ${basename(drive0)}`);
    });
  } else {
    setSpec('spec-drive', '');
  }
}

function match(s: string, re: RegExp): string | null {
  const m = s.match(re);
  return m ? m[1] : null;
}
function basename(p: string): string {
  const slash = p.lastIndexOf('/');
  return slash >= 0 ? p.slice(slash + 1) : p;
}
function setSpec(id: string, text: string): void {
  const el = document.getElementById(id);
  if (el) el.textContent = text;
}
async function head(url: string): Promise<number | null> {
  try {
    // url is relative to the cfg's URL — resolve against /tinyemu/.
    const resolved = url.startsWith('/') || url.startsWith('http')
      ? url
      : `/tinyemu/${url}`;
    const r = await fetch(resolved, { method: 'HEAD' });
    if (!r.ok) return null;
    const cl = r.headers.get('content-length');
    return cl ? parseInt(cl, 10) : null;
  } catch {
    return null;
  }
}
function humanize(n: number): string {
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)}K`;
  return `${(n / 1024 / 1024).toFixed(1)}M`;
}

// --- 3. View tabs -----------------------------------------------------------
// Segmented control at top: kernel / shell / wash / washlog. body[data-view]
// drives CSS visibility; the bridge attaches/focuses the relevant
// element here. Ctrl-` cycles through the four.
type View = 'kernel' | 'shell' | 'wash' | 'washlog' | 'diag';
const VIEWS: View[] = ['kernel', 'shell', 'wash', 'washlog', 'diag'];

function setView(view: View): void {
  document.body.dataset.view = view;
  for (const v of VIEWS) {
    document.getElementById('view-' + v)?.classList.toggle('active', v === view);
  }
  if (view === 'kernel') document.getElementById('term_paste')?.focus();
  if (view === 'shell')   { shellTerm?.fit?.(); shellTerm?.focus(); }
  if (view === 'washlog') { washlogTerm?.fit?.(); washlogTerm?.focus(); }
  if (view === 'diag')    { diagTerm?.fit?.(); diagTerm?.focus(); }
  dbg.log('view', view);
}
for (const v of VIEWS) {
  document.getElementById('view-' + v)?.addEventListener('click', () => setView(v));
}
window.addEventListener('keydown', (e) => {
  if (e.ctrlKey && (e.key === '`' || e.key === '~')) {
    e.preventDefault();
    const cur = (document.body.dataset.view || 'kernel') as View;
    const next = VIEWS[(VIEWS.indexOf(cur) + 1) % VIEWS.length];
    setView(next);
  }
});

// Forward declarations — the shell + washlog xterm instances are
// constructed lazily after window.washConsoles is populated.
// fit callbacks are invoked by setView to grow/shrink the xterm grid
// when the user switches to that tab (the host element had display:none
// before, so the original ResizeObserver fired with 0×0 and we end up
// undersized until the next layout change).
let shellTerm: { focus: () => void; fit?: () => void } | undefined;
let washlogTerm: { focus: () => void; fit?: () => void } | undefined;
let diagTerm: { focus: () => void; fit?: () => void } | undefined;

// --- 4. Term tap + resize ---------------------------------------------------
const encoder = new TextEncoder();
let bellardTerm: JsLinuxTerm | undefined;

// Userspace / login / wash-router-ready detection over the term byte
// stream. We buffer the last ~256 bytes and string-match for the
// known signposts; cheap and resilient to chunk boundaries.
let kernelByteCount = 0;
let firstKernelByte = true;
let userspaceDetected = false;
let loginPromptDetected = false;
let washReadyDetected = false;
const tail = new Uint8Array(256);
let tailLen = 0;
const td = new TextDecoder('utf-8', { fatal: false });

function feedTail(bytes: Uint8Array): void {
  if (bytes.length >= tail.length) {
    tail.set(bytes.subarray(bytes.length - tail.length));
    tailLen = tail.length;
  } else {
    const room = tail.length - bytes.length;
    if (tailLen > room) {
      tail.copyWithin(0, tailLen - room, tailLen);
      tailLen = room;
    }
    tail.set(bytes, tailLen);
    tailLen += bytes.length;
  }
}
function tailContains(needle: string): boolean {
  const text = td.decode(tail.subarray(0, tailLen));
  return text.includes(needle);
}
function detectStages(): void {
  if (!userspaceDetected) {
    // Buildroot busybox-init: rcS prints "Starting" lines, or hostname
    // banner appears, or sysctl/syslogd messages — any of these means
    // /sbin/init has handed off to userspace.
    if (tailContains('Starting ')
        || tailContains('Welcome to ')
        || tailContains('OpenRC')
        || tailContains('Run /sbin/init')) {
      userspaceDetected = true;
      setStage('userspace');
    }
  }
  if (!loginPromptDetected && (tailContains('login:') || tailContains('# '))) {
    loginPromptDetected = true;
    setStage('ready · shell', 'ready');
  }
  if (!washReadyDetected && tailContains('wash-router: starting')) {
    washReadyDetected = true;
    setStage('wash-router up', 'wash');
  }
}

const waitForTerm = setInterval(() => {
  const term = window.term;
  if (!term) return;
  clearInterval(waitForTerm);
  bellardTerm = term;
  dbg.log('tinyemu', 'term tap installed');

  const origWrite = term.write.bind(term);
  term.write = (s: string) => {
    origWrite(s);
    const bytes = encoder.encode(s);
    dbg.pushBytes('riscv', bytes);
    kernelByteCount += bytes.length;
    if (firstKernelByte) {
      firstKernelByte = false;
      setStage('kernel booting');
    }
    feedTail(bytes);
    detectStages();
  };

  // First resize as soon as the term exists — Bellard's open() picks a
  // default grid; we want to expand it to fill #term_wrap immediately.
  applyTermResize();
}, 50);

// Resize Bellard's Term whenever the host element changes shape.
// resizePixel recomputes cols/rows from char dimensions internally.
// ResizeObserver fires on layout changes the window-resize event
// misses (e.g. flex relayout after the title bar measures, view
// toggle revealing the term, font load). Debounced via rAF.
let resizeRaf = 0;
function applyTermResize(): void {
  if (resizeRaf) cancelAnimationFrame(resizeRaf);
  resizeRaf = requestAnimationFrame(() => {
    resizeRaf = 0;
    if (!bellardTerm || typeof bellardTerm.resizePixel !== 'function') return;
    const host = document.getElementById('term_wrap');
    if (!host) return;
    const rect = host.getBoundingClientRect();
    if (rect.width < 32 || rect.height < 32) return; // not laid out yet
    const ok = bellardTerm.resizePixel(rect.width, rect.height);
    if (ok) dbg.log('tinyemu', `term resized to ${Math.round(rect.width)}×${Math.round(rect.height)}px`);
  });
}
window.addEventListener('resize', applyTermResize);
const termWrapEl = document.getElementById('term_wrap');
if (termWrapEl && typeof ResizeObserver !== 'undefined') {
  new ResizeObserver(applyTermResize).observe(termWrapEl);
}

// --- 5. Module lifecycle ----------------------------------------------------
const waitForModule = setInterval(() => {
  const mod = (window as any).Module;
  if (!mod || typeof mod !== 'object') return;
  clearInterval(waitForModule);
  dbg.log('tinyemu', `Module appeared, keys=${Object.keys(mod).slice(0, 12).join(',')}`);

  // Tap Module.print / printErr (Emscripten stdio surfaces) and route
  // them straight to the WS bus as dedicated sources. This is where
  // TinyEMU itself prints diagnostics like "HTIF: unsupported tohost"
  // — the ONE path in tinyemu's HTIF code that triggers exit() —
  // which were previously lost to console.log (not captured by
  // installErrorCapture). Critical for debugging why the wasm
  // exit(1)s before any kernel output.
  const origPrint = mod.print;
  const origPrintErr = mod.printErr;
  mod.print = (...args: unknown[]) => {
    const line = args.map((a) => String(a)).join(' ');
    dbg.log('tinyemu.stdout', line);
    if (typeof origPrint === 'function') return origPrint.apply(mod, args);
  };
  mod.printErr = (...args: unknown[]) => {
    const line = args.map((a) => String(a)).join(' ');
    dbg.log('tinyemu.stderr', line);
    if (typeof origPrintErr === 'function') return origPrintErr.apply(mod, args);
  };

  const wrap = (key: string) => {
    const orig = mod[key];
    if (orig !== undefined && typeof orig !== 'function') return;
    mod[key] = (...args: unknown[]) => {
      const argStr = args.map(a => {
        try { return typeof a === 'string' ? a : JSON.stringify(a); }
        catch { return String(a); }
      }).join(', ');
      dbg.log('tinyemu', `Module.${key}(${argStr})`);
      // Stage progression from Emscripten lifecycle.
      if (key === 'preRun')                setStage('wasm preRun');
      if (key === 'onRuntimeInitialized')  setStage('wasm runtime initialized');
      if (key === 'onAbort') {
        setStage(`aborted: ${argStr.slice(0, 60)}`, 'error');
        try { throw new Error('abort-stack'); }
        catch (e) {
          const stack = (e as Error).stack || '<no stack>';
          for (const line of stack.split('\n').slice(0, 15)) {
            dbg.log('tinyemu', `abort-stack: ${line.trim()}`);
          }
        }
      }
      if (typeof orig === 'function') return orig.apply(mod, args);
    };
  };
  for (const k of ['preRun', 'postRun', 'onRuntimeInitialized', 'onAbort']) wrap(k);

  let ticks = 0;
  const watch = setInterval(() => {
    ticks++;
    if (ticks > 40) { clearInterval(watch); return; }
    if (mod.calledRun) {
      clearInterval(watch);
      dbg.log('tinyemu', `Module.calledRun=true at ~${ticks * 250}ms`);
      setStage('VM running');
    }
  }, 250);
}, 50);

// --- 6. wash multi-channel virtio-console wiring + shell bootstrap ----------
// TinyEMU exposes N virtio-console devices; the bridge wires each to
// its role:
//   washConsoles[0]  → /dev/hvc1  login (future getty) — no consumer yet
//   washConsoles[1]  → /dev/hvc2  wash router DATA plane
//                                  ↳ feeds the wash shell via window.washV86Bus
//   washConsoles[2]  → /dev/hvc3  wash supervisor LOG (wash-router stdout +
//                                  respawn-loop messages); streamed to
//                                  server as `[wash-log]` source tag for
//                                  immediate crash visibility.
//
// The wash shell expects a v86-style bus on `window.washV86Bus`:
//   bus.send('virtio-console0-input', byte)
//   bus.register('virtio-console0-output-bytes', handler)
// We adapt washConsoles[1] (the data channel) into that shape, append
// ?transport=virtio-console&port=2, then import the shell bundle.

interface WashConsole {
  channel: number;
  input: (byte: number) => void;
  setOutputHandler: (fn: (bytes: Uint8Array) => void) => void;
}
declare global {
  interface Window {
    washConsoles?: WashConsole[];
    washV86Bus?: { send: (event: string, payload: unknown) => void; register: (event: string, handler: (data: unknown) => void) => void };
  }
}

(async () => {
  // Dual-path: dbg.log AND console.error so the message appears even
  // if the WS path drops it. installErrorCapture forwards console.error
  // to the WS bus, AND it's visible in DevTools.
  console.error('[wash-bootstrap] enter');
  dbg.log('wash', 'bootstrap: enter');
  const arr = window.washConsoles;
  if (!arr || arr.length < 3) {
    console.error('[wash-bootstrap] washConsoles missing/short');
    dbg.log('wash', `bootstrap: washConsoles missing/short (len=${arr?.length ?? 'undef'})`);
    return;
  }
  console.error('[wash-bootstrap] ' + arr.length + ' channels found');
  dbg.log('wash', `bootstrap: ${arr.length} channels found`);
  // Wash data + log + diag now live on the multiport virtio-console
  // (raw chardev /dev/vport0p{0,1,2}). Login (getty) stays on hvc1.
  const vports = (window as any).washVports as Array<typeof arr[0]> | undefined;
  if (!vports || vports.length < 3) {
    console.error('[wash-bootstrap] washVports missing/short');
    dbg.log('wash', `bootstrap: washVports missing/short (len=${vports?.length ?? 'undef'})`);
    return;
  }
  const dataVC = vports[0]; // /dev/vport0p0 — wash router data plane
  const logVC  = vports[1]; // /dev/vport0p1 — wash supervisor log
  const diagVC = vports[2]; // /dev/vport0p2 — periodic ps/uptime/dmesg dump

  // Spin up xterm.js instances for shell + washlog tabs.
  let xtermMod: typeof import('@xterm/xterm');
  try {
    xtermMod = await import('@xterm/xterm');
    console.error('[wash-bootstrap] xterm imported');
    dbg.log('wash', 'bootstrap: xterm imported');
  } catch (e) {
    console.error('[wash-bootstrap] xterm import FAILED', e);
    dbg.log('wash', `bootstrap: xterm import FAILED ${(e as Error).message}`);
    return;
  }
  const xtermOpts = {
    convertEol: true,
    fontSize: 12,
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    theme: { background: '#000000', foreground: '#cfd0d4' },
  };

  // /dev/hvc1 — login channel (washConsoles[0]). FitAddon makes the
  // xterm grid resize to fill the host element on view-show.
  const loginVC = arr[0];
  const shellHostEl = document.getElementById('shell_host');
  if (shellHostEl) {
    const fitMod = await import('@xterm/addon-fit');
    const t = new xtermMod.Terminal(xtermOpts);
    const fit = new fitMod.FitAddon();
    t.loadAddon(fit);
    t.open(shellHostEl);
    const fitNow = () => { try { fit.fit(); } catch (e) { /* host 0×0 yet */ } };
    requestAnimationFrame(fitNow);
    if (typeof ResizeObserver !== 'undefined') new ResizeObserver(fitNow).observe(shellHostEl);
    t.writeln('\x1b[36m[wash demo] /dev/hvc1 — login (getty). Type to interact.\x1b[0m');
    loginVC.setOutputHandler((bytes) => {
      t.write(bytes);
      dbg.log('login', new TextDecoder('utf-8', { fatal: false }).decode(bytes).replace(/[\x00-\x08\x0b-\x1f]/g, '·'));
    });
    t.onData((data) => {
      for (const ch of data) loginVC.input(ch.charCodeAt(0));
    });
    shellTerm = { focus: () => t.focus(), fit: fitNow };
  }

  // /dev/hvc3 — wash supervisor log (washConsoles[2]). Bytes go to:
  //   - server log (dbg.pushBytes 'wash-log') so an off-page tail
  //     sees crash info instantly
  //   - the [Wash log] tab's xterm view
  const washlogHostEl = document.getElementById('washlog_host');
  let washlogXt: { write: (s: string | Uint8Array) => void } | undefined;
  if (washlogHostEl) {
    const fitMod2 = await import('@xterm/addon-fit');
    const t = new xtermMod.Terminal(xtermOpts);
    const fit = new fitMod2.FitAddon();
    t.loadAddon(fit);
    t.open(washlogHostEl);
    const fitNow = () => { try { fit.fit(); } catch (e) { /* host 0×0 yet */ } };
    requestAnimationFrame(fitNow);
    if (typeof ResizeObserver !== 'undefined') new ResizeObserver(fitNow).observe(washlogHostEl);
    t.writeln('\x1b[36m[wash demo] /dev/vport0p1 — wash supervisor + router stdout/stderr.\x1b[0m');
    t.onData((data) => {
      for (const ch of data) logVC.input(ch.charCodeAt(0));
    });
    washlogXt = t;
    washlogTerm = { focus: () => t.focus(), fit: fitNow };
  }
  logVC.setOutputHandler((bytes) => {
    dbg.pushBytes('wash-log', bytes);
    washlogXt?.write(bytes);
  });

  // hvc4 → diag tap. Server-side tag is `[diag]`; also rendered in
  // the [Diag] tab via its own xterm so a developer can watch
  // ps / uptime / dmesg live.
  const diagHostEl = document.getElementById('diag_host');
  if (diagVC && diagHostEl) {
    const fitMod3 = await import('@xterm/addon-fit');
    const t = new xtermMod.Terminal(xtermOpts);
    const fit = new fitMod3.FitAddon();
    t.loadAddon(fit);
    t.open(diagHostEl);
    const fitNow = () => { try { fit.fit(); } catch (e) { /* host 0×0 yet */ } };
    requestAnimationFrame(fitNow);
    if (typeof ResizeObserver !== 'undefined') new ResizeObserver(fitNow).observe(diagHostEl);
    t.writeln('\x1b[36m[wash demo] /dev/vport0p2 — in-VM diagnostics (ps, uptime, dmesg).\x1b[0m');
    t.onData((data) => {
      for (const ch of data) diagVC.input(ch.charCodeAt(0));
    });
    diagVC.setOutputHandler((bytes) => {
      dbg.pushBytes('diag', bytes);
      t.write(bytes);
    });
    diagTerm = { focus: () => t.focus(), fit: fitNow };
  } else if (diagVC) {
    // No host element — still tap to the server log.
    diagVC.setOutputHandler((bytes) => { dbg.pushBytes('diag', bytes); });
  }

  // Data channel — wash protocol frames, feeds the shell. Build the
  // v86-compatible bus. Only the events the shell actually uses are
  // implemented; anything else logs a warning so future shell
  // additions are easy to spot.
  let firstWashByte = true;
  const outHandlers = new Set<(data: unknown) => void>();

  // Shell event-name conventions (see web/shell/src/virtio.ts):
  //   bus.send  ('virtio-console{N}-input-bytes',  Uint8Array)
  //   bus.register('virtio-console{N}-output-bytes', handler(Uint8Array))
  // where N is the URL ?port= value (we transiently set port=2).
  // Port number maps to virtio-console DEVICE index in TinyEMU:
  //   N=2 → washConsoles[1] (wash data, /dev/hvc2) ← what shell uses
  //   N=1 → washConsoles[0] (login,    /dev/hvc1)
  //   N=3 → washConsoles[2] (wash log, /dev/hvc3)
  // (SBI claims hvc0, so kernel's hvcN corresponds to our device N-1.)
  let txByteCount = 0;
  let rxByteCount = 0;
  const reIn  = /^virtio-console(\d+)-input-bytes$/;
  const reOut = /^virtio-console(\d+)-output-bytes$/;
  const handlersByPort = new Map<number, Set<(d: unknown) => void>>();

  function vcForPort(n: number): WashConsole | undefined {
    const idx = n - 1; // hvcN → washConsoles[N-1]
    return arr![idx];
  }

  // Wire ALL channels' output to per-port handler sets, so when the
  // shell registers for a port, our handler is already collecting.
  for (let n = 1; n <= 3; n++) {
    const vc = vcForPort(n);
    if (!vc) continue;
    // Special-case the dataVC (port 2) — it already has setOutputHandler
    // attached for our logging + auto-flip below. We chain: log THEN
    // dispatch to any port-handlers the shell registered.
  }

  window.washV86Bus = {
    send(event, payload) {
      const m = reIn.exec(event);
      if (m) {
        const port = parseInt(m[1], 10);
        const vc = vcForPort(port);
        if (!vc) {
          dbg.log('wash', `bus.send: no vc for port ${port}`);
          return;
        }
        const bytes = payload as Uint8Array;
        for (let i = 0; i < bytes.length; i++) vc.input(bytes[i]);
        txByteCount += bytes.length;
        if (txByteCount <= 64 || (txByteCount % 256) < bytes.length) {
          const hex = Array.from(bytes.slice(0, 16)).map(b => b.toString(16).padStart(2,'0')).join(' ');
          dbg.log('wash-tx', `port=${port} ${bytes.length}B (cum ${txByteCount}): ${hex}${bytes.length > 16 ? '…' : ''}`);
        }
        return;
      }
      dbg.log('wash', `bus.send unhandled: ${event}`);
    },
    register(event, handler) {
      const m = reOut.exec(event);
      if (m) {
        const port = parseInt(m[1], 10);
        let s = handlersByPort.get(port);
        if (!s) { s = new Set(); handlersByPort.set(port, s); }
        s.add(handler);
        dbg.log('wash', `bus.register port=${port} (${s.size} handler(s))`);
        // Backwards compat with the existing outHandlers iteration in
        // the dataVC setOutputHandler below (port 2 = dataVC).
        if (port === 2) outHandlers.add(handler);
        return;
      }
      dbg.log('wash', `bus.register unhandled: ${event}`);
    },
  };

  // Tap the output side too — separate from auto-flip — so we see
  // what bytes the wash-router is actually emitting.
  dataVC.setOutputHandler((bytes) => {
    rxByteCount += bytes.length;
    if (rxByteCount <= 64 || (rxByteCount % 256) < bytes.length) {
      const hex = Array.from(bytes.slice(0, 16)).map(b => b.toString(16).padStart(2,'0')).join(' ');
      dbg.log('wash-rx', `${bytes.length}B (cum ${rxByteCount}): ${hex}${bytes.length > 16 ? '…' : ''}`);
    }
    if (firstWashByte) {
      firstWashByte = false;
      dbg.log('wash', `first byte(s) from /dev/hvc2 (n=${bytes.length})`);
      setStage('wash-router up', 'wash');
      // Auto-flip to the wash desktop tab on first data-channel byte.
      // The user can still hit [kernel] / [shell] / [wash log] in the
      // title bar to switch away — auto-flip only fires once.
      setView('wash');
    }
    for (const h of outHandlers) {
      try { h(bytes); } catch (e) { dbg.log('wash', `out handler threw: ${(e as Error).message}`); }
    }
  });

  // The shell reads ?transport=virtio-console&port=2 from
  // window.location.search during its synchronous init (Conn picks
  // transport, then connects). We don't want this in the URL bar
  // permanently — so set it transiently around the import, then
  // restore the clean URL once the shell has consumed it.
  const origUrl = window.location.pathname + window.location.search + window.location.hash;
  const transientSp = new URLSearchParams(window.location.search);
  transientSp.set('transport', 'virtio-console');
  if (!transientSp.has('port')) transientSp.set('port', '2');
  window.history.replaceState(null, '', window.location.pathname + '?' + transientSp.toString());

  dbg.log('wash', 'loading shell bundle…');
  try {
    await loadShellModule('/shell/shell.js');
    dbg.log('wash', 'shell bundle loaded');
  } catch (e) {
    dbg.log('wash', `shell load failed: ${(e as Error).message}`);
  } finally {
    // Restore the original (clean) URL. The shell has already read
    // its transport hint by now.
    window.history.replaceState(null, '', origUrl);
  }
})();

async function loadShellModule(url: string): Promise<void> {
  await import(/* @vite-ignore */ url);
}

// --- 7. WS-driven admin commands -------------------------------------------
// Admin frames arrive through dbg's WS singleton — no polling. The
// server fans admin→browser; we react to ctl (page-level verbs) and
// input (bytes to type into the VM console via console_write1).
dbg.onMessage((msg) => {
  if (msg.t === 'ctl') {
    if (msg.verb === 'reload') {
      dbg.log('ctl', 'reload received — reloading page');
      window.location.reload();
    } else if (msg.verb === 'reset' || msg.verb === 'stop' || msg.verb === 'run') {
      // Hooks for future emulator-control verbs once jslinux exposes
      // run/stop bindings. No-op today.
      dbg.log('ctl', `verb ${msg.verb} not wired yet`);
    }
  } else if (msg.t === 'input') {
    // Optional msg.port routes to a specific virtio-console:
    //   port=1 → /dev/hvc1 (login)        → washConsoles[0]
    //   port=2 → /dev/hvc2 (wash data)    → washConsoles[1]
    //   port=3 → /dev/hvc3 (wash log/ctl) → washConsoles[2]
    // No port → legacy console_write1 (defaults to hvc1 via legacy fifo).
    const data = String(msg.data ?? '');
    // For binary data sent as latin1 from the CLI --hex path,
    // charCodeAt gives the raw byte. For text, encoder + iterate.
    const bytes = (msg.encoding === 'latin1')
      ? Uint8Array.from(data, c => c.charCodeAt(0) & 0xff)
      : new TextEncoder().encode(data);
    const port = typeof msg.port === 'number' ? msg.port : null;
    if (port !== null) {
      const idx = port - 1;
      const vc = (window as Window).washConsoles?.[idx];
      if (!vc) {
        dbg.log('input', `port=${port} → washConsoles[${idx}] not present`);
        return;
      }
      for (let i = 0; i < bytes.length; i++) vc.input(bytes[i]);
      dbg.log('input', `port=${port} typed ${bytes.length} bytes`);
      return;
    }
    if (typeof window.console_write1 !== 'function') {
      dbg.log('input', 'console_write1 unavailable; dropping');
      return;
    }
    for (let i = 0; i < bytes.length; i++) window.console_write1(bytes[i]);
    dbg.log('input', `legacy typed ${bytes.length} bytes`);
  }
});
