// NotifyWidget renders the notification history fed by com.wash.notify
// (the background notification service introduced in M2). The service
// is the source of truth; this widget is a pure renderer of the
// state-service snapshot.
//
// Subscription wiring lives in the App (so the App can coordinate
// auto-expand-on-new-event with section state). This component only
// renders.

import type { Component, JSX } from 'solid-js';
import { For, Show } from 'solid-js';
import { tokens } from '@wash/ui';

export interface NotifyEntry {
  id: string;
  source_app: string;
  source_instance: string;
  title: string;
  body?: string;
  level: string;
  created_sec: number;
  read: boolean;
}

export interface NotifyWidgetProps {
  notifications: () => NotifyEntry[];
  /** Mark a single entry as read. */
  onMarkRead: (id: string) => void;
  /** Clear the entire history. */
  onClearAll: () => void;
}

export const NotifyWidget: Component<NotifyWidgetProps> = (props) => {
  const empty = () => props.notifications().length === 0;

  return (
    <div
      data-testid="notify-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      <Show when={empty()}>
        <div
          data-testid="notify-empty"
          style={{
            opacity: 0.5,
            'font-style': 'italic',
            'text-align': 'center',
            padding: '12px 0',
            'font-size': '11px',
          }}
        >
          no notifications
        </div>
      </Show>
      <For each={props.notifications()}>
        {(n) => <NotifyRow entry={n} onMarkRead={() => props.onMarkRead(n.id)} />}
      </For>
      <Show when={!empty()}>
        <button
          type="button"
          data-testid="notify-clear-all"
          onClick={props.onClearAll}
          style={{
            background: 'transparent',
            color: tokens.fgMuted,
            border: `1px solid ${tokens.borderMenu}`,
            'border-radius': tokens.radiusSm,
            padding: '4px 8px',
            cursor: 'pointer',
            font: tokens.type.textSm,
            'align-self': 'flex-end',
            'margin-top': '4px',
          }}
        >
          clear all
        </button>
      </Show>
    </div>
  );
};

const NotifyRow: Component<{ entry: NotifyEntry; onMarkRead: () => void }> = (props) => {
  // Border-left colour communicates level at a glance — same hues the
  // shell's transient toasts use (see web/shell/src/notify.ts).
  const stripeColor = () => {
    switch (props.entry.level) {
      case 'error':
        return tokens.borderDanger;
      case 'warn':
        return tokens.accentAmber;
      default:
        return tokens.accentBlue;
    }
  };
  const rowStyle = (): JSX.CSSProperties => ({
    'border-left': `3px solid ${stripeColor()}`,
    background: props.entry.read ? 'rgba(255,255,255,0.02)' : 'rgba(80,90,180,0.08)',
    padding: '6px 8px',
    'border-radius': tokens.radiusSm,
    cursor: 'pointer',
    'font-size': '11px',
    transition: 'background 0.12s',
    opacity: props.entry.read ? 0.65 : 1,
  });
  return (
    <div
      data-testid={`notify-row-${props.entry.id}`}
      data-level={props.entry.level}
      data-read={props.entry.read ? 'true' : 'false'}
      style={rowStyle()}
      onClick={props.onMarkRead}
      title={`From ${props.entry.source_app} — click to mark read`}
    >
      <div
        style={{
          display: 'flex',
          'justify-content': 'space-between',
          'align-items': 'baseline',
          gap: '6px',
        }}
      >
        <span
          style={{
            'font-weight': 600,
            color: tokens.fg,
            overflow: 'hidden',
            'text-overflow': 'ellipsis',
            'white-space': 'nowrap',
            flex: 1,
          }}
        >
          {props.entry.title}
        </span>
        <span style={{ opacity: 0.5, 'font-size': '10px', 'flex-shrink': 0 }}>
          {timeAgo(props.entry.created_sec)}
        </span>
      </div>
      <Show when={props.entry.body}>
        <div
          style={{
            opacity: 0.75,
            'margin-top': '2px',
            'word-break': 'break-word',
            'line-height': 1.35,
          }}
        >
          {props.entry.body}
        </div>
      </Show>
      <div
        style={{
          opacity: 0.4,
          'margin-top': '3px',
          font: tokens.type.monoSm,
        }}
      >
        {sourceLabel(props.entry.source_app)}
      </div>
    </div>
  );
};

/** sourceLabel strips the com.wash. prefix from app ids so the
 *  source row stays compact in the narrow sidebar. */
function sourceLabel(appID: string): string {
  if (appID.startsWith('com.wash.')) return appID.slice('com.wash.'.length);
  return appID || '?';
}

/** timeAgo renders an absolute unix seconds value as a short relative
 *  string ("now", "5m", "2h", "3d"). Avoids importing a date lib for
 *  one widget. */
function timeAgo(unixSec: number): string {
  if (!unixSec) return '';
  const diff = Math.floor(Date.now() / 1000) - unixSec;
  if (diff < 30) return 'now';
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  return `${Math.floor(diff / 86400)}d`;
}
