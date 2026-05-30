// Files-clipboard pure logic, lifted from fm's App() closure.
// Framework-free (no DOM, no Solid). The router owns the actual
// clipboard and pushes clipboard_files_state to every fm window
// (cross-window cut/copy/paste); these helpers turn that push into
// state and turn a paste into a bulk-ops plan. Previously reachable
// only through the cross-window clipboard browser e2e.
//
// fm-specific (edit has no files clipboard), so it lives in-app rather
// than @wash/fs-client.

export interface ClipboardState {
  op: 'copy' | 'cut';
  paths: string[];
}

// parseClipboardState validates a raw clipboard_files_state push into a
// ClipboardState, or null when the clipboard is empty/cleared/malformed:
// a live clipboard needs a known op AND at least one path. (Matches the
// original's String(op||'') coercion + non-array → [] handling.)
export function parseClipboardState(op: unknown, paths: unknown): ClipboardState | null {
  const o = String(op || '');
  const ps: string[] = Array.isArray(paths) ? (paths as string[]) : [];
  if ((o === 'copy' || o === 'cut') && ps.length > 0) {
    return { op: o, paths: ps };
  }
  return null;
}

// PastePlan is how a paste resolves: which bulk-ops job to enqueue, and
// whether to clear the clipboard afterwards. cut is one-shot — a second
// paste must not re-move already-moved paths — so cut clears, copy doesn't.
export interface PastePlan {
  op: 'move' | 'copy';
  paths: string[];
  dest: string;
  clearAfter: boolean;
}

// planPaste returns the paste action for the current clipboard + dest, or
// null when there's nothing to paste (no/empty clipboard, or no dest). A
// 'cut' maps to a bulk move, 'copy' to a bulk copy.
export function planPaste(cb: ClipboardState | null, dest: string): PastePlan | null {
  if (!cb || cb.paths.length === 0 || !dest) return null;
  return {
    op: cb.op === 'cut' ? 'move' : 'copy',
    paths: cb.paths,
    dest,
    clearAfter: cb.op === 'cut',
  };
}
