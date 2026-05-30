// NetWidget renders the com.wash.netd status inside the sidebar (docs/NET.md
// §2.11, §3, B1d). netd publishes {status, phase, summary, diagnostics} via its
// StateService; the session BE re-brands the push to "net.state" and the App
// feeds it here. The widget surfaces the current posture and — critically — the
// await-confirm state, the home of the commit-confirm "you're about to be
// locked out" prompt (§7; the live countdown lands with the apply terminal in
// B3). "Configure…" launches the windowed com.wash.net app.
//
// Pure renderer — subscription wiring lives in the App.

import type { Component } from 'solid-js';
import { For, Show } from 'solid-js';

export interface NetDiag {
  path: string;
  code: string;
  message: string;
  severity: string | number;
}

export interface NetState {
  status: string; // idle | await-confirm | committed | reverted | failed
  phase?: string;
  summary?: string[];
  diagnostics?: NetDiag[];
}

export interface NetWidgetProps {
  state: () => NetState | null;
  onConfigure: () => void;
}

function statusColor(status: string): string {
  switch (status) {
    case 'await-confirm':
      return '#d0a040';
    case 'committed':
      return '#3aa050';
    case 'reverted':
    case 'failed':
      return '#a02d2d';
    default:
      return '#7a7a8a';
  }
}

function isErr(d: NetDiag): boolean {
  return d.severity === 'error' || d.severity === 0;
}

export const NetWidget: Component<NetWidgetProps> = (props) => {
  const st = () => props.state();
  const status = () => st()?.status ?? 'idle';
  const summary = () => st()?.summary ?? [];
  const errs = () => (st()?.diagnostics ?? []).filter(isErr);
  const awaiting = () => status() === 'await-confirm';

  return (
    <div
      data-testid="net-widget"
      data-status={status()}
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px', 'font-size': '11px' }}
    >
      <div style={{ display: 'flex', 'align-items': 'center', gap: '6px' }}>
        <span
          data-testid="net-status"
          style={{
            width: '8px',
            height: '8px',
            'border-radius': '50%',
            background: statusColor(status()),
            'flex-shrink': 0,
          }}
        />
        <span style={{ color: '#ddd', flex: 1 }}>{status()}</span>
        <Show when={st()?.phase}>
          <span style={{ opacity: 0.6, font: '10px ui-monospace,Menlo,Consolas,monospace' }}>{st()!.phase}</span>
        </Show>
      </div>

      <Show when={awaiting() && summary().length > 0}>
        <div
          data-testid="net-pending"
          style={{
            padding: '6px 8px',
            background: 'rgba(208,160,64,0.08)',
            border: '1px solid #4a4030',
            'border-radius': '3px',
            display: 'flex',
            'flex-direction': 'column',
            gap: '3px',
          }}
        >
          <span style={{ color: '#d0a040' }}>awaiting confirmation</span>
          <For each={summary()}>
            {(line) => (
              <span style={{ opacity: 0.85, font: '10px ui-monospace,Menlo,Consolas,monospace' }}>{line}</span>
            )}
          </For>
        </div>
      </Show>

      <Show when={errs().length > 0}>
        <span data-testid="net-errcount" style={{ color: '#e0a0a0' }}>
          {errs().length} validation error{errs().length === 1 ? '' : 's'}
        </span>
      </Show>

      <button
        type="button"
        data-testid="net-configure"
        onClick={() => props.onConfigure()}
        style={{
          background: 'transparent',
          color: '#ddd',
          border: '1px solid #3a3a4a',
          'border-radius': '3px',
          padding: '3px 8px',
          cursor: 'pointer',
          font: '11px ui-sans-serif,system-ui,sans-serif',
          'align-self': 'flex-start',
        }}
      >
        Configure…
      </button>
    </div>
  );
};
