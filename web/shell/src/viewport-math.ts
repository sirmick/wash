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
