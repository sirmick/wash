// PrivWidget renders the wash-priv approval queue in the sidebar.
// Each pending request gets a row with Approve/Reject buttons; once
// approved, the row shows running → done with the exit code. The
// section header carries a red trust treatment (the same hue the
// ROOT window stripe uses) so the user sees at a glance that the
// section gates root.
//
// Lock state is shown as a small icon + click-to-lock button in the
// widget body when the password cache is hot.
//
// Pure renderer — subscription wiring + the unlock overlay live in
// the App. The reserved-id check at internal/router/registry.go:162
// still gates which binary can claim com.wash.priv, so the trust
// signal here is router-enforced.

import type { Component } from 'solid-js';
import { For, Show } from 'solid-js';
import { tokens, washAssetUrl } from '@wash/ui';

export type PrivStatus = 'queued' | 'running' | 'done' | 'rejected' | 'error';
export type PrivKind = 'run' | 'spawn' | 'run_inline';

export interface PrivReq {
  req_id: string;
  kind: PrivKind;
  sender_app_id: string;
  sender_inst_id: string;
  app_id?: string;
  argv: string[];
  cwd?: string;
  reason: string;
  status: PrivStatus;
  exit_code?: number;
  error?: string;
  cli_origin?: {
    pid: number;
    uid: number;
    comm: string;
    tty: string;
  };
}

export interface PrivWidgetProps {
  locked: () => boolean;
  reqs: () => PrivReq[];
  onApprove: (reqID: string) => void;
  onReject: (reqID: string, reason?: string) => void;
  onLock: () => void;
}

export const PrivWidget: Component<PrivWidgetProps> = (props) => {
  const empty = () => props.reqs().length === 0;
  return (
    <div
      data-testid="priv-widget"
      data-locked={props.locked() ? 'true' : 'false'}
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      <LockBar locked={props.locked} onLock={props.onLock} />
      <Show when={empty()}>
        <div
          data-testid="priv-empty"
          style={{
            opacity: 0.5,
            'font-style': 'italic',
            'text-align': 'center',
            padding: '12px 0',
            'font-size': '11px',
          }}
        >
          no pending requests
        </div>
      </Show>
      <For each={props.reqs()}>
        {(r) => (
          <PrivRow
            req={r}
            onApprove={() => props.onApprove(r.req_id)}
            onReject={() => props.onReject(r.req_id)}
          />
        )}
      </For>
    </div>
  );
};

const LockBar: Component<{ locked: () => boolean; onLock: () => void }> = (props) => {
  return (
    <div
      data-testid="priv-lock-bar"
      data-locked={props.locked() ? 'true' : 'false'}
      style={{
        display: 'flex',
        'align-items': 'center',
        'justify-content': 'space-between',
        padding: '4px 6px',
        background: props.locked() ? 'rgba(80,80,100,0.18)' : 'rgba(160,60,60,0.22)',
        'border-radius': '3px',
        'font-size': '11px',
        color: '#ddd',
      }}
    >
      <span style={{ display: 'inline-flex', 'align-items': 'center', gap: '6px' }}>
        <svg width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <use href={washAssetUrl(`icons.svg#${props.locked() ? 'lock' : 'unlock'}`)} />
        </svg>
        {props.locked() ? 'locked' : 'unlocked'}
      </span>
      <Show when={!props.locked()}>
        <button
          type="button"
          data-testid="priv-lock"
          onClick={props.onLock}
          style={{
            background: 'transparent',
            color: '#ddd',
            border: '1px solid #6a2a2a',
            'border-radius': '3px',
            padding: '1px 6px',
            cursor: 'pointer',
            font: `10px ${tokens.fontSans}`,
          }}
        >
          lock now
        </button>
      </Show>
    </div>
  );
};

const PrivRow: Component<{ req: PrivReq; onApprove: () => void; onReject: () => void }> = (props) => {
  const isPending = () => props.req.status === 'queued';
  const isTerminal = () =>
    props.req.status === 'done' || props.req.status === 'rejected' || props.req.status === 'error';
  const argvPreview = () => argvPreviewStr(props.req.argv);
  const target = () => {
    if (props.req.kind === 'spawn' && props.req.app_id) return props.req.app_id;
    return argvPreview() || '(no command)';
  };
  return (
    <div
      data-testid={`priv-req-${props.req.req_id}`}
      data-status={props.req.status}
      style={{
        padding: '6px 8px',
        background: 'rgba(255,255,255,0.02)',
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': '3px',
        display: 'flex',
        'flex-direction': 'column',
        gap: '3px',
        'font-size': '11px',
      }}
    >
      <div
        style={{
          display: 'flex',
          'align-items': 'baseline',
          'justify-content': 'space-between',
          gap: '6px',
        }}
      >
        <span
          style={{
            color: '#ddd',
            overflow: 'hidden',
            'text-overflow': 'ellipsis',
            'white-space': 'nowrap',
            flex: 1,
            'font-weight': 600,
          }}
          title={target()}
        >
          {target()}
        </span>
        <span
          style={{
            font: `10px ${tokens.fontMono}`,
            opacity: 0.6,
            'flex-shrink': 0,
          }}
        >
          {props.req.status}
        </span>
      </div>
      <div
        style={{
          font: `10px ${tokens.fontMono}`,
          opacity: 0.55,
          'word-break': 'break-all',
        }}
      >
        from {sourceLabel(props.req)}
      </div>
      <Show when={props.req.reason}>
        <div style={{ opacity: 0.75, 'word-break': 'break-word', 'font-style': 'italic' }}>
          “{props.req.reason}”
        </div>
      </Show>
      <Show when={isPending()}>
        <div style={{ display: 'flex', gap: '6px', 'margin-top': '4px' }}>
          <button
            type="button"
            data-testid={`priv-approve-${props.req.req_id}`}
            onClick={props.onApprove}
            style={{
              background: tokens.bgSuccess,
              color: '#fff',
              border: `1px solid ${tokens.fgSuccess}`,
              'border-radius': '3px',
              padding: '2px 8px',
              cursor: 'pointer',
              font: `11px ${tokens.fontSans}`,
              'font-weight': 600,
            }}
          >
            approve
          </button>
          <button
            type="button"
            data-testid={`priv-reject-${props.req.req_id}`}
            onClick={props.onReject}
            style={{
              background: 'transparent',
              color: '#ddd',
              border: `1px solid ${tokens.borderMenu}`,
              'border-radius': '3px',
              padding: '2px 8px',
              cursor: 'pointer',
              font: `11px ${tokens.fontSans}`,
            }}
          >
            reject
          </button>
        </div>
      </Show>
      <Show when={isTerminal() && props.req.exit_code != null && props.req.exit_code !== 0}>
        <div
          style={{
            opacity: 0.85,
            color: tokens.fgDanger,
            font: `10px ${tokens.fontMono}`,
          }}
        >
          exit {props.req.exit_code}
          <Show when={props.req.error}> · {props.req.error}</Show>
        </div>
      </Show>
    </div>
  );
};

/** sourceLabel formats the request source for the row. CLI origins
 *  (sender_app_id = "cli.wash.sudo") show their attested pid + tty;
 *  in-process apps show their short app id. */
function sourceLabel(r: PrivReq): string {
  if (r.cli_origin) {
    const where = r.cli_origin.tty || `pid ${r.cli_origin.pid}`;
    return `wash-sudo (${where})`;
  }
  if (r.sender_app_id) {
    const short = r.sender_app_id.startsWith('com.wash.')
      ? r.sender_app_id.slice('com.wash.'.length)
      : r.sender_app_id;
    return short;
  }
  return '?';
}

function argvPreviewStr(argv: string[]): string {
  return argv
    .map((a) => (/[\s"'\\]/.test(a) ? JSON.stringify(a) : a))
    .join(' ');
}
