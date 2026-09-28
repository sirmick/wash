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
import { For, Show, createMemo, createSignal } from 'solid-js';
import { Menu, MenuItem, MenuSeparator } from './menu';
import { tokens } from './tokens';
import { agentStateColor, agentStateLabel } from './agent-status';
import type * as agentproto from './agent-protocol.gen';

/** A team entry under an orchestrator: a package heading or a member row. */
type TeamEntry = { pkg: string; label: string } | { key: string };

/**
 * Splits the roster into top-level rows and each orchestrator's team.
 * Members whose orchestrator has no row here stay top-level, labelled.
 * A team lists members without a package first, then by package code, then
 * by name: a stable order, where the attention sort would reshuffle it on
 * every state change. Questions stay at the top of the pane either way.
 */
export function rosterTeams(rows: agentproto.Row[]): { top: string[]; teams: Map<string, TeamEntry[]> } {
  const leads = new Map<string, string>();
  for (const r of rows) if (r.workspace?.orchestrator && r.session_id) leads.set(r.session_id, r.key);
  const members = new Map<string, agentproto.Row[]>();
  const top: string[] = [];
  for (const r of rows) {
    const lead = r.workspace && !r.workspace.orchestrator ? leads.get(r.workspace.lead_session) : undefined;
    if (lead === undefined) top.push(r.key);
    else members.set(lead, [...(members.get(lead) ?? []), r]);
  }
  const teams = new Map<string, TeamEntry[]>();
  for (const [lead, list] of members) {
    list.sort((a, b) =>
      (a.workspace!.node ?? '').localeCompare(b.workspace!.node ?? '') ||
      a.workspace!.member.localeCompare(b.workspace!.member) ||
      a.key.localeCompare(b.key));
    const entries: TeamEntry[] = [];
    let pkg = '';
    for (const r of list) {
      const code = r.workspace!.node ?? '';
      if (code && code !== pkg) {
        const title = r.workspace!.node_title;
        entries.push({ pkg: code, label: title ? `${code} · ${title}` : code });
      }
      pkg = code;
      entries.push({ key: r.key });
    }
    teams.set(lead, entries);
  }
  return { top, teams };
}

/** A permission question waiting for a human (docs/AGENT_TERM.md §12). */
/** A remembered agent session (docs/AGENT_TERM.md §13). */
export interface AgentRosterProps {
  rows: () => agentproto.Row[];
  /** local clock anchor per row key, so elapsed keeps counting between pushes */
  startedAt: (key: string) => number;
  /** ticking "now" from the App, so every row's clock advances together */
  now: () => number;
  /** activate a row. The host decides what that means: the desktop rail
   *  went to the owning terminal; com.wash.ai points its detail pane at
   *  the session. */
  onActivate: (row: agentproto.Row) => void;
  /** the session the host is currently showing, marked as current */
  activeKey?: () => string;
  /** a detached session is still running with no window — open one */
  onReattach?: (row: agentproto.Row) => void;
  /** permission questions waiting on the human */
  asks?: () => agentproto.Ask[];
  /** Question sets waiting for the human; each opens its asking session. */
  questions?: () => agentproto.PendingQuestion[];
  /** answer one: decision allow|deny, remember writes the named rule */
  onAnswer?: (ask: agentproto.Ask, decision: 'allow' | 'deny', remember: boolean, scope?: 'workspace') => void;
  // recent / onResume / onCopyID used to live here. They went with
  // RecentRow: the roster answers "what is running", and reopening
  // something that ISN'T is com.wash.ai's History menu and HistoryPanel,
  // which can search transcripts and carry metadata a roster row cannot.
  /** let the window go, keep the session running */
  onDetach?: (row: agentproto.Row) => void;
  /** end the current turn; the session stays available */
  onCancel?: (row: agentproto.Row) => void;
  /** end the session and its adapter process */
  onStop?: (row: agentproto.Row) => void;
  /** give the session a name of your own; the host opens its dialog */
  onRename?: (row: agentproto.Row) => void;
  /** allow the session another folder; the host opens its file picker */
  onAddRoot?: (row: agentproto.Row) => void;
  /** open a terminal in the session's working directory */
  onOpenTerminal?: (row: agentproto.Row) => void;
  onOpenFileManager?: (row: agentproto.Row) => void;
  onOpenTextEditor?: (row: agentproto.Row) => void;
}

// stateLabel is a roster row's state in the shared vocabulary of
// agent-status.ts (docs/AGENT_MESSENGER.md M5).
export function stateLabel(row: agentproto.Row): string {
  // Work left running in the background outlives the turn: the session is
  // waiting on it, not done.
  if (row.background && row.state !== 'needs-input' && row.state !== 'working') return `background · ${row.background}`;
  return agentStateLabel(row.state, row.reason);
}

export function fmtElapsed(ms: number): string {
  const secs = Math.max(0, Math.floor(ms / 1000));
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  return `${Math.floor(secs / 3600)}h`;
}

/**
 * AgentAsks is the questions half on its own.
 *
 * It exists because the desktop rail keeps answering permission questions
 * after docs/SIDEBAR.md M2c moved everything else into com.wash.ai — a
 * named exception to "mutation belongs in the app" (§3), decided
 * deliberately. Everything else that moved, you were opening the app for
 * anyway: to read the transcript, to reply, to watch it work. Answering
 * yes/no is the one case where opening a window is pure overhead, and an
 * agent blocked on a human is the single thing the rail exists to
 * surface. One click stays one click.
 *
 * The roster renders this too, so an Agent window can answer without
 * going back to the rail.
 */
export const AgentAsks: Component<{
  asks: () => agentproto.Ask[];
  onAnswer?: (ask: agentproto.Ask, decision: 'allow' | 'deny', remember: boolean, scope?: 'workspace') => void;
}> = (props) => (
  <For each={props.asks()}>
    {(a) => <AskRow ask={a} onAnswer={(d, r, scope) => props.onAnswer?.(a, d, r, scope)} />}
  </For>
);

export const AgentRoster: Component<AgentRosterProps> = (props) => {
  const rowByKey = createMemo(() => new Map(props.rows().map((r) => [r.key, r] as const)));
  const teams = createMemo(() => rosterTeams(props.rows()));
  const sameKeys = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);
  const rowKeys = createMemo(() => teams().top, undefined, { equals: sameKeys });
  // Entries are strings so <For> keeps each one across pushes, like rows.
  const teamKeys = (lead: string) =>
    (teams().teams.get(lead) ?? []).map((e) => ('pkg' in e ? `pkg\u0000${e.pkg}\u0000${e.label}` : `row\u0000${e.key}`));
  const empty = () => props.rows().length === 0;
  const rowView = (key: string, depth: number) => {
    const r = () => rowByKey().get(key)!;
    return (
      <Show when={rowByKey().has(key)}>
        <AgentRowView
          row={r()}
          depth={depth}
          members={depth === 0 ? (teams().teams.get(key) ?? []).filter((e) => 'key' in e).length : 0}
          elapsed={fmtElapsed(props.now() - props.startedAt(key))}
          onActivate={() => props.onActivate(r())}
          active={props.activeKey?.() === key}
          onReattach={r().detached ? () => props.onReattach?.(r()) : undefined}
          detached={r().detached === true}
          onDetach={props.onDetach ? () => props.onDetach?.(r()) : undefined}
          onCancel={props.onCancel ? () => props.onCancel?.(r()) : undefined}
          onStop={props.onStop ? () => props.onStop?.(r()) : undefined}
          onRename={props.onRename ? () => props.onRename?.(r()) : undefined}
          onAddRoot={props.onAddRoot ? () => props.onAddRoot?.(r()) : undefined}
          onOpenTerminal={props.onOpenTerminal ? () => props.onOpenTerminal?.(r()) : undefined}
          onOpenFileManager={props.onOpenFileManager ? () => props.onOpenFileManager?.(r()) : undefined}
          onOpenTextEditor={props.onOpenTextEditor ? () => props.onOpenTextEditor?.(r()) : undefined}
        />
      </Show>
    );
  };
  return (
    <div
      data-testid="agents-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}
    >
      {/* Questions first: an agent blocked on a human outranks every
          status line below it. */}
      <AgentAsks asks={() => props.asks?.() ?? []} onAnswer={props.onAnswer} />
      {/* Question sets are answered in the asking session's window, where
          they are pinned above its composer; here they are a way there. */}
      <For each={props.questions?.() ?? []}>{(q) => (
        <button data-wash-hit type="button" data-testid={`agents-question-${q.id}`}
          onClick={() => { const r = rowByKey().get(q.row_key); if (r) props.onActivate(r); }}
          style={{
            'text-align': 'left', cursor: 'pointer', font: tokens.type.textSm, color: tokens.fg,
            background: tokens.bgDenied, border: `1px solid ${tokens.borderDenied}`, 'border-radius': tokens.radiusMd,
            padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
          }}>
          <span style={{ color: tokens.accentAmber, 'font-weight': 600 }}>● Needs you</span>{' '}
          {q.workspace_name || q.agent}: {q.set.title || q.set.questions[0]?.question}
        </button>
      )}</For>
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
      {/* Keyed by session key, not by row object. Every roster push,
          usage patch and preview patch hands this a NEW object for a row
          whose identity has not changed; <For> keys by reference, so each
          one tore that row down — and an open row menu went with it, a
          beat after a turn ended, under the cursor. The row is read
          through an accessor, so its fields still update in place. */}
      <For each={rowKeys()}>
        {(key) => {
          const members = createMemo(() => teamKeys(key), undefined, { equals: sameKeys });
          return (
            <>
              {rowView(key, 0)}
              {/* The orchestrator's team, indented under it on one guide
                  line, so which sessions are its members is visible at a
                  glance rather than inferred from matching folders. */}
              <Show when={members().length > 0}>
                <div
                  data-testid={`agents-team-${key}`}
                  style={{
                    display: 'flex',
                    'flex-direction': 'column',
                    gap: '4px',
                    'margin-left': '10px',
                    'padding-left': '8px',
                    'border-left': `2px solid ${tokens.borderFocus}`,
                  }}
                >
                  <For each={members()}>
                    {(entry) => {
                      const [kind, a, b] = entry.split('\u0000');
                      return kind === 'pkg' ? (
                        <div
                          data-testid={`agents-package-${a}`}
                          style={{
                            'font-size': '10px',
                            'font-weight': 600,
                            opacity: 0.6,
                            'text-transform': 'uppercase',
                            'letter-spacing': '0.04em',
                            padding: '4px 0 0',
                            overflow: 'hidden',
                            'text-overflow': 'ellipsis',
                            'white-space': 'nowrap',
                          }}
                        >
                          {b}
                        </div>
                      ) : (
                        rowView(a, 1)
                      );
                    }}
                  </For>
                </div>
              </Show>
            </>
          );
        }}
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
  ask: agentproto.Ask;
  onAnswer: (decision: 'allow' | 'deny', remember: boolean, scope?: 'workspace') => void;
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
        {/* Covers every member of the team, in whatever worktree each one
            works in — the per-directory rule below covers only this one. */}
        <Show when={props.ask.suggested_rule && props.ask.workspace_name}>
          <AskBtn
            testid="agents-ask-always-workspace"
            title={`Writes ${props.ask.suggested_rule} for every member of ${props.ask.workspace_name}`}
            onClick={() => props.onAnswer('allow', true, 'workspace')}
          >
            Always {props.ask.suggested_rule} for {props.ask.workspace_name}
          </AskBtn>
        </Show>
        <Show when={props.ask.suggested_rule}>
          <AskBtn
            testid="agents-ask-always"
            title={
              props.ask.rule_cwd
                ? `Writes the rule ${props.ask.suggested_rule} to your agent policy — only for ${props.ask.rule_cwd}`
                : `Writes the rule ${props.ask.suggested_rule} to your agent policy`
            }
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
    data-wash-hit
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
  row: agentproto.Row;
  /** 1 for a member listed under its orchestrator */
  depth?: number;
  /** how many members list under this orchestrator row */
  members?: number;
  elapsed: string;
  onActivate: () => void;
  onReattach?: () => void;
  detached?: boolean;
  /** this row is the session the host is showing right now */
  active?: boolean;
  onDetach?: () => void;
  onCancel?: () => void;
  onStop?: () => void;
  onRename?: () => void;
  onAddRoot?: () => void;
  onOpenTerminal?: () => void;
  onOpenFileManager?: () => void;
  onOpenTextEditor?: () => void;
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
  const hasVerbs = () =>
    Boolean(props.onDetach || props.onCancel || props.onStop || props.onRename || props.onAddRoot || props.onOpenTerminal || props.onOpenFileManager || props.onOpenTextEditor);
  // Where it's working: "wash · main*" — repo, branch, and a star when the
  // tree is dirty. Absent for an agent outside a checkout.
  const place = (): string => {
    const bits: string[] = [];
    if (props.row.dir) bits.push(props.row.dir);
    if (props.row.branch) bits.push(props.row.branch + (props.row.dirty ? '*' : ''));
    return bits.join(' · ');
  };
  const rowStyle = (): JSX.CSSProperties => ({
    'border-left': `3px solid ${agentStateColor(props.row.state)}`,
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
      data-wash-hit
      data-testid={`agents-row-${props.row.key}`}
      data-agent={props.row.agent}
      data-agent-state={props.row.state}
      data-active={props.active ? 'true' : 'false'}
      style={rowStyle()}
      // One click, every row (docs/AGENT_UX.md N4). Detached rows used to
      // insist on a dblclick, on the theory that the two click events
      // preceding it could spawn two Agent windows — but agentd's
      // claimDetached is atomic and was always the real guard (see
      // TestClaimDetachedAllowsOnlyOneReattach), so the dblclick bought
      // nothing except a row that ignored the first click people gave it.
      //
      // The verbs Menu portals to document.body, but Solid delegates a
      // portal's events through its owner — so picking "End session…"
      // arrived HERE too and raised the controller window over the menu
      // mid-confirm. Only a click inside the row's own DOM activates it.
      onClick={(e) => {
        if (!e.currentTarget.contains(e.target as Node)) return;
        if (props.detached) props.onReattach?.();
        else props.onActivate();
      }}
      onContextMenu={(e) => {
        if (!e.currentTarget.contains(e.target as Node)) return;
        if (hasVerbs()) openMenu(e);
      }}
      // One title, chosen. There used to be two attributes here and JSX
      // kept the last, so the detached hint never rendered — a detached
      // row claimed clicking went "to its terminal", which is the one
      // thing it does not have.
      title={
        props.detached
          ? 'Detached — click to open a window on it'
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
            background: agentStateColor(props.row.state),
            'flex-shrink': 0,
          }}
        />
        {/* A member leads with its name in the team; the agent slug says
            less once every row under an orchestrator is "claude". */}
        <Show when={props.row.workspace && !props.row.workspace.orchestrator}>
          <span data-testid="agents-member" style={{ 'font-weight': 600, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
            {props.row.workspace!.member}
          </span>
        </Show>
        <span
          style={{
            'font-weight': props.row.workspace && !props.row.workspace.orchestrator ? 400 : 600,
            opacity: props.row.workspace && !props.row.workspace.orchestrator ? 0.7 : 1,
            overflow: 'hidden',
            'text-overflow': 'ellipsis',
            'white-space': 'nowrap',
          }}
        >
          {props.row.agent}
        </span>
        <Show when={place()}>
          <span style={{ opacity: 0.7, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
            {place()}
          </span>
        </Show>
      </div>
      {/* Team membership is said in words, not only by indentation: an
          orchestrator names its workspace and team size, and a member
          whose orchestrator has no row here still says whose it is. */}
      <Show when={props.row.workspace?.orchestrator}>
        <div data-testid="agents-orchestrator" style={{ 'font-size': '10px', 'font-weight': 600, color: tokens.accentBlue, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
          Orchestrator · {props.row.workspace!.name}
          {props.members ? ` · ${props.members} member${props.members === 1 ? '' : 's'}` : ''}
        </div>
      </Show>
      <Show when={props.row.workspace && !props.row.workspace.orchestrator && !props.depth}>
        <div data-testid="agents-member-of" style={{ 'font-size': '10px', opacity: 0.7, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
          Member of {props.row.workspace!.name}
        </div>
      </Show>
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
      <Show when={props.row.preview}>
        <div
          data-testid="agents-preview"
          style={{
            opacity: 0.62,
            'font-size': '10px',
            'line-height': 1.35,
            'white-space': 'pre-line',
            overflow: 'hidden',
            display: '-webkit-box',
            '-webkit-box-orient': 'vertical',
            '-webkit-line-clamp': '2',
            'word-break': 'break-word',
          }}
        >
          {props.row.preview}
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
            data-wash-hit
            type="button"
            // Named to collide with nothing: "agents-row-menu" made a
            // prefix query for rows (agents-row-<key>) match this button
            // too, and "agents-menu" would have been a prefix of the
            // agents-menu-* items below. Same trap the ask block
            // documents; it caught the M2b roster e2e counting two rows
            // for one session.
            data-testid="agents-verbs-btn"
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
              {/* The agent names the session once and first wins; this
                  is how a person overrides it. Needs a session id — the
                  name is stored against the agent's id, so a row that
                  has none yet has nothing to name. */}
              <MenuItem
                label="Rename…"
                data-testid="agents-menu-rename"
                disabled={!props.onRename || !props.row.session_id}
                onClick={run(props.onRename)}
              />
              {/* The session cwd is the scope the person consented to; it
                  is the wrong LIMIT. A monorepo sibling, a generated
                  schema in another tree — the alternative was starting the
                  agent at a parent and granting far more than the two
                  folders it needed. The label counts what is already
                  allowed, because the whole hazard of widening is
                  forgetting you did. */}
              <MenuItem
                label={
                  (props.row.roots?.length ?? 0) > 0
                    ? `Also allow a folder… (${props.row.roots!.length})`
                    : 'Also allow a folder…'
                }
                data-testid="agents-menu-add-root"
                disabled={!props.onAddRoot}
                onClick={run(props.onAddRoot)}
              />
              {/* Where the agent is working is exactly where a person
                  wants a shell — to run the test it just changed, to see
                  the diff it made. Needs a cwd: a row with none has
                  nowhere to open. */}
              <MenuItem
                label="Open terminal in project folder"
                data-testid="agents-menu-open-terminal"
                disabled={!props.onOpenTerminal || !props.row.cwd}
                onClick={run(props.onOpenTerminal)}
              />
              <MenuItem
                label="Open file manager in project folder"
                data-testid="agents-menu-open-file-manager"
                disabled={!props.onOpenFileManager || !props.row.cwd}
                onClick={run(props.onOpenFileManager)}
              />
              <MenuItem
                label="Open text editor in project folder"
                data-testid="agents-menu-open-text-editor"
                disabled={!props.onOpenTextEditor || !props.row.cwd}
                onClick={run(props.onOpenTextEditor)}
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
