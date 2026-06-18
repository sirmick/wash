// Developer settings panel — controls the wash-vscode background
// service: install / update code-server, start / stop it, and a hard
// restart. Host-rendered by the settings app over the service's
// status / log pushes (relayed as svc.recv through the panel port).
//
// This bundle (panel.js) ships embedded in the wash-vscode binary and
// is loaded on demand by the settings host (docs/SETTINGS.md). It was
// extracted verbatim from the settings monolith's DeveloperPane.

import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { JSX } from 'solid-js';
import {
  Row,
  Section,
  ServiceBadge,
  SmallBtn,
  defineSettingsPanel,
  tokens,
} from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';
import { RotateCcw, Play, Square as StopIcon, Download, CircleAlert } from 'lucide-solid';

// vscode service status snapshot (apps/vscode/be statusPayload).
interface VscodeStatus {
  installed: boolean;
  version: string;
  managed: boolean;
  arch_ok: boolean;
  latest: string;
  running: boolean;
  installing: boolean;
}

// decodeUtf8 turns a base64 chunk (the service's log bytes) into text.
// Best-effort: malformed input yields "".
function decodeUtf8(b64: string): string {
  try {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new TextDecoder().decode(bytes);
  } catch {
    return '';
  }
}

const Panel = (props: SettingsPanelProps) => {
  const port = props.port;
  const [status, setStatus] = createSignal<VscodeStatus | null>(null);
  const [log, setLog] = createSignal('');

  onMount(() => {
    const off = port.onMessage((p) => {
      switch (p.kind) {
        case 'status':
          setStatus(p as unknown as VscodeStatus);
          return;
        case 'log': {
          const b64 = (p as { bytes?: string }).bytes;
          if (b64) setLog((cur) => (cur + decodeUtf8(b64)).slice(-8000));
          return;
        }
        case 'error':
          setLog((cur) => cur + `\n[error] ${(p as { msg?: string }).msg ?? ''}\n`);
          return;
        // ready / exited only matter to workbench windows; ignore here.
      }
    });
    port.send({ kind: 'subscribe' });
    onCleanup(() => {
      port.send({ kind: 'unsubscribe' });
      off();
    });
  });

  const s = () => status();
  const updatable = () => {
    const v = s();
    return !!v && v.installed && !!v.latest && v.latest !== v.version;
  };

  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="VS Code (code-server)">
        <Show when={s()} fallback={<div style={{ opacity: 0.6 }}>Connecting to service…</div>}>
          <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
            <Row label="Status">
              <ServiceBadge
                tone={!s()!.installed ? 'absent' : s()!.installing ? 'busy' : s()!.running ? 'on' : 'off'}
                label={
                  !s()!.installed
                    ? 'not installed'
                    : s()!.installing
                      ? 'installing…'
                      : s()!.running
                        ? 'running'
                        : 'stopped'
                }
              />
            </Row>
            <Show when={s()!.installed}>
              <Row label="Version">
                <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>
                  {s()!.version || '—'}
                  <Show when={updatable()}>
                    <span style={{ opacity: 0.6 }}> → {s()!.latest} available</span>
                  </Show>
                </span>
              </Row>
            </Show>
            <Show when={!s()!.arch_ok}>
              <div style={warnStyle}>
                <CircleAlert size={13} /> Unsupported CPU architecture for code-server.
              </div>
            </Show>
            <div style={{ display: 'flex', gap: '6px', 'flex-wrap': 'wrap' }}>
              <Show when={!s()!.installed && s()!.arch_ok}>
                <SmallBtn onClick={() => port.send({ kind: 'install', version: '' })} data-testid="dev-install">
                  <Download size={14} /> Install
                </SmallBtn>
              </Show>
              <Show when={updatable()}>
                <SmallBtn onClick={() => port.send({ kind: 'update' })} data-testid="dev-update">
                  <Download size={14} /> Update to {s()!.latest}
                </SmallBtn>
              </Show>
              <Show when={s()!.installed}>
                <Show
                  when={s()!.running}
                  fallback={
                    <SmallBtn onClick={() => port.send({ kind: 'start' })} data-testid="dev-start">
                      <Play size={14} /> Start
                    </SmallBtn>
                  }
                >
                  <SmallBtn onClick={() => port.send({ kind: 'stop' })} data-testid="dev-stop">
                    <StopIcon size={14} /> Stop
                  </SmallBtn>
                </Show>
                <SmallBtn onClick={() => port.restart()} data-testid="dev-restart">
                  <RotateCcw size={14} /> Restart
                </SmallBtn>
              </Show>
            </div>
          </div>
        </Show>
      </Section>

      <Show when={log()}>
        <Section title="Install log">
          <pre data-testid="dev-log" style={logStyle}>{log()}</pre>
        </Section>
      </Show>
    </div>
  );
};

const logStyle: JSX.CSSProperties = {
  margin: 0,
  'max-height': '180px',
  overflow: 'auto',
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  padding: '8px 10px',
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgDim,
  'white-space': 'pre-wrap',
  'word-break': 'break-word',
};

const warnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  color: tokens.fgDanger,
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

defineSettingsPanel('wash-settings-panel-vscode', Panel);
