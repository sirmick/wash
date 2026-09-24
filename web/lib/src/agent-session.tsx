// <AgentSession> — the transcript surface for a managed agent session
// (docs/AGENT_APP.md §9).
//
// Rendered by three hosts: the standalone com.wash.ai window, a wash-term
// pane, and a wash-edit panel. It therefore owns NOTHING — no session, no
// launcher, no approval logic, no subscription. Everything arrives as an
// accessor and leaves as a callback, exactly the contract <Terminal> has.
//
// The transcript is one line per tool call, plus the diff that call made
// when it made one: commands run in a wash-term tab and approvals live in
// agentd's queue, but a change to a file is the one thing a person must
// be able to read WITHOUT leaving the conversation to go and find it.

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';
import { agentStateColor, agentStateLabel } from './agent-status';
import { HighlightedCode, Markdown } from './markdown';
import { Terminal } from './terminal';
import { WASH_SCROLL_CLASS } from './scrollbars';
import {
  acceptsDrop,
  describeSkipped,
  fencedAttachment,
  imageFilesFrom,
  imageTooBig,
  insertAt,
  isTextLike,
  MAX_IMAGE_BYTES,
  pathRefs,
  readImageData,
  readTextFile,
  washPathsFrom,
} from './agent-compose-drop';

/** One line in a transcript, as agentd publishes it. */
export interface AgentEvent {
  seq: number;
  /** message | thought | tool */
  kind: string;
  text?: string;
  tool_id?: string;
  /** read | edit | delete | move | search | fetch | execute | think | other */
  tool_kind?: string;
  title?: string;
  /** pending | in_progress | completed | failed */
  status?: string;
  /** the file a tool call touched, when it named one: what a click opens */
  path?: string;
  /** a unified diff of what an edit tool changed, rendered by agentd from
   *  the ACP `diff` content block's before/after pair */
  diff?: string;
  /** set on kind==="image"; text then holds the base64 bytes */
  mime?: string;
  /** set on kind==="decision": why wash allowed or refused (status says which),
   *  and the tool call's subject, already cut to one short line */
  reason?: string;
  detail?: string;
  /** set on kind==="terminal": the raw channel its pty writes to */
  channel?: number;
  at_ms: number;
  /** a wire-only delta: text is what was ADDED to the row with this seq
   *  since the last event for it (see agent-events.ts) */
  append?: boolean;
  /** the row's UTF-8 byte length after this event applies (message/thought) */
  text_len?: number;
}

/** Text pushed into the composer from outside, and a counter that says
 *  "this is a new send" — see AgentSessionProps.insertDraft. */
export interface InsertedDraft {
  text: string;
  seq: number;
}

/** One attachment on its way out with a prompt, in agentd's wire shape
 *  (apps/agentd/be/attach.go): an image travels by value because the
 *  bytes came from a clipboard and exist nowhere on disk; a file travels
 *  by reference, so the agent reads it through the same confinement and
 *  the same permission ask as any other read. */
export interface PromptBlock {
  type: 'image' | 'file';
  /** image: the mime type, and base64 bytes without the data: header */
  mime?: string;
  data?: string;
  /** file: an absolute path, and what to call it */
  path?: string;
  name?: string;
}

/** A permission question waiting on this session. */
export interface AgentAsk {
  id: string;
  tool: string;
  subject?: string;
  suggested_rule?: string;
  /** the directory the rule is confined to, when it is (Bash: per project) */
  rule_cwd?: string;
  /** set when the asking session is a workspace member: the name of that
   *  workspace, which is what the third "always" answer is scoped to. A
   *  per-directory rule has to be re-answered by every member, because each
   *  works in its own worktree. */
  workspace_name?: string;
  age_ms: number;
}

/** What the status line shows. */
export interface AgentStatus {
  agent?: string;
  model?: string;
  dir?: string;
  cwd?: string;
  branch?: string;
  dirty?: boolean;
  /** One of AgentState (see agent-status.ts): running | working |
   *  needs-input | done | failed | stale. */
  state?: string;
  /** qualifies the state: which input is wanted, or how it ended */
  reason?: string;
  /** context tokens used / window size, from the agent's usage_update */
  used?: number;
  size?: number;
  /** the agent's own name for this session */
  title?: string;
  /** the agent's active approval preset, and what it offers */
  mode?: string;
  modes?: { id: string; name: string; description?: string }[];
  /** the agent's generic settings: model, reasoning effort, plan mode… */
  configs?: AgentConfig[];
  /** the agent's own slash commands */
  commands?: { name: string; description?: string }[];
  /** folders allowed beyond `dir` (agentd roots.go). Shown in the status
   *  bar because the whole hazard of widening a session is forgetting
   *  that you did. */
  roots?: string[];
  /** wash is auto-approving this session's permission requests (host-side
   *  yolo). Rendered as a standing badge, never as a quiet flag: an agent
   *  nobody is vetting must not look like one that is being watched. */
  yolo?: boolean;
  /** prompts agentd is holding until the current turn ends. Messenger
   *  semantics: the composer stays open mid-turn, what you send is queued
   *  in order, and the status line says how many are waiting. */
  queued?: number;
}

export interface AgentConfig {
  id: string;
  name: string;
  description?: string;
  current?: string;
  values?: { value: string; name: string; description?: string }[];
}

export interface AgentSessionProps {
  events: () => AgentEvent[];
  asks?: () => AgentAsk[];
  status?: () => AgentStatus;
  /** Send a prompt, with whatever the composer had attached to it.
   *  Absent while the session is not ready. */
  onSend?: (text: string, blocks?: PromptBlock[]) => void;
  /** Pick files to attach, over the session's own folder. Resolves with
   *  absolute paths (empty when cancelled). Absent hides the Attach
   *  button — a host with no file client cannot offer it. */
  onPickFiles?: () => Promise<string[]>;
  /** Take back one of the extra folders the session was allowed. Absent
   *  leaves the status bar's root chips read-only. */
  onRemoveRoot?: (path: string) => void;
  /** Text to drop into the composer from OUTSIDE the session.
   *
   *  Two kinds of sender, one seam. A separate app sends the standalone
   *  Agent window an `agent_draft` app message and wash-ai passes it
   *  here; a host that EMBEDS this component (wash-edit's agent tab) has
   *  no app on the other end of a message and pushes into the signal
   *  directly:
   *
   *      const [draft, setDraft] = createSignal<InsertedDraft>();
   *      let n = 0;
   *      const sendToAgent = (text: string) => setDraft({ text, seq: ++n });
   *      <AgentSession insertDraft={draft} … />
   *
   *  `seq` is what makes sending the same selection twice insert it
   *  twice; the text alone could not say that. Inserted at the caret when
   *  the composer has one, else at the end — and it QUEUES for free while
   *  the session is still starting, because the composer is a controlled
   *  input on a signal this writes to, so text set before `onSend` exists
   *  is simply there when the box goes live. */
  insertDraft?: () => InsertedDraft | undefined;
  /** Answer a pending question. `rule` is set when the user chose "always". */
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string, scope?: 'workspace') => void;
  /** Click on a tool row — the host decides what that opens. */
  onOpenTool?: (e: AgentEvent) => void;
  /** Abort the running turn. Absent means the session cannot be stopped. */
  onCancel?: () => void;
  /** Switch the agent's approval preset. Absent hides the control. */
  onSetMode?: (modeID: string) => void;
  /** Change one of the agent's own settings. Absent hides the controls. */
  onSetConfig?: (id: string, value: string) => void;
  /** Rendered above the transcript; the launcher uses it for its form. */
  header?: JSX.Element;
  placeholder?: string;
  /** Omit the composer for transcript previews with a separate inbox input. */
  hideComposer?: boolean;
}

// fmtTokens renders a context count the way a status bar wants it: two
// significant figures and a k, because the exact token count is never the
// question — "how close am I to the wall" is.
function fmtTokens(n: number): string {
  if (n < 1000) return String(n);
  const k = n / 1000;
  return (k < 10 ? k.toFixed(1) : Math.round(k).toString()) + 'k';
}

// State dot colours, the same vocabulary the roster established: blue
// working, amber needs-input, green done, muted otherwise.
function dotColor(status?: string): string {
  switch (status) {
    case 'in_progress':
      return tokens.accentBlue;
    case 'completed':
      return tokens.accentGreen;
    case 'failed':
      return tokens.accentRed;
    case 'pending':
      return tokens.accentAmber;
  }
  return tokens.fgDim;
}

/** Indeterminate "the agent is thinking" ring. */
const Spinner: Component<{ size?: number; color?: string }> = (p) => (
  <span
    data-wash-spin
    aria-label="working"
    role="status"
    style={{
      width: `${p.size ?? 12}px`,
      height: `${p.size ?? 12}px`,
      flex: 'none',
      display: 'inline-block',
      'border-radius': '50%',
      border: `2px solid ${tokens.borderMenu}`,
      'border-top-color': p.color ?? tokens.accentBlue,
      animation: tokens.animSpin,
    }}
  />
);

const Dot: Component<{ color: string }> = (p) => (
  <span
    aria-hidden="true"
    style={{
      width: '7px',
      height: '7px',
      'border-radius': '50%',
      background: p.color,
      flex: 'none',
      display: 'inline-block',
    }}
  />
);

/** basename: a transcript row has no width for an absolute path, and the
 *  directory is the session cwd nearly every time. The full path stays in
 *  the title attribute. */
function baseName(path: string): string {
  const i = path.lastIndexOf('/');
  return i < 0 ? path : path.slice(i + 1);
}

/** One tool call: kind, argument, state — and, when the agent reported one,
 *  the diff it made.
 *
 *  The diff is the point. An `edit` row that says only "Edit main.go" is a
 *  claim; the unified diff underneath is the evidence, and reading it is
 *  the whole reason to watch an agent rather than run one. agentd renders
 *  it (apps/agentd/be/diff.go) so the wire carries hunks rather than two
 *  whole copies of the file, and it arrives here as text to colour. */
const ToolRow: Component<{ e: AgentEvent; onOpen?: (e: AgentEvent) => void }> = (p) => {
  const clickable = () => !!p.onOpen;
  // Expanded by default: a diff nobody opened is a diff nobody read, and
  // the box is height-capped so even a big one costs a scroll, not a
  // transcript.
  const [open, setOpen] = createSignal(true);
  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '2px' }}>
    <div
      data-testid="agent-tool-row"
      data-path={p.e.path || undefined}
      data-wash-hit={clickable() ? '' : undefined}
      role={clickable() ? 'button' : undefined}
      tabindex={clickable() ? 0 : undefined}
      title={p.e.path || undefined}
      onClick={() => p.onOpen?.(p.e)}
      onKeyDown={(ev) => {
        if (clickable() && (ev.key === 'Enter' || ev.key === ' ')) {
          ev.preventDefault();
          p.onOpen?.(p.e);
        }
      }}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: `${tokens.spaceMd}px`,
        background: tokens.bgInset,
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': tokens.radiusMd,
        padding: `${tokens.spaceXs}px ${tokens.spaceMd}px`,
        font: tokens.type.monoMd,
        color: tokens.fgMuted,
        cursor: clickable() ? 'pointer' : 'default',
      }}
    >
      <span
        style={{
          flex: 'none',
          width: '54px',
          color: tokens.fgDim,
          font: tokens.type.monoSm,
          'letter-spacing': '0.08em',
          'text-transform': 'uppercase',
          overflow: 'hidden',
          'text-overflow': 'ellipsis',
        }}
      >
        {p.e.tool_kind || 'tool'}
      </span>
      <span
        style={{
          color: tokens.fg,
          'white-space': 'nowrap',
          overflow: 'hidden',
          'text-overflow': 'ellipsis',
          'min-width': 0,
          flex: 1,
        }}
      >
        {p.e.title || p.e.text || p.e.tool_id || ''}
      </span>
      {/* The file, named separately from the title: an adapter's title is
          prose ("Edit file"), and the path is the part you click. */}
      <Show when={p.e.path && baseName(p.e.path!) !== (p.e.title ?? '')}>
        <span
          data-testid="agent-tool-path"
          style={{
            flex: 'none',
            font: tokens.type.monoSm,
            color: clickable() ? tokens.accentBlue : tokens.fgDim,
            'max-width': '24ch',
            overflow: 'hidden',
            'text-overflow': 'ellipsis',
            'white-space': 'nowrap',
          }}
        >
          {baseName(p.e.path!)}
        </span>
      </Show>
      <Show when={p.e.diff}>
        <button
          type="button"
          data-testid="agent-tool-diff-toggle"
          data-wash-hit
          title={open() ? 'Hide the diff' : 'Show the diff'}
          onClick={(ev) => {
            // The row itself opens the file; the caret only folds.
            ev.stopPropagation();
            setOpen(!open());
          }}
          style={{
            flex: 'none',
            background: 'transparent',
            border: 'none',
            color: tokens.fgMuted,
            font: tokens.type.monoSm,
            cursor: 'pointer',
            padding: '0 2px',
          }}
        >
          {open() ? '▾' : '▸'} diff
        </button>
      </Show>
      <Dot color={dotColor(p.e.status)} />
    </div>

    <Show when={p.e.diff && open()}>
      <pre
        data-testid="agent-tool-diff"
        class={WASH_SCROLL_CLASS}
        style={{
          margin: 0,
          background: tokens.bgInset,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': tokens.radiusMd,
          padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
          font: tokens.type.monoSm,
          color: tokens.fgMuted,
          'max-height': 'min(320px, 45vh)',
          overflow: 'auto',
          'white-space': 'pre',
          // The transcript is a flex column; without this a diff in a
          // short pane is squeezed to nothing (the terminal row learned
          // the same lesson).
          'flex-shrink': 0,
        }}
      >
        <HighlightedCode code={p.e.diff!} lang="diff" />
      </pre>
    </Show>
    </div>
  );
};

// Keyboard answers use Alt rather than a bare letter: the composer is
// focused most of the time, and a bare A must stay a letter you can type.
// Alt+A / Alt+D collide with nothing in the terminal-adjacent muscle
// memory this desktop already trains.
// How many command suggestions fit before the list stops being a help.
const MAX_SLASH = 8;

const ALLOW_HINT = '⌥A';
const DENY_HINT = '⌥D';
const STOP_HINT = 'Esc';

// How many of your own prompts ↑ walks back through. Fifty is a session's
// worth: far enough that the thing you want is in there, short enough that
// holding ↑ is not a way to lose your place.
const MAX_PROMPT_HISTORY = 50;

const hintStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  opacity: 0.7,
  'margin-left': `${tokens.spaceXs}px`,
};

/** Wash's own verdict on a tool call, as one coloured line: a green tick for
 *  an approval nobody was asked for, red for a call that did not run. It used
 *  to be ordinary transcript prose ("Auto-approved (yolo): Bash …"), which
 *  read like the agent talking and hid the one thing worth seeing: that the
 *  guard was off, or that something was refused. */
export const DecisionRow: Component<{ e: AgentEvent }> = (p) => {
  const allowed = () => p.e.status === 'allow';
  const label = () => allowed()
    ? (p.e.reason?.startsWith('allowed once') ? 'Allowed once' : 'Auto-approved')
    : 'Not approved';
  return (
    <div
      data-testid="agent-decision"
      data-status={p.e.status}
      title={p.e.text}
      style={{
        display: 'flex', 'align-items': 'baseline', gap: `${tokens.spaceSm}px`, 'min-width': 0,
        font: tokens.type.textSm, padding: `2px ${tokens.spaceSm}px`,
        'border-left': `3px solid ${allowed() ? tokens.fgSuccess : tokens.borderDanger}`,
        background: allowed() ? 'transparent' : tokens.bgDenied,
        'border-radius': tokens.radiusSm,
      }}
    >
      <span style={{ color: allowed() ? tokens.fgSuccess : tokens.fgDanger, 'font-weight': 600, 'white-space': 'nowrap' }}>
        {allowed() ? '✓' : '✕'} {label()}
      </span>
      <span style={{ font: tokens.type.monoSm, 'font-weight': 600, color: tokens.fg, 'white-space': 'nowrap' }}>{p.e.title}</span>
      <Show when={p.e.detail}>
        <span style={{ font: tokens.type.monoSm, color: tokens.fgMuted, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', 'min-width': 0 }}>{p.e.detail}</span>
      </Show>
      <Show when={p.e.reason}>
        <span style={{ color: tokens.fgDim, 'white-space': 'nowrap', 'margin-left': 'auto' }}>{p.e.reason}</span>
      </Show>
    </div>
  );
};

/** A workspace inbox message: "<sender> · <type>", a blank line, the body.
 *  A teammate's body is agent-authored Markdown, the same as the agent's own
 *  prose, and was showing its ** and lists raw. A human's stays literal, for
 *  the reason the user row gives: Markdown would eat what they meant to type.
 *  The origin line becomes a small header instead of the body's first line. */
export const Collaboration: Component<{ text: string }> = (p) => {
  const split = () => {
    const i = p.text.indexOf('\n\n');
    return i < 0 ? { origin: '', body: p.text } : { origin: p.text.slice(0, i), body: p.text.slice(i + 2) };
  };
  const fromHuman = () => split().origin.startsWith('human ·');
  return (
    <div data-testid="agent-collaboration" style={{ 'white-space': 'normal' }}>
      <Show when={split().origin}>
        <div style={{ font: tokens.type.monoSm, color: tokens.fgMuted, 'margin-bottom': `${tokens.spaceXs}px` }}>
          {split().origin}
        </div>
      </Show>
      <Show when={!fromHuman()} fallback={<div style={{ 'white-space': 'pre-wrap' }}>{split().body}</div>}>
        <Markdown text={split().body} />
      </Show>
    </div>
  );
};

/** A pending question, rendered inline as a second view of agentd's queue. */
const AskRow: Component<{
  ask: AgentAsk;
  /** only the row a keystroke would answer advertises the shortcut */
  keyed?: boolean;
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string, scope?: 'workspace') => void;
}> = (p) => (
  <div
    style={{
      display: 'flex',
      'flex-wrap': 'wrap',
      'align-items': 'center',
      gap: `${tokens.spaceSm}px`,
      background: tokens.bgDenied,
      border: `1px solid ${tokens.borderDenied}`,
      'border-radius': tokens.radiusMd,
      padding: `${tokens.spaceMd}px`,
    }}
  >
    <div style={{ flex: '1 1 100%', font: tokens.type.monoMd, color: tokens.fg, 'word-break': 'break-all' }}>
      {p.ask.subject || p.ask.tool}
    </div>
    <button
      data-wash-hit
      type="button"
      onClick={() => p.onAnswer?.(p.ask.id, 'allow')}
      style={askBtn(tokens.bgSuccess, tokens.fgSuccess)}
    >
      Allow
      <Show when={p.keyed}>
        <span style={hintStyle}>{ALLOW_HINT}</span>
      </Show>
    </button>
    {/* Two "always" answers where there is a workspace, because they mean
        different things: the per-directory rule covers the one worktree
        this member happens to work in, while the workspace rule covers
        every member of the team — including ones not launched yet. With a
        fleet the first is the one that makes you answer again and again. */}
    <Show when={p.ask.suggested_rule && p.ask.workspace_name}>
      <button
        data-wash-hit
        type="button"
        data-testid="agent-ask-always-workspace"
        onClick={() => p.onAnswer?.(p.ask.id, 'allow', p.ask.suggested_rule, 'workspace')}
        title={`Allows ${p.ask.suggested_rule} for every member of ${p.ask.workspace_name}, in whatever folder it works in. Ends with the workspace.`}
        style={askBtn(tokens.bgInfo, tokens.fgInfo)}
      >
        Always allow <span style={{ font: tokens.type.monoSm }}>{p.ask.suggested_rule}</span>
        <span style={{ font: tokens.type.monoSm, opacity: 0.7 }}>for {p.ask.workspace_name}</span>
      </button>
    </Show>
    <Show when={p.ask.suggested_rule}>
      <button
        data-wash-hit
        type="button"
        onClick={() => p.onAnswer?.(p.ask.id, 'allow', p.ask.suggested_rule)}
        title={p.ask.rule_cwd ? `Only for ${p.ask.rule_cwd}` : undefined}
        style={askBtn(tokens.bgInfo, tokens.fgInfo)}
      >
        Always allow <span style={{ font: tokens.type.monoSm }}>{p.ask.suggested_rule}</span>
        <Show when={p.ask.rule_cwd}>
          <span style={{ font: tokens.type.monoSm, opacity: 0.7 }}>in {p.ask.rule_cwd}</span>
        </Show>
      </button>
    </Show>
    <button
      data-wash-hit
      type="button"
      onClick={() => p.onAnswer?.(p.ask.id, 'deny')}
      style={askBtn(tokens.bgDanger, tokens.fgDanger)}
    >
      Deny
      <Show when={p.keyed}>
        <span style={hintStyle}>{DENY_HINT}</span>
      </Show>
    </button>
  </div>
);

function askBtn(bg: string, fg: string): JSX.CSSProperties {
  return {
    display: 'inline-flex',
    'align-items': 'center',
    gap: `${tokens.spaceXs}px`,
    font: tokens.type.textSm,
    padding: `${tokens.spaceXs}px ${tokens.spaceMd}px`,
    'border-radius': tokens.radiusSm,
    border: `1px solid ${bg}`,
    background: bg,
    color: fg,
    cursor: 'pointer',
    'white-space': 'nowrap',
  };
}

export const AgentSession: Component<AgentSessionProps> = (props) => {
  const [draft, setDraft] = createSignal('');
  let scroller: HTMLDivElement | undefined;
  let input: HTMLTextAreaElement | undefined;

  // Follow the tail only when already at it: a user reading back through a
  // long turn must not be yanked to the bottom by the agent still talking.
  const [pinned, setPinned] = createSignal(true);
  const atBottom = () => {
    if (!scroller) return true;
    return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  };
  createEffect(() => {
    props.events();
    props.asks?.();
    if (pinned() && scroller) queueMicrotask(() => scroller!.scrollTo({ top: scroller!.scrollHeight }));
  });
  onMount(() => input?.focus());

  // Answer the oldest pending question from the keyboard. agentd sorts
  // asks oldest-first, so "the one the shortcut answers" is the one at
  // the top — and it is the only row that advertises the keys, because a
  // shortcut that silently picks among several is worse than none.
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.altKey || e.ctrlKey || e.metaKey) return;
      const pending = props.asks?.() ?? [];
      if (pending.length === 0) return;
      const k = e.key.toLowerCase();
      if (k !== 'a' && k !== 'd') return;
      e.preventDefault();
      props.onAnswer?.(pending[0].id, k === 'a' ? 'allow' : 'deny');
    };
    window.addEventListener('keydown', onKey);
    onCleanup(() => window.removeEventListener('keydown', onKey));
  });

  // Commands offered for what is currently typed. Empty unless the draft
  // starts with "/", so the composer is bare the rest of the time.
  const slashMatches = () => {
    const d = draft();
    if (!d.startsWith('/')) return [];
    const typed = d.slice(1).split(/\s/)[0].toLowerCase();
    // A completed command followed by a space is no longer a search.
    if (d.slice(1).includes(' ')) return [];
    const all = props.status?.().commands ?? [];
    if (typed === '') return all;
    return all.filter((c) => c.name.toLowerCase().startsWith(typed));
  };

  // ↑ history. The ring IS the transcript: every prompt you sent is
  // already a user row, so recall is per-session for free, survives a
  // resume, and has nothing to persist. A send agentd has not echoed back
  // yet is held in `unecho` so ↑ works the instant after Enter, and drops
  // out again as soon as the row arrives.
  const [unecho, setUnecho] = createSignal<string[]>([]);
  const prompts = () => {
    const sent: string[] = [];
    for (const e of props.events()) {
      if (e.kind === 'user' && (e.text ?? '') !== '') sent.push(e.text!);
    }
    for (const t of unecho()) {
      if (!sent.includes(t)) sent.push(t);
    }
    return sent.length > MAX_PROMPT_HISTORY ? sent.slice(-MAX_PROMPT_HISTORY) : sent;
  };
  // -1 is "not browsing": ↑ only ENTERS history from an empty composer, so
  // it stays an arrow key in a draft you are editing.
  const [histAt, setHistAt] = createSignal(-1);
  const recallPrev = (): boolean => {
    const h = prompts();
    if (h.length === 0) return false;
    const at = histAt();
    if (at < 0) {
      if (draft() !== '') return false;
      setHistAt(h.length - 1);
      setDraft(h[h.length - 1]);
      return true;
    }
    // At the oldest: stay there rather than wrapping round to the newest,
    // which loses the place you were walking back to.
    if (at > 0) {
      setHistAt(at - 1);
      setDraft(h[at - 1]);
    }
    return true;
  };
  const recallNext = (): boolean => {
    const at = histAt();
    if (at < 0) return false;
    const h = prompts();
    if (at >= h.length - 1) {
      setHistAt(-1);
      setDraft('');
      return true;
    }
    setHistAt(at + 1);
    setDraft(h[at + 1]);
    return true;
  };

  // A one-line note under the box for anything the composer would not
  // take: a binary drop, an oversized image, a file that failed to read.
  const [dropNote, setDropNote] = createSignal('');

  // What the composer is holding, alongside the text. Cleared on send:
  // an attachment belongs to the message it was collected for, and a
  // screenshot silently riding along on the NEXT prompt is a surprise.
  const [attached, setAttached] = createSignal<PromptBlock[]>([]);
  const dropAttachment = (i: number) => setAttached((a) => a.filter((_, n) => n !== i));

  const send = () => {
    const text = draft().trim();
    const blocks = attached();
    if ((!text && blocks.length === 0) || !props.onSend) return;
    props.onSend(text, blocks.length > 0 ? blocks : undefined);
    if (text) setUnecho((u) => [...u, text].slice(-MAX_PROMPT_HISTORY));
    setHistAt(-1);
    setDraft('');
    setAttached([]);
    setPinned(true);
  };

  // Paste an image. A text paste is left entirely alone — the default is
  // correct there, and intercepting it would break every other paste in
  // the box. Refused BEFORE the read: a 40 MB paste should not become
  // 53 MB of base64 on its way to being rejected.
  const attachImages = async (files: readonly File[]) => {
    const skipped: string[] = [];
    for (const f of files) {
      if (f.size > MAX_IMAGE_BYTES) {
        setDropNote(imageTooBig(f));
        continue;
      }
      try {
        const data = await readImageData(f);
        if (!data) throw new Error('empty');
        setAttached((a) => [...a, { type: 'image', mime: f.type, data, name: f.name || 'pasted image' }]);
      } catch {
        skipped.push(f.name || 'image');
      }
    }
    if (skipped.length > 0) setDropNote(describeSkipped(skipped));
  };

  const onPaste = (e: ClipboardEvent) => {
    const imgs = imageFilesFrom(e.clipboardData);
    if (imgs.length === 0) return;
    e.preventDefault();
    void attachImages(imgs);
  };

  // Text handed to this composer by another app. Inserted at the caret,
  // never sent: what someone does with a pasted-in selection — add a
  // question above it, trim it, think better of it — is the whole reason
  // it goes to the composer rather than straight to the agent.
  let lastDraftSeq = -1;
  createEffect(() => {
    const d = props.insertDraft?.();
    if (!d || d.seq === lastDraftSeq) return;
    lastDraftSeq = d.seq;
    if (!d.text) return;
    const cur = draft();
    const start = input?.selectionStart ?? cur.length;
    const end = input?.selectionEnd ?? start;
    const r = insertAt(cur, start, end, d.text);
    setDraft(r.text);
    setHistAt(-1);
    setPinned(true);
    queueMicrotask(() => {
      input?.focus();
      input?.setSelectionRange(r.caret, r.caret);
    });
  });

  const pickFiles = async () => {
    if (!props.onPickFiles) return;
    const paths = await props.onPickFiles();
    if (paths.length === 0) return;
    setAttached((a) => [
      ...a,
      ...paths.map((p): PromptBlock => ({ type: 'file', path: p, name: baseName(p) })),
    ]);
    input?.focus();
  };

  const st = () => props.status?.() ?? {};

  // Stop is offered whenever there is a turn to stop — INCLUDING while a
  // question is pending, which is exactly when a runaway turn is easiest
  // to notice and, until now, the one moment the button hid itself. Esc
  // is the same verb from the keyboard. agentd's agent_cancel already
  // cancels the asks along with the turn (acp.go, cancelAsksFor), so one
  // press ends both; what was missing was any way to reach it.
  const pendingAsks = () => (props.asks?.() ?? []).length;
  const stoppable = () => !!props.onCancel && (st().state === 'working' || pendingAsks() > 0);
  const cancelTurn = (): boolean => {
    if (!stoppable()) return false;
    props.onCancel!();
    return true;
  };

  // Drops onto the composer (agent-compose-drop.ts): a wash drag becomes
  // @path references at the caret; an OS text file is attached inline as
  // a fenced block; anything else is named in a note under the box. The
  // placeholder has promised this since the composer existed.
  const [dropping, setDropping] = createSignal(false);
  const onDragOver = (e: DragEvent) => {
    if (!acceptsDrop(e.dataTransfer)) return;
    e.preventDefault();
    e.dataTransfer!.dropEffect = 'copy';
    setDropping(true);
  };
  const onDragLeave = () => setDropping(false);
  const onDrop = async (e: DragEvent) => {
    setDropping(false);
    const dt = e.dataTransfer;
    if (!acceptsDrop(dt)) return;
    e.preventDefault();
    e.stopPropagation();
    let insert = '';
    const skipped: string[] = [];
    const paths = washPathsFrom(dt);
    if (paths.length > 0) {
      insert = pathRefs(paths);
    } else {
      const parts: string[] = [];
      // An image dropped from the OS becomes an attachment, not a
      // "not attached" note: it is exactly the thing the agent can use.
      const imgs = imageFilesFrom(dt);
      if (imgs.length > 0) void attachImages(imgs);
      for (const f of Array.from(dt!.files ?? [])) {
        if ((f.type || '').toLowerCase().startsWith('image/')) continue;
        if (!isTextLike(f)) {
          skipped.push(f.name);
          continue;
        }
        try {
          parts.push(fencedAttachment(f.name, await readTextFile(f)));
        } catch {
          skipped.push(f.name);
        }
      }
      insert = parts.join('\n\n');
    }
    setDropNote(describeSkipped(skipped));
    if (!insert) return;
    const cur = draft();
    const start = input?.selectionStart ?? cur.length;
    const end = input?.selectionEnd ?? start;
    const r = insertAt(cur, start, end, insert);
    setDraft(r.text);
    // Land the caret after what was inserted, once Solid has written the
    // new value into the textarea.
    queueMicrotask(() => {
      input?.focus();
      input?.setSelectionRange(r.caret, r.caret);
    });
  };

  return (
    <div
      // Esc anywhere in the session — the composer, an ask row's buttons,
      // the transcript — is Stop. Handled here rather than on the textarea
      // so it does not depend on where focus happens to be when a turn
      // goes wrong, and swallowed only when it actually stopped something,
      // so Esc still closes whatever is above this when there is no turn.
      onKeyDown={(e) => {
        if (e.key !== 'Escape' || e.defaultPrevented) return;
        if (cancelTurn()) {
          e.preventDefault();
          e.stopPropagation();
        }
      }}
      style={{
        display: 'flex',
        'flex-direction': 'column',
        height: '100%',
        'min-height': 0,
        background: tokens.bgWindow,
        color: tokens.fg,
      }}
    >
      <Show when={props.header}>{props.header}</Show>

      <div
        ref={scroller}
        data-testid="agent-transcript"
        onScroll={() => setPinned(atBottom())}
        style={{
          flex: 1,
          'min-height': 0,
          'overflow-y': 'auto',
          // The transcript keeps its own scroll: a wheel that reaches the
          // top or the bottom stops here instead of chaining out to
          // whatever pane or window is behind it and taking the layout
          // beside it along for the ride.
          'overscroll-behavior': 'contain',
          padding: `${tokens.spaceLg}px`,
          display: 'flex',
          'flex-direction': 'column',
          gap: `${tokens.spaceMd}px`,
        }}
      >
        <For each={props.events()}>
          {(e) => (
            <>
            <Show when={e.kind === 'image'}>
              {/* A data: URI, so the image never leaves the machine and
                  no request is made for it. Bounded by agentd before it
                  ever reaches here. */}
              <img
                src={`data:${e.mime || 'image/png'};base64,${e.text ?? ''}`}
                alt="image from the agent"
                style={{
                  'max-width': '100%',
                  height: 'auto',
                  'border-radius': tokens.radiusMd,
                  border: `1px solid ${tokens.borderMenu}`,
                }}
              />
            </Show>

            <Show when={e.kind === 'terminal'}>
              {/* A command the agent handed to wash, rendered LIVE on the
                  channel its pty writes to. Not a transcript of what
                  happened — the actual terminal, mid-run: it scrolls, it
                  takes Ctrl+C, and it is the same component wash-term
                  uses. Watching is the whole point; a captured blob after
                  the fact is what this capability replaced. */}
              <div
                data-testid="agent-terminal"
                data-channel={e.channel}
                style={{
                  border: `1px solid ${tokens.borderMenu}`,
                  'border-radius': tokens.radiusMd,
                  overflow: 'hidden',
                  // The transcript is a flex column, so without this the box
                  // is squeezed to nothing in a short container — which is
                  // exactly what wash-edit's pane is. It measured 2px tall
                  // there while holding a full directory listing: present,
                  // correct, and invisible.
                  'flex-shrink': 0,
                }}
              >
                <div
                  style={{
                    font: tokens.type.monoSm,
                    color: tokens.fgMuted,
                    padding: '3px 8px',
                    background: tokens.bgMenu,
                    'white-space': 'nowrap',
                    overflow: 'hidden',
                    'text-overflow': 'ellipsis',
                  }}
                >
                  $ {e.title ?? ''}
                  <Show when={e.status && e.status !== 'running'}>
                    <span style={{ opacity: '0.8' }}> — {e.status}</span>
                  </Show>
                </div>
                <Show
                  when={e.channel}
                  fallback={
                    /* Finished: the pty is gone and so is its channel, so
                       show what it PRODUCED. A command that ends quickly —
                       or one the agent releases straight away — would
                       otherwise leave an empty frame where its output
                       should be. */
                    <pre
                      data-testid="agent-terminal-output"
                      class={WASH_SCROLL_CLASS}
                      style={{
                        margin: 0,
                        padding: '6px 8px',
                        'max-height': 'min(220px, 30vh)',
                        overflow: 'auto',
                        font: tokens.type.monoSm,
                        'white-space': 'pre-wrap',
                        'overflow-wrap': 'anywhere',
                      }}
                    >{e.text ?? ''}</pre>
                  }
                >
                  <div style={{ height: 'min(220px, 30vh)', 'min-height': '96px' }}>
                    <Terminal channelId={e.channel} />
                  </div>
                </Show>
              </div>
            </Show>

            <Show
              when={e.kind !== 'tool' && e.kind !== 'image' && e.kind !== 'terminal' && e.kind !== 'decision'}
              fallback={
                <Show when={e.kind === 'tool'} fallback={<Show when={e.kind === 'decision'}><DecisionRow e={e} /></Show>}>
                  <ToolRow e={e} onOpen={props.onOpenTool} />
                </Show>
              }
            >
              <div
                data-testid={e.kind === 'user' ? 'agent-human-message' : undefined}
                style={{
                  font: tokens.type.textMd,
                  color: e.kind === 'thought' ? tokens.fgMuted : tokens.fg,
                  'font-style': e.kind === 'thought' ? 'italic' : 'normal',
                  'white-space': 'pre-wrap',
                  'overflow-wrap': 'anywhere',
                  // What you typed gets a rule down its left edge. Without
                  // it a transcript is a wall of prose with no way to see
                  // where your turn ended and the agent's began.
                  ...(e.kind === 'user'
                    ? {
                        'border-left': `3px solid ${tokens.accentBlue}`,
                        padding: `${tokens.spaceMd}px ${tokens.spaceLg}px`,
                        'border-radius': tokens.radiusMd,
                        background: tokens.bgInfo,
                        color: tokens.fgInfo,
                        'font-family': tokens.fontSans,
                        'font-weight': 600,
                      }
                    : {}),
                }}
              >
                {/* Agent-authored prose is Markdown; what you typed is literal.
                    Rendering your own prompt as Markdown would eat the
                    asterisks and backticks you meant to send. */}
                <Show when={e.kind === 'message' || e.kind === 'thought'} fallback={
                  <Show when={e.kind === 'collaboration'} fallback={<>{e.text}</>}>
                    <Collaboration text={e.text ?? ''} />
                  </Show>
                }>
                  <Markdown text={e.text ?? ''} />
                </Show>
              </div>
            </Show>
            </>
          )}
        </For>

        <For each={props.asks?.() ?? []}>
          {(a, i) => <AskRow ask={a} keyed={i() === 0} onAnswer={props.onAnswer} />}
        </For>

        {/* The tail spinner is the answer to "did it hear me?" — a turn can
            think for many seconds before its first chunk arrives, and an
            empty transcript is indistinguishable from a broken one. Hidden
            while a question is pending, because then the thing waiting is
            you, not the agent. */}
        <Show when={(st().state === 'working' && pendingAsks() === 0) || stoppable()}>
          <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceMd}px`, color: tokens.fgDim, font: tokens.type.textSm }}>
            <Show when={st().state === 'working' && pendingAsks() === 0}>
              <Spinner />
              <span>working…</span>
            </Show>
            <Show when={stoppable()}>
              <button
                data-wash-hit
                type="button"
                data-testid="agent-stop"
                title="End this turn — and the question it is waiting on"
                onClick={() => cancelTurn()}
                style={askBtn(tokens.bgNeutral, tokens.fgMuted)}
              >
                Stop
                <span style={hintStyle}>{STOP_HINT}</span>
              </button>
            </Show>
          </div>
        </Show>
      </div>

      <Show when={!props.hideComposer}>
      <div
        data-testid="agent-composer-drop"
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
        style={{
          flex: 'none',
          'border-top': `1px solid ${dropping() ? tokens.borderFocus : tokens.borderMenu}`,
          padding: `${tokens.spaceMd}px`,
        }}
      >
        {/* Slash commands appear only while you are typing one. An agent
            can offer dozens, and a permanent wall of chips above the
            composer costs more attention than it saves — the list is an
            autocomplete, not a toolbar. */}
        <Show when={slashMatches().length > 0}>
          <div
            data-testid="agent-commands"
            style={{
              display: 'flex',
              gap: `${tokens.spaceSm}px`,
              'flex-wrap': 'wrap',
              'margin-bottom': `${tokens.spaceXs}px`,
              'align-items': 'baseline',
            }}
          >
            <For each={slashMatches().slice(0, MAX_SLASH)}>
              {(cmd) => (
                <button
                  data-wash-hit
                  type="button"
                  title={cmd.description}
                  onClick={() => {
                    setDraft('/' + cmd.name + ' ');
                    input?.focus();
                  }}
                  style={{
                    font: tokens.type.monoSm,
                    padding: `2px ${tokens.spaceSm}px`,
                    'border-radius': tokens.radiusSm,
                    border: `1px solid ${tokens.borderMenu}`,
                    background: tokens.bgInset,
                    color: tokens.fgMuted,
                    cursor: 'pointer',
                  }}
                >
                  /{cmd.name}
                </button>
              )}
            </For>
            <Show when={slashMatches().length > MAX_SLASH}>
              <span style={{ font: tokens.type.monoSm, color: tokens.fgDim }}>
                +{slashMatches().length - MAX_SLASH} more
              </span>
            </Show>
          </div>
        </Show>

        {/* What is going out with the next message. Shown as chips rather
            than folded into the text: an image has no textual form, and a
            file attached by reference is a different thing from its path
            typed into the prompt. */}
        <Show when={attached().length > 0}>
          <div
            data-testid="agent-attachments"
            style={{
              display: 'flex',
              'flex-wrap': 'wrap',
              gap: `${tokens.spaceSm}px`,
              'margin-bottom': `${tokens.spaceXs}px`,
            }}
          >
            <For each={attached()}>
              {(b, i) => (
                <span
                  data-testid="agent-attachment"
                  data-kind={b.type}
                  title={b.path || b.name}
                  style={{
                    display: 'inline-flex',
                    'align-items': 'center',
                    gap: `${tokens.spaceXs}px`,
                    padding: `1px ${tokens.spaceSm}px`,
                    'border-radius': tokens.radiusSm,
                    border: `1px solid ${tokens.borderMenu}`,
                    background: tokens.bgInset,
                    font: tokens.type.monoSm,
                    color: tokens.fgMuted,
                    'max-width': '28ch',
                  }}
                >
                  <Show when={b.type === 'image' && b.data}>
                    {/* A thumbnail, so "which screenshot is that" is not a
                        question. A data: URI — the bytes are already here
                        and no request is made for them. */}
                    <img
                      src={`data:${b.mime || 'image/png'};base64,${b.data}`}
                      alt=""
                      style={{ width: '18px', height: '18px', 'object-fit': 'cover', 'border-radius': '2px', flex: 'none' }}
                    />
                  </Show>
                  <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                    {b.name}
                  </span>
                  <button
                    type="button"
                    data-wash-hit
                    data-testid="agent-attachment-remove"
                    aria-label={`Remove ${b.name ?? 'attachment'}`}
                    onClick={() => dropAttachment(i())}
                    style={{
                      flex: 'none',
                      background: 'transparent',
                      border: 'none',
                      color: tokens.fgDim,
                      font: tokens.type.monoSm,
                      cursor: 'pointer',
                      padding: '0 2px',
                    }}
                  >
                    ×
                  </button>
                </span>
              )}
            </For>
          </div>
        </Show>

        {/* The composer stays open mid-turn on purpose. A message typed
            while the agent is replying is queued by agentd and sent when
            the turn ends — the way a messenger behaves — rather than the
            box greying out for the length of a reply. The placeholder
            says so while it applies, and the status line counts what is
            waiting. */}
        <textarea
          ref={input}
          data-testid="agent-composer"
          rows={2}
          value={draft()}
          disabled={!props.onSend}
          placeholder={
            props.placeholder ??
            (st().state === 'working'
              ? 'Type the next message — it is sent when this turn ends'
              : 'Ask, or drop a file from wash-fm…')
          }
          onPaste={onPaste}
          onInput={(e) => {
            setDraft(e.currentTarget.value);
            // Typing leaves history: the recalled prompt is now a draft
            // you are editing, and ↑ should behave like an arrow key in it.
            setHistAt(-1);
          }}
          onKeyDown={(e) => {
            // Ctrl/Cmd+Enter sends, unconditionally — the muscle memory
            // every other chat composer trains, and the one that still
            // works when a modifier is already held down.
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              send();
              return;
            }
            // Enter sends; Shift+Enter is a newline. A composer that
            // needed a modifier to send would be wrong for a chat and a
            // surprise in every other wash text field.
            if (e.key === 'Enter' && !e.shiftKey && !e.altKey) {
              e.preventDefault();
              send();
              return;
            }
            // ↑ from an empty composer walks back through your own
            // prompts, ↓ forward; ↓ past the newest returns the empty box.
            if (e.key === 'ArrowUp' && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
              if (recallPrev()) e.preventDefault();
              return;
            }
            if (e.key === 'ArrowDown' && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
              if (recallNext()) e.preventDefault();
              return;
            }
          }}
          style={{
            width: '100%',
            resize: 'none',
            background: tokens.bgInset,
            border: `1px solid ${tokens.borderMenu}`,
            'border-radius': tokens.radiusMd,
            padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
            font: tokens.type.textMd,
            'font-weight': 500,
            // fg, not fgInfo: fgInfo is the pair for bgInfo (the sent
            // message below), and on bgInset it is a blue-on-grey that
            // no other wash text field types in.
            color: tokens.fg,
            outline: 'none',
            'box-sizing': 'border-box',
          }}
        />
        <div style={{ display: 'flex', 'align-items': 'baseline', gap: `${tokens.spaceMd}px`, 'margin-top': `${tokens.spaceXs}px` }}>
          {/* Attach is offered only by a host that has a file client to
              open a picker with — the same rule every other callback here
              follows. Paste and drop need no button. */}
          <Show when={props.onPickFiles}>
            <button
              type="button"
              data-wash-hit
              data-testid="agent-attach"
              title="Attach a file from this session's folder"
              onClick={() => void pickFiles()}
              style={{
                flex: 'none',
                font: tokens.type.monoSm,
                padding: `1px ${tokens.spaceSm}px`,
                'border-radius': tokens.radiusSm,
                border: `1px solid ${tokens.borderMenu}`,
                background: 'transparent',
                color: tokens.fgMuted,
                cursor: 'pointer',
              }}
            >
              Attach…
            </button>
          </Show>
          <Show when={dropNote()}>
            <div data-testid="agent-drop-note" style={{ font: tokens.type.textSm, color: tokens.fgMuted }}>
              {dropNote()}
            </div>
          </Show>
        </div>
      </div>

      </Show>

      <div
        data-testid="agent-status-bar"
        style={{
          flex: 'none',
          height: '22px',
          'min-width': 0,
          'white-space': 'nowrap',
          'overflow-x': 'auto',
          'scrollbar-width': 'none',
          display: 'flex',
          'align-items': 'center',
          gap: `${tokens.spaceMd}px`,
          padding: `0 ${tokens.spaceMd}px`,
          background: tokens.bgMenu,
          'border-top': `1px solid ${tokens.borderMenu}`,
          font: tokens.type.monoSm,
          color: tokens.fgMuted,
          'font-variant-numeric': 'tabular-nums',
        }}
      >
        {/* The spinner IS the working state here — motion says "in a
            turn" better than a colour does at this size — but it now
            carries the vocabulary's blue rather than being the one
            surface where working has no colour at all. Every other state
            is a dot, coloured and named by the shared map
            (docs/AGENT_MESSENGER.md M5); this status line used to fall
            everything that was not needs-input or done into one dim grey,
            so a FAILED session looked identical to an idle one. */}
        <Show
          when={st().state === 'working'}
          fallback={
            <Show when={st().state}>
              <span title={`Session: ${agentStateLabel(st().state, st().reason)}`} style={{ display: 'inline-flex', 'align-items': 'center', gap: `${tokens.spaceSm}px`, 'flex-shrink': 0, color: agentStateColor(st().state) }}>
                <Dot color={agentStateColor(st().state)} />
                {agentStateLabel(st().state, st().reason)}
              </span>
            </Show>
          }
        >
          <span title="Session: Working" aria-label="Working" style={{ display: 'inline-flex', 'flex-shrink': 0 }}><Spinner size={9} color={tokens.accentBlue} /></span>
        </Show>
        <Show when={(st().queued ?? 0) > 0}>
          <span
            data-testid="agent-queued"
            title={`${st().queued} queued messages; sent in order after the current turn`}
            aria-label={`${st().queued} queued messages`}
            style={{ color: tokens.accentBlue, 'flex-shrink': 0 }}
          >
            {st().queued}
          </span>
        </Show>
        <Show when={st().agent}>
          <span title={`Provider: ${st().agent}`}>{st().agent}</span>
        </Show>
        <Show when={st().dir}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span title={`Working folder: ${st().cwd || st().dir}`}>{st().dir}</span>
        </Show>
        {/* Folders allowed BEYOND the cwd. Named, not counted: "+2
            folders" tells you that you widened the session and not what
            you widened it to, and the second is the part that matters. */}
        <For each={st().roots ?? []}>
          {(root) => (
            <span
              data-testid="agent-root"
              data-path={root}
              title={`Also allowed: ${root}`}
              style={{
                display: 'inline-flex',
                'align-items': 'center',
                gap: '2px',
                padding: '0 4px',
                'border-radius': tokens.radiusSm,
                border: `1px solid ${tokens.borderMenu}`,
                color: tokens.accentAmber,
                'max-width': '14ch',
                'flex-shrink': 0,
              }}
            >
              <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                {baseName(root)}
              </span>
              <Show when={props.onRemoveRoot}>
                <button
                  type="button"
                  data-wash-hit
                  data-testid="agent-root-remove"
                  aria-label={`Stop allowing ${root}`}
                  title={`Stop allowing ${root}`}
                  onClick={() => props.onRemoveRoot?.(root)}
                  style={{
                    background: 'transparent',
                    border: 'none',
                    color: tokens.fgDim,
                    font: tokens.type.monoSm,
                    cursor: 'pointer',
                    padding: 0,
                  }}
                >
                  ×
                </button>
              </Show>
            </span>
          )}
        </For>
        {/* The agent's own settings — model, reasoning effort, plan mode
            — all arrive in one generic shape, so ONE control renders
            them and whatever an adapter adds later. When the agent
            exposes `mode` here too, this replaces the dedicated mode
            select rather than showing it twice. */}
        <For each={props.status?.().configs ?? []}>
          {(cfg) => (
            <>
              <select
                data-testid={`agent-config-${cfg.id}`}
                value={cfg.current ?? ''}
                title={`${cfg.name}: ${cfg.values?.find((v) => v.value === cfg.current)?.name || cfg.current || "Not reported"}${cfg.description ? ` — ${cfg.description}` : ""}`}
                onChange={(e) => props.onSetConfig?.(cfg.id, e.currentTarget.value)}
                style={{
                  background: 'transparent',
                  border: 'none',
                  color: tokens.fgMuted,
                  font: tokens.type.monoSm,
                  cursor: 'pointer',
                  outline: 'none',
                  'max-width': '16ch',
                }}
              >
                <For each={cfg.values ?? []}>
                  {(v) => (
                    <option value={v.value} title={v.description}>
                      {v.name}
                    </option>
                  )}
                </For>
              </select>
              <span style={{ color: tokens.fgDim }}>·</span>
            </>
          )}
        </For>

        {/* Host-side auto-approval, when it is on. Loud on purpose — red,
            first in the bar, and permanently present — because the whole
            hazard of the feature is forgetting it is on. It sits NEXT TO
            the agent's own mode rather than replacing it: they are
            different decisions by different parties (the agent's policy
            vs wash's willingness to ask). */}
        <Show when={st().yolo}>
          <span
            data-testid="agent-yolo-badge"
            title="wash is approving this session's tool requests without asking"
            style={{
              display: 'inline-flex',
              'align-items': 'center',
              gap: '4px',
              padding: '0 6px',
              'border-radius': '3px',
              background: tokens.accentRed,
              color: '#ffffff',
              font: tokens.type.textSm,
              'flex-shrink': 0,
            }}
          >
            YOLO
          </span>
          <span style={{ color: tokens.fgDim }}>·</span>
        </Show>

        {/* The approval preset lives HERE, on the session it governs,
            rather than in Settings: it is a per-session decision about
            this piece of work, and the agent owns it — wash is only
            asking. Changing it is visible to the agent and reversible
            from either side, unlike a blanket allow wash keeps to
            itself. */}
        <Show when={(st().modes?.length ?? 0) > 0 && props.onSetMode && !(st().configs ?? []).some((c) => c.id === 'mode')}>
          {/* The current mode is marked on the OPTION, not as `value` on the
              select. A `value` set on the select is applied once, when its
              own effect first runs, and whether that lands before or after
              the <For> has inserted the options depends on what else in
              this template happens to be reactive — the moment the
              composer's placeholder started following the turn state, the
              value landed first and the select sat on its first option.
              `selected` per option is order-proof. */}
          <select
            data-testid="agent-mode"
            title={`Approval mode: ${st().modes?.find((m) => m.id === st().mode)?.name ?? st().mode ?? 'Not reported'}${st().modes?.find((m) => m.id === st().mode)?.description ? ` — ${st().modes?.find((m) => m.id === st().mode)?.description}` : ''}`}
            onChange={(e) => props.onSetMode?.(e.currentTarget.value)}
            style={{
              background: 'transparent',
              border: 'none',
              color: tokens.fgMuted,
              font: tokens.type.monoSm,
              cursor: 'pointer',
              outline: 'none',
              'max-width': '14ch',
            }}
          >
            <For each={st().modes}>
              {(m) => (
                <option value={m.id} title={m.description} selected={m.id === st().mode}>
                  {m.name}
                </option>
              )}
            </For>
          </select>
          <span style={{ color: tokens.fgDim }}>·</span>
        </Show>

        <Show when={st().used && st().size}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span title={`Context: ${st().used?.toLocaleString()} / ${st().size?.toLocaleString()} tokens`}>{fmtTokens(st().used!)}/{fmtTokens(st().size!)}</span>
        </Show>
        <Show when={st().branch}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span title={`Git branch: ${st().branch}${st().dirty ? ' — uncommitted changes' : ' — clean'}`}>
            {st().branch}
            {st().dirty ? ' *' : ''}
          </span>
        </Show>
      </div>
    </div>
  );
};
