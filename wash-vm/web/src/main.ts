// Demo outer page entrypoint.
//
// Boot flow:
//   1. Set up the xterm.js boot terminal in the overlay.
//   2. Detect required assets (libv86.js, kernel.bin, rootfs.ext2).
//      Missing? Show clear messaging; do not crash.
//   3. Load libv86 from public/. Construct an emulator wired to
//      our offscreen canvas, kernel, and rootfs.
//   4. Hook virtio-console port 0 → boot terminal.
//   5. Hook virtio-console port 1 → WASH_READY token watcher.
//   6. Stash emulator.bus on window.washV86Bus so the wash shell
//      can find it.
//   7. Import the shell bundle. The shell reads the URL's
//      ?transport=virtio-console&port=2 hint and connects through
//      VirtioConsoleSocket to the BE-side wash-router running
//      inside the VM on /dev/vport0p2.
//   8. On WASH_READY → fade the boot overlay out, revealing the
//      desktop the shell mounted under it.

import { BootTerminal } from './boot';
import * as dbg from './dbg';

declare global {
  interface Window {
    /** Set by libv86 (loaded from /libv86.js). Constructor for V86. */
    V86?: V86Ctor;
    /** Published by us once the emulator is ready; the wash shell
     *  picks this up to talk to the BE. */
    washV86Bus?: V86Bus;
  }
}

interface V86Ctor {
  new (opts: V86Options): V86Instance;
}

interface V86Options {
  wasm_path?: string;
  memory_size?: number;
  vga_memory_size?: number;
  bios?: { url: string };
  vga_bios?: { url: string };
  bzimage?: { url: string };
  cmdline?: string;
  initrd?: { url: string };
  hda?: { url: string; size?: number };
  filesystem?: { baseurl?: string; basefs?: string };
  autostart?: boolean;
  screen_container?: HTMLElement;
  disable_keyboard?: boolean;
  disable_mouse?: boolean;
  uart1?: boolean;
  uart2?: boolean;
  uart3?: boolean;
  virtio_console?: boolean;
}

interface V86Bus {
  send(event: string, payload: any): void;
  register(event: string, handler: (data: any) => void): void;
}

interface V86Instance {
  bus: V86Bus;
  run(): void;
  stop(): void;
  /** Reset the emulated machine — equivalent to power-cycling. */
  restart?(): void;
  /** Kill the emulator entirely and free its WASM instance. */
  destroy?(): void;
}

const bootEl = document.getElementById('boot')!;
const bootStageEl = document.getElementById('boot-stage')!;
const bootTermHost = document.getElementById('boot-term')!;
const v86ScreenContainer = document.getElementById('v86-screen-container')!;
const bootHeaderEl = document.getElementById('boot-header')!;

const boot = new BootTerminal(bootTermHost);

// Pipe browser errors + console.error/warn to the demo server so an
// off-page observer sees them without devtools open. Server: WS bus
// on /ws (started by `pnpm -F @wash/demo dev`); tail: `pnpm cli tail`.
dbg.installErrorCapture();
dbg.log('demo', 'page loaded');

function setStage(label: string): void {
  bootStageEl.textContent = label;
  dbg.log('demo', `stage: ${label}`);
}

// exists checks whether `url` resolves to a real asset, not Vite's
// SPA fallback. We can't trust 200-vs-404 alone: dev-mode Vite (and
// many static hosts) serve index.html on miss with a 200. Guarding
// on Content-Type catches the fallback explicitly.
async function exists(url: string): Promise<boolean> {
  try {
    const r = await fetch(url, { method: 'HEAD' });
    if (!r.ok) return false;
    const ct = (r.headers.get('content-type') || '').toLowerCase();
    if (ct.startsWith('text/html')) return false; // SPA fallback
    return true;
  } catch {
    return false;
  }
}

async function main() {
  setStage('checking assets…');

  // Six blobs supplied by the demo author. Paths are conventional;
  // adjust if your image layout differs.
  const libv86Url = '/libv86.js';
  const wasmUrl = '/v86.wasm';
  const biosUrl = '/seabios.bin';
  const vgaBiosUrl = '/vgabios.bin';
  const kernelUrl = '/kernel.bin';
  const rootfsUrl = '/rootfs.ext2';

  const [hasLib, hasWasm, hasBios, hasVgaBios, hasKernel, hasRootfs] = await Promise.all([
    exists(libv86Url),
    exists(wasmUrl),
    exists(biosUrl),
    exists(vgaBiosUrl),
    exists(kernelUrl),
    exists(rootfsUrl),
  ]);

  if (!hasLib || !hasWasm || !hasBios || !hasVgaBios || !hasKernel || !hasRootfs) {
    boot.writeln('one or more required assets are missing in public/:');
    boot.writeln(`  /libv86.js     ${hasLib ? 'OK' : 'MISSING'}`);
    boot.writeln(`  /v86.wasm      ${hasWasm ? 'OK' : 'MISSING'}`);
    boot.writeln(`  /seabios.bin   ${hasBios ? 'OK' : 'MISSING'}`);
    boot.writeln(`  /vgabios.bin   ${hasVgaBios ? 'OK' : 'MISSING'}`);
    boot.writeln(`  /kernel.bin    ${hasKernel ? 'OK' : 'MISSING'}`);
    boot.writeln(`  /rootfs.ext2   ${hasRootfs ? 'OK' : 'MISSING'}`);
    boot.writeln('');
    boot.writeln('drop libv86.js + v86.wasm (from copy/v86 releases) and');
    boot.writeln('seabios.bin + vgabios.bin (from the v86 repo bios/ dir)');
    boot.writeln('along with your custom kernel.bin + rootfs.ext2 into');
    boot.writeln('web/demo/public/. See web/demo/README.md.');
    setStage('missing assets');
    return;
  }

  setStage('loading libv86…');
  boot.writeln('loading libv86…');
  await loadScript(libv86Url);
  if (!window.V86) {
    boot.writeln('libv86.js loaded but window.V86 is undefined — wrong build?');
    setStage('libv86 load failed');
    return;
  }

  setStage('starting kernel…');
  boot.writeln('starting v86 emulator…');

  const emulator = new window.V86({
    wasm_path: wasmUrl,
    memory_size: 128 * 1024 * 1024,
    vga_memory_size: 2 * 1024 * 1024,
    // SeaBIOS handles the bzImage real-mode boot protocol. Without
    // it the CPU runs in 16-bit mode forever and the kernel never
    // gets handed control — emulator-started fires but no console
    // output appears.
    bios: { url: biosUrl },
    vga_bios: { url: vgaBiosUrl },
    bzimage: { url: kernelUrl },
    // Single-port v86 build (production libv86.js exposes only
    // virtio-console0). Strategy:
    //   - ttyS0 (8250)        → kernel console = boot log
    //   - hvc0 (virtio-cons.) → wash router CBOR bus (NOT a console)
    // Therefore: console=ttyS0 ONLY; never list hvc0 as console, or
    // kernel printk would pollute the wash byte stream.
    cmdline: 'earlyprintk=ttyS0,115200 console=ttyS0,115200 rootwait root=/dev/sda',
    hda: { url: rootfsUrl },
    autostart: true,
    screen_container: v86ScreenContainer,
    disable_keyboard: true,
    disable_mouse: true,
    virtio_console: true,
  });

  // 8250 (ttyS0) → boot terminal + debug-server stream. v86 fires
  // one byte per event; dbg.pushBytes buffers until newline before
  // shipping each kernel log line.
  //
  // Auto-login: scan the byte stream for "login: " and type
  // "root\n" once. Eliminates the manual login dance every reload
  // — debugging is much smoother when each reload lands at a
  // shell prompt automatically.
  const loginNeedle = new TextEncoder().encode('login: ');
  let loginMatched = false;
  let loginCarry = new Uint8Array(0);
  const tryAutoLogin = (byte: number) => {
    if (loginMatched) return;
    const next = new Uint8Array(loginCarry.length + 1);
    next.set(loginCarry, 0);
    next[loginCarry.length] = byte;
    // Trim carry to needle length max.
    loginCarry = next.length > loginNeedle.length
      ? next.slice(next.length - loginNeedle.length)
      : next;
    if (loginCarry.length === loginNeedle.length) {
      let match = true;
      for (let i = 0; i < loginNeedle.length; i++) {
        if (loginCarry[i] !== loginNeedle[i]) { match = false; break; }
      }
      if (match) {
        loginMatched = true;
        dbg.log('auto-login', 'detected getty banner, sending root');
        setTimeout(() => {
          for (const c of [0x72, 0x6f, 0x6f, 0x74, 0x0a]) { // 'root\n'
            emulator.bus.send('serial0-input', c);
          }
        }, 200);
      }
    }
  };
  emulator.bus.register('serial0-output-byte', (b) => {
    const byte = typeof b === 'number' ? b : (b as Uint8Array)[0];
    const buf = new Uint8Array([byte]);
    boot.write(buf);
    dbg.pushBytes('kernel', buf);
    tryAutoLogin(byte);
  });

  // User input from the boot terminal → ttyS0. The rootfs runs a
  // getty on ttyS0, so clicking the terminal and typing drops you
  // straight into a busybox login shell inside the VM. Each typed
  // UTF-8 char is sent as a single byte via the v86 serial-input
  // event.
  const inputEnc = new TextEncoder();
  const typeBytes = (bytes: Uint8Array) => {
    for (let i = 0; i < bytes.length; i++) {
      emulator.bus.send('serial0-input', bytes[i]);
    }
  };
  boot.onInput((data) => typeBytes(inputEnc.encode(data)));

  // Admin control over the v86 emulator arrives via the WS bus from
  // dbg.ts. CLI: `pnpm cli reload | reset | input "ls\n"`.
  dbg.onMessage((msg) => {
    if (msg.t === 'input') {
      const bytes = new TextEncoder().encode(String(msg.data ?? ''));
      if (bytes.length > 0) {
        dbg.log('input', `typed ${bytes.length} bytes`);
        typeBytes(bytes);
      }
    } else if (msg.t === 'ctl' && typeof msg.verb === 'string') {
      dbg.log('ctl', `verb ${msg.verb}`);
      void applyControl(emulator, msg.verb);
    }
  });

  // virtio-console0 (hvc0 inside the VM) is the wash router bus.
  // Bytes flowing out are wash frames bound for the shell; the
  // wash shell's VirtioConsoleSocket picks them up via the bus we
  // expose on window.washV86Bus. "Ready" is implicit: the first
  // frame arriving here means the router is alive and shipped
  // its catalog. Fade the boot overlay then.
  let firstWashFrame = true;
  let washByteCount = 0;
  emulator.bus.register('virtio-console0-output-bytes', (chunk: unknown) => {
    const n = chunk instanceof Uint8Array ? chunk.length
            : chunk instanceof ArrayBuffer ? chunk.byteLength
            : Array.isArray(chunk) ? chunk.length : 0;
    washByteCount += n;
    if (firstWashFrame) {
      firstWashFrame = false;
      dbg.log('washbus', `first byte(s) on virtio-console0 (n=${n})`);
      setStage('wash ready');
      bootHeaderEl.classList.add('ready');
      setTimeout(() => bootEl.classList.add('hidden'), 400);
    } else if (washByteCount < 10000 && (washByteCount % 256) < n) {
      // Occasional progress markers — helpful when the FE doesn't
      // render but bytes ARE flowing. Throttled to once per ~256B.
      dbg.log('washbus', `cumulative bytes on virtio-console0: ${washByteCount}`);
    }
  });

  // v86 lifecycle + asset events. Surface anything that goes wrong
  // so we don't sit on "starting kernel…" forever.
  emulator.bus.register('emulator-started', () => {
    boot.writeln('v86: emulator started');
    dbg.log('v86', 'emulator-started');
  });
  emulator.bus.register('emulator-stopped', () => {
    boot.writeln('v86: emulator stopped');
    dbg.log('v86', 'emulator-stopped');
  });
  emulator.bus.register('emulator-loaded', () => {
    boot.writeln('v86: assets loaded');
    dbg.log('v86', 'emulator-loaded');
  });
  emulator.bus.register('download-error', (err: any) => {
    const s = `download error: ${JSON.stringify(err)}`;
    boot.writeln(`v86: ${s}`);
    dbg.log('v86', s);
  });

  // Publish the bus so the wash shell can pick it up. The shell's
  // pickTransport() (web/shell/src/main.tsx) reads this when the
  // URL contains ?transport=virtio-console.
  window.washV86Bus = emulator.bus;

  // Load the shell bundle. It self-mounts on import. We append the
  // transport query param so the shell knows to use our virtio bus
  // instead of trying to open a (non-existent) WebSocket.
  await ensureShellTransportParam();
  setStage('loading wash shell…');
  await loadModule('/shell/shell.js');
  setStage('waiting for backend…');
}

// loadScript injects <script src=…> and resolves when it runs.
function loadScript(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = url;
    s.async = false;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error(`failed to load ${url}`));
    document.head.appendChild(s);
  });
}

// loadModule imports a script as an ES module.
async function loadModule(url: string): Promise<void> {
  await import(/* @vite-ignore */ url);
}

// applyControl handles a directive from the /ctl drop-file. Keeping
// this small + deliberately limited — anything that needs more is
// better done via a reload.
async function applyControl(emulator: V86Instance, directive: string): Promise<void> {
  const cmd = directive.toLowerCase();
  switch (cmd) {
    case 'restart':
    case 'reboot': {
      // v86's `restart()` resets the emulated machine in place —
      // re-runs the BIOS + kernel boot path. Faster than a page
      // reload because libv86 and the WASM JIT are already warm.
      if (typeof emulator.restart === 'function') {
        emulator.restart();
      } else {
        emulator.stop();
        emulator.run();
      }
      return;
    }
    case 'stop': {
      emulator.stop();
      return;
    }
    case 'run':
    case 'start': {
      emulator.run();
      return;
    }
    case 'reload-page': {
      // Sometimes restart() leaves the host JS in a weird state
      // (xterm.js holding boot output from before reset). Full
      // browser reload is the nuclear option.
      window.location.reload();
      return;
    }
    default:
      console.warn(`wash demo: unknown control directive: ${directive}`);
  }
}

// ensureShellTransportParam rewrites the URL so the shell picks the
// virtio-console transport. The shell reads window.location.search.
// We can't navigate (would re-run main.ts); instead we mutate
// history. This must happen BEFORE the shell module is imported.
async function ensureShellTransportParam(): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  if (params.get('transport') !== 'virtio-console') {
    params.set('transport', 'virtio-console');
    if (!params.has('port')) params.set('port', '2');
    const newUrl = `${window.location.pathname}?${params.toString()}${window.location.hash}`;
    window.history.replaceState(null, '', newUrl);
  }
}

main().catch((err) => {
  console.error('wash demo: fatal:', err);
  boot.writeln(`fatal: ${String(err)}`);
  setStage('failed');
});
