// Back/forward navigation history — the pure index arithmetic, lifted
// out of fm's App() closure. Framework-free (no DOM, no Solid) so the
// off-by-one-prone push/back/forward logic is unit-testable with
// node:test instead of being reachable only through the browser e2e.
//
// fm-specific for now (edit has no back/forward), so it lives in-app
// rather than in @wash/fs-client; trivially liftable if edit ever grows
// history nav. The reducer is side-effect-free: back()/forward() return
// where to go, and App applies the navigation (selectPath) + stores the
// new state. Standard browser semantics: pushing after going back
// discards the forward entries.

export interface NavHistory {
  // Visited paths, oldest first. idx points at the current location.
  entries: string[];
  idx: number;
}

export const emptyHistory = (): NavHistory => ({ entries: [], idx: -1 });

// initAt resets history to a single entry — the initial location, on
// first list_ok. Discards any prior history.
export const initAt = (p: string): NavHistory => ({ entries: [p], idx: 0 });

// pushPath records a navigation to p: truncate any forward entries
// (we've branched off the old forward path) then append p and point
// idx at it.
export function pushPath(h: NavHistory, p: string): NavHistory {
  const entries = h.entries.slice(0, h.idx + 1);
  entries.push(p);
  return { entries, idx: entries.length - 1 };
}

export const canBack = (h: NavHistory): boolean => h.idx > 0;
export const canForward = (h: NavHistory): boolean => h.idx < h.entries.length - 1;

// Move describes a back/forward step: the new index + the path now at
// it. Returned (not applied) so the caller drives selectPath itself.
export interface Move {
  idx: number;
  path: string;
}

// back returns the previous entry, or null if already at the oldest.
export function back(h: NavHistory): Move | null {
  if (!canBack(h)) return null;
  const idx = h.idx - 1;
  return { idx, path: h.entries[idx] };
}

// forward returns the next entry, or null if already at the newest.
export function forward(h: NavHistory): Move | null {
  if (!canForward(h)) return null;
  const idx = h.idx + 1;
  return { idx, path: h.entries[idx] };
}

// at returns a copy of h pointing at idx (entries unchanged) — how App
// commits a back/forward Move into the stored history.
export function at(h: NavHistory, idx: number): NavHistory {
  return { entries: h.entries, idx };
}
