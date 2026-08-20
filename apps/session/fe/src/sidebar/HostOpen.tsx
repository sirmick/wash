// A row of doors, one per host (docs/SIDEBAR.md M2c, M3c).
//
// Every relocation milestone ends the same way: the surface moves into an
// app, and what the rail keeps is "what needs me, and where" plus a way
// through. The way through is always the same shape — a button per host,
// named for what you'd find on the other side, opening that host's copy of
// the app via launchOn. Agents was the first; bulk is the second, which is
// why this is a component rather than a pattern copied twice.
//
// The rail cannot do the acting itself: its sends gateway through the
// session BE, which resolves inside its own router. launchOn carries an
// origin. That asymmetry IS the plan, and this button is where it shows.
//
// Deliberately dumb: callers hand over the final list, in order, already
// decided. Whether a host with nothing to report still deserves a door is
// a per-section judgement (com.wash.ai yes — it is where a new session
// starts; fm no — an empty job queue has nothing to manage), so it lives
// with the caller and not in here.

import { For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { tokens } from '@wash/ui';
import { LOCAL_ORIGIN } from './awareness';
import { hostHue } from './host-hue';

export interface HostDoor {
  origin: string;
  /** what you'd find through it — "2 waiting · build01" */
  label: string;
  /** a claim on attention, rendered as a pill. 0 = none. */
  badge?: number;
}

export interface HostOpenProps {
  doors: () => HostDoor[];
  onOpen: (origin: string) => void;
  /** testid prefix; buttons are `${testid}-${origin}` */
  testid: string;
  /** names the destination in the tooltip: "Open the {what} on ..." */
  what: string;
}

export const HostOpen: Component<HostOpenProps> = (props) => (
  <div
    data-testid={props.testid}
    style={{ display: 'flex', 'flex-direction': 'column', gap: '4px', 'margin-top': '4px' }}
  >
    <For each={props.doors()}>
      {(d) => {
        const isLocal = d.origin === LOCAL_ORIGIN;
        // Local is neutral-teal; a remote host wears the same hue the
        // window stripe and its rail group already gave it, so the door
        // and the thing behind it read as one machine.
        const hue = () => (isLocal ? tokens.accentTeal : hostHue(d.origin));
        return (
          <button
            type="button"
            data-testid={`${props.testid}-${d.origin}`}
            data-origin={d.origin}
            data-badge={String(d.badge ?? 0)}
            title={`Open the ${props.what} on ${isLocal ? 'this machine' : d.origin}`}
            onClick={() => props.onOpen(d.origin)}
            style={{
              background: tokens.bgMenu,
              color: tokens.fg,
              border: `1px solid ${tokens.borderMenu}`,
              'border-left': `3px solid ${hue()}`,
              'border-radius': tokens.radiusSm,
              padding: '4px 8px',
              cursor: 'pointer',
              'font-size': '11px',
              'text-align': 'left',
              display: 'flex',
              'align-items': 'center',
              gap: '6px',
            }}
          >
            <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
              {d.label}
            </span>
            <Show when={(d.badge ?? 0) > 0}>
              <span
                data-testid={`${props.testid}-badge-${d.origin}`}
                style={{
                  background: tokens.accentAmber,
                  color: '#fff',
                  'border-radius': tokens.radiusXl,
                  padding: '0 5px',
                  'font-weight': 700,
                }}
              >
                {d.badge}
              </span>
            </Show>
            {/* The affordance, not decoration: this opens a window. */}
            <span style={{ opacity: 0.5 }}>↗</span>
          </button>
        );
      }}
    </For>
  </div>
);
