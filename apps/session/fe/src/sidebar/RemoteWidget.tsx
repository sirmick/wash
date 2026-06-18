// RemoteWidget renders the open remote-host sessions in the sidebar
// (docs/REMOTE.md R2, §6.1). The com.wash.remote supervisor publishes
// {hosts:[{host,origin,status,…}]} via its StateService; the session BE
// re-brands the push to "remote.state" and the App feeds it here.
//
// Each connected host is a Launch dropdown: click it to pick an app from
// that host's live catalog and launch it (window.wash.launchOn), the same
// app-picker wash-connect uses. The host stays attached in the shell even
// after wash-connect closes, so launching works straight from the sidebar.
// "Manage…" opens wash-connect for adding hosts / auth / bookmarks.
//
// Pure renderer — subscription wiring lives in the App.

import type { Component } from 'solid-js';
import { For, Show, createSignal } from 'solid-js';
import { tokens } from '@wash/ui';

export interface RemoteHost {
  host: string;
  origin: string;
  status: string; // starting | up | reconnecting | down
  error?: string;
  code?: string;
}

export interface RemoteApp {
  id: string;
  name: string;
  icon?: string;
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

const SpriteIcon: Component<{ name: string; size: number }> = (props) => (
  <svg
    width={props.size}
    height={props.size}
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    style={{ display: 'block' }}
  >
    <use href={`/icons.svg#${props.name}`} />
  </svg>
);

export interface RemoteWidgetProps {
  hosts: () => RemoteHost[];
  // appsFor lists a connected host's launchable apps (reactive: re-runs when
  // the host's catalog changes).
  appsFor: (origin: string) => RemoteApp[];
  onLaunch: (origin: string, appID: string) => void;
  onManage: () => void;
}

// HostEntry is one host: a Launch trigger that drops down the host's app
// list when connected. A non-connected host routes its click to Manage
// (to see status / reconnect).
const HostEntry: Component<{
  host: RemoteHost;
  appsFor: (origin: string) => RemoteApp[];
  onLaunch: (origin: string, appID: string) => void;
  onManage: () => void;
}> = (props) => {
  const [open, setOpen] = createSignal(false);
  const up = () => props.host.status === 'up';
  const onClick = () => {
    if (up()) setOpen((v) => !v);
    else props.onManage();
  };
  return (
    <div style={{ position: 'relative' }}>
      <button
        type="button"
        data-testid={`remote-host-${props.host.origin}`}
        data-status={props.host.status}
        onClick={onClick}
        title={props.host.error ? `${props.host.host} — ${props.host.error}` : props.host.host}
        style={{
          display: 'flex',
          'align-items': 'center',
          gap: '6px',
          width: '100%',
          background: 'transparent',
          border: 'none',
          cursor: 'pointer',
          padding: '2px 0',
          'text-align': 'left',
          opacity: props.host.status === 'down' ? 0.55 : 1,
        }}
      >
        <span
          style={{
            width: '8px',
            height: '8px',
            'border-radius': '50%',
            background: hostColor(props.host.origin),
            'flex-shrink': 0,
          }}
        />
        <span style={{ color: '#ddd', flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
          {props.host.host}
        </span>
        <Show when={up()} fallback={<span style={{ opacity: 0.6, font: `10px ${tokens.fontMono}` }}>{statusLabel(props.host.status)}</span>}>
          <span style={{ opacity: 0.6, font: `10px ${tokens.fontSans}` }}>Launch ▾</span>
        </Show>
      </button>

      <Show when={open() && up()}>
        <div style={backdropStyle} onClick={() => setOpen(false)} />
        <div style={menuStyle} data-testid={`remote-apps-${props.host.origin}`} role="menu">
          <Show
            when={props.appsFor(props.host.origin).length > 0}
            fallback={<div style={menuEmptyStyle}>loading apps…</div>}
          >
            <For each={props.appsFor(props.host.origin)}>
              {(app) => (
                <button
                  type="button"
                  style={menuItemStyle}
                  data-testid={`remote-launch-${props.host.origin}-${app.id}`}
                  role="menuitem"
                  title={app.id}
                  onClick={() => { props.onLaunch(props.host.origin, app.id); setOpen(false); }}
                >
                  <span style={menuIconStyle}>
                    <Show when={app.icon} fallback={<span style={{ opacity: 0.3 }}>·</span>}>
                      <SpriteIcon name={app.icon!} size={14} />
                    </Show>
                  </span>
                  <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{app.name}</span>
                </button>
              )}
            </For>
          </Show>
        </div>
      </Show>
    </div>
  );
};

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
            <HostEntry host={h} appsFor={props.appsFor} onLaunch={props.onLaunch} onManage={props.onManage} />
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

const backdropStyle = { position: 'fixed' as const, inset: '0', 'z-index': 40 };

const menuStyle = {
  position: 'absolute' as const,
  top: 'calc(100% + 2px)',
  left: '0',
  right: '0',
  'z-index': 41,
  'min-width': '150px',
  'max-height': '240px',
  overflow: 'auto' as const,
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': '4px',
  'box-shadow': '0 8px 24px rgba(0,0,0,0.45)',
  padding: '4px',
  display: 'flex',
  'flex-direction': 'column' as const,
  gap: '1px',
};

const menuItemStyle = {
  display: 'flex',
  'align-items': 'center',
  gap: '7px',
  padding: '5px 7px',
  background: 'transparent',
  color: tokens.fg,
  border: 'none',
  'border-radius': '3px',
  cursor: 'pointer',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  'text-align': 'left' as const,
  width: '100%',
};

const menuIconStyle = {
  width: '14px',
  height: '14px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  'flex-shrink': 0,
  opacity: 0.85,
};

const menuEmptyStyle = {
  padding: '6px 7px',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  opacity: 0.7,
};
