// AgentsWidget renders the coding-agent roster fed by com.wash.agentd
// (docs/AGENT_TERM.md §7). One row per agent wash can see, across every
// terminal window: what it is, where it's working, what it's doing, and
// for how long.
//
// The roster's job is to answer "who needs me?" without hunting through
// windows, so the service sorts needs-input first and this renders that
// order as given. Clicking a row goes to the terminal that owns it —
// that's the whole interaction.
//
// Pure renderer; subscription wiring lives in the App.

import type { Component, JSX } from 'solid-js';
import { For, Show } from 'solid-js';
import { tokens } from '@wash/ui';

export interface AgentRow {
  key: string;
  agent: string;
  /** running | working | needs-input | done | stale */
  state: string;
  reason?: string;
  session_id?: string;
  cwd?: string;
  dir?: string;
  branch?: string;
  dirty?: boolean;
  term_instance: string;
  window_id: number;
  channel_id: number;
  /** elapsed in this state as of the push; anchored locally by the App */
  since_ms: number;
}

export interface AgentsWidgetProps {
  rows: () => AgentRow[];
  /** local clock anchor per row key, so elapsed keeps counting between pushes */
  startedAt: (key: string) => number;
  /** ticking "now" from the App, so every row's clock advances together */
  now: () => number;
  /** go to the terminal that owns this agent */
  onFocus: (row: AgentRow) => void;
}

// stateColor is the same language as the terminal's own tab dot: blue
// working, amber needs-input, green done, muted for detected-but-quiet,
// and a dim grey for a row whose terminal has stopped reporting.
export function stateColor(state: string): string {
  switch (state) {
    case 'working': return tokens.accentBlue;
    case 'needs-input': return tokens.accentAmber;
    case 'done': return tokens.accentGreen;
    case 'stale': return tokens.fgDim;
    default: return tokens.fgMuted;
  }
}

export function stateLabel(row: AgentRow): string {
  switch (row.state) {
    case 'needs-input': return row.reason ? `needs input · ${row.reason}` : 'needs input';
    case 'stale': return 'not responding';
    default: return row.state;
  }
}

export function fmtElapsed(ms: number): string {
  const secs = Math.max(0, Math.floor(ms / 1000));
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  return `${Math.floor(secs / 3600)}h`;
}

export const AgentsWidget: Component<AgentsWidgetProps> = (props) => {
  const empty = () => props.rows().length === 0;
  return (
    <div
      data-testid="agents-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      <Show when={empty()}>
        <div
          data-testid="agents-empty"
          style={{
            opacity: 0.5,
            'font-style': 'italic',
            'text-align': 'center',
            padding: '12px 0',
            'font-size': '11px',
          }}
        >
          no agents running
        </div>
      </Show>
      <For each={props.rows()}>
        {(r) => (
          <AgentRowView
            row={r}
            elapsed={fmtElapsed(props.now() - props.startedAt(r.key))}
            onFocus={() => props.onFocus(r)}
          />
        )}
      </For>
    </div>
  );
};

const AgentRowView: Component<{ row: AgentRow; elapsed: string; onFocus: () => void }> = (props) => {
  // Where it's working: "wash · main*" — repo, branch, and a star when the
  // tree is dirty. Absent for an agent outside a checkout.
  const place = (): string => {
    const bits: string[] = [];
    if (props.row.dir) bits.push(props.row.dir);
    if (props.row.branch) bits.push(props.row.branch + (props.row.dirty ? '*' : ''));
    return bits.join(' · ');
  };
  const rowStyle = (): JSX.CSSProperties => ({
    'border-left': `3px solid ${stateColor(props.row.state)}`,
    background: props.row.state === 'needs-input' ? 'rgba(224,178,95,0.10)' : 'rgba(255,255,255,0.02)',
    padding: '6px 8px',
    'border-radius': tokens.radiusSm,
    cursor: 'pointer',
    'font-size': '11px',
    opacity: props.row.state === 'stale' ? 0.55 : 1,
    display: 'flex',
    'flex-direction': 'column',
    gap: '2px',
  });
  return (
    <div
      data-testid={`agents-row-${props.row.key}`}
      data-agent={props.row.agent}
      data-agent-state={props.row.state}
      style={rowStyle()}
      onClick={props.onFocus}
      title={`${props.row.agent} in ${props.row.cwd || 'unknown directory'} — click to go to its terminal`}
    >
      <div style={{ display: 'flex', 'align-items': 'baseline', gap: '6px' }}>
        <span
          data-testid="agents-dot"
          style={{
            width: '7px',
            height: '7px',
            'border-radius': '50%',
            background: stateColor(props.row.state),
            'flex-shrink': 0,
          }}
        />
        <span style={{ 'font-weight': 600, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
          {props.row.agent}
        </span>
        <Show when={place()}>
          <span style={{ opacity: 0.7, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
            {place()}
          </span>
        </Show>
      </div>
      <div style={{ display: 'flex', 'justify-content': 'space-between', gap: '6px', opacity: 0.8 }}>
        <span data-testid="agents-state">{stateLabel(props.row)}</span>
        <span style={{ 'font-variant-numeric': 'tabular-nums', 'flex-shrink': 0 }}>{props.elapsed}</span>
      </div>
    </div>
  );
};
