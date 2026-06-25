// wash-app-vscode-workbench — the VS Code editor window. On cold launch
// it prompts for a folder (FilePicker, directory mode); on a refresh it
// restores the folder from wash:state and skips the prompt. Once a
// folder is chosen it asks the wash-vscode service (via its BE relay) to
// ensure code-server is running, receives { path }, and renders the
// workbench in an iframe at path?folder=<folder>.
//
// It remembers its folder across server restarts: an upgrade restarts
// code-server and the service broadcasts a fresh ready{path}; the window
// re-applies its stored folder so it reloads the same workspace.

import { Match, Switch, createMemo, createSignal } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Button, FilePicker, IngressFrame, createAppBus, defineWashApp, tokens } from '@wash/ui';

type Phase = 'choosing' | 'launching' | 'ready' | 'absent' | 'error';

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
  // Cold launch starts at the folder prompt. A wash:state restore (on
  // refresh) flips us straight to launching with the saved folder.
  const [phase, setPhase] = createSignal<Phase>('choosing');
  const [path, setPath] = createSignal('');
  const [folder, setFolder] = createSignal('');
  const [error, setError] = createSignal('');

  const src = createMemo(() => {
    const p = path();
    if (!p) return '';
    const f = folder();
    return f ? `${p}?folder=${encodeURIComponent(f)}` : p;
  });

  // openFolder is the one entry into "bring code-server up for this
  // folder": persist the choice (so a refresh skips the picker) and
  // ask the service to ensure the server.
  const openFolder = (f: string) => {
    setFolder(f);
    setError('');
    setPhase('launching');
    send({ kind: 'save_folder', folder: f });
    send({ kind: 'ensure' });
  };

  const handleBE = (m: any) => {
    switch (m?.kind) {
      case 'ready':
        setPath(m.path);
        setPhase('ready');
        break;
      case 'status':
        // Not installed yet — point the user at Settings.
        if (!m.installed && phase() !== 'ready') setPhase('absent');
        break;
      case 'shutdown': {
        // The service is going away — close this window too. Address our own
        // window by (origin, id): props.instance is the origin-tagged compound
        // id, which never matches the bare window-list instanceID for a remote
        // instance (docs/REMOTE.md R2). data-wash-window is our bare id.
        const id = Number(props.host.getAttribute('data-wash-window') || 0);
        if (id) window.wash.closeWindow(id, props.origin);
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

  // wash:state restores the folder this window opened last time.
  // Fires on (re)mount when the BE has a persisted blob. Treat a
  // missing folder as "stay on the picker".
  const handleState = (s: { folder?: string } | null) => {
    if (s && typeof s.folder === 'string' && s.folder && phase() === 'choosing') {
      openFolder(s.folder);
    }
  };

  const { send } = createAppBus(props, {
    onMsg: handleBE,
    onState: (s) => handleState(s as { folder?: string } | null),
  });

  const retry = () => {
    setError('');
    if (folder()) {
      setPhase('launching');
      send({ kind: 'ensure' });
    } else {
      setPhase('choosing');
    }
  };

  const closeSelf = () => {
    // (origin, id) — see the shutdown handler above; props.instance is the
    // compound id, data-wash-window our bare window id.
    const id = Number(props.host.getAttribute('data-wash-window') || 0);
    if (id) window.wash.closeWindow(id, props.origin);
  };

  return (
    <Switch>
      <Match when={phase() === 'ready'}>
        <IngressFrame path={src()} host={props.host} title="VS Code" />
      </Match>
      <Match when={phase() === 'choosing'}>
        <FilePicker
          open={true}
          mode="directory"
          host={props.host}
          hostInstanceID={props.instance}
          onConfirm={(p) => openFolder(p)}
          onCancel={closeSelf}
          data-testid="vscode-folder-picker"
        />
      </Match>
      <Match when={phase() === 'launching'}>
        <Centered><Spinner /><p style={bodyStyle}>Starting VS Code…</p></Centered>
      </Match>
      <Match when={phase() === 'absent'}>
        <Centered>
          <p style={bodyStyle}>
            VS Code isn't installed yet. Open <strong>Settings → Developer</strong> to install it.
          </p>
        </Centered>
      </Match>
      <Match when={phase() === 'error'}>
        <Centered>
          <p style={{ ...bodyStyle, color: tokens.fgDanger, 'white-space': 'pre-wrap' }}>{error()}</p>
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
  // #007acc is the VS Code brand blue — kept literal so the spinner
  // matches the embedded editor rather than wash's theme tokens.
  <div style={{ width: '28px', height: '28px', border: '3px solid rgba(255,255,255,0.2)', 'border-top-color': '#007acc', 'border-radius': '50%', animation: 'wash-vscode-spin 0.8s linear infinite' }} />
);

const bodyStyle: JSX.CSSProperties = {
  font: tokens.type.textMd,
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
  // #1e1e1e is the VS Code editor background — kept literal so the
  // window chrome matches the embedded iframe, not wash's theme tokens.
  style: `display:block;width:100%;height:100%;overflow:hidden;background:#1e1e1e;color:${tokens.fg};box-sizing:border-box`,
});
