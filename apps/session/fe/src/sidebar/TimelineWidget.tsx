// TimelineWidget renders the activity journal (docs/COMMANDER.md §6): what
// happened on this seat's hosts, newest first, grouped by hour, every row a
// jump. The App owns the entries (query + live tail merged across origins)
// and the jump; this widget filters, groups and renders.

import type { Component } from 'solid-js';
import { For, Show, createMemo, createSignal } from 'solid-js';
import { tokens, washAssetUrl } from '@wash/ui';
import {
  FAMILIES, familyOf, filterEntries, fmtClock, groupByHour, kindIcon, kindLabel, rowText,
  type TimelineEntry, type TimelineFamily,
} from '../timeline';

export interface TimelineWidgetProps {
  entries: () => TimelineEntry[];
  /** a page beyond the loaded ones exists on some host */
  more: () => boolean;
  loading: () => boolean;
  /** the journal is off on every host */
  off: () => boolean;
  onJump: (e: TimelineEntry) => void;
  onLoadMore: () => void;
  onClear: () => void;
  hostColor: (host: string) => string;
  /** Mission Commander's automatic briefs (docs/COMMANDER.md §5.3), as the
   *  local host's commander reports them; null while unknown. */
  auto?: () => { on: boolean; detail: string } | null;
  onAuto?: (on: boolean) => void;
  onBriefNow?: () => void;
}

// Icon draws one glyph from the shell's sprite, the way Section does.
const Icon: Component<{ name: string; size: number }> = (props) => (
  <svg width={props.size} height={props.size} fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <use href={washAssetUrl(`icons.svg#${props.name}`)} />
  </svg>
);

const familyColor = (f: TimelineFamily): string => {
  switch (f) {
    case 'windows': return tokens.accentBlue;
    case 'agents': return tokens.accentViolet;
    case 'files': return tokens.accentGreen;
    case 'hosts': return tokens.accentAmber;
    default: return tokens.fgMuted;
  }
};

export const TimelineWidget: Component<TimelineWidgetProps> = (props) => {
  const [family, setFamily] = createSignal<TimelineFamily | 'all'>('all');
  const [query, setQuery] = createSignal('');
  const rows = createMemo(() => filterEntries(props.entries(), family(), query()));
  const hours = createMemo(() => groupByHour(rows()));

  return (
    <div data-testid="timeline-widget" style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
      <div style={{ display: 'flex', gap: '4px', 'flex-wrap': 'wrap' }}>
        <For each={FAMILIES}>
          {(f) => (
            <button
              type="button"
              data-wash-hit
              data-testid={`timeline-family-${f.id}`}
              data-active={family() === f.id ? 'true' : undefined}
              onClick={() => setFamily(f.id)}
              style={{
                font: tokens.type.textSm,
                padding: '2px 7px',
                'border-radius': tokens.radiusSm,
                border: `1px solid ${family() === f.id ? tokens.fgMuted : tokens.borderMenu}`,
                background: family() === f.id ? tokens.bgRowSelected : 'transparent',
                color: tokens.fg,
                cursor: 'pointer',
              }}
            >
              {f.label}
            </button>
          )}
        </For>
      </div>
      <input
        type="text"
        data-testid="timeline-search"
        placeholder="Search the timeline…"
        value={query()}
        onInput={(ev) => setQuery(ev.currentTarget.value)}
        style={{
          width: '100%', 'box-sizing': 'border-box', padding: '4px 8px',
          background: 'transparent', color: tokens.fg,
          border: `1px solid ${tokens.borderMenu}`, 'border-radius': tokens.radiusSm,
          outline: 'none', font: tokens.type.textSm,
        }}
      />
      <Show when={props.off()}>
        <div data-testid="timeline-off" style={{ opacity: 0.5, 'font-style': 'italic', 'text-align': 'center', padding: '12px 0', 'font-size': '11px' }}>
          the activity journal is off
        </div>
      </Show>
      <Show when={!props.off() && rows().length === 0}>
        <div data-testid="timeline-empty" style={{ opacity: 0.5, 'font-style': 'italic', 'text-align': 'center', padding: '12px 0', 'font-size': '11px' }}>
          {props.loading() ? 'loading…' : query() || family() !== 'all' ? 'nothing matches' : 'nothing yet'}
        </div>
      </Show>
      <For each={hours()}>
        {(hour) => (
          <div data-testid="timeline-hour">
            <div
              style={{
                font: tokens.type.textSm, color: tokens.fgMuted, 'letter-spacing': '.06em',
                'text-transform': 'uppercase', padding: '6px 2px 2px', 'font-variant-numeric': 'tabular-nums',
              }}
            >
              {hour.label}
            </div>
            <For each={hour.rows}>
              {(e) => {
                const text = rowText(e);
                const remote = e.host !== 'local';
                return (
                  <div
                    data-wash-hit
                    data-testid={`timeline-row-${e.host}-${e.seq}`}
                    data-kind={e.kind}
                    role="button"
                    tabIndex={0}
                    title={e.line}
                    onClick={() => props.onJump(e)}
                    onKeyDown={(ev) => { if (ev.key === 'Enter') props.onJump(e); }}
                    style={{
                      display: 'grid',
                      'grid-template-columns': '38px 16px minmax(0, 1fr)',
                      gap: '6px',
                      'align-items': 'start',
                      padding: '3px 4px',
                      'border-radius': tokens.radiusSm,
                      cursor: e.intent ? 'pointer' : 'default',
                    }}
                  >
                    <span style={{ font: tokens.type.textSm, color: tokens.fgMuted, 'font-variant-numeric': 'tabular-nums' }}>
                      {fmtClock(e.ts)}
                    </span>
                    <span style={{ color: familyColor(familyOf(e.kind)), display: 'inline-flex', 'padding-top': '1px' }}>
                      <Icon name={kindIcon(e.kind)} size={13} />
                    </span>
                    <span style={{ 'min-width': 0 }}>
                      <span style={{ display: 'block', font: tokens.type.textSm, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                        <span style={{ color: tokens.fgMuted }}>{kindLabel(e.kind)} </span>
                        <span>{text.main}</span>
                        <Show when={remote}>
                          <span
                            data-testid="timeline-host"
                            style={{
                              'margin-left': '6px', font: tokens.type.textSm, color: props.hostColor(e.host),
                              border: `1px solid ${props.hostColor(e.host)}`, 'border-radius': '8px', padding: '0 5px',
                            }}
                          >
                            {e.host}
                          </span>
                        </Show>
                      </span>
                      <Show when={text.sub}>
                        <span style={{ display: 'block', font: tokens.type.textSm, color: tokens.fgMuted, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                          {text.sub}
                        </span>
                      </Show>
                    </span>
                  </div>
                );
              }}
            </For>
          </div>
        )}
      </For>
      <div style={{ display: 'flex', gap: '8px', 'justify-content': 'space-between', 'padding-top': '4px' }}>
        <Show when={props.more()}>
          <button type="button" data-wash-hit data-testid="timeline-more" onClick={props.onLoadMore}
            style={{ font: tokens.type.textSm, background: 'transparent', border: 'none', color: tokens.fgMuted, cursor: 'pointer', padding: '2px 4px' }}>
            earlier…
          </button>
        </Show>
        <Show when={props.auto?.()}>
          {(a) => (
            <label data-wash-hit data-testid="timeline-auto" title={a().detail}
              style={{ display: 'inline-flex', 'align-items': 'center', gap: '4px', font: tokens.type.textSm, color: tokens.fgMuted, cursor: 'pointer' }}>
              <input type="checkbox" data-testid="timeline-auto-toggle" checked={a().on} onChange={(e) => props.onAuto?.(e.currentTarget.checked)} />
              auto briefs
              <Show when={a().on}>
                <button type="button" data-wash-hit data-testid="timeline-brief-now" onClick={() => props.onBriefNow?.()}
                  style={{ font: tokens.type.textSm, background: 'transparent', border: 'none', color: tokens.fgMuted, cursor: 'pointer', padding: '0 2px' }}>
                  · now
                </button>
              </Show>
            </label>
          )}
        </Show>
        <Show when={!props.off() && props.entries().length > 0}>
          <button type="button" data-wash-hit data-testid="timeline-clear" onClick={props.onClear}
            style={{ font: tokens.type.textSm, background: 'transparent', border: 'none', color: tokens.fgMuted, cursor: 'pointer', padding: '2px 4px', 'margin-left': 'auto' }}>
            clear journal
          </button>
        </Show>
      </div>
    </div>
  );
};
