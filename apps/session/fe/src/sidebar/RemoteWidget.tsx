// RemoteWidget renders the open remote-host sessions in the sidebar
// (docs/REMOTE.md R2, §6.1). The com.wash.remote supervisor publishes
// {hosts:[{host,origin,status,…}]} via its StateService; the session BE
// re-brands the push to "remote.state" and the App feeds it here. This is
// the glanceable "what's connected" view — colour-coded per host, status
// dots — while wash-connect is the full management surface. "Manage…"
// (and clicking a host) opens the wash-connect window.
//
// Pure renderer — subscription wiring lives in the App.

import type { Component } from 'solid-js';
import { For, Show } from 'solid-js';
import { tokens } from '@wash/ui';

export interface RemoteHost {
  host: string;
  origin: string;
  status: string; // starting | up | reconnecting | down
  error?: string;
  code?: string;
}

// Host accent palette + hash, mirroring web/shell/src/host-colors.ts and
// the wash-connect FE so a host's dot here is the same hue as its window
// stripe and its wash-connect entry (the constant per-host colour).
const PALETTE = [
  tokens.accentBlue,
  tokens.accentGreen,
  tokens.accentViolet,
  tokens.accentAmber,
  tokens.accentCyan,
  tokens.accentRed,
];
function hostColor(origin: string): string {
  let h = 0;
  for (let i = 0; i < origin.length; i++) h = ((h << 5) - h + origin.charCodeAt(i)) | 0;
  return PALETTE[Math.abs(h) % PALETTE.length];
}

function statusLabel(s: string): string {
  switch (s) {
    case 'starting': return 'connecting…';
    case 'up': return 'connected';
    case 'reconnecting': return 'reconnecting…';
    case 'down': return 'disconnected';
    default: return s;
  }
}

export interface RemoteWidgetProps {
  hosts: () => RemoteHost[];
  onManage: () => void;
}

export const RemoteWidget: Component<RemoteWidgetProps> = (props) => (
  <div
    data-testid="remote-widget"
    style={{ display: 'flex', 'flex-direction': 'column', gap: '6px', 'font-size': '11px' }}
  >
    <Show
      when={props.hosts().length > 0}
      fallback={
        <span data-testid="remote-empty" style={{ opacity: 0.45, font: `10px ${tokens.fontMono}` }}>
          no remote sessions
        </span>
      }
    >
      <div data-testid="remote-hosts" style={{ display: 'flex', 'flex-direction': 'column', gap: '4px' }}>
        <For each={props.hosts()}>
          {(h) => (
            <button
              type="button"
              data-testid={`remote-host-${h.origin}`}
              data-status={h.status}
              onClick={() => props.onManage()}
              title={h.error ? `${h.host} — ${h.error}` : h.host}
              style={{
                display: 'flex',
                'align-items': 'center',
                gap: '6px',
                background: 'transparent',
                border: 'none',
                cursor: 'pointer',
                padding: '2px 0',
                'text-align': 'left',
                opacity: h.status === 'down' ? 0.55 : 1,
              }}
            >
              <span
                style={{
                  width: '8px',
                  height: '8px',
                  'border-radius': '50%',
                  background: hostColor(h.origin),
                  'flex-shrink': 0,
                }}
              />
              <span style={{ color: '#ddd', flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                {h.host}
              </span>
              <span style={{ opacity: 0.6, font: `10px ${tokens.fontMono}` }}>{statusLabel(h.status)}</span>
            </button>
          )}
        </For>
      </div>
    </Show>

    <button
      type="button"
      data-testid="remote-manage"
      onClick={() => props.onManage()}
      style={{
        background: 'transparent',
        color: '#ddd',
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': '3px',
        padding: '3px 8px',
        cursor: 'pointer',
        font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
        'align-self': 'flex-start',
      }}
    >
      Manage…
    </button>
  </div>
);
