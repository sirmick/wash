// Pure decisions for the shell's window switcher (Ctrl+Alt+Tab) and
// show-desktop (Ctrl+Alt+D). wm.ts is DOM-bound; this file is not, so the
// MRU / cycling / minimise-restore rules get a `node --test` net.
//
// MRU order comes from the wm's global stacking counter `gz`: every
// raise/focus/first-appearance bumps it, so "highest gz first" IS the focus
// history without keeping a second list that could drift from it.

export interface SwitchableWin {
  origin: string;
  windowID: number;
  gz: number;
  state: 'normal' | 'minimized' | 'maximized';
}

/** mruOrder returns wins most-recently-focused first. Minimised windows are
 * included (Alt+Tab on every desktop lets you switch to a minimised
 * window; the commit path restores it). Stable for equal gz. */
export function mruOrder<T extends { gz: number }>(wins: ReadonlyArray<T>): T[] {
  return [...wins].sort((a, b) => b.gz - a.gz);
}

/** initialIndex is where the highlight starts when the switcher opens: the
 * PREVIOUS window (index 1) when there is one, so a single tap swaps to it;
 * the only window otherwise. */
export function initialIndex(count: number): number {
  return count > 1 ? 1 : 0;
}

/** cycle advances the highlight with wraparound; backwards for Shift. */
export function cycle(index: number, count: number, backwards: boolean): number {
  if (count <= 0) return 0;
  return backwards ? (index - 1 + count) % count : (index + 1) % count;
}

/** isSwitcherChord: Ctrl+Alt+Tab (forward) / Ctrl+Alt+Shift+Tab (back). Meta
 * excluded so a Super-bound OS chord never doubles as ours. */
export function isSwitcherChord(ev: { key: string; ctrlKey: boolean; altKey: boolean; metaKey: boolean }): boolean {
  return ev.key === 'Tab' && ev.ctrlKey && ev.altKey && !ev.metaKey;
}

/** isShowDesktopChord: Ctrl+Alt+D. */
export function isShowDesktopChord(ev: {
  key: string;
  ctrlKey: boolean;
  altKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
}): boolean {
  return (ev.key === 'd' || ev.key === 'D') && ev.ctrlKey && ev.altKey && !ev.metaKey && !ev.shiftKey;
}

/** chordReleased: the switcher commits when EITHER held modifier goes up —
 * Ctrl or Alt — matching how people let go of a chord (rarely both keys in
 * the same frame). */
export function chordReleased(key: string): boolean {
  return key === 'Control' || key === 'Alt' || key === 'Meta';
}

export type ShowDesktopPlan =
  | { action: 'minimize'; targets: Array<{ origin: string; windowID: number }> }
  | { action: 'restore'; targets: Array<{ origin: string; windowID: number }> }
  | { action: 'none' };

/** showDesktopPlan decides what Ctrl+Alt+D does:
 *  - anything is showing → minimise all of it (the caller remembers the set);
 *  - nothing showing and a remembered set exists → restore those that still
 *    exist (a window closed while hidden is simply skipped);
 *  - otherwise nothing. */
export function showDesktopPlan(
  wins: ReadonlyArray<SwitchableWin>,
  remembered: ReadonlyArray<{ origin: string; windowID: number }> | null,
): ShowDesktopPlan {
  const showing = wins.filter((w) => w.state !== 'minimized');
  if (showing.length > 0) {
    return { action: 'minimize', targets: showing.map((w) => ({ origin: w.origin, windowID: w.windowID })) };
  }
  if (remembered && remembered.length > 0) {
    const alive = remembered.filter((r) => wins.some((w) => w.origin === r.origin && w.windowID === r.windowID));
    if (alive.length > 0) return { action: 'restore', targets: alive };
  }
  return { action: 'none' };
}
