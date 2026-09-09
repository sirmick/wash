// The History panel — every conversation this machine has had (GH #21).
//
// A menu was the obvious home for this and the wrong one. Menus are fine
// for the last five things; history grows without bound, and after a
// month of use it is hundreds of entries in a dropdown you scroll. The
// metadata is columnar (agent, model, where, when, how long), which a
// single-line menu item cannot carry. And the way you actually find a
// session is by remembering what it was ABOUT — "the one where I was
// chasing the reconnect race" — which is a search, not a scroll.
//
// So: search first, results below, newest first. Searching covers the
// conversation itself and not just the title, because titles are the
// agent's summary of the work and yours is different.
//
// Fork is deliberately absent. Under ACP a fork would be session/fork,
// an UNSTABLE capability whose method shape this build has not verified
// against a real adapter, and the honest version — replaying our own
// stored transcript into a fresh session — is not built yet. A button
// that guesses is worse than one that is missing.

import { For, Show, createSignal, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import { Button, Input, Menu, MenuItem, MenuSeparator, Overlay, fmtBytes, tokens } from '@wash/ui';

/** One stored session, as agentd's history index describes it. */
export interface SessionMeta {
  session_id: string;
  agent?: string;
  model?: string;
  cwd?: string;
  dir?: string;
  title?: string;
  /** the name a person gave it; `title` is then the same string */
  user_title?: string;
  started_ms?: number;
  ended_ms?: number;
  end_reason?: string;
  events?: number;
  bytes?: number;
  /** running right now, per the roster — not something to start again */
  live?: boolean;
  /** running with no window on it: reattach, do not resume */
  detached?: boolean;
  /** the roster key a reattach names */
  row_key?: string;
  /**
   * The line that matched, with a little either side. Present only for a
   * content match — a metadata hit quotes nothing back, because the row
   * already shows the title and directory.
   */
  snippet?: string;
}

/**
 * highlightParts splits a snippet into alternating plain / matched runs
 * so the row can mark what was searched for.
 *
 * Case-insensitive and term-wise, matching the backend: a query is words,
 * all of them required, order irrelevant. Overlapping and repeated hits
 * collapse into one run each, so "race race" does not produce nested
 * marks or drop a character between two adjacent matches.
 *
 * Pure, and exported for the test — it is the only part of this with
 * anything to get wrong.
 */
export function highlightParts(text: string, query: string): { t: string; hit: boolean }[] {
  const terms = query.toLowerCase().split(/\s+/).filter((t) => t.length > 0);
  if (text === '' || terms.length === 0) return [{ t: text, hit: false }];
  const low = text.toLowerCase();
  // Mark every matched byte first, then run-length the flags. Simpler
  // than merging intervals, and immune to the overlap cases.
  const marked = new Array<boolean>(text.length).fill(false);
  for (const term of terms) {
    let at = low.indexOf(term);
    while (at >= 0) {
      for (let i = at; i < at + term.length; i++) marked[i] = true;
      at = low.indexOf(term, at + 1);
    }
  }
  const out: { t: string; hit: boolean }[] = [];
  let start = 0;
  for (let i = 1; i <= text.length; i++) {
    if (i === text.length || marked[i] !== marked[start]) {
      out.push({ t: text.slice(start, i), hit: marked[start] });
      start = i;
    }
  }
  return out;
}

/**
 * What clicking a row should do.
 *
 * The panel used to have exactly one answer — resume — including for
 * sessions that were already running, which duplicates them. That is the
 * outcome the History MENU's live-filter exists to prevent, happening in
 * the view that had no filter. The two views may differ in presentation;
 * they may not differ about what is safe to click.
 */
export function historyAction(s: SessionMeta): 'resume' | 'reattach' | 'focus' | 'none' {
  if (s.detached && s.row_key) return 'reattach';
  // Live with a window: picking it goes THERE (docs/AGENT_UX.md N1).
  // Resuming would fork a second adapter onto one conversation, which is
  // why this used to be inert — but an inert row in a list you opened to
  // get somewhere is its own defect, and "go to it" is the answer the
  // list was always implying.
  if (s.live && s.row_key) return 'focus';
  if (s.live) return 'none';
  return 'resume';
}

/** "just now / 5m ago / 3h ago / 2d ago" — same language as the sidebar. */
export function fmtAgo(nowMS: number, atMS: number): string {
  if (!atMS) return '';
  const secs = Math.max(0, Math.floor((nowMS - atMS) / 1000));
  if (secs < 45) return 'just now';
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86_400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86_400)}d ago`;
}

/** How long the session ran. Absent when it never ended. */
export function fmtSpan(startedMS?: number, endedMS?: number): string {
  if (!startedMS || !endedMS || endedMS <= startedMS) return '';
  const secs = Math.floor((endedMS - startedMS) / 1000);
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  return m ? `${h}h ${m}m` : `${h}h`;
}

/** What to call a session that never named itself. */
export function sessionLabel(s: SessionMeta): string {
  if (s.title) return s.title;
  const where = s.dir || s.cwd;
  if (s.agent && where) return `${s.agent} · ${where}`;
  return s.agent || s.session_id;
}

const metaStyle = {
  font: tokens.type.textSm,
  color: tokens.fgMuted,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
} as const;

export const HistoryPanel: Component<{
  sessions: () => SessionMeta[];
  query: () => string;
  onQuery: (q: string) => void;
  onResume: (s: SessionMeta) => void;
  onClose: () => void;
  /** true between asking and the answer landing — an empty list mid-flight
   *  is not the same claim as "nothing matched". */
  loading?: () => boolean;
  /** give a session a name of your own; the host opens its dialog */
  onRename?: (s: SessionMeta) => void;
  /** delete a stored session — its transcript and its history entry. The
   *  host confirms; a running session is refused by agentd and disabled
   *  here. */
  onDelete?: (s: SessionMeta) => void;
  /** "Delete all older than…" — the host asks for the horizon */
  onPrune?: () => void;
}> = (props) => {
  const now = Date.now();
  let inputEl!: HTMLInputElement;
  // Typing is why the panel is open; landing focus anywhere else means
  // the first thing every user does is click the box.
  onMount(() => inputEl?.focus());
  const [selected, setSelected] = createSignal(0);
  // The per-row verbs menu: which row, and where. Menu portals to
  // document.body, so these are viewport coordinates.
  const [menuFor, setMenuFor] = createSignal<{ s: SessionMeta; x: number; y: number } | null>(null);
  const hasVerbs = () => Boolean(props.onRename || props.onDelete);
  const openMenu = (s: SessionMeta, e: MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setMenuFor({ s, x: e.clientX, y: e.clientY });
  };

  const rows = () => props.sessions();

  return (
    <Overlay
      onDismiss={props.onClose}
      align="top"
      data-testid="ai-history-panel"
      innerStyle={{ width: 'min(760px, 92vw)', 'max-height': '76vh', display: 'flex', 'flex-direction': 'column' }}
    >
      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceMd}px`, 'margin-bottom': `${tokens.spaceMd}px` }}>
        <div style={{ 'font-weight': 600 }}>History</div>
        <div style={{ font: tokens.type.textSm, color: tokens.fgMuted, 'margin-left': 'auto' }}>
          <Show when={!props.loading?.()} fallback="searching…">
            <span data-testid="ai-history-count">{rows().length} session{rows().length === 1 ? '' : 's'}</span>
          </Show>
        </div>
        {/* Pruning lives up here, beside the count it acts on. The store
            grows without bound otherwise, and `rm` in the state dir was
            the only way to lose a conversation. */}
        <Show when={props.onPrune}>
          <Button variant="ghost" data-testid="ai-history-prune" onClick={() => props.onPrune?.()}>
            Delete older than…
          </Button>
        </Show>
      </div>

      <Input
        ref={inputEl}
        data-testid="ai-history-search"
        placeholder="Search conversations, titles, models, directories…"
        value={props.query()}
        onInput={(e: InputEvent) => props.onQuery((e.currentTarget as HTMLInputElement).value)}
        onKeyDown={(e: KeyboardEvent) => {
          // Escape belongs to the Overlay; the arrows and Enter make the
          // list usable without leaving the search box.
          if (e.key === 'ArrowDown') {
            e.preventDefault();
            setSelected((i) => Math.min(i + 1, Math.max(0, rows().length - 1)));
          } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            setSelected((i) => Math.max(0, i - 1));
          } else if (e.key === 'Enter') {
            const s = rows()[selected()];
            if (s && historyAction(s) !== 'none') props.onResume(s);
          }
        }}
      />

      <div
        data-testid="ai-history-list"
        style={{ 'margin-top': `${tokens.spaceMd}px`, overflow: 'auto', flex: 1, 'min-height': 0 }}
      >
        <Show
          when={rows().length > 0}
          fallback={
            <div
              data-testid="ai-history-empty"
              style={{ padding: `${tokens.spaceLg}px 0`, 'text-align': 'center', color: tokens.fgMuted, font: tokens.type.textSm }}
            >
              <Show when={props.query()} fallback="No conversations yet — they are saved from the first message.">
                Nothing matches “{props.query()}”.
              </Show>
            </div>
          }
        >
          <For each={rows()}>
            {(s, i) => (
              <div
                data-wash-hit="subtle"
                data-testid="ai-history-row"
                data-session-id={s.session_id}
                data-action={historyAction(s)}
                onMouseEnter={() => setSelected(i())}
                onClick={() => { if (historyAction(s) !== 'none') props.onResume(s); }}
                onContextMenu={(e) => hasVerbs() && openMenu(s, e)}
                style={{
                  display: 'flex',
                  'flex-direction': 'column',
                  gap: '2px',
                  padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
                  'border-radius': tokens.radiusSm,
                  cursor: historyAction(s) === 'none' ? 'default' : 'pointer',
                  opacity: historyAction(s) === 'none' ? 0.55 : 1,
                  background: selected() === i() ? tokens.bgRowSelected : 'transparent',
                }}
              >
                <div style={{ display: 'flex', 'align-items': 'baseline', gap: `${tokens.spaceMd}px` }}>
                  <span
                    data-testid="ai-history-title"
                    style={{ 'font-weight': 600, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}
                  >
                    {sessionLabel(s)}
                  </span>
                  {/* A running session says so rather than looking like
                      one more thing to open. "Open" is the honest word
                      for a detached one: it is still running, and this
                      puts a window back on it. */}
                  <Show when={historyAction(s) !== 'resume'}>
                    <span
                      data-testid="ai-history-state"
                      style={{
                        ...metaStyle,
                        'flex-shrink': 0,
                        color: historyAction(s) === 'reattach' ? tokens.accentAmber : tokens.fgMuted,
                      }}
                    >
                      {historyAction(s) === 'reattach' ? 'running — open' : 'running — go to it'}
                    </span>
                  </Show>
                  <span style={{ ...metaStyle, 'margin-left': 'auto', 'flex-shrink': 0 }}>
                    {fmtAgo(now, s.ended_ms || s.started_ms || 0)}
                  </span>
                  {/* Right-click works on the row, but a right-click-only
                      verb is a verb nobody finds — same ellipsis the
                      roster rows carry. */}
                  <Show when={hasVerbs()}>
                    <button
                      type="button"
                      data-testid="ai-history-verbs"
                      data-wash-hit
                      title="Session actions"
                      aria-label="Session actions"
                      aria-haspopup="menu"
                      onClick={(e) => openMenu(s, e)}
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
                {/* The metadata line is why this is a panel and not a
                    menu: it does not fit on one. */}
                <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, ...metaStyle }}>
                  <Show when={s.agent}><span data-testid="ai-history-agent">{s.agent}</span></Show>
                  <Show when={s.model}><span data-testid="ai-history-model">{s.model}</span></Show>
                  <Show when={s.dir || s.cwd}>
                    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                      {s.dir || s.cwd}
                    </span>
                  </Show>
                  <span style={{ 'margin-left': 'auto', 'flex-shrink': 0, display: 'flex', gap: `${tokens.spaceMd}px` }}>
                    <Show when={fmtSpan(s.started_ms, s.ended_ms)}>
                      <span>{fmtSpan(s.started_ms, s.ended_ms)}</span>
                    </Show>
                    <Show when={s.events}><span>{s.events} lines</span></Show>
                    <Show when={s.bytes}><span>{fmtBytes(s.bytes ?? 0)}</span></Show>
                    {/* A session with no ending is one the router
                        outlived — worth saying, because resuming it
                        behaves differently from one that finished. */}
                    <Show when={!s.end_reason}>
                      <span data-testid="ai-history-unfinished" style={{ color: tokens.accentAmber }}>
                        unfinished
                      </span>
                    </Show>
                  </span>
                </div>
                {/* Why this row is in the list. Without it a search
                    result is a title you still have to open to identify,
                    which is the thing searching was meant to save. */}
                <Show when={s.snippet}>
                  <div
                    data-testid="ai-history-snippet"
                    style={{
                      ...metaStyle,
                      'margin-top': '2px',
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                      'white-space': 'nowrap',
                    }}
                  >
                    <For each={highlightParts(s.snippet ?? '', props.query())}>
                      {(part) => (
                        <Show when={part.hit} fallback={<span>{part.t}</span>}>
                          <span
                            data-testid="ai-history-hit"
                            style={{ color: tokens.accentAmber, 'font-weight': 600 }}
                          >
                            {part.t}
                          </span>
                        </Show>
                      )}
                    </For>
                  </div>
                </Show>
              </div>
            )}
          </For>
        </Show>
      </div>

      <div style={{ display: 'flex', 'justify-content': 'flex-end', gap: `${tokens.spaceMd}px`, 'margin-top': `${tokens.spaceMd}px` }}>
        <Button data-testid="ai-history-close" onClick={props.onClose}>Close</Button>
      </div>

      <Show when={menuFor()}>
        {(m) => (
          <Menu x={m().x} y={m().y} onDismiss={() => setMenuFor(null)} data-testid="ai-history-actions">
            <MenuItem
              label="Rename…"
              data-testid="ai-history-menu-rename"
              disabled={!props.onRename}
              onClick={() => { const s = m().s; setMenuFor(null); props.onRename?.(s); }}
            />
            <MenuSeparator />
            {/* A running session cannot be deleted — its file is being
                written — and the item says so by being disabled rather
                than absent. End it first; then it is history. */}
            <MenuItem
              label={m().s.live ? 'Delete (still running)' : 'Delete…'}
              data-testid="ai-history-menu-delete"
              disabled={!props.onDelete || m().s.live === true}
              onClick={() => { const s = m().s; setMenuFor(null); props.onDelete?.(s); }}
            />
          </Menu>
        )}
      </Show>
    </Overlay>
  );
};
