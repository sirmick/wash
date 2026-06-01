// Pure focus-reconciliation decisions for the shell window manager,
// factored out of wm.ts. wm.ts is a window/DOM-bound singleton (it touches
// `window` and a Solid store at module load), so it can't be imported under
// `node --test`; this helper can, which is where the snapshot focus-claim
// behaviour (chrome-windows / app-state) gets a fast regression net instead
// of being e2e-only.

export interface FocusClaim {
  window_id: number;
  focused?: boolean;
}

// focusFromSnapshot resolves which window claims focus in a full session
// snapshot, or null when nothing claims it — the reconnect / "no claim →
// clear focus" path.
//
// The router attests at most one focused window per session, so in practice
// at most one entry has `focused: true`. If more than one were marked, the
// last in iteration order wins: this matches the previous in-loop
// `if (sw.focused) setFocused(sw.window_id)`, where each later claim
// overwrote the earlier one and the post-loop block only cleared focus when
// no window claimed it. An empty snapshot yields null.
export function focusFromSnapshot(wins: ReadonlyArray<FocusClaim>): number | null {
  let claim: number | null = null;
  for (const w of wins) {
    if (w.focused) claim = w.window_id;
  }
  return claim;
}
