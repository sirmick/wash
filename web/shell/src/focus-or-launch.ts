// Pure "which window should this door open?" decision for the shell
// (docs/AGENT_UX.md N1).
//
// The defect this fixes: every rail door and every roster row that wanted
// to show you an app called launchOn, which always spawns. Click "Open
// Agent" four times and you have four Agent windows, none of which is the
// one you were looking for. A door is navigation, not creation.
//
// Pure and window-shaped on purpose: the shell holds the router-attested
// instance→app-id map, so it is the only layer that can answer "is one of
// these windows already this app's?" — but the ANSWER is arithmetic over a
// list, which is testable without a DOM, a router, or a Solid store.
//
// Deliberately app-agnostic. The agent feature is what motivated it, but
// nothing here knows about agents; fm, net and settings doors have the
// same defect and can use the same primitive.

export interface AppWindow {
  windowID: number;
  origin: string;
  /** Router-attested app id of the instance owning this window. */
  appID: string;
  focused: boolean;
}

/**
 * pickWindow returns the window a door for (origin, appID) should raise,
 * or null when there is nothing open and the caller should launch.
 *
 * With several windows of one app on one host it cycles: the door raises
 * the newest, and clicking it again while that one is focused moves to the
 * next, wrapping around. That is the taskbar-group gesture every desktop
 * already teaches, and it keeps a second click from being a dead click —
 * which is what "focus the newest, always" would give you.
 *
 * Window ids are minted in ascending order per router, so id order is
 * creation order; "newest" is the largest id. Ids are per-router, hence
 * the origin filter — (origin, windowID) is the only unique identity.
 */
export function pickWindow(
  wins: readonly AppWindow[],
  origin: string,
  appID: string,
): AppWindow | null {
  const mine = wins
    .filter((w) => w.origin === origin && w.appID === appID)
    .sort((a, b) => a.windowID - b.windowID);
  if (mine.length === 0) return null;
  const at = mine.findIndex((w) => w.focused);
  if (at >= 0) return mine[(at + 1) % mine.length];
  return mine[mine.length - 1];
}
