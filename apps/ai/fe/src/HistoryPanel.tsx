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
import { Button, Input, Overlay, fmtBytes, tokens } from '@wash/ui';

/** One stored session, as agentd's history index describes it. */
export interface SessionMeta {
  session_id: string;
  agent?: string;
  model?: string;
  cwd?: string;
  dir?: string;
  title?: string;
  started_ms?: number;
  ended_ms?: number;
  end_reason?: string;
  events?: number;
  bytes?: number;
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
}> = (props) => {
  const now = Date.now();
  let inputEl!: HTMLInputElement;
  // Typing is why the panel is open; landing focus anywhere else means
  // the first thing every user does is click the box.
  onMount(() => inputEl?.focus());
  const [selected, setSelected] = createSignal(0);

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
            if (s) props.onResume(s);
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
                data-testid="ai-history-row"
                data-session-id={s.session_id}
                onMouseEnter={() => setSelected(i())}
                onClick={() => props.onResume(s)}
                style={{
                  display: 'flex',
                  'flex-direction': 'column',
                  gap: '2px',
                  padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
                  'border-radius': tokens.radiusSm,
                  cursor: 'pointer',
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
                  <span style={{ ...metaStyle, 'margin-left': 'auto', 'flex-shrink': 0 }}>
                    {fmtAgo(now, s.ended_ms || s.started_ms || 0)}
                  </span>
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
              </div>
            )}
          </For>
        </Show>
      </div>

      <div style={{ display: 'flex', 'justify-content': 'flex-end', gap: `${tokens.spaceMd}px`, 'margin-top': `${tokens.spaceMd}px` }}>
        <Button data-testid="ai-history-close" onClick={props.onClose}>Close</Button>
      </div>
    </Overlay>
  );
};
