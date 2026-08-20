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
//
// The button itself is <HostOpen>, shared with the bulk door since M3c —
// every relocation ends in the same shape. What stays here is the part
// that is about agents: how a host is described, and the rule that local
// always has a door.

import type { Component } from 'solid-js';
import { LOCAL_ORIGIN, type AgentHostSummary } from './awareness';
import { HostOpen, type HostDoor } from './HostOpen';

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
  const doors = (): HostDoor[] => {
    const summaries: AgentHostSummary[] = [{ origin: LOCAL_ORIGIN, waiting: 0, running: 0 }];
    for (const h of props.hosts()) {
      if (h.origin === LOCAL_ORIGIN) summaries[0] = h;
      else summaries.push(h);
    }
    return summaries.map((h) => ({
      origin: h.origin,
      label: label(h.origin, h.waiting, h.running),
      badge: h.waiting,
    }));
  };

  return <HostOpen testid="agents-open" what="Agent app" doors={doors} onOpen={props.onOpen} />;
};
