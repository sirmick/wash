// Per-host accent hue for the rail (docs/REMOTE.md §11).
//
// A host keeps one colour across the whole experience — its window top
// stripe, its toast, its Hosts entry, and (since docs/SIDEBAR.md M1c) its
// awareness group in every section. Deterministic from the origin name, so
// the hue survives reconnects with nothing to persist.
//
// This mirrors web/shell/src/host-colors.ts rather than importing it: the
// shell and an app FE are separate bundles, and @wash/ui is the only shared
// surface between them. It was already duplicated into the Hosts widget and
// wash-connect; this module is where the sidebar's copy lives now, so the
// rail has one definition instead of one per widget.
//
// Known gap, inherited: the shell's version honours user overrides
// (setHostColor). Those aren't visible from an app bundle, so a pinned
// colour would show in the window stripe and not here. Folding host hues
// into a shell-exposed accessor is the fix, and it belongs with the M6
// cleanup rather than here.

import { tokens } from '@wash/ui';

const PALETTE = [
  tokens.accentBlue,
  tokens.accentGreen,
  tokens.accentViolet,
  tokens.accentAmber,
  tokens.accentCyan,
  tokens.accentRed,
];

/** hostHue returns the stable accent for an origin. */
export function hostHue(origin: string): string {
  let h = 0;
  for (let i = 0; i < origin.length; i++) h = ((h << 5) - h + origin.charCodeAt(i)) | 0;
  return PALETTE[Math.abs(h) % PALETTE.length];
}
