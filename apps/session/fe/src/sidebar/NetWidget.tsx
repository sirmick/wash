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
import { tokens } from '@wash/ui';

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

// NetIface is one non-local, non-docker interface and its IPs, pushed live by
// the session BE's host-stats ticker (host.ifaces).
export interface NetIface {
  name: string;
  ips: string[];
}

export interface NetWidgetProps {
  state: () => NetState | null;
  ifaces: () => NetIface[];
  onConfigure: () => void;
}

function statusColor(status: string): string {
  switch (status) {
    case 'await-confirm':
      return tokens.accentAmber;
    case 'committed':
      return tokens.accentGreen;
    case 'reverted':
    case 'failed':
      return tokens.borderDanger;
    default:
      return tokens.fgMuted;
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
        <span style={{ color: tokens.fg, flex: 1 }}>{status()}</span>
        <Show when={st()?.phase}>
          <span style={{ opacity: 0.6, font: tokens.type.monoSm }}>{st()!.phase}</span>
        </Show>
      </div>

      {/* Live addressing: the IP of every non-local, non-docker interface. */}
      <Show when={props.ifaces().length > 0} fallback={
        <span data-testid="net-noaddr" style={{ opacity: 0.45, font: tokens.type.textSm }}>
          no addressed interfaces
        </span>
      }>
        <div data-testid="net-ifaces" style={{ display: 'flex', 'flex-direction': 'column', gap: '2px' }}>
          <For each={props.ifaces()}>
            {(ifc) => (
              <div
                data-testid={`net-iface-${ifc.name}`}
                style={{ display: 'flex', gap: '6px', 'align-items': 'baseline', font: tokens.type.monoSm }}
              >
                <span style={{ color: tokens.accentBlue, 'min-width': '52px' }}>{ifc.name}</span>
                <span style={{ color: tokens.fg, flex: 1, 'word-break': 'break-all' }}>{ifc.ips.join('  ')}</span>
              </div>
            )}
          </For>
        </div>
      </Show>

      <Show when={awaiting() && summary().length > 0}>
        <div
          data-testid="net-pending"
          style={{
            padding: '6px 8px',
            background: 'rgba(208,160,64,0.08)',
            border: '1px solid #4a4030',
            'border-radius': tokens.radiusSm,
            display: 'flex',
            'flex-direction': 'column',
            gap: '3px',
          }}
        >
          <span style={{ color: tokens.accentAmber }}>awaiting confirmation</span>
          <For each={summary()}>
            {(line) => (
              <span style={{ opacity: 0.85, font: tokens.type.monoSm }}>{line}</span>
            )}
          </For>
        </div>
      </Show>

      <Show when={errs().length > 0}>
        <span data-testid="net-errcount" style={{ color: tokens.fgDanger }}>
          {errs().length} validation error{errs().length === 1 ? '' : 's'}
        </span>
      </Show>

      <button
        type="button"
        data-testid="net-configure"
        onClick={() => props.onConfigure()}
        style={{
          background: 'transparent',
          color: tokens.fg,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': tokens.radiusSm,
          padding: '3px 8px',
          cursor: 'pointer',
          font: tokens.type.textSm,
          'align-self': 'flex-start',
        }}
      >
        Configure…
      </button>
    </div>
  );
};
