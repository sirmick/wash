// Files-clipboard pure logic, lifted from fm's App() closure.
// Framework-free (no DOM, no Solid). The router owns the actual
// clipboard and pushes clipboard_files_state to every fm window
// (cross-window cut/copy/paste); these helpers turn that push into
// state and turn a paste into a bulk-ops plan. Previously reachable
// only through the cross-window clipboard browser e2e.
//
// fm-specific (edit has no files clipboard), so it lives in-app rather
// than @wash/fs-client.

import { baseName, parentPath } from '@wash/fs-client';

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

// SkipReason is why a clipboard entry was left out of the plan:
//   - 'into-self':   dest is the entry itself or inside it. bulk-ops
//                    would recurse into its own output (copy) or fail
//                    the rename (move); the service refuses the WHOLE
//                    job (bulkops.ValidatePaths), so the plan does too.
//   - 'same-folder': the entry already lives in dest. Not a copy or a
//                    move — and, unguarded, a Replace answer to the
//                    resulting self-collision deletes the source. These
//                    are dropped per entry, like the DnD same-parent
//                    filter; the rest of the paste proceeds.
export type SkipReason = 'into-self' | 'same-folder';

export interface SkippedEntry {
  path: string;
  reason: SkipReason;
}

// PastePlan is how a paste resolves: which bulk-ops job to enqueue
// (paths may be empty — then nothing is dispatched and `skipped` says
// why), and whether to clear the clipboard afterwards. cut is one-shot —
// a second paste must not re-move already-moved paths — so cut clears,
// copy doesn't. A cut that dispatches nothing keeps the clipboard: the
// user can still paste it somewhere valid.
export interface PastePlan {
  op: 'move' | 'copy';
  paths: string[];
  dest: string;
  clearAfter: boolean;
  skipped: SkippedEntry[];
}

// isWithin reports whether p is strictly inside dir — separator-aware,
// so '/a/bc' is not within '/a/b'.
function isWithin(p: string, dir: string): boolean {
  const d = dir.endsWith('/') ? dir : dir + '/';
  return p !== dir && p.startsWith(d);
}

// planPaste returns the paste action for the current clipboard + dest, or
// null when there's nothing to paste (no/empty clipboard, or no dest). A
// 'cut' maps to a bulk move, 'copy' to a bulk copy. Entries that can't
// be pasted into dest are filtered BEFORE dispatch (see SkipReason) so a
// same-folder Ctrl+V never reaches the bulk service as a self-overwrite.
export function planPaste(cb: ClipboardState | null, dest: string): PastePlan | null {
  if (!cb || cb.paths.length === 0 || !dest) return null;
  const skipped: SkippedEntry[] = [];
  const ok: string[] = [];
  for (const src of cb.paths) {
    if (dest === src || isWithin(dest, src)) skipped.push({ path: src, reason: 'into-self' });
    else if (parentPath(src) === dest) skipped.push({ path: src, reason: 'same-folder' });
    else ok.push(src);
  }
  // Mirror bulkops.ValidatePaths' reject-whole-job: one into-self entry
  // refuses the paste, not just that entry.
  const refused = skipped.some((s) => s.reason === 'into-self');
  const paths = refused ? [] : ok;
  return {
    op: cb.op === 'cut' ? 'move' : 'copy',
    paths,
    dest,
    clearAfter: cb.op === 'cut' && paths.length > 0,
    skipped,
  };
}

// PasteStatus is the status-line text a plan warrants: an error when
// the paste was refused, info when entries were merely dropped.
export interface PasteStatus {
  kind: 'error' | 'info';
  text: string;
}

// pasteStatus renders what to tell the user about a plan's skipped
// entries, or null when nothing was skipped. Names the first offender,
// like the DnD guard messages.
export function pasteStatus(plan: PastePlan): PasteStatus | null {
  const self = plan.skipped.find((s) => s.reason === 'into-self');
  if (self) {
    return { kind: 'error', text: `paste: cannot ${plan.op} ${baseName(self.path)} into itself` };
  }
  const here = plan.skipped.filter((s) => s.reason === 'same-folder');
  if (here.length === 0) return null;
  const what = here.length === 1 ? baseName(here[0].path) : `${here.length} items`;
  const verb = here.length === 1 ? 'is' : 'are';
  return { kind: 'info', text: `paste: ${what} ${verb} already in this folder` };
}
