// Per-host awareness groups inside a rail section (docs/SIDEBAR.md M1c,
// §3.2(1)).
//
// The rail shows every host at once rather than following the focused one:
// the interrupts worth surfacing are precisely the ones from machines you
// are NOT looking at, and the focused host's state is already on screen in
// its own windows. Follow-focus's one good idea survives here as
// presentation — local first (the section's own body), then a group per
// remote host, collapsed but badged, auto-expanding when that host says
// something new, with the focused window's host emphasised.
//
// Awareness only, deliberately: a group states what is happening on that
// host and stops. Acting on it means opening the owning app THERE, which
// is what M2–M5 relocate — a control affordance here would be the original
// defect wearing a host label.
//
// docs/AGENT_UX.md §1 sharpens that rule rather than breaking it: what the
// doctrine forbids is chrome that MUTATES app state, and opening a window
// mutates nothing. A section may therefore pass `onOpen` to put a single ↗
// door on each host row — navigation, not control. Sections that don't
// pass it stay exactly as inert as before, which is most of them; the ↗ is
// deliberately separate from the header so the disclosure gesture and the
// travel gesture can never be confused for one another.
//
// Group collapse rides the section-state machinery (ids are
// "<section>:<origin>"), so it persists and auto-expands exactly as a
// section does, with no second mechanism to keep in sync.

import { For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { tokens } from '@wash/ui';
import type { HostGroupRow } from './awareness';
import type { SectionState } from './Section';

export interface HostGroupsProps {
  /** The section these groups belong to; used for state ids + testids. */
  section: string;
  rows: () => HostGroupRow[];
  /** Per-host collapse state, keyed "<section>:<origin>". */
  stateFor: (id: string) => SectionState;
  onToggle: (id: string) => void;
  /** Accent for this host, from the shell's per-origin hue. Null = none. */
  hostColor: (origin: string) => string | null;
  /** Origin of the focused window, so its host reads as the current one. */
  focusedOrigin?: () => string | undefined;
  /**
   * Optional ↗ door per host row: go to this host's window for the section's
   * app, opening it if there is none (docs/AGENT_UX.md N1). Omit and the
   * rows stay inert.
   */
  onOpen?: (origin: string) => void;
  /** Tooltip for the ↗, e.g. "Open Agent on build01". */
  openTitle?: (origin: string) => string;
}

/** groupID is the section-state key for one host's group. */
export function groupID(section: string, origin: string): string {
  return `${section}:${origin}`;
}

export const HostGroups: Component<HostGroupsProps> = (props) => {
  const isFocused = (origin: string) => props.focusedOrigin?.() === origin;

  return (
    <Show when={props.rows().length > 0}>
      <div
        data-testid={`host-groups-${props.section}`}
        style={{ display: 'flex', 'flex-direction': 'column', 'margin-top': '6px', gap: '2px' }}
      >
        <For each={props.rows()} fallback={null}>
          {(row) => {
            const id = () => groupID(props.section, row.origin);
            const open = () => props.stateFor(id()) === 'expanded';
            // A reconnecting host's numbers are from before the blip. Show
            // them, but dimmed and labelled — the honest version of a stale
            // count (§3.2(4)).
            const stale = () => row.status === 'reconnecting';
            const hue = () => props.hostColor(row.origin);
            return (
              <div
                data-testid={`host-group-${props.section}-${row.origin}`}
                data-origin={row.origin}
                data-status={row.status}
                data-state={open() ? 'expanded' : 'collapsed'}
                data-focused={isFocused(row.origin) ? 'true' : 'false'}
                style={{ opacity: stale() ? 0.55 : 1 }}
              >
                <div
                  onClick={() => props.onToggle(id())}
                  data-testid={`host-group-header-${props.section}-${row.origin}`}
                  style={{
                    display: 'flex',
                    'align-items': 'center',
                    gap: '5px',
                    cursor: 'pointer',
                    'user-select': 'none',
                    font: tokens.type.textSm,
                    padding: '1px 0',
                  }}
                >
                  <span
                    style={{
                      'flex-shrink': 0,
                      width: '9px',
                      'font-size': '9px',
                      color: tokens.fgMuted,
                      transition: 'transform 120ms',
                      transform: open() ? 'rotate(90deg)' : 'rotate(0deg)',
                      display: 'inline-block',
                    }}
                  >
                    ▶
                  </span>
                  {/* The host's own hue, the same one its windows and toasts
                      wear (docs/REMOTE.md §11) — one thread through the
                      whole remote experience. */}
                  <span
                    data-testid={`host-group-name-${props.section}-${row.origin}`}
                    style={{
                      color: hue() ?? tokens.fg,
                      'font-weight': isFocused(row.origin) ? 700 : 600,
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                      'white-space': 'nowrap',
                    }}
                  >
                    {row.origin}
                  </span>
                  <span style={{ flex: 1 }} />
                  <Show when={stale()}>
                    <span
                      data-testid={`host-group-stale-${props.section}-${row.origin}`}
                      style={{ color: tokens.fgMuted, font: tokens.type.textSm }}
                    >
                      reconnecting
                    </span>
                  </Show>
                  <Show when={row.badge}>
                    <span
                      data-testid={`host-group-badge-${props.section}-${row.origin}`}
                      style={{
                        'font-size': '10px',
                        padding: '0 5px',
                        'border-radius': tokens.radiusXl,
                        background: hue() ?? tokens.accentBlue,
                        color: '#fff',
                        'font-weight': 700,
                      }}
                    >
                      {row.badge}
                    </span>
                  </Show>
                  {/* stopPropagation: the header owns the disclosure, this
                      owns the travel. Without it every trip to a host would
                      also fold the group you just looked at. */}
                  <Show when={props.onOpen}>
                    <span
                      data-testid={`host-group-open-${props.section}-${row.origin}`}
                      title={props.openTitle?.(row.origin) ?? `Open on ${row.origin}`}
                      onClick={(ev) => {
                        ev.stopPropagation();
                        props.onOpen?.(row.origin);
                      }}
                      style={{
                        'flex-shrink': 0,
                        'font-size': '10px',
                        padding: '0 3px',
                        color: tokens.fgMuted,
                        cursor: 'pointer',
                      }}
                      onMouseEnter={(ev) => (ev.currentTarget.style.color = tokens.fg)}
                      onMouseLeave={(ev) => (ev.currentTarget.style.color = tokens.fgMuted)}
                    >
                      ↗
                    </span>
                  </Show>
                </div>
                <Show when={open()}>
                  <div
                    data-testid={`host-group-body-${props.section}-${row.origin}`}
                    style={{
                      padding: '1px 0 3px 14px',
                      color: tokens.fgMuted,
                      font: tokens.type.textSm,
                    }}
                  >
                    {row.summary || 'nothing to report'}
                  </div>
                </Show>
              </div>
            );
          }}
        </For>
      </div>
    </Show>
  );
};
