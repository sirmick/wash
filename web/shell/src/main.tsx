// Browser shell runtime entrypoint. Connects to ws://<host>/ws and
// drives the WM via the messages in WIRE.md §8.
//
// WM state is server-authoritative: the router sends a session.snapshot
// on connect and session.patch on every change. The shell stores the
// state and renders from it. Local pointer interactions emit
// window.move/resize/state/focus back to the router, which applies
// the mutation and broadcasts the patch to all attached shells —
// keeping every browser viewing the session in sync.

import { render } from 'solid-js/web';
import { For, Show, createEffect, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { Conn, type ConnState } from './ws';
import { beginBundle, finishBundle, pushBundleBytes } from './assets';
import { washFetch, handleAssetReadOK, handleAssetReadErr, pushAssetBytes, finishAsset } from './wash-fetch';
import {
  VIEWPORTS_PER_AXIS,
  applySessionPatch,
  applySessionSnapshot,
  dismissCrashed,
  focused,
  markCrashed,
  mountDesktop,
  moveLocal,
  raiseLocal,
  screenSize,
  setViewport,
  viewport,
  viewportFor,
  windows,
} from './wm';
import { Desktop } from './desktop';
import { FloatingWindow } from './window';
import {
  CatalogApp,
  Sub,
  WindowInfo,
  closeRawSubscriber,
  deliverRaw,
  deliverToInstance,
  replaceSavedStates,
  setSavedState,
  subscribeRaw,
} from './api';
import { CreditTracker } from './credit';
import { showToast } from './notify';
import { virtioConsoleFactory } from './virtio';

interface ShellCatalog {
  t: 'catalog';
  apps: CatalogApp[];
}

interface ShellAppDeclared {
  t: 'app.declared';
  instance_id: string;
  element: string;
  surface: 'desktop' | 'window';
  manifest: any;
}

export interface SessionWindow {
  window_id: number;
  instance_id: string;
  element: string;
  icon?: string;
  title: string;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
  state: 'normal' | 'minimized' | 'maximized';
  focused: boolean;
  restore_x?: number;
  restore_y?: number;
  restore_w?: number;
  restore_h?: number;
  // is_root is router-attested (SO_PEERCRED uid==0, or app_id is in
  // the privilege-chain reserved set). When true the WM paints a red
  // stripe + ROOT label on the titlebar. Never set by the app itself.
  is_root?: boolean;
}

interface ShellSessionSnapshot {
  t: 'session.snapshot';
  windows: SessionWindow[];
  app_state?: Record<string, unknown>;
}

export interface SessionPatch {
  op: 'window.upsert' | 'window.delete' | 'app_state';
  window?: SessionWindow;
  window_id?: number;
  instance_id?: string;
  state?: unknown;
}

interface ShellSessionPatch {
  t: 'session.patch';
  patches: SessionPatch[];
}

interface ShellAppMsgDeliver {
  t: 'app_msg.deliver';
  instance_id: string;
  data: unknown;
}

interface ShellChannelBind {
  t: 'channel.bind';
  channel_id: number;
  window_id: number;
  kind?: string;
  instance_id?: string;
}

interface ShellChannelUnbind {
  t: 'channel.unbind';
  channel_id: number;
  reason?: string;
}

// channelOwner records which window an open raw channel is rooted at,
// so the shell can clean up subscribers when the window goes away or
// the router unbinds the channel.
const channelOwner = new Map<number, number>(); // channel_id → window_id

interface ShellNotify {
  t: 'notify';
  instance_id: string;
  title: string;
  body?: string;
  level?: 'info' | 'warn' | 'error';
}

export interface ShellAppCrashed {
  t: 'app.crashed';
  instance_id: string;
  window_id?: number;
  app_id: string;
  exit_code: number;
  signal?: string;
  uptime: string;
  log: string;
}

// Track declared instances so window.create can resolve element by id.
const instances = new Map<string, { element: string; surface: string }>();

// bundleReady is the promise that resolves once an instance's bundle
// has been imported (and customElements.define has run). The
// router can race ShellWindowCreate ahead of the bundle finishing,
// so handleWindowCreate must wait — otherwise document.createElement
// produces an HTMLUnknownElement and connectedCallback never fires.
const bundleReady = new Map<string, Promise<void>>();

// Reactive subs the chrome (mounted via window.wash) listens to.
const catalogSub = new Sub<CatalogApp[]>([]);
const windowsSub = new Sub<WindowInfo[]>([]);
// viewportSub mirrors the Solid viewport signal into the cross-element
// pub/sub the session app subscribes to via window.wash.onViewport.
// We also publish per-window viewport assignments here so the pager
// can draw window outlines in the right cell without re-deriving
// the math FE-side.
const viewportSub = new Sub<{ vx: number; vy: number }>({ vx: 0, vy: 0 });
const screenSub = new Sub<{ w: number; h: number }>({ w: window.innerWidth, h: window.innerHeight });

function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/ws`;
}

/**
 * Pick the wash transport from the URL. Default is a real WebSocket
 * to the router's HTTP listener. `?transport=virtio-console&port=2`
 * routes through the v86 emulator's virtio-console bus events — used
 * by the online demo (PLAN.md Phase 6b). The v86 instance attaches
 * its bus to `window.washV86Bus` before loading the shell.
 */
function pickTransport(): string | (() => import('./ws').SocketFactory extends () => infer S ? S : never) {
  const params = new URLSearchParams(window.location.search);
  const t = params.get('transport');
  if (t !== 'virtio-console') return wsURL();
  const portN = Number(params.get('port') ?? '2');
  const bus = (window as unknown as { washV86Bus?: import('./virtio').V86Bus }).washV86Bus;
  if (!bus) {
    // Fall back to ws if the bus isn't wired yet — the page hasn't
    // booted v86 properly. Log loudly so the demo author notices.
    console.error('wash shell: ?transport=virtio-console requested but window.washV86Bus is undefined; falling back to WebSocket');
    return wsURL();
  }
  // Returning a factory; Conn detects via typeof.
  return virtioConsoleFactory(bus, portN) as any;
}

// Credit tracker for per-channel flow control (QOS.md §5). Bytes
// absorbed on each raw channel count toward a running tally;
// crossing the replenish threshold emits one channel.credit{ch,n}
// frame so the router-side Bulk producer doesn't stall.
//
// The sender closes over `conn`, which is assigned just below. JS
// closure semantics make this safe — the closure isn't *called*
// until the first raw frame arrives, by which point conn is bound.
let conn: Conn;
const creditTracker = new CreditTracker((channelID, n) => {
  conn.sendCtrl({ t: 'channel.credit', ch: channelID, n });
});

conn = new Conn(
  pickTransport() as any,
  (msg) => {
    switch (msg.t) {
      case 'catalog':
        catalogSub.set((msg as ShellCatalog).apps);
        break;
      case 'app.declared':
        handleAppDeclared(msg as ShellAppDeclared);
        break;
      case 'session.snapshot':
        handleSnapshot(msg as ShellSessionSnapshot);
        break;
      case 'session.patch':
        handlePatch(msg as ShellSessionPatch);
        break;
      case 'app_msg.deliver':
        deliverAppMsg(msg as ShellAppMsgDeliver);
        break;
      case 'notify': {
        const n = msg as ShellNotify;
        showToast({
          instanceID: n.instance_id,
          title: n.title,
          body: n.body,
          level: n.level,
        });
        break;
      }
      case 'app.crashed':
        handleCrash(msg as ShellAppCrashed);
        break;
      case 'shell.reload': {
        // Dev-mode signal from the router: a watched binary
        // changed and our embedded bundles are stale. Reload
        // the page so the next shell.js + app bundles fetch
        // fresh. The router's re-exec / app respawn happens
        // independently; we just need to drop our cached state.
        // eslint-disable-next-line no-console
        console.info('wash shell: reload requested by router');
        window.location.reload();
        break;
      }
      case 'channel.bind': {
        const b = msg as ShellChannelBind;
        if (b.kind === 'bundle' && b.instance_id) {
          // Bundle delivery channel — start accumulating until the
          // matching channel.unbind triggers the dynamic import.
          bundleReady.set(b.instance_id, beginBundle(b.channel_id, b.instance_id));
        } else if (b.kind === 'asset') {
          // Asset channel: state lives in wash-fetch.ts, keyed by
          // (req_id, channel_id) via the preceding asset.read.ok.
          // Nothing to do here.
        } else {
          channelOwner.set(b.channel_id, b.window_id);
        }
        break;
      }
      case 'asset.read.ok':
        handleAssetReadOK(msg as { req_id: number; channel_id: number; size: number; mime?: string });
        break;
      case 'asset.read.err':
        handleAssetReadErr(msg as { req_id: number; code: string; msg?: string });
        break;
      case 'channel.unbind': {
        const u = msg as ShellChannelUnbind;
        // Try each accumulator in turn; harmless on miss.
        finishBundle(u.channel_id);
        finishAsset(u.channel_id);
        channelOwner.delete(u.channel_id);
        closeRawSubscriber(u.channel_id);
        // Forget any pending credit count — channel is gone, no
        // point sending credit for a dead id.
        creditTracker.forget(u.channel_id);
        break;
      }
    }
  },
  (channelID, bytes) => {
    // Asset (washFetch) and bundle (kind=bundle) channels divert
    // bytes into their own accumulators; everything else flows to the
    // per-channel raw subscriber (xterm's pty, etc.).
    if (pushAssetBytes(channelID, bytes)) return;
    if (pushBundleBytes(channelID, bytes)) return;
    deliverRaw(channelID, bytes);
    // Bulk-class raw flows (terminal output, file content) drain
    // the router-side credit window — replenish via channel.credit
    // as we absorb. Bundle bytes were returned-early above; those
    // flow Interactive class and bypass the credit ledger on the
    // BE side, so emitting credit for them is a no-op but cheap.
    creditTracker.absorbed(channelID, bytes.length);
  },
);

// deliverAppMsg routes a BE→FE message to its element, queuing if the
// element hasn't mounted yet (Solid's onMount can run after the next
// WS message is processed).
function deliverAppMsg(msg: ShellAppMsgDeliver) {
  deliverToInstance(msg.instance_id, msg.data);
}

// Mirror Solid's windows store into the cross-element Sub so vanilla
// custom elements (the session chrome) can subscribe without taking
// a Solid dep.
createEffect(() => {
  const focusedID = focused();
  const s = screenSize();
  windowsSub.set(
    windows.map((w) => ({
      windowID: w.windowID,
      instanceID: w.instanceID,
      element: w.element,
      icon: w.icon,
      title: w.title,
      focused: focusedID === w.windowID,
      state: w.state,
      x: w.x,
      y: w.y,
      w: w.w,
      h: w.h,
      // viewport: cell where the window's center lives; convenient
      // for the pager + taskbar dblclick snap-to.
      viewport: viewportFor(w),
    })),
  );
  // screenSub also updates here because window list rendering for
  // the pager depends on cell dimensions; cheaper than its own effect.
  if (screenSub.value.w !== s.w || screenSub.value.h !== s.h) {
    screenSub.set(s);
  }
});

createEffect(() => {
  viewportSub.set(viewport());
});

function handleAppDeclared(msg: ShellAppDeclared): void {
  instances.set(msg.instance_id, { element: msg.element, surface: msg.surface });
  // Bundle bytes arrive on a kind=bundle raw channel: channel.bind
  // calls beginBundle() (which records the in-flight promise) and
  // channel.unbind calls finishBundle() (which imports and resolves).
  // For desktop-surface apps we mount the desktop element once the
  // import has completed.
  if (msg.surface === 'desktop') {
    waitForBundleByInstance(msg.instance_id)
      .then(() => mountDesktop({ instanceID: msg.instance_id, element: msg.element }))
      .catch((err) => console.error('wash: desktop bundle:', err));
  }
  // surface=window apps mount on their session.window upsert; the
  // window-create path awaits the bundle promise the same way.
}

// waitForBundleByInstance polls bundleReady — the channel.bind for
// the bundle may arrive slightly after the app.declared, so we wait
// briefly for it to land.
function waitForBundleByInstance(instanceID: string): Promise<void> {
  const existing = bundleReady.get(instanceID);
  if (existing) return existing;
  // The channel.bind {kind:bundle} may not have arrived yet — poll
  // the map until it does. This loop runs ~zero times in practice
  // because the router sends app.declared then ChannelBind back-to-
  // back, but it's defensive against frame-order surprises.
  return new Promise((resolve, reject) => {
    const start = Date.now();
    const check = () => {
      const p = bundleReady.get(instanceID);
      if (p) {
        p.then(resolve, reject);
        return;
      }
      if (Date.now() - start > 10_000) {
        reject(new Error(`bundle for ${instanceID} not announced within 10s`));
        return;
      }
      setTimeout(check, 25);
    };
    check();
  });
}

// handleSnapshot rebuilds the local WM state from the router's
// canonical view. Sent on connect/reconnect. The app_state cache is
// replaced wholesale so stale entries from no-longer-running
// instances don't linger.
function handleSnapshot(msg: ShellSessionSnapshot): void {
  replaceSavedStates(msg.app_state);
  applySessionSnapshot(msg.windows, waitForBundle);
}

// handleCrash marks the matching window crashed in the WM store so
// the FloatingWindow renders the tombstone overlay instead of the
// (dead) custom element. The router still ships a window-delete
// patch right after this; wm.applySessionPatch ignores deletes for
// already-crashed windows so the tombstone survives.
function handleCrash(msg: ShellAppCrashed): void {
  markCrashed(msg.instance_id, {
    appID: msg.app_id,
    exitCode: msg.exit_code,
    signal: msg.signal,
    uptime: msg.uptime,
    log: msg.log,
  });
}

// Tracks windowIDs we've already first-sighted. The wm store can't
// serve this on its own: applySessionPatch defers the upsert for an
// unseen window behind waitForBundle, so a window-in-flight isn't
// in `windows` yet. Without a separate set, every BE patch that
// arrives before the bundle resolves looks "fresh" and we'd
// re-relocate the same window N times.
const seenWindowIDs = new Set<number>();

function handlePatch(msg: ShellSessionPatch): void {
  // Apply app_state ops first so when a window upsert in the same
  // patch triggers a remount, wash:state carries the latest blob.
  for (const p of msg.patches) {
    if (p.op === 'app_state' && typeof p.instance_id === 'string') {
      setSavedState(p.instance_id, p.state ?? null);
    }
  }
  // First-sight detection for viewport auto-relocation: any
  // window.upsert whose id isn't in seenWindowIDs is a new spawn.
  // The router cascades new windows from (40, 40); if the user is
  // looking at a non-(0,0) viewport, we re-issue a window.move so
  // the window appears where they're actually looking. Otherwise
  // new windows silently land off-screen in cell (0,0).
  //
  // We MUTATE the patch's window x/y here (rather than calling
  // moveLocal afterwards) so applySessionPatch's bundle-deferred
  // upsert uses the relocated coords directly — moveLocal on a
  // not-yet-in-store window is a no-op.
  const vp = viewport();
  const s = screenSize();
  const moves: Array<{ id: number; x: number; y: number }> = [];
  for (const p of msg.patches) {
    if (p.op === 'window.upsert' && p.window && !seenWindowIDs.has(p.window.window_id)) {
      if (vp.vx !== 0 || vp.vy !== 0) {
        p.window.x = p.window.x + vp.vx * s.w;
        p.window.y = p.window.y + vp.vy * s.h;
        moves.push({ id: p.window.window_id, x: p.window.x, y: p.window.y });
      }
      seenWindowIDs.add(p.window.window_id);
    }
    if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      seenWindowIDs.delete(p.window_id);
    }
  }
  applySessionPatch(
    msg.patches.filter((p) => p.op !== 'app_state'),
    waitForBundle,
  );
  for (const m of moves) {
    conn.sendCtrl({ t: 'window.move', window_id: m.id, x: m.x, y: m.y });
  }
}

function waitForBundle(instanceID: string): Promise<void> {
  return waitForBundleByInstance(instanceID);
}

// Bridge a window's close-button click into the WS protocol.
// Crashed windows are FE-only tombstones — the router-side state was
// already torn down on abnormal exit, so a close_clicked would have
// nowhere to land. Drop them directly out of the WM store.
function onWindowClose(windowID: number): void {
  const w = windows.find((x) => x.windowID === windowID);
  if (w?.crashed) {
    dismissCrashed(windowID);
    return;
  }
  conn.sendCtrl({ t: 'window.close_clicked', window_id: windowID });
  // The actual removal happens when the router sends window.destroy.
}

const [connState, setConnState] = createSignal<ConnState>('connecting');
conn.onState(setConnState);

// Ctrl+Alt+Arrows pan one viewport. Listening at the document level
// means the chord works regardless of which (if any) window has focus.
// Apps inside windows that want to swallow these keys can preventDefault
// on their own keydown handler — keypresses bubble up to here only when
// nobody else stops them.
window.addEventListener('keydown', (ev: KeyboardEvent) => {
  if (!ev.ctrlKey || !ev.altKey || ev.shiftKey || ev.metaKey) return;
  const vp = viewport();
  let dx = 0;
  let dy = 0;
  switch (ev.key) {
    case 'ArrowLeft':
      dx = -1;
      break;
    case 'ArrowRight':
      dx = 1;
      break;
    case 'ArrowUp':
      dy = -1;
      break;
    case 'ArrowDown':
      dy = 1;
      break;
    default:
      return;
  }
  ev.preventDefault();
  setViewport(vp.vx + dx, vp.vy + dy);
});

// Viewport pan: the cam div translates the windows layer by
// (-vx*W, -vy*H) screen pixels so the user "moves" across a
// VIEWPORTS_PER_AXIS² grid without the router knowing. The Desktop
// surface (taskbar, wallpaper) sits outside this container — it
// stays fixed across viewports, matching X11 viewport semantics.
// pointer-events:none on the cam lets clicks fall through to the
// desktop surface in empty space; floating windows re-enable
// pointer-events on their own frames.
const camStyle = () => {
  const vp = viewport();
  const s = screenSize();
  return {
    position: 'absolute' as const,
    inset: '0',
    transform: `translate(${-vp.vx * s.w}px, ${-vp.vy * s.h}px)`,
    transition: 'transform 260ms cubic-bezier(.2,.7,.2,1)',
    'will-change': 'transform' as const,
    'pointer-events': 'none' as const,
  };
};

const App = () => (
  <>
    <Desktop />
    <div data-testid="wash-cam" style={camStyle()}>
      <For each={windows}>{(w) => <FloatingWindow win={w} onClose={onWindowClose} />}</For>
    </div>
    <ConnectionBanner state={connState()} />
  </>
);

// ConnectionBanner shows a transient status overlay when the WS
// is anything other than open. Lives in the shell (not in an app)
// because if the WS is down, the apps are unreachable anyway.
// Top-center placement so it's visible without covering taskbar
// or window chrome.
const ConnectionBanner: Component<{ state: ConnState }> = (props) => (
  <Show when={props.state !== 'open'}>
    <div
      data-testid="wash-connection-banner"
      data-state={props.state}
      style={{
        position: 'fixed',
        top: '10px',
        left: '50%',
        transform: 'translateX(-50%)',
        background: props.state === 'closed' ? '#5a1a1a' : '#3a2a1a',
        color: '#eee',
        border: `1px solid ${props.state === 'closed' ? '#a04040' : '#a07040'}`,
        'border-radius': '6px',
        padding: '6px 14px',
        font: '12px ui-sans-serif,system-ui,sans-serif',
        'box-shadow': '0 6px 16px rgba(0,0,0,0.5)',
        'z-index': 100000,
        'pointer-events': 'none',
        animation: 'wash-fade-in 200ms ease-out',
      }}
    >
      {props.state === 'connecting' && 'connecting…'}
      {props.state === 'reconnecting' && 'router unreachable — reconnecting…'}
      {props.state === 'closed' && 'disconnected'}
    </div>
  </Show>
);

void conn.ready();
render(App, document.getElementById('root')!);

// Provide a tiny FE-side API for apps that want to send app_msg back
// to their BE half. The session app uses this to send the "launch"
// action. Exposed as window.wash so app bundles can find it without
// import gymnastics.
// Recipient mirrors wire.Recipient — exactly one field set. AppID
// works only for singleton-instancing apps (router spawns on demand
// when not yet running); InstanceID is a direct address.
export type Recipient = { app_id: string } | { instance_id: string };

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      sendAppMsgTo(recipient: Recipient, data: unknown): void;
      catalog(): CatalogApp[];
      onCatalog(cb: (apps: CatalogApp[]) => void): () => void;
      windows(): WindowInfo[];
      onWindowsChanged(cb: (windows: WindowInfo[]) => void): () => void;
      focusWindow(id: number): void;
      closeWindow(id: number): void;
      moveWindow(id: number, x: number, y: number): void;
      resizeWindow(id: number, w: number, h: number): void;
      minimizeWindow(id: number): void;
      maximizeWindow(id: number): void;
      restoreWindow(id: number): void;
      // Virtual-desktop viewport API. The shell pans a viewport-sized
      // camera over a VIEWPORTS_PER_AXIS² plane; setViewport switches
      // cells with a CSS transition. viewportFor returns the cell
      // owning a given window's center (used for taskbar dblclick).
      viewports(): { perAxis: number };
      getViewport(): { vx: number; vy: number };
      setViewport(vx: number, vy: number): void;
      onViewport(cb: (vp: { vx: number; vy: number }) => void): () => void;
      onScreenSize(cb: (s: { w: number; h: number }) => void): () => void;
      log(level: 'error' | 'warn' | 'info' | 'debug', source: string, msg: string, stack?: string): void;
      openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      writeRaw(channelID: number, bytes: Uint8Array): void;
    };
  }
}

type LogLevel = 'error' | 'warn' | 'info' | 'debug';

function shellLog(level: LogLevel, source: string, msg: string, stack?: string) {
  // Best-effort — drop silently if the WS isn't open. Avoid recursing
  // through console.error since we wrap it below.
  try {
    conn.sendCtrl({ t: 'log', level, source, msg, ...(stack ? { stack } : {}) });
  } catch {
    /* ignore */
  }
}

window.wash = {
  sendAppMsg(instanceID, data) {
    conn.sendCtrl({ t: 'app_msg.send', instance_id: instanceID, data });
  },
  sendAppMsgTo(recipient, data) {
    conn.sendCtrl({ t: 'app_msg.send', to: recipient, data });
  },
  catalog: () => catalogSub.value,
  onCatalog: (cb) => catalogSub.on(cb),
  windows: () => windowsSub.value,
  onWindowsChanged: (cb) => windowsSub.on(cb),
  focusWindow(id) {
    // Local raise gives instant visual focus feedback; the router's
    // patch will confirm the z bump moments later.
    raiseLocal(id);
    conn.sendCtrl({ t: 'window.focus', window_id: id });
  },
  closeWindow(id) {
    conn.sendCtrl({ t: 'window.close_clicked', window_id: id });
  },
  moveWindow(id, x, y) {
    conn.sendCtrl({ t: 'window.move', window_id: id, x, y });
  },
  resizeWindow(id, w, h) {
    conn.sendCtrl({ t: 'window.resize', window_id: id, w, h });
  },
  minimizeWindow(id) {
    conn.sendCtrl({ t: 'window.state', window_id: id, state: 'minimized' });
  },
  maximizeWindow(id) {
    conn.sendCtrl({ t: 'window.state', window_id: id, state: 'maximized' });
  },
  restoreWindow(id) {
    conn.sendCtrl({ t: 'window.state', window_id: id, state: 'normal' });
    // Restoring also brings to front + grabs focus.
    conn.sendCtrl({ t: 'window.focus', window_id: id });
  },
  viewports: () => ({ perAxis: VIEWPORTS_PER_AXIS }),
  getViewport: () => viewportSub.value,
  setViewport: (vx, vy) => setViewport(vx, vy),
  onViewport: (cb) => viewportSub.on(cb),
  onScreenSize: (cb) => screenSub.on(cb),
  log(level, source, msg, stack) {
    shellLog(level, source, msg, stack);
  },
  openRawChannel(channelID, onBytes) {
    return subscribeRaw(channelID, onBytes);
  },
  writeRaw(channelID, bytes) {
    conn.sendRaw(channelID, bytes);
  },
};

// Auto-capture browser errors so they show up server-side.
window.addEventListener('error', (ev: ErrorEvent) => {
  const stack = ev.error && ev.error.stack ? String(ev.error.stack) : '';
  const where = ev.filename ? `${ev.filename}:${ev.lineno}:${ev.colno}` : '';
  shellLog('error', 'shell', `${ev.message}${where ? ' @ ' + where : ''}`, stack);
});
window.addEventListener('unhandledrejection', (ev: PromiseRejectionEvent) => {
  const reason = ev.reason as unknown;
  let msg: string;
  let stack = '';
  if (reason instanceof Error) {
    msg = reason.message;
    stack = reason.stack ?? '';
  } else {
    msg = typeof reason === 'string' ? reason : JSON.stringify(reason);
  }
  shellLog('error', 'shell', 'unhandled rejection: ' + msg, stack);
});

// Mirror every console.* level so app code's traces land server-side
// too — invaluable when debugging an FE flow whose only visible
// symptom is "nothing happened." The original console output is kept
// so the dev tools view is unchanged.
function mirrorConsole(method: 'error' | 'warn' | 'log' | 'info' | 'debug', level: LogLevel): void {
  const orig = (console[method] as (...a: unknown[]) => void).bind(console);
  console[method] = (...args: unknown[]) => {
    orig(...args);
    shellLog(level, 'console', args.map(stringifyArg).join(' '));
  };
}
mirrorConsole('error', 'error');
mirrorConsole('warn', 'warn');
mirrorConsole('log', 'info');
mirrorConsole('info', 'info');
mirrorConsole('debug', 'debug');

function stringifyArg(a: unknown): string {
  if (a instanceof Error) return a.stack ?? a.message;
  if (typeof a === 'string') return a;
  try {
    return JSON.stringify(a);
  } catch {
    return String(a);
  }
}
