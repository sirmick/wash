// Pure geometry / ordering decisions for the shell window manager, factored
// out of wm.ts. wm.ts is a window/DOM-bound singleton (it touches `window`
// and a Solid store at module load), so it can't be imported under
// `node --test`; these helpers can, which is where the chrome-windows /
// viewport behaviour gets a fast regression net instead of e2e-only.

export interface ViewportCoord {
  vx: number;
  vy: number;
}
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}
export interface Size {
  w: number;
  h: number;
}

// Round a (possibly fractional) viewport coordinate and clamp it into the
// perAxis×perAxis grid. Used when the user pans/sets the camera.
export function clampViewport(vx: number, vy: number, perAxis: number): ViewportCoord {
  const max = perAxis - 1;
  return {
    vx: Math.max(0, Math.min(max, Math.round(vx))),
    vy: Math.max(0, Math.min(max, Math.round(vy))),
  };
}

// The viewport cell that "owns" a window — the cell its CENTER falls in,
// clamped to the grid. Used to snap the camera to a window's home cell and
// to decide which cell a freshly-placed window belongs to.
export function viewportForRect(r: Rect, screen: Size, perAxis: number): ViewportCoord {
  const max = perAxis - 1;
  const cx = r.x + r.w / 2;
  const cy = r.y + r.h / 2;
  return {
    vx: Math.max(0, Math.min(max, Math.floor(cx / screen.w))),
    vy: Math.max(0, Math.min(max, Math.floor(cy / screen.h))),
  };
}

// The z to assign a window being raised to the front: one above the current
// maximum (1 when there are no windows, matching the 0-baseline store).
export function nextZ(windows: ReadonlyArray<{ z: number }>): number {
  let maxZ = 0;
  for (const w of windows) if (w.z > maxZ) maxZ = w.z;
  return maxZ + 1;
}

// Clamp a rect's origin into the perAxis² plane, the same bound the
// titlebar drag enforces. Every path that writes window coordinates goes
// through this — a window outside the plane is reachable from no viewport.
export function clampToPlane(r: Rect, screen: Size, perAxis: number): { x: number; y: number } {
  return {
    x: Math.round(Math.max(0, Math.min(screen.w * perAxis - r.w, r.x))),
    y: Math.round(Math.max(0, Math.min(screen.h * perAxis - r.h, r.y))),
  };
}

// Is this rect stranded — entirely outside the plane, so no viewport can
// show any part of it and the titlebar can never be grabbed?
//
// Deliberately narrower than "not fully inside": a window hanging off the
// edge of cell (2,2) is ugly but still reachable, and dragging it back
// uninvited would be worse than leaving it. Only the unreachable case is
// worth an unsolicited move.
export function isOrphaned(r: Rect, screen: Size, perAxis: number): boolean {
  const planeW = screen.w * perAxis;
  const planeH = screen.h * perAxis;
  return r.x >= planeW || r.y >= planeH || r.x + r.w <= 0 || r.y + r.h <= 0;
}

// Is any part of this rect inside the camera's current cell? Used to decide
// whether revealing a window needs the camera to move at all: if the user
// can already see some of it, panning the whole desktop away from what they
// were looking at is worse than leaving it.
export function isOnScreen(r: Rect, screen: Size, vp: ViewportCoord): boolean {
  const left = vp.vx * screen.w;
  const top = vp.vy * screen.h;
  return r.x < left + screen.w && r.x + r.w > left && r.y < top + screen.h && r.y + r.h > top;
}

// Where a window lands when it is sent to viewport cell (vx, vy).
//
// There is no per-window viewport field to set: every window lives in one
// plane of perAxis² screens and the shell pans a camera over it, so sending
// a window to a cell is a move by whole screens, preserving where it sits
// WITHIN the cell. Clamped to the plane like the titlebar drag — a window
// off the far edge is reachable from no viewport at all.
export function sendToViewportRect(r: Rect, screen: Size, perAxis: number, vx: number, vy: number): { x: number; y: number } {
  const cur = viewportForRect(r, screen, perAxis);
  const maxX = screen.w * perAxis - r.w;
  const maxY = screen.h * perAxis - r.h;
  return {
    x: Math.round(Math.max(0, Math.min(maxX, r.x + (vx - cur.vx) * screen.w))),
    y: Math.round(Math.max(0, Math.min(maxY, r.y + (vy - cur.vy) * screen.h))),
  };
}
