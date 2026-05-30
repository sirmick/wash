// wash-app-vscode — the VS Code manager (control panel).
//
// Shows code-server's status (version, update availability, running),
// runs install/upgrade as a live PTY stream in an xterm (package-
// manager style), exposes Start/Stop, and launches workbench windows
// at a folder chosen via a directory picker. Everything is mediated by
// the daemon through this window's BE relay.

import { Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Button, ConfirmDialog, FilePicker, Terminal, defineWashApp, tokens } from '@wash/ui';
import type { TerminalAPI } from '@wash/ui';

interface Status {
  installed: boolean;
  version: string;
  managed: boolean;
  arch_ok: boolean;
  latest: string;
  running: boolean;
  path: string;
  installing: boolean;
}

type View = 'panel' | 'installing' | 'error';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [view, setView] = createSignal<View>('panel');
  const [status, setStatus] = createSignal<Status | null>(null);
  const [error, setError] = createSignal('');
  const [pickerOpen, setPickerOpen] = createSignal(false);
  const [confirmClose, setConfirmClose] = createSignal(false);

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  let termAPI: TerminalAPI | null = null;
  let logPending: Uint8Array[] = [];
  const writeLog = (b: Uint8Array) => (termAPI ? termAPI.write(b) : logPending.push(b));
  const attachTerm = (api: TerminalAPI) => {
    termAPI = api;
    for (const b of logPending) api.write(b);
    logPending = [];
  };

  const handleBE = (m: any) => {
    switch (m?.kind) {
      case 'status':
        setStatus(m as Status);
        if ((m as Status).installing) setView('installing');
        else if (view() === 'installing' && !(m as Status).installing) {
          /* keep terminal visible until ready/error resolves it */
        }
        break;
      case 'log':
        setView('installing');
        writeLog(base64ToBytes(m.bytes));
        break;
      case 'ready':
        // install/upgrade finished (or a server start) — back to panel.
        if (view() === 'installing') setView('panel');
        break;
      case 'error':
        setError(m.msg || 'unknown error');
        setView('error');
        break;
      case 'confirm_close':
        setConfirmClose(true);
        break;
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    send({ kind: 'status' });
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
  });

  const install = () => { setView('installing'); termAPI?.write('\x1bc'); send({ kind: 'install' }); };
  const update = () => { setView('installing'); termAPI?.write('\x1bc'); send({ kind: 'update' }); };
  const start = () => send({ kind: 'ensure' });
  const stop = () => send({ kind: 'stop' });
  const openFolderPicker = () => setPickerOpen(true);
  const onPickFolder = (folder: string) => {
    setPickerOpen(false);
    send({ kind: 'open_window', folder });
  };

  const s = () => status();
  const canUpdate = () => {
    const st = s();
    return !!(st && st.managed && st.latest && st.version && st.latest !== st.version);
  };
  const versionLabel = createMemo(() => {
    const st = s();
    if (!st) return 'checking…';
    if (!st.installed) return 'not installed';
    return `v${st.version}${st.running ? ' · running' : ' · stopped'}`;
  });

  return (
    <div style={shellStyle}>
      <div style={headerStyle}>
        <span style={{ font: `600 ${tokens.fontSizeMd} ${tokens.fontSans}` }}>VS Code</span>
        <span style={{ opacity: 0.65, font: `${tokens.fontSizeSm} ${tokens.fontMono}` }}>{versionLabel()}</span>
      </div>

      <Show when={view() === 'installing'}>
        <div style={{ flex: 1, 'min-height': 0, display: 'flex', 'flex-direction': 'column' }}>
          <div style={subHeaderStyle}>Working…</div>
          <div style={{ flex: 1, 'min-height': 0 }}>
            <Terminal onReady={attachTerm} />
          </div>
        </div>
      </Show>

      <Show when={view() === 'error'}>
        <div style={bodyPadStyle}>
          <p style={{ ...bodyStyle, color: '#e06c75', 'white-space': 'pre-wrap' }}>{error()}</p>
          <Button onClick={() => { setError(''); setView('panel'); send({ kind: 'status' }); }}>Back</Button>
        </div>
      </Show>

      <Show when={view() === 'panel'}>
        <div style={bodyPadStyle}>
          <Show when={s()} fallback={<p style={bodyStyle}>Checking for code-server…</p>}>
            <Show
              when={s()!.installed}
              fallback={
                <Show
                  when={s()!.arch_ok}
                  fallback={<p style={bodyStyle}>No prebuilt code-server for this architecture. Install <code>code-server</code> manually (it'll be picked up from PATH).</p>}
                >
                  <p style={bodyStyle}>code-server isn't installed yet. wash will download it (~120&nbsp;MB, bundles its own Node) and show the install live.</p>
                  <Button onClick={install}>Install VS Code</Button>
                </Show>
              }
            >
              <Show when={canUpdate()}>
                <div style={updateRowStyle}>
                  <span>Update available: v{s()!.latest}</span>
                  <Button size="sm" onClick={update}>Update</Button>
                </div>
              </Show>

              <p style={bodyStyle}>Open a folder in a new VS Code window — all windows share one code-server.</p>
              <Button onClick={openFolderPicker}>Open Folder…</Button>

              <div style={{ display: 'flex', gap: '8px', 'margin-top': '8px' }}>
                <Show when={!s()!.running}>
                  <Button variant="ghost" size="sm" onClick={start}>Start server</Button>
                </Show>
                <Show when={s()!.running}>
                  <Button variant="ghost" size="sm" onClick={stop}>Stop server</Button>
                </Show>
              </div>
            </Show>
          </Show>
        </div>
      </Show>

      <FilePicker
        open={pickerOpen()}
        mode="directory"
        host={props.host}
        hostInstanceID={props.instance}
        onConfirm={onPickFolder}
        onCancel={() => setPickerOpen(false)}
        data-testid="vscode-folder-picker"
      />

      <Show when={confirmClose()}>
        <ConfirmDialog
          title="Quit VS Code?"
          confirmLabel="Quit"
          onConfirm={() => { setConfirmClose(false); send({ kind: 'force_quit' }); }}
          onCancel={() => setConfirmClose(false)}
          data-testid="vscode-confirm-quit"
          confirmTestid="vscode-quit-btn"
        >
          This stops code-server and closes any open VS Code windows.
        </ConfirmDialog>
      </Show>
    </div>
  );
};

// ----- styles -----

const shellStyle: JSX.CSSProperties = {
  width: '100%', height: '100%', display: 'flex', 'flex-direction': 'column',
  background: tokens.bgWindow, color: tokens.fg, 'box-sizing': 'border-box',
};
const headerStyle: JSX.CSSProperties = {
  display: 'flex', 'align-items': 'baseline', gap: '10px', padding: '10px 14px',
  'border-bottom': '1px solid rgba(255,255,255,0.08)',
};
const subHeaderStyle: JSX.CSSProperties = {
  padding: '6px 14px', font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`, opacity: 0.7,
};
const bodyPadStyle: JSX.CSSProperties = {
  padding: '16px 14px', display: 'flex', 'flex-direction': 'column', gap: '12px', 'align-items': 'flex-start',
};
const bodyStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`, color: tokens.fg, opacity: 0.85, 'line-height': 1.5, margin: 0,
};
const updateRowStyle: JSX.CSSProperties = {
  display: 'flex', 'align-items': 'center', gap: '10px', padding: '6px 10px',
  background: 'rgba(0,122,204,0.15)', 'border-radius': '6px', font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
};

function base64ToBytes(str: string): Uint8Array {
  const bin = atob(str);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

defineWashApp('wash-app-vscode', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};box-sizing:border-box`,
});
