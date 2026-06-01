// Pure service-status decisions, extracted from the StatusBadge component +
// the row predicates so the (active, sub) → tone/label mapping is unit-tested
// directly instead of only through the services e2e. The component maps the
// returned tone to colours + an icon; this owns the branching.

export type BadgeTone = 'failed' | 'active' | 'transitioning' | 'inactive';

// Classify a unit's (ActiveState, SubState) into a presentation tone + the
// human label to show. Order matters: failed wins, then running, then the
// in-flight states, else inactive.
export function serviceBadge(active: string, sub: string): { tone: BadgeTone; label: string } {
  if (active === 'failed' || sub === 'crashed') return { tone: 'failed', label: 'failed' };
  if (active === 'active' || active === 'reloading') return { tone: 'active', label: sub || 'running' };
  if (active === 'activating' || active === 'deactivating') return { tone: 'transitioning', label: active };
  return { tone: 'inactive', label: sub || active || 'inactive' };
}

// A unit counts as "active" (drives the row's data-active marker) when it's
// running or mid-reload.
export const isActive = (active: string): boolean => active === 'active' || active === 'reloading';

// A unit counts as "failed" when systemd says failed or the sub-state crashed.
export const isFailed = (active: string, sub: string): boolean => active === 'failed' || sub === 'crashed';
