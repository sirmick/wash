// BulkWidget renders the wash-bulk queue inside the sidebar. Each
// active or recent job appears as a row with op label, progress bar,
// status, and a Cancel button while running. Conflicts are NOT
// rendered inline (they're too dense for a 300px column); the parent
// App opens BulkConflictOverlay when state.conflicts is non-empty.
//
// Pure renderer — subscription wiring lives in the App.

import type { Component } from 'solid-js';
import { For, Show } from 'solid-js';
import { tokens } from '@wash/ui';

export interface BulkJob {
  job_id: string;
  op: string; // "delete" | "move" | "copy"
  status: string; // "queued" | "running" | "done" | "failed" | "cancelled"
  paths: string[];
  dest: string;
  done: number;
  total: number;
  error: string;
}

export interface BulkWidgetProps {
  jobs: () => BulkJob[];
  onCancel: (jobID: string) => void;
}

export const BulkWidget: Component<BulkWidgetProps> = (props) => {
  const empty = () => props.jobs().length === 0;
  return (
    <div
      data-testid="bulk-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      <Show when={empty()}>
        <div
          data-testid="bulk-empty"
          style={{
            opacity: 0.5,
            'font-style': 'italic',
            'text-align': 'center',
            padding: '12px 0',
            'font-size': '11px',
          }}
        >
          queue is empty
        </div>
      </Show>
      <For each={props.jobs()}>
        {(j) => <BulkRow job={j} onCancel={() => props.onCancel(j.job_id)} />}
      </For>
    </div>
  );
};

const BulkRow: Component<{ job: BulkJob; onCancel: () => void }> = (props) => {
  const fraction = () => {
    if (!props.job.total) return props.job.status === 'done' ? 1 : 0;
    return Math.min(1, props.job.done / props.job.total);
  };
  const label = () => {
    const n = props.job.paths.length;
    return `${props.job.op} ${n} item${n === 1 ? '' : 's'}${props.job.dest ? ` → ${props.job.dest}` : ''}`;
  };
  const isTerminal = () =>
    props.job.status === 'done' || props.job.status === 'failed' || props.job.status === 'cancelled';
  const barColor = () => {
    if (props.job.status === 'failed') return tokens.borderDanger;
    if (props.job.status === 'cancelled') return tokens.fgMuted;
    if (props.job.status === 'done') return tokens.accentGreen;
    return tokens.accentBlue;
  };
  return (
    <div
      data-testid={`bulk-job-${props.job.job_id}`}
      data-status={props.job.status}
      style={{
        padding: '6px 8px',
        background: 'rgba(255,255,255,0.02)',
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': '3px',
        display: 'flex',
        'flex-direction': 'column',
        gap: '4px',
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
          }}
        >
          {label()}
        </span>
        <Show
          when={!isTerminal()}
          fallback={
            <span
              data-testid={`bulk-status-${props.job.job_id}`}
              style={{
                font: `10px ${tokens.fontMono}`,
                opacity: 0.65,
                'flex-shrink': 0,
              }}
            >
              {props.job.status}
            </span>
          }
        >
          <button
            type="button"
            data-testid={`bulk-cancel-${props.job.job_id}`}
            onClick={props.onCancel}
            style={{
              background: 'transparent',
              color: tokens.fg,
              border: `1px solid ${tokens.borderMenu}`,
              'border-radius': '3px',
              padding: '1px 6px',
              cursor: 'pointer',
              font: `10px ${tokens.fontSans}`,
              'flex-shrink': 0,
            }}
          >
            cancel
          </button>
        </Show>
      </div>
      <div
        data-testid={`bulk-progress-${props.job.job_id}`}
        data-fraction={fraction().toFixed(3)}
        style={{
          height: '4px',
          background: tokens.bgRowHover,
          'border-radius': '2px',
          overflow: 'hidden',
        }}
      >
        <div
          style={{
            width: `${Math.round(fraction() * 100)}%`,
            height: '100%',
            background: barColor(),
            transition: 'width 0.15s linear',
          }}
        />
      </div>
      <Show when={props.job.error}>
        <div
          style={{
            opacity: 0.85,
            color: tokens.fgDanger,
            font: `10px ${tokens.fontMono}`,
            'word-break': 'break-all',
          }}
        >
          {props.job.error}
        </div>
      </Show>
    </div>
  );
};
