// Per-host accent colour for the remote-apps UI (docs/REMOTE.md R2, §11).
// The same hue threads the whole experience: the window top stripe, the
// Hosts sidebar entry, merged sidebar-widget tags, and the priv modal
// band. Drawn from the @wash/ui accent palette so host hues speak the
// desktop's visual language.
//
// LOCAL is neutral (null): local windows show no stripe, so "no stripe"
// reads unambiguously as "this machine".

import { tokens } from '@wash/ui';
import { type Origin, LOCAL_ORIGIN } from './clients';

const PALETTE: string[] = [
  tokens.accentBlue,
  tokens.accentGreen,
  tokens.accentViolet,
  tokens.accentAmber,
  tokens.accentCyan,
  tokens.accentRed,
];

// Manual overrides, set by the Hosts sidebar widget (M3); origin → colour.
const overrides = new Map<Origin, string>();

/** setHostColor pins an explicit colour for an origin (user override). */
export function setHostColor(origin: Origin, color: string): void {
  overrides.set(origin, color);
}

function hash(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  return Math.abs(h);
}

/**
 * hostColor returns the accent for an origin, or null for LOCAL (no
 * stripe). Deterministic by origin name so a host keeps its hue across
 * reconnects; an explicit override wins.
 */
export function hostColor(origin: Origin): string | null {
  if (origin === LOCAL_ORIGIN) return null;
  const o = overrides.get(origin);
  if (o) return o;
  return PALETTE[hash(origin) % PALETTE.length];
}
