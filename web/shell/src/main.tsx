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
import { For, createEffect } from 'solid-js';
import { Conn } from './ws';
import { fetchAndImport, onAssetDeliver } from './assets';
import {
  applySessionPatch,
  applySessionSnapshot,
  focused,
  mountDesktop,
  raiseLocal,
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
import { showToast } from './notify';

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

interface ShellAssetDeliver {
  t: 'asset.deliver';
  instance_id: string;
  name: string;
  bytes: string;
  end: boolean;
  mime?: string;
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

function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/ws`;
}

const conn = new Conn(
  wsURL(),
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
      case 'asset.deliver':
        onAssetDeliver(msg as ShellAssetDeliver);
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
      case 'channel.bind': {
        const b = msg as ShellChannelBind;
        channelOwner.set(b.channel_id, b.window_id);
        break;
      }
      case 'channel.unbind': {
        const u = msg as ShellChannelUnbind;
        channelOwner.delete(u.channel_id);
        closeRawSubscriber(u.channel_id);
        break;
      }
    }
  },
  (channelID, bytes) => deliverRaw(channelID, bytes),
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
  windowsSub.set(
    windows.map((w) => ({
      windowID: w.windowID,
      instanceID: w.instanceID,
      element: w.element,
      title: w.title,
      focused: focusedID === w.windowID,
      state: w.state,
    })),
  );
});

function handleAppDeclared(msg: ShellAppDeclared): void {
  instances.set(msg.instance_id, { element: msg.element, surface: msg.surface });
  const p = fetchAndImport(conn.sendCtrl.bind(conn), msg.instance_id, 'index.js');
  bundleReady.set(msg.instance_id, p);
  if (msg.surface === 'desktop') {
    p.then(() => mountDesktop({ instanceID: msg.instance_id, element: msg.element })).catch((err) =>
      console.error('wash: desktop bundle:', err),
    );
  }
  // surface=window apps are mounted when their session.upsert lands;
  // we wait for bundleReady there so the element class is defined
  // before onMount calls createElement.
}

// handleSnapshot rebuilds the local WM state from the router's
// canonical view. Sent on connect/reconnect. The app_state cache is
// replaced wholesale so stale entries from no-longer-running
// instances don't linger.
function handleSnapshot(msg: ShellSessionSnapshot): void {
  replaceSavedStates(msg.app_state);
  applySessionSnapshot(msg.windows, waitForBundle);
}

function handlePatch(msg: ShellSessionPatch): void {
  // Apply app_state ops first so when a window upsert in the same
  // patch triggers a remount, wash:state carries the latest blob.
  for (const p of msg.patches) {
    if (p.op === 'app_state' && typeof p.instance_id === 'string') {
      setSavedState(p.instance_id, p.state ?? null);
    }
  }
  applySessionPatch(
    msg.patches.filter((p) => p.op !== 'app_state'),
    waitForBundle,
  );
}

function waitForBundle(instanceID: string): Promise<void> {
  return bundleReady.get(instanceID) ?? Promise.resolve();
}

// Bridge a window's close-button click into the WS protocol.
function onWindowClose(windowID: number): void {
  conn.sendCtrl({ t: 'window.close_clicked', window_id: windowID });
  // The actual removal happens when the router sends window.destroy.
}

const App = () => (
  <>
    <Desktop />
    <For each={windows}>{(w) => <FloatingWindow win={w} onClose={onWindowClose} />}</For>
  </>
);

void conn.ready();
render(App, document.getElementById('root')!);

// Provide a tiny FE-side API for apps that want to send app_msg back
// to their BE half. The session app uses this to send the "launch"
// action. Exposed as window.wash so app bundles can find it without
// import gymnastics.
declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
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
      saveState(instanceID: string, state: unknown): void;
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
  saveState(instanceID, state) {
    conn.sendCtrl({ t: 'app_state.save', instance_id: instanceID, state });
  },
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

// Mirror console.error / console.warn so app code's complaints land
// server-side too. We keep the original console output so the dev
// tools view is unchanged.
const origError = console.error.bind(console);
console.error = (...args: unknown[]) => {
  origError(...args);
  shellLog('error', 'console', args.map(stringifyArg).join(' '));
};
const origWarn = console.warn.bind(console);
console.warn = (...args: unknown[]) => {
  origWarn(...args);
  shellLog('warn', 'console', args.map(stringifyArg).join(' '));
};

function stringifyArg(a: unknown): string {
  if (a instanceof Error) return a.stack ?? a.message;
  if (typeof a === 'string') return a;
  try {
    return JSON.stringify(a);
  } catch {
    return String(a);
  }
}
