// wash-app-vscode-workbench — the embedded VS Code workbench window.
// Asks the daemon (via its BE relay) to ensure code-server is running,
// receives { path, folder }, and renders the workbench in an iframe at
// path?folder=<folder>. One per opened folder; all share the daemon's
// single code-server.
//
// It remembers its folder across server restarts: an upgrade restarts
// code-server and the daemon broadcasts a folderless ready{path}; the
// window re-applies its stored folder so it reloads the same workspace.

import { Match, Switch, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Button, IngressFrame, defineWashApp, tokens } from '@wash/ui';

type Phase = 'launching' | 'ready' | 'absent' | 'error';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [phase, setPhase] = createSignal<Phase>('launching');
  const [path, setPath] = createSignal('');
  const [folder, setFolder] = createSignal('');
  const [error, setError] = createSignal('');

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const src = createMemo(() => {
    const p = path();
    if (!p) return '';
    const f = folder();
    return f ? `${p}?folder=${encodeURIComponent(f)}` : p;
  });

  const handleBE = (m: any) => {
    switch (m?.kind) {
      case 'ready':
        setPath(m.path);
        if (m.folder) setFolder(m.folder); // keep prior folder on folderless (restart) readys
        setPhase('ready');
        break;
      case 'status':
        // Not installed yet — point the user at the manager.
        if (!m.installed && phase() !== 'ready') setPhase('absent');
        break;
      case 'shutdown': {
        // The manager is quitting — close this window too.
        const win = window.wash.windows().find((w) => w.instanceID === props.instance);
        if (win) window.wash.closeWindow(win.windowID);
        break;
      }
      case 'exited':
        setError(m.reason || 'code-server exited');
        setPhase('error');
        break;
      case 'error':
        setError(m.msg || 'unknown error');
        setPhase('error');
        break;
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    send({ kind: 'ensure' });
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
  });

  const retry = () => { setError(''); setPhase('launching'); send({ kind: 'ensure' }); };

  return (
    <Switch>
      <Match when={phase() === 'ready'}>
        <IngressFrame path={src()} host={props.host} title="VS Code" />
      </Match>
      <Match when={phase() === 'launching'}>
        <Centered><Spinner /><p style={bodyStyle}>Starting VS Code…</p></Centered>
      </Match>
      <Match when={phase() === 'absent'}>
        <Centered>
          <p style={bodyStyle}>VS Code isn't installed yet. Open the <strong>VS Code</strong> app to install it.</p>
        </Centered>
      </Match>
      <Match when={phase() === 'error'}>
        <Centered>
          <p style={{ ...bodyStyle, color: '#e06c75', 'white-space': 'pre-wrap' }}>{error()}</p>
          <Button onClick={retry}>Try again</Button>
        </Centered>
      </Match>
    </Switch>
  );
};

const Centered: Component<{ children: JSX.Element }> = (props) => (
  <div style={{ width: '100%', height: '100%', display: 'flex', 'flex-direction': 'column', 'align-items': 'center', 'justify-content': 'center', gap: '12px', padding: '24px', 'box-sizing': 'border-box', 'text-align': 'center' }}>
    {props.children}
  </div>
);

const Spinner: Component = () => (
  <div style={{ width: '28px', height: '28px', border: '3px solid rgba(255,255,255,0.2)', 'border-top-color': '#007acc', 'border-radius': '50%', animation: 'wash-vscode-spin 0.8s linear infinite' }} />
);

const bodyStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  color: tokens.fg,
  opacity: 0.85,
  'line-height': 1.5,
  'max-width': '440px',
  margin: 0,
};

const style = document.createElement('style');
style.textContent = '@keyframes wash-vscode-spin{to{transform:rotate(360deg)}}';
document.head.appendChild(style);

defineWashApp('wash-app-vscode-workbench', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:#1e1e1e;color:${tokens.fg};box-sizing:border-box`,
});
