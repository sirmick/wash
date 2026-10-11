// The taskbar's one spatial decision, kept pure so it can be tested without
// a DOM: does visiting this window need the camera to move?
//
// Windows live on one 3×3-screen plane and the shell pans a camera over it,
// so a window in another cell can be focused with nothing visible happening.
// A click on its pill then "does nothing" — the bug this file exists for.
// The pager and the pill's dblclick always snap to the window's home cell;
// a single click is gentler: it pans only when NONE of the window is in the
// current cell. If the user can already see some of it, yanking the whole
// desktop sideways is worse than leaving the camera be (the same rule the
// shell's revealWindow applies to raises an app asked for).

export interface Cell {
  vx: number;
  vy: number;
}
export interface Size {
  w: number;
  h: number;
}
export interface PlacedWindow {
  /** global plane coordinates and content size, as WindowInfo carries them */
  x: number;
  y: number;
  w: number;
  h: number;
  state: 'normal' | 'minimized' | 'maximized';
  /** the cell that owns the window: where its centre is */
  viewport: Cell;
}

/** The cell to pan to before visiting `win`, or null when it is already in
 *  view. A maximized window is drawn filling its home cell, not where its
 *  restore rect is, so for it the question is about the cell. A minimized
 *  one reappears exactly where it was (minimizing never moves a window), so
 *  its rect is the right thing to test. */
export function panToReveal(win: PlacedWindow, vp: Cell, screen: Size): Cell | null {
  const home = win.viewport;
  if (win.state === 'maximized') {
    return home.vx === vp.vx && home.vy === vp.vy ? null : home;
  }
  const left = vp.vx * screen.w;
  const top = vp.vy * screen.h;
  const onScreen = win.x < left + screen.w && win.x + win.w > left && win.y < top + screen.h && win.y + win.h > top;
  return onScreen ? null : home;
}
