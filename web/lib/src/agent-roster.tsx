// AgentRoster renders the coding-agent roster fed by com.wash.agentd
// (docs/AGENT_TERM.md §7). One row per agent wash can see, across every
// window: what it is, where it's working, what it's doing, and for how
// long.
//
// The roster's job is to answer "who needs me?" without hunting through
// windows, so the service sorts needs-input first and this renders that
// order as given.
//
// It lives in @wash/ui because it has two homes (docs/SIDEBAR.md M2):
// com.wash.ai's roster pane, which is where the VERBS belong — an app
// talking to its own host's agentd has a router-attested sender, so it can
// act — and, until M2c, the desktop rail. Same renderer either way; the
// difference is which callbacks the host passes.
//
// Pure renderer; subscription wiring lives in the consumer.

import type { Component, JSX } from 'solid-js';
import { For, Show, createSignal } from 'solid-js';
import { Menu, MenuItem, MenuSeparator } from './menu';
import { tokens } from './tokens';
import type { AgentConfig } from './agent-session';

export interface RosterRow {
  key: string;
  agent: string;
  /** running | working | needs-input | done | stale */
  state: string;
  reason?: string;
  /** still running, no window pointing at it — double-clicking reattaches */
  detached?: boolean;
  session_id?: string;
  /** the agent's own name for this session, when it has one */
  title?: string;
  cwd?: string;
  dir?: string;
  branch?: string;
  dirty?: boolean;
  term_instance: string;
  window_id: number;
  channel_id: number;
  /** elapsed in this state as of the push; anchored locally by the App */
  since_ms: number;
  // The rest of what agentd publishes per row (apps/agentd/be/app.go).
  // The rail never read these; com.wash.ai's status line and Session menu
  // do, which is why they belong on the shared type rather than on a
  // near-duplicate one in the app.
  /** the agent's context accounting, from its usage_update */
  used?: number;
  size?: number;
  /** the agent's active approval preset, and what it offers */
  mode?: string;
  modes?: { id: string; name: string; description?: string }[];
  /** wash answering this session's permission questions on its own */
  yolo?: boolean;
  /** the agent's generic settings block (model, reasoning effort, …) */
  configs?: AgentConfig[];
  /** the agent's own slash commands */
  commands?: { name: string; description?: string }[];
}

/** A permission question waiting for a human (docs/AGENT_TERM.md §12). */
export interface RosterAsk {
  id: string;
  agent: string;
  tool: string;
  subject?: string;
  cwd?: string;
  dir?: string;
  /** what "Always allow" would write — shown ON the button */
  suggested_rule?: string;
  row_key: string;
  /** who asked — attribution only; the answer routes by `id` in agentd */
  source_app?: string;
  source_instance?: string;
  age_ms: number;
}

/** A remembered agent session (docs/AGENT_TERM.md §13). */
export interface RosterSession {
  session_id: string;
  agent: string;
  cwd?: string;
  dir?: string;
  /** unix seconds */
  last_seen: number;
  /** running right now — it's in the roster above, so don't offer resume */
  live?: boolean;
}

export interface AgentRosterProps {
  rows: () => RosterRow[];
  /** local clock anchor per row key, so elapsed keeps counting between pushes */
  startedAt: (key: string) => number;
  /** ticking "now" from the App, so every row's clock advances together */
  now: () => number;
  /** activate a row. The host decides what that means: the desktop rail
   *  went to the owning terminal; com.wash.ai points its detail pane at
   *  the session. */
  onActivate: (row: RosterRow) => void;
  /** the session the host is currently showing, marked as current */
  activeKey?: () => string;
  /** a detached session is still running with no window — open one */
  onReattach?: (row: RosterRow) => void;
  /** permission questions waiting on the human */
  asks?: () => RosterAsk[];
  /** answer one: decision allow|deny, remember writes the named rule */
  onAnswer?: (ask: RosterAsk, decision: 'allow' | 'deny', remember: boolean) => void;
  /** remembered sessions, most recent first */
  recent?: () => RosterSession[];
  /** reopen one — agentd loads it and opens an Agent window */
  onResume?: (session: RosterSession, fork: boolean) => void;
  /** put the session id on the clipboard */
  onCopyID?: (session: RosterSession) => void;
  /** let the window go, keep the session running */
  onDetach?: (row: RosterRow) => void;
  /** end the current turn; the session stays available */
  onCancel?: (row: RosterRow) => void;
  /** end the session and its adapter process */
  onStop?: (row: RosterRow) => void;
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

export function stateLabel(row: RosterRow): string {
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

export const AgentRoster: Component<AgentRosterProps> = (props) => {
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
            onActivate={() => props.onActivate(r)}
            active={props.activeKey?.() === r.key}
            onReattach={r.detached ? () => props.onReattach?.(r) : undefined}
            detached={r.detached === true}
            onDetach={props.onDetach ? () => props.onDetach?.(r) : undefined}
            onCancel={props.onCancel ? () => props.onCancel?.(r) : undefined}
            onStop={props.onStop ? () => props.onStop?.(r) : undefined}
          />
        )}
      </For>
      {/* Earlier sessions live in the Agent app's History menu now. The
          sidebar answers "what is running"; a list of things that are
          NOT running was answering a different question in the same
          space. */}
    </div>
  );
};

// fmtAgo renders "just now / 5m ago / 3h ago / 2d ago" from unix seconds.
export function fmtAgo(nowMS: number, unixSec: number): string {
  if (!unixSec) return '';
  const secs = Math.max(0, Math.floor(nowMS / 1000) - unixSec);
  if (secs < 45) return 'just now';
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86_400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86_400)}d ago`;
}

// AskRow is the actionable half of the roster: what the agent wants to do,
// and the three answers. "Always allow" names the exact rule it will write
// — what you clicked is what gets saved.
const AskRow: Component<{
  ask: RosterAsk;
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

const AgentRowView: Component<{
  row: RosterRow;
  elapsed: string;
  onActivate: () => void;
  onReattach?: () => void;
  detached?: boolean;
  /** this row is the session the host is showing right now */
  active?: boolean;
  onDetach?: () => void;
  onCancel?: () => void;
  onStop?: () => void;
}> = (props) => {
  // The verbs live in a menu rather than a strip of buttons: the set
  // grows (resume and fork are still to come) and a sidebar row is 190px
  // wide, so buttons would either wrap into a block or start hiding
  // themselves. A menu also lets an unavailable verb render DISABLED
  // instead of vanishing — "Stop turn" greyed out teaches that the verb
  // exists and why it doesn't apply, which a missing button cannot.
  const [menuAt, setMenuAt] = createSignal<{ x: number; y: number } | null>(null);
  // Ending a session kills the adapter and everything it was holding,
  // and it sits next to Detach in the list. Opening a menu is already
  // deliberate, but picking the wrong row of a small list is not, so the
  // item asks once inside the menu it was picked from.
  const [confirmEnd, setConfirmEnd] = createSignal(false);
  const openMenu = (e: MouseEvent) => {
    // Both triggers must stop the row's own click, which focuses.
    e.preventDefault();
    e.stopPropagation();
    setConfirmEnd(false);
    setMenuAt({ x: e.clientX, y: e.clientY });
  };
  const closeMenu = () => {
    setMenuAt(null);
    setConfirmEnd(false);
  };
  // Menu coords are viewport coords: Menu portals to document.body and
  // lays out position:fixed, so clientX/clientY is what it wants.
  const run = (fn?: () => void) => () => {
    closeMenu();
    fn?.();
  };
  const hasVerbs = () => Boolean(props.onDetach || props.onCancel || props.onStop);
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
    // The row the host is showing reads as selected. Kept subtle: the
    // state colour on the left edge is the row's primary signal and a
    // strong selection fill would out-shout it.
    background: props.active
      ? 'rgba(255,255,255,0.09)'
      : props.row.state === 'needs-input' ? 'rgba(224,178,95,0.10)' : 'rgba(255,255,255,0.02)',
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
      data-active={props.active ? 'true' : 'false'}
      style={rowStyle()}
      // Attached rows retain their fast single-click focus. Detached rows
      // wait for dblclick so the two click events preceding it cannot spawn
      // two Agent windows for the same session.
      onClick={() => { if (!props.detached) props.onActivate(); }}
      onDblClick={() => { if (props.detached) props.onReattach?.(); }}
      onContextMenu={(e) => hasVerbs() && openMenu(e)}
      // One title, chosen. There used to be two attributes here and JSX
      // kept the last, so the detached hint never rendered — a detached
      // row claimed clicking went "to its terminal", which is the one
      // thing it does not have.
      title={
        props.detached
          ? 'Detached — double-click to open a window on it'
          : `${props.row.agent} in ${props.row.cwd || 'unknown directory'}`
      }
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
      {/* What the session is ABOUT, in the agent's own words. It names
          itself once it works out what the work is, so this costs no
          extra model call — and a sidebar of "codex · wash" rows tells
          you nothing the moment there are three of them. */}
      <Show when={props.row.title}>
        <div
          data-testid="agents-title"
          style={{ opacity: 0.75, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}
        >
          {props.row.title}
        </div>
      </Show>
      <div style={{ display: 'flex', 'align-items': 'baseline', gap: '6px', opacity: 0.8 }}>
        <span data-testid="agents-state">{stateLabel(props.row)}</span>
        <span
          style={{ 'font-variant-numeric': 'tabular-nums', 'flex-shrink': 0, 'margin-left': 'auto' }}
        >
          {props.elapsed}
        </span>
        {/* Right-click works on the whole row, but a right-click-only
            verb is a verb nobody finds. The ellipsis is the discoverable
            half of the same menu. */}
        <Show when={hasVerbs()}>
          <button
            type="button"
            data-testid="agents-row-menu"
            title="Session actions"
            aria-label="Session actions"
            aria-haspopup="menu"
            onClick={openMenu}
            style={{
              background: 'transparent',
              color: tokens.fg,
              border: 'none',
              padding: '0 2px',
              cursor: 'pointer',
              'font-size': '12px',
              'line-height': 1,
              'flex-shrink': 0,
            }}
          >
            ⋯
          </button>
        </Show>
      </div>
      <Show when={menuAt()}>
        {(at) => (
          <Menu
            x={at().x}
            y={at().y}
            onDismiss={closeMenu}
            data-testid="agents-row-actions"
          >
            <Show
              when={!confirmEnd()}
              fallback={
                <>
                  {/* The confirm replaces the list rather than nesting:
                      the destructive item must not stay one pixel from
                      the pointer that just landed on it. */}
                  <MenuItem
                    label="Confirm — end this session"
                    data-testid="agents-menu-end-confirm"
                    onClick={run(props.onStop)}
                  />
                  <MenuItem label="Cancel" data-testid="agents-menu-end-cancel" onClick={closeMenu} />
                </>
              }
            >
              <MenuItem
                label={props.detached ? 'Attach a window' : 'Go to its window'}
                data-testid="agents-menu-attach"
                onClick={run(props.detached ? props.onReattach : props.onActivate)}
              />
              <MenuItem
                label="Stop turn"
                data-testid="agents-menu-cancel"
                // Disabled rather than absent: it says the verb exists
                // and that there is simply no turn to stop.
                disabled={!props.onCancel || props.row.state !== 'working'}
                onClick={run(props.onCancel)}
              />
              <MenuItem
                label="Detach"
                data-testid="agents-menu-detach"
                disabled={!props.onDetach || props.detached === true}
                onClick={run(props.onDetach)}
              />
              <MenuSeparator />
              <MenuItem
                label="End session…"
                data-testid="agents-menu-end"
                disabled={!props.onStop}
                onClick={() => setConfirmEnd(true)}
              />
            </Show>
          </Menu>
        )}
      </Show>
    </div>
  );
};
