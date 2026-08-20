// The Agents section's way in (docs/SIDEBAR.md M2c).
//
// The roster and its verbs moved into com.wash.ai, because that is where
// they can be correct: an app talking to its own host's agentd carries a
// router-attested sender, so `launchOn(origin, 'com.wash.ai')` gets
// working verbs on ANY host with no new addressing. The rail could never
// have that — its sends gateway through the session BE, which resolves
// inside its own router.
//
// So what stays here is the answer to "what needs me, and where", plus a
// door. One button per host with agents, named for what you'd find:
// "2 waiting · build01". Local first, same order as everything else in
// the rail.
//
// This is the deep-link the §3.2(7) tripwire is about. If it starts
// feeling like a toll — if you keep opening the app for things the rail
// used to answer at a glance — that is the signal to re-cut M3–M5 toward
// richer rail widgets over hostgw instead. Worth noticing that the thing
// you'd miss is a GLANCE, which the host groups above already give; what
// you cross this door for is to ACT.

import { For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { tokens } from '@wash/ui';
import { LOCAL_ORIGIN, type AgentHostSummary } from './awareness';
import { hostHue } from './host-hue';

export interface AgentOpenProps {
  /** hosts with agent activity, local first */
  hosts: () => AgentHostSummary[];
  onOpen: (origin: string) => void;
}

/** label says what you'd find on the other side of the door. */
function label(origin: string, waiting: number, running: number): string {
  const where = origin === LOCAL_ORIGIN ? '' : ` · ${origin}`;
  if (waiting > 0) return `${waiting} waiting${where}`;
  if (running > 0) return `${running} running${where}`;
  return `Open Agent${where}`;
}

export const AgentOpen: Component<AgentOpenProps> = (props) => {
  // Every host with agents gets a button, plus LOCAL always — "open the
  // Agent app here" is a reasonable thing to want with nothing running at
  // all, and it is where a new session starts.
  const hosts = (): AgentHostSummary[] => {
    const out: AgentHostSummary[] = [{ origin: LOCAL_ORIGIN, waiting: 0, running: 0 }];
    for (const h of props.hosts()) {
      if (h.origin === LOCAL_ORIGIN) out[0] = h;
      else out.push(h);
    }
    return out;
  };

  return (
    <div
      data-testid="agents-open"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '4px', 'margin-top': '4px' }}
    >
      <For each={hosts()}>
        {(h) => {
          const isLocal = h.origin === LOCAL_ORIGIN;
          const hue = () => (isLocal ? tokens.accentTeal : hostHue(h.origin));
          return (
            <button
              type="button"
              data-testid={`agents-open-${h.origin}`}
              data-origin={h.origin}
              data-waiting={String(h.waiting)}
              title={`Open the Agent app on ${isLocal ? 'this machine' : h.origin}`}
              onClick={() => props.onOpen(h.origin)}
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
                {label(h.origin, h.waiting, h.running)}
              </span>
              {/* The affordance, not decoration: this opens a window. */}
              <Show when={h.waiting > 0}>
                <span
                  data-testid={`agents-open-badge-${h.origin}`}
                  style={{
                    background: tokens.accentAmber,
                    color: '#fff',
                    'border-radius': tokens.radiusXl,
                    padding: '0 5px',
                    'font-weight': 700,
                  }}
                >
                  {h.waiting}
                </span>
              </Show>
              <span style={{ opacity: 0.5 }}>↗</span>
            </button>
          );
        }}
      </For>
    </div>
  );
};
