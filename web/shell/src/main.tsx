// Browser shell runtime entrypoint. Connects to ws://<host>/ws and
// drives the WM via the messages in WIRE.md §8.

import { render } from 'solid-js/web';
import { For } from 'solid-js';
import { Conn } from './ws';
import { fetchAndImport, onAssetDeliver } from './assets';
import {
  addWindow,
  mountDesktop,
  removeWindow,
  setTitle,
  unmountDesktop,
  windows,
} from './wm';
import { Desktop } from './desktop';
import { FloatingWindow } from './window';

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

function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/ws`;
}

const conn = new Conn(wsURL(), (msg) => {
  switch (msg.t) {
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
    };
  }
}

window.wash = {
  sendAppMsg(instanceID, data) {
    conn.sendCtrl({ t: 'app_msg.send', instance_id: instanceID, data });
  },
};
