// What an Agent window re-asks agentd for when the shell's connection comes
// back without remounting it (GH #21).
//
// Every view this window shows is a push: the manager's roster, a
// controller's row and asks, the transcript. The router drops pushes sent
// while no shell is attached, and agentd only re-sends a view that CHANGED
// from the last one it sent — so a session that went quiet during the
// outage (finished its turn, was detached, started) stayed wrong on screen,
// with the verbs for its old state, until something else happened to it.
// A reload was fine, because mount asks; a blip remounts nothing.
//
// Both asks are idempotent for a window that already holds the role: a
// manager_subscribe or a claim from the lease holder just force-sends the
// current view.

import type { agentproto } from '@wash/ui';

export function reconnectRequests(role: 'session' | 'manager', sessionKey: string): agentproto.AgentdRequest[] {
  if (role === 'manager') return [{ kind: 'manager_subscribe' }];
  // A controller with no session yet is a launcher; there is nothing to
  // re-claim, and its pending start answers on its own.
  if (!sessionKey) return [];
  return [
    { kind: 'session_claim', key: sessionKey },
    { kind: 'transcript_subscribe', key: sessionKey, replay: true },
  ];
}
