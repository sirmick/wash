// Visible-tree flattening — the pure recursion behind fm's visibleRows
// memo, lifted from App(). Framework-free (no DOM, no Solid): it walks a
// plain listings map + expanded set and returns the flat row list the
// view renders. edit reimplements the same walk (docs/FE_REFACTOR_PLAN.md
// §2), so it's shared.
//
// Reactivity note for Solid callers: pass the store proxies straight in.
// flattenTree runs synchronously inside the createMemo, so its property
// reads (listings[p], expanded[childPath]) register with the active
// computation exactly as the inline walk's did — tracking is preserved.

import { joinPath, parentPath } from './paths.ts';
import { sortedFiltered, type SortableEntry, type SortOptions } from './sort.ts';

export interface TreeRow<E> {
  entry: E;
  path: string;
  depth: number;
  // childCount is the total entry count of a listed sub-directory
  // (including hidden — show-hidden only affects rendering), so the size
  // column can show "12 items". undefined for files / unlisted dirs.
  childCount?: number;
}

export interface TreeInput<E extends SortableEntry> {
  listings: Record<string, E[]>;
  expanded: Record<string, true>;
  sort: SortOptions;
  // start is the tree root to walk from — the shallowest listed ancestor
  // (fm's treeRoot). cur is the current path(), which drives the
  // in-flight fallback bridge (below).
  start: string;
  cur: string;
}

export function flattenTree<E extends SortableEntry>(input: TreeInput<E>): TreeRow<E>[] {
  const { listings, expanded, sort, start, cur } = input;
  const rows: TreeRow<E>[] = [];
  // walked tracks dirs the main walk descended into, so the fallback
  // below can find ancestors it couldn't reach and avoid double-render.
  const walked = new Set<string>();

  const walk = (p: string, depth: number) => {
    const entries = listings[p];
    if (!entries) return;
    walked.add(p);
    for (const e of sortedFiltered(entries, sort)) {
      const childPath = joinPath(p, e.name);
      let childCount: number | undefined;
      if (e.type === 'dir' && listings[childPath]) {
        childCount = listings[childPath].length;
      }
      rows.push({ entry: e, path: childPath, depth, childCount });
      if (e.type === 'dir' && expanded[childPath] && listings[childPath]) {
        walk(childPath, depth + 1);
      }
    }
  };

  if (listings[start]) walk(start, 0);

  // Fallback bridge: if the main walk from start didn't reach cur's
  // neighbourhood (an intermediate ancestor's listing is still in
  // flight), walk up from cur to the HIGHEST still-unwalked listed
  // ancestor and walk from there at the depth it would have under the
  // main walk — so rows don't shift vertically when intermediates land.
  if (cur) {
    let fallbackRoot = '';
    let p = cur;
    while (p && listings[p] && !walked.has(p)) {
      fallbackRoot = p;
      const par = parentPath(p);
      if (par === p) break;
      p = par;
    }
    if (fallbackRoot) {
      const segs = (s: string) => s.split('/').filter(Boolean).length;
      const baseDepth = Math.max(0, segs(fallbackRoot) - segs(start));
      walk(fallbackRoot, baseDepth);
    }
  }

  return rows;
}
