// Framework-free helpers for <wash-app-display> frame handling.
//
// SerialQueue runs async steps strictly one after another, in submission
// order. The display element decodes each incoming frame with
// createImageBitmap(), which is asynchronous and NOT ordered: a large full
// frame can finish decoding AFTER a later, tiny dirty-rect frame, so the
// full frame would then overwrite the newer pixels with older ones and the
// window shows a stale region until that area next repaints
// (REVIEW-DISPLAY-2026-09 #5). Chaining the decode+draw of every frame on
// one queue restores the wire order. A failing step never breaks the chain.

export class SerialQueue {
  private tail: Promise<void> = Promise.resolve();
  private depth = 0;

  // enqueue schedules `step` to run after every previously enqueued step has
  // settled. Returns a promise for this step's completion (errors swallowed)
  // so callers/tests can await it.
  enqueue(step: () => Promise<void> | void): Promise<void> {
    this.depth++;
    const run = async () => {
      try {
        await step();
      } catch {
        /* the caller logs; the queue must keep flowing */
      } finally {
        this.depth--;
      }
    };
    this.tail = this.tail.then(run, run);
    return this.tail;
  }

  // pending is the number of steps not yet finished (for diagnostics/tests).
  get pending(): number {
    return this.depth;
  }
}

// Normalize a DOM WheelEvent's delta into the shape the compositor wants:
// a pixel delta (Firefox delivers LINE deltas, ±3 per notch, so raw deltaY
// scrolled guests ~40× too slow — REVIEW-X11-WAYLAND #10) plus an integer
// notch count. The compositor turns notches into a clean value120
// (notches*120), which Xwayland accumulates into buttons 4/5 — without it an
// X11 client (a menu, a combo list) gets NO discrete scroll at all
// (REVIEW-DISPLAY-2026-09 #15). Shared by the window canvas and the popup
// overlay so both paths agree.
export const WHEEL_LINE_PX = 40;
export const WHEEL_NOTCH_PX = 120;

export interface WheelAxis {
  axis: 'v' | 'h';
  delta: number;
  notches: number;
}

export function normalizeWheel(
  ev: { deltaX: number; deltaY: number; deltaMode: number },
  pageHeightPx: number,
): WheelAxis[] {
  const scale = ev.deltaMode === 1 ? WHEEL_LINE_PX : ev.deltaMode === 2 ? (pageHeightPx || 800) : 1;
  const out: WheelAxis[] = [];
  const py = ev.deltaY * scale;
  const px = ev.deltaX * scale;
  if (py) out.push({ axis: 'v', delta: Math.round(py), notches: Math.round(py / WHEEL_NOTCH_PX) });
  if (px) out.push({ axis: 'h', delta: Math.round(px), notches: Math.round(px / WHEEL_NOTCH_PX) });
  return out;
}
