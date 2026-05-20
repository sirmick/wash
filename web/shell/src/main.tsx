// Browser shell runtime entrypoint. Connects to ws://<host>/ws and
// drives the WM via the messages in WIRE.md §8.

import { render } from 'solid-js/web';
import { For, createEffect } from 'solid-js';
import { Conn } from './ws';
import { fetchAndImport, onAssetDeliver } from './assets';
import {
  addWindow,
  focused,
  mountDesktop,
  raise,
  removeWindow,
  setTitle,
  windows,
} from './wm';
import { Desktop } from './desktop';
import { FloatingWindow } from './window';
import { CatalogApp, Sub, WindowInfo } from './api';

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

interface ShellWindowCreate {
  t: 'window.create';
  window_id: number;
  instance_id: string;
  title: string;
  w: number;
  h: number;
}

interface ShellWindowDestroy {
  t: 'window.destroy';
  window_id: number;
}

interface ShellWindowTitle {
  t: 'window.title';
  window_id: number;
  title: string;
}

interface ShellAssetDeliver {
  t: 'asset.deliver';
  instance_id: string;
  name: string;
  bytes: string;
  end: boolean;
  mime?: string;
}

// Track declared instances so window.create can resolve element by id.
const instances = new Map<string, { element: string; surface: string }>();

// Reactive subs the chrome (mounted via window.wash) listens to.
const catalogSub = new Sub<CatalogApp[]>([]);
const windowsSub = new Sub<WindowInfo[]>([]);

function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/ws`;
}

const conn = new Conn(wsURL(), (msg) => {
  switch (msg.t) {
    case 'catalog':
      catalogSub.set((msg as ShellCatalog).apps);
      break;
    case 'app.declared':
      handleAppDeclared(msg as ShellAppDeclared);
      break;
    case 'window.create':
      handleWindowCreate(msg as ShellWindowCreate);
      break;
    case 'window.destroy':
      removeWindow((msg as ShellWindowDestroy).window_id);
      break;
    case 'window.title':
      setTitle((msg as ShellWindowTitle).window_id, (msg as ShellWindowTitle).title);
      break;
    case 'asset.deliver':
      onAssetDeliver(msg as ShellAssetDeliver);
      break;
  }
});

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
    })),
  );
});

function handleAppDeclared(msg: ShellAppDeclared): void {
  instances.set(msg.instance_id, { element: msg.element, surface: msg.surface });
  if (msg.surface === 'desktop') {
    // Fetch the bundle, then mount.
    fetchAndImport(conn.sendCtrl.bind(conn), msg.instance_id, 'index.js')
      .then(() => mountDesktop({ instanceID: msg.instance_id, element: msg.element }))
      .catch((err) => console.error('wash: desktop bundle:', err));
  } else {
    // surface=window: wait for window.create, but pre-fetch the bundle.
    fetchAndImport(conn.sendCtrl.bind(conn), msg.instance_id, 'index.js').catch((err) =>
      console.error('wash: window bundle:', err),
    );
  }
}

function handleWindowCreate(msg: ShellWindowCreate): void {
  const inst = instances.get(msg.instance_id);
  if (!inst) {
    console.warn('wash: window.create for unknown instance', msg.instance_id);
    return;
  }
  addWindow({
    windowID: msg.window_id,
    instanceID: msg.instance_id,
    element: inst.element,
    title: msg.title || 'wash',
    w: msg.w || 480,
    h: msg.h || 320,
  });
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
      log(level: 'error' | 'warn' | 'info' | 'debug', source: string, msg: string, stack?: string): void;
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
    raise(id);
    conn.sendCtrl({ t: 'window.focus', window_id: id });
  },
  closeWindow(id) {
    conn.sendCtrl({ t: 'window.close_clicked', window_id: id });
  },
  log(level, source, msg, stack) {
    shellLog(level, source, msg, stack);
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
