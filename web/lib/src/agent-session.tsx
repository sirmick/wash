// <AgentSession> — the transcript surface for a managed agent session
// (docs/AGENT_APP.md §9).
//
// Rendered by three hosts: the standalone com.wash.ai window, a wash-term
// pane, and a wash-edit panel. It therefore owns NOTHING — no session, no
// launcher, no approval logic, no subscription. Everything arrives as an
// accessor and leaves as a callback, exactly the contract <Terminal> has.
//
// The transcript is deliberately one line per tool call. Diffs open in
// wash-edit, commands run in a wash-term tab, approvals live in agentd's
// queue — this component's job is to be the thing that points at them,
// not to reimplement any of them.

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';
import { Markdown } from './markdown';

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
  /** set on kind==="image"; text then holds the base64 bytes */
  mime?: string;
  at_ms: number;
}

/** A permission question waiting on this session. */
export interface AgentAsk {
  id: string;
  tool: string;
  subject?: string;
  suggested_rule?: string;
  age_ms: number;
}

/** What the status line shows. */
export interface AgentStatus {
  agent?: string;
  model?: string;
  dir?: string;
  branch?: string;
  dirty?: boolean;
  /** running | working | needs-input | done */
  state?: string;
  /** context tokens used / window size, from the agent's usage_update */
  used?: number;
  size?: number;
  /** the agent's own name for this session */
  title?: string;
  /** the agent's active approval preset, and what it offers */
  mode?: string;
  modes?: { id: string; name: string; description?: string }[];
}

export interface AgentSessionProps {
  events: () => AgentEvent[];
  asks?: () => AgentAsk[];
  status?: () => AgentStatus;
  /** Send a prompt. Absent while the session is not ready. */
  onSend?: (text: string) => void;
  /** Answer a pending question. `rule` is set when the user chose "always". */
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string) => void;
  /** Click on a tool row — the host decides what that opens. */
  onOpenTool?: (e: AgentEvent) => void;
  /** Abort the running turn. Absent means the session cannot be stopped. */
  onCancel?: () => void;
  /** Switch the agent's approval preset. Absent hides the control. */
  onSetMode?: (modeID: string) => void;
  /** Rendered above the transcript; the launcher uses it for its form. */
  header?: JSX.Element;
  placeholder?: string;
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
const Spinner: Component<{ size?: number }> = (p) => (
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
      'border-top-color': tokens.accentBlue,
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

/** One tool call: kind, argument, state. Clickable when the host says so. */
const ToolRow: Component<{ e: AgentEvent; onOpen?: (e: AgentEvent) => void }> = (p) => {
  const clickable = () => !!p.onOpen;
  return (
    <div
      role={clickable() ? 'button' : undefined}
      tabindex={clickable() ? 0 : undefined}
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
      <Dot color={dotColor(p.e.status)} />
    </div>
  );
};

// Keyboard answers use Alt rather than a bare letter: the composer is
// focused most of the time, and a bare A must stay a letter you can type.
// Alt+A / Alt+D collide with nothing in the terminal-adjacent muscle
// memory this desktop already trains.
const ALLOW_HINT = '⌥A';
const DENY_HINT = '⌥D';

const hintStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  opacity: 0.7,
  'margin-left': `${tokens.spaceXs}px`,
};

/** A pending question, rendered inline as a second view of agentd's queue. */
const AskRow: Component<{
  ask: AgentAsk;
  /** only the row a keystroke would answer advertises the shortcut */
  keyed?: boolean;
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string) => void;
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
      type="button"
      onClick={() => p.onAnswer?.(p.ask.id, 'allow')}
      style={askBtn(tokens.bgSuccess, tokens.fgSuccess)}
    >
      Allow
      <Show when={p.keyed}>
        <span style={hintStyle}>{ALLOW_HINT}</span>
      </Show>
    </button>
    <Show when={p.ask.suggested_rule}>
      <button
        type="button"
        onClick={() => p.onAnswer?.(p.ask.id, 'allow', p.ask.suggested_rule)}
        style={askBtn(tokens.bgInfo, tokens.fgInfo)}
      >
        Always allow <span style={{ font: tokens.type.monoSm }}>{p.ask.suggested_rule}</span>
      </button>
    </Show>
    <button
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

  const send = () => {
    const text = draft().trim();
    if (!text || !props.onSend) return;
    props.onSend(text);
    setDraft('');
    setPinned(true);
  };

  const st = () => props.status?.() ?? {};

  return (
    <div
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
        onScroll={() => setPinned(atBottom())}
        style={{
          flex: 1,
          'min-height': 0,
          'overflow-y': 'auto',
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

            <Show
              when={e.kind !== 'tool' && e.kind !== 'image'}
              fallback={<Show when={e.kind === 'tool'}><ToolRow e={e} onOpen={props.onOpenTool} /></Show>}
            >
              <div
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
                        'border-left': `2px solid ${tokens.borderFocus}`,
                        'padding-left': `${tokens.spaceMd}px`,
                        color: tokens.fgMuted,
                      }
                    : {}),
                }}
              >
                {/* Agents answer in Markdown; what you typed is literal.
                    Rendering your own prompt as Markdown would eat the
                    asterisks and backticks you meant to send. */}
                <Show when={e.kind === 'message'} fallback={<>{e.text}</>}>
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
        <Show when={props.status?.().state === 'working' && (props.asks?.() ?? []).length === 0}>
          <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceMd}px`, color: tokens.fgDim, font: tokens.type.textSm }}>
            <Spinner />
            <span>working…</span>
            <Show when={props.onCancel}>
              <button
                type="button"
                data-testid="agent-stop"
                onClick={() => props.onCancel?.()}
                style={askBtn(tokens.bgNeutral, tokens.fgMuted)}
              >
                Stop
              </button>
            </Show>
          </div>
        </Show>
      </div>

      <div
        style={{
          flex: 'none',
          'border-top': `1px solid ${tokens.borderMenu}`,
          padding: `${tokens.spaceMd}px`,
        }}
      >
        <textarea
          ref={input}
          rows={2}
          value={draft()}
          disabled={!props.onSend}
          placeholder={props.placeholder ?? 'Ask, or drop a file from wash-fm…'}
          onInput={(e) => setDraft(e.currentTarget.value)}
          onKeyDown={(e) => {
            // Enter sends; Shift+Enter is a newline. A composer that
            // needed a modifier to send would be wrong for a chat and a
            // surprise in every other wash text field.
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              send();
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
            color: tokens.fg,
            outline: 'none',
            'box-sizing': 'border-box',
          }}
        />
      </div>

      <div
        style={{
          flex: 'none',
          height: '22px',
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
        <Show
          when={st().state === 'working'}
          fallback={
            <Show when={st().state}>
              <Dot color={st().state === 'needs-input' ? tokens.accentAmber : st().state === 'done' ? tokens.accentGreen : tokens.fgDim} />
            </Show>
          }
        >
          <Spinner size={9} />
        </Show>
        <Show when={st().agent}>
          <span>{st().agent}</span>
        </Show>
        <Show when={st().dir}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span>{st().dir}</span>
        </Show>
        {/* The approval preset lives HERE, on the session it governs,
            rather than in Settings: it is a per-session decision about
            this piece of work, and the agent owns it — wash is only
            asking. Changing it is visible to the agent and reversible
            from either side, unlike a blanket allow wash keeps to
            itself. */}
        <Show when={(st().modes?.length ?? 0) > 0 && props.onSetMode}>
          <select
            data-testid="agent-mode"
            value={st().mode ?? ''}
            title={st().modes?.find((m) => m.id === st().mode)?.description ?? 'Approval mode'}
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
                <option value={m.id} title={m.description}>
                  {m.name}
                </option>
              )}
            </For>
          </select>
          <span style={{ color: tokens.fgDim }}>·</span>
        </Show>

        <Show when={st().used && st().size}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span title="context used / window">{fmtTokens(st().used!)}/{fmtTokens(st().size!)}</span>
        </Show>
        <Show when={st().branch}>
          <span style={{ color: tokens.fgDim }}>·</span>
          <span>
            {st().branch}
            {st().dirty ? ' *' : ''}
          </span>
        </Show>
      </div>
    </div>
  );
};
