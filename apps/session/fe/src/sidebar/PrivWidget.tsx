// PrivWidget renders the wash-priv approval queue in the sidebar.
// Each pending request gets a row with Approve / Approve-app / Reject
// buttons; once approved, the row shows running → done with the exit
// code. The section header carries a red trust treatment (the same hue
// the ROOT window stripe uses) so the user sees at a glance that the
// section gates root.
//
// Rows are ordered active-first (running, then queued, then finished)
// so the request that needs attention floats to the top; only the top
// three show by default, with an expander for the rest. Standing
// per-app grants ("approve for app") are listed as revocable chips.
//
// Lock state is shown as a small icon + click-to-lock button in the
// widget body when the password cache is hot.
//
// Pure renderer — subscription wiring + the unlock overlay live in
// the App. The reserved-id check at internal/router/registry.go:162
// still gates which binary can claim com.wash.priv, so the trust
// signal here is router-enforced.

import type { Component } from 'solid-js';
import { For, Show, createMemo, createSignal } from 'solid-js';
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
  created_ms?: number;
  started_ms?: number;
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
  grants?: () => string[];
  onApprove: (reqID: string) => void;
  onApproveApp?: (reqID: string) => void;
  onReject: (reqID: string, reason?: string) => void;
  onRevokeApp?: (appID: string) => void;
  onLock: () => void;
}

// How many rows show before the expander kicks in.
const VISIBLE_LIMIT = 3;

// statusRank orders rows active-first: running (happening now), then
// queued (awaiting your decision), then terminal (history).
function statusRank(s: PrivStatus): number {
  switch (s) {
    case 'running':
      return 0;
    case 'queued':
      return 1;
    default:
      return 2;
  }
}

// orderReqs sorts a copy active-first, newest-first within each band so
// the latest activity sits at the top of its group.
function orderReqs(reqs: PrivReq[]): PrivReq[] {
  return [...reqs].sort((a, b) => {
    const r = statusRank(a.status) - statusRank(b.status);
    if (r !== 0) return r;
    return (b.created_ms ?? 0) - (a.created_ms ?? 0);
  });
}

export const PrivWidget: Component<PrivWidgetProps> = (props) => {
  const empty = () => props.reqs().length === 0;
  const ordered = createMemo(() => orderReqs(props.reqs()));
  const grants = createMemo(() => props.grants?.() ?? []);
  const [expanded, setExpanded] = createSignal(false);
  const visible = createMemo(() =>
    expanded() ? ordered() : ordered().slice(0, VISIBLE_LIMIT),
  );
  const hiddenCount = () => Math.max(0, ordered().length - VISIBLE_LIMIT);

  return (
    <div
      data-testid="priv-widget"
      data-locked={props.locked() ? 'true' : 'false'}
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      <LockBar locked={props.locked} onLock={props.onLock} />
      <Show when={grants().length > 0}>
        <div
          data-testid="priv-grants"
          style={{ display: 'flex', 'flex-wrap': 'wrap', gap: '4px' }}
        >
          <For each={grants()}>
            {(appID) => (
              <GrantChip appID={appID} onRevoke={() => props.onRevokeApp?.(appID)} />
            )}
          </For>
        </div>
      </Show>
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
      <For each={visible()}>
        {(r) => (
          <PrivRow
            req={r}
            granted={grants().includes(r.sender_app_id)}
            onApprove={() => props.onApprove(r.req_id)}
            onApproveApp={
              props.onApproveApp ? () => props.onApproveApp!(r.req_id) : undefined
            }
            onReject={() => props.onReject(r.req_id)}
          />
        )}
      </For>
      <Show when={hiddenCount() > 0}>
        <button
          type="button"
          data-testid="priv-more"
          onClick={() => setExpanded((v) => !v)}
          style={{
            background: 'transparent',
            color: tokens.fg,
            border: `1px solid ${tokens.borderMenu}`,
            'border-radius': tokens.radiusSm,
            padding: '3px 8px',
            cursor: 'pointer',
            font: `10px ${tokens.fontSans}`,
            opacity: 0.8,
          }}
        >
          {expanded() ? 'show less' : `show ${hiddenCount()} more`}
        </button>
      </Show>
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
        background: props.locked() ? `color-mix(in srgb, ${tokens.fgMuted} 18%, transparent)` : `color-mix(in srgb, ${tokens.accentRed} 22%, transparent)`,
        'border-radius': tokens.radiusSm,
        'font-size': '11px',
        color: tokens.fg,
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
            color: tokens.fg,
            border: `1px solid ${tokens.borderDanger}`,
            'border-radius': tokens.radiusSm,
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

// GrantChip shows one app that holds standing "approve for app" consent,
// with an ✕ to revoke it.
const GrantChip: Component<{ appID: string; onRevoke: () => void }> = (props) => {
  return (
    <span
      data-testid={`priv-grant-${props.appID}`}
      title={`Auto-approving requests from ${props.appID} this session`}
      style={{
        display: 'inline-flex',
        'align-items': 'center',
        gap: '4px',
        padding: '1px 4px 1px 7px',
        background: `color-mix(in srgb, ${tokens.accentRed} 16%, transparent)`,
        border: `1px solid ${tokens.borderDanger}`,
        'border-radius': '999px',
        font: `10px ${tokens.fontSans}`,
        color: tokens.fg,
      }}
    >
      auto: {shortApp(props.appID)}
      <button
        type="button"
        data-testid={`priv-revoke-${props.appID}`}
        onClick={props.onRevoke}
        title="Stop auto-approving"
        style={{
          background: 'transparent',
          color: tokens.fg,
          border: 'none',
          padding: '0 2px',
          cursor: 'pointer',
          'line-height': 1,
          'font-size': '12px',
          opacity: 0.7,
        }}
      >
        ✕
      </button>
    </span>
  );
};

const PrivRow: Component<{
  req: PrivReq;
  granted: boolean;
  onApprove: () => void;
  onApproveApp?: () => void;
  onReject: () => void;
}> = (props) => {
  const isPending = () => props.req.status === 'queued';
  const isTerminal = () =>
    props.req.status === 'done' || props.req.status === 'rejected' || props.req.status === 'error';
  // Standing per-app consent only makes sense for in-app requests:
  // CLI origins (wash-sudo) are per-pid and refused a grant by the BE.
  const canGrantApp = () => !props.req.cli_origin && !!props.req.sender_app_id && !props.granted;
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
        'border-radius': tokens.radiusSm,
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
            color: tokens.fg,
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
        <div style={{ display: 'flex', gap: '6px', 'margin-top': '4px', 'flex-wrap': 'wrap' }}>
          <button
            type="button"
            data-testid={`priv-approve-${props.req.req_id}`}
            onClick={props.onApprove}
            style={{
              background: tokens.bgSuccess,
              color: '#fff',
              border: `1px solid ${tokens.fgSuccess}`,
              'border-radius': tokens.radiusSm,
              padding: '2px 8px',
              cursor: 'pointer',
              font: `11px ${tokens.fontSans}`,
              'font-weight': 600,
            }}
          >
            approve
          </button>
          <Show when={canGrantApp() && props.onApproveApp}>
            <button
              type="button"
              data-testid={`priv-approve-app-${props.req.req_id}`}
              onClick={props.onApproveApp}
              title={`Approve, and auto-approve future requests from ${sourceLabel(props.req)} this session`}
              style={{
                background: 'transparent',
                color: tokens.fg,
                border: `1px solid ${tokens.fgSuccess}`,
                'border-radius': tokens.radiusSm,
                padding: '2px 8px',
                cursor: 'pointer',
                font: `11px ${tokens.fontSans}`,
              }}
            >
              approve app
            </button>
          </Show>
          <button
            type="button"
            data-testid={`priv-reject-${props.req.req_id}`}
            onClick={props.onReject}
            style={{
              background: 'transparent',
              color: tokens.fg,
              border: `1px solid ${tokens.borderMenu}`,
              'border-radius': tokens.radiusSm,
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
    return shortApp(r.sender_app_id);
  }
  return '?';
}

/** shortApp trims the com.wash. namespace prefix for display. */
function shortApp(appID: string): string {
  return appID.startsWith('com.wash.') ? appID.slice('com.wash.'.length) : appID;
}

function argvPreviewStr(argv: string[]): string {
  return argv
    .map((a) => (/[\s"'\\]/.test(a) ? JSON.stringify(a) : a))
    .join(' ');
}
