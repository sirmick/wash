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

import { QuestionPanel, type QuestionPanelProps } from './question-panel';
import type * as agentproto from './agent-protocol.gen';
import { For, Show, createEffect, createMemo, createSignal, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';
import { Tab } from './tab';
import { agentStateColor, agentStateLabel } from './agent-status';
import { HighlightedCode, Markdown } from './markdown';
import { PathLinksProvider, hitTitle, looksLikePath, pathResolver, useHits, usePathLinks } from './path-links';
import type { PathLinks } from './path-links';
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
  modes?: agentproto.Mode[];
  /** the agent's generic settings: model, reasoning effort, plan mode… */
  configs?: agentproto.Config[];
  /** the agent's own slash commands */
  commands?: agentproto.Command[];
  /** folders allowed beyond `dir` (agentd roots.go). Shown in the status
   *  bar because the whole hazard of widening a session is forgetting
   *  that you did. */
  roots?: string[];
  /** wash is auto-approving this session's permission requests (host-side
   *  yolo). Rendered as a standing badge, never as a quiet flag: an agent
   *  nobody is vetting must not look like one that is being watched. */
  yolo?: boolean;
  /** work the session left running in the background (a Bash run in the
   *  background): while it runs the session is waiting on it, not done. */
  background?: string;
  /** prompts agentd is holding until the current turn ends. Messenger
   *  semantics: the composer stays open mid-turn, what you send is queued
   *  in order, and the status line says how many are waiting. */
  queued?: number;
}

export interface AgentSessionProps {
  events: () => agentproto.Event[];
  asks?: () => agentproto.Ask[];
  /** Question sets waiting for the human: shown pinned above the composer. */
  questions?: () => agentproto.PendingQuestion[];
  /** Answer or decline a question set. */
  onQuestionAnswer?: QuestionPanelProps['onAnswer'];
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
  /** Makes the files the transcript names links: tool rows, and paths in
   *  the agent's prose. The host resolves them against the session's
   *  folder and decides where they open (path-links.tsx). Absent, nothing
   *  is a link. */
  links?: PathLinks;
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
const ToolRow: Component<{ e: agentproto.Event }> = (p) => {
  // The file a row names is its path, or a title that is itself one (some
  // adapters report no path). A link only when the host says it is a file
  // it can open.
  const token = () => {
    if (p.e.path) return p.e.path;
    const t = (p.e.title ?? '').trim();
    return looksLikePath(t) ? t : '';
  };
  const links = usePathLinks();
  const hits = useHits(() => (token() ? [{ start: 0, end: token().length, token: token() }] : []));
  const hit = () => hits().get(token());
  const clickable = () => !!hit();
  const openHit = () => { const h = hit(); if (h) links?.open(h); };
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
      title={hit() ? hitTitle(hit()!) : p.e.path || undefined}
      onClick={openHit}
      onKeyDown={(ev) => {
        if (clickable() && (ev.key === 'Enter' || ev.key === ' ')) {
          ev.preventDefault();
          openHit();
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
const clipped: JSX.CSSProperties = { overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', 'min-width': 0 };

/** The agent's thinking, collapsed: open-model adapters stream it, and in
 *  full it buries the reply. The summary counts characters as they arrive,
 *  so a long think is visibly progressing without being read. */
export const ThoughtRow: Component<{ e: agentproto.Event; open: boolean; onToggle: (open: boolean) => void }> = (p) => {
  const chars = () => (p.e.text ?? '').length;
  return (
    <details
      data-testid="agent-thought"
      open={p.open}
      onToggle={(ev) => p.onToggle((ev.currentTarget as HTMLDetailsElement).open)}
      style={{ 'flex-shrink': 0, color: tokens.fgMuted, font: tokens.type.textSm }}
    >
      <summary data-wash-hit style={{ cursor: 'pointer', 'user-select': 'none' }}>
        Thinking <span data-testid="agent-thought-chars" style={{ font: tokens.type.monoSm, color: tokens.fgDim }}>· {chars().toLocaleString()} chars</span>
      </summary>
      <div style={{ 'font-style': 'italic', 'white-space': 'pre-wrap', 'overflow-wrap': 'anywhere', 'margin-top': `${tokens.spaceXs}px`, 'padding-left': `${tokens.spaceMd}px`, 'border-left': `2px solid ${tokens.borderMenu}` }}>
        <Markdown text={p.e.text ?? ''} />
      </div>
    </details>
  );
};

/** A per-call yolo approval, which agentd no longer writes but older saved
 *  transcripts still hold: hidden, for the reason agentd stopped. */
export const routineYolo = (e: agentproto.Event): boolean =>
  e.kind === 'decision' && e.status === 'allow' && e.reason === 'yolo';

export const DecisionRow: Component<{ e: agentproto.Event }> = (p) => {
  const allowed = () => p.e.status === 'allow';
  const label = () => allowed()
    ? (p.e.reason?.startsWith('allowed once') ? 'Allowed once' : 'Auto-approved')
    : 'Not approved';
  return (
    <div
      data-testid="agent-decision"
      data-status={p.e.status}
      style={{
        display: 'flex', 'align-items': 'baseline', gap: `${tokens.spaceSm}px`, 'min-width': 0, overflow: 'hidden',
        // The transcript is a flex column. overflow:hidden (for the
        // ellipses) also lets the row shrink, and it did: to 4px, a green
        // stub with the verdict inside it. The terminal row needs the same.
        'flex-shrink': 0,
        font: tokens.type.textSm, padding: `2px ${tokens.spaceSm}px`,
        'border-left': `3px solid ${allowed() ? tokens.fgSuccess : tokens.borderDanger}`,
        background: allowed() ? 'transparent' : tokens.bgDenied,
        'border-radius': tokens.radiusSm,
      }}
    >
      <span style={{ color: allowed() ? tokens.fgSuccess : tokens.fgDanger, 'font-weight': 600, 'white-space': 'nowrap', 'flex-shrink': 0 }}>
        {allowed() ? '✓' : '✕'} {label()}
      </span>
      {/* Every text part shrinks to an ellipsis, the full text on hover. The
          title and reason could not: a long command or rule name ran past the
          conversation column, under a workspace's sidebar. */}
      <span title={p.e.title} style={{ ...clipped, font: tokens.type.monoSm, 'font-weight': 600, color: tokens.fg }}>{p.e.title}</span>
      <Show when={p.e.detail}>
        <span title={p.e.detail} style={{ ...clipped, font: tokens.type.monoSm, color: tokens.fgMuted, flex: '1 1 auto' }}>{p.e.detail}</span>
      </Show>
      <Show when={p.e.reason}>
        <span title={p.e.reason} style={{ ...clipped, color: tokens.fgDim, 'margin-left': 'auto' }}>{p.e.reason}</span>
      </Show>
    </div>
  );
};

/** One message of an inbox turn as the transcript shows it: its label
 *  ("<sender> · <type>"), the type parsed off the label, and the body. */
export interface InboxSection {
  label: string;
  type: string;
  body: string;
}

/** Splits a collaboration event's text into its messages. agentd writes a
 *  single message as "<label>\n\n<body>" and a batch as "N messages\n\n"
 *  followed by one "#### <label>" heading per message (workspace.go
 *  inboxDisplay); the heading is the only seam a batch has, so the split
 *  keys on it. The type is whatever follows the label's last " · ". */
export function inboxSections(text: string): { origin: string; sections: InboxSection[] } {
  const i = text.indexOf('\n\n');
  const origin = i < 0 ? '' : text.slice(0, i);
  const body = i < 0 ? text : text.slice(i + 2);
  const typeOf = (label: string) => {
    const at = label.lastIndexOf(' · ');
    return at < 0 ? '' : label.slice(at + 3).trim();
  };
  if (/^\d+ messages$/.test(origin) && body.startsWith('#### ')) {
    const sections: InboxSection[] = [];
    for (const part of body.split(/^#### /m)) {
      if (part === '') continue;
      const nl = part.indexOf('\n');
      const label = (nl < 0 ? part : part.slice(0, nl)).trim();
      sections.push({ label, type: typeOf(label), body: (nl < 0 ? '' : part.slice(nl + 1)).trim() });
    }
    return { origin, sections };
  }
  return { origin, sections: [{ label: origin, type: typeOf(origin), body }] };
}

/** Message types that are bookkeeping — a checkpoint, a watchdog note, a
 *  member's aside — and so collapse to their label until opened. Results,
 *  questions and answers are the inbox's point; they stay open. */
export const ROUTINE_INBOX_TYPES: ReadonlySet<string> = new Set(['progress', 'lifecycle', 'note', 'flash']);

/** A workspace inbox turn, one block per message. A teammate's body is
 *  agent-authored Markdown, the same as the agent's own prose, and was
 *  showing its ** and lists raw. A human's stays literal, for the reason
 *  the user row gives: Markdown would eat what they meant to type. Routine
 *  types (ROUTINE_INBOX_TYPES) are closed by default with a one-line
 *  excerpt; `open`/`onToggle` hold that state outside the row, as
 *  thoughts do, because a re-rendered event would otherwise snap it shut. */
export const Collaboration: Component<{
  text: string;
  open?: (key: string) => boolean;
  onToggle?: (key: string, open: boolean) => void;
}> = (p) => {
  const parsed = createMemo(() => inboxSections(p.text));
  const fromHuman = (s: InboxSection) => s.label.startsWith('human ·');
  const prominent = (s: InboxSection) => s.type === 'question' || s.type === 'result' || s.type === 'answer' || s.type === 'decision_response';
  const excerpt = (body: string) => {
    const line = body.split('\n').find((l) => l.trim() !== '') ?? '';
    return line.length > 120 ? line.slice(0, 117) + '…' : line;
  };
  const body = (s: InboxSection) => (
    <Show when={!fromHuman(s)} fallback={<div style={{ 'white-space': 'pre-wrap' }}>{s.body}</div>}>
      <Markdown text={s.body} />
    </Show>
  );
  return (
    <div data-testid="agent-collaboration" style={{ 'white-space': 'normal', display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceSm}px` }}>
      <Show when={parsed().sections.length > 1}>
        <div style={{ font: tokens.type.monoSm, color: tokens.fgDim }}>{parsed().origin}</div>
      </Show>
      <For each={parsed().sections}>
        {(s, i) => (
          <Show
            when={ROUTINE_INBOX_TYPES.has(s.type)}
            fallback={
              <div
                data-testid="agent-inbox-message"
                data-inbox-type={s.type}
                style={prominent(s) ? { 'border-left': `2px solid ${tokens.accentTeal}`, 'padding-left': `${tokens.spaceMd}px` } : {}}
              >
                <Show when={s.label}>
                  <div style={{ font: tokens.type.monoSm, color: prominent(s) ? tokens.fg : tokens.fgMuted, 'margin-bottom': `${tokens.spaceXs}px` }}>
                    {s.label}
                  </div>
                </Show>
                {body(s)}
              </div>
            }
          >
            <details
              data-testid="agent-inbox-routine"
              data-inbox-type={s.type}
              open={p.open?.(String(i())) ?? false}
              onToggle={(ev) => p.onToggle?.(String(i()), (ev.currentTarget as HTMLDetailsElement).open)}
              style={{ color: tokens.fgMuted, font: tokens.type.textSm }}
            >
              <summary data-wash-hit style={{ cursor: 'pointer', 'user-select': 'none', display: 'flex', gap: `${tokens.spaceSm}px`, 'min-width': 0 }}>
                <span style={{ font: tokens.type.monoSm, 'flex-shrink': 0 }}>{s.label}</span>
                <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', color: tokens.fgDim }}>{excerpt(s.body)}</span>
              </summary>
              <div style={{ 'margin-top': `${tokens.spaceXs}px`, 'padding-left': `${tokens.spaceMd}px`, 'border-left': `2px solid ${tokens.borderMenu}`, color: tokens.fg }}>
                {body(s)}
              </div>
            </details>
          </Show>
        )}
      </For>
    </div>
  );
};

/** Whose turn each event belongs to: a `user` event opens the owner's turn,
 *  a `collaboration` event (an inbox batch) the team's, and every event
 *  after one belongs to it until the next opener. This is the whole basis
 *  of the Owner lane — the transcript already keeps the two apart at the
 *  turn boundary; it only ever merged them in the rendering. */
export type TurnLane = 'owner' | 'team';
export function laneOf(events: readonly agentproto.Event[]): Map<number, TurnLane> {
  const lanes = new Map<number, TurnLane>();
  let current: TurnLane = 'owner';
  for (const e of events) {
    if (e.kind === 'user') current = 'owner';
    else if (e.kind === 'collaboration') current = 'team';
    lanes.set(e.seq, current);
  }
  return lanes;
}

/** Owner prompts that have no reply yet: an owner turn with no `message`
 *  event before the next turn opener. What the Owner tab's badge counts. */
export function unansweredPrompts(events: readonly agentproto.Event[]): number {
  let n = 0;
  let open = false;
  for (const e of events) {
    if (e.kind === 'user') {
      if (open) n++;
      open = true;
    } else if (e.kind === 'collaboration') {
      if (open) n++;
      open = false;
    } else if (e.kind === 'message' && open) {
      open = false;
    }
  }
  return open ? n + 1 : n;
}

/** A pending question, rendered inline as a second view of agentd's queue. */
const AskRow: Component<{
  ask: agentproto.Ask;
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
  // Which thinking panels are open, by event seq. Held here, not in the
  // row: every streamed chunk replaces the event object and so remounts
  // its row, which would snap an opened panel shut on the next chunk.
  const [openThoughts, setOpenThoughts] = createSignal<ReadonlySet<number>>(new Set());
  const setThoughtOpen = (seq: number, open: boolean) => {
    if (openThoughts().has(seq) === open) return;
    const next = new Set(openThoughts());
    if (open) next.add(seq);
    else next.delete(seq);
    setOpenThoughts(next);
  };
  // Routine inbox messages that have been opened, by "<seq>:<index>" — the
  // same reasoning as openThoughts.
  const [openInbox, setOpenInbox] = createSignal<ReadonlySet<string>>(new Set());
  const setInboxOpen = (key: string, open: boolean) => {
    if (openInbox().has(key) === open) return;
    const next = new Set(openInbox());
    if (open) next.add(key);
    else next.delete(key);
    setOpenInbox(next);
  };

  // The two lanes of a workspace orchestrator's transcript. The owner's
  // questions and the orchestrator's answers to them were one line in
  // forty among members' progress and the supervisor's notes; the Owner
  // tab is that conversation alone. Tabs appear only once an inbox turn
  // has arrived — a plain session has one lane and no strip. The composer
  // sits below both: there is one session, and what you type goes to it
  // whichever lane you are reading.
  const lanes = createMemo(() => laneOf(props.events()));
  const hasInbox = createMemo(() => props.events().some((e) => e.kind === 'collaboration'));
  const [lane, setLane] = createSignal<TurnLane>('team');
  // The newest inbox turn seen while on the Team tab; what has arrived
  // since is the Team tab's badge while you read the Owner lane.
  const [teamSeenSeq, setTeamSeenSeq] = createSignal(0);
  const lastInboxSeq = createMemo(() => {
    let last = 0;
    for (const e of props.events()) if (e.kind === 'collaboration' && e.seq > last) last = e.seq;
    return last;
  });
  createEffect(() => {
    if (lane() === 'team') setTeamSeenSeq(lastInboxSeq());
  });
  const unseenTeam = createMemo(() => {
    const since = teamSeenSeq();
    let n = 0;
    for (const e of props.events()) if (e.kind === 'collaboration' && e.seq > since) n++;
    return n;
  });
  const unanswered = createMemo(() => unansweredPrompts(props.events()));
  // What the Owner lane shows: what you typed, and the agent's prose and
  // images in reply. Its tool calls and thinking stay on the Team tab,
  // where the whole turn is.
  const visible = (e: agentproto.Event) => {
    if (lane() === 'team' || !hasInbox()) return true;
    if (e.kind === 'user') return true;
    return (e.kind === 'message' || e.kind === 'image') && lanes().get(e.seq) === 'owner';
  };
  const ownerReply = (e: agentproto.Event) => hasInbox() && e.kind === 'message' && lanes().get(e.seq) === 'owner';

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
  // shortcut that silently picks among several is worse than none. Bound
  // to this session's element, not the window: Agent windows share one
  // page and a workspace keeps hidden sessions mounted, so a window-wide
  // listener answered every session's ask at once.
  const answerFromKeys = (e: KeyboardEvent) => {
    if (!e.altKey || e.ctrlKey || e.metaKey) return;
    const pending = props.asks?.() ?? [];
    if (pending.length === 0) return;
    const k = e.key.toLowerCase();
    if (k !== 'a' && k !== 'd') return;
    e.preventDefault();
    props.onAnswer?.(pending[0].id, k === 'a' ? 'allow' : 'deny');
  };

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

  // One resolver per session view: its cache is what keeps a streaming
  // reply from re-probing the same paths on every chunk.
  const resolver = props.links ? pathResolver(props.links) : undefined;

  return (
    <PathLinksProvider resolver={resolver}>
    <div
      // Esc anywhere in the session — the composer, an ask row's buttons,
      // the transcript — is Stop. Handled here rather than on the textarea
      // so it does not depend on where focus happens to be when a turn
      // goes wrong, and swallowed only when it actually stopped something,
      // so Esc still closes whatever is above this when there is no turn.
      onKeyDown={(e) => {
        answerFromKeys(e);
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

      <Show when={hasInbox()}>
        <div
          role="tablist"
          aria-label="Transcript lanes"
          data-testid="agent-lanes"
          style={{ display: 'flex', 'align-items': 'flex-end', 'flex-shrink': 0, background: tokens.bgMenu, 'border-bottom': `1px solid ${tokens.borderMenu}`, padding: `0 ${tokens.spaceMd}px` }}
        >
          <Tab
            role="tab"
            data-testid="agent-lane-owner"
            active={lane() === 'owner'}
            aria-selected={lane() === 'owner'}
            onClick={() => setLane('owner')}
            title="What you asked, and the orchestrator's replies to you"
          >
            Owner
            <Show when={unanswered() > 0}>
              <span data-testid="agent-lane-owner-badge" title={`${unanswered()} of your messages have no reply yet`} style={{ font: tokens.type.monoSm, color: tokens.accentAmber, 'margin-left': '6px' }}>{unanswered()}</span>
            </Show>
          </Tab>
          <Tab
            role="tab"
            data-testid="agent-lane-team"
            active={lane() === 'team'}
            aria-selected={lane() === 'team'}
            onClick={() => setLane('team')}
            title="The whole transcript: members' messages, tool calls, and every turn"
          >
            Team
            <Show when={lane() === 'owner' && unseenTeam() > 0}>
              <span data-testid="agent-lane-team-badge" title={`${unseenTeam()} inbox turns since you last looked`} style={{ font: tokens.type.monoSm, color: tokens.accentBlue, 'margin-left': '6px' }}>{unseenTeam()}</span>
            </Show>
          </Tab>
        </div>
      </Show>

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
            <Show when={visible(e)}>
            <Show when={e.kind === 'image'}>
              {/* A data: URI, so the image never leaves the machine and
                  no request is made for it. Bounded by agentd before it
                  ever reaches here. */}
              <img
                src={`data:${e.mime || 'image/png'};base64,${e.text ?? ''}`}
                alt="image from the agent"
                style={{
                  // The transcript is a column flex container, so its
                  // default align-items: stretch sets this image's used
                  // width to exactly 100% — max-width cannot hold it back,
                  // and height: auto then scales to match. A small
                  // screenshot arrived upscaled and soft. Opting out of the
                  // stretch lets it sit at its natural size, with
                  // max-width still bounding anything genuinely large.
                  'align-self': 'flex-start',
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

            <Show when={e.kind === 'thought'}>
              <ThoughtRow e={e} open={openThoughts().has(e.seq)} onToggle={(open) => setThoughtOpen(e.seq, open)} />
            </Show>
            <Show
              when={e.kind !== 'tool' && e.kind !== 'image' && e.kind !== 'terminal' && e.kind !== 'decision' && e.kind !== 'thought'}
              fallback={
                <Show when={e.kind === 'tool'} fallback={<Show when={e.kind === 'decision' && !routineYolo(e)}><DecisionRow e={e} /></Show>}>
                  <ToolRow e={e} />
                </Show>
              }
            >
              <div
                data-testid={e.kind === 'user' ? 'agent-human-message' : ownerReply(e) ? 'agent-owner-reply' : undefined}
                style={{
                  font: tokens.type.textMd,
                  color: tokens.fg,
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
                  // In a workspace, the orchestrator's reply TO YOU wears a
                  // thinner version of your rule, so a question and its
                  // answer read as a pair among the turns it spends on
                  // members' mail. Its prose about that mail has no rule.
                  ...(ownerReply(e)
                    ? { 'border-left': `2px solid ${tokens.accentBlue}`, 'padding-left': `${tokens.spaceLg}px` }
                    : {}),
                }}
              >
                {/* Agent-authored prose is Markdown; what you typed is literal.
                    Rendering your own prompt as Markdown would eat the
                    asterisks and backticks you meant to send. */}
                <Show when={e.kind === 'message'} fallback={
                  <Show when={e.kind === 'collaboration'} fallback={<>{e.text}</>}>
                    <Collaboration
                      text={e.text ?? ''}
                      open={(key) => openInbox().has(`${e.seq}:${key}`)}
                      onToggle={(key, open) => setInboxOpen(`${e.seq}:${key}`, open)}
                    />
                  </Show>
                }>
                  <Markdown text={e.text ?? ''} />
                </Show>
              </div>
            </Show>
            </Show>
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

      <QuestionPanel questions={() => props.questions?.() ?? []} onAnswer={props.onQuestionAnswer} />

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
                {st().background && st().state !== 'working' && st().state !== 'needs-input' ? `background · ${st().background}` : agentStateLabel(st().state, st().reason)}
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
    </PathLinksProvider>
  );
};
