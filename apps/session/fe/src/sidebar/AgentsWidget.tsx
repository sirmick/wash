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

/** A permission question waiting for a human (docs/AGENT_TERM.md §12). */
export interface AgentAsk {
  id: string;
  agent: string;
  tool: string;
  subject?: string;
  cwd?: string;
  dir?: string;
  /** what "Always allow" would write — shown ON the button */
  suggested_rule?: string;
  row_key: string;
  term_instance: string;
  age_ms: number;
}

export interface AgentsWidgetProps {
  rows: () => AgentRow[];
  /** local clock anchor per row key, so elapsed keeps counting between pushes */
  startedAt: (key: string) => number;
  /** ticking "now" from the App, so every row's clock advances together */
  now: () => number;
  /** go to the terminal that owns this agent */
  onFocus: (row: AgentRow) => void;
  /** permission questions waiting on the human */
  asks?: () => AgentAsk[];
  /** answer one: decision allow|deny, remember writes the named rule */
  onAnswer?: (ask: AgentAsk, decision: 'allow' | 'deny', remember: boolean) => void;
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
      {/* Questions first: an agent blocked on a human outranks every
          status line below it. */}
      <For each={props.asks?.() ?? []}>
        {(a) => <AskRow ask={a} onAnswer={(d, r) => props.onAnswer?.(a, d, r)} />}
      </For>
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

// AskRow is the actionable half of the roster: what the agent wants to do,
// and the three answers. "Always allow" names the exact rule it will write
// — what you clicked is what gets saved.
const AskRow: Component<{
  ask: AgentAsk;
  onAnswer: (decision: 'allow' | 'deny', remember: boolean) => void;
}> = (props) => {
  const what = () => {
    const s = props.ask.subject ?? '';
    return s ? `${props.ask.tool}: ${s}` : props.ask.tool;
  };
  return (
    <div
      // Identity goes in a data attribute, NOT the testid: the buttons
      // below are testid'd agents-ask-allow/-always/-deny, so an
      // id-suffixed container testid would make [data-testid^="agents-ask-"]
      // match five elements per question (it did, twice).
      data-testid="agents-ask"
      data-ask-id={props.ask.id}
      data-tool={props.ask.tool}
      style={{
        'border-left': `3px solid ${tokens.accentAmber}`,
        background: 'rgba(224,178,95,0.14)',
        padding: '7px 8px',
        'border-radius': tokens.radiusSm,
        'font-size': '11px',
        display: 'flex',
        'flex-direction': 'column',
        gap: '5px',
      }}
    >
      <div>
        <span style={{ 'font-weight': 600 }}>{props.ask.agent}</span>
        <span style={{ opacity: 0.8 }}> wants to run</span>
        <Show when={props.ask.dir}>
          <span style={{ opacity: 0.6 }}> in {props.ask.dir}</span>
        </Show>
      </div>
      <div
        data-testid="agents-ask-what"
        style={{
          font: tokens.type.monoSm,
          background: tokens.bgInset,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': tokens.radiusSm,
          padding: '3px 5px',
          'word-break': 'break-all',
          'max-height': '54px',
          overflow: 'hidden',
        }}
      >
        {what()}
      </div>
      <div style={{ display: 'flex', gap: '4px', 'flex-wrap': 'wrap' }}>
        <AskBtn testid="agents-ask-allow" onClick={() => props.onAnswer('allow', false)}>Allow</AskBtn>
        <Show when={props.ask.suggested_rule}>
          <AskBtn
            testid="agents-ask-always"
            title={`Writes the rule ${props.ask.suggested_rule} to your agent policy`}
            onClick={() => props.onAnswer('allow', true)}
          >
            Always {props.ask.suggested_rule}
          </AskBtn>
        </Show>
        <AskBtn testid="agents-ask-deny" onClick={() => props.onAnswer('deny', false)}>Deny</AskBtn>
      </div>
    </div>
  );
};

const AskBtn: Component<{
  testid: string;
  title?: string;
  onClick: () => void;
  children: JSX.Element;
}> = (props) => (
  <button
    type="button"
    data-testid={props.testid}
    title={props.title}
    onClick={props.onClick}
    style={{
      background: tokens.bgMenu,
      color: tokens.fg,
      border: `1px solid ${tokens.borderMenu}`,
      'border-radius': tokens.radiusSm,
      padding: '3px 8px',
      cursor: 'pointer',
      'font-size': '11px',
      'max-width': '100%',
      overflow: 'hidden',
      'text-overflow': 'ellipsis',
      'white-space': 'nowrap',
    }}
  >
    {props.children}
  </button>
);

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
