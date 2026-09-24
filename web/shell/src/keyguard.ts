// Desktop-background keyboard guard (docs/Review-findings.md P2 "desktop
// keyboard focus leaks").
//
// With no wash window focused — the person clicked the wallpaper — a
// Ctrl+W / Ctrl+N / Ctrl+T reaches the browser and closes the tab or opens
// another one, which from inside the desktop reads as "the whole thing just
// vanished". Apps in windows already swallow what they own (edit's Ctrl+W
// closes a tab), so the guard is scoped to the no-focus case only.
//
// Limitation, stated plainly: Chromium treats Ctrl+W / Ctrl+T / Ctrl+N as
// browser-reserved and never delivers them to the page, so there the guard
// is a no-op. Firefox and the login-fronted PWA / kiosk contexts do deliver
// them, and there it works. It is cheap, so it is on everywhere.

const SWALLOWED = new Set(['w', 'n', 't']);

export interface GuardKey {
  key: string;
  ctrlKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

/** shouldSwallowDesktopKey: true for Ctrl+W/N/T (no Alt/Meta, any Shift)
 * while no wash window has focus. */
export function shouldSwallowDesktopKey(ev: GuardKey, hasFocusedWindow: boolean): boolean {
  if (hasFocusedWindow) return false;
  if (!ev.ctrlKey || ev.altKey || ev.metaKey) return false;
  return SWALLOWED.has(ev.key.toLowerCase());
}
