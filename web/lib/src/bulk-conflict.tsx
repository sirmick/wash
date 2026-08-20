// BulkConflictOverlay is a screen-anchored modal that pops when the
// bulk service reports a pending conflict.
//
// It lives in @wash/ui because it has two homes (docs/SIDEBAR.md M3b):
// com.wash.fm, which is where answering it can WORK — an app talking to
// its own host's bulk service has a router-attested sender — and, until
// M3c, the desktop rail, whose answer routes through the session BE
// gateway and so only ever reaches the local host.
//
// resolve_conflict is idempotent by job id and first answer wins, so two
// surfaces racing on one conflict is safe rather than merely unlikely.
//
// UI shape mirrors Windows Explorer / GNOME Files conventions:
//
//   - dir vs dir   → Merge / Merge All / Replace / Skip / Skip All / Cancel
//   - file vs file → Replace / Replace All / Skip / Skip All / Cancel
//   - type mismatch → Replace ... with ... / Skip / Cancel  (explicit)
//
// One overlay at a time. If multiple conflicts are pending, this
// component shows the FIRST one in state.conflicts; resolving it
// removes it from state and the next one (if any) pops on the next
// render. State ordering matches the queue's worker order.

import type { Component, JSX } from 'solid-js';
import { Show } from 'solid-js';
import { Button } from './button';
import { tokens } from './tokens';

export interface BulkConflict {
  job_id: string;
  src: string;
  src_type: string; // "file" | "dir" | "symlink"
  dst: string;
  dst_type: string;
}

export interface BulkConflictOverlayProps {
  conflict: () => BulkConflict | null;
  onResolve: (jobID: string, action: string) => void;
}

export const BulkConflictOverlay: Component<BulkConflictOverlayProps> = (props) => {
  return (
    <Show when={props.conflict()}>
      {(c) => <ConflictModal conflict={c()} onResolve={props.onResolve} />}
    </Show>
  );
};

const ConflictModal: Component<{
  conflict: BulkConflict;
  onResolve: (jobID: string, action: string) => void;
}> = (props) => {
  const c = () => props.conflict;
  const isDirVsDir = () => c().src_type === 'dir' && c().dst_type === 'dir';
  const isSameType = () => c().src_type === c().dst_type;
  const kindOf = (t: string) => (t === 'dir' ? 'folder' : t || 'item');

  const title = () => {
    if (isDirVsDir()) return 'Merge with existing folder?';
    if (isSameType()) return 'Replace existing?';
    return `Replace existing ${kindOf(c().dst_type)} with ${kindOf(c().src_type)}?`;
  };

  const onAction = (action: string) => () => props.onResolve(c().job_id, action);

  const backdropStyle: JSX.CSSProperties = {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.55)',
    display: 'flex',
    'align-items': 'center',
    'justify-content': 'center',
    'z-index': 12000,
    animation: 'wash-fade-in 120ms ease-out',
  };

  const panelStyle: JSX.CSSProperties = {
    background: tokens.bgWindow,
    border: `1px solid ${tokens.borderFocus}`,
    'border-radius': tokens.radiusXl,
    padding: '20px 22px',
    'min-width': '420px',
    'max-width': '600px',
    'box-shadow': '0 16px 40px rgba(0,0,0,0.6)',
    color: tokens.fg,
    font: tokens.type.textMd,
    animation: 'wash-pop-in 140ms ease-out',
  };

  return (
    <div data-testid="bulk-conflict-overlay" style={backdropStyle}>
      <div data-testid={`bulk-conflict-modal-${c().job_id}`} style={panelStyle}>
        <div style={{ font: tokens.type.titleSm, 'margin-bottom': '10px' }}>
          {title()}
        </div>
        <div
          style={{
            font: tokens.type.monoMd,
            opacity: 0.85,
            'word-break': 'break-all',
            'margin-bottom': '14px',
            padding: '8px 10px',
            background: 'rgba(0,0,0,0.25)',
            'border-radius': tokens.radiusMd,
          }}
        >
          {c().dst}
        </div>
        <div style={{ display: 'flex', gap: '8px', 'flex-wrap': 'wrap' }}>
          <Show
            when={isDirVsDir()}
            fallback={
              <>
                <Button
                  data-testid={`bulk-conflict-replace-${c().job_id}`}
                  variant="danger"
                  size="sm"
                  onClick={onAction('replace')}
                >
                  Replace
                </Button>
                <Button
                  data-testid={`bulk-conflict-replace-all-${c().job_id}`}
                  variant="danger"
                  size="sm"
                  onClick={onAction('replace_all')}
                >
                  Replace All
                </Button>
              </>
            }
          >
            <Button
              data-testid={`bulk-conflict-merge-${c().job_id}`}
              variant="ghost"
              size="sm"
              onClick={onAction('merge')}
            >
              Merge
            </Button>
            <Button
              data-testid={`bulk-conflict-merge-all-${c().job_id}`}
              variant="ghost"
              size="sm"
              onClick={onAction('merge_all')}
            >
              Merge All
            </Button>
            <Button
              data-testid={`bulk-conflict-replace-${c().job_id}`}
              variant="danger"
              size="sm"
              onClick={onAction('replace')}
            >
              Replace
            </Button>
          </Show>
          <Button
            data-testid={`bulk-conflict-skip-${c().job_id}`}
            variant="ghost"
            size="sm"
            onClick={onAction('skip')}
          >
            Skip
          </Button>
          <Button
            data-testid={`bulk-conflict-skip-all-${c().job_id}`}
            variant="ghost"
            size="sm"
            onClick={onAction('skip_all')}
          >
            Skip All
          </Button>
          <Button
            data-testid={`bulk-conflict-cancel-${c().job_id}`}
            variant="ghost"
            size="sm"
            onClick={onAction('cancel')}
          >
            Cancel
          </Button>
        </div>
      </div>
    </div>
  );
};
